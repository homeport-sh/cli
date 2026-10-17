package envfile

import (
	"bytes"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"path"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// File variables (design/file-variables.md in homeport cloud): a variable
// whose value is a file - a CA, a client certificate and key, a service
// account's JSON, a keystore - that every sandbox of the app sees read-only
// at /run/secrets/<NAME>, with $NAME holding that path. Any bytes, text or
// binary. The same limits on every plan.
const (
	// MaxFileBytes is the most one file holds: AWS RDS's global bundle
	// (every region's CA, about 160 KB) fits.
	MaxFileBytes = 256 << 10
	// MaxFileTotal is the most an environment's files hold together.
	MaxFileTotal = 1 << 20
	// MaxFiles is the most file variables an environment has.
	MaxFiles = 32
)

// SecretsDir is where a sandbox sees its file variables.
const SecretsDir = "/run/secrets"

// CheckFile says whether homeportd could take content as file variable
// name: a name a plain variable could have, and 1 to MaxFileBytes bytes.
func CheckFile(name string, content []byte) error {
	if !ValidName(name) {
		return fmt.Errorf("invalid env var name %q (letters, digits and _, not starting with a digit)", name)
	}
	if Reserved(name) {
		return fmt.Errorf("env var name %q is reserved: it would change how a shell or the dynamic linker behaves", name)
	}
	if len(content) == 0 {
		return fmt.Errorf("%s: the file is empty", name)
	}
	if len(content) > MaxFileBytes {
		return fmt.Errorf("%s: the file is %s; a file variable holds at most %s", name, Size(len(content)), Size(MaxFileBytes))
	}
	return nil
}

// CheckFiles checks each file, and that together they're within what an
// environment holds: MaxFiles files, MaxFileTotal bytes.
func CheckFiles(files map[string][]byte) error {
	names := slices.Sorted(func(yield func(string) bool) {
		for n := range files {
			if !yield(n) {
				return
			}
		}
	})
	total := 0
	for _, n := range names {
		if err := CheckFile(n, files[n]); err != nil {
			return err
		}
		total += len(files[n])
	}
	if len(files) > MaxFiles {
		return fmt.Errorf("%d file variables: an environment has at most %d", len(files), MaxFiles)
	}
	if total > MaxFileTotal {
		return fmt.Errorf("the files would total %s: an environment's hold at most %s", Size(total), Size(MaxFileTotal))
	}
	return nil
}

// FileLine is one file's env-sync line: "@file NAME <base64>", the bytes as
// they are. One line, so homeportd reads it with read -r; and not KEY=..., so
// a homeportd from before file variables refuses the whole sync.
func FileLine(name string, content []byte) (string, error) {
	if err := CheckFile(name, content); err != nil {
		return "", err
	}
	return "@file " + name + " " + base64.StdEncoding.EncodeToString(content), nil
}

// NameFromFile is the variable name a file's name suggests: upper case,
// every run of anything but A-Z and 0-9 one _, none at either end, and _
// first if it starts with a digit (do-ca.crt is DO_CA_CRT). "" if nothing
// is left. The dashboard has the same rule.
func NameFromFile(filename string) string {
	base := path.Base(strings.ReplaceAll(filename, `\`, "/"))
	var b strings.Builder
	under := false
	for _, r := range strings.ToUpper(base) {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			if under && b.Len() > 0 {
				b.WriteByte('_')
			}
			under = false
			b.WriteRune(r)
			continue
		}
		under = true
	}
	name := b.String()
	if name != "" && name[0] >= '0' && name[0] <= '9' {
		name = "_" + name
	}
	return name
}

// FileMeta is what may be shown of a file variable once it's saved: its
// size and kind, and of a certificate (public) its subject and expiry. Never
// a key's, a JSON's or a text's content.
type FileMeta struct {
	Bytes int `json:"bytes"`
	// Type: pem, der (a certificate), json, text or binary
	Type   string     `json:"type"`
	Blocks []PEMBlock `json:"blocks,omitempty"`
	// JSONKind: service_account for a Google service account's key
	JSONKind string `json:"json_kind,omitempty"`
}

// PEMBlock is one block of a PEM file, or a DER certificate.
type PEMBlock struct {
	// Kind: CERTIFICATE, a kind of key, ... or OTHER (pemKinds)
	Kind     string     `json:"kind"`
	Subject  string     `json:"subject,omitempty"`
	NotAfter *time.Time `json:"not_after,omitempty"`
}

// pemKinds are the block types kept by name; any other is OTHER, so what a
// file calls its blocks is never stored as it is.
var pemKinds = []string{
	"CERTIFICATE", "TRUSTED CERTIFICATE", "X509 CRL", "CERTIFICATE REQUEST", "NEW CERTIFICATE REQUEST",
	"PRIVATE KEY", "ENCRYPTED PRIVATE KEY", "RSA PRIVATE KEY", "EC PRIVATE KEY", "DSA PRIVATE KEY", "OPENSSH PRIVATE KEY",
	"PUBLIC KEY", "RSA PUBLIC KEY", "EC PARAMETERS", "DH PARAMETERS", "PGP PRIVATE KEY BLOCK", "PGP PUBLIC KEY BLOCK",
}

// Inspect is what may be shown of content (FileMeta).
func Inspect(content []byte) FileMeta {
	m := FileMeta{Bytes: len(content)}
	rest := content
	for {
		var b *pem.Block
		b, rest = pem.Decode(rest)
		if b == nil {
			break
		}
		kind := b.Type
		if !slices.Contains(pemKinds, kind) {
			kind = "OTHER"
		}
		pb := PEMBlock{Kind: kind}
		if kind == "CERTIFICATE" || kind == "TRUSTED CERTIFICATE" {
			certBlock(&pb, b.Bytes)
		}
		m.Blocks = append(m.Blocks, pb)
	}
	switch {
	case len(m.Blocks) > 0:
		m.Type = "pem"
	case isDERCert(content):
		m.Type = "der"
		pb := PEMBlock{Kind: "CERTIFICATE"}
		certBlock(&pb, content)
		m.Blocks = []PEMBlock{pb}
	case json.Valid(bytes.TrimSpace(content)):
		m.Type = "json"
		var probe struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(content, &probe) == nil && probe.Type == "service_account" {
			m.JSONKind = "service_account"
		}
	case utf8.Valid(content) && !bytes.ContainsRune(content, 0):
		m.Type = "text"
	default:
		m.Type = "binary"
	}
	return m
}

func isDERCert(b []byte) bool {
	_, err := x509.ParseCertificate(b)
	return err == nil
}

// certBlock fills a certificate's subject and expiry, when it parses.
func certBlock(pb *PEMBlock, der []byte) {
	c, err := x509.ParseCertificate(der)
	if err != nil {
		return
	}
	pb.Subject = c.Subject.String()
	na := c.NotAfter.UTC()
	pb.NotAfter = &na
}

// Summary is FileMeta in a few words: "certificate, CN=…, expires 12 Mar
// 2033", "private key", "service account JSON", "binary, 2.1 KB".
func (m FileMeta) Summary() string {
	switch m.Type {
	case "pem", "der":
		parts := make([]string, 0, len(m.Blocks))
		for _, b := range m.Blocks {
			parts = append(parts, b.summary())
		}
		return strings.Join(parts, " + ")
	case "json":
		if m.JSONKind == "service_account" {
			return "service account JSON"
		}
		return "JSON"
	case "text":
		return "text, " + Size(m.Bytes)
	}
	return "binary, " + Size(m.Bytes)
}

func (b PEMBlock) summary() string {
	switch {
	case b.Kind == "CERTIFICATE" || b.Kind == "TRUSTED CERTIFICATE":
		s := "certificate"
		if b.Subject != "" {
			s += ", " + b.Subject
		}
		if b.NotAfter != nil {
			s += ", expires " + b.NotAfter.Format("2 Jan 2006")
		}
		return s
	case strings.HasSuffix(b.Kind, "PRIVATE KEY") || b.Kind == "PGP PRIVATE KEY BLOCK":
		return "private key"
	case strings.HasSuffix(b.Kind, "PUBLIC KEY") || b.Kind == "PGP PUBLIC KEY BLOCK":
		return "public key"
	case b.Kind == "OTHER":
		return "PEM block"
	}
	return strings.ToLower(b.Kind)
}

// Size is n bytes as people read it: 12 bytes, 2.1 KB, 256 KB, 1 MB.
func Size(n int) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%d bytes", n)
	case n < 1<<20:
		return trimZero(float64(n)/1024) + " KB"
	}
	return trimZero(float64(n)/(1<<20)) + " MB"
}

func trimZero(f float64) string {
	return strings.TrimSuffix(fmt.Sprintf("%.1f", f), ".0")
}
