// Package bundle is the archive a bundle or a static site travels in, from
// the builder that packs it, through the control plane that checks it, to
// the host that unpacks it. One package, so all three agree on what's in
// one and on what's refused: three readers would be three sets of rules to
// keep in step.
//
// An artifact is a tar compressed with zstd, cut into chunks: each chunk a
// zstd stream of its own holding whole tar entries, so the chunks one after
// another are one ordinary .tar.zst (any zstd tool reads it). After them, in
// a zstd skippable frame that tools pass over, rides the manifest: every
// entry's path, mode, size and sha256, and the chunk it's in. A host that
// already has most of a release's files - its previous release - fetches
// only the chunks with the rest, by byte range, and checks each file it
// writes against the manifest.
//
//	chunk 0 | chunk 1 | ... | chunk n (ends with the tar trailer) | skippable frame
//	                                                               [magic][size][manifest][len][HPBUNDL1]
//
// What's read is held to files and folders at plain relative names: no
// links (a symlink could point anywhere on the host), no devices or fifos,
// no setuid, setgid or sticky bits, within a count of files and bytes that
// is checked as it inflates, never after.
package bundle

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"os"
	"path"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"
)

// Why an archive is refused.
var (
	ErrNotArchive   = errors.New("not a tar.zst or tar.gz archive")
	ErrTooLarge     = errors.New("unpacks to more bytes than allowed")
	ErrTooManyFiles = errors.New("has more files than allowed")
	// ErrContents: something other than a file or a folder at a plain
	// relative name (a *ContentError says which)
	ErrContents = errors.New("has something that isn't a file or a folder")
	ErrManifest = errors.New("manifest doesn't match the archive")
)

// ContentError is an entry that isn't a file or a folder at a plain name.
type ContentError struct {
	What string // "a symlink", "a hard link", ...
	Name string
	Rest string // what follows the name, if anything
}

func (e *ContentError) Error() string {
	if e.Name == "" {
		return e.What + e.Rest
	}
	return e.What + " (" + e.Name + ")" + e.Rest
}

// Is makes every ContentError an ErrContents.
func (e *ContentError) Is(target error) bool { return target == ErrContents }

// Limits bound what an archive may hold: files, and their bytes unpacked.
type Limits struct {
	MaxFiles int
	MaxBytes int64
}

// Options shape what a Writer writes.
type Options struct {
	// ChunkBytes: a chunk is closed once it holds this many bytes of tar
	// (DefaultChunkBytes when 0). Smaller chunks make a host fetch less
	// for a small change, and compress a little worse.
	ChunkBytes int64
	// Level is zstd's (zstd.SpeedBetterCompression when 0).
	Level zstd.EncoderLevel
}

// DefaultChunkBytes is a chunk's size, unpacked: measured on a Laravel
// bundle (199 MB in 6636 files), where it costs ~2% of the packed size
// against one stream, and a code change fetches a few MB of chunks.
const DefaultChunkBytes = 1 << 20

// Summary is what an artifact is: its bytes and their sha256, its
// manifest's sha256, and the files it holds and their bytes unpacked.
type Summary struct {
	Size           int64
	SHA256         string
	ManifestSHA256 string
	Files          int
	Bytes          int64
}

// Entry is one file or folder in the manifest.
type Entry struct {
	Path   string `json:"p"`
	Dir    bool   `json:"d,omitempty"`
	Mode   uint32 `json:"m"` // permission bits alone
	Size   int64  `json:"s,omitempty"`
	SHA256 string `json:"h,omitempty"` // hex; files only
	Chunk  int    `json:"c"`
}

// Chunk is where a chunk lies in the artifact.
type Chunk struct {
	Off int64 `json:"o"`
	Len int64 `json:"l"`
}

// Manifest lists an artifact's chunks and its entries, in archive order.
type Manifest struct {
	Version int     `json:"v"`
	Chunks  []Chunk `json:"chunks"`
	Entries []Entry `json:"entries"`
}

const manifestVersion = 1

// FooterSize is the bytes at an artifact's end that say where its manifest
// is: its length, and a magic.
const FooterSize = 16

var footerMagic = []byte("HPBUNDL1")

// the zstd skippable frame the manifest rides in (any of 0x184D2A50-5F)
const skippableMagic = 0x184D2A5E

var (
	zstdMagic = []byte{0x28, 0xb5, 0x2f, 0xfd}
	gzipMagic = []byte{0x1f, 0x8b}
)

// Writer writes an artifact: Dir and File in the order they're to be
// unpacked, then Close.
type Writer struct {
	out    *countWriter
	opt    Options
	enc    *zstd.Encoder
	tw     *tar.Writer
	cur    *chunkWriter
	m      Manifest
	start  int64 // the open chunk's offset, -1 none
	files  int
	bytes  int64
	closed bool
	err    error
}

type countWriter struct {
	w io.Writer
	h hash.Hash
	n int64
}

func (c *countWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.h.Write(p[:n])
	c.n += int64(n)
	return n, err
}

// chunkWriter is what tar writes to: the open chunk's encoder, counting.
type chunkWriter struct {
	w *Writer
	n int64
}

func (c *chunkWriter) Write(p []byte) (int, error) {
	if err := c.w.open(); err != nil {
		return 0, err
	}
	n, err := c.w.enc.Write(p)
	c.n += int64(n)
	return n, err
}

// NewWriter writes an artifact to w.
func NewWriter(w io.Writer, opt Options) *Writer {
	if opt.ChunkBytes <= 0 {
		opt.ChunkBytes = DefaultChunkBytes
	}
	if opt.Level == 0 {
		opt.Level = zstd.SpeedBetterCompression
	}
	bw := &Writer{out: &countWriter{w: w, h: sha256.New()}, opt: opt, start: -1, m: Manifest{Version: manifestVersion}}
	bw.cur = &chunkWriter{w: bw}
	bw.tw = tar.NewWriter(bw.cur)
	return bw
}

// open starts a chunk if none is open.
func (w *Writer) open() error {
	if w.start >= 0 {
		return nil
	}
	if w.enc == nil {
		enc, err := zstd.NewWriter(w.out, zstd.WithEncoderLevel(w.opt.Level), zstd.WithEncoderConcurrency(1))
		if err != nil {
			return err
		}
		w.enc = enc
	} else {
		w.enc.Reset(w.out)
	}
	w.start, w.cur.n = w.out.n, 0
	return nil
}

// cut closes the open chunk: its zstd stream ends, whole.
func (w *Writer) cut() error {
	if w.start < 0 {
		return nil
	}
	if !w.closed { // Close has written the trailer
		if err := w.tw.Flush(); err != nil {
			return err
		}
	}
	if err := w.enc.Close(); err != nil {
		return err
	}
	w.m.Chunks = append(w.m.Chunks, Chunk{Off: w.start, Len: w.out.n - w.start})
	w.start = -1
	return nil
}

// next is the chunk the next entry goes in, cutting the open one if it's
// full.
func (w *Writer) next() (int, error) {
	if w.closed {
		return 0, errors.New("bundle: write after close")
	}
	if w.start >= 0 && w.cur.n >= w.opt.ChunkBytes {
		if err := w.cut(); err != nil {
			return 0, err
		}
	}
	if err := w.open(); err != nil {
		return 0, err
	}
	return len(w.m.Chunks), nil
}

func (w *Writer) fail(err error) error {
	if w.err == nil {
		w.err = err
	}
	return err
}

// Dir adds a folder.
func (w *Writer) Dir(name string, mode fs.FileMode) error {
	return w.DirTime(name, mode, time.Time{})
}

// DirTime adds a folder, modified at mtime.
func (w *Writer) DirTime(name string, mode fs.FileMode, mtime time.Time) error {
	if w.err != nil {
		return w.err
	}
	if !validName(name) {
		return w.fail(&ContentError{What: "a path out of the archive", Name: name})
	}
	c, err := w.next()
	if err != nil {
		return w.fail(err)
	}
	perm := uint32(mode.Perm())
	if err := w.tw.WriteHeader(&tar.Header{Typeflag: tar.TypeDir, Name: name + "/", Mode: int64(perm), ModTime: mtime}); err != nil {
		return w.fail(err)
	}
	w.m.Entries = append(w.m.Entries, Entry{Path: name, Dir: true, Mode: perm, Chunk: c})
	return nil
}

// File adds a file of size bytes, read from r.
func (w *Writer) File(name string, mode fs.FileMode, size int64, r io.Reader) error {
	return w.FileTime(name, mode, size, time.Time{}, r)
}

// FileTime adds a file of size bytes, read from r, modified at mtime.
func (w *Writer) FileTime(name string, mode fs.FileMode, size int64, mtime time.Time, r io.Reader) error {
	if w.err != nil {
		return w.err
	}
	if !validName(name) {
		return w.fail(&ContentError{What: "a path out of the archive", Name: name})
	}
	if size < 0 {
		return w.fail(fmt.Errorf("bundle: %s: negative size", name))
	}
	c, err := w.next()
	if err != nil {
		return w.fail(err)
	}
	perm := uint32(mode.Perm())
	if err := w.tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: name, Mode: int64(perm), Size: size, ModTime: mtime}); err != nil {
		return w.fail(err)
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(w.tw, h), io.LimitReader(r, size))
	if err != nil {
		return w.fail(err)
	}
	if n != size {
		return w.fail(fmt.Errorf("bundle: %s: read %d bytes, expected %d", name, n, size))
	}
	w.m.Entries = append(w.m.Entries, Entry{Path: name, Mode: perm, Size: size, SHA256: hex.EncodeToString(h.Sum(nil)), Chunk: c})
	w.files++
	w.bytes += size
	return nil
}

// Close ends the archive and writes its manifest. The summary is the
// whole artifact's.
func (w *Writer) Close() (Summary, error) {
	if w.err != nil {
		return Summary{}, w.err
	}
	if w.closed {
		return Summary{}, errors.New("bundle: closed twice")
	}
	w.closed = true
	if err := w.open(); err != nil {
		return Summary{}, err
	}
	// the trailer, in the last chunk
	if err := w.tw.Close(); err != nil {
		return Summary{}, err
	}
	if err := w.cut(); err != nil {
		return Summary{}, err
	}
	raw, err := w.m.Encode()
	if err != nil {
		return Summary{}, err
	}
	if _, err := w.out.Write(skippable(raw)); err != nil {
		return Summary{}, err
	}
	ms := sha256.Sum256(raw)
	return Summary{Size: w.out.n, SHA256: hex.EncodeToString(w.out.h.Sum(nil)), ManifestSHA256: hex.EncodeToString(ms[:]),
		Files: w.files, Bytes: w.bytes}, nil
}

// skippable is the frame the manifest rides in, its footer at its end.
func skippable(raw []byte) []byte {
	b := make([]byte, 8, 8+len(raw)+FooterSize)
	binary.LittleEndian.PutUint32(b[0:], skippableMagic)
	binary.LittleEndian.PutUint32(b[4:], uint32(len(raw)+FooterSize))
	b = append(b, raw...)
	b = binary.LittleEndian.AppendUint64(b, uint64(len(raw)))
	return append(b, footerMagic...)
}

// Encode is the manifest as it's stored: JSON, compressed with zstd.
func (m *Manifest) Encode() ([]byte, error) {
	j, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	enc, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedBetterCompression), zstd.WithEncoderConcurrency(1))
	if err != nil {
		return nil, err
	}
	defer enc.Close()
	return enc.EncodeAll(j, nil), nil
}

// ParseFooter is the length of the manifest that ends right before footer,
// an artifact's last FooterSize bytes.
func ParseFooter(footer []byte) (int64, error) {
	if len(footer) != FooterSize || !bytes.Equal(footer[8:], footerMagic) {
		return 0, fmt.Errorf("%w: no manifest at its end", ErrManifest)
	}
	n := binary.LittleEndian.Uint64(footer)
	if n == 0 || n > 1<<30 {
		return 0, fmt.Errorf("%w: manifest of %d bytes", ErrManifest, n)
	}
	return int64(n), nil
}

// ManifestRange is where the manifest is in an artifact of size bytes
// whose last FooterSize bytes are footer.
func ManifestRange(size int64, footer []byte) (off, n int64, err error) {
	if n, err = ParseFooter(footer); err != nil {
		return 0, 0, err
	}
	off = size - FooterSize - n
	if off < 8 {
		return 0, 0, fmt.Errorf("%w: manifest of %d bytes in %d", ErrManifest, n, size)
	}
	return off, n, nil
}

// maxManifestEntryBytes bounds an entry's JSON, a path of up to maxName
// with its hash and numbers.
const (
	maxName               = 4096
	maxManifestEntryBytes = maxName + 256
)

// maxEntries is how many entries (folders too) limits allow: folders can
// outnumber files, but not without bound.
func maxEntries(lim Limits) int { return 3*lim.MaxFiles + 16 }

// DecodeManifest reads a stored manifest and checks it hangs together
// within lim: chunks one after another from the start, entries in chunk
// order at plain unique names, no entry under a file, files and bytes
// within the limits. It's still only a claim about the archive: what's
// read with it is checked against it (ReadRange, Verify).
func DecodeManifest(raw []byte, lim Limits) (*Manifest, error) {
	d, err := zstd.NewReader(bytes.NewReader(raw), zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxMemory(64<<20))
	if err != nil {
		return nil, err
	}
	defer d.Close()
	most := int64(maxEntries(lim))*maxManifestEntryBytes + 4096
	j, err := io.ReadAll(io.LimitReader(d, most+1))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrManifest, err)
	}
	if int64(len(j)) > most {
		return nil, fmt.Errorf("%w: manifest too large", ErrTooManyFiles)
	}
	var m Manifest
	if err := json.Unmarshal(j, &m); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrManifest, err)
	}
	return &m, m.check(lim)
}

func (m *Manifest) check(lim Limits) error {
	bad := func(format string, a ...any) error {
		return fmt.Errorf("%w: "+format, append([]any{ErrManifest}, a...)...)
	}
	if m.Version != manifestVersion {
		return bad("version %d", m.Version)
	}
	if len(m.Chunks) == 0 {
		return bad("no chunks")
	}
	var at int64
	for i, c := range m.Chunks {
		if c.Off != at || c.Len <= 0 {
			return bad("chunk %d at %d+%d, expected at %d", i, c.Off, c.Len, at)
		}
		at += c.Len
	}
	if len(m.Entries) > maxEntries(lim) {
		return fmt.Errorf("%w: more than %d", ErrTooManyFiles, lim.MaxFiles)
	}
	kinds := make(map[string]bool, len(m.Entries)) // path: is a folder
	files, chunk := 0, 0
	var total int64
	for _, e := range m.Entries {
		if e.Chunk < chunk || e.Chunk >= len(m.Chunks) {
			return bad("%s in chunk %d", e.Path, e.Chunk)
		}
		chunk = e.Chunk
		if !validName(e.Path) {
			return &ContentError{What: "a path out of the archive", Name: e.Path}
		}
		if _, dup := kinds[e.Path]; dup {
			return bad("%s twice", e.Path)
		}
		if parent := path.Dir(e.Path); parent != "." {
			if dir, ok := kinds[parent]; ok && !dir {
				return bad("%s is under a file", e.Path)
			}
		}
		kinds[e.Path] = e.Dir
		if e.Mode&^0o777 != 0 {
			return bad("%s has mode %o", e.Path, e.Mode)
		}
		if e.Dir {
			if e.Size != 0 || e.SHA256 != "" {
				return bad("folder %s has a size or a hash", e.Path)
			}
			continue
		}
		if e.Size < 0 || !validHash(e.SHA256) {
			return bad("%s has size %d, hash %q", e.Path, e.Size, e.SHA256)
		}
		files++
		total += e.Size
		if files > lim.MaxFiles {
			return fmt.Errorf("%w: more than %d", ErrTooManyFiles, lim.MaxFiles)
		}
		if total > lim.MaxBytes {
			return fmt.Errorf("%w: over %d bytes", ErrTooLarge, lim.MaxBytes)
		}
	}
	// a folder named after its files (entries come parent first from a
	// writer, but a folder listed after its contents is still a folder)
	for p := range kinds {
		for q := path.Dir(p); q != "."; q = path.Dir(q) {
			if d, ok := kinds[q]; ok && !d {
				return bad("%s is under a file", p)
			}
		}
	}
	return nil
}

func validHash(h string) bool {
	if len(h) != 64 {
		return false
	}
	for _, c := range h {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// validName is a plain relative path: no leading /, no . or .. segment,
// no empty segment, no NUL.
func validName(name string) bool {
	return name != "" && len(name) <= maxName && !strings.ContainsRune(name, 0) && !strings.HasPrefix(name, "/") &&
		path.Clean(name) == name && name != "." && name != ".." && !strings.HasPrefix(name, "../")
}

// entryName is a tar header's name as an entry's path: "./" and a
// folder's trailing "/" cut; "" is the archive's own top folder.
func entryName(h *tar.Header) string {
	name := strings.TrimPrefix(h.Name, "./")
	for strings.HasPrefix(name, "./") {
		name = strings.TrimPrefix(name, "./")
	}
	if name == "." || name == "./" {
		return ""
	}
	if h.Typeflag == tar.TypeDir || (h.Typeflag == tar.TypeReg || h.Typeflag == tar.TypeRegA) && strings.HasSuffix(name, "/") {
		name = strings.TrimSuffix(name, "/")
	}
	return name
}

// kind refuses a header that isn't a file or a folder at a plain name; dir
// is whether it's a folder.
func kind(h *tar.Header, name string) (dir bool, err error) {
	switch h.Typeflag {
	case tar.TypeSymlink:
		return false, &ContentError{What: "a symlink", Name: name, Rest: ": files, not links"}
	case tar.TypeLink:
		return false, &ContentError{What: "a hard link", Name: name, Rest: ": files, not links"}
	case tar.TypeDir:
		dir = true
	case tar.TypeReg, tar.TypeRegA:
		dir = strings.HasSuffix(h.Name, "/")
	default:
		return false, &ContentError{What: "something that isn't a file or a folder", Name: name}
	}
	if !validName(name) {
		return false, &ContentError{What: "a path out of the archive", Name: h.Name}
	}
	return dir, nil
}

// decompressor reads r as gzip or zstd, whichever it is.
func decompressor(r io.Reader) (io.Reader, func(), error) {
	br := bufio.NewReaderSize(r, 64<<10)
	head, _ := br.Peek(4)
	switch {
	case bytes.HasPrefix(head, zstdMagic):
		d, err := zstd.NewReader(br, zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxMemory(256<<20))
		if err != nil {
			return nil, nil, err
		}
		return d, d.Close, nil
	case bytes.HasPrefix(head, gzipMagic):
		z, err := gzip.NewReader(br)
		if err != nil {
			return nil, nil, ErrNotArchive
		}
		return z, func() { z.Close() }, nil
	}
	return nil, nil, ErrNotArchive
}

// streamCap bounds a whole tar's bytes as they inflate - its files' and
// what tar adds around them - so junk can't make a reader spin.
func streamCap(lim Limits) int64 {
	return lim.MaxBytes + int64(maxEntries(lim))*3*512 + 1<<20
}

// Read reads a whole archive - a tar.zst from a Writer, or any tar.gz -
// calling fn (if not nil) with each file and folder in turn; a file's body
// is its bytes, read or not. It refuses anything else, a name that isn't
// plain, and more files or bytes than lim, counted as they inflate.
// Setuid, setgid and sticky bits are dropped from what fn is told.
func Read(r io.Reader, lim Limits, fn func(Entry, io.Reader) error) error {
	dec, done, err := decompressor(r)
	if err != nil {
		return err
	}
	defer done()
	capped := io.LimitReader(dec, streamCap(lim)+1)
	tr := tar.NewReader(capped)
	files := 0
	var total int64
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("%w: %v", ErrNotArchive, err)
		}
		if h.Typeflag == tar.TypeXGlobalHeader {
			continue
		}
		name := entryName(h)
		if name == "" && (h.Typeflag == tar.TypeDir) {
			continue // the archive's own top folder
		}
		dir, err := kind(h, name)
		if err != nil {
			return err
		}
		e := Entry{Path: name, Dir: dir, Mode: uint32(h.Mode) & 0o777}
		if !dir {
			e.Size = h.Size
			files++
			total += h.Size
			if files > lim.MaxFiles {
				return fmt.Errorf("%w: more than %d", ErrTooManyFiles, lim.MaxFiles)
			}
			if h.Size < 0 || total > lim.MaxBytes {
				return fmt.Errorf("%w: over %d bytes", ErrTooLarge, lim.MaxBytes)
			}
		}
		if fn != nil {
			if err := fn(e, tr); err != nil {
				return err
			}
		}
		if _, err := io.Copy(io.Discard, tr); err != nil {
			return fmt.Errorf("%w: %v", ErrNotArchive, err)
		}
	}
	// what follows the archive's end must inflate too
	n, err := io.Copy(io.Discard, capped)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrNotArchive, err)
	}
	if n > 0 && capped.(*io.LimitedReader).N == 0 {
		return fmt.Errorf("%w: over %d bytes", ErrTooLarge, lim.MaxBytes)
	}
	return nil
}

// ReadRange reads chunks first through last of m's artifact from r - their
// bytes alone, one after another - calling fn with each entry in them in
// turn. Each entry is checked against the manifest as it's read: its name,
// kind, mode and size, and a file's sha256 once its bytes are through (fn
// is still called first: a caller that writes a file removes it when
// ReadRange fails).
func ReadRange(r io.Reader, m *Manifest, first, last int, lim Limits, fn func(Entry, io.Reader) error) error {
	if first < 0 || last >= len(m.Chunks) || first > last {
		return fmt.Errorf("bundle: chunks %d-%d of %d", first, last, len(m.Chunks))
	}
	d, err := zstd.NewReader(r, zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxMemory(256<<20))
	if err != nil {
		return err
	}
	defer d.Close()
	var want []Entry
	var size int64
	for _, e := range m.Entries {
		if e.Chunk >= first && e.Chunk <= last {
			want = append(want, e)
			size += e.Size
		}
	}
	capped := io.LimitReader(d, size+int64(len(want))*3*512+1<<20)
	tr := tar.NewReader(capped)
	h256 := sha256.New()
	for _, e := range want {
		h, err := tr.Next()
		if err != nil {
			return fmt.Errorf("%w: %s: %v", ErrManifest, e.Path, err)
		}
		name := entryName(h)
		dir, err := kind(h, name)
		if err != nil {
			return err
		}
		if name != e.Path || dir != e.Dir || uint32(h.Mode)&0o7777 != e.Mode || (!dir && h.Size != e.Size) {
			return fmt.Errorf("%w: %s (%o, %d bytes) where it lists %s (%o, %d bytes)", ErrManifest, name, h.Mode, h.Size, e.Path, e.Mode, e.Size)
		}
		h256.Reset()
		body := io.TeeReader(tr, h256)
		if fn != nil {
			if err := fn(e, body); err != nil {
				return err
			}
		}
		if _, err := io.Copy(io.Discard, body); err != nil {
			return fmt.Errorf("%w: %s: %v", ErrManifest, e.Path, err)
		}
		if !dir && hex.EncodeToString(h256.Sum(nil)) != e.SHA256 {
			return fmt.Errorf("%w: %s isn't what it lists", ErrManifest, e.Path)
		}
	}
	// nothing more: the chunks' end, or the archive's trailer
	if _, err := tr.Next(); err != io.EOF {
		return fmt.Errorf("%w: more in chunks %d-%d than it lists", ErrManifest, first, last)
	}
	rest, err := io.ReadAll(io.LimitReader(capped, 1<<20))
	if err != nil {
		return fmt.Errorf("%w: %v", ErrManifest, err)
	}
	if len(rest) == 1<<20 || bytes.Count(rest, []byte{0}) != len(rest) {
		return fmt.Errorf("%w: more in chunks %d-%d than it lists", ErrManifest, first, last)
	}
	return nil
}

// Verify reads a whole artifact of size bytes from r against its manifest
// raw (read first from its end: ManifestRange), calling fn (if not nil) as
// ReadRange does. Every entry is checked against the manifest, every chunk
// is read on its own, and what follows the chunks is exactly the manifest's
// frame: what passes, a host may fetch any chunk of and trust. The summary
// is the artifact's.
func Verify(r io.Reader, size int64, raw []byte, lim Limits, fn func(Entry, io.Reader) error) (Summary, error) {
	m, err := DecodeManifest(raw, lim)
	if err != nil {
		return Summary{}, err
	}
	end := m.Chunks[len(m.Chunks)-1].Off + m.Chunks[len(m.Chunks)-1].Len
	frame := skippable(raw)
	if end+int64(len(frame)) != size {
		return Summary{}, fmt.Errorf("%w: %d bytes of chunks and %d of manifest in %d", ErrManifest, end, len(frame), size)
	}
	h := sha256.New()
	tee := io.TeeReader(r, h)
	for i, c := range m.Chunks {
		chunk := io.LimitReader(tee, c.Len)
		if err := ReadRange(chunk, m, i, i, lim, fn); err != nil {
			return Summary{}, err
		}
		if n, _ := io.Copy(io.Discard, chunk); n != 0 {
			return Summary{}, fmt.Errorf("%w: chunk %d has %d bytes past its end", ErrManifest, i, n)
		}
	}
	got := make([]byte, len(frame))
	if _, err := io.ReadFull(tee, got); err != nil || !bytes.Equal(got, frame) {
		return Summary{}, fmt.Errorf("%w: the manifest's frame isn't at the end", ErrManifest)
	}
	if n, _ := io.Copy(io.Discard, io.LimitReader(tee, 1)); n != 0 {
		return Summary{}, fmt.Errorf("%w: bytes after the manifest", ErrManifest)
	}
	sum := Summary{Size: size, SHA256: hex.EncodeToString(h.Sum(nil))}
	ms := sha256.Sum256(raw)
	sum.ManifestSHA256 = hex.EncodeToString(ms[:])
	for _, e := range m.Entries {
		if !e.Dir {
			sum.Files++
			sum.Bytes += e.Size
		}
	}
	return sum, nil
}

// PackDir writes the folder dir as an artifact to w: its files and folders
// in name order, never following a link - a symlink, a device or a fifo is
// refused.
func PackDir(dir string, w io.Writer, opt Options) (Summary, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return Summary{}, err
	}
	defer root.Close()
	bw := NewWriter(w, opt)
	err = fs.WalkDir(root.FS(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == "." {
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		switch t := fi.Mode().Type(); {
		case t == fs.ModeDir:
			return bw.DirTime(p, fi.Mode(), fi.ModTime())
		case t == 0:
			f, err := root.Open(p)
			if err != nil {
				return err
			}
			defer f.Close()
			return bw.FileTime(p, fi.Mode(), fi.Size(), fi.ModTime(), f)
		case t&fs.ModeSymlink != 0:
			return &ContentError{What: "a symlink", Name: p, Rest: ": files, not links"}
		default:
			return &ContentError{What: "something that isn't a file or a folder", Name: p}
		}
	})
	if err != nil {
		return Summary{}, err
	}
	return bw.Close()
}
