package envfile_test

import (
	"testing"

	"github.com/homeport-sh/cli/envfile"
)

func TestNames(t *testing.T) {
	for name, want := range map[string]bool{
		"DATABASE_URL": true, "_PRIVATE": true, "lower_ok": true, "A1": true,
		"": false, "1ABC": false, "WITH-DASH": false, "WITH SPACE": false, "A=B": false, "Ä": false,
	} {
		if got := envfile.ValidName(name); got != want {
			t.Errorf("%q: %v, want %v", name, got, want)
		}
	}
}

// Names that change how a shell or the dynamic linker behaves must be refused
// before they ever reach a host: homeportd (a bash script) runs as root to
// build gVisor sandboxes, and a value named BASH_ENV or LD_PRELOAD would run
// as root if it entered that process's environment (the C1 escalation). PATH
// stays allowed — it is legitimate for an app, and never enters root context.
func TestReservedNamesAreRefused(t *testing.T) {
	for name, reserved := range map[string]bool{
		"BASH_ENV": true, "ENV": true, "SHELLOPTS": true, "PS4": true, "IFS": true,
		"GCONV_PATH": true, "LD_PRELOAD": true, "LD_LIBRARY_PATH": true,
		"BASH_FUNC_x": true, "HOMEPORT_ROOT": true, "SANDBOX_CGROUP": true,
		// legitimate, must stay allowed
		"PATH": false, "DATABASE_URL": false, "NODE_ENV": false, "LDAP_URL": false, "ENVIRONMENT": false,
	} {
		if got := envfile.Reserved(name); got != reserved {
			t.Errorf("Reserved(%q) = %v, want %v", name, got, reserved)
		}
		_, err := envfile.Line(name, "x")
		if reserved && err == nil {
			t.Errorf("Line accepted reserved name %q", name)
		}
		if !reserved && err != nil {
			t.Errorf("Line refused legitimate name %q: %v", name, err)
		}
	}
}

func TestEncodingEscapesExactlyWhatHomeportdUnescapes(t *testing.T) {
	for in, want := range map[string]string{
		"plain":                `"plain"`,
		"":                     `""`,
		`back\slash`:           `"back\\slash"`,
		`quo"te`:               `"quo\"te"`,
		"tick`cmd`":            "\"tick\\`cmd\\`\"",
		"$HOME and ${X}":       `"\$HOME and \${X}"`,
		"  spaced  ":           `"  spaced  "`,
		"it's # not a comment": `"it's # not a comment"`,
	} {
		if got := envfile.Encode(in); got != want {
			t.Errorf("Encode(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestALineIsOneLine(t *testing.T) {
	if l, err := envfile.Line("KEY", `v"1`); err != nil || l != `KEY="v\"1"` {
		t.Fatalf("%q, %v", l, err)
	}
	for name, val := range map[string]string{
		"bad name": "x", "newline": "a\nb", "carriage return": "a\rb", "nul": "a\x00b",
	} {
		n := "KEY"
		if name == "bad name" {
			n = "1BAD"
		}
		if _, err := envfile.Line(n, val); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
