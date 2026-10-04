package main

import (
	"strings"
	"testing"
)

// How a hosted app runs is exec'd in its sandbox without a shell: run may
// name only $PORT and $HOST, a release is args to ./bin, processes are named
// and bounded.
func TestHowAnAppRunsIsCheckedForTheSandbox(t *testing.T) {
	for run, ok := range map[string]bool{
		"":                           true,
		"serve --port $PORT":         true,
		"serve --addr ${HOST}:$PORT": true,
		"serve $SECRET":              false,
		"serve; rm -rf /":            false,
	} {
		if err := checkRun(run); (err == nil) != ok {
			t.Errorf("run %q: %v", run, err)
		}
	}
	for release, ok := range map[string]bool{
		"":                true,
		"migrate --force": true,
		"./bin migrate":   false,
		"migrate && seed": false,
	} {
		if err := checkSandboxedRelease(release); (err == nil) != ok {
			t.Errorf("release %q: %v", release, err)
		}
	}
	good := map[string]processConfig{"worker": {Run: "queue:work", Memory: "256M", CPU: "50%"}}
	if err := checkProcesses(good); err != nil {
		t.Errorf("a worker: %v", err)
	}
	for name, procs := range map[string]map[string]processConfig{
		"named web":  {"web": {Run: "x"}},
		"no run":     {"worker": {}},
		"a shell":    {"worker": {Run: "work && more"}},
		"bad memory": {"worker": {Run: "x", Memory: "lots"}},
		"too many":   {"a": {Run: "x"}, "b": {Run: "x"}, "c": {Run: "x"}, "d": {Run: "x"}, "e": {Run: "x"}},
	} {
		if err := checkProcesses(procs); err == nil || !strings.Contains(err.Error(), configFile) {
			t.Errorf("%s: %v", name, err)
		}
	}
}
