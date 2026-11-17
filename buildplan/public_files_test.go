package buildplan_test

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/homeport-sh/cli/buildplan"
)

// A framework that writes homeport's files manifest (.output/files.json,
// docs/edge-caching.md "The files manifest") - rsc-kit 0.35.0+ does - says
// itself which URLs its server answers from files before any of the app's
// code runs, and the headers it sends with them. The step puts exactly those
// in .homeport/public: a file with the year-long immutable Cache-Control in
// immutable/ (the release after keeps it a day, for a tab opened before
// it), another in files/, each with its headers in routes.json and the
// compressed copies the framework wrote beside it. A file its server sends
// with no Cache-Control is in files/ with an exact set: the host adds none
// of its own. Nothing it doesn't list, and no file the host would send
// headers the app doesn't.
const filesManifest = `{"v":1,"root":"public","files":["/_app/icon.svg","/assets/index-Ab12.js","/assets/style-Cd34.css","/favicon.ico","/gone.js","/robots.txt","/sw.js","/x/odd.js"],
"precompressed":["br","gzip","zstd"],
"headers":[{"match":"/sw.js","headers":{"cache-control":"public, max-age=0, must-revalidate"}},
{"match":"/assets/**","headers":{"cache-control":"public, max-age=31536000, immutable"}},
{"match":"/assets/style-Cd34.css","headers":{"x-frame-options":"DENY"}},
{"match":"/_app/**","headers":{"cache-control":"public, max-age=3600","x-content-type-options":"nosniff"}},
{"match":"/x/*","headers":{"cache-control":"no-store"}},
{"match":"/x/odd.js","headers":{"cache-control":"public, max-age=60"}},
{"match":"/robots.txt","headers":{"x-content-type-options":"nosniff"}}],
"middleware":false}`

func filesOutput(manifest string) map[string]string {
	return map[string]string{
		".output/files.json":                         manifest,
		".output/public/assets/index-Ab12.js":        "js",
		".output/public/assets/index-Ab12.js.br":     "js-br",
		".output/public/assets/index-Ab12.js.gz":     "js-gz",
		".output/public/assets/index-Ab12.js.zst":    "js-zst",
		".output/public/assets/style-Cd34.css":       "css",
		".output/public/_app/icon.svg":               "<svg/>",
		".output/public/favicon.ico":                 "ico",
		".output/public/sw.js":                       "sw",
		".output/public/robots.txt":                  "robots",
		".output/public/sw.js.br":                    "sw-br",
		".output/public/x/odd.js":                    "odd",
		".output/public/unlisted.txt":                "not listed",
		".output/server/rsc-static/about/index.html": "<p>never from here</p>",
	}
}

func TestTheFilesManifestIsWhatTheHostServes(t *testing.T) {
	got, out, marker, routes := publicRun(t, "files", ".output/files.json", filesOutput(filesManifest), "")
	same(t, got, []string{
		"immutable /assets/index-Ab12.js",
		"immutable /assets/index-Ab12.js.br",
		"immutable /assets/index-Ab12.js.gz",
		"immutable /assets/style-Cd34.css",
		"files /_app/icon.svg",
		"files /favicon.ico",
		"files /robots.txt",
		"files /sw.js",
		"files /sw.js.br",
		"files /x/odd.js",
	}, out)
	var r struct {
		V       int
		Headers []struct {
			Set   [][2]string
			URLs  []string
			Exact bool
		}
	}
	if err := json.Unmarshal([]byte(routes), &r); err != nil || r.V != 1 {
		t.Fatalf("routes.json: %v\n%s", err, routes)
	}
	sets := map[string]string{}
	for _, h := range r.Headers {
		b, _ := json.Marshal(h.Set)
		if h.Exact {
			b = append([]byte("exact "), b...)
		}
		for _, u := range h.URLs {
			sets[u] = string(b)
		}
	}
	want := map[string]string{
		// every rule that matches, in order: exact, or a prefix ending /**
		"/assets/index-Ab12.js":  `[["cache-control","public, max-age=31536000, immutable"]]`,
		"/assets/style-Cd34.css": `[["cache-control","public, max-age=31536000, immutable"],["x-frame-options","DENY"]]`,
		"/_app/icon.svg":         `[["cache-control","public, max-age=3600"],["x-content-type-options","nosniff"]]`,
		"/sw.js":                 `[["cache-control","public, max-age=0, must-revalidate"]]`,
		// a match that's neither is no rule here (rsc-kit 0.35.1 leaves
		// such a file out of files)
		"/x/odd.js": `[["cache-control","public, max-age=60"]]`,
		// no Cache-Control: sent with exactly the app's headers, or none
		"/favicon.ico": `exact []`,
		"/robots.txt":  `exact [["x-content-type-options","nosniff"]]`,
	}
	for u, w := range want {
		if sets[u] != w {
			t.Errorf("%s: headers %s, want %s", u, sets[u], w)
		}
	}
	if len(sets) != len(want) {
		t.Errorf("headers for %d URLs, want %d: %s", len(sets), len(want), routes)
	}
	for _, s := range []string{"left to the app: /gone.js"} {
		if !strings.Contains(out, s) {
			t.Errorf("the log doesn't say %q:\n%s", s, out)
		}
	}
	if marker != "" {
		t.Errorf("an edge marker from files alone: %s", marker)
	}
}

// Anything but a manifest it reads whole - another version, middleware, a
// root outside the manifest's folder, a file that leaves its root - leaves
// every file to the app, as before; and none at all is none.
func TestAFilesManifestItCantTrustIsTheApps(t *testing.T) {
	for name, m := range map[string]string{
		"another version":    strings.Replace(filesManifest, `"v":1`, `"v":2`, 1),
		"middleware":         strings.Replace(filesManifest, `"middleware":false`, `"middleware":true`, 1),
		"no middleware said": strings.Replace(filesManifest, `"middleware":false`, `"other":false`, 1),
		"a root outside":     strings.Replace(filesManifest, `"root":"public"`, `"root":"../public"`, 1),
		"an absolute root":   strings.Replace(filesManifest, `"root":"public"`, `"root":"/etc"`, 1),
		"not JSON":           "{",
	} {
		got, out, _, _ := publicRun(t, "files", ".output/files.json", filesOutput(m), "")
		if len(got) != 0 || !strings.Contains(out, "served by the app") {
			t.Errorf("%s: %q\n%s", name, got, out)
		}
	}
	files := filesOutput(filesManifest)
	delete(files, ".output/files.json")
	if got, _, _, _ := publicRun(t, "files", ".output/files.json", files, ""); len(got) != 0 {
		t.Errorf("no manifest: %q", got)
	}
}

// A listed file that is a link out of its root is the app's.
func TestAFilesManifestsLinkIsTheApps(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("no node")
	}
	b := t.TempDir()
	for name, body := range map[string]string{".output/files.json": `{"v":1,"root":"public","files":["/a.js","/p.txt"],"headers":[{"match":"/**","headers":{"cache-control":"no-cache"}}],"middleware":false}`, ".output/public/a.js": "a"} {
		_ = os.MkdirAll(filepath.Dir(filepath.Join(b, name)), 0o755)
		_ = os.WriteFile(filepath.Join(b, name), []byte(body), 0o644)
	}
	if err := os.Symlink("/etc/hosts", filepath.Join(b, ".output/public/p.txt")); err != nil {
		t.Fatal(err)
	}
	script, _ := filepath.Abs("embed/public.mjs")
	cmd := exec.Command(node, script, "files", ".output/files.json")
	cmd.Dir = b
	out, _ := cmd.CombinedOutput()
	if _, err := os.Stat(filepath.Join(b, ".homeport/public/files/a.js")); err != nil {
		t.Errorf("a.js: %v\n%s", err, out)
	}
	if _, err := os.Lstat(filepath.Join(b, ".homeport/public/files/p.txt")); err == nil {
		t.Errorf("a link out of the root was copied\n%s", out)
	}
}

var filesStepRe = regexp.MustCompile(`\{ printf '%s' '([A-Za-z0-9+/=]+)' \| base64 -d > \.homeport-public\.mjs && bun \.homeport-public\.mjs files \.output/files\.json \|\| rm -rf \.homeport/public; rm -f \.homeport-public\.mjs; \}`)

// An rsc-kit app's server is a binary, which has no folder for the tree:
// the build writes it in the app's folder from the manifest, after the
// build, and the plan says so ("public": true) for the builder to ship it.
func TestAnRSCKitAppsBuildWritesThePublicTree(t *testing.T) {
	want, _ := os.ReadFile("embed/public.mjs")
	p := detect(t, map[string]string{"package.json": rscKitPkg, "bun.lock": bunLock}, buildplan.Settings{})
	m := filesStepRe.FindStringSubmatch(p.Command)
	if m == nil || !strings.HasPrefix(p.Command, "bun run build && ") {
		t.Fatalf("no step after the build: %s", p.Command)
	}
	if b, _ := base64.StdEncoding.DecodeString(m[1]); string(b) != string(want) {
		t.Errorf("the step isn't embed/public.mjs")
	}
	if b, _ := json.Marshal(p); !strings.Contains(string(b), `"public":true`) {
		t.Errorf("the plan doesn't say so: %s", b)
	}
	// a person's build command too
	p = detect(t, map[string]string{"package.json": rscKitPkg, "bun.lock": bunLock}, buildplan.Settings{Install: "bun install"})
	if !filesStepRe.MatchString(p.Command) || !p.Public {
		t.Errorf("with settings: %+v", p)
	}
	// homeport.yaml saying what the build makes: no step, no tree
	p = detect(t, map[string]string{"package.json": rscKitPkg, "bun.lock": bunLock, "homeport.yaml": "build:\n  artifact: dist/app\n"}, buildplan.Settings{})
	if filesStepRe.MatchString(p.Command) || p.Public {
		t.Errorf("homeport.yaml's build: %+v", p)
	}
	// the step for real, in the build's shell: it never fails the build
	stepCmd := filesStepRe.FindString(detect(t, map[string]string{"package.json": rscKitPkg, "bun.lock": bunLock}, buildplan.Settings{}).Command)
	if _, err := exec.LookPath("bun"); err == nil {
		dir := t.TempDir()
		out, err := shell(t, dir, stepCmd+" && echo after")
		if err != nil || !strings.Contains(out, "after") {
			t.Fatalf("the step failed the build: %v\n%s", err, out)
		}
		if _, err := os.Stat(filepath.Join(dir, ".homeport-public.mjs")); err == nil {
			t.Errorf("the step left its script")
		}
	}
}
