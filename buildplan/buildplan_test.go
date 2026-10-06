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

// A PHP app ships as a bundle: its installed files and, at their top, the
// image's prebuilt FrankenPHP (bin) - nothing is compiled per app. It's
// served by default, from the bundle's folder: php-server over public/ on
// the port homeport gives it, in worker mode through Octane's worker when
// the app uses Octane. What homeport.yaml or a person says still wins; a
// static site runs nothing.
func TestAPHPAppShipsAsABundle(t *testing.T) {
	php := map[string]string{"composer.json": `{"require":{"laravel/framework":"^13.0"}}`, "composer.lock": "{}"}
	p := detect(t, php, buildplan.Settings{})
	if p.Kind != buildplan.Bundle || p.Artifact != ".homeport-bundle" || p.StaticFallback ||
		p.Command != "frankenphp-bundle . .homeport-bundle && mv -T .homeport-bundle/frankenphp .homeport-bundle/bin" || p.Run != "php-server --root public --listen :$PORT" {
		t.Fatalf("default: %+v", p)
	}
	// on the base with the static FrankenPHP and Bun, pinned by digest
	if !strings.HasPrefix(p.Image, "ghcr.io/homeport-sh/frankenphp:8.5-1.12.7-bun1.4.2@sha256:") {
		t.Fatalf("image: %s", p.Image)
	}
	if b, _ := json.Marshal(p); !strings.Contains(string(b), `"kind":"bundle"`) {
		t.Fatalf("json: %s", b)
	}
	with := func(extra map[string]string) map[string]string {
		m := map[string]string{"composer.json": `{}`, "composer.lock": "{}"}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	octane := with(map[string]string{"composer.json": `{"require":{"laravel/framework":"^13.0","laravel/octane":"^2.13"}}`})
	if p := detect(t, octane, buildplan.Settings{}); p.Run != "run --config .homeport/octane.caddyfile --adapter caddyfile" {
		t.Fatalf("octane: %q", p.Run)
	}
	if p := detect(t, with(map[string]string{"homeport.yaml": "run: php-server --root public --listen :$PORT --access-log\n"}), buildplan.Settings{}); p.Run != "php-server --root public --listen :$PORT --access-log" {
		t.Fatalf("homeport.yaml: %q", p.Run)
	}
	// a run from before bundles: an embedded FrankenPHP served public/
	// without --root; from a bundle's folder that would serve the app's
	// own files, so it keeps serving public/
	if p := detect(t, php, buildplan.Settings{Run: "php-server"}); p.Run != "php-server --root public" {
		t.Errorf("bare php-server: %q", p.Run)
	}
	for _, run := range []string{"php-server --listen :$PORT", "php-server  --listen :$PORT --access-log"} {
		p := detect(t, with(map[string]string{"homeport.yaml": "run: " + run + "\n"}), buildplan.Settings{})
		if !strings.HasPrefix(p.Run, "php-server --root public ") || !strings.Contains(p.Run, "--listen :$PORT") {
			t.Errorf("%q: %q", run, p.Run)
		}
		if p := detect(t, php, buildplan.Settings{Run: run}); !strings.HasPrefix(p.Run, "php-server --root public ") {
			t.Errorf("settings %q: %q", run, p.Run)
		}
	}
	for _, run := range []string{"php-server -r web --listen :$PORT", "php-server --root=web --listen :$PORT", "php-cli artisan serve"} {
		if p := detect(t, with(map[string]string{"homeport.yaml": "run: " + run + "\n"}), buildplan.Settings{}); p.Run != run {
			t.Errorf("%q changed: %q", run, p.Run)
		}
	}
	if p := detect(t, with(map[string]string{"homeport.yaml": "build:\n  artifact: dist/app\n"}), buildplan.Settings{}); p.Command != "frankenphp-bundle . dist/app && mv -T dist/app/frankenphp dist/app/bin" || p.Artifact != "dist/app" || p.Kind != buildplan.Bundle {
		t.Fatalf("artifact: %+v", p)
	}
	if p := detect(t, php, buildplan.Settings{Run: "php-server --root public --listen :$PORT --debug"}); p.Run != "php-server --root public --listen :$PORT --debug" {
		t.Fatalf("settings: %q", p.Run)
	}
	if p := detect(t, php, buildplan.Settings{Kind: buildplan.Static, Output: "public"}); p.Run != "" || p.Kind != buildplan.Static {
		t.Fatalf("static: %+v", p)
	}
	// other toolchains' binaries run as they are
	if p := detect(t, map[string]string{"go.mod": "module m\n\ngo 1.24\n"}, buildplan.Settings{}); p.Run != "" || p.Kind != buildplan.Binary {
		t.Fatalf("go: %+v", p)
	}
}

// How the app runs - its start command, its release command and its
// processes - is a person's say too: what they set wins over homeport.yaml,
// which wins over detection; what they leave empty is the file's, or none.
func TestHowItRunsIsSetOverTheFileOverDetection(t *testing.T) {
	files := map[string]string{
		"go.mod":        "module m\n\ngo 1.24\n",
		"homeport.yaml": "run: serve --port $PORT\nrelease: migrate\nprocesses:\n  worker: work --queue default\n",
	}
	p := detect(t, files, buildplan.Settings{})
	if p.Run != "serve --port $PORT" || p.Release != "migrate" || len(p.Processes) != 1 || p.Processes[0].Name != "worker" {
		t.Fatalf("file: %+v", p)
	}
	p = detect(t, files, buildplan.Settings{Release: "migrate --force", Processes: []buildplan.Process{
		{Name: "scheduler", Run: "schedule:work"}, {Name: "mailer", Run: "queue:work --queue mail"}}})
	if p.Run != "serve --port $PORT" || p.Release != "migrate --force" {
		t.Fatalf("settings: %+v", p)
	}
	// the person's processes are the app's: the file's are replaced, not added to
	if len(p.Processes) != 2 || p.Processes[0].Name != "mailer" || p.Processes[1].Name != "scheduler" {
		t.Fatalf("processes: %+v", p.Processes)
	}
	// none anywhere: none
	if p := detect(t, map[string]string{"go.mod": "module m\n\ngo 1.24\n"}, buildplan.Settings{}); p.Release != "" || p.Processes != nil {
		t.Fatalf("detected: %+v", p)
	}
}

// One deploy's settings ("use once") win over the app's saved ones, field by
// field: what it leaves empty is the saved settings'.
func TestOneDeploysSettingsWinOverTheSavedOnes(t *testing.T) {
	off, on := false, true
	saved := buildplan.Settings{Root: "web", Command: "bun run build", Run: "serve", Release: "migrate", Octane: &on,
		Processes: []buildplan.Process{{Name: "worker", Run: "work"}}}
	got := saved.With(buildplan.Settings{Run: "serve --debug", Octane: &off})
	if got.Run != "serve --debug" || got.Release != "migrate" || got.Command != "bun run build" || got.Root != "web" ||
		got.Octane == nil || *got.Octane || len(got.Processes) != 1 {
		t.Fatalf("with: %+v", got)
	}
	got = saved.With(buildplan.Settings{Processes: []buildplan.Process{{Name: "mailer", Run: "mail"}}})
	if len(got.Processes) != 1 || got.Processes[0].Name != "mailer" || got.Octane == nil || !*got.Octane {
		t.Fatalf("processes: %+v", got)
	}
	if len(saved.Processes) != 1 || saved.Processes[0].Name != "worker" {
		t.Fatalf("saved changed: %+v", saved)
	}
	if !(buildplan.Settings{}).IsZero() || saved.IsZero() || (buildplan.Settings{Octane: &off}).IsZero() {
		t.Fatal("IsZero")
	}
}

// Laravel Octane is served when the app requires laravel/octane - unless a
// person turned it off; turned on, it needs the package (its worker comes
// from it). A start command wins over either.
func TestOctaneIsDetectedAndCanBeTurnedOff(t *testing.T) {
	off, on := false, true
	octane := map[string]string{"composer.json": `{"require":{"laravel/framework":"^13.0","laravel/octane":"^2.13"}}`, "composer.lock": "{}"}
	plain := map[string]string{"composer.json": `{"require":{"laravel/framework":"^13.0"}}`, "composer.lock": "{}"}
	if p := detect(t, octane, buildplan.Settings{}); p.Run != buildplan.PHPOctaneRun || !p.Octane || !p.OctaneAvailable {
		t.Fatalf("detected: %+v", p)
	}
	if p := detect(t, octane, buildplan.Settings{Octane: &off}); p.Run != buildplan.PHPRun || p.Octane || !p.OctaneAvailable {
		t.Fatalf("off: %+v", p)
	}
	if p := detect(t, octane, buildplan.Settings{Octane: &on, Run: "php-server --root public --listen :$PORT --debug"}); p.Octane || !strings.HasSuffix(p.Run, "--debug") {
		t.Fatalf("a start command wins: %+v", p)
	}
	if p := detect(t, plain, buildplan.Settings{}); p.Run != buildplan.PHPRun || p.Octane || p.OctaneAvailable {
		t.Fatalf("plain: %+v", p)
	}
	if _, err := buildplan.Detect(repo(plain), buildplan.Settings{Octane: &on}); err == nil || !strings.Contains(err.Error(), "laravel/octane") {
		t.Fatalf("on without the package: %v", err)
	}
	if _, err := buildplan.Detect(repo(map[string]string{"go.mod": "module m\n\ngo 1.24\n"}), buildplan.Settings{Octane: &on}); err == nil || !strings.Contains(err.Error(), "Laravel") {
		t.Fatalf("on for Go: %v", err)
	}
	// off means nothing to an app that isn't PHP
	if p := detect(t, map[string]string{"go.mod": "module m\n\ngo 1.24\n"}, buildplan.Settings{Octane: &off}); p.Octane || p.Run != "" {
		t.Fatalf("off for Go: %+v", p)
	}
}

// How an app runs is exec'd in its sandbox without a shell, so settings are
// checked by the sandbox's rules before anything is planned: run may name
// only $PORT and $HOST; a release and each process are args to ./bin with no
// variables; at most four processes, named, never web or release.
func TestHowItRunsIsCheckedByTheSandboxsRules(t *testing.T) {
	ok := buildplan.Settings{Run: "serve --addr ${HOST}:$PORT", Release: "php-cli artisan migrate --force",
		Processes: []buildplan.Process{{Name: "worker", Run: "php-cli artisan queue:work", Memory: "256M", CPU: "50%"}}}
	if err := ok.Check(); err != nil {
		t.Fatalf("ok: %v", err)
	}
	five := make([]buildplan.Process, 5)
	for i := range five {
		five[i] = buildplan.Process{Name: "p" + string(rune('a'+i)), Run: "x"}
	}
	for name, c := range map[string]struct {
		s    buildplan.Settings
		says string
	}{
		"run with a shell":         {buildplan.Settings{Run: "serve; rm -rf /"}, "run"},
		"run with a variable":      {buildplan.Settings{Run: "serve $SECRET"}, "$PORT"},
		"run with a newline":       {buildplan.Settings{Run: "serve\nrm"}, "control characters"},
		"release with &&":          {buildplan.Settings{Release: "migrate && seed"}, "release"},
		"release with a pipe":      {buildplan.Settings{Release: "migrate | tee"}, "release"},
		"release with a variable":  {buildplan.Settings{Release: "migrate $DB"}, "release"},
		"release with a newline":   {buildplan.Settings{Release: "migrate\nseed"}, "release"},
		"release through ./bin":    {buildplan.Settings{Release: "./bin migrate"}, "write `migrate`"},
		"release too long":         {buildplan.Settings{Release: strings.Repeat("x", 1001)}, "1000"},
		"process named web":        {buildplan.Settings{Processes: []buildplan.Process{{Name: "web", Run: "x"}}}, "not web or release"},
		"process named release":    {buildplan.Settings{Processes: []buildplan.Process{{Name: "release", Run: "x"}}}, "not web or release"},
		"process named in caps":    {buildplan.Settings{Processes: []buildplan.Process{{Name: "Worker", Run: "x"}}}, "lowercase"},
		"process name too long":    {buildplan.Settings{Processes: []buildplan.Process{{Name: strings.Repeat("a", 16), Run: "x"}}}, "max 15"},
		"process twice":            {buildplan.Settings{Processes: []buildplan.Process{{Name: "worker", Run: "x"}, {Name: "worker", Run: "y"}}}, "twice"},
		"process with no command":  {buildplan.Settings{Processes: []buildplan.Process{{Name: "worker"}}}, "command"},
		"process with a shell":     {buildplan.Settings{Processes: []buildplan.Process{{Name: "worker", Run: "work; rm -rf /"}}}, "without a shell"},
		"process with a backtick":  {buildplan.Settings{Processes: []buildplan.Process{{Name: "worker", Run: "work `id`"}}}, "without a shell"},
		"process with a newline":   {buildplan.Settings{Processes: []buildplan.Process{{Name: "worker", Run: "work\nrm"}}}, "control characters"},
		"process command too long": {buildplan.Settings{Processes: []buildplan.Process{{Name: "worker", Run: strings.Repeat("x", 1001)}}}, "1000"},
		"process memory":           {buildplan.Settings{Processes: []buildplan.Process{{Name: "worker", Run: "x", Memory: "lots"}}}, "memory"},
		"process cpu":              {buildplan.Settings{Processes: []buildplan.Process{{Name: "worker", Run: "x", CPU: "half"}}}, "cpu"},
		"five processes":           {buildplan.Settings{Processes: five}, "at most 4"},
	} {
		err := c.s.Check()
		if err == nil || !strings.Contains(err.Error(), c.says) {
			t.Errorf("%s: %v (want it to say %q)", name, err, c.says)
		}
		if _, derr := buildplan.Detect(repo(map[string]string{"go.mod": "module m\n\ngo 1.24\n"}), c.s); derr == nil {
			t.Errorf("%s: planned", name)
		}
	}
}

// The Octane switch is the person's say, so it wins over homeport.yaml's run;
// a start command they set wins over the switch.
func TestTheOctaneSwitchWinsOverTheFilesRun(t *testing.T) {
	off, on := false, true
	files := map[string]string{"composer.json": `{"require":{"laravel/octane":"^2.13"}}`, "composer.lock": "{}",
		"homeport.yaml": "run: php-server --root public --listen :$PORT --access-log\n"}
	if p := detect(t, files, buildplan.Settings{}); p.Octane || !strings.HasSuffix(p.Run, "--access-log") {
		t.Fatalf("the file's run: %+v", p)
	}
	if p := detect(t, files, buildplan.Settings{Octane: &on}); !p.Octane || p.Run != buildplan.PHPOctaneRun {
		t.Fatalf("on: %+v", p)
	}
	if p := detect(t, files, buildplan.Settings{Octane: &off}); p.Octane || !strings.HasSuffix(p.Run, "--access-log") {
		t.Fatalf("off keeps the file's run: %+v", p)
	}
	files["homeport.yaml"] = "run: " + buildplan.PHPOctaneRun + "\n"
	if p := detect(t, files, buildplan.Settings{Octane: &off}); p.Octane || p.Run != buildplan.PHPRun {
		t.Fatalf("off over the file's Octane run: %+v", p)
	}
}

// A process's memory and CPU are bounded as homeportd bounds them: 1M to
// 16384M (K counts in whole MB, so less than 1024K is none), 1% to 1600%.
func TestAProcesssLimitsAreHomeportdsLimits(t *testing.T) {
	with := func(mem, cpu string) buildplan.Settings {
		return buildplan.Settings{Processes: []buildplan.Process{{Name: "worker", Run: "work", Memory: mem, CPU: cpu}}}
	}
	for _, ok := range []buildplan.Settings{with("1M", ""), with("16384M", ""), with("16G", ""), with("1024K", ""), with("", "1%"), with("", "1600%"), with("256M", "50%")} {
		if err := ok.Check(); err != nil {
			t.Errorf("%+v: %v", ok.Processes[0], err)
		}
	}
	for _, bad := range []buildplan.Settings{with("0M", ""), with("512K", ""), with("16385M", ""), with("17G", ""), with("99999999G", ""),
		with("123456789M", ""), with("", "0%"), with("", "1601%"), with("", "9999%"), with("", "00000%")} {
		if err := bad.Check(); err == nil {
			t.Errorf("%+v accepted", bad.Processes[0])
		}
	}
}

// Nothing in settings holds a control character - DEL included: none is
// ever meant, and the builder's jq would write one larger than Go counts it.
func TestSettingsHoldNoControlCharacters(t *testing.T) {
	for name, s := range map[string]buildplan.Settings{
		"install DEL":  {Install: "bun install\x7f"},
		"command tab":  {Command: "bun\trun build"},
		"command ESC":  {Command: "bun run build\x1b[31m"},
		"output DEL":   {Output: "dist\x7f"},
		"kind NUL":     {Kind: "static\x00"},
		"run DEL":      {Run: "serve\x7f"},
		"release BEL":  {Release: "migrate\x07"},
		"process name": {Processes: []buildplan.Process{{Name: "work\x7f", Run: "x"}}},
		"process run":  {Processes: []buildplan.Process{{Name: "worker", Run: "x\x7f"}}},
		"memory":       {Processes: []buildplan.Process{{Name: "worker", Run: "x", Memory: "1M\x7f"}}},
		"bad utf-8":    {Command: "bun run \xff"},
	} {
		err := s.Check()
		if err == nil || !strings.Contains(err.Error(), "control character") && !strings.Contains(err.Error(), "UTF-8") {
			t.Errorf("%s: %v", name, err)
		}
	}
	if err := (buildplan.Settings{Command: "bun run build && echo é"}).Check(); err != nil {
		t.Fatalf("plain text: %v", err)
	}
}
