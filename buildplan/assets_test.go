package buildplan_test

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/homeport-sh/cli/buildplan"
)

// A server whose fingerprinted files ship inside it - a compiled binary,
// rsc-kit's - names their folder and URL path in the plan (assets), for
// the builder to keep them beside it: the release after it answers a tab
// opened before that deploy with them.
func TestAServersFingerprintedFilesAreNamed(t *testing.T) {
	for name, c := range map[string]struct {
		files map[string]string
		root  string
		want  []buildplan.PlanAsset
	}{
		"rsc-kit": {map[string]string{"package.json": rscKitPkg, "bun.lock": bunLock}, "",
			[]buildplan.PlanAsset{{From: ".output/public/assets", At: "/assets"}}},
		"rsc-kit in a folder": {map[string]string{"web/package.json": rscKitPkg, "web/bun.lock": bunLock}, "web",
			[]buildplan.PlanAsset{{From: ".output/public/assets", At: "/assets"}}},
		"next-bun-compile": {js(`{"scripts":{"build":"NEXT_ADAPTER_PATH=next-bun-compile next build"},"dependencies":{"next":"16","next-bun-compile":"^2"}}`, "bun.lock", "{}"), "",
			[]buildplan.PlanAsset{{From: ".next/static", At: "/_next/static"}}},
		"Nuxt, compiled": {js(`{"scripts":{"build":"nuxt build --preset bun && bun build --compile --production .output/server/index.mjs --outfile server"},"dependencies":{"nuxt":"^4"}}`, "bun.lock", "{}",
			"nuxt.config.ts", "export default defineNuxtConfig({ nitro: { serveStatic: 'inline' } })\n"), "",
			[]buildplan.PlanAsset{{From: ".output/public/_nuxt", At: "/_nuxt"}}},
	} {
		p := detect(t, c.files, buildplan.Settings{Root: c.root})
		if !reflect.DeepEqual(p.Assets, c.want) {
			t.Errorf("%s: %+v", name, p.Assets)
		}
	}
	// the builder reads them as assets in the plan's JSON
	p := detect(t, map[string]string{"package.json": rscKitPkg, "bun.lock": bunLock}, buildplan.Settings{})
	if b, _ := json.Marshal(p); !strings.Contains(string(b), `"assets":[{"from":".output/public/assets","at":"/assets"}]`) {
		t.Errorf("json: %s", b)
	}
	// a server that serves files of no framework's names none
	p = detect(t, js(`{"scripts":{"start":"node server.js"},"dependencies":{"express":"^5"}}`, "server.js", "app.listen()\n"), buildplan.Settings{})
	if p.Assets != nil {
		t.Errorf("Express: %+v", p.Assets)
	}
	if b, _ := json.Marshal(p); strings.Contains(string(b), `"assets"`) {
		t.Errorf("json without any: %s", b)
	}
}

// Every framework whose fingerprinted files the build knows names them,
// whether or not the bundle has a tree of them in .homeport/public too: a
// bundle's, a compiled binary's, one with a start command of the app's own.
func TestEveryFrameworksFingerprintedFilesAreNamed(t *testing.T) {
	const (
		skBunPkg  = `{"scripts":{"build":"vite build"},"devDependencies":{"@sveltejs/adapter-bun":"1.0.0","@sveltejs/kit":"^3","vite":"^8"}}`
		skNodePkg = `{"scripts":{"build":"vite build"},"devDependencies":{"@sveltejs/adapter-node":"^5","@sveltejs/kit":"^2","vite":"^7"}}`
		bunVite   = "import adapter from '@sveltejs/adapter-bun';\nimport { sveltekit } from '@sveltejs/kit/vite';\nexport default { plugins: [sveltekit({ adapter: adapter(%s)%s })] };\n"
		rrPkg     = `{"scripts":{"build":"react-router build","start":"react-router-serve ./build/server/index.js"},"dependencies":{"@react-router/node":"^7","@react-router/serve":"^7","@react-router/dev":"^7"}}`
		nitroTSS  = `{"scripts":{"build":"vite build"},"dependencies":{"@tanstack/react-start":"^1","nitro":"^3"}}`
		settings  = "from pathlib import Path\nBASE_DIR = Path(__file__).resolve().parent.parent\nSTATIC_URL = %s\nSTATIC_ROOT = %s\n"
	)
	immutable := func(from, at string) []buildplan.PlanAsset { return []buildplan.PlanAsset{{From: from, At: at}} }
	for name, c := range map[string]struct {
		files map[string]string
		s     buildplan.Settings
		want  []buildplan.PlanAsset
	}{
		"SvelteKit adapter-bun, compiled": {js(skBunPkg, "bun.lock", "{}", "vite.config.js", fmt.Sprintf(bunVite, "{ buildOptions: { compile: true } }", "")), buildplan.Settings{},
			immutable(".svelte-kit/output/client/_app/immutable", "/_app/immutable")},
		"SvelteKit adapter-bun, compiled, kit's outDir, appDir and paths.base": {js(skBunPkg, "bun.lock", "{}", "vite.config.js",
			fmt.Sprintf(bunVite, "{ out: 'out', buildOptions: { compile: true } }", ", outDir: '.kit', appDir: 'app', paths: { relative: false, base: '/docs' }")), buildplan.Settings{},
			immutable(".kit/output/client/app/immutable", "/docs/app/immutable")},
		"SvelteKit adapter-bun, compiled, svelte.config.js": {js(skBunPkg, "bun.lock", "{}", "svelte.config.js",
			"import adapter from '@sveltejs/adapter-bun';\nexport default { kit: { adapter: adapter({ buildOptions: { compile: true } }), appDir: 'app' } };\n"), buildplan.Settings{},
			immutable(".svelte-kit/output/client/app/immutable", "/app/immutable")},
		"SvelteKit adapter-bun": {js(skBunPkg, "bun.lock", "{}", "vite.config.js", fmt.Sprintf(bunVite, "", "")), buildplan.Settings{},
			immutable("build/client/_app/immutable", "/_app/immutable")},
		"SvelteKit adapter-bun, its out": {js(skBunPkg, "bun.lock", "{}", "vite.config.js", fmt.Sprintf(bunVite, "{ out: 'dist' }", ", paths: { base: '/x' }")), buildplan.Settings{},
			immutable("dist/client/_app/immutable", "/x/_app/immutable")},
		"SvelteKit adapter-node": {js(skNodePkg, "bun.lock", "{}", "svelte.config.js", "import adapter from '@sveltejs/adapter-node';\nexport default { kit: { adapter: adapter() } };\n"), buildplan.Settings{},
			immutable("build/client/_app/immutable", "/_app/immutable")},
		"SvelteKit adapter-node, its own start command": {js(skNodePkg, "bun.lock", "{}", "svelte.config.js", "import adapter from '@sveltejs/adapter-node';\nexport default { kit: { adapter: adapter() } };\n"),
			buildplan.Settings{Run: "--import ./.homeport/boot.mjs server.js"}, immutable("build/client/_app/immutable", "/_app/immutable")},
		"SvelteKit, a base that isn't a plain path": {js(skBunPkg, "bun.lock", "{}", "vite.config.js", fmt.Sprintf(bunVite, "", ", paths: { base: '/a b' }")), buildplan.Settings{}, nil},
		"Astro": {js(`{"scripts":{"build":"astro build"},"dependencies":{"astro":"^5","@astrojs/node":"^9"}}`, "bun.lock", "{}"), buildplan.Settings{},
			immutable("dist/client/_astro", "/_astro")},
		"Astro, its own start command": {js(`{"scripts":{"build":"astro build"},"dependencies":{"astro":"^5","@astrojs/node":"^9"}}`, "bun.lock", "{}"),
			buildplan.Settings{Run: "--import ./.homeport/boot.mjs server.mjs"}, immutable("dist/client/_astro", "/_astro")},
		"React Router": {js(rrPkg, "bun.lock", "{}"), buildplan.Settings{}, immutable("build/client/assets", "/assets")},
		"React Router, a custom server": {js(`{"scripts":{"build":"react-router build","start":"node server.js"},"dependencies":{"@react-router/node":"^7","@react-router/express":"^7","@react-router/dev":"^7","express":"^5"}}`, "bun.lock", "{}"),
			buildplan.Settings{}, immutable("build/client/assets", "/assets")},
		"Remix, Vite": {js(`{"scripts":{"build":"remix vite:build","start":"remix-serve ./build/server/index.js"},"dependencies":{"@remix-run/node":"^2","@remix-run/serve":"^2","@remix-run/dev":"^2"}}`, "bun.lock", "{}"),
			buildplan.Settings{}, immutable("build/client/assets", "/assets")},
		"Remix, its classic compiler": {js(`{"scripts":{"build":"remix build","start":"remix-serve ./build/index.js"},"dependencies":{"@remix-run/node":"^2","@remix-run/serve":"^2","@remix-run/dev":"^2"}}`, "bun.lock", "{}"),
			buildplan.Settings{}, immutable("public/build", "/build")},
		"Next.js standalone": {js(`{"scripts":{"build":"next build"},"dependencies":{"next":"16"}}`, "next.config.ts", "export default { output: \"standalone\" }\n"), buildplan.Settings{},
			immutable(".next/static", "/_next/static")},
		"Next.js, standalone by default": {js(`{"scripts":{"build":"next build"},"dependencies":{"next":"16"}}`), buildplan.Settings{},
			immutable(".next/static", "/_next/static")},
		"Nuxt": {js(`{"scripts":{"build":"nuxt build"},"dependencies":{"nuxt":"^4"}}`, "bun.lock", "{}"), buildplan.Settings{},
			immutable(".output/public/_nuxt", "/_nuxt")},
		"TanStack Start on Nitro": {js(nitroTSS, "bun.lock", "{}", "vite.config.ts", "export default defineConfig({ plugins: [tanstackStart(), nitro({ preset: 'bun' })] });\n"), buildplan.Settings{},
			immutable(".output/public/assets", "/assets")},
		"TanStack Start on Nitro, compiled": {js(`{"scripts":{"build":"vite build && bun build --compile .output/server/index.mjs --outfile server"},"dependencies":{"@tanstack/react-start":"^1","nitro":"^3"}}`, "bun.lock", "{}",
			"vite.config.ts", "export default defineConfig({ plugins: [tanstackStart(), nitro({ preset: 'bun', serveStatic: 'inline' })] });\n"), buildplan.Settings{},
			immutable(".output/public/assets", "/assets")},
		"Laravel and Vite": {laravel(nil, "package.json", `{"scripts":{"build":"vite build"},"devDependencies":{"laravel-vite-plugin":"^2","vite":"^7"}}`, "bun.lock", "{}",
			"vite.config.js", "export default defineConfig({ plugins: [laravel({ input: ['resources/js/app.js'], refresh: true })] });\n"), buildplan.Settings{},
			immutable("public/build/assets", "/build/assets")},
		"Laravel and Vite, its buildDirectory": {laravel(nil, "package.json", `{"scripts":{"build":"vite build"},"devDependencies":{"laravel-vite-plugin":"^2","vite":"^7"}}`, "bun.lock", "{}",
			"vite.config.ts", "export default defineConfig({ plugins: [laravel({ input: ['resources/js/app.js'], buildDirectory: 'dist' })] });\n"), buildplan.Settings{},
			immutable("public/dist/assets", "/dist/assets")},
		"Laravel, no front end built": {laravel(nil, "package.json", `{"devDependencies":{"laravel-vite-plugin":"^2"}}`), buildplan.Settings{}, nil},
		"Django, STATIC_ROOT": {pyproject([]string{"django", "gunicorn", "whitenoise"}, "manage.py", managePy, "mysite/settings.py", fmt.Sprintf(settings, `"static/"`, `BASE_DIR / "staticfiles"`)), buildplan.Settings{},
			immutable("staticfiles", "/static")},
		"Django, os.path.join": {pyproject([]string{"django", "gunicorn", "whitenoise"}, "manage.py", managePy, "mysite/settings.py", fmt.Sprintf(settings, `'/assets/'`, `os.path.join(BASE_DIR, 'collected')`)), buildplan.Settings{},
			immutable("collected", "/assets")},
		"Django, no STATIC_ROOT":        {pyproject([]string{"django", "gunicorn", "whitenoise"}, "manage.py", managePy, "mysite/settings.py", "STATIC_URL = 'static/'\n"), buildplan.Settings{}, nil},
		"Django, files on another host": {pyproject([]string{"django", "gunicorn", "whitenoise"}, "manage.py", managePy, "mysite/settings.py", fmt.Sprintf(settings, `"https://cdn.example.com/static/"`, `BASE_DIR / "staticfiles"`)), buildplan.Settings{}, nil},
	} {
		p := detect(t, c.files, c.s)
		if !reflect.DeepEqual(p.Assets, c.want) {
			t.Errorf("%s: %+v", name, p.Assets)
		}
	}
}

// homeport.yaml's assets name the fingerprinted files of an app whose
// framework has no folder the build knows (Express, Go, Flask), or another
// folder than the detected one: they replace what's detected.
func TestHomeportYAMLsAssetsAreTheApps(t *testing.T) {
	const expressPkg = `{"scripts":{"build":"vite build","start":"node server.js"},"dependencies":{"express":"^5","vite":"^7"}}`
	yml := func(body string) map[string]string {
		return js(expressPkg, "bun.lock", "{}", "server.js", "app.listen()\n", "homeport.yaml", body)
	}
	for name, c := range map[string]struct {
		files map[string]string
		want  []buildplan.PlanAsset
	}{
		"Express": {yml("assets:\n  - from: public/dist/assets\n    at: /assets\n  - from: ./static/\n    at: /static/\n"),
			[]buildplan.PlanAsset{{From: "public/dist/assets", At: "/assets"}, {From: "static", At: "/static"}}},
		"Next.js, another folder than the detected one": {js(`{"scripts":{"build":"next build"},"dependencies":{"next":"16"}}`,
			"homeport.yaml", "assets:\n  - from: build/hashed\n    at: /hashed\n"),
			[]buildplan.PlanAsset{{From: "build/hashed", At: "/hashed"}}},
		"Go": {map[string]string{"go.mod": "module example.com/app\n\ngo 1.27\n", "main.go": "package main\n",
			"homeport.yaml": "assets:\n  - from: web/dist\n    at: /dist\n"},
			[]buildplan.PlanAsset{{From: "web/dist", At: "/dist"}}},
	} {
		p := detect(t, c.files, buildplan.Settings{})
		if !reflect.DeepEqual(p.Assets, c.want) {
			t.Errorf("%s: %+v", name, p.Assets)
		}
	}

	// one the builder wouldn't take is said, not left out
	for name, body := range map[string]string{
		"outside the app":         "assets:\n  - from: ../shared\n    at: /shared\n",
		"an absolute folder":      "assets:\n  - from: /srv/assets\n    at: /assets\n",
		"homeport's own":          "assets:\n  - from: .homeport/x\n    at: /x\n",
		"a URL path that isn't":   "assets:\n  - from: dist\n    at: assets\n",
		"the site's root":         "assets:\n  - from: dist\n    at: /\n",
		"without a folder":        "assets:\n  - at: /assets\n",
		"the whole app":           "assets:\n  - from: .\n    at: /x\n",
		"a space":                 "assets:\n  - from: my assets\n    at: /assets\n",
		"more than the builder's": "assets:\n" + strings.Repeat("  - from: a\n    at: /a\n", 9),
	} {
		if _, err := buildplan.Detect(repo(yml(body)), buildplan.Settings{}); err == nil || !strings.Contains(err.Error(), "homeport.yaml: assets") {
			t.Errorf("%s: %v", name, err)
		}
	}
	// a static site's files are served as they are: nothing to keep beside a server
	site := map[string]string{"package.json": `{"scripts":{"build":"vite build"},"devDependencies":{"vite":"^7"}}`, "bun.lock": "{}",
		"homeport.yaml": "assets:\n  - from: dist/assets\n    at: /assets\n"}
	if _, err := buildplan.Detect(repo(site), buildplan.Settings{}); err == nil || !strings.Contains(err.Error(), "static site") {
		t.Errorf("a static site: %v", err)
	}
}

// The build settings' assets (the dashboard's) replace homeport.yaml's and
// the detected ones, checked as the file's are; a deploy's replace the
// saved ones when it has any.
func TestTheBuildSettingsAssetsAreTheApps(t *testing.T) {
	files := js(`{"scripts":{"build":"vite build","start":"node server.js"},"dependencies":{"express":"^5","vite":"^7"}}`, "bun.lock", "{}",
		"server.js", "app.listen()\n", "homeport.yaml", "assets:\n  - from: public/dist\n    at: /dist\n")
	p := detect(t, files, buildplan.Settings{Assets: []buildplan.PlanAsset{{From: "./out/assets/", At: "/assets/"}}})
	if want := []buildplan.PlanAsset{{From: "out/assets", At: "/assets"}}; !reflect.DeepEqual(p.Assets, want) {
		t.Errorf("the settings': %+v", p.Assets)
	}
	if err := (buildplan.Settings{Assets: []buildplan.PlanAsset{{From: "../x", At: "/x"}}}).Check(); err == nil || !strings.Contains(err.Error(), "assets") {
		t.Errorf("one outside the app: %v", err)
	}
	if (buildplan.Settings{Assets: []buildplan.PlanAsset{{From: "a", At: "/a"}}}).IsZero() {
		t.Error("settings with assets are zero")
	}
	saved := buildplan.Settings{Assets: []buildplan.PlanAsset{{From: "a", At: "/a"}}}
	if got := saved.With(buildplan.Settings{}).Assets; !reflect.DeepEqual(got, saved.Assets) {
		t.Errorf("a deploy without any: %+v", got)
	}
	if got := saved.With(buildplan.Settings{Assets: []buildplan.PlanAsset{{From: "b", At: "/b"}}}).Assets; !reflect.DeepEqual(got, []buildplan.PlanAsset{{From: "b", At: "/b"}}) {
		t.Errorf("a deploy's: %+v", got)
	}
	if b, _ := json.Marshal(saved); !strings.Contains(string(b), `"assets":[{"from":"a","at":"/a"}]`) {
		t.Errorf("json: %s", b)
	}
}
