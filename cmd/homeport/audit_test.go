package main

import (
	"cmp"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// auditKinds are the kinds of action the API knows.
var auditKinds = []string{"people", "access", "billing", "apps", "data", "domains", "jobs", "support", "homeport"}

// serveAudit is GET /v1/cli/teams/{team}/audit: the team's activity,
// newest first, narrowed by app, actor and kind, a page of limit at a time
// (at most auditPage) from cursor.
func (f *fakeAPI) serveAudit(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f.auditReads = append(f.auditReads, q)
	if k := q.Get("kind"); k != "" && !slices.Contains(auditKinds, k) {
		f.write(w, 400, map[string]string{"error": "no such kind: " + k, "field": "kind"})
		return
	}
	limit := 50
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 1000 {
			f.write(w, 400, map[string]string{"error": "a number, 1 to 1000", "field": "limit"})
			return
		}
		limit = n
	}
	if f.auditPage > 0 {
		limit = min(limit, f.auditPage)
	}
	match := []map[string]any{}
	for _, e := range f.audit {
		if app := q.Get("app"); app != "" && e["app_id"] != app {
			continue
		}
		if k := q.Get("kind"); k != "" && e["kind"] != k {
			continue
		}
		if a := q.Get("actor"); a != "" && e["actor_email"] != a && (a != "support" || e["actor_kind"] != "operator") {
			continue
		}
		match = append(match, e)
	}
	from := 0
	if c := q.Get("cursor"); c != "" {
		from, _ = strconv.Atoi(strings.TrimPrefix(c, "c"))
	}
	from = min(from, len(match))
	next := ""
	if from+limit < len(match) {
		next = "c" + strconv.Itoa(from+limit)
	}
	f.write(w, 200, map[string]any{"entries": match[from:min(from+limit, len(match))], "next": next, "days": cmp.Or(f.auditDays, 30)})
}

// entry is one entry as the API gives it.
func entry(id int, at, kind, name, email, via, action, group, says, app, appName, env, target string) map[string]any {
	e := map[string]any{"id": id, "at": at, "actor_kind": kind, "actor_name": name, "actor_email": email, "via": via,
		"ip": "203.0.113.4", "user_agent": "homeport/1.0", "action": action, "kind": group, "says": says,
		"target": target, "detail": map[string]any{}}
	if app != "" {
		e["app_id"], e["app_name"], e["environment"] = app, appName, env
	}
	return e
}

// someActivity is a team's: a person's, support's, a token's, a person's
// on staging, and Homeport's, newest first.
func someActivity() []map[string]any {
	return []map[string]any{
		entry(5, "2026-11-16T09:12:00Z", "user", "Alice Doe", "alice@example.com", "dashboard", "member.removed", "people",
			"removed member bob@example.com", "", "", "", "bob@example.com"),
		entry(4, "2026-11-16T09:00:00Z", "operator", "Homeport support", "", "operator", "support.rebuilt", "support",
			"rebuilt", blogID, "blog", "production", ""),
		entry(3, "2026-11-16T08:30:00Z", "token", "deploy-bot", "", "api", "deploy.started", "apps",
			"deployed abc1234", blogID, "blog", "production", "abc1234"),
		entry(2, "2026-11-15T23:00:00Z", "user", "Alice Doe", "alice@example.com", "cli", "variables.changed", "apps",
			"changed variables API_KEY", stageID, "blog", "staging", "API_KEY"),
		entry(1, "2026-11-14T10:00:00Z", "system", "Homeport", "", "system", "memory.raised", "apps",
			"raised the memory", blogID, "blog", "production", ""),
	}
}

// inUTC prints times as UTC for the test: golden output's clock.
func inUTC(t *testing.T) {
	was := time.Local
	time.Local = time.UTC
	t.Cleanup(func() { time.Local = was })
}

func row(format string, a ...any) string {
	return strings.TrimRight(fmt.Sprintf(format, a...), " ") + "\n"
}

// `homeport audit` in a linked folder: the linked environment's newest
// entries, each a sentence - who, what they did (the entry's own verb),
// how - newest first.
func TestAuditListsTheLinkedEnvironmentsActivity(t *testing.T) {
	inUTC(t)
	f, h := linkedHarness(t)
	f.audit = someActivity()
	if code := h.run("audit"); code != 0 {
		t.Fatalf("%d %s", code, h.said())
	}
	want := "Activity in alice/blog (production), newest first:\n\n" +
		row("%-14s%-24s%-19s%s", "WHEN", "WHO", "WHAT", "VIA") +
		row("%-14s%-24s%-19s%s", "Nov 16 09:00", "Homeport support", "rebuilt", "") +
		row("%-14s%-24s%-19s%s", "Nov 16 08:30", "deploy-bot (API token)", "deployed abc1234", "API") +
		row("%-14s%-24s%-19s%s", "Nov 14 10:00", "Homeport", "raised the memory", "")
	if got := h.out.String(); got != want {
		t.Errorf("said:\n%s\nwant:\n%s", got, want)
	}
	if len(f.auditReads) != 1 || f.auditReads[0].Encode() != "app="+blogID+"&limit=50" {
		t.Errorf("reads %v", f.auditReads)
	}

	// --env: another environment of the linked app
	f.auditReads = nil
	if code := h.run("audit", "--env", "staging"); code != 0 || !strings.Contains(h.out.String(), "alice/blog (staging)") ||
		!strings.Contains(h.out.String(), "Alice Doe (alice@example.com)") || !strings.Contains(h.out.String(), "changed variables API_KEY") {
		t.Fatalf("staging: %d %s", code, h.said())
	}
	if f.auditReads[0].Get("app") != stageID {
		t.Errorf("staging read %v", f.auditReads)
	}

	// none: it says so, on stderr, and how far back it looked
	f.audit = nil
	if code := h.run("audit"); code != 0 || h.out.Len() != 0 || !strings.Contains(h.err.String(), "No activity in alice/blog (production)") ||
		!strings.Contains(h.err.String(), "30 days") {
		t.Fatalf("none: %d out=%q err=%q", code, h.out.String(), h.err.String())
	}
}

// Not linked, or --team alone: the whole team's, each entry's app and
// environment said; --app (and --env) one environment's.
func TestAuditListsTheTeamsActivity(t *testing.T) {
	inUTC(t)
	f := newFakeAPI(t)
	h := newHarness(t, f)
	h.login(t)
	f.audit = someActivity()
	if code := h.run("audit"); code != 0 {
		t.Fatalf("%d %s", code, h.said())
	}
	out := h.out.String()
	for _, want := range []string{"Activity in alice, newest first:", "APP", "Alice Doe (alice@example.com)", "removed member bob@example.com",
		"dashboard", "blog (production)", "blog (staging)", "CLI", "Homeport support", "rebuilt"} {
		if !strings.Contains(out, want) {
			t.Errorf("didn't say %q:\n%s", want, out)
		}
	}
	if q := f.auditReads[0]; q.Has("app") {
		t.Errorf("the team's read named an app: %v", q)
	}

	// --app and --env: that environment's
	f.auditReads = nil
	if code := h.run("audit", "--app", "blog", "--env", "staging"); code != 0 || !strings.Contains(h.out.String(), "alice/blog (staging)") ||
		f.auditReads[0].Get("app") != stageID {
		t.Fatalf("--app: %d %s %v", code, h.said(), f.auditReads)
	}

	// --env names an app's environment: alone with --team, it's a usage error
	if code := h.run("audit", "--team", "alice", "--env", "staging"); code != exitUsage {
		t.Errorf("--env without an app: %d %s", code, h.said())
	}

	// --team in a linked folder: the team's, not the link's
	// (last: a harness of its own takes over the API and the sign-in)
	lf, lh := linkedHarness(t)
	lf.audit = someActivity()
	if code := lh.run("audit", "--team", "alice"); code != 0 || !strings.Contains(lh.out.String(), "Activity in alice, newest first:") ||
		lf.auditReads[0].Has("app") {
		t.Fatalf("--team: %d %s %v", code, lh.said(), lf.auditReads)
	}
}

// The filters reach the API as it names them; a refusal is said with the
// flag it's about; more than one page says how to see the rest.
func TestAuditFilters(t *testing.T) {
	f, h := linkedHarness(t)
	f.audit = someActivity()
	if code := h.run("audit", "--team", "alice", "--actor", "alice@example.com", "--kind", "apps", "--since", "7d", "--until", "2026-11-16", "--limit", "1"); code != 0 {
		t.Fatalf("%d %s", code, h.said())
	}
	q := f.auditReads[0]
	if q.Get("actor") != "alice@example.com" || q.Get("kind") != "apps" || q.Get("since") != "7d" || q.Get("until") != "2026-11-16" || q.Get("limit") != "1" {
		t.Errorf("query %v", q)
	}
	if !strings.Contains(h.out.String(), "changed variables API_KEY") || strings.Contains(h.said(), "Showing the newest") {
		t.Errorf("one page:\n%s", h.said())
	}
	if code := h.run("audit", "--team", "alice", "--kind", "apps", "--limit", "1"); code != 0 {
		t.Fatalf("%d %s", code, h.said())
	}
	if !strings.Contains(h.out.String(), "deployed abc1234") || !strings.Contains(h.said(), "Showing the newest 1") ||
		!strings.Contains(h.said(), "--limit") || !strings.Contains(h.said(), "--export") {
		t.Errorf("more than a page:\n%s", h.said())
	}

	if code := h.run("audit", "--kind", "nothing"); code != exitError || !strings.Contains(h.err.String(), "--kind: no such kind: nothing") {
		t.Errorf("refused: %d %s", code, h.said())
	}
	for _, args := range [][]string{{"--export", "xml"}, {"--export", "csv", "--json"}, {"--export", "csv", "--cursor", "c2"},
		{"--output", "x.csv"}, {"stray"}} {
		if code := h.run(append([]string{"audit"}, args...)...); code != exitUsage {
			t.Errorf("%v: %d %s", args, code, h.said())
		}
	}
}

// --json is the page as the API answers it, every field of each entry
// kept; --cursor reads the page after.
func TestAuditJSON(t *testing.T) {
	f, h := linkedHarness(t)
	f.audit = someActivity()
	f.audit[1]["new_field"] = "kept"
	if code := h.run("audit", "--json", "--limit", "2"); code != 0 {
		t.Fatalf("%d %s", code, h.said())
	}
	var page struct {
		Entries []map[string]any `json:"entries"`
		Next    string           `json:"next"`
		Days    int              `json:"days"`
	}
	if err := json.Unmarshal(h.out.Bytes(), &page); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, h.out.String())
	}
	if len(page.Entries) != 2 || page.Entries[0]["new_field"] != "kept" || page.Entries[0]["says"] != "rebuilt" ||
		page.Next != "c2" || page.Days != 30 {
		t.Errorf("page %+v", page)
	}
	if code := h.run("audit", "--json", "--limit", "2", "--cursor", page.Next); code != 0 || f.auditReads[1].Get("cursor") != "c2" {
		t.Fatalf("cursor: %d %s %v", code, h.said(), f.auditReads)
	}
}

// --export reads every entry that matches, page by page, into a file as
// the Activity page's export writes it, and says where.
func TestAuditExportsEveryEntry(t *testing.T) {
	f, h := linkedHarness(t)
	f.audit = someActivity()
	f.auditPage = 2
	if code := h.run("audit", "--team", "alice", "--export", "csv"); code != 0 {
		t.Fatalf("%d %s", code, h.said())
	}
	name := "activity-" + time.Now().UTC().Format("2006-01-02") + ".csv"
	b, err := os.ReadFile(filepath.Join(h.a.wd, name))
	if err != nil {
		t.Fatal(err)
	}
	rows, err := csv.NewReader(strings.NewReader(string(b))).ReadAll()
	if err != nil {
		t.Fatalf("not CSV: %v\n%s", err, b)
	}
	if strings.Join(rows[0], ",") != "at,actor_kind,actor_name,actor_email,via,ip,user_agent,action,kind,app,app_name,environment,target,detail" {
		t.Errorf("columns %v", rows[0])
	}
	if len(rows) != 6 || rows[1][7] != "member.removed" || rows[5][7] != "memory.raised" || rows[2][9] != blogID || rows[2][13] != "{}" {
		t.Errorf("rows:\n%s", b)
	}
	if !strings.Contains(h.err.String(), "5 entries") || !strings.Contains(h.err.String(), name) {
		t.Errorf("said %s", h.said())
	}
	var cursors []string
	for _, q := range f.auditReads {
		cursors = append(cursors, q.Get("limit")+"/"+q.Get("cursor"))
	}
	if got := strings.Join(cursors, " "); got != "1000/ 1000/c2 1000/c4" {
		t.Errorf("reads %s", got)
	}

	// never over a file that's there
	if code := h.run("audit", "--export", "csv"); code != exitError || !strings.Contains(h.err.String(), "--output") {
		t.Errorf("again: %d %s", code, h.said())
	}
}

// An export stops at its most - newest first - and says so: in the file,
// as the Activity page's does, and to the person, with how to get the
// older ones.
func TestAuditExportSaysWhenItStopped(t *testing.T) {
	f, h := linkedHarness(t)
	was := auditExportMost
	auditExportMost = 2
	t.Cleanup(func() { auditExportMost = was })
	f.audit = someActivity()
	f.auditPage = 2
	if code := h.run("audit", "--team", "alice", "--export", "json", "--output", "-"); code != 0 {
		t.Fatalf("%d %s", code, h.said())
	}
	var out struct {
		Entries   []map[string]any `json:"entries"`
		Truncated bool             `json:"truncated"`
		Note      string           `json:"note"`
	}
	if err := json.Unmarshal(h.out.Bytes(), &out); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, h.out.String())
	}
	// the oldest kept is the 16th's: --until the 17th takes up from it
	if len(out.Entries) != 2 || !out.Truncated || !strings.Contains(out.Note, "2 entries") || !strings.Contains(out.Note, "--until 2026-11-17") {
		t.Errorf("export %+v", out)
	}
	if !strings.Contains(h.err.String(), out.Note) {
		t.Errorf("didn't say it stopped: %s", h.err.String())
	}

	if code := h.run("audit", "--team", "alice", "--export", "csv", "--output", "a.csv"); code != 0 {
		t.Fatalf("%d %s", code, h.said())
	}
	b, _ := os.ReadFile(filepath.Join(h.a.wd, "a.csv"))
	if lines := strings.Split(strings.TrimSpace(string(b)), "\n"); len(lines) != 4 || !strings.HasPrefix(lines[3], "# Stopped at 2 entries") {
		t.Errorf("csv:\n%s", b)
	}
}
