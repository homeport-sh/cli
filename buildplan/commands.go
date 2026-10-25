package buildplan

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"go.yaml.in/yaml/v3"
)

// MaxReleaseCommands is how many release commands an app may have:
// homeportd runs them one after another, in the ten minutes a release gets.
const MaxReleaseCommands = 10

// Commands is an app's release commands, run in order before a release
// goes live, each to success: the first that fails stops the deploy. Read
// as a string (one) or a list of strings, in JSON and homeport.yaml; one is
// written as a string, as every CLI and control plane before read it, and
// several as a list.
type Commands []string

// One is the release command cmd alone; none when it's empty.
func One(cmd string) Commands {
	if cmd == "" {
		return nil
	}
	return Commands{cmd}
}

// Last is the last command, "" for none.
func (c Commands) Last() string {
	if len(c) == 0 {
		return ""
	}
	return c[len(c)-1]
}

// MarshalJSON writes one command as a string, several as a list.
func (c Commands) MarshalJSON() ([]byte, error) {
	if len(c) == 1 {
		return json.Marshal(c[0])
	}
	return json.Marshal([]string(c))
}

// UnmarshalJSON reads a string ("" is none) or a list of strings.
func (c *Commands) UnmarshalJSON(b []byte) error {
	var one string
	if err := json.Unmarshal(b, &one); err == nil {
		*c = One(one)
		return nil
	}
	var many []string
	if err := json.Unmarshal(b, &many); err != nil {
		return errors.New("a release command is a string, or a list of strings")
	}
	*c = many
	return nil
}

// UnmarshalYAML reads a string or a list of strings, as written: "" is
// [""] and [] empty (homeport.yaml's "there's none", fileNone).
func (c *Commands) UnmarshalYAML(n *yaml.Node) error {
	switch n.Kind {
	case yaml.ScalarNode:
		var one string
		if err := n.Decode(&one); err != nil {
			return err
		}
		*c = Commands{one}
		return nil
	case yaml.SequenceNode:
		out := Commands{}
		for _, item := range n.Content {
			if item.Kind != yaml.ScalarNode || item.Tag != "!!str" && item.Tag != "!!int" && item.Tag != "!!float" && item.Tag != "!!bool" {
				return fmt.Errorf("line %d: release: a command, or a list of commands (each a string)", item.Line)
			}
			out = append(out, item.Value)
		}
		*c = out
		return nil
	}
	return fmt.Errorf("line %d: release: a command, or a list of commands", n.Line)
}

// CheckReleases says whether commands are release commands the sandbox can
// run, each by CheckRelease: at most MaxReleaseCommands, none empty, one
// that isn't named by its place when there are several.
func CheckReleases(commands Commands) error {
	if len(commands) > MaxReleaseCommands {
		return fmt.Errorf("release commands: at most %d, run one after another (there are %d)", MaxReleaseCommands, len(commands))
	}
	for i, cmd := range commands {
		what := "release command"
		if len(commands) > 1 {
			what = fmt.Sprintf("release command %d", i+1)
		}
		if err := checkText(what, cmd); err != nil {
			return err
		}
		if strings.TrimSpace(cmd) == "" {
			return fmt.Errorf("%s: empty - give it a command, or remove it", what)
		}
		if err := checkArgs(what, cmd); err != nil {
			return err
		}
	}
	return nil
}

// Trimmed is c with the spaces around each command cut, the empty ones
// dropped: a list as a person typed it.
func (c Commands) Trimmed() Commands {
	var out Commands
	for _, cmd := range c {
		if cmd = strings.TrimSpace(cmd); cmd != "" {
			out = append(out, cmd)
		}
	}
	return out
}

// String is the commands for people: one as it is, several in order.
func (c Commands) String() string { return strings.Join(c, ", then ") }
