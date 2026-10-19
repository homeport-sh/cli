package main

import (
	"strings"
	"testing"
)

// refsHarness is an environment with references (design/variable-references.md
// in homeport-sh/cloud): a variable naming another, a secret naming a
// secret, and one naming nothing.
func refsHarness(t *testing.T) (*fakeAPI, *harness) {
	f, h := linkedHarness(t)
	f.env = map[string]string{"APP_URL": "https://cloudtrain.ai", "VITE_APP_URL": "${{APP_URL}}/app", "TOKEN": "t0k", "AUTH": "Bearer ${{TOKEN}}"}
	f.secret = map[string]bool{"APP_URL": false, "VITE_APP_URL": false, "TOKEN": true, "AUTH": true}
	f.refs = map[string][]string{"VITE_APP_URL": {"APP_URL"}, "AUTH": {"TOKEN"}}
	f.resolved = map[string]string{"VITE_APP_URL": "https://cloudtrain.ai/app"}
	return f, h
}

// env ls's REF column names what each value references - a secret's too,
// a name and never a value; VALUE is a variable's text as written.
func TestEnvLsSaysWhatAValueNames(t *testing.T) {
	_, h := refsHarness(t)
	if code := h.run("env", "ls"); code != 0 {
		t.Fatalf("%d %s", code, h.said())
	}
	said := h.said()
	if strings.Contains(said, "t0k") || strings.Contains(said, "Bearer") {
		t.Fatalf("a secret shown: %s", said)
	}
	rows := map[string]string{}
	for l := range strings.Lines(said) {
		if fs := strings.Fields(l); len(fs) > 0 {
			rows[fs[0]] = strings.Join(fs, " ")
		}
	}
	if !strings.Contains(rows["NAME"], "SOURCE REF VALUE") {
		t.Fatalf("header %q", rows["NAME"])
	}
	for name, want := range map[string]string{
		"VITE_APP_URL": "variable own APP_URL ${{APP_URL}}/app",
		"AUTH":         "secret own TOKEN",
		"APP_URL":      "variable own - https://cloudtrain.ai",
		"TOKEN":        "secret own -",
	} {
		if !strings.HasSuffix(rows[name], want) {
			t.Fatalf("%s: %q, want …%q", name, rows[name], want)
		}
	}
}

// env get prints a variable's value as written; --resolved, as it runs.
// A secret's is never printed, resolved or not.
func TestEnvGet(t *testing.T) {
	_, h := refsHarness(t)
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"VITE_APP_URL"}, "${{APP_URL}}/app\n"},
		{[]string{"VITE_APP_URL", "--resolved"}, "https://cloudtrain.ai/app\n"},
		{[]string{"--resolved", "VITE_APP_URL"}, "https://cloudtrain.ai/app\n"},
		{[]string{"APP_URL", "--resolved"}, "https://cloudtrain.ai\n"},
	} {
		if code := h.run(append([]string{"env", "get"}, c.args...)...); code != 0 || h.out.String() != c.want {
			t.Fatalf("%v: %d %q, want %q", c.args, code, h.said(), c.want)
		}
	}
	for _, c := range []struct {
		args []string
		says string
	}{
		{[]string{"AUTH", "--resolved"}, "AUTH is a secret: its value is never shown"},
		{[]string{"TOKEN"}, "TOKEN is a secret: its value is never shown"},
		{[]string{"NOPE"}, "NOPE isn't set"},
		{[]string{}, "usage"},
		{[]string{"A", "B"}, "usage"},
	} {
		code := h.run(append([]string{"env", "get"}, c.args...)...)
		if code == 0 || !strings.Contains(h.said(), c.says) || strings.Contains(h.said(), "t0k") {
			t.Fatalf("%v: %d %q, want %q", c.args, code, h.said(), c.says)
		}
	}
}

// An agent's list says what each value names, never a value.
func TestMCPListSaysWhatAValueNames(t *testing.T) {
	_, h := refsHarness(t)
	cs := mcpSession(t, h, nil)
	text, isErr := callTool(t, cs, "list_variables", blog)
	if isErr || !strings.Contains(strings.Join(strings.Fields(text), ""), `"name":"AUTH","refs":["TOKEN"]`) || strings.Contains(text, "t0k") || strings.Contains(text, "cloudtrain.ai") {
		t.Fatalf("list %v %s", isErr, text)
	}
}
