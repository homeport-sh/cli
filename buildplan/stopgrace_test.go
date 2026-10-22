package buildplan_test

import (
	"strings"
	"testing"

	"github.com/homeport-sh/cli/buildplan"
)

// A stop grace is how long the web or a process gets to finish after
// SIGTERM before it's killed: whole seconds or minutes, 1s to 15m.
func TestAStopGraceIsWholeSecondsOrMinutesWithinItsBounds(t *testing.T) {
	for v, secs := range map[string]int{"1s": 1, "20s": 20, "90s": 90, "2m": 120, "15m": 900, "900s": 900, "1m": 60} {
		got, err := buildplan.StopGraceSeconds(v)
		if err != nil || got != secs {
			t.Errorf("%q: %d, %v; want %d", v, got, err, secs)
		}
		if err := buildplan.CheckStopGrace("worker", v); err != nil {
			t.Errorf("%q refused: %v", v, err)
		}
	}
	for _, v := range []string{"0s", "0m", "901s", "16m", "1h", "90", "1.5m", "-1s", "1m30s", "abc", " 90s", "90S", "99999999s", "s"} {
		if _, err := buildplan.StopGraceSeconds(v); err == nil {
			t.Errorf("%q accepted", v)
		}
		err := buildplan.CheckStopGrace("worker", v)
		if err == nil || !strings.Contains(err.Error(), "1s to 15m") || !strings.Contains(err.Error(), "worker") {
			t.Errorf("%q: %v", v, err)
		}
	}
	// none set is the default
	if err := buildplan.CheckStopGrace("web", ""); err != nil {
		t.Fatalf("unset: %v", err)
	}
}

// Settings hold the web's grace and each process's, checked with the rest.
func TestSettingsStopGracesAreChecked(t *testing.T) {
	good := buildplan.Settings{StopGrace: "60s", Processes: []buildplan.Process{{Name: "worker", Run: "work", StopGrace: "10m"}}}
	if err := good.Check(); err != nil {
		t.Fatalf("good: %v", err)
	}
	for name, s := range map[string]buildplan.Settings{
		"web":     {StopGrace: "16m"},
		"process": {Processes: []buildplan.Process{{Name: "worker", Run: "work", StopGrace: "0s"}}},
		"DEL":     {Processes: []buildplan.Process{{Name: "worker", Run: "work", StopGrace: "10s\x7f"}}},
	} {
		if err := s.Check(); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if err := (buildplan.Settings{StopGrace: "1h"}).Check(); err == nil || !strings.Contains(err.Error(), "web") {
		t.Fatalf("the web's grace says it's the web's: %v", err)
	}
	if (buildplan.Settings{StopGrace: "30s"}).IsZero() {
		t.Fatal("a grace alone is a setting")
	}
}

// homeport.yaml's stop_grace is the web's, and a process's its own; the
// settings' web grace wins over the file's, and the settings' processes
// replace the file's, graces and all.
func TestAStopGraceComesFromTheFileUnlessSettingsSayOtherwise(t *testing.T) {
	files := map[string]string{
		"go.mod": "module m\n\ngo 1.24\n",
		"homeport.yaml": "run: serve\nstop_grace: 45s\nprocesses:\n" +
			"  worker:\n    run: work --queue default\n    stop_grace: 10m\n  clock: tick\n",
	}
	p := detect(t, files, buildplan.Settings{})
	if p.StopGrace != "45s" || len(p.Processes) != 2 || p.Processes[0].Name != "clock" || p.Processes[0].StopGrace != "" ||
		p.Processes[1].Name != "worker" || p.Processes[1].StopGrace != "10m" {
		t.Fatalf("file: %+v", p)
	}
	p = detect(t, files, buildplan.Settings{StopGrace: "2m", Processes: []buildplan.Process{{Name: "mailer", Run: "mail", StopGrace: "30s"}}})
	if p.StopGrace != "2m" || len(p.Processes) != 1 || p.Processes[0].StopGrace != "30s" {
		t.Fatalf("settings: %+v", p)
	}
	// one deploy's settings over the saved ones: its grace wins, none keeps the saved
	saved := buildplan.Settings{StopGrace: "45s"}
	if got := saved.With(buildplan.Settings{StopGrace: "5m"}); got.StopGrace != "5m" {
		t.Fatalf("with: %+v", got)
	}
	if got := saved.With(buildplan.Settings{Run: "serve"}); got.StopGrace != "45s" {
		t.Fatalf("with none: %+v", got)
	}
	// a static site runs nothing: no grace
	site := detect(t, map[string]string{"index.html": "<p>hi</p>", "homeport.yaml": "stop_grace: 45s\n"}, buildplan.Settings{})
	if site.Kind != buildplan.Static || site.StopGrace != "" {
		t.Fatalf("site: %+v", site)
	}
}
