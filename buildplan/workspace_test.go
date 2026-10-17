package buildplan_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/homeport-sh/cli/buildplan"
)

// member is a workspace: the root's files, and the member's at apps/web.
func member(root map[string]string, pkg string, files ...string) map[string]string {
	m := map[string]string{"apps/web/package.json": pkg}
	for k, v := range root {
		m[k] = v
	}
	for i := 0; i+1 < len(files); i += 2 {
		m["apps/web/"+files[i]] = files[i+1]
	}
	return m
}

const expressApp = `{"name":"web","scripts":{"start":"node server.js"},"dependencies":{"express":"^5"}}`

// A workspace member is an app: its workspace installs at the workspace's
// root - only what the member needs where the package manager can say so,
// else the whole workspace - and it builds and runs in its own folder.
func TestAWorkspaceMemberInstallsAtTheWorkspacesRoot(t *testing.T) {
	cases := map[string]struct {
		root            map[string]string
		pm, install     string
		prune, notPrune string
	}{
		"bun": {map[string]string{"package.json": `{"workspaces":["apps/*","packages/*"]}`, "bun.lock": bunLock},
			"bun", "(cd ../.. && bun install --frozen-lockfile --linker=hoisted --filter ./apps/web)",
			"bun install --frozen-lockfile --production --linker=hoisted --filter ./apps/web", ""},
		"npm": {map[string]string{"package.json": `{"workspaces":{"packages":["apps/*"]}}`, "package-lock.json": npmLock},
			"npm", "(cd ../.. && npm ci --workspace=apps/web --include-workspace-root)", "(cd ../.. && npm prune --omit=dev)", ""},
		"pnpm": {map[string]string{"package.json": `{}`, "pnpm-workspace.yaml": "packages:\n  - 'apps/*'\n", "pnpm-lock.yaml": "x"},
			"pnpm", "(cd ../.. && pnpm install --frozen-lockfile --config.node-linker=hoisted)", "(cd ../.. && pnpm prune --prod", ""},
		"yarn": {map[string]string{"package.json": `{"workspaces":["apps/web"]}`, "yarn.lock": "x"},
			"yarn", "(cd ../.. && yarn install --frozen-lockfile)", "(cd ../.. && yarn install --frozen-lockfile --production", ""},
		"yarn berry": {map[string]string{"package.json": `{"packageManager":"yarn@4.5.0","workspaces":["apps/**"]}`, "yarn.lock": "__metadata:\n"},
			"yarn", "(cd ../.. && YARN_NODE_LINKER=node-modules yarn install --immutable)", "(cd ../.. && yarn workspaces focus --all --production)", ""},
	}
	for name, c := range cases {
		p, err := buildplan.Detect(repo(member(c.root, expressApp)), buildplan.Settings{Root: "apps/web"})
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if p.PackageManager != c.pm || p.Root != "apps/web" || !strings.HasSuffix(p.Install, c.install) || !strings.Contains(p.Command, c.prune) ||
			p.Kind != buildplan.Bundle || p.Framework != "Express" || p.Run != "--import ./.homeport/boot.mjs server.js" {
			t.Errorf("%s: %+v\ninstall: %s\ncommand: %s", name, p, p.Install, p.Command)
		}
		// the workspace's dependencies, hoisted to its root, ship with it
		if !strings.Contains(p.Command, "../../node_modules") {
			t.Errorf("%s: the workspace's node_modules aren't in the bundle: %s", name, p.Command)
		}
	}
}

// The workspace's root may be a folder of the repository, not its root.
func TestAWorkspaceInAFolder(t *testing.T) {
	files := map[string]string{"js/package.json": `{"workspaces":["apps/*"]}`, "js/bun.lock": bunLock,
		"js/apps/web/package.json": expressApp}
	p, err := buildplan.Detect(repo(files), buildplan.Settings{Root: "js/apps/web"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(p.Install, "(cd ../.. && bun install --frozen-lockfile --linker=hoisted --filter ./apps/web)") {
		t.Errorf("%s", p.Install)
	}
}

// cloudtrain's shape: a Next.js app compiled by next-bun-compile, in a Bun
// workspace - a binary, built in the member's folder.
func TestACompiledWorkspaceMemberIsABinary(t *testing.T) {
	files := member(map[string]string{"package.json": `{"workspaces":["apps/*"]}`, "bun.lock": bunLock},
		`{"name":"web","scripts":{"build":"next build","start":"next start"},"dependencies":{"next":"16"},"devDependencies":{"next-bun-compile":"^2"}}`,
		"next.config.ts", "export default { adapterPath: \"next-bun-compile\" }\n")
	p, err := buildplan.Detect(repo(files), buildplan.Settings{Root: "apps/web"})
	if err != nil {
		t.Fatal(err)
	}
	if p.Kind != buildplan.Binary || p.Artifact != "server" || p.Command != "bun run build" ||
		p.Install != "(cd ../.. && bun install --frozen-lockfile --filter ./apps/web)" {
		t.Errorf("%+v", p)
	}
}

// A Vite member has an index.html: it's still a site its build makes, not
// plain HTML served as it is.
func TestAViteWorkspaceMemberIsBuilt(t *testing.T) {
	files := member(map[string]string{"package.json": `{"workspaces":["apps/*"]}`, "package-lock.json": npmLock},
		`{"name":"web","scripts":{"build":"vite build"},"devDependencies":{"vite":"^7"}}`, "index.html", "<html></html>")
	p, err := buildplan.Detect(repo(files), buildplan.Settings{Root: "apps/web"})
	if err != nil {
		t.Fatal(err)
	}
	if p.Kind != buildplan.Static || p.Artifact != "dist" || p.Framework != "Vite" || p.Command != "npm run build" ||
		!strings.HasSuffix(p.Install, "(cd ../.. && npm ci --workspace=apps/web --include-workspace-root)") {
		t.Errorf("%+v", p)
	}
}

// Next.js traces a workspace member's standalone server from the
// workspace's root: it's at .next/standalone/<member>/server.js.
func TestANextWorkspaceMembersStandaloneIsNested(t *testing.T) {
	files := member(map[string]string{"package.json": `{"workspaces":["apps/*"]}`, "bun.lock": bunLock},
		`{"name":"web","scripts":{"build":"next build","start":"next start"},"dependencies":{"next":"16"}}`)
	p, err := buildplan.Detect(repo(files), buildplan.Settings{Root: "apps/web"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(p.Run, " apps/web/server.js") || !strings.Contains(p.Command, "[ -f .next/standalone/apps/web/server.js ]") ||
		!strings.Contains(p.Command, buildplan.BundleDir+"/apps/web/.next/static") {
		t.Errorf("run: %s\ncommand: %s", p.Run, p.Command)
	}
}

// A lockfile above whose workspaces don't take the folder in: said plainly.
func TestAFolderTheWorkspaceDoesntListIsRefused(t *testing.T) {
	for name, root := range map[string]map[string]string{
		"workspaces":          {"package.json": `{"workspaces":["packages/*"]}`, "bun.lock": bunLock},
		"no workspaces":       {"package.json": `{}`, "package-lock.json": npmLock},
		"pnpm":                {"package.json": `{}`, "pnpm-workspace.yaml": "packages:\n  - 'packages/*'\n", "pnpm-lock.yaml": "x"},
		"excluded":            {"package.json": `{"workspaces":["apps/*","!apps/web"]}`, "yarn.lock": "x"},
		"no package.json":     {"bun.lock": bunLock},
		"pnpm without a list": {"package.json": `{"workspaces":["apps/*"]}`, "pnpm-lock.yaml": "x"},
	} {
		_, err := buildplan.Detect(repo(member(root, expressApp)), buildplan.Settings{Root: "apps/web"})
		if err == nil || !strings.Contains(err.Error(), "workspace") || strings.Contains(err.Error(), "aren't supported") {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// The control plane reads a workspace's root files too: what tells it a
// folder is a member, and how the workspace installs.
func TestWorkspaceFilesAreTheRootsLockfilesAndLists(t *testing.T) {
	got := strings.Join(buildplan.WorkspaceFiles(), " ")
	for _, f := range []string{"package.json", "pnpm-workspace.yaml", "bun.lock", "package-lock.json", "pnpm-lock.yaml", "yarn.lock"} {
		if !strings.Contains(" "+got+" ", " "+f+" ") {
			t.Errorf("%s isn't read: %s", f, got)
		}
	}
}

// The plan's own install and bundle steps, run for real on a Bun workspace
// (Linux, as a builder): the member's workspace dependency, a link at the
// workspace's root, ships in the bundle as files.
func TestAWorkspaceMembersBundleHasItsWorkspaceDependencies(t *testing.T) {
	bun, err := exec.LookPath("bun")
	if err != nil {
		t.Skip("no bun")
	}
	dir := t.TempDir()
	write := func(name, body string) {
		_ = os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755)
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("package.json", `{"name":"root","private":true,"workspaces":["apps/*","packages/*"]}`)
	write("packages/ui/package.json", `{"name":"ui","version":"1.0.0","main":"index.js"}`)
	write("packages/ui/index.js", "module.exports = 'ui'\n")
	// no registry: only workspace dependencies on disk (express is only
	// what the plan reads, to see a Node server)
	write("apps/web/package.json", `{"name":"web","scripts":{"start":"node server.js","build":"echo built"},"dependencies":{"ui":"workspace:*"}}`)
	write("apps/web/server.js", "require('ui')\n")
	write("apps/other/package.json", `{"name":"other","dependencies":{"ui":"workspace:*"}}`)
	if out, err := exec.Command(bun, "install", "--cwd", dir).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	_ = os.RemoveAll(filepath.Join(dir, "node_modules"))
	files := map[string]string{}
	for _, f := range []string{"package.json", "bun.lock", "apps/web/package.json", "apps/other/package.json", "packages/ui/package.json"} {
		b, _ := os.ReadFile(filepath.Join(dir, f))
		files[f] = string(b)
	}
	files["apps/web/package.json"] = expressApp // what it's planned as: a Node server
	p, err := buildplan.Detect(repo(files), buildplan.Settings{Root: "apps/web"})
	if err != nil {
		t.Fatal(err)
	}
	steps := p.Command[strings.Index(p.Command, "rm -rf "+buildplan.BundleDir):strings.Index(p.Command, " && cp /usr/local/bin/")]
	out, err := shell(t, filepath.Join(dir, "apps/web"), p.Install+" && "+steps)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	b, err := os.ReadFile(filepath.Join(dir, "apps/web", buildplan.BundleDir, "node_modules/ui/index.js"))
	if err != nil || string(b) != "module.exports = 'ui'\n" {
		t.Fatalf("ui isn't in the bundle: %v %q\n%s", err, b, out)
	}
	if st, err := os.Lstat(filepath.Join(dir, "apps/web", buildplan.BundleDir, "node_modules/ui")); err != nil || st.Mode()&os.ModeSymlink != 0 {
		t.Errorf("ui is a link in the bundle: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "apps/web", buildplan.BundleDir, "node_modules/web")); err == nil {
		t.Errorf("the member is in its own bundle's node_modules")
	}
}
