package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

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
		case args[0] == "--settings" && len(args) > 1:
			settings, args = args[1], args[2:]
		case len(args) == 1:
			dir, args = args[0], nil
		default:
			return errors.New("usage: homeport build-plan [--settings <file.json>] [dir]")
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
	p, err := planBuild(dir, s)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(p)
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
	procs := map[string]processConfig{}
	for _, pr := range p.Processes {
		procs[pr.Name] = processConfig{Run: pr.Run, Memory: pr.Memory, CPU: pr.CPU}
	}
	if err := checkProcesses(procs); err != nil {
		return buildplan.Plan{}, err
	}
	return p, nil
}
