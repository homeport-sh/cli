package buildplan_test

import (
	"strings"
	"testing"

	"github.com/homeport-sh/cli/buildplan"
)

// A health check path is a path on the app: starting with /, no host,
// query, spaces or "..", at most 200 characters; unset is the default.
func TestAHealthPathIsAPathOnTheApp(t *testing.T) {
	for _, p := range []string{"", "/", "/healthz", "/api/health", "/api/health/", "/v1.2/up_ok-now"} {
		if err := buildplan.CheckHealthPath(p); err != nil {
			t.Errorf("%q refused: %v", p, err)
		}
	}
	for _, p := range []string{"healthz", "//evil.example/x", "/a/../b", "/..", "/health?x=1", "/a b", "/a//b", "/a#b",
		"/" + strings.Repeat("a", 200), "/ü"} {
		if err := buildplan.CheckHealthPath(p); err == nil {
			t.Errorf("%q accepted", p)
		}
	}
	if err := buildplan.CheckHealthPath("/" + strings.Repeat("a", 199)); err != nil {
		t.Fatalf("200 characters: %v", err)
	}
}

// homeport.yaml's health: is the path the app's health is checked on, in
// place of what detection says; a bad one is refused before anything builds.
func TestHomeportYamlSaysWhereTheHealthCheckGoes(t *testing.T) {
	gomod := "module m\n\ngo 1.24\n"
	p := detect(t, map[string]string{"go.mod": gomod, "homeport.yaml": "run: serve\nhealth: /healthz\n"}, buildplan.Settings{})
	if p.Health != "/healthz" {
		t.Fatalf("health = %q", p.Health)
	}
	// a JavaScript app's detected "/" gives way to the file's
	jsFiles := map[string]string{"package.json": `{"scripts":{"build":"vite build","start":"node server.js"},"dependencies":{"express":"4"}}`,
		"bun.lock": bunLock, "homeport.yaml": "health: /api/up\n"}
	if js := detect(t, jsFiles, buildplan.Settings{}); js.Health != "/api/up" {
		t.Fatalf("js health = %q", js.Health)
	}
	// and stays when the settings say what the build makes
	if js := detect(t, jsFiles, buildplan.Settings{Output: "server"}); js.Health != "/api/up" {
		t.Fatalf("js as a binary: health = %q", js.Health)
	}
	for _, bad := range []string{"health: healthz\n", "health:\n  path: /healthz\n", "health: /a/../b\n"} {
		_, err := buildplan.Detect(repo(map[string]string{"go.mod": gomod, "homeport.yaml": bad}), buildplan.Settings{})
		if err == nil || !strings.Contains(err.Error(), "homeport.yaml") {
			t.Errorf("%q: %v", bad, err)
		}
	}
	// a static site isn't health-checked
	site := detect(t, map[string]string{"index.html": "<p>hi</p>", "homeport.yaml": "health: /healthz\n"}, buildplan.Settings{})
	if site.Kind != buildplan.Static || site.Health != "" {
		t.Fatalf("site: %+v", site)
	}
}

// FileHealth is homeport.yaml's health path alone, checked: what the control
// plane reads from a deploy's commit or upload.
func TestFileHealthReadsHomeportYaml(t *testing.T) {
	if got, err := buildplan.FileHealth([]byte("run: serve\nhealth: /healthz\n")); err != nil || got != "/healthz" {
		t.Fatalf("got %q, %v", got, err)
	}
	if got, err := buildplan.FileHealth([]byte("run: serve\n")); err != nil || got != "" {
		t.Fatalf("none: %q, %v", got, err)
	}
	for _, bad := range []string{"health: /a b\n", "health:\n  path: /healthz\n", "health: ["} {
		if _, err := buildplan.FileHealth([]byte(bad)); err == nil || !strings.Contains(err.Error(), "homeport.yaml") {
			t.Errorf("%q: %v", bad, err)
		}
	}
}
