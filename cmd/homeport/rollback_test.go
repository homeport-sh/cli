package main

import (
	"strings"
	"testing"
	"time"

	"github.com/homeport-sh/cli/buildplan"
	"github.com/homeport-sh/cli/internal/cloud"
)

const (
	liveDeploy    = "d0000000-0000-4000-8000-000000000003"
	earlierDeploy = "d0000000-0000-4000-8000-000000000002"
	oldestDeploy  = "d0000000-0000-4000-8000-000000000001"
)

// rollbackHarness is linked to blog, whose earlier releases can be rolled
// back to: one with a release command, built with other build variables.
func rollbackHarness(t *testing.T) (*fakeAPI, *harness) {
	t.Helper()
	f, h := linkedHarness(t)
	at := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	f.apps[0].Deploys = []cloud.Deploy{
		{ID: liveDeploy, SHA: strings.Repeat("c", 40), Status: "live", CreatedAt: at.Add(48 * time.Hour)},
		{ID: earlierDeploy, SHA: strings.Repeat("b", 40), Status: "live", CreatedAt: at.Add(24 * time.Hour),
			Rollback: &cloud.RollbackOffer{KeptUntil: at.Add(29 * 24 * time.Hour), Release: buildplan.One("./app migrate"), BuildVariablesDiffer: true}},
		{ID: oldestDeploy, SHA: strings.Repeat("a", 40), Status: "live", CreatedAt: at,
			Rollback: &cloud.RollbackOffer{KeptUntil: at.Add(28 * 24 * time.Hour)}},
	}
	return f, h
}

// `homeport rollback` alone lists what can be rolled back to, and changes
// nothing.
func TestRollbackListsWhatCanBeRolledBackTo(t *testing.T) {
	f, h := rollbackHarness(t)
	if code := h.run("rollback"); code != 0 {
		t.Fatalf("%d %s", code, h.said())
	}
	said := h.said()
	for _, want := range []string{"d0000000", "bbbbbbb", "aaaaaaa", "./app migrate", "other build variables", "Nov 8"} {
		if !strings.Contains(said, want) {
			t.Fatalf("doesn't say %q: %s", want, said)
		}
	}
	if strings.Contains(said, "ccccccc") {
		t.Fatalf("offers the live release: %s", said)
	}
	if len(f.rollbacks) != 0 {
		t.Fatalf("rolled back: %v", f.rollbacks)
	}
}

// Without a terminal to ask in, a rollback needs --yes: a script never
// rolls back by accident. With it, it ships and follows the release to
// the address, its release command skipped.
func TestRollbackByCommitNeedsYesWithoutATerminal(t *testing.T) {
	f, h := rollbackHarness(t)
	if code := h.run("rollback", "bbbbbbb"); code != exitUsage || len(f.rollbacks) != 0 {
		t.Fatalf("without --yes: %d %v %s", code, f.rollbacks, h.said())
	}
	if code := h.run("rollback", "bbbbbbb", "--yes"); code != 0 {
		t.Fatalf("%d %s", code, h.said())
	}
	if len(f.rollbacks) != 1 || f.rollbacks[0].Deploy != earlierDeploy || f.rollbacks[0].RunRelease {
		t.Fatalf("asked %+v", f.rollbacks)
	}
	if said := h.said(); !strings.Contains(said, "Live: https://blog.homeport.app") || strings.Contains(said, "building") {
		t.Fatalf("said %s", said)
	}
}

// In a terminal it asks first; --run-release runs its release command.
func TestRollbackAsksInATerminal(t *testing.T) {
	f, h := rollbackHarness(t)
	h.a.tty = true
	h.a.in = strings.NewReader("n\n")
	if code := h.run("rollback", "d0000000-0000-4000-8000-000000000002"); code != 0 || len(f.rollbacks) != 0 {
		t.Fatalf("answered no: %d %v %s", code, f.rollbacks, h.said())
	}
	h.a.in, h.a.lines = strings.NewReader("y\n"), nil
	if code := h.run("rollback", "--run-release", "d0000000-0000-4000-8000-000000000002"); code != 0 {
		t.Fatalf("%d %s", code, h.said())
	}
	if len(f.rollbacks) != 1 || !f.rollbacks[0].RunRelease {
		t.Fatalf("asked %+v", f.rollbacks)
	}
	if !strings.Contains(h.said(), "Roll back") {
		t.Fatalf("didn't ask: %s", h.said())
	}
}

// What isn't on the list - the live release, a prefix two match, nothing -
// is refused, saying what is.
func TestRollbackRefusesWhatItCantRollBackTo(t *testing.T) {
	f, h := rollbackHarness(t)
	for _, which := range []string{"ccccccc", "d0000000", "1234567", "abc"} {
		if code := h.run("rollback", which, "--yes"); code != exitUsage {
			t.Fatalf("%s: %d %s", which, code, h.said())
		}
	}
	if len(f.rollbacks) != 0 {
		t.Fatalf("rolled back: %v", f.rollbacks)
	}
}
