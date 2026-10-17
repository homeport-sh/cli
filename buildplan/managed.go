package buildplan

import (
	"io"
	"io/fs"
	"regexp"
	"slices"

	"github.com/homeport-sh/cli/envfile"
)

// Framework-managed variables (design/build-variables.md in homeport
// cloud): what a framework needs the same on every build and every copy of
// an app - an action encryption key above all - which nobody sets by
// hand, so homeport sets them for each environment.

// How a managed variable's value is made.
const (
	// MakeKey: 32 random bytes, standard base64 (an AES-256 key for
	// Next.js, @vitejs/plugin-rsc and Astro; an HMAC secret for rsc-kit),
	// made once per environment and kept.
	MakeKey = "key"
	// MakeRelease: the deploy's release name, made for each build and
	// never kept.
	MakeRelease = "release"
)

// ManagedVar is one variable homeport sets for an app.
type ManagedVar struct {
	Name string
	// Make is how its value is made: MakeKey or MakeRelease.
	Make string
	// Available is who sees it.
	Available envfile.Availability
	// Fixed: its availability can't be changed, even on a value of the
	// person's own under the name (another breaks the framework).
	Fixed bool
	// Previous is where a rotation keeps the value before it, so what was
	// signed with it still checks ("" when nothing reads one).
	Previous string
	// Why it's set, for a person: one line.
	Why string
}

var managed = []ManagedVar{
	{Name: "ASTRO_KEY", Make: MakeKey, Available: envfile.BuildOnly,
		Why: "Astro server islands: the same key on every build keeps pages cached from one build working with the next"},
	{Name: "NEXT_DEPLOYMENT_ID", Make: MakeRelease, Available: envfile.BuildOnly, Fixed: true,
		Why: "Next.js: tells deploys apart, so a page from an older deploy reloads instead of failing; set for each deploy"},
	{Name: "NEXT_SERVER_ACTIONS_ENCRYPTION_KEY", Make: MakeKey, Available: envfile.Build, Fixed: true,
		Why: "Next.js server actions: the same key on every build keeps forms in open tabs working across deploys"},
	{Name: "RSC_ACTION_ENCRYPTION_KEY", Make: MakeKey, Available: envfile.Build, Fixed: true,
		Why: "React server actions (@vitejs/plugin-rsc): the same key on every build keeps forms in open tabs working across deploys. " +
			"rsc-kit reads it; another framework needs rsc({ defineEncryptionKey: \"process.env.RSC_ACTION_ENCRYPTION_KEY\" })"},
	{Name: "RSC_SIGNING_SECRET", Make: MakeKey, Available: envfile.Runtime, Previous: "RSC_SIGNING_SECRET_PREVIOUS",
		Why: "rsc-kit: signs what a page watches for changes, the same on every copy of the app"},
}

// Managed is homeport's entry for a name, whatever the app: how a control
// plane tells a name's rules (a fixed availability) from a person's own.
func Managed(name string) (ManagedVar, bool) {
	i := slices.IndexFunc(managed, func(m ManagedVar) bool { return m.Name == name })
	if i < 0 {
		return ManagedVar{}, false
	}
	return managed[i], true
}

// ManagedNames are every name homeport may set, sorted.
func ManagedNames() []string {
	out := make([]string, len(managed))
	for i, m := range managed {
		out[i] = m.Name
	}
	return out
}

// pluginRSC is @vitejs/plugin-rsc named in a lockfile, as each package
// manager writes it: "@vitejs/plugin-rsc": (bun.lock), node_modules/... "
// (package-lock.json), '@vitejs/plugin-rsc@0.5.38': (pnpm),
// "@vitejs/plugin-rsc@^0.5.38": (Yarn); not another package it begins.
var pluginRSC = regexp.MustCompile(`@vitejs/plugin-rsc(?:["'@:\s]|$)`)

// maxLockfile is the most of a lockfile read to find a package: far more
// than detection's 1 MiB, since a large app's lockfile is several.
const maxLockfile = 64 << 20

// ManagedVars are the variables homeport sets for the app in fsys (a
// repository), at s's root, sorted by name: told from its package.json and
// its lockfile (its workspace's, for a member), so a framework that depends
// on @vitejs/plugin-rsc counts too. None for an app that isn't JavaScript.
func ManagedVars(fsys fs.FS, s Settings) []ManagedVar {
	r := reader{fsys: fsys, root: "."}
	if s.Root != "" {
		r.root = clean(s.Root)
	}
	deps := r.deps()
	if deps == nil {
		return nil
	}
	if !r.hasLockfile() {
		if w, err := r.findWorkspace(); err == nil {
			r.ws = w
		}
	}
	want := map[string]bool{}
	if deps["next"] {
		want["NEXT_SERVER_ACTIONS_ENCRYPTION_KEY"], want["NEXT_DEPLOYMENT_ID"] = true, true
	}
	if deps["@vitejs/plugin-rsc"] || r.lock().lockfileNames(pluginRSC) {
		want["RSC_ACTION_ENCRYPTION_KEY"] = true
	}
	if deps["@rsc-kit/core"] {
		want["RSC_SIGNING_SECRET"] = true
	}
	if deps["astro"] && r.astroServerOutput() {
		want["ASTRO_KEY"] = true
	}
	var out []ManagedVar
	for _, m := range managed {
		if want[m.Name] {
			out = append(out, m)
		}
	}
	return out
}

// lockfileNames says whether any of the folder's lockfiles matches re.
func (r reader) lockfileNames(re *regexp.Regexp) bool {
	for _, l := range lockfiles {
		p := r.name(l.file)
		st, err := lstat(r.fsys, p)
		if err != nil || !st.Mode().IsRegular() {
			continue
		}
		f, err := r.fsys.Open(p)
		if err != nil {
			continue
		}
		b, err := io.ReadAll(io.LimitReader(f, maxLockfile))
		f.Close()
		if err == nil && re.Match(b) {
			return true
		}
	}
	return false
}

// astroServerOutput: an Astro config that renders on a server (an output
// of server or hybrid, or an adapter).
func (r reader) astroServerOutput() bool {
	for _, f := range []string{"astro.config.mjs", "astro.config.ts", "astro.config.js", "astro.config.mts"} {
		if b, err := r.read(f); err == nil && astroServer.Match(b) {
			return true
		}
	}
	return false
}
