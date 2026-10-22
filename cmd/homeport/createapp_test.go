package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/homeport-sh/cli/internal/config"
)

// unlinked makes the harness's folder name, with an app's files and no
// link: a folder `homeport deploy` hasn't deployed yet.
func unlinked(t *testing.T, h *harness, name string) {
	t.Helper()
	h.a.wd = filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(h.a.wd, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(h.a.wd, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// `homeport deploy` in a folder whose app the team doesn't have asks to
// make it - named after the folder - then deploys it, and links the folder.
func TestDeployOffersToMakeTheApp(t *testing.T) {
	f := newFakeAPI(t)
	h := newHarness(t, f)
	h.login(t)
	unlinked(t, h, "rsc-sharp")
	h.a.tty = true
	h.a.in = strings.NewReader("y\n")
	if code := h.run("deploy", "--detach"); code != 0 {
		t.Fatalf("deploy: %d %s", code, h.said())
	}
	if !strings.Contains(h.said(), `No app "rsc-sharp" on team "alice". Create it? [y/N]`) {
		t.Fatalf("asked %s", h.said())
	}
	if len(f.created) != 1 || f.created[0].Name != "rsc-sharp" || len(f.deploys) != 1 {
		t.Fatalf("created %+v, deploys %d", f.created, len(f.deploys))
	}
	if !strings.Contains(h.said(), "alice/rsc-sharp (production)") {
		t.Fatalf("said %s", h.said())
	}
	l, at, err := config.FindLink(h.a.wd)
	if err != nil || at != h.a.wd || l.Project != "rsc-sharp" || l.Environment != "production" || l.App != f.apps[len(f.apps)-1].ID {
		t.Fatalf("link %+v %s %v", l, at, err)
	}
	// deployed again, it's the linked app: nothing asked, nothing made
	h.a.in = strings.NewReader("")
	if code := h.run("deploy", "--detach"); code != 0 || len(f.created) != 1 || strings.Contains(h.said(), "Create it?") {
		t.Fatalf("again: %d %+v %s", code, f.created, h.said())
	}
}

func TestDeployMakesNoAppWhenTheAnswerIsNo(t *testing.T) {
	f := newFakeAPI(t)
	h := newHarness(t, f)
	h.login(t)
	unlinked(t, h, "rsc-sharp")
	h.a.tty = true
	h.a.in = strings.NewReader("n\n")
	if code := h.run("deploy"); code != 0 || len(f.created) != 0 || len(f.deploys) != 0 || !strings.Contains(h.said(), "Not created") {
		t.Fatalf("%d %+v %s", code, f.created, h.said())
	}
	if _, _, err := config.FindLink(h.a.wd); err == nil {
		t.Fatal("linked to nothing")
	}
}

// --yes makes it without asking: in CI, or a script, with no terminal.
func TestDeployYesMakesTheAppWithoutAsking(t *testing.T) {
	f := newFakeAPI(t)
	h := newHarness(t, f)
	h.login(t)
	project(t, h) // linked to blog: --app names another
	if code := h.run("deploy", "--app", "web", "--env", "staging", "--yes", "--detach"); code != 0 {
		t.Fatalf("deploy: %d %s", code, h.said())
	}
	if len(f.created) != 1 || f.created[0] != (fakeCreate{Name: "web", Environment: "staging"}) || strings.Contains(h.said(), "Create it?") {
		t.Fatalf("created %+v: %s", f.created, h.said())
	}
	if !strings.Contains(h.said(), "alice/web (staging)") {
		t.Fatalf("said %s", h.said())
	}
	// the folder was linked: it stays linked to its own app
	if l, _, _ := config.FindLink(h.a.wd); l.Project != "blog" {
		t.Fatalf("link %+v", l)
	}
}

// With no terminal to ask in and no --yes, nothing is made: it says how.
func TestDeployWithoutATerminalNeedsYesToMakeAnApp(t *testing.T) {
	f := newFakeAPI(t)
	h := newHarness(t, f)
	h.login(t)
	unlinked(t, h, "rsc-sharp")
	code := h.run("deploy")
	if code != exitUsage || len(f.created) != 0 || len(f.deploys) != 0 {
		t.Fatalf("%d %+v %s", code, f.created, h.said())
	}
	for _, s := range []string{`no app "rsc-sharp" on team "alice"`, "--yes", "homeport link"} {
		if !strings.Contains(h.said(), s) {
			t.Errorf("didn't say %q: %s", s, h.said())
		}
	}
}

// A folder named after an app the team has isn't deployed to it unasked:
// linking it is a choice.
func TestAnUnlinkedFolderNamedAfterAnAppIsntDeployedToIt(t *testing.T) {
	f := newFakeAPI(t)
	h := newHarness(t, f)
	h.login(t)
	unlinked(t, h, "blog")
	h.a.tty = true
	if code := h.run("deploy", "--yes"); code != exitUsage || len(f.created) != 0 || len(f.deploys) != 0 || !strings.Contains(h.said(), "homeport link") {
		t.Fatalf("%d %+v %s", code, f.created, h.said())
	}
}

// A name the folder can't be an app by: it's made one that can, or asked for.
func TestAnAppIsNamedAfterItsFolder(t *testing.T) {
	for dir, want := range map[string]string{
		"rsc-sharp":             "rsc-sharp",
		"My_App.v2":             "my-app-v2",
		"--odd--":               "odd",
		"a  b":                  "a-b",
		"ünïcode":               "n-code",
		strings.Repeat("x", 50): strings.Repeat("x", 40),
		"___":                   "",
	} {
		if got := appNameFor(dir); got != want {
			t.Errorf("appNameFor(%q) = %q, want %q", dir, got, want)
		}
	}
}

// `homeport link --app` names an app the team doesn't have: made, as
// deploy makes it, then linked.
func TestLinkOffersToMakeTheApp(t *testing.T) {
	f := newFakeAPI(t)
	h := newHarness(t, f)
	h.login(t)
	if code := h.run("link", "--app", "web"); code != exitUsage || len(f.created) != 0 || !strings.Contains(h.said(), "--yes") {
		t.Fatalf("no terminal: %d %s", code, h.said())
	}
	h.a.tty = true
	h.a.in = strings.NewReader("yes\n")
	if code := h.run("link", "--app", "web"); code != 0 || len(f.created) != 1 || f.created[0].Name != "web" {
		t.Fatalf("link: %d %+v %s", code, f.created, h.said())
	}
	if l, _, err := config.FindLink(h.a.wd); err != nil || l.Project != "web" || l.Environment != "production" {
		t.Fatalf("link %+v %v", l, err)
	}
}

// The MCP server makes an app when the person approves, and links the
// folder it runs in when nothing links it yet - so deploy deploys there.
func TestTheMCPMakesAnApp(t *testing.T) {
	f := newFakeAPI(t)
	h := newHarness(t, f)
	h.login(t)
	unlinked(t, h, "rsc-sharp")
	no := &person{}
	cs := mcpSession(t, h, no)
	if text, isErr := callTool(t, cs, "create_app", map[string]any{"team": "alice", "name": "rsc-sharp"}); !isErr || !strings.Contains(text, "declined") || len(f.created) != 0 {
		t.Fatalf("declined: %v %s %+v", isErr, text, f.created)
	}
	if len(no.asked) != 1 || !strings.Contains(no.asked[0], "rsc-sharp") || !strings.Contains(no.asked[0], "alice") {
		t.Fatalf("asked %q", no.asked)
	}
	yes := &person{approve: true}
	cs = mcpSession(t, h, yes)
	text, isErr := callTool(t, cs, "create_app", map[string]any{"team": "alice", "name": "rsc-sharp"})
	if isErr || len(f.created) != 1 || f.created[0].Name != "rsc-sharp" || !strings.Contains(text, "rsc-sharp.homeport.app") {
		t.Fatalf("create: %v %s %+v", isErr, text, f.created)
	}
	if l, at, err := config.FindLink(h.a.wd); err != nil || at != h.a.wd || l.Project != "rsc-sharp" {
		t.Fatalf("link %+v %v", l, err)
	}
	if _, isErr := callTool(t, cs, "deploy", map[string]any{}); isErr || len(f.deploys) != 1 {
		t.Fatalf("deploy %v %d", isErr, len(f.deploys))
	}
	// a name the team has is the API's refusal, said to the agent
	if text, isErr := callTool(t, cs, "create_app", map[string]any{"team": "alice", "name": "blog"}); !isErr || !strings.Contains(text, "already has an app called blog") {
		t.Fatalf("taken: %v %s", isErr, text)
	}
}
