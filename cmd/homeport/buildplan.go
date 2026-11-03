package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/homeport-sh/cli/buildplan"
)

// cmdBuildPlan prints the plan for a repository (default: here), as JSON:
// how it builds and what it produces (package buildplan). Builders run it on
// a checkout, with the app's build settings (--settings, a JSON file: its
// folder, and what a person set in the UI); anyone can run it to see what a
// hosted build will do.
func cmdBuildPlan(args []string) error {
	dir, settings := ".", ""
	for len(args) > 0 {
		switch {
		case args[0] == "-h" || args[0] == "--help":
			fmt.Println(buildPlanUsage)
			return nil
		case args[0] == "--settings" && len(args) > 1:
			settings, args = args[1], args[2:]
		case len(args) == 1:
			dir, args = args[0], nil
		default:
			return errors.New(buildPlanUsage)
		}
	}
	var s buildplan.Settings
	if settings != "" {
		f, err := os.Open(settings)
		if err != nil {
			return err
		}
		defer f.Close()
		dec := json.NewDecoder(io.LimitReader(f, 64<<10))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&s); err != nil {
			return fmt.Errorf("build settings: %w", err)
		}
	}
	p, err := planFolder(dir, s)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(p)
}

const buildPlanUsage = "usage: homeport build-plan [--settings <file.json>] [dir]"

// planFolder plans the app in dir, a folder. A workspace member's folder
// in a checkout (a package.json, its lockfile the workspace's) plans as
// its hosted build does: from the checkout's top, with the root directory
// set to it.
func planFolder(dir string, s buildplan.Settings) (buildplan.Plan, error) {
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return buildplan.Plan{}, fmt.Errorf("%s isn't a folder", dir)
	}
	if s.Root == "" {
		if top, rel := checkoutMember(dir); top != "" {
			s.Root = rel
			p, err := planBuild(top, s)
			if err == nil {
				p.Warnings = append(p.Warnings, "planned from the repository's root with the root directory "+rel+", as its hosted build is: its lockfile is the workspace's - set "+rel+" as the app's root directory")
			}
			return p, err
		}
	}
	return planBuild(dir, s)
}

// checkoutMember: dir has a package.json and no lockfile of its own, in a
// git checkout - the checkout's top (the nearest folder above with .git)
// and dir from it. Only files are read, nothing runs; a builder's checkout
// has no .git (an upload never carries it), so nothing above it is.
func checkoutMember(dir string) (top, rel string) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", ""
	}
	if st, err := os.Stat(filepath.Join(abs, "package.json")); err != nil || !st.Mode().IsRegular() {
		return "", ""
	}
	for _, f := range buildplan.WorkspaceFiles() {
		if f == "package.json" || f == "pnpm-workspace.yaml" {
			continue
		}
		if _, err := os.Lstat(filepath.Join(abs, f)); err == nil {
			return "", ""
		}
	}
	for d := filepath.Dir(abs); ; d = filepath.Dir(d) {
		if _, err := os.Lstat(filepath.Join(d, ".git")); err == nil {
			r, err := filepath.Rel(d, abs)
			if err != nil {
				return "", ""
			}
			return d, filepath.ToSlash(r)
		}
		if filepath.Dir(d) == d {
			return "", ""
		}
	}
}

// planBuild reads a repository and runs nothing from it: its files are
// untrusted. A ${VAR} stays literal, for the build's own shell to expand. How
// a binary runs is checked by the sandbox's rules, since a hosted app always
// runs in one.
func planBuild(dir string, s buildplan.Settings) (buildplan.Plan, error) {
	p, err := buildplan.Detect(os.DirFS(dir), s)
	if err != nil {
		return buildplan.Plan{}, err
	}
	if err := checkRun(p.Run); err != nil {
		return buildplan.Plan{}, err
	}
	if err := checkSandboxedRelease(p.Release); err != nil {
		return buildplan.Plan{}, err
	}
	if err := checkStopGrace(p.StopGrace); err != nil {
		return buildplan.Plan{}, err
	}
	procs := map[string]processConfig{}
	for _, pr := range p.Processes {
		procs[pr.Name] = processConfig{Run: pr.Run, Memory: pr.Memory, CPU: pr.CPU, StopGrace: pr.StopGrace}
	}
	if err := checkProcesses(procs); err != nil {
		return buildplan.Plan{}, err
	}
	return p, nil
}
