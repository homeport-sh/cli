package buildplan_test

import (
	"encoding/json"
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

const rscKitPkg = `{"scripts":{"build":"rsc-kit build","compile":"rsc-kit compile"},"devDependencies":{"vite":"^8"},"dependencies":{"@rsc-kit/core":"^0.29.6"}}`

// An rsc-kit app says what it made: its build writes .output/rsc-kit.json (a
// server to compile, or a static export), which the builder reads after the
// build. Detection names no compile step and guesses no output.
func TestAnRSCKitAppIsDecidedByItsBuild(t *testing.T) {
	p := detect(t, map[string]string{"package.json": rscKitPkg, "bun.lock": bunLock}, buildplan.Settings{})
	if !p.RSCKit || p.Framework != "rsc-kit" || p.Toolchain != "bun" || p.Install != "bun install --frozen-lockfile" ||
		p.Command != "bun run build" || strings.Contains(p.Command, "compile") {
		t.Fatalf("rsc-kit: %+v", p)
	}
	// the builder reads it as rsc_kit in the plan's JSON
	if b, _ := json.Marshal(p); !strings.Contains(string(b), `"rsc_kit":true`) {
		t.Fatalf("json: %s", b)
	}
	if buildplan.RSCKitMarker != ".output/rsc-kit.json" {
		t.Fatalf("marker: %s", buildplan.RSCKitMarker)
	}
	// in a folder of the repository, too
	p = detect(t, map[string]string{"web/package.json": rscKitPkg, "web/bun.lock": bunLock}, buildplan.Settings{Root: "web"})
	if !p.RSCKit || p.Root != "web" {
		t.Fatalf("root: %+v", p)
	}
	// a person's install command keeps the build's say
	p = detect(t, map[string]string{"package.json": rscKitPkg, "bun.lock": bunLock}, buildplan.Settings{Install: "bun install"})
	if !p.RSCKit || p.Install != "bun install" {
		t.Fatalf("install: %+v", p)
	}
	// not a Bun project: as before, nothing read after the build
	p = detect(t, map[string]string{"package.json": rscKitPkg, "package-lock.json": "{}"}, buildplan.Settings{})
	if p.RSCKit || p.Kind != buildplan.Binary {
		t.Fatalf("npm: %+v", p)
	}
}

// What homeport.yaml or a person said about the build wins over the marker.
func TestSayingHowAnRSCKitAppBuildsWins(t *testing.T) {
	for name, c := range map[string]struct {
		yaml string
		s    buildplan.Settings
		kind string
	}{
		"static: dist":   {"static: dist\n", buildplan.Settings{}, buildplan.Static},
		"build.command":  {"build:\n  command: bun run compile\n  artifact: dist/app\n", buildplan.Settings{}, buildplan.Binary},
		"build.artifact": {"build:\n  artifact: dist/app\n", buildplan.Settings{}, buildplan.Binary},
		"settings kind":  {"", buildplan.Settings{Kind: buildplan.Static}, buildplan.Static},
		"settings build": {"", buildplan.Settings{Command: "bun run compile"}, buildplan.Binary},
		"settings out":   {"", buildplan.Settings{Output: "dist/app"}, buildplan.Binary},
	} {
		files := map[string]string{"package.json": rscKitPkg, "bun.lock": bunLock}
		if c.yaml != "" {
			files["homeport.yaml"] = c.yaml
		}
		p := detect(t, files, c.s)
		if p.RSCKit || p.Kind != c.kind {
			t.Errorf("%s: %+v", name, p)
		}
	}
}

// A FrankenPHP binary prints its help and exits with no args, so a PHP app
// is served by default: php-server on the port homeport gives it. What
// homeport.yaml or a person says still wins; a static site runs nothing.
func TestAPHPAppIsServedByDefault(t *testing.T) {
	php := map[string]string{"composer.json": `{}`, "composer.lock": "{}"}
	if p := detect(t, php, buildplan.Settings{}); p.Run != "php-server --listen :$PORT" {
		t.Fatalf("default: %q", p.Run)
	}
	with := func(extra map[string]string) map[string]string {
		m := map[string]string{"composer.json": `{}`, "composer.lock": "{}"}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	if p := detect(t, with(map[string]string{"homeport.yaml": "run: php-server --listen :$PORT --worker public/index.php\n"}), buildplan.Settings{}); p.Run != "php-server --listen :$PORT --worker public/index.php" {
		t.Fatalf("homeport.yaml: %q", p.Run)
	}
	if p := detect(t, php, buildplan.Settings{Run: "php-server --listen :$PORT --access-log"}); p.Run != "php-server --listen :$PORT --access-log" {
		t.Fatalf("settings: %q", p.Run)
	}
	if p := detect(t, php, buildplan.Settings{Kind: buildplan.Static, Output: "public"}); p.Run != "" {
		t.Fatalf("static: %q", p.Run)
	}
	// other toolchains' binaries run as they are
	if p := detect(t, map[string]string{"go.mod": "module m\n\ngo 1.24\n"}, buildplan.Settings{}); p.Run != "" {
		t.Fatalf("go: %q", p.Run)
	}
}
