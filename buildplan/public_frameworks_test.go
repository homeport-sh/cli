package buildplan_test

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/homeport-sh/cli/buildplan"
)

// The content-hashed assets Next.js standalone, react-router-serve and
// remix-serve answer from files - the same Cache-Control, a year and
// immutable, and none of the app's code before them - go in the bundle's
// immutable tree too, for the host to answer (and the builder to compress
// once) without waking the app. Only those: a URL the app's middleware,
// proxy, headers, redirects or rewrites could take is left to the app.
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
	t.Run("headers of the config's", func(t *testing.T) {
		got, out := public(t, "next", ".", nextStandalone("", nil, map[string]any{"headers": []any{map[string]any{"source": "/_next/static/chunks/:path*",
			"regex": `^/_next/static/chunks(?:/((?:[^/]+?)(?:/(?:[^/]+?))*))?(?:/)?$`, "headers": []any{map[string]any{"key": "x-a", "value": "b"}}}}}, nil, nil))
		same(t, got, []string{"immutable /_next/static/css/9f8e7d.css", "immutable /_next/static/media/font.p.woff2", "immutable /_next/static/kV3x9Q/_buildManifest.js"}, out)
		if !strings.Contains(out, "left to the app: /_next/static/chunks/0a1b2c3d.js - the config's headers, redirects or rewrites") {
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
