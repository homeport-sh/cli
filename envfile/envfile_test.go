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
