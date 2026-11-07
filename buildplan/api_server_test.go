package buildplan_test

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/homeport-sh/cli/buildplan"
)

// The server an api/ folder is built into (embed/api-server.mjs): built
// by Bun from a real folder, as the build's step builds it, then run on
// each runtime and asked over HTTP. The body limit is 64 bytes here, the
// time to a first byte 500 ms; the server's own are 32 MiB and 300 s.

// apiFolder is an app of handlers, each checking one rule.
var apiFolder = map[string]string{
	"api/hello.ts": `import { greeting } from "./lib/greet";
export function GET(req: Request): Response {
  return Response.json({ hello: greeting })
}
`,
	"api/lib/greet.ts":       "export const greeting: string = 'world'\n",
	"api/posts/[id].ts":      "export const GET = (req, { params }) => Response.json(params)\n",
	"api/posts/new.ts":       "export const GET = () => Response.json({ new: true })\n",
	"api/files/[...path].ts": "export const GET = (req, ctx) => Response.json(ctx.params)\n",
	"api/echo.ts": `export async function POST(req: Request) {
  const body = await req.text()
  return Response.json({ got: body.length })
}
`,
	"api/slow.ts": `export async function GET(req: Request) {
  await new Promise((ok) => req.signal.addEventListener('abort', () => { (globalThis as any).aborted = true; ok(null) }))
  await new Promise((ok) => setTimeout(ok, 100))
  return new Response('late')
}
`,
	"api/aborted.ts": "export const GET = () => Response.json({ aborted: !!(globalThis as any).aborted })\n",
	"api/bad.ts": `export function GET() { return 'not a response' }
export function POST() { throw new Error('secret detail') }
`,
	"api/opts.ts": "export const GET = () => new Response('get')\n",
	"api/all.ts":  "export default {\n  fetch(req: Request) { return new Response(req.method) },\n}\n",
	"api/both.ts": "export const GET = () => new Response('named')\nexport default {\n  fetch: () => new Response('fetch'),\n}\n",
	"api/cookies.ts": `export function GET() {
  const h = new Headers()
  h.append('set-cookie', 'a=1; Path=/')
  h.append('set-cookie', 'b=2; Path=/')
  return new Response('ok', { headers: h })
}
`,
	"api/stream.ts": `export function GET() {
  let i = 0
  return new Response(new ReadableStream({
    async pull(c) { await new Promise((ok) => setTimeout(ok, 5)); if (i++ < 3) c.enqueue(new TextEncoder().encode('chunk' + i + ';')); else c.close() },
  }))
}
`,
	"api/conf.ts": "export const config = { runtime: 'edge' }\nexport const GET = () => new Response('ok')\n",
	"api/_middleware.ts": `export default function (req: Request) {
  if (req.headers.get('x-block')) return Response.json({ error: 'blocked' }, { status: 401 })
}
`,
}

// buildAPI runs the generator on files, into a bundle of its own: its
// output, and the bundle's folder.
func buildAPI(t *testing.T, files map[string]string, limit, timeout int) (string, string, error) {
	t.Helper()
	bun, err := exec.LookPath("bun")
	if err != nil {
		t.Skip("no bun")
	}
	app := t.TempDir()
	for name, body := range files {
		_ = os.MkdirAll(filepath.Dir(filepath.Join(app, name)), 0o755)
		if err := os.WriteFile(filepath.Join(app, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	p, err := buildplan.Detect(os.DirFS(app), buildplan.Settings{})
	if err != nil || p.API == nil {
		t.Fatalf("%v %+v", err, p)
	}
	b := filepath.Join(app, buildplan.BundleDir)
	if err := os.MkdirAll(filepath.Join(b, ".homeport"), 0o755); err != nil {
		t.Fatal(err)
	}
	// what the app layout copies: the app's files (no .env, as the step
	// removes)
	for name, body := range files {
		_ = os.MkdirAll(filepath.Dir(filepath.Join(b, name)), 0o755)
		_ = os.WriteFile(filepath.Join(b, name), []byte(body), 0o644)
	}
	gen := filepath.Join(app, ".homeport-api.mjs")
	if err := os.WriteFile(gen, []byte(buildplan.APIServer()), 0o644); err != nil {
		t.Fatal(err)
	}
	_, hasPkg := files["package.json"]
	cmd := exec.Command(bun, ".homeport-api.mjs", "--homeport-api-build", buildplan.APIBuildArg(p, hasPkg, buildplan.BundleDir, limit, timeout))
	cmd.Dir = app
	out, err := cmd.CombinedOutput()
	return string(out), b, err
}

// runAPI starts the bundle's server on rt: its address.
func runAPI(t *testing.T, rt, bundle string) string {
	t.Helper()
	bin, err := exec.LookPath(rt)
	if err != nil {
		t.Skip("no " + rt)
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	cmd := exec.Command(bin, ".homeport/api.mjs")
	cmd.Dir = bundle
	cmd.Env = append(os.Environ(), fmt.Sprintf("PORT=%d", port))
	var logs bytes.Buffer
	cmd.Stdout, cmd.Stderr = &logs, &logs
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		if t.Failed() {
			t.Logf("%s's logs:\n%s", rt, logs.String())
		}
	})
	addr := fmt.Sprintf("http://127.0.0.1:%d", port)
	for range 100 {
		if c, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port)); err == nil {
			c.Close()
			return addr
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("%s's server never listened:\n%s", rt, logs.String())
	return ""
}

type answer struct {
	status int
	header http.Header
	body   string
}

func ask(t *testing.T, method, url string, body io.Reader, header ...string) answer {
	t.Helper()
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	c := http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }, Timeout: 10 * time.Second}
	res, err := c.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return answer{res.StatusCode, res.Header, string(b)}
}

func TestTheAPIServer(t *testing.T) {
	out, bundle, err := buildAPI(t, apiFolder, 64, 500)
	if err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	// the log: the routes, public, and the gate
	for _, want := range []string{"==> api/: 14 handlers, built into a server. All are public: anyone can call them", "GET /api/hello",
		"POST /api/echo", "GET,POST /api/bad", "* /api/all", "api/_middleware.ts runs before each of them"} {
		if !strings.Contains(out, want) {
			t.Errorf("the log has no %q:\n%s", want, out)
		}
	}
	for _, rt := range []string{"bun", "node"} {
		t.Run(rt, func(t *testing.T) {
			u := runAPI(t, rt, bundle)
			check := func(a answer, status int, body string) {
				t.Helper()
				if a.status != status || body != "" && strings.TrimSpace(a.body) != body {
					t.Errorf("%d %q, want %d %q", a.status, a.body, status, body)
				}
			}
			check(ask(t, "GET", u+"/api/hello", nil), 200, `{"hello":"world"}`)
			a := ask(t, "HEAD", u+"/api/hello", nil) // GET's, no body
			check(a, 200, "")
			if a.body != "" || !strings.HasPrefix(a.header.Get("Content-Type"), "application/json") {
				t.Errorf("HEAD: %q %q", a.body, a.header)
			}
			a = ask(t, "GET", u+"/api/hello/?x=1", nil)
			if a.status != 308 || a.header.Get("Location") != "/api/hello?x=1" {
				t.Errorf("trailing slash: %d %q", a.status, a.header.Get("Location"))
			}
			// params: decoded; exact before [id] before [...x]
			check(ask(t, "GET", u+"/api/posts/42", nil), 200, `{"id":"42"}`)
			check(ask(t, "GET", u+"/api/posts/a%20b%2Fc", nil), 200, `{"id":"a b/c"}`)
			check(ask(t, "GET", u+"/api/posts/new", nil), 200, `{"new":true}`)
			check(ask(t, "GET", u+"/api/posts/%00", nil), 400, "")
			check(ask(t, "GET", u+"/api/posts/%E0%A4%41", nil), 400, "")
			check(ask(t, "GET", u+"/api/files/a/b/c.txt", nil), 200, `{"path":["a","b","c.txt"]}`)
			check(ask(t, "GET", u+"/api/files", nil), 404, `{"error":"not found"}`)
			check(ask(t, "GET", u+"/api/nope", nil), 404, `{"error":"not found"}`)
			// the body: counted as it arrives
			check(ask(t, "POST", u+"/api/echo", strings.NewReader("hello")), 200, `{"got":5}`)
			check(ask(t, "POST", u+"/api/echo", strings.NewReader(strings.Repeat("x", 65))), 413, "")
			check(ask(t, "POST", u+"/api/echo", io.MultiReader(strings.NewReader(strings.Repeat("x", 50)), strings.NewReader(strings.Repeat("y", 50)))), 413, "")
			// errors: no detail out
			a = ask(t, "GET", u+"/api/bad", nil)
			check(a, 500, `{"error":"internal error"}`)
			a = ask(t, "POST", u+"/api/bad", nil)
			check(a, 500, `{"error":"internal error"}`)
			// methods
			a = ask(t, "OPTIONS", u+"/api/opts", nil)
			if a.status != 204 || a.header.Get("Allow") != "GET, HEAD, OPTIONS" || a.header.Get("Access-Control-Allow-Origin") != "" {
				t.Errorf("OPTIONS: %d %q", a.status, a.header)
			}
			a = ask(t, "DELETE", u+"/api/opts", nil)
			if a.status != 405 || a.header.Get("Allow") != "GET, HEAD, OPTIONS" {
				t.Errorf("DELETE: %d %q", a.status, a.header)
			}
			check(ask(t, "PUT", u+"/api/all", nil), 200, "PUT")
			check(ask(t, "GET", u+"/api/both", nil), 200, "named")
			check(ask(t, "POST", u+"/api/both", nil), 200, "fetch")
			// the gate
			check(ask(t, "GET", u+"/api/hello", nil, "X-Block", "1"), 401, `{"error":"blocked"}`)
			check(ask(t, "GET", u+"/api/nope", nil, "X-Block", "1"), 404, "") // only matched routes
			// several cookies; a stream
			a = ask(t, "GET", u+"/api/cookies", nil)
			if c := a.header.Values("Set-Cookie"); !slices.Equal(c, []string{"a=1; Path=/", "b=2; Path=/"}) {
				t.Errorf("cookies: %q", c)
			}
			check(ask(t, "GET", u+"/api/stream", nil), 200, "chunk1;chunk2;chunk3;")
			check(ask(t, "GET", u+"/api/conf", nil), 200, "ok")
			// API only: / is up, the rest not found
			a = ask(t, "GET", u+"/", nil)
			if a.status != 200 || strings.TrimSpace(a.body) != `{"ok":true}` || a.header.Get("Cache-Control") != "no-store" {
				t.Errorf("/: %d %q %q", a.status, a.body, a.header)
			}
			a = ask(t, "GET", u+"/wp-login.php", nil)
			if a.status != 404 || !strings.HasPrefix(a.header.Get("Content-Type"), "text/html") {
				t.Errorf("outside /api: %d %q", a.status, a.header)
			}
			// the time to a first byte: 504, and the handler's signal aborts
			start := time.Now()
			check(ask(t, "GET", u+"/api/slow", nil), 504, "")
			if d := time.Since(start); d > 3*time.Second {
				t.Errorf("504 after %s", d)
			}
			time.Sleep(200 * time.Millisecond)
			check(ask(t, "GET", u+"/api/aborted", nil), 200, `{"aborted":true}`)
		})
	}
}

// With no package.json, a handler imports relative files and node:
// modules only: a package fails the build, named.
func TestTheAPIServerRefusesPackagesWithNoPackageJSON(t *testing.T) {
	out, _, err := buildAPI(t, map[string]string{"api/hello.ts": "import { z } from 'zod'\nimport fs from 'node:fs'\nexport const GET = () => new Response(String(z) + fs)\n"}, 0, 0)
	if err == nil || !strings.Contains(out, "api/hello.ts imports zod, a package, and there's no package.json") {
		t.Fatalf("%v\n%s", err, out)
	}
}

// A handler file that doesn't build fails the build, naming it.
func TestABrokenHandlerFailsTheBuild(t *testing.T) {
	out, _, err := buildAPI(t, map[string]string{"api/hello.ts": "export function GET() {\n  return new Response(\n"}, 0, 0)
	if err == nil || !strings.Contains(out, "api/hello.ts") {
		t.Fatalf("%v\n%s", err, out)
	}
}

// Beside a site: its files are served as a static site serves them, with
// a static site's caching; its pages go in the public tree; nothing of
// api/, the project's files or its dotfiles is served.
func TestTheAPIServerServesTheSite(t *testing.T) {
	files := map[string]string{
		"index.html":               "<h1>home</h1>",
		"about.html":               "<h1>about</h1>",
		"docs/index.html":          "<h1>docs</h1>",
		"404.html":                 "<h1>lost</h1>",
		"assets/app-1234abcd.js":   "console.log(1)",
		"style.css":                "body{}",
		".env":                     "SECRET=1",
		".well-known/security.txt": "Contact: x",
		"api/hi.ts":                "export const GET = () => new Response('hi')\n",
		"api/data.json":            "{}",
	}
	out, bundle, err := buildAPI(t, files, 0, 0)
	if err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	for _, want := range []string{"==> api/: 1 handler, built into a server", "no api/_middleware.ts: each handler does its own auth", "==> pages stay files", "==> this site now runs as an app"} {
		if !strings.Contains(out, want) {
			t.Errorf("the log has no %q:\n%s", want, out)
		}
	}
	if _, err := os.Stat(filepath.Join(bundle, ".env")); err == nil {
		t.Error("the bundle has the .env: Bun would load it")
	}
	for _, f := range []string{".env", "api/hi.ts", "api/data.json"} {
		if _, err := os.Stat(filepath.Join(bundle, ".homeport/site", f)); err == nil {
			t.Errorf("the site has %s", f)
		}
	}
	u := runAPI(t, "bun", bundle)
	for path, want := range map[string]string{"/": "<h1>home</h1>", "/about": "<h1>about</h1>", "/about.html": "<h1>about</h1>", "/docs": "<h1>docs</h1>",
		"/docs/": "<h1>docs</h1>", "/style.css": "body{}", "/.well-known/security.txt": "Contact: x"} {
		if a := ask(t, "GET", u+path, nil); a.status != 200 || a.body != want {
			t.Errorf("%s: %d %q", path, a.status, a.body)
		}
	}
	for path, cache := range map[string]string{"/": "no-cache", "/assets/app-1234abcd.js": "public, max-age=31536000, immutable", "/style.css": "public, max-age=3600"} {
		if a := ask(t, "GET", u+path, nil); a.header.Get("Cache-Control") != cache {
			t.Errorf("%s: %q", path, a.header.Get("Cache-Control"))
		}
	}
	if a := ask(t, "GET", u+"/style.css", nil); a.header.Get("Content-Type") != "text/css; charset=utf-8" {
		t.Errorf("type: %q", a.header.Get("Content-Type"))
	}
	// not files: its 404 page; api/ isn't the site's
	for _, path := range []string{"/nope", "/.env", "/api/hi.ts", "/package.json"} {
		if a := ask(t, "GET", u+path, nil); a.status != 404 || strings.Contains(a.body, "SECRET") || strings.Contains(a.body, "export") {
			t.Errorf("%s: %d %q", path, a.status, a.body)
		}
	}
	if a := ask(t, "GET", u+"/nope", nil); a.body != "<h1>lost</h1>" {
		t.Errorf("404 page: %q", a.body)
	}
	if a := ask(t, "GET", u+"/api/hi", nil); a.body != "hi" {
		t.Errorf("/api/hi: %d %q", a.status, a.body)
	}
	if a := ask(t, "POST", u+"/about", nil); a.status != 405 {
		t.Errorf("POST a page: %d", a.status)
	}
	// its public tree, for the host: what the server answers from a file,
	// the same file
	want := []string{"files /.well-known/security.txt", "files /404.html", "files /about.html", "files /docs/index.html", "files /index.html",
		"files /style.css", "immutable /assets/app-1234abcd.js", "pages /", "pages /404", "pages /about", "pages /docs", "pages /docs/"}
	if got := sitePublic(t, bundle); !slices.Equal(got, want) {
		t.Errorf("public tree:\n got  %q\n want %q", got, want)
	}

	// an SPA (index.html its only page): every path that isn't a file is it
	out, bundle, err = buildAPI(t, map[string]string{"index.html": "<div id=app>", "api/hi.ts": "export const GET = () => new Response('hi')\n"}, 0, 0)
	if err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	u = runAPI(t, "bun", bundle)
	if a := ask(t, "GET", u+"/settings/profile", nil); a.status != 200 || a.body != "<div id=app>" {
		t.Errorf("SPA: %d %q", a.status, a.body)
	}
}

// sitePublic runs public.mjs's site kind in a built bundle: its tree.
func sitePublic(t *testing.T, bundle string) []string {
	t.Helper()
	script, _ := filepath.Abs("embed/public.mjs")
	cmd := exec.Command("bun", script, "site", ".homeport/site", "", buildplan.StaticImmutableRe)
	cmd.Dir = bundle
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	var got []string
	root := filepath.Join(bundle, ".homeport/public")
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		tree, url, _ := strings.Cut(filepath.ToSlash(rel), "/")
		url = "/" + url
		if tree == "pages" {
			url = strings.TrimSuffix(url, ".html")
		}
		got = append(got, tree+" "+url)
		return nil
	})
	slices.Sort(got)
	return got
}

// The build's whole command, in the build's shell (Linux): the bundle is
// assembled, the server built into it with the site, its pages put in the
// public tree, nothing left behind - and the bundle runs as the host runs
// it, ./bin with the plan's args.
func TestTheAPIBuildForReal(t *testing.T) {
	bun, err := exec.LookPath("bun")
	if err != nil {
		t.Skip("no bun")
	}
	files := map[string]string{"index.html": "<h1>home</h1>", "about.html": "<h1>about</h1>", ".env": "SECRET=1",
		"api/hello.ts": "import { x } from './_lib/x'\nexport const GET = () => Response.json({ x, secret: process.env.SECRET ?? null })\n", "api/_lib/x.ts": "export const x = 1\n"}
	dir := t.TempDir()
	for name, body := range files {
		_ = os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755)
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	p := detect(t, files, buildplan.Settings{})
	// the image's Bun is the one on PATH here, unchecked
	cmd := strings.ReplaceAll(p.Command, "cp /usr/local/bin/bun ", "cp "+bun+" ")
	cmd = regexp.MustCompile(`case \$\(uname -m\) in x86_64\) s=[0-9a-f]+ ;; aarch64\) s=[0-9a-f]+ ;; \*\) s=none ;; esac && \{ echo .*? >&2; exit 1; \}; \} && `).ReplaceAllString(cmd, "")
	out, err := shell(t, dir, cmd)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(out, "==> api/: 1 handler") || !strings.Contains(out, "served without waking the app") {
		t.Errorf("the log:\n%s", out)
	}
	for _, f := range []string{".homeport-api.mjs", ".homeport-api-entry.mjs", ".homeport-public.mjs", buildplan.BundleDir + "/.env", buildplan.BundleDir + "/.homeport/site/.env"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err == nil {
			t.Errorf("%s is left", f)
		}
	}
	for _, f := range []string{buildplan.BundleDir + "/.homeport/public/pages/about.html", buildplan.BundleDir + "/.homeport/public/pages/.html", ".output/edge.json"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("no %s", f)
		}
	}
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	run := exec.Command("./bin", strings.Fields(p.Run)...)
	run.Dir = filepath.Join(dir, buildplan.BundleDir)
	run.Env = append(os.Environ(), fmt.Sprintf("PORT=%d", port))
	var logs bytes.Buffer
	run.Stdout, run.Stderr = &logs, &logs
	if err := run.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = run.Process.Kill(); _ = run.Wait() }()
	u := fmt.Sprintf("http://127.0.0.1:%d", port)
	for range 100 {
		if c, err := net.Dial("tcp", u[len("http://"):]); err == nil {
			c.Close()
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if a := ask(t, "GET", u+"/api/hello", nil); a.status != 200 || strings.TrimSpace(a.body) != `{"x":1,"secret":null}` {
		t.Errorf("/api/hello: %d %q\n%s", a.status, a.body, logs.String())
	}
	if a := ask(t, "GET", u+"/about", nil); a.body != "<h1>about</h1>" {
		t.Errorf("/about: %d %q", a.status, a.body)
	}
}
