package buildplan

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// A PHP app - Laravel, or plain PHP with no framework - ships as a bundle:
// its files, installed (composer, when it has a composer.json; front-end
// assets, when it builds any), and at their top homeport's FrankenPHP as
// bin, built once on the FrankenPHP base: nothing is compiled per app. The
// bundle's own web config serves its document root (frankenphp-bundle,
// images/frankenphp in homeport's repository). What only Laravel needs -
// its caches, Inertia's SSR, Reverb, Octane, its queue - is laravel.go's.

// phpApp: a composer.json says it's a PHP app; so does an index.php, at its
// top or in public/, where nothing says it's another toolchain's (a Go or
// Rust app, a Python one, with a PHP file in it).
func (r reader) phpApp() bool {
	if r.exists("composer.json") {
		return true
	}
	if !r.exists("index.php") && !r.exists("public/index.php") {
		return false
	}
	return !r.exists("go.mod") && !r.exists("Cargo.toml") && !r.python()
}

// phpDocRoot: the folder the web serves, in the app's - public/ when it has
// public/index.php (Laravel's, and many a plain app's), else its own folder
// (.) when its index.php is at its top. frankenphp-bundle serves public/
// unless told otherwise, and from any root never serves what isn't the
// site's: composer's files, vendor/, dotfiles, homeport.yaml, php.ini.
func (r reader) phpDocRoot() string {
	if !r.exists("public/index.php") && r.exists("index.php") {
		return "."
	}
	return "public"
}

// php plans a PHP app: on homeport's FrankenPHP base, where FrankenPHP is
// built already. With a composer.json the platform check comes first
// (seconds), then composer; then the front-end assets if the app builds
// any, and the app is bundled with that FrankenPHP.
func (r reader) php(p *Plan, cfg fileConfig, s Settings) error {
	composer := r.exists("composer.json")
	if composer && !r.exists("composer.lock") {
		return errors.New("composer.json without composer.lock: commit the lockfile, so the build installs what you tested")
	}
	if err := r.phpVersion(); err != nil {
		return err
	}
	p.Toolchain, p.Image, p.Framework, p.phpRoot = "php", FrankenPHPImage, "PHP", r.phpDocRoot()
	if composer {
		p.Install = "composer check-platform-reqs --no-dev --lock && composer install --no-dev --optimize-autoloader --no-interaction"
	}
	// Inertia's SSR: built with the rest, run beside the web (laravel.go)
	ssr := cfg.Build.Command == "" && cfg.Static == "" && r.inertiaSSR()
	p.Install = join(p.Install, r.frontEnd(ssr))
	if cfg.Static == "" {
		p.Kind = Bundle
		if cfg.Build.Artifact == "" {
			p.Artifact, p.StaticFallback = BundleDir, false
		}
	}
	if p.Command != "" {
		return r.laravel(p, cfg, s, false, false)
	}
	// a bundle's executable is bin, at its top: frankenphp-bundle puts
	// FrankenPHP there as frankenphp
	root := ""
	if p.phpRoot != "public" {
		root = "--root " + p.phpRoot + " "
	}
	p.Command = "frankenphp-bundle " + root + ". " + p.Artifact + " && mv -T " + p.Artifact + "/frankenphp " + p.Artifact + "/bin"
	return r.laravel(p, cfg, s, true, ssr)
}

// phpVersion: whether composer.json's require.php allows the PHP homeport
// runs (PHPVersion, a line: any of its releases). Read as Composer reads a
// constraint - , or a space is and, | or || is or, ~8.4 is ^8.4 - with the
// version ranges npm's share (semver.go). One this can't read is left to the
// build's composer check-platform-reqs.
func (r reader) phpVersion() error {
	b, err := r.read("composer.json")
	if err != nil {
		return nil
	}
	var c struct {
		Require map[string]any `json:"require"`
	}
	if json.Unmarshal(b, &c) != nil {
		return nil
	}
	asked, _ := c.Require["php"].(string)
	if strings.TrimSpace(asked) == "" {
		return nil
	}
	rng, err := parseRange(composerRange(asked))
	if err != nil {
		return nil
	}
	line := mustVersion(PHPVersion + ".0")
	for patch := range 100 {
		if v := (semver{line[0], line[1], patch}); rng.has(v) {
			return nil
		}
	}
	return fmt.Errorf("composer.json requires PHP %q: homeport runs PHP %s", asked, PHPVersion)
}

// ~ with two parts: Composer's lets the minor rise (~8.4 is >=8.4 <9),
// npm's doesn't
var composerTilde = regexp.MustCompile(`~\s*(v?\d+\.\d+)(\s|,|\||$)`)

func composerRange(c string) string {
	c = composerTilde.ReplaceAllString(c, "^$1$2")
	c = strings.ReplaceAll(c, "||", "|")
	c = strings.ReplaceAll(c, "|", "||")
	return strings.ReplaceAll(c, ",", " ")
}
