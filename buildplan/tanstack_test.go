package buildplan_test

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/homeport-sh/cli/buildplan"
)

// TanStack Start's vite build makes a server only with Nitro's Vite plugin:
// Nitro's .output, as Nuxt's, started at server/index.mjs. Its own
// dist/server/server.js is a fetch handler, not a server, so without Nitro
// it's the start script that serves it.
func TestTanStackStartShipsNitrosOutput(t *testing.T) {
	const viteNitro = "import { tanstackStart } from '@tanstack/react-start/plugin/vite'\nimport { nitro } from 'nitro/vite'\n" +
		"export default defineConfig({ plugins: [nitro(), tanstackStart(), viteReact()] })\n"
	for name, c := range map[string]struct {
		files   map[string]string
		runtime string
		run     string
		command []string
	}{
		"React, with Nitro": {js(`{"scripts":{"build":"vite build","dev":"vite dev --port 3000"},"dependencies":{"@tanstack/react-start":"1.168.61","@tanstack/react-router":"1.170.42","nitro":"3.0.260610-beta","react":"^19.2.0"},"devDependencies":{"vite":"^8.0.0"}}`,
			"vite.config.ts", viteNitro),
			"node", "--import ./.homeport/boot.mjs server/index.mjs", []string{"npm run build", ".output/server/index.mjs", ".output"}},
		"Solid, with Nitro, started on Bun": {js(`{"scripts":{"build":"vite build","start":"bun .output/server/index.mjs"},"dependencies":{"@tanstack/solid-start":"^1","nitro":"^3.0.0"},"devDependencies":{"vite":"^8"}}`, "bun.lock", "{}"),
			"bun", "--preload ./.homeport/boot.mjs server/index.mjs", []string{"bun run build", ".output"}},
		"Nitro's bun preset": {js(`{"scripts":{"build":"vite build"},"dependencies":{"@tanstack/react-start":"^1","nitro":"^3"}}`, "bun.lock", "{}",
			"vite.config.ts", "export default defineConfig({ plugins: [tanstackStart(), nitro({ preset: 'bun' }), viteReact()] })\n"),
			"bun", "--preload ./.homeport/boot.mjs server/index.mjs", []string{".output"}},
		"Nitro's node-server preset": {js(`{"scripts":{"build":"vite build"},"dependencies":{"@tanstack/react-start":"^1","nitro":"^3"}}`,
			"vite.config.ts", "export default defineConfig({ plugins: [tanstackStart(), nitro({ preset: \"node-server\" })] })\n"),
			"node", "--import ./.homeport/boot.mjs server/index.mjs", []string{".output"}},
		"without Nitro, served by srvx": {js(`{"scripts":{"build":"vite build","start":"srvx --prod -s ../client dist/server/server.js"},"dependencies":{"@tanstack/react-start":"^1","srvx":"^0.8"}}`),
			"node", "--import ./.homeport/boot.mjs .homeport/start.mjs --prod -s ../client dist/server/server.js",
			[]string{"npm run build", "npm prune --omit=dev", "node_modules/.bin/srvx"}},
		"without Nitro, TanStack's Bun server": {js(`{"scripts":{"build":"vite build","start":"bun run server.ts"},"dependencies":{"@tanstack/react-start":"^1"}}`, "bun.lock", "{}"),
			"bun", "--preload ./.homeport/boot.mjs server.ts", []string{"bun run build", "bun install --frozen-lockfile --production"}},
	} {
		p := detect(t, c.files, buildplan.Settings{})
		if p.Kind != buildplan.Bundle || p.Framework != "TanStack Start" || p.Runtime != c.runtime || p.Run != c.run ||
			p.Artifact != buildplan.BundleDir || p.StaticFallback || p.RuntimeReason == "" || p.Health != "/" {
			t.Errorf("%s: %+v", name, p)
			continue
		}
		at := 0
		for _, step := range c.command {
			i := strings.Index(p.Command[at:], step)
			if i < 0 {
				t.Errorf("%s: the build doesn't %q (after %d): %s", name, step, at, p.Command)
				break
			}
			at += i
		}
	}
}

// Without Nitro and without a start script - the official starter - the
// build's dist/server/server.js is a fetch handler with no server: homeport
// ships a small one of its own beside it, on Bun unless the project pins
// Node.
func TestTanStackStartWithNothingToServeItGetsHomeportsServer(t *testing.T) {
	const react = `{"scripts":{"build":"vite build","dev":"vite dev --port 3000","preview":"vite preview"},"dependencies":{"@tanstack/react-start":"1.168.61","react":"^19"},"devDependencies":{"vite":"^8"}}`
	for name, c := range map[string]struct {
		files   map[string]string
		runtime string
		run     string
		install string // in the install, when Bun runs it beside npm
	}{
		"the React starter, npm":            {js(react), "node", "--import ./.homeport/boot.mjs .homeport/tanstack-start.mjs", "npm ci"},
		"the React starter, bun.lock":       {js(react, "bun.lock", "{}"), "bun", "--preload ./.homeport/boot.mjs .homeport/tanstack-start.mjs", "bun install"},
		"bun.lock, and an .nvmrc pins Node": {js(react, "bun.lock", "{}", ".nvmrc", "24\n"), "node", "--import ./.homeport/boot.mjs .homeport/tanstack-start.mjs", "bun-linux-"},
		"Solid, bun.lock": {js(`{"scripts":{"build":"vite build"},"dependencies":{"@tanstack/solid-start":"^1","solid-js":"^1.9"},"devDependencies":{"vite":"^8"}}`, "bun.lock", "{}"),
			"bun", "--preload ./.homeport/boot.mjs .homeport/tanstack-start.mjs", "bun install"},
		"an .nvmrc pins Node": {js(react, ".nvmrc", "24\n"), "node", "--import ./.homeport/boot.mjs .homeport/tanstack-start.mjs", "npm ci"},
		"engines.node pins Node": {js(`{"scripts":{"build":"vite build"},"engines":{"node":">=22"},"dependencies":{"@tanstack/react-start":"^1"}}`),
			"node", "--import ./.homeport/boot.mjs .homeport/tanstack-start.mjs", "npm ci"},
	} {
		p := detect(t, c.files, buildplan.Settings{})
		if p.Kind != buildplan.Bundle || p.Framework != "TanStack Start" || p.Runtime != c.runtime || p.Run != c.run ||
			!strings.Contains(p.RuntimeReason, "homeport's server") || !strings.Contains(p.Install, c.install) || p.Health != "/" {
			t.Errorf("%s: %+v", name, p)
			continue
		}
		// the build checks it made the handler, and the server is written
		// into the bundle, after the production dependencies
		at := 0
		for _, step := range []string{"run build", "dist/server/server.js", "--production", "> " + buildplan.BundleDir + "/.homeport/tanstack-start.mjs"} {
			if _, bun := c.files["bun.lock"]; step == "--production" && !bun {
				step = "npm prune --omit=dev"
			}
			i := strings.Index(p.Command[at:], step)
			if i < 0 {
				t.Errorf("%s: the build doesn't %q (after %d): %s", name, step, at, p.Command)
				break
			}
			at += i
		}
	}
	// a Vite base is the server's: its files are under it
	p := detect(t, js(react, "vite.config.ts", "export default defineConfig({ base: '/app/', plugins: [tanstackStart({ router: { basepath: '/app' } })] })\n"), buildplan.Settings{})
	if p.Run != "--import ./.homeport/boot.mjs .homeport/tanstack-start.mjs /app/" {
		t.Errorf("base: %+v", p)
	}
	// a start command of the person's own still wins
	p = detect(t, js(react), buildplan.Settings{Run: "--import ./.homeport/boot.mjs server.mjs"})
	if p.Run != "--import ./.homeport/boot.mjs server.mjs" || strings.Contains(p.Command, "tanstack-start.mjs") {
		t.Errorf("set run: %+v", p)
	}
}

// tanStackServer is the server homeport ships for a TanStack Start app on
// the runtime, as its plan writes it.
func tanStackServer(t *testing.T, rt string) string {
	t.Helper()
	files := js(`{"scripts":{"build":"vite build"},"dependencies":{"@tanstack/react-start":"^1"}}`)
	if rt == "bun" {
		files = js(`{"scripts":{"build":"vite build"},"dependencies":{"@tanstack/react-start":"^1"}}`, "bun.lock", "{}")
	}
	p := detect(t, files, buildplan.Settings{})
	if p.Runtime != rt {
		t.Fatalf("runtime %s: %+v", rt, p)
	}
	end := strings.Index(p.Command, "' | base64 -d | gzip -dc > "+buildplan.BundleDir+"/.homeport/tanstack-start.mjs")
	if end < 0 {
		t.Fatalf("no server written: %s", p.Command)
	}
	begin := strings.LastIndex(p.Command[:end], "printf '%s' '")
	z, err := base64.StdEncoding.DecodeString(p.Command[begin+len("printf '%s' '") : end])
	if err != nil {
		t.Fatal(err)
	}
	r, err := gzip.NewReader(bytes.NewReader(z))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(r)
	return string(b)
}

// homeport's server serves dist/client's files - hashed assets cached for
// good, each with its type, under the Vite base it's given - and hands
// every other request to the handler, with the URL the browser used: the
// Host, and the scheme from X-Forwarded-Proto. A visitor's X-Forwarded-Host
// and X-Forwarded-For change nothing. It caps request bodies, shows no
// error pages, and listens on PORT and HOST, on Node and Bun.
func TestHomeportsTanStackStartServer(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		f := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(f, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("dist/client/assets/main-B2jnoNjx.js", "console.log(1)")
	write("dist/client/assets/styles-Cx6X9hdU.css", "body{}")
	write("dist/client/robots.txt", "User-agent: *")
	write("package.json", `{"type":"module"}`)
	write("secret.txt", "not served")
	write("dist/server/server.js", `export default { async fetch(req) {
  const u = new URL(req.url)
  if (u.pathname === "/nope") return new Response("not found", { status: 404, headers: { "content-type": "text/html" } })
  if (u.pathname === "/crash") throw new Error("kaboom")
  const h = new Headers({ "content-type": "application/json" })
  h.append("set-cookie", "a=1; Path=/")
  h.append("set-cookie", "b=2; Path=/")
  return new Response(JSON.stringify({ url: req.url, method: req.method, ip: req.ip ?? null, body: await req.text() }), { headers: h })
} }
`)
	ran := 0
	for rt, port := range map[string]string{"node": "39881", "bun": "39882"} {
		bin, err := exec.LookPath(rt)
		if err != nil {
			continue
		}
		ran++
		write(".homeport/tanstack-start.mjs", tanStackServer(t, rt))
		var out strings.Builder
		// start runs the server (with args), its env without NODE_ENV, so
		// nothing depends on the app's
		start := func(port string, args ...string) *exec.Cmd {
			cmd := exec.Command(bin, append([]string{".homeport/tanstack-start.mjs"}, args...)...)
			var env []string
			for _, e := range os.Environ() {
				if !strings.HasPrefix(e, "NODE_ENV=") {
					env = append(env, e)
				}
			}
			cmd.Dir, cmd.Env = dir, append(env, "PORT="+port, "HOST=127.0.0.1", "BODY_SIZE_LIMIT=4096")
			cmd.Stdout, cmd.Stderr = &out, &out
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 100; i++ {
				if r, err := http.Get("http://127.0.0.1:" + port + "/x"); err == nil {
					r.Body.Close()
					break
				}
				time.Sleep(50 * time.Millisecond)
			}
			return cmd
		}
		cmd := start(port)
		base := "http://127.0.0.1:" + port
		get := func(method, path, body string, h map[string]string) (*http.Response, string) {
			req, _ := http.NewRequest(method, base+path, strings.NewReader(body))
			for k, v := range h {
				if k == "Host" {
					req.Host = v
				}
				req.Header.Set(k, v)
			}
			r, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("%s %s %s: %v (%s)", rt, method, path, err, out.String())
			}
			b, _ := io.ReadAll(r.Body)
			r.Body.Close()
			return r, string(b)
		}
		r, b := get("GET", "/assets/main-B2jnoNjx.js", "", nil)
		if r.StatusCode != 200 || b != "console.log(1)" || !strings.HasPrefix(r.Header.Get("Content-Type"), "text/javascript") ||
			r.Header.Get("Cache-Control") != "max-age=31536000, immutable" {
			t.Errorf("%s: asset %d %v %q", rt, r.StatusCode, r.Header, b)
		}
		if r, _ := get("GET", "/assets/styles-Cx6X9hdU.css", "", nil); !strings.HasPrefix(r.Header.Get("Content-Type"), "text/css") {
			t.Errorf("%s: css %v", rt, r.Header)
		}
		r, b = get("GET", "/robots.txt", "", nil)
		if r.StatusCode != 200 || b != "User-agent: *" || !strings.HasPrefix(r.Header.Get("Content-Type"), "text/plain") || strings.Contains(r.Header.Get("Cache-Control"), "immutable") {
			t.Errorf("%s: robots %d %v %q", rt, r.StatusCode, r.Header, b)
		}
		if r, b := get("HEAD", "/robots.txt", "", nil); r.StatusCode != 200 || b != "" {
			t.Errorf("%s: HEAD %d %q", rt, r.StatusCode, b)
		}
		// nothing outside dist/client: those go to the handler
		for _, path := range []string{"/../../secret.txt", "/%2e%2e/%2e%2e/secret.txt", "/assets/", "/"} {
			if _, b := get("GET", path, "", nil); strings.Contains(b, "not served") || !strings.Contains(b, `"method":"GET"`) {
				t.Errorf("%s: %s: %q", rt, path, b)
			}
		}
		// the URL is the browser's, through the edge: its Host, https
		r, b = get("POST", "/_serverFn/abc?x=1", "payload", map[string]string{"Host": "app.example.com", "X-Forwarded-Proto": "https"})
		if r.StatusCode != 200 || !strings.Contains(b, `"url":"https://app.example.com/_serverFn/abc?x=1"`) || !strings.Contains(b, `"method":"POST"`) || !strings.Contains(b, `"body":"payload"`) {
			t.Errorf("%s: handler %d %q", rt, r.StatusCode, b)
		}
		// a visitor's own X-Forwarded-Host and X-Forwarded-For, kept by
		// proxies that trust their peer, are ignored
		_, b = get("GET", "/reset", "", map[string]string{"Host": "app.example.com", "X-Forwarded-Proto": "https",
			"X-Forwarded-Host": "evil.example, app.example.com", "X-Forwarded-For": "6.6.6.6, 1.2.3.4, 10.0.0.2"})
		if !strings.Contains(b, `"url":"https://app.example.com/reset"`) || strings.Contains(b, "evil") || strings.Contains(b, "6.6.6.6") {
			t.Errorf("%s: spoofed: %q", rt, b)
		}
		// an error is a bare 500, with no development page, whatever NODE_ENV
		if r, b := get("GET", "/crash", "", nil); r.StatusCode != 500 || strings.Contains(b, "kaboom") || strings.Contains(b, "server.js") {
			t.Errorf("%s: crash %d %q", rt, r.StatusCode, b)
		}
		// BODY_SIZE_LIMIT caps a body
		if r, b := get("POST", "/upload", strings.Repeat("x", 10000), nil); r.StatusCode == 200 {
			t.Errorf("%s: a body past the limit: %d %q", rt, r.StatusCode, b[:min(len(b), 80)])
		}
		if c := r.Header.Values("Set-Cookie"); len(c) != 2 {
			t.Errorf("%s: cookies %v", rt, c)
		}
		// a POST to an asset's path is the handler's
		if _, b := get("POST", "/robots.txt", "x", nil); !strings.Contains(b, `"method":"POST"`) {
			t.Errorf("%s: POST asset %q", rt, b)
		}
		// without forwarded headers, the Host it was sent to
		if _, b := get("GET", "/x", "", nil); !strings.Contains(b, `"url":"http://127.0.0.1:`+port+`/x"`) {
			t.Errorf("%s: plain %q", rt, b)
		}
		if r, _ := get("GET", "/nope", "", nil); r.StatusCode != 404 {
			t.Errorf("%s: 404 %d", rt, r.StatusCode)
		}
		_ = cmd.Process.Kill()
		_ = cmd.Wait()

		// under a Vite base, the files are under it
		based := map[string]string{"node": "39883", "bun": "39884"}[rt]
		cmd = start(based, "/app/")
		base = "http://127.0.0.1:" + based
		if r, b := get("GET", "/app/assets/main-B2jnoNjx.js", "", nil); r.StatusCode != 200 || b != "console.log(1)" || r.Header.Get("Cache-Control") != "max-age=31536000, immutable" {
			t.Errorf("%s: base asset %d %v %q", rt, r.StatusCode, r.Header, b)
		}
		if r, b := get("GET", "/app/robots.txt", "", nil); r.StatusCode != 200 || b != "User-agent: *" {
			t.Errorf("%s: base robots %d %q", rt, r.StatusCode, b)
		}
		if _, b := get("GET", "/assets/main-B2jnoNjx.js", "", nil); !strings.Contains(b, `"method":"GET"`) {
			t.Errorf("%s: outside the base: %q", rt, b)
		}
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}
	if ran == 0 {
		t.Skip("no node or bun here")
	}
}

// A Nitro preset for someone else's platform makes no server to run.
func TestTanStackStartOnAnotherPlatformsNitroPresetIsRefused(t *testing.T) {
	_, err := buildplan.Detect(repo(js(`{"scripts":{"build":"vite build"},"dependencies":{"@tanstack/react-start":"^1","nitro":"^3"}}`,
		"vite.config.ts", "export default defineConfig({ plugins: [tanstackStart(), nitro({ preset: 'vercel' })] })\n")), buildplan.Settings{})
	if err == nil || !strings.Contains(err.Error(), "vercel") || !strings.Contains(err.Error(), "node-server") {
		t.Fatalf("%v", err)
	}
	// in nitro.config, Nitro's own config file, too
	_, err = buildplan.Detect(repo(js(`{"scripts":{"build":"vite build"},"dependencies":{"@tanstack/react-start":"^1","nitro":"^3"}}`,
		"vite.config.ts", "export default defineConfig({ plugins: [tanstackStart(), nitro()] })\n", "nitro.config.ts", "export default defineConfig({ preset: `netlify` })\n")), buildplan.Settings{})
	if err == nil || !strings.Contains(err.Error(), "netlify") || !strings.Contains(err.Error(), "nitro.config.ts") {
		t.Fatalf("nitro.config: %v", err)
	}
	// node-cluster writes .output/server/index.mjs, as node-server does; bun
	// in nitro.config is Bun
	for preset, rt := range map[string]string{"node-cluster": "node", "node_cluster": "node", "bun": "bun"} {
		p := detect(t, js(`{"scripts":{"build":"vite build"},"dependencies":{"@tanstack/react-start":"^1","nitro":"^3"}}`,
			"vite.config.ts", "export default defineConfig({ plugins: [tanstackStart(), nitro()] })\n", "nitro.config.mjs", "export default { preset: '"+preset+"' }\n"), buildplan.Settings{})
		if p.Runtime != rt || p.Run != map[string]string{"node": "--import", "bun": "--preload"}[rt]+" ./.homeport/boot.mjs server/index.mjs" {
			t.Errorf("%s: %+v", preset, p)
		}
	}
	// a preset elsewhere in the config (a comment, another plugin) isn't Nitro's
	p := detect(t, js(`{"scripts":{"build":"vite build"},"dependencies":{"@tanstack/react-start":"^1","nitro":"^3"}}`,
		"vite.config.ts", "// nitro({ preset: 'vercel' })\nexport default defineConfig({ plugins: [other({ preset: 'x' }), tanstackStart(), nitro()] })\n"), buildplan.Settings{})
	if p.Framework != "TanStack Start" || p.Runtime != "node" {
		t.Fatalf("%+v", p)
	}
}

// A Vite app with TanStack Router but not Start is a single-page app: still
// a static site.
func TestTanStackRouterWithoutStartIsStatic(t *testing.T) {
	p := detect(t, js(`{"scripts":{"build":"vite build && tsc"},"dependencies":{"@tanstack/react-router":"^1.170","react":"^19"},"devDependencies":{"@tanstack/router-plugin":"^1","vite":"^8"}}`,
		"vite.config.ts", "export default defineConfig({ plugins: [tanstackRouter({ target: 'react' }), viteReact()] })\n"), buildplan.Settings{})
	if p.Kind != buildplan.Static || p.Framework != "Vite" || p.Artifact != "dist" {
		t.Fatalf("%+v", p)
	}
}

// bun build --compile of Nitro's server is the server, compiled: on Nitro's
// bun preset, its public assets inlined.
func TestTanStackStartsCompiledNitroServerIsABinary(t *testing.T) {
	p := detect(t, js(`{"scripts":{"build":"vite build && bun build --compile .output/server/index.mjs --outfile server"},"dependencies":{"@tanstack/react-start":"^1","nitro":"^3"}}`, "bun.lock", "{}",
		"vite.config.ts", "export default defineConfig({ plugins: [tanstackStart(), nitro({ preset: 'bun', serveStatic: 'inline' })] })\n"), buildplan.Settings{})
	if p.Kind != buildplan.Binary || p.Framework != "TanStack Start" || p.Artifact != "server" || p.Runtime != "bun" {
		t.Fatalf("%+v", p)
	}
}
