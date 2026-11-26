package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/homeport-sh/cli/buildplan"
	"github.com/homeport-sh/cli/internal/cloud"
)

// offer is what resolve does for an app the team doesn't have: make it -
// an app with no repository, deployed from the CLI - once the person says
// so, or with --yes.
type offer struct {
	yes bool
	// fromFolder: the name is the folder's, not one --app gave. An app the
	// team has by that name isn't deployed to unasked: linking it is a
	// choice the person makes.
	fromFolder bool
	// root: the app's folder in what deploy uploads (source.Folder)
	root string
}

// errNotCreated is a person's no to making an app: nothing more to do.
var errNotCreated = errors.New("not created")

// maxAppName is the longest an app's name is (an address's label).
const maxAppName = 40

// appNameFor is the app a folder named dir deploys as when nothing names
// it: lowercase letters, digits and dashes, up to 40, not starting or
// ending with a dash ("" when dir has none of them).
func appNameFor(dir string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(dir) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			b.WriteRune(r)
			dash = false
			continue
		}
		dash = true
	}
	name := b.String()
	if len(name) > maxAppName {
		name = strings.TrimRight(name[:maxAppName], "-")
	}
	return name
}

// folderApp is the app this folder deploys as, unnamed: its name's.
func (a *app) folderApp() (string, error) {
	base := filepath.Base(a.wd)
	name := appNameFor(base)
	if name == "" {
		return "", usageErr("%q can't name an app: name it with --app (lowercase letters, digits and dashes)", base)
	}
	return name, nil
}

// folderBelow: a folder deployed for the first time that builds nothing,
// with its app in the one folder below it that does (owl-app/), deploys
// that folder, asked first - false if the person says no. With yes, or no
// terminal, it refuses, so no app that can't build is made.
func (a *app) folderBelow(yes bool) (bool, error) {
	below := buildplan.FoldersBelow(os.DirFS(a.wd), "")
	switch {
	case len(below) == 0:
		return true, nil
	case len(below) > 1:
		return false, usageErr("nothing in this folder builds, but %s/ do: run `homeport deploy` from the app's folder", strings.Join(below, "/, "))
	case yes || !a.tty:
		return false, usageErr("nothing in this folder builds, but %s/ does: run `homeport deploy` from %s/", below[0], below[0])
	}
	fmt.Fprintf(a.out, "Nothing in this folder builds, but %s/ does. Deploy %s/? [Y/n] ", below[0], below[0])
	answer, err := a.line()
	if err != nil {
		return false, err
	}
	if no := strings.ToLower(strings.TrimSpace(answer)); no != "" && no != "y" && no != "yes" {
		fmt.Fprintln(a.out, "Not deployed.")
		return false, nil
	}
	a.wd = filepath.Join(a.wd, below[0])
	return true, nil
}

// create makes team the app name - its environment env ("" production) -
// asking first unless o.yes; errNotCreated if the person says no.
func (a *app) create(ctx context.Context, c *cloud.Client, team cloud.Team, name, env string, o *offer) (*cloud.App, error) {
	if !o.yes {
		if !a.tty {
			return nil, usageErr("no app %q on team %q: pass --yes to create it (there's no terminal to ask in), or run `homeport link` to pick one of its apps",
				name, team.Slug)
		}
		fmt.Fprintf(a.out, "No app %q on team %q. Create it? [y/N] ", name, team.Slug)
		answer, err := a.line()
		if err != nil {
			return nil, err
		}
		if yes := strings.ToLower(answer); yes != "y" && yes != "yes" {
			fmt.Fprintln(a.out, "Not created.")
			return nil, errNotCreated
		}
	}
	made, err := c.CreateApp(ctx, team.ID, cloud.NewApp{Name: name, Environment: env, Root: o.root})
	if err != nil {
		return nil, err
	}
	fmt.Fprintf(a.out, "Created %s/%s (%s), deployed from your computer.\n", team.Slug, made.ProjectName, made.Environment)
	return made, nil
}
