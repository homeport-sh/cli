package buildplan

import (
	_ "embed"
	"encoding/base64"
	"errors"
	"fmt"
	"path"
	"strings"

	"github.com/homeport-sh/cli/supervise"
	"go.yaml.in/yaml/v3"
)

// A backend beside a renderer (design/backend-beside.md in homeport cloud):
// an app whose pages an rsc-kit renderer serves, with a backend of another
// language beside it - a Laravel, PHP, Go, Rust or Python app with
// @rsc-kit/core in its package.json, built with Bun. It's one app: the
// backend's own plan (its build, its release command, its processes, the
// web's args), then the renderer's build, put in the bundle beside it.
//
// The web runs through homeport's supervisor (homeport supervise, which the
// builder puts in the bundle as SuperviseFile, Plan.Supervise): the backend
// first, as the web would run alone but on supervise.BackendPort, listening
// on every address; then the renderer on the app's port, its RSC_BACKEND
// the backend. The host sends the backend's paths (Plan.Backend: the
// bundle's supervise.BackendPathsFile, a path a line) straight to the
// backend, everything else to the renderer: rsc-kit's build names them
// (its marker's backendPaths), and homeport.yaml's backend.paths or the
// build settings' are used in their place.

// SuperviseFile is where the builder puts homeport's supervisor in a bundle
// whose plan says Supervise: the CLI that made the plan, run as
// `.homeport/supervise supervise`.
const SuperviseFile = ".homeport/supervise"

// how the host matches a backend path (pathrule in homeport's engine, the
// same rules): at most maxBackendPaths, each at most maxBackendPath bytes
const (
	maxBackendPaths = 20
	maxBackendPath  = 128
)

// the host's own paths: never a backend's
var reservedPaths = []string{"/_edge", "/.well-known", "/__homeport"}

// rsc-kit's host-call endpoint, unless the app moves it (RSC_HOST_CALL_PATH)
const hostCallPath = "/__rsc/host-call"

// CheckBackendPaths says why paths can't be an app's backend paths, or nil:
// each from the root, a segment at a time - letters, digits and . _ ~ -,
// never . or .., no trailing / - none of the host's own, at most 20, each
// once. The host matches a path itself and anything under it.
func CheckBackendPaths(paths []string) error {
	if len(paths) > maxBackendPaths {
		return fmt.Errorf("backend paths: at most %d", maxBackendPaths)
	}
	seen := make(map[string]bool, len(paths))
	for _, p := range paths {
		if err := checkBackendPath(p); err != nil {
			return err
		}
		if seen[p] {
			return fmt.Errorf("backend path %q: named twice", p)
		}
		seen[p] = true
	}
	return nil
}

func checkBackendPath(p string) error {
	if p == "" || p[0] != '/' {
		return fmt.Errorf("backend path %q: starts with /", p)
	}
	if p == "/" {
		return errors.New("backend path /: every path - that's a backend app, not one beside a renderer")
	}
	if len(p) > maxBackendPath {
		return fmt.Errorf("backend path %.40q...: at most %d bytes", p, maxBackendPath)
	}
	for _, seg := range strings.Split(p[1:], "/") {
		if seg == "" {
			return fmt.Errorf("backend path %q: no empty segment or trailing /", p)
		}
		if seg == "." || seg == ".." {
			return fmt.Errorf("backend path %q: no . or .. segment", p)
		}
		if strings.Trim(seg, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789._~-") != "" {
			return fmt.Errorf("backend path %q: letters, digits and . _ ~ - only", p)
		}
	}
	for _, r := range reservedPaths {
		if under(p, r) {
			return fmt.Errorf("backend path %q: %s is the host's", p, r)
		}
	}
	return nil
}

// under: path is rule, or under it on a segment boundary
func under(path, rule string) bool {
	return strings.HasPrefix(path, rule) && (len(path) == len(rule) || path[len(rule)] == '/')
}

// checkHostCall: none of paths holds rsc-kit's host-call endpoint, which
// only the renderer calls - the host would send browsers to it.
func checkHostCall(paths []string) error {
	for _, p := range paths {
		if under(hostCallPath, p) {
			return fmt.Errorf("backend path %q is rsc-kit's host-call endpoint (%s) or holds it: only the renderer calls it", p, hostCallPath)
		}
	}
	return nil
}

// backendConfig is homeport.yaml's backend: its paths alone.
type backendConfig struct {
	Paths []string `yaml:"paths"`
}

// UnmarshalYAML: backend: {paths: [...]}, nothing else, at least one path.
func (b *backendConfig) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.MappingNode {
		return errors.New("backend: paths: [/api, ...]")
	}
	for i := 0; i < len(n.Content); i += 2 {
		if k := n.Content[i].Value; k != "paths" {
			return fmt.Errorf("backend: %q: paths is all it says", k)
		}
	}
	type plain backendConfig
	if err := n.Decode((*plain)(b)); err != nil {
		return err
	}
	if len(b.Paths) == 0 {
		return errors.New("backend: paths: name one or more - with none, leave backend out, and rsc-kit's build names them")
	}
	if err := CheckBackendPaths(b.Paths); err != nil {
		return err
	}
	return checkHostCall(b.Paths)
}

// rscKitPair: the app's folder has a backend and an rsc-kit renderer, built
// with Bun - from its files alone (Files), as a control plane reads them.
func (r reader) rscKitPair() bool {
	return r.deps()["@rsc-kit/core"] && (r.exists("bun.lock") || r.exists("bun.lockb")) &&
		(r.exists("composer.json") || r.exists("go.mod") || r.rustApp() || r.python())
}

// pairs: whether detection makes the app a pair - nothing said what its
// build makes, or that it's a site.
func (r reader) pairs(cfg fileConfig, s Settings) bool {
	return r.rscKitPair() && cfg.Build.Image == "" && cfg.Build.Command == "" && cfg.Build.Artifact == "" && cfg.Static == "" &&
		s.Command == "" && s.Kind == "" && s.Output == ""
}

//go:embed embed/pair.mjs
var pairMJS string

// pairMemoryNote: the renderer runs in the app's memory.
const pairMemoryNote = "rsc-kit's renderer runs in the app's memory, beside %s: size the app for both"

// pair makes the backend's plan p one with rsc-kit's renderer beside it:
// the renderer built after the backend's own build, its marker read for the
// backend's paths (given: homeport.yaml's or the settings', in their place),
// and it and the supervisor put in the bundle.
func (r reader) pair(p *Plan, given []string) error {
	if !r.hasScript("build") {
		return errors.New("rsc-kit beside a backend: package.json has no build script - the renderer's build is its build script (vite build)")
	}
	pkg := r.pkg()
	if err := r.bunVersion(pkg); err != nil {
		return err
	}
	backend := p.Framework
	if r.requires("laravel/framework") {
		backend = "Laravel"
	}
	b := BundleDir
	install := "bun install --frozen-lockfile"
	switch p.Toolchain {
	case "php", "python":
		// its front-end build is the renderer's (frontEnd): PHP's image has
		// Bun, Python's fetches it
		if p.Toolchain == "python" {
			p.setup = join(p.setup, fetchBun)
		}
	case "go":
		// a binary, alone: in a bundle, as its bin
		p.setup = fetchBun
		p.Install = join(fetchBun, install)
		p.build = join(p.Command, "bun run build")
		p.Kind, p.Artifact, p.StaticFallback = Bundle, b, false
		p.assemble = "rm -rf " + b + " && mkdir -p " + b + "/.homeport && mv server " + b + "/bin"
	case "rust":
		p.setup = join(p.setup, fetchBun)
		p.Install = join(p.setup, install)
		p.build = join(p.build, "bun run build")
	default:
		return fmt.Errorf("rsc-kit beside a %s app: not one homeport runs beside it", p.Toolchain)
	}
	renderer, err := r.renderer(b)
	if err != nil {
		return err
	}
	steps := join("mkdir -p "+b+"/.homeport",
		"printf '%s' '"+base64.StdEncoding.EncodeToString([]byte(pairMJS))+"' | base64 -d > .homeport-pair.mjs",
		strings.Join(append([]string{"bun .homeport-pair.mjs", b + "/" + supervise.BackendPathsFile, backend}, given...), " "),
		"rm -f .homeport-pair.mjs",
		renderer,
		superviseWrap(b))
	if p.Toolchain == "php" {
		p.Command = join(p.Command, steps)
	} else {
		p.assemble = join(p.assemble, steps)
		p.Command = join(p.build, p.assemble)
	}
	p.Framework = backend + " · rsc-kit"
	p.Supervise, p.Backend = true, true
	// what the renderer embeds, as its build left it
	p.Assets = append(p.Assets, PlanAsset{From: ".output/public/assets", At: "/assets"})
	p.Warnings = append(p.Warnings, fmt.Sprintf(pairMemoryNote, backend))
	return nil
}

// renderer puts rsc-kit's server in the bundle b, for the sandbox's Linux
// (glibc), and says how it runs (supervise.RendererFile): the binary its
// single-binary step (compile:binary, else compile) makes with bun build
// --compile, run again for this build's arch; else its server folder, on
// the pinned Bun. Sized to the app's memory, as Inertia's renderer is.
func (r reader) renderer(b string) (string, error) {
	cmd := r.compileStep("compile:binary")
	if cmd == "" {
		cmd = r.compileStep("compile")
	}
	var steps []string
	line := "BUN_JSC_forceRAMSize=%heapbytes% "
	if cmd != "" {
		again, musl := glibcCompile(cmd)
		if musl != "" {
			return "", fmt.Errorf("rsc-kit's single-binary step compiles for %s, which the sandbox can't run: compile for bun-linux-x64 or bun-linux-arm64 (glibc), or drop --target", musl)
		}
		out := compiledName(cmd)
		if !relPath(out) {
			return "", fmt.Errorf("rsc-kit's single-binary step compiles to %q, which isn't a file in the app's folder", out)
		}
		out = clean(out)
		steps = append(glibcBunSteps(), again,
			`{ [ -f `+out+` ] || { echo 'homeport: the single-binary step made no `+out+`' >&2; exit 1; }; }`,
			"mkdir -p "+b+"/.homeport/rsc-kit", "cp "+out+" "+b+"/.homeport/rsc-kit/server", "chmod 755 "+b+"/.homeport/rsc-kit/server")
		line += ".homeport/rsc-kit/server"
	} else {
		steps = append([]string{`{ [ -f .output/server/index.mjs ] || { echo 'homeport: the build made no .output/server/index.mjs' >&2; exit 1; }; }`},
			glibcBunSteps()...)
		steps = append(steps,
			"mkdir -p "+b+"/.output && cp -RL .output/server .output/public "+b+"/.output/",
			"cp "+glibcBun+" "+b+"/.homeport/bun", "chmod 755 "+b+"/.homeport/bun")
		line += ".homeport/bun .output/server/index.mjs"
	}
	steps = append(steps, "printf '%s\\n' '"+line+"' > "+b+"/"+supervise.RendererFile)
	return join(steps...), nil
}

// glibcBunSteps put the pinned glibc Bun at glibcBun, checked, and this
// build's arch as Bun names it in $t: what a compile for the sandbox runs on
// (the FrankenPHP image's Bun is musl's).
func glibcBunSteps() []string {
	return []string{
		"case $(uname -m) in x86_64) a=x64 t=x64 ;; aarch64) a=aarch64 t=arm64 ;; *) echo \"homeport: no Bun for $(uname -m)\" >&2; exit 1 ;; esac",
		"mkdir -p " + path.Dir(glibcBun),
		"curl -fsSL https://registry.npmjs.org/@oven/bun-linux-$a/-/bun-linux-$a-" + BunVersion + ".tgz | tar -xzO package/bin/bun > " + glibcBun,
		"chmod 755 " + glibcBun, checkSum(glibcBun, bunSum, "Bun"),
	}
}

// glibcCompile is a bun build --compile command run again for this build's
// arch on the glibc Bun (glibcBunSteps' $t), or the musl target it names,
// which the sandbox can't run.
func glibcCompile(cmd string) (again, musl string) {
	if t := targetRe.FindString(cmd); strings.Contains(t, "musl") {
		return "", strings.TrimLeft(t[len("--target"):], "= ")
	}
	return strings.Join(strings.Fields(targetRe.ReplaceAllString(cmd, "")), " ") +
		" --target=bun-linux-$t --compile-executable-path=" + glibcBun, ""
}

// superviseWrap writes the bundle b's .homeport/wrap: the web run through
// the supervisor, behind Laravel's config runner when the bundle has one
// (homeport's FrankenPHP base writes it, configcache.go).
func superviseWrap(b string) string {
	write := func(args string) string { return "printf '%s\\n' '" + args + "' > " + b + "/" + wrapFile }
	run := SuperviseFile + " supervise"
	return "if [ -f " + b + "/" + startPHPF + " ]; then " + write(run+" ./bin php-cli "+startPHPF) + "; else " + write(run) + "; fi"
}
