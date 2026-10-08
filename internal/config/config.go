// Package config is what the CLI keeps on disk: its sign-in (credentials,
// its owner's alone) and a project's link to an app on homeport.sh
// (.homeport/link.json, which git ignores).
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
)

// ErrNoCredentials: the CLI isn't signed in here.
var ErrNoCredentials = errors.New("not signed in: run `homeport login`")

// Credentials are the CLI's sign-in: the token (never printed) and what it
// was given for.
type Credentials struct {
	API     string `json:"api"`
	Token   string `json:"token"`
	TokenID string `json:"token_id"`
	Name    string `json:"name"`  // the device's, as the dashboard lists it
	Email   string `json:"email"` // who it signed in as
}

// CredentialsPath is $XDG_CONFIG_HOME/homeport/credentials, or
// ~/.config/homeport/credentials.
func CredentialsPath() string {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			home = "."
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "homeport", "credentials")
}

// LoadCredentials reads the sign-in. One others can read is refused, as
// ssh refuses a key: the token is as good as the person.
func LoadCredentials() (*Credentials, error) {
	p := CredentialsPath()
	f, err := os.Open(p)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrNoCredentials
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("%s can be read by others: run `chmod 600 %s`, or `homeport login` again", p, p)
	}
	var c Credentials
	if err := json.NewDecoder(f).Decode(&c); err != nil || c.Token == "" {
		return nil, fmt.Errorf("%s isn't a sign-in: run `homeport login` again", p)
	}
	return &c, nil
}

// SaveCredentials writes the sign-in, readable by its owner alone: written
// whole to a new file of mode 0600, then moved into place.
func SaveCredentials(c Credentials) error {
	p := CredentialsPath()
	dir := filepath.Dir(p)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".credentials-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), p)
}

// RemoveCredentials forgets the sign-in; none is nothing to do.
func RemoveCredentials() error {
	err := os.Remove(CredentialsPath())
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

// ErrNotLinked: no folder from here up is linked to an app.
var ErrNotLinked = errors.New("this folder isn't linked to an app: run `homeport link`")

// Link is a project's app on homeport.sh: the team, the app and the
// environment `homeport deploy` deploys.
type Link struct {
	Team        string `json:"team"`
	TeamSlug    string `json:"team_slug"`
	App         string `json:"app"` // the environment's id
	Project     string `json:"project"`
	Environment string `json:"environment"`
}

// LinkDir is the folder a link lives in, in the project's.
const LinkDir = ".homeport"

// SaveLink links dir: .homeport/link.json, beside a .gitignore that
// ignores the folder itself, so nothing needs adding to the project's.
func SaveLink(dir string, l Link) error {
	d := filepath.Join(dir, LinkDir)
	if err := os.MkdirAll(d, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(d, ".gitignore"), []byte("*\n"), 0o644); err != nil {
		return err
	}
	b, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(d, "link.json"), append(b, '\n'), 0o644)
}

// FindLink is the link of dir or the nearest folder above it, and that
// folder.
func FindLink(dir string) (*Link, string, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return nil, "", err
	}
	for {
		b, err := os.ReadFile(filepath.Join(dir, LinkDir, "link.json"))
		if err == nil {
			var l Link
			if err := json.Unmarshal(b, &l); err != nil || l.Team == "" || l.App == "" {
				return nil, "", fmt.Errorf("%s isn't a link: run `homeport link` again", filepath.Join(dir, LinkDir, "link.json"))
			}
			return &l, dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return nil, "", ErrNotLinked
		}
		dir = parent
	}
}
