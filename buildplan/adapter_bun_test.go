package buildplan_test

import (
	"strings"
	"testing"

	"github.com/homeport-sh/cli/buildplan"
)

const skBun = `{"scripts":{"build":"vite build","dev":"vite dev"},"devDependencies":{"@sveltejs/adapter-bun":"1.0.0","@sveltejs/kit":"^3.0.0","svelte":"^5","vite":"^8"}}`

func skBunConfig(call string) string {
	return "import adapter from '@sveltejs/adapter-bun';\nimport { sveltekit } from '@sveltejs/kit/vite';\n" +
		"export default defineConfig({ plugins: [sveltekit({ adapter: " + call + " })] });\n"
}

// SvelteKit's own Bun adapter writes a Bun server to its out (build): the
// app with its production dependencies, on Bun whatever else says Node,
// built in Bun (bun run --bun build: Vite's shebang is Node's).
func TestSvelteKitsBunAdapterIsABunBundle(t *testing.T) {
	for name, c := range map[string]struct {
		files map[string]string
		run   string
	}{
		"vite.config, bun.lock": {js(skBun, "bun.lock", "{}", "vite.config.ts", skBunConfig("adapter()")),
			"--preload ./.homeport/boot.mjs build/index.js"},
		"its out": {js(skBun, "bun.lock", "{}", "vite.config.js", skBunConfig("adapter({ out: 'dist', serverOptions: { idleTimeout: 30 } })")),
			"--preload ./.homeport/boot.mjs dist/index.js"},
		"svelte.config.js": {js(skBun, "bun.lock", "{}", "svelte.config.js", "import adapter from '@sveltejs/adapter-bun';\nexport default { kit: { adapter: adapter() } };\n"),
			"--preload ./.homeport/boot.mjs build/index.js"},
		"npm installs it": {js(skBun, "vite.config.ts", skBunConfig("adapter()")),
			"--preload ./.homeport/boot.mjs build/index.js"},
		"an .nvmrc doesn't make it Node": {js(skBun, "bun.lock", "{}", ".nvmrc", "24\n", "vite.config.ts", skBunConfig("adapter()")),
			"--preload ./.homeport/boot.mjs build/index.js"},
	} {
		p := detect(t, c.files, buildplan.Settings{})
		if p.Kind != buildplan.Bundle || p.Framework != "SvelteKit" || p.Runtime != "bun" || p.Run != c.run || !strings.Contains(p.RuntimeReason, "adapter-bun") {
			t.Errorf("%s: %+v", name, p)
			continue
		}
		if !strings.HasPrefix(p.Command, "bun run --bun build && ") || !strings.Contains(p.Command, "--production") && !strings.Contains(p.Command, "npm prune --omit=dev") {
			t.Errorf("%s: build %s", name, p.Command)
		}
		// it knows its scheme from the edge's X-Forwarded-Proto, its host
		// from the Host; a visitor's X-Forwarded-Host isn't read
		if !strings.Contains(p.Command, `"PROTOCOL_HEADER":"x-forwarded-proto"`) || strings.Contains(p.Command, "HOST_HEADER") {
			t.Errorf("%s: env %s", name, p.Command)
		}
		// and the build checks the adapter wrote what it starts
		entry := strings.TrimPrefix(c.run, "--preload ./.homeport/boot.mjs ")
		if !strings.Contains(p.Command, "[ -f "+entry+" ]") {
			t.Errorf("%s: no entry check: %s", name, p.Command)
		}
	}
}

// With envPrefix, the adapter reads <prefix>PORT and <prefix>HOST and
// refuses unknown prefixed names: the boot sets the prefixed ones from
// homeport's.
func TestSvelteKitsBunAdapterEnvPrefix(t *testing.T) {
	p := detect(t, js(skBun, "bun.lock", "{}", "vite.config.ts", skBunConfig("adapter({ envPrefix: 'MY_APP_' })")), buildplan.Settings{})
	for _, want := range []string{`"MY_APP_PORT":"$PORT"`, `"MY_APP_HOST":"$HOST"`, `"MY_APP_PROTOCOL_HEADER":"x-forwarded-proto"`} {
		if !strings.Contains(p.Command, want) {
			t.Errorf("no %s: %s", want, p.Command)
		}
	}
	if strings.Contains(p.Command, "HOST_HEADER") || strings.Contains(p.Command, `{"PROTOCOL_HEADER"`) || strings.Contains(p.Command, `,"PROTOCOL_HEADER"`) {
		t.Errorf("unprefixed: %s", p.Command)
	}
}

// buildOptions.compile makes one executable with the client assets in it:
// that binary is what runs.
func TestSvelteKitsBunAdapterCompiledIsABinary(t *testing.T) {
	for name, c := range map[string]struct {
		config, artifact string
		machine          string // the machine a target needs, checked before the build
	}{
		"compile: true":                  {skBunConfig("adapter({ buildOptions: { compile: true } })"), "build/server", ""},
		"a target":                       {skBunConfig("adapter({ buildOptions: { compile: 'bun-linux-x64', minify: true } })"), "build/server", "x86_64"},
		"an outfile, in its out":         {skBunConfig("adapter({ out: 'dist', buildOptions: { compile: { outfile: 'application' }, bytecode: true } })"), "dist/application", ""},
		"an outfile with a linux target": {skBunConfig("adapter({ buildOptions: { compile: { outfile: 'app', target: 'bun-linux-arm64' } } })"), "build/app", "aarch64"},
		// imported by another name
		"imported as bun": {"import bun from '@sveltejs/adapter-bun';\nexport default defineConfig({ plugins: [sveltekit({ adapter: bun({ buildOptions: { compile: true } }) })] });\n", "build/server", ""},
		"imported as bunAdapter, in svelte.config.js": {"import bunAdapter from \"@sveltejs/adapter-bun\";\nimport adapter from 'other';\nexport default { kit: { adapter: bunAdapter({ out: 'out', buildOptions: { compile: { outfile: 'srv' } } }) } };\n", "out/srv", ""},
	} {
		f := "vite.config.ts"
		if strings.Contains(c.config, "kit: {") {
			f = "svelte.config.js"
		}
		p := detect(t, js(skBun, "bun.lock", "{}", f, c.config), buildplan.Settings{})
		if p.Kind != buildplan.Binary || p.Framework != "SvelteKit" || p.Artifact != c.artifact || p.Runtime != "bun" || !strings.HasSuffix(p.Command, "bun run --bun build") ||
			!strings.Contains(p.RuntimeReason, "adapter-bun") || (c.machine == "") != (p.Command == "bun run --bun build") ||
			c.machine != "" && !strings.Contains(p.Command, `[ "$(uname -m)" = `+c.machine+` ]`) {
			t.Errorf("%s: %+v", name, p)
		}
	}
	// envPrefix under another name is read too: refused when compiled
	if _, err := buildplan.Detect(repo(js(skBun, "bun.lock", "{}", "vite.config.ts", "import bun from '@sveltejs/adapter-bun';\nexport default defineConfig({ plugins: [sveltekit({ adapter: bun({ envPrefix: 'APP_', buildOptions: { compile: true } }) })] });\n")), buildplan.Settings{}); err == nil || !strings.Contains(err.Error(), "APP_PORT") {
		t.Errorf("envPrefix as bun: %v", err)
	}
	// compile: false is the bundle
	p := detect(t, js(skBun, "bun.lock", "{}", "vite.config.ts", skBunConfig("adapter({ buildOptions: { compile: false } })")), buildplan.Settings{})
	if p.Kind != buildplan.Bundle {
		t.Errorf("compile: false: %+v", p)
	}
	// with npm, Bun beside Node for the build
	p = detect(t, js(skBun, "vite.config.ts", skBunConfig("adapter({ buildOptions: { compile: true } })")), buildplan.Settings{})
	if p.Kind != buildplan.Binary || p.Command != "bun run --bun build" || !strings.Contains(p.Install, "bun-linux-") {
		t.Errorf("npm: %+v", p)
	}
}

// What the adapter would make, but homeport can't run, is refused saying why.
func TestSvelteKitsBunAdapterRefusals(t *testing.T) {
	for name, c := range map[string]struct {
		files map[string]string
		s     buildplan.Settings
		says  string
	}{
		"Node": {js(skBun, "bun.lock", "{}", "vite.config.ts", skBunConfig("adapter()")), buildplan.Settings{Runtime: "node"}, "Bun"},
		"a compiled envPrefix": {js(skBun, "bun.lock", "{}", "vite.config.ts", skBunConfig("adapter({ envPrefix: 'APP_', buildOptions: { compile: true } })")),
			buildplan.Settings{}, "APP_PORT"},
		"another platform's target": {js(skBun, "bun.lock", "{}", "vite.config.ts", skBunConfig("adapter({ buildOptions: { compile: 'bun-darwin-arm64' } })")),
			buildplan.Settings{}, "bun-darwin-arm64"},
		"musl": {js(skBun, "bun.lock", "{}", "vite.config.ts", skBunConfig("adapter({ buildOptions: { compile: { target: 'bun-linux-x64-musl' } } })")),
			buildplan.Settings{}, "musl"},
	} {
		_, err := buildplan.Detect(repo(c.files), c.s)
		if err == nil || !strings.Contains(err.Error(), c.says) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// The config says which adapter builds it: an app moving from adapter-node
// to adapter-bun, with both installed, is what its config imports.
func TestTheConfigSaysWhichSvelteKitAdapter(t *testing.T) {
	both := `{"scripts":{"build":"vite build"},"devDependencies":{"@sveltejs/adapter-bun":"1.0.0","@sveltejs/adapter-node":"^5","@sveltejs/kit":"^3"}}`
	p := detect(t, js(both, "bun.lock", "{}", "vite.config.ts", skBunConfig("adapter()")), buildplan.Settings{})
	if p.Runtime != "bun" || !strings.HasPrefix(p.Command, "bun run --bun build") {
		t.Errorf("bun: %+v", p)
	}
	p = detect(t, js(both, "bun.lock", "{}", "vite.config.ts", "import adapter from '@sveltejs/adapter-node';\nexport default defineConfig({ plugins: [sveltekit({ adapter: adapter() })] });\n"), buildplan.Settings{})
	if p.Runtime != "node" || strings.Contains(p.Command, "--bun") || p.Run != "--import ./.homeport/boot.mjs build/index.js" {
		t.Errorf("node: %+v", p)
	}
}

// A compiled Nitro server has no folder beside it: its public assets are
// in it only with serveStatic: 'inline', else every asset is a 500.
func TestACompiledNitroServerNeedsItsAssetsInlined(t *testing.T) {
	nuxt := `{"scripts":{"build":"nuxt build --preset bun && bun build --compile --production .output/server/index.mjs --outfile server"},"dependencies":{"nuxt":"^4"}}`
	start := `{"scripts":{"build":"vite build && bun build --compile .output/server/index.mjs --outfile server"},"dependencies":{"@tanstack/react-start":"^1","nitro":"^3"}}`
	for name, c := range map[string]struct {
		files map[string]string
		ok    bool
	}{
		"Nuxt":               {js(nuxt, "bun.lock", "{}"), false},
		"Nuxt, inlined":      {js(nuxt, "bun.lock", "{}", "nuxt.config.ts", "export default defineNuxtConfig({ nitro: { preset: 'bun', serveStatic: 'inline' } })\n"), true},
		"Nuxt, in a comment": {js(nuxt, "bun.lock", "{}", "nuxt.config.ts", "// nitro: { serveStatic: 'inline' }\nexport default defineNuxtConfig({})\n"), false},
		"Nuxt, a backtick":   {js(nuxt, "bun.lock", "{}", "nuxt.config.ts", "export default defineNuxtConfig({ $production: { nitro: { serveStatic: `inline` } } })\n"), true},
		"TanStack Start, in nitro.config": {js(start, "bun.lock", "{}", "vite.config.ts", "export default defineConfig({ plugins: [tanstackStart(), nitro()] })\n",
			"nitro.config.ts", "export default defineConfig({ preset: 'bun', serveStatic: 'inline' })\n"), true},
		"TanStack Start":          {js(start, "bun.lock", "{}", "vite.config.ts", "export default defineConfig({ plugins: [tanstackStart(), nitro({ preset: 'bun' })] })\n"), false},
		"TanStack Start, inlined": {js(start, "bun.lock", "{}", "vite.config.ts", "export default defineConfig({ plugins: [tanstackStart(), nitro({ preset: 'bun', serveStatic: 'inline' })] })\n"), true},
	} {
		p, err := buildplan.Detect(repo(c.files), buildplan.Settings{})
		switch {
		case c.ok && (err != nil || p.Kind != buildplan.Binary || p.Artifact != "server"):
			t.Errorf("%s: %+v %v", name, p, err)
		case !c.ok && (err == nil || !strings.Contains(err.Error(), "serveStatic: 'inline'")):
			t.Errorf("%s: %v", name, err)
		}
	}
	// the files it's read from are fetched
	have := strings.Join(buildplan.Files(), " ")
	for _, f := range []string{"nuxt.config.ts", "nitro.config.ts", "nitro.config.mjs"} {
		if !strings.Contains(have, f) {
			t.Errorf("no %s: %s", f, have)
		}
	}
}

// The adapter a SvelteKit config imports is the one it builds with: one
// installed but not imported is ignored - adapter-static's site, whatever
// server adapter is installed beside it.
func TestTheImportedSvelteKitAdapterDecides(t *testing.T) {
	static := "import adapter from '@sveltejs/adapter-static';\nexport default { kit: { adapter: adapter() } };\n"
	for name, deps := range map[string]string{
		"adapter-bun installed":  `"@sveltejs/adapter-bun":"1.0.0"`,
		"adapter-node installed": `"@sveltejs/adapter-node":"^5"`,
	} {
		p := detect(t, js(`{"scripts":{"build":"vite build"},"devDependencies":{"@sveltejs/kit":"^3",`+deps+`,"@sveltejs/adapter-static":"^3"}}`, "bun.lock", "{}", "svelte.config.js", static), buildplan.Settings{})
		if p.Kind != buildplan.Static || p.Framework != "SvelteKit" || p.Artifact != "build" {
			t.Errorf("%s: %+v", name, p)
		}
	}
	// both server adapters, and no config to say: adapter-node, as before
	p := detect(t, js(`{"scripts":{"build":"vite build"},"devDependencies":{"@sveltejs/kit":"^3","@sveltejs/adapter-bun":"1.0.0","@sveltejs/adapter-node":"^5"}}`), buildplan.Settings{})
	if p.Runtime != "node" || p.Run != "--import ./.homeport/boot.mjs build/index.js" {
		t.Errorf("no config: %+v", p)
	}
}

// svelte-smol isn't supported: SvelteKit's own adapter-bun compiles an app.
func TestSvelteSmolIsRefused(t *testing.T) {
	_, err := buildplan.Detect(repo(js(`{"scripts":{"build":"vite build"},"devDependencies":{"@sveltejs/kit":"^2","@orochibraru/svelte-smol":"^1"}}`, "bun.lock", "{}",
		"svelte.config.js", "import adapter from '@orochibraru/svelte-smol'\nexport default { kit: { adapter: adapter() } }\n")), buildplan.Settings{})
	if err == nil || !strings.Contains(err.Error(), "@sveltejs/adapter-bun") || !strings.Contains(err.Error(), "buildOptions.compile") {
		t.Fatalf("%v", err)
	}
}

// SvelteKit 3 configures the adapter in the Vite config (sveltekit({
// adapter: ... })), SvelteKit 2 in svelte.config.js: each adapter, and
// each of its options homeport reads, the same from either.
func TestSvelteKitsAdapterIsReadFromEitherConfig(t *testing.T) {
	forms := map[string]func(imp, call string) (string, string){
		"SvelteKit 3, vite.config.ts": func(imp, call string) (string, string) {
			return "vite.config.ts", "import { sveltekit } from '@sveltejs/kit/vite';\n" + imp + "\nexport default defineConfig({\n\tplugins: [\n\t\tsveltekit({\n\t\t\tcompilerOptions: { runes: true },\n\t\t\tadapter: " + call + "\n\t\t})\n\t]\n});\n"
		},
		"SvelteKit 2, svelte.config.js": func(imp, call string) (string, string) {
			return "svelte.config.js", imp + "\nexport default { kit: { adapter: " + call + " } };\n"
		},
	}
	kit := `{"scripts":{"build":"vite build"},"devDependencies":{"@sveltejs/kit":"^3",%s}}`
	for form, config := range forms {
		for name, c := range map[string]struct {
			deps, imp, call string
			kind, artifact  string
			runtime, run    string
			in              string // in the build command
		}{
			"adapter-node, its out": {`"@sveltejs/adapter-node":"^5"`, "import adapter from '@sveltejs/adapter-node';", "adapter({ out: 'out', precompress: true })",
				buildplan.Bundle, buildplan.BundleDir, "node", "--import ./.homeport/boot.mjs out/index.js", "[ -f out/index.js ]"},
			"adapter-static, its pages": {`"@sveltejs/adapter-static":"^3"`, "import adapter from '@sveltejs/adapter-static';", "adapter({ pages: 'public', fallback: '200.html' })",
				buildplan.Static, "public", "", "", ""},
			"adapter-static, adapter-bun installed": {`"@sveltejs/adapter-static":"^3","@sveltejs/adapter-bun":"1.0.0"`, "import adapter from '@sveltejs/adapter-static';", "adapter()",
				buildplan.Static, "build", "", "", ""},
			"adapter-bun, its out": {`"@sveltejs/adapter-bun":"1.0.0"`, "import adapter from '@sveltejs/adapter-bun';", "adapter({ out: 'dist' })",
				buildplan.Bundle, buildplan.BundleDir, "bun", "--preload ./.homeport/boot.mjs dist/index.js", "[ -f dist/index.js ]"},
			"adapter-bun, envPrefix": {`"@sveltejs/adapter-bun":"1.0.0"`, "import adapter from '@sveltejs/adapter-bun';", "adapter({ envPrefix: 'APP_' })",
				buildplan.Bundle, buildplan.BundleDir, "bun", "--preload ./.homeport/boot.mjs build/index.js", `"APP_PORT":"$PORT"`},
			"adapter-bun, compiled": {`"@sveltejs/adapter-bun":"1.0.0"`, "import adapter from '@sveltejs/adapter-bun';", "adapter({ buildOptions: { compile: true } })",
				buildplan.Binary, "build/server", "bun", "", "bun run --bun build"},
			"adapter-bun, compiled to an outfile in its out": {`"@sveltejs/adapter-bun":"1.0.0"`, "import adapter from '@sveltejs/adapter-bun';", "adapter({ out: 'dist', buildOptions: { compile: { outfile: 'app' } } })",
				buildplan.Binary, "dist/app", "bun", "", "bun run --bun build"},
			"adapter-node imported, adapter-bun installed too": {`"@sveltejs/adapter-node":"^5","@sveltejs/adapter-bun":"1.0.0"`, "import adapter from '@sveltejs/adapter-node';", "adapter()",
				buildplan.Bundle, buildplan.BundleDir, "node", "--import ./.homeport/boot.mjs build/index.js", "[ -f build/index.js ]"},
		} {
			f, body := config(c.imp, c.call)
			p, err := buildplan.Detect(repo(js(strings.Replace(kit, "%s", c.deps, 1), "bun.lock", "{}", f, body)), buildplan.Settings{})
			if err != nil || p.Kind != c.kind || p.Framework != "SvelteKit" || p.Artifact != c.artifact || p.Runtime != c.runtime || p.Run != c.run || !strings.Contains(p.Command, c.in) {
				t.Errorf("%s, %s: %+v %v", form, name, p, err)
			}
		}
	}
}
