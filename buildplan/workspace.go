package buildplan

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"slices"
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

// A workspace's root - the repository's, with nothing said about how it
// builds - isn't an app: its apps are its members. The app is the member
// its build script builds (bun or pnpm --filter, npm --workspace, yarn
// workspace, turbo --filter), else the one member that's an app; it's
// planned as if its folder had been set, so the build runs there.

// rootApp is the member a workspace's root builds, and how it was told.
type rootApp struct {
	dir    string // the member's folder
	script string // the script the root runs in it, when not build
	why    string
}

// frameworks are packages that make a member an app.
var frameworks = append(slices.Clone(servers), "astro", "vite", "@sveltejs/kit", "nuxt", "@react-router/dev", "@remix-run/dev")

// workspaceApp: the app a workspace's root (r, the repository's root)
// builds; nil when r isn't one, or is an app itself.
func (r reader) workspaceApp() (*rootApp, error) {
	if !r.exists("package.json") || !r.hasLockfile() {
		return nil, nil
	}
	pm := ""
	for _, l := range lockfiles {
		if pm == "" && r.exists(l.file) {
			pm = l.pm
		}
	}
	patterns, _ := r.workspacePatterns(pm)
	pkg, deps := r.pkg(), r.deps()
	if len(patterns) == 0 || pkg.Scripts["start"] != "" || slices.ContainsFunc(frameworks, func(f string) bool { return deps[f] }) {
		return nil, nil
	}
	members := r.members(patterns)
	if name, script, said := builtMember(pkg.Scripts["build"]); name != "" {
		for _, m := range members {
			mr := reader{fsys: r.fsys, root: m}
			if name == m || strings.TrimPrefix(name, "./") == m || mr.pkgName() == name {
				a := &rootApp{dir: m, why: "the repository's root is a workspace, and its build script (" + said + ") builds " + m}
				if script != "build" && mr.pkg().Scripts[script] != "" {
					a.script = script
				}
				return a, nil
			}
		}
		return nil, fmt.Errorf("the repository's root is a workspace, and its build script (%s) builds %q, which isn't one of its packages - set the root directory to the app's folder", said, name)
	}
	var apps []string
	for _, m := range members {
		mr := reader{fsys: r.fsys, root: m}
		d, s := mr.deps(), mr.pkg().Scripts
		if (s["build"] != "" || s["start"] != "") && slices.ContainsFunc(frameworks, func(f string) bool { return d[f] }) {
			apps = append(apps, m)
		}
	}
	switch len(apps) {
	case 0:
		return nil, errors.New("the repository's root is a workspace, and none of its packages is an app homeport recognises - set the root directory to the app's folder")
	case 1:
		return &rootApp{dir: apps[0], why: "the repository's root is a workspace, and " + apps[0] + " is its one app"}, nil
	}
	return nil, fmt.Errorf("the repository's root is a workspace with more than one app (%s): set the root directory to the one this app is", strings.Join(apps, ", "))
}

// members: the workspace's folders with a package.json, sorted - not in
// node_modules or a dot-folder, at most four deep.
func (r reader) members(patterns []string) []string {
	var out []string
	_ = fs.WalkDir(r.fsys, r.root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() || p == r.root {
			return nil
		}
		rel := strings.TrimPrefix(p, r.root+"/")
		if r.root == "." {
			rel = p
		}
		if strings.HasPrefix(d.Name(), ".") || d.Name() == "node_modules" || strings.Count(rel, "/") >= 4 {
			return fs.SkipDir
		}
		if memberOf(rel, patterns) && (reader{fsys: r.fsys, root: p}).exists("package.json") {
			out = append(out, p)
		}
		return nil
	})
	slices.Sort(out)
	return out
}

// builtMember: the one package a root's build script builds by name, the
// script it runs there, and the command that said so; "" when it builds
// none, or several.
func builtMember(build string) (name, script, said string) {
	for _, cmd := range strings.Split(build, "&&") {
		f := strings.Fields(strings.TrimSpace(cmd))
		if len(f) > 0 && (f[0] == "npx" || f[0] == "bunx") {
			f = f[1:]
		}
		if len(f) == 0 {
			continue
		}
		var n, s string
		var rest []string
		flag := func(i int, names ...string) (string, int) {
			for _, fl := range names {
				if f[i] == fl && i+1 < len(f) {
					return f[i+1], 2
				}
				if v, ok := strings.CutPrefix(f[i], fl+"="); ok {
					return v, 1
				}
			}
			return "", 0
		}
		switch f[0] {
		case "bun", "pnpm", "turbo", "npm":
			names := map[string][]string{"bun": {"--filter", "-F"}, "pnpm": {"--filter", "-F"}, "turbo": {"--filter", "-F"}, "npm": {"--workspace", "-w"}}[f[0]]
			for i := 1; i < len(f); {
				if v, k := flag(i, names...); k > 0 {
					if n != "" {
						return "", "", ""
					}
					n, i = v, i+k
					continue
				}
				if !strings.HasPrefix(f[i], "-") && f[i] != "run" && f[i] != "run-script" {
					rest = append(rest, f[i])
				}
				i++
			}
		case "yarn":
			if len(f) > 3 && f[1] == "workspace" {
				n, rest = f[2], slices.DeleteFunc(f[3:], func(x string) bool { return x == "run" })
			}
		}
		n = strings.Trim(n, `'"`)
		if f[0] == "turbo" {
			n = strings.Trim(strings.TrimPrefix(strings.TrimSuffix(strings.TrimPrefix(n, "..."), "..."), "^"), "{}")
		}
		if n == "" || len(rest) == 0 {
			continue
		}
		s = rest[0]
		if n == "*" || strings.ContainsAny(n, "*?[!") || name != "" {
			return "", "", ""
		}
		name, script, said = n, s, strings.TrimSpace(cmd)
	}
	return name, script, said
}
