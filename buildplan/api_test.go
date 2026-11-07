package buildplan_test

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/homeport-sh/cli/buildplan"
)

// An api/ folder of handler files is built into a small server: the app's
// own files, plus one generated entry (design/api-folder.md in cloud).

const hello = "export function GET(req: Request) {\n  return Response.json({ hello: 'world' })\n}\n"

// routes is the plan's routes as "METHODS /path file".
func routes(p buildplan.Plan) []string {
	if p.API == nil {
		return nil
	}
	var out []string
	for _, r := range p.API.Routes {
		m := strings.Join(r.Methods, ",")
		if r.Fetch {
			m = "fetch"
		}
		out = append(out, m+" "+r.Path+" "+r.File)
	}
	return out
}

// One handler file is a whole project: no package.json, no lockfile, no
// build. It runs on Bun (decision 5), as a bundle like any JavaScript app,
// health on / (which answers {"ok":true}).
func TestAnAPIFolderAloneIsAnApp(t *testing.T) {
	p := detect(t, map[string]string{"api/hello.ts": hello}, buildplan.Settings{})
	if p.Kind != buildplan.Bundle || p.Toolchain != "bun" || p.Image != buildplan.BunImage || p.Install != "" || p.Artifact != buildplan.BundleDir ||
		p.Runtime != "bun" || p.RuntimeVersion != buildplan.BunVersion || p.Health != "/" || p.Run != "--preload ./.homeport/boot.mjs .homeport/api.mjs" {
		t.Fatalf("%+v", p)
	}
	if got := routes(p); !slices.Equal(got, []string{"GET /api/hello api/hello.ts"}) {
		t.Fatalf("routes: %q", got)
	}
	if p.API.Frontend != "" || p.Framework != "API" || !strings.Contains(p.RuntimeReason, "no package.json") {
		t.Fatalf("%+v %+v", p, p.API)
	}
	// homeport's step builds it, after the bundle is assembled: nothing of
	// the app's to build
	if p.Build != "" || !strings.Contains(p.Command, "rm -rf "+buildplan.BundleDir) || !strings.Contains(p.Command, ".homeport-api.mjs") {
		t.Fatalf("command: %s", p.Command)
	}
	if strings.Index(p.Command, "rm -rf "+buildplan.BundleDir) > strings.Index(p.Command, ".homeport-api.mjs") {
		t.Fatalf("the server is built before the bundle is assembled: %s", p.Command)
	}
}

// A package.json with nothing to install needs no lockfile (decision 6);
// with dependencies, the lockfile rule stays. With a lockfile it installs
// as any JavaScript app, and a build script runs first.
func TestAnAPIOnlyProjectsPackageJSON(t *testing.T) {
	p := detect(t, map[string]string{"package.json": `{"type":"module"}`, "api/hello.ts": hello}, buildplan.Settings{})
	if p.Kind != buildplan.Bundle || p.Install == "" && p.Toolchain != "bun" || p.Runtime != "node" || len(routes(p)) != 1 {
		t.Fatalf("no dependencies: %+v", p)
	}
	// Node's image: the pinned Bun is fetched beside it, for the build step
	if p.Toolchain != "node" || !strings.Contains(p.Install, "bun-linux") || strings.Contains(p.Install, "npm ci") {
		t.Fatalf("no dependencies, on Node: %+v", p)
	}
	if p := detect(t, map[string]string{"package.json": `{"engines":{"bun":"1.4"}}`, "api/hello.ts": hello}, buildplan.Settings{}); p.Toolchain != "bun" || p.Runtime != "bun" {
		t.Fatalf("engines.bun: %+v", p)
	}
	_, err := buildplan.Detect(repo(map[string]string{"package.json": `{"dependencies":{"zod":"^4"}}`, "api/hello.ts": hello}), buildplan.Settings{})
	if err == nil || !strings.Contains(err.Error(), "lockfile") {
		t.Fatalf("dependencies, no lockfile: %v", err)
	}
	p = detect(t, js(`{"scripts":{"build":"tsc --noEmit"},"dependencies":{"zod":"^4"}}`, "api/hello.ts", hello), buildplan.Settings{})
	if p.Kind != buildplan.Bundle || !strings.Contains(p.Install, "npm ci") || !strings.HasPrefix(p.Command, "npm run build && ") ||
		!strings.Contains(p.Command, "npm prune --omit=dev") || p.Build != "npm run build" || len(routes(p)) != 1 {
		t.Fatalf("lockfile: %+v", p)
	}
	if p := detect(t, map[string]string{"package.json": `{"dependencies":{"zod":"^4"}}`, "bun.lock": "{}", "api/hello.ts": hello}, buildplan.Settings{}); p.Toolchain != "node" ||
		!strings.HasSuffix(p.Install, " && bun install --frozen-lockfile --linker=hoisted") || p.Runtime != "node" {
		// Bun installs; Node runs it, the default: Node's image, Bun beside it
		t.Fatalf("bun.lock: %+v", p)
	}
}

// With a frontend - a Vite build, plain HTML - the pages stay files and the
// handlers are built into the server beside them.
func TestAFrontendWithAnAPIFolder(t *testing.T) {
	p := detect(t, js(`{"scripts":{"build":"vite build"},"devDependencies":{"vite":"^7"}}`, "api/contact.ts",
		"export async function POST(req: Request) {\n  return Response.json({ ok: true })\n}\n"), buildplan.Settings{})
	if p.Kind != buildplan.Bundle || p.Framework != "Vite + API" || p.Runtime != "node" || p.API == nil || p.API.Frontend != "dist" ||
		!strings.HasPrefix(p.Command, "npm run build && ") || p.Health != "/" {
		t.Fatalf("vite: %+v %+v", p, p.API)
	}
	// its pages: the public tree, from the site the server keeps
	if !strings.Contains(p.Command, "public.mjs site .homeport/site") {
		t.Fatalf("vite: no public tree: %s", p.Command)
	}
	p = detect(t, map[string]string{"index.html": "<h1>hi</h1>", "api/contact.ts": "export const POST = async (req) => new Response('ok')\n"}, buildplan.Settings{})
	if p.Kind != buildplan.Bundle || p.Framework != "HTML + API" || p.Runtime != "bun" || p.API == nil || p.API.Frontend != "." || p.Install != "" {
		t.Fatalf("plain HTML: %+v %+v", p, p.API)
	}
	// the default export's fetch counts beside a frontend
	p = detect(t, map[string]string{"index.html": "x", "api/all.ts": "export default {\n  fetch(req) { return new Response('x') },\n}\n"}, buildplan.Settings{})
	if got := routes(p); !slices.Equal(got, []string{"fetch /api/all api/all.ts"}) {
		t.Fatalf("fetch: %q", got)
	}
	// with no handler, a site stays a site
	if p := detect(t, map[string]string{"index.html": "x", "api/data.json": "{}", "api/client.ts": "export function getPosts() {}\n"}, buildplan.Settings{}); p.Kind != buildplan.Static || p.API != nil {
		t.Fatalf("no handler: %+v", p)
	}
}

// What a handler file is: an upper-case method exported as a function, or
// a default export with fetch. Not a type file, a test, a file or folder
// starting with _ or ., or a link.
func TestWhatAHandlerFileIs(t *testing.T) {
	for src, want := range map[string]string{
		"export function GET() {}":                           "GET",
		"export async function POST(req) {}":                 "POST",
		"export const PUT = async (req) => {}":               "PUT",
		"export const PATCH = function (req) {}":             "PATCH",
		"export const DELETE = (req: Request) => {}":         "DELETE",
		"export const OPTIONS = req => new Response(null)":   "OPTIONS",
		"export const HEAD = async req => new Response()":    "HEAD",
		"export function GET() {}\nexport function POST(){}": "GET,POST",
		"export function getPosts() {}":                      "",
		"// export function GET() {}":                        "",
		"/*\nexport function GET() {}\n*/":                   "",
		"  export function GET() {}":                         "",
		"export const GET = 'x'":                             "",
		"export const GET = handler":                         "",
	} {
		p := detect(t, map[string]string{"index.html": "x", "api/x.ts": src}, buildplan.Settings{})
		got := ""
		if p.API != nil {
			got = strings.Join(p.API.Routes[0].Methods, ",")
		}
		if got != want {
			t.Errorf("%q: %q, want %q", src, got, want)
		}
	}
	files := map[string]string{"index.html": "x"}
	for _, f := range []string{"api/types.d.ts", "api/x.test.ts", "api/x.spec.js", "api/_lib/db.ts", "api/.hidden.ts", "api/x.tsx", "api/x.cjs", "api/x.json", "src/api/x.ts"} {
		files[f] = hello
	}
	if p := detect(t, files, buildplan.Settings{}); p.API != nil {
		t.Fatalf("none is a handler: %+v", p.API)
	}
	for _, f := range []string{"api/a.ts", "api/b.mts", "api/c.js", "api/d.mjs"} {
		if p := detect(t, map[string]string{"index.html": "x", f: hello}, buildplan.Settings{}); len(routes(p)) != 1 {
			t.Errorf("%s: %+v", f, p.API)
		}
	}
}

// Routes are file names: index is its folder, [id] a param, [...path] one or
// more segments. Two files for one route fail the build, naming both.
func TestRoutesFromFileNames(t *testing.T) {
	p := detect(t, map[string]string{
		"api/index.ts":            hello,
		"api/contact.ts":          hello,
		"api/posts/index.ts":      hello,
		"api/posts/[id].ts":       hello,
		"api/posts/new.ts":        hello,
		"api/files/[...path].mjs": hello,
		"api/lib/db.ts":           "export const db = 1\n",
	}, buildplan.Settings{})
	want := []string{"GET /api api/index.ts", "GET /api/contact api/contact.ts", "GET /api/files/[...path] api/files/[...path].mjs",
		"GET /api/posts api/posts/index.ts", "GET /api/posts/[id] api/posts/[id].ts", "GET /api/posts/new api/posts/new.ts"}
	if got := routes(p); !slices.Equal(got, want) {
		t.Fatalf("routes:\n got  %q\n want %q", got, want)
	}
	for name, files := range map[string][]string{
		"index and file":     {"api/posts.ts", "api/posts/index.ts"},
		"two extensions":     {"api/a.ts", "api/a.js"},
		"two params":         {"api/p/[id].ts", "api/p/[slug].ts"},
		"two catch-alls":     {"api/f/[...a].ts", "api/f/[...b].ts"},
		"a catch-all folder": {"api/[...a]/x.ts"},
		"optional":           {"api/[[...a]].ts"},
		"a space":            {"api/a b.ts"},
		"a param twice":      {"api/[id]/[id].ts"},
	} {
		m := map[string]string{}
		for _, f := range files {
			m[f] = hello
		}
		_, err := buildplan.Detect(repo(m), buildplan.Settings{})
		if err == nil || !strings.Contains(err.Error(), files[0]) || len(files) > 1 && !strings.Contains(err.Error(), files[1]) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// api/_middleware.ts is a gate, never a route, and alone it doesn't make
// a project an API.
func TestTheMiddlewareFile(t *testing.T) {
	mw := "export default function (req) {\n  if (!req.headers.get('authorization')) return new Response('no', { status: 401 })\n}\n"
	p := detect(t, map[string]string{"api/_middleware.ts": mw, "api/hello.ts": hello}, buildplan.Settings{})
	if p.API == nil || p.API.Middleware != "api/_middleware.ts" || len(p.API.Routes) != 1 {
		t.Fatalf("%+v", p.API)
	}
	if _, err := buildplan.Detect(repo(map[string]string{"api/_middleware.ts": mw}), buildplan.Settings{}); err == nil {
		t.Fatal("middleware alone is an app")
	}
	if _, err := buildplan.Detect(repo(map[string]string{"api/_middleware.ts": mw, "api/_middleware.js": mw, "api/a.ts": hello}), buildplan.Settings{}); err == nil ||
		!strings.Contains(err.Error(), "_middleware") {
		t.Fatalf("two: %v", err)
	}
}

// With no frontend there's nothing else to confirm the intent: a default
// export's fetch alone is a Worker's or a Bun script's shape, not an API.
func TestAPIOnlyNeedsANamedMethod(t *testing.T) {
	worker := "export default {\n  fetch(req) { return new Response('x') }\n}\n"
	if _, err := buildplan.Detect(repo(map[string]string{"api/index.ts": worker}), buildplan.Settings{}); err == nil {
		t.Fatal("fetch alone made an API-only app")
	}
	p := detect(t, map[string]string{"api/index.ts": worker, "api/hello.ts": hello}, buildplan.Settings{})
	if got := routes(p); !slices.Equal(got, []string{"fetch /api api/index.ts", "GET /api/hello api/hello.ts"}) {
		t.Fatalf("with a named one: %q", got)
	}
}

// (req, res) handlers are another contract, which homeport doesn't run
// (decision 7): API only, the build's error; beside a frontend, the site
// stays a site and the log names the file.
func TestReqResHandlersAreNamed(t *testing.T) {
	old := "export default function handler(req, res) {\n  res.json({ ok: true })\n}\n"
	_, err := buildplan.Detect(repo(map[string]string{"api/hello.ts": old}), buildplan.Settings{})
	if err == nil || !strings.Contains(err.Error(), "api/hello.ts is a (req, res) handler, which homeport doesn't run: export function GET(req) instead") {
		t.Fatalf("API only: %v", err)
	}
	p := detect(t, map[string]string{"index.html": "x", "api/hello.ts": old}, buildplan.Settings{})
	if p.Kind != buildplan.Static || !slices.ContainsFunc(p.Warnings, func(w string) bool { return strings.Contains(w, "api/hello.ts is a (req, res) handler") }) {
		t.Fatalf("with a frontend: %+v", p)
	}
	// beside web handlers: those are built, it's named
	p = detect(t, map[string]string{"api/hello.ts": hello, "api/old.js": "export default async (req, res) => { res.end() }\n"}, buildplan.Settings{})
	if len(routes(p)) != 1 || !slices.ContainsFunc(p.Warnings, func(w string) bool { return strings.Contains(w, "api/old.js is a (req, res) handler") }) {
		t.Fatalf("beside: %+v", p)
	}
}

// Nothing in the "never" table reads api/: a project with its own server,
// a framework that owns its routes, a project that says what runs, a
// project of its own in api/, a Worker, a workspace's root, api: false.
func TestAPIFolderIsLeftAlone(t *testing.T) {
	h := []string{"api/hello.ts", hello}
	for name, c := range map[string]struct {
		files map[string]string
		s     buildplan.Settings
		why   string // in a warning; "" none
	}{
		"next":        {js(`{"scripts":{"build":"next build","start":"next start"},"dependencies":{"next":"16"}}`, h...), buildplan.Settings{}, "its own server (Next.js)"},
		"hono":        {js(`{"scripts":{"start":"node server.js"},"dependencies":{"hono":"^4"}}`, h...), buildplan.Settings{}, "its own server (Hono)"},
		"express":     {js(`{"dependencies":{"express":"^5"},"main":"server.js"}`, h...), buildplan.Settings{}, "its own server (Express)"},
		"sveltekit":   {js(`{"scripts":{"build":"vite build"},"devDependencies":{"@sveltejs/kit":"^2","@sveltejs/adapter-static":"^3","vite":"^7"}}`, h...), buildplan.Settings{}, "SvelteKit"},
		"next export": {js(`{"scripts":{"build":"next build"},"dependencies":{"next":"16"}}`, append(h, "next.config.js", "export default { output: 'export' }")...), buildplan.Settings{}, "Next.js"},
		"start":       {js(`{"scripts":{"start":"node server.js"}}`, h...), buildplan.Settings{}, "start script"},
		"bun dev":     {js(`{"scripts":{"dev":"bun --watch src/index.ts"}}`, append(h, "bun.lock", "{}")...), buildplan.Settings{}, "dev script"},
		"run":         {map[string]string{"index.html": "x", "api/hello.ts": hello, "homeport.yaml": "run: x\n"}, buildplan.Settings{}, "run:"},
		"artifact":    {js(`{"scripts":{"build":"tsc"}}`, append(h, "homeport.yaml", "build:\n  artifact: server\n")...), buildplan.Settings{}, ""},
		"api project": {map[string]string{"index.html": "x", "api/hello.ts": hello, "api/package.json": "{}"}, buildplan.Settings{}, "api/package.json makes it a project of its own"},
		"api go":      {map[string]string{"index.html": "x", "api/hello.ts": hello, "api/go.mod": "module x\n"}, buildplan.Settings{}, "api/go.mod"},
		"wrangler":    {map[string]string{"index.html": "x", "api/hello.ts": hello, "wrangler.toml": "name = 'x'\n"}, buildplan.Settings{}, "wrangler.toml"},
		"serverless":  {map[string]string{"index.html": "x", "api/hello.ts": hello, "serverless.yml": "service: x\n"}, buildplan.Settings{}, "serverless.yml"},
		"yaml off":    {map[string]string{"index.html": "x", "api/hello.ts": hello, "homeport.yaml": "api: false\n"}, buildplan.Settings{}, "api: false in homeport.yaml"},
		"settings":    {map[string]string{"index.html": "x", "api/hello.ts": hello}, buildplan.Settings{API: new(bool)}, "api: false in the build settings"},
		"static":      {map[string]string{"index.html": "x", "api/hello.ts": hello}, buildplan.Settings{Kind: buildplan.Static, Output: "."}, ""},
		"go":          {map[string]string{"go.mod": "module x\n\ngo 1.24\n", "api/hello.ts": hello}, buildplan.Settings{}, ""},
		"composer":    {map[string]string{"composer.json": "{}", "composer.lock": "{}", "api/hello.ts": hello, "public/index.php": ""}, buildplan.Settings{}, ""},
		"python":      {map[string]string{"requirements.txt": "fastapi\n", "main.py": "", "api/hello.ts": hello}, buildplan.Settings{}, ""},
	} {
		p, err := buildplan.Detect(repo(c.files), c.s)
		if err != nil {
			continue // left alone, and not an app: as before
		}
		if p.API != nil || strings.Contains(p.Command, ".homeport-api.mjs") {
			t.Errorf("%s: built: %+v", name, p.API)
		}
		if c.why != "" && !slices.ContainsFunc(p.Warnings, func(w string) bool { return strings.HasPrefix(w, "api/ left alone: ") && strings.Contains(w, c.why) }) {
			t.Errorf("%s: no warning naming %q: %q", name, c.why, p.Warnings)
		}
	}
	// an API-only repo with api: false gets today's error
	_, err := buildplan.Detect(repo(map[string]string{"api/hello.ts": hello, "homeport.yaml": "api: false\n"}), buildplan.Settings{})
	if err == nil || !strings.Contains(err.Error(), "can't tell how to build this app") {
		t.Fatalf("api: false, API only: %v", err)
	}
	// a workspace's root plans its member, whose api/ the person picks by
	// its root directory
	ws := map[string]string{"package.json": `{"workspaces":["web"]}`, "package-lock.json": "{}",
		"web/package.json": `{"scripts":{"build":"vite build"},"devDependencies":{"vite":"^7"}}`, "web/api/hello.ts": hello}
	if p := detect(t, ws, buildplan.Settings{}); p.API != nil || p.Kind != buildplan.Static {
		t.Fatalf("workspace root: %+v", p)
	}
	if p := detect(t, ws, buildplan.Settings{Root: "web"}); p.API == nil {
		t.Fatalf("workspace member, picked: %+v", p)
	}
}

// api: false is a setting: one deploy's over the app's, in the JSON the
// dashboard saves.
func TestTheAPISetting(t *testing.T) {
	off := false
	s := buildplan.Settings{API: &off}
	if s.IsZero() {
		t.Fatal("api: false is something set")
	}
	if w := (buildplan.Settings{}).With(s); w.API == nil || *w.API {
		t.Fatalf("with: %+v", w)
	}
	if w := s.With(buildplan.Settings{}); w.API == nil || *w.API {
		t.Fatalf("kept: %+v", w)
	}
	b, _ := json.Marshal(s)
	if string(b) != `{"api":false}` {
		t.Fatalf("json: %s", b)
	}
}

// APIFiles: what a control plane fetches of a commit's tree, beside Files,
// to show the routes before the first build - handler candidates and what
// would leave api/ alone, at most 20.
func TestAPIFiles(t *testing.T) {
	paths := []string{"index.html", "api/hello.ts", "api/_middleware.ts", "api/_lib/db.ts", "api/x.d.ts", "api/x.test.ts", "api/package.json",
		"api/readme.md", "src/api/x.ts", "api/posts/[id].ts"}
	got := buildplan.APIFiles(paths)
	want := []string{"api/_middleware.ts", "api/hello.ts", "api/package.json", "api/posts/[id].ts"}
	if !slices.Equal(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
	var many []string
	for i := range 50 {
		many = append(many, "api/r"+string(rune('a'+i%26))+string(rune('a'+i/26))+".ts")
	}
	if n := len(buildplan.APIFiles(many)); n != 20 {
		t.Fatalf("%d of 50", n)
	}
	for _, f := range []string{"wrangler.toml", "wrangler.json", "wrangler.jsonc", "serverless.yml"} {
		if !slices.Contains(buildplan.Files(), f) {
			t.Errorf("Files has no %s", f)
		}
	}
}
