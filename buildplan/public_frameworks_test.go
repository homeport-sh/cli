package buildplan_test

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/homeport-sh/cli/buildplan"
)

// The content-hashed assets Next.js standalone, react-router-serve and
// remix-serve answer from files - the same Cache-Control, a year and
// immutable, and none of the app's code before them - go in the bundle's
// immutable tree too, for the host to answer (and the builder to compress
// once) without waking the app. Only those: a URL the app's middleware,
// proxy, conditional headers, redirects or rewrites could take is left to
// the app; Next.js config's headers go with the files (routes.json).
// The manifests are as Next.js 16's standalone build and React Router 7's
// and Remix 2's Vite builds write them.

func jsonOf(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// nextStandalone is a standalone build's manifests: the routes (headers,
// redirects, rewrites), the config, and middleware (Edge) and proxy (Node)
// manifests - with no middleware unless set.
func nextStandalone(at string, config map[string]any, routes map[string]any, middleware, functions map[string]any) map[string]string {
	cfg := map[string]any{"basePath": "", "assetPrefix": "", "i18n": nil, "trailingSlash": false}
	for k, v := range config {
		cfg[k] = v
	}
	rm := map[string]any{"version": 3, "caseSensitive": false, "basePath": cfg["basePath"],
		"redirects":      []any{map[string]any{"source": "/:path+/", "destination": "/:path+", "internal": true, "statusCode": 308, "regex": "^(?:/((?:[^/]+?)(?:/(?:[^/]+?))*))/$"}},
		"headers":        []any{},
		"onMatchHeaders": []any{},
		"rewrites":       map[string]any{"beforeFiles": []any{}, "afterFiles": []any{}, "fallback": []any{}}}
	for k, v := range routes {
		rm[k] = v
	}
	if middleware == nil {
		middleware = map[string]any{}
	}
	if functions == nil {
		functions = map[string]any{}
	}
	p := at + ".next/"
	files := map[string]string{
		p + "routes-manifest.json":                    jsonOf(rm),
		p + "required-server-files.json":              jsonOf(map[string]any{"version": 1, "config": cfg}),
		p + "server/middleware-manifest.json":         jsonOf(map[string]any{"version": 3, "middleware": middleware, "sortedMiddleware": []string{}, "functions": map[string]any{}}),
		p + "server/functions-config-manifest.json":   jsonOf(map[string]any{"version": 1, "functions": functions}),
		p + "static/chunks/0a1b2c3d.js":               "chunk",
		p + "static/chunks/app/page-4e5f.js":          "page",
		p + "static/css/9f8e7d.css":                   "css",
		p + "static/media/font.p.woff2":               "font",
		p + "static/kV3x9Q/_buildManifest.js":         "build manifest",
		p + "static/service-worker/sw.js":             "a worker: its own headers",
		p + "static/chunks/app/blog/[slug]/page-1.js": "a URL only the app reads",
		p + "static/chunks/0a1b2c3d.js.br":            "a copy: never a URL of the tree's",
		at + "public/favicon.ico":                     "not hashed: max-age=0, the app's",
		at + "server.js":                              "the server",
	}
	return files
}

var nextAssets = []string{
	"immutable /_next/static/chunks/0a1b2c3d.js",
	"immutable /_next/static/chunks/app/page-4e5f.js",
	"immutable /_next/static/css/9f8e7d.css",
	"immutable /_next/static/media/font.p.woff2",
	"immutable /_next/static/kV3x9Q/_buildManifest.js",
}

func TestNextStandaloneStaticAssetsAreServedWithoutTheApp(t *testing.T) {
	got, out := public(t, "next", ".", nextStandalone("", nil, nil, nil, nil))
	same(t, got, nextAssets, out)
	for _, u := range []string{"/_next/static/service-worker/sw.js", "/_next/static/chunks/0a1b2c3d.js.br"} {
		if !strings.Contains(out, "left to the app: "+u) {
			t.Errorf("the build log doesn't say %s is the app's:\n%s", u, out)
		}
	}
	if strings.Contains(out, "edge.json:") {
		t.Errorf("an edge marker with no pages:\n%s", out)
	}

	t.Run("a base path", func(t *testing.T) {
		got, out := public(t, "next", ".", nextStandalone("", map[string]any{"basePath": "/docs"}, nil, nil, nil))
		var want []string
		for _, a := range nextAssets {
			want = append(want, strings.Replace(a, " /", " /docs/", 1))
		}
		same(t, got, want, out)
	})

	t.Run("a workspace member", func(t *testing.T) {
		got, out := public(t, "next", "apps/web", nextStandalone("apps/web/", nil, nil, nil, nil))
		same(t, got, nextAssets, out)
	})

	// middleware whose matcher leaves out /_next/static (Next's own
	// example): the assets are the server's still
	t.Run("middleware that skips them", func(t *testing.T) {
		mw := map[string]any{"/": map[string]any{"files": []string{"server/middleware.js"}, "name": "middleware", "page": "/",
			"matchers": []any{map[string]any{"regexp": `^(?:\/(_next\/data\/[^/]{1,}))?(?:\/((?!_next\/static|_next\/image|favicon.ico).*))(\.json)?[\/#\?]?$`,
				"originalSource": "/((?!_next/static|_next/image|favicon.ico).*)"}}}}
		got, out := public(t, "next", ".", nextStandalone("", nil, nil, mw, nil))
		same(t, got, nextAssets, out)
	})
}

func TestWhatNextsCodeCanTakeIsLeftToTheApp(t *testing.T) {
	all := func(t *testing.T, got []string, out, why string) {
		t.Helper()
		same(t, got, nil, out)
		if !strings.Contains(out, why) {
			t.Errorf("the build log doesn't say why (%q):\n%s", why, out)
		}
	}
	t.Run("middleware on every path", func(t *testing.T) {
		mw := map[string]any{"/": map[string]any{"name": "middleware", "page": "/", "matchers": []any{map[string]any{"regexp": "^/.*$", "originalSource": "/:path*"}}}}
		got, out := public(t, "next", ".", nextStandalone("", nil, nil, mw, nil))
		all(t, got, out, "its middleware runs for it")
	})
	t.Run("a proxy (Node middleware) with no matcher", func(t *testing.T) {
		fn := map[string]any{"/_middleware": map[string]any{"runtime": "nodejs", "matchers": []any{map[string]any{"regexp": "^.*$", "originalSource": "/:path*"}}}}
		got, out := public(t, "next", ".", nextStandalone("", nil, nil, nil, fn))
		all(t, got, out, "its middleware runs for it")
	})
	t.Run("middleware whose matcher has conditions", func(t *testing.T) {
		mw := map[string]any{"/": map[string]any{"name": "middleware", "page": "/", "matchers": []any{map[string]any{"regexp": `^/_next/static/css(?:/.*)?$`,
			"has": []any{map[string]any{"type": "header", "key": "x-a"}}}}}}
		got, out := public(t, "next", ".", nextStandalone("", nil, nil, mw, nil))
		var want []string
		for _, a := range nextAssets {
			if !strings.Contains(a, "/css/") {
				want = append(want, a)
			}
		}
		same(t, got, want, out)
	})
	t.Run("a header rule with has or missing", func(t *testing.T) {
		got, out := public(t, "next", ".", nextStandalone("", nil, map[string]any{"headers": []any{map[string]any{"source": "/_next/static/chunks/:path*",
			"regex": `^/_next/static/chunks(?:/((?:[^/]+?)(?:/(?:[^/]+?))*))?(?:/)?$`, "has": []any{map[string]any{"type": "query", "key": "v"}},
			"headers": []any{map[string]any{"key": "x-a", "value": "b"}}}}}, nil, nil))
		same(t, got, []string{"immutable /_next/static/css/9f8e7d.css", "immutable /_next/static/media/font.p.woff2", "immutable /_next/static/kV3x9Q/_buildManifest.js"}, out)
		if !strings.Contains(out, "left to the app: /_next/static/chunks/0a1b2c3d.js - a header rule with has or missing matches it") {
			t.Errorf("the build log doesn't say why:\n%s", out)
		}
	})
	t.Run("a beforeFiles rewrite", func(t *testing.T) {
		got, out := public(t, "next", ".", nextStandalone("", nil, map[string]any{"rewrites": map[string]any{"beforeFiles": []any{map[string]any{
			"source": "/_next/static/css/:p", "destination": "/x", "regex": `^/_next/static/css(?:/([^/]+?))(?:/)?$`}}, "afterFiles": []any{}, "fallback": []any{}}}, nil, nil))
		if len(got) != len(nextAssets)-1 || strings.Contains(strings.Join(got, " "), "/css/") {
			t.Errorf("served without the app: %q\n%s", got, out)
		}
	})
	t.Run("an asset prefix", func(t *testing.T) {
		got, out := public(t, "next", ".", nextStandalone("", map[string]any{"assetPrefix": "https://cdn.example.com"}, nil, nil, nil))
		all(t, got, out, "assetPrefix")
	})
	t.Run("i18n", func(t *testing.T) {
		got, out := public(t, "next", ".", nextStandalone("", map[string]any{"i18n": map[string]any{"locales": []string{"en", "fr"}, "defaultLocale": "en"}}, nil, nil, nil))
		all(t, got, out, "i18n")
	})
	t.Run("no manifests", func(t *testing.T) {
		got, out := public(t, "next", ".", map[string]string{".next/static/chunks/a.js": "a", "server.js": "x"})
		all(t, got, out, "served by the app")
	})
}

// react-router-serve and remix-serve: express.static on the client build's
// assets/ at publicPath, immutable for a year, before the app.
func rrBuild(server, publicPath, assetsDir string) map[string]string {
	files := map[string]string{
		server: "const assetsBuildDirectory = " + jsonOf(assetsDir) + ";\nconst basename = \"/\";\nconst future = {};\nconst isSpaMode = false;\n" +
			"const publicPath = " + jsonOf(publicPath) + ";\nexport { assetsBuildDirectory, basename, future, isSpaMode, publicPath };\n",
		assetsDir + "/assets/entry.client-BdF3x.js": "entry",
		assetsDir + "/assets/root-Cx2Q.css":         "css",
		assetsDir + "/assets/root-Cx2Q.css.gz":      "a copy",
		assetsDir + "/favicon.ico":                  "not hashed: no Cache-Control, the app's",
		assetsDir + "/assets/.hidden.js":            "a dotfile: express.static ignores it",
		assetsDir + "/assets/manifest-91ab.js":      "manifest",
	}
	return files
}

func TestReactRouterServeAndRemixServeAssetsAreServedWithoutTheApp(t *testing.T) {
	want := []string{"immutable /assets/entry.client-BdF3x.js", "immutable /assets/root-Cx2Q.css", "immutable /assets/manifest-91ab.js"}
	for _, kind := range []string{"react-router", "remix"} {
		t.Run(kind, func(t *testing.T) {
			got, out := public(t, kind, "build/server/index.js", rrBuild("build/server/index.js", "/", "build/client"))
			same(t, got, want, out)
			if !strings.Contains(out, "left to the app: /assets/root-Cx2Q.css.gz") {
				t.Errorf("the build log doesn't say the .gz is the app's:\n%s", out)
			}
		})
		t.Run(kind+", a public path", func(t *testing.T) {
			got, out := public(t, kind, "build/server/index.js", rrBuild("build/server/index.js", "/app/", "build/client"))
			var w []string
			for _, a := range want {
				w = append(w, strings.Replace(a, " /", " /app/", 1))
			}
			same(t, got, w, out)
		})
		// Vite 8's Rolldown declares the exports with var, Vite 7 with const
		t.Run(kind+", a Vite 8 build", func(t *testing.T) {
			files := rrBuild("build/server/index.js", "/", "build/client")
			files["build/server/index.js"] = strings.ReplaceAll(files["build/server/index.js"], "const ", "var ")
			got, out := public(t, kind, "build/server/index.js", files)
			same(t, got, want, out)
		})
	}
	// Remix's classic compiler (public/build at /build/), and a build that
	// isn't one of these (React Router's RSC build): the app's
	t.Run("classic Remix", func(t *testing.T) {
		got, out := public(t, "remix", "build/index.js", rrBuild("build/index.js", "/build/", "public/build"))
		same(t, got, nil, out)
		if !strings.Contains(out, "served by the app") {
			t.Errorf("the build log doesn't say why:\n%s", out)
		}
	})
	t.Run("an RSC build", func(t *testing.T) {
		got, out := public(t, "react-router", "build/server/index.js", map[string]string{"build/server/index.js": "export default { fetch() {} };\n",
			"build/client/assets/a-1.js": "a"})
		same(t, got, nil, out)
		if !strings.Contains(out, "served by the app") {
			t.Errorf("the build log doesn't say why:\n%s", out)
		}
	})
}

// A compressed copy is never a URL of the tree's (the host serves a copy
// for its file, and a .br or .gz URL is the app's): adapter-node 6's table
// lists its own.
func TestNoCompressedCopyInTheTree(t *testing.T) {
	files := map[string]string{"build/adapter-node.js": adapterNode6("",
		[][2]string{{"/_app/immutable/entry/start.A1.js", "_app/immutable/entry/start.A1.js"}, {"/_app/immutable/entry/start.A1.js.br", "_app/immutable/entry/start.A1.js.br"}},
		[][2]string{{"/", "index.html"}}, nil)}
	files["build/client/_app/immutable/entry/start.A1.js"] = "start"
	files["build/client/_app/immutable/entry/start.A1.js.br"] = "copy"
	files["build/prerendered/index.html"] = "home"
	got, out := public(t, "sveltekit", "build", files)
	same(t, got, []string{"immutable /_app/immutable/entry/start.A1.js", "pages /"}, out)
}

func TestThePlanPutsTheAssetsOfMoreFrameworksInTheBundle(t *testing.T) {
	want, err := os.ReadFile("embed/public.mjs")
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]struct {
		files     map[string]string
		kind, dir string
	}{
		"Next.js standalone": {js(`{"scripts":{"build":"next build","start":"next start"},"dependencies":{"next":"16"}}`,
			"next.config.mjs", "export default { output: 'standalone' };\n"), "next", "."},
		"React Router": {js(`{"scripts":{"build":"react-router build","start":"react-router-serve ./build/server/index.js"},"dependencies":{"@react-router/node":"^7","@react-router/serve":"^7","react-router":"^7"},"devDependencies":{"@react-router/dev":"^7"}}`),
			"react-router", "build/server/index.js"},
		"Remix": {js(`{"scripts":{"build":"remix vite:build","start":"remix-serve ./build/server/index.js"},"dependencies":{"@remix-run/node":"^2","@remix-run/serve":"^2"},"devDependencies":{"@remix-run/dev":"^2"}}`),
			"remix", "build/server/index.js"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			p := detect(t, c.files, buildplan.Settings{})
			m := stepRe.FindStringSubmatch(p.Command)
			if m == nil {
				t.Fatalf("no step in the build: %s", p.Command)
			}
			if b, _ := base64.StdEncoding.DecodeString(m[1]); string(b) != string(want) {
				t.Errorf("the step isn't embed/public.mjs")
			}
			if m[2] != c.kind || m[3] != c.dir {
				t.Errorf("the step runs %s %s, want %s %s", m[2], m[3], c.kind, c.dir)
			}
			if strings.Index(p.Command, m[0]) < strings.Index(p.Command, " "+buildplan.BundleDir+"/bin") {
				t.Errorf("the step runs before the bundle has its bin")
			}
		})
	}
	// a server of the app's own (an Express app running React Router's
	// handler): none - its code runs before any file
	t.Run("React Router behind an Express server", func(t *testing.T) {
		p := detect(t, js(`{"scripts":{"build":"react-router build","start":"node server.js"},"dependencies":{"@react-router/node":"^7","@react-router/express":"^7","express":"^5","react-router":"^7"},"devDependencies":{"@react-router/dev":"^7"}}`,
			"server.js", "import express from 'express';\n"), buildplan.Settings{})
		if stepRe.MatchString(p.Command) {
			t.Errorf("a step for an app with its own server: %s", p.Command)
		}
	})
}

// publicRoutes is routes.json as the step writes it (docs/edge-caching.md,
// "The public tree's routes").
type publicRoutes struct {
	V       int `json:"v"`
	Headers []struct {
		Set  [][2]string `json:"set"`
		URLs []string    `json:"urls"`
	} `json:"headers"`
	Redirects []publicRedirect `json:"redirects"`
}

type publicRedirect struct {
	From     string `json:"from"`
	To       string `json:"to"`
	Status   int    `json:"status"`
	Caseless bool   `json:"caseless"`
	Refresh  bool   `json:"refresh"`
}

func readRoutes(t *testing.T, raw string) publicRoutes {
	t.Helper()
	var r publicRoutes
	if raw == "" {
		return r
	}
	if err := json.Unmarshal([]byte(raw), &r); err != nil || r.V != 1 {
		t.Fatalf("routes.json isn't v1: %v\n%s", err, raw)
	}
	return r
}

// headersOf: each URL's headers, as routes.json gives them
func (r publicRoutes) headersOf() map[string][][2]string {
	out := map[string][][2]string{}
	for _, h := range r.Headers {
		for _, u := range h.URLs {
			out[u] = h.Set
		}
	}
	return out
}

// A Next.js config's headers(): exactly what Next.js 16.4's router sends
// with each asset (resolve-routes.js: every matching rule in order, a later
// one's value replacing an earlier's by name in any case, :params
// compiled; router-server.js: its year only without a Cache-Control of the
// config's) - recorded from next start on the same rules. The assets are
// in the tree with them, not left to the app.
func TestNextConfigHeadersGoWithTheAssets(t *testing.T) {
	security := []any{
		map[string]any{"key": "X-Frame-Options", "value": "DENY"},
		map[string]any{"key": "X-Content-Type-Options", "value": "nosniff"},
		map[string]any{"key": "Referrer-Policy", "value": "strict-origin-when-cross-origin"},
		map[string]any{"key": "Permissions-Policy", "value": "camera=(), microphone=()"},
		map[string]any{"key": "Content-Security-Policy", "value": "default-src 'self' https:"},
	}
	rules := []any{
		map[string]any{"source": "/(.*)", "regex": `^(?:/(.*))(?:/)?$`, "headers": security},
		map[string]any{"source": "/_next/static/:path*", "regex": `^/_next/static(?:/((?:[^/]+?)(?:/(?:[^/]+?))*))?(?:/)?$`,
			"headers": []any{map[string]any{"key": "X-Asset", "value": "yes"}, map[string]any{"key": "referrer-policy", "value": "no-referrer"}}},
		map[string]any{"source": "/_next/static/chunks/:file", "regex": `^/_next/static/chunks(?:/([^/]+?))(?:/)?$`,
			"headers": []any{map[string]any{"key": "X-File", "value": ":file"}, map[string]any{"key": "Cache-Control", "value": "no-store"}}},
	}
	got, out, _, raw := publicRun(t, "next", ".", nextStandalone("", nil, map[string]any{"headers": rules}, nil, nil), "")
	same(t, got, nextAssets, out)
	r := readRoutes(t, raw)
	sec := [][2]string{{"X-Frame-Options", "DENY"}, {"X-Content-Type-Options", "nosniff"}, {"referrer-policy", "no-referrer"},
		{"Permissions-Policy", "camera=(), microphone=()"}, {"Content-Security-Policy", "default-src 'self' https:"}, {"X-Asset", "yes"}}
	want := map[string][][2]string{
		"/_next/static/chunks/0a1b2c3d.js":       append(slices.Clone(sec), [2]string{"X-File", "0a1b2c3d.js"}, [2]string{"Cache-Control", "no-store"}),
		"/_next/static/chunks/app/page-4e5f.js":  sec, // :file is one segment
		"/_next/static/css/9f8e7d.css":           sec,
		"/_next/static/media/font.p.woff2":       sec,
		"/_next/static/kV3x9Q/_buildManifest.js": sec,
	}
	if g := r.headersOf(); !reflect.DeepEqual(g, want) {
		t.Errorf("the assets' headers:\n got  %q\n want %q\n%s", g, want, raw)
	}
	if len(r.Headers) != 2 {
		t.Errorf("one set for the files that share one: %d sets\n%s", len(r.Headers), raw)
	}
	if !strings.Contains(out, "with the headers its config gives them: 5 of them") {
		t.Errorf("the build log doesn't say so:\n%s", out)
	}

	t.Run("none", func(t *testing.T) {
		_, _, _, raw := publicRun(t, "next", ".", nextStandalone("", nil, nil, nil, nil), "")
		if r := readRoutes(t, raw); len(r.Headers) != 0 {
			t.Errorf("headers with no rules: %s", raw)
		}
	})
	t.Run("a header homeport can't send is the app's", func(t *testing.T) {
		for name, h := range map[string]map[string]any{
			"a name homeport sets": {"key": "Vary", "value": "Origin"},
			"a cookie":             {"key": "Set-Cookie", "value": "a=b"},
			"a brace":              {"key": "X-A", "value": "{x}"},
			"a repeated param":     {"key": "X-A", "value": ":path*"},
		} {
			got, out, _, raw := publicRun(t, "next", ".", nextStandalone("", nil, map[string]any{"headers": []any{map[string]any{"source": "/_next/static/css/:path*",
				"regex": `^/_next/static/css(?:/((?:[^/]+?)(?:/(?:[^/]+?))*))?(?:/)?$`, "headers": []any{h}}}}, nil, nil), "")
			if slices.Contains(got, "immutable /_next/static/css/9f8e7d.css") || !strings.Contains(out, "left to the app: /_next/static/css/9f8e7d.css") {
				t.Errorf("%s: served without the app:\n%q\n%s", name, got, out)
			}
			if len(got) != len(nextAssets)-1 || len(readRoutes(t, raw).Headers) != 0 {
				t.Errorf("%s: the other assets aren't as they were: %q %s", name, got, raw)
			}
		}
	})
	t.Run("more than the host reads is the app's", func(t *testing.T) {
		// a set of 32 headers of 8000 bytes for each asset: past routes.json's
		// 1 MiB
		var rules []any
		for i, a := range nextAssets {
			u := strings.Fields(a)[1]
			var hs []any
			for j := range 32 {
				hs = append(hs, map[string]any{"key": fmt.Sprintf("X-H%d", j), "value": strings.Repeat(string(rune('a'+i)), 8000)})
			}
			rules = append(rules, map[string]any{"source": u, "regex": "^" + regexp.QuoteMeta(u) + "(?:/)?$", "headers": hs})
		}
		internal := map[string]any{"source": "/:path+/", "destination": "/:path+", "internal": true, "statusCode": 308, "regex": "^(?:/((?:[^/]+?)(?:/(?:[^/]+?))*))/$"}
		got, out, _, raw := publicRun(t, "next", ".", nextStandalone("", nil, map[string]any{"headers": rules, "redirects": []any{internal}}, nil, nil), "")
		if len(got) != 0 || !strings.Contains(out, "left to the app: /_next/static/css/9f8e7d.css - more headers, or header sets, than homeport sends from files") {
			t.Errorf("served without the app: %q\n%s", got, out)
		}
		if r := readRoutes(t, raw); len(r.Headers) != 0 || len(r.Redirects) != 1 || len(raw) > 1<<20 {
			t.Errorf("routes.json: %d bytes, %d sets, %d redirects", len(raw), len(r.Headers), len(r.Redirects))
		}
	})
}

// A Next.js config's redirects(), in routes.json for the host to answer -
// in order, up to the first it can't answer as Next.js does. Next matches
// literals in any case and sends Refresh with a 308, so each says so
// (caseless, a 308's refresh): the format is every framework's, and the
// host knows none. What the host answers for each (the status, Location,
// Refresh) is recorded from next start in the engine's tests;
// e2e/static-pages.sh checks it against a running app.
func TestNextConfigRedirectsAreAnsweredWithoutTheApp(t *testing.T) {
	internal := map[string]any{"source": "/:path+/", "destination": "/:path+", "internal": true, "statusCode": 308, "regex": "^(?:/((?:[^/]+?)(?:/(?:[^/]+?))*))/$"}
	red := func(source, dest string, status int, regex string, more ...any) map[string]any {
		r := map[string]any{"source": source, "destination": dest, "statusCode": status, "regex": regex}
		for i := 0; i+1 < len(more); i += 2 {
			r[more[i].(string)] = more[i+1]
		}
		return r
	}
	redirects := []any{internal,
		red("/", "/dashboard", 307, `^(?!/_next)/(?:/)?$`),
		red("/old/:slug", "/new/:slug", 308, `^(?!/_next)/old(?:/([^/]+?))(?:/)?$`),
		red("/docs/:path*", "/guide/:path*", 301, `^(?!/_next)/docs(?:/((?:[^/]+?)(?:/(?:[^/]+?))*))?(?:/)?$`),
		red("/m/:a/:b", "/n/:b/:a", 307, `^(?!/_next)/m(?:/([^/]+?))(?:/([^/]+?))(?:/)?$`),
		red("/bare", "https://example.com", 308, `^(?!/_next)/bare(?:/)?$`),
		red("/v1.0/x", "/v2/x/", 302, `^(?!/_next)/v1\.0/x(?:/)?$`),
		red("/cond", "/dashboard", 307, `^(?!/_next)/cond(?:/)?$`, "has", []any{map[string]any{"type": "cookie", "key": "c"}}),
		red("/after", "/dashboard", 308, `^(?!/_next)/after(?:/)?$`),
	}
	got, out, _, raw := publicRun(t, "next", ".", nextStandalone("", nil, map[string]any{"redirects": redirects}, nil, nil), "")
	same(t, got, nextAssets, out)
	want := []publicRedirect{{"/:path+/", "/:path+", 308, true, true}, {"/", "/dashboard", 307, true, false}, {"/old/:slug", "/new/:slug", 308, true, true},
		{"/docs/:path*", "/guide/:path*", 301, true, false}, {"/m/:a/:b", "/n/:b/:a", 307, true, false}, {"/bare", "https://example.com/", 308, true, true}, {"/v1.0/x", "/v2/x/", 302, true, false}}
	if g := readRoutes(t, raw).Redirects; !reflect.DeepEqual(g, want) {
		t.Errorf("redirects:\n got  %v\n want %v", g, want)
	}
	if !strings.Contains(out, "redirects, answered without the app: /:path+/ -> /:path+ (308), / -> /dashboard (307)") {
		t.Errorf("the build log doesn't say so:\n%s", out)
	}

	// each stops the list: Next's router tries it before any after it
	for name, r := range map[string]map[string]any{
		"a param with a pattern":         red("/u/:id(\\d+)", "/user/:id", 308, `^(?!/_next)/u(?:/(\d+))(?:/)?$`),
		"a param first":                  red("/:slug", "/blog/:slug", 308, `^(?!/_next)(?:/([^/]+?))(?:/)?$`),
		"under _next":                    red("/_nextish", "/x", 308, `^(?!/_next)/_nextish(?:/)?$`),
		"a query in the destination":     red("/q", "/x?a=1", 307, `^(?!/_next)/q(?:/)?$`),
		"a param it doesn't have":        red("/p/:a", "/x/:b", 307, `^(?!/_next)/p(?:/([^/]+?))(?:/)?$`),
		"another repeat":                 red("/r/:a*", "/x/:a", 307, `^(?!/_next)/r(?:/((?:[^/]+?)(?:/(?:[^/]+?))*))?(?:/)?$`),
		"a regex Next built another way": red("/w", "/x", 307, `^/w(?:/)?$`),
		"a status of its own":            red("/s", "/x", 300, `^(?!/_next)/s(?:/)?$`),
		"a port":                         red("/port", "https://example.com:8443/x", 307, `^(?!/_next)/port(?:/)?$`),
	} {
		_, _, _, raw := publicRun(t, "next", ".", nextStandalone("", nil, map[string]any{"redirects": []any{internal, r, red("/", "/dashboard", 307, `^(?!/_next)/(?:/)?$`)}}, nil, nil), "")
		if g := readRoutes(t, raw).Redirects; !reflect.DeepEqual(g, []publicRedirect{{"/:path+/", "/:path+", 308, true, true}}) {
			t.Errorf("%s: %v", name, g)
		}
	}

	t.Run("a base path", func(t *testing.T) {
		_, _, _, raw := publicRun(t, "next", ".", nextStandalone("", map[string]any{"basePath": "/b"}, map[string]any{"redirects": []any{internal,
			red("/b", "/b/home", 307, `^(?!/b/_next)/b(?:/)?$`),
			red("/b/old", "/b/new", 308, `^(?!/b/_next)/b/old(?:/)?$`),
			red("/b/:x", "/b/y/:x", 308, `^(?!/b/_next)/b(?:/([^/]+?))(?:/)?$`),
			red("/b/after", "/b", 308, `^(?!/b/_next)/b/after(?:/)?$`)}}, nil, nil), "")
		want := []publicRedirect{{"/:path+/", "/:path+", 308, true, true}, {"/b", "/b/home", 307, true, false}, {"/b/old", "/b/new", 308, true, true}}
		if g := readRoutes(t, raw).Redirects; !reflect.DeepEqual(g, want) {
			t.Errorf("redirects:\n got  %v\n want %v", g, want)
		}
	})
	t.Run("case-sensitive routes", func(t *testing.T) {
		_, _, _, raw := publicRun(t, "next", ".", nextStandalone("", nil, map[string]any{"caseSensitive": true, "redirects": redirects}, nil, nil), "")
		if raw != "" {
			t.Errorf("the host matches in any case, but the config says not to: %s", raw)
		}
	})
	t.Run("trailingSlash: the internal redirects are the app's", func(t *testing.T) {
		_, _, _, raw := publicRun(t, "next", ".", nextStandalone("", map[string]any{"trailingSlash": true}, map[string]any{"redirects": []any{
			red("/:file((?!\\.well-known(?:/.*)?)(?:[^/]+/)*[^/]+\\.\\w+)/", "/:file", 308, `^(?:/((?!\.well-known(?:/.*)?)(?:[^/]+/)*[^/]+\.\w+))/$`, "internal", true),
			red("/", "/dashboard", 307, `^(?!/_next)/(?:/)?$`)}}, nil, nil), "")
		if raw != "" {
			t.Errorf("redirects after one it can't answer: %s", raw)
		}
	})
}
