package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/homeport-sh/cli/internal/config"
)

func TestEnvPullWritesTheAddOnCredentialsAndNothingElse(t *testing.T) {
	f := newFakeAPI(t)
	h := newHarness(t, f)
	h.login(t)
	if err := config.SaveLink(h.a.wd, config.Link{Team: team1, TeamSlug: "alice", App: blogID, Project: "blog", Environment: "production"}); err != nil {
		t.Fatal(err)
	}
	local := filepath.Join(h.a.wd, ".env.local")
	os.WriteFile(local, []byte("# mine\nFOO=keep\nDATABASE_URL=old\nexport DATABASE_URL=older\n"), 0o644)
	os.WriteFile(filepath.Join(h.a.wd, ".gitignore"), []byte("node_modules\n"), 0o644)

	// the link's app, its staging environment
	if code := h.run("env", "pull", "--env", "staging"); code != 0 {
		t.Fatalf("%d %s", code, h.said())
	}
	if len(f.pulled) != 1 || f.pulled[0] != stageID {
		t.Fatalf("pulled %v", f.pulled)
	}
	b, _ := os.ReadFile(local)
	want := "# homeport env pull: AWS_ACCESS_KEY_ID DATABASE_URL\n# mine\nFOO=keep\nDATABASE_URL=\"postgres://u:pw@db/x\"\nAWS_ACCESS_KEY_ID=\"AKIA1\"\n"
	if string(b) != want {
		t.Fatalf(".env.local\n%s\nwant\n%s", b, want)
	}
	if info, _ := os.Stat(local); info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", info.Mode())
	}
	if g, _ := os.ReadFile(filepath.Join(h.a.wd, ".gitignore")); string(g) != "node_modules\n.env.local\n" {
		t.Fatalf(".gitignore %q", g)
	}
	said := h.said()
	if strings.Contains(said, "postgres://") || strings.Contains(said, "AKIA1") || !strings.Contains(said, "DATABASE_URL") {
		t.Fatalf("said %s", said)
	}
	// again: nothing doubles
	h.run("env", "pull", "--env", "staging")
	if b2, _ := os.ReadFile(local); string(b2) != want {
		t.Fatalf("twice\n%s", b2)
	}
	if g, _ := os.ReadFile(filepath.Join(h.a.wd, ".gitignore")); strings.Count(string(g), ".env.local") != 1 {
		t.Fatalf(".gitignore %q", g)
	}
}

// Production's are never pulled - the server refuses, and nothing is written.
func TestEnvPullRefusesProduction(t *testing.T) {
	f := newFakeAPI(t)
	h := newHarness(t, f)
	h.login(t)
	config.SaveLink(h.a.wd, config.Link{Team: team1, TeamSlug: "alice", App: blogID, Project: "blog", Environment: "production"})
	if code := h.run("env", "pull"); code != exitError || !strings.Contains(h.err.String(), "production") {
		t.Fatalf("%d %s", code, h.said())
	}
	if _, err := os.Stat(filepath.Join(h.a.wd, ".env.local")); !os.IsNotExist(err) {
		t.Fatal("wrote .env.local")
	}
}

// A .gitignore that covers it already is left alone.
func TestEnvPullLeavesACoveringGitignore(t *testing.T) {
	f := newFakeAPI(t)
	h := newHarness(t, f)
	h.login(t)
	config.SaveLink(h.a.wd, config.Link{Team: team1, TeamSlug: "alice", App: stageID, Project: "blog", Environment: "staging"})
	os.WriteFile(filepath.Join(h.a.wd, ".gitignore"), []byte(".env*\n"), 0o644)
	if code := h.run("env", "pull"); code != 0 {
		t.Fatalf("%d %s", code, h.said())
	}
	if g, _ := os.ReadFile(filepath.Join(h.a.wd, ".gitignore")); string(g) != ".env*\n" {
		t.Fatalf(".gitignore %q", g)
	}
}

// A key it wrote that the environment no longer has (a database detached)
// goes; a line of yours stays.
func TestEnvPullRemovesWhatItWroteThatsGone(t *testing.T) {
	f := newFakeAPI(t)
	h := newHarness(t, f)
	h.login(t)
	config.SaveLink(h.a.wd, config.Link{Team: team1, TeamSlug: "alice", App: stageID, Project: "blog", Environment: "staging"})
	local := filepath.Join(h.a.wd, ".env.local")
	os.WriteFile(local, []byte("FOO=keep\n"), 0o600)
	if code := h.run("env", "pull"); code != 0 {
		t.Fatalf("%d %s", code, h.said())
	}
	f.pullValues = map[string]string{"AWS_ACCESS_KEY_ID": "AKIA2"}
	if code := h.run("env", "pull"); code != 0 {
		t.Fatalf("%d %s", code, h.said())
	}
	b, _ := os.ReadFile(local)
	if want := "# homeport env pull: AWS_ACCESS_KEY_ID\nFOO=keep\nAWS_ACCESS_KEY_ID=\"AKIA2\"\n"; string(b) != want {
		t.Fatalf(".env.local\n%s\nwant\n%s", b, want)
	}
}

// A link's labels can't point a pull at another environment: it's named as
// the API names its ids. And an app its own CI deploys has add-ons too.
func TestEnvPullTrustsTheIdsNotTheLabels(t *testing.T) {
	f := newFakeAPI(t)
	h := newHarness(t, f)
	h.login(t)
	config.SaveLink(h.a.wd, config.Link{Team: team1, TeamSlug: "alice", App: blogID, Project: "blog", Environment: "staging"})
	if code := h.run("env", "pull"); code != exitUsage || len(f.pulled) != 0 {
		t.Fatalf("a mislabelled link: %d %s", code, h.said())
	}
	if code := h.run("env", "pull", "--team", "alice", "--app", "api"); code != exitError || len(f.pulled) != 1 || f.pulled[0] != apiID {
		t.Fatalf("a CI app (production: refused by the API): %d %v %s", code, f.pulled, h.said())
	}
}

// What production shares is withheld by the platform, and it says so.
func TestEnvPullSaysWhatWasWithheld(t *testing.T) {
	f := newFakeAPI(t)
	f.pullValues, f.withheld = map[string]string{"AWS_ACCESS_KEY_ID": "AKIA1"}, []string{"DATABASE_URL"}
	h := newHarness(t, f)
	h.login(t)
	config.SaveLink(h.a.wd, config.Link{Team: team1, TeamSlug: "alice", App: stageID, Project: "blog", Environment: "staging"})
	if code := h.run("env", "pull"); code != 0 || !strings.Contains(h.said(), "DATABASE_URL") || !strings.Contains(h.said(), "production") {
		t.Fatalf("%d %s", code, h.said())
	}
}

// In a git checkout, git says whether .env.local is ignored - a pattern in
// a .gitignore above counts - and a .gitignore that can't be written means
// nothing is written.
func TestEnvPullAsksGitWhatsIgnored(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	f := newFakeAPI(t)
	h := newHarness(t, f)
	h.login(t)
	root := h.a.wd
	exec.Command("git", "-C", root, "init", "-q").Run()
	os.WriteFile(filepath.Join(root, ".gitignore"), []byte("*.local\n"), 0o644)
	app := filepath.Join(root, "web")
	os.MkdirAll(app, 0o755)
	config.SaveLink(app, config.Link{Team: team1, TeamSlug: "alice", App: stageID, Project: "blog", Environment: "staging"})
	h.a.wd = app
	if code := h.run("env", "pull"); code != 0 {
		t.Fatalf("%d %s", code, h.said())
	}
	if _, err := os.Stat(filepath.Join(app, ".gitignore")); !os.IsNotExist(err) {
		t.Fatal("a .gitignore was added though git ignores .env.local already")
	}

	// a .gitignore it can't write: nothing written
	other := t.TempDir()
	config.SaveLink(other, config.Link{Team: team1, TeamSlug: "alice", App: stageID, Project: "blog", Environment: "staging"})
	os.Mkdir(filepath.Join(other, ".gitignore"), 0o755) // a folder: not writable as a file
	h.a.wd = other
	if code := h.run("env", "pull"); code == 0 {
		t.Fatalf("wrote with no .gitignore: %s", h.said())
	}
	if _, err := os.Stat(filepath.Join(other, ".env.local")); !os.IsNotExist(err) {
		t.Fatal("wrote .env.local before ignoring it")
	}
}
