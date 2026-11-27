package buildplan_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/homeport-sh/cli/buildplan"
)

const cronsYAML = `
crons:
  cleanup:
    command: cleanup
    schedule: "45 3 * * *"
    timeout: 300s
  reports: { command: php-cli artisan reports:send, schedule: "@daily", timezone: America/New_York }
`

func cronsJSON(t *testing.T, c []buildplan.Cron) string {
	t.Helper()
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// homeport.yaml's crons are in the plan, by name, each with its timeout in
// seconds (600 unsaid) and its zone (UTC unsaid): what the control plane
// keeps as the app's declared jobs on each deploy.
func TestHomeportYamlsCronsAreInThePlan(t *testing.T) {
	p := detect(t, map[string]string{"go.mod": "module x\n\ngo 1.24\n", "homeport.yaml": cronsYAML}, buildplan.Settings{})
	want := `[{"name":"cleanup","command":"cleanup","schedule":"45 3 * * *","timezone":"UTC","timeout":300},` +
		`{"name":"reports","command":"php-cli artisan reports:send","schedule":"@daily","timezone":"America/New_York","timeout":600}]`
	if got := cronsJSON(t, p.Crons); got != want {
		t.Fatalf("plan's crons:\n got %s\nwant %s", got, want)
	}
	if p := detect(t, map[string]string{"go.mod": "module x\n\ngo 1.24\n"}, buildplan.Settings{}); p.Crons != nil {
		t.Fatalf("none said: %v", p.Crons)
	}
}

// A cron that can't run fails the plan, saying which and why.
func TestABadCronInHomeportYamlFailsThePlan(t *testing.T) {
	for yaml, says := range map[string]string{
		"crons:\n  Clean: {command: cleanup, schedule: '* * * * *'}\n":                   `cron "Clean": a name of lowercase letters, digits and -`,
		"crons:\n  clean: {schedule: '* * * * *'}\n":                                     `cron "clean": a command`,
		"crons:\n  clean: {command: ./bin cleanup, schedule: '* * * * *'}\n":             "args to the app's binary",
		"crons:\n  clean: {command: cleanup && rm x, schedule: '* * * * *'}\n":           "without a shell",
		"crons:\n  clean: {command: cleanup}\n":                                          `cron "clean": a schedule`,
		"crons:\n  clean: {command: cleanup, schedule: '45 3 * * * ; x'}\n":              `cron "clean": schedule`,
		"crons:\n  clean: {command: cleanup, schedule: '* * * * *', timeout: 5s}\n":      `cron "clean": timeout 10s to 1h`,
		"crons:\n  clean: {command: cleanup, schedule: '* * * * *', timeout: 2h}\n":      `cron "clean": timeout 10s to 1h`,
		"crons:\n  clean: {command: cleanup, schedule: '* * * * *', timeout: soon}\n":    `cron "clean": timeout`,
		"crons:\n  clean: {command: cleanup, schedule: '* * * * *', timezone: Mars/X}\n": `cron "clean": timezone`,
	} {
		_, err := buildplan.Detect(repo(map[string]string{"go.mod": "module x\n\ngo 1.24\n", "homeport.yaml": yaml}), buildplan.Settings{})
		if err == nil || !strings.Contains(err.Error(), says) {
			t.Errorf("%q: %v, want %q", yaml, err, says)
		}
	}
	var many strings.Builder
	many.WriteString("crons:\n")
	for i := range buildplan.MaxCrons + 1 {
		many.WriteString("  job-" + string(rune('a'+i)) + ": {command: x, schedule: '@daily'}\n")
	}
	if _, err := buildplan.Detect(repo(map[string]string{"go.mod": "module x\n\ngo 1.24\n", "homeport.yaml": many.String()}), buildplan.Settings{}); err == nil ||
		!strings.Contains(err.Error(), "at most 25 crons") {
		t.Errorf("26 crons: %v", err)
	}
	// a static site has no server for them to run in
	if _, err := buildplan.Detect(repo(map[string]string{"index.html": "<h1>hi</h1>", "homeport.yaml": cronsYAML}), buildplan.Settings{}); err == nil ||
		!strings.Contains(err.Error(), "a static site") {
		t.Errorf("static site: %v", err)
	}
}

// FileCrons is homeport.yaml's crons alone, checked: what the control plane
// reads from a CI upload's commit, which no builder planned.
func TestFileCronsReadsHomeportYaml(t *testing.T) {
	got, err := buildplan.FileCrons([]byte(cronsYAML + "run: serve\n"))
	if err != nil || len(got) != 2 || got[0].Name != "cleanup" || got[1].Timezone != "America/New_York" {
		t.Fatalf("got %v, %v", got, err)
	}
	if got, err := buildplan.FileCrons([]byte("run: serve\n")); err != nil || got != nil {
		t.Fatalf("none: %v, %v", got, err)
	}
	if _, err := buildplan.FileCrons([]byte("crons:\n  x: {command: a}\n")); err == nil {
		t.Fatal("no schedule passed")
	}
	if _, err := buildplan.FileCrons([]byte("crons: [")); err == nil {
		t.Fatal("broken YAML passed")
	}
}

// CheckCrons is what the control plane holds a builder's plan to: the same
// rules as homeport.yaml's, each name once.
func TestCheckCrons(t *testing.T) {
	ok := buildplan.Cron{Name: "cleanup", Command: "cleanup", Schedule: "45 3 * * *", Timezone: "UTC", Timeout: 300}
	if err := buildplan.CheckCrons([]buildplan.Cron{ok}); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []buildplan.Cron{
		{Name: "Bad", Command: "x", Schedule: "@daily", Timezone: "UTC", Timeout: 60},
		{Name: "x", Command: "a; b", Schedule: "@daily", Timezone: "UTC", Timeout: 60},
		{Name: "x", Command: "a", Schedule: "", Timezone: "UTC", Timeout: 60},
		{Name: "x", Command: "a", Schedule: "@daily", Timezone: "", Timeout: 60},
		{Name: "x", Command: "a", Schedule: "@daily", Timezone: "UTC", Timeout: 9},
		{Name: "x", Command: "a", Schedule: "@daily", Timezone: "UTC", Timeout: 3601},
	} {
		if err := buildplan.CheckCrons([]buildplan.Cron{bad}); err == nil {
			t.Errorf("%+v passed", bad)
		}
	}
	if err := buildplan.CheckCrons([]buildplan.Cron{ok, ok}); err == nil || !strings.Contains(err.Error(), "twice") {
		t.Errorf("a name twice: %v", err)
	}
}
