package buildplan

import (
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"slices"
	"strings"
)

// An api/ folder of handler files - web-standard Request in, Response out,
// one file a route - is built into a small server: the app's own files,
// one generated entry importing each handler (embed/api-server.mjs, which
// is also the server), run on Node or Bun as any JavaScript app. Beside a
// frontend (a Vite or Astro site, plain HTML) the server keeps the site's
// files and answers its pages too; with none, the project can be one file.
// What counts, and what leaves api/ alone, is decided here, once: the
// generator is handed the routes.

// StaticImmutableRe is the URL paths a static site's host caches a year as
// immutable - fingerprinted files - which the API server's frontend, and
// its public tree, cache as a static site would.
const StaticImmutableRe = `^(/(assets|_app/immutable|_astro|_next/static|_nuxt)/.*|.*[.-][0-9a-f]{8,}\.[A-Za-z0-9]+)$`

// PlanAPI is what an api/ folder makes: its routes, its gate, and the
// frontend served beside them.
type PlanAPI struct {
	Routes []APIRoute `json:"routes"`
	// Middleware: api/_middleware.*, run before every route; "" none
	Middleware string `json:"middleware,omitempty"`
	// Frontend: the site's folder, relative to the app's ("." plain HTML);
	// "" none known before the build - API only, unless the build makes a
	// site folder (SiteFolders) after all
	Frontend string `json:"frontend,omitempty"`
}

// APIRoute is one handler file: its URL path ([id] a param, [...x] the rest
// of the path), its file, relative to the app's folder, and the methods it
// exports by name; Fetch: its default export has a fetch, for the rest.
type APIRoute struct {
	Path    string   `json:"path"`
	File    string   `json:"file"`
	Methods []string `json:"methods,omitempty"`
	Fetch   bool     `json:"fetch,omitempty"`
}

//go:embed embed/api-server.mjs
var apiServerMJS string

// APIServer is embed/api-server.mjs: the generator, and the server it
// builds (homeport dev runs it too).
func APIServer() string { return apiServerMJS }

var (
	apiMethods = []string{"GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS"}
	apiExts    = []string{".ts", ".mts", ".js", ".mjs"}
	// a method exported as a function, at a line's start
	apiFuncRe  = regexp.MustCompile(`(?m)^export\s+(?:async\s+)?function\s*\*?\s*(GET|POST|PUT|PATCH|DELETE|HEAD|OPTIONS)\s*\(`)
	apiConstRe = regexp.MustCompile(`(?m)^export\s+const\s+(GET|POST|PUT|PATCH|DELETE|HEAD|OPTIONS)\s*(?::[^=\n]+)?=\s*(?:async\b|function\b|\(|[A-Za-z_$][\w$]*\s*=>)`)
	// export default { ... fetch ... }
	apiDefaultObjRe = regexp.MustCompile(`(?m)^export\s+default\s*\{`)
	apiFetchRe      = regexp.MustCompile(`(?:^|[\s,{])(?:async\s+)?fetch\s*[(:]`)
	// (req, res): a default export taking a response to write, or Vercel's
	// Node types
	apiReqResRe = regexp.MustCompile(`(?m)^export\s+default\s+(?:async\s+)?(?:function\s*[\w$]*\s*)?\(\s*[\w$]+\s*(?::[^,)]*)?,\s*(?:res|response)\b|from\s*['"]@vercel/node['"]`)
	apiConfigRe = regexp.MustCompile(`(?m)^export\s+const\s+(config|maxDuration|runtime)\b`)
	// a route's parts: a plain name, [param], [...rest]
	apiNameRe  = regexp.MustCompile(`^[A-Za-z0-9._~-]+$`)
	apiParamRe = regexp.MustCompile(`^\[(\.\.\.)?([A-Za-z_][A-Za-z0-9_]*)\]$`)
	// what makes api/ a project of its own
	apiManifests = []string{"package.json", "go.mod", "composer.json", "pyproject.toml", "requirements.txt", "Cargo.toml", ConfigFile}
	// a Worker's or a function's project: its default { fetch } isn't ours
	apiForeign = []string{"wrangler.toml", "wrangler.json", "wrangler.jsonc", "serverless.yml"}
)

// at most this many files in api/ are read
const maxAPIFiles = 500

// apiSource: a file in api/ that could be a handler (rel: relative to the
// app's folder), or the middleware
func apiSource(rel string) bool {
	in, ok := strings.CutPrefix(rel, "api/")
	if !ok || !slices.Contains(apiExts, path.Ext(rel)) {
		return false
	}
	base := path.Base(rel)
	if strings.Contains(base, ".d.") || strings.Contains(base, ".test.") || strings.Contains(base, ".spec.") {
		return false
	}
	if apiMiddleware(rel) {
		return true
	}
	return !slices.ContainsFunc(strings.Split(in, "/"), func(s string) bool { return strings.HasPrefix(s, "_") || strings.HasPrefix(s, ".") })
}

func apiMiddleware(rel string) bool {
	return slices.Contains([]string{"api/_middleware.ts", "api/_middleware.mts", "api/_middleware.js", "api/_middleware.mjs"}, rel)
}

// APIFiles are the files of a tree (paths relative to the app's folder)
// Detect reads to find an api/ folder's routes, beside Files: what leaves
// it alone first, then the middleware and the handler candidates - at most
// 20, which is what a control plane fetches. The builder reads them all.
func APIFiles(paths []string) []string {
	var first, rest []string
	for _, p := range paths {
		switch {
		case slices.ContainsFunc(apiManifests, func(m string) bool { return p == "api/"+m }), apiMiddleware(p):
			first = append(first, p)
		case apiSource(p):
			rest = append(rest, p)
		}
	}
	slices.Sort(first)
	slices.Sort(rest)
	out := slices.Concat(first, rest)
	if len(out) > 20 {
		out = out[:20]
	}
	slices.Sort(out)
	return out
}

// apiScan is what api/ holds.
type apiScan struct {
	routes     []APIRoute
	middleware string
	named      bool     // a route exports a method by name
	sources    bool     // any file that could be a handler
	reqRes     []string // (req, res) handlers: not run
	notes      []string // what's ignored in them
	err        error    // routes that can't be: two files for one, a name that isn't one
}

// scanAPI reads the app's api/: regular files in real folders only.
func (r reader) scanAPI() apiScan {
	var a apiScan
	dir := r.name("api")
	if st, err := lstat(r.fsys, dir); err != nil || !st.IsDir() {
		return a
	}
	var errs []string
	byKey := map[string]string{}
	n := 0
	_ = fs.WalkDir(r.fsys, dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel := strings.TrimPrefix(p, r.root+"/")
		if r.root == "." {
			rel = p
		}
		if d.IsDir() {
			if p != dir && (strings.HasPrefix(d.Name(), "_") || strings.HasPrefix(d.Name(), ".") || d.Name() == "node_modules") {
				return fs.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() || !apiSource(rel) {
			return nil
		}
		if n++; n > maxAPIFiles {
			return fs.SkipAll
		}
		a.sources = true
		b, err := r.read(rel)
		if err != nil {
			return nil
		}
		src := stripComments(b)
		if apiMiddleware(rel) {
			if a.middleware != "" {
				errs = append(errs, "two middleware files, "+a.middleware+" and "+rel+": keep one")
			}
			a.middleware = rel
			return nil
		}
		var methods []string
		for _, m := range slices.Concat(apiFuncRe.FindAllSubmatch(src, -1), apiConstRe.FindAllSubmatch(src, -1)) {
			methods = append(methods, string(m[1]))
		}
		fetch := false
		if loc := apiDefaultObjRe.FindIndex(src); loc != nil {
			fetch = apiFetchRe.Match(src[loc[1]:])
		}
		if len(methods) == 0 && !fetch {
			if apiReqResRe.Match(src) {
				a.reqRes = append(a.reqRes, rel)
			}
			return nil
		}
		for _, m := range apiConfigRe.FindAllSubmatch(src, -1) {
			a.notes = append(a.notes, rel+": export const "+string(m[1])+" is ignored - homeport doesn't read it")
		}
		route, key, err := apiRoute(rel)
		if err != nil {
			errs = append(errs, err.Error())
			return nil
		}
		if other, ok := byKey[key]; ok {
			errs = append(errs, other+" and "+rel+" are both "+route+": keep one")
			return nil
		}
		byKey[key] = rel
		a.named = a.named || len(methods) > 0
		methods = slices.DeleteFunc(slices.Clone(apiMethods), func(m string) bool { return !slices.Contains(methods, m) })
		a.routes = append(a.routes, APIRoute{Path: route, File: rel, Methods: methods, Fetch: fetch})
		return nil
	})
	if n > maxAPIFiles {
		errs = append(errs, fmt.Sprintf("api/ has more than %d source files: move what isn't a handler out, or into a folder starting with _", maxAPIFiles))
	}
	if len(errs) > 0 {
		a.err = errors.New("api/: " + strings.Join(errs, "; "))
	}
	slices.SortFunc(a.routes, func(x, y APIRoute) int { return strings.Compare(x.Path, y.Path) })
	return a
}

// apiRoute is a handler file's URL path, and its shape (params unnamed): two
// files of one shape are one route.
func apiRoute(rel string) (route, key string, err error) {
	parts := strings.Split(strings.TrimSuffix(rel, path.Ext(rel)), "/")
	if parts[len(parts)-1] == "index" {
		parts = parts[:len(parts)-1]
	}
	keys := []string{"api"}
	seen := map[string]bool{}
	for i, s := range parts[1:] {
		m := apiParamRe.FindStringSubmatch(s)
		switch {
		case m != nil && m[1] != "" && i != len(parts)-2:
			return "", "", fmt.Errorf("%s: a catch-all, %s, is only the last part of a route", rel, s)
		case m != nil && seen[m[2]]:
			return "", "", fmt.Errorf("%s: the param %s is in it twice", rel, m[2])
		case m != nil:
			seen[m[2]] = true
			keys = append(keys, "["+m[1]+"]")
		case strings.HasPrefix(s, "[["):
			return "", "", fmt.Errorf("%s: an optional catch-all ([[...x]]) isn't supported: use index.ts beside [...x].ts", rel)
		case !apiNameRe.MatchString(s):
			return "", "", fmt.Errorf("%s: %q isn't a route's name - letters, digits, . _ ~ -, or [param]", rel, s)
		default:
			keys = append(keys, s)
		}
	}
	return "/" + strings.Join(parts, "/"), "/" + strings.Join(keys, "/"), nil
}

// reqResError is the one line for a (req, res) handler.
func reqResError(file string) string {
	return file + " is a (req, res) handler, which homeport doesn't run: export function GET(req) instead"
}

// apiOff says why api/ is left alone whatever it holds, or "": what the
// person said, a project of its own in it, a Worker's, a workspace's root.
func (r reader) apiOff(cfg fileConfig, s Settings) string {
	for _, m := range apiManifests {
		if r.exists("api/" + m) {
			return "api/" + m + " makes it a project of its own"
		}
	}
	for _, f := range apiForeign {
		if r.exists(f) {
			return f + " says it's a Worker's or a function's project"
		}
	}
	switch {
	case s.API != nil && !*s.API:
		return "api: false in the build settings"
	case cfg.API != nil && !*cfg.API:
		return "api: false in " + ConfigFile
	case r.wsRoot:
		return "the repository's root is a workspace: set the root directory to the app's folder"
	case s.Run != "" || cfg.Run != "":
		return "a start command (run:) says what runs"
	case s.Kind != "" || s.Output != "":
		return "the build settings say what the build makes"
	case cfg.Build.Artifact != "" || cfg.Static != "" || cfg.Build.Image != "":
		return ConfigFile + " says what the build makes"
	}
	return ""
}

// apiJS says why a JavaScript project's api/ is left alone, or "": it has
// a server of its own, a framework that owns its routes, or says what runs.
func (r reader) apiJS(pkg pkgJSON, deps map[string]bool) string {
	if deps["@sveltejs/kit"] {
		return "SvelteKit owns its routes"
	}
	for _, c := range presets {
		if slices.ContainsFunc(c.deps, func(d string) bool { return deps[d] }) {
			return "this project has its own server (" + c.p.name + ")"
		}
	}
	for _, s := range servers {
		if deps[s] {
			return "this project has its own server (" + s + ")"
		}
	}
	if pkg.Scripts["start"] != "" {
		return "its start script says what runs"
	}
	if d, err := parseStart(pkg.Scripts, "dev", 0); err == nil && d.runtime == "bun" && d.file != "" {
		return "its dev script runs " + d.file
	}
	return ""
}

// apiAt is the api/ folder's server, at one of the points detection
// reaches: beside a site (site its folder), or API only (site "": a build
// may make one), which needs a method exported by name. nil when api/ is
// left alone; an error when it can't be built.
func (r reader) apiAt(cfg fileConfig, s Settings, site string) (*apiScan, error) {
	if r.apiOff(cfg, s) != "" {
		return nil, nil
	}
	if r.exists("package.json") && r.apiJS(r.pkg(), r.deps()) != "" {
		return nil, nil
	}
	a := r.scanAPI()
	if !a.sources {
		return nil, nil
	}
	if a.err != nil {
		return nil, a.err
	}
	if site == "" && !a.named {
		if len(a.reqRes) > 0 {
			return nil, errors.New(reqResError(a.reqRes[0]))
		}
		return nil, nil
	}
	if len(a.routes) == 0 {
		return nil, nil
	}
	return &a, nil
}

// apiWarnings: why api/ was left alone, for a JavaScript or plain HTML
// project that has handler files.
func (r reader) apiWarnings(p *Plan, cfg fileConfig, s Settings) []string {
	if p.API != nil {
		return nil
	}
	a := r.scanAPI()
	if !a.sources {
		return nil
	}
	var out []string
	for _, f := range a.reqRes {
		out = append(out, reqResError(f))
	}
	why := r.apiOff(cfg, s)
	if why == "" && r.exists("package.json") {
		why = r.apiJS(r.pkg(), r.deps())
		if why == "" && p.Compiled {
			why = "its build compiles its own server"
		}
		if why == "" && !r.hasLockfile() && r.ws == nil && len(r.deps()) > 0 {
			why = "package.json has dependencies and no lockfile: commit the lockfile"
		}
	}
	switch {
	case why != "" && (len(a.routes) > 0 || a.err != nil):
		out = append(out, "api/ left alone: "+why)
	case len(a.routes) == 0 && len(a.reqRes) == 0:
		out = append(out, "api/ left alone: no file exports GET, POST or another method")
	}
	return out
}

// apiApp makes p the server: the app's files and dependencies bundled as
// any JavaScript app's, then the handlers built into .homeport/api.mjs and
// the site (site, or a folder the build makes when "") copied beside it,
// its files in the public tree. pmName is the package manager, "" when
// there's nothing to install; runtime what a person said.
func (r reader) apiApp(p *Plan, a *apiScan, cfg fileConfig, site, pmName, runtime, runtimeFrom string) error {
	pkg := r.pkg()
	hasPkg := r.exists("package.json")
	var rt, why string
	switch {
	case runtime != "":
		rt, why = runtime, "set in "+runtimeFrom
	case !hasPkg:
		rt, why = "bun", "no package.json: nothing to install, so Bun"
	default:
		rt, why = r.runtimeFor(pkg, nil, start{}, errShell, "", "", "")
	}
	m := pm{run: map[string]string{"bun": "bun run", "node": "npm run"}[rt]}
	if pmName != "" {
		m = r.pmFor(pmName, pkg, true)
	}
	if hasPkg && (pmName == "bun" || rt == "bun") {
		if err := r.bunVersion(pkg); err != nil {
			return err
		}
	}
	p.setup, p.Install, p.Toolchain = "", "", "bun"
	bin, sums := "/usr/local/bin/bun", bunSum
	if rt == "bun" && (pmName == "bun" || pmName == "") {
		p.Image = BunImage
	} else {
		// Node's image, with Bun beside it: it builds the server
		nodeV, nodeWhy, err := r.nodeVersion(pkg)
		if err != nil && rt == "node" {
			return err
		}
		if err != nil {
			nodeV, nodeWhy = DefaultNode, ""
		}
		p.Toolchain, p.Image, p.setup = "node", nodeImage(nodeV), fetchBun
		if rt == "node" {
			bin, sums = "/usr/local/bin/node", nodeSum(nodeV)
			p.RuntimeVersion = nodeV
			if nodeWhy != "" {
				why += "; " + nodeWhy
			}
		} else {
			bin = "/tmp/homeport/bin/bun"
		}
	}
	if rt == "bun" {
		p.RuntimeVersion = BunVersion
	}
	p.setup = join(p.setup, m.setup)
	p.Install = join(p.setup, m.install)
	p.Runtime, p.RuntimeReason, p.PackageManager = rt, why, pmName
	p.Kind, p.Artifact, p.StaticFallback, p.Health = Bundle, BundleDir, false, "/"
	switch site {
	case "":
		p.Framework = "API"
	case ".":
		p.Framework = "HTML + API"
	default:
		p.Framework += " + API" // the site's framework
	}
	p.Run = runArgs(rt, ".homeport/api.mjs", nil, nil)
	p.API = &PlanAPI{Routes: a.routes, Middleware: a.middleware, Frontend: site}
	p.Warnings = append(p.Warnings, a.notes...)
	for _, f := range a.reqRes {
		p.Warnings = append(p.Warnings, reqResError(f))
	}

	build := cfg.Build.Command
	if build == "" && pkg.Scripts[r.buildScript()] != "" {
		build = m.run + " " + r.buildScript()
	}
	p.build, p.assemble = build, join(assemble("app", m, rt, bin, sums, "", false, nil, r.ws), apiStep(a, site, rt, hasPkg))
	p.Command = join(p.build, p.assemble)
	return nil
}

// apiOnly plans an app that is its api/ folder alone - no frontend, maybe
// no package.json - where detection would otherwise have failed: ok when
// it is one.
func (r reader) apiOnly(p *Plan, cfg fileConfig, s Settings) (bool, error) {
	a, err := r.apiAt(cfg, s, "")
	if a == nil || err != nil {
		return false, err
	}
	rt, from := saidRuntime(cfg, s)
	return true, r.apiApp(p, a, cfg, "", "", rt, from)
}

// saidRuntime is the runtime a person said, and where: the build settings,
// else homeport.yaml.
func saidRuntime(cfg fileConfig, s Settings) (string, string) {
	if s.Runtime == "" && cfg.Runtime != "" {
		return cfg.Runtime, ConfigFile
	}
	return s.Runtime, "the build settings"
}

// apiConfig is what the generator is handed (embed/api-server.mjs).
type apiConfig struct {
	Routes     []APIRoute `json:"routes"`
	Middleware string     `json:"middleware,omitempty"`
	Site       string     `json:"site,omitempty"`  // the frontend's folder
	Sites      []string   `json:"sites,omitempty"` // or the first of these the build made, with an index.html
	Target     string     `json:"target"`          // bun or node
	Bare       bool       `json:"bare,omitempty"`  // no package.json: no package imports
	Out        string     `json:"out"`             // the bundle
	Immutable  string     `json:"immutable"`
	// the body limit (bytes) and the time to a first byte (ms), when not
	// the server's own (32 MiB, 300 s): smaller in tests
	Limit   int `json:"limit,omitempty"`
	Timeout int `json:"timeout,omitempty"`
}

func newAPIConfig(a *apiScan, site, rt string, hasPkg bool) apiConfig {
	c := apiConfig{Routes: a.routes, Middleware: a.middleware, Site: site, Target: rt, Bare: !hasPkg, Out: BundleDir, Immutable: StaticImmutableRe}
	if site == "" && hasPkg {
		c.Sites = SiteFolders
	}
	return c
}

// apiStep builds the server into the bundle, then the site's files into its
// public tree (public.mjs's site kind), for the host to answer without the
// app.
func apiStep(a *apiScan, site, rt string, hasPkg bool) string {
	j, _ := json.Marshal(newAPIConfig(a, site, rt, hasPkg))
	const b = BundleDir
	gen := "{ printf '%s' '" + base64.StdEncoding.EncodeToString([]byte(apiServerMJS)) + "' | base64 -d > .homeport-api.mjs && " +
		"bun .homeport-api.mjs --homeport-api-build " + base64.StdEncoding.EncodeToString(j) + "; s=$?; rm -f .homeport-api.mjs .homeport-api-entry.mjs; [ $s = 0 ]; }"
	tree := "{ printf '%s' '" + base64.StdEncoding.EncodeToString([]byte(publicMJS)) + "' | base64 -d > .homeport-public.mjs && (cd " + b +
		" && ./bin ../.homeport-public.mjs site .homeport/site ../.output/edge.json '" + StaticImmutableRe + "') || rm -rf " + b + "/.homeport/public; rm -f .homeport-public.mjs; }"
	return gen + " && { [ ! -d " + b + "/.homeport/site ] || " + tree + "; }"
}
