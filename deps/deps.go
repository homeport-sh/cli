// Package deps reads what a project's lockfiles install - each package by
// name and exact version, as OSV (osv.dev) knows them - and asks OSV which
// have known vulnerabilities. A build sends its list to the control plane,
// which checks each live release at build time and again as advisories
// are published; `homeport audit` checks a folder from the terminal.
//
// Only packages from the public registries are listed (npm's, Packagist):
// one from a company's own registry, a git repository or the project's own
// workspace has a name that's the team's, and no public advisory covers
// it, so it's never sent anywhere.
package deps

import (
	"bufio"
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Dep is one installed package: its OSV ecosystem ("npm", "Packagist"),
// its name and its exact version.
type Dep struct {
	Ecosystem string `json:"ecosystem"`
	Name      string `json:"name"`
	Version   string `json:"version"`
}

// maxLockfile is past any real lockfile; a bigger one isn't read.
const maxLockfile = 64 << 20

// MaxDeps is past any real project's packages; past it, the rest aren't
// listed.
const MaxDeps = 20000

// lockfiles are what Read reads, each by its parser.
var lockfiles = []struct {
	file  string
	parse func([]byte) ([]Dep, error)
}{
	{"package-lock.json", npmLock}, {"npm-shrinkwrap.json", npmLock},
	{"bun.lock", bunLock}, {"pnpm-lock.yaml", pnpmLock}, {"yarn.lock", yarnLock},
	{"composer.lock", composerLock},
}

// Read lists what the lockfiles at fsys's top install, sorted, each once.
// No lockfile is nothing; one that isn't readable is an error naming it.
func Read(fsys fs.FS) ([]Dep, error) {
	var all []Dep
	for _, l := range lockfiles {
		st, err := fs.Stat(fsys, l.file)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("%s: %w", l.file, err)
		}
		if !st.Mode().IsRegular() || st.Size() > maxLockfile {
			continue
		}
		b, err := fs.ReadFile(fsys, l.file)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", l.file, err)
		}
		ds, err := l.parse(b)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", l.file, err)
		}
		all = append(all, ds...)
	}
	slices.SortFunc(all, func(a, b Dep) int {
		return cmp.Or(cmp.Compare(a.Ecosystem, b.Ecosystem), cmp.Compare(a.Name, b.Name), cmp.Compare(a.Version, b.Version))
	})
	all = slices.Compact(all)
	if len(all) > MaxDeps {
		all = all[:MaxDeps]
	}
	return all, nil
}

// exact is a published version: 1.2.3, 1.2.3-rc.1+build - not a range, a
// path, a URL or a git ref.
var exact = regexp.MustCompile(`^v?[0-9]+(\.[0-9]+)*([-+][0-9A-Za-z.+-]*)?$`)

// npmName is a package name npm's registry could have.
var npmName = regexp.MustCompile(`^(@[a-z0-9][a-z0-9._~-]*/)?[a-z0-9][a-z0-9._~-]*$`)

// publicNPM: a tarball from npm's registry (or Yarn's mirror of it), or
// none said - the default registry.
func publicNPM(resolved string) bool {
	if resolved == "" {
		return true
	}
	u, err := url.Parse(resolved)
	return err == nil && u.Scheme == "https" && (u.Host == "registry.npmjs.org" || u.Host == "registry.yarnpkg.com")
}

// npmDep is name@version as npm's, when it's one OSV could know.
func npmDep(name, version, resolved string) (Dep, bool) {
	version = strings.TrimPrefix(version, "v")
	if !npmName.MatchString(name) || !exact.MatchString(version) || !publicNPM(resolved) {
		return Dep{}, false
	}
	return Dep{"npm", name, version}, true
}

// splitAt splits name@version at its version's @ (a scoped name has its own
// first).
func splitAt(s string) (string, string, bool) {
	i := strings.LastIndex(s, "@")
	if i <= 0 {
		return "", "", false
	}
	return s[:i], s[i+1:], true
}

// npmLock reads package-lock.json (or npm-shrinkwrap.json): lockfile
// version 2 and 3's packages, version 1's nested dependencies.
func npmLock(b []byte) ([]Dep, error) {
	var l struct {
		Packages map[string]struct {
			Version  string `json:"version"`
			Resolved string `json:"resolved"`
			Link     bool   `json:"link"`
		} `json:"packages"`
		Dependencies map[string]npmV1 `json:"dependencies"`
	}
	if err := json.Unmarshal(b, &l); err != nil {
		return nil, err
	}
	var out []Dep
	if l.Packages != nil {
		for path, p := range l.Packages {
			i := strings.LastIndex(path, "node_modules/")
			if i < 0 || p.Link {
				continue // the project itself, or a workspace's folder
			}
			if d, ok := npmDep(path[i+len("node_modules/"):], p.Version, p.Resolved); ok {
				out = append(out, d)
			}
		}
		return out, nil
	}
	var walk func(map[string]npmV1, int)
	walk = func(ds map[string]npmV1, depth int) {
		for name, p := range ds {
			if d, ok := npmDep(name, p.Version, p.Resolved); ok {
				out = append(out, d)
			}
			if depth < 64 {
				walk(p.Dependencies, depth+1)
			}
		}
	}
	walk(l.Dependencies, 0)
	return out, nil
}

// npmV1 is a lockfile version 1 package.
type npmV1 struct {
	Version      string           `json:"version"`
	Resolved     string           `json:"resolved"`
	Dependencies map[string]npmV1 `json:"dependencies"`
}

// bunLock reads bun.lock (JSON with trailing commas): each package's
// ["name@version", registry, …] - registry "" is the default.
func bunLock(b []byte) ([]Dep, error) {
	var l struct {
		Packages map[string][]json.RawMessage `json:"packages"`
	}
	if err := json.Unmarshal(withoutTrailingCommas(b), &l); err != nil {
		return nil, err
	}
	var out []Dep
	for _, p := range l.Packages {
		if len(p) < 2 {
			continue // a workspace's
		}
		var id, registry string
		if json.Unmarshal(p[0], &id) != nil || json.Unmarshal(p[1], &registry) != nil {
			continue // a git or a file package's second is an object
		}
		name, version, ok := splitAt(id)
		if !ok {
			continue
		}
		if d, ok := npmDep(name, version, registry); ok {
			out = append(out, d)
		}
	}
	return out, nil
}

// withoutTrailingCommas is JSON with a comma before a closing bracket or
// brace dropped, outside strings, as bun.lock writes it.
func withoutTrailingCommas(b []byte) []byte {
	out := make([]byte, 0, len(b))
	in, esc := false, false
	for i := 0; i < len(b); i++ {
		c := b[i]
		if in {
			out = append(out, c)
			switch {
			case esc:
				esc = false
			case c == '\\':
				esc = true
			case c == '"':
				in = false
			}
			continue
		}
		if c == '"' {
			in = true
		}
		if c == ',' {
			j := i + 1
			for j < len(b) && (b[j] == ' ' || b[j] == '\t' || b[j] == '\n' || b[j] == '\r') {
				j++
			}
			if j < len(b) && (b[j] == '}' || b[j] == ']') {
				continue
			}
		}
		out = append(out, c)
	}
	return out
}

// pnpmLock reads pnpm-lock.yaml: its packages' keys, /name@version
// (lockfile 6), name@version (9) or /name/version (5), each maybe with its
// peers in parentheses; one from another registry says its tarball.
func pnpmLock(b []byte) ([]Dep, error) {
	var l struct {
		Packages map[string]struct {
			Resolution struct {
				Tarball string `yaml:"tarball"`
			} `yaml:"resolution"`
		} `yaml:"packages"`
	}
	if err := yaml.Unmarshal(b, &l); err != nil {
		return nil, err
	}
	var out []Dep
	for key, p := range l.Packages {
		k, _, _ := strings.Cut(strings.TrimPrefix(key, "/"), "(")
		name, version, ok := splitAt(k)
		if !ok && strings.HasPrefix(key, "/") {
			if i := strings.LastIndex(k, "/"); i > 0 {
				name, version, ok = k[:i], k[i+1:], true
			}
		}
		if !ok {
			continue
		}
		if d, ok := npmDep(name, version, p.Resolution.Tarball); ok {
			out = append(out, d)
		}
	}
	return out, nil
}

// yarnLock reads yarn.lock: Yarn 1's own format, or Berry's YAML (it has
// __metadata), whose npm packages resolve to name@npm:version.
func yarnLock(b []byte) ([]Dep, error) {
	if bytes.Contains(b, []byte("\n__metadata:")) || bytes.HasPrefix(b, []byte("__metadata:")) {
		var l map[string]struct {
			Resolution string `yaml:"resolution"`
		}
		if err := yaml.Unmarshal(b, &l); err != nil {
			return nil, err
		}
		var out []Dep
		for key, p := range l {
			if key == "__metadata" {
				continue
			}
			name, version, ok := strings.Cut(p.Resolution, "@npm:")
			if !ok || name == "" {
				continue // a workspace, a git repository, a URL
			}
			if d, ok := npmDep(name, version, ""); ok {
				out = append(out, d)
			}
		}
		return out, nil
	}
	var out []Dep
	var name, version, resolved string
	flush := func() {
		if name != "" {
			if d, ok := npmDep(name, version, resolved); ok {
				out = append(out, d)
			}
		}
		name, version, resolved = "", "", ""
	}
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "" || strings.HasPrefix(line, "#"):
		case !strings.HasPrefix(line, " "):
			flush()
			// "a@^1", a@^1.2: - its first specifier names it
			first, _, _ := strings.Cut(strings.TrimSuffix(line, ":"), ",")
			n, _, ok := splitAt(strings.Trim(strings.TrimSpace(first), `"`))
			if ok {
				name = n
			}
		case strings.HasPrefix(line, "  version "):
			version = strings.Trim(strings.TrimPrefix(line, "  version "), `"`)
		case strings.HasPrefix(line, "  resolved "):
			resolved = strings.Trim(strings.TrimPrefix(line, "  resolved "), `"`)
		}
	}
	flush()
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// composerLock reads composer.lock: its packages and dev packages, each
// listed where Packagist is where it came from (its notification URL).
func composerLock(b []byte) ([]Dep, error) {
	type pkg struct {
		Name    string `json:"name"`
		Version string `json:"version"`
		Notify  string `json:"notification-url"`
	}
	var l struct {
		Packages    []pkg `json:"packages"`
		PackagesDev []pkg `json:"packages-dev"`
	}
	if err := json.Unmarshal(b, &l); err != nil {
		return nil, err
	}
	var out []Dep
	for _, p := range slices.Concat(l.Packages, l.PackagesDev) {
		v := strings.TrimPrefix(p.Version, "v")
		u, err := url.Parse(p.Notify)
		if err != nil || u.Host != "packagist.org" || !exact.MatchString(v) || !strings.Contains(p.Name, "/") {
			continue
		}
		out = append(out, Dep{"Packagist", p.Name, v})
	}
	return out, nil
}
