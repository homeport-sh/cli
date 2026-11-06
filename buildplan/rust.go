package buildplan

import (
	"errors"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"
)

// A Rust app - a crate with a binary, and its Cargo.lock - is built with
// cargo in the official Rust image, and ships as a bundle: its binary as
// bin, beside the app's own files (static/, templates/, config/), run from
// that folder, so whatever it reads relative to it is there. The binary
// links the boxes' glibc (2.39, Ubuntu 24.04), which the sandbox lends it
// read-only with their other libraries (libssl, libgcc_s): the image is
// Debian bookworm's, glibc 2.36, never newer. Like a Go app's, it must
// listen on $PORT (Rocket's default environment says it: RocketEnv).
//
// Its compiled dependencies are kept with its other dependencies, in the
// builder's cache ($CARGO_HOME, which the builder sets): a build whose
// Cargo.lock didn't change compiles the app's own crates alone.

// RustVersion is the Rust a project that names none builds with, and
// RustImage its official image (bookworm), pinned by digest: a tag can be
// pushed again. A version the project names (rust-toolchain.toml, a
// rust-version newer than this) builds in that version's image.
const (
	RustVersion = "1.99.1"
	RustImage   = "rust:" + RustVersion + "-bookworm@sha256:7085845219c18e7c5b1f34ed9ce89b84f822f2e3d54a02774cedc996ac0f304f"
)

// rustOldest is the oldest Rust with a bookworm image.
const rustOldest = "1.67.0"

// rustTargetMB: past this many MB, the kept compiled dependencies are
// dropped before the build (one that changed its Cargo.lock or its Rust
// leaves the old ones), so the cache stays under what's saved (1 GiB
// packed; they pack about 3 to 1).
const rustTargetMB = 2048

// rustJobs: the build's share of a builder's cores (homeportd's BuildCPU,
// 200%); more rustc at once only adds to its memory.
const rustJobs = 2

// LocoRun is how a Loco app's CLI serves it: on homeport's address and
// port, in production (its config/production.yaml).
const LocoRun = "start --environment production --binding $HOST --port $PORT"

// RocketEnv: Rocket listens on ROCKET_ADDRESS and ROCKET_PORT
// (127.0.0.1:8000), not on HOST and PORT - by default, on homeport's.
var RocketEnv = map[string]string{"ROCKET_ADDRESS": "$HOST", "ROCKET_PORT": "$PORT"}

var (
	crateNameRe   = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_-]{0,63}$`)
	rustChannelRe = regexp.MustCompile(`^1\.[0-9]{1,3}(\.[0-9]{1,3})?$`)
	rustPreRe     = regexp.MustCompile(`^(nightly|beta)(-[0-9]{4}-[0-9]{2}-[0-9]{2})?$`)
	rustDepRe     = regexp.MustCompile(`(?m)^\s*([A-Za-z0-9_-]+)\s*(\.\s*workspace\s*)?=`)
	rustDepHeadRe = regexp.MustCompile(`(?m)^\s*\[dependencies\.([A-Za-z0-9_-]+)\]`)
)

// rustFrameworks: what a dependency says the app is, in the order tried
// (Loco is built on Axum).
var rustFrameworks = []struct{ dep, name string }{
	{"loco-rs", "Loco"}, {"rocket", "Rocket"}, {"actix-web", "Actix Web"}, {"axum", "Axum"},
	{"poem", "Poem"}, {"salvo", "Salvo"}, {"warp", "Warp"},
}

// rustToolchainFiles: rustup's, in the order it reads them in a folder.
var rustToolchainFiles = []string{"rust-toolchain", "rust-toolchain.toml"}

// rustFiles are the files Rust detection reads in the app's folder (Files);
// Cargo.toml, Cargo.lock and the toolchain files above it too
// (WorkspaceFiles).
func rustFiles() []string {
	return slices.Concat([]string{"Cargo.toml", "Cargo.lock", "src/main.rs"}, rustToolchainFiles)
}

// rustApp says whether the app's folder is a crate with a binary.
func (r reader) rustApp() bool {
	b, err := r.read("Cargo.toml")
	return err == nil && tomlHas(b, "[package]") && (tomlHas(b, "[[bin]]") || r.exists("src/main.rs"))
}

// cargoWorkspace is the folder of the Cargo workspace the app's crate is
// in, when it's a member ("" when it's none): the nearest folder above
// with a Cargo.toml that has a [workspace].
func (r reader) cargoWorkspace() (string, []byte) {
	for d := r.root; d != "."; {
		d = path.Dir(d)
		if b, err := (reader{fsys: r.fsys, root: d}).read("Cargo.toml"); err == nil && tomlHas(b, "[workspace]") {
			return d, b
		}
	}
	return "", nil
}

// rustDeps are Cargo.toml's [dependencies], by name.
func rustDeps(cargo []byte) map[string]bool {
	out := map[string]bool{}
	for _, m := range rustDepRe.FindAllStringSubmatch(tomlSection(cargo, "[dependencies]"), -1) {
		out[m[1]] = true
	}
	for _, m := range rustDepHeadRe.FindAllStringSubmatch(string(cargo), -1) {
		out[m[1]] = true
	}
	return out
}

// rustBin is the binary the app runs: default-run; else its only one;
// else a Loco app's CLI (<package>-cli); else the package's own (src/main.rs,
// or a [[bin]] named as the package).
func rustBin(cargo []byte, hasMain, loco bool) (string, error) {
	pkg := tomlSection(cargo, "[package]")
	name, _ := tomlString(pkg, "name")
	var bins []string
	main := hasMain
	for _, s := range tomlSections(cargo, "[[bin]]") {
		n, _ := tomlString(s, "name")
		if p, _ := tomlString(s, "path"); p == "src/main.rs" || n == name {
			main = false // src/main.rs is this one
		}
		if n != "" && !slices.Contains(bins, n) {
			bins = append(bins, n)
		}
	}
	if main && name != "" {
		bins = append(bins, name) // Cargo's autobins: src/main.rs, named as the package
	}
	bin, _ := tomlString(pkg, "default-run")
	switch {
	case bin != "":
	case len(bins) == 1:
		bin = bins[0]
	case loco && slices.Contains(bins, name+"-cli"):
		bin = name + "-cli"
	case slices.Contains(bins, name):
		bin = name
	case len(bins) == 0:
		return "", errors.New("Cargo.toml names no package or binary")
	default:
		return "", fmt.Errorf("Cargo.toml has several binaries (%s): set default-run in its [package] to the one that serves the app", strings.Join(bins, ", "))
	}
	if !crateNameRe.MatchString(bin) {
		return "", fmt.Errorf("Cargo.toml's binary %q isn't a crate name", bin)
	}
	return bin, nil
}

// rust: the Rust the app builds with - its image, whether the
// build uses the image's own toolchain (RUSTUP_TOOLCHAIN, over a toolchain
// file that names it otherwise, like 1.85 for 1.85.1) - and what a person
// should know. rustup reads rust-toolchain(.toml) in the app's folder or
// the nearest above it; with none, the pinned Rust, or the crate's
// rust-version (its workspace's) when that's newer.
func (r reader) rust(cargo, ws []byte) (image string, own bool, warning string, err error) {
	for d := r.root; ; d = path.Dir(d) {
		dr := reader{fsys: r.fsys, root: d}
		for _, f := range rustToolchainFiles {
			b, err := dr.read(f)
			if err != nil {
				continue
			}
			channel, ok := tomlString(tomlSection(b, "[toolchain]"), "channel")
			if !ok && f == "rust-toolchain" && !strings.Contains(string(b), "[toolchain]") {
				channel, _, _ = strings.Cut(strings.TrimSpace(string(b)), "\n")
				channel = strings.TrimSpace(channel)
			}
			at := path.Join(d, f)
			switch {
			case channel == "" || channel == "stable":
				return RustImage, true, "", nil
			case rustChannelRe.MatchString(channel):
				v, _, _ := partial(channel)
				if v.cmp(mustVersion(rustOldest)) < 0 {
					return "", false, "", fmt.Errorf("%s: Rust %s is older than any Rust homeport builds with (%s and newer)", at, channel, rustOldest)
				}
				return "rust:" + channel + "-bookworm", true, "", nil
			case rustPreRe.MatchString(channel):
				return RustImage, false, at + " asks for Rust " + channel + ": rustup downloads it in every build (the image has Rust " + RustVersion + ")", nil
			}
			return "", false, "", fmt.Errorf("%s: channel %q isn't a Rust version homeport builds with (stable, 1.x, 1.x.y, nightly or beta)", at, channel)
		}
		if d == "." {
			break
		}
	}
	v, ok := tomlString(tomlSection(cargo, "[package]"), "rust-version")
	if !ok {
		v, ok = tomlString(tomlSection(ws, "[workspace.package]"), "rust-version")
	}
	if ok && rustChannelRe.MatchString(v) {
		if s, _, _ := partial(v); s.cmp(mustVersion(RustVersion)) > 0 {
			return "rust:" + v + "-bookworm", true, "", nil
		}
	}
	return RustImage, true, "", nil
}

// rustSetup: where cargo keeps what it compiled (in the dependency cache,
// dropped past rustTargetMB), its jobs, and the image's own toolchain.
// Exported, so a build command someone sets builds there too.
func rustSetup(own bool) string {
	env := `export CARGO_TARGET_DIR="${CARGO_HOME:-$HOME/.cargo}/homeport-target" CARGO_BUILD_JOBS=` + fmt.Sprint(rustJobs)
	if own {
		env += ` RUSTUP_TOOLCHAIN="$RUST_VERSION"`
	}
	return join(env, fmt.Sprintf(`{ [ ! -d "$CARGO_TARGET_DIR" ] || [ "$(du -sm "$CARGO_TARGET_DIR" | cut -f1)" -lt %d ] || rm -rf "$CARGO_TARGET_DIR"; }`, rustTargetMB))
}

// rs plans a Rust app (rustApp) in p.
func (r reader) rs(p *Plan, cfg fileConfig) error {
	cargo, err := r.read("Cargo.toml")
	if err != nil {
		return err
	}
	wsDir, ws := r.cargoWorkspace()
	if !r.exists("Cargo.lock") && (wsDir == "" || !(reader{fsys: r.fsys, root: wsDir}).exists("Cargo.lock")) {
		return errors.New("Cargo.toml without Cargo.lock: commit the lockfile (cargo generate-lockfile), so the build installs what you tested")
	}
	deps := rustDeps(cargo)
	p.Toolchain, p.Framework = "rust", "Rust"
	for _, f := range rustFrameworks {
		if deps[f.dep] {
			p.Framework = f.name
			break
		}
	}
	bin, err := rustBin(cargo, r.exists("src/main.rs"), p.Framework == "Loco")
	if err != nil {
		return err
	}
	image, own, warning, err := r.rust(cargo, ws)
	if err != nil {
		return err
	}
	p.Image = image
	if warning != "" {
		p.Warnings = append(p.Warnings, warning)
	}
	switch p.Framework {
	case "Loco":
		p.Run = LocoRun
	case "Rocket":
		p.Env = RocketEnv
	}
	built := `"$CARGO_TARGET_DIR/release/` + bin + `"`
	p.setup = rustSetup(own)
	p.Install = p.setup
	p.build = cmpOr(cfg.Build.Command, "cargo build --release --locked --bin "+bin+" && cp "+built+" server")
	if cfg.Build.Artifact != "" || cfg.Static != "" {
		// homeport.yaml says what the build makes
		p.Command = p.build
		return nil
	}
	b := BundleDir
	p.Kind, p.Artifact, p.StaticFallback = Bundle, b, false
	p.assemble = join(noBinFolder("Rust", "binary"),
		"rm -rf "+b+" && mkdir -p "+b+"/.homeport",
		copyTree("r=$(pwd -P)", ".", b, "./.git", "./"+b, "./target", "./server"),
		"cp "+built+" "+b+"/bin")
	p.Command = join(p.build, p.assemble)
	return nil
}

// rustRelease: a Loco app's migrations, when it has its migration crate.
func (r reader) rustRelease(p *Plan) (release, why, warning string) {
	b, _ := r.read("Cargo.toml")
	if p.Framework == "Loco" && rustDeps(b)["migration"] {
		return "db migrate --environment production", "a Loco app with migrations: its CLI's db migrate", ""
	}
	return "", "", ""
}

// rustNotAnApp: why a folder with a Cargo.toml that isn't a crate with a
// binary can't be built ("" when there's no Cargo.toml).
func (r reader) rustNotAnApp() string {
	b, err := r.read("Cargo.toml")
	if err != nil {
		return ""
	}
	if tomlHas(b, "[workspace]") && !tomlHas(b, "[package]") {
		members := tomlStrings(tomlSection(b, "[workspace]"), "members")
		return "this folder is a Cargo workspace, not a crate: set the root directory to its member that's the app (" + strings.Join(members, ", ") + ")"
	}
	return "Cargo.toml is a library here (no src/main.rs or [[bin]]): set the root directory to the crate that's the app"
}
