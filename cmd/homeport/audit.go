package main

import (
	"context"
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

// audit is `homeport audit`: a team's activity (design/audit-log.md in
// homeport cloud) - who changed what, and when - newest first. In a linked
// folder it's the linked environment's (--env another's, --all-envs every
// environment's of the app); --team alone, or a folder nothing links, the
// whole team's; --app an app's, every environment of it, or with --env
// one's. The API reads the names, so a token that may only audit:read
// names them too. --json is a page as the API answers it; --export
// downloads the Activity page's export of every entry that matches.
func (a *app) audit(ctx context.Context, args []string) error {
	fs, f := a.flags("audit"), envFlags{}
	f.app = fs.String("app", "", "an app, by name: every environment of it, unless --env names one (instead of the link)")
	f.env = fs.String("env", "", "one environment, by name (default: the linked one)")
	f.team = fs.String("team", "", "the team, by its address (instead of the link)")
	allEnvs := fs.Bool("all-envs", false, "every environment of the linked app")
	actor := fs.String("actor", "", "who: a person's email, an API token's name, support (Homeport support) or homeport (Homeport itself)")
	kind := fs.String("kind", "", "a kind of action: people, access, billing, apps, data, domains, jobs, support or homeport")
	since := fs.String("since", "", "from: a time (2026-11-15T09:00:00Z), a day (2026-11-15, UTC) or an age (12h, 7d)")
	until := fs.String("until", "", "to: a time, a day (UTC, the day included) or an age")
	limit := fs.Int("limit", 50, "how many, newest first: 1 to 1000")
	cursor := fs.String("cursor", "", "with --json: the page after one, by its next")
	asJSON := fs.Bool("json", false, "the page as JSON, as the API answers it")
	export := fs.String("export", "", "csv or json: every entry that matches (up to 100,000), to a file, as the Activity page exports it")
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
	case *allEnvs && *f.env != "":
		return usageErr("--all-envs is every environment: --env names one")
	case *allEnvs && *f.app == "" && *f.team != "":
		return usageErr("--all-envs is every environment of an app: name it with --app, or link this folder")
	}
	c, team, filter, where, err := a.auditScope(ctx, f, *allEnvs)
	if err != nil {
		return err
	}
	filter.Actor, filter.Kind, filter.Since, filter.Until = *actor, *kind, *since, *until
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
	// one environment's says none; an app's or the team's says each one's
	oneEnv := filter.Env != ""
	head := []string{"WHEN", "WHO", "WHAT"}
	if !oneEnv {
		head = append(head, "APP")
	}
	rows := [][]string{append(head, "VIA")}
	for _, e := range page.Entries {
		r := []string{e.At.Local().Format("Jan 2 15:04"), auditWho(e), e.Says}
		if !oneEnv {
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

// auditScope is whose activity the flags name, as the API reads it: an
// app's or one environment's (--app and --env by name, or the folder's
// link by its ids), or the team's (--team alone, or no link) - its team's
// id, the filter naming the app and environment, and how to say it. The
// names go to the API as typed: it reads them, for a token that may only
// audit:read too.
func (a *app) auditScope(ctx context.Context, f envFlags, allEnvs bool) (c *cloud.Client, team string, filter cloud.AuditFilter, where string, err error) {
	if c, err = a.client(); err != nil {
		return nil, "", filter, "", err
	}
	var link *config.Link
	if *f.app == "" && *f.team == "" {
		l, _, lerr := config.FindLink(a.wd)
		if lerr != nil && !errors.Is(lerr, config.ErrNotLinked) {
			return nil, "", filter, "", lerr
		}
		link = l
	}
	if link != nil {
		where := link.TeamSlug + "/" + link.Project
		switch {
		case allEnvs:
			return c, link.Team, cloud.AuditFilter{App: link.App}, where + ", every environment", nil
		case *f.env != "" && *f.env != link.Environment:
			return c, link.Team, cloud.AuditFilter{App: link.App, Env: *f.env}, where + " (" + *f.env + ")", nil
		}
		return c, link.Team, cloud.AuditFilter{Env: link.App}, where + " (" + link.Environment + ")", nil
	}
	if *f.env != "" && *f.app == "" {
		return nil, "", filter, "", usageErr("--env is an app's environment: name the app with --app, or link this folder")
	}
	if allEnvs && *f.app == "" {
		return nil, "", filter, "", usageErr("--all-envs is every environment of an app: name it with --app, or link this folder")
	}
	who, err := c.WhoAmI(ctx)
	if err != nil {
		return nil, "", filter, "", err
	}
	t, err := a.pickTeam(who, *f.team)
	if err != nil {
		return nil, "", filter, "", err
	}
	switch {
	case *f.app != "" && *f.env != "":
		return c, t.ID, cloud.AuditFilter{App: *f.app, Env: *f.env}, t.Slug + "/" + *f.app + " (" + *f.env + ")", nil
	case *f.app != "":
		return c, t.ID, cloud.AuditFilter{App: *f.app}, t.Slug + "/" + *f.app + ", every environment", nil
	}
	return c, t.ID, filter, t.Slug, nil
}

// auditFlags are the flags the API's fields are: an answer about "env" is
// --env's.
var auditFlags = map[string]string{"app": "--app", "env": "--env", "actor": "--actor", "kind": "--kind", "since": "--since",
	"until": "--until", "limit": "--limit", "cursor": "--cursor", "format": "--export"}

// auditRefusal is a refused read said with the flag it's about.
func auditRefusal(err error) error {
	var e *cloud.Error
	if errors.As(err, &e) && auditFlags[e.Field] != "" {
		return fmt.Errorf("%s: %s", auditFlags[e.Field], e.Message)
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

// auditExport downloads the Activity page's export of what filter
// matches, as format, to output (default: activity-<date>.<format> here,
// never over a file that's there; - stdout), and says where - and, when
// the API cut it short, so, and how to get the rest.
func (a *app) auditExport(ctx context.Context, c *cloud.Client, team string, filter cloud.AuditFilter, format, output string) error {
	if output == "" {
		output = "activity-" + time.Now().UTC().Format(time.DateOnly) + "." + format
	}
	path := output
	if output != "-" && !filepath.IsAbs(path) {
		path = filepath.Join(a.wd, path)
	}
	if output != "-" {
		if _, err := os.Lstat(path); err == nil {
			return fmt.Errorf("%s is there already: name another with --output", output)
		}
	}
	body, note, err := c.AuditExport(ctx, team, filter, format)
	if err != nil {
		return auditRefusal(err)
	}
	defer body.Close()
	if output == "-" {
		if _, err := io.Copy(a.out, body); err != nil {
			return err
		}
	} else {
		fh, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("%s is there already: name another with --output", output)
		}
		if err != nil {
			return err
		}
		if _, err := io.Copy(fh, body); err != nil {
			fh.Close()
			os.Remove(path)
			return err
		}
		if err := fh.Close(); err != nil {
			return err
		}
		fmt.Fprintf(a.err, "Wrote %s.\n", output)
	}
	if note != "" {
		fmt.Fprintln(a.err, note)
	}
	return nil
}

// thousands is n with its thousands separated: 100,000.
func thousands(n int) string {
	s := strconv.Itoa(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}
