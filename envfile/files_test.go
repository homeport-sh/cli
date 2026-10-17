package envfile_test

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/homeport-sh/cli/envfile"
)

// A file variable is any bytes, 1 to MaxFileBytes, under a name a plain
// variable could have: the same names, the same reserved ones refused.
func TestCheckFile(t *testing.T) {
	if err := envfile.CheckFile("DB_CA", bytes.Repeat([]byte("a"), envfile.MaxFileBytes)); err != nil {
		t.Fatalf("the most a file holds: %v", err)
	}
	if err := envfile.CheckFile("KEYSTORE", []byte{0, 1, 2, 0xff, '\n', '\r'}); err != nil {
		t.Fatalf("binary: %v", err)
	}
	for name, c := range map[string]struct {
		name    string
		content []byte
		says    string
	}{
		"empty":     {"DB_CA", nil, "empty"},
		"too big":   {"DB_CA", bytes.Repeat([]byte("a"), envfile.MaxFileBytes+1), "256 KB"},
		"bad name":  {"do-ca.crt", []byte("x"), "invalid"},
		"reserved":  {"LD_PRELOAD", []byte("x"), "reserved"},
		"homeport_": {"HOMEPORT_X", []byte("x"), "reserved"},
	} {
		err := envfile.CheckFile(c.name, c.content)
		if err == nil || !strings.Contains(err.Error(), c.says) {
			t.Errorf("%s: %v, want it to say %q", name, err, c.says)
		}
	}
}

// What one change may carry: at most MaxFiles files, MaxFileTotal bytes.
func TestCheckFiles(t *testing.T) {
	quarter := bytes.Repeat([]byte("a"), envfile.MaxFileTotal/4)
	four := map[string][]byte{"A": quarter, "B": quarter, "C": quarter, "D": quarter}
	if err := envfile.CheckFiles(four); err != nil {
		t.Fatalf("1 MB exactly: %v", err)
	}
	four["E"] = []byte("x")
	if err := envfile.CheckFiles(four); err == nil || !strings.Contains(err.Error(), "1 MB") {
		t.Fatalf("over 1 MB: %v", err)
	}
	many := map[string][]byte{}
	for i := range envfile.MaxFiles + 1 {
		many[string(rune('A'+i/26))+string(rune('A'+i%26))] = []byte("x")
	}
	if err := envfile.CheckFiles(many); err == nil || !strings.Contains(err.Error(), "32") {
		t.Fatalf("33 files: %v", err)
	}
	if err := envfile.CheckFiles(map[string][]byte{"OK": []byte("x"), "BAD-NAME": []byte("x")}); err == nil || !strings.Contains(err.Error(), "BAD-NAME") {
		t.Fatalf("each file is checked: %v", err)
	}
}

// One env-sync line per file, which homeportd reads with read -r and an
// older homeportd refuses (it isn't KEY=...): the bytes as they are, base64.
func TestFileLine(t *testing.T) {
	content := []byte("-----BEGIN CERTIFICATE-----\r\nMII\x00\xff\n-----END CERTIFICATE-----\n")
	line, err := envfile.FileLine("DB_CA", content)
	if err != nil {
		t.Fatal(err)
	}
	want := "@file DB_CA " + base64.StdEncoding.EncodeToString(content)
	if line != want {
		t.Fatalf("%q, want %q", line, want)
	}
	if strings.ContainsAny(line, "\n\r\x00") {
		t.Fatal("not one line")
	}
	if _, err := envfile.FileLine("LD_PRELOAD", content); err == nil {
		t.Fatal("a reserved name made a line")
	}
	if _, err := envfile.FileLine("DB_CA", nil); err == nil {
		t.Fatal("an empty file made a line")
	}
}

// A file's name, from what it was called: what the dashboard and the CLI
// suggest (and a person may change).
func TestNameFromFile(t *testing.T) {
	for in, want := range map[string]string{
		"do-ca.crt":                 "DO_CA_CRT",
		"service-account.json":      "SERVICE_ACCOUNT_JSON",
		"2024 bundle.pem":           "_2024_BUNDLE_PEM",
		"..json":                    "JSON",
		"/home/me/certs/client.key": "CLIENT_KEY",
		`C:\certs\rds-ca.pem`:       "RDS_CA_PEM",
		"Ärger.p12":                 "RGER_P12",
		"a--b__c.d":                 "A_B_C_D",
		"___":                       "",
		"":                          "",
	} {
		got := envfile.NameFromFile(in)
		if got != want {
			t.Errorf("NameFromFile(%q) = %q, want %q", in, got, want)
		}
		if got != "" && !envfile.ValidName(got) {
			t.Errorf("NameFromFile(%q) = %q, not a valid name", in, got)
		}
	}
}

func cert(t *testing.T, cn string, notAfter time.Time) (der []byte, key *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: cn},
		NotBefore: notAfter.Add(-time.Hour), NotAfter: notAfter, IsCA: true, BasicConstraintsValid: true}
	der, err = x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return der, key
}

// What may be shown of a file once it's saved: its size and kind, and a
// certificate's subject and expiry (certificates are public) - never a
// key's or a JSON's content.
func TestInspect(t *testing.T) {
	expires := time.Date(2033, 3, 12, 10, 0, 0, 0, time.UTC)
	der, key := cert(t, "project-ca", expires)
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, _ := x509.MarshalPKCS8PrivateKey(key)
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})

	m := envfile.Inspect(certPEM)
	if m.Type != "pem" || m.Bytes != len(certPEM) || len(m.Blocks) != 1 || m.Blocks[0].Kind != "CERTIFICATE" ||
		m.Blocks[0].Subject != "CN=project-ca" || m.Blocks[0].NotAfter == nil || !m.Blocks[0].NotAfter.Equal(expires) {
		t.Fatalf("certificate: %+v", m)
	}
	if s := m.Summary(); s != "certificate, CN=project-ca, expires 12 Mar 2033" {
		t.Fatalf("summary %q", s)
	}

	both := append(append([]byte{}, certPEM...), keyPEM...)
	m = envfile.Inspect(both)
	if len(m.Blocks) != 2 || m.Blocks[1].Kind != "PRIVATE KEY" || m.Blocks[1].Subject != "" || m.Blocks[1].NotAfter != nil {
		t.Fatalf("certificate and key: %+v", m)
	}
	if s := m.Summary(); s != "certificate, CN=project-ca, expires 12 Mar 2033 + private key" {
		t.Fatalf("summary %q", s)
	}
	if s := envfile.Inspect(keyPEM).Summary(); s != "private key" {
		t.Fatalf("key summary %q", s)
	}

	// a block type nobody uses is kept as "other", never as the file says it
	odd := pem.EncodeToMemory(&pem.Block{Type: "SECRET sk_live_123", Bytes: []byte("x")})
	if m := envfile.Inspect(odd); len(m.Blocks) != 1 || m.Blocks[0].Kind != "OTHER" {
		t.Fatalf("odd block: %+v", m)
	}

	if m := envfile.Inspect(der); m.Type != "der" || len(m.Blocks) != 1 || m.Blocks[0].Subject != "CN=project-ca" {
		t.Fatalf("DER: %+v", m)
	}

	sa := []byte(`{"type": "service_account", "project_id": "p", "private_key": "-----BEGIN PRIVATE KEY-----\nabc\n-----END PRIVATE KEY-----\n"}`)
	m = envfile.Inspect(sa)
	if m.Type != "json" || m.JSONKind != "service_account" || len(m.Blocks) != 0 {
		t.Fatalf("service account: %+v", m)
	}
	if s := m.Summary(); s != "service account JSON" {
		t.Fatalf("summary %q", s)
	}
	if m := envfile.Inspect([]byte(`{"a": 1}`)); m.Type != "json" || m.JSONKind != "" || m.Summary() != "JSON" {
		t.Fatalf("json: %+v", m)
	}
	if m := envfile.Inspect([]byte("hello\nworld\n")); m.Type != "text" || m.Summary() != "text, 12 bytes" {
		t.Fatalf("text: %+v %q", m, m.Summary())
	}
	bin := bytes.Repeat([]byte{0, 0xff}, 1100)
	if m := envfile.Inspect(bin); m.Type != "binary" || m.Summary() != "binary, 2.1 KB" {
		t.Fatalf("binary: %+v %q", m, m.Summary())
	}
}
