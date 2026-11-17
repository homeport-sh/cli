package main

import (
	"strings"
	"testing"
)

// design/safe-env-change.md: a change is started on the app as a release
// is, and refused when the app won't start on it. set waits to say which.

func pending() map[string]any { return map[string]any{"state": "pending", "version": 2} }

func TestEnvSetWaitsUntilTheValuesAreLive(t *testing.T) {
	f, h := linkedHarness(t)
	f.delivery = []map[string]any{pending(), pending(), {"state": "live", "version": 2}}
	if code := h.run("env", "set", "API_KEY=s3cret"); code != 0 {
		t.Fatalf("%d %s", code, h.said())
	}
	if said := h.said(); !strings.Contains(said, "Starting the app with the new values… live.\n") {
		t.Fatalf("said %s", said)
	}
}

func TestEnvSetSaysARefusalAndFails(t *testing.T) {
	f, h := linkedHarness(t)
	f.delivery = []map[string]any{pending(), {"state": "rejected", "version": 2,
		"detail": "the app's last output:\nconfig: API_KEY must be 32 characters\nhomeportd: env change refused: the web failed with the new values — the old values are live again"}}
	code := h.run("env", "set", "API_KEY=short")
	said := h.said()
	if code == 0 {
		t.Fatalf("exit 0: %s", said)
	}
	for _, want := range []string{"Starting the app with the new values… refused.", "config: API_KEY must be 32 characters",
		"The old values are still live. Fix the value and set it again."} {
		if !strings.Contains(said, want) {
			t.Fatalf("said %s, want %q", said, want)
		}
	}
	if strings.Contains(said, "short") {
		t.Fatalf("said the value: %s", said)
	}
}

func TestEnvSetNoWait(t *testing.T) {
	f, h := linkedHarness(t)
	f.delivery = []map[string]any{pending()}
	if code := h.run("env", "set", "--no-wait", "API_KEY=s3cret"); code != 0 {
		t.Fatalf("%d %s", code, h.said())
	}
	if said := h.said(); strings.Contains(said, "Starting") {
		t.Fatalf("waited: %s", said)
	}
}

// Nothing deployed yet (or Save and rebuild): its next deploy takes them.
func TestEnvSetBeforeADeployDoesntWait(t *testing.T) {
	f, h := linkedHarness(t)
	f.delivery = []map[string]any{{"state": "deploy", "version": 2}}
	if code := h.run("env", "set", "API_KEY=s3cret"); code != 0 {
		t.Fatalf("%d %s", code, h.said())
	}
	if said := h.said(); !strings.Contains(said, "Its next deploy starts it with them.\n") || strings.Contains(said, "Starting") {
		t.Fatalf("said %s", said)
	}
}

// A platform before delivery says nothing of it: nothing's waited for.
func TestEnvSetOnAnOlderPlatform(t *testing.T) {
	_, h := linkedHarness(t)
	if code := h.run("env", "set", "API_KEY=s3cret"); code != 0 || strings.Contains(h.said(), "Starting") {
		t.Fatalf("%d %s", code, h.said())
	}
}

func TestEnvUnsetSaysARefusal(t *testing.T) {
	f, h := linkedHarness(t)
	f.delivery = []map[string]any{{"state": "rejected", "version": 2, "detail": "homeportd: env change refused: a process failed with the new values — the old values are live again"}}
	if code := h.run("env", "unset", "API_KEY"); code == 0 || !strings.Contains(h.said(), "refused") {
		t.Fatalf("%d %s", code, h.said())
	}
}
