// Package source packs a working tree for `homeport deploy`: what git would
// commit - tracked files and untracked ones that aren't ignored, as they
// are on disk now - as a gzipped tar with one top folder, the shape of a
// commit's tarball, which a hosted build unpacks in its sandbox. Nothing in
// the tree is run here.
package source

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strings"
)

// Top is the archive's one top folder; a build strips it.
const Top = "source"

// MaxFiles is the most files an upload may hold (the platform's limit).
const MaxFiles = 200_000

// ErrTooLarge: the packed tree is past the cap it was packed under.
var ErrTooLarge = errors.New("the working tree is too large to upload")

// Tree is a working tree's files to upload.
type Tree struct {
	Root  string   // the checkout's top (or the folder, without git)
	Files []string // slash-separated, from Root, sorted
	Git   bool     // Root is a git checkout: git said what's ignored
	Head  string   // HEAD's commit ("" none)
	Clean bool     // nothing differs from HEAD, untracked files included
}

// never is what's never uploaded, wherever it is: git's own folder, and
// the link to homeport.
func never(rel string) bool {
	for _, part := range strings.Split(rel, "/") {
		if part == ".git" || part == ".homeport" {
			return true
		}
	}
	return false
}

// Collect is the tree dir is in: its git checkout's, from the top (a
// commit's tarball is the whole repository; the app's folder in it is
// the app's setting), or dir's own when it isn't in one.
func Collect(dir string) (*Tree, error) {
	if top, err := gitOut(dir, "rev-parse", "--show-toplevel"); err == nil && top != "" {
		return collectGit(strings.TrimSpace(top))
	}
	return collectDir(dir)
}

func gitOut(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Stderr = io.Discard
	out, err := cmd.Output()
	return string(out), err
}

func collectGit(root string) (*Tree, error) {
	if r, err := filepath.EvalSymlinks(root); err == nil {
		root = r
	}
	out, err := gitOut(root, "ls-files", "-z", "--cached", "--others", "--exclude-standard")
	if err != nil {
		return nil, fmt.Errorf("listing the checkout's files: %w", err)
	}
	t := &Tree{Root: root, Git: true}
	seen := map[string]bool{}
	for _, f := range strings.Split(out, "\x00") {
		if f == "" || seen[f] || never(f) {
			continue
		}
		seen[f] = true
		info, err := os.Lstat(filepath.Join(root, filepath.FromSlash(f)))
		if err != nil || info.IsDir() {
			continue // deleted since its commit, or a submodule
		}
		t.Files = append(t.Files, f)
	}
	slices.Sort(t.Files)
	if head, err := gitOut(root, "rev-parse", "--verify", "-q", "HEAD"); err == nil {
		t.Head = strings.TrimSpace(head)
	}
	if status, err := gitOut(root, "status", "--porcelain", "-z", "--untracked-files=normal"); err == nil {
		t.Clean = t.Head != ""
		for _, entry := range strings.Split(status, "\x00") {
			// "XY path": what differs; the link to homeport never counts
			if len(entry) > 3 && !never(entry[3:]) {
				t.Clean = false
			}
		}
	}
	return t, nil
}

func collectDir(dir string) (*Tree, error) {
	root, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if r, err := filepath.EvalSymlinks(root); err == nil {
		root = r
	}
	t := &Tree{Root: root}
	ign := &ignorer{}
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if rel != "." && (never(rel) || ign.ignored(rel, true)) {
				return filepath.SkipDir
			}
			base := rel
			if base == "." {
				base = ""
			}
			if b, err := os.ReadFile(filepath.Join(p, ".gitignore")); err == nil {
				ign.add(base, string(b))
			}
			return nil
		}
		if never(rel) || ign.ignored(rel, false) {
			return nil
		}
		t.Files = append(t.Files, rel)
		return nil
	})
	if err != nil {
		return nil, err
	}
	slices.Sort(t.Files)
	return t, nil
}

// Summary is what was packed.
type Summary struct {
	Files  int
	Bytes  int64  // the archive's size
	Digest string // 40 hex: the archive's SHA-1, the same for the same tree
}

// counter counts what's written, refusing past max.
type counter struct {
	w   io.Writer
	n   int64
	max int64
}

func (c *counter) Write(p []byte) (int, error) {
	if c.n+int64(len(p)) > c.max {
		return 0, ErrTooLarge
	}
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}

// Pack writes t to w as a gzipped tar of Top/<file>: plain files (0755 if
// executable, else 0644, no owner, no times - the same tree packs to the
// same bytes) and links that stay inside the tree. It stops at max bytes
// packed (ErrTooLarge), and refuses a link out of the tree, naming it.
func Pack(t *Tree, w io.Writer, max int64) (Summary, error) {
	if len(t.Files) > MaxFiles {
		return Summary{}, fmt.Errorf("%w: more than %d files", ErrTooLarge, MaxFiles)
	}
	sum := sha1.New()
	c := &counter{w: io.MultiWriter(w, sum), max: max}
	gz, err := gzip.NewWriterLevel(c, gzip.BestCompression)
	if err != nil {
		return Summary{}, err
	}
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: Top + "/", Typeflag: tar.TypeDir, Mode: 0o755, Format: tar.FormatPAX}); err != nil {
		return Summary{}, err
	}
	files := 0
	for _, rel := range t.Files {
		p := filepath.Join(t.Root, filepath.FromSlash(rel))
		info, err := os.Lstat(p)
		if err != nil {
			return Summary{}, err
		}
		h := &tar.Header{Name: Top + "/" + rel, Format: tar.FormatPAX}
		switch {
		case info.Mode().IsRegular():
			h.Typeflag, h.Size, h.Mode = tar.TypeReg, info.Size(), 0o644
			if info.Mode()&0o111 != 0 {
				h.Mode = 0o755
			}
		case info.Mode()&fs.ModeSymlink != 0:
			to, err := os.Readlink(p)
			if err != nil {
				return Summary{}, err
			}
			to = filepath.ToSlash(to)
			if path.IsAbs(to) || strings.HasPrefix(path.Clean(path.Join(path.Dir(rel), to)), "..") {
				return Summary{}, fmt.Errorf("%s links outside the project (to %s): a build couldn't follow it; ignore it in .gitignore, or copy what it points to in", rel, to)
			}
			h.Typeflag, h.Linkname, h.Mode = tar.TypeSymlink, to, 0o777
		default:
			continue // a socket, a pipe: nothing a build reads
		}
		if err := tw.WriteHeader(h); err != nil {
			return Summary{}, packErr(err)
		}
		if h.Typeflag == tar.TypeReg {
			f, err := os.Open(p)
			if err != nil {
				return Summary{}, err
			}
			_, err = io.CopyN(tw, f, h.Size)
			f.Close()
			if err != nil {
				return Summary{}, packErr(fmt.Errorf("%s: %w", rel, err))
			}
		}
		files++
	}
	if err := tw.Close(); err != nil {
		return Summary{}, packErr(err)
	}
	if err := gz.Close(); err != nil {
		return Summary{}, packErr(err)
	}
	return Summary{Files: files, Bytes: c.n, Digest: hex.EncodeToString(sum.Sum(nil))}, nil
}

func packErr(err error) error {
	if errors.Is(err, ErrTooLarge) {
		return ErrTooLarge
	}
	return err
}
