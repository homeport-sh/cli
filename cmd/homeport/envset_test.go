package main

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/homeport-sh/cli/envfile"
	"github.com/homeport-sh/cli/internal/config"
)

// a CA's certificate, PEM, as a provider's download gives it
func caPEM(t *testing.T) []byte {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(7), Subject: pkix.Name{CommonName: "project-ca"},
		NotBefore: time.Date(2023, 3, 12, 0, 0, 0, 0, time.UTC), NotAfter: time.Date(2033, 3, 12, 0, 0, 0, 0, time.UTC),
		IsCA: true, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func linkedHarness(t *testing.T) (*fakeAPI, *harness) {
	t.Helper()
	f := newFakeAPI(t)
	h := newHarness(t, f)
	h.login(t)
	if err := config.SaveLink(h.a.wd, config.Link{Team: team1, TeamSlug: "alice", App: blogID, Project: "blog", Environment: "production"}); err != nil {
		t.Fatal(err)
	}
	return f, h
}

func TestEnvSetSetsPlainVariablesAndNeverSaysAValue(t *testing.T) {
	f, h := linkedHarness(t)
	if code := h.run("env", "set", "API_KEY=s3cret", "MODE=a=b"); code != 0 {
		t.Fatalf("%d %s", code, h.said())
	}
	if f.env["API_KEY"] != "s3cret" || f.env["MODE"] != "a=b" || f.envApp != blogID {
		t.Fatalf("set %v on %s", f.env, f.envApp)
	}
	said := h.said()
	if strings.Contains(said, "s3cret") || !strings.Contains(said, "API_KEY") || !strings.Contains(said, "restarts") {
		t.Fatalf("said %s", said)
	}
}

// --from-file reads the file here and sends its bytes; the name comes from
// the file's when none is given, and the path the app reads it at is said.
func TestEnvSetFromFile(t *testing.T) {
	f, h := linkedHarness(t)
	ca := caPEM(t)
	os.WriteFile(filepath.Join(h.a.wd, "do-ca.crt"), ca, 0o600)
	if code := h.run("env", "set", "--from-file", "do-ca.crt"); code != 0 {
		t.Fatalf("%d %s", code, h.said())
	}
	if !bytes.Equal(f.files["DO_CA_CRT"], ca) {
		t.Fatalf("files %v", f.files)
	}
	said := h.said()
	for _, want := range []string{"DO_CA_CRT", "/run/secrets/DO_CA_CRT", "certificate, CN=project-ca, expires 12 Mar 2033"} {
		if !strings.Contains(said, want) {
			t.Fatalf("said %s, want %q", said, want)
		}
	}
	if strings.Contains(said, "BEGIN CERTIFICATE") {
		t.Fatalf("said the content: %s", said)
	}

	// named, the flag after the name; another environment; binary as it is
	bin := []byte{0x30, 0x82, 0, 0xff, '\n', '\r', 0}
	os.WriteFile(filepath.Join(h.a.wd, "client.p12"), bin, 0o600)
	if code := h.run("env", "set", "KEYSTORE", "--from-file", "client.p12", "--env", "staging"); code != 0 {
		t.Fatalf("%d %s", code, h.said())
	}
	if !bytes.Equal(f.files["KEYSTORE"], bin) || f.envApp != stageID {
		t.Fatalf("files %v on %s", f.files, f.envApp)
	}
}

// What the platform would refuse is refused here first, before the file is
// sent anywhere.
func TestEnvSetRefusesBeforeSending(t *testing.T) {
	f, h := linkedHarness(t)
	os.WriteFile(filepath.Join(h.a.wd, "big.pem"), bytes.Repeat([]byte("a"), envfile.MaxFileBytes+1), 0o600)
	os.WriteFile(filepath.Join(h.a.wd, "empty.pem"), nil, 0o600)
	os.WriteFile(filepath.Join(h.a.wd, "ok.pem"), []byte("x"), 0o600)
	for _, c := range []struct {
		args []string
		code int
		says string
	}{
		{[]string{"env", "set", "--from-file", "big.pem"}, exitError, "256 KB"},
		{[]string{"env", "set", "--from-file", "empty.pem"}, exitError, "empty"},
		{[]string{"env", "set", "--from-file", "missing.pem"}, exitError, "missing.pem"},
		{[]string{"env", "set", "LD_PRELOAD", "--from-file", "ok.pem"}, exitError, "reserved"},
		{[]string{"env", "set", "BAD=x", "1BAD=y"}, exitError, "1BAD"},
		{[]string{"env", "set", "MULTI=a\nb"}, exitError, "--from-file"},
		{[]string{"env", "set", "BARE"}, exitUsage, "--from-file"},
		{[]string{"env", "set", "A", "B", "--from-file", "ok.pem"}, exitUsage, "one name"},
		{[]string{"env", "set"}, exitUsage, "usage"},
		{[]string{"env", "unset"}, exitUsage, "usage"},
		{[]string{"env", "unset", "A=1"}, exitUsage, "A=1"},
		{[]string{"env", "nope"}, exitUsage, "usage"},
	} {
		if code := h.run(c.args...); code != c.code || !strings.Contains(h.said(), c.says) {
			t.Errorf("%q: %d %s, want %d saying %q", c.args, code, h.said(), c.code, c.says)
		}
	}
	if f.envPosts != 0 {
		t.Fatalf("%d sets reached the platform", f.envPosts)
	}
}

func TestEnvUnsetAndLs(t *testing.T) {
	f, h := linkedHarness(t)
	f.env = map[string]string{"API_KEY": "s3cret", "MODE": "x"}
	f.files = map[string][]byte{"NODE_EXTRA_CA_CERTS": caPEM(t)}
	if code := h.run("env", "ls"); code != 0 {
		t.Fatalf("%d %s", code, h.said())
	}
	said := h.said()
	for _, want := range []string{"API_KEY", "MODE", "NODE_EXTRA_CA_CERTS", "file", "/run/secrets/NODE_EXTRA_CA_CERTS", "certificate, CN=project-ca, expires 12 Mar 2033"} {
		if !strings.Contains(said, want) {
			t.Fatalf("ls said %s, want %q", said, want)
		}
	}
	if strings.Contains(said, "s3cret") {
		t.Fatalf("ls said a value: %s", said)
	}
	if code := h.run("env", "unset", "API_KEY", "NODE_EXTRA_CA_CERTS"); code != 0 {
		t.Fatalf("%d %s", code, h.said())
	}
	if _, ok := f.env["API_KEY"]; ok || len(f.files) != 0 || f.env["MODE"] != "x" {
		t.Fatalf("after unset %v %v", f.env, f.files)
	}
}

// An agent names a file by its path here; the CLI reads it, so its content
// never passes through the agent, and the person is asked naming it, its
// path and what it is - never its content.
func TestMCPSetsAFileVariableFromAPath(t *testing.T) {
	f := newFakeAPI(t)
	h := newHarness(t, f)
	h.login(t)
	ca := caPEM(t)
	os.WriteFile(filepath.Join(h.a.wd, "do-ca.crt"), ca, 0o600)
	yes := &person{approve: true}
	cs := mcpSession(t, h, yes)
	text, isErr := callTool(t, cs, "set_variables", with(map[string]any{"files": map[string]string{"DB_CA": "do-ca.crt"}}))
	if isErr || !strings.Contains(text, "DB_CA") || strings.Contains(text, "BEGIN") || !bytes.Equal(f.files["DB_CA"], ca) {
		t.Fatalf("set: %v %s %v", isErr, text, f.files)
	}
	if len(yes.asked) != 1 || !strings.Contains(yes.asked[0], "DB_CA") || !strings.Contains(yes.asked[0], "do-ca.crt") ||
		!strings.Contains(yes.asked[0], "certificate, CN=project-ca") || strings.Contains(yes.asked[0], "BEGIN") {
		t.Fatalf("asked %q", yes.asked)
	}
	if text, isErr := callTool(t, cs, "list_variables", blog); isErr || !strings.Contains(text, "DB_CA") || !strings.Contains(text, "/run/secrets/DB_CA") {
		t.Fatalf("list: %v %s", isErr, text)
	}

	// refused here, before anything's asked or sent
	os.WriteFile(filepath.Join(h.a.wd, "big"), bytes.Repeat([]byte("a"), envfile.MaxFileBytes+1), 0o600)
	posts := f.envPosts
	for _, files := range []map[string]string{{"BIG": "big"}, {"GONE": "missing.pem"}, {"LD_PRELOAD": "do-ca.crt"}} {
		if text, isErr := callTool(t, cs, "set_variables", with(map[string]any{"files": files})); !isErr {
			t.Errorf("%v: accepted: %s", files, text)
		}
	}
	if f.envPosts != posts || len(yes.asked) != 1 {
		t.Fatalf("sent %d, asked %q", f.envPosts-posts, yes.asked)
	}
}
