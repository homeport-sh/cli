package buildplan_test

import (
	"maps"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/homeport-sh/cli/artifact"
	"github.com/homeport-sh/cli/buildplan"
)

const goMod = "module x\n\ngo 1.26\n"

// a package as it is once its image is published, for both architectures
var published = buildplan.Package{
	Name: "lightpanda", Version: "1.0.0", About: "a headless browser", Provides: []string{"lightpanda"}, License: "AGPL-3.0",
	Images: map[string]string{
		artifact.ArchAMD64: "ghcr.io/homeport-sh/pkg-lightpanda:1.0.0-x86-64@sha256:" + strings.Repeat("a", 64),
		artifact.ArchARM64: "ghcr.io/homeport-sh/pkg-lightpanda:1.0.0-arm64@sha256:" + strings.Repeat("b", 64),
	},
}

// Lightpanda is the first package: published (homeport-sh/homeport
// images/pkg-lightpanda, upstream's exact files), and listed: the
// dashboard's list offers it.
func TestLightpandaIsPublishedAndListed(t *testing.T) {
	i := slices.IndexFunc(buildplan.Packages(), func(p buildplan.Package) bool { return p.Name == "lightpanda" })
	if i < 0 {
		t.Fatalf("lightpanda isn't on the list: %+v", buildplan.Packages())
	}
	p := buildplan.Packages()[i]
	if p.Version != "1.0.0" || !slices.Equal(p.Provides, []string{"lightpanda"}) || p.License != "AGPL-3.0" || !p.Listed || !p.Published() {
		t.Fatalf("%+v", p)
	}
	want := map[string]string{
		artifact.ArchAMD64: "ghcr.io/homeport-sh/pkg-lightpanda:1.0.0-x86-64@sha256:d2a12d302a3719d487a09bc719a9df65b1ee813b66d6f3c59774b02a5208369b",
		artifact.ArchARM64: "ghcr.io/homeport-sh/pkg-lightpanda:1.0.0-arm64@sha256:3d156a0bd32766646073018af3efefdf563b5780e26cd4ebb3783a3cfdfca579",
	}
	if !maps.Equal(p.Images, want) || p.Size[artifact.ArchAMD64] != 188303266 || p.Size[artifact.ArchARM64] != 192792091 {
		t.Fatalf("images %v, sizes %v", p.Images, p.Size)
	}
	if err := (buildplan.Settings{Packages: []string{"lightpanda"}}).Check(); err != nil {
		t.Fatalf("lightpanda, by name: %v", err)
	}
	if !slices.ContainsFunc(buildplan.Listed(), func(p buildplan.Package) bool { return p.Name == "lightpanda" }) {
		t.Fatal("lightpanda isn't in the dashboard's list")
	}
}

// fonts is a package of data alone (DejaVu's core faces and a fontconfig
// config, for libvips, librsvg and pango to draw text): it provides no
// executable, and homeportd points FONTCONFIG_FILE at its config. It's on
// the list, unpublished until its image is (homeport-sh/homeport
// images/pkg-fonts), so it can't be chosen yet.
func TestFontsIsAPackageOfData(t *testing.T) {
	i := slices.IndexFunc(buildplan.Packages(), func(p buildplan.Package) bool { return p.Name == "fonts" })
	if i < 0 {
		t.Fatalf("fonts isn't on the list: %+v", buildplan.Packages())
	}
	p := buildplan.Packages()[i]
	if p.Version != "2.37" || len(p.Provides) != 0 || !p.Data() || p.License != "Bitstream-Vera" || p.Published() {
		t.Fatalf("%+v", p)
	}
	if err := (buildplan.Settings{Packages: []string{"fonts"}}).Check(); err == nil || !strings.Contains(err.Error(), "isn't published yet") {
		t.Fatalf("fonts, unpublished: %v", err)
	}
	if lp := buildplan.Packages()[slices.IndexFunc(buildplan.Packages(), func(p buildplan.Package) bool { return p.Name == "lightpanda" })]; lp.Data() {
		t.Fatal("lightpanda isn't data")
	}
}

// Only a package on homeport's list can be chosen: never any name, never
// one twice, never more than a few.
func TestOnlyCuratedPackagesAreAccepted(t *testing.T) {
	defer buildplan.WithCatalog([]buildplan.Package{published})()
	if err := (buildplan.Settings{Packages: []string{"lightpanda"}}).Check(); err != nil {
		t.Fatalf("a published package was refused: %v", err)
	}
	for name, c := range map[string]struct {
		pkgs []string
		want string
	}{
		"not on the list":     {[]string{"imagemagick"}, "isn't one of homeport's packages"},
		"an apt name":         {[]string{"ffmpeg=7:6.1"}, "isn't one of homeport's packages"},
		"a path":              {[]string{"../lightpanda"}, "isn't one of homeport's packages"},
		"empty":               {[]string{""}, "isn't one of homeport's packages"},
		"twice":               {[]string{"lightpanda", "lightpanda"}, "more than once"},
		"too many":            {slices.Repeat([]string{"x"}, buildplan.MaxPackages+1), "at most"},
		"a control character": {[]string{"lightpanda\n"}, "control characters"},
	} {
		err := buildplan.Settings{Packages: c.pkgs}.Check()
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", name, err, c.want)
		}
	}
}

// The plan names each package with its version and its image for each
// architecture, which the builder unpacks into the release. Settings win
// over homeport.yaml's packages: list, as for processes.
func TestChosenPackagesAreInThePlan(t *testing.T) {
	defer buildplan.WithCatalog([]buildplan.Package{published})()
	want := []buildplan.PlanPackage{{Name: "lightpanda", Version: "1.0.0", Images: published.Images}}

	p := detect(t, map[string]string{"go.mod": goMod}, buildplan.Settings{Packages: []string{"lightpanda"}})
	if !slices.EqualFunc(p.Packages, want, samePkg) {
		t.Fatalf("from settings: %+v", p.Packages)
	}
	p = detect(t, map[string]string{"go.mod": goMod, "homeport.yaml": "packages: [lightpanda]\n"}, buildplan.Settings{})
	if !slices.EqualFunc(p.Packages, want, samePkg) {
		t.Fatalf("from homeport.yaml: %+v", p.Packages)
	}
	if p = detect(t, map[string]string{"go.mod": goMod}, buildplan.Settings{}); len(p.Packages) != 0 {
		t.Fatalf("none chosen: %+v", p.Packages)
	}
	// homeport.yaml is held to the same list
	_, err := buildplan.Detect(repo(map[string]string{"go.mod": goMod, "homeport.yaml": "packages: [chromium]\n"}), buildplan.Settings{})
	if err == nil || !strings.Contains(err.Error(), "isn't one of homeport's packages") {
		t.Fatalf("homeport.yaml's unknown package: %v", err)
	}
	// a JavaScript app's bundle and a PHP app's bundle carry them too
	p = detect(t, map[string]string{"package.json": `{"scripts":{"build":"tsc","start":"node dist/server.js"},"dependencies":{"express":"^5"}}`,
		"bun.lock": bunLock}, buildplan.Settings{Packages: []string{"lightpanda"}})
	if p.Kind != buildplan.Bundle || len(p.Packages) != 1 {
		t.Fatalf("a JavaScript app: %+v", p)
	}
}

// A static site runs nothing, so there's nothing to give a package to.
func TestAStaticSiteHasNoPackages(t *testing.T) {
	defer buildplan.WithCatalog([]buildplan.Package{published})()
	_, err := buildplan.Detect(repo(map[string]string{"index.html": "<h1>hi</h1>"}), buildplan.Settings{Packages: []string{"lightpanda"}})
	if err == nil || !strings.Contains(err.Error(), "static site") {
		t.Fatalf("%v", err)
	}
}

// One deploy's packages, when it sets any, are the whole list; packages
// are a setting like any other.
func TestPackagesAreASetting(t *testing.T) {
	saved := buildplan.Settings{Packages: []string{"a"}}
	if saved.IsZero() {
		t.Fatal("packages are something set")
	}
	if got := saved.With(buildplan.Settings{Run: "serve"}); !slices.Equal(got.Packages, []string{"a"}) {
		t.Fatalf("kept: %+v", got)
	}
	if got := saved.With(buildplan.Settings{Packages: []string{"b"}}); !slices.Equal(got.Packages, []string{"b"}) {
		t.Fatalf("replaced: %+v", got)
	}
}

var (
	pkgNameRe  = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)
	pkgImageRe = regexp.MustCompile(`^ghcr\.io/homeport-sh/pkg-[a-z0-9-]+:[A-Za-z0-9._-]+@sha256:[0-9a-f]{64}$`)
)

// The list itself: plain names, a version, what each puts on PATH (no two
// the same; nothing for a package of data), and images only from
// homeport's own registry, pinned by digest, for both architectures or
// neither.
func TestTheCatalogIsWellFormed(t *testing.T) {
	seen, provided := map[string]bool{}, map[string]string{}
	for _, p := range buildplan.Packages() {
		if !pkgNameRe.MatchString(p.Name) || seen[p.Name] || p.Version == "" || p.About == "" || p.License == "" {
			t.Errorf("%+v", p)
		}
		seen[p.Name] = true
		for _, x := range p.Provides {
			if !pkgNameRe.MatchString(x) || provided[x] != "" {
				t.Errorf("%s provides %q (also %q's)", p.Name, x, provided[x])
			}
			provided[x] = p.Name
		}
		if len(p.Images) != 0 && len(p.Images) != 2 {
			t.Errorf("%s: images for both architectures or neither: %v", p.Name, p.Images)
		}
		for arch, ref := range p.Images {
			if (arch != artifact.ArchAMD64 && arch != artifact.ArchARM64) || !pkgImageRe.MatchString(ref) ||
				!strings.HasPrefix(ref, "ghcr.io/homeport-sh/pkg-"+p.Name+":"+p.Version+"-"+arch+"@") {
				t.Errorf("%s: %s %s", p.Name, arch, ref)
			}
		}
	}
	// the test's own published entry passes the same rules
	for arch, ref := range published.Images {
		if !pkgImageRe.MatchString(ref) {
			t.Errorf("test entry %s %s", arch, ref)
		}
	}
}

func samePkg(a, b buildplan.PlanPackage) bool {
	return a.Name == b.Name && a.Version == b.Version && len(a.Images) == len(b.Images) &&
		a.Images[artifact.ArchAMD64] == b.Images[artifact.ArchAMD64] && a.Images[artifact.ArchARM64] == b.Images[artifact.ArchARM64]
}

// An unlisted package can be chosen by name, but the dashboard's list
// doesn't show it. Listed is what the dropdown shows: published and listed.
func TestAnUnlistedPackageIsChosenByNameOnly(t *testing.T) {
	unlisted, listed := published, published
	listed.Name, listed.Provides, listed.Listed = "ffmpeg", []string{"ffmpeg"}, true
	listed.Images = map[string]string{
		artifact.ArchAMD64: "ghcr.io/homeport-sh/pkg-ffmpeg:1.0.0-x86-64@sha256:" + strings.Repeat("c", 64),
		artifact.ArchARM64: "ghcr.io/homeport-sh/pkg-ffmpeg:1.0.0-arm64@sha256:" + strings.Repeat("d", 64),
	}
	notYet := listed
	notYet.Name, notYet.Provides, notYet.Images = "pandoc", []string{"pandoc"}, nil
	defer buildplan.WithCatalog([]buildplan.Package{unlisted, listed, notYet})()
	if err := (buildplan.Settings{Packages: []string{"lightpanda", "ffmpeg"}}).Check(); err != nil {
		t.Fatalf("an unlisted package, by name: %v", err)
	}
	got := buildplan.Listed()
	if len(got) != 1 || got[0].Name != "ffmpeg" {
		t.Fatalf("the dropdown shows %+v, want ffmpeg alone", got)
	}
}
