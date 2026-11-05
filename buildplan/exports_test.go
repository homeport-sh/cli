package buildplan_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Node 22.12 and later require() a package's "module-sync" file where it
// has one, listed first (exports' order is @swc/helpers' own): Next.js standalone traced @swc/helpers' "default" (cjs) file
// and shipped no esm one, and clinicq-dashboard crashed at start, Cannot
// find module …/@swc/helpers/esm/_interop_require_default.js. The build
// (embed/exports.mjs) puts back such a package whole, from what the build
// installed, and fails when it can't.

const helpersPkg = `{"name":"@swc/helpers","version":"0.5.24","exports":{
	"./_/_interop_require_default":{"module-sync":"./esm/_interop_require_default.js","webpack":"./esm/_interop_require_default.js","import":"./esm/_interop_require_default.js","default":"./cjs/_interop_require_default.cjs"},
	"./_/_unused":{"module-sync":"./esm/_unused.js","default":"./cjs/_unused.cjs"},
	"./*":{"module-sync":"./esm/*.js","default":"./cjs/*.cjs"}}}`

// exports runs the step on a bundle and what was installed, each made of
// files; it answers the bundle's files after, and what the step said.
func exports(t *testing.T, bundle, installed map[string]string) (map[string]bool, string, error) {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("no node")
	}
	dir := t.TempDir()
	write := func(root string, files map[string]string) {
		for name, body := range files {
			p := filepath.Join(dir, root, name)
			_ = os.MkdirAll(filepath.Dir(p), 0o755)
			if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	write("b", bundle)
	write("src", installed)
	script, err := filepath.Abs("embed/exports.mjs")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(node, script, "b", "src")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	got := map[string]bool{}
	_ = filepath.Walk(filepath.Join(dir, "b"), func(p string, fi os.FileInfo, _ error) error {
		if fi != nil && !fi.IsDir() {
			rel, _ := filepath.Rel(filepath.Join(dir, "b"), p)
			got[filepath.ToSlash(rel)] = true
		}
		return nil
	})
	return got, string(out), err
}

func TestAPackageTracedForDefaultGetsItsModuleSyncFileBack(t *testing.T) {
	const h = "node_modules/@swc/helpers/"
	installed := map[string]string{h + "package.json": helpersPkg,
		h + "cjs/_interop_require_default.cjs": "c", h + "esm/_interop_require_default.js": "e",
		h + "cjs/_unused.cjs": "c", h + "esm/_unused.js": "e", h + "node_modules/x/index.js": "nested"}
	got, out, err := exports(t, map[string]string{h + "package.json": helpersPkg, h + "cjs/_interop_require_default.cjs": "c",
		"node_modules/next/package.json": `{"name":"next","version":"16.0.0","exports":{".":"./index.js"}}`, "node_modules/next/index.js": "n"},
		installed)
	if err != nil || !got[h+"esm/_interop_require_default.js"] {
		t.Fatalf("not put back: %v %s %v", err, out, got)
	}
	if !strings.Contains(out, "@swc/helpers@0.5.24") {
		t.Errorf("said %q", out)
	}
	if got[h+"node_modules/x/index.js"] {
		t.Error("its own node_modules came too")
	}
}

// What was never traced (no file of the entry shipped) is no one's: left.
func TestAnEntryNothingShippedIsLeft(t *testing.T) {
	const h = "node_modules/@swc/helpers/"
	got, out, err := exports(t, map[string]string{h + "package.json": helpersPkg},
		map[string]string{h + "package.json": helpersPkg, h + "esm/_unused.js": "e", h + "cjs/_unused.cjs": "c"})
	if err != nil || got[h+"esm/_unused.js"] || out != "" {
		t.Fatalf("%v %q %v", err, out, got)
	}
}

// Missing where the build installed it too, or installed another version:
// the build fails, saying which.
func TestAModuleSyncFileNowhereFailsTheBuild(t *testing.T) {
	const h = "node_modules/@swc/helpers/"
	bundle := map[string]string{h + "package.json": helpersPkg, h + "cjs/_interop_require_default.cjs": "c"}
	for name, installed := range map[string]map[string]string{
		"missing": {h + "package.json": helpersPkg, h + "cjs/_interop_require_default.cjs": "c"},
		"another version": {h + "package.json": strings.Replace(helpersPkg, "0.5.24", "0.5.23", 1),
			h + "cjs/_interop_require_default.cjs": "c", h + "esm/_interop_require_default.js": "e"},
	} {
		_, out, err := exports(t, bundle, installed)
		if err == nil || !strings.Contains(out, "esm/_interop_require_default.js") {
			t.Errorf("%s: %v %q", name, err, out)
		}
	}
}
