package buildplan

import (
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"path"
	"regexp"
	"slices"
	"strings"
)

// Laravel's own servers, beside the web.
//
// Inertia's server-side rendering: a Laravel app that requires
// inertiajs/inertia-laravel and has an SSR build (package.json's build:ssr,
// or an ssr entry in the Vite config) builds its SSR bundle too, and runs
// Inertia's SSR renderer in the web's own sandbox: PHP reaches it at
// Inertia's default, 127.0.0.1:13714, as it would locally. The renderer is
// one of three things, by the JavaScript app's rule:
//
//   - the binary the project's own SSR build compiles (bun build --compile):
//     it runs as it is, and no runtime ships - compiled for the sandbox's
//     Linux (glibc), as the same command with a --target, when the command
//     names none (the build image is Alpine's musl);
//   - else the SSR bundle, made one file with the packages it imports (no
//     node_modules ships), on Node or Bun, the pinned official binary,
//     checked, chosen as a JavaScript app's runtime is.
//
// homeportd runs the web through .homeport/wrap when the bundle has one:
// `./bin php-cli .homeport/beside.php ./bin <the web's args>`, a supervisor
// in the app's own PHP, which starts each command in .homeport/beside, then
// the web; starts one that exits again; passes a stop to the web; and exits
// as the web does. So the renderer starts, sleeps, wakes and stops with the
// web, one beside each copy.
//
// Laravel Reverb (laravel/reverb) runs as a process of its own, reverb, on
// the port homeportd gives it: homeportd sends the app's WebSocket and
// signed API requests (/app/…, /apps/…) to it.

// ReverbRun is how the reverb process runs: Reverb's server on the process's
// own port, on every address of its sandbox (homeportd substitutes $HOST and
// $PORT).
const ReverbRun = "php-cli artisan reverb:start --host=$HOST --port=$PORT"

// SSRInertia is Plan.SSR for Inertia's server-side rendering.
const SSRInertia = "inertia"

// what the bundle says runs beside the web, and how homeportd runs it
const (
	wrapFile   = ".homeport/wrap"
	besideFile = ".homeport/beside"
	besidePHPF = ".homeport/beside.php"
	wrapArgs   = "bin php-cli " + besidePHPF
)

//go:embed embed/beside.php
var besidePHP string

// an SSR entry for laravel-vite-plugin in the Vite config: ssr: '…' or [ … ]
var viteSSRRe = regexp.MustCompile(`\bssr\s*:\s*['"\x60\[]`)

// inertiaSSR says whether the app renders Inertia pages on the server: it
// requires inertia-laravel and builds an SSR bundle.
func (r reader) inertiaSSR() bool {
	if !r.requires("inertiajs/inertia-laravel") || !r.exists("package.json") {
		return false
	}
	if r.hasScript("build:ssr") {
		return true
	}
	for _, f := range svelteConfigs[1:] { // the Vite configs
		if b, err := r.read(f); err == nil && viteSSRRe.Match(b) {
			return true
		}
	}
	return false
}

// phpAssets is what a PHP app's install runs after composer: its front-end
// build, and its SSR build when it renders on the server.
func (r reader) phpAssets(ssr bool) string {
	if !r.exists("package.json") {
		return ""
	}
	build := ""
	if r.hasScript("build") {
		build = "bun run build"
	}
	if ssr {
		if r.hasScript("build:ssr") {
			build = "bun run build:ssr" // the client's build and the SSR one
		} else {
			build = join(build, "bun run vite build --ssr")
		}
	}
	if build == "" {
		return ""
	}
	install := "bun install"
	if r.exists("bun.lock") || r.exists("bun.lockb") {
		install += " --frozen-lockfile"
	}
	return install + " && " + build
}

// startSSR matches a composer script that starts Inertia's SSR server, and
// the runtime it names
var startSSRRe = regexp.MustCompile(`inertia:start-ssr\b[^"']*--runtime[= ](bun|node)\b`)

// ssrRuntime is the SSR server's runtime and why, by a JavaScript app's
// rules: the build settings or homeport.yaml, engines, a version file, what
// the project's own SSR command runs; then, for the SSR server alone, a Bun
// lockfile; else Node. Its version is the project's, from the pinned ones.
func (r reader) ssrRuntime(cfg fileConfig, s Settings) (rt, version, why string, err error) {
	set, from := s.Runtime, "the build settings"
	if set == "" && cfg.Runtime != "" {
		set, from = cfg.Runtime, ConfigFile
	}
	pkg := r.pkg()
	// what the project runs its SSR server with: a package.json script that
	// runs the SSR bundle, else a composer script's inertia:start-ssr
	var st start
	stErr := errors.New("no SSR command")
	stFrom := ""
	for _, name := range slices.Sorted(maps.Keys(pkg.Scripts)) {
		if strings.Contains(pkg.Scripts[name], "bootstrap/ssr/") {
			if x, e := parseStart(pkg.Scripts, name, 0); e == nil && x.runtime != "" {
				st, stErr, stFrom = x, nil, name+" script"
				break
			}
		}
	}
	if stErr != nil {
		if cmd, rtc := r.composerStartSSR(); rtc != "" {
			st, stErr, stFrom = start{runtime: rtc, says: cmd}, nil, "composer script"
		}
	}
	rt, why = r.runtimeFor(pkg, nil, st, stErr, stFrom, set, from)
	if set == "" && why == defaultRuntimeWhy && (r.exists("bun.lock") || r.exists("bun.lockb")) {
		lock := "bun.lock"
		if !r.exists(lock) {
			lock = "bun.lockb"
		}
		rt, why = "bun", "nothing else says, and its lockfile is Bun's ("+lock+")"
	}
	if rt == "bun" {
		if err := r.bunVersion(pkg); err != nil {
			return "", "", "", err
		}
		return rt, BunVersion, why, nil
	}
	v, vwhy, err := r.nodeVersion(pkg)
	if err != nil {
		return "", "", "", err
	}
	if vwhy != "" {
		why += "; " + vwhy
	}
	return rt, v, why, nil
}

// composerStartSSR: a composer script that runs inertia:start-ssr with a
// runtime, and that runtime
func (r reader) composerStartSSR() (string, string) {
	b, err := r.read("composer.json")
	if err != nil {
		return "", ""
	}
	var c struct {
		Scripts map[string]json.RawMessage `json:"scripts"`
	}
	if json.Unmarshal(b, &c) != nil {
		return "", ""
	}
	for _, name := range slices.Sorted(maps.Keys(c.Scripts)) {
		var lines []string
		var one string
		if json.Unmarshal(c.Scripts[name], &one) == nil {
			lines = []string{one}
		} else {
			_ = json.Unmarshal(c.Scripts[name], &lines)
		}
		for _, l := range lines {
			if m := startSSRRe.FindStringSubmatch(l); m != nil {
				return "inertia:start-ssr --runtime=" + m[1], m[1]
			}
		}
	}
	return "", ""
}

// ssr is how an app's SSR renderer runs: compiled (the binary at out, made
// by compile, which is run again with target when it's set), or the bundle
// on rt at version.
type ssr struct {
	rt, version, why string
	compile, target  string
	out              string
}

// ssrCompiled: the project's own SSR build compiles the SSR bundle - a
// bun build --compile in build:ssr, or in a script it runs - and the binary
// it makes.
func (r reader) ssrCompiled() (cmd, out string, err error) {
	scripts := r.pkg().Scripts
	var find func(name string, depth int) string
	find = func(name string, depth int) string {
		if depth > 4 {
			return ""
		}
		for _, seg := range strings.Split(scripts[name], "&&") {
			seg = strings.TrimSpace(seg)
			f := strings.Fields(seg)
			switch {
			case len(f) >= 2 && f[0] == "bun" && f[1] == "build" && slices.Contains(f, "--compile"):
				return seg
			case len(f) == 3 && f[1] == "run" && slices.Contains([]string{"npm", "bun", "pnpm", "yarn"}, f[0]) && scripts[f[2]] != "":
				if c := find(f[2], depth+1); c != "" {
					return c
				}
			case len(f) == 2 && (f[0] == "pnpm" || f[0] == "yarn") && scripts[f[1]] != "":
				if c := find(f[1], depth+1); c != "" {
					return c
				}
			}
		}
		return ""
	}
	cmd = find("build:ssr", 0)
	if cmd == "" {
		return "", "", nil
	}
	out = compiledName(cmd)
	if !relPath(out) {
		return "", "", fmt.Errorf("its SSR build compiles to %q, which isn't a file in the app's folder", out)
	}
	return cmd, clean(out), nil
}

// ssrFor decides how the SSR renderer runs: the project's compiled binary,
// else the bundle on the runtime the project's signals choose.
func (r reader) ssrFor(cfg fileConfig, s Settings) (ssr, error) {
	cmd, out, err := r.ssrCompiled()
	if err != nil {
		return ssr{}, err
	}
	if cmd != "" {
		if err := r.bunVersion(r.pkg()); err != nil {
			return ssr{}, err
		}
		x := ssr{rt: "bun", version: BunVersion, out: out, compile: cmd,
			why: "its SSR build compiles the renderer (" + cmd + "): that binary runs, and no runtime ships"}
		switch t := regexp.MustCompile(`--target[= ](\S+)`).FindStringSubmatch(cmd); {
		case t == nil:
			x.target = " --target=bun-linux-$a" // the sandbox's glibc, not the image's musl
		case strings.Contains(t[1], "musl"):
			return ssr{}, fmt.Errorf("its SSR build compiles for %s, which the sandbox can't run: compile for bun-linux-x64 or bun-linux-arm64 (glibc), or drop --target", t[1])
		default:
			x.compile = "" // as it is: already for glibc
		}
		return x, nil
	}
	rt, v, why, err := r.ssrRuntime(cfg, s)
	return ssr{rt: rt, version: v, why: why}, err
}

// ssrAssemble puts Inertia's SSR renderer in the bundle b, beside the web:
// what runs (the compiled binary, or the SSR bundle as one file at
// bootstrap/ssr/ssr.mjs - where Inertia finds it, the only file there - and
// the runtime, checked), the supervisor and .homeport/wrap.
func ssrAssemble(b string, x ssr) string {
	var steps []string
	beside := x.out
	if x.out != "" {
		if x.compile != "" {
			steps = append(steps,
				"case $(uname -m) in x86_64) a=x64 ;; aarch64) a=arm64 ;; *) echo \"homeport: no Bun for $(uname -m)\" >&2; exit 1 ;; esac",
				x.compile+x.target)
		}
		steps = append(steps,
			`{ [ -f `+x.out+` ] || { echo 'homeport: the SSR build made no `+x.out+`' >&2; exit 1; }; }`,
			"mkdir -p "+path.Dir(b+"/"+x.out)+" "+b+"/.homeport",
			"cp "+x.out+" "+b+"/"+x.out, "chmod 755 "+b+"/"+x.out)
	} else {
		bin := ".homeport/" + x.rt
		beside = bin + " bootstrap/ssr/ssr.mjs"
		steps = append(steps,
			// where Inertia looks for it, in its order
			`{ s=; for f in bootstrap/ssr/ssr.js bootstrap/ssr/app.js bootstrap/ssr/ssr.mjs bootstrap/ssr/app.mjs; do if [ -f "$f" ]; then s=$f; break; fi; done; `+
				`[ -n "$s" ] || { echo 'homeport: the SSR build made no bootstrap/ssr/ssr.js, app.js, ssr.mjs or app.mjs' >&2; exit 1; }; }`,
			// its packages in it: the bundle ships no node_modules
			"rm -rf "+b+"/bootstrap/ssr && mkdir -p "+b+"/bootstrap/ssr "+b+"/.homeport",
			`bun build "./$s" --target=node --format=esm --outfile=`+b+"/bootstrap/ssr/ssr.mjs")
		if x.rt == "bun" {
			steps = append(steps,
				"case $(uname -m) in x86_64) a=x64 ;; aarch64) a=aarch64 ;; *) echo \"homeport: no Bun for $(uname -m)\" >&2; exit 1 ;; esac",
				"curl -fsSL https://registry.npmjs.org/@oven/bun-linux-$a/-/bun-linux-$a-"+BunVersion+".tgz | tar -xzO package/bin/bun > "+b+"/"+bin,
				"chmod 755 "+b+"/"+bin, checkSum(b+"/"+bin, bunSum, "Bun"))
		} else {
			steps = append(steps,
				"case $(uname -m) in x86_64) a=x64 ;; aarch64) a=arm64 ;; *) echo \"homeport: no Node for $(uname -m)\" >&2; exit 1 ;; esac",
				"curl -fsSL https://nodejs.org/dist/v"+x.version+"/node-v"+x.version+"-linux-$a.tar.gz | tar -xzO node-v"+x.version+"-linux-$a/bin/node > "+b+"/"+bin,
				"chmod 755 "+b+"/"+bin, checkSum(b+"/"+bin, nodeSum(x.version), "Node"))
		}
	}
	steps = append(steps,
		"printf '%s' '"+base64.StdEncoding.EncodeToString([]byte(besidePHP))+"' | base64 -d > "+b+"/"+besidePHPF,
		"printf '%s\\n' '"+beside+"' > "+b+"/"+besideFile,
		"printf '%s\\n' '"+wrapArgs+"' > "+b+"/"+wrapFile)
	return join(steps...)
}

// reverbProcess adds Reverb's process to the plan, unless the app runs one of
// its own by that name.
func reverbProcess(p *Plan) error {
	if slices.ContainsFunc(p.Processes, func(x Process) bool { return x.Name == "reverb" }) {
		return nil
	}
	if len(p.Processes) >= MaxProcesses {
		return fmt.Errorf("Reverb runs as a process of its own, and the app has %d already: at most %d in all - drop one, or run Reverb as one of them (named reverb)",
			len(p.Processes), MaxProcesses)
	}
	p.Processes = slices.SortedFunc(slices.Values(append(slices.Clone(p.Processes), Process{Name: "reverb", Run: ReverbRun})),
		func(a, b Process) int { return strings.Compare(a.Name, b.Name) })
	return nil
}
