package buildplan

import (
	"regexp"
	"strings"
)

// A small reading of TOML (pyproject.toml, Cargo.toml, rust-toolchain.toml):
// a table's body by its header, and a key's string or array of strings in
// it. Enough for the keys detection reads; anything else is left alone.

var (
	// a table's header, [name] or an array of tables' [[name]]
	tomlHeadRe = regexp.MustCompile(`(?m)^\s*\[\[?[^\[\]\n]+\]\]?\s*(#.*)?$`)
	tomlKeyRe  = func(k string) *regexp.Regexp { return regexp.MustCompile(`(?m)^\s*` + regexp.QuoteMeta(k) + `\s*=\s*`) }
	tomlStrRe  = regexp.MustCompile(`^"([^"\n]*)"|^'([^'\n]*)'`)
)

// tomlSections are the bodies of every table with this header, in order,
// each from its header to the next: one for a [table], each of an
// [[array]]'s.
func tomlSections(b []byte, header string) []string {
	s := string(b)
	var out []string
	for _, loc := range tomlHeadRe.FindAllStringIndex(s, -1) {
		if strings.TrimSpace(strings.SplitN(s[loc[0]:loc[1]], "#", 2)[0]) != header {
			continue
		}
		rest := s[loc[1]:]
		if next := tomlHeadRe.FindStringIndex(rest); next != nil {
			rest = rest[:next[0]]
		}
		out = append(out, rest)
	}
	return out
}

// tomlSection is a table's body, from its header to the next.
func tomlSection(b []byte, header string) string {
	if s := tomlSections(b, header); len(s) > 0 {
		return s[0]
	}
	return ""
}

// tomlHas says whether a table with this header is there, empty or not.
func tomlHas(b []byte, header string) bool { return tomlSections(b, header) != nil }

// tomlString is a key's string value in a table's body.
func tomlString(section, key string) (string, bool) {
	loc := tomlKeyRe(key).FindStringIndex(section)
	if loc == nil {
		return "", false
	}
	m := tomlStrRe.FindStringSubmatch(section[loc[1]:])
	if m == nil {
		return "", false
	}
	return m[1] + m[2], true
}

// tomlStrings are a key's array of strings in a table's body.
func tomlStrings(section, key string) []string {
	loc := tomlKeyRe(key).FindStringIndex(section)
	if loc == nil {
		return nil
	}
	s := section[loc[1]:]
	if !strings.HasPrefix(s, "[") {
		return nil
	}
	var out []string
	for i := 1; i < len(s); i++ {
		switch c := s[i]; c {
		case ']':
			return out
		case '#':
			for i < len(s) && s[i] != '\n' {
				i++
			}
		case '"', '\'':
			end := strings.IndexByte(s[i+1:], c)
			if end < 0 {
				return out
			}
			out = append(out, s[i+1:i+1+end])
			i += end + 1
		}
	}
	return out
}
