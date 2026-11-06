package buildplan_test

import (
	"testing"

	"github.com/homeport-sh/cli/buildplan"
)

// A default environment homeportd takes: names an app may set, one line
// each, naming no variable but $PORT and $HOST.
func TestCheckEnv(t *testing.T) {
	for _, ok := range []map[string]string{
		nil,
		{"ROCKET_ADDRESS": "$HOST", "ROCKET_PORT": "$PORT"},
		{"A": "${PORT}x", "B": "http://${HOST}:$PORT/", "C": ""},
	} {
		if err := buildplan.CheckEnv(ok); err != nil {
			t.Errorf("%v: %v", ok, err)
		}
	}
	for _, bad := range []map[string]string{
		{"LD_PRELOAD": "x"},
		{"HOMEPORT_X": "x"},
		{"1A": "x"},
		{"A": "a\nb"},
		{"A": "$HOME"},
		{"A": "$PORTS"},
		{"A": "$(id)"},
	} {
		if err := buildplan.CheckEnv(bad); err == nil {
			t.Errorf("%v: taken", bad)
		}
	}
}
