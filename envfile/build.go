package envfile

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
)

// Build variables (design/build-variables.md in homeport cloud): every
// variable has an "Available to" - the running app alone (the default),
// the build and the running app, or the build alone. One list of names a
// framework writes into the page, and the build's limits, shared by the
// homeport CLI, homeport cloud and (through cloud's page data) its
// dashboard, so none keeps a copy.

// Availability is who sees a variable's value.
type Availability string

const (
	// Runtime: the running app alone, as every variable was before.
	Runtime Availability = "runtime"
	// Build: the build and the running app.
	Build Availability = "build"
	// BuildOnly: the build alone; never sent to the app's host.
	BuildOnly Availability = "build_only"
)

// Availabilities are the three, in the order they're offered.
var Availabilities = []Availability{Runtime, Build, BuildOnly}

// Valid says whether a is one of the three, as stored.
func (a Availability) Valid() bool { return slices.Contains(Availabilities, a) }

// BuildSees says whether a build is given the value.
func (a Availability) BuildSees() bool { return a == Build || a == BuildOnly }

// RuntimeSees says whether the running app is given the value.
func (a Availability) RuntimeSees() bool { return a == Runtime || a == Build }

// Label is how a is shown to a person.
func (a Availability) Label() string {
	switch a {
	case Runtime:
		return "Runtime"
	case Build:
		return "Build and runtime"
	case BuildOnly:
		return "Build only"
	}
	return ""
}

// ParseAvailability is s as an Availability, refused unless it's one of
// the three as stored (runtime, build, build_only).
func ParseAvailability(s string) (Availability, error) {
	a := Availability(s)
	if !a.Valid() {
		return "", fmt.Errorf("%q: available to is runtime, build or build_only", s)
	}
	return a, nil
}

// PublicPrefixes are the name prefixes a framework writes into what it
// builds (Vite and what's built on it, Next.js, Astro and SvelteKit 2,
// Nuxt, Expo, Create React App, Gatsby, Vue CLI, Storybook, RedwoodJS):
// public by that framework's own rule, so they default to Build.
var PublicPrefixes = []string{"VITE_", "NEXT_PUBLIC_", "PUBLIC_", "NUXT_PUBLIC_", "EXPO_PUBLIC_", "REACT_APP_",
	"GATSBY_", "VUE_APP_", "STORYBOOK_", "REDWOOD_ENV_"}

// DefaultAvailable is a name's availability when it's first set and none
// is given: Build for a public prefix followed by a name, else Runtime.
// Prefixes match as written, upper case, as the frameworks match them.
func DefaultAvailable(name string) Availability {
	for _, p := range PublicPrefixes {
		if len(name) > len(p) && strings.HasPrefix(name, p) {
			return Build
		}
	}
	return Runtime
}

// A build's limits: every one checked when a value is set, and again when
// a build is sent. MaxBuildBytes keeps the job within the 64 KiB a builder
// reads: the job's links, settings and ids need the other half.
const (
	MaxBuildVars  = 64       // variables a build is given
	MaxBuildValue = 4096     // bytes one build value holds
	MaxBuildBytes = 32 << 10 // bytes a build's variables take in its job, as JSON
)

// BuilderNames are what a builder sets in a build itself, and must win:
// the build's PATH, its HOME and temp folder, and the package managers'
// caches. A build may not take them; at runtime they're ordinary.
var BuilderNames = []string{"PATH", "HOME", "TMPDIR", "CI", "GOCACHE", "GOMODCACHE", "BUN_INSTALL_CACHE_DIR", "npm_config_cache", "CARGO_HOME"}

// CheckBuildName says whether a build may be given a variable named n:
// well formed, not Reserved, and not one of BuilderNames.
func CheckBuildName(n string) error {
	if !ValidName(n) {
		return fmt.Errorf("invalid env var name %q (letters, digits and _, not starting with a digit)", n)
	}
	if Reserved(n) {
		return fmt.Errorf("env var name %q is reserved: it would change how a shell or the dynamic linker behaves", n)
	}
	if slices.Contains(BuilderNames, n) {
		return fmt.Errorf("%s is the builder's own: a build sets it itself, so it can only be available at runtime", n)
	}
	return nil
}

// CheckBuildValue says whether a build may be given v as name's value: one
// line of at most MaxBuildValue bytes. It names the variable, never the
// value.
func CheckBuildValue(name, v string) error {
	if len(v) > MaxBuildValue {
		return fmt.Errorf("%s: a build variable is at most %d bytes", name, MaxBuildValue)
	}
	if strings.ContainsAny(v, "\n\r\x00") {
		return fmt.Errorf("%s: a build variable can't contain a newline, carriage return or NUL", name)
	}
	return nil
}

// BuildBytes is the room vars take in a build's job: their JSON, as the
// job carries them (a quote or a < is longer there than in the value).
func BuildBytes(vars map[string]string) int {
	b, err := json.Marshal(vars)
	if err != nil {
		return 0 // a map of strings always encodes
	}
	n := len(b)
	clear(b)
	return n
}

// CheckBuild says whether a build may be given vars: each name and value,
// at most MaxBuildVars of them, taking at most MaxBuildBytes. It names
// variables and sizes, never values.
func CheckBuild(vars map[string]string) error {
	for _, n := range slices.Sorted(maps.Keys(vars)) {
		if err := CheckBuildName(n); err != nil {
			return err
		}
		if err := CheckBuildValue(n, vars[n]); err != nil {
			return err
		}
	}
	if len(vars) > MaxBuildVars {
		return fmt.Errorf("%d build variables: a build takes at most %d", len(vars), MaxBuildVars)
	}
	if size := BuildBytes(vars); size > MaxBuildBytes {
		return fmt.Errorf("the build variables are too large: %.1f KiB altogether, at most %d KiB - shorten or remove some",
			float64(size)/1024, MaxBuildBytes>>10)
	}
	return nil
}
