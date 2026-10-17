package main

import (
	"maps"
	"strings"
	"testing"
)

// A value's availability: its name's default when none is given (a public
// prefix is Build and runtime), or --build, --build-only, --runtime. What
// was set is said with who sees it, and a change the build sees says how
// to put it in the built app. Never a value.
func TestEnvSetSaysWhoSeesItAndWhenToRebuild(t *testing.T) {
	f, h := linkedHarness(t)
	if code := h.run("env", "set", "NEXT_PUBLIC_API_URL=https://api.example"); code != 0 {
		t.Fatalf("%d %s", code, h.said())
	}
	if len(f.availSent) != 0 {
		t.Fatalf("sent an availability nobody chose: %v", f.availSent)
	}
	said := h.said()
	for _, want := range []string{"NEXT_PUBLIC_API_URL", "build and runtime", "homeport env rebuild"} {
		if !strings.Contains(said, want) {
			t.Fatalf("said %s, want %q", said, want)
		}
	}
	if strings.Contains(said, "api.example") {
		t.Fatalf("said a value: %s", said)
	}

	if code := h.run("env", "set", "NPM_TOKEN=npm_s3cret", "--build-only"); code != 0 {
		t.Fatalf("%d %s", code, h.said())
	}
	if !maps.Equal(f.availSent, map[string]string{"NPM_TOKEN": "build_only"}) {
		t.Fatalf("sent %v", f.availSent)
	}
	if said := h.said(); !strings.Contains(said, "build only") || strings.Contains(said, "npm_s3cret") {
		t.Fatalf("said %s", said)
	}

	if code := h.run("env", "set", "NEXT_PUBLIC_FLAG=1", "--runtime"); code != 0 || f.availSent["NEXT_PUBLIC_FLAG"] != "runtime" {
		t.Fatalf("--runtime: %d %v %s", code, f.availSent, h.said())
	}

	// a plain runtime value: no rebuild to speak of
	f.builtChanged = nil
	if code := h.run("env", "set", "SENTRY_DSN=x"); code != 0 {
		t.Fatalf("%d %s", code, h.said())
	}
	if said := h.said(); strings.Contains(said, "rebuild") {
		t.Fatalf("a runtime value said to rebuild: %s", said)
	}
}

// NAME with a flag and no value changes who sees it alone: the value is
// kept, and never sent.
func TestEnvSetAvailabilityAlone(t *testing.T) {
	f, h := linkedHarness(t)
	f.env = map[string]string{"MODE": "x"}
	if code := h.run("env", "set", "MODE", "--build"); code != 0 {
		t.Fatalf("%d %s", code, h.said())
	}
	if f.env["MODE"] != "x" || !maps.Equal(f.availSent, map[string]string{"MODE": "build"}) {
		t.Fatalf("env %v sent %v", f.env, f.availSent)
	}
	if said := h.said(); !strings.Contains(said, "MODE") || !strings.Contains(said, "build and runtime") {
		t.Fatalf("said %s", said)
	}
}

// The flags are one choice, a name alone needs one, and a file variable is
// runtime only: each refused before anything is sent.
func TestEnvSetAvailabilityRefusals(t *testing.T) {
	f, h := linkedHarness(t)
	for _, args := range [][]string{
		{"env", "set", "A=1", "--build", "--runtime"},
		{"env", "set", "A=1", "--build-only", "--build"},
		{"env", "set", "MODE"},
		{"env", "set", "CA", "--from-file", "ca.crt", "--build"},
	} {
		if code := h.run(args...); code == 0 {
			t.Fatalf("%v: allowed", args)
		}
	}
	if f.envPosts != 0 {
		t.Fatalf("%d sets reached the platform", f.envPosts)
	}
}

// env ls says who sees each and where it comes from - homeport's keys
// among them - and whether the environment needs a rebuild.
func TestEnvLsSaysWhoSeesEach(t *testing.T) {
	f, h := linkedHarness(t)
	f.env = map[string]string{"NEXT_PUBLIC_API_URL": "x", "NPM_TOKEN": "y", "NEXT_SERVER_ACTIONS_ENCRYPTION_KEY": "k", "STRIPE_KEY": "z"}
	f.available = map[string]string{"NEXT_PUBLIC_API_URL": "build", "NPM_TOKEN": "build_only", "NEXT_SERVER_ACTIONS_ENCRYPTION_KEY": "build"}
	f.homeport = map[string]string{"NEXT_SERVER_ACTIONS_ENCRYPTION_KEY": "keeps forms in open tabs working"}
	f.builtChanged = map[string]bool{"NPM_TOKEN": true}
	if code := h.run("env", "ls"); code != 0 {
		t.Fatalf("%d %s", code, h.said())
	}
	lines := strings.Split(h.said(), "\n")
	row := func(name string) string {
		for _, l := range lines {
			if strings.HasPrefix(l, name+" ") {
				return l
			}
		}
		t.Fatalf("no row for %s: %s", name, h.said())
		return ""
	}
	if !strings.Contains(lines[0], "AVAILABLE TO") || !strings.Contains(lines[0], "SOURCE") {
		t.Fatalf("header %q", lines[0])
	}
	for name, want := range map[string][]string{
		"NEXT_PUBLIC_API_URL":                {"build + runtime", "plain", "own"},
		"NPM_TOKEN":                          {"build only", "own"},
		"NEXT_SERVER_ACTIONS_ENCRYPTION_KEY": {"build + runtime", "homeport"},
		"STRIPE_KEY":                         {"runtime", "own"},
	} {
		for _, w := range want {
			if !strings.Contains(row(name), w) {
				t.Fatalf("%s: %q, want %q", name, row(name), w)
			}
		}
	}
	if said := h.said(); !strings.Contains(said, "needs a rebuild") || !strings.Contains(said, "homeport env rebuild") {
		t.Fatalf("said %s", said)
	}
}

// env rebuild builds the live commit again with today's variables.
func TestEnvRebuild(t *testing.T) {
	f, h := linkedHarness(t)
	if code := h.run("env", "rebuild"); code != 0 || f.rebuilds != 1 {
		t.Fatalf("%d %d %s", code, f.rebuilds, h.said())
	}
	if said := h.said(); !strings.Contains(said, "b9") || !strings.Contains(said, "production") {
		t.Fatalf("said %s", said)
	}
	f.noLive = true
	if code := h.run("env", "rebuild"); code == 0 || !strings.Contains(h.said(), "nothing is live") {
		t.Fatalf("with nothing live: %d %s", code, h.said())
	}
}

// An agent says who sees a variable too, by name - with its value, or
// alone - asked of the person with it, and told when a rebuild is needed.
// A bad choice is refused before anything's asked.
func TestMCPSetsAvailability(t *testing.T) {
	f := newFakeAPI(t)
	h := newHarness(t, f)
	h.login(t)
	yes := &person{approve: true}
	cs := mcpSession(t, h, yes)
	text, isErr := callTool(t, cs, "set_variables", with(map[string]any{"set": map[string]string{"NPM_TOKEN": "npm_s3cret"}, "available": map[string]string{"NPM_TOKEN": "build_only"}}))
	if isErr || strings.Contains(text, "npm_s3cret") || !maps.Equal(f.availSent, map[string]string{"NPM_TOKEN": "build_only"}) {
		t.Fatalf("set: %v %s %v", isErr, text, f.availSent)
	}
	if !strings.Contains(text, "needs_rebuild") || !strings.Contains(yes.asked[0], "build only") {
		t.Fatalf("said %s, asked %q", text, yes.asked)
	}
	if _, isErr := callTool(t, cs, "set_variables", with(map[string]any{"available": map[string]string{"NPM_TOKEN": "runtime"}})); isErr || f.env["NPM_TOKEN"] != "npm_s3cret" || f.availSent["NPM_TOKEN"] != "runtime" {
		t.Fatalf("alone: %v %v %v", isErr, f.env, f.availSent)
	}
	posts := f.envPosts
	if text, isErr := callTool(t, cs, "set_variables", with(map[string]any{"available": map[string]string{"NPM_TOKEN": "always"}})); !isErr || f.envPosts != posts {
		t.Fatalf("a bad choice: %v %s", isErr, text)
	}
	if text, _ := callTool(t, cs, "list_variables", blog); !strings.Contains(text, "runtime") {
		t.Fatalf("list %s", text)
	}
}
