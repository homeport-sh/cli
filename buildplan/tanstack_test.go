package buildplan_test

import (
	"strings"
	"testing"

	"github.com/homeport-sh/cli/buildplan"
)

// TanStack Start's vite build makes a server only with Nitro's Vite plugin:
// Nitro's .output, as Nuxt's, started at server/index.mjs. Its own
// dist/server/server.js is a fetch handler, not a server, so without Nitro
// it's the start script that serves it.
func TestTanStackStartShipsNitrosOutput(t *testing.T) {
	const viteNitro = "import { tanstackStart } from '@tanstack/react-start/plugin/vite'\nimport { nitro } from 'nitro/vite'\n" +
		"export default defineConfig({ plugins: [nitro(), tanstackStart(), viteReact()] })\n"
	for name, c := range map[string]struct {
		files   map[string]string
		runtime string
		run     string
		command []string
	}{
		"React, with Nitro": {js(`{"scripts":{"build":"vite build","dev":"vite dev --port 3000"},"dependencies":{"@tanstack/react-start":"1.168.61","@tanstack/react-router":"1.170.42","nitro":"3.0.260610-beta","react":"^19.2.0"},"devDependencies":{"vite":"^8.0.0"}}`,
			"vite.config.ts", viteNitro),
			"node", "--import ./.homeport/boot.mjs server/index.mjs", []string{"npm run build", ".output/server/index.mjs", ".output"}},
		"Solid, with Nitro, started on Bun": {js(`{"scripts":{"build":"vite build","start":"bun .output/server/index.mjs"},"dependencies":{"@tanstack/solid-start":"^1","nitro":"^3.0.0"},"devDependencies":{"vite":"^8"}}`, "bun.lock", "{}"),
			"bun", "--preload ./.homeport/boot.mjs server/index.mjs", []string{"bun run build", ".output"}},
		"Nitro's bun preset": {js(`{"scripts":{"build":"vite build"},"dependencies":{"@tanstack/react-start":"^1","nitro":"^3"}}`, "bun.lock", "{}",
			"vite.config.ts", "export default defineConfig({ plugins: [tanstackStart(), nitro({ preset: 'bun' }), viteReact()] })\n"),
			"bun", "--preload ./.homeport/boot.mjs server/index.mjs", []string{".output"}},
		"Nitro's node-server preset": {js(`{"scripts":{"build":"vite build"},"dependencies":{"@tanstack/react-start":"^1","nitro":"^3"}}`,
			"vite.config.ts", "export default defineConfig({ plugins: [tanstackStart(), nitro({ preset: \"node-server\" })] })\n"),
			"node", "--import ./.homeport/boot.mjs server/index.mjs", []string{".output"}},
		"without Nitro, served by srvx": {js(`{"scripts":{"build":"vite build","start":"srvx --prod -s ../client dist/server/server.js"},"dependencies":{"@tanstack/react-start":"^1","srvx":"^0.8"}}`),
			"node", "--import ./.homeport/boot.mjs .homeport/start.mjs --prod -s ../client dist/server/server.js",
			[]string{"npm run build", "npm prune --omit=dev", "node_modules/.bin/srvx"}},
		"without Nitro, TanStack's Bun server": {js(`{"scripts":{"build":"vite build","start":"bun run server.ts"},"dependencies":{"@tanstack/react-start":"^1"}}`, "bun.lock", "{}"),
			"bun", "--preload ./.homeport/boot.mjs server.ts", []string{"bun run build", "bun install --frozen-lockfile --production"}},
	} {
		p := detect(t, c.files, buildplan.Settings{})
		if p.Kind != buildplan.Bundle || p.Framework != "TanStack Start" || p.Runtime != c.runtime || p.Run != c.run ||
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
	}
}

// Without Nitro and without a start script - how the official starter
// comes - the build's dist/server/server.js is a request handler with no
// server: refused, with the fix TanStack's hosting guide gives (Nitro, its
// bun preset for Bun). A start script that serves it still runs.
func TestTanStackStartWithoutNitroOrAStartScriptIsRefused(t *testing.T) {
	for name, files := range map[string]map[string]string{
		"React, npm":      js(`{"scripts":{"build":"vite build","dev":"vite dev --port 3000","preview":"vite preview"},"dependencies":{"@tanstack/react-start":"1.168.61","react":"^19"},"devDependencies":{"vite":"^8"}}`),
		"Solid, bun.lock": js(`{"scripts":{"build":"vite build"},"dependencies":{"@tanstack/solid-start":"^1","solid-js":"^1.9"},"devDependencies":{"vite":"^8"}}`, "bun.lock", "{}"),
	} {
		_, err := buildplan.Detect(repo(files), buildplan.Settings{})
		if err == nil {
			t.Errorf("%s: not refused", name)
			continue
		}
		for _, want := range []string{"dist/server/server.js", "install nitro", "nitro() from 'nitro/vite'", "nitro({ preset: 'bun' })", "start script"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("%s: no %q in %v", name, want, err)
			}
		}
	}
}

// A Nitro preset for someone else's platform makes no server to run.
func TestTanStackStartOnAnotherPlatformsNitroPresetIsRefused(t *testing.T) {
	_, err := buildplan.Detect(repo(js(`{"scripts":{"build":"vite build"},"dependencies":{"@tanstack/react-start":"^1","nitro":"^3"}}`,
		"vite.config.ts", "export default defineConfig({ plugins: [tanstackStart(), nitro({ preset: 'vercel' })] })\n")), buildplan.Settings{})
	if err == nil || !strings.Contains(err.Error(), "vercel") || !strings.Contains(err.Error(), "node-server") {
		t.Fatalf("%v", err)
	}
	// in nitro.config, Nitro's own config file, too
	_, err = buildplan.Detect(repo(js(`{"scripts":{"build":"vite build"},"dependencies":{"@tanstack/react-start":"^1","nitro":"^3"}}`,
		"vite.config.ts", "export default defineConfig({ plugins: [tanstackStart(), nitro()] })\n", "nitro.config.ts", "export default defineConfig({ preset: `netlify` })\n")), buildplan.Settings{})
	if err == nil || !strings.Contains(err.Error(), "netlify") || !strings.Contains(err.Error(), "nitro.config.ts") {
		t.Fatalf("nitro.config: %v", err)
	}
	// node-cluster writes .output/server/index.mjs, as node-server does; bun
	// in nitro.config is Bun
	for preset, rt := range map[string]string{"node-cluster": "node", "node_cluster": "node", "bun": "bun"} {
		p := detect(t, js(`{"scripts":{"build":"vite build"},"dependencies":{"@tanstack/react-start":"^1","nitro":"^3"}}`,
			"vite.config.ts", "export default defineConfig({ plugins: [tanstackStart(), nitro()] })\n", "nitro.config.mjs", "export default { preset: '"+preset+"' }\n"), buildplan.Settings{})
		if p.Runtime != rt || p.Run != map[string]string{"node": "--import", "bun": "--preload"}[rt]+" ./.homeport/boot.mjs server/index.mjs" ||
			rt == "bun" && p.RuntimeReason != "Nitro's bun preset (nitro.config.mjs) builds a Bun server" {
			t.Errorf("%s: %+v", preset, p)
		}
	}
	// a preset elsewhere in the config (a comment, another plugin) isn't Nitro's
	p := detect(t, js(`{"scripts":{"build":"vite build"},"dependencies":{"@tanstack/react-start":"^1","nitro":"^3"}}`,
		"vite.config.ts", "// nitro({ preset: 'vercel' })\nexport default defineConfig({ plugins: [other({ preset: 'x' }), tanstackStart(), nitro()] })\n"), buildplan.Settings{})
	if p.Framework != "TanStack Start" || p.Runtime != "node" {
		t.Fatalf("%+v", p)
	}
}

// A Vite app with TanStack Router but not Start is a single-page app: still
// a static site.
func TestTanStackRouterWithoutStartIsStatic(t *testing.T) {
	p := detect(t, js(`{"scripts":{"build":"vite build && tsc"},"dependencies":{"@tanstack/react-router":"^1.170","react":"^19"},"devDependencies":{"@tanstack/router-plugin":"^1","vite":"^8"}}`,
		"vite.config.ts", "export default defineConfig({ plugins: [tanstackRouter({ target: 'react' }), viteReact()] })\n"), buildplan.Settings{})
	if p.Kind != buildplan.Static || p.Framework != "Vite" || p.Artifact != "dist" {
		t.Fatalf("%+v", p)
	}
}

// bun build --compile of Nitro's server is the server, compiled: on Nitro's
// bun preset, its public assets inlined.
func TestTanStackStartsCompiledNitroServerIsABinary(t *testing.T) {
	p := detect(t, js(`{"scripts":{"build":"vite build && bun build --compile .output/server/index.mjs --outfile server"},"dependencies":{"@tanstack/react-start":"^1","nitro":"^3"}}`, "bun.lock", "{}",
		"vite.config.ts", "export default defineConfig({ plugins: [tanstackStart(), nitro({ preset: 'bun', serveStatic: 'inline' })] })\n"), buildplan.Settings{})
	if p.Kind != buildplan.Binary || p.Framework != "TanStack Start" || p.Artifact != "server" || p.Runtime != "bun" {
		t.Fatalf("%+v", p)
	}
}
