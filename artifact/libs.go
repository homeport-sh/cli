package artifact

import (
	"debug/elf"
	"io"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
)

// An app's executable runs in a sandbox that lends it the server's own
// libraries, read-only (/lib, /usr/lib: Ubuntu 24.04's). Linked against
// one the servers don't have (libpq, a system SQLite), it builds, ships,
// and fails as it starts - after the deploy, as a release that never gets
// healthy. CheckLibraries says so at build time instead, for any
// executable: a Rust or Go (cgo) binary, a bundle's bin, whatever built it.

// HostLibraries are the libraries every server lends an app: glibc's own,
// GCC's runtime (libgcc_s, libstdc++), OpenSSL 3, and the compression
// libraries Ubuntu's base system can't be without (dpkg's: zlib, zstd, xz,
// bzip2). A server may have more; these are the ones homeport promises.
var HostLibraries = []string{
	// glibc 2.39
	"libc.so.6", "libm.so.6", "libmvec.so.1", "libpthread.so.0", "libdl.so.2", "librt.so.1",
	"libutil.so.1", "libresolv.so.2", "libanl.so.1", "libcrypt.so.1",
	"ld-linux-x86-64.so.2", "ld-linux-aarch64.so.1",
	// GCC's runtime
	"libgcc_s.so.1", "libstdc++.so.6",
	// OpenSSL 3
	"libssl.so.3", "libcrypto.so.3",
	// compression
	"libz.so.1", "libzstd.so.1", "liblzma.so.5", "libbz2.so.1.0",
}

// hostLoaders are glibc's dynamic loaders, by name: any other (musl's) the
// servers don't have.
var hostLoaders = []string{"ld-linux-x86-64.so.2", "ld-linux-aarch64.so.1"}

// libraryFixes: what to do instead of a library the servers don't have,
// for the ones apps commonly link.
var libraryFixes = map[string]string{
	"libpq.so.5":           "libpq (Postgres): a pure-Rust client (sqlx, tokio-postgres) needs none, nor does Go's pgx; Diesel's postgres links it in with pq-sys's bundled feature",
	"libsqlite3.so.0":      "SQLite: the bundled feature links it in (rusqlite, libsqlite3-sys; sqlx's sqlite does by default); in Go, a pure-Go driver (modernc.org/sqlite)",
	"libmysqlclient.so.21": "MySQL's client: a pure-Rust client (sqlx, mysql_async) needs none, nor does Go's go-sql-driver/mysql; Diesel's mysql links it in with mysqlclient-sys's bundled feature",
	"libmariadb.so.3":      "MariaDB's client: a pure-Rust client (sqlx, mysql_async) needs none, nor does Go's go-sql-driver/mysql",
	"libssl.so.1.1":        "OpenSSL 1.1: the servers have OpenSSL 3 - build on a newer image, or use rustls instead of native-tls",
	"libcrypto.so.1.1":     "OpenSSL 1.1: the servers have OpenSSL 3 - build on a newer image, or use rustls instead of native-tls",
}

// LibraryError is an executable that wouldn't start on a server: its
// loader isn't glibc's (Loader), or it needs libraries the servers don't
// have and it doesn't carry (Missing).
type LibraryError struct {
	Loader  string
	Missing []string
}

func (e *LibraryError) Error() string {
	if e.Loader != "" {
		return "it's linked against another C library than glibc (its loader is " + e.Loader + "; musl's, as on Alpine): the servers run glibc - build it on a glibc image (Debian, Ubuntu), or statically"
	}
	var fixes []string
	for _, l := range e.Missing {
		if f, ok := libraryFixes[l]; ok && !slices.Contains(fixes, f) {
			fixes = append(fixes, f)
		}
	}
	s := "it needs " + strings.Join(e.Missing, ", ") + ", which the servers don't have: they lend an app glibc, libgcc, libstdc++, OpenSSL 3, zlib, zstd, xz and bzip2 alone. " +
		"Link the rest in statically, or use a library that needs none"
	if len(fixes) > 0 {
		s += ". " + strings.Join(fixes, ". ")
	}
	return s
}

// CheckLibraries says whether the executable at p would start on a server:
// a nil error when it isn't a dynamically linked ELF (a static one, a
// script), or when its loader is glibc's and each library it needs is a
// server's (HostLibraries) or, in a bundle whose folder is root, one it
// carries where its RUNPATH (or RPATH) finds it, relative to it ($ORIGIN)
// and inside the bundle. A lone binary ships alone: root "", nothing beside
// it counts. A *LibraryError says what wouldn't start.
//
// Only the executable's own needs are read, not those of the libraries it
// carries.
func CheckLibraries(p, root string) (err error) {
	f, err := elf.Open(p)
	if err != nil {
		return nil // not an ELF: whatever runs it says
	}
	defer f.Close()
	defer func() {
		if recover() != nil {
			err = nil // malformed past what debug/elf reads: it fails as it starts, if at all
		}
	}()
	for _, prog := range f.Progs {
		if prog.Type != elf.PT_INTERP {
			continue
		}
		b, _ := io.ReadAll(io.LimitReader(prog.Open(), 4096))
		interp := strings.TrimRight(string(b), "\x00")
		if !slices.Contains(hostLoaders, path.Base(interp)) {
			return &LibraryError{Loader: interp}
		}
	}
	needed, err := f.ImportedLibraries()
	if err != nil {
		return nil
	}
	dirs, _ := f.DynString(elf.DT_RUNPATH)
	if len(dirs) == 0 {
		dirs, _ = f.DynString(elf.DT_RPATH) // ld.so reads RPATH only without a RUNPATH
	}
	var missing []string
	for _, lib := range needed {
		if !slices.Contains(HostLibraries, lib) && !carried(p, root, dirs, lib) {
			missing = append(missing, lib)
		}
	}
	if len(missing) > 0 {
		return &LibraryError{Missing: missing}
	}
	return nil
}

// carried says whether a bundle (root) carries lib where the executable
// p's runpath (dirs, each a list) finds it: a $ORIGIN-relative folder
// inside root.
func carried(p, root string, dirs []string, lib string) bool {
	if root == "" || strings.Contains(lib, "/") {
		return false
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return false
	}
	origin, err := filepath.Abs(filepath.Dir(p))
	if err != nil {
		return false
	}
	for _, list := range dirs {
		for _, d := range strings.Split(list, ":") {
			rest, ok := strings.CutPrefix(d, "$ORIGIN")
			if !ok {
				rest, ok = strings.CutPrefix(d, "${ORIGIN}")
			}
			if !ok {
				continue // a folder of the server's: none of these is promised
			}
			dir := filepath.Join(origin, rest)
			if rel, err := filepath.Rel(root, dir); err != nil || rel == ".." || strings.HasPrefix(rel, "../") {
				continue
			}
			if fi, err := os.Lstat(filepath.Join(dir, lib)); err == nil && fi.Mode().IsRegular() {
				return true
			}
		}
	}
	return false
}
