package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/homeport-sh/cli/internal/cloud"
	"github.com/homeport-sh/cli/internal/config"
)

const (
	team1   = "7e000000-0000-4000-8000-000000000001"
	blogID  = "a0000000-0000-4000-8000-000000000001"
	stageID = "a0000000-0000-4000-8000-000000000002"
	apiID   = "a0000000-0000-4000-8000-000000000003"
)

// fakeAPI is homeport.sh's API as the CLI sees it.
type fakeAPI struct {
	t   *testing.T
	srv *httptest.Server
	mu  sync.Mutex

	// signing in: polls answered pending first, then slow_down once, then
	// how it ends ("approve", "deny", "expire")
	pending int
	slow    bool
	ending  string
	issued  []string // tokens given
	revoked []string // tokens logged out

	apps     []cloud.App
	uploads  map[string][]byte
	deploys  []cloud.DeployRequest
	refuse   *cloud.Error // the deploy's answer, when it's refused
	tails    []string     // a build's log as each poll sees it
	build    string       // how it ends: succeeded, failed
	reason   string
	steps    []string // the deploy's statuses, poll by poll
	detail   string
	buildGet int

	tokenNames []string          // tokens made for CI, by name
	env        map[string]string // an environment's variables, as set
	dbCalls    []string
	pulled     []string // the apps whose add-on credentials were pulled
	pullValues map[string]string
	withheld   []string
}

func newFakeAPI(t *testing.T) *fakeAPI {
	f := &fakeAPI{t: t, ending: "approve", uploads: map[string][]byte{}, build: "succeeded",
		apps: []cloud.App{
			{ID: blogID, Name: "blog", ProjectName: "blog", Environment: "production", Builds: "hosted", Domain: "blog.homeport.app"},
			{ID: stageID, Name: "blog-staging", ProjectName: "blog", Environment: "staging", Builds: "hosted", Domain: "blog-staging.homeport.app"},
			{ID: apiID, Name: "api", ProjectName: "api", Environment: "production", Builds: "ci", Domain: "api.homeport.app"},
		},
		tails: []string{"==> fetching\n", "==> fetching\ncompiling\n", "==> fetching\ncompiling\n==> built\n"},
		steps: []string{"uploaded", "deploying", "live"}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeAPI) write(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func (f *fakeAPI) signedIn(r *http.Request) bool {
	tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	for _, r := range f.revoked {
		if r == tok {
			return false
		}
	}
	for _, i := range f.issued {
		if i == tok {
			return true
		}
	}
	return tok == "hpcli_from-ci"
}

func (f *fakeAPI) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := r.URL.Path
	switch {
	case r.Method == "POST" && p == "/v1/cli/device":
		var in struct{ Name string }
		json.NewDecoder(r.Body).Decode(&in)
		if in.Name != "alice-laptop" {
			f.t.Errorf("device named %q", in.Name)
		}
		f.write(w, 200, map[string]any{"device_code": "dev-code", "user_code": "BCDF-GHJK",
			"verification_uri": "https://app.homeport.test/cli", "verification_uri_complete": "https://app.homeport.test/cli?code=BCDF-GHJK",
			"expires_in": 600, "interval": 5})
		return
	case r.Method == "POST" && p == "/v1/cli/device/token":
		switch {
		case f.pending > 0:
			f.pending--
			f.write(w, 400, map[string]string{"error": cloud.Pending})
		case f.slow:
			f.slow = false
			f.write(w, 400, map[string]string{"error": cloud.SlowDown})
		case f.ending == "deny":
			f.write(w, 400, map[string]string{"error": cloud.Denied})
		case f.ending == "expire":
			f.write(w, 400, map[string]string{"error": cloud.Expired})
		default:
			// each API's tokens its own
			tok := "hpcli_secret" + strings.Repeat("x", len(f.issued)) + "-" + f.srv.URL[strings.LastIndex(f.srv.URL, ":")+1:]
			f.issued = append(f.issued, tok)
			f.write(w, 200, map[string]any{"token": tok, "token_id": "t1", "name": "alice-laptop",
				"expires_at": time.Now().Add(90 * 24 * time.Hour), "user": map[string]string{"name": "Alice Doe", "email": "alice@example.com", "login": "alice"}})
		}
		return
	case r.Method == "PUT" && strings.HasPrefix(p, "/bucket/"):
		if r.Header.Get("Authorization") != "" {
			f.t.Error("the token went to the bucket")
		}
		b, _ := io.ReadAll(r.Body)
		if int64(len(b)) != r.ContentLength {
			f.t.Errorf("PUT %d bytes, said %d", len(b), r.ContentLength)
		}
		f.uploads[strings.TrimPrefix(p, "/bucket/")] = b
		return
	}
	if !f.signedIn(r) {
		f.write(w, 401, map[string]string{"error": "not signed in"})
		return
	}
	switch {
	case r.Method == "GET" && p == "/v1/cli/whoami":
		f.write(w, 200, map[string]any{"user": map[string]string{"name": "Alice Doe", "email": "alice@example.com", "login": "alice"},
			"token":     map[string]any{"id": "t1", "name": "alice-laptop", "created_at": time.Now(), "expires_at": time.Now().Add(90 * 24 * time.Hour)},
			"teams":     []map[string]string{{"ID": team1, "Name": "alice", "Slug": "alice", "Role": "owner"}},
			"dashboard": "https://app.homeport.test"})
	case r.Method == "GET" && strings.HasSuffix(p, "/env/addons"):
		app := strings.TrimSuffix(strings.TrimPrefix(p, "/v1/cli/teams/"+team1+"/apps/"), "/env/addons")
		f.pulled = append(f.pulled, app)
		if app == blogID || app == apiID { // production's
			f.write(w, 400, map[string]string{"error": "production's credentials aren't pulled to a laptop", "field": "environment"})
			return
		}
		values := f.pullValues
		if values == nil {
			values = map[string]string{"DATABASE_URL": "postgres://u:pw@db/x", "AWS_ACCESS_KEY_ID": "AKIA1"}
		}
		f.write(w, 200, map[string]any{"Values": values, "Withheld": f.withheld})
	case r.Method == "POST" && p == "/v1/cli/tokens":
		var in struct{ Name string }
		json.NewDecoder(r.Body).Decode(&in)
		f.tokenNames = append(f.tokenNames, in.Name)
		f.write(w, 200, map[string]any{"Token": "hpcli_ci-token", "Session": map[string]any{"ID": "c9", "Name": in.Name}})
	case r.Method == "DELETE" && p == "/v1/cli/token":
		f.revoked = append(f.revoked, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		w.WriteHeader(204)
	case r.Method == "GET" && p == "/v1/cli/teams/"+team1+"/apps":
		f.write(w, 200, f.apps)
	case r.Method == "GET" && strings.HasPrefix(p, "/v1/cli/teams/"+team1+"/apps/") && !strings.Contains(strings.TrimPrefix(p, "/v1/cli/teams/"+team1+"/apps/"), "/"):
		for _, a := range f.apps {
			if a.ID == strings.TrimPrefix(p, "/v1/cli/teams/"+team1+"/apps/") {
				f.write(w, 200, a)
				return
			}
		}
		f.write(w, 404, map[string]string{"error": "not found"})
	case r.Method == "POST" && strings.HasSuffix(p, "/sources"):
		var in struct{ Bytes int64 }
		json.NewDecoder(r.Body).Decode(&in)
		f.write(w, 200, map[string]any{"ID": "s1", "URL": f.srv.URL + "/bucket/s1", "MaxBytes": 500 << 20})
	case r.Method == "POST" && strings.HasSuffix(p, "/deploys"):
		var in cloud.DeployRequest
		json.NewDecoder(r.Body).Decode(&in)
		f.deploys = append(f.deploys, in)
		if f.refuse != nil {
			f.write(w, f.refuse.Status, map[string]string{"error": f.refuse.Message, "field": f.refuse.Field})
			return
		}
		f.write(w, 200, map[string]string{"build": "b1"})
	case r.Method == "GET" && p == "/v1/cli/teams/"+team1+"/builds/b1":
		i := min(f.buildGet, len(f.tails)-1)
		f.buildGet++
		status, deploy := "running", ""
		if f.buildGet >= len(f.tails) {
			status = f.build
			if status == "succeeded" {
				deploy = "d1"
			}
		}
		f.write(w, 200, map[string]any{"ID": "b1", "Status": status, "Reason": f.reason, "Deploy": deploy, "Log": f.tails[i]})
	case strings.HasSuffix(p, "/env") && r.Method == "GET":
		var names []string
		for k := range f.env {
			names = append(names, k)
		}
		slices.Sort(names)
		f.write(w, 200, map[string]any{"Names": names})
	case strings.HasSuffix(p, "/env") && r.Method == "POST":
		var in struct {
			Set   map[string]string
			Unset []string
		}
		json.NewDecoder(r.Body).Decode(&in)
		if f.env == nil {
			f.env = map[string]string{}
		}
		maps.Copy(f.env, in.Set)
		for _, k := range in.Unset {
			delete(f.env, k)
		}
		names := slices.Sorted(maps.Keys(f.env))
		f.write(w, 200, names)
	case strings.HasSuffix(p, "/logs"):
		f.write(w, 200, map[string]any{"Cursor": "c2", "Lines": []map[string]any{{"Time": "2026-10-08T12:00:00Z", "Level": "error", "Message": "panic: boom", "Process": "web"}}})
	case strings.Contains(p, "/database"):
		f.dbCalls = append(f.dbCalls, r.Method+" "+p[strings.Index(p, "/database"):])
		f.write(w, 200, map[string]any{"Available": true, "Database": nil,
			"Attachable": []map[string]any{{"ID": "db2", "Name": "api-production", "UsedBy": []string{"api-production"}}}})
	case strings.HasSuffix(p, "/usage"):
		f.write(w, 200, map[string]any{"Plan": "Starter", "Estimate": map[string]any{"TotalCents": 900}})
	case r.Method == "GET" && p == "/v1/cli/teams/"+team1+"/deploys/d1":
		s := f.steps[0]
		if len(f.steps) > 1 {
			f.steps = f.steps[1:]
		}
		f.write(w, 200, map[string]any{"ID": "d1", "Status": s, "Detail": f.detail})
	default:
		f.t.Errorf("unexpected %s %s", r.Method, p)
		w.WriteHeader(404)
	}
}

// harness is the CLI run as a person would, against f.
type harness struct {
	f        *fakeAPI
	a        *app
	out, err *bytes.Buffer
	opened   []string
	slept    []time.Duration
}

func newHarness(t *testing.T, f *fakeAPI) *harness {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOMEPORT_API", f.srv.URL)
	t.Setenv("HOMEPORT_TOKEN", "")
	h := &harness{f: f, out: &bytes.Buffer{}, err: &bytes.Buffer{}}
	h.a = &app{in: strings.NewReader(""), out: h.out, err: h.err, wd: t.TempDir(),
		open:     func(u string) error { h.opened = append(h.opened, u); return nil },
		sleep:    func(_ context.Context, d time.Duration) error { h.slept = append(h.slept, d); return nil },
		hostname: func() (string, error) { return "alice-laptop", nil }}
	return h
}

func (h *harness) run(args ...string) int {
	h.out.Reset()
	h.err.Reset()
	return h.a.run(args)
}

func (h *harness) said() string { return h.out.String() + h.err.String() }

func (h *harness) login(t *testing.T) {
	t.Helper()
	if code := h.run("login"); code != 0 {
		t.Fatalf("login: %d %s", code, h.said())
	}
}

func TestLoginSignsInAndNeverShowsTheToken(t *testing.T) {
	f := newFakeAPI(t)
	f.pending, f.slow = 1, true
	h := newHarness(t, f)
	h.a.tty = true
	h.login(t)
	said := h.said()
	for _, want := range []string{"BCDF-GHJK", "https://app.homeport.test/cli?code=BCDF-GHJK", "Signed in as Alice Doe (alice@example.com)"} {
		if !strings.Contains(said, want) {
			t.Errorf("didn't say %q:\n%s", want, said)
		}
	}
	if strings.Contains(said, "hpcli_") {
		t.Fatal("printed the token")
	}
	if len(h.opened) != 1 || h.opened[0] != "https://app.homeport.test/cli?code=BCDF-GHJK" {
		t.Fatalf("opened %v", h.opened)
	}
	// polled at the interval, and slower once told to
	if len(h.slept) != 3 || h.slept[0] != 5*time.Second || h.slept[2] != 10*time.Second {
		t.Fatalf("slept %v", h.slept)
	}
	c, err := config.LoadCredentials()
	if err != nil || c.Token != f.issued[0] || c.Email != "alice@example.com" || c.Name != "alice-laptop" || c.API != f.srv.URL {
		t.Fatalf("credentials %+v %v", c, err)
	}
	if info, _ := os.Stat(config.CredentialsPath()); info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", info.Mode())
	}
}

func TestWithoutATerminalLoginOpensNoBrowser(t *testing.T) {
	f := newFakeAPI(t)
	h := newHarness(t, f)
	h.login(t)
	if len(h.opened) != 0 || !strings.Contains(h.said(), "https://app.homeport.test/cli?code=BCDF-GHJK") {
		t.Fatalf("opened %v; said %s", h.opened, h.said())
	}
}

func TestLoggingInAgainRevokesTheOldSignIn(t *testing.T) {
	f := newFakeAPI(t)
	h := newHarness(t, f)
	h.login(t)
	h.login(t)
	if len(f.revoked) != 1 || f.revoked[0] != f.issued[0] {
		t.Fatalf("revoked %v of %v", f.revoked, f.issued)
	}
	if c, _ := config.LoadCredentials(); c.Token != f.issued[1] {
		t.Fatal("kept the old token")
	}
}

func TestADeniedOrExpiredLoginSaysSo(t *testing.T) {
	for ending, want := range map[string]string{"deny": "denied", "expire": "expired"} {
		f := newFakeAPI(t)
		f.ending = ending
		h := newHarness(t, f)
		if code := h.run("login"); code != exitError || !strings.Contains(h.err.String(), want) {
			t.Errorf("%s: %d %s", ending, code, h.said())
		}
		if _, err := config.LoadCredentials(); err == nil {
			t.Errorf("%s: saved a sign-in", ending)
		}
	}
}

func TestWhoamiAndLogout(t *testing.T) {
	f := newFakeAPI(t)
	h := newHarness(t, f)
	if code := h.run("whoami"); code != exitSignedOut || !strings.Contains(h.err.String(), "homeport login") {
		t.Fatalf("signed out: %d %s", code, h.said())
	}
	h.login(t)
	if code := h.run("whoami"); code != 0 {
		t.Fatalf("whoami: %d %s", code, h.said())
	}
	for _, want := range []string{"Alice Doe", "alice@example.com", "alice-laptop", "alice (owner)"} {
		if !strings.Contains(h.out.String(), want) {
			t.Errorf("whoami didn't say %q: %s", want, h.out.String())
		}
	}
	if code := h.run("logout"); code != 0 {
		t.Fatalf("logout: %d %s", code, h.said())
	}
	if len(f.revoked) != 1 || f.revoked[0] != f.issued[0] {
		t.Fatalf("not revoked on the server: %v", f.revoked)
	}
	if _, err := os.Stat(config.CredentialsPath()); !os.IsNotExist(err) {
		t.Fatal("the credentials are still there")
	}
	if code := h.run("logout"); code != 0 || !strings.Contains(h.said(), "not signed in") {
		t.Fatalf("logout twice: %d %s", code, h.said())
	}
}

// In CI there's no browser: HOMEPORT_TOKEN is the sign-in.
func TestATokenFromTheEnvironment(t *testing.T) {
	f := newFakeAPI(t)
	h := newHarness(t, f)
	t.Setenv("HOMEPORT_TOKEN", "hpcli_from-ci")
	if code := h.run("whoami"); code != 0 {
		t.Fatalf("%d %s", code, h.said())
	}
}

func TestLinkingWithFlags(t *testing.T) {
	f := newFakeAPI(t)
	h := newHarness(t, f)
	h.login(t)
	if code := h.run("link", "--team", "alice", "--app", "blog", "--env", "staging"); code != 0 {
		t.Fatalf("%d %s", code, h.said())
	}
	l, at, err := config.FindLink(h.a.wd)
	if err != nil || at != h.a.wd || *l != (config.Link{Team: team1, TeamSlug: "alice", App: stageID, Project: "blog", Environment: "staging"}) {
		t.Fatalf("%+v %v", l, err)
	}
	// an app its own CI deploys isn't built here
	if code := h.run("link", "--app", "api"); code != exitUsage || !strings.Contains(h.err.String(), "api") {
		t.Fatalf("a CI app: %d %s", code, h.said())
	}
}

func TestLinkingAsks(t *testing.T) {
	f := newFakeAPI(t)
	h := newHarness(t, f)
	h.login(t)
	h.a.tty = true
	// one team: no question; the app: blog (only hosted ones); the environment: 2nd
	h.a.in = strings.NewReader("blog\n2\n")
	if code := h.run("link"); code != 0 {
		t.Fatalf("%d %s", code, h.said())
	}
	if strings.Contains(h.out.String(), "api") {
		t.Fatalf("offered a CI app: %s", h.out.String())
	}
	l, _, _ := config.FindLink(h.a.wd)
	if l.App != stageID {
		t.Fatalf("linked %+v", l)
	}
	// without a terminal it doesn't ask: it says which flags to pass
	h.a.tty = false
	if code := h.run("link"); code != exitUsage || !strings.Contains(h.err.String(), "--app") {
		t.Fatalf("no terminal: %d %s", code, h.said())
	}
}

// project is a linked folder with an app in it.
func project(t *testing.T, h *harness) {
	t.Helper()
	write := func(name, body string) {
		p := filepath.Join(h.a.wd, name)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(body), 0o644)
	}
	write("go.mod", "module blog\n")
	write("main.go", "package main\n")
	write(".gitignore", "server\n")
	write("server", "a build output: not uploaded")
	if err := config.SaveLink(h.a.wd, config.Link{Team: team1, TeamSlug: "alice", App: blogID, Project: "blog", Environment: "production"}); err != nil {
		t.Fatal(err)
	}
}

func TestDeployingUploadsTheTreeAndFollowsItLive(t *testing.T) {
	f := newFakeAPI(t)
	h := newHarness(t, f)
	h.login(t)
	project(t, h)
	code := h.run("deploy", "--run", "./server --debug", "--release", "./migrate", "--process", "worker=./work --queue", "--process", "cron=./cron")
	if code != 0 {
		t.Fatalf("%d %s", code, h.said())
	}
	// the tree, as a build unpacks it
	files := untar(t, f.uploads["s1"])
	if _, ignored := files["source/server"]; files["source/main.go"] != "package main\n" || ignored {
		t.Fatalf("uploaded %v", keys(files))
	}
	if _, ok := files["source/.homeport/link.json"]; ok {
		t.Fatal("uploaded the link")
	}
	d := f.deploys[0]
	if d.Source != "s1" || len(d.SHA) != 40 || d.Save || d.Settings.Run != "./server --debug" || d.Settings.Release != "./migrate" ||
		len(d.Settings.Processes) != 2 || d.Settings.Processes[0].Name != "worker" || d.Settings.Processes[0].Run != "./work --queue" {
		t.Fatalf("deployed %+v", d)
	}
	out := h.out.String()
	// each line of the build's log once, as it came
	if strings.Count(out, "compiling") != 1 || strings.Count(out, "==> fetching") != 1 || strings.Count(out, "==> built") != 1 {
		t.Fatalf("the log:\n%s", out)
	}
	if !strings.Contains(out, "https://blog.homeport.app") || !strings.Contains(out, "https://app.homeport.test/alice/blog/production/builds/b1") {
		t.Fatalf("said:\n%s", out)
	}
}

func TestDeployingSavesWhenAsked(t *testing.T) {
	f := newFakeAPI(t)
	h := newHarness(t, f)
	h.login(t)
	project(t, h)
	if code := h.run("deploy", "--release", "./migrate", "--save", "--unset", "processes"); code != 0 {
		t.Fatalf("%d %s", code, h.said())
	}
	if d := f.deploys[0]; !d.Save || d.Settings.Release != "./migrate" || len(d.Unset) != 1 || d.Unset[0] != "processes" {
		t.Fatalf("%+v", d)
	}
	// emptying needs saving, as in the dashboard
	if code := h.run("deploy", "--unset", "run"); code != exitUsage {
		t.Fatalf("unset without save: %d", code)
	}
	if code := h.run("deploy", "--process", "no-equals"); code != exitUsage {
		t.Fatalf("a bad --process: %d", code)
	}
}

// Exit codes CI can tell apart.
func TestDeployExitCodes(t *testing.T) {
	cases := []struct {
		name  string
		setup func(*fakeAPI, *harness)
		code  int
		says  string
	}{
		{"build failed", func(f *fakeAPI, _ *harness) { f.build, f.reason = "failed", "the build failed" }, exitBuildFailed, "the build failed"},
		{"deploy failed", func(f *fakeAPI, _ *harness) {
			f.steps, f.detail = []string{"deploying", "failed"}, "health check failed: 502"
		}, exitDeployFailed, "health check failed"},
		{"refused", func(f *fakeAPI, _ *harness) {
			f.refuse = &cloud.Error{Status: 400, Message: "this environment deploys from its own CI", Field: "source"}
		}, exitError, "its own CI"},
		{"signed out", func(_ *fakeAPI, h *harness) { config.RemoveCredentials() }, exitSignedOut, "homeport login"},
		{"not linked", func(_ *fakeAPI, h *harness) { os.RemoveAll(filepath.Join(h.a.wd, ".homeport")) }, exitUsage, "homeport link"},
	}
	for _, c := range cases {
		f := newFakeAPI(t)
		h := newHarness(t, f)
		h.login(t)
		project(t, h)
		c.setup(f, h)
		if code := h.run("deploy"); code != c.code || !strings.Contains(h.said(), c.says) {
			t.Errorf("%s: %d, want %d; said %s", c.name, code, c.code, h.said())
		}
	}
}

func TestDeployingWithoutWaiting(t *testing.T) {
	f := newFakeAPI(t)
	h := newHarness(t, f)
	h.login(t)
	project(t, h)
	if code := h.run("deploy", "--detach"); code != 0 || f.buildGet != 0 || !strings.Contains(h.out.String(), "/builds/b1") {
		t.Fatalf("%d (polled %d) %s", code, f.buildGet, h.said())
	}
}

func TestALogShowsOnlyWhatsNew(t *testing.T) {
	for _, c := range []struct{ prev, cur, want string }{
		{"", "a\nb\n", "a\nb\n"},
		{"a\nb\n", "a\nb\nc\n", "c\n"},
		{"a\nb\n", "a\nb\n", ""},
		// the tail moved past what was printed: the new end, without repeating
		{strings.Repeat("x", 300) + "\nold end\n", strings.Repeat("x", 100) + "\nold end\nnew\n", "new\n"},
		// nothing in common: more came than the tail holds
		{"gone\n", "all new\n", "…\nall new\n"},
	} {
		if got := logDelta(c.prev, c.cur); got != c.want {
			t.Errorf("logDelta(%q, %q) = %q, want %q", c.prev, c.cur, got, c.want)
		}
	}
}

func untar(t *testing.T, b []byte) map[string]string {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("not gzip: %v", err)
	}
	out := map[string]string{}
	tr := tar.NewReader(zr)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(tr)
		out[h.Name] = string(body)
	}
}

func keys(m map[string]string) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

// A token goes only to the API it was given by: HOMEPORT_API pointing
// elsewhere is signed out there, not a way to send it anywhere.
func TestATokenGoesOnlyWhereItWasGiven(t *testing.T) {
	f := newFakeAPI(t)
	h := newHarness(t, f)
	h.login(t)
	other := newFakeAPI(t)
	t.Setenv("HOMEPORT_API", other.srv.URL)
	if code := h.run("whoami"); code != exitSignedOut || !strings.Contains(h.err.String(), f.srv.URL) {
		t.Fatalf("another API: %d %s", code, h.said())
	}
	// signing in there logs the old token out where it was given, not there
	h.login(t)
	if len(f.revoked) != 1 || len(other.revoked) != 0 {
		t.Fatalf("revoked at its own %v, at the other %v", f.revoked, other.revoked)
	}
	// plain http is refused but on this computer
	t.Setenv("HOMEPORT_API", "http://api.example.com")
	if code := h.run("login"); code != exitUsage || !strings.Contains(h.err.String(), "https") {
		t.Fatalf("http: %d %s", code, h.said())
	}
}

// Without a terminal, a question with a default takes it: CI and the MCP
// server link production without --env.
func TestWithoutATerminalTheDefaultIsTaken(t *testing.T) {
	f := newFakeAPI(t)
	h := newHarness(t, f)
	h.login(t)
	if code := h.run("link", "--app", "blog"); code != 0 {
		t.Fatalf("%d %s", code, h.said())
	}
	if l, _, _ := config.FindLink(h.a.wd); l.Environment != "production" {
		t.Fatalf("%+v", l)
	}
}

func TestDeployingSaysWhatSecretsItLeftOut(t *testing.T) {
	f := newFakeAPI(t)
	h := newHarness(t, f)
	h.login(t)
	project(t, h)
	os.WriteFile(filepath.Join(h.a.wd, ".env"), []byte("KEY=s3cret"), 0o644)
	if code := h.run("deploy"); code != 0 {
		t.Fatalf("%d %s", code, h.said())
	}
	if _, ok := untar(t, f.uploads["s1"])["source/.env"]; ok {
		t.Fatal("uploaded .env")
	}
	if !strings.Contains(h.said(), "left out .env") {
		t.Fatalf("didn't say: %s", h.said())
	}
}

// `homeport token create`: a named token for CI, printed once to stdout and
// nowhere else.
func TestMakingATokenForCI(t *testing.T) {
	f := newFakeAPI(t)
	h := newHarness(t, f)
	h.login(t)
	if code := h.run("token", "create", "--name", "github-actions"); code != 0 {
		t.Fatalf("%d %s", code, h.said())
	}
	if strings.TrimSpace(h.out.String()) != "hpcli_ci-token" || strings.Contains(h.err.String(), "hpcli_") {
		t.Fatalf("out %q err %q", h.out.String(), h.err.String())
	}
	if f.tokenNames[0] != "github-actions" {
		t.Fatalf("%v", f.tokenNames)
	}
	if code := h.run("token", "create"); code != exitUsage {
		t.Fatalf("no name: %d", code)
	}
}

// A link's names are only labels: what it deploys is the environment its
// ids name, said by the API's names - and a link whose labels disagree with
// them (edited, or stale) is refused rather than shown as something else.
func TestALinkIsCheckedAgainstTheAPI(t *testing.T) {
	f := newFakeAPI(t)
	h := newHarness(t, f)
	h.login(t)
	project(t, h)
	// the ids say production; the labels say staging
	config.SaveLink(h.a.wd, config.Link{Team: team1, TeamSlug: "alice", App: blogID, Project: "blog", Environment: "staging"})
	if code := h.run("deploy"); code != exitUsage || !strings.Contains(h.err.String(), "production") || len(f.deploys) != 0 || len(f.uploads) != 0 {
		t.Fatalf("a mislabelled link: %d %s", code, h.said())
	}
	config.SaveLink(h.a.wd, config.Link{Team: team1, TeamSlug: "alice", App: blogID, Project: "blog", Environment: "production"})
	if code := h.run("deploy"); code != 0 || !strings.Contains(h.out.String(), "alice/blog (production)") {
		t.Fatalf("%d %s", code, h.said())
	}
}
