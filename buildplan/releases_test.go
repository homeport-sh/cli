package buildplan_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/homeport-sh/cli/buildplan"
)

// releaseJSON is what a value says its release command is, as JSON: a
// string for one, a list for several, "" for none.
func releaseJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return string(m["release"])
}

// settings reads build settings as the control plane sends them.
func settings(t *testing.T, js string) buildplan.Settings {
	t.Helper()
	var s buildplan.Settings
	if err := json.Unmarshal([]byte(js), &s); err != nil {
		t.Fatalf("settings %s: %v", js, err)
	}
	return s
}

var releaseApp = map[string]string{"package-lock.json": npmLock,
	"package.json": `{"scripts":{"start":"node server.js"},"dependencies":{"express":"^5"}}`}

func with(files map[string]string, kv ...string) map[string]string {
	out := map[string]string{}
	for k, v := range files {
		out[k] = v
	}
	for i := 0; i+1 < len(kv); i += 2 {
		out[kv[i]] = kv[i+1]
	}
	return out
}

// homeport.yaml's release: one command, or a list of them run in order;
// an empty list, as "", says there's none.
func TestHomeportYAMLSaysSeveralReleaseCommands(t *testing.T) {
	p, err := buildplan.Detect(repo(with(releaseApp, "homeport.yaml", "release:\n  - scripts/migrate.js\n  - scripts/seed.js --class=Base\n")), buildplan.Settings{})
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if got := releaseJSON(t, p); got != `["scripts/migrate.js","scripts/seed.js --class=Base"]` {
		t.Errorf("a list: %s", got)
	}
	// one is still a string, as it always was
	if got := releaseJSON(t, detect(t, with(releaseApp, "homeport.yaml", "release: scripts/migrate.js\n"), buildplan.Settings{})); got != `"scripts/migrate.js"` {
		t.Errorf("one: %s", got)
	}
	if got := releaseJSON(t, detect(t, with(releaseApp, "homeport.yaml", "release: [scripts/migrate.js]\n"), buildplan.Settings{})); got != `"scripts/migrate.js"` {
		t.Errorf("a list of one: %s", got)
	}
	for _, none := range []string{"release: []\n", "release: \"\"\n", "release: none\n"} {
		p := detect(t, with(releaseApp, "homeport.yaml", none), buildplan.Settings{})
		if got := releaseJSON(t, p); got != "" || !p.NoRelease || !strings.Contains(p.ReleaseReason, "homeport.yaml") {
			t.Errorf("%q: %s %v %q", none, got, p.NoRelease, p.ReleaseReason)
		}
	}
	// each is checked: one the sandbox can't run fails the plan's check
	if _, err := buildplan.Detect(repo(with(releaseApp, "homeport.yaml", "release:\n  - migrate\n  - 7\n  - [x]\n")), buildplan.Settings{}); err == nil {
		t.Error("a list of a list: no error")
	}
}

// Build settings' release: a string or a list, read as either; sent as
// they were, one command as a string - what every CLI before read.
func TestBuildSettingsSaySeveralReleaseCommands(t *testing.T) {
	s := settings(t, `{"release":["scripts/migrate.js","scripts/seed.js --class=Base"]}`)
	if err := s.Check(); err != nil {
		t.Fatalf("check: %v", err)
	}
	if got := releaseJSON(t, s); got != `["scripts/migrate.js","scripts/seed.js --class=Base"]` {
		t.Errorf("settings: %s", got)
	}
	// they win over homeport.yaml's, the whole list
	p := detect(t, with(releaseApp, "homeport.yaml", "release: [a, b, c]\n"), s)
	if got := releaseJSON(t, p); got != `["scripts/migrate.js","scripts/seed.js --class=Base"]` || p.ReleaseReason != "" {
		t.Errorf("plan: %s (%s)", got, p.ReleaseReason)
	}
	one := settings(t, `{"release":"scripts/migrate.js"}`)
	if got := releaseJSON(t, one); got != `"scripts/migrate.js"` {
		t.Errorf("one: %s", got)
	}
	if got := releaseJSON(t, settings(t, `{"release":["scripts/migrate.js"]}`)); got != `"scripts/migrate.js"` {
		t.Errorf("a list of one: %s", got)
	}
	if got := releaseJSON(t, settings(t, `{"run":"x"}`)); got != "" {
		t.Errorf("none: %s", got)
	}
	// one deploy's say over the saved ones: its list, whole
	saved := settings(t, `{"release":["a","b"]}`)
	if got := releaseJSON(t, saved.With(settings(t, `{"release":"c"}`))); got != `"c"` {
		t.Errorf("with one: %s", got)
	}
	if got := releaseJSON(t, saved.With(settings(t, `{"run":"x"}`))); got != `["a","b"]` {
		t.Errorf("with nothing said: %s", got)
	}
	if got := releaseJSON(t, saved.With(settings(t, `{"no_release":true}`))); got != "" {
		t.Errorf("with none: %s", got)
	}
}

// Each command is held to the sandbox's rules and named when it isn't;
// at most MaxReleaseCommands of them, and none empty.
func TestSeveralReleaseCommandsAreEachChecked(t *testing.T) {
	cases := map[string]struct{ settings, says string }{
		"a shell in the second":  {`{"release":["migrate","seed && more"]}`, "release command 2"},
		"./bin in the first":     {`{"release":["./bin migrate","seed"]}`, "release command 1"},
		"an empty one":           {`{"release":["migrate","  "]}`, "release command 2"},
		"a newline":              {`{"release":["migrate\nseed"]}`, "release command"},
		"too many":               {`{"release":["a","b","c","d","e","f","g","h","i","j","k"]}`, "at most 10"},
		"a list and none at all": {`{"release":["a","b"],"no_release":true}`, "one or the other"},
	}
	for name, c := range cases {
		err := settings(t, c.settings).Check()
		if err == nil || !strings.Contains(err.Error(), c.says) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if err := settings(t, `{"release":["a","b","c","d","e","f","g","h","i","j"]}`).Check(); err != nil {
		t.Errorf("ten: %v", err)
	}
}

// A Laravel app's views are cached once, after every command has run:
// the runner wraps the last.
func TestALaravelAppsSeveralReleaseCommandsCacheViewsAfterTheLast(t *testing.T) {
	const runner = "php-cli .homeport/release.php ./bin"
	p := detect(t, laravel(nil), settings(t, `{"release":["php-cli artisan migrate --force","php-cli artisan db:seed --class=Base --force"]}`))
	if got := releaseJSON(t, p); got != `["php-cli artisan migrate --force","`+runner+` php-cli artisan db:seed --class=Base --force"]` {
		t.Errorf("release: %s", got)
	}
	p = detect(t, laravel(nil, "homeport.yaml", "release:\n  - php-cli artisan migrate --force\n  - php-cli artisan db:seed --force\n"), buildplan.Settings{})
	if got := releaseJSON(t, p); got != `["php-cli artisan migrate --force","`+runner+` php-cli artisan db:seed --force"]` {
		t.Errorf("homeport.yaml: %s", got)
	}
}
