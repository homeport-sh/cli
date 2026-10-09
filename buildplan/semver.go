package buildplan

import (
	"errors"
	"strconv"
	"strings"
)

// A small reading of npm's version ranges (engines.node, engines.bun):
// enough to tell which pinned release one allows. Comparators (>= > <= < =),
// ^ ~, x-ranges (22.x, 22, *), hyphens (20 - 22), and || between sets.

type semver [3]int

func (a semver) cmp(b semver) int {
	for i := range 3 {
		if a[i] != b[i] {
			if a[i] < b[i] {
				return -1
			}
			return 1
		}
	}
	return 0
}

func mustVersion(v string) semver {
	s, n, err := partial(v)
	if err != nil || n != 3 {
		panic("buildplan: bad version " + v)
	}
	return s
}

// partial parses 22, 22.1, 22.1.3 (an x or * ends it), and says how many
// parts it had.
func partial(v string) (semver, int, error) {
	var s semver
	v = strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(v), "="), "v")
	if v == "" || v == "*" || v == "x" || v == "X" {
		return s, 0, nil
	}
	parts := strings.Split(v, ".")
	if len(parts) > 3 {
		return s, 0, errors.New("not a version")
	}
	n := 0
	for i, p := range parts {
		if p == "x" || p == "X" || p == "*" {
			break
		}
		p, _, _ = strings.Cut(p, "-") // a prerelease: its version
		x, err := strconv.Atoi(p)
		if err != nil || x < 0 {
			return s, 0, errors.New("not a version")
		}
		s[i], n = x, i+1
	}
	return s, n, nil
}

// bound: one comparator; op is >=, >, <=, < or =.
type bound struct {
	op string
	v  semver
}

func (b bound) ok(v semver) bool {
	c := v.cmp(b.v)
	switch b.op {
	case ">=":
		return c >= 0
	case ">":
		return c > 0
	case "<=":
		return c <= 0
	case "<":
		return c < 0
	}
	return c == 0
}

type versionRange [][]bound // any set, every bound in it

func (r versionRange) has(v semver) bool {
	for _, set := range r {
		ok := true
		for _, b := range set {
			ok = ok && b.ok(v)
		}
		if ok {
			return true
		}
	}
	return false
}

// up: the version past a partial one's last given part (22 -> 23.0.0).
func up(s semver, n int) semver {
	if n == 0 {
		return semver{1 << 30}
	}
	out := semver{}
	copy(out[:n], s[:n])
	out[n-1]++
	return out
}

func parseRange(r string) (versionRange, error) {
	var out versionRange
	for _, alt := range strings.Split(r, "||") {
		f := strings.Fields(alt)
		var set []bound
		if len(f) == 3 && f[1] == "-" {
			lo, _, err := partial(f[0])
			if err != nil {
				return nil, err
			}
			hi, n, err := partial(f[2])
			if err != nil {
				return nil, err
			}
			set = append(set, bound{">=", lo})
			if n == 3 {
				set = append(set, bound{"<=", hi})
			} else {
				set = append(set, bound{"<", up(hi, n)})
			}
			out = append(out, set)
			continue
		}
		// ">= 20" is ">=20"
		for i := 0; i < len(f); i++ {
			if strings.Trim(f[i], "<>=^~") == "" && i+1 < len(f) {
				f[i+1] = f[i] + f[i+1]
				f = append(f[:i], f[i+1:]...)
			}
		}
		for _, c := range f {
			op := ""
			for _, o := range []string{">=", "<=", ">", "<", "^", "~", "="} {
				if strings.HasPrefix(c, o) {
					op, c = o, strings.TrimPrefix(c, o)
					break
				}
			}
			v, n, err := partial(c)
			if err != nil {
				return nil, err
			}
			switch op {
			case "", "=":
				if n == 3 {
					set = append(set, bound{"=", v})
				} else {
					set = append(set, bound{">=", v}, bound{"<", up(v, n)})
				}
			case "^":
				set = append(set, bound{">=", v})
				switch {
				case n == 0:
				case v[0] > 0 || n == 1:
					set = append(set, bound{"<", up(v, 1)})
				case v[1] > 0 || n == 2:
					set = append(set, bound{"<", up(v, 2)})
				default:
					set = append(set, bound{"<", up(v, 3)})
				}
			case "~":
				set = append(set, bound{">=", v})
				if n > 0 {
					set = append(set, bound{"<", up(v, min(n, 2))})
				}
			case ">", "<=":
				if n < 3 && n > 0 {
					// >20 is >=21; <=20 is <21
					if op == ">" {
						set = append(set, bound{">=", up(v, n)})
					} else {
						set = append(set, bound{"<", up(v, n)})
					}
				} else {
					set = append(set, bound{op, v})
				}
			default:
				set = append(set, bound{op, v})
			}
		}
		out = append(out, set)
	}
	if len(out) == 0 {
		return nil, errors.New("no range")
	}
	return out, nil
}
