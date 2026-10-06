// Package buildplan decides how a repository is built and what it produces -
// a binary that runs, or a static site that's served - from the repository's
// own files. homeport.yaml is never needed: the toolchain's files (go.mod, a
// lockfile, a framework's config) say enough. Where it exists, its build
// fields are used; where a person said otherwise (Settings, from the UI),
// that wins.
//
// It reads files and runs nothing: they're untrusted. A builder runs it on a
// checkout; a control plane runs it on the few files it fetched (Files).
package buildplan

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"path"
	"regexp"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// What a build produces.
const (
	Binary = "binary" // one executable, run by homeport
	Static = "static" // a folder of files, served as they are
	// a folder run by homeport: its executable is bin, at its top, run from
	// the folder; .homeport/writable lists the paths in it the app writes
	Bundle = "bundle"
)

// RSCKitMarker is what an rsc-kit build (0.29.6+) writes, in the app's
// folder: {"output":"server","compile":"<script>","binary":"<file>"} or
// {"output":"export","dir":"<folder>"}.
const RSCKitMarker = ".output/rsc-kit.json"

// ConfigFile is homeport's optional per-app config.
const ConfigFile = "homeport.yaml"

// Settings are what a person set about the build, each overriding what
// homeport.yaml or detection would say; empty is "detect it".
type Settings struct {
	Root    string `json:"root,omitempty"`    // the app's folder in the repository
	Kind    string `json:"kind,omitempty"`    // binary or static
	Install string `json:"install,omitempty"` // runs before the build command
	Command string `json:"command,omitempty"` // the build
	Output  string `json:"output,omitempty"`  // the binary, or the site's folder, relative to Root
	Run     string `json:"run,omitempty"`     // a binary's args (it's run as ./bin <run>)
}

// Plan is how the app is built and what comes out.
type Plan struct {
	Kind      string `json:"kind"`                // Binary, Static or Bundle
	Framework string `json:"framework,omitempty"` // what was recognised, for people: SvelteKit, Astro, Go, ...
	Toolchain string `json:"toolchain"`           // go, bun, node, php, custom, or none (nothing to build)
	Image     string `json:"image,omitempty"`     // the toolchain's image; none for none
	Install   string `json:"install,omitempty"`
	Command   string `json:"command,omitempty"` // empty: nothing to build
	Artifact  string `json:"artifact"`          // the binary, or the site's or bundle's folder, relative to Root
	Root      string `json:"root,omitempty"`    // the app's folder in the repository; "" is its root
	// StaticFallback: Artifact is a guess at a binary; if the build makes
	// none, a site folder (build/, dist/ or out/ with an index.html) is
	// taken instead
	StaticFallback bool `json:"static_fallback,omitempty"`
	// RSCKit: an rsc-kit app, whose build says what it made. After Command
	// the builder reads RSCKitMarker: a server, compiled by one of the app's
	// scripts into a binary, or a static export. Kind and Artifact are then
	// only what a builder that doesn't read it falls back to.
	RSCKit bool `json:"rsc_kit,omitempty"`

	// how a binary runs, from homeport.yaml or Settings (a PHP app's has a
	// default, PHPRun): checked by whoever
	// runs it (the CLI, the control plane), not here
	Run       string    `json:"run,omitempty"`
	Release   string    `json:"release,omitempty"`
	Processes []Process `json:"processes,omitempty"`
}

// Process is one of the app's processes beside the web.
type Process struct {
	Name   string `json:"name"`
	Run    string `json:"run"`
	Memory string `json:"memory,omitempty"`
	CPU    string `json:"cpu,omitempty"`
}

// FrankenPHPImage is homeport's FrankenPHP base (images/frankenphp): a
// static FrankenPHP 1.12.7 with PHP 8.5 and the standard extensions, built
// once, that bundles ship (frankenphp-bundle); composer, and Bun 1.4.2 for
// front-end assets. Pinned by digest: a tag can be pushed again.
const FrankenPHPImage = "ghcr.io/homeport-sh/frankenphp:8.5-1.12.7-bun1.4.2@sha256:190bf056138b92cce824a5630db049a6c1bb9f77bc4fb6853ef4761944503e48"

// BundleDir is where a PHP app's build puts its bundle, in the app's folder.
const BundleDir = ".homeport-bundle"

// PHPRun is how a PHP app's bundle runs when nothing says: FrankenPHP (its
// bin) serves public/ on the port homeport gives it ($PORT is substituted
// where it's run, without a shell); with no args it would print its help
// and exit. PHPOctaneRun serves an app that uses Octane in FrankenPHP's
// worker mode, through Octane's worker: the config frankenphp-bundle writes
// for it (octane:frankenphp's, without artisan in front; HOMEPORT_WORKERS
// workers, 2 by default, which the smallest size has the memory for).
const (
	PHPRun       = "php-server --root public --listen :$PORT"
	PHPOctaneRun = "run --config .homeport/octane.caddyfile --adapter caddyfile"
)

// SiteFolders are where a build's static site lands, tried in order when a
// guessed binary isn't there.
var SiteFolders = []string{"build", "dist", "out"}

// Files are the files Detect reads, relative to the app's folder: a control
// plane fetches just these to show what a build will do.
func Files() []string {
	return []string{ConfigFile, "go.mod", "composer.json", "composer.lock", "package.json", "bun.lock", "bun.lockb",
		"package-lock.json", ".nvmrc", ".bun-version", "index.html",
		"astro.config.mjs", "astro.config.ts", "astro.config.js", "astro.config.mts"}
}

var (
	versionRe = regexp.MustCompile(`^[0-9]{1,3}(\.[0-9]{1,4}){0,2}$`)
	imageRe   = regexp.MustCompile(`^[a-z0-9]+([._/-][a-z0-9]+)*(:[A-Za-z0-9._-]{1,128})?(@sha256:[0-9a-f]{64})?$`)
	// a folder or file inside the repository: plain names, no .., no spaces
	relRe        = regexp.MustCompile(`^[A-Za-z0-9._-]+(/[A-Za-z0-9._-]+)*$`)
	astroServer  = regexp.MustCompile(`output\s*:\s*['"](server|hybrid)['"]|adapter\s*:`)
	maxCommandSz = 1000
)

// fileConfig is what Detect reads of homeport.yaml.
type fileConfig struct {
	Build struct {
		Command  string `yaml:"command"`
		Artifact string `yaml:"artifact"`
		Image    string `yaml:"image"`
	} `yaml:"build"`
	Static    string                   `yaml:"static"`
	Run       string                   `yaml:"run"`
	Release   string                   `yaml:"release"`
	Processes map[string]processConfig `yaml:"processes"`
}

type processConfig struct {
	Run    string `yaml:"run"`
	Memory string `yaml:"memory"`
	CPU    string `yaml:"cpu"`
}

// UnmarshalYAML: a process is its args ("worker: queue:work") or run + limits.
func (p *processConfig) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		return n.Decode(&p.Run)
	}
	type plain processConfig
	return n.Decode((*plain)(p))
}

// Check says whether settings are well-formed, before anything reads them.
func (s Settings) Check() error {
	if s.Root != "" && (!relPath(s.Root) || s.Root == ".") {
		return fmt.Errorf("root directory %q must be a folder inside the repository", s.Root)
	}
	if s.Output != "" && !relPath(s.Output) {
		return fmt.Errorf("output %q must be a path inside the app's folder", s.Output)
	}
	if s.Kind != "" && s.Kind != Binary && s.Kind != Static {
		return fmt.Errorf("kind %q: binary or static", s.Kind)
	}
	for name, v := range map[string]string{"install": s.Install, "build": s.Command, "run": s.Run} {
		if len(v) > maxCommandSz || strings.ContainsAny(v, "\n\r\x00") {
			return fmt.Errorf("%s command: one line, at most %d characters", name, maxCommandSz)
		}
	}
	return nil
}

func relPath(p string) bool {
	p = strings.TrimPrefix(p, "./")
	if p == "." {
		return true
	}
	if !relRe.MatchString(p) {
		return false
	}
	return !slices.Contains(strings.Split(p, "/"), "..")
}

func clean(p string) string {
	p = path.Clean(strings.TrimPrefix(p, "./"))
	return p
}

// Detect says how the app in fsys (a repository) builds.
func Detect(fsys fs.FS, s Settings) (Plan, error) {
	if err := s.Check(); err != nil {
		return Plan{}, err
	}
	r := reader{fsys: fsys, root: "."}
	if s.Root != "" {
		r.root = clean(s.Root)
		if st, err := lstat(fsys, r.root); err != nil || !st.IsDir() {
			return Plan{}, fmt.Errorf("root directory %q isn't a folder in the repository", s.Root)
		}
	}
	var cfg fileConfig
	if b, err := r.read(ConfigFile); err == nil {
		if err := yaml.Unmarshal(b, &cfg); err != nil {
			return Plan{}, fmt.Errorf("%s: %w", ConfigFile, err)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return Plan{}, err
	}

	p, err := r.detect(cfg)
	if err != nil {
		return Plan{}, err
	}
	if s.Root != "" {
		p.Root = r.root
	}
	p.Run, p.Release = cfg.Run, cfg.Release
	for _, name := range slices.Sorted(maps.Keys(cfg.Processes)) {
		pc := cfg.Processes[name]
		p.Processes = append(p.Processes, Process{Name: name, Run: pc.Run, Memory: pc.Memory, CPU: pc.CPU})
	}

	// the person's say, last
	if s.Kind != "" && s.Kind != p.Kind {
		p.Kind, p.StaticFallback = s.Kind, false
		if s.Output == "" {
			p.Artifact = map[string]string{Binary: "server", Static: "dist"}[s.Kind]
		}
	}
	if s.Install != "" {
		p.Install = s.Install
	}
	if s.Kind != "" || s.Command != "" || s.Output != "" {
		p.RSCKit = false // the person said what the build makes
	}
	if s.Command != "" {
		p.Command = s.Command
		if p.Toolchain == "none" {
			return Plan{}, errors.New("a build command needs a toolchain: there's no package.json, go.mod or composer.json in the app's folder")
		}
	}
	if s.Output != "" {
		p.Artifact, p.StaticFallback = clean(s.Output), false
	}
	if s.Run != "" {
		p.Run = s.Run
	}
	if p.Toolchain == "php" && p.Kind == Bundle {
		switch {
		case p.Run == "" && r.requires("laravel/octane"):
			p.Run = PHPOctaneRun
		case p.Run == "":
			p.Run = PHPRun
		default:
			p.Run = phpServerRoot(p.Run)
		}
	}
	if p.Kind == Static {
		p.Run, p.Release, p.Processes = "", "", nil // nothing runs
	}
	return p, nil
}

// detect is the plan from the repository's files and homeport.yaml.
func (r reader) detect(cfg fileConfig) (Plan, error) {
	p := Plan{Kind: Binary, Command: cfg.Build.Command, Artifact: cfg.Build.Artifact}
	if p.Artifact == "" {
		p.Artifact, p.StaticFallback = "server", true
	}
	if !relPath(p.Artifact) {
		return Plan{}, fmt.Errorf("build.artifact %q must be a path inside the repository", p.Artifact)
	}
	p.Artifact = clean(p.Artifact)
	if cfg.Static != "" {
		if !relPath(cfg.Static) {
			return Plan{}, fmt.Errorf("static %q must be a folder inside the repository", cfg.Static)
		}
		p.Kind, p.Artifact, p.StaticFallback = Static, clean(cfg.Static), false
	}

	switch {
	case cfg.Build.Image != "":
		if !imageRe.MatchString(cfg.Build.Image) || len(cfg.Build.Image) > 255 {
			return Plan{}, fmt.Errorf("build.image %q isn't an image reference", cfg.Build.Image)
		}
		if p.Command == "" {
			return Plan{}, errors.New("build.image needs a build.command: what to run in it")
		}
		p.Toolchain, p.Image, p.Framework = "custom", cfg.Build.Image, "Custom image"
	case r.exists("composer.json"):
		// PHP: on homeport's FrankenPHP base, where FrankenPHP is built
		// already. The platform check comes first (seconds), then composer,
		// then the front-end assets if the app builds any, and the app is
		// bundled with that FrankenPHP: nothing is compiled.
		if !r.exists("composer.lock") {
			return Plan{}, errors.New("composer.json without composer.lock: commit the lockfile, so the build installs what you tested")
		}
		p.Toolchain, p.Image, p.Framework = "php", FrankenPHPImage, "PHP"
		p.Install = "composer check-platform-reqs --no-dev --lock && composer install --no-dev --optimize-autoloader --no-interaction"
		if r.exists("package.json") && r.hasScript("build") {
			if r.exists("bun.lock") || r.exists("bun.lockb") {
				p.Install += " && bun install --frozen-lockfile && bun run build"
			} else {
				p.Install += " && bun install && bun run build"
			}
		}
		if cfg.Static == "" {
			p.Kind = Bundle
			if cfg.Build.Artifact == "" {
				p.Artifact, p.StaticFallback = BundleDir, false
			}
		}
		if p.Command == "" {
			p.Command = "frankenphp-bundle . " + p.Artifact
		}
	case r.exists("go.mod"):
		v, err := r.goVersion()
		if err != nil {
			return Plan{}, err
		}
		p.Toolchain, p.Image, p.Framework = "go", "golang:"+v, "Go"
		if p.Command == "" {
			p.Command = `CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o ` + p.Artifact + " ."
		}
	case r.exists("package.json") && (r.exists("bun.lock") || r.exists("bun.lockb") || r.packageManager("bun") != ""):
		v := r.packageManager("bun")
		if v == "" {
			v = r.firstLine(".bun-version")
		}
		if v == "" {
			v = "1"
		}
		if !versionRe.MatchString(v) {
			return Plan{}, fmt.Errorf("bun version %q isn't a version", v)
		}
		p.Toolchain, p.Image, p.Install, p.Framework = "bun", "oven/bun:"+v, "bun install --frozen-lockfile", "Bun"
		if p.Command == "" {
			p.Command = "bun run build"
		}
		if r.deps()["@rsc-kit/core"] {
			r.rscKit(&p, cfg)
		} else {
			r.site(&p, cfg)
		}
	case r.exists("package.json") && r.exists("package-lock.json"):
		v := strings.TrimPrefix(r.firstLine(".nvmrc"), "v")
		if v == "" {
			v = "22"
		}
		if !versionRe.MatchString(v) {
			return Plan{}, fmt.Errorf(".nvmrc %q isn't a version", v)
		}
		p.Toolchain, p.Image, p.Install, p.Framework = "node", "node:"+v, "npm ci", "Node"
		if p.Command == "" {
			p.Command = "npm run build"
		}
		r.site(&p, cfg)
	case r.exists("index.html"):
		// plain HTML: nothing to build, the folder is the site
		p.Toolchain, p.Framework, p.Kind, p.Artifact, p.StaticFallback = "none", "HTML", Static, ".", false
		if cfg.Static != "" {
			p.Artifact = clean(cfg.Static)
		}
	default:
		return Plan{}, errors.New("can't tell how to build this app: no go.mod, composer.json, bun or npm lockfile, or index.html in its folder - " +
			"set the build and output in the app's build settings")
	}
	return p, nil
}

// rscKit: an rsc-kit app's build decides between a server and a static
// export (RSCKit), unless homeport.yaml said what it makes. Its default
// binary, else a site in dist/, is what an older builder falls back to.
func (r reader) rscKit(p *Plan, cfg fileConfig) {
	p.Framework = "rsc-kit"
	if cfg.Static != "" || cfg.Build.Command != "" || cfg.Build.Artifact != "" {
		return
	}
	p.RSCKit, p.Artifact, p.StaticFallback = true, "dist/app", true
}

// servers are packages that mean the app runs a server, not a site.
var servers = []string{"@rsc-kit/core", "next", "nuxt", "@remix-run/node", "@react-router/node", "@tanstack/react-start",
	"hono", "express", "elysia", "fastify", "@sveltejs/adapter-node", "@astrojs/node"}

// site recognises a JavaScript project whose build is a static site, from its
// packages and config: where the site lands, unless something said otherwise.
func (r reader) site(p *Plan, cfg fileConfig) {
	deps := r.deps()
	if slices.ContainsFunc(servers, func(s string) bool { return deps[s] }) {
		return
	}
	framework, folder := "", ""
	switch {
	case deps["@sveltejs/adapter-static"]:
		framework, folder = "SvelteKit", "build"
	case deps["astro"]:
		for _, f := range []string{"astro.config.mjs", "astro.config.ts", "astro.config.js", "astro.config.mts"} {
			if b, err := r.read(f); err == nil && astroServer.Match(b) {
				return
			}
		}
		framework, folder = "Astro", "dist"
	case deps["vite"] && !deps["@sveltejs/kit"]:
		framework, folder = "Vite", "dist"
	default:
		return
	}
	p.Framework = framework
	if cfg.Static == "" && cfg.Build.Artifact == "" {
		p.Kind, p.Artifact, p.StaticFallback = Static, folder, false
	}
}

// reader reads the app's files: regular files only, never through a symlink
// (which could point anywhere on a builder), at most 1 MiB each.
type reader struct {
	fsys fs.FS
	root string
}

func (r reader) name(n string) string { return path.Join(r.root, n) }

func (r reader) read(n string) ([]byte, error) {
	p := r.name(n)
	st, err := lstat(r.fsys, p)
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", n)
	}
	f, err := r.fsys.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, 1<<20))
}

// lstat is Lstat where the filesystem can tell links apart (a checkout on
// disk), else Stat (files fetched into memory, which are never links).
func lstat(fsys fs.FS, name string) (fs.FileInfo, error) {
	if l, ok := fsys.(fs.ReadLinkFS); ok {
		return l.Lstat(name)
	}
	return fs.Stat(fsys, name)
}

func (r reader) exists(n string) bool {
	st, err := lstat(r.fsys, r.name(n))
	return err == nil && st.Mode().IsRegular()
}

func (r reader) deps() map[string]bool {
	b, err := r.read("package.json")
	if err != nil {
		return nil
	}
	var pkg struct {
		Deps    map[string]any `json:"dependencies"`
		DevDeps map[string]any `json:"devDependencies"`
	}
	if json.Unmarshal(b, &pkg) != nil {
		return nil
	}
	out := map[string]bool{}
	for k := range pkg.Deps {
		out[k] = true
	}
	for k := range pkg.DevDeps {
		out[k] = true
	}
	return out
}

// phpServerRoot: a php-server run that names no document root serves
// public/. An embedded FrankenPHP did that by itself, so a run written for
// one says no --root; run from a bundle's folder it would serve the app's
// own files (its config, its .env).
func phpServerRoot(run string) string {
	f := strings.Fields(run)
	if len(f) == 0 || f[0] != "php-server" {
		return run
	}
	for _, a := range f[1:] {
		if a == "-r" || a == "--root" || strings.HasPrefix(a, "--root=") || strings.HasPrefix(a, "-r=") {
			return run
		}
	}
	return strings.Join(append([]string{"php-server", "--root", "public"}, f[1:]...), " ")
}

// requires says whether composer.json requires a package.
func (r reader) requires(pkg string) bool {
	b, err := r.read("composer.json")
	if err != nil {
		return false
	}
	var c struct {
		Require map[string]any `json:"require"`
	}
	return json.Unmarshal(b, &c) == nil && c.Require[pkg] != nil
}

func (r reader) hasScript(name string) bool {
	b, err := r.read("package.json")
	if err != nil {
		return false
	}
	var pkg struct {
		Scripts map[string]string `json:"scripts"`
	}
	return json.Unmarshal(b, &pkg) == nil && pkg.Scripts[name] != ""
}

// packageManager is the version in package.json's "packageManager":
// "<name>@<version>", or "".
func (r reader) packageManager(name string) string {
	b, err := r.read("package.json")
	if err != nil {
		return ""
	}
	var pkg struct {
		PackageManager string `json:"packageManager"`
	}
	if json.Unmarshal(b, &pkg) != nil {
		return ""
	}
	v, ok := strings.CutPrefix(pkg.PackageManager, name+"@")
	if !ok {
		return ""
	}
	v, _, _ = strings.Cut(v, "+") // a pinned hash after the version
	return v
}

func (r reader) firstLine(name string) string {
	b, err := r.read(name)
	if err != nil {
		return ""
	}
	line, _, _ := strings.Cut(string(b), "\n")
	return strings.TrimSpace(line)
}

var (
	goToolchainRe = regexp.MustCompile(`(?m)^toolchain\s+go(\S+)\s*$`)
	goLineRe      = regexp.MustCompile(`(?m)^go\s+(\S+)\s*$`)
)

func (r reader) goVersion() (string, error) {
	b, err := r.read("go.mod")
	if err != nil {
		return "", err
	}
	v := ""
	if m := goToolchainRe.FindSubmatch(b); m != nil {
		v = string(m[1])
	} else if m := goLineRe.FindSubmatch(b); m != nil {
		v = string(m[1])
	}
	if !versionRe.MatchString(v) {
		return "", fmt.Errorf("go.mod's Go version %q isn't a version", v)
	}
	return v, nil
}
