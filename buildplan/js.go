package buildplan

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"path"
	"regexp"
	"slices"
	"strings"
)

// A JavaScript app runs what its build produces: the framework's own
// production output (Next.js standalone, Nitro's .output, SvelteKit's
// build/, ...) or the app's files with its production dependencies, in a
// bundle whose bin is the runtime the project itself uses - Node or Bun, a
// pinned official binary - started as its start script starts it. A binary
// only when the project's own build compiles one (bun build --compile):
// compiling an app that doesn't can leave out what it loads at runtime.

// Node releases homeport builds with and ships, one per supported major:
// the official Docker image, pinned by digest, and the sha256 of its
// bin/node - the official linux tarball's (itself checked against
// nodejs.org's SHASUMS256.txt), which the image's is byte for byte. The
// build checks the binary it ships against it.
type nodeRelease struct {
	Version string
	Digest  string            // node:<version>-bookworm's index digest
	Sum     map[string]string // uname -m: sha256 of bin/node
}

var nodeReleases = []nodeRelease{
	{"22.23.3", "sha256:0e5f906573693feaa1e21057ebdcfdb5bd5021f050b2dc7c9deceb629c7da2a8", map[string]string{
		"x86_64":  "fde6a4bf8d0562f7751d1a2d6cb9b417c4cfe107bbcb0aa3e9a24e125e348f48",
		"aarch64": "d09e299258c24f7cdf6f5d5ec185e3a56512b27a697113735dac909f1cac7b8d"}},
	{"24.21.0", "sha256:3d27e5c11e5786e309ec3e03f93ae536eb36e6e5eb3714d5eb3300a36157add0", map[string]string{
		"x86_64":  "7fde7b8afa198da66257f42ee2001d874c7355631e6d1579a5fb5ef1f246df4c",
		"aarch64": "0f8949d1028f6d61506b2d5bc57e7e6fe893d7b1997509b7847294fc9c616584"}},
	{"26.10.0", "sha256:e6cfc3514df35d1cb534e83f9279a242ad6e578692ab7b56d73dadcc7c4354a0", map[string]string{
		"x86_64":  "ab9c8eecf9f82d6693cdc3accced17034065c8d96213b0aa76a7e803d20ae1da",
		"aarch64": "71b004f18a82f3ea8f26109798564a12e5e4c7989a4c35b93851830b5815dd03"}},
}

// DefaultNode is the Node a project that says none runs: the current LTS.
const DefaultNode = "24.21.0"

// nodeLTS are the LTS lines by codename (.nvmrc's lts/<name>).
var nodeLTS = map[string]string{"jod": "22", "krypton": "24"}

// Bun, pinned the same way: the official image by digest, and its binary's
// sha256 (the release's, by its SHASUMS256.txt; npm's @oven/bun-linux-*
// and the image carry the same bytes). A Bun version the project names
// must be this one's (bunVersion): no other is run.
const (
	BunVersion = "1.4.3"
	BunImage   = "oven/bun:1.4.3@sha256:ec06c3b6cea04192ae6770c434f668ca41d343ad19fa6472216c7b48be39c598"
)

var bunSum = map[string]string{
	"x86_64":  "7ce7d6b654eddeec20afddf2396317fba6dc19ab52788a27e894e1806bf8f24b",
	"aarch64": "029ece746ac9f970c76d8f48bdd1b1d0c52f3636b74d19f546f5e7c1165c0abe",
}

// CorepackVersion installs pnpm and Yarn where the image has no corepack
// (Node 25 stopped shipping it).
const CorepackVersion = "0.36.0"

// Where a bundle keeps what's homeport's: the boot (.homeport/boot.mjs)
// every start runs first, the start shim for a package's command, and the
// writable list. CacheDir is Node's compile cache, writable and kept with
// the release (a dot folder can't be: the sandbox's writable paths are
// plain names).
const (
	bootFile = ".homeport/boot.mjs"
	CacheDir = "homeport-cache"
)

// a preset: one framework's defaults
type preset struct {
	name string
	// what of the build ships: "standalone" (Next.js), "output" (Nitro's
	// .output) or "app" (the app's folder, production dependencies only)
	layout string
	entry  string // the file it starts, unless its start script says
	needs  string // a runtime it can only run on
	// only: it runs on needs whatever else says (why: the reason); else
	// needs is only when nothing else says
	only bool
	why  string
	// build: its build command, when it isn't the build script run as the
	// package manager runs it
	build string
	// the boot's env defaults: a value "$X" is X's value
	env map[string]string
}

// forwarded: the scheme from homeport's edge, which terminates TLS, for
// SvelteKit's adapters. The host is the request's Host, the one the edge
// routed on: X-Forwarded-Host is a visitor's to set (proxies that trust
// their peer keep it), so HOST_HEADER isn't.
var forwarded = map[string]string{"PROTOCOL_HEADER": "x-forwarded-proto"}

// presets, most specific first: a Nuxt app depends on h3, a NestJS one on
// express.
var presets = []struct {
	deps []string
	p    preset
}{
	{[]string{"next"}, preset{name: "Next.js", layout: "standalone", entry: "server.js"}},
	{[]string{"nuxt"}, preset{name: "Nuxt", layout: "output", entry: "server/index.mjs"}},
	// Nitro's output when it builds with Nitro's Vite plugin, else the app
	// (tanStackStart)
	{[]string{"@tanstack/react-start", "@tanstack/solid-start"}, preset{name: "TanStack Start", layout: "app"}},
	{[]string{"@react-router/dev", "@react-router/serve", "@react-router/node"}, preset{name: "React Router", layout: "app"}},
	{[]string{"@remix-run/dev", "@remix-run/serve", "@remix-run/node"}, preset{name: "Remix", layout: "app"}},
	// SvelteKit's Bun adapter: a Bun.serve server, built in Bun (svelteBun)
	{[]string{"@sveltejs/adapter-bun"}, preset{name: "SvelteKit", layout: "app", entry: "build/index.js", needs: "bun", only: true,
		why: "its adapter, adapter-bun, builds a Bun server", build: "bun run --bun build", env: forwarded}},
	// adapter-node reads the origin from the forwarded headers homeport's
	// proxy sets, when ORIGIN isn't
	{[]string{"@sveltejs/adapter-node"}, preset{name: "SvelteKit", layout: "app", entry: "build/index.js", env: forwarded}},
	{[]string{"@astrojs/node"}, preset{name: "Astro", layout: "app", entry: "dist/server/entry.mjs"}},
	{[]string{"@nestjs/core"}, preset{name: "NestJS", layout: "app", entry: "dist/main.js"}},
	{[]string{"elysia"}, preset{name: "Elysia", layout: "app", needs: "bun"}},
	{[]string{"hono"}, preset{name: "Hono", layout: "app"}},
	{[]string{"fastify"}, preset{name: "Fastify", layout: "app"}},
	{[]string{"express"}, preset{name: "Express", layout: "app"}},
	{[]string{"koa"}, preset{name: "Koa", layout: "app"}},
}

var (
	nextOutputRe = regexp.MustCompile(`output\s*:\s*['"](standalone|export)['"]`)
	astroMiddle  = regexp.MustCompile(`mode\s*:\s*['"]middleware['"]`)
	// an option in an adapter( call's text (adapterCall)
	adapterOutRe  = regexp.MustCompile(`\bout\s*:\s*['"]([^'"]+)['"]`)
	outfileRe     = regexp.MustCompile(`--outfile(?:=|\s+)(\S+)`)
	entryFileRe   = regexp.MustCompile(`^\S+\.(m|c)?(j|t)sx?$`)
	viteConfigs   = []string{"vite.config.js", "vite.config.ts", "vite.config.mjs", "vite.config.mts"}
	nuxtConfigs   = []string{"nuxt.config.ts", "nuxt.config.js", "nuxt.config.mjs", "nuxt.config.mts"}
	nitroConfigs  = []string{"nitro.config.ts", "nitro.config.js", "nitro.config.mjs", "nitro.config.mts"}
	svelteConfigs = append([]string{"svelte.config.js"}, viteConfigs...)
	nextConfigs   = []string{"next.config.js", "next.config.mjs", "next.config.ts", "next.config.cjs", "next.config.mts"}
)

// pkgJSON is what's read of package.json.
type pkgJSON struct {
	Scripts        map[string]string `json:"scripts"`
	Engines        map[string]any    `json:"engines"`
	PackageManager string            `json:"packageManager"`
	Main           string            `json:"main"`
	Module         string            `json:"module"`
}

func (r reader) pkg() pkgJSON {
	var p pkgJSON
	if b, err := r.read("package.json"); err == nil {
		_ = json.Unmarshal(b, &p)
	}
	return p
}

// lockfiles: each package manager's, in the order one is told apart
var lockfiles = []struct{ pm, file string }{
	{"bun", "bun.lock"}, {"bun", "bun.lockb"}, {"pnpm", "pnpm-lock.yaml"}, {"yarn", "yarn.lock"},
	{"npm", "package-lock.json"}, {"npm", "npm-shrinkwrap.json"},
}

// packageManagerOf is the project's package manager: its lockfile's. With
// lockfiles of more than one, package.json's packageManager picks among
// them, else the order homeport always had (bun, pnpm, Yarn, npm), with a
// warning; a packageManager naming a manager whose lockfile isn't there is
// refused - the install would be from nothing.
func (r reader) packageManagerOf(pkg pkgJSON) (name, warning string, err error) {
	var have []string
	for _, l := range lockfiles {
		if r.exists(l.file) && !slices.Contains(have, l.pm) {
			have = append(have, l.pm)
		}
	}
	if name, _, ok := strings.Cut(pkg.PackageManager, "@"); ok && slices.Contains([]string{"npm", "pnpm", "yarn", "bun"}, name) {
		if slices.Contains(have, name) {
			return name, "", nil
		}
		want := ""
		for _, l := range lockfiles {
			if l.pm == name && want == "" {
				want = l.file
			}
		}
		return "", "", fmt.Errorf("package.json's packageManager is %s, but there's no %s: commit it (or correct packageManager), so the build installs what you tested", pkg.PackageManager, want)
	}
	switch len(have) {
	case 0:
		return "", "", errors.New("no lockfile")
	case 1:
		return have[0], "", nil
	}
	file := ""
	for _, l := range lockfiles {
		if l.pm == have[0] && r.exists(l.file) && file == "" {
			file = l.file
		}
	}
	return have[0], fmt.Sprintf("lockfiles of %s: installing with %s (%s) - delete the others, or name one in package.json's packageManager",
		strings.Join(have, " and "), have[0], file), nil
}

func (r reader) hasLockfile() bool {
	return slices.ContainsFunc(lockfiles, func(l struct{ pm, file string }) bool { return r.exists(l.file) })
}

// yarnBerry: Yarn 2+, by packageManager or the lockfile's own header.
func (r reader) yarnBerry(pkg pkgJSON) bool {
	if v, ok := strings.CutPrefix(pkg.PackageManager, "yarn@"); ok {
		return !strings.HasPrefix(v, "1.")
	}
	b, _ := r.read("yarn.lock")
	return strings.Contains(string(b), "__metadata:")
}

// pm is how a package manager installs, prunes and runs scripts.
type pm struct {
	name, install, prune, run string
	setup                     string // what the image needs first (corepack)
}

func (r reader) pmFor(name string, pkg pkgJSON, pinnedBun bool) pm {
	corepack := "mkdir -p /tmp/homeport/bin && export PATH=/tmp/homeport/bin:$PATH COREPACK_ENABLE_DOWNLOAD_PROMPT=0 && " +
		"{ command -v corepack >/dev/null || npm install -g --prefix /tmp/homeport --no-fund --no-audit corepack@" + CorepackVersion + " >/dev/null; } && " +
		"corepack enable --install-directory /tmp/homeport/bin " + name
	switch name {
	case "pnpm":
		// hoisted: a flat node_modules, not pnpm's links
		return pm{name: name, setup: corepack, install: "pnpm install --frozen-lockfile --config.node-linker=hoisted",
			prune: "pnpm prune --prod --config.node-linker=hoisted", run: "pnpm run"}
	case "yarn":
		if r.yarnBerry(pkg) {
			// a node_modules, not Plug'n'Play
			return pm{name: name, setup: corepack + " && export YARN_NODE_LINKER=node-modules",
				install: "YARN_NODE_LINKER=node-modules yarn install --immutable", prune: "yarn workspaces focus --all --production", run: "yarn run"}
		}
		return pm{name: name, setup: corepack, install: "yarn install --frozen-lockfile",
			prune: "yarn install --frozen-lockfile --production --ignore-scripts", run: "yarn run"}
	case "bun":
		// hoisted where it's ours to say (a Bun older than --linker
		// installs hoisted anyway, outside workspaces)
		linker := ""
		if pinnedBun {
			linker = " --linker=hoisted"
		}
		// it reinstalls node_modules: what the build generated into it
		// (Prisma's client in node_modules/.prisma) is kept aside, and back
		keep := `{ mkdir -p /tmp/homeport/keep && for d in node_modules/.[!.]*; do if [ -e "$d" ] && [ "$d" != node_modules/.bin ] && [ "$d" != node_modules/.cache ]; then mv "$d" /tmp/homeport/keep/; fi; done; }`
		back := `{ for d in /tmp/homeport/keep/.[!.]*; do if [ -e "$d" ]; then rm -rf "node_modules/${d##*/}" && mv "$d" node_modules/; fi; done; }`
		return pm{name: name, install: "bun install --frozen-lockfile" + linker,
			prune: keep + " && rm -rf node_modules && bun install --frozen-lockfile --production" + linker + " && " + back, run: "bun run"}
	}
	return pm{name: "npm", install: "npm ci", prune: "npm prune --omit=dev", run: "npm run"}
}

// start is what a start script runs, read without a shell.
type start struct {
	runtime string   // bun or node; "" when it doesn't say
	file    string   // a file it runs
	bin     string   // or a package's command
	args    []string // the runtime's flags before file, then file's args after
	flags   []string
	says    string   // the command, for the reason
	vars    []string // the variables it sets first (errEnv)
}

var (
	errShell = errors.New("needs a shell")
	// it sets a variable before its command
	errEnv = errors.New("sets a variable")
)

// parseStart reads a script (package.json's scripts, by name) as the
// command it runs, following npm run / bun run / pnpm / yarn to the script
// they name.
func parseStart(scripts map[string]string, name string, depth int) (start, error) {
	cmd := strings.TrimSpace(scripts[name])
	if cmd == "" || depth > 4 {
		return start{}, errors.New("no script " + name)
	}
	// plain enough to run without a shell: what a start command may be
	if CheckRun(cmd) != nil {
		return start{says: cmd}, errShell
	}
	f := strings.Fields(cmd)
	// variables first (A=b node x, cross-env A=b node x): an app's
	// variables are set as such, not dropped - but NODE_ENV=production,
	// PORT and HOST are homeport's to set anyway
	var vars []string
	for len(f) > 0 && (strings.Contains(f[0], "=") || f[0] == "cross-env") {
		k, _, _ := strings.Cut(f[0], "=")
		if f[0] != "cross-env" && f[0] != "NODE_ENV=production" && k != "PORT" && k != "HOST" {
			vars = append(vars, k)
		}
		f = f[1:]
	}
	if len(f) == 0 {
		return start{says: cmd}, errShell
	}
	if len(vars) > 0 {
		// what it runs, read as the rest would be: the runtime it says
		// still counts; only running it as it is is refused
		rest := maps.Clone(scripts)
		rest[name] = strings.Join(f, " ")
		st, err := parseStart(rest, name, depth)
		st.says, st.vars = cmd, vars
		if err != nil {
			return st, err
		}
		return st, errEnv
	}
	s := start{says: cmd}
	switch f[0] {
	case "npm", "pnpm", "yarn":
		rest := f[1:]
		if len(rest) > 0 && (rest[0] == "run" || rest[0] == "run-script") {
			rest = rest[1:]
		}
		if len(rest) == 1 {
			if f[0] == "npm" && rest[0] == "start" || scripts[rest[0]] != "" {
				return parseStart(scripts, rest[0], depth+1)
			}
		}
		return s, errShell
	case "node":
		s.runtime = "node"
		f = f[1:]
	case "bun":
		s.runtime = "bun"
		f = f[1:]
		bunFlag := false
		for len(f) > 0 && (f[0] == "run" || f[0] == "--bun" || f[0] == "--watch" || f[0] == "--hot" || f[0] == "--smol") {
			if f[0] == "--bun" {
				bunFlag = true
			}
			if f[0] == "--smol" {
				s.flags = append(s.flags, f[0])
			}
			f = f[1:]
		}
		if len(f) > 0 && !strings.HasPrefix(f[0], "-") && !entryFileRe.MatchString(f[0]) {
			if scripts[f[0]] != "" && len(f) == 1 {
				inner, err := parseStart(scripts, f[0], depth+1)
				if bunFlag {
					inner.runtime = "bun"
				}
				if err == nil || errors.Is(err, errShell) || errors.Is(err, errEnv) {
					inner.says = cmd + " (" + inner.says + ")"
				}
				return inner, err
			}
			// a package's command: bun run respects its #!/usr/bin/env
			// node, unless --bun
			s.bin, s.args = f[0], f[1:]
			if !bunFlag {
				s.runtime = "node"
			}
			return s, nil
		}
	default:
		// a package's command (next start, react-router-serve, ...):
		// its #!/usr/bin/env node runs it on Node
		s.bin, s.args, s.runtime = f[0], f[1:], "node"
		return s, nil
	}
	// the runtime's flags, then the file and its args
	for len(f) > 0 && strings.HasPrefix(f[0], "-") {
		flag := f[0]
		s.flags = append(s.flags, flag)
		f = f[1:]
		if slices.Contains([]string{"-r", "--require", "--import", "--preload", "--loader", "--experimental-loader", "-C", "--conditions", "--env-file", "--title"}, flag) && len(f) > 0 {
			s.flags = append(s.flags, f[0])
			f = f[1:]
		}
	}
	if s.runtime == "node" {
		s.flags = slices.DeleteFunc(s.flags, func(x string) bool { return x == "--watch" })
	}
	if len(f) == 0 {
		return s, errShell
	}
	s.file, s.args = f[0], f[1:]
	return s, nil
}

// js plans a JavaScript app (a package.json and its package manager's
// lockfile). runtime is what the build settings, else homeport.yaml, said.
func (r reader) js(p *Plan, cfg fileConfig, runtime, runtimeFrom, settingsRun string) error {
	pkg := r.pkg()
	deps := r.deps()
	pmName, warning, err := r.packageManagerOf(pkg)
	if err != nil {
		return err
	}
	if warning != "" {
		p.Warnings = append(p.Warnings, warning)
	}
	p.PackageManager = pmName

	// rsc-kit: its build says what it made (Bun only, as before)
	if pmName == "bun" && deps["@rsc-kit/core"] {
		if err := r.bunVersion(pkg); err != nil {
			return err
		}
		p.Toolchain, p.Image, p.Install, p.Framework = "bun", BunImage, "bun install --frozen-lockfile", "Bun"
		if p.Command == "" {
			p.Command = "bun run build"
		}
		r.rscKit(p, cfg)
		p.PackageManager = ""
		return nil
	}

	// a SvelteKit app builds with the adapter its config imports: one
	// installed but not imported isn't its
	svelteAdapters := r.svelteAdapters()
	if slices.Contains(svelteAdapters, "@orochibraru/svelte-smol") {
		return errors.New("svelte-smol isn't supported: SvelteKit's own Bun adapter compiles an app - use @sveltejs/adapter-bun with buildOptions.compile")
	}
	var pr *preset
	for _, c := range presets {
		if c.p.name == "SvelteKit" {
			if len(svelteAdapters) > 0 && !slices.Contains(svelteAdapters, c.deps[0]) {
				continue
			}
			// both server adapters and no config to say: adapter-node
			if len(svelteAdapters) == 0 && c.deps[0] == "@sveltejs/adapter-bun" && deps["@sveltejs/adapter-node"] {
				continue
			}
		}
		if slices.ContainsFunc(c.deps, func(d string) bool { return deps[d] }) {
			pr = &c.p
			break
		}
	}
	switch {
	case pr != nil && pr.name == "TanStack Start":
		if pr, err = r.tanStackStart(*pr, deps); err != nil {
			return err
		}
	case pr != nil && pr.only && pr.name == "SvelteKit":
		pr = r.svelteBun(*pr)
	}
	if pr != nil && pr.only && runtime != "" && runtime != pr.needs {
		return fmt.Errorf("the runtime is %s (%s), but %s: it runs on %s only - set the runtime to %s, or leave it to detection", runtime, runtimeFrom, pr.why, title(pr.needs), pr.needs)
	}

	// the project's own build compiles its server: that binary is what runs
	build := pkg.Scripts["build"]
	if c := r.compiled(pkg, deps, pr, build); c.kind != "" && cfg.Build.Artifact == "" && cfg.Static == "" {
		if c.err != nil {
			return c.err
		}
		if runtime == "node" {
			return fmt.Errorf("the runtime is node (%s), but %s: a compiled app runs on the Bun it was compiled with - set the runtime to bun, or leave it to detection", runtimeFrom, c.reason)
		}
		p.Compiled = true
		if err := r.bunVersion(pkg); err != nil {
			return err
		}
		m := r.pmFor(pmName, pkg, false)
		p.Toolchain, p.Kind, p.Framework, p.StaticFallback = "bun", c.kind, c.framework, false
		if pmName == "bun" {
			p.Image, p.Install = BunImage, m.install
		} else {
			// Bun beside Node, for the compile
			nv, _, err := r.nodeVersion(pkg)
			if err != nil {
				return err
			}
			p.Toolchain, p.Image = "node", nodeImage(nv)
			p.setup = join(fetchBun, m.setup)
			p.Install = join(p.setup, m.install)
		}
		p.Runtime, p.RuntimeVersion, p.RuntimeReason = "bun", BunVersion, c.reason
		if p.Command == "" {
			p.Command = cmpOr(c.build, m.run+" build")
		}
		p.Artifact = c.artifact
		return nil
	}

	// homeport.yaml says it's a site, or a binary the build makes
	if cfg.Static != "" || cfg.Build.Artifact != "" {
		return r.siteToolchain(p, pkg, pmName)
	}
	// a static site
	if r.site(p, cfg) || r.nextExport(p, cfg, deps) {
		return r.siteToolchain(p, pkg, pmName)
	}

	// what the developer runs: the start script, else (a Bun app's) dev
	st, stErr := parseStart(pkg.Scripts, "start", 0)
	stFrom := "start script"
	if stErr != nil && !errors.Is(stErr, errShell) && !errors.Is(stErr, errEnv) {
		if d, err := parseStart(pkg.Scripts, "dev", 0); err == nil && d.runtime == "bun" && d.file != "" {
			st, stErr, stFrom = d, nil, "dev script"
		}
	}
	if pr != nil && pr.name == "NestJS" {
		// nest start compiles first: start:prod is what runs the build
		if sp, err := parseStart(pkg.Scripts, "start:prod", 0); err == nil {
			st, stErr = sp, nil
		}
	}

	rt, reason := r.runtimeFor(pkg, pr, st, stErr, stFrom, runtime, runtimeFrom)

	// what it starts
	name, layout := "Node", "app"
	if rt == "bun" {
		name = "Bun"
	}
	var env map[string]string
	entry, binName := "", ""
	var args, flags []string
	if pr != nil {
		name, layout, env, entry = pr.name, pr.layout, pr.env, pr.entry
	}
	switch {
	case pr != nil && pr.layout != "app":
		// the framework's own output has its own entry
	case pr != nil && pr.name == "SvelteKit":
		// the adapter's out: in svelte.config.js, or (SvelteKit 3) in the
		// sveltekit() plugin's options in the Vite config
		if m := adapterOutRe.FindSubmatch(r.svelteAdapterCall(pr)); m != nil && relPath(string(m[1])) {
			entry = clean(string(m[1])) + "/index.js"
		}
	case pr != nil && pr.name == "Astro":
		for _, f := range []string{"astro.config.mjs", "astro.config.ts", "astro.config.js", "astro.config.mts"} {
			if b, err := r.read(f); err == nil && astroMiddle.Match(b) {
				return errors.New("Astro's node adapter is in middleware mode, which needs a server of yours to run it: set mode: 'standalone' in " + f)
			}
		}
	case errors.Is(stErr, errEnv) && settingsRun == "" && cfg.Run == "":
		return fmt.Errorf("the start script (%s) sets %s before its command: set %s as an environment variable of the app instead, and drop it from the script",
			st.says, strings.Join(st.vars, ", "), map[bool]string{true: "them", false: "it"}[len(st.vars) > 1])
	case stErr == nil && st.file != "":
		entry, args, flags = st.file, st.args, st.flags
		if st.runtime != "" && st.runtime != rt {
			// its flags are the other runtime's
			flags = nil
		}
	case stErr == nil && st.bin != "" && st.bin != "nest":
		binName, args = st.bin, st.args
	case pr != nil && pr.entry != "":
	case rt == "bun" && (pkg.Module != "" || pkg.Main != ""):
		entry = cmpOr(pkg.Module, pkg.Main)
	case pkg.Main != "":
		entry = pkg.Main
	}
	if entry == "" && binName == "" && settingsRun == "" && cfg.Run == "" {
		switch {
		case errors.Is(stErr, errShell):
			return fmt.Errorf("the start script (%s) needs a shell, which the sandbox doesn't have: set a start command - args to %s, like `%s` - "+
				"and run anything before it (migrations) as the release command", st.says, rt, runArgs(rt, "server.js", nil, nil))
		case pr != nil && pr.name == "TanStack Start":
			// TanStack's hosting guide: Node, Bun and the rest follow Nitro's
			return fmt.Errorf("TanStack Start's build without Nitro is dist/server/server.js, a request handler with no server: " +
				"run npm install nitro (or your package manager's add), then add nitro() from 'nitro/vite' to vite.config's plugins - " +
				"nitro({ preset: 'bun' }) to run on Bun - and homeport runs Nitro's .output/server/index.mjs. Or add a start script that serves it")
		case pr != nil:
			return fmt.Errorf("can't tell how this %s app starts: add a start script (like `%s server.js`), or set a start command", pr.name, rt)
		}
		// nothing says it's a server: a guess, as before - a binary its
		// build makes, else a site folder
		return r.guess(p, pkg, pmName)
	}
	if entry != "" && (!relPath(entry) || strings.HasPrefix(entry, "/")) {
		return fmt.Errorf("the start script runs %q, which isn't a file in the app's folder", entry)
	}

	// the toolchain: Bun's image when Bun installs and runs it, else Node's
	// (with Bun beside it when either is Bun)
	m := r.pmFor(pmName, pkg, true)
	usesBun := pmName == "bun" || rt == "bun"
	if usesBun {
		if err := r.bunVersion(pkg); err != nil {
			return err
		}
	}
	nodeV, nodeWhy, err := r.nodeVersion(pkg)
	if err != nil && rt == "node" {
		return err
	}
	if rt == "bun" && pmName == "bun" {
		p.Toolchain, p.Image = "bun", BunImage
	} else {
		if err != nil {
			nodeV, nodeWhy = DefaultNode, ""
		}
		p.Toolchain, p.Image = "node", nodeImage(nodeV)
		if usesBun {
			p.setup = fetchBun
		}
	}
	p.setup = join(p.setup, m.setup)
	p.Install = join(p.setup, m.install)
	p.Runtime, p.RuntimeReason, p.Framework, p.Kind, p.Artifact, p.StaticFallback = rt, reason, name, Bundle, BundleDir, false
	p.Health = "/"
	if rt == "node" {
		p.RuntimeVersion = nodeV
		if nodeWhy != "" {
			p.RuntimeReason += "; " + nodeWhy
		}
	} else {
		p.RuntimeVersion = BunVersion
	}

	// the build, then the bundle made from what it left
	build = cfg.Build.Command
	if build == "" && pkg.Scripts["build"] != "" {
		build = m.run + " build"
		if pr != nil && pr.build != "" {
			build = pr.build
		}
		if name == "Next.js" && !r.nextStandalone() {
			// Next's own default, when the config doesn't say
			build = "NEXT_PRIVATE_STANDALONE=1 " + build
		}
	}
	if build == "" && layout != "app" {
		return fmt.Errorf("%s needs a build: package.json has no build script", name)
	}
	keepMaps := slices.Contains(flags, "--enable-source-maps")
	bin, sums := "/usr/local/bin/node", nodeSum(nodeV)
	if rt == "bun" {
		bin, sums = "/usr/local/bin/bun", bunSum
		if p.Toolchain == "node" {
			bin = "/tmp/homeport/bin/bun"
		}
	}
	p.build, p.assemble = build, assemble(layout, m, rt, bin, sums, binName, keepMaps, env)
	if pr != nil && pr.name == "SvelteKit" && entry != "" && binName == "" && settingsRun == "" && cfg.Run == "" {
		// what it starts is the adapter's: missing, the build fails here,
		// not the deploy
		p.assemble = join(`{ [ -f `+entry+` ] || { echo 'homeport: the build made no `+entry+` - is the adapter in the config the one installed, and its out this?' >&2; exit 1; }; }`, p.assemble)
	}
	p.Command = join(build, p.assemble)
	if binName != "" {
		entry = ".homeport/start.mjs"
	}
	if entry != "" {
		p.Run = runArgs(rt, entry, flags, args)
	}
	return nil
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func join(parts ...string) string {
	return strings.Join(slices.DeleteFunc(parts, func(s string) bool { return s == "" }), " && ")
}

// runArgs: the args to the bundle's bin - the runtime - that start entry,
// after the boot.
func runArgs(rt, entry string, flags, args []string) string {
	boot := "--import ./" + bootFile
	if rt == "bun" {
		boot = "--preload ./" + bootFile
	}
	return strings.Join(slices.Concat([]string{boot}, flags, []string{entry}, args), " ")
}

// runtimeFor decides Bun or Node, and says why: the person's say, the
// project's (engines, version files), what its start command invokes, a
// framework that runs on one only, else Node.
func (r reader) runtimeFor(pkg pkgJSON, pr *preset, st start, stErr error, stFrom, set, setFrom string) (string, string) {
	if set != "" {
		return set, "set in " + setFrom
	}
	if pr != nil && pr.only {
		return pr.needs, pr.why
	}
	rt, why := "", ""
	// a start script that sets variables still says what it runs
	ran := stErr == nil || errors.Is(stErr, errEnv)
	_, bun := pkg.Engines["bun"]
	_, node := pkg.Engines["node"]
	switch {
	case bun && !node:
		rt, why = "bun", "package.json's engines name Bun"
	case node && !bun:
		rt, why = "node", "package.json's engines name Node"
	case r.exists(".bun-version"):
		rt, why = "bun", "the app has a .bun-version"
	case r.exists(".nvmrc"):
		rt, why = "node", "the app has an .nvmrc"
	case r.exists(".node-version"):
		rt, why = "node", "the app has a .node-version"
	case ran && st.runtime == "bun":
		rt, why = "bun", "its "+stFrom+" runs "+st.says
	case ran && st.runtime == "node" && st.bin != "":
		rt, why = "node", "its "+stFrom+" runs "+st.says+", a Node command (#!/usr/bin/env node: Node even from bun run, unless it says bun --bun)"
	case ran && st.runtime == "node":
		rt, why = "node", "its "+stFrom+" runs "+st.says
	}
	// a framework that runs on one only: only when nothing else said
	if rt == "" && pr != nil && pr.needs != "" {
		return pr.needs, cmpOr(pr.why, pr.name+" runs on "+title(pr.needs))
	}
	if rt == "" {
		return "node", defaultRuntimeWhy
	}
	return rt, why
}

// defaultRuntimeWhy is why Node runs an app when nothing says which.
const defaultRuntimeWhy = "nothing says Bun: Node, the default"

func title(rt string) string { return map[string]string{"bun": "Bun", "node": "Node"}[rt] }

// guess: no server the plan can see - a binary the build makes (server),
// else a site folder, as homeport always guessed.
func (r reader) guess(p *Plan, pkg pkgJSON, pmName string) error {
	if err := r.siteToolchain(p, pkg, pmName); err != nil {
		return err
	}
	p.Framework = map[string]string{"bun": "Bun"}[pmName]
	if p.Framework == "" {
		p.Framework = "Node"
	}
	return nil
}

// siteToolchain: what builds a site (or a guess): the package manager's
// image, its install and build.
func (r reader) siteToolchain(p *Plan, pkg pkgJSON, pmName string) error {
	if pmName == "bun" {
		if err := r.bunVersion(pkg); err != nil {
			return err
		}
		m := r.pmFor(pmName, pkg, true)
		p.Toolchain, p.Image, p.Install = "bun", BunImage, m.install
		if p.Command == "" {
			p.Command = m.run + " build"
		}
	} else {
		v, _, err := r.nodeVersion(pkg)
		if err != nil {
			return err
		}
		m := r.pmFor(pmName, pkg, false)
		p.Toolchain, p.Image, p.setup = "node", nodeImage(v), m.setup
		p.Install = join(p.setup, m.install)
		if p.Command == "" {
			p.Command = m.run + " build"
		}
	}
	if p.Framework == "" {
		p.Framework = map[string]string{"bun": "Bun"}[pmName]
		if p.Framework == "" {
			p.Framework = "Node"
		}
	}
	p.PackageManager = pmName
	return nil
}

// nitroInline: a compiled Nitro server has no public folder beside it
// (it looks in the binary's own filesystem), so its assets must be in it:
// Nitro's serveStatic: 'inline' - in nuxt.config's nitro, or the options of
// the nitro() Vite plugin.
func (r reader) nitroInline(framework string) error {
	where, files := "nuxt.config's nitro options", nuxtConfigs
	if framework != "Nuxt" {
		where, files = "the nitro() plugin's options in vite.config, or nitro.config", slices.Concat(viteConfigs, nitroConfigs)
	}
	for _, f := range files {
		b, err := r.read(f)
		if err != nil {
			continue
		}
		if slices.Contains(viteConfigs, f) {
			b = callText(b, nitroCallRe)
		}
		if inlineRe.Match(stripComments(b)) {
			return nil
		}
	}
	return fmt.Errorf("the build compiles Nitro's server (.output/server/index.mjs), which serves its public assets from beside it, and a binary has nothing beside it: "+
		"set serveStatic: 'inline' in %s, so they're in the binary", where)
}

var svelteAdapterImportRe = regexp.MustCompile("\\bfrom\\s*['\"`](@sveltejs/adapter-[a-z0-9-]+|@orochibraru/svelte-smol)['\"`]")

// svelteAdapters: the SvelteKit adapters a config imports.
func (r reader) svelteAdapters() []string {
	var out []string
	for _, f := range svelteConfigs {
		if b, err := r.read(f); err == nil {
			for _, m := range svelteAdapterImportRe.FindAllSubmatch(stripComments(b), -1) {
				if !slices.Contains(out, string(m[1])) {
					out = append(out, string(m[1]))
				}
			}
		}
	}
	return out
}

// svelteAdapterCallOf: the text of the call of package pkg's default
// import - adapter( ... ), bun( ... ), whatever it's named - in the config
// that imports it; else the first adapter( call; nil without one.
func (r reader) svelteAdapterCallOf(pkg string) []byte {
	importRe := regexp.MustCompile("\\bimport\\s+([A-Za-z_$][\\w$]*)\\s*(?:,\\s*\\{[^}]*\\}\\s*)?from\\s*['\"`]" + regexp.QuoteMeta(pkg) + "['\"`]")
	for _, f := range svelteConfigs {
		b, err := r.read(f)
		if err != nil {
			continue
		}
		b = stripComments(b)
		if m := importRe.FindSubmatch(b); m != nil {
			return callText(b, regexp.MustCompile("(?:^|[^\\w$.])"+regexp.QuoteMeta(string(m[1]))+"\\s*\\("))
		}
	}
	for _, f := range svelteConfigs {
		if b, err := r.read(f); err == nil {
			if c := adapterCall(b); c != nil {
				return c
			}
		}
	}
	return nil
}

// svelteAdapterCall: the call of the preset's adapter.
func (r reader) svelteAdapterCall(pr *preset) []byte {
	if pr.only {
		return r.svelteAdapterCallOf("@sveltejs/adapter-bun")
	}
	return r.svelteAdapterCallOf("@sveltejs/adapter-node")
}

// svelteBunCall: adapter-bun's call.
func (r reader) svelteBunCall() []byte { return r.svelteAdapterCallOf("@sveltejs/adapter-bun") }

// svelteBun: adapter-bun's env defaults under its envPrefix, when it has one:
// it reads <prefix>PORT and <prefix>HOST, and refuses unknown prefixed
// names, so the boot sets those from homeport's PORT and HOST.
func (r reader) svelteBun(p preset) *preset {
	m := envPrefixRe.FindSubmatch(r.svelteBunCall())
	if m == nil || len(m[1]) == 0 {
		return &p
	}
	prefix := string(m[1])
	p.env = map[string]string{prefix + "PORT": "$PORT", prefix + "HOST": "$HOST"}
	for k, v := range forwarded {
		p.env[prefix+k] = v
	}
	return &p
}

// svelteBunCompiled: adapter-bun with buildOptions.compile makes one
// executable, client assets in it: <out>/server, or its outfile.
func (r reader) svelteBunCompiled() (compiledServer, bool) {
	call := r.svelteBunCall()
	m := compileRe.FindSubmatch(call)
	if m == nil || string(m[1]) == "false" {
		return compiledServer{}, false
	}
	out, name := "build", "server"
	if o := adapterOutRe.FindSubmatch(call); o != nil && relPath(string(o[1])) {
		out = clean(string(o[1]))
	}
	if o := outfileOptRe.FindSubmatch(call); o != nil && relPath(string(o[1])) {
		name = string(o[1])
	}
	c := compiledServer{kind: Binary, artifact: out + "/" + name, framework: "SvelteKit",
		reason: "its adapter, adapter-bun, compiles it with Bun (buildOptions.compile)", build: "bun run --bun build"}
	target := string(m[2])
	if t := compileTargetRe.FindSubmatch(call); target == "" && t != nil {
		target = string(t[1])
	}
	switch {
	case target != "" && (!strings.HasPrefix(target, "bun-linux-") || strings.Contains(target, "musl")):
		c.err = fmt.Errorf("adapter-bun compiles for %s, which homeport's Linux (glibc) can't run: drop the target, so it compiles for the machine it builds on", target)
	case target != "":
		// a Linux target is this machine's or it won't run here: checked
		// before the build
		machine := map[bool]string{true: "aarch64", false: "x86_64"}[strings.HasPrefix(target, "bun-linux-arm64") || strings.HasPrefix(target, "bun-linux-aarch64")]
		c.build = join(`{ [ "$(uname -m)" = `+machine+` ] || { echo "homeport: adapter-bun compiles for `+target+`, and this machine is $(uname -m): drop the target, so it compiles for the machine it builds on" >&2; exit 1; }; }`, c.build)
	case envPrefixRe.Match(call) && len(envPrefixRe.FindSubmatch(call)[1]) > 0:
		prefix := string(envPrefixRe.FindSubmatch(call)[1])
		c.err = fmt.Errorf("adapter-bun's envPrefix makes the compiled server listen on %sPORT and %sHOST, and homeport sets PORT and HOST: drop envPrefix, or don't compile (the bundle sets the prefixed names for you)", prefix, prefix)
	}
	return c, true
}

// tanStackStart: a TanStack Start app built with Nitro's Vite plugin (its
// nitro package) ships Nitro's .output, as Nuxt does, on Bun when the
// plugin's preset is bun. Without Nitro, vite build makes
// dist/server/server.js, a fetch handler that a start script serves (srvx,
// TanStack's Bun server): the app, as any other. A preset for another
// platform makes no server to run.
func (r reader) tanStackStart(p preset, deps map[string]bool) (*preset, error) {
	if !deps["nitro"] && !deps["nitro-nightly"] {
		return &p, nil
	}
	p.layout, p.entry = "output", "server/index.mjs"
	// the nitro() plugin's options, then Nitro's own nitro.config
	for _, f := range slices.Concat(viteConfigs, nitroConfigs) {
		b, err := r.read(f)
		if err != nil {
			continue
		}
		if slices.Contains(viteConfigs, f) {
			b = callText(b, nitroCallRe)
		}
		m := presetRe.FindSubmatch(stripComments(b))
		if m == nil {
			continue
		}
		switch preset := strings.ReplaceAll(string(m[1]), "_", "-"); preset {
		case "bun":
			p.needs, p.why = "bun", "Nitro's bun preset ("+f+") builds a Bun server"
		case "node-server", "node", "node-cluster":
		default:
			return nil, fmt.Errorf("%s builds with Nitro's %s preset, which makes no server homeport runs: drop the preset (node-server is Nitro's default), or set 'node-server' or 'bun'", f, preset)
		}
		break
	}
	return &p, nil
}

// nextStandalone: the config sets output: "standalone".
func (r reader) nextStandalone() bool {
	for _, f := range nextConfigs {
		if b, err := r.read(f); err == nil {
			if m := nextOutputRe.FindSubmatch(b); m != nil && string(m[1]) == "standalone" {
				return true
			}
		}
	}
	return false
}

// nextExport: a Next.js app with output: "export" is a static site, in out/.
func (r reader) nextExport(p *Plan, cfg fileConfig, deps map[string]bool) bool {
	if !deps["next"] {
		return false
	}
	for _, f := range nextConfigs {
		if b, err := r.read(f); err == nil {
			if m := nextOutputRe.FindSubmatch(b); m != nil && string(m[1]) == "export" {
				p.Framework = "Next.js"
				if cfg.Static == "" && cfg.Build.Artifact == "" {
					p.Kind, p.Artifact, p.StaticFallback = Static, "out", false
				}
				return true
			}
		}
	}
	return false
}

// compiledName: the binary bun build --compile makes - its --outfile, else
// named after its entry.
// compiledServer is a build that compiles the app's server.
type compiledServer struct {
	kind, artifact, framework, reason string
	build                             string // its build command, when it isn't the build script's
	err                               error  // it compiles, but what it makes can't run here
}

var (
	outfileOptRe = regexp.MustCompile(`\boutfile\s*:\s*['"]([^'"]+)['"]`)
	nbcConfigRe  = regexp.MustCompile(`adapterPath[^\n]*next-bun-compile`)
)

// compiled: whether the project's own build compiles its server, and to
// what. A tool that does (adapter-bun, next-bun-compile), or bun build
// --compile in the build script - when no framework's server is what
// starts, or the compiled file is that server (its output, or what the
// start script runs), not a part of it like a worker.
func (r reader) compiled(pkg pkgJSON, deps map[string]bool, pr *preset, build string) compiledServer {
	if deps["next"] && deps["next-bun-compile"] {
		said := strings.Contains(build, "NEXT_ADAPTER_PATH=next-bun-compile")
		for _, f := range nextConfigs {
			if b, err := r.read(f); err == nil && nbcConfigRe.Match(stripComments(b)) {
				said = true
			}
		}
		if said {
			return compiledServer{kind: Binary, artifact: "server", framework: "Next.js", reason: "its build compiles it with next-bun-compile (Bun)"}
		}
	}
	if deps["@sveltejs/adapter-bun"] && pr != nil && pr.only && pr.name == "SvelteKit" {
		if c, ok := r.svelteBunCompiled(); ok {
			return c
		}
	}
	at := strings.Index(build, "bun build")
	if at < 0 || !strings.Contains(build[at:], "--compile") {
		return compiledServer{}
	}
	name := compiledName(build[at:])
	starts := false
	if f := strings.Fields(pkg.Scripts["start"]); len(f) > 0 {
		starts = clean(f[0]) == name
	}
	entry := ""
	for _, f := range strings.Fields(build[at:]) {
		if entryFileRe.MatchString(f) && relPath(f) {
			entry = clean(f)
			break
		}
	}
	framework := "Bun"
	if pr == nil && pkg.Scripts["start"] != "" && !starts {
		return compiledServer{} // the start script runs something else: the compile made a tool
	}
	if pr != nil {
		framework = pr.name
		server := pr.entry
		switch pr.layout {
		case "standalone":
			server = ".next/standalone/" + pr.entry
		case "output":
			server = ".output/" + pr.entry
		}
		if !starts && (entry == "" || entry != server) {
			return compiledServer{} // a part of the server, not the server
		}
	}
	c := compiledServer{kind: Binary, artifact: name, framework: framework, reason: "its build script compiles it with bun build --compile"}
	if pr != nil && pr.layout == "output" {
		c.err = r.nitroInline(pr.name)
	}
	return c
}

var (
	blockCommentRe = regexp.MustCompile(`(?s)/\*.*?\*/`)
	// a // comment: at a line's start or after a space or punctuation, so
	// a URL's // (https://) isn't one
	lineCommentRe = regexp.MustCompile(`(?m)(^|[\s;,{}()\[\]])//.*$`)
	adapterCallRe = regexp.MustCompile(`\badapter\s*\(`)
	nitroCallRe   = regexp.MustCompile(`\bnitro\s*\(`)
	inlineRe      = regexp.MustCompile("\\bserveStatic\\s*:\\s*['\"`]inline['\"`]")
	envPrefixRe   = regexp.MustCompile(`\benvPrefix\s*:\s*['"]([^'"]*)['"]`)
	// buildOptions.compile: true, false, a target, or options
	compileRe       = regexp.MustCompile(`\bcompile\s*:\s*(?:(true|false)|['"]([^'"]*)['"]|\{)`)
	compileTargetRe = regexp.MustCompile(`\btarget\s*:\s*['"]([^'"]+)['"]`)
	presetRe        = regexp.MustCompile("\\bpreset\\s*:\\s*['\"`]([^'\"`]+)['\"`]")
)

// stripComments is config code without its comments.
func stripComments(b []byte) []byte {
	return lineCommentRe.ReplaceAll(blockCommentRe.ReplaceAll(b, nil), []byte("$1"))
}

// adapterCall is the text inside a config's adapter( ... ) call, comments
// stripped: where an adapter's options are, not any key of the same name
// elsewhere in the file. Empty without one.
func adapterCall(b []byte) []byte { return callText(b, adapterCallRe) }

// callText is the text inside the first call that re matches (its name and
// open paren), comments stripped. Empty without one.
func callText(b []byte, re *regexp.Regexp) []byte {
	b = stripComments(b)
	loc := re.FindIndex(b)
	if loc == nil {
		return nil
	}
	depth := 1
	for i := loc[1]; i < len(b); i++ {
		switch b[i] {
		case '(':
			depth++
		case ')':
			if depth--; depth == 0 {
				return b[loc[1]:i]
			}
		}
	}
	return b[loc[1]:]
}

func compiledName(build string) string {
	if m := outfileRe.FindStringSubmatch(build); m != nil && relPath(m[1]) {
		return clean(m[1])
	}
	for _, f := range strings.Fields(build) {
		if entryFileRe.MatchString(f) && relPath(f) {
			b := path.Base(f)
			return strings.TrimSuffix(b, path.Ext(b))
		}
	}
	return "server"
}

// bunVersion: whether the Bun the project names - packageManager,
// .bun-version, engines.bun - is the pinned one: 1, 1.4, 1.4.x, or a range
// it's in. Anything else is refused, as Node outside its pinned lines is:
// every Bun homeport runs is pinned by digest and checksum.
func (r reader) bunVersion(pkg pkgJSON) error {
	asked := []struct{ from, v string }{{"package.json's packageManager", r.packageManager("bun")}, {".bun-version", r.firstLine(".bun-version")}}
	if e, ok := pkg.Engines["bun"].(string); ok {
		asked = append(asked, struct{ from, v string }{"engines.bun", e})
	}
	pinned := mustVersion(BunVersion)
	for _, a := range asked {
		v := strings.TrimPrefix(strings.TrimSpace(a.v), "v")
		if v == "" {
			continue
		}
		if versionRe.MatchString(v) {
			// a bare version is its line: 1, 1.4 and 1.4.x are 1.4.3's
			f := strings.Split(v, ".")
			if f[0] == "1" && (len(f) == 1 || f[1] == strings.Split(BunVersion, ".")[1]) {
				continue
			}
		} else if rng, err := parseRange(v); err == nil && rng.has(pinned) {
			continue
		}
		return fmt.Errorf("%s asks for Bun %q: homeport runs Bun %s - ask for 1.4 (or a range it's in)", a.from, a.v, BunVersion)
	}
	return nil
}

func nodeImage(v string) string {
	for _, n := range nodeReleases {
		if n.Version == v {
			return "node:" + n.Version + "-bookworm@" + n.Digest
		}
	}
	return "node:" + v
}

// nodeVersion: the pinned release the project's Node version asks for -
// .nvmrc, .node-version, else engines.node - and what asked; the current
// LTS when nothing does.
func (r reader) nodeVersion(pkg pkgJSON) (string, string, error) {
	asked, from := "", ""
	for _, f := range []string{".nvmrc", ".node-version"} {
		if v := r.firstLine(f); v != "" {
			asked, from = v, f
			break
		}
	}
	if asked == "" {
		if e, ok := pkg.Engines["node"].(string); ok && strings.TrimSpace(e) != "" {
			asked, from = e, "engines.node"
		}
	}
	if asked == "" {
		return DefaultNode, "", nil
	}
	v, err := pickNode(asked)
	if err != nil {
		return "", "", fmt.Errorf("%s asks for Node %q: %w", from, asked, err)
	}
	return v, fmt.Sprintf("Node %s, as %s asks (%s)", v, from, asked), nil
}

func supportedNode() string {
	var majors []string
	for _, n := range nodeReleases {
		majors = append(majors, strings.Split(n.Version, ".")[0])
	}
	return strings.Join(majors[:len(majors)-1], ", ") + " or " + majors[len(majors)-1]
}

// pickNode: a bare version (22, v22.11.0) is its major line's release;
// lts/* the default; lts/<codename> its line; node/latest the newest; a
// range the default if it's in it, else the newest that is.
func pickNode(asked string) (string, error) {
	a := strings.ToLower(strings.TrimSpace(asked))
	bad := fmt.Errorf("homeport runs Node %s", supportedNode())
	switch {
	case a == "lts/*" || a == "lts":
		return DefaultNode, nil
	case strings.HasPrefix(a, "lts/"):
		if major, ok := nodeLTS[strings.TrimPrefix(a, "lts/")]; ok {
			return nodeMajor(major, bad)
		}
		return "", bad
	case a == "node" || a == "latest" || a == "current" || a == "stable":
		return nodeReleases[len(nodeReleases)-1].Version, nil
	}
	if bare := strings.TrimPrefix(a, "v"); versionRe.MatchString(bare) {
		return nodeMajor(strings.Split(bare, ".")[0], bad)
	}
	rng, err := parseRange(a)
	if err != nil {
		return "", bad
	}
	if rng.has(mustVersion(DefaultNode)) {
		return DefaultNode, nil
	}
	for i := len(nodeReleases) - 1; i >= 0; i-- {
		if rng.has(mustVersion(nodeReleases[i].Version)) {
			return nodeReleases[i].Version, nil
		}
	}
	return "", bad
}

func nodeMajor(major string, bad error) (string, error) {
	for _, n := range nodeReleases {
		if strings.Split(n.Version, ".")[0] == major {
			return n.Version, nil
		}
	}
	return "", bad
}

// fetchBun puts the pinned Bun on PATH in a Node image, by its checksum.
var fetchBun = "mkdir -p /tmp/homeport/bin && export PATH=/tmp/homeport/bin:$PATH && " +
	"case $(uname -m) in x86_64) a=x64 s=" + bunSum["x86_64"] + " ;; aarch64) a=aarch64 s=" + bunSum["aarch64"] + " ;; *) echo \"homeport: no Bun for $(uname -m)\" >&2; exit 1 ;; esac && " +
	"curl -fsSL https://registry.npmjs.org/@oven/bun-linux-$a/-/bun-linux-$a-" + BunVersion + ".tgz | tar -xzO package/bin/bun > /tmp/homeport/bin/bun && " +
	"chmod 755 /tmp/homeport/bin/bun && echo \"$s  /tmp/homeport/bin/bun\" | sha256sum -c --quiet -"

// checkSum: the binary at path is the pinned one for this machine.
func checkSum(path string, sums map[string]string, what string) string {
	return "case $(uname -m) in x86_64) s=" + sums["x86_64"] + " ;; aarch64) s=" + sums["aarch64"] + " ;; *) s=none ;; esac && " +
		"{ echo \"$s  " + path + "\" | sha256sum -c --quiet - || { echo 'homeport: " + what + " is not the official binary homeport pins' >&2; exit 1; }; }"
}

// assemble makes the bundle from what the build left: the framework's
// output (or the app with its production dependencies), files only - no
// links, no node_modules/.bin, no source maps unless it starts with them -
// the runtime as bin, and the boot.
// bin is where the runtime is in the image, sums its pinned checksums (nil:
// not pinned, not checked).
func assemble(layout string, m pm, rt, bin string, sums map[string]string, binName string, keepMaps bool, env map[string]string) string {
	const b = BundleDir
	// a tree's files into the bundle: links followed, hard links copied
	put := func(src, dst string, exclude ...string) string {
		ex := ""
		for _, e := range exclude {
			ex += " --exclude=" + e
		}
		// links: dangling ones dropped, and none out of the app's folder
		// (tar -h would copy what it points at: / or the repository)
		// - every link, whatever its name (find -exec, not a read loop);
		// the copy through a file, not a pipe, so a failed tar -c fails
		return "find " + src + " -xtype l -delete && " +
			`r=$(pwd -P) && find ` + src + ` -type l -exec sh -c 'r=$1; shift; for l; do t=$(readlink -f -- "$l"); case "$t" in "$r"|"$r"/*) ;; *) printf "homeport: %s links outside the app (%s)\n" "$l" "$t" >&2; exit 1 ;; esac; done' sh "$r" {} + && ` +
			"(cd " + src + " && tar -chf \"$r/.homeport-copy.tar\" --hard-dereference --exclude=./.homeport-copy.tar" + ex + " .) && tar -xf \"$r/.homeport-copy.tar\" -C " + dst + " && rm -f \"$r/.homeport-copy.tar\""
	}
	steps := []string{"rm -rf " + b + " && mkdir -p " + b + "/.homeport"}
	switch layout {
	case "standalone":
		steps = append(steps,
			`{ [ -f .next/standalone/server.js ] || { echo 'homeport: the build made no .next/standalone/server.js - set output: "standalone" in next.config' >&2; exit 1; }; }`,
			put(".next/standalone", b, "./.next/cache"),
			"mkdir -p "+b+"/.next/static && "+put(".next/static", b+"/.next/static"),
			"{ [ ! -d public ] || { mkdir -p "+b+"/public && "+put("public", b+"/public")+"; }; }")
	case "output":
		steps = append(steps,
			`{ [ -f .output/server/index.mjs ] || { echo 'homeport: the build made no .output/server/index.mjs' >&2; exit 1; }; }`,
			put(".output", b))
	default:
		steps = append(steps, m.prune)
		if binName != "" {
			// a package's command: its file, found through node_modules/.bin,
			// run as the main module on Node (Module.runMain: a CommonJS bin
			// reading require.main finds itself); Bun has no working
			// runMain, so there it's imported
			steps = append(steps,
				"{ t=$(readlink -f node_modules/.bin/"+binName+") && [ -f \"$t\" ] || { echo 'homeport: the start script runs "+binName+", which is in no production dependency' >&2; exit 1; }; }",
				"t=${t#\"$(pwd -P)\"/}",
				`printf 'import Module, { createRequire } from "node:module";\nprocess.argv[1] = new URL("../%s", import.meta.url).pathname;\nif (typeof Bun === "undefined") Module.runMain();\nelse { Bun.main = process.argv[1]; createRequire(process.argv[1])(process.argv[1]); }\n' "$t" > `+b+"/.homeport/start.mjs")
		}
		steps = append(steps,
			put(".", b, "./.git", "./"+b, "./node_modules/.cache"),
			"find "+b+" -path '*/node_modules/.bin' -prune -exec rm -rf {} +")
	}
	if !keepMaps {
		steps = append(steps, "find "+b+" -name '*.map' -type f -delete")
	}
	// the runtime: only the one it runs
	steps = append(steps, "cp "+bin+" "+b+"/bin")
	if sums != nil {
		steps = append(steps, checkSum(b+"/bin", sums, title(rt)))
	}
	steps = append(steps, "printf '%s' '"+boot(env)+"' > "+b+"/"+bootFile)
	if rt == "node" {
		steps = append(steps, "mkdir -p "+b+"/"+CacheDir+" && echo "+CacheDir+" > "+b+"/.homeport/writable")
	}
	return join(steps...)
}

// nodeSum: the pinned release's binaries, by machine.
func nodeSum(v string) map[string]string {
	for _, n := range nodeReleases {
		if n.Version == v {
			return n.Sum
		}
	}
	return nil
}

// boot is .homeport/boot.mjs, run before the app (--import, --preload)
// (an env default's value "$X" is X's value: presets only, never the
// app's own env, so no literal value starts with $):
// the framework's env defaults (the app's own env wins); Node's compile
// cache in the release's writable folder, flushed once the app has loaded
// (a stopped app is sent SIGTERM, which wouldn't flush it); and a server
// on $PORT that listens on localhost only - Fastify's default - listening
// on every address instead, since nothing else in the sandbox reaches it.
// No single quotes: it's written in some.
func boot(env map[string]string) string {
	e, _ := json.Marshal(env)
	if env == nil {
		e = []byte("{}")
	}
	return `import * as m from "node:module";import net from "node:net";import http from "node:http";import https from "node:https";` +
		`const e=` + string(e) + `;for(const k in e){const v=e[k][0]==="$"?process.env[e[k].slice(1)]:e[k];if(v!==undefined)process.env[k]??=v}` +
		`try{if(m.enableCompileCache){m.enableCompileCache(new URL("../` + CacheDir + `",import.meta.url).pathname);` +
		`for(const t of [5e3,6e4])setTimeout(()=>{try{m.flushCompileCache()}catch{}},t).unref()}}catch{}` +
		`const p=Number(process.env.PORT),l=["localhost","127.0.0.1"];` +
		// each server class that defines its own listen: node:net's, and on
		// Bun, its http.Server and https.Server, which aren't net's
		`for(const S of [net.Server,http.Server,https.Server]){if(!Object.hasOwn(S.prototype,"listen"))continue;const o=S.prototype.listen;` +
		`S.prototype.listen=function(...a){const x=a[0];` +
		`if(x&&typeof x==="object"&&Number(x.port)===p&&l.includes(x.host))a[0]={...x,host:"0.0.0.0"};` +
		`else if(Number(x)===p&&l.includes(a[1]))a[1]="0.0.0.0";return o.apply(this,a)}}`
}

// lockfileAbove: a lockfile in a folder above the app's, up to the
// repository's root - a workspace's.
func (r reader) lockfileAbove() bool {
	for d := path.Dir(r.root); ; d = path.Dir(d) {
		for _, l := range lockfiles {
			if st, err := lstat(r.fsys, path.Join(d, l.file)); err == nil && st.Mode().IsRegular() {
				return true
			}
		}
		if d == "." || d == "/" {
			return false
		}
	}
}
