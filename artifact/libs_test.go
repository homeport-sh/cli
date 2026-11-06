package artifact_test

import (
	"bytes"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/homeport-sh/cli/artifact"
)

// elfExec is a little x86-64 ELF executable: its loader (PT_INTERP; ""
// for none, a static one), the libraries it needs (DT_NEEDED) and its
// RUNPATH ("" none) - all debug/elf reads of it.
func elfExec(interp string, needed []string, runpath string) []byte {
	le := binary.LittleEndian
	// .dynstr, and the .dynamic that names its strings
	dynstr := []byte{0}
	str := func(s string) uint64 {
		off := len(dynstr)
		dynstr = append(append(dynstr, s...), 0)
		return uint64(off)
	}
	var dyn []byte
	tag := func(t, v uint64) { dyn = le.AppendUint64(le.AppendUint64(dyn, t), v) }
	for _, n := range needed {
		tag(1, str(n)) // DT_NEEDED
	}
	if runpath != "" {
		tag(29, str(runpath)) // DT_RUNPATH
	}
	tag(0, 0)
	shstr := []byte("\x00.dynstr\x00.dynamic\x00.shstrtab\x00")

	const ehdr, phdr, shdr = 64, 56, 64
	nph := 0
	if interp != "" {
		nph = 1
	}
	off := ehdr + nph*phdr
	interpOff := off
	off += len(interp) + 1
	dynstrOff := off
	off += len(dynstr)
	dynOff := off
	off += len(dyn)
	shstrOff := off
	off += len(shstr)
	shoff := off

	var b bytes.Buffer
	h := make([]byte, ehdr)
	copy(h, "\x7fELF\x02\x01\x01")
	le.PutUint16(h[16:], 2)    // ET_EXEC
	le.PutUint16(h[18:], 0x3e) // x86-64
	le.PutUint32(h[20:], 1)
	if nph > 0 {
		le.PutUint64(h[32:], ehdr)
	}
	le.PutUint64(h[40:], uint64(shoff))
	le.PutUint16(h[52:], ehdr)
	le.PutUint16(h[54:], phdr)
	le.PutUint16(h[56:], uint16(nph))
	le.PutUint16(h[58:], shdr)
	le.PutUint16(h[60:], 4) // null, .dynstr, .dynamic, .shstrtab
	le.PutUint16(h[62:], 3)
	b.Write(h)
	if nph > 0 {
		p := make([]byte, phdr)
		le.PutUint32(p[0:], 3) // PT_INTERP
		le.PutUint32(p[4:], 4)
		le.PutUint64(p[8:], uint64(interpOff))
		le.PutUint64(p[32:], uint64(len(interp)+1))
		le.PutUint64(p[40:], uint64(len(interp)+1))
		le.PutUint64(p[48:], 1)
		b.Write(p)
	}
	b.WriteString(interp + "\x00")
	b.Write(dynstr)
	b.Write(dyn)
	b.Write(shstr)
	sec := func(name, typ uint32, off, size, link, entsize uint64) {
		s := make([]byte, shdr)
		le.PutUint32(s[0:], name)
		le.PutUint32(s[4:], typ)
		le.PutUint64(s[24:], off)
		le.PutUint64(s[32:], size)
		le.PutUint32(s[40:], uint32(link))
		le.PutUint64(s[48:], 1)
		le.PutUint64(s[56:], entsize)
		b.Write(s)
	}
	sec(0, 0, 0, 0, 0, 0)
	sec(1, 3, uint64(dynstrOff), uint64(len(dynstr)), 0, 0) // .dynstr, SHT_STRTAB
	sec(9, 6, uint64(dynOff), uint64(len(dyn)), 1, 16)      // .dynamic, SHT_DYNAMIC
	sec(18, 3, uint64(shstrOff), uint64(len(shstr)), 0, 0)  // .shstrtab
	return b.Bytes()
}

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
		"rust, rustls":         {exe: elfExec(glibcLoader, []string{"libgcc_s.so.1", "libm.so.6", "libc.so.6"}, "")},
		"node":                 {exe: elfExec(glibcLoader, []string{"libdl.so.2", "libstdc++.so.6", "libm.so.6", "libgcc_s.so.1", "libpthread.so.0", "libc.so.6"}, "")},
		"native-tls":           {exe: elfExec(glibcLoader, []string{"libssl.so.3", "libcrypto.so.3", "libz.so.1", "libc.so.6"}, "")},
		"arm64's loader":       {exe: elfExec("/lib/ld-linux-aarch64.so.1", []string{"libc.so.6"}, "")},
		"static":               {exe: elfExec("", nil, "")},
		"not an ELF":           {exe: []byte("#!/bin/sh\nexec ./server\n")},
		"its own libpq":        {exe: elfExec(glibcLoader, []string{"libpq.so.5", "libc.so.6"}, "$ORIGIN/lib"), bundled: true},
		"its own, ${ORIGIN}/.": {exe: elfExec(glibcLoader, []string{"libpq.so.5", "libc.so.6"}, "/usr/local/lib:${ORIGIN}/lib/."), bundled: true},
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
		"libpq": {exe: elfExec(glibcLoader, []string{"libpq.so.5", "libc.so.6"}, ""),
			says: []string{"libpq.so.5", "sqlx", "bundled"}},
		"sqlite and mysql": {exe: elfExec(glibcLoader, []string{"libsqlite3.so.0", "libmysqlclient.so.21", "libc.so.6"}, ""),
			says: []string{"libsqlite3.so.0, libmysqlclient.so.21", "rusqlite", "modernc.org/sqlite", "mysql"}},
		"openssl 1.1": {exe: elfExec(glibcLoader, []string{"libssl.so.1.1", "libc.so.6"}, ""),
			says: []string{"libssl.so.1.1", "OpenSSL 3"}},
		"a lone binary's lib beside it": {exe: elfExec(glibcLoader, []string{"libpq.so.5", "libc.so.6"}, "$ORIGIN/lib"),
			says: []string{"libpq.so.5"}},
		"a runpath out of the bundle": {exe: elfExec(glibcLoader, []string{"libpq.so.5", "libc.so.6"}, "$ORIGIN/../lib"), bundled: true,
			says: []string{"libpq.so.5"}},
		"musl": {exe: elfExec("/lib/ld-musl-x86_64.so.1", []string{"libc.musl-x86_64.so.1"}, ""),
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
