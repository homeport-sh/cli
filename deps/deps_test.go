package deps

import (
	"os"
	"slices"
	"testing"
)

// Each lockfile gives what it installs from the public registries - by
// name and exact version, as OSV knows them. A package from anywhere else
// (a company's own registry, a git repository, the project's own
// workspace) isn't listed: its name is the team's, and no public advisory
// covers it.
func TestReadListsWhatALockfileInstalls(t *testing.T) {
	for dir, want := range map[string][]Dep{
		"npm":      {{"npm", "express", "4.18.2"}, {"npm", "qs", "6.11.0"}},
		"bun":      {{"npm", "@types/node", "22.1.0"}, {"npm", "next", "15.0.0"}},
		"pnpm9":    {{"npm", "@babel/core", "7.24.0"}, {"npm", "react", "18.2.0"}},
		"pnpm6":    {{"npm", "@babel/core", "7.24.0"}, {"npm", "lodash", "4.17.20"}},
		"yarn1":    {{"npm", "@babel/code-frame", "7.22.13"}, {"npm", "minimist", "1.2.5"}},
		"berry":    {{"npm", "axios", "1.6.0"}},
		"composer": {{"Packagist", "laravel/framework", "11.9.0"}, {"Packagist", "phpunit/phpunit", "10.5.0"}},
	} {
		t.Run(dir, func(t *testing.T) {
			got, err := Read(os.DirFS("testdata/" + dir))
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(got, want) {
				t.Fatalf("Read = %v\nwant %v", got, want)
			}
		})
	}
}

// No lockfile, nothing to say; one that isn't readable is an error, so a
// build can say it couldn't check.
func TestReadWithoutALockfile(t *testing.T) {
	got, err := Read(os.DirFS("testdata/none"))
	if err != nil || len(got) != 0 {
		t.Fatalf("Read = %v, %v; want nothing", got, err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(dir+"/composer.lock", []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(os.DirFS(dir)); err == nil {
		t.Fatal("an unreadable composer.lock: no error")
	}
}

// ReadWith reads the same lockfiles from anywhere - a repository a
// provider serves, an uploaded tree - by name.
func TestReadWithReadsByName(t *testing.T) {
	var asked []string
	got, err := ReadWith(func(name string) ([]byte, bool, error) {
		asked = append(asked, name)
		if name != "composer.lock" {
			return nil, false, nil
		}
		b, err := os.ReadFile("testdata/composer/composer.lock")
		return b, true, err
	})
	if err != nil || !slices.Equal(got, []Dep{{"Packagist", "laravel/framework", "11.9.0"}, {"Packagist", "phpunit/phpunit", "10.5.0"}}) {
		t.Fatalf("ReadWith = %v, %v", got, err)
	}
	if !slices.Equal(asked, Lockfiles()) {
		t.Fatalf("asked for %v, want %v", asked, Lockfiles())
	}
}
