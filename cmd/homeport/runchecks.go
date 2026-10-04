package main

import (
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
)

// How a hosted app runs is checked against the sandbox's rules before a
// build is shipped: its run args, its release command and its processes are
// exec'd without a shell, inside the sandbox homeportd starts.

const configFile = "homeport.yaml"

var (
	memoryRe = regexp.MustCompile(`^[0-9]+[KMG]$`)
	cpuRe    = regexp.MustCompile(`^[0-9]+%$`)
	// run: launch args appended to the binary. exec (no shell), so no shell
	// metachars; only $PORT/$HOST are substituted server-side.
	runRe = regexp.MustCompile(`^[A-Za-z0-9 ._:/=@,+${}-]*$`)
	// args to ./bin exec'd without a shell and with no variables: a
	// process's run, and a sandboxed release command
	binArgsRe  = regexp.MustCompile(`^[A-Za-z0-9 ._:/=@,+-]+$`)
	procNameRe = regexp.MustCompile(`^[a-z][a-z0-9]{0,14}$`)
	runVarRe   = regexp.MustCompile(`\$(\{(PORT|HOST)\}|(PORT|HOST)\b)`)
)

// processConfig is one long-running command beside the web.
type processConfig struct {
	Run    string
	Memory string
	CPU    string
}

// maxProcesses matches homeportd's PROC_MAX: an app's process range has a
// slot for the release command and four processes.
const maxProcesses = 4

// checkSandboxedRelease: a sandboxed release command runs in a sandbox of its
// own, which has no shell, so it's args to ./bin.
func checkSandboxedRelease(release string) error {
	switch {
	case release == "":
		return nil
	case strings.HasPrefix(strings.TrimSpace(release), "./bin"):
		return fmt.Errorf("%s: a sandboxed release command is args to ./bin — write `%s`, not %q",
			configFile, strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(release), "./bin")), release)
	case !binArgsRe.MatchString(release):
		return fmt.Errorf("%s: a sandboxed release command is args to ./bin, run without a shell (letters, digits, spaces, . _ : / = @ , + - only; no && or variables)", configFile)
	}
	return nil
}

// checkRun: run is args to ./bin that may name only $PORT and $HOST.
func checkRun(run string) error {
	switch {
	case run != "" && !runRe.MatchString(run):
		return fmt.Errorf("%s: run has unsupported characters (letters, digits, spaces, . _ : / = @ , + - ${} only)", configFile)
	case run != "" && strings.Contains(runVarRe.ReplaceAllString(run, ""), "$"):
		return fmt.Errorf("%s: run may only reference $PORT and $HOST, no other variables", configFile)
	}
	return nil
}

// checkProcesses: each process is args to ./bin with sane limits.
func checkProcesses(procs map[string]processConfig) error {
	if len(procs) > maxProcesses {
		return fmt.Errorf("%s: at most %d processes", configFile, maxProcesses)
	}
	for _, name := range slices.Sorted(maps.Keys(procs)) {
		p := procs[name]
		switch {
		case !procNameRe.MatchString(name) || name == "web" || name == "release":
			return fmt.Errorf("%s: process name %q: lowercase letters and digits, max 15, not web or release", configFile, name)
		case p.Run == "":
			return fmt.Errorf("%s: process %q needs run: its args to ./bin", configFile, name)
		case !binArgsRe.MatchString(p.Run):
			return fmt.Errorf("%s: process %q runs args to ./bin, without a shell (letters, digits, spaces, . _ : / = @ , + - only; no ; && or variables)", configFile, name)
		case p.Memory != "" && !memoryRe.MatchString(p.Memory):
			return fmt.Errorf("%s: process %q memory must be like 256M or 1G", configFile, name)
		case p.CPU != "" && !cpuRe.MatchString(p.CPU):
			return fmt.Errorf("%s: process %q cpu must be like 50%%", configFile, name)
		}
	}
	return nil
}
