package main

import (
	"fmt"
	"maps"
	"slices"

	"github.com/homeport-sh/cli/buildplan"
)

// How a hosted app runs is checked against the sandbox's rules before a
// build is shipped: its run args, its release command and its processes are
// exec'd without a shell, inside the sandbox homeportd starts. The rules are
// buildplan's (settings from the UI are held to them too); here they're said
// about homeport.yaml, where the rest of a plan's run comes from.

const configFile = "homeport.yaml"

// processConfig is one long-running command beside the web.
type processConfig struct {
	Run    string
	Memory string
	CPU    string
}

func inConfig(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", configFile, err)
}

// checkSandboxedRelease: a sandboxed release command runs in a sandbox of its
// own, which has no shell, so it's args to ./bin.
func checkSandboxedRelease(release string) error { return inConfig(buildplan.CheckRelease(release)) }

// checkRun: run is args to ./bin that may name only $PORT and $HOST.
func checkRun(run string) error { return inConfig(buildplan.CheckRun(run)) }

// checkProcesses: each process is args to ./bin with sane limits.
func checkProcesses(procs map[string]processConfig) error {
	list := make([]buildplan.Process, 0, len(procs))
	for _, name := range slices.Sorted(maps.Keys(procs)) {
		p := procs[name]
		list = append(list, buildplan.Process{Name: name, Run: p.Run, Memory: p.Memory, CPU: p.CPU})
	}
	return inConfig(buildplan.CheckProcesses(list))
}
