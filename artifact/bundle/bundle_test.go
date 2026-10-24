package bundle

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
)

var lim = Limits{MaxFiles: 100, MaxBytes: 1 << 20}

type file struct {
	name string
	mode fs.FileMode
	body string
	dir  bool
}

func pack(t *testing.T, opt Options, files ...file) ([]byte, Summary) {
	t.Helper()
	var b bytes.Buffer
	w := NewWriter(&b, opt)
	for _, f := range files {
		var err error
		if f.dir {
			err = w.Dir(f.name, f.mode)
		} else {
			err = w.File(f.name, f.mode, int64(len(f.body)), strings.NewReader(f.body))
		}
		if err != nil {
			t.Fatalf("%s: %v", f.name, err)
		}
	}
	sum, err := w.Close()
	if err != nil {
		t.Fatal(err)
	}
	return b.Bytes(), sum
}

// verify is Verify as a control plane calls it: the manifest read from
// the artifact's end first, then the whole of it streamed.
func verify(art []byte) (Summary, error) {
	if len(art) < FooterSize {
		return Summary{}, errors.New("short")
	}
	off, n, err := ManifestRange(int64(len(art)), art[len(art)-FooterSize:])
	if err != nil {
		return Summary{}, err
	}
	return Verify(bytes.NewReader(art), int64(len(art)), art[off:off+n], lim, nil)
}

func sha(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

func manifestOf(t *testing.T, art []byte) (*Manifest, []byte) {
	t.Helper()
	n, err := ParseFooter(art[len(art)-FooterSize:])
	if err != nil {
		t.Fatal(err)
	}
	raw := art[int64(len(art))-FooterSize-n : len(art)-FooterSize]
	m, err := DecodeManifest(raw, lim)
	if err != nil {
		t.Fatal(err)
	}
	return m, raw
}

var site = []file{
	{name: "bin", mode: 0o755, body: "\x7fELF the binary"},
	{name: "public", dir: true, mode: 0o755},
	{name: "public/index.php", mode: 0o644, body: strings.Repeat("<?php echo 1; ", 50)},
	{name: "vendor", dir: true, mode: 0o755},
	{name: "vendor/a.php", mode: 0o644, body: strings.Repeat("a", 3000)},
	{name: "vendor/b.php", mode: 0o644, body: strings.Repeat("b", 3000)},
	{name: "vendor/empty", mode: 0o644, body: ""},
}

// What's written is read back whole, as it went in, and the summary says
// what the bytes are.
func TestARoundTrip(t *testing.T) {
	art, sum := pack(t, Options{ChunkBytes: 4096}, site...)
	if sum.Size != int64(len(art)) || sum.SHA256 != sha(art) {
		t.Fatalf("summary %+v, artifact %d bytes %s", sum, len(art), sha(art))
	}
	if sum.Files != 5 || sum.Bytes != int64(len(site[0].body)+len(site[2].body)+6000) {
		t.Fatalf("summary %+v", sum)
	}
	m, raw := manifestOf(t, art)
	if sum.ManifestSHA256 != sha(raw) {
		t.Fatalf("manifest sha %s, want %s", sum.ManifestSHA256, sha(raw))
	}
	if len(m.Chunks) < 2 {
		t.Fatalf("want several chunks at 4 KiB, got %d", len(m.Chunks))
	}
	got := map[string]string{}
	err := Read(bytes.NewReader(art), lim, func(e Entry, body io.Reader) error {
		b, err := io.ReadAll(body)
		if e.Dir {
			got[e.Path+"/"] = fmt.Sprint(e.Mode)
		} else {
			got[e.Path] = fmt.Sprint(e.Mode) + ":" + string(b)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range site {
		want := fmt.Sprint(uint32(f.mode)) + ":" + f.body
		k := f.name
		if f.dir {
			want, k = fmt.Sprint(uint32(f.mode)), k+"/"
		}
		if got[k] != want {
			t.Errorf("%s: got %q", k, got[k])
		}
	}
	for i, e := range m.Entries {
		if e.Path != strings.TrimSuffix(site[i].name, "/") || e.Dir != site[i].dir {
			t.Errorf("entry %d: %+v", i, e)
		}
		if !e.Dir && e.SHA256 != sha([]byte(site[i].body)) {
			t.Errorf("%s: hash %s", e.Path, e.SHA256)
		}
	}
}

// Each chunk is a zstd stream of its own, of whole tar entries: a host
// fetches just the ones it needs.
func TestEachChunkStandsAlone(t *testing.T) {
	art, _ := pack(t, Options{ChunkBytes: 4096}, site...)
	m, _ := manifestOf(t, art)
	seen := 0
	for i, c := range m.Chunks {
		r := bytes.NewReader(art[c.Off : c.Off+c.Len])
		err := ReadRange(r, m, i, i, lim, func(e Entry, body io.Reader) error {
			seen++
			_, err := io.Copy(io.Discard, body)
			return err
		})
		if err != nil {
			t.Fatalf("chunk %d: %v", i, err)
		}
	}
	if seen != len(m.Entries) {
		t.Fatalf("read %d entries chunk by chunk, manifest has %d", seen, len(m.Entries))
	}
	// adjacent chunks read as one range too
	last := len(m.Chunks) - 1
	r := bytes.NewReader(art[m.Chunks[0].Off : m.Chunks[last].Off+m.Chunks[last].Len])
	if err := ReadRange(r, m, 0, last, lim, func(Entry, io.Reader) error { return nil }); err != nil {
		t.Fatal(err)
	}
}

// Any zstd tool reads the whole thing as a tar (the manifest rides in a
// skippable frame).
func TestTheArtifactIsAPlainTarZst(t *testing.T) {
	art, _ := pack(t, Options{ChunkBytes: 4096}, site...)
	if !bytes.HasPrefix(art, []byte{0x28, 0xb5, 0x2f, 0xfd}) {
		t.Fatalf("not zstd: % x", art[:4])
	}
	d, err := zstd.NewReader(bytes.NewReader(art))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	tr := tar.NewReader(d)
	var names []string
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, h.Name)
	}
	if len(names) != len(site) || names[0] != "bin" || names[1] != "public/" {
		t.Fatalf("names %q", names)
	}
}

// Verify reads the whole artifact against its manifest: what a control
// plane checks before a host relies on it.
func TestVerifyAcceptsWhatTheWriterWrote(t *testing.T) {
	art, sum := pack(t, Options{ChunkBytes: 4096}, site...)
	got, err := verify(art)
	if err != nil {
		t.Fatal(err)
	}
	if got != sum {
		t.Fatalf("verify %+v, writer %+v", got, sum)
	}
}

func TestVerifyRefusesTampering(t *testing.T) {
	art, _ := pack(t, Options{ChunkBytes: 4096}, site...)
	m, _ := manifestOf(t, art)
	for name, mangle := range map[string]func([]byte) []byte{
		"junk before the manifest": func(a []byte) []byte {
			end := m.Chunks[len(m.Chunks)-1].Off + m.Chunks[len(m.Chunks)-1].Len
			return append(append(append([]byte{}, a[:end]...), 0, 0, 0, 0), a[end:]...)
		},
		"a byte flipped in a chunk": func(a []byte) []byte {
			b := append([]byte{}, a...)
			b[m.Chunks[1].Off+m.Chunks[1].Len/2] ^= 0xff
			return b
		},
		"no footer": func(a []byte) []byte { return a[:len(a)-3] },
	} {
		t.Run(name, func(t *testing.T) {
			b := mangle(art)
			if _, err := verify(b); err == nil {
				t.Fatal("accepted")
			}
		})
	}
}

// A manifest that lies about a file's bytes is caught as they're read.
func TestReadRangeChecksEachFileAgainstTheManifest(t *testing.T) {
	art, _ := pack(t, Options{}, site...)
	m, _ := manifestOf(t, art)
	for name, lie := range map[string]func(*Manifest){
		"hash": func(m *Manifest) { m.Entries[4].SHA256 = strings.Repeat("0", 64) },
		"size": func(m *Manifest) { m.Entries[4].Size++ },
		"path": func(m *Manifest) { m.Entries[4].Path = "vendor/c.php" },
		"mode": func(m *Manifest) { m.Entries[4].Mode = 0o755 },
		"kind": func(m *Manifest) { m.Entries[1].Dir = false },
	} {
		t.Run(name, func(t *testing.T) {
			cp := *m
			cp.Entries = append([]Entry{}, m.Entries...)
			lie(&cp)
			c := cp.Chunks[0]
			err := ReadRange(bytes.NewReader(art[c.Off:c.Off+c.Len]), &cp, 0, 0, lim, func(e Entry, body io.Reader) error {
				_, err := io.Copy(io.Discard, body)
				return err
			})
			if err == nil {
				t.Fatal("accepted")
			}
		})
	}
}

func tarOf(t *testing.T, gz bool, hdrs ...*tar.Header) []byte {
	t.Helper()
	var b bytes.Buffer
	var w io.WriteCloser
	if gz {
		w = gzip.NewWriter(&b)
	} else {
		z, _ := zstd.NewWriter(&b)
		w = z
	}
	tw := tar.NewWriter(w)
	for _, h := range hdrs {
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if h.Typeflag == tar.TypeReg {
			tw.Write(bytes.Repeat([]byte{'x'}, int(h.Size)))
		}
	}
	tw.Close()
	w.Close()
	return b.Bytes()
}

func reg(name string, size int64) *tar.Header {
	return &tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o644, Size: size}
}

// Read takes any tar - gzip (what builders made before) or zstd - and
// refuses whatever isn't a file or a folder at a plain relative name, and
// what's over the limits, counted as it inflates.
func TestReadRefusesWhatIsNotAFileOrAFolder(t *testing.T) {
	for name, c := range map[string]struct {
		hdr  *tar.Header
		want string
	}{
		"symlink":      {&tar.Header{Name: "l", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"}, "symlink"},
		"hard link":    {&tar.Header{Name: "h", Typeflag: tar.TypeLink, Linkname: "a"}, "hard link"},
		"device":       {&tar.Header{Name: "d", Typeflag: tar.TypeChar}, "isn't a file or a folder"},
		"fifo":         {&tar.Header{Name: "f", Typeflag: tar.TypeFifo}, "isn't a file or a folder"},
		"dotdot":       {reg("../x", 1), "path"},
		"inner dotdot": {reg("a/../../x", 1), "path"},
		"absolute":     {reg("/etc/x", 1), "path"},
		"setuid":       {&tar.Header{Name: "s", Typeflag: tar.TypeReg, Mode: 0o4755}, ""},
	} {
		for _, gz := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s gzip=%v", name, gz), func(t *testing.T) {
				art := tarOf(t, gz, reg("a", 1), c.hdr)
				var modes []uint32
				err := Read(bytes.NewReader(art), lim, func(e Entry, _ io.Reader) error {
					modes = append(modes, e.Mode)
					return nil
				})
				if c.want == "" {
					// setuid and the like aren't refused: they're dropped
					if err != nil || modes[1] != 0o755 {
						t.Fatalf("err %v modes %o", err, modes)
					}
					return
				}
				if err == nil || !strings.Contains(err.Error(), c.want) {
					t.Fatalf("err %v, want %q", err, c.want)
				}
			})
		}
	}
}

func TestReadEnforcesTheLimits(t *testing.T) {
	var many []*tar.Header
	for i := range lim.MaxFiles + 1 {
		many = append(many, reg(fmt.Sprintf("f%d", i), 0))
	}
	if err := Read(bytes.NewReader(tarOf(t, false, many...)), lim, nil); !errors.Is(err, ErrTooManyFiles) {
		t.Fatalf("files: %v", err)
	}
	// a few KB of zeros that would inflate past the cap
	bomb := tarOf(t, false, reg("a", lim.MaxBytes/2), reg("b", lim.MaxBytes/2+1))
	if len(bomb) > 64<<10 {
		t.Fatalf("bomb is %d bytes", len(bomb))
	}
	if err := Read(bytes.NewReader(bomb), lim, nil); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("bytes: %v", err)
	}
	if err := Read(strings.NewReader("not an archive at all"), lim, nil); !errors.Is(err, ErrNotArchive) {
		t.Fatalf("junk: %v", err)
	}
}

func TestDecodeManifestRefusesAnInconsistentOne(t *testing.T) {
	art, _ := pack(t, Options{ChunkBytes: 4096}, site...)
	m, _ := manifestOf(t, art)
	for name, c := range map[string]struct {
		mangle func(*Manifest)
		lim    Limits
	}{
		"a gap between chunks": {mangle: func(m *Manifest) { m.Chunks[1].Off++ }},
		"a chunk out of range": {mangle: func(m *Manifest) { m.Entries[0].Chunk = 99 }},
		"chunks out of order":  {mangle: func(m *Manifest) { m.Entries[0].Chunk = len(m.Chunks) - 1 }},
		"a path out":           {mangle: func(m *Manifest) { m.Entries[0].Path = "../bin" }},
		"a duplicate":          {mangle: func(m *Manifest) { m.Entries[5].Path = "vendor/a.php" }},
		"a short hash":         {mangle: func(m *Manifest) { m.Entries[0].SHA256 = "abc" }},
		"under a file":         {mangle: func(m *Manifest) { m.Entries[2].Path = "bin/index.php" }},
		"too many files":       {mangle: func(*Manifest) {}, lim: Limits{MaxFiles: 4, MaxBytes: 1 << 20}},
		"too many bytes":       {mangle: func(*Manifest) {}, lim: Limits{MaxFiles: 100, MaxBytes: 100}},
	} {
		t.Run(name, func(t *testing.T) {
			cp := *m
			cp.Entries = append([]Entry{}, m.Entries...)
			cp.Chunks = append([]Chunk{}, m.Chunks...)
			c.mangle(&cp)
			l := c.lim
			if l.MaxFiles == 0 {
				l = lim
			}
			raw, err := cp.Encode()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := DecodeManifest(raw, l); err == nil {
				t.Fatal("accepted")
			}
		})
	}
}

// PackDir packs a folder in name order, refusing a link rather than
// following it.
func TestPackDir(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "b/c"), 0o755)
	os.WriteFile(filepath.Join(dir, "bin"), []byte("\x7fELF"), 0o755)
	os.WriteFile(filepath.Join(dir, "b/c/x"), []byte("x"), 0o600)
	os.WriteFile(filepath.Join(dir, "a"), []byte("a"), 0o644)
	var b bytes.Buffer
	sum, err := PackDir(dir, &b, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if sum.Files != 3 {
		t.Fatalf("%+v", sum)
	}
	m, _ := manifestOf(t, b.Bytes())
	var names []string
	for _, e := range m.Entries {
		names = append(names, fmt.Sprintf("%s:%o", e.Path, e.Mode))
	}
	if got := strings.Join(names, " "); got != "a:644 b:755 b/c:755 b/c/x:600 bin:755" {
		t.Fatalf("entries %s", got)
	}
	os.Symlink("/etc/passwd", filepath.Join(dir, "b/link"))
	if _, err := PackDir(dir, io.Discard, Options{}); err == nil || !strings.Contains(err.Error(), "symlink (b/link)") {
		t.Fatalf("symlink: %v", err)
	}
}

// A file's mtime travels in its tar header (Laravel compares a compiled
// view's to its source's), not in the manifest.
func TestModTimeTravels(t *testing.T) {
	at := time.Unix(1_700_000_000, 0)
	var b bytes.Buffer
	w := NewWriter(&b, Options{})
	w.FileTime("a", 0o644, 1, at, strings.NewReader("a"))
	w.Close()
	var got time.Time
	Read(bytes.NewReader(b.Bytes()), lim, func(e Entry, _ io.Reader) error { got = e.ModTime; return nil })
	if !got.Equal(at) {
		t.Fatalf("Read: %v", got)
	}
	m, _ := manifestOf(t, b.Bytes())
	c := m.Chunks[0]
	ReadRange(bytes.NewReader(b.Bytes()[c.Off:c.Off+c.Len]), m, 0, 0, lim, func(e Entry, _ io.Reader) error { got = e.ModTime; return nil })
	if !got.Equal(at) {
		t.Fatalf("ReadRange: %v", got)
	}
}
