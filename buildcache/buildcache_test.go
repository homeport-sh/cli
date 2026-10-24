package buildcache

import (
	"archive/tar"
	"bytes"
	"errors"
	"io"
	"io/fs"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/klauspost/compress/zstd"
)

const lockA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func write(t *testing.T, root, rel, data string, mode os.FileMode) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(data), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil {
		t.Fatal(err)
	}
}

// a cache as a build leaves it: package managers' folders, an executable,
// a link inside it, an empty folder, a read-only one (Go's module cache)
func sampleCache(t *testing.T) string {
	d := t.TempDir()
	write(t, d, "bun/react@19.0.0/index.js", "module.exports = 1\n", 0o644)
	write(t, d, "npm/_cacache/index-v5/aa/bb", strings.Repeat("x", 100_000), 0o644)
	write(t, d, "home/.cache/composer/files/laravel/framework/abc.zip", "zip\n", 0o644)
	write(t, d, "cargo/bin/tool", "#!/bin/sh\n", 0o755)
	write(t, d, "go/mod/example.com/m@v1.0.0/go.mod", "module m\n", 0o444)
	os.Chmod(filepath.Join(d, "go/mod/example.com/m@v1.0.0"), 0o555)
	os.MkdirAll(filepath.Join(d, "home/empty"), 0o755)
	os.Symlink("../bun/react@19.0.0/index.js", filepath.Join(d, "home/link"))
	t.Cleanup(func() { os.Chmod(filepath.Join(d, "go/mod/example.com/m@v1.0.0"), 0o755) })
	return d
}

// tree is a folder's entries: each path, its kind and mode, a file's
// content, a link's target.
func tree(t *testing.T, d string) map[string]string {
	t.Helper()
	out := map[string]string{}
	filepath.WalkDir(d, func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			t.Fatal(err)
		}
		rel, _ := filepath.Rel(d, p)
		if rel == "." {
			return nil
		}
		fi, _ := os.Lstat(p)
		v := fi.Mode().String()
		switch {
		case fi.Mode()&fs.ModeSymlink != 0:
			l, _ := os.Readlink(p)
			v += " -> " + l
		case fi.Mode().IsRegular():
			b, _ := os.ReadFile(p)
			v += " " + string(b)
		}
		out[rel] = v
		return nil
	})
	return out
}

func restoreDirs(t *testing.T, d string) {
	t.Cleanup(func() {
		filepath.WalkDir(d, func(p string, e fs.DirEntry, err error) error {
			if err == nil && e.IsDir() {
				os.Chmod(p, 0o755)
			}
			return nil
		})
	})
}

func TestPackUnpackRoundTrip(t *testing.T) {
	src := sampleCache(t)
	var buf bytes.Buffer
	n, err := Pack(&buf, src, lockA, 1<<30)
	if err != nil {
		t.Fatal(err)
	}
	if n != int64(buf.Len()) {
		t.Errorf("Pack said %d bytes, wrote %d", n, buf.Len())
	}
	// compressed: the 100 kB of x is a few bytes
	if buf.Len() > 20_000 {
		t.Errorf("packed %d bytes: not compressed", buf.Len())
	}
	dst := t.TempDir()
	restoreDirs(t, dst)
	lock, err := Unpack(bytes.NewReader(buf.Bytes()), dst, DefaultLimits)
	if err != nil {
		t.Fatal(err)
	}
	if lock != lockA {
		t.Errorf("lock %q, want %q", lock, lockA)
	}
	want, got := tree(t, src), tree(t, dst)
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: got %q, want %q", k, got[k], v)
		}
	}
	for k := range got {
		if _, ok := want[k]; !ok {
			t.Errorf("%s: unpacked, never packed", k)
		}
	}
}

// what a standard zstd reader makes of it: the header is a skippable
// frame, so the object is a plain .tar.zst to any tool
func TestPackedIsAPlainTarZst(t *testing.T) {
	src := sampleCache(t)
	var buf bytes.Buffer
	if _, err := Pack(&buf, src, lockA, 1<<30); err != nil {
		t.Fatal(err)
	}
	z, err := zstd.NewReader(&buf)
	if err != nil {
		t.Fatal(err)
	}
	defer z.Close()
	tr := tar.NewReader(z)
	names := 0
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if h.Uid != 0 || h.Gid != 0 || h.Uname != "" || h.Gname != "" {
			t.Errorf("%s: owner kept (%d:%d %s:%s)", h.Name, h.Uid, h.Gid, h.Uname, h.Gname)
		}
		names++
	}
	if names == 0 {
		t.Fatal("no entries")
	}
}

// A build's credentials (a registry token it wrote into ~/.npmrc, Composer's
// auth.json, ...) never leave the builder in its cache.
func TestPackLeavesCredentialsOut(t *testing.T) {
	src := t.TempDir()
	for _, f := range []string{"home/.npmrc", "home/.yarnrc.yml", "home/.yarnrc", "home/.netrc", "home/.git-credentials",
		"home/.config/composer/auth.json", "home/.composer/auth.json", "home/.cache/composer/auth.json", "home/.docker/config.json",
		"cargo/credentials.toml", "cargo/credentials", "home/.pypirc", "home/.config/gh/hosts.yml", "home/.bunfig.toml", "home/.ssh/id_ed25519"} {
		write(t, src, f, "secret\n", 0o600)
	}
	write(t, src, "npm/kept", "kept\n", 0o644)
	var buf bytes.Buffer
	if _, err := Pack(&buf, src, lockA, 1<<30); err != nil {
		t.Fatal(err)
	}
	dst := t.TempDir()
	if _, err := Unpack(&buf, dst, DefaultLimits); err != nil {
		t.Fatal(err)
	}
	got := tree(t, dst)
	for k, v := range got {
		if strings.Contains(v, "secret") {
			t.Errorf("%s packed", k)
		}
	}
	if got["npm/kept"] == "" {
		t.Errorf("npm/kept left out: %v", got)
	}
}

func TestPackOverTheCap(t *testing.T) {
	src := t.TempDir()
	// incompressible: random-ish bytes
	b := make([]byte, 1<<20)
	r := rand.NewChaCha8([32]byte{1})
	r.Read(b)
	write(t, src, "big", string(b), 0o644)
	_, err := Pack(io.Discard, src, lockA, 64<<10)
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("err %v, want ErrTooLarge", err)
	}
}

func TestPackRefusesABadLock(t *testing.T) {
	if _, err := Pack(io.Discard, t.TempDir(), "not a lock", 1<<30); err == nil {
		t.Fatal("packed with a bad lock")
	}
}

// archive is a cache object made by hand: the header, then entries.
func archive(t *testing.T, header bool, entries ...tar.Header) []byte {
	t.Helper()
	var buf bytes.Buffer
	if header {
		writeHeader(&buf, lockA)
	}
	z, _ := zstd.NewWriter(&buf)
	tw := tar.NewWriter(z)
	for _, h := range entries {
		h := h
		body := ""
		if h.Typeflag == tar.TypeReg {
			body = "data\n"
			h.Size = int64(len(body))
		}
		if h.Mode == 0 {
			h.Mode = 0o644
		}
		if err := tw.WriteHeader(&h); err != nil {
			t.Fatal(err)
		}
		io.WriteString(tw, body)
	}
	tw.Close()
	z.Close()
	return buf.Bytes()
}

// A cache is what a customer's build left: whatever it holds, unpacking
// it writes inside the cache's folder and nowhere else.
func TestUnpackStaysInside(t *testing.T) {
	outside := t.TempDir()
	cases := map[string][]tar.Header{
		"dotdot":   {{Name: "../escaped", Typeflag: tar.TypeReg}},
		"absolute": {{Name: outside + "/escaped", Typeflag: tar.TypeReg}},
		"through a link": {
			{Name: "l", Typeflag: tar.TypeSymlink, Linkname: outside},
			{Name: "l/escaped", Typeflag: tar.TypeReg},
		},
		"through a link to ..": {
			{Name: "l", Typeflag: tar.TypeSymlink, Linkname: ".."},
			{Name: "l/escaped", Typeflag: tar.TypeReg},
		},
		"a hard link out": {{Name: "h", Typeflag: tar.TypeLink, Linkname: "../../etc/passwd"}},
	}
	for name, entries := range cases {
		dst := t.TempDir()
		_, err := Unpack(bytes.NewReader(archive(t, true, entries...)), dst, DefaultLimits)
		if name != "a hard link out" && err == nil {
			t.Errorf("%s: unpacked", name)
		}
		if _, err := os.Lstat(filepath.Join(outside, "escaped")); err == nil {
			t.Fatalf("%s: wrote outside the cache", name)
		}
		if _, err := os.Lstat(filepath.Join(dst, "h")); err == nil {
			t.Errorf("%s: a hard link made", name)
		}
	}
}

// setuid and the like never come back: a cache's modes are its
// permissions alone.
func TestUnpackDropsSpecialModes(t *testing.T) {
	dst := t.TempDir()
	data := archive(t, true, tar.Header{Name: "s", Typeflag: tar.TypeReg, Mode: 0o4755},
		tar.Header{Name: "fifo", Typeflag: tar.TypeFifo}, tar.Header{Name: "dev", Typeflag: tar.TypeChar})
	if _, err := Unpack(bytes.NewReader(data), dst, DefaultLimits); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(dst, "s"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&(fs.ModeSetuid|fs.ModeSetgid|fs.ModeSticky) != 0 || fi.Mode().Perm() != 0o755 {
		t.Errorf("mode %v", fi.Mode())
	}
	for _, f := range []string{"fifo", "dev"} {
		if _, err := os.Lstat(filepath.Join(dst, f)); err == nil {
			t.Errorf("%s made", f)
		}
	}
}

func TestUnpackRefusesWhatIsntACache(t *testing.T) {
	good := archive(t, true, tar.Header{Name: "a", Typeflag: tar.TypeReg})
	cases := map[string][]byte{
		"empty":           nil,
		"no header":       archive(t, false, tar.Header{Name: "a", Typeflag: tar.TypeReg}),
		"truncated":       good[:len(good)-8],
		"garbage":         []byte("<html>AccessDenied</html>"),
		"header alone":    good[:HeaderLen],
		"another version": append([]byte(nil), bytes.Replace(good, []byte("homeport-cache 1 "), []byte("homeport-cache 9 "), 1)...),
	}
	for name, data := range cases {
		if _, err := Unpack(bytes.NewReader(data), t.TempDir(), DefaultLimits); err == nil {
			t.Errorf("%s: unpacked", name)
		}
	}
	if _, err := Unpack(iotest.HalfReader(bytes.NewReader(good)), t.TempDir(), DefaultLimits); err != nil {
		t.Errorf("a slow stream: %v", err)
	}
}

func TestUnpackLimits(t *testing.T) {
	data := archive(t, true, tar.Header{Name: "a", Typeflag: tar.TypeReg}, tar.Header{Name: "b", Typeflag: tar.TypeReg})
	if _, err := Unpack(bytes.NewReader(data), t.TempDir(), Limits{Bytes: 6, Entries: 100}); !errors.Is(err, ErrTooLarge) {
		t.Errorf("bytes: %v", err)
	}
	if _, err := Unpack(bytes.NewReader(data), t.TempDir(), Limits{Bytes: 1 << 20, Entries: 1}); !errors.Is(err, ErrTooLarge) {
		t.Errorf("entries: %v", err)
	}
}

func TestLock(t *testing.T) {
	src := t.TempDir()
	write(t, src, "package.json", `{"name":"x"}`, 0o644)
	write(t, src, "bun.lock", "lock 1\n", 0o644)
	base := Lock(src, "", "oven/bun:1.3", "amd64")
	if !ValidLock(base) {
		t.Fatalf("lock %q", base)
	}
	if again := Lock(src, "", "oven/bun:1.3", "amd64"); again != base {
		t.Error("not stable")
	}
	// package.json isn't a lockfile: a version bump in it changes nothing
	write(t, src, "package.json", `{"name":"x","version":"2"}`, 0o644)
	if Lock(src, "", "oven/bun:1.3", "amd64") != base {
		t.Error("package.json changed the lock")
	}
	for name, l := range map[string]string{
		"another image": Lock(src, "", "oven/bun:1.4", "amd64"),
		"another arch":  Lock(src, "", "oven/bun:1.3", "arm64"),
	} {
		if l == base {
			t.Errorf("%s: the same lock", name)
		}
	}
	write(t, src, "bun.lock", "lock 2\n", 0o644)
	if Lock(src, "", "oven/bun:1.3", "amd64") == base {
		t.Error("a changed lockfile kept the lock")
	}

	// an app in a folder of a workspace: its own lockfile and the root's
	ws := t.TempDir()
	write(t, ws, "pnpm-lock.yaml", "root\n", 0o644)
	a := Lock(ws, "apps/web", "node:22")
	write(t, ws, "apps/web/pnpm-lock.yaml", "web\n", 0o644)
	if Lock(ws, "apps/web", "node:22") == a {
		t.Error("the app folder's lockfile not read")
	}
	write(t, ws, "pnpm-lock.yaml", "root 2\n", 0o644)
	b := Lock(ws, "apps/web", "node:22")
	write(t, ws, "pnpm-lock.yaml", "root 3\n", 0o644)
	if Lock(ws, "apps/web", "node:22") == b {
		t.Error("the workspace root's lockfile not read")
	}

	// every package manager the builder runs
	for _, f := range []string{"bun.lockb", "package-lock.json", "npm-shrinkwrap.json", "yarn.lock", "composer.lock", "go.sum", "Cargo.lock", "uv.lock"} {
		d := t.TempDir()
		empty := Lock(d, "", "img")
		write(t, d, f, "x\n", 0o644)
		if Lock(d, "", "img") == empty {
			t.Errorf("%s not read", f)
		}
	}
}

// The checkout is the customer's: a lockfile that's a link out of it is
// never read (the builder reads it as root).
func TestLockStaysInTheCheckout(t *testing.T) {
	outside := t.TempDir()
	write(t, outside, "secret", "s1\n", 0o644)
	src := t.TempDir()
	if err := os.Symlink(filepath.Join(outside, "secret"), filepath.Join(src, "bun.lock")); err != nil {
		t.Fatal(err)
	}
	a := Lock(src, "", "img")
	write(t, outside, "secret", "s2\n", 0o644)
	if Lock(src, "", "img") != a {
		t.Error("read through a link out of the checkout")
	}
	// and an app folder of .. reads nothing above it
	if Lock(src, "../x", "img") != Lock(t.TempDir(), "", "img") {
		t.Error("an app folder outside the checkout read")
	}
}
