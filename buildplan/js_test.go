package buildplan_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/homeport-sh/cli/buildplan"
)

const npmLock = "{}"

// js is a JavaScript app's files: package.json, and a lockfile unless one
// is given.
func js(pkg string, files ...string) map[string]string {
	m := map[string]string{"package.json": pkg}
	for i := 0; i+1 < len(files); i += 2 {
		m[files[i]] = files[i+1]
	}
	if !hasAny(m, "bun.lock", "bun.lockb", "package-lock.json", "pnpm-lock.yaml", "yarn.lock") {
		m["package-lock.json"] = npmLock
	}
	return m
}

func hasAny(m map[string]string, names ...string) bool {
	for _, n := range names {
		if _, ok := m[n]; ok {
			return true
		}
	}
	return false
}

// Each framework is a preset: its name, how it builds, what of its output
// ships and how that starts. A server app is a bundle - the framework's own
// production output, the runtime the project uses as its bin - never a
// compiled binary unless the project's own build makes one.
func TestEachFrameworkIsAPreset(t *testing.T) {
	for name, c := range map[string]struct {
		files     map[string]string
		framework string
		runtime   string
		run       string
		command   []string // what the build command does, in order
	}{
		"Next.js standalone": {js(`{"scripts":{"build":"next build","start":"next start"},"dependencies":{"next":"16.4.0","react":"19"}}`,
			"next.config.ts", "export default { output: \"standalone\" }\n"),
			"Next.js", "node", "--import ./.homeport/boot.mjs server.js",
			[]string{"npm run build", ".next/standalone", ".next/static", "public"}},
		"Nuxt": {js(`{"scripts":{"build":"nuxt build","preview":"nuxt preview"},"dependencies":{"nuxt":"^4"}}`),
			"Nuxt", "node", "--import ./.homeport/boot.mjs server/index.mjs", []string{"npm run build", ".output"}},
		"SvelteKit adapter-node": {js(`{"scripts":{"build":"vite build"},"devDependencies":{"@sveltejs/kit":"^2","@sveltejs/adapter-node":"^5","vite":"^7"}}`),
			"SvelteKit", "node", "--import ./.homeport/boot.mjs build/index.js", []string{"npm run build", "npm prune --omit=dev"}},
		"Astro node": {js(`{"scripts":{"build":"astro build","start":"astro dev"},"dependencies":{"astro":"^5","@astrojs/node":"^9"}}`,
			"astro.config.mjs", "export default defineConfig({ output: 'server', adapter: node({ mode: 'standalone' }) })\n"),
			"Astro", "node", "--import ./.homeport/boot.mjs dist/server/entry.mjs", []string{"npm run build", "npm prune --omit=dev"}},
		"React Router": {js(`{"scripts":{"build":"react-router build","start":"react-router-serve ./build/server/index.js"},"dependencies":{"@react-router/node":"^7","@react-router/serve":"^7","react-router":"^7"},"devDependencies":{"@react-router/dev":"^7"}}`),
			"React Router", "node", "--import ./.homeport/boot.mjs .homeport/start.mjs ./build/server/index.js",
			[]string{"npm run build", "npm prune --omit=dev", "node_modules/.bin/react-router-serve"}},
		"Remix": {js(`{"scripts":{"build":"remix vite:build","start":"remix-serve ./build/server/index.js"},"dependencies":{"@remix-run/node":"^2","@remix-run/serve":"^2"},"devDependencies":{"@remix-run/dev":"^2"}}`),
			"Remix", "node", "--import ./.homeport/boot.mjs .homeport/start.mjs ./build/server/index.js",
			[]string{"npm run build", "node_modules/.bin/remix-serve"}},
		"Express": {js(`{"scripts":{"start":"node server.js"},"dependencies":{"express":"^5"}}`),
			"Express", "node", "--import ./.homeport/boot.mjs server.js", []string{"npm prune --omit=dev"}},
		"Fastify": {js(`{"scripts":{"build":"tsc","start":"node dist/app.js"},"dependencies":{"fastify":"^5"},"devDependencies":{"typescript":"^5"}}`),
			"Fastify", "node", "--import ./.homeport/boot.mjs dist/app.js", []string{"npm run build", "npm prune --omit=dev"}},
		"NestJS": {js(`{"scripts":{"build":"nest build","start":"nest start","start:prod":"node dist/main"},"dependencies":{"@nestjs/core":"^11","@nestjs/common":"^11"},"devDependencies":{"@nestjs/cli":"^11"}}`),
			"NestJS", "node", "--import ./.homeport/boot.mjs dist/main", []string{"npm run build", "npm prune --omit=dev"}},
		"NestJS without start:prod": {js(`{"scripts":{"build":"nest build","start":"nest start"},"dependencies":{"@nestjs/core":"^11"}}`),
			"NestJS", "node", "--import ./.homeport/boot.mjs dist/main.js", []string{"npm run build"}},
		"Hono on Node": {js(`{"scripts":{"build":"tsc","start":"node dist/index.js"},"dependencies":{"hono":"^4","@hono/node-server":"^1"}}`),
			"Hono", "node", "--import ./.homeport/boot.mjs dist/index.js", []string{"npm run build"}},
		"Hono on Bun": {js(`{"scripts":{"dev":"bun run --hot src/index.ts","start":"bun src/index.ts"},"dependencies":{"hono":"^4"}}`, "bun.lock", "{}"),
			"Hono", "bun", "--preload ./.homeport/boot.mjs src/index.ts", []string{"bun install --frozen-lockfile --production"}},
		"Elysia": {js(`{"scripts":{"dev":"bun run --watch src/index.ts"},"dependencies":{"elysia":"^1"},"module":"src/index.js"}`, "bun.lock", "{}"),
			"Elysia", "bun", "--preload ./.homeport/boot.mjs src/index.ts", []string{"bun install --frozen-lockfile --production"}},
		"plain Bun": {js(`{"scripts":{"start":"bun run index.ts"}}`, "bun.lock", "{}"),
			"Bun", "bun", "--preload ./.homeport/boot.mjs index.ts", []string{"bun install"}},
		"plain Node": {js(`{"main":"index.js","dependencies":{"ws":"^8"}}`),
			"Node", "node", "--import ./.homeport/boot.mjs index.js", []string{"npm prune --omit=dev"}},
	} {
		p := detect(t, c.files, buildplan.Settings{})
		if p.Kind != buildplan.Bundle || p.Framework != c.framework || p.Runtime != c.runtime || p.Run != c.run ||
			p.Artifact != buildplan.BundleDir || p.StaticFallback || p.RuntimeReason == "" || p.Health != "/" {
			t.Errorf("%s: %+v", name, p)
			continue
		}
		at := 0
		for _, step := range c.command {
			i := strings.Index(p.Command[at:], step)
			if i < 0 {
				t.Errorf("%s: the build doesn't %q (after %d): %s", name, step, at, p.Command)
				break
			}
			at += i
		}
		// every bundle: its runtime as bin, and the boot that sets it up
		if !strings.Contains(p.Command, buildplan.BundleDir+"/bin") || !strings.Contains(p.Command, ".homeport/boot.mjs") {
			t.Errorf("%s: no bin or boot: %s", name, p.Command)
		}
		if err := buildplan.CheckRun(p.Run); err != nil {
			t.Errorf("%s: run %q: %v", name, p.Run, err)
		}
	}
}

// The plan says which runtime and why, for the dashboard to show.
func TestThePlanSaysTheRuntimeAndWhy(t *testing.T) {
	p := detect(t, js(`{"scripts":{"build":"next build","start":"next start"},"dependencies":{"next":"16"}}`), buildplan.Settings{})
	b, _ := json.Marshal(p)
	for _, want := range []string{`"runtime":"node"`, `"runtime_version":"24.21.0"`, `"runtime_reason":`, `"package_manager":"npm"`, `"health":"/"`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("json has no %s: %s", want, b)
		}
	}
}

// The runtime is what the developer runs, first match wins: the build
// settings, package.json's engines, a version file, what the start command
// really invokes, a framework that can only run on one, else Node. A
// lockfile says how to install, never what runs.
func TestTheRuntimeIsWhatTheProjectRuns(t *testing.T) {
	next := `{"scripts":{"build":"next build","start":%q},"dependencies":{"next":"16"}}`
	hono := `{"scripts":{"start":%q},"dependencies":{"hono":"^4"}}`
	for name, c := range map[string]struct {
		files   map[string]string
		s       buildplan.Settings
		runtime string
		says    string // in the reason
	}{
		"Next.js with bun.lock and next start":    {js(sprintf(next, "next start"), "bun.lock", "{}"), buildplan.Settings{}, "node", "next start"},
		"Next.js with bun --bun next start":       {js(sprintf(next, "bun --bun next start"), "bun.lock", "{}"), buildplan.Settings{}, "bun", "bun --bun next start"},
		"bun run start resolves the script":       {js(`{"scripts":{"build":"next build","start":"bun run serve","serve":"next start"},"dependencies":{"next":"16"}}`, "bun.lock", "{}"), buildplan.Settings{}, "node", "next start"},
		"Hono with package-lock":                  {js(sprintf(hono, "node dist/index.js")), buildplan.Settings{}, "node", "node dist/index.js"},
		"Hono with bun.lock and bun src/index.ts": {js(sprintf(hono, "bun src/index.ts"), "bun.lock", "{}"), buildplan.Settings{}, "bun", "bun src/index.ts"},
		"Elysia's dev script":                     {js(`{"scripts":{"dev":"bun run --watch src/index.ts"},"dependencies":{"elysia":"^1"}}`, "bun.lock", "{}"), buildplan.Settings{}, "bun", "bun run --watch src/index.ts"},
		"Elysia":                                  {js(`{"module":"src/index.ts","dependencies":{"elysia":"^1"}}`, "package-lock.json", "{}"), buildplan.Settings{}, "bun", "Elysia runs on Bun"},
		"Elysia with .nvmrc still needs Bun":      {js(`{"scripts":{"start":"bun src/index.ts"},"dependencies":{"elysia":"^1"}}`, "bun.lock", "{}", ".nvmrc", "24\n"), buildplan.Settings{}, "bun", ".nvmrc"},
		"engines names bun":                       {js(`{"engines":{"bun":">=1.2"},"scripts":{"start":"node server.js"},"dependencies":{"express":"^5"}}`), buildplan.Settings{}, "bun", "engines"},
		"engines names node":                      {js(`{"engines":{"node":">=22"},"scripts":{"start":"bun server.ts"},"dependencies":{"express":"^5"}}`, "bun.lock", "{}"), buildplan.Settings{}, "node", "engines"},
		".bun-version":                            {js(sprintf(hono, "node dist/index.js"), ".bun-version", "1.4.2\n"), buildplan.Settings{}, "bun", ".bun-version"},
		".nvmrc":                                  {js(sprintf(hono, "bun src/index.ts"), "bun.lock", "{}", ".nvmrc", "v24\n"), buildplan.Settings{}, "node", ".nvmrc"},
		".node-version":                           {js(sprintf(hono, "bun src/index.ts"), "bun.lock", "{}", ".node-version", "24.21.0\n"), buildplan.Settings{}, "node", ".node-version"},
		"a package's command runs on Node":        {js(`{"scripts":{"start":"fastify start -l info app.js"},"dependencies":{"fastify":"^5","fastify-cli":"^7"}}`, "bun.lock", "{}"), buildplan.Settings{}, "node", "fastify"},
		"nothing says: Node":                      {js(`{"main":"index.js","dependencies":{"express":"^5"}}`, "bun.lock", "{}"), buildplan.Settings{}, "node", "default"},
		"settings beat everything":                {js(`{"engines":{"node":"24"},"scripts":{"start":"node server.js"},"dependencies":{"express":"^5"}}`, ".nvmrc", "24\n"), buildplan.Settings{Runtime: "bun"}, "bun", "build settings"},
		"settings beat Elysia":                    {js(`{"scripts":{"start":"bun src/index.ts"},"dependencies":{"elysia":"^1"}}`, "bun.lock", "{}"), buildplan.Settings{Runtime: "node"}, "node", "build settings"},
		"homeport.yaml says":                      {js(sprintf(next, "next start"), "homeport.yaml", "runtime: bun\n"), buildplan.Settings{}, "bun", "homeport.yaml"},
		"settings beat homeport.yaml":             {js(sprintf(next, "next start"), "homeport.yaml", "runtime: bun\n"), buildplan.Settings{Runtime: "node"}, "node", "build settings"},
	} {
		p := detect(t, c.files, c.s)
		if p.Runtime != c.runtime || !strings.Contains(p.RuntimeReason, c.says) {
			t.Errorf("%s: %s (%q), want %s because of %q", name, p.Runtime, p.RuntimeReason, c.runtime, c.says)
		}
		// only the runtime it needs ships as bin
		want := map[string]string{"node": "/usr/local/bin/node", "bun": "/bun "}[c.runtime]
		if !strings.Contains(p.Command, want) {
			t.Errorf("%s: doesn't ship %s: %s", name, c.runtime, p.Command)
		}
		other := map[string]string{"node": "--preload", "bun": "--import"}[c.runtime]
		if strings.Contains(p.Run, other) {
			t.Errorf("%s: run %q is the other runtime's", name, p.Run)
		}
	}
	if err := (buildplan.Settings{Runtime: "deno"}).Check(); err == nil || !strings.Contains(err.Error(), "bun or node") {
		t.Fatalf("runtime deno: %v", err)
	}
	if _, err := buildplan.Detect(repo(js(`{}`, "homeport.yaml", "runtime: deno\n")), buildplan.Settings{}); err == nil {
		t.Fatal("homeport.yaml runtime: deno")
	}
	// one deploy's runtime over the saved one
	if got := (buildplan.Settings{Runtime: "node"}).With(buildplan.Settings{Runtime: "bun"}); got.Runtime != "bun" {
		t.Fatalf("with: %+v", got)
	}
	if (buildplan.Settings{Runtime: "bun"}).IsZero() {
		t.Fatal("IsZero")
	}
}

func sprintf(f, a string) string {
	b, _ := json.Marshal(a)
	return strings.Replace(f, "%q", string(b), 1)
}

// Installs use the project's package manager, frozen to its lockfile:
// package.json's packageManager, else the lockfile. node_modules has no
// links (a bundle is files): pnpm and Yarn install hoisted.
func TestInstallsUseTheProjectsPackageManager(t *testing.T) {
	express := `{"scripts":{"start":"node server.js"},"dependencies":{"express":"^5"}%s}`
	with := func(extra string) string { return strings.Replace(express, "%s", extra, 1) }
	for name, c := range map[string]struct {
		files   map[string]string
		pm      string
		install string
		prune   string
	}{
		"npm":                   {js(with("")), "npm", "npm ci", "npm prune --omit=dev"},
		"pnpm":                  {js(with(""), "pnpm-lock.yaml", "lockfileVersion: '9.0'\n"), "pnpm", "pnpm install --frozen-lockfile --config.node-linker=hoisted", "pnpm prune --prod --config.node-linker=hoisted"},
		"yarn classic":          {js(with(""), "yarn.lock", "# yarn lockfile v1\n"), "yarn", "yarn install --frozen-lockfile", "yarn install --frozen-lockfile --production"},
		"yarn berry":            {js(with(""), "yarn.lock", "__metadata:\n  version: 8\n"), "yarn", "YARN_NODE_LINKER=node-modules yarn install --immutable", "yarn workspaces focus --all --production"},
		"bun":                   {js(with(""), "bun.lock", "{}"), "bun", "bun install --frozen-lockfile --linker=hoisted", "bun install --frozen-lockfile --production --linker=hoisted"},
		"bun.lockb":             {js(with(""), "bun.lockb", "\x00"), "bun", "bun install --frozen-lockfile", ""},
		"packageManager":        {js(with(`,"packageManager":"pnpm@10.4.1+sha512.abc"`), "pnpm-lock.yaml", "x", "package-lock.json", "{}"), "pnpm", "pnpm install --frozen-lockfile", ""},
		"packageManager yarn 4": {js(with(`,"packageManager":"yarn@4.6.0"`), "yarn.lock", "x"), "yarn", "yarn install --immutable", "yarn workspaces focus"},
	} {
		p := detect(t, c.files, buildplan.Settings{})
		if p.PackageManager != c.pm || !strings.Contains(p.Install, c.install) || !strings.Contains(p.Command, c.prune) {
			t.Errorf("%s: %s\ninstall: %s\ncommand: %s", name, p.PackageManager, p.Install, p.Command)
		}
	}
	// pnpm and Yarn through corepack, which Node 26 no longer ships
	p := detect(t, js(with(""), "pnpm-lock.yaml", "x"), buildplan.Settings{})
	if !strings.Contains(p.Install, "corepack@") || !strings.Contains(p.Install, "corepack enable") {
		t.Errorf("pnpm without corepack: %s", p.Install)
	}
	// Bun to install a Node app with: pinned, its checksum checked
	p = detect(t, js(with(""), "bun.lock", "{}"), buildplan.Settings{})
	if p.Runtime != "node" || !strings.HasPrefix(p.Image, "node:") || !strings.Contains(p.Install, "bun-linux-") ||
		!strings.Contains(p.Install, "sha256sum -c") || !strings.Contains(p.Install, buildplan.BunVersion) {
		t.Errorf("bun on node: %+v", p)
	}
	// with no lockfile there's nothing to install from
	if _, err := buildplan.Detect(repo(map[string]string{"package.json": with("")}), buildplan.Settings{}); err == nil || !strings.Contains(err.Error(), "lockfile") {
		t.Errorf("no lockfile: %v", err)
	}
}

// Node is a pinned, official release - by .nvmrc, .node-version or
// engines.node - its image pinned by digest and its binary checked against
// the official build's checksum before it ships. The current LTS when
// nothing says.
func TestNodeIsAPinnedOfficialRelease(t *testing.T) {
	express := `{"scripts":{"start":"node server.js"},"dependencies":{"express":"^5"}%s}`
	for name, c := range map[string]struct {
		files map[string]string
		want  string
	}{
		"default":             {js(strings.Replace(express, "%s", "", 1)), "24.21.0"},
		".nvmrc major":        {js(strings.Replace(express, "%s", "", 1), ".nvmrc", "22\n"), "22.23.3"},
		".nvmrc exact":        {js(strings.Replace(express, "%s", "", 1), ".nvmrc", "v22.11.0\n"), "22.23.3"},
		".nvmrc lts/*":        {js(strings.Replace(express, "%s", "", 1), ".nvmrc", "lts/*\n"), "24.21.0"},
		".nvmrc lts/jod":      {js(strings.Replace(express, "%s", "", 1), ".nvmrc", "lts/jod\n"), "22.23.3"},
		".nvmrc node":         {js(strings.Replace(express, "%s", "", 1), ".nvmrc", "node\n"), "26.10.0"},
		".node-version":       {js(strings.Replace(express, "%s", "", 1), ".node-version", "26.1.0\n"), "26.10.0"},
		"engines range":       {js(strings.Replace(express, "%s", `,"engines":{"node":">=18"}`, 1)), "24.21.0"},
		"engines caret":       {js(strings.Replace(express, "%s", `,"engines":{"node":"^22.12.0"}`, 1)), "22.23.3"},
		"engines x-range":     {js(strings.Replace(express, "%s", `,"engines":{"node":"26.x"}`, 1)), "26.10.0"},
		"engines or":          {js(strings.Replace(express, "%s", `,"engines":{"node":"^20 || ^22"}`, 1)), "22.23.3"},
		"engines and":         {js(strings.Replace(express, "%s", `,"engines":{"node":">=20 <25"}`, 1)), "24.21.0"},
		"engines hyphen":      {js(strings.Replace(express, "%s", `,"engines":{"node":"22 - 24"}`, 1)), "24.21.0"},
		".nvmrc over engines": {js(strings.Replace(express, "%s", `,"engines":{"node":">=24"}`, 1), ".nvmrc", "22\n"), "22.23.3"},
	} {
		p := detect(t, c.files, buildplan.Settings{})
		if p.RuntimeVersion != c.want || !strings.HasPrefix(p.Image, "node:"+c.want+"-bookworm@sha256:") {
			t.Errorf("%s: %s %s", name, p.RuntimeVersion, p.Image)
		}
	}
	p := detect(t, js(strings.Replace(express, "%s", "", 1)), buildplan.Settings{})
	// both architectures' binaries, by the official tarballs' checksums
	for _, sum := range []string{"7fde7b8afa198da66257f42ee2001d874c7355631e6d1579a5fb5ef1f246df4c", "0f8949d1028f6d61506b2d5bc57e7e6fe893d7b1997509b7847294fc9c616584"} {
		if !strings.Contains(p.Command, sum) {
			t.Errorf("no checksum %s: %s", sum, p.Command)
		}
	}
	for name, v := range map[string]string{"end of life": "18", "too new": "27", "range": ">=27", "nonsense": "banana", "lts/iron": "lts/iron"} {
		_, err := buildplan.Detect(repo(js(strings.Replace(express, "%s", "", 1), ".nvmrc", v+"\n")), buildplan.Settings{})
		if err == nil || !strings.Contains(err.Error(), "22, 24 or 26") {
			t.Errorf("%s (%s): %v", name, v, err)
		}
	}
}

// Bun is pinned too when nothing asks for another: by digest, its binary
// checked. A version the project names is its own.
func TestBunIsPinnedUnlessTheProjectSaysOtherwise(t *testing.T) {
	elysia := `{"scripts":{"start":"bun src/index.ts"},"dependencies":{"elysia":"^1"}%s}`
	p := detect(t, js(strings.Replace(elysia, "%s", "", 1), "bun.lock", "{}"), buildplan.Settings{})
	if p.Image != buildplan.BunImage || p.RuntimeVersion != buildplan.BunVersion || !strings.Contains(p.Command, "a83d263767d839e4d2649ca8e35d07159c7afc99afdc96d731ced29e056dda0c") {
		t.Fatalf("pinned: %+v", p)
	}
	p = detect(t, js(strings.Replace(elysia, "%s", `,"packageManager":"bun@1.2.20"`, 1), "bun.lock", "{}"), buildplan.Settings{})
	if p.Image != "oven/bun:1.2.20" || p.RuntimeVersion != "1.2.20" || strings.Contains(p.Command, "sha256sum") {
		t.Fatalf("named: %+v", p)
	}
	p = detect(t, js(strings.Replace(elysia, "%s", `,"engines":{"bun":">=1.1"}`, 1), "bun.lock", "{}"), buildplan.Settings{})
	if p.Image != buildplan.BunImage {
		t.Fatalf("a range the pinned one meets: %+v", p)
	}
}

// A binary only when the project's own build makes one: bun build
// --compile in its build script. Otherwise a JavaScript server is a bundle
// - the old guess at a binary is gone.
func TestABinaryOnlyWhenTheBuildCompilesOne(t *testing.T) {
	p := detect(t, js(`{"scripts":{"build":"bun build --compile --minify src/index.ts --outfile dist/app"},"dependencies":{"elysia":"^1"}}`, "bun.lock", "{}"), buildplan.Settings{})
	if p.Kind != buildplan.Binary || p.Artifact != "dist/app" || p.Command != "bun run build" || p.StaticFallback || p.Runtime != "bun" ||
		!strings.Contains(p.RuntimeReason, "--compile") || p.Run != "" {
		t.Fatalf("compiled: %+v", p)
	}
	p = detect(t, js(`{"scripts":{"build":"bun build ./server.ts --compile --outfile=server"}}`, "bun.lock", "{}"), buildplan.Settings{})
	if p.Kind != buildplan.Binary || p.Artifact != "server" {
		t.Fatalf("--outfile=: %+v", p)
	}
	// with no --outfile, bun names it after the entry
	p = detect(t, js(`{"scripts":{"build":"bun build --compile ./src/main.ts"}}`, "bun.lock", "{}"), buildplan.Settings{})
	if p.Kind != buildplan.Binary || p.Artifact != "main" {
		t.Fatalf("no outfile: %+v", p)
	}
	// a Bun app whose build doesn't compile is a bundle with Bun
	p = detect(t, js(`{"scripts":{"build":"tsc --noEmit","start":"bun src/index.ts"}}`, "bun.lock", "{}"), buildplan.Settings{})
	if p.Kind != buildplan.Bundle || p.Runtime != "bun" {
		t.Fatalf("not compiled: %+v", p)
	}
}

// Next.js ships its standalone output. A config that doesn't set output
// gets it through Next's own default (NEXT_PRIVATE_STANDALONE); if the build
// still makes none it fails saying so. output: "export" is a static site.
func TestNextJSShipsItsStandaloneOutput(t *testing.T) {
	pkg := `{"scripts":{"build":"next build","start":"next start"},"dependencies":{"next":"16"}}`
	p := detect(t, js(pkg, "next.config.mjs", "export default { reactStrictMode: true }\n"), buildplan.Settings{})
	if !strings.Contains(p.Command, "NEXT_PRIVATE_STANDALONE=1 npm run build") || !strings.Contains(p.Command, `output: "standalone"`) {
		t.Fatalf("unset: %s", p.Command)
	}
	p = detect(t, js(pkg, "next.config.js", "module.exports = { output: 'standalone' }\n"), buildplan.Settings{})
	if strings.Contains(p.Command, "NEXT_PRIVATE_STANDALONE") {
		t.Fatalf("set: %s", p.Command)
	}
	p = detect(t, js(pkg, "next.config.ts", "const c = { output: 'export' }\nexport default c\n"), buildplan.Settings{})
	if p.Kind != buildplan.Static || p.Artifact != "out" || p.Framework != "Next.js" || p.Run != "" || p.Runtime != "" {
		t.Fatalf("export: %+v", p)
	}
}

// What a person or homeport.yaml says still wins: a build command is the
// build, and the bundle is still made from what it leaves; a start command
// replaces the detected one; saying it's a binary or a site is that.
func TestSayingHowAJSAppBuildsWins(t *testing.T) {
	express := js(`{"scripts":{"build":"tsc","start":"node dist/server.js"},"dependencies":{"express":"^5"}}`)
	p := detect(t, express, buildplan.Settings{Command: "npm run build:prod"})
	if !strings.HasPrefix(p.Command, "npm run build:prod && ") || !strings.Contains(p.Command, buildplan.BundleDir+"/bin") || p.Kind != buildplan.Bundle {
		t.Fatalf("command: %+v", p)
	}
	files := map[string]string{"homeport.yaml": "build:\n  command: npm run build:prod\n"}
	for k, v := range express {
		files[k] = v
	}
	if p := detect(t, files, buildplan.Settings{}); !strings.HasPrefix(p.Command, "npm run build:prod && ") || p.Kind != buildplan.Bundle {
		t.Fatalf("homeport.yaml command: %+v", p)
	}
	if p := detect(t, express, buildplan.Settings{Run: "--import ./.homeport/boot.mjs dist/other.js"}); p.Run != "--import ./.homeport/boot.mjs dist/other.js" {
		t.Fatalf("run: %+v", p)
	}
	files["homeport.yaml"] = "run: dist/other.js\n"
	if p := detect(t, files, buildplan.Settings{}); p.Run != "dist/other.js" {
		t.Fatalf("homeport.yaml run: %+v", p)
	}
	p = detect(t, express, buildplan.Settings{Kind: buildplan.Static, Output: "public"})
	if p.Kind != buildplan.Static || p.Artifact != "public" || p.Run != "" || strings.Contains(p.Command, buildplan.BundleDir) {
		t.Fatalf("static: %+v", p)
	}
	p = detect(t, express, buildplan.Settings{Output: "dist/app", Command: "npm run compile"})
	if p.Kind != buildplan.Binary || p.Artifact != "dist/app" || p.Command != "npm run compile" || p.Run != "" {
		t.Fatalf("binary: %+v", p)
	}
}

// A server's start command that can't run without a shell is refused, with
// what to do, rather than guessed at.
func TestAStartScriptThatNeedsAShellIsRefused(t *testing.T) {
	for _, start := range []string{"prisma migrate deploy && node server.js", "node server.js | pino-pretty", "node $ENTRY"} {
		_, err := buildplan.Detect(repo(js(sprintf(`{"scripts":{"start":%q},"dependencies":{"express":"^5"}}`, start))), buildplan.Settings{})
		if err == nil || !strings.Contains(err.Error(), "start command") {
			t.Errorf("%q: %v", start, err)
		}
	}
	// $PORT is substituted where it runs, without a shell
	if p := detect(t, js(`{"scripts":{"start":"node server.js --port $PORT"},"dependencies":{"express":"^5"}}`), buildplan.Settings{}); p.Run != "--import ./.homeport/boot.mjs server.js --port $PORT" {
		t.Errorf("$PORT: %+v", p)
	}
	// one it can't tell at all
	if _, err := buildplan.Detect(repo(js(`{"dependencies":{"express":"^5"}}`)), buildplan.Settings{}); err == nil || !strings.Contains(err.Error(), "start") {
		t.Errorf("no start: %v", err)
	}
	// a start command set in settings is enough
	p := detect(t, js(`{"dependencies":{"express":"^5"}}`), buildplan.Settings{Run: "--import ./.homeport/boot.mjs app.js"})
	if p.Kind != buildplan.Bundle || p.Run != "--import ./.homeport/boot.mjs app.js" {
		t.Errorf("set: %+v", p)
	}
}

// An Astro app built for someone else's server (middleware mode) has no
// server of its own to run.
func TestAstroMiddlewareModeIsRefused(t *testing.T) {
	_, err := buildplan.Detect(repo(js(`{"scripts":{"build":"astro build"},"dependencies":{"astro":"^5","@astrojs/node":"^9"}}`,
		"astro.config.mjs", "export default defineConfig({ output: 'server', adapter: node({ mode: 'middleware' }) })\n")), buildplan.Settings{})
	if err == nil || !strings.Contains(err.Error(), "standalone") {
		t.Fatalf("%v", err)
	}
}

// The bundle is lean and runs as the sandbox wants: no source maps (unless
// the app starts with them), no node_modules/.bin, no links; Node's compile
// cache in a writable folder of the release, which survives a sleep.
func TestTheBundleIsLeanAndCachesCompiledCode(t *testing.T) {
	p := detect(t, js(`{"scripts":{"start":"node server.js"},"dependencies":{"express":"^5"}}`), buildplan.Settings{})
	for _, want := range []string{"-name '*.map'", "node_modules/.bin", "--hard-dereference", "homeport-cache", ".homeport/writable", "enableCompileCache", "flushCompileCache"} {
		if !strings.Contains(p.Command, want) {
			t.Errorf("no %s: %s", want, p.Command)
		}
	}
	p = detect(t, js(`{"scripts":{"start":"node --enable-source-maps server.js"},"dependencies":{"express":"^5"}}`), buildplan.Settings{})
	if strings.Contains(p.Command, "-name '*.map'") || !strings.Contains(p.Run, "--enable-source-maps server.js") {
		t.Errorf("source maps kept: %+v", p)
	}
	// a Bun bundle has no compile cache to keep
	p = detect(t, js(`{"scripts":{"start":"bun src/index.ts"}}`, "bun.lock", "{}"), buildplan.Settings{})
	if strings.Contains(p.Command, ".homeport/writable") {
		t.Errorf("bun writable: %s", p.Command)
	}
}

// A SvelteKit app behind homeport's proxy knows its origin from the
// forwarded headers; the boot sets what each framework reads.
func TestSvelteKitTrustsTheForwardedOrigin(t *testing.T) {
	p := detect(t, js(`{"scripts":{"build":"vite build"},"devDependencies":{"@sveltejs/kit":"^2","@sveltejs/adapter-node":"^5"}}`,
		"svelte.config.js", "import adapter from '@sveltejs/adapter-node';\nexport default { kit: { adapter: adapter({ out: 'out' }) } };\n"), buildplan.Settings{})
	if !strings.Contains(p.Command, `"PROTOCOL_HEADER":"x-forwarded-proto"`) || !strings.Contains(p.Command, `"HOST_HEADER":"x-forwarded-host"`) ||
		p.Run != "--import ./.homeport/boot.mjs out/index.js" {
		t.Fatalf("%+v", p)
	}
}

// The files detection reads for a JavaScript app, so a control plane
// fetches them too.
func TestTheFilesDetectionReadsForJS(t *testing.T) {
	have := map[string]bool{}
	for _, f := range buildplan.Files() {
		have[f] = true
	}
	for _, f := range []string{"pnpm-lock.yaml", "yarn.lock", ".node-version", ".nvmrc", ".bun-version", "next.config.js", "next.config.mjs",
		"next.config.ts", "svelte.config.js", "astro.config.mjs", "npm-shrinkwrap.json"} {
		if !have[f] {
			t.Errorf("%s not listed", f)
		}
	}
}
