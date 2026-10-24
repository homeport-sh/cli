// Package buildcache is an app's dependency cache as object storage keeps
// it (cloud's design/builds.md, "The dependency cache in object storage"):
// the cache's folder as a zstd-compressed tar, behind a header that names
// the lockfiles it was made from. It is the one definition the builder
// (homeportd: Lock, Pack, Unpack) and the control plane (ReadLock, the
// limits) share, as envfile and buildplan are.
//
// The builder packs and unpacks it as root, outside the build's sandbox,
// so both treat it as the customer's: Pack never follows a link out of the
// folder and leaves credentials a build may have written out; Unpack writes
// inside its folder alone (os.Root), drops owners and special modes, and
// refuses an archive over its limits. A cache it can't read is no cache:
// the caller empties the folder and builds without it.
package buildcache

import (
	"archive/tar"
	"bufio"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/klauspost/compress/zstd"
)

// ErrTooLarge: past a limit (Pack's size, Unpack's Limits).
var ErrTooLarge = errors.New("buildcache: over the limit")

// The header is a zstd skippable frame - any zstd reader passes over it, so
// the object is a plain .tar.zst - whose payload is
// "homeport-cache 1 <lock>\n": the control plane reads the lock from an
// object's first HeaderLen bytes (ReadLock) without decompressing
// anything.
const (
	headerMagic   = 0x184D2A5C // zstd's skippable frames are 0x184D2A50-0x184D2A5F
	headerPrefix  = "homeport-cache 1 "
	headerPayload = len(headerPrefix) + 64 + 1
	// HeaderLen is how many of a cache's first bytes hold its header.
	HeaderLen = 8 + headerPayload
)

const (
	// MaxSaved is the most one cache may be, packed: a builder saves none
	// over it, the control plane keeps none over it.
	MaxSaved = 1 << 30
	// MaxRestore is the most caches a build job offers (the app's newest):
	// a builder refuses a job with more.
	MaxRestore = 4
)

var lockRe = regexp.MustCompile(`^[0-9a-f]{64}$`)

// ValidLock is a lock as Lock makes it.
func ValidLock(s string) bool { return lockRe.MatchString(s) }

func writeHeader(w io.Writer, lock string) error {
	var b [8]byte
	binary.LittleEndian.PutUint32(b[0:4], headerMagic)
	binary.LittleEndian.PutUint32(b[4:8], uint32(headerPayload))
	_, err := io.WriteString(w, string(b[:])+headerPrefix+lock+"\n")
	return err
}

func readHeader(r io.Reader) (string, error) {
	var b [HeaderLen]byte
	if _, err := io.ReadFull(r, b[:]); err != nil {
		return "", fmt.Errorf("buildcache: no header: %w", err)
	}
	if binary.LittleEndian.Uint32(b[0:4]) != headerMagic || binary.LittleEndian.Uint32(b[4:8]) != uint32(headerPayload) {
		return "", errors.New("buildcache: not a homeport cache")
	}
	lock, ok := parseHeader(b[:])
	if !ok {
		return "", errors.New("buildcache: a header this builder can't read")
	}
	return lock, nil
}

func parseHeader(b []byte) (string, bool) {
	if len(b) < HeaderLen || binary.LittleEndian.Uint32(b[0:4]) != headerMagic || binary.LittleEndian.Uint32(b[4:8]) != uint32(headerPayload) {
		return "", false
	}
	p := string(b[8:HeaderLen])
	lock, ok := strings.CutPrefix(strings.TrimSuffix(p, "\n"), headerPrefix)
	return lock, ok && strings.HasSuffix(p, "\n") && ValidLock(lock)
}

// ReadLock is the lock a cache's first HeaderLen bytes name; false: not a
// cache this version reads.
func ReadLock(head []byte) (string, bool) { return parseHeader(head) }

// LockFiles are the files a cache's lock is made from, in the checkout's
// root and in the app's folder: every package manager's lockfile the
// builder's toolchains run (homeport build-plan's: bun, npm, pnpm, yarn,
// Composer, Go modules), and those a custom build.image may.
var LockFiles = []string{"bun.lock", "bun.lockb", "package-lock.json", "npm-shrinkwrap.json", "pnpm-lock.yaml", "yarn.lock",
	"composer.lock", "go.sum", "Cargo.lock", "uv.lock", "poetry.lock", "Pipfile.lock", "requirements.txt", "Gemfile.lock"}

// maxLockfile is the most of a lockfile Lock reads.
const maxLockfile = 64 << 20

// Lock is what a cache is keyed by: the lockfiles in checkout's root and in
// appDir (relative to it; "" the root), with parts (the toolchain image,
// the architecture: a native module built for one is no use to another).
// Only regular files inside the checkout are read - never a link - so the
// builder, as root, reads nothing of the box's.
func Lock(checkout, appDir string, parts ...string) string {
	h := sha256.New()
	io.WriteString(h, "homeport-cache-lock 1\n")
	for _, p := range parts {
		fmt.Fprintf(h, "part %q\n", p)
	}
	root, err := os.OpenRoot(checkout)
	if err == nil {
		defer root.Close()
		dirs := []string{"."}
		if appDir != "" && filepath.IsLocal(appDir) {
			if c := path.Clean(filepath.ToSlash(appDir)); c != "." {
				dirs = append(dirs, c)
			}
		}
		for _, d := range dirs {
			for _, f := range LockFiles {
				name := path.Join(d, f)
				fi, err := root.Lstat(name)
				if err != nil || !fi.Mode().IsRegular() {
					continue
				}
				file, err := root.Open(name)
				if err != nil {
					continue
				}
				fmt.Fprintf(h, "file %q\n", name)
				n, _ := io.Copy(h, io.LimitReader(file, maxLockfile))
				file.Close()
				fmt.Fprintf(h, "\nsize %d\n", n)
			}
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

// secretNames are files a build may leave credentials in (a private
// registry's token, from the env vault): never packed, wherever they are.
var secretNames = []string{".npmrc", ".yarnrc", ".yarnrc.yml", ".bunfig.toml", ".netrc", ".git-credentials", ".pypirc",
	"auth.json", "credentials", "credentials.toml"}

// secretDirs are folders that hold nothing but credentials.
var secretDirs = []string{".ssh", ".docker", ".gnupg", ".aws", ".kube", ".config/gh"}

func secret(rel string, dir bool) bool {
	if dir {
		return slices.ContainsFunc(secretDirs, func(d string) bool { return rel == d || strings.HasSuffix(rel, "/"+d) })
	}
	return slices.Contains(secretNames, path.Base(rel))
}

// counter counts what's written, and stops past max.
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

// Pack writes dir as a cache object keyed by lock: the header, then the
// folder as a zstd-compressed tar - folders, regular files and links,
// owners left out, credentials (secretNames, secretDirs) left out. Past max
// bytes written it stops, ErrTooLarge. It says how many bytes it wrote.
func Pack(w io.Writer, dir, lock string, max int64) (int64, error) {
	if !ValidLock(lock) {
		return 0, fmt.Errorf("buildcache: a bad lock %q", lock)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return 0, err
	}
	defer root.Close()
	c := &counter{w: w, max: max}
	if err := writeHeader(c, lock); err != nil {
		return c.n, err
	}
	z, err := zstd.NewWriter(c, zstd.WithEncoderLevel(zstd.SpeedDefault), zstd.WithWindowSize(8<<20))
	if err != nil {
		return c.n, err
	}
	tw := tar.NewWriter(z)
	fsys := root.FS()
	walkErr := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == "." {
			return nil
		}
		if secret(p, d.IsDir()) {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		link := ""
		switch {
		case fi.Mode()&fs.ModeSymlink != 0:
			if link, err = fs.ReadLink(fsys, p); err != nil {
				return err
			}
		case fi.IsDir(), fi.Mode().IsRegular():
		default:
			return nil // a socket, a device: nothing a cache needs
		}
		h, err := tar.FileInfoHeader(fi, link)
		if err != nil {
			return err
		}
		h.Name, h.Mode = p, int64(fi.Mode().Perm())
		h.Uid, h.Gid, h.Uname, h.Gname = 0, 0, "", ""
		h.Format = tar.FormatPAX
		h.AccessTime, h.ChangeTime = h.ModTime, h.ModTime
		h.PAXRecords = nil
		if fi.IsDir() {
			h.Name += "/"
		}
		if err := tw.WriteHeader(h); err != nil {
			return err
		}
		if !fi.Mode().IsRegular() {
			return nil
		}
		f, err := fsys.Open(p)
		if err != nil {
			return err
		}
		defer f.Close()
		if _, err := io.CopyN(tw, f, h.Size); err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
		return nil
	})
	if walkErr != nil {
		z.Close()
		if errors.Is(walkErr, ErrTooLarge) {
			return c.n, ErrTooLarge
		}
		return c.n, walkErr
	}
	if err := tw.Close(); err != nil {
		z.Close()
		return c.n, err
	}
	if err := z.Close(); err != nil {
		if errors.Is(err, ErrTooLarge) {
			return c.n, ErrTooLarge
		}
		return c.n, err
	}
	return c.n, nil
}

// Limits are the most Unpack takes: bytes of files, and entries.
type Limits struct {
	Bytes   int64
	Entries int
}

// DefaultLimits are a cache's: as large as a cache volume (BUILD_CACHE_SIZE's
// 10 GiB), and far more entries than any package manager's cache holds.
var DefaultLimits = Limits{Bytes: 10 << 30, Entries: 4 << 20}

// Unpack reads a cache object into dir, an empty folder, and says the lock
// it was made from. Everything lands inside dir: an entry that names a
// path outside it, directly or through a link the archive made, is an
// error. Folders, regular files and links alone; permissions alone (no
// owners, no setuid). On an error dir holds part of the cache: the caller
// empties it.
func Unpack(r io.Reader, dir string, lim Limits) (string, error) {
	br := bufio.NewReaderSize(r, 1<<20)
	lock, err := readHeader(br)
	if err != nil {
		return "", err
	}
	if _, err := br.Peek(1); err != nil {
		return "", errors.New("buildcache: nothing after the header")
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return "", err
	}
	defer root.Close()
	z, err := zstd.NewReader(br, zstd.WithDecoderMaxWindow(64<<20), zstd.WithDecoderConcurrency(2))
	if err != nil {
		return "", err
	}
	defer z.Close()
	tr := tar.NewReader(z)
	left, entries := lim.Bytes, 0
	type dirMode struct {
		name string
		mode fs.FileMode
	}
	var dirs []dirMode
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", fmt.Errorf("buildcache: %w", err)
		}
		entries++
		if entries > lim.Entries {
			return "", ErrTooLarge
		}
		name := strings.TrimSuffix(h.Name, "/")
		if !filepath.IsLocal(name) || strings.Contains(name, "\\") {
			return "", fmt.Errorf("buildcache: %q is outside the cache", h.Name)
		}
		name = path.Clean(name)
		perm := fs.FileMode(h.Mode).Perm()
		if parent := path.Dir(name); parent != "." {
			if err := root.MkdirAll(parent, 0o755); err != nil {
				return "", fmt.Errorf("buildcache: %w", err)
			}
		}
		switch h.Typeflag {
		case tar.TypeDir:
			if err := root.MkdirAll(name, 0o755); err != nil {
				return "", fmt.Errorf("buildcache: %w", err)
			}
			dirs = append(dirs, dirMode{name, perm})
		case tar.TypeReg:
			if h.Size < 0 || h.Size > left {
				return "", ErrTooLarge
			}
			left -= h.Size
			if err := root.Remove(name); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return "", fmt.Errorf("buildcache: %w", err)
			}
			f, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
			if err != nil {
				return "", fmt.Errorf("buildcache: %w", err)
			}
			_, err = io.CopyN(f, tr, h.Size)
			if cerr := f.Close(); err == nil {
				err = cerr
			}
			if err != nil {
				return "", fmt.Errorf("buildcache: %s: %w", name, err)
			}
			if err := root.Chmod(name, perm); err != nil {
				return "", fmt.Errorf("buildcache: %w", err)
			}
			root.Chtimes(name, h.ModTime, h.ModTime)
		case tar.TypeSymlink:
			if err := root.Remove(name); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return "", fmt.Errorf("buildcache: %w", err)
			}
			if err := root.Symlink(h.Linkname, name); err != nil {
				return "", fmt.Errorf("buildcache: %w", err)
			}
		default:
			// hard links, devices, fifos: Pack makes none, and a cache
			// needs none
		}
	}
	// the whole frame, its checksum too: a cache cut short is no cache
	if _, err := io.Copy(io.Discard, z); err != nil {
		return "", fmt.Errorf("buildcache: %w", err)
	}
	// read-only folders (Go's module cache) last, deepest first
	slices.SortFunc(dirs, func(a, b dirMode) int { return len(b.name) - len(a.name) })
	for _, d := range dirs {
		if err := root.Chmod(d.name, d.mode); err != nil {
			return "", fmt.Errorf("buildcache: %w", err)
		}
	}
	return lock, nil
}
