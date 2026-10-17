package envfile_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/homeport-sh/cli/envfile"
)

// A variable's "Available to" is one of three, and says who sees it: the
// build, the running app, or both.
func TestAvailabilitySaysWhoSeesAValue(t *testing.T) {
	for a, want := range map[envfile.Availability]struct {
		build, runtime bool
		label          string
	}{
		envfile.Runtime:   {false, true, "Runtime"},
		envfile.Build:     {true, true, "Build and runtime"},
		envfile.BuildOnly: {true, false, "Build only"},
	} {
		if !a.Valid() {
			t.Errorf("%q isn't valid", a)
		}
		if a.BuildSees() != want.build || a.RuntimeSees() != want.runtime {
			t.Errorf("%q: build %v runtime %v, want %v %v", a, a.BuildSees(), a.RuntimeSees(), want.build, want.runtime)
		}
		if a.Label() != want.label {
			t.Errorf("%q: label %q, want %q", a, a.Label(), want.label)
		}
	}
	for _, a := range []envfile.Availability{"", "Runtime", "build-only", "both"} {
		if a.Valid() {
			t.Errorf("%q is valid", a)
		}
	}
	if _, err := envfile.ParseAvailability("buildonly"); err == nil {
		t.Error("ParseAvailability took buildonly")
	}
	if a, err := envfile.ParseAvailability("build_only"); err != nil || a != envfile.BuildOnly {
		t.Errorf("ParseAvailability(build_only) = %q, %v", a, err)
	}
}

// A name a framework writes into the page is public by that framework's own
// rule, so it defaults to Build and runtime; every other name to Runtime.
// Prefixes match as written, upper case, as the frameworks match them.
func TestPublicPrefixesDefaultToBuildAndRuntime(t *testing.T) {
	want := []string{"VITE_", "NEXT_PUBLIC_", "PUBLIC_", "NUXT_PUBLIC_", "EXPO_PUBLIC_", "REACT_APP_",
		"GATSBY_", "VUE_APP_", "STORYBOOK_", "REDWOOD_ENV_"}
	if fmt.Sprint(envfile.PublicPrefixes) != fmt.Sprint(want) {
		t.Errorf("PublicPrefixes = %v, want %v", envfile.PublicPrefixes, want)
	}
	for name, a := range map[string]envfile.Availability{
		"VITE_API_URL": envfile.Build, "NEXT_PUBLIC_API_URL": envfile.Build, "PUBLIC_SITE": envfile.Build,
		"NUXT_PUBLIC_API_BASE": envfile.Build, "EXPO_PUBLIC_KEY": envfile.Build, "REACT_APP_X": envfile.Build,
		"GATSBY_X": envfile.Build, "VUE_APP_X": envfile.Build, "STORYBOOK_X": envfile.Build, "REDWOOD_ENV_X": envfile.Build,
		// near misses
		"NEXTPUBLIC_X": envfile.Runtime, "next_public_x": envfile.Runtime, "vite_x": envfile.Runtime,
		"PUBLIC": envfile.Runtime, "VITE": envfile.Runtime, "NEXT_PUBLIC_": envfile.Runtime, "MY_PUBLIC_KEY": envfile.Runtime,
		"DATABASE_URL": envfile.Runtime, "NPM_TOKEN": envfile.Runtime,
	} {
		if got := envfile.DefaultAvailable(name); got != a {
			t.Errorf("DefaultAvailable(%q) = %q, want %q", name, got, a)
		}
	}
}

// A build may not take a name its builder sets itself (the build needs its
// own HOME and caches), nor one homeportd refuses anywhere. At runtime those
// builder names are ordinary.
func TestNamesABuildMayNotTake(t *testing.T) {
	for _, n := range []string{"PATH", "HOME", "TMPDIR", "CI", "GOCACHE", "GOMODCACHE", "BUN_INSTALL_CACHE_DIR", "npm_config_cache", "CARGO_HOME"} {
		err := envfile.CheckBuildName(n)
		if err == nil || !strings.Contains(err.Error(), n) || !strings.Contains(err.Error(), "builder") {
			t.Errorf("CheckBuildName(%q) = %v, want the builder's own", n, err)
		}
		if _, err := envfile.Line(n, "x"); err != nil {
			t.Errorf("%s at runtime: %v", n, err)
		}
	}
	for _, n := range []string{"LD_PRELOAD", "BASH_ENV", "HOMEPORT_X", "1X", "A-B", ""} {
		if envfile.CheckBuildName(n) == nil {
			t.Errorf("CheckBuildName(%q) took it", n)
		}
	}
	for _, n := range []string{"NEXT_PUBLIC_X", "NPM_TOKEN", "SENTRY_AUTH_TOKEN", "VITE_X", "lower_ok", "NODE_OPTIONS"} {
		if err := envfile.CheckBuildName(n); err != nil {
			t.Errorf("CheckBuildName(%q): %v", n, err)
		}
	}
}

// A build value is one line of at most 4096 bytes; a refusal names the
// variable, never its value.
func TestABuildValueIsOneShortLine(t *testing.T) {
	const secret = "s3cr3t-value"
	for name, v := range map[string]string{
		"too long": secret + strings.Repeat("x", envfile.MaxBuildValue),
		"newline":  secret + "\nx", "carriage return": secret + "\rx", "NUL": secret + "\x00x",
	} {
		err := envfile.CheckBuildValue("NPM_TOKEN", v)
		if err == nil {
			t.Errorf("%s: taken", name)
			continue
		}
		if !strings.Contains(err.Error(), "NPM_TOKEN") || strings.Contains(err.Error(), secret) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if err := envfile.CheckBuildValue("NPM_TOKEN", strings.Repeat("x", envfile.MaxBuildValue)); err != nil {
		t.Errorf("4096 bytes: %v", err)
	}
	if err := envfile.CheckBuildValue("NPM_TOKEN", ""); err != nil {
		t.Errorf("empty: %v", err)
	}
}

// A build takes at most 64 variables and 32 KiB of them altogether, as the
// job encodes them (JSON), so the job always fits a builder's 64 KiB.
func TestABuildsVariablesFitItsJob(t *testing.T) {
	if envfile.MaxBuildVars != 64 || envfile.MaxBuildValue != 4096 || envfile.MaxBuildBytes != 32<<10 {
		t.Fatalf("limits %d %d %d", envfile.MaxBuildVars, envfile.MaxBuildValue, envfile.MaxBuildBytes)
	}
	many := map[string]string{}
	for i := range 65 {
		many[fmt.Sprintf("V%d", i)] = "x"
	}
	if err := envfile.CheckBuild(many); err == nil || !strings.Contains(err.Error(), "65") {
		t.Errorf("65 variables: %v", err)
	}
	delete(many, "V64")
	if err := envfile.CheckBuild(many); err != nil {
		t.Errorf("64 variables: %v", err)
	}
	// 9 values of 4000 bytes: each fine, 36 KB together
	big := map[string]string{}
	for i := range 9 {
		big[fmt.Sprintf("V%d", i)] = strings.Repeat("x", 4000)
	}
	err := envfile.CheckBuild(big)
	if err == nil || !strings.Contains(err.Error(), "KiB") {
		t.Errorf("36 KB: %v", err)
	}
	// JSON is what's measured: a < is six bytes there
	esc := map[string]string{"A": strings.Repeat("<", 4000), "B": strings.Repeat("<", 4000)}
	if envfile.BuildBytes(esc) <= 8000*6 {
		t.Errorf("BuildBytes(<) = %d, want it measured as JSON", envfile.BuildBytes(esc))
	}
	if err := envfile.CheckBuild(map[string]string{"PATH": "/x"}); err == nil {
		t.Error("CheckBuild took PATH")
	}
	if err := envfile.CheckBuild(map[string]string{"X": "a\nb"}); err == nil {
		t.Error("CheckBuild took a newline")
	}
}
