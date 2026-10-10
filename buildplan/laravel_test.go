package buildplan_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/homeport-sh/cli/buildplan"
)

// laravel is a Laravel app's files: composer.json requiring laravel/framework
// and the packages named, its lockfile, and the extra files given in pairs.
func laravel(requires []string, files ...string) map[string]string {
	req := `"laravel/framework":"^13.0"`
	for _, r := range requires {
		req += `,"` + r + `":"*"`
	}
	m := map[string]string{"composer.json": `{"require":{` + req + `}}`, "composer.lock": "{}"}
	for i := 0; i+1 < len(files); i += 2 {
		m[files[i]] = files[i+1]
	}
	return m
}

const inertia = "inertiajs/inertia-laravel"

// Inertia's server-side rendering is detected from inertia-laravel and an SSR
// build: package.json's build:ssr, or an ssr entry in the Vite config. The
// build makes the SSR bundle too, and the app's bundle runs Inertia's SSR
// server beside the web, in the same sandbox, through .homeport/wrap.
func TestInertiaSSRIsDetectedAndBuilt(t *testing.T) {
	kit := laravel([]string{inertia}, "package.json", `{"type":"module","scripts":{"build":"vite build","build:ssr":"vite build && vite build --ssr"}}`,
		"package-lock.json", "{}")
	p := detect(t, kit, buildplan.Settings{})
	if p.SSR != "inertia" || p.Framework != "PHP" {
		t.Fatalf("ssr: %+v", p)
	}
	// build:ssr builds both: it replaces the build, it doesn't follow it
	if !strings.HasSuffix(p.Install, " && bun install && bun run build:ssr") {
		t.Errorf("install: %s", p.Install)
	}
	// the SSR bundle is one file, its packages in it: no node_modules ships
	for _, want := range []string{"bun build", "--target=node", "bootstrap/ssr/ssr.mjs", ".homeport/wrap", ".homeport/beside.php", ".homeport/beside"} {
		if !strings.Contains(p.Command, want) {
			t.Errorf("command has no %q: %s", want, p.Command)
		}
	}
	// Laravel's caches, then the bundle, then the SSR renderer into it
	if !strings.HasPrefix(p.Command, buildplan.LaravelCaches+" && frankenphp-bundle . .homeport-bundle && mv -T .homeport-bundle/frankenphp .homeport-bundle/bin && ") {
		t.Errorf("the bundle comes first: %s", p.Command)
	}

	// no build:ssr, but the Vite config names an SSR entry: the build, then
	// Vite's SSR build
	vite := laravel([]string{inertia}, "package.json", `{"scripts":{"build":"vite build"}}`, "bun.lock", "{}",
		"vite.config.ts", "laravel({ input: ['resources/js/app.tsx'], ssr: 'resources/js/ssr.tsx', refresh: true })")
	if p := detect(t, vite, buildplan.Settings{}); p.SSR != "inertia" || !strings.HasSuffix(p.Install, "bun run build && bun run vite build --ssr") {
		t.Errorf("vite config: %+v", p)
	}

	// Inertia without an SSR build renders in the browser: nothing beside
	// the web
	for name, files := range map[string]map[string]string{
		"inertia, no ssr build": laravel([]string{inertia}, "package.json", `{"scripts":{"build":"vite build"}}`, "package-lock.json", "{}",
			"vite.config.js", "laravel({ input: 'resources/js/app.js', refresh: true })"),
		"an ssr build, no inertia": laravel(nil, "package.json", `{"scripts":{"build":"vite build","build:ssr":"vite build --ssr"}}`, "package-lock.json", "{}"),
		"inertia, no package.json": laravel([]string{inertia}),
	} {
		p := detect(t, files, buildplan.Settings{})
		if p.SSR != "" || strings.Contains(p.Command, ".homeport/wrap") || p.Runtime != "" {
			t.Errorf("%s: %+v", name, p)
		}
	}
	// a Vite top-level ssr option isn't an SSR entry
	opts := laravel([]string{inertia}, "package.json", `{"scripts":{"build":"vite build"}}`, "package-lock.json", "{}",
		"vite.config.js", "export default { plugins: [laravel({ input: 'resources/js/app.js' })], ssr: { noExternal: true } }")
	if p := detect(t, opts, buildplan.Settings{}); p.SSR != "" {
		t.Errorf("ssr options: %+v", p)
	}
}

// The SSR server runs on Node or Bun, chosen by the rules a JavaScript app's
// runtime is - the build settings or homeport.yaml, engines, a version file,
// what the project's SSR command runs - then the lockfile, else Node. The
// pinned official binary ships in the bundle, its checksum checked.
func TestTheSSRRuntimeIsChosenByTheProjectsSignals(t *testing.T) {
	pkg := func(extra string) string {
		return `{"scripts":{"build":"vite build","build:ssr":"vite build && vite build --ssr"` + extra + `}`
	}
	for name, c := range map[string]struct {
		files   map[string]string
		s       buildplan.Settings
		rt, why string
	}{
		"nothing says":   {laravel([]string{inertia}, "package.json", pkg("}"), "package-lock.json", "{}"), buildplan.Settings{}, "node", "Node, the default"},
		"the settings":   {laravel([]string{inertia}, "package.json", pkg("}"), "package-lock.json", "{}"), buildplan.Settings{Runtime: "bun"}, "bun", "the build settings"},
		"homeport.yaml":  {laravel([]string{inertia}, "package.json", pkg("}"), "package-lock.json", "{}", "homeport.yaml", "runtime: bun\n"), buildplan.Settings{}, "bun", "homeport.yaml"},
		"settings win":   {laravel([]string{inertia}, "package.json", pkg("}"), "bun.lock", "{}", "homeport.yaml", "runtime: bun\n"), buildplan.Settings{Runtime: "node"}, "node", "the build settings"},
		"engines bun":    {laravel([]string{inertia}, "package.json", pkg(`},"engines":{"bun":">=1.4"}`), "package-lock.json", "{}"), buildplan.Settings{}, "bun", "engines"},
		"engines node":   {laravel([]string{inertia}, "package.json", pkg(`},"engines":{"node":"22"}`), "bun.lock", "{}"), buildplan.Settings{}, "node", "engines"},
		".bun-version":   {laravel([]string{inertia}, "package.json", pkg("}"), "package-lock.json", "{}", ".bun-version", "1.4"), buildplan.Settings{}, "bun", ".bun-version"},
		".nvmrc":         {laravel([]string{inertia}, "package.json", pkg("}"), "bun.lock", "{}", ".nvmrc", "22"), buildplan.Settings{}, "node", ".nvmrc"},
		"a bun script":   {laravel([]string{inertia}, "package.json", pkg(`,"ssr":"bun bootstrap/ssr/ssr.js"}`), "package-lock.json", "{}"), buildplan.Settings{}, "bun", "bun bootstrap/ssr/ssr.js"},
		"a node script":  {laravel([]string{inertia}, "package.json", pkg(`,"start:ssr":"node bootstrap/ssr/ssr.mjs"}`), "bun.lock", "{}"), buildplan.Settings{}, "node", "node bootstrap/ssr/ssr.mjs"},
		"start-ssr bun":  {laravel([]string{inertia}, "package.json", pkg("}"), "package-lock.json", "{}"), buildplan.Settings{}, "bun", "inertia:start-ssr --runtime=bun"},
		"bun's lockfile": {laravel([]string{inertia}, "package.json", pkg("}"), "bun.lock", "{}"), buildplan.Settings{}, "bun", "bun.lock"},
	} {
		if name == "start-ssr bun" {
			c.files["composer.json"] = `{"require":{"laravel/framework":"^13.0","` + inertia + `":"^2"},` +
				`"scripts":{"dev:ssr":["Composer\\Config::disableProcessTimeout","npx concurrently \"php artisan inertia:start-ssr --runtime=bun\""]}}`
		}
		p := detect(t, c.files, c.s)
		if p.SSR != "inertia" || p.Runtime != c.rt || !strings.Contains(p.RuntimeReason, c.why) {
			t.Errorf("%s: %s (%s), want %s (%s)", name, p.Runtime, p.RuntimeReason, c.rt, c.why)
			continue
		}
		// the pinned binary, into the bundle, checked
		switch c.rt {
		case "node":
			if !strings.Contains(p.Command, "https://nodejs.org/dist/v"+p.RuntimeVersion+"/node-v"+p.RuntimeVersion+"-linux-") ||
				!strings.Contains(p.Command, "sha256sum -c") || !strings.Contains(p.Command, besideLine(".homeport/node --max-old-space-size=%heap% --import ./.homeport/boot.mjs bootstrap/ssr/ssr.mjs")) {
				t.Errorf("%s: node isn't fetched and checked: %s", name, p.Command)
			}
		case "bun":
			if p.RuntimeVersion != buildplan.BunVersion || !strings.Contains(p.Command, "bun-linux-$a-"+buildplan.BunVersion+".tgz") ||
				!strings.Contains(p.Command, "sha256sum -c") || !strings.Contains(p.Command, besideLine(".homeport/bun --smol bootstrap/ssr/ssr.mjs")) {
				t.Errorf("%s: bun isn't fetched and checked: %s", name, p.Command)
			}
		}
	}
	// Node's version is the project's, from its pinned releases
	if p := detect(t, laravel([]string{inertia}, "package.json", pkg("}"), "package-lock.json", "{}", ".nvmrc", "22"), buildplan.Settings{}); p.RuntimeVersion != "22.23.3" {
		t.Errorf(".nvmrc 22: %s", p.RuntimeVersion)
	}
	if p := detect(t, laravel([]string{inertia}, "package.json", pkg("}"), "package-lock.json", "{}"), buildplan.Settings{}); p.RuntimeVersion != buildplan.DefaultNode {
		t.Errorf("default: %s", p.RuntimeVersion)
	}
	// a version homeport doesn't pin is refused, as a JavaScript app's is
	for name, files := range map[string]map[string]string{
		"node 18": laravel([]string{inertia}, "package.json", pkg("}"), "package-lock.json", "{}", ".nvmrc", "18"),
		"bun 1.3": laravel([]string{inertia}, "package.json", pkg("}"), "bun.lock", "{}", ".bun-version", "1.3"),
	} {
		if _, err := buildplan.Detect(repo(files), buildplan.Settings{}); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	// a runtime setting means nothing to a PHP app with no SSR
	if p := detect(t, laravel(nil), buildplan.Settings{Runtime: "bun"}); p.Runtime != "" || p.SSR != "" {
		t.Errorf("no ssr: %+v", p)
	}
}

// besideLine is how the command writes .homeport/beside's line.
func besideLine(cmd string) string {
	return "printf '%s\\n' '" + cmd + "' > .homeport-bundle/.homeport/beside"
}

// When the project's own SSR build compiles the SSR bundle (bun build
// --compile), that binary is what runs beside the web, and no runtime ships.
// The build image's Bun is musl's, and a compile there makes a musl binary
// whatever --target says; the sandbox has glibc. So the project's command is
// run again for the build's own arch, on the pinned glibc Bun (fetched,
// checked): --target=bun-linux-<arch> --compile-executable-path=<it>, any
// --target of its own replaced. A musl target is refused.
func TestACompiledSSRBinaryRunsAsItIs(t *testing.T) {
	compile := "bun build --compile bootstrap/ssr/app.js --outfile bootstrap/ssr/server"
	glibc := " --target=bun-linux-$t --compile-executable-path=/tmp/homeport/bun-glibc"
	for name, scripts := range map[string]string{
		"in build:ssr":            `"build:ssr":"vite build && vite build --ssr && ` + compile + `"`,
		"a script build:ssr runs": `"build:ssr":"vite build && vite build --ssr && bun run ssr:compile","ssr:compile":"` + compile + `"`,
	} {
		p := detect(t, laravel([]string{inertia}, "package.json", `{"scripts":{"build":"vite build",`+scripts+`}}`, "package-lock.json", "{}"), buildplan.Settings{})
		if p.SSR != "inertia" || p.Runtime != "bun" || p.RuntimeVersion != buildplan.BunVersion || !strings.Contains(p.RuntimeReason, "--compile") {
			t.Errorf("%s: %+v", name, p)
			continue
		}
		for _, want := range []string{compile + glibc, "bun-linux-$a-" + buildplan.BunVersion + ".tgz", "sha256sum -c",
			"cp bootstrap/ssr/server .homeport-bundle/bootstrap/ssr/server", besideLine("BUN_JSC_forceRAMSize=%heapbytes% bootstrap/ssr/server"), ".homeport/wrap"} {
			if !strings.Contains(p.Command, want) {
				t.Errorf("%s: no %q in %s", name, want, p.Command)
			}
		}
		for _, not := range []string{"nodejs.org", "--target=node", "> .homeport-bundle/.homeport/bun"} {
			if strings.Contains(p.Command, not) {
				t.Errorf("%s: %q in %s", name, not, p.Command)
			}
		}
	}
	// a target of its own is the build's arch on glibc, as any
	own := `"build:ssr":"vite build --ssr && bun build --compile --target=bun-linux-x64 bootstrap/ssr/app.js --outfile=ssr-bin"`
	p := detect(t, laravel([]string{inertia}, "package.json", `{"scripts":{`+own+`}}`, "package-lock.json", "{}"), buildplan.Settings{})
	if !strings.Contains(p.Command, "bun build --compile bootstrap/ssr/app.js --outfile=ssr-bin"+glibc) || !strings.Contains(p.Command, besideLine("BUN_JSC_forceRAMSize=%heapbytes% ssr-bin")) {
		t.Errorf("its own target: %s", p.Command)
	}
	musl := `"build:ssr":"vite build --ssr && bun build --compile --target=bun-linux-x64-musl bootstrap/ssr/app.js --outfile=ssr-bin"`
	if _, err := buildplan.Detect(repo(laravel([]string{inertia}, "package.json", `{"scripts":{`+musl+`}}`, "package-lock.json", "{}")), buildplan.Settings{}); err == nil || !strings.Contains(err.Error(), "musl") {
		t.Errorf("musl: %v", err)
	}
}

// Laravel Reverb runs as a process of its own, named reverb, listening on its
// $PORT: homeport sends the app's WebSocket paths to it. A reverb process the
// app declares is the app's; others are kept, four at most in all.
func TestReverbRunsAsAProcess(t *testing.T) {
	reverb := func(p buildplan.Plan) *buildplan.Process {
		i := slices.IndexFunc(p.Processes, func(x buildplan.Process) bool { return x.Name == "reverb" })
		if i < 0 {
			return nil
		}
		return &p.Processes[i]
	}
	p := detect(t, laravel([]string{"laravel/reverb"}), buildplan.Settings{})
	if !p.Reverb || reverb(p) == nil || reverb(p).Run != buildplan.ReverbRun {
		t.Fatalf("reverb: %+v", p)
	}
	if !strings.Contains(buildplan.ReverbRun, "reverb:start") || !strings.Contains(buildplan.ReverbRun, "--port=$PORT") {
		t.Errorf("run: %s", buildplan.ReverbRun)
	}
	if p := detect(t, laravel(nil), buildplan.Settings{}); p.Reverb || len(p.Processes) != 0 {
		t.Errorf("no reverb: %+v", p)
	}

	// beside homeport.yaml's processes, in name order
	yaml := laravel([]string{"laravel/reverb"}, "homeport.yaml", "processes:\n  worker: php-cli artisan queue:work\n  scheduler: php-cli artisan schedule:work\n")
	p = detect(t, yaml, buildplan.Settings{})
	if names := procNames(p); strings.Join(names, " ") != "reverb scheduler worker" {
		t.Errorf("with the file's: %v", names)
	}
	// and beside the settings', which replace the file's
	p = detect(t, yaml, buildplan.Settings{Processes: []buildplan.Process{{Name: "mailer", Run: "php-cli artisan queue:work --queue=mail"}}})
	if names := procNames(p); strings.Join(names, " ") != "mailer reverb" {
		t.Errorf("with the settings': %v", names)
	}
	// one the app names reverb is its own
	own := laravel([]string{"laravel/reverb"}, "homeport.yaml", "processes:\n  reverb:\n    run: php-cli artisan reverb:start --host=0.0.0.0 --port=$PORT --debug\n    memory: 256M\n")
	p = detect(t, own, buildplan.Settings{})
	if r := reverb(p); r == nil || !strings.HasSuffix(r.Run, "--debug") || r.Memory != "256M" || len(p.Processes) != 1 {
		t.Errorf("its own: %+v", p.Processes)
	}
	// four of the app's own leave no room for Reverb: it's left out, and
	// the plan says so - the build goes on
	four := []buildplan.Process{{Name: "a", Run: "x"}, {Name: "b", Run: "x"}, {Name: "c", Run: "x"}, {Name: "d", Run: "x"}}
	p = detect(t, laravel([]string{"laravel/reverb"}), buildplan.Settings{Processes: four})
	if reverb(p) != nil || len(p.Processes) != 4 || !slices.ContainsFunc(p.Warnings, func(w string) bool { return strings.Contains(w, "Reverb") }) {
		t.Errorf("five: %+v %v", p.Processes, p.Warnings)
	}
}

// Reverb's process is sized for itself, not at the app's memory: 128M (an
// app's smallest size is 256M), unless the app declares its own reverb.
func TestReverbIsSizedForItself(t *testing.T) {
	p := detect(t, laravel([]string{"laravel/reverb"}), buildplan.Settings{})
	if len(p.Processes) != 1 || p.Processes[0].Memory != buildplan.ReverbMemory || buildplan.ReverbMemory != "128M" {
		t.Errorf("size: %+v", p.Processes)
	}
}

// The renderer is held to the app's memory: Node's heap at a quarter of it
// (the supervisor fills in %heap% from HOMEPORT_MEMORY_MB), with Node's
// compile cache kept in the release's writable folder; Bun in --smol (tuned,
// not capped). The plan says the renderer shares the app's memory.
func TestTheRendererIsHeldToTheAppsMemory(t *testing.T) {
	pkg := `{"scripts":{"build":"vite build","build:ssr":"vite build && vite build --ssr"}}`
	p := detect(t, laravel([]string{inertia}, "package.json", pkg, "package-lock.json", "{}"), buildplan.Settings{})
	for _, want := range []string{"--max-old-space-size=%heap%", ".homeport-bundle/.homeport/boot.mjs", "echo homeport-cache >> .homeport-bundle/.homeport/writable"} {
		if !strings.Contains(p.Command, want) {
			t.Errorf("node: no %q", want)
		}
	}
	// general advice: the plan doesn't know the app's size, so it says no
	// size is too small
	if !slices.ContainsFunc(p.Warnings, func(w string) bool {
		return strings.Contains(w, "the app's memory") && !strings.Contains(w, "512") && !strings.Contains(w, "at least")
	}) {
		t.Errorf("no general memory note: %v", p.Warnings)
	}
	p = detect(t, laravel([]string{inertia}, "package.json", pkg, "bun.lock", "{}"), buildplan.Settings{})
	if !strings.Contains(p.Command, "--smol") || strings.Contains(p.Command, "max-old-space-size") {
		t.Errorf("bun: %s", p.Command)
	}
}

func procNames(p buildplan.Plan) []string {
	var out []string
	for _, x := range p.Processes {
		out = append(out, x.Name)
	}
	return out
}

// A process may name $PORT and $HOST, as the start command may: homeportd
// gives each process a port of its own. No other variable.
func TestAProcessMayNameItsPort(t *testing.T) {
	if err := buildplan.CheckProcesses([]buildplan.Process{{Name: "ws", Run: "php-cli artisan reverb:start --host=$HOST --port=${PORT}"}}); err != nil {
		t.Errorf("$PORT: %v", err)
	}
	for _, run := range []string{"serve --key=$APP_KEY", "serve $PORTS", "serve && x"} {
		if err := buildplan.CheckProcesses([]buildplan.Process{{Name: "ws", Run: run}}); err == nil {
			t.Errorf("%q: no error", run)
		}
	}
	// a release command still names none
	if err := buildplan.CheckRelease("migrate --port=$PORT"); err == nil {
		t.Error("release with $PORT: no error")
	}
}
