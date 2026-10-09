package source

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

func write(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t",
		"GIT_COMMITTER_EMAIL=t@example.com", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func repo(t *testing.T, files map[string]string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	dir := t.TempDir()
	write(t, dir, files)
	git(t, dir, "init", "-q", "-b", "main")
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-q", "-m", "first")
	return dir
}

// unpack reads an archive back: its names and bodies (a link's is "-> target").
func unpack(t *testing.T, gz []byte) map[string]string {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	tr := tar.NewReader(zr)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		switch h.Typeflag {
		case tar.TypeSymlink:
			out[h.Name] = "-> " + h.Linkname
		case tar.TypeDir:
			out[h.Name] = "dir"
		default:
			b, _ := io.ReadAll(tr)
			out[h.Name] = string(b)
			if h.Mode&0o111 != 0 {
				out[h.Name] += " (exec)"
			}
		}
	}
}

var hex40 = regexp.MustCompile(`^[0-9a-f]{40}$`)

func TestAGitCheckoutUploadsWhatGitWouldCommit(t *testing.T) {
	dir := repo(t, map[string]string{
		".gitignore":   "node_modules/\n*.log\n",
		"go.mod":       "module m\n",
		"web/main.go":  "package main\n",
		"web/.env.log": "secret",
	})
	head := git(t, dir, "rev-parse", "HEAD")
	write(t, dir, map[string]string{
		"node_modules/x/index.js": "ignored",
		"debug.log":               "ignored",
		"new.go":                  "package main // untracked, not ignored: uploaded\n",
		".homeport/link.json":     "{}",
	})
	// from a folder inside it: the whole checkout goes, as a commit's tarball would
	tree, err := Collect(filepath.Join(dir, "web"))
	if err != nil {
		t.Fatal(err)
	}
	if !tree.Git || tree.Root != mustReal(t, dir) {
		t.Fatalf("tree %+v", tree)
	}
	want := []string{".gitignore", "go.mod", "new.go", "web/main.go"}
	if !slices.Equal(tree.Files, want) {
		t.Fatalf("files %v, want %v", tree.Files, want)
	}
	// an untracked file: not HEAD
	if tree.Clean || tree.Head != head {
		t.Fatalf("clean %v head %q", tree.Clean, tree.Head)
	}
	var buf bytes.Buffer
	sum, err := Pack(tree, &buf, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	got := unpack(t, buf.Bytes())
	if got["source/"] != "dir" || got["source/web/main.go"] != "package main\n" || len(got) != 5 {
		t.Fatalf("archive %v", got)
	}
	if sum.Files != 4 || sum.Bytes != int64(buf.Len()) || !hex40.MatchString(sum.Digest) {
		t.Fatalf("summary %+v (archive %d bytes)", sum, buf.Len())
	}
	// the same tree packs the same, byte for byte: its digest is its identity
	var again bytes.Buffer
	sum2, _ := Pack(tree, &again, 1<<20)
	if sum2.Digest != sum.Digest || !bytes.Equal(again.Bytes(), buf.Bytes()) {
		t.Fatal("packing twice differs")
	}
	// committed: clean, and HEAD is what's built
	git(t, dir, "add", "new.go")
	git(t, dir, "commit", "-q", "-m", "second")
	tree, _ = Collect(dir)
	if !tree.Clean || tree.Head != git(t, dir, "rev-parse", "HEAD") {
		t.Fatalf("after commit: %+v", tree)
	}
}

func TestAFolderWithoutGitHonoursItsGitignores(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, map[string]string{
		".gitignore":          "dist/\n*.secret\n",
		"index.ts":            "x",
		"dist/out.js":         "built",
		"api/.gitignore":      "/tmp\n",
		"api/tmp/a":           "tmp",
		"api/handler.ts":      "y",
		"keys.secret":         "no",
		".git/config":         "not a checkout, but never uploaded",
		".homeport/link.json": "{}",
	})
	tree, err := Collect(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{".gitignore", "api/.gitignore", "api/handler.ts", "index.ts"}
	if tree.Git || !slices.Equal(tree.Files, want) || tree.Clean || tree.Head != "" {
		t.Fatalf("tree %+v, want files %v", tree, want)
	}
}

func TestPackingKeepsModesAndLinksThatStayInside(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, map[string]string{"bin/run": "#!/bin/sh\n", "a.txt": "a"})
	os.Chmod(filepath.Join(dir, "bin/run"), 0o755)
	if err := os.Symlink("../a.txt", filepath.Join(dir, "bin/a")); err != nil {
		t.Fatal(err)
	}
	tree, err := Collect(dir)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if _, err := Pack(tree, &buf, 1<<20); err != nil {
		t.Fatal(err)
	}
	got := unpack(t, buf.Bytes())
	if got["source/bin/run"] != "#!/bin/sh\n (exec)" || got["source/bin/a"] != "-> ../a.txt" || got["source/a.txt"] != "a" {
		t.Fatalf("%v", got)
	}

	// a link out of the folder is refused, naming it: the build couldn't follow it
	if err := os.Symlink("/etc/passwd", filepath.Join(dir, "passwd")); err != nil {
		t.Fatal(err)
	}
	tree, _ = Collect(dir)
	_, err = Pack(tree, io.Discard, 1<<20)
	if err == nil || !strings.Contains(err.Error(), "passwd") {
		t.Fatalf("a link out: %v", err)
	}
}

func TestPackingStopsAtTheCap(t *testing.T) {
	dir := t.TempDir()
	// incompressible enough
	var big []byte
	for block := sha256.Sum256([]byte("seed")); len(big) < 64<<10; block = sha256.Sum256(block[:]) {
		big = append(big, block[:]...)
	}
	write(t, dir, map[string]string{"big.bin": string(big)})
	tree, _ := Collect(dir)
	if _, err := Pack(tree, io.Discard, 1<<10); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("over the cap: %v", err)
	}
}

func mustReal(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// A link through a link is judged as the filesystem would follow it: s -> .
// and e -> s/s/../.. reads as inside, and leads out.
func TestALinkThroughALinkIsRefused(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, map[string]string{"a.txt": "a"})
	os.Symlink(".", filepath.Join(dir, "s"))
	os.Symlink("s/s/../..", filepath.Join(dir, "e"))
	tree, err := Collect(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Pack(tree, io.Discard, 1<<20); err == nil || !strings.Contains(err.Error(), "outside") {
		t.Fatalf("%v", err)
	}
}

// What looks like a secret stays home, even when git doesn't ignore it:
// variables belong in the environment, keys nowhere.
func TestSecretsAreLeftOut(t *testing.T) {
	dir := repo(t, map[string]string{"go.mod": "module m\n", ".env.example": "KEY=\n"})
	write(t, dir, map[string]string{
		".env": "KEY=s3cret", ".env.production": "x", "web/.env.local": "x", "deploy/key.pem": "x", "id_ed25519": "x", "keys/id_rsa": "x",
		"id_generator.go": "package main\n",
	})
	tree, err := Collect(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{".env.example", "go.mod", "id_generator.go"}
	if !slices.Equal(tree.Files, want) {
		t.Fatalf("files %v", tree.Files)
	}
	left := []string{".env", ".env.production", "deploy/key.pem", "id_ed25519", "keys/id_rsa", "web/.env.local"}
	if !slices.Equal(tree.Secrets, left) {
		t.Fatalf("left out %v", tree.Secrets)
	}
	// a folder that isn't a checkout: the same
	plain := t.TempDir()
	write(t, plain, map[string]string{"index.html": "x", ".env": "x"})
	if tree, _ := Collect(plain); len(tree.Files) != 1 || len(tree.Secrets) != 1 {
		t.Fatalf("%+v", tree)
	}
}
