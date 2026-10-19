package main

import (
	"strings"
	"testing"
)

// --secret and --plain say which a value is (design/secret-variables.md in
// homeport-sh/cloud): a secret's value is never shown again, a variable's
// is, in env ls's VALUE column. Unsaid, a value keeps its kind and a new
// one is a secret.
func TestEnvSetSecretOrPlain(t *testing.T) {
	f, h := linkedHarness(t)
	if code := h.run("env", "set", "STRIPE_KEY=sk_live_1", "--secret"); code != 0 {
		t.Fatalf("%d %s", code, h.said())
	}
	if !f.secretSent["STRIPE_KEY"] || len(f.secretSent) != 1 {
		t.Fatalf("secret sent %v", f.secretSent)
	}
	if said := h.said(); strings.Contains(said, "sk_live_1") || !strings.Contains(said, "a secret") {
		t.Fatalf("said %s", said)
	}
	f, h = linkedHarness(t)
	if code := h.run("env", "set", "TZ=Pacific/Fiji", "LOG_LEVEL=debug", "--plain"); code != 0 {
		t.Fatalf("%d %s", code, h.said())
	}
	if v, ok := f.secretSent["TZ"]; !ok || v || f.secretSent["LOG_LEVEL"] {
		t.Fatalf("secret sent %v", f.secretSent)
	}
	if said := h.said(); !strings.Contains(said, "variables: their values are shown") {
		t.Fatalf("said %s", said)
	}
}

func TestEnvSetUnsaidSendsNoKind(t *testing.T) {
	f, h := linkedHarness(t)
	if code := h.run("env", "set", "A=1"); code != 0 {
		t.Fatalf("%d %s", code, h.said())
	}
	if f.secretSent != nil {
		t.Fatalf("a kind sent unasked: %v", f.secretSent)
	}
}

// a name alone: its kind changed, its value kept
func TestEnvSetKindAlone(t *testing.T) {
	f, h := linkedHarness(t)
	f.env = map[string]string{"TZ": "UTC"}
	f.secret = map[string]bool{"TZ": false}
	if code := h.run("env", "set", "TZ", "--secret"); code != 0 {
		t.Fatalf("%d %s", code, h.said())
	}
	if len(f.setSent) != 0 || !f.secretSent["TZ"] {
		t.Fatalf("set %v secret %v", f.setSent, f.secretSent)
	}
}

func TestEnvSetForSecret(t *testing.T) {
	f, h := linkedHarness(t)
	if code := h.run("env", "set", "MIGRATION_DATABASE_URL=postgres://ddl", "--for", "release", "--plain"); code != 0 {
		t.Fatalf("%d %s", code, h.said())
	}
	if v, ok := f.rolesSent["release"].Secret["MIGRATION_DATABASE_URL"]; !ok || v {
		t.Fatalf("roles sent %+v", f.rolesSent)
	}
}

func TestEnvSecretRefused(t *testing.T) {
	for _, args := range [][]string{
		{"set", "A=1", "--secret", "--plain"},
		{"set", "A", "--plain", "--from-file", "x.pem"},
		{"set", "A", "--for", "release", "--secret"},
	} {
		f, h := linkedHarness(t)
		if code := h.run(append([]string{"env"}, args...)...); code == 0 {
			t.Fatalf("%v took: %s", args, h.said())
		}
		if f.envPosts != 0 {
			t.Fatalf("%v sent", args)
		}
	}
}

// env ls: KIND says secret, variable or file; VALUE a variable's value
// alone, never a secret's
func TestEnvLsShowsVariablesNotSecrets(t *testing.T) {
	f, h := linkedHarness(t)
	f.env = map[string]string{"TZ": "Pacific/Fiji", "STRIPE_KEY": "sk_live_1"}
	f.secret = map[string]bool{"TZ": false, "STRIPE_KEY": true}
	if code := h.run("env", "ls"); code != 0 {
		t.Fatalf("%d %s", code, h.said())
	}
	said := h.said()
	if strings.Contains(said, "sk_live_1") || !strings.Contains(said, "VALUE") {
		t.Fatalf("said %s", said)
	}
	rows := map[string][]string{}
	for l := range strings.Lines(said) {
		if fs := strings.Fields(l); len(fs) > 0 {
			rows[fs[0]] = fs
		}
	}
	if r := rows["TZ"]; !strings.Contains(strings.Join(r, " "), "variable own - Pacific/Fiji") {
		t.Fatalf("TZ: %v", r)
	}
	if r := rows["STRIPE_KEY"]; !strings.Contains(strings.Join(r, " "), "secret own") {
		t.Fatalf("STRIPE_KEY: %v", r)
	}
}

// An agent says which a value is too, asked of the person with it; a file
// marked a variable is refused before anything's asked.
func TestMCPSetsSecretOrVariable(t *testing.T) {
	f := newFakeAPI(t)
	h := newHarness(t, f)
	h.login(t)
	yes := &person{approve: true}
	cs := mcpSession(t, h, yes)
	text, isErr := callTool(t, cs, "set_variables", with(map[string]any{"set": map[string]string{"TZ": "UTC"}, "secret": map[string]bool{"TZ": false}}))
	if isErr || f.secretSent["TZ"] || len(f.secretSent) != 1 || !strings.Contains(yes.asked[0], "TZ: a variable") {
		t.Fatalf("set: %v %s %v asked %q", isErr, text, f.secretSent, yes.asked)
	}
	posts := f.envPosts
	if text, isErr := callTool(t, cs, "set_variables", with(map[string]any{"files": map[string]string{"CA": "ca.pem"}, "secret": map[string]bool{"CA": false}})); !isErr || f.envPosts != posts || !strings.Contains(text, "always a secret") {
		t.Fatalf("a shown file: %v %s", isErr, text)
	}
}
