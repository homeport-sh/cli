package buildplan_test

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/homeport-sh/cli/buildplan"
)

// The prerendered pages a framework's own server answers from files, before
// any of the app's code runs, are put in the bundle's .homeport/public, for
// homeport's host to answer without waking the app. These run the build's
// step (buildplan/embed/public.mjs) on what real builds left - the names
// and tables of SvelteKit 3 + adapter-node 6, SvelteKit 2 + adapter-node 5
// (with a base path) and Astro 7 + @astrojs/node 11 builds - and check what
// it serves. e2e/static-pages.sh builds the real apps and checks each URL
// against the running app.

// public runs the step in a bundle made of files, and lists the tree it
// made: "<tree> <url>".
func public(t *testing.T, kind, dir string, files map[string]string) ([]string, string) {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("no node")
	}
	b := t.TempDir()
	for name, body := range files {
		_ = os.MkdirAll(filepath.Dir(filepath.Join(b, name)), 0o755)
		if err := os.WriteFile(filepath.Join(b, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	script, err := filepath.Abs("embed/public.mjs")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(node, script, kind, dir)
	cmd.Dir = b
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("the step failed the build: %v\n%s", err, out)
	}
	var got []string
	root := filepath.Join(b, ".homeport/public")
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		tree, url, _ := strings.Cut(filepath.ToSlash(rel), "/")
		url = "/" + url
		if tree == "pages" {
			url = strings.TrimSuffix(url, ".html")
		}
		got = append(got, tree+" "+url)
		return nil
	})
	slices.Sort(got)
	return got, string(out)
}

func same(t *testing.T, got, want []string, out string) {
	t.Helper()
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("served without the app:\n got  %q\n want %q\n%s", got, want, out)
	}
}

// adapter-node 6's asset table, as its build writes build/adapter-node.js
func adapterNode6(base string, client, prerendered [][2]string, aliases [][2]string) string {
	table := func(e [][2]string, a [][2]string) string {
		var t struct {
			Entries [][2]any    `json:"entries"`
			Aliases [][2]string `json:"aliases"`
		}
		t.Entries, t.Aliases = [][2]any{}, [][2]string{}
		for _, x := range e {
			t.Entries = append(t.Entries, [2]any{x[0], map[string]any{"file": x[1], "size": 1, "etag": "x"}})
		}
		t.Aliases = append(t.Aliases, a...)
		j, _ := json.Marshal(t)
		q, _ := json.Marshal(string(j))
		return "JSON.parse(" + string(q) + ")"
	}
	return "import { dirname } from 'node:path';\nimport { fileURLToPath } from 'node:url';\nexport { server } from './server/server.js';\n" +
		"export const dir = dirname(fileURLToPath(import.meta.url));\n" +
		"export const base = \"" + base + "\";\nexport const app_path = \"_app\";\nexport const origin = undefined;\nexport const env_prefix = \"\";\n" +
		`export const mime_types = {".txt":"text/plain",".html":"text/html",".json":"application/json",".css":"text/css",".js":"text/javascript"};` + "\n" +
		"export const assets = " + table(client, aliases) + ";\n" +
		"export const prerendered_assets = " + table(prerendered, nil) + ";\n"
}

func TestSvelteKitAdapterNode6PrerenderedPagesAreServedWithoutTheApp(t *testing.T) {
	files := map[string]string{
		"build/adapter-node.js": adapterNode6("",
			[][2]string{
				{"/_app/immutable/assets/0.BtO9cou5.css", "_app/immutable/assets/0.BtO9cou5.css"},
				{"/_app/immutable/entry/start.C_w6oddR.js", "_app/immutable/entry/start.C_w6oddR.js"},
				{"/_app/version.json", "_app/version.json"},
				{"/robots.txt", "robots.txt"},
				{"/legal.html", "legal.html"},
				{"/feed", "feed"},
			},
			[][2]string{
				{"/", "index.html"},
				{"/about", "about.html"},
				{"/blog/one", "blog/one.html"},
				{"/api/data.json", "api/data.json"},
				// a client file wins its URL
				{"/robots.txt", "robots.txt"},
			},
			// a static .html file: SvelteKit answers it at both clean forms
			[][2]string{{"/legal", "/legal.html"}, {"/legal/", "/legal.html"}}),
	}
	for _, f := range []string{"_app/immutable/assets/0.BtO9cou5.css", "_app/immutable/entry/start.C_w6oddR.js", "_app/version.json", "robots.txt", "legal.html", "feed"} {
		files["build/client/"+f] = f
	}
	for _, f := range []string{"index.html", "about.html", "blog/one.html", "api/data.json", "robots.txt"} {
		files["build/prerendered/"+f] = "prerendered " + f
	}
	got, out := public(t, "sveltekit", "build", files)
	same(t, got, []string{
		"immutable /_app/immutable/assets/0.BtO9cou5.css",
		"immutable /_app/immutable/entry/start.C_w6oddR.js",
		"files /_app/version.json",
		"files /robots.txt",
		"files /legal.html",
		"pages /legal",
		"pages /legal/",
		"pages /",
		"pages /about",
		"pages /blog/one",
		"files /api/data.json",
		// no type the adapter knows: it'd send none, Caddy would guess one
	}, out)
	if !strings.Contains(out, "left to the app: /feed") {
		t.Errorf("the build log doesn't say /feed is the app's:\n%s", out)
	}
}

func TestSvelteKitAdapterNode5WithABasePath(t *testing.T) {
	files := map[string]string{
		"build/handler.js":                                          "import { manifest, prerendered, base } from './server/manifest.js';\n",
		"build/server/chunks/handler-OCRnrCci.js":                   `const prerendered = new Set(["/docs/","/docs/about","/docs/api/data.json","/docs/blog/one"]);` + "\n",
		"build/server/chunks/manifest.js-D2hNH5ju.js":               "const manifest = (() => {\nreturn {\n\tappDir: \"_app\",\n\tappPath: \"docs/_app\",\n};\n})();\n",
		"build/client/docs/_app/immutable/entry/app.HwFsJNRu.js":    "js",
		"build/client/docs/_app/immutable/entry/app.HwFsJNRu.js.br": "br",
		"build/client/docs/_app/version.json":                       "{}",
		"build/client/docs/robots.txt":                              "r",
		"build/client/docs/.hidden":                                 "h",
		"build/prerendered/docs/index.html":                         "home",
		"build/prerendered/docs/about.html":                         "about",
		"build/prerendered/docs/api/data.json":                      "{}",
		"build/prerendered/docs/blog/one.html":                      "one",
	}
	got, out := public(t, "sveltekit", "build", files)
	same(t, got, []string{
		"immutable /docs/_app/immutable/entry/app.HwFsJNRu.js",
		"files /docs/_app/version.json",
		"files /docs/robots.txt",
		"pages /docs/",
		"pages /docs/about",
		"files /docs/api/data.json",
		"pages /docs/blog/one",
	}, out)
}

// astroEntry: the manifest Astro's build puts in dist/server/entry.mjs
func astroEntry(base, trailingSlash, format string) string {
	m, _ := json.Marshal(map[string]any{
		"rootDir": "file:///app/", "outDir": "file:///app/dist/", "buildClientDir": "file:///app/dist/client/", "buildServerDir": "file:///app/dist/server/",
		"adapterName": "@astrojs/node", "assetsDir": "_astro", "routes": []any{map[string]any{"routeData": map[string]any{"_meta": map[string]any{"trailingSlash": trailingSlash}}}},
		"base": base, "trailingSlash": trailingSlash, "buildFormat": format,
	})
	return "import { deserializeManifest } from './chunks/x.mjs';\nvar _manifest = deserializeManifest(" + string(m) + ");\n"
}

func astroFiles(entry string, pages ...string) map[string]string {
	f := map[string]string{
		"dist/server/entry.mjs":                 entry,
		"dist/client/_astro/about.CTzzmHRg.css": "css",
		"dist/client/robots.txt":                "r",
		"dist/client/.env":                      "secret",
		"dist/client/.well-known/security.txt":  "s",
		"dist/client/LICENSE":                   "l",
	}
	for _, p := range pages {
		f["dist/client/"+p] = p
	}
	return f
}

func TestAstroNodeStandaloneByTrailingSlash(t *testing.T) {
	// in every case: the immutable asset, robots.txt and .well-known; never
	// .env (send denies dotfiles), nor LICENSE (no extension to type it)
	cases := map[string]struct {
		base, slash, format string
		pages               []string
		want                []string
	}{
		"ignore: both forms of a folder": {"/", "ignore", "directory", []string{"index.html", "about/index.html"},
			[]string{"files /index.html", "files /about/index.html", "pages /", "pages /about/", "pages /about"}},
		"never: no slash, the root at /": {"/", "never", "directory", []string{"index.html", "about/index.html"},
			[]string{"files /index.html", "files /about/index.html", "pages /", "pages /about"}},
		"always: the slash form only": {"/", "always", "directory", []string{"index.html", "about/index.html"},
			[]string{"files /index.html", "files /about/index.html", "pages /", "pages /about/"}},
		// a base: only under it, and not base alone (nor, with never,
		// base/, which redirects)
		"base, never, format file: clean URLs": {"/docs", "never", "file", []string{"index.html", "about.html", "blog/one.html"},
			[]string{"files /docs/index.html", "files /docs/about.html", "files /docs/blog/one.html", "pages /docs/about", "pages /docs/blog/one"}},
		"base, ignore: base/": {"/docs/", "ignore", "directory", []string{"index.html"},
			[]string{"files /docs/index.html", "pages /docs/"}},
		// always redirects /about to /about/, which send can't answer
		"always, preserve: exact files only": {"/", "always", "preserve", []string{"index.html", "about.html"},
			[]string{"files /index.html", "files /about.html", "pages /"}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			b := strings.TrimSuffix(c.base, "/")
			want := append(c.want, "immutable "+b+"/_astro/about.CTzzmHRg.css", "files "+b+"/robots.txt", "files "+b+"/.well-known/security.txt")
			got, out := public(t, "astro", "dist", astroFiles(astroEntry(c.base, c.slash, c.format), c.pages...))
			same(t, got, want, out)
		})
	}
}

// What request-time code the app's own server runs for a URL, the host
// mustn't skip: anything the step can't prove is served from files, before
// that code, is left to the app - and the build log says so.
func TestWhatTheAppsCodeRunsForIsLeftToTheApp(t *testing.T) {
	t.Run("Astro's staticHeaders: the adapter adds headers to prerendered pages", func(t *testing.T) {
		f := astroFiles(astroEntry("/", "ignore", "directory"), "index.html", "about/index.html")
		f["dist/_headers.json"] = `[{"pathname":"/about","headers":[{"key":"Content-Security-Policy","value":"default-src 'self'"}]}]`
		got, out := public(t, "astro", "dist", f)
		same(t, got, []string{"immutable /_astro/about.CTzzmHRg.css"}, out)
		if !strings.Contains(out, "static headers") {
			t.Errorf("the build log doesn't say why:\n%s", out)
		}
	})
	t.Run("another Astro adapter", func(t *testing.T) {
		f := astroFiles(strings.Replace(astroEntry("/", "ignore", "directory"), "@astrojs/node", "@astrojs/vercel", 1), "index.html")
		got, out := public(t, "astro", "dist", f)
		same(t, got, nil, out)
		if !strings.Contains(out, "served by the app, which wakes for them: the adapter is @astrojs/vercel") {
			t.Errorf("the build log doesn't say why:\n%s", out)
		}
	})
	t.Run("a SvelteKit build it can't read", func(t *testing.T) {
		got, out := public(t, "sveltekit", "build", map[string]string{"build/adapter-node.js": "export const base = \"\";\n", "build/prerendered/index.html": "x"})
		same(t, got, nil, out)
		if !strings.Contains(out, "served by the app") {
			t.Errorf("the build log doesn't say why:\n%s", out)
		}
	})
	t.Run("URLs only the app can read", func(t *testing.T) {
		got, out := public(t, "sveltekit", "build", map[string]string{
			"build/adapter-node.js":       adapterNode6("", nil, [][2]string{{"/caf%C3%A9", "café.html"}, {"/a b", "a b.html"}, {"/ok", "ok.html"}}, nil),
			"build/prerendered/café.html": "x", "build/prerendered/a b.html": "x", "build/prerendered/ok.html": "x",
		})
		same(t, got, []string{"pages /ok"}, out)
	})
}

// stepRe: the step in the build: the script, what it runs on
var stepRe = regexp.MustCompile(`\{ printf '%s' '([A-Za-z0-9+/=]+)' \| base64 -d > \.homeport-public\.mjs && \(cd ` + regexp.QuoteMeta(buildplan.BundleDir) + ` && \./bin \.\./\.homeport-public\.mjs (\w+) ([\w./-]+)\) \|\| rm -rf ` + regexp.QuoteMeta(buildplan.BundleDir) + `/\.homeport/public; rm -f \.homeport-public\.mjs; \}`)

// sveltekitApp: a SvelteKit app on adapter-node
const sveltekitApp = `{"scripts":{"build":"vite build"},"devDependencies":{"@sveltejs/kit":"^3","@sveltejs/adapter-node":"^6"}}`

// The build runs the step on the bundle, with the bundle's own runtime, and
// it never fails the build; an app that starts with its own server (a
// start command of its own, which may run anything before SvelteKit's
// handler) gets none, and the build log says why.
func TestThePlanPutsThePrerenderedPagesInTheBundle(t *testing.T) {
	step := stepRe
	want, err := os.ReadFile("embed/public.mjs")
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]struct {
		files     map[string]string
		kind, dir string
	}{
		"SvelteKit, adapter-node": {js(sveltekitApp, "svelte.config.js", "import adapter from '@sveltejs/adapter-node';\nexport default { kit: { adapter: adapter() } };\n"), "sveltekit", "build"},
		"SvelteKit, its out":      {js(sveltekitApp, "svelte.config.js", "import adapter from '@sveltejs/adapter-node';\nexport default { kit: { adapter: adapter({ out: 'out' }) } };\n"), "sveltekit", "out"},
		"Astro, standalone": {js(`{"scripts":{"build":"astro build"},"dependencies":{"astro":"^7","@astrojs/node":"^11"}}`,
			"astro.config.mjs", "import node from '@astrojs/node';\nexport default { output: 'server', adapter: node({ mode: 'standalone' }) };\n"), "astro", "dist"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			p := detect(t, c.files, buildplan.Settings{})
			m := step.FindStringSubmatch(p.Command)
			if m == nil {
				t.Fatalf("no step in the build: %s", p.Command)
			}
			if b, _ := base64.StdEncoding.DecodeString(m[1]); string(b) != string(want) {
				t.Errorf("the step isn't embed/public.mjs")
			}
			if m[2] != c.kind || m[3] != c.dir {
				t.Errorf("the step runs %s %s, want %s %s", m[2], m[3], c.kind, c.dir)
			}
			// after the bundle's runtime is in it
			if strings.Index(p.Command, m[0]) < strings.Index(p.Command, " "+buildplan.BundleDir+"/bin") {
				t.Errorf("the step runs before the bundle has its bin")
			}
		})
	}
	t.Run("its own start command", func(t *testing.T) {
		p := detect(t, js(sveltekitApp, "svelte.config.js", "import adapter from '@sveltejs/adapter-node';\nexport default { kit: { adapter: adapter() } };\n"),
			buildplan.Settings{Run: "--import ./.homeport/boot.mjs server.js"})
		if step.MatchString(p.Command) {
			t.Fatalf("a step for an app with its own server: %s", p.Command)
		}
		if !strings.Contains(p.Command, "echo 'homeport: prerendered pages are served by the app, which wakes for them: it starts with its own command, which may run code of its own before them'") {
			t.Errorf("the build log doesn't say why: %s", p.Command)
		}
	})
	t.Run("an Express app", func(t *testing.T) {
		p := detect(t, js(expressApp), buildplan.Settings{})
		if strings.Contains(p.Command, "homeport-public") {
			t.Errorf("a step for an Express app: %s", p.Command)
		}
	})
}

// The step for real, in the build's shell (Linux): a failing step leaves no
// tree, and the build goes on.
func TestTheStepNeverFailsTheBuild(t *testing.T) {
	p := detect(t, js(sveltekitApp, "svelte.config.js", "import adapter from '@sveltejs/adapter-node';\nexport default { kit: { adapter: adapter() } };\n"), buildplan.Settings{})
	stepCmd := stepRe.FindString(p.Command)
	if stepCmd == "" {
		t.Fatal("no step")
	}
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, buildplan.BundleDir, ".homeport/public/pages"), 0o755)
	// a bin that fails, as a runtime that can't run it would
	_ = os.WriteFile(filepath.Join(dir, buildplan.BundleDir, "bin"), []byte("#!/bin/sh\nexit 3\n"), 0o755)
	out, err := shell(t, dir, stepCmd+" && echo after")
	if err != nil || !strings.Contains(out, "after") {
		t.Fatalf("the step failed the build: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(dir, buildplan.BundleDir, ".homeport/public")); err == nil {
		t.Errorf("a failed step left its tree")
	}
	if _, err := os.Stat(filepath.Join(dir, ".homeport-public.mjs")); err == nil {
		t.Errorf("the step left its script")
	}
	// and a failure before it still fails the build
	if out, err := shell(t, dir, "false && "+stepCmd); err == nil {
		t.Errorf("a failure before the step didn't fail the build:\n%s", out)
	}
}
