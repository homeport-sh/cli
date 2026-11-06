package buildplan_test

import (
	"maps"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"

	"github.com/homeport-sh/cli/buildplan"
)

const axumToml = `[package]
name = "hello"
version = "0.1.0"
edition = "2024"

[dependencies]
axum = "0.8"
tokio = { version = "1", features = ["rt-multi-thread", "macros", "net"] }
`

func rustApp(toml string, more ...string) map[string]string {
	files := map[string]string{"Cargo.toml": toml, "Cargo.lock": "version = 4\n", "src/main.rs": "fn main() {}\n"}
	for i := 0; i+1 < len(more); i += 2 {
		files[more[i]] = more[i+1]
	}
	return files
}

// The smallest Axum app - Cargo.toml, Cargo.lock, src/main.rs - and the
// whole plan it gets: built with cargo in the pinned official image (glibc
// no newer than the boxes'), its compiled dependencies kept in the
// dependency cache, and shipped as a bundle - the binary as bin, beside
// the app's own files (static/, templates/), run from that folder.
func TestAMinimalAxumAppsPlan(t *testing.T) {
	p := detect(t, rustApp(axumToml), buildplan.Settings{})
	setup := `export CARGO_TARGET_DIR="${CARGO_HOME:-$HOME/.cargo}/homeport-target" CARGO_BUILD_JOBS=2 RUSTUP_TOOLCHAIN="$RUST_VERSION"` +
		` && { [ ! -d "$CARGO_TARGET_DIR" ] || [ "$(du -sm "$CARGO_TARGET_DIR" | cut -f1)" -lt 2048 ] || rm -rf "$CARGO_TARGET_DIR"; }`
	build := `cargo build --release --locked --bin hello && cp "$CARGO_TARGET_DIR/release/hello" server`
	if p.Kind != buildplan.Bundle || p.Toolchain != "rust" || p.Framework != "Axum" || p.Artifact != ".homeport-bundle" || p.StaticFallback {
		t.Fatalf("%+v", p)
	}
	if p.Image != "rust:1.99.1-bookworm@sha256:7085845219c18e7c5b1f34ed9ce89b84f822f2e3d54a02774cedc996ac0f304f" {
		t.Errorf("image %q", p.Image)
	}
	if p.Install != setup {
		t.Errorf("install:\n got %s\nwant %s", p.Install, setup)
	}
	if p.Build != build || !strings.HasPrefix(p.Command, build+" && ") {
		t.Errorf("build %q, command %q", p.Build, p.Command)
	}
	for _, want := range []string{
		"rm -rf .homeport-bundle && mkdir -p .homeport-bundle/.homeport",
		"--exclude=./.git --exclude=./.homeport-bundle --exclude=./target --exclude=./server",
		`cp "$CARGO_TARGET_DIR/release/hello" .homeport-bundle/bin`,
	} {
		if !strings.Contains(p.Command, want) {
			t.Errorf("command lacks %q:\n%s", want, p.Command)
		}
	}
	if p.Run != "" || p.Release.String() != "" || p.Processes != nil || p.Queue != nil {
		t.Errorf("runs: %+v", p)
	}
}

// Which binary: default-run, else the one [[bin]], else the one named as
// the package; several, none of them the package's, is for a person to
// say. A crate name is the only thing of Cargo.toml's in the command.
func TestARustAppsBinary(t *testing.T) {
	bin := func(p buildplan.Plan) string {
		_, rest, _ := strings.Cut(p.Build, "--bin ")
		name, _, _ := strings.Cut(rest, " ")
		return name
	}
	for toml, want := range map[string]string{
		axumToml: "hello",
		"[package]\nname = \"web\"\ndefault-run = \"serve\"\n\n[[bin]]\nname = \"serve\"\n\n[[bin]]\nname = \"seed\"\n": "serve",
		"[package]\nname = \"web\"\n\n[[bin]]\nname = \"api-server\"\npath = \"src/main.rs\"\n":                         "api-server",
		"[package]\nname = \"web\"\n\n[[bin]]\nname = \"web\"\n\n[[bin]]\nname = \"tool\"\n":                            "web",
		"[package]\nname = \"web\"\n\n[[bin]]\nname = \"migrate\"\npath = \"src/bin/migrate.rs\"\n":                     "web",
	} {
		if got := bin(detect(t, rustApp(toml), buildplan.Settings{})); got != want {
			t.Errorf("%s\n: binary %q, want %q", toml, got, want)
		}
	}
	// src/main.rs is the package's binary too (Cargo's autobins), so not here
	several := rustApp("[package]\nname = \"web\"\n\n[[bin]]\nname = \"a\"\n\n[[bin]]\nname = \"b\"\n")
	delete(several, "src/main.rs")
	_, err := buildplan.Detect(repo(several), buildplan.Settings{})
	if err == nil || !strings.Contains(err.Error(), "default-run") {
		t.Errorf("several binaries: %v", err)
	}
	_, err = buildplan.Detect(repo(rustApp("[package]\nname = \"web\"\ndefault-run = \"x; rm -rf /\"\n")), buildplan.Settings{})
	if err == nil {
		t.Error("a binary name that isn't a crate name")
	}
}

// The lockfile is the build: no Cargo.lock, no build. A library crate (no
// binary) isn't an app.
func TestARustAppNeedsItsLockfileAndABinary(t *testing.T) {
	files := rustApp(axumToml)
	delete(files, "Cargo.lock")
	if _, err := buildplan.Detect(repo(files), buildplan.Settings{}); err == nil || !strings.Contains(err.Error(), "Cargo.lock") {
		t.Errorf("no lockfile: %v", err)
	}
	lib := map[string]string{"Cargo.toml": "[package]\nname = \"lib\"\n", "Cargo.lock": "", "src/lib.rs": ""}
	if _, err := buildplan.Detect(repo(lib), buildplan.Settings{}); err == nil {
		t.Error("a library was planned as an app")
	}
	// ...but beside a JavaScript app (napi-rs), it's that app
	lib["package.json"], lib["bun.lock"] = `{"scripts":{"build":"vite build"},"devDependencies":{"vite":"^7"}}`, bunLock
	if p := detect(t, lib, buildplan.Settings{}); p.Toolchain != "bun" {
		t.Errorf("napi-rs: %+v", p)
	}
}

// The Rust it builds with: rust-toolchain.toml's (or rust-toolchain's)
// version, in the official image of that version; stable or none, the
// pinned one; a rust-version newer than the pinned one, that version. The
// image's own toolchain is used (RUSTUP_TOOLCHAIN), so nothing is
// downloaded; a nightly or beta channel is rustup's to fetch.
func TestTheRustItBuildsWith(t *testing.T) {
	for name, c := range map[string]struct {
		files    []string
		image    string
		override bool
	}{
		"toolchain file":        {[]string{"rust-toolchain.toml", "[toolchain]\nchannel = \"1.85.0\"\ncomponents = [\"clippy\"]\n"}, "rust:1.85.0-bookworm", true},
		"minor only":            {[]string{"rust-toolchain.toml", "[toolchain]\nchannel = \"1.88\"\n"}, "rust:1.88-bookworm", true},
		"legacy file":           {[]string{"rust-toolchain", "1.90.0\n"}, "rust:1.90.0-bookworm", true},
		"legacy toml":           {[]string{"rust-toolchain", "[toolchain]\nchannel = \"1.90\"\n"}, "rust:1.90-bookworm", true},
		"stable":                {[]string{"rust-toolchain.toml", "[toolchain]\nchannel = \"stable\"\n"}, buildplan.RustImage, true},
		"nightly":               {[]string{"rust-toolchain.toml", "[toolchain]\nchannel = \"nightly-2026-10-01\"\n"}, buildplan.RustImage, false},
		"rust-version older":    {nil, buildplan.RustImage, true},
		"rust-version is newer": {[]string{"Cargo.toml", strings.Replace(axumToml, "edition = \"2024\"\n", "edition = \"2024\"\nrust-version = \"1.101\"\n", 1)}, "rust:1.101-bookworm", true},
	} {
		files := rustApp(strings.Replace(axumToml, "edition = \"2024\"\n", "edition = \"2024\"\nrust-version = \"1.80\"\n", 1), c.files...)
		p := detect(t, files, buildplan.Settings{})
		if p.Image != c.image || strings.Contains(p.Install, "RUSTUP_TOOLCHAIN") != c.override {
			t.Errorf("%s: image %q, install %q", name, p.Image, p.Install)
		}
		if !c.override && len(p.Warnings) == 0 {
			t.Errorf("%s: no warning that the toolchain is downloaded", name)
		}
	}
	for _, bad := range []string{"1.60.0", "1.2.3.4", "1.85; rm -rf /"} {
		files := rustApp(axumToml, "rust-toolchain.toml", "[toolchain]\nchannel = \""+bad+"\"\n")
		if _, err := buildplan.Detect(repo(files), buildplan.Settings{}); err == nil {
			t.Errorf("channel %q accepted", bad)
		}
	}
}

// A crate in a Cargo workspace (the lockfile and rust-toolchain.toml at
// the workspace's root): built in its own folder, which is what ships. A
// workspace's root is no app: its members are, by root directory. The
// control plane fetches the workspace's files above the app's folder.
func TestARustCrateInAWorkspace(t *testing.T) {
	files := map[string]string{
		"Cargo.toml":          "[workspace]\nmembers = [\"crates/api\", \"crates/core\"]\nresolver = \"3\"\n\n[workspace.package]\nrust-version = \"1.101\"\n",
		"Cargo.lock":          "version = 4\n",
		"rust-toolchain.toml": "[toolchain]\nchannel = \"1.98.0\"\n",
		"crates/api/Cargo.toml": "[package]\nname = \"api\"\nrust-version.workspace = true\n\n[dependencies]\n" +
			"actix-web = { workspace = true }\n",
		"crates/api/src/main.rs":  "fn main() {}\n",
		"crates/core/Cargo.toml":  "[package]\nname = \"core\"\n",
		"crates/core/src/lib.rs":  "",
		"crates/api/static/a.css": "",
	}
	p := detect(t, files, buildplan.Settings{Root: "crates/api"})
	if p.Toolchain != "rust" || p.Framework != "Actix Web" || p.Root != "crates/api" || p.Image != "rust:1.98.0-bookworm" || !strings.Contains(p.Build, "--bin api") {
		t.Fatalf("%+v", p)
	}
	// no toolchain file: the workspace's rust-version
	delete(files, "rust-toolchain.toml")
	if p = detect(t, files, buildplan.Settings{Root: "crates/api"}); p.Image != "rust:1.101-bookworm" {
		t.Fatalf("workspace rust-version: %+v", p)
	}
	_, err := buildplan.Detect(repo(files), buildplan.Settings{})
	if err == nil || !strings.Contains(err.Error(), "crates/api") || !strings.Contains(err.Error(), "root directory") {
		t.Fatalf("the workspace's root: %v", err)
	}
	for _, f := range []string{"Cargo.toml", "Cargo.lock", "rust-toolchain.toml", "rust-toolchain"} {
		if !slices.Contains(buildplan.WorkspaceFiles(), f) || !slices.Contains(buildplan.Files(), f) {
			t.Errorf("%s isn't fetched", f)
		}
	}
	if !slices.Contains(buildplan.Files(), "src/main.rs") {
		t.Error("src/main.rs isn't fetched")
	}
}

// A Go program's folder that is a Rust crate is Rust's, whatever module is
// above it.
func TestARustCrateInAGoRepository(t *testing.T) {
	files := rustApp(axumToml)
	moved := map[string]string{"go.mod": "module m\n\ngo 1.26\n"}
	for k, v := range files {
		moved["svc/"+k] = v
	}
	if p := detect(t, moved, buildplan.Settings{Root: "svc"}); p.Toolchain != "rust" {
		t.Fatalf("%+v", p)
	}
}

// Loco runs as its CLI's start, on homeport's address and port, in
// production; its migrations, when it has them, are the release command.
// Rocket reads ROCKET_PORT, not PORT: said.
func TestRustFrameworks(t *testing.T) {
	loco := "[package]\nname = \"shop\"\ndefault-run = \"shop-cli\"\n\n[dependencies]\nloco-rs = { workspace = true }\n" +
		"migration = { path = \"migration\" }\naxum = \"0.8\"\n\n[[bin]]\nname = \"shop-cli\"\npath = \"src/bin/main.rs\"\n\n[[bin]]\nname = \"tool\"\npath = \"src/bin/tool.rs\"\n"
	p := detect(t, rustApp(loco), buildplan.Settings{})
	if p.Framework != "Loco" || !strings.Contains(p.Build, "--bin shop-cli") || p.Run != "start --environment production --binding $HOST --port $PORT" ||
		p.Release.String() != "db migrate --environment production" {
		t.Fatalf("loco: %+v", p)
	}
	if err := buildplan.CheckRun(p.Run); err != nil {
		t.Fatal(err)
	}
	// without default-run: its CLI, by Loco's naming
	p = detect(t, rustApp(strings.Replace(loco, "default-run = \"shop-cli\"\n", "", 1)), buildplan.Settings{})
	if !strings.Contains(p.Build, "--bin shop-cli") {
		t.Fatalf("loco's cli: %+v", p)
	}
	// no migration crate: no release command
	p = detect(t, rustApp(strings.Replace(loco, "migration = { path = \"migration\" }\n", "", 1)), buildplan.Settings{})
	if p.Release.String() != "" {
		t.Fatalf("loco without migrations: %+v", p)
	}
	// a start command someone set wins
	if p = detect(t, rustApp(loco), buildplan.Settings{Run: "start --all"}); p.Run != "start --all" {
		t.Fatalf("loco, run said: %+v", p)
	}

	for dep, name := range map[string]string{"actix-web": "Actix Web", "poem": "Poem", "salvo": "Salvo", "warp": "Warp", "serde": "Rust"} {
		toml := "[package]\nname = \"x\"\n\n[dependencies." + dep + "]\nversion = \"1\"\n"
		if p := detect(t, rustApp(toml), buildplan.Settings{}); p.Framework != name {
			t.Errorf("%s: %q", dep, p.Framework)
		}
	}
}

// Rocket listens on ROCKET_ADDRESS and ROCKET_PORT (127.0.0.1:8000), not
// HOST and PORT: its plan's default environment says them, the bundle
// carries it (.homeport/env), and homeportd sets each the app's own
// variables don't. A build that doesn't make homeport's bundle carries
// none, and says what to set.
func TestRocketListensOnHomeportsPortByDefault(t *testing.T) {
	rocket := rustApp("[package]\nname = \"x\"\n\n[dependencies]\nrocket = \"0.5\"\n")
	p := detect(t, rocket, buildplan.Settings{})
	if p.Framework != "Rocket" || !maps.Equal(p.Env, map[string]string{"ROCKET_ADDRESS": "$HOST", "ROCKET_PORT": "$PORT"}) || len(p.Warnings) > 0 {
		t.Fatalf("rocket: %+v", p)
	}
	dir := t.TempDir()
	if err := os.MkdirAll(dir+"/.homeport-bundle", 0o755); err != nil {
		t.Fatal(err)
	}
	_, step, ok := strings.Cut(p.Command, `cp "$CARGO_TARGET_DIR/release/x" .homeport-bundle/bin && `)
	if !ok {
		t.Fatalf("no step after the bundle's bin:\n%s", p.Command)
	}
	cmd := exec.Command("sh", "-c", step)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s: %v %s", step, err, out)
	}
	if b, err := os.ReadFile(dir + "/.homeport-bundle/" + buildplan.BundleEnvFile); err != nil || string(b) != "ROCKET_ADDRESS=$HOST\nROCKET_PORT=$PORT\n" {
		t.Fatalf("%q %v", b, err)
	}
	// they said it's a binary: nothing carries the defaults
	p = detect(t, rocket, buildplan.Settings{Kind: buildplan.Binary})
	if p.Env != nil || strings.Contains(p.Command, buildplan.BundleEnvFile) ||
		!slices.ContainsFunc(p.Warnings, func(w string) bool { return strings.Contains(w, "ROCKET_ADDRESS=$HOST, ROCKET_PORT=$PORT") }) {
		t.Fatalf("rocket, a binary: %+v", p)
	}
	// an app without defaults writes none
	if p = detect(t, rustApp(axumToml), buildplan.Settings{}); p.Env != nil || strings.Contains(p.Command, buildplan.BundleEnvFile) {
		t.Fatalf("axum: %+v", p)
	}
}

// What a person says still wins: a build command builds, and the bundle is
// still made from what it left; a binary is what the build left at
// server; their own install keeps the toolchain's setup.
func TestARustAppsSettings(t *testing.T) {
	p := detect(t, rustApp(axumToml), buildplan.Settings{Command: "cargo build --release --locked --features prod"})
	if p.Build != "cargo build --release --locked --features prod" || !strings.Contains(p.Command, `cp "$CARGO_TARGET_DIR/release/hello" .homeport-bundle/bin`) {
		t.Errorf("command: %+v", p)
	}
	p = detect(t, rustApp(axumToml), buildplan.Settings{Kind: buildplan.Binary})
	if p.Kind != buildplan.Binary || p.Artifact != "server" || p.Command != `cargo build --release --locked --bin hello && cp "$CARGO_TARGET_DIR/release/hello" server` {
		t.Errorf("binary: %+v", p)
	}
	p = detect(t, rustApp(axumToml), buildplan.Settings{Install: "apt-get --version"})
	if !strings.HasPrefix(p.Install, "export CARGO_TARGET_DIR=") || !strings.HasSuffix(p.Install, " && apt-get --version") {
		t.Errorf("install: %q", p.Install)
	}
	p = detect(t, rustApp(axumToml, "homeport.yaml", "packages: [fonts]\nrun: --port $PORT\n"), buildplan.Settings{})
	if p.Run != "--port $PORT" || len(p.Packages) != 1 {
		t.Errorf("homeport.yaml: %+v", p)
	}
}
