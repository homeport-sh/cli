package buildplan_test

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/homeport-sh/cli/buildplan"
)

// cloudtrain's HTTPRoute, as homeport.yaml says it (design/headers.md in
// homeport-sh/cloud): the longest prefix wins, and its rule alone applies
const cloudtrainYAML = `
headers:
  - path: /shopify/embedded
    set:
      Content-Security-Policy: "frame-ancestors https://admin.shopify.com https://*.myshopify.com"
      Strict-Transport-Security: max-age=31536000
  - path: /
    set:
      Strict-Transport-Security: max-age=31536000
      X-Frame-Options: DENY
    remove: [X-Powered-By]
`

var cloudtrain = []buildplan.HeaderRule{
	{Path: "/shopify/embedded", Set: map[string]string{
		"Content-Security-Policy":   "frame-ancestors https://admin.shopify.com https://*.myshopify.com",
		"Strict-Transport-Security": "max-age=31536000",
	}},
	{Path: "/", Set: map[string]string{"Strict-Transport-Security": "max-age=31536000", "X-Frame-Options": "DENY"},
		Remove: []string{"X-Powered-By"}},
}

func headersJSON(t *testing.T, h []buildplan.HeaderRule) string {
	t.Helper()
	b, err := json.Marshal(h)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestHomeportYamlsHeadersAreInThePlan(t *testing.T) {
	p := detect(t, map[string]string{"go.mod": "module x\n\ngo 1.24\n", "homeport.yaml": cloudtrainYAML}, buildplan.Settings{})
	if got, want := headersJSON(t, p.Headers), headersJSON(t, cloudtrain); got != want {
		t.Fatalf("plan's headers:\n got %s\nwant %s", got, want)
	}
	// a static site has them too: nothing of its own can set them
	p = detect(t, map[string]string{"index.html": "<h1>hi</h1>", "homeport.yaml": cloudtrainYAML}, buildplan.Settings{})
	if p.Kind != buildplan.Static || len(p.Headers) != 2 {
		t.Fatalf("a static site's plan: kind %s, headers %v", p.Kind, p.Headers)
	}
}

// Settings' rules, when there are any, replace the file's entirely - as
// processes do - never merged rule by rule
func TestSettingsHeadersReplaceTheFiles(t *testing.T) {
	mine := []buildplan.HeaderRule{{Path: "/", Set: map[string]string{"X-Frame-Options": "SAMEORIGIN"}}}
	p := detect(t, map[string]string{"go.mod": "module x\n\ngo 1.24\n", "homeport.yaml": cloudtrainYAML}, buildplan.Settings{Headers: mine})
	if got, want := headersJSON(t, p.Headers), headersJSON(t, mine); got != want {
		t.Fatalf("got %s want %s", got, want)
	}
	saved := buildplan.Settings{Headers: mine}
	if got := saved.With(buildplan.Settings{Run: "x"}).Headers; headersJSON(t, got) != headersJSON(t, mine) {
		t.Fatalf("one deploy's settings without headers dropped the saved ones: %v", got)
	}
	if got := saved.With(buildplan.Settings{Headers: cloudtrain}).Headers; headersJSON(t, got) != headersJSON(t, cloudtrain) {
		t.Fatalf("one deploy's headers don't replace the saved ones: %v", got)
	}
	if (buildplan.Settings{Headers: mine}).IsZero() {
		t.Fatal("settings with headers are zero")
	}
}

func TestABadHeaderInHomeportYamlFailsThePlan(t *testing.T) {
	_, err := buildplan.Detect(repo(map[string]string{"go.mod": "module x\n\ngo 1.24\n",
		"homeport.yaml": "headers:\n  - path: /\n    set:\n      Set-Cookie: a=1\n"}), buildplan.Settings{})
	if err == nil || !strings.Contains(err.Error(), "homeport.yaml") || !strings.Contains(err.Error(), "Set-Cookie") {
		t.Fatalf("err = %v", err)
	}
	if err := (buildplan.Settings{Headers: []buildplan.HeaderRule{{Path: "/", Remove: []string{"Content-Length"}}}}).Check(); err == nil {
		t.Fatal("settings' bad header passed Check")
	}
}

// FileHeaders is homeport.yaml's rules alone, checked: what a deploy that
// no builder planned (a CI upload) is served with
func TestFileHeadersReadsHomeportYaml(t *testing.T) {
	got, err := buildplan.FileHeaders([]byte(cloudtrainYAML + "run: serve\nbuild:\n  command: make\n"))
	if err != nil || headersJSON(t, got) != headersJSON(t, cloudtrain) {
		t.Fatalf("got %v, %v", got, err)
	}
	if got, err := buildplan.FileHeaders([]byte("run: serve\n")); err != nil || got != nil {
		t.Fatalf("no headers: %v, %v", got, err)
	}
	if _, err := buildplan.FileHeaders([]byte("headers:\n  - path: /\n    set:\n      X: a{b}\n")); err == nil {
		t.Fatal("a placeholder passed")
	}
	if _, err := buildplan.FileHeaders([]byte("headers: [")); err == nil {
		t.Fatal("broken YAML passed")
	}
}

// The wire homeportd takes (add's 19th argument, before base64): a v2 line,
// then each rule's sets by name, then its removes, rules in their order
func TestTheHeadersWire(t *testing.T) {
	want := "v2\n" +
		"/shopify/embedded\tset\tContent-Security-Policy\tframe-ancestors https://admin.shopify.com https://*.myshopify.com\n" +
		"/shopify/embedded\tset\tStrict-Transport-Security\tmax-age=31536000\n" +
		"/\tset\tStrict-Transport-Security\tmax-age=31536000\n" +
		"/\tset\tX-Frame-Options\tDENY\n" +
		"/\tremove\tX-Powered-By\n"
	if got := buildplan.HeadersWire(cloudtrain); got != want {
		t.Fatalf("wire:\n%q\nwant\n%q", got, want)
	}
	if got := buildplan.HeadersWire(nil); got != "" {
		t.Fatalf("no rules: %q", got)
	}
}

func set(path string, kv ...string) buildplan.HeaderRule {
	r := buildplan.HeaderRule{Path: path, Set: map[string]string{}}
	for i := 0; i+1 < len(kv); i += 2 {
		r.Set[kv[i]] = kv[i+1]
	}
	return r
}

func manyRules(n int) []buildplan.HeaderRule {
	var rs []buildplan.HeaderRule
	for i := range n {
		rs = append(rs, set("/r"+strconv.Itoa(i), "X", "v"))
	}
	return rs
}

func manySets(n int, value string) buildplan.HeaderRule {
	r := set("/")
	for i := range n {
		r.Set["X-"+strconv.Itoa(i)] = value
	}
	return r
}

func manyRemoves(n int) buildplan.HeaderRule {
	r := buildplan.HeaderRule{Path: "/"}
	for i := range n {
		r.Remove = append(r.Remove, "X-"+strconv.Itoa(i))
	}
	return r
}

// The rules homeportd's validate_headers holds too (cloud's
// internal/enginecontract runs both on the same cases)
func TestHeaderRulesAreChecked(t *testing.T) {
	ok := map[string][]buildplan.HeaderRule{
		"cloudtrain":         cloudtrain,
		"none":               nil,
		"a quoted origin":    {set("/", "Permissions-Policy", `geolocation=(self "https://a.example")`)},
		"a backslash":        {set("/", "X-A", `a\b`)},
		"'self'":             {set("/", "Content-Security-Policy", "default-src 'self'")},
		"removing Server":    {{Path: "/", Remove: []string{"Server"}}},
		"16 rules":           manyRules(16),
		"32 sets":            {manySets(32, "v")},
		"16 removes":         {manyRemoves(16)},
		"value 8192":         {set("/", "X", strings.Repeat("a", 8192))},
		"a 255-segment path": {set("/"+strings.Repeat("a", 255), "X", "v")},
		"Content-Type":       {set("/.well-known/apple-app-site-association", "Content-Type", "application/json")},
	}
	bad := map[string][]buildplan.HeaderRule{
		"17 rules":                manyRules(17),
		"33 sets":                 {manySets(33, "v")},
		"17 removes":              {manyRemoves(17)},
		"value 8193":              {set("/", "X", strings.Repeat("a", 8193))},
		"over 48 KiB":             {manySets(7, strings.Repeat("a", 8000))},
		"an empty rule":           {{Path: "/"}},
		"empty value":             {set("/", "X", "")},
		"spaces around a value":   {set("/", "X", " v")},
		"CR":                      {set("/", "X", "a\rb")},
		"non-ASCII":               {set("/", "X", "café")},
		"placeholder":             {set("/", "X", "{env.HOMEPORT_DNS_TOKEN}")},
		"brace":                   {set("/", "X", "a}b")},
		"backtick":                {set("/", "X", "a`b")},
		"bad name":                {set("/", "Bad Name", "v")},
		"-Server":                 {set("/", "-Server", "v")},
		"a digit first":           {set("/", "1X", "v")},
		"defer":                   {set("/", "defer", "v")},
		"Match":                   {set("/", "Match", "v")},
		"a 65-char name":          {set("/", "X"+strings.Repeat("a", 64), "v")},
		"Set-Cookie":              {set("/", "Set-Cookie", "a=1")},
		"set-cookie":              {set("/", "set-cookie", "a=1")},
		"removing Content-Length": {{Path: "/", Remove: []string{"Content-Length"}}},
		"Content-Encoding":        {set("/", "Content-Encoding", "gzip")},
		"Vary":                    {set("/", "Vary", "*")},
		"x-homeport-*":            {set("/", "x-homeport-anything", "v")},
		"X-Origin-Auth":           {set("/", "X-Origin-Auth", "v")},
		"X-Robots-Tag":            {set("/", "X-Robots-Tag", "all")},
		"setting Server":          {set("/", "Server", "v")},
		"a name twice":            {set("/", "X-A", "1", "x-a", "2")},
		"set and remove":          {{Path: "/", Set: map[string]string{"X-A": "1"}, Remove: []string{"x-a"}}},
		"no slash":                {set("a", "X", "v")},
		"trailing slash":          {set("/a/", "X", "v")},
		"double slash":            {set("//a", "X", "v")},
		"dot segment":             {set("/a/./b", "X", "v")},
		"dot-dot segment":         {set("/a/../b", "X", "v")},
		"a wildcard":              {set("/a*", "X", "v")},
		"an escape":               {set("/a%2fb", "X", "v")},
		"a 257-char path":         {set("/"+strings.Repeat("a", 256), "X", "v")},
		"path twice":              {set("/a", "X", "v"), set("/a", "Y", "v")},
		"path twice in two cases": {set("/Shop", "X", "v"), set("/shop", "Y", "v")},
	}
	for name, rules := range ok {
		if err := buildplan.CheckHeaders(rules); err != nil {
			t.Errorf("%s: refused: %v", name, err)
		}
	}
	for name, rules := range bad {
		if err := buildplan.CheckHeaders(rules); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
