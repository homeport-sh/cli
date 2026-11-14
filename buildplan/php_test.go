package buildplan_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/homeport-sh/cli/buildplan"
)

const (
	bundleAt   = "frankenphp-bundle . .homeport-bundle && mv -T .homeport-bundle/frankenphp .homeport-bundle/bin"
	bundleRoot = "frankenphp-bundle --root . . .homeport-bundle && mv -T .homeport-bundle/frankenphp .homeport-bundle/bin"
	composerIn = "composer check-platform-reqs --no-dev --lock && composer install --no-dev --optimize-autoloader --no-interaction"
)

// A plain PHP app - no framework: index.php and a few more .php files at
// its top, no composer.json - is a PHP app as a Laravel one is: a bundle,
// its files and FrankenPHP as bin, served by the bundle's web config. Its
// document root is its own folder (--root .), where its files are; nothing
// to install, nothing of Laravel's.
func TestAPlainPHPAppIsServedFromItsOwnFolder(t *testing.T) {
	p := detect(t, map[string]string{"index.php": "<?php echo 'hi';", "about.php": "<?php echo 'about';"}, buildplan.Settings{})
	if p.Kind != buildplan.Bundle || p.Toolchain != "php" || p.Framework != "PHP" || p.Image != buildplan.FrankenPHPImage ||
		p.Artifact != ".homeport-bundle" || p.StaticFallback {
		t.Fatalf("plan: %+v", p)
	}
	if p.Install != "" || p.Command != bundleRoot || p.Build != "" || p.Run != buildplan.PHPRun {
		t.Fatalf("install %q, command %q, build %q, run %q", p.Install, p.Command, p.Build, p.Run)
	}
	if p.Octane || p.OctaneAvailable || p.Queue != nil || len(p.Release) > 0 || p.SSR != "" || p.Reverb || len(p.Assets) > 0 {
		t.Fatalf("Laravel's in a plain app: %+v", p)
	}
}

// Its document root is public/ when the app has public/index.php - as a
// Laravel app does, and many a plain one - and the app's own folder
// otherwise: index.php at its top, a public/ of assets or none.
func TestAPHPAppsDocumentRoot(t *testing.T) {
	for name, c := range map[string]struct {
		files   map[string]string
		command string
	}{
		"index.php at its top":                    {map[string]string{"index.php": "<?php"}, bundleRoot},
		"public/index.php":                        {map[string]string{"public/index.php": "<?php", "src/app.php": "<?php"}, bundleAt},
		"both: public/ is the site":               {map[string]string{"index.php": "<?php", "public/index.php": "<?php"}, bundleAt},
		"public/ without index.php: its top":      {map[string]string{"index.php": "<?php", "public/app.css": "a{}"}, bundleRoot},
		"composer, index.php at its top":          {map[string]string{"composer.json": `{"require":{"monolog/monolog":"^3"}}`, "composer.lock": "{}", "index.php": "<?php"}, bundleRoot},
		"composer, public/index.php":              {map[string]string{"composer.json": `{}`, "composer.lock": "{}", "public/index.php": "<?php"}, bundleAt},
		"composer, no index.php: public/, as was": {map[string]string{"composer.json": `{}`, "composer.lock": "{}"}, bundleAt},
	} {
		if p := detect(t, c.files, buildplan.Settings{}); p.Command != c.command || p.Toolchain != "php" {
			t.Errorf("%s: command %q, toolchain %q", name, p.Command, p.Toolchain)
		}
	}
	// homeport.yaml's artifact: the bundle's there, from the same root
	p := detect(t, map[string]string{"index.php": "<?php", "homeport.yaml": "build:\n  artifact: dist/app\n"}, buildplan.Settings{})
	if p.Command != "frankenphp-bundle --root . . dist/app && mv -T dist/app/frankenphp dist/app/bin" {
		t.Errorf("artifact: %q", p.Command)
	}
}

// Composer runs when there's a composer.json - with its lockfile, and the
// PHP it requires checked against homeport's - and not when there's none.
// Front-end assets are built as a Laravel app's are, when it has a build
// script.
func TestAPlainPHPAppsInstall(t *testing.T) {
	p := detect(t, map[string]string{"composer.json": `{"require":{"php":"^8.3"}}`, "composer.lock": "{}", "index.php": "<?php"}, buildplan.Settings{})
	if p.Install != composerIn {
		t.Errorf("composer: %q", p.Install)
	}
	if _, err := buildplan.Detect(repo(map[string]string{"composer.json": `{}`, "index.php": "<?php"}), buildplan.Settings{}); err == nil || !strings.Contains(err.Error(), "composer.lock") {
		t.Errorf("no lockfile: %v", err)
	}
	if _, err := buildplan.Detect(repo(map[string]string{"composer.json": `{"require":{"php":"^7.4"}}`, "composer.lock": "{}", "index.php": "<?php"}), buildplan.Settings{}); err == nil || !strings.Contains(err.Error(), "homeport runs PHP 8.5") {
		t.Errorf("PHP 7.4: %v", err)
	}
	p = detect(t, map[string]string{"index.php": "<?php", "package.json": `{"scripts":{"build":"vite build"}}`, "bun.lock": "{}"}, buildplan.Settings{})
	if p.Toolchain != "php" || p.Install != "bun install --frozen-lockfile && bun run build" || p.Command != bundleRoot {
		t.Errorf("assets: %+v", p)
	}
}

// A folder with index.php is a PHP app, whatever else is beside it - an
// index.html too, a package.json for its assets - and one without isn't:
// a .php file elsewhere in a static site, a Go app, stay what they were.
func TestWhatIsAPlainPHPApp(t *testing.T) {
	for name, files := range map[string]map[string]string{
		"index.html beside":   {"index.php": "<?php", "index.html": "<h1>hi</h1>"},
		"public/index.php":    {"public/index.php": "<?php"},
		"package.json beside": {"index.php": "<?php", "package.json": `{}`},
	} {
		if p := detect(t, files, buildplan.Settings{}); p.Toolchain != "php" || p.Kind != buildplan.Bundle {
			t.Errorf("%s: %+v", name, p)
		}
	}
	if p := detect(t, map[string]string{"index.html": "<h1>hi</h1>", "contact.php": "<?php"}, buildplan.Settings{}); p.Kind != buildplan.Static {
		t.Errorf("a site with a .php file: %+v", p)
	}
	if p := detect(t, map[string]string{"go.mod": "module m\n\ngo 1.24\n", "public/index.php": "<?php"}, buildplan.Settings{}); p.Toolchain != "go" {
		t.Errorf("go: %+v", p)
	}
	// what the control plane fetches to show the plan
	for _, f := range []string{"index.php", "public/index.php"} {
		if !slices.Contains(buildplan.Files(), f) {
			t.Errorf("Files() hasn't %s", f)
		}
	}
	// and what's said of a folder with nothing to build names it
	if _, err := buildplan.Detect(repo(map[string]string{"README.md": "x"}), buildplan.Settings{}); err == nil || !strings.Contains(err.Error(), "index.php") {
		t.Errorf("unknown: %v", err)
	}
}

// What the app's settings say still wins: a start command (a php-server
// one gets no --root public: its folder is the site), a static kind.
func TestAPlainPHPAppsSettings(t *testing.T) {
	files := map[string]string{"index.php": "<?php"}
	if p := detect(t, files, buildplan.Settings{Run: "php-server --listen :$PORT"}); p.Run != "php-server --listen :$PORT" {
		t.Errorf("php-server: %q", p.Run)
	}
	if p := detect(t, files, buildplan.Settings{Kind: buildplan.Static, Output: "."}); p.Kind != buildplan.Static || p.Run != "" {
		t.Errorf("static: %+v", p)
	}
	if _, err := buildplan.Detect(repo(files), buildplan.Settings{Octane: new(true)}); err == nil {
		t.Error("Octane on a plain app")
	}
}
