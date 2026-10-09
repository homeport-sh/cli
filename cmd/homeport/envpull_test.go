package main

import (
	"os"
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
	want := "# mine\nFOO=keep\nDATABASE_URL=\"postgres://u:pw@db/x\"\nAWS_ACCESS_KEY_ID=\"AKIA1\"\n"
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
