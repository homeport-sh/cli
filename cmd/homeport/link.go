package main

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/homeport-sh/cli/internal/cloud"
	"github.com/homeport-sh/cli/internal/config"
)

// link ties this folder to one of your apps' environments - the one
// `homeport deploy` deploys - in .homeport/link.json, which git ignores.
// Only an environment homeport builds can be linked: one its own CI
// deploys is deployed from there.
func (a *app) link(ctx context.Context, args []string) error {
	fs := a.flags("link")
	team := fs.String("team", "", "the team, by its address (slug)")
	appName := fs.String("app", "", "the app, by name")
	env := fs.String("env", "", "the environment (default: production)")
	if err := parse(fs, args); err != nil {
		return err
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	target, err := a.resolve(ctx, c, *team, *appName, *env, true)
	if err != nil {
		return err
	}
	if err := config.SaveLink(a.wd, target.link); err != nil {
		return err
	}
	fmt.Fprintf(a.out, "Linked this folder to %s/%s (%s). `homeport deploy` deploys it.\n",
		target.link.TeamSlug, target.link.Project, target.link.Environment)
	return nil
}

// target is an environment to deploy, and the dashboard it's on.
type target struct {
	link      config.Link
	domain    string
	dashboard string
}

// resolve finds the environment the flags name - asking for what they
// don't, when there's a terminal to ask in. hostedOnly: only those
// homeport builds (to link or deploy); reading, variables and add-ons are
// any environment's.
func (a *app) resolve(ctx context.Context, c *cloud.Client, teamSlug, appName, envName string, hostedOnly bool) (*target, error) {
	who, err := c.WhoAmI(ctx)
	if err != nil {
		return nil, err
	}
	if len(who.Teams) == 0 {
		return nil, fmt.Errorf("you aren't in a team yet: make one at %s", orDefault(who.Dashboard))
	}
	var team cloud.Team
	switch {
	case teamSlug != "":
		i := slices.IndexFunc(who.Teams, func(t cloud.Team) bool { return t.Slug == teamSlug || t.ID == teamSlug })
		if i < 0 {
			return nil, usageErr("you aren't in a team %q", teamSlug)
		}
		team = who.Teams[i]
	case len(who.Teams) == 1:
		team = who.Teams[0]
	default:
		var names []string
		for _, t := range who.Teams {
			names = append(names, t.Slug)
		}
		i, err := a.choose("team", "--team", names, -1)
		if err != nil {
			return nil, err
		}
		team = who.Teams[i]
	}
	apps, err := c.Apps(ctx, team.ID)
	if err != nil {
		return nil, err
	}
	// the environments homeport builds, by app
	byApp := map[string][]cloud.App{}
	var names []string
	for _, e := range apps {
		if hostedOnly && e.Builds != "hosted" {
			continue
		}
		if _, ok := byApp[e.ProjectName]; !ok {
			names = append(names, e.ProjectName)
		}
		byApp[e.ProjectName] = append(byApp[e.ProjectName], e)
	}
	slices.Sort(names)
	if appName == "" {
		if len(names) == 0 {
			return nil, fmt.Errorf("%s has no app homeport builds from a repository: create one at %s", team.Slug, orDefault(who.Dashboard))
		}
		i, err := a.choose("app", "--app", names, -1)
		if err != nil {
			return nil, err
		}
		appName = names[i]
	}
	envs, ok := byApp[appName]
	if !ok {
		for _, e := range apps {
			if e.ProjectName == appName {
				return nil, usageErr("%s is deployed by %s, not built by homeport: deploy it from there", appName, sourceName(e.Builds))
			}
		}
		return nil, usageErr("%s has no app %q", team.Slug, appName)
	}
	slices.SortFunc(envs, func(x, y cloud.App) int {
		switch {
		case x.Environment == "production":
			return -1
		case y.Environment == "production":
			return 1
		}
		return strings.Compare(x.Environment, y.Environment)
	})
	var env cloud.App
	switch {
	case envName != "":
		i := slices.IndexFunc(envs, func(e cloud.App) bool { return e.Environment == envName })
		if i < 0 {
			return nil, usageErr("%s has no environment %q built by homeport", appName, envName)
		}
		env = envs[i]
	case len(envs) == 1:
		env = envs[0]
	default:
		var names []string
		for _, e := range envs {
			names = append(names, e.Environment)
		}
		i, err := a.choose("environment", "--env", names, 0)
		if err != nil {
			return nil, err
		}
		env = envs[i]
	}
	return &target{link: config.Link{Team: team.ID, TeamSlug: team.Slug, App: env.ID, Project: env.ProjectName, Environment: env.Environment},
		domain: env.Domain, dashboard: orDefault(who.Dashboard)}, nil
}

// linked is the environment a link's ids name, as the API names it. A
// link's names are labels for people: one whose labels disagree with what
// its ids are now (edited, or renamed since) is refused, never shown as
// one environment while acting on another. hostedOnly as for resolve.
func (a *app) linked(ctx context.Context, c *cloud.Client, l config.Link, hostedOnly bool) (*target, error) {
	who, err := c.WhoAmI(ctx)
	if err != nil {
		return nil, err
	}
	i := slices.IndexFunc(who.Teams, func(t cloud.Team) bool { return t.ID == l.Team })
	if i < 0 {
		return nil, usageErr("this folder's link names a team you aren't in: run `homeport link` again")
	}
	team := who.Teams[i]
	apps, err := c.Apps(ctx, team.ID)
	if err != nil {
		return nil, err
	}
	j := slices.IndexFunc(apps, func(e cloud.App) bool { return e.ID == l.App })
	if j < 0 {
		return nil, usageErr("this folder's link names an environment %s doesn't have (deleted?): run `homeport link` again", team.Slug)
	}
	env := apps[j]
	if team.Slug != l.TeamSlug || env.ProjectName != l.Project || env.Environment != l.Environment {
		return nil, usageErr("this folder's link says %s/%s (%s), but it is %s/%s (%s): run `homeport link` again",
			l.TeamSlug, l.Project, l.Environment, team.Slug, env.ProjectName, env.Environment)
	}
	if hostedOnly && env.Builds != "hosted" {
		return nil, usageErr("%s/%s (%s) is deployed by %s, not built by homeport", team.Slug, env.ProjectName, env.Environment, sourceName(env.Builds))
	}
	return &target{link: config.Link{Team: team.ID, TeamSlug: team.Slug, App: env.ID, Project: env.ProjectName, Environment: env.Environment},
		domain: env.Domain, dashboard: orDefault(who.Dashboard)}, nil
}

func sourceName(builds string) string {
	if builds == "upload" {
		return "upload"
	}
	return "its own CI"
}

// orDefault is the dashboard's address, homeport.sh's if the API didn't say.
func orDefault(dashboard string) string {
	if dashboard == "" {
		return "https://app.homeport.sh"
	}
	return dashboard
}
