package buildplan_test

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/homeport-sh/cli/buildplan"
)

// A backend's paths, by the rule the host matches them with: a path from
// the root, a segment at a time, no trailing slash, no . or .., nothing the
// host keeps for itself; at most 20, each once.
func TestBackendPathsAreChecked(t *testing.T) {
	if err := buildplan.CheckBackendPaths([]string{"/api", "/webhooks/stripe", "/a.b_c~d-e", "/_edgex", "/.well-knownx"}); err != nil {
		t.Errorf("good paths: %v", err)
	}
	if err := buildplan.CheckBackendPaths(nil); err != nil {
		t.Errorf("none: %v", err)
	}
	long := "/" + strings.Repeat("a", 128)
	for _, bad := range [][]string{{""}, {"api"}, {"/"}, {"/api/"}, {"//api"}, {"/a//b"}, {"/a/../b"}, {"/./a"}, {"/a b"}, {"/a%20"},
		{"/a?b"}, {"/a*"}, {"/_edge"}, {"/_edge/x"}, {"/.well-known/acme-challenge"}, {"/__homeport"}, {long}, {"/api", "/api"}} {
		if err := buildplan.CheckBackendPaths(bad); err == nil {
			t.Errorf("%q: no error", bad)
		}
	}
	many := make([]string, 21)
	for i := range many {
		many[i] = "/p" + strings.Repeat("x", i)
	}
	if err := buildplan.CheckBackendPaths(many); err == nil {
		t.Error("21 paths: no error")
	}
	if err := buildplan.CheckBackendPaths(many[:20]); err != nil {
		t.Errorf("20 paths: %v", err)
	}
	if err := buildplan.CheckBackendPaths([]string{"/" + strings.Repeat("a", 127)}); err != nil {
		t.Errorf("128 bytes: %v", err)
	}
}

// rsc-kit's package.json, with its single-binary step
const rscKitServer = `{"type":"module","scripts":{"build":"vite build","compile:binary":"bun build --compile .output/server/compile.mjs --outfile dist/app"},` +
	`"dependencies":{"@rsc-kit/core":"0.35.0","react":"19"}}`

// A Laravel app with an rsc-kit renderer is one app: Laravel's own plan (its
// build, caches, release command, processes, the web's args), the renderer
// built after it and put in the bundle, and the web run through homeport's
// supervisor - Laravel on the backend's port, behind the config's runner,
// then the renderer in front of it on the app's. Its compile step runs again
// for the sandbox's Linux (glibc).
func TestALaravelAppWithAnRSCKitRendererIsOneApp(t *testing.T) {
	p := detect(t, laravel([]string{"laravel/reverb"}, "package.json", rscKitServer, "bun.lock", bunLock,
		"database/migrations/0001_create.php", "<?php"), buildplan.Settings{})
	if p.Framework != "Laravel · rsc-kit" || p.Kind != buildplan.Bundle || p.Toolchain != "php" || p.Artifact != buildplan.BundleDir ||
		!p.Supervise || !p.Backend || p.RSCKit || p.SSR != "" {
		t.Fatalf("pair: %+v", p)
	}
	if !strings.HasSuffix(p.Install, " && bun install --frozen-lockfile && bun run build") {
		t.Errorf("install: %s", p.Install)
	}
	if p.Run != buildplan.PHPRun || p.Release.String() == "" || !strings.Contains(p.Release.String(), "migrate") {
		t.Errorf("Laravel's own run and release: %q %q", p.Run, p.Release.String())
	}
	if !strings.HasPrefix(p.Command, buildplan.LaravelCaches+" && frankenphp-bundle . .homeport-bundle && mv -T .homeport-bundle/frankenphp .homeport-bundle/bin && ") {
		t.Errorf("Laravel's bundle first: %s", p.Command)
	}
	for _, want := range []string{
		"bun build --compile .output/server/compile.mjs --outfile dist/app --target=bun-linux-$t --compile-executable-path=/tmp/homeport/bun-glibc",
		"cp dist/app .homeport-bundle/.homeport/rsc-kit/server",
		"printf '%s\\n' 'BUN_JSC_forceRAMSize=%heapbytes% .homeport/rsc-kit/server' > .homeport-bundle/.homeport/renderer",
		"bun .homeport-pair.mjs .homeport-bundle/.homeport/backend-paths Laravel",
		"if [ -f .homeport-bundle/.homeport/start.php ]; then printf '%s\\n' '.homeport/supervise supervise ./bin php-cli .homeport/start.php' > .homeport-bundle/.homeport/wrap; " +
			"else printf '%s\\n' '.homeport/supervise supervise' > .homeport-bundle/.homeport/wrap; fi",
	} {
		if !strings.Contains(p.Command, want) {
			t.Errorf("command has no %q:\n%s", want, p.Command)
		}
	}
	// the marker is read before what it names is compiled
	if strings.Index(p.Command, "homeport-pair.mjs") > strings.Index(p.Command, "--compile-executable-path") {
		t.Errorf("compiled before the marker was read:\n%s", p.Command)
	}
	if !slices.Contains(p.Assets, buildplan.PlanAsset{From: ".output/public/assets", At: "/assets"}) {
		t.Errorf("assets: %+v", p.Assets)
	}
	if !slices.ContainsFunc(p.Warnings, func(w string) bool {
		return strings.Contains(w, "rsc-kit's renderer runs in the app's memory, beside Laravel")
	}) {
		t.Errorf("warnings: %q", p.Warnings)
	}
	// its processes are Laravel's, their config cached as they start
	if len(p.Processes) == 0 || !strings.HasPrefix(p.Processes[0].Run, buildplan.StartRunner+" ") {
		t.Errorf("processes: %+v", p.Processes)
	}
	b, _ := json.Marshal(p)
	if !strings.Contains(string(b), `"supervise":true`) || !strings.Contains(string(b), `"backend":true`) {
		t.Errorf("json: %s", b)
	}
	// the wrap names the supervisor where the builder puts it
	if buildplan.SuperviseFile != ".homeport/supervise" {
		t.Errorf("SuperviseFile %q", buildplan.SuperviseFile)
	}
}

// A Go backend is a binary on its own; with a renderer it's a bundle, the
// binary its bin. The Go image has no Bun: the pinned one is fetched first.
func TestAGoAppWithAnRSCKitRendererIsOneApp(t *testing.T) {
	p := detect(t, map[string]string{"go.mod": "module m\n\ngo 1.27\n", "main.go": "package main", "package.json": rscKitServer, "bun.lock": bunLock},
		buildplan.Settings{})
	if p.Framework != "Go · rsc-kit" || p.Kind != buildplan.Bundle || p.Toolchain != "go" || p.Artifact != buildplan.BundleDir ||
		p.StaticFallback || !p.Supervise || !p.Backend {
		t.Fatalf("pair: %+v", p)
	}
	if !strings.HasPrefix(p.Install, "mkdir -p /tmp/homeport/bin && export PATH=/tmp/homeport/bin:$PATH && ") ||
		!strings.HasSuffix(p.Install, " && bun install --frozen-lockfile") {
		t.Errorf("install: %s", p.Install)
	}
	for _, want := range []string{
		`CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o server . && bun run build && `,
		"rm -rf .homeport-bundle && mkdir -p .homeport-bundle/.homeport && mv server .homeport-bundle/bin",
		"bun .homeport-pair.mjs .homeport-bundle/.homeport/backend-paths Go",
		"printf '%s\\n' 'BUN_JSC_forceRAMSize=%heapbytes% .homeport/rsc-kit/server' > .homeport-bundle/.homeport/renderer",
	} {
		if !strings.Contains(p.Command, want) {
			t.Errorf("command has no %q:\n%s", want, p.Command)
		}
	}
	if p.Build != `CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o server . && bun run build` {
		t.Errorf("build: %s", p.Build)
	}
}

// With no single-binary step, the renderer is rsc-kit's server folder on the
// pinned Bun, for the sandbox's Linux.
func TestARendererWithoutABinaryRunsOnBun(t *testing.T) {
	pkg := `{"scripts":{"build":"vite build"},"dependencies":{"@rsc-kit/core":"0.35.0"}}`
	p := detect(t, map[string]string{"go.mod": "module m\n\ngo 1.27\n", "package.json": pkg, "bun.lock": bunLock}, buildplan.Settings{})
	for _, want := range []string{
		"cp /tmp/homeport/bun-glibc .homeport-bundle/.homeport/bun",
		"mkdir -p .homeport-bundle/.output && cp -RL .output/server .output/public .homeport-bundle/.output/",
		"printf '%s\\n' 'BUN_JSC_forceRAMSize=%heapbytes% .homeport/bun .output/server/index.mjs' > .homeport-bundle/.homeport/renderer",
	} {
		if !strings.Contains(p.Command, want) {
			t.Errorf("command has no %q:\n%s", want, p.Command)
		}
	}
	if strings.Contains(p.Command, "--compile") {
		t.Errorf("compiled: %s", p.Command)
	}
}

// homeport.yaml's backend paths are the backend's, in place of the ones
// rsc-kit's build names; the build settings' over both. Checked as the host
// matches them, and never the renderer's host-call endpoint.
func TestABackendsPathsFromHomeportYaml(t *testing.T) {
	files := func(yaml string) map[string]string {
		return laravel(nil, "package.json", rscKitServer, "bun.lock", bunLock, "homeport.yaml", yaml)
	}
	p := detect(t, files("backend:\n  paths: [/api, /webhooks]\n"), buildplan.Settings{})
	if !strings.Contains(p.Command, "bun .homeport-pair.mjs .homeport-bundle/.homeport/backend-paths Laravel /api /webhooks") {
		t.Errorf("paths: %s", p.Command)
	}
	p = detect(t, files("backend:\n  paths: [/api]\n"), buildplan.Settings{BackendPaths: []string{"/auth"}})
	if !strings.Contains(p.Command, "backend-paths Laravel /auth &&") {
		t.Errorf("settings' paths: %s", p.Command)
	}
	for yaml, want := range map[string]string{
		"backend:\n  paths: [/api/]\n":            `homeport.yaml: backend path "/api/"`,
		"backend:\n  paths: [/__rsc]\n":           `homeport.yaml: backend path "/__rsc" is rsc-kit's host-call endpoint`,
		"backend:\n  paths: [/__rsc/host-call]\n": "host-call endpoint",
		"backend:\n  paths: /api\n":               "homeport.yaml",
		"backend:\n  pathz: [/api]\n":             "homeport.yaml",
	} {
		if _, err := buildplan.Detect(repo(files(yaml)), buildplan.Settings{}); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v, want %q", yaml, err, want)
		}
	}
	if _, err := buildplan.Detect(repo(files("")), buildplan.Settings{BackendPaths: []string{"api"}}); err == nil {
		t.Error("settings' bad path: no error")
	}
	// with no renderer beside it, there's nothing to say them to
	_, err := buildplan.Detect(repo(laravel(nil, "homeport.yaml", "backend:\n  paths: [/api]\n")), buildplan.Settings{})
	if err == nil || !strings.Contains(err.Error(), "rsc-kit") {
		t.Errorf("no renderer: %v", err)
	}
}

// What makes it not a pair: a build a person or homeport.yaml set (theirs
// makes the bundle), an rsc-kit app with no backend beside it, a compile
// step for a Linux the sandbox can't run.
func TestWhatIsntAPair(t *testing.T) {
	for name, c := range map[string]struct {
		files map[string]string
		s     buildplan.Settings
	}{
		"a build command":    {laravel(nil, "package.json", rscKitServer, "bun.lock", bunLock), buildplan.Settings{Command: "make"}},
		"homeport.yaml's":    {laravel(nil, "package.json", rscKitServer, "bun.lock", bunLock, "homeport.yaml", "build:\n  command: make\n"), buildplan.Settings{}},
		"rsc-kit alone":      {map[string]string{"package.json": rscKitServer, "bun.lock": bunLock}, buildplan.Settings{}},
		"not a Bun project":  {laravel(nil, "package.json", rscKitServer, "package-lock.json", "{}"), buildplan.Settings{}},
		"no rsc-kit":         {laravel(nil, "package.json", `{"scripts":{"build":"vite build"}}`, "bun.lock", bunLock), buildplan.Settings{}},
		"a static site said": {laravel(nil, "package.json", rscKitServer, "bun.lock", bunLock, "homeport.yaml", "static: public\n"), buildplan.Settings{}},
	} {
		p, err := buildplan.Detect(repo(c.files), c.s)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if p.Backend || p.Supervise || strings.Contains(p.Framework, "·") {
			t.Errorf("%s: a pair: %+v", name, p)
		}
	}
	musl := strings.Replace(rscKitServer, "--outfile dist/app", "--outfile dist/app --target=bun-linux-x64-musl", 1)
	if _, err := buildplan.Detect(repo(laravel(nil, "package.json", musl, "bun.lock", bunLock)), buildplan.Settings{}); err == nil ||
		!strings.Contains(err.Error(), "bun-linux-x64-musl") {
		t.Errorf("musl: %v", err)
	}
	// a renderer's build is its build script
	if _, err := buildplan.Detect(repo(map[string]string{"go.mod": "module m\n\ngo 1.27\n", "package.json": `{"dependencies":{"@rsc-kit/core":"0.35.0"}}`,
		"bun.lock": bunLock}), buildplan.Settings{}); err == nil || !strings.Contains(err.Error(), "build script") {
		t.Errorf("no build script: %v", err)
	}
}

// Inertia's SSR renderer runs beside the web through the same supervisor:
// .homeport/beside, and no PHP of homeport's.
func TestInertiaSSRRunsThroughTheSupervisor(t *testing.T) {
	p := detect(t, laravel([]string{inertia}, "package.json", `{"scripts":{"build":"vite build","build:ssr":"vite build && vite build --ssr"}}`,
		"bun.lock", bunLock), buildplan.Settings{})
	if p.SSR != buildplan.SSRInertia || !p.Supervise || p.Backend {
		t.Fatalf("ssr: %+v", p)
	}
	if strings.Contains(p.Command, "beside.php") || !strings.Contains(p.Command, "> .homeport-bundle/.homeport/beside") ||
		!strings.Contains(p.Command, "'.homeport/supervise supervise ./bin php-cli .homeport/start.php' > .homeport-bundle/.homeport/wrap") {
		t.Errorf("command: %s", p.Command)
	}
}
