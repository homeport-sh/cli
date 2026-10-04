package buildplan_test

import (
	"strings"
	"testing"
	"testing/fstest"

	"github.com/homeport-sh/cli/buildplan"
)

func repo(files map[string]string) fstest.MapFS {
	m := fstest.MapFS{}
	for name, body := range files {
		m[name] = &fstest.MapFile{Data: []byte(body)}
	}
	return m
}

func detect(t *testing.T, files map[string]string, s buildplan.Settings) buildplan.Plan {
	t.Helper()
	p, err := buildplan.Detect(repo(files), s)
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	return p
}

const bunLock = "{}"

// A static site needs nothing but its own files: the framework says where the
// site lands, and the plan says it's static.
func TestStaticSitesAreDetectedFromTheirFramework(t *testing.T) {
	for name, c := range map[string]struct {
		files     map[string]string
		framework string
		output    string
	}{
		"SvelteKit adapter-static": {map[string]string{
			"package.json": `{"scripts":{"build":"vite build"},"devDependencies":{"@sveltejs/kit":"^3","@sveltejs/adapter-static":"^4","vite":"^7"}}`,
			"bun.lock":     bunLock}, "SvelteKit", "build"},
		"Astro": {map[string]string{
			"package.json":     `{"scripts":{"build":"astro build"},"dependencies":{"astro":"^5"}}`,
			"bun.lock":         bunLock,
			"astro.config.mjs": "export default defineConfig({ site: 'https://x' })\n"}, "Astro", "dist"},
		"Vite": {map[string]string{
			"package.json":      `{"scripts":{"build":"vite build"},"devDependencies":{"vite":"^7","react":"^19"}}`,
			"package-lock.json": "{}"}, "Vite", "dist"},
	} {
		p := detect(t, c.files, buildplan.Settings{})
		if p.Kind != buildplan.Static || p.Framework != c.framework || p.Artifact != c.output || p.Command == "" {
			t.Errorf("%s: %+v", name, p)
		}
	}
}

// Plain HTML: nothing to build, the folder is the site.
func TestPlainHTMLIsServedAsIs(t *testing.T) {
	p := detect(t, map[string]string{"index.html": "<h1>hi</h1>"}, buildplan.Settings{})
	if p.Kind != buildplan.Static || p.Toolchain != "none" || p.Command != "" || p.Image != "" || p.Artifact != "." {
		t.Fatalf("%+v", p)
	}
}

// An Astro or SvelteKit app that renders on a server isn't a static site.
func TestServerRenderedAppsAreNotStatic(t *testing.T) {
	p := detect(t, map[string]string{
		"package.json":    `{"scripts":{"build":"astro build"},"dependencies":{"astro":"^5","@astrojs/node":"^9"}}`,
		"bun.lock":        bunLock,
		"astro.config.ts": "export default defineConfig({ output: 'server', adapter: node() })\n",
	}, buildplan.Settings{})
	if p.Kind != buildplan.Binary {
		t.Fatalf("astro server: %+v", p)
	}
	// an rsc-kit app builds with vite but compiles a server binary
	p = detect(t, map[string]string{"package.json": `{"devDependencies":{"vite":"^7"},"dependencies":{"@rsc-kit/core":"^0.28"}}`, "bun.lock": bunLock}, buildplan.Settings{})
	if p.Kind != buildplan.Binary {
		t.Fatalf("rsc-kit: %+v", p)
	}
}

// A guessed binary may turn out to be a site: the builder may take a site
// folder instead. Said, not set, when the output was named.
func TestAGuessedBinaryMayBeASite(t *testing.T) {
	p := detect(t, map[string]string{"package.json": `{"scripts":{"build":"x"}}`, "bun.lock": bunLock}, buildplan.Settings{})
	if p.Kind != buildplan.Binary || !p.StaticFallback {
		t.Fatalf("%+v", p)
	}
	p = detect(t, map[string]string{"go.mod": "module m\n\ngo 1.24\n"}, buildplan.Settings{Output: "bin/app"})
	if p.StaticFallback || p.Artifact != "bin/app" {
		t.Fatalf("named: %+v", p)
	}
}

// The app can live in a folder of the repository (a monorepo): everything is
// read from there, and nothing outside it.
func TestTheAppsRootDirectory(t *testing.T) {
	files := map[string]string{
		"go.mod":               "module root\n\ngo 1.24\n", // the repo's own: not the site's
		"website/package.json": `{"scripts":{"build":"vite build"},"devDependencies":{"@sveltejs/adapter-static":"^4","@sveltejs/kit":"^3"}}`,
		"website/bun.lock":     bunLock,
	}
	p := detect(t, files, buildplan.Settings{Root: "website"})
	if p.Root != "website" || p.Kind != buildplan.Static || p.Framework != "SvelteKit" || p.Toolchain != "bun" {
		t.Fatalf("%+v", p)
	}
	for _, bad := range []string{"../x", "/etc", "a/../../b", "web site", "a\\b", ".."} {
		if _, err := buildplan.Detect(repo(files), buildplan.Settings{Root: bad}); err == nil {
			t.Errorf("root %q accepted", bad)
		}
	}
	if _, err := buildplan.Detect(repo(files), buildplan.Settings{Root: "nope"}); err == nil {
		t.Error("a root that isn't there")
	}
}

// What the person sets in the UI wins over homeport.yaml, which wins over
// detection.
func TestSettingsOverrideTheFileWhichOverridesDetection(t *testing.T) {
	files := map[string]string{
		"package.json":  `{"scripts":{"build":"vite build"},"devDependencies":{"vite":"^7"}}`,
		"bun.lock":      bunLock,
		"homeport.yaml": "build:\n  command: bun run build:site\nstatic: ./public-out\n",
	}
	p := detect(t, files, buildplan.Settings{})
	if p.Command != "bun run build:site" || p.Artifact != "public-out" || p.Kind != buildplan.Static {
		t.Fatalf("file: %+v", p)
	}
	p = detect(t, files, buildplan.Settings{Command: "bun run other", Install: "bun install", Output: "out", Kind: buildplan.Static})
	if p.Command != "bun run other" || p.Install != "bun install" || p.Artifact != "out" {
		t.Fatalf("settings: %+v", p)
	}
	p = detect(t, map[string]string{"go.mod": "module m\n\ngo 1.24\n"}, buildplan.Settings{Run: "serve --port $PORT"})
	if p.Run != "serve --port $PORT" {
		t.Fatalf("run: %+v", p)
	}
	for name, s := range map[string]buildplan.Settings{
		"output escapes": {Output: "../x"}, "kind": {Kind: "docker"}, "long command": {Command: strings.Repeat("x", 2000)},
		"newline": {Install: "a\nb"},
	} {
		if _, err := buildplan.Detect(repo(files), s); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

// The handful of files detection reads, so a control plane can fetch just
// those from a repository's host.
func TestTheFilesDetectionReads(t *testing.T) {
	want := map[string]bool{"package.json": false, "go.mod": false, "homeport.yaml": false, "index.html": false, "astro.config.mjs": false}
	for _, f := range buildplan.Files() {
		if _, ok := want[f]; ok {
			want[f] = true
		}
	}
	for f, seen := range want {
		if !seen {
			t.Errorf("%s not listed", f)
		}
	}
}
