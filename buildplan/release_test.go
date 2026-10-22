package buildplan_test

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/homeport-sh/cli/buildplan"
)

// With no release command said, the plan has the framework's obvious
// migration as one, and says why; one said in the settings or homeport.yaml
// wins, and homeport.yaml's release: "" says there's none (NoRelease).
func TestAReleaseCommandIsDetected(t *testing.T) {
	const runner = "php-cli .homeport/release.php ./bin "
	rsc := `{"name":"web","scripts":{"build":"bun --bun vite build"},"dependencies":{"@rsc-kit/core":"^0.30"}}`
	cases := map[string]struct {
		files   map[string]string
		release string
		why     string
	}{
		"laravel":             {laravel(nil), runner + "php-cli artisan migrate --force", "Laravel"},
		"laravel with octane": {laravel([]string{"laravel/octane"}), runner + "php-cli artisan migrate --force", "Laravel"},
		"rsc-kit": {map[string]string{"package.json": rsc, "bun.lock": bunLock, "src/commands/migrate.ts": "export default async () => {}\n"},
			"migrate", "src/commands/migrate.ts"},
		"a migrate script that runs a file": {map[string]string{"package-lock.json": npmLock,
			"package.json": `{"scripts":{"start":"node server.js","migrate":"node scripts/migrate.js --all"},"dependencies":{"express":"^5"}}`},
			"scripts/migrate.js --all", `"migrate"`},
		"db:migrate with Prisma": {map[string]string{"package-lock.json": npmLock,
			"package.json": `{"scripts":{"start":"node server.js","db:migrate":"prisma migrate deploy"},"dependencies":{"express":"^5","prisma":"^6"}}`},
			"node_modules/prisma/build/index.js migrate deploy", `"db:migrate"`},
		"db:migrate with drizzle-kit, by bunx": {map[string]string{"bun.lock": bunLock,
			"package.json": `{"scripts":{"start":"bun src/index.ts","db:migrate":"bunx --bun drizzle-kit migrate"},"dependencies":{"hono":"4","drizzle-kit":"1"}}`},
			"node_modules/drizzle-kit/bin.cjs migrate", `"db:migrate"`},
		"Prisma's migrations": {map[string]string{"package-lock.json": npmLock, "prisma/migrations/migration_lock.toml": "provider = \"postgresql\"\n",
			"package.json": `{"scripts":{"start":"node server.js"},"dependencies":{"express":"^5","prisma":"^6","@prisma/client":"^6"}}`},
			"node_modules/prisma/build/index.js migrate deploy", "prisma/migrations"},
	}
	for name, c := range cases {
		p := detect(t, c.files, buildplan.Settings{})
		if p.Release != c.release || !strings.Contains(p.ReleaseReason, c.why) {
			t.Errorf("%s: %q (%s)", name, p.Release, p.ReleaseReason)
		}
		if err := buildplan.CheckRelease(p.Release); err != nil {
			t.Errorf("%s: %v", name, err)
		}
		// said wins, and isn't a detection
		if p := detect(t, c.files, buildplan.Settings{Release: "other"}); !strings.HasSuffix(p.Release, "other") || p.ReleaseReason != "" {
			t.Errorf("%s, settings: %q (%s)", name, p.Release, p.ReleaseReason)
		}
		files := map[string]string{"homeport.yaml": "release: \"\"\n"}
		for k, v := range c.files {
			files[k] = v
		}
		if p := detect(t, files, buildplan.Settings{}); strings.Contains(p.Release, "migrate") || !p.NoRelease {
			t.Errorf("%s, none in homeport.yaml: %q", name, p.Release)
		}
	}
}

// What can't run as args to the bundle's bin isn't guessed at: the plan
// says why there's no release command, so it can be set.
func TestAReleaseCommandThatCantRunIsSaid(t *testing.T) {
	cases := map[string]struct {
		files map[string]string
		says  string
	}{
		"a devDependency": {map[string]string{"package-lock.json": npmLock,
			"package.json": `{"scripts":{"start":"node server.js","db:migrate":"drizzle-kit migrate"},"dependencies":{"express":"^5"},"devDependencies":{"drizzle-kit":"1"}}`},
			"devDependencies"},
		"Prisma's migrations, Prisma a devDependency": {map[string]string{"package-lock.json": npmLock, "prisma/migrations/migration_lock.toml": "x",
			"package.json": `{"scripts":{"start":"node server.js"},"dependencies":{"express":"^5"},"devDependencies":{"prisma":"^6"}}`},
			"devDependencies"},
		"a shell": {map[string]string{"package-lock.json": npmLock,
			"package.json": `{"scripts":{"start":"node server.js","migrate":"node a.js && node b.js"},"dependencies":{"express":"^5"}}`},
			"release command"},
	}
	for name, c := range cases {
		p := detect(t, c.files, buildplan.Settings{})
		if p.Release != "" || !slices.ContainsFunc(p.Warnings, func(w string) bool { return strings.Contains(w, c.says) && strings.Contains(w, "release") }) {
			t.Errorf("%s: %q %q", name, p.Release, p.Warnings)
		}
	}
	// a site, or a Go binary, has none
	for name, files := range map[string]map[string]string{
		"go":   {"go.mod": "module m\n\ngo 1.24\n"},
		"vite": {"package-lock.json": npmLock, "package.json": `{"scripts":{"build":"vite build","migrate":"node m.js"},"devDependencies":{"vite":"^7"}}`},
	} {
		if p := detect(t, files, buildplan.Settings{}); p.Release != "" || p.ReleaseReason != "" {
			t.Errorf("%s: %q", name, p.Release)
		}
	}
}

// The control plane fetches what release detection reads.
func TestReleaseFilesAreFetched(t *testing.T) {
	for _, f := range []string{"src/commands/migrate.ts", "prisma/migrations/migration_lock.toml"} {
		if !slices.Contains(buildplan.Files(), f) {
			t.Errorf("%s isn't in Files", f)
		}
	}
}

// "No release command" is a choice of its own, apart from saying nothing
// (detect it): NoRelease in the settings, or release: "" or release: none
// in homeport.yaml. The plan says there's none and who said so, and what
// detection found, which doesn't run - so a person knows what they turned
// off.
func TestNoReleaseCommandIsSaid(t *testing.T) {
	const runner = "php-cli .homeport/release.php ./bin"
	pkg := `{"scripts":{"start":"node server.js","migrate":"node scripts/migrate.js"},"dependencies":{"express":"^5"}}`
	cases := map[string]struct {
		files    map[string]string
		release  string // what runs: Laravel's view caching, never the migration
		detected string
	}{
		"laravel":          {laravel(nil), runner, "php-cli artisan migrate --force"},
		"a migrate script": {map[string]string{"package-lock.json": npmLock, "package.json": pkg}, "", "scripts/migrate.js"},
	}
	with := func(files map[string]string, yml string) map[string]string {
		out := map[string]string{"homeport.yaml": yml}
		for k, v := range files {
			out[k] = v
		}
		return out
	}
	for name, c := range cases {
		said := map[string]struct {
			files map[string]string
			s     buildplan.Settings
			by    string
		}{
			"settings":               {c.files, buildplan.Settings{NoRelease: true}, "the build settings"},
			"settings over the file": {with(c.files, "release: migrate\n"), buildplan.Settings{NoRelease: true}, "the build settings"},
			`release: ""`:            {with(c.files, "release: \"\"\n"), buildplan.Settings{}, "homeport.yaml"},
			"release: none":          {with(c.files, "release: none\n"), buildplan.Settings{}, "homeport.yaml"},
		}
		for how, w := range said {
			p := detect(t, w.files, w.s)
			if p.Release != c.release || !p.NoRelease || !strings.Contains(p.ReleaseReason, w.by) || p.ReleaseDetected != c.detected {
				t.Errorf("%s, %s: release %q, none %v (%s), detected %q", name, how, p.Release, p.NoRelease, p.ReleaseReason, p.ReleaseDetected)
			}
			if len(p.Warnings) > 0 {
				t.Errorf("%s, %s: warnings %q", name, how, p.Warnings)
			}
		}
		// detected: not none, and nothing besides it
		if p := detect(t, c.files, buildplan.Settings{}); p.NoRelease || p.ReleaseDetected != "" || !strings.HasSuffix(p.Release, c.detected) {
			t.Errorf("%s, detected: %q, none %v, detected %q", name, p.Release, p.NoRelease, p.ReleaseDetected)
		}
		// a command said over the file's none is that command
		if p := detect(t, with(c.files, "release: none\n"), buildplan.Settings{Release: "other"}); p.NoRelease || !strings.HasSuffix(p.Release, "other") ||
			p.ReleaseDetected != c.detected {
			t.Errorf("%s, a command over none: %q, none %v, detected %q", name, p.Release, p.NoRelease, p.ReleaseDetected)
		}
	}
}

// NoRelease is a setting like any other: one deploy's over the saved ones,
// either way round, never beside a command, and in JSON as no_release.
func TestNoReleaseIsASetting(t *testing.T) {
	saved := buildplan.Settings{Release: "migrate"}
	if got := saved.With(buildplan.Settings{NoRelease: true}); !got.NoRelease || got.Release != "" {
		t.Errorf("none over a command: %+v", got)
	}
	if got := (buildplan.Settings{NoRelease: true}).With(buildplan.Settings{Release: "migrate"}); got.NoRelease || got.Release != "migrate" {
		t.Errorf("a command over none: %+v", got)
	}
	if got := (buildplan.Settings{NoRelease: true}).With(buildplan.Settings{Run: "serve"}); !got.NoRelease {
		t.Errorf("none kept: %+v", got)
	}
	if (buildplan.Settings{NoRelease: true}).IsZero() {
		t.Error("none is something set")
	}
	if err := (buildplan.Settings{NoRelease: true, Release: "migrate"}).Check(); err == nil || !strings.Contains(err.Error(), "release") {
		t.Errorf("a command and none: %v", err)
	}
	if b, _ := json.Marshal(buildplan.Settings{NoRelease: true}); string(b) != `{"no_release":true}` {
		t.Errorf("json: %s", b)
	}
}
