package main

import (
	"cmp"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
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
// newest first, narrowed as auditMatch reads it, a page of limit at a time
// (at most auditPage) from cursor.
func (f *fakeAPI) serveAudit(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f.auditReads = append(f.auditReads, q)
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
	match, refused := f.auditMatch(q)
	if refused != nil {
		f.write(w, 400, refused)
		return
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

// auditMatch are the entries q's filters match, as the API reads them: app
// a whole app (blog, by name or any of its environments' ids), env one
// environment (its id, or its name with app), actor an email, support or
// a token's name. A filter that isn't one is refused, as the API words it.
func (f *fakeAPI) auditMatch(q url.Values) ([]map[string]any, map[string]string) {
	if k := q.Get("kind"); k != "" && !slices.Contains(auditKinds, k) {
		return nil, map[string]string{"error": "no such kind: " + k + " (the kinds are " + strings.Join(auditKinds, ", ") + ")", "field": "kind"}
	}
	app, env := q.Get("app"), q.Get("env")
	if app != "" && app != "blog" && app != blogID && app != stageID {
		return nil, map[string]string{"error": "no such app: " + app, "field": "app"}
	}
	if env != "" && app == "" && env != blogID && env != stageID {
		return nil, map[string]string{"error": "no such environment: " + env + " (an environment's id, or its name with app)", "field": "env"}
	}
	match := []map[string]any{}
	for _, e := range f.audit {
		if app != "" && e["app_name"] != "blog" {
			continue
		}
		if env != "" && e["app_id"] != env && e["environment"] != env {
			continue
		}
		if k := q.Get("kind"); k != "" && e["kind"] != k {
			continue
		}
		if a := q.Get("actor"); a != "" && e["actor_email"] != a && (a != "support" || e["actor_kind"] != "operator") &&
			(e["actor_kind"] != "token" || e["actor_name"] != a) {
			continue
		}
		match = append(match, e)
	}
	return match, nil
}

// serveAuditExport is GET /v1/cli/teams/{team}/audit/export: the Activity
// page's export of the entries that match, as CSV or JSON - here the
// matching entries' ids, and a note when auditNote cuts it short.
func (f *fakeAPI) serveAuditExport(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f.auditExports = append(f.auditExports, q)
	format := q.Get("format")
	if format != "csv" && format != "json" {
		f.write(w, 400, map[string]string{"error": "CSV or JSON", "field": "format"})
		return
	}
	match, refused := f.auditMatch(q)
	if refused != nil {
		f.write(w, 400, refused)
		return
	}
	var ids []string
	for _, e := range match {
		ids = append(ids, fmt.Sprint(e["id"]))
	}
	if f.auditNote != "" {
		w.Header().Set("Homeport-Export-Note", f.auditNote)
	}
	w.Header().Set("Content-Type", map[string]string{"csv": "text/csv; charset=utf-8", "json": "application/json"}[format])
	w.Header().Set("Content-Disposition", `attachment; filename="activity-2026-11-16.`+format+`"`)
	fmt.Fprintf(w, "%s export: %s\n", format, strings.Join(ids, ","))
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
	if len(f.auditReads) != 1 || f.auditReads[0].Encode() != "env="+blogID+"&limit=50" {
		t.Errorf("reads %v", f.auditReads)
	}

	// --env: another environment of the linked app, by its name: the API
	// reads the name, so a token that may only audit:read names it too
	f.auditReads = nil
	if code := h.run("audit", "--env", "staging"); code != 0 || !strings.Contains(h.out.String(), "alice/blog (staging)") ||
		!strings.Contains(h.out.String(), "Alice Doe (alice@example.com)") || !strings.Contains(h.out.String(), "changed variables API_KEY") {
		t.Fatalf("staging: %d %s", code, h.said())
	}
	if f.auditReads[0].Encode() != "app="+blogID+"&env=staging&limit=50" {
		t.Errorf("staging read %v", f.auditReads)
	}

	// --all-envs: every environment of the linked app, each entry's said
	f.auditReads = nil
	if code := h.run("audit", "--all-envs"); code != 0 || !strings.Contains(h.out.String(), "Activity in alice/blog, every environment, newest first:") ||
		!strings.Contains(h.out.String(), "blog (staging)") || !strings.Contains(h.out.String(), "blog (production)") ||
		strings.Contains(h.out.String(), "removed member") {
		t.Fatalf("every environment: %d %s", code, h.said())
	}
	if f.auditReads[0].Encode() != "app="+blogID+"&limit=50" {
		t.Errorf("every environment's read %v", f.auditReads)
	}

	// none: it says so, on stderr, and how far back it looked
	f.audit = nil
	if code := h.run("audit"); code != 0 || h.out.Len() != 0 || !strings.Contains(h.err.String(), "No activity in alice/blog (production)") ||
		!strings.Contains(h.err.String(), "30 days") {
		t.Fatalf("none: %d out=%q err=%q", code, h.out.String(), h.err.String())
	}
}

// Not linked, or --team alone: the whole team's, each entry's app and
// environment said; --app the app's, every environment of it, and --env
// one environment's - named as typed, for the API to read.
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

	// --app: every environment of it
	f.auditReads = nil
	if code := h.run("audit", "--app", "blog"); code != 0 || !strings.Contains(h.out.String(), "Activity in alice/blog, every environment, newest first:") ||
		!strings.Contains(h.out.String(), "blog (staging)") || strings.Contains(h.out.String(), "removed member") ||
		f.auditReads[0].Encode() != "app=blog&limit=50" {
		t.Fatalf("--app: %d %s %v", code, h.said(), f.auditReads)
	}
	// --app and --env: that environment's
	f.auditReads = nil
	if code := h.run("audit", "--app", "blog", "--env", "staging"); code != 0 || !strings.Contains(h.out.String(), "alice/blog (staging)") ||
		f.auditReads[0].Encode() != "app=blog&env=staging&limit=50" {
		t.Fatalf("--app --env: %d %s %v", code, h.said(), f.auditReads)
	}
	// an app that isn't: the API's words, with the flag
	if code := h.run("audit", "--app", "nope"); code != exitError || !strings.Contains(h.err.String(), "--app: no such app: nope") {
		t.Errorf("no such app: %d %s", code, h.said())
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

	// an API token, by its name
	if code := h.run("audit", "--team", "alice", "--actor", "deploy-bot"); code != 0 || f.auditReads[len(f.auditReads)-1].Get("actor") != "deploy-bot" ||
		!strings.Contains(h.out.String(), "deploy-bot (API token)") || strings.Contains(h.out.String(), "Alice Doe") {
		t.Errorf("a token's: %d %s", code, h.said())
	}

	if code := h.run("audit", "--kind", "nothing"); code != exitError || !strings.Contains(h.err.String(), "--kind: no such kind: nothing (the kinds are people,") {
		t.Errorf("refused: %d %s", code, h.said())
	}
	for _, args := range [][]string{{"--export", "xml"}, {"--export", "csv", "--json"}, {"--export", "csv", "--cursor", "c2"},
		{"--export", "csv", "--limit", "5"}, {"--output", "x.csv"}, {"--all-envs", "--team", "alice"}, {"stray"}} {
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

// --export downloads the Activity page's export of every entry that
// matches - the API's, filtered as a page is - into a file, and says
// where.
func TestAuditExportsEveryEntry(t *testing.T) {
	f, h := linkedHarness(t)
	f.audit = someActivity()
	if code := h.run("audit", "--export", "csv", "--all-envs", "--kind", "apps", "--until", "2026-11-15"); code != 0 {
		t.Fatalf("%d %s", code, h.said())
	}
	name := "activity-" + time.Now().UTC().Format("2006-01-02") + ".csv"
	b, err := os.ReadFile(filepath.Join(h.a.wd, name))
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "csv export: 3,2,1\n" {
		t.Errorf("file: %q", b)
	}
	if len(f.auditReads) != 0 || len(f.auditExports) != 1 ||
		f.auditExports[0].Encode() != "app="+blogID+"&format=csv&kind=apps&until=2026-11-15" {
		t.Errorf("reads %v, exports %v", f.auditReads, f.auditExports)
	}
	if !strings.Contains(h.err.String(), "Wrote "+name) || strings.Contains(h.err.String(), "Stopped") {
		t.Errorf("said %s", h.said())
	}

	// never over a file that's there
	if code := h.run("audit", "--export", "csv"); code != exitError || !strings.Contains(h.err.String(), "--output") {
		t.Errorf("again: %d %s", code, h.said())
	}
	// a filter that isn't one: the API's words, with the flag
	if code := h.run("audit", "--export", "json", "--output", "k.json", "--kind", "nothing"); code != exitError ||
		!strings.Contains(h.err.String(), "--kind: no such kind: nothing") {
		t.Errorf("refused: %d %s", code, h.said())
	}
	if _, err := os.Stat(filepath.Join(h.a.wd, "k.json")); err == nil {
		t.Error("a refused export left a file")
	}
}

// An export cut short says so, as the API does: in the file, and to the
// person, with how to get the older ones.
func TestAuditExportSaysWhenItStopped(t *testing.T) {
	f, h := linkedHarness(t)
	f.audit = someActivity()
	f.auditNote = "truncated at 100,000 rows, newest first: for older entries, export again with until 2026-11-14, or narrow the date range"
	if code := h.run("audit", "--team", "alice", "--export", "json", "--output", "-"); code != 0 {
		t.Fatalf("%d %s", code, h.said())
	}
	if h.out.String() != "json export: 5,4,3,2,1\n" || !strings.Contains(h.err.String(), f.auditNote) {
		t.Errorf("out %q err %q", h.out.String(), h.err.String())
	}
	if want := (url.Values{"format": {"json"}}).Encode(); f.auditExports[0].Encode() != want {
		t.Errorf("export %v", f.auditExports)
	}
}
