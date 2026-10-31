package main

import (
	"context"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/homeport-sh/cli/internal/cloud"
)

const domainsUsage = `usage: homeport domains ls
       homeport domains add <domain>
       homeport domains get <domain>
       homeport domains remove <domain> [--yes]
       homeport domains connect <domain> [--no-browser]
  each [--env <environment>] [--team <slug> --app <name>]`

// domains is `homeport domains`: an app's platform domains - its tenants'
// own, on the Domains API (design/platforms.md in homeport cloud) -
// listed, added with the DNS records to set, shown, removed, and connected
// (its records set at its DNS provider in one sign-in there).
func (a *app) domains(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return usageErr("%s", domainsUsage)
	}
	switch args[0] {
	case "ls":
		return a.domainsLs(ctx, args[1:])
	case "add":
		return a.domainsAdd(ctx, args[1:])
	case "get":
		return a.domainsGet(ctx, args[1:])
	case "remove":
		return a.domainsRemove(ctx, args[1:])
	case "connect":
		return a.domainsConnect(ctx, args[1:])
	}
	return usageErr("%s", domainsUsage)
}

// domainArg is the one domain args name, as the API takes it: lowercase,
// no trailing dot. Only what can't be a name is refused here (a scheme, a
// path, a port); the platform checks the rest.
func domainArg(pos []string) (string, error) {
	if len(pos) != 1 {
		return "", usageErr("%s", domainsUsage)
	}
	d := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(pos[0])), ".")
	if d == "" || strings.ContainsAny(d, "/:@?# \t") || strings.Contains(d, "..") {
		return "", usageErr("%q isn't a domain: the name alone, as shop.example.com", pos[0])
	}
	return d, nil
}

// domainsLs is `homeport domains ls`: every platform domain, page by page,
// with its status and why it isn't live; and how many the bill counts.
func (a *app) domainsLs(ctx context.Context, args []string) error {
	fs, f := a.envFlagSet("domains ls")
	if err := parse(fs, args); err != nil {
		return err
	}
	c, t, _, err := a.envTarget(ctx, f)
	if err != nil {
		return err
	}
	var all []cloud.Domain
	page := &cloud.Domains{}
	for after := ""; ; after = page.Next {
		if page, err = c.Domains(ctx, t.link.Team, t.link.App, after); err != nil {
			return err
		}
		all = append(all, page.Domains...)
		if page.Next == "" || page.Next == after {
			break
		}
	}
	if len(all) == 0 {
		fmt.Fprintf(a.out, "%s has no platform domains: `homeport domains add <domain>`, or the Domains API from the app.\n", envWhere(t))
		return nil
	}
	var table strings.Builder
	tw := tabwriter.NewWriter(&table, 0, 4, 2, ' ', 0)
	fmt.Fprintf(tw, "DOMAIN\tSTATUS\tADDED\tWHY\t\n")
	for _, d := range all {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t\n", d.Domain, d.Status, day(d.CreatedAt), d.Why)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	for l := range strings.Lines(table.String()) {
		fmt.Fprintln(a.out, strings.TrimRight(l, " \n"))
	}
	fmt.Fprintf(a.out, "\n%d live of %d; the plan includes %d.\n", page.Live, page.Total, page.Included)
	return nil
}

// domainsAdd is `homeport domains add <domain>`: the domain added (the
// same one again is the one there), and what to set at its DNS provider.
func (a *app) domainsAdd(ctx context.Context, args []string) error {
	fs, f := a.envFlagSet("domains add")
	pos, err := parseMixed(fs, args)
	if err != nil {
		return err
	}
	name, err := domainArg(pos)
	if err != nil {
		return err
	}
	c, t, _, err := a.envTarget(ctx, f)
	if err != nil {
		return err
	}
	d, err := c.AddDomain(ctx, t.link.Team, t.link.App, name)
	if err != nil {
		return err
	}
	fmt.Fprintf(a.out, "Added %s to %s.\n", d.Domain, envWhere(t))
	sayDomain(a.out, d)
	return nil
}

// domainsGet is `homeport domains get <domain>`: its status, why it isn't
// live, and its DNS records.
func (a *app) domainsGet(ctx context.Context, args []string) error {
	fs, f := a.envFlagSet("domains get")
	pos, err := parseMixed(fs, args)
	if err != nil {
		return err
	}
	name, err := domainArg(pos)
	if err != nil {
		return err
	}
	c, t, _, err := a.envTarget(ctx, f)
	if err != nil {
		return err
	}
	d, err := c.Domain(ctx, t.link.Team, t.link.App, name)
	if err != nil {
		return err
	}
	fmt.Fprintf(a.out, "%s on %s\n", d.Domain, envWhere(t))
	sayDomain(a.out, d)
	return nil
}

// domainsRemove is `homeport domains remove <domain>`: asked first, as the
// tenant's site stops being served there.
func (a *app) domainsRemove(ctx context.Context, args []string) error {
	fs, f := a.envFlagSet("domains remove")
	yes := fs.Bool("yes", false, "don't ask first (needed without a terminal)")
	pos, err := parseMixed(fs, args)
	if err != nil {
		return err
	}
	name, err := domainArg(pos)
	if err != nil {
		return err
	}
	c, t, _, err := a.envTarget(ctx, f)
	if err != nil {
		return err
	}
	if !*yes {
		if !a.tty {
			return usageErr("removing %s from %s: pass --yes (there's no terminal to ask in)", name, envWhere(t))
		}
		fmt.Fprintf(a.out, "Remove %s from %s? It stops being served there. [y/N] ", name, envWhere(t))
		answer, err := a.line()
		if err != nil {
			return err
		}
		if yes := strings.ToLower(answer); yes != "y" && yes != "yes" {
			fmt.Fprintln(a.out, "Not removed.")
			return nil
		}
	}
	if err := c.RemoveDomain(ctx, t.link.Team, t.link.App, name); err != nil {
		return err
	}
	fmt.Fprintf(a.out, "Removed %s from %s: it's no longer served there.\n", name, envWhere(t))
	return nil
}

// domainsConnect is `homeport domains connect <domain>`: its DNS provider's
// page that sets its records, opened in the browser (or said, without a
// terminal or with --no-browser). The person signs in there and confirms.
func (a *app) domainsConnect(ctx context.Context, args []string) error {
	fs, f := a.envFlagSet("domains connect")
	noBrowser := fs.Bool("no-browser", false, "say the link instead of opening it")
	pos, err := parseMixed(fs, args)
	if err != nil {
		return err
	}
	name, err := domainArg(pos)
	if err != nil {
		return err
	}
	c, t, _, err := a.envTarget(ctx, f)
	if err != nil {
		return err
	}
	link, err := c.ConnectDomain(ctx, t.link.Team, t.link.App, name)
	if err != nil {
		return err
	}
	if a.tty && !*noBrowser && a.open(link) == nil {
		fmt.Fprintf(a.out, "Opened %s - sign in at its DNS provider and confirm the records for %s.\n", link, name)
	} else {
		fmt.Fprintf(a.out, "Open %s in a browser, sign in at its DNS provider, and confirm the records for %s.\n", link, name)
	}
	fmt.Fprintf(a.out, "Then `homeport domains get %s` says when it's live.\n", name)
	return nil
}

// sayDomain is a domain's status, why it isn't live, and the records to
// set at its DNS provider.
func sayDomain(out io.Writer, d *cloud.Domain) {
	fmt.Fprintf(out, "Status: %s", d.Status)
	if d.Why != "" {
		fmt.Fprintf(out, " (%s)", d.Why)
	}
	fmt.Fprintln(out)
	if d.Unrouted != "" {
		fmt.Fprintf(out, "Not reachable yet: %s.\n", d.Unrouted)
	}
	if d.Wildcard != "" {
		fmt.Fprintf(out, "Under the app's wildcard %s: no DNS records to set.\n", d.Wildcard)
	}
	switch d.Certificate {
	case "wildcard":
		fmt.Fprintln(out, "Every name under it is served one wildcard certificate.")
	case "lapsing":
		fmt.Fprint(out, "Its wildcard certificate is lapsing: the certificate it has serves on until it expires, and isn't renewed")
		if d.CertificateWhy != "" {
			fmt.Fprintf(out, " (%s)", d.CertificateWhy)
		}
		fmt.Fprintln(out, ". Its _acme-challenge record below renews it.")
	}
	if len(d.Records) > 0 {
		fmt.Fprintln(out, "Set these at its DNS provider:")
		tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
		fmt.Fprintf(tw, "  TYPE\tNAME\tVALUE\n")
		for _, r := range d.Records {
			fmt.Fprintf(tw, "  %s\t%s\t%s\n", r.Type, r.Name, r.Value)
		}
		tw.Flush()
	}
	if c := d.Connect; c != nil && c.Available && (d.Status != "live" || d.Unrouted != "") {
		fmt.Fprintf(out, "Its DNS is at %s, which can set these itself: `homeport domains connect %s`.\n", c.Provider, d.Domain)
	}
	if d.Status == "pending" && d.DropsAt != "" {
		fmt.Fprintf(out, "It's dropped on %s unless its records are set by then.\n", day(d.DropsAt))
	}
}

// day is an API time as a day, or as it came when it isn't one.
func day(s string) string {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.Format("2006-01-02")
	}
	return s
}
