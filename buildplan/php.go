package buildplan

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// phpVersion: whether composer.json's require.php allows the PHP homeport
// runs (PHPVersion, a line: any of its releases). Read as Composer reads a
// constraint - , or a space is and, | or || is or, ~8.4 is ^8.4 - with the
// version ranges npm's share (semver.go). One this can't read is left to the
// build's composer check-platform-reqs.
func (r reader) phpVersion() error {
	b, err := r.read("composer.json")
	if err != nil {
		return nil
	}
	var c struct {
		Require map[string]any `json:"require"`
	}
	if json.Unmarshal(b, &c) != nil {
		return nil
	}
	asked, _ := c.Require["php"].(string)
	if strings.TrimSpace(asked) == "" {
		return nil
	}
	rng, err := parseRange(composerRange(asked))
	if err != nil {
		return nil
	}
	line := mustVersion(PHPVersion + ".0")
	for patch := range 100 {
		if v := (semver{line[0], line[1], patch}); rng.has(v) {
			return nil
		}
	}
	return fmt.Errorf("composer.json requires PHP %q: homeport runs PHP %s", asked, PHPVersion)
}

// ~ with two parts: Composer's lets the minor rise (~8.4 is >=8.4 <9),
// npm's doesn't
var composerTilde = regexp.MustCompile(`~\s*(v?\d+\.\d+)(\s|,|\||$)`)

func composerRange(c string) string {
	c = composerTilde.ReplaceAllString(c, "^$1$2")
	c = strings.ReplaceAll(c, "||", "|")
	c = strings.ReplaceAll(c, "|", "||")
	return strings.ReplaceAll(c, ",", " ")
}
