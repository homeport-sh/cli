package deps

import (
	"bytes"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

// homeportd links this package and starts on every SSH call: its
// expressions are built on first use, not at start (Go's init trace of
// this test binary).
func TestDepsDoesNearlyNothingAtStart(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), "GODEBUG=inittrace=1")
	var errOut bytes.Buffer
	cmd.Stderr = &errOut
	if err := cmd.Run(); err != nil {
		t.Fatalf("%v: %s", err, errOut.String())
	}
	for _, line := range strings.Split(errOut.String(), "\n") {
		// init github.com/homeport-sh/cli/deps @4.1 ms, 0.001 ms clock, 984 bytes, 4 allocs
		f := strings.Fields(line)
		if len(f) == 11 && f[0] == "init" && f[1] == "github.com/homeport-sh/cli/deps" {
			if n, err := strconv.Atoi(f[9]); err != nil || n >= 10 {
				t.Fatalf("deps allocates %s times at start, want under 10", f[9])
			}
			return
		}
	}
}
