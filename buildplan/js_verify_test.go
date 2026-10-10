package buildplan_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/homeport-sh/cli/buildplan"
)

// A start script that sets variables is refused only when it's what would
// run: a start command set in settings or homeport.yaml replaces it. PORT
// and HOST are homeport's to set, like NODE_ENV=production: dropped.
func TestAStartScriptsVariablesMatterOnlyWhenItRuns(t *testing.T) {
	pkg := `{"scripts":{"start":"NODE_OPTIONS=--max-old-space-size=256 node server.js"},"dependencies":{"express":"^5"}}`
	if p := detect(t, js(pkg), buildplan.Settings{Run: "--import ./.homeport/boot.mjs server.js"}); p.Run != "--import ./.homeport/boot.mjs server.js" {
		t.Errorf("settings: %+v", p)
	}
	if p := detect(t, js(pkg, "homeport.yaml", "run: --import ./.homeport/boot.mjs server.js\n"), buildplan.Settings{}); p.Run != "--import ./.homeport/boot.mjs server.js" {
		t.Errorf("homeport.yaml: %+v", p)
	}
	for _, start := range []string{"PORT=3000 node server.js", "HOST=0.0.0.0 PORT=8080 node server.js"} {
		p := detect(t, js(sprintf(`{"scripts":{"start":%q},"dependencies":{"express":"^5"}}`, start)), buildplan.Settings{})
		if p.Run != "--import ./.homeport/boot.mjs server.js" {
			t.Errorf("%q: %+v", start, p)
		}
	}
}

// With no framework, a compiled file is the app only when nothing else
// starts it: a start script that runs something else (node dist/server.js)
// means the compile made a tool, not the server.
func TestACompiledToolDoesntMakeAPlainAppABinary(t *testing.T) {
	p := detect(t, js(`{"scripts":{"build":"tsc && bun build --compile src/cli.ts --outfile bin/cli","start":"node dist/server.js"}}`), buildplan.Settings{})
	if p.Kind != buildplan.Bundle || p.Run != "--import ./.homeport/boot.mjs dist/server.js" {
		t.Errorf("%+v", p)
	}
	p = detect(t, js(`{"scripts":{"build":"bun build --compile src/index.ts --outfile server","start":"./server"}}`, "bun.lock", "{}"), buildplan.Settings{})
	if p.Kind != buildplan.Binary || p.Artifact != "server" || !p.Compiled {
		t.Errorf("starts it: %+v", p)
	}
}

// A compiled app runs on the Bun it was compiled with: Node can't be
// chosen for it.
func TestNodeCantRunACompiledApp(t *testing.T) {
	files := js(`{"scripts":{"build":"bun build --compile index.ts --outfile server"}}`, "bun.lock", "{}")
	if _, err := buildplan.Detect(repo(files), buildplan.Settings{Runtime: "node"}); err == nil || !strings.Contains(err.Error(), "compiled") {
		t.Fatalf("%v", err)
	}
	if p := detect(t, files, buildplan.Settings{Runtime: "bun"}); p.Kind != buildplan.Binary {
		t.Fatalf("bun: %+v", p)
	}
}

// Config is read as code, not text: a commented-out line isn't the config,
// and an adapter's options are the adapter( call's, not any name: in the
// file.
func TestConfigIsReadWithoutCommentsAndInItsCall(t *testing.T) {
	next := `{"scripts":{"build":"next build"},"dependencies":{"next":"16","next-bun-compile":"^2"}}`
	for _, cfg := range []string{"// adapterPath: \"next-bun-compile\",\nexport default {}\n", "/* adapterPath: 'next-bun-compile' */\nexport default {}\n"} {
		if p := detect(t, js(next, "next.config.ts", cfg), buildplan.Settings{}); p.Kind != buildplan.Bundle {
			t.Errorf("%q: %+v", cfg, p)
		}
	}
	p := detect(t, js(`{"scripts":{"build":"vite build"},"devDependencies":{"@sveltejs/kit":"^2","@sveltejs/adapter-node":"^5"}}`, "svelte.config.js",
		"import adapter from '@sveltejs/adapter-node'\nexport default { kit: { adapter: adapter({ precompress: { brotli: true }, out: 'out' }) } }\n"), buildplan.Settings{})
	if p.Run != "--import ./.homeport/boot.mjs out/index.js" {
		t.Errorf("out after a nested object: %+v", p)
	}
	p = detect(t, js(`{"scripts":{"build":"vite build"},"devDependencies":{"@sveltejs/kit":"^2","@sveltejs/adapter-node":"^5"}}`, "svelte.config.js",
		"import adapter from '@sveltejs/adapter-node'\n// adapter({ out: 'old' })\nexport default { kit: { adapter: adapter() } }\n"), buildplan.Settings{})
	if p.Run != "--import ./.homeport/boot.mjs build/index.js" {
		t.Errorf("a commented-out out: %+v", p)
	}
}

// Two managers' lockfiles and no packageManager: the order homeport always
// had (bun, pnpm, Yarn, npm), with a warning saying which and why.
func TestTwoLockfilesPickOneWithAWarning(t *testing.T) {
	express := `{"scripts":{"start":"node server.js"},"dependencies":{"express":"^5"}}`
	p := detect(t, js(express, "package-lock.json", "{}", "bun.lock", "{}"), buildplan.Settings{})
	if p.PackageManager != "bun" || len(p.Warnings) != 1 || !strings.Contains(p.Warnings[0], "bun.lock") {
		t.Errorf("%+v", p)
	}
	if p := detect(t, js(express, "package-lock.json", "{}", "yarn.lock", "x"), buildplan.Settings{}); p.PackageManager != "yarn" || len(p.Warnings) != 1 {
		t.Errorf("yarn: %+v", p)
	}
	if p := detect(t, js(express), buildplan.Settings{}); len(p.Warnings) != 0 {
		t.Errorf("one: %+v", p.Warnings)
	}
}

// An app in a workspace, whose lockfile is the repository's: said plainly,
// not "commit the lockfile".
func TestAWorkspaceMemberSaysSo(t *testing.T) {
	files := map[string]string{"package.json": `{"workspaces":["apps/*"]}`, "pnpm-lock.yaml": "x",
		"apps/web/package.json": `{"scripts":{"start":"node server.js"},"dependencies":{"express":"^5"}}`}
	_, err := buildplan.Detect(repo(files), buildplan.Settings{Root: "apps/web"})
	if err == nil || !strings.Contains(err.Error(), "workspace") || strings.Contains(err.Error(), "commit the lockfile") {
		t.Fatalf("%v", err)
	}
}

// On Bun, a package's command that is CommonJS is loaded as the main module
// too: Bun.main is set to it before it is required, so require.main (and
// import.meta.main) are itself. Bun's Module._load is a no-op stub.
func TestOnBunAPackagesCommandIsTheMainModuleToo(t *testing.T) {
	p := detect(t, js(`{"scripts":{"start":"bun --bun fastify start app.js"},"dependencies":{"fastify":"^5","fastify-cli":"^7"}}`, "bun.lock", "{}"), buildplan.Settings{})
	if p.Runtime != "bun" || !strings.Contains(p.Command, "Bun.main = process.argv[1]") {
		t.Fatalf("%+v", p)
	}
}

// The link check holds for any name a link can have - spaces, newlines -
// and a copy that fails fails the build (a pipe's first half doesn't).
func TestTheCopyChecksEveryLinkAndFailsWhenItFails(t *testing.T) {
	p := detect(t, js(`{"scripts":{"start":"node server.js"},"dependencies":{"express":"^5"}}`), buildplan.Settings{})
	i := strings.Index(p.Command, "rm -rf "+buildplan.BundleDir)
	j := strings.Index(p.Command, "&& find "+buildplan.BundleDir+" -path")
	copyStep := strings.Replace(p.Command[i:j], "npm prune --omit=dev && ", "", 1)
	for _, name := range []string{"a link", "new\nline"} {
		dir := t.TempDir()
		_ = os.WriteFile(filepath.Join(dir, "server.js"), []byte("x"), 0o644)
		_ = os.Symlink("/etc", filepath.Join(dir, name))
		if out, err := shell(t, dir, copyStep); err == nil || !strings.Contains(out, "links outside the app") {
			t.Errorf("%q: %v %s", name, err, out)
		}
	}
	if os.Geteuid() == 0 {
		t.Skip("root reads an unreadable file")
	}
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "server.js"), []byte("x"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "secret"), []byte("x"), 0o000)
	if out, err := shell(t, dir, copyStep); err == nil {
		t.Errorf("an unreadable file was copied past: %s", out)
	}
}

// The boot makes a server on $PORT that listens on localhost listen on every
// address, on both runtimes: Bun's http.Server isn't node:net's, so it's
// patched too. Run with whichever runtime this machine has.
func TestTheBootOpensALocalhostServerOnBothRuntimes(t *testing.T) {
	p := detect(t, js(`{"scripts":{"start":"node server.js"},"dependencies":{"express":"^5"}}`), buildplan.Settings{})
	i := strings.Index(p.Command, "printf '%s' '")
	boot := p.Command[i+len("printf '%s' '"):]
	boot = boot[:strings.Index(boot, "'")]
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "boot.mjs"), []byte(boot), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "server.mjs"), []byte(`import http from "node:http"
const s = http.createServer()
s.listen(Number(process.env.PORT), "localhost", () => { console.log(s.address().address); s.close() })
`), 0o644)
	ran := 0
	for rt, flag := range map[string]string{"node": "--import", "bun": "--preload"} {
		bin, err := exec.LookPath(rt)
		if err != nil {
			continue
		}
		ran++
		cmd := exec.Command(bin, flag, "./boot.mjs", "server.mjs")
		cmd.Dir, cmd.Env = dir, append(os.Environ(), "PORT=39871")
		out, err := cmd.CombinedOutput()
		if err != nil || strings.TrimSpace(string(out)) != "0.0.0.0" {
			t.Errorf("%s: %v %s", rt, err, out)
		}
	}
	if ran == 0 {
		t.Skip("no node or bun here")
	}
}
