package buildplan_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/homeport-sh/cli/buildplan"
	"github.com/homeport-sh/cli/envfile"
)

func managedNames(t *testing.T, files map[string]string, s buildplan.Settings) []string {
	t.Helper()
	var out []string
	for _, m := range buildplan.ManagedVars(repo(files), s) {
		out = append(out, m.Name)
	}
	return out
}

// The variables a framework needs the same on every build and every copy,
// which homeport sets for an app, are told from its files: Next.js's action
// key and deployment ID, @vitejs/plugin-rsc's key (from the lockfile, so a
// framework that depends on it - Waku - counts), rsc-kit's signing secret,
// a server-rendered Astro app's key, and Laravel's log channel and URL,
// wherever Laravel runs.
func TestTheVariablesAFrameworkNeedsHomeportToSet(t *testing.T) {
	const next = `{"scripts":{"build":"next build"},"dependencies":{"next":"16.4.2","react":"19"}}`
	for name, c := range map[string]struct {
		files map[string]string
		s     buildplan.Settings
		want  []string
	}{
		"Next.js": {js(next), buildplan.Settings{}, []string{"NEXT_DEPLOYMENT_ID", "NEXT_SERVER_ACTIONS_ENCRYPTION_KEY"}},
		"rsc-kit, bun.lock": {js(`{"dependencies":{"@rsc-kit/core":"0.30.19"},"devDependencies":{"@vitejs/plugin-rsc":"0.5.38","vite":"8"}}`,
			"bun.lock", `{"packages":{"@vitejs/plugin-rsc":["@vitejs/plugin-rsc@0.5.38"]}}`),
			buildplan.Settings{}, []string{"RSC_ACTION_ENCRYPTION_KEY", "RSC_SIGNING_SECRET"}},
		"Waku, transitive, package-lock.json": {js(`{"dependencies":{"waku":"1.0.0-rc.3"}}`,
			"package-lock.json", `{"packages":{"node_modules/waku":{},"node_modules/@vitejs/plugin-rsc":{"version":"0.5.38"}}}`),
			buildplan.Settings{}, []string{"RSC_ACTION_ENCRYPTION_KEY"}},
		"plugin-rsc, pnpm-lock.yaml": {js(`{"dependencies":{"react-router":"8"}}`,
			"pnpm-lock.yaml", "packages:\n  '@vitejs/plugin-rsc@0.5.38':\n    resolution: {}\n"),
			buildplan.Settings{}, []string{"RSC_ACTION_ENCRYPTION_KEY"}},
		"plugin-rsc, yarn.lock": {js(`{"dependencies":{"react-router":"8"}}`,
			"yarn.lock", "\"@vitejs/plugin-rsc@^0.5.38\":\n  version \"0.5.38\"\n"),
			buildplan.Settings{}, []string{"RSC_ACTION_ENCRYPTION_KEY"}},
		"TanStack Start": {js(`{"dependencies":{"@tanstack/react-start":"1.169.3","nitro":"3"}}`,
			"package-lock.json", `{"packages":{"node_modules/@tanstack/react-start":{}}}`),
			buildplan.Settings{}, nil},
		"TanStack Start with RSC": {js(`{"dependencies":{"@tanstack/react-start":"1.169.3","@tanstack/react-start-rsc":"1","@vitejs/plugin-rsc":"0.5.38"}}`,
			"package-lock.json", `{"packages":{"node_modules/@vitejs/plugin-rsc":{}}}`),
			buildplan.Settings{}, []string{"RSC_ACTION_ENCRYPTION_KEY"}},
		"Astro, server": {js(`{"dependencies":{"astro":"7.4.0","@astrojs/node":"11.1.8"}}`,
			"astro.config.mjs", "export default defineConfig({ output: 'server', adapter: node({ mode: 'standalone' }) })"),
			buildplan.Settings{}, []string{"ASTRO_KEY"}},
		"Astro, static": {js(`{"dependencies":{"astro":"7.4.0"}}`, "astro.config.mjs", "export default defineConfig({})"),
			buildplan.Settings{}, nil},
		"a plain Vite app": {js(`{"dependencies":{"react":"19"},"devDependencies":{"vite":"8"}}`), buildplan.Settings{}, nil},
		"a lockfile that only mentions it": {js(`{"dependencies":{"react":"19"}}`,
			"package-lock.json", `{"packages":{"node_modules/@vitejs/plugin-rsc-helper":{}}}`), buildplan.Settings{}, nil},
		"a Go app": {map[string]string{"go.mod": "module x\n\ngo 1.27\n"}, buildplan.Settings{}, nil},
		"rsc-kit beside Laravel": {laravel(nil, "package.json", `{"dependencies":{"@rsc-kit/core":"0.35.0"}}`, "bun.lock", "{}"),
			buildplan.Settings{}, []string{"APP_URL", "LOG_CHANNEL", "RSC_HOST_CALL_SECRET", "RSC_SIGNING_SECRET"}},
		"Laravel":           {laravel(nil), buildplan.Settings{}, []string{"APP_URL", "LOG_CHANNEL"}},
		"Laravel with Vite": {laravel(nil, "package.json", `{"devDependencies":{"vite":"7","laravel-vite-plugin":"2"}}`), buildplan.Settings{}, []string{"APP_URL", "LOG_CHANNEL"}},
		"Laravel in a folder": {map[string]string{"api/composer.json": `{"require":{"laravel/framework":"^12.0"}}`, "api/composer.lock": "{}"},
			buildplan.Settings{Root: "api"}, []string{"APP_URL", "LOG_CHANNEL"}},
		"a PHP app that isn't Laravel": {map[string]string{"composer.json": `{"require":{"symfony/framework-bundle":"^7.0"}}`, "composer.lock": "{}"},
			buildplan.Settings{}, nil},
		"plain PHP": {map[string]string{"index.php": "<?php echo 1;"}, buildplan.Settings{}, nil},
		"rsc-kit beside Go": {map[string]string{"go.mod": "module x\n\ngo 1.27\n", "package.json": `{"dependencies":{"@rsc-kit/core":"0.35.0"}}`, "bun.lock": "{}"},
			buildplan.Settings{}, []string{"RSC_HOST_CALL_SECRET", "RSC_SIGNING_SECRET"}},
		"Next.js in a folder": {map[string]string{"apps/web/package.json": next, "apps/web/package-lock.json": npmLock},
			buildplan.Settings{Root: "apps/web"}, []string{"NEXT_DEPLOYMENT_ID", "NEXT_SERVER_ACTIONS_ENCRYPTION_KEY"}},
		"plugin-rsc in a workspace's lockfile": {map[string]string{
			"package.json":          `{"workspaces":["apps/*"]}`,
			"bun.lock":              `{"packages":{"@vitejs/plugin-rsc":["@vitejs/plugin-rsc@0.5.38"]}}`,
			"apps/web/package.json": `{"name":"web","dependencies":{"@rsc-kit/core":"0.30.19"}}`,
		}, buildplan.Settings{Root: "apps/web"}, []string{"RSC_ACTION_ENCRYPTION_KEY", "RSC_SIGNING_SECRET"}},
	} {
		got := managedNames(t, c.files, c.s)
		if fmt.Sprint(got) != fmt.Sprint(c.want) {
			t.Errorf("%s: %v, want %v", name, got, c.want)
		}
	}
}

// Each says how it's made, who sees it, whether its availability is fixed,
// and why, in a line the dashboard shows.
func TestHowEachManagedVariableIsMade(t *testing.T) {
	files := js(`{"dependencies":{"next":"16","@rsc-kit/core":"0.30","astro":"7","@astrojs/node":"11"}}`,
		"package-lock.json", `{"packages":{"node_modules/@vitejs/plugin-rsc":{}}}`,
		"astro.config.mjs", "export default defineConfig({ output: 'server' })", "go.mod", "module x\n\ngo 1.27\n", "bun.lock", "{}") // a Go backend beside rsc-kit
	got := map[string]buildplan.ManagedVar{}
	for _, m := range buildplan.ManagedVars(repo(files), buildplan.Settings{}) {
		got[m.Name] = m
		if m.Why == "" {
			t.Errorf("%s: no why", m.Name)
		}
	}
	for name, want := range map[string]struct {
		make      string
		available envfile.Availability
		fixed     bool
		previous  string
	}{
		"NEXT_SERVER_ACTIONS_ENCRYPTION_KEY": {buildplan.MakeKey, envfile.Build, true, ""},
		"NEXT_DEPLOYMENT_ID":                 {buildplan.MakeRelease, envfile.BuildOnly, true, ""},
		"RSC_ACTION_ENCRYPTION_KEY":          {buildplan.MakeKey, envfile.Build, true, ""},
		"RSC_SIGNING_SECRET":                 {buildplan.MakeKey, envfile.Runtime, false, "RSC_SIGNING_SECRET_PREVIOUS"},
		"RSC_HOST_CALL_SECRET":               {buildplan.MakeKey, envfile.Runtime, false, "RSC_HOST_CALL_SECRET_PREVIOUS"},
		"ASTRO_KEY":                          {buildplan.MakeKey, envfile.BuildOnly, false, ""},
	} {
		m, ok := got[name]
		if !ok {
			t.Errorf("%s: missing", name)
			continue
		}
		if m.Make != want.make || m.Available != want.available || m.Fixed != want.fixed || m.Previous != want.previous {
			t.Errorf("%s: %+v, want %+v", name, m, want)
		}
	}
	if !strings.Contains(got["RSC_ACTION_ENCRYPTION_KEY"].Why, `defineEncryptionKey: "process.env.RSC_ACTION_ENCRYPTION_KEY"`) {
		t.Errorf("plugin-rsc's key doesn't say how to wire it: %q", got["RSC_ACTION_ENCRYPTION_KEY"].Why)
	}
}

// Managed returns the entry for a name, so a control plane can tell one of
// homeport's from a person's: the two action keys' availability is fixed
// whoever sets them.
func TestAManagedNameIsKnownWithoutTheApp(t *testing.T) {
	for _, n := range []string{"NEXT_SERVER_ACTIONS_ENCRYPTION_KEY", "NEXT_DEPLOYMENT_ID", "RSC_ACTION_ENCRYPTION_KEY", "RSC_SIGNING_SECRET", "RSC_HOST_CALL_SECRET", "ASTRO_KEY", "LOG_CHANNEL", "APP_URL"} {
		if m, ok := buildplan.Managed(n); !ok || m.Name != n {
			t.Errorf("Managed(%q) = %+v, %v", n, m, ok)
		}
	}
	// not homeport's: a person's, or Laravel's own maintenance driver
	// (cache costs a lookup a request; only the app can choose that)
	for _, n := range []string{"DATABASE_URL", "APP_MAINTENANCE_DRIVER"} {
		if _, ok := buildplan.Managed(n); ok {
			t.Errorf("Managed(%s)", n)
		}
	}
	names := buildplan.ManagedNames()
	if !slices.IsSorted(names) || len(names) != 8 {
		t.Errorf("ManagedNames = %v", names)
	}
	// homeport's keys are build variables: a build may take every one
	for _, n := range names {
		if m, _ := buildplan.Managed(n); m.Available.BuildSees() {
			if err := envfile.CheckBuildName(n); err != nil {
				t.Errorf("%s: %v", n, err)
			}
		}
	}
}

// Laravel's is a fixed value, not made: a default added only where the app
// has none of its own, seen at runtime, and the person may set another.
func TestLaravelsDefaultsAreValues(t *testing.T) {
	got := map[string]buildplan.ManagedVar{}
	for _, m := range buildplan.ManagedVars(repo(laravel(nil)), buildplan.Settings{}) {
		got[m.Name] = m
	}
	for name, value := range map[string]string{"LOG_CHANNEL": "stderr"} {
		m, ok := got[name]
		if !ok {
			t.Errorf("%s: missing", name)
			continue
		}
		if m.Make != buildplan.MakeValue || m.Value != value || m.Available != envfile.Runtime || m.Fixed || m.Previous != "" || m.Why == "" {
			t.Errorf("%s: %+v, want the value %q at runtime, not fixed", name, m, value)
		}
	}
	// a key, a release or a URL has no value of its own
	for _, n := range buildplan.ManagedNames() {
		if m, _ := buildplan.Managed(n); (m.Make == buildplan.MakeValue) != (m.Value != "") {
			t.Errorf("%s: make %q, value %q", n, m.Make, m.Value)
		}
	}
}

// Laravel's APP_URL is the environment's address, which only the control
// plane knows (its primary domain, or its homeport.run name): declared
// here with no value, seen at runtime, and the person may set another.
func TestLaravelsURLIsTheControlPlanes(t *testing.T) {
	var got *buildplan.ManagedVar
	for _, m := range buildplan.ManagedVars(repo(laravel(nil)), buildplan.Settings{}) {
		if m.Name == "APP_URL" {
			got = &m
		}
	}
	if got == nil {
		t.Fatal("no APP_URL for a Laravel app")
	}
	if got.Make != buildplan.MakeURL || got.Value != "" || got.Available != envfile.Runtime || got.Fixed || got.Previous != "" || got.Why == "" {
		t.Errorf("APP_URL: %+v, want made from the URL at runtime, not fixed", *got)
	}
}

// A key homeport makes is a secret; a default or a URL is shown as it is,
// like any value a person can read.
func TestOnlyAKeyIsSecret(t *testing.T) {
	for _, n := range buildplan.ManagedNames() {
		m, _ := buildplan.Managed(n)
		if m.Secret() != (m.Make == buildplan.MakeKey) {
			t.Errorf("%s (%s): secret %v", n, m.Make, m.Secret())
		}
	}
	for n, secret := range map[string]bool{"NEXT_SERVER_ACTIONS_ENCRYPTION_KEY": true, "RSC_SIGNING_SECRET": true, "LOG_CHANNEL": false, "APP_URL": false} {
		if m, _ := buildplan.Managed(n); m.Secret() != secret {
			t.Errorf("%s: secret %v, want %v", n, m.Secret(), secret)
		}
	}
}
