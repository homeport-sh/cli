package buildplan

import (
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

// HeaderRule is the response headers for the paths under Path: a prefix on
// segment boundaries ("/" is every path; /a is /a and /a/…, not /ab). The
// longest prefix that matches a request is the one rule its answer gets -
// rules never merge - and what it sets replaces the app's own value; Remove
// takes a header the app sends out of the answer. homeportd applies them on
// the host (design/headers.md in homeport-sh/cloud).
type HeaderRule struct {
	Path   string            `json:"path" yaml:"path"`
	Set    map[string]string `json:"set,omitempty" yaml:"set"`
	Remove []string          `json:"remove,omitempty" yaml:"remove"`
}

// The limits homeportd holds the wire to (headers_parse).
const (
	MaxHeaderRules   = 16
	MaxHeadersSet    = 32
	MaxHeadersRemove = 16
	MaxHeaderValue   = 8 << 10
	MaxHeadersWire   = 48 << 10
	maxHeaderPath    = 256
)

var (
	headerPathRe = regexp.MustCompile(`^(/[A-Za-z0-9._~-]+)+$`)
	headerNameRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9-]{0,63}$`)
	// what's never set or removed: framing, what Caddy sets itself (encode,
	// file_server, HTTP/3), the app's own state, and homeport's; defer and
	// match are Caddy's header block's own words
	refusedHeaders = []string{"set-cookie", "content-length", "transfer-encoding", "connection", "keep-alive",
		"proxy-connection", "te", "trailer", "upgrade", "content-encoding", "vary", "content-range", "accept-ranges",
		"etag", "last-modified", "date", "alt-svc", "location", "x-origin-auth", "x-robots-tag", "defer", "match"}
	// the edge removes these already: removing them is allowed, setting isn't
	unsettableHeaders = []string{"server", "via"}
)

// CheckHeaders says whether rules are ones homeportd takes: the same limits
// and refusals as its headers_parse, so a deploy is refused with the reason
// before it reaches a host.
func CheckHeaders(rules []HeaderRule) error {
	if len(rules) > MaxHeaderRules {
		return fmt.Errorf("at most %d header rules", MaxHeaderRules)
	}
	paths := map[string]bool{}
	for _, r := range rules {
		if err := checkHeaderPath(r.Path); err != nil {
			return err
		}
		if paths[strings.ToLower(r.Path)] {
			return fmt.Errorf("headers: %s is given twice (paths match in any case)", r.Path)
		}
		paths[strings.ToLower(r.Path)] = true
		if len(r.Set) == 0 && len(r.Remove) == 0 {
			return fmt.Errorf("headers: %s sets and removes nothing", r.Path)
		}
		if len(r.Set) > MaxHeadersSet {
			return fmt.Errorf("headers: at most %d headers set for %s", MaxHeadersSet, r.Path)
		}
		if len(r.Remove) > MaxHeadersRemove {
			return fmt.Errorf("headers: at most %d headers removed for %s", MaxHeadersRemove, r.Path)
		}
		names := map[string]bool{}
		for name, value := range r.Set {
			if err := checkHeaderName(name, true); err != nil {
				return err
			}
			if names[strings.ToLower(name)] {
				return fmt.Errorf("headers: %s is given twice for %s", name, r.Path)
			}
			names[strings.ToLower(name)] = true
			if err := checkHeaderValue(name, value); err != nil {
				return err
			}
		}
		for _, name := range r.Remove {
			if err := checkHeaderName(name, false); err != nil {
				return err
			}
			if names[strings.ToLower(name)] {
				return fmt.Errorf("headers: %s is given twice for %s", name, r.Path)
			}
			names[strings.ToLower(name)] = true
		}
	}
	if n := len(HeadersWire(rules)); n > MaxHeadersWire {
		return fmt.Errorf("headers: %d KiB in all, more than the %d KiB a host takes", (n+1023)>>10, MaxHeadersWire>>10)
	}
	return nil
}

func checkHeaderPath(p string) error {
	if len(p) > maxHeaderPath || (p != "/" && !headerPathRe.MatchString(p)) {
		return fmt.Errorf("headers: path %q must be / or segments of letters, digits and . _ ~ - (no * or trailing /), at most %d characters", p, maxHeaderPath)
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "." || seg == ".." {
			return fmt.Errorf("headers: path %q may not have a . or .. segment", p)
		}
	}
	return nil
}

func checkHeaderName(name string, set bool) error {
	if !headerNameRe.MatchString(name) {
		return fmt.Errorf("headers: %q isn't a header's name (a letter, then letters, digits and -, at most 64)", name)
	}
	l := strings.ToLower(name)
	if slices.Contains(refusedHeaders, l) || strings.HasPrefix(l, "x-homeport-") {
		return fmt.Errorf("headers: %s is homeport's or the app's own, and can't be set or removed", name)
	}
	if set && slices.Contains(unsettableHeaders, l) {
		return fmt.Errorf("headers: %s can't be set (it can be removed)", name)
	}
	return nil
}

func checkHeaderValue(name, v string) error {
	if len(v) == 0 || len(v) > MaxHeaderValue {
		return fmt.Errorf("headers: %s's value must be 1 to %d characters", name, MaxHeaderValue)
	}
	for _, c := range []byte(v) {
		if c < ' ' || c > '~' {
			return fmt.Errorf("headers: %s's value must be printable ASCII on one line", name)
		}
	}
	if v[0] == ' ' || v[len(v)-1] == ' ' {
		return fmt.Errorf("headers: %s's value has spaces around it", name)
	}
	if strings.ContainsAny(v, "`{}") {
		return fmt.Errorf("headers: %s's value may not hold ` { or }", name)
	}
	return nil
}

// HeadersWire is rules as homeportd takes them (add's 19th argument, before
// base64): a "v2" line, then each rule's sets by name and its removes, in
// the rules' order; "" for none. Checked rules make a wire homeportd takes.
func HeadersWire(rules []HeaderRule) string {
	if len(rules) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("v2\n")
	for _, r := range rules {
		for _, name := range slices.Sorted(maps.Keys(r.Set)) {
			fmt.Fprintf(&b, "%s\tset\t%s\t%s\n", r.Path, name, r.Set[name])
		}
		for _, name := range r.Remove {
			fmt.Fprintf(&b, "%s\tremove\t%s\n", r.Path, name)
		}
	}
	return b.String()
}

// FileHeaders is homeport.yaml's header rules alone, checked; nil when it
// has none. A deploy no builder planned (a CI upload) is served with them,
// read by the control plane from the commit.
func FileHeaders(b []byte) ([]HeaderRule, error) {
	var cfg struct {
		Headers []HeaderRule `yaml:"headers"`
	}
	if err := yaml.Unmarshal(b, &cfg); err != nil {
		return nil, fmt.Errorf("%s: %w", ConfigFile, err)
	}
	if err := CheckHeaders(cfg.Headers); err != nil {
		return nil, fmt.Errorf("%s: %w", ConfigFile, err)
	}
	if len(cfg.Headers) == 0 {
		return nil, nil
	}
	return cfg.Headers, nil
}
