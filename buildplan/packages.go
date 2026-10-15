package buildplan

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
)

// System packages: executables an app runs beside its own (a headless
// browser, ffmpeg) from a short list homeport curates and patches. Each is
// an image of homeport's own (ghcr.io/homeport-sh/pkg-<name>), built from a
// recipe and pinned here by digest per architecture. The builder unpacks the
// chosen ones into the release, at PackagesDir/<name>/, so the release
// carries them: nothing is installed on a host. homeportd puts each
// PackagesDir/<name>/bin on the sandbox's PATH. Design: design/packages.md
// (homeport-sh/cloud).
//
// An image's root is the package's folder: bin/ with its executables (the
// ones in Provides) and whatever else they read, beside them or under it -
// libraries found through RPATH=$ORIGIN, never through LD_LIBRARY_PATH,
// which the app's own executables would read too. No symlinks: a release
// is files.

// PackagesDir is where a release's packages are, in its folder.
const PackagesDir = ".homeport/pkgs"

// MaxPackages is how many packages an app may choose.
const MaxPackages = 8

// Package is one package on homeport's list.
type Package struct {
	Name     string   `json:"name"`     // what a person chooses: lightpanda
	Version  string   `json:"version"`  // upstream's
	About    string   `json:"about"`    // one line, for the list in build settings
	Provides []string `json:"provides"` // its executables, in its bin/
	License  string   `json:"license"`  // SPDX, of what ships
	// Images: per architecture (artifact.ArchAMD64, artifact.ArchARM64),
	// its image, pinned by digest; none until it's published
	Images map[string]string `json:"images,omitempty"`
	// Size: what it adds to a release, unpacked, in bytes, per architecture
	Size map[string]int64 `json:"size,omitempty"`
}

// Published says whether it can be chosen: its image is there for every
// architecture homeport runs.
func (p Package) Published() bool { return len(p.Images) == 2 }

// PlanPackage is a package as a plan names it: what the builder unpacks.
type PlanPackage struct {
	Name    string            `json:"name"`
	Version string            `json:"version"`
	Images  map[string]string `json:"images"`
}

// catalog is the list. Lightpanda 1.0.0 (github.com/lightpanda-io/browser,
// tag 1.0.0 at 588f6223): a headless browser speaking CDP, which an app
// starts as `lightpanda serve --host 127.0.0.1 --port <p>` and drives with
// puppeteer-core or playwright over localhost. Upstream's linux binaries,
// linked against glibc only (libc, libm, ld.so; at most GLIBC_2.38 on
// arm64, 2.35 on x86-64), so the host's /lib is all they need.
var catalog = []Package{
	{
		Name: "lightpanda", Version: "1.0.0", License: "AGPL-3.0",
		About:    "Lightpanda, a headless browser for scraping and automation (Chrome DevTools Protocol)",
		Provides: []string{"lightpanda"},
	},
}

// Packages is the list, for showing.
func Packages() []Package {
	out := make([]Package, len(catalog))
	for i, p := range catalog {
		p.Provides, p.Images, p.Size = slices.Clone(p.Provides), maps.Clone(p.Images), maps.Clone(p.Size)
		out[i] = p
	}
	return out
}

// CheckPackages says whether names are packages an app may choose: each on
// the list and published, none twice, at most MaxPackages.
func CheckPackages(names []string) error {
	if len(names) > MaxPackages {
		return fmt.Errorf("packages: at most %d", MaxPackages)
	}
	seen := map[string]bool{}
	for _, n := range names {
		if err := checkText("package", n); err != nil {
			return err
		}
		if seen[n] {
			return fmt.Errorf("package %q is chosen more than once", n)
		}
		seen[n] = true
		p, ok := lookupPackage(n)
		if !ok {
			return fmt.Errorf("package %q isn't one of homeport's packages (%s)", n, strings.Join(packageNames(), ", "))
		}
		if !p.Published() {
			return fmt.Errorf("package %q isn't published yet: it can't be chosen until its image is", n)
		}
	}
	return nil
}

func lookupPackage(name string) (Package, bool) {
	i := slices.IndexFunc(catalog, func(p Package) bool { return p.Name == name })
	if i < 0 {
		return Package{}, false
	}
	return catalog[i], true
}

func packageNames() []string {
	out := make([]string, len(catalog))
	for i, p := range catalog {
		out[i] = p.Name
	}
	return out
}

// planPackages is what a plan says of the chosen packages, by name.
func planPackages(names []string) []PlanPackage {
	var out []PlanPackage
	for _, n := range slices.Sorted(slices.Values(names)) {
		p, _ := lookupPackage(n)
		out = append(out, PlanPackage{Name: p.Name, Version: p.Version, Images: maps.Clone(p.Images)})
	}
	return out
}

// errStaticPackages: a site is served as files, and nothing of it runs.
var errStaticPackages = errors.New("packages are for an app that runs: a static site runs nothing")
