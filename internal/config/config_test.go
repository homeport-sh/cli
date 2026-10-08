package config

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestCredentialsAreTheirOwnersAlone(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if _, err := LoadCredentials(); !errors.Is(err, ErrNoCredentials) {
		t.Fatalf("none yet: %v", err)
	}
	c := Credentials{API: "https://api.example", Token: "hpcli_secret", TokenID: "t1", Name: "laptop", Email: "a@example.com"}
	if err := SaveCredentials(c); err != nil {
		t.Fatal(err)
	}
	p := CredentialsPath()
	if filepath.Base(p) != "credentials" || filepath.Base(filepath.Dir(p)) != "homeport" {
		t.Fatalf("path %s", p)
	}
	info, err := os.Stat(p)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v %v", info.Mode(), err)
	}
	if dir, _ := os.Stat(filepath.Dir(p)); dir.Mode().Perm() != 0o700 {
		t.Fatalf("folder mode %v", dir.Mode())
	}
	got, err := LoadCredentials()
	if err != nil || *got != c {
		t.Fatalf("%+v %v", got, err)
	}
	// readable by others: refused, as ssh refuses a key
	if runtime.GOOS != "windows" {
		os.Chmod(p, 0o644)
		if _, err := LoadCredentials(); err == nil || errors.Is(err, ErrNoCredentials) {
			t.Fatalf("world-readable: %v", err)
		}
		os.Chmod(p, 0o600)
	}
	if err := RemoveCredentials(); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadCredentials(); !errors.Is(err, ErrNoCredentials) {
		t.Fatalf("removed: %v", err)
	}
	if err := RemoveCredentials(); err != nil {
		t.Fatalf("removing none: %v", err)
	}
}

func TestWithoutXDGItsUnderHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", home)
	if p := CredentialsPath(); p != filepath.Join(home, ".config", "homeport", "credentials") {
		t.Fatalf("%s", p)
	}
}

func TestALinkIsFoundFromBelowIt(t *testing.T) {
	dir := t.TempDir()
	l := Link{Team: "team1", TeamSlug: "alice", App: "app1", Project: "blog", Environment: "production"}
	if err := SaveLink(dir, l); err != nil {
		t.Fatal(err)
	}
	// it ignores itself: nothing to add to the project's .gitignore
	if b, err := os.ReadFile(filepath.Join(dir, ".homeport", ".gitignore")); err != nil || string(b) != "*\n" {
		t.Fatalf("%q %v", b, err)
	}
	deep := filepath.Join(dir, "web", "src")
	os.MkdirAll(deep, 0o755)
	got, at, err := FindLink(deep)
	if err != nil || *got != l || at != dir {
		t.Fatalf("%+v %s %v", got, at, err)
	}
	if _, _, err := FindLink(t.TempDir()); !errors.Is(err, ErrNotLinked) {
		t.Fatalf("not linked: %v", err)
	}
}
