package buildplan

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
)

// Laravel's own servers, beside the web.
//
// Inertia's server-side rendering: a Laravel app that requires
// inertiajs/inertia-laravel and has an SSR build (package.json's build:ssr,
// or an ssr entry for laravel-vite-plugin in the Vite config) builds its SSR
// bundle too, made one file with the packages it imports (no node_modules
// ships), and runs Inertia's SSR server in the web's own sandbox: PHP reaches
// it at Inertia's default, 127.0.0.1:13714. Its runtime - Node or Bun, the
// pinned official binary, checked - ships in the bundle, chosen as a
// JavaScript app's is. homeportd runs the web through .homeport/wrap when the
// bundle has one: `<runtime> .homeport/ssr.mjs ./bin <the web's args>`. The
// wrapper starts the SSR server, then the web as its child; a stop reaches the
// web, and the web's exit is its exit. So the SSR server starts, sleeps, wakes
// and stops with the web, one beside each copy.
//
// Laravel Reverb (laravel/reverb) runs as a process of its own, reverb, on
// the port homeportd gives it: homeportd sends the app's WebSocket paths
// (/app, /apps) to it.

// ReverbRun is how the reverb process runs: Reverb's server on the process's
// own port, on every address of its sandbox (homeportd substitutes $HOST and
// $PORT).
const ReverbRun = "php-cli artisan reverb:start --host=$HOST --port=$PORT"

// SSRInertia is Plan.SSR for Inertia's server-side rendering.
const SSRInertia = "inertia"

// the bundle's wrapper and the file homeportd reads it from
const (
	wrapFile = ".homeport/wrap"
	ssrFile  = ".homeport/ssr.mjs"
)

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

// ssrAssemble puts Inertia's SSR server in the bundle b: the SSR bundle as
// one file (bootstrap/ssr/ssr.mjs, where Inertia finds it: the only file
// there), the runtime,
// checked, the wrapper, and .homeport/wrap, which homeportd runs the web
// through.
func ssrAssemble(b, rt, version string) string {
	bin := ".homeport/" + rt
	steps := []string{
		// where Inertia looks for it, in its order
		`{ s=; for f in bootstrap/ssr/ssr.js bootstrap/ssr/app.js bootstrap/ssr/ssr.mjs bootstrap/ssr/app.mjs; do if [ -f "$f" ]; then s=$f; break; fi; done; ` +
			`[ -n "$s" ] || { echo 'homeport: the SSR build made no bootstrap/ssr/ssr.js, app.js, ssr.mjs or app.mjs' >&2; exit 1; }; }`,
		// its packages in it: the bundle ships no node_modules
		"rm -rf " + b + "/bootstrap/ssr && mkdir -p " + b + "/bootstrap/ssr " + b + "/.homeport",
		`bun build "./$s" --target=node --format=esm --outfile=` + b + "/bootstrap/ssr/ssr.mjs",
	}
	if rt == "bun" {
		steps = append(steps,
			"case $(uname -m) in x86_64) a=x64 ;; aarch64) a=aarch64 ;; *) echo \"homeport: no Bun for $(uname -m)\" >&2; exit 1 ;; esac",
			"curl -fsSL https://registry.npmjs.org/@oven/bun-linux-$a/-/bun-linux-$a-"+BunVersion+".tgz | tar -xzO package/bin/bun > "+b+"/"+bin)
		steps = append(steps, "chmod 755 "+b+"/"+bin, checkSum(b+"/"+bin, bunSum, "Bun"))
	} else {
		steps = append(steps,
			"case $(uname -m) in x86_64) a=x64 ;; aarch64) a=arm64 ;; *) echo \"homeport: no Node for $(uname -m)\" >&2; exit 1 ;; esac",
			"curl -fsSL https://nodejs.org/dist/v"+version+"/node-v"+version+"-linux-$a.tar.gz | tar -xzO node-v"+version+"-linux-$a/bin/node > "+b+"/"+bin)
		steps = append(steps, "chmod 755 "+b+"/"+bin, checkSum(b+"/"+bin, nodeSum(version), "Node"))
	}
	steps = append(steps,
		"printf '%s' '"+ssrWrapper+"' > "+b+"/"+ssrFile,
		"printf '%s\\n' '"+bin+" "+ssrFile+"' > "+b+"/"+wrapFile)
	return join(steps...)
}

// ssrWrapper is .homeport/ssr.mjs: `<runtime> .homeport/ssr.mjs ./bin <args>`.
// Inertia's SSR server starts in it (an SSR bundle that fails to load is
// said, and the web serves on: pages render in the browser), then the web,
// as its child; a stop is passed on to the web, and the web's exit is the
// wrapper's. One line, no single quotes: it's written in some.
var ssrWrapper = `import { spawn } from "node:child_process";import { existsSync } from "node:fs";import { constants } from "node:os";` +
	`const [bin, ...args] = process.argv.slice(2);let web;` +
	`for (const s of ["SIGTERM", "SIGINT", "SIGHUP"]) process.on(s, () => (web ? web.kill(s) : process.exit(0)));` +
	`const ssr = ["../bootstrap/ssr/ssr.mjs", "../bootstrap/ssr/ssr.js"].map((f) => new URL(f, import.meta.url)).find((u) => existsSync(u));` +
	`try { if (!ssr) throw new Error("no bootstrap/ssr/ssr.mjs"); await import(ssr.href); } ` +
	`catch (e) { console.error("homeport: Inertia SSR server did not start, so pages render in the browser:", e); }` +
	`web = spawn(bin, args, { stdio: "inherit" });` +
	`web.on("error", (e) => { console.error("homeport: the web did not start:", e); process.exit(1); });` +
	`web.on("exit", (code, sig) => process.exit(code ?? 128 + (constants.signals[sig] ?? 0)));`

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
