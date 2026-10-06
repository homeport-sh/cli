package buildplan

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// How a hosted app runs is exec'd in its sandbox without a shell - homeportd
// starts its web, its release command and each process as args to the app's
// bin - so what a person sets about it is held to homeportd's own charsets
// (valid_bin_args, valid_proc_name, PROC_MAX), and so is what the control
// plane takes back from a builder.

// MaxProcesses matches homeportd's PROC_MAX: an app's process range has a
// slot for the release command and four processes.
const MaxProcesses = 4

var (
	// run: args to the bin, which may name only $PORT and $HOST (homeportd
	// substitutes them, without a shell)
	runRe    = regexp.MustCompile(`^[A-Za-z0-9 ._:/=@,+${}-]*$`)
	runVarRe = regexp.MustCompile(`\$(\{(PORT|HOST)\}|(PORT|HOST)\b)`)
	// a release command and a process: args to the bin, no variables at all
	binArgsRe   = regexp.MustCompile(`^[A-Za-z0-9 ._:/=@,+-]+$`)
	procNameRe  = regexp.MustCompile(`^[a-z][a-z0-9]{0,14}$`)
	procMemRe   = regexp.MustCompile(`^[0-9]{1,8}[KMG]$`)
	procCPURe   = regexp.MustCompile(`^[0-9]{1,4}%$`)
	binArgsSays = "letters, digits, spaces and . _ : / = @ , + - only; no ; && | or variables"
)

// CheckRun says whether run is args the sandbox can start the app with.
func CheckRun(run string) error {
	switch {
	case run == "":
		return nil
	case len(run) > maxCommandSz:
		return fmt.Errorf("start command: at most %d characters", maxCommandSz)
	case !runRe.MatchString(run):
		return errors.New("start command: args to the app's binary, run without a shell (letters, digits, spaces and . _ : / = @ , + - ${} only)")
	case strings.Contains(runVarRe.ReplaceAllString(run, ""), "$"):
		return errors.New("start command: may name only $PORT and $HOST, no other variables")
	}
	return nil
}

// CheckRelease says whether release is a command the sandbox can run before
// a release goes live: args to the app's bin, in a sandbox of its own that
// has no shell.
func CheckRelease(release string) error {
	r := strings.TrimSpace(release)
	switch {
	case release == "":
		return nil
	case len(release) > maxCommandSz:
		return fmt.Errorf("release command: at most %d characters", maxCommandSz)
	case r == "./bin" || strings.HasPrefix(r, "./bin "):
		return fmt.Errorf("release command: args to the app's binary - write `%s`, not %q", strings.TrimSpace(strings.TrimPrefix(r, "./bin")), release)
	case !binArgsRe.MatchString(release):
		return errors.New("release command: args to the app's binary, run without a shell (" + binArgsSays + ")")
	}
	return nil
}

// CheckProcesses says whether processes are ones the sandbox can run beside
// the web: at most MaxProcesses, each named once (never web or release) and
// args to the app's bin, with its memory and CPU, if any, in their shapes.
func CheckProcesses(procs []Process) error {
	if len(procs) > MaxProcesses {
		return fmt.Errorf("at most %d processes", MaxProcesses)
	}
	seen := map[string]bool{}
	for _, p := range procs {
		switch {
		case !procNameRe.MatchString(p.Name) || p.Name == "web" || p.Name == "release":
			return fmt.Errorf("process name %q: lowercase letters and digits, starting with a letter, max 15, not web or release", p.Name)
		case seen[p.Name]:
			return fmt.Errorf("process %q is there twice", p.Name)
		case p.Run == "":
			return fmt.Errorf("process %q needs a command: its args to the app's binary", p.Name)
		case len(p.Run) > maxCommandSz:
			return fmt.Errorf("process %q: its command is at most %d characters", p.Name, maxCommandSz)
		case !binArgsRe.MatchString(p.Run):
			return fmt.Errorf("process %q: args to the app's binary, without a shell (%s)", p.Name, binArgsSays)
		case p.Memory != "" && !procMemRe.MatchString(p.Memory):
			return fmt.Errorf("process %q: memory must be like 256M or 1G", p.Name)
		case p.CPU != "" && !procCPURe.MatchString(p.CPU):
			return fmt.Errorf("process %q: cpu must be like 50%%", p.Name)
		}
		seen[p.Name] = true
	}
	return nil
}
