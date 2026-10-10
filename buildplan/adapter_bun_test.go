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
		// it knows its origin from the edge's forwarded headers
		if !strings.Contains(p.Command, `"PROTOCOL_HEADER":"x-forwarded-proto"`) || !strings.Contains(p.Command, `"HOST_HEADER":"x-forwarded-host"`) {
			t.Errorf("%s: env %s", name, p.Command)
		}
	}
}

// With envPrefix, the adapter reads <prefix>PORT and <prefix>HOST and
// refuses unknown prefixed names: the boot sets the prefixed ones from
// homeport's.
func TestSvelteKitsBunAdapterEnvPrefix(t *testing.T) {
	p := detect(t, js(skBun, "bun.lock", "{}", "vite.config.ts", skBunConfig("adapter({ envPrefix: 'MY_APP_' })")), buildplan.Settings{})
	for _, want := range []string{`"MY_APP_PORT":"$PORT"`, `"MY_APP_HOST":"$HOST"`, `"MY_APP_PROTOCOL_HEADER":"x-forwarded-proto"`, `"MY_APP_HOST_HEADER":"x-forwarded-host"`} {
		if !strings.Contains(p.Command, want) {
			t.Errorf("no %s: %s", want, p.Command)
		}
	}
	if strings.Contains(p.Command, `{"HOST_HEADER"`) || strings.Contains(p.Command, `,"PROTOCOL_HEADER"`) {
		t.Errorf("unprefixed: %s", p.Command)
	}
}

// buildOptions.compile makes one executable with the client assets in it:
// that binary is what runs.
func TestSvelteKitsBunAdapterCompiledIsABinary(t *testing.T) {
	for name, c := range map[string]struct {
		call, artifact string
	}{
		"compile: true":                  {"adapter({ buildOptions: { compile: true } })", "build/server"},
		"a target":                       {"adapter({ buildOptions: { compile: 'bun-linux-x64', minify: true } })", "build/server"},
		"an outfile, in its out":         {"adapter({ out: 'dist', buildOptions: { compile: { outfile: 'application' }, bytecode: true } })", "dist/application"},
		"an outfile with a linux target": {"adapter({ buildOptions: { compile: { outfile: 'app', target: 'bun-linux-arm64' } } })", "build/app"},
	} {
		p := detect(t, js(skBun, "bun.lock", "{}", "vite.config.ts", skBunConfig(c.call)), buildplan.Settings{})
		if p.Kind != buildplan.Binary || p.Framework != "SvelteKit" || p.Artifact != c.artifact || p.Runtime != "bun" || p.Command != "bun run --bun build" ||
			!strings.Contains(p.RuntimeReason, "adapter-bun") {
			t.Errorf("%s: %+v", name, p)
		}
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
		"Nuxt":                    {js(nuxt, "bun.lock", "{}"), false},
		"Nuxt, inlined":           {js(nuxt, "bun.lock", "{}", "nuxt.config.ts", "export default defineNuxtConfig({ nitro: { preset: 'bun', serveStatic: 'inline' } })\n"), true},
		"Nuxt, in a comment":      {js(nuxt, "bun.lock", "{}", "nuxt.config.ts", "// nitro: { serveStatic: 'inline' }\nexport default defineNuxtConfig({})\n"), false},
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
	if !strings.Contains(have, "nuxt.config.ts") {
		t.Errorf("no nuxt.config.ts: %s", have)
	}
}
