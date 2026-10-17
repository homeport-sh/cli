package buildplan

import (
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"strings"

	"go.yaml.in/yaml/v3"
)

// A workspace member - an app in a bun, npm, pnpm or Yarn workspace, whose
// lockfile is the workspace's, above it - installs at the workspace's root
// (only what the member needs, where the package manager can say so), then
// builds and runs in its own folder: the build's commands run there, and
// the output is relative to it.

// WorkspaceFiles are the files Detect reads in each folder above the app's,
// to find its workspace: a control plane fetches these too.
func WorkspaceFiles() []string {
	out := []string{"package.json", "pnpm-workspace.yaml"}
	for _, l := range lockfiles {
		out = append(out, l.file)
	}
	return out
}

// workspace is where an app's workspace is, when it's a member.
type workspace struct {
	root string // the workspace's folder in the repository: "." its root
	rel  string // the member's folder, from root
	up   string // root, from the member's folder: ../..
	name string // the member's package name, when it's a plain one
}

// pkgNameRe: a package name safe in a path and a shell word.
var pkgNameRe = regexp.MustCompile(`^(@[a-z0-9][a-z0-9._-]*/)?[a-z0-9][a-z0-9._-]*$`)

// findWorkspace: the nearest folder above the app's with a lockfile, and
// whether its workspaces take the app's folder in - package.json's
// workspaces, or pnpm's pnpm-workspace.yaml. No lockfile above: no
// workspace (nil, nil).
func (r reader) findWorkspace() (*workspace, error) {
	if r.root == "." {
		return nil, nil
	}
	for d := path.Dir(r.root); ; d = path.Dir(d) {
		lr := reader{fsys: r.fsys, root: d}
		if lr.hasLockfile() {
			rel := r.root
			if d != "." {
				rel = strings.TrimPrefix(r.root, d+"/")
			}
			pm := ""
			for _, l := range lockfiles {
				if pm == "" && lr.exists(l.file) {
					pm = l.pm
				}
			}
			patterns, from := lr.workspacePatterns(pm)
			where := "the repository's root"
			if d != "." {
				where = d
			}
			if !memberOf(rel, patterns) {
				return nil, fmt.Errorf("%s looks like a workspace member: its lockfile is in %s, but %s doesn't list it among the workspace's packages - "+
					"add it there, or give the app a lockfile of its own in %s", r.root, where, from, r.root)
			}
			w := &workspace{root: d, rel: rel, up: strings.Repeat("../", strings.Count(rel, "/")+1)}
			w.up = strings.TrimSuffix(w.up, "/")
			if n := r.pkgName(); pkgNameRe.MatchString(n) {
				w.name = n
			}
			return w, nil
		}
		if d == "." {
			return nil, nil
		}
	}
}

// workspacePatterns: the workspace's package globs, and where they're said.
func (r reader) workspacePatterns(pm string) ([]string, string) {
	if pm == "pnpm" {
		var y struct {
			Packages []string `yaml:"packages"`
		}
		b, _ := r.read("pnpm-workspace.yaml")
		_ = yaml.Unmarshal(b, &y)
		return y.Packages, r.name("pnpm-workspace.yaml")
	}
	var pkg struct {
		Workspaces json.RawMessage `json:"workspaces"`
	}
	b, _ := r.read("package.json")
	_ = json.Unmarshal(b, &pkg)
	var list []string
	if json.Unmarshal(pkg.Workspaces, &list) != nil {
		var obj struct {
			Packages []string `json:"packages"`
		}
		_ = json.Unmarshal(pkg.Workspaces, &obj)
		list = obj.Packages
	}
	return list, r.name("package.json") + "'s workspaces"
}

// memberOf: a folder the globs take in (a "!" glob takes it out again).
func memberOf(dir string, patterns []string) bool {
	in := false
	for _, p := range patterns {
		neg := strings.HasPrefix(p, "!")
		p = strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(p, "!"), "./"), "/")
		if globDir(p, dir) {
			in = !neg
		}
	}
	return in
}

// globDir: a workspace glob (*, and ** for any depth) matches a folder.
func globDir(pattern, dir string) bool {
	ps, ds := strings.Split(pattern, "/"), strings.Split(dir, "/")
	var match func(i, j int) bool
	match = func(i, j int) bool {
		if i == len(ps) {
			return j == len(ds)
		}
		if ps[i] == "**" {
			for k := j; k <= len(ds); k++ {
				if match(i+1, k) {
					return true
				}
			}
			return false
		}
		if j == len(ds) {
			return false
		}
		ok, err := path.Match(ps[i], ds[j])
		return err == nil && ok && match(i+1, j+1)
	}
	return match(0, 0)
}

func (r reader) pkgName() string {
	var p struct {
		Name string `json:"name"`
	}
	b, _ := r.read("package.json")
	_ = json.Unmarshal(b, &p)
	return p.Name
}

// lock is where the app's lockfile is: its workspace's root, or its folder.
func (r reader) lock() reader {
	if r.ws != nil {
		return reader{fsys: r.fsys, root: r.ws.root}
	}
	return r
}

// inWorkspace: a package manager's install and prune, run at the workspace's
// root - filtered to the member where it can be (Bun's --filter, npm's
// --workspace with the root's own dependencies); pnpm's hoisted install and
// Yarn's install the whole workspace.
func (w *workspace) inWorkspace(m pm) pm {
	if w == nil {
		return m
	}
	install, prune := m.install, m.prune
	switch m.name {
	case "bun":
		filter := " --filter ./" + w.rel
		install += filter
		// the member's own node_modules too: what didn't hoist
		prune = strings.Replace(prune, "rm -rf node_modules && ", "rm -rf node_modules "+w.rel+"/node_modules && ", 1)
		prune = strings.Replace(prune, "--production"+bunLinker(m), "--production"+bunLinker(m)+filter, 1)
	case "npm":
		install += " --workspace=" + w.rel + " --include-workspace-root"
	}
	m.install = "(cd " + w.up + " && " + install + ")"
	m.prune = "(cd " + w.up + " && " + prune + ")"
	return m
}

func bunLinker(m pm) string {
	if strings.Contains(m.install, "--linker=hoisted") {
		return " --linker=hoisted"
	}
	return ""
}
