package main

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/homeport-sh/cli/internal/cloud"
	"github.com/homeport-sh/cli/internal/config"
)

// auditExportMost is the most entries an export holds: the Activity
// page's.
var auditExportMost = 100_000

// audit is `homeport audit`: a team's activity (design/audit-log.md in
// homeport cloud) - who changed what, and when - newest first. In a linked
// folder it's the linked environment's; --team alone, or a folder nothing
// links, the whole team's; --app (and --env) one environment's. --json is
// a page as the API answers it; --export reads every entry that matches
// into a file, as the Activity page's export writes it.
func (a *app) audit(ctx context.Context, args []string) error {
	fs, f := a.envFlagSet("audit")
	actor := fs.String("actor", "", "who: a person's email, support (Homeport support) or homeport (Homeport itself)")
	kind := fs.String("kind", "", "a kind of action: people, access, billing, apps, data, domains, jobs, support or homeport")
	since := fs.String("since", "", "from: a time (2026-11-15T09:00:00Z), a day (2026-11-15, UTC) or an age (12h, 7d)")
	until := fs.String("until", "", "before: a time, a day (before it starts, UTC) or an age")
	limit := fs.Int("limit", 50, "how many, newest first: 1 to 1000")
	cursor := fs.String("cursor", "", "with --json: the page after one, by its next")
	asJSON := fs.Bool("json", false, "the page as JSON, as the API answers it")
	export := fs.String("export", "", "csv or json: every entry that matches (up to 100,000), to a file")
	output := fs.String("output", "", "where --export writes (default: activity-<date>.<csv|json> here; - for stdout)")
	fs.StringVar(output, "o", "", "the same as --output")
	if err := parse(fs, args); err != nil {
		return err
	}
	set := map[string]bool{}
	fs.Visit(func(fl *flag.Flag) { set[fl.Name] = true })
	switch {
	case *export != "" && *export != "csv" && *export != "json":
		return usageErr("--export is csv or json, not %q", *export)
	case *export != "" && (*asJSON || set["cursor"] || set["limit"]):
		return usageErr("--export reads every entry that matches: --json, --cursor and --limit are for a page")
	case *export == "" && *output != "":
		return usageErr("--output is where --export writes")
	case *cursor != "" && !*asJSON:
		return usageErr("--cursor reads the page after one --json printed")
	case *f.env != "" && *f.app == "" && *f.team != "":
		return usageErr("--env is an app's environment: name the app with --app")
	}
	c, team, app, where, err := a.auditScope(ctx, f)
	if err != nil {
		return err
	}
	filter := cloud.AuditFilter{App: app, Actor: *actor, Kind: *kind, Since: *since, Until: *until}
	if *export != "" {
		return a.auditExport(ctx, c, team, filter, *export, *output)
	}
	page, err := c.Audit(ctx, team, filter, *cursor, *limit)
	if err != nil {
		return auditRefusal(err)
	}
	if *asJSON {
		b, err := json.MarshalIndent(page, "", "  ")
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(a.out, "%s\n", b)
		return err
	}
	if len(page.Entries) == 0 {
		fmt.Fprintf(a.err, "No activity in %s matches, as far back as the plan keeps (%d days).\n", where, page.Days)
		return nil
	}
	fmt.Fprintf(a.out, "Activity in %s, newest first:\n\n", where)
	head := []string{"WHEN", "WHO", "WHAT"}
	if app == "" {
		head = append(head, "APP")
	}
	rows := [][]string{append(head, "VIA")}
	for _, e := range page.Entries {
		r := []string{e.At.Local().Format("Jan 2 15:04"), auditWho(e), e.Says}
		if app == "" {
			r = append(r, auditApp(e))
		}
		rows = append(rows, append(r, auditVia[e.Via]))
	}
	if err := a.table(rows); err != nil {
		return err
	}
	if page.Next != "" {
		fmt.Fprintf(a.err, "\nShowing the newest %d: --limit shows up to %s, --export saves every one.\n", len(page.Entries), thousands(cloud.MaxAuditPage))
	}
	return nil
}

// auditScope is whose activity the flags name: an environment's (--app,
// or the folder's link) or the team's (--team alone, or no link) - its
// team's id, the environment's ("" the team's) and how to say it.
func (a *app) auditScope(ctx context.Context, f envFlags) (c *cloud.Client, team, app, where string, err error) {
	if *f.app == "" {
		linked := false
		if *f.team == "" {
			_, _, lerr := config.FindLink(a.wd)
			if lerr != nil && !errors.Is(lerr, config.ErrNotLinked) {
				return nil, "", "", "", lerr
			}
			linked = lerr == nil
		}
		if !linked {
			if *f.env != "" {
				return nil, "", "", "", usageErr("--env is an app's environment: name the app with --app, or link this folder")
			}
			if c, err = a.client(); err != nil {
				return nil, "", "", "", err
			}
			who, err := c.WhoAmI(ctx)
			if err != nil {
				return nil, "", "", "", err
			}
			t, err := a.pickTeam(who, *f.team)
			if err != nil {
				return nil, "", "", "", err
			}
			return c, t.ID, "", t.Slug, nil
		}
	}
	c, t, _, err := a.envTarget(ctx, f)
	if err != nil {
		return nil, "", "", "", err
	}
	return c, t.link.Team, t.link.App, envWhere(t), nil
}

// auditRefusal is a refused read said with the flag it's about.
func auditRefusal(err error) error {
	var e *cloud.Error
	if errors.As(err, &e) && e.Field != "" && e.Field != "app" {
		return fmt.Errorf("--%s: %s", e.Field, e.Message)
	}
	return err
}

// auditWho is who did it: a person by name and email, a token by its
// name, Homeport support and Homeport by theirs.
func auditWho(e cloud.AuditEntry) string {
	switch {
	case e.ActorKind == "token":
		return e.ActorName + " (API token)"
	case e.ActorKind == "user" && e.ActorName != "" && e.ActorEmail != "":
		return e.ActorName + " (" + e.ActorEmail + ")"
	case e.ActorKind == "user" && e.ActorEmail != "":
		return e.ActorEmail
	}
	return e.ActorName
}

// auditApp is the app environment it was done to: "" the team's own.
func auditApp(e cloud.AuditEntry) string {
	if e.AppName == "" || e.Environment == "" {
		return e.AppName
	}
	return e.AppName + " (" + e.Environment + ")"
}

// auditVia is how it was done, as said: support's and Homeport's go
// unsaid, their names say it.
var auditVia = map[string]string{"dashboard": "dashboard", "cli": "CLI", "api": "API"}

// auditColumns are an export's CSV columns: the Activity page's.
var auditColumns = []string{"at", "actor_kind", "actor_name", "actor_email", "via", "ip", "user_agent", "action", "kind",
	"app", "app_name", "environment", "target", "detail"}

// auditExport reads every entry that matches, newest first, up to
// auditExportMost, and writes them to output (default: activity-<date>.
// <format> here, never over a file that's there; - stdout) as the Activity
// page's export does: CSV with a closing # line when it stopped, or JSON
// as {entries, truncated, note}.
func (a *app) auditExport(ctx context.Context, c *cloud.Client, team string, filter cloud.AuditFilter, format, output string) error {
	var entries []cloud.AuditEntry
	for cursor := ""; ; {
		page, err := c.Audit(ctx, team, filter, cursor, cloud.MaxAuditPage)
		if err != nil {
			return auditRefusal(err)
		}
		entries = append(entries, page.Entries...)
		if cursor = page.Next; cursor == "" || len(entries) > auditExportMost {
			break
		}
	}
	note := ""
	if len(entries) > auditExportMost {
		entries = entries[:auditExportMost]
		note = auditCut(auditExportMost, entries[auditExportMost-1].At)
	}
	var b bytes.Buffer
	if format == "json" {
		body, err := json.MarshalIndent(struct {
			Entries   []cloud.AuditEntry `json:"entries"`
			Truncated bool               `json:"truncated"`
			Note      string             `json:"note,omitempty"`
		}{append([]cloud.AuditEntry{}, entries...), note != "", note}, "", "  ")
		if err != nil {
			return err
		}
		b.Write(body)
		b.WriteString("\n")
	} else {
		w := csv.NewWriter(&b)
		_ = w.Write(auditColumns)
		for _, e := range entries {
			_ = w.Write([]string{e.At.UTC().Format(time.RFC3339Nano), e.ActorKind, e.ActorName, e.ActorEmail, e.Via, e.IP, e.UserAgent,
				e.Action, e.Kind, e.App, e.AppName, e.Environment, e.Target, string(e.Detail)})
		}
		w.Flush()
		if err := w.Error(); err != nil {
			return err
		}
		if note != "" {
			b.WriteString("# " + note + "\n")
		}
	}
	if output == "-" {
		if _, err := io.Copy(a.out, &b); err != nil {
			return err
		}
	} else {
		if output == "" {
			output = "activity-" + time.Now().UTC().Format(time.DateOnly) + "." + format
		}
		path := output
		if !filepath.IsAbs(path) {
			path = filepath.Join(a.wd, path)
		}
		fh, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("%s is there already: name another with --output", output)
		}
		if err != nil {
			return err
		}
		if _, err := io.Copy(fh, &b); err != nil {
			fh.Close()
			return err
		}
		if err := fh.Close(); err != nil {
			return err
		}
		fmt.Fprintf(a.err, "Wrote %s to %s.\n", entriesSaid(len(entries)), output)
	}
	if note != "" {
		fmt.Fprintln(a.err, note)
	}
	return nil
}

// auditCut says an export stopped at most entries, the oldest of them at
// oldest, and how to get the rest: --until the day after it, so none
// from that day is missed.
func auditCut(most int, oldest time.Time) string {
	return fmt.Sprintf("Stopped at %s, newest first: for older ones, export again with --until %s, or narrow the range.",
		entriesSaid(most), oldest.UTC().AddDate(0, 0, 1).Format(time.DateOnly))
}

func entriesSaid(n int) string {
	if n == 1 {
		return "1 entry"
	}
	return thousands(n) + " entries"
}

// thousands is n with its thousands separated: 100,000.
func thousands(n int) string {
	s := strconv.Itoa(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}
