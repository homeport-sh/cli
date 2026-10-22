package buildplan

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/homeport-sh/cli/artifact"
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
// which the app's own executables would read too. A package of data alone
// (fonts) has no bin/ and provides nothing: its share/ is what the app's
// own libraries read, found through an environment homeportd sets for it.
// No symlinks: a release is files.

// PackagesDir is where a release's packages are, in its folder.
const PackagesDir = ".homeport/pkgs"

// MaxPackages is how many packages an app may choose.
const MaxPackages = 8

// Package is one package on homeport's list.
type Package struct {
	Name     string   `json:"name"`     // what a person chooses: lightpanda
	Version  string   `json:"version"`  // upstream's
	About    string   `json:"about"`    // one line, for the list in build settings
	Provides []string `json:"provides"` // its executables, in its bin/; none for data
	License  string   `json:"license"`  // SPDX, of what ships
	// Listed: shown in the dashboard's list (Listed). An unlisted package
	// can still be chosen by name, in build settings or homeport.yaml.
	Listed bool `json:"listed"`
	// Images: per architecture (artifact.ArchAMD64, artifact.ArchARM64),
	// its image, ghcr.io/homeport-sh/pkg-<name>:<version>-<arch>, pinned
	// by digest; none until it's published
	Images map[string]string `json:"images,omitempty"`
	// Size: what it adds to a release, unpacked, in bytes, per architecture
	Size map[string]int64 `json:"size,omitempty"`
}

// Published says whether it can be chosen: its image is there for every
// architecture homeport runs.
func (p Package) Published() bool { return len(p.Images) == 2 }

// Data says it's a package of data alone: no executables, nothing on PATH
// (fonts). A builder and host need a newer homeportd for one (the control
// plane gates it).
func (p Package) Data() bool { return len(p.Provides) == 0 }

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
// arm64, 2.35 on x86-64), so the host's /lib is all they need. Its image
// is upstream's exact files (homeport-sh/homeport images/pkg-lightpanda).
// Listed: its licence (AGPL-3.0) allows redistributing upstream's
// unmodified binaries.
var catalog = []Package{
	{
		Name: "lightpanda", Version: "1.0.0", License: "AGPL-3.0", Listed: true,
		About:    "Lightpanda, a headless browser for scraping and automation (Chrome DevTools Protocol)",
		Provides: []string{"lightpanda"},
		// published 2026-10-15 from images/pkg-lightpanda (homeport-sh/homeport)
		Images: map[string]string{
			artifact.ArchAMD64: "ghcr.io/homeport-sh/pkg-lightpanda:1.0.0-x86-64@sha256:d2a12d302a3719d487a09bc719a9df65b1ee813b66d6f3c59774b02a5208369b",
			artifact.ArchARM64: "ghcr.io/homeport-sh/pkg-lightpanda:1.0.0-arm64@sha256:3d156a0bd32766646073018af3efefdf563b5780e26cd4ebb3783a3cfdfca579",
		},
		Size: map[string]int64{artifact.ArchAMD64: 188303266, artifact.ArchARM64: 192792091},
	},
	// fonts: DejaVu 2.37's core faces (Sans, Sans Mono and Serif, each
	// regular and bold: Debian's fonts-dejavu-core) and a fontconfig config
	// naming them (share/fonts.conf), so libvips, librsvg and pango - sharp's
	// text and SVG - draw text in a sandbox that has no fonts. homeportd sets
	// FONTCONFIG_FILE to the config. Upstream's exact files, out of its
	// release archive by sha256 (homeport-sh/homeport images/pkg-fonts).
	// Unlisted until its licence (Bitstream Vera's, DejaVu's changes public
	// domain) is confirmed.
	{
		Name: "fonts", Version: "2.37", License: "Bitstream-Vera",
		About: "Fonts (DejaVu Sans, Sans Mono and Serif) and fontconfig, for drawing text: sharp, libvips, librsvg",
		Images: map[string]string{
			artifact.ArchAMD64: "ghcr.io/homeport-sh/pkg-fonts:2.37-x86-64@sha256:abdcd27505de7ce30de8ec000980886e1fa66525458046a3c782040cdebf7f1d",
			artifact.ArchARM64: "ghcr.io/homeport-sh/pkg-fonts:2.37-arm64@sha256:35c515bcda6846ebfdbff0987544b5e2166efcb474e2e33acdb0000e69c7fc21",
		},
		Size: map[string]int64{artifact.ArchAMD64: 2881840, artifact.ArchARM64: 2881840},
	},
}

// Listed is what the dashboard shows a person to choose from: the
// published, listed packages.
func Listed() []Package {
	var out []Package
	for _, p := range Packages() {
		if p.Listed && p.Published() {
			out = append(out, p)
		}
	}
	return out
}

// Packages is the whole list, listed or not.
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
