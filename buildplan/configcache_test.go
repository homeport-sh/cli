package buildplan_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/homeport-sh/cli/buildplan"
)

// A Laravel app's config is cached as each of its servers starts, from the
// environment it starts with: the bundle's .homeport/start.php caches it,
// then runs the command. The web starts through the bundle's own
// .homeport/wrap, which homeportd reads, so its start command stays as it
// was; the plan runs each process through the runner. Cached at build, the
// config would hold the build's environment, and an app whose environment
// changes is restarted without a build.
func TestALaravelAppsProcessesStartWithItsConfigCached(t *testing.T) {
	const runner = buildplan.StartRunner + " "
	if buildplan.StartRunner != "php-cli .homeport/start.php ./bin" {
		t.Fatalf("runner: %q", buildplan.StartRunner)
	}
	runs := func(p buildplan.Plan) map[string]string {
		m := map[string]string{}
		for _, x := range p.Processes {
			m[x.Name] = x.Run
		}
		return m
	}

	// homeport.yaml's processes, and Reverb's
	files := laravel([]string{"laravel/reverb"}, "homeport.yaml", "processes:\n  worker: php-cli artisan queue:work\n")
	p := detect(t, files, buildplan.Settings{})
	if got := runs(p); got["worker"] != runner+"php-cli artisan queue:work" || got["reverb"] != runner+buildplan.ReverbRun || len(got) != 2 {
		t.Errorf("processes: %v", got)
	}
	if err := buildplan.CheckProcesses(p.Processes); err != nil {
		t.Errorf("check: %v", err)
	}
	// the web's start command is as it was: its wrap runs it through
	if p.Run != buildplan.PHPRun {
		t.Errorf("run: %q", p.Run)
	}
	// Octane's too
	if p := detect(t, laravel([]string{"laravel/octane"}), buildplan.Settings{}); p.Run != buildplan.PHPOctaneRun || !p.Octane {
		t.Errorf("octane: %q", p.Run)
	}

	// the settings' processes, which replace the file's
	p = detect(t, files, buildplan.Settings{Processes: []buildplan.Process{{Name: "mailer", Run: "php-cli artisan queue:work --queue=mail", Memory: "256M"}}})
	if got := runs(p); got["mailer"] != runner+"php-cli artisan queue:work --queue=mail" || got["reverb"] != runner+buildplan.ReverbRun {
		t.Errorf("settings: %v", got)
	}
	if i := slices.IndexFunc(p.Processes, func(x buildplan.Process) bool { return x.Name == "mailer" }); i < 0 || p.Processes[i].Memory != "256M" {
		t.Errorf("its size: %+v", p.Processes)
	}

	// one that runs through it already (a plan's command, set again) isn't
	// run through it twice
	p = detect(t, laravel(nil), buildplan.Settings{Processes: []buildplan.Process{{Name: "worker", Run: runner + "php-cli artisan queue:work"}}})
	if got := runs(p); got["worker"] != runner+"php-cli artisan queue:work" {
		t.Errorf("twice: %v", got)
	}

	// one too long to run through it runs as it is, and the plan says so
	long := "php-cli artisan queue:work " + strings.Repeat("x", 1000-len("php-cli artisan queue:work "))
	p = detect(t, laravel(nil), buildplan.Settings{Processes: []buildplan.Process{{Name: "worker", Run: long}}})
	if got := runs(p); got["worker"] != long || !slices.ContainsFunc(p.Warnings, func(w string) bool { return strings.Contains(w, "config") && strings.Contains(w, "worker") }) {
		t.Errorf("too long: %v %v", got, p.Warnings)
	}
	// one the sandbox can't run is left as it is, for the check that
	// refuses it, saying why
	p = detect(t, laravel(nil, "homeport.yaml", "processes:\n  worker: php-cli artisan queue:work && x\n"), buildplan.Settings{})
	if got := runs(p); got["worker"] != "php-cli artisan queue:work && x" || buildplan.CheckProcesses(p.Processes) == nil {
		t.Errorf("can't run: %v", got)
	}

	// not Laravel, or a build a person wrote (the runner may not be in its
	// bundle): the processes are theirs, as they were
	for name, c := range map[string]struct {
		files map[string]string
		s     buildplan.Settings
	}{
		"plain PHP": {map[string]string{"composer.json": `{"require":{"slim/slim":"^4"}}`, "composer.lock": "{}",
			"homeport.yaml": "processes:\n  worker: php-cli worker.php\n"}, buildplan.Settings{}},
		"build command": {files, buildplan.Settings{Command: "frankenphp-bundle . .homeport-bundle"}},
		"homeport.yaml": {laravel(nil, "homeport.yaml", "build:\n  command: frankenphp-bundle . .homeport-bundle\nprocesses:\n  worker: php-cli artisan queue:work\n"), buildplan.Settings{}},
	} {
		p := detect(t, c.files, c.s)
		for n, r := range runs(p) {
			if strings.Contains(r, "start.php") {
				t.Errorf("%s: %s runs %q", name, n, r)
			}
		}
	}
}

// Inertia's SSR renderer runs beside the web through the bundle's wrap: the
// wrap starts the config's runner first when the bundle has one, so the web
// is cached as it would be without SSR.
func TestTheSSRWrapCachesTheConfigFirst(t *testing.T) {
	kit := laravel([]string{inertia}, "package.json", `{"scripts":{"build":"vite build","build:ssr":"vite build && vite build --ssr"}}`, "package-lock.json", "{}")
	p := detect(t, kit, buildplan.Settings{})
	want := "if [ -f .homeport-bundle/.homeport/start.php ]; then printf '%s\\n' 'bin php-cli .homeport/start.php ./bin php-cli .homeport/beside.php' > .homeport-bundle/.homeport/wrap; " +
		"else printf '%s\\n' 'bin php-cli .homeport/beside.php' > .homeport-bundle/.homeport/wrap; fi"
	if !strings.Contains(p.Command, want) {
		t.Errorf("wrap: %s", p.Command)
	}
}
