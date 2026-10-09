package buildplan_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/homeport-sh/cli/buildplan"
)

// An install command a person sets replaces the install, not what the
// image needs first: Bun fetched into a Node image, corepack's pnpm.
func TestASetInstallKeepsTheToolchainsSetup(t *testing.T) {
	next := `{"scripts":{"build":"next build","start":"next start"},"dependencies":{"next":"16"}}`
	p := detect(t, js(next, "bun.lock", "{}"), buildplan.Settings{Install: "bun install"})
	if !strings.HasSuffix(p.Install, " && bun install") || !strings.Contains(p.Install, "bun-linux-") {
		t.Errorf("bun on node: %s", p.Install)
	}
	p = detect(t, js(next, "pnpm-lock.yaml", "x"), buildplan.Settings{Install: "pnpm install"})
	if !strings.HasSuffix(p.Install, " && pnpm install") || !strings.Contains(p.Install, "corepack enable") {
		t.Errorf("pnpm: %s", p.Install)
	}
	// nothing to set up: the person's install alone
	p = detect(t, js(next), buildplan.Settings{Install: "npm install"})
	if p.Install != "npm install" {
		t.Errorf("npm: %s", p.Install)
	}
}

// Bun's prune reinstalls node_modules, which would lose what the build
// generated into it (Prisma's client in node_modules/.prisma): its dot
// folders are kept.
func TestBunsPruneKeepsWhatTheBuildGenerated(t *testing.T) {
	p := detect(t, js(`{"scripts":{"start":"bun src/index.ts"},"dependencies":{"@prisma/client":"^6"},"devDependencies":{"prisma":"^6"}}`, "bun.lock", "{}"), buildplan.Settings{})
	if !strings.Contains(p.Command, "/tmp/homeport/keep") || !strings.Contains(p.Command, "node_modules/.[!.]*") {
		t.Fatalf("%s", p.Command)
	}
}

// The package manager is the lockfile's. packageManager must agree with it,
// and without a lockfile there's nothing to install from.
func TestThePackageManagerIsTheLockfiles(t *testing.T) {
	express := `{"scripts":{"start":"node server.js"},"dependencies":{"express":"^5"}%s}`
	with := func(extra string) string { return strings.Replace(express, "%s", extra, 1) }
	for name, c := range map[string]struct {
		files map[string]string
		says  string
	}{
		"packageManager without its lockfile": {js(with(`,"packageManager":"pnpm@10.4.1"`), "package-lock.json", "{}"), "pnpm-lock.yaml"},
		"packageManager alone":                {map[string]string{"package.json": with(`,"packageManager":"bun@1.4.2"`)}, "lockfile"},
	} {
		if _, err := buildplan.Detect(repo(c.files), buildplan.Settings{}); err == nil || !strings.Contains(err.Error(), c.says) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// packageManager picks among the lockfiles there are
	if p := detect(t, js(with(`,"packageManager":"pnpm@10"`), "pnpm-lock.yaml", "x", "package-lock.json", "{}"), buildplan.Settings{}); p.PackageManager != "pnpm" {
		t.Errorf("chosen: %+v", p)
	}
}

// A variable a start script sets before the command isn't dropped
// silently: it's an app variable, set where the app's variables are.
// NODE_ENV=production is what homeport sets anyway.
func TestAStartScriptsVariablesAreRefused(t *testing.T) {
	for _, start := range []string{"NODE_OPTIONS=--max-old-space-size=256 node server.js", "cross-env DEBUG=app node server.js"} {
		_, err := buildplan.Detect(repo(js(sprintf(`{"scripts":{"start":%q},"dependencies":{"express":"^5"}}`, start))), buildplan.Settings{})
		if err == nil || !strings.Contains(err.Error(), "environment variable") {
			t.Errorf("%q: %v", start, err)
		}
	}
	p := detect(t, js(`{"scripts":{"start":"NODE_ENV=production node server.js"},"dependencies":{"express":"^5"}}`), buildplan.Settings{})
	if p.Run != "--import ./.homeport/boot.mjs server.js" {
		t.Errorf("NODE_ENV: %+v", p)
	}
}

// A start script's flags are its runtime's: run on the other one (a
// version file says so), they're dropped, not passed to a runtime that
// doesn't know them.
func TestFlagsOfTheOtherRuntimeAreDropped(t *testing.T) {
	p := detect(t, js(`{"scripts":{"start":"bun --smol src/index.ts"},"dependencies":{"hono":"^4"}}`, "bun.lock", "{}", ".nvmrc", "24\n"), buildplan.Settings{})
	if p.Runtime != "node" || p.Run != "--import ./.homeport/boot.mjs src/index.ts" {
		t.Errorf("%+v", p)
	}
	p = detect(t, js(`{"scripts":{"start":"bun --smol src/index.ts"},"dependencies":{"hono":"^4"}}`, "bun.lock", "{}"), buildplan.Settings{})
	if p.Runtime != "bun" || p.Run != "--preload ./.homeport/boot.mjs --smol src/index.ts" {
		t.Errorf("kept: %+v", p)
	}
}

// A build that compiles something isn't a binary app when a framework's
// server is what starts: only with no server preset, or when the start
// script runs what it compiled.
func TestACompiledWorkerDoesntMakeAServerABinary(t *testing.T) {
	p := detect(t, js(`{"scripts":{"build":"next build && bun build --compile worker.ts --outfile worker","start":"next start"},"dependencies":{"next":"16"}}`, "bun.lock", "{}"), buildplan.Settings{})
	if p.Kind != buildplan.Bundle || p.Framework != "Next.js" {
		t.Errorf("next: %+v", p)
	}
	// no preset: the compiled binary is the app
	if p := detect(t, js(`{"scripts":{"build":"bun build --compile index.ts --outfile server"}}`, "bun.lock", "{}"), buildplan.Settings{}); p.Kind != buildplan.Binary {
		t.Errorf("plain: %+v", p)
	}
}

// Saying it's a binary or a site where there's no build script leaves
// nothing to build: refused, rather than a build of "install && ".
func TestAnOverrideWithNothingToBuildIsRefused(t *testing.T) {
	express := js(`{"scripts":{"start":"node server.js"},"dependencies":{"express":"^5"}}`)
	for name, s := range map[string]buildplan.Settings{"kind": {Kind: buildplan.Static, Output: "public"}, "output": {Output: "server"}} {
		if _, err := buildplan.Detect(repo(express), s); err == nil || !strings.Contains(err.Error(), "build command") {
			t.Errorf("%s: %v", name, err)
		}
	}
	if p := detect(t, express, buildplan.Settings{Kind: buildplan.Static, Output: "public", Command: "npm run site"}); p.Command != "npm run site" {
		t.Errorf("with one: %+v", p)
	}
}

// Each check in the build stands alone: an earlier step failing is that
// step's failure, never a check's message.
func TestEachCheckIsBraced(t *testing.T) {
	p := detect(t, js(`{"scripts":{"build":"next build","start":"next start"},"dependencies":{"next":"16"}}`), buildplan.Settings{})
	if !strings.Contains(p.Command, "&& { [ -f .next/standalone/server.js ] || {") {
		t.Errorf("standalone: %s", p.Command)
	}
	p = detect(t, js(`{"scripts":{"start":"react-router-serve ./build/server/index.js"},"dependencies":{"@react-router/serve":"^7"}}`), buildplan.Settings{})
	if !strings.Contains(p.Command, "&& { t=$(readlink -f node_modules/.bin/react-router-serve) && [ -f \"$t\" ] || {") {
		t.Errorf("start shim: %s", p.Command)
	}
}

// What the build's shell does, run: on Linux, with GNU tools, as a builder.
func shell(t *testing.T, dir, script string) (string, error) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("the build's shell is a Linux builder's (GNU find, tar, sha256sum)")
	}
	cmd := exec.Command("/bin/sh", "-ec", script)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// The binary that ships is checked: another one fails the build, saying so.
func TestTheChecksumRefusesAnotherBinary(t *testing.T) {
	p := detect(t, js(`{"scripts":{"start":"node server.js"},"dependencies":{"express":"^5"}}`), buildplan.Settings{})
	i := strings.Index(p.Command, "case $(uname -m)")
	if i < 0 {
		t.Fatal("no checksum in the build")
	}
	check := p.Command[i:]
	check = check[:strings.Index(check, "; }; }")+len("; }; }")]
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, buildplan.BundleDir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, buildplan.BundleDir, "bin"), []byte("not node"), 0o755); err != nil {
		t.Fatal(err)
	}
	out, err := shell(t, dir, check)
	if err == nil || !strings.Contains(out, "not the official binary") {
		t.Fatalf("%v: %s", err, out)
	}
}

// A link out of the app's folder isn't followed into the bundle: the
// build fails naming it. Links inside it are taken as files.
func TestALinkOutOfTheAppIsRefused(t *testing.T) {
	p := detect(t, js(`{"scripts":{"start":"node server.js"},"dependencies":{"express":"^5"}}`), buildplan.Settings{})
	i := strings.Index(p.Command, "rm -rf "+buildplan.BundleDir)
	j := strings.Index(p.Command, "&& find "+buildplan.BundleDir+" -path")
	if i < 0 || j < 0 {
		t.Fatalf("no copy in the build: %s", p.Command)
	}
	// the copy, without the prune before it
	copyStep := strings.Replace(p.Command[i:j], "npm prune --omit=dev && ", "", 1)
	mk := func() string {
		dir := t.TempDir()
		_ = os.WriteFile(filepath.Join(dir, "server.js"), []byte("x"), 0o644)
		_ = os.Symlink("server.js", filepath.Join(dir, "inside.js"))
		return dir
	}
	dir := mk()
	if out, err := shell(t, dir, copyStep); err != nil {
		t.Fatalf("inside: %v %s", err, out)
	}
	if b, err := os.ReadFile(filepath.Join(dir, buildplan.BundleDir, "inside.js")); err != nil || string(b) != "x" {
		t.Fatalf("the inside link isn't its file: %q %v", b, err)
	}
	dir = mk()
	_ = os.Symlink("/etc", filepath.Join(dir, "etc"))
	out, err := shell(t, dir, copyStep)
	if err == nil || !strings.Contains(out, "links outside the app") {
		t.Fatalf("outside: %v %s", err, out)
	}
}

// SvelteKit 3 has no svelte.config.js: its adapter is passed to the
// sveltekit() Vite plugin, where its out is read too.
func TestSvelteKit3sAdapterIsInTheViteConfig(t *testing.T) {
	p := detect(t, js(`{"scripts":{"build":"vite build"},"devDependencies":{"@sveltejs/kit":"^3","@sveltejs/adapter-node":"^6"}}`,
		"vite.config.ts", "export default defineConfig({ plugins: [sveltekit({ adapter: adapter({ out: 'server-out' }) })] })\n"), buildplan.Settings{})
	if p.Run != "--import ./.homeport/boot.mjs server-out/index.js" {
		t.Fatalf("%+v", p)
	}
	have := map[string]bool{}
	for _, f := range buildplan.Files() {
		have[f] = true
	}
	if !have["vite.config.ts"] || !have["vite.config.js"] {
		t.Fatal("vite.config not listed")
	}
}

// A framework app whose own build compiles its server ships that binary:
// the compiled file IS the server. next-bun-compile (a Next.js build
// adapter, writing ./server), Nitro's output compiled with bun build
// --compile, and svelte-smol (a SvelteKit adapter that compiles).
func TestAFrameworkWhoseBuildCompilesItsServerShipsTheBinary(t *testing.T) {
	for name, c := range map[string]struct {
		files     map[string]string
		framework string
		artifact  string
	}{
		"next-bun-compile, by adapterPath": {js(`{"scripts":{"build":"next build","start":"next start"},"dependencies":{"next":"16"},"devDependencies":{"next-bun-compile":"^2"}}`,
			"next.config.ts", "export default { adapterPath: \"next-bun-compile\" }\n"), "Next.js", "server"},
		"next-bun-compile, by NEXT_ADAPTER_PATH": {js(`{"scripts":{"build":"NEXT_ADAPTER_PATH=next-bun-compile next build"},"dependencies":{"next":"16","next-bun-compile":"^2"}}`, "bun.lock", "{}"),
			"Next.js", "server"},
		"Nitro's bun preset, compiled": {js(`{"scripts":{"build":"nuxt build --preset bun && bun build --compile .output/server/index.mjs --outfile server"},"dependencies":{"nuxt":"^4"}}`, "bun.lock", "{}"),
			"Nuxt", "server"},
		"svelte-smol on SvelteKit 3": {js(`{"scripts":{"build":"vite build"},"devDependencies":{"@sveltejs/kit":"^3","@orochibraru/svelte-smol":"^1"}}`, "bun.lock", "{}",
			"vite.config.ts", "import adapter from '@orochibraru/svelte-smol'\nexport default defineConfig({ plugins: [sveltekit({ adapter: adapter() })] })\n"), "SvelteKit", "build/server"},
		"svelte-smol on SvelteKit 3, named": {js(`{"scripts":{"build":"vite build"},"devDependencies":{"@sveltejs/kit":"^3","@orochibraru/svelte-smol":"^1"}}`, "bun.lock", "{}",
			"vite.config.ts", "import adapter from '@orochibraru/svelte-smol'\nexport default defineConfig({ plugins: [sveltekit({ adapter: adapter({ out: 'dist', buildOptions: { compile: { outfile: 'app' } } }) })] })\n"), "SvelteKit", "dist/app"},
	} {
		p := detect(t, c.files, buildplan.Settings{})
		if p.Kind != buildplan.Binary || p.Framework != c.framework || p.Artifact != c.artifact || p.Runtime != "bun" || p.StaticFallback ||
			strings.Contains(p.Command, buildplan.BundleDir) || strings.Contains(p.Command, "NEXT_PRIVATE_STANDALONE") || p.RuntimeReason == "" {
			t.Errorf("%s: %+v", name, p)
		}
		if p.Toolchain == "node" && !strings.Contains(p.Install, "bun-linux-") {
			t.Errorf("%s: no Bun to compile with: %s", name, p.Install)
		}
	}
	// svelte-smol on SvelteKit 2 compiles build/server with client/ and
	// prerendered/ beside it: a bundle, the binary its bin
	p := detect(t, js(`{"scripts":{"build":"vite build"},"devDependencies":{"@sveltejs/kit":"^2","@orochibraru/svelte-smol":"^1"}}`, "bun.lock", "{}",
		"svelte.config.js", "import adapter from '@orochibraru/svelte-smol'\nexport default { kit: { adapter: adapter() } }\n"), buildplan.Settings{})
	if p.Kind != buildplan.Bundle || p.Framework != "SvelteKit" || p.Run != "" || p.Runtime != "bun" ||
		!strings.Contains(p.Command, "mv "+buildplan.BundleDir+"/server "+buildplan.BundleDir+"/bin") {
		t.Errorf("SvelteKit 2: %+v", p)
	}
	// next-bun-compile isn't standalone's business
	p = detect(t, js(`{"scripts":{"build":"next build"},"dependencies":{"next":"16"},"devDependencies":{"next-bun-compile":"^2"}}`), buildplan.Settings{})
	if p.Kind != buildplan.Bundle {
		t.Errorf("installed but not the adapter: %+v", p)
	}
}

// A package's command is run as the main module on Node (Module.runMain),
// so a CommonJS bin that reads require.main - fastify-cli does - finds
// itself; importing it left require.main unset.
func TestAPackagesCommandRunsAsTheMainModule(t *testing.T) {
	p := detect(t, js(`{"scripts":{"start":"fastify start -l info app.js"},"dependencies":{"fastify":"^5","fastify-cli":"^7"}}`), buildplan.Settings{})
	if !strings.Contains(p.Command, `Module.runMain()`) || !strings.Contains(p.Command, `import Module, { createRequire } from "node:module"`) {
		t.Fatalf("%s", p.Command)
	}
}
