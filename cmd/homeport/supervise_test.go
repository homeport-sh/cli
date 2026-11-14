package main

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

// homeport supervise is the supervisor a bundle's web runs through
// (package supervise), hidden from the help: its exit is the command's,
// and it reads no config, signed in or not.
func TestSuperviseRunsTheSupervisor(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("HOME", t.TempDir())
	var out, errb bytes.Buffer
	a := &app{in: strings.NewReader(""), out: &out, err: &errb, wd: t.TempDir()}
	if code := a.run([]string{"supervise"}); code != 2 {
		t.Errorf("exit %d:\n%s", code, errb.String())
	}
	if !strings.Contains(errb.String(), "homeport: supervise: nothing to run") {
		t.Errorf("not supervised:\n%s", errb.String())
	}
	if ents, _ := os.ReadDir(os.Getenv("HOME")); len(ents) != 0 {
		t.Errorf("wrote %v in HOME", ents)
	}
	a.usage()
	if strings.Contains(out.String(), "supervise") {
		t.Error("in the help")
	}
}
