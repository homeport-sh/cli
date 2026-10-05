// Package envfile is homeportd's env file format, for Go: the names it
// accepts and the canonical KEY="value" encoding it stores, so systemd and
// bash read every value identically (see env_encode_value in homeportd).
// Shared by the homeport CLI and homeport cloud, which both send env to
// homeportd's env-sync.
package envfile

import (
	"fmt"
	"regexp"
	"strings"
)

var name = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// ValidName reports whether name is a well-formed env var name (letters,
// digits and _, not starting with a digit). It says nothing about whether the
// name is safe to accept — see Reserved.
func ValidName(n string) bool { return name.MatchString(n) }

// reservedExact are names that must never be set on an app: each changes how a
// shell or the C runtime behaves, and homeportd runs as root (a bash script
// building gVisor sandboxes) where such a name in the environment would run
// the app's code as root (the C1 escalation). PATH is deliberately NOT here:
// it is legitimate for an app, and the sandbox gives the app's process a fixed
// PATH of its own, so a value set here never reaches a root shell.
var reservedExact = map[string]bool{
	"BASH_ENV": true, "ENV": true, "SHELLOPTS": true, "BASHOPTS": true,
	"PS4": true, "IFS": true, "GCONV_PATH": true,
}

// reservedPrefix are name prefixes that must never be set, for the same reason:
// the dynamic linker's LD_*, exported bash functions (BASH_FUNC_*), and
// homeport's own HOMEPORT_*/SANDBOX_* which steer the engine.
var reservedPrefix = []string{"LD_", "BASH_FUNC_", "HOMEPORT_", "SANDBOX_"}

// Reserved reports whether name is one homeportd must refuse however well
// formed it is. The same denylist is enforced in homeportd itself; this is the
// server-side guard, so a reserved name never reaches a host.
func Reserved(n string) bool {
	if reservedExact[n] {
		return true
	}
	for _, p := range reservedPrefix {
		if strings.HasPrefix(n, p) {
			return true
		}
	}
	return false
}

// Encode is homeportd's canonical form of a value: double-quoted, with
// exactly \ " ` $ escaped.
func Encode(v string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "`", "\\`", `$`, `\$`).Replace(v) + `"`
}

// Line is one env-sync line. Values travel one per line, so a newline,
// carriage return or NUL can't be sent and is refused rather than mangled.
func Line(n, v string) (string, error) {
	if !ValidName(n) {
		return "", fmt.Errorf("invalid env var name %q (letters, digits and _, not starting with a digit)", n)
	}
	if Reserved(n) {
		return "", fmt.Errorf("env var name %q is reserved: it would change how a shell or the dynamic linker behaves", n)
	}
	if strings.ContainsAny(v, "\n\r\x00") {
		return "", fmt.Errorf("%s: a value can't contain a newline, carriage return or NUL", n)
	}
	return n + "=" + Encode(v), nil
}
