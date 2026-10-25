package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/homeport-sh/cli/internal/cloud"
)

// rollback is `homeport rollback [<deploy>]`: an earlier release's build
// shipped again, no build, as the dashboard's "Roll back to this" does.
// Alone it lists what can be rolled back to. Its release command runs only
// with --run-release: migrations go forward.
func (a *app) rollback(ctx context.Context, args []string) error {
	fs, f := a.envFlagSet("rollback")
	runRelease := fs.Bool("run-release", false, "run its release command too (skipped by default: migrations go forward)")
	yes := fs.Bool("yes", false, "don't ask first (needed without a terminal)")
	detach := fs.Bool("detach", false, "start the rollback and don't wait for it")
	timeout := fs.Duration("timeout", 30*time.Minute, "how long to wait for it to go live")
	pos, err := parseMixed(fs, args)
	if err != nil {
		return err
	}
	if len(pos) > 1 {
		return usageErr("roll back to one deploy: homeport rollback <deploy>")
	}
	c, t, _, err := a.envTarget(ctx, f)
	if err != nil {
		return err
	}
	detail, err := c.App(ctx, t.link.Team, t.link.App)
	if err != nil {
		return err
	}
	var offers []cloud.Deploy
	for _, d := range detail.Deploys {
		if d.Rollback != nil {
			offers = append(offers, d)
		}
	}
	if len(pos) == 0 {
		a.listRollbacks(t, offers)
		return nil
	}
	d, err := pickRollback(offers, pos[0])
	if err != nil {
		a.listRollbacks(t, offers)
		return withCode(exitUsage, err)
	}
	sha := shortSHA(d.SHA)
	if !*yes {
		if !a.tty {
			return usageErr("rolling %s back to %s: pass --yes (there's no terminal to ask in)", envWhere(t), sha)
		}
		fmt.Fprintf(a.out, "Roll %s back to %s (deployed %s)? Its build ships again; runtime variables and settings stay as they are now.\n",
			envWhere(t), sha, d.CreatedAt.Local().Format("Jan 2 15:04"))
		if d.Rollback.BuildVariablesDiffer {
			fmt.Fprintln(a.out, "It was built with other build variables than the environment has now, and keeps their old values.")
		}
		if len(d.Rollback.Release) > 0 && !*runRelease {
			fmt.Fprintf(a.out, "Its release command (%s) is skipped: --run-release runs it.\n", d.Rollback.Release)
		}
		fmt.Fprint(a.out, "Roll back? [y/N] ")
		answer, err := a.line()
		if err != nil {
			return err
		}
		if yes := strings.ToLower(answer); yes != "y" && yes != "yes" {
			fmt.Fprintln(a.out, "Not rolled back.")
			return nil
		}
	}
	deploy, err := c.Rollback(ctx, t.link.Team, t.link.App, d.ID, *runRelease)
	if err != nil {
		return err
	}
	l := t.link
	page := fmt.Sprintf("%s/%s/%s/%s/deploys", t.dashboard, l.TeamSlug, l.Project, l.Environment)
	fmt.Fprintf(a.out, "Rolling %s back to %s, without a build: %s\n", envWhere(t), sha, page)
	if *detach {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	return a.followDeploy(ctx, c, t, deploy, page)
}

// listRollbacks says what the environment can be rolled back to, newest
// first.
func (a *app) listRollbacks(t *target, offers []cloud.Deploy) {
	if len(offers) == 0 {
		fmt.Fprintf(a.out, "%s has no earlier release to roll back to: one that went live, its build kept (30 days).\n", envWhere(t))
		return
	}
	fmt.Fprintf(a.out, "%s can be rolled back to (homeport rollback <deploy>):\n", envWhere(t))
	for _, d := range offers {
		line := fmt.Sprintf("  %s  %s  deployed %s  kept until %s", d.ID[:min(8, len(d.ID))], shortSHA(d.SHA),
			d.CreatedAt.Local().Format("Jan 2 15:04"), d.Rollback.KeptUntil.Local().Format("Jan 2"))
		if len(d.Rollback.Release) > 0 {
			line += "  release: " + d.Rollback.Release.String() + " (skipped unless --run-release)"
		}
		if d.Rollback.BuildVariablesDiffer {
			line += "  built with other build variables"
		}
		fmt.Fprintln(a.out, line)
	}
}

// pickRollback is the offer which names: a deploy id, or its first 8 or
// more characters (only one may match), or a commit, 7 or more hex (the
// newest release of it).
func pickRollback(offers []cloud.Deploy, which string) (*cloud.Deploy, error) {
	which = strings.ToLower(strings.TrimSpace(which))
	var byID []int
	for i, d := range offers {
		if d.ID == which || (len(which) >= 8 && strings.HasPrefix(d.ID, which)) {
			byID = append(byID, i)
		}
	}
	switch {
	case len(byID) == 1:
		return &offers[byID[0]], nil
	case len(byID) > 1:
		return nil, fmt.Errorf("%q is the start of %d deploys' ids: give more of it", which, len(byID))
	}
	if len(which) >= 7 && isHex(which) {
		for i, d := range offers { // newest first
			if strings.HasPrefix(strings.ToLower(d.SHA), which) {
				return &offers[i], nil
			}
		}
	}
	return nil, fmt.Errorf("%q isn't a release this environment can be rolled back to", which)
}

func isHex(s string) bool {
	for _, r := range s {
		if !strings.ContainsRune("0123456789abcdef", r) {
			return false
		}
	}
	return true
}

func shortSHA(sha string) string { return sha[:min(7, len(sha))] }
