package artifact_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/homeport-sh/cli/artifact"
	"github.com/homeport-sh/cli/artifact/artifacttest"
)

const glibcLoader = "/lib64/ld-linux-x86-64.so.2"

func writeExec(t *testing.T, dir string, b []byte) string {
	t.Helper()
	p := filepath.Join(dir, "bin")
	if err := os.WriteFile(p, b, 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// An executable starts on a server when its loader is glibc's and each
// library it needs is one the servers lend every app - or, for a bundle,
// one it carries, found through a RUNPATH relative to it ($ORIGIN). A
// static one needs nothing; a file that isn't an ELF isn't this check's.
func TestCheckLibraries(t *testing.T) {
	for name, c := range map[string]struct {
		exe     []byte
		bundled bool
	}{
		"rust, rustls":         {exe: artifacttest.Exec(glibcLoader, []string{"libgcc_s.so.1", "libm.so.6", "libc.so.6"}, "")},
		"node":                 {exe: artifacttest.Exec(glibcLoader, []string{"libdl.so.2", "libstdc++.so.6", "libm.so.6", "libgcc_s.so.1", "libpthread.so.0", "libc.so.6"}, "")},
		"native-tls":           {exe: artifacttest.Exec(glibcLoader, []string{"libssl.so.3", "libcrypto.so.3", "libz.so.1", "libc.so.6"}, "")},
		"arm64's loader":       {exe: artifacttest.Exec("/lib/ld-linux-aarch64.so.1", []string{"libc.so.6"}, "")},
		"static":               {exe: artifacttest.Exec("", nil, "")},
		"not an ELF":           {exe: []byte("#!/bin/sh\nexec ./server\n")},
		"its own libpq":        {exe: artifacttest.Exec(glibcLoader, []string{"libpq.so.5", "libc.so.6"}, "$ORIGIN/lib"), bundled: true},
		"its own, ${ORIGIN}/.": {exe: artifacttest.Exec(glibcLoader, []string{"libpq.so.5", "libc.so.6"}, "/usr/local/lib:${ORIGIN}/lib/."), bundled: true},
	} {
		dir := t.TempDir()
		p := writeExec(t, dir, c.exe)
		root := ""
		if c.bundled {
			root = dir
			if err := os.MkdirAll(dir+"/lib", 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(dir+"/lib/libpq.so.5", []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if err := artifact.CheckLibraries(p, root); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// What doesn't start is refused, saying which library and what to do
// instead: a library the servers don't have, one a lone binary carries
// beside it (only the binary ships), one a RUNPATH finds outside the
// bundle, and a musl loader.
func TestCheckLibrariesRefuses(t *testing.T) {
	for name, c := range map[string]struct {
		exe     []byte
		bundled bool
		says    []string
	}{
		"libpq": {exe: artifacttest.Exec(glibcLoader, []string{"libpq.so.5", "libc.so.6"}, ""),
			says: []string{"libpq.so.5", "sqlx", "bundled"}},
		"sqlite and mysql": {exe: artifacttest.Exec(glibcLoader, []string{"libsqlite3.so.0", "libmysqlclient.so.21", "libc.so.6"}, ""),
			says: []string{"libsqlite3.so.0, libmysqlclient.so.21", "rusqlite", "modernc.org/sqlite", "mysql"}},
		"openssl 1.1": {exe: artifacttest.Exec(glibcLoader, []string{"libssl.so.1.1", "libc.so.6"}, ""),
			says: []string{"libssl.so.1.1", "OpenSSL 3"}},
		"a lone binary's lib beside it": {exe: artifacttest.Exec(glibcLoader, []string{"libpq.so.5", "libc.so.6"}, "$ORIGIN/lib"),
			says: []string{"libpq.so.5"}},
		"a runpath out of the bundle": {exe: artifacttest.Exec(glibcLoader, []string{"libpq.so.5", "libc.so.6"}, "$ORIGIN/../lib"), bundled: true,
			says: []string{"libpq.so.5"}},
		"musl": {exe: artifacttest.Exec("/lib/ld-musl-x86_64.so.1", []string{"libc.musl-x86_64.so.1"}, ""),
			says: []string{"musl", "/lib/ld-musl-x86_64.so.1", "glibc"}},
	} {
		dir := t.TempDir()
		root := ""
		if c.bundled {
			if err := os.MkdirAll(dir+"/app", 0o755); err != nil {
				t.Fatal(err)
			}
			root = dir + "/app"
		}
		for _, d := range []string{dir + "/lib", dir + "/app/lib"} {
			if err := os.MkdirAll(d, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(d+"/libpq.so.5", []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		at := dir
		if c.bundled {
			at = root
			if err := os.RemoveAll(root + "/lib"); err != nil { // only out of the bundle
				t.Fatal(err)
			}
		}
		err := artifact.CheckLibraries(writeExec(t, at, c.exe), root)
		var le *artifact.LibraryError
		if !errors.As(err, &le) {
			t.Errorf("%s: %v", name, err)
			continue
		}
		for _, s := range c.says {
			if !strings.Contains(err.Error(), s) {
				t.Errorf("%s: %q lacks %q", name, err, s)
			}
		}
	}
}
