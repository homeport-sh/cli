package source

import (
	"regexp"
	"strings"
)

// ignorer is .gitignore's rules, for a folder that isn't a git checkout
// (in one, git itself says what's ignored). Each file's patterns speak for
// its own folder; the last that matches decides, and a ! pattern takes a
// path back.
type ignorer struct{ rules []rule }

type rule struct {
	base    string // the folder of the .gitignore it came from ("" the root)
	re      *regexp.Regexp
	negate  bool
	dirOnly bool
}

// add reads the patterns of the .gitignore in base.
func (m *ignorer) add(base, patterns string) {
	for _, line := range strings.Split(patterns, "\n") {
		line = strings.TrimRight(line, "\r")
		// trailing spaces are dropped unless escaped
		for strings.HasSuffix(line, " ") && !strings.HasSuffix(line, `\ `) {
			line = line[:len(line)-1]
		}
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		r := rule{base: base}
		if strings.HasPrefix(line, "!") {
			r.negate, line = true, line[1:]
		} else if strings.HasPrefix(line, `\`) {
			line = line[1:]
		}
		if strings.HasSuffix(line, "/") {
			r.dirOnly, line = true, strings.TrimRight(line, "/")
		}
		if line == "" {
			continue
		}
		// a slash anywhere but the end anchors it to its folder; else it
		// matches a name at any depth
		anchored := strings.Contains(line, "/")
		line = strings.TrimPrefix(line, "/")
		expr := globRegexp(line)
		if !anchored {
			expr = "(?:.*/)?" + expr
		}
		re, err := regexp.Compile("^" + expr + "$")
		if err != nil {
			continue // a pattern git would take as nothing
		}
		r.re = re
		m.rules = append(m.rules, r)
	}
}

// ignored reports whether rel (slash-separated, from the root) is ignored.
// The caller walks top-down and doesn't descend into an ignored folder: as
// in git, nothing inside one can be taken back.
func (m *ignorer) ignored(rel string, dir bool) bool {
	ignored := false
	for _, r := range m.rules {
		p := rel
		if r.base != "" {
			var ok bool
			if p, ok = strings.CutPrefix(rel, r.base+"/"); !ok {
				continue
			}
		}
		if r.dirOnly && !dir {
			continue
		}
		if r.re.MatchString(p) {
			ignored = !r.negate
		}
	}
	return ignored
}

// globRegexp is a gitignore glob as a regular expression: * and ? within a
// name, [...] a class, ** across folders.
func globRegexp(glob string) string {
	var b strings.Builder
	for i := 0; i < len(glob); i++ {
		c := glob[i]
		switch {
		case strings.HasPrefix(glob[i:], "**/"):
			b.WriteString("(?:.*/)?")
			i += 2
		case strings.HasPrefix(glob[i:], "/**") && i+3 == len(glob):
			b.WriteString("/.*")
			i += 2
		case strings.HasPrefix(glob[i:], "**"):
			b.WriteString(".*")
			i++
		case c == '*':
			b.WriteString("[^/]*")
		case c == '?':
			b.WriteString("[^/]")
		case c == '[':
			end := strings.IndexByte(glob[i+1:], ']')
			if end < 0 {
				b.WriteString(`\[`)
				continue
			}
			class := glob[i+1 : i+1+end]
			if strings.HasPrefix(class, "!") {
				class = "^" + class[1:]
			}
			b.WriteString("[" + strings.ReplaceAll(class, `\`, `\\`) + "]")
			i += end + 1
		case c == '\\' && i+1 < len(glob):
			i++
			b.WriteString(regexp.QuoteMeta(string(glob[i])))
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	return b.String()
}

// Covers reports whether a .gitignore's patterns ignore rel (a file in the
// .gitignore's folder or below it).
func Covers(patterns, rel string) bool {
	m := &ignorer{}
	m.add("", patterns)
	return m.ignored(rel, false)
}
