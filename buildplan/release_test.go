package buildplan_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/homeport-sh/cli/buildplan"
)

// With no release command said, the plan has the framework's obvious
// migration as one, and says why; one said in the settings or homeport.yaml
// wins, and homeport.yaml's release: "" says there's none.
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
		if p := detect(t, files, buildplan.Settings{}); strings.Contains(p.Release, "migrate") || p.ReleaseReason != "" {
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
