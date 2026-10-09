package main

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/homeport-sh/cli/envfile"
	"github.com/homeport-sh/cli/internal/config"
	"github.com/homeport-sh/cli/internal/source"
)

// localEnv is where `homeport env pull` writes.
const localEnv = ".env.local"

// env is `homeport env pull`: an environment's add-on credentials - its
// database's and storage's variables - into .env.local, to develop against
// them. Never production's (the platform refuses), never the app's own
// variables, and no value is ever printed.
func (a *app) env(ctx context.Context, args []string) error {
	if len(args) == 0 || args[0] != "pull" {
		return usageErr("usage: homeport env pull [--env <environment>]")
	}
	fs := a.flags("env pull")
	envName := fs.String("env", "", "the environment (default: the linked one); never production")
	team := fs.String("team", "", "the team, by its address (instead of the link)")
	appName := fs.String("app", "", "the app, by name (instead of the link)")
	if err := parse(fs, args[1:]); err != nil {
		return err
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	dir := a.wd
	var t *target
	switch {
	case *appName != "" || *team != "":
		if t, err = a.resolve(ctx, c, *team, *appName, *envName, false); err != nil {
			return err
		}
	default:
		l, at, err := config.FindLink(a.wd)
		if errors.Is(err, config.ErrNotLinked) {
			return withCode(exitUsage, err)
		}
		if err != nil {
			return err
		}
		dir = at
		// named as the API names the link's ids, never by its labels
		if t, err = a.linked(ctx, c, *l, false); err != nil {
			return err
		}
		if *envName != "" && *envName != t.link.Environment {
			if t, err = a.resolve(ctx, c, t.link.TeamSlug, t.link.Project, *envName, false); err != nil {
				return err
			}
		}
	}
	values, withheld, err := c.PullEnv(ctx, t.link.Team, t.link.App)
	if err != nil {
		return err
	}
	where := fmt.Sprintf("%s/%s (%s)", t.link.TeamSlug, t.link.Project, t.link.Environment)
	if len(withheld) > 0 {
		fmt.Fprintf(a.out, "Withheld %s: production uses that add-on too, and production's credentials aren't pulled.\n", strings.Join(withheld, ", "))
	}
	names := slices.Sorted(maps.Keys(values))
	// ignored first: .env.local is never written where git would take it
	ignored, err := ignoreLocalEnv(dir)
	if err != nil {
		return fmt.Errorf("making git ignore %s, so nothing was written: %w", localEnv, err)
	}
	if ignored {
		fmt.Fprintf(a.out, "Added %s to .gitignore: never commit it.\n", localEnv)
	}
	if err := writeLocalEnv(filepath.Join(dir, localEnv), values); err != nil {
		return err
	}
	if len(names) == 0 {
		fmt.Fprintf(a.out, "%s has no database or storage to pull: %s holds none of its credentials now.\n", where, localEnv)
		return nil
	}
	fmt.Fprintf(a.out, "Wrote %s's add-on credentials to %s: %s.\n", where, localEnv, strings.Join(names, ", "))
	return nil
}

// ownsLine is the first line of a .env.local a pull wrote: the keys it
// owns, so a later pull can remove one the environment no longer has.
const ownsLine = "# homeport env pull: "

// writeLocalEnv puts values in the env file at path, readable by its owner
// alone: the keys it pulls replaced where they are (and once), the keys
// an earlier pull wrote that aren't pulled now removed, every other line
// left as it was, new keys after.
func writeLocalEnv(path string, values map[string]string) error {
	old, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	owned := map[string]bool{}
	for k := range values {
		owned[k] = true
	}
	lines := []string{}
	if len(old) > 0 {
		lines = strings.Split(strings.TrimSuffix(string(old), "\n"), "\n")
	}
	if len(lines) > 0 && strings.HasPrefix(lines[0], ownsLine) {
		for _, k := range strings.Fields(strings.TrimPrefix(lines[0], ownsLine)) {
			owned[k] = true // an earlier pull's: replaced, or gone
		}
		lines = lines[1:]
	}
	written := map[string]bool{}
	out := []string{ownsLine + strings.Join(slices.Sorted(maps.Keys(values)), " ")}
	if len(values) == 0 {
		out = nil
	}
	for _, line := range lines {
		key := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "export "))
		key, _, _ = strings.Cut(key, "=")
		key = strings.TrimSpace(key)
		if !owned[key] {
			out = append(out, line)
			continue
		}
		if _, now := values[key]; !now {
			continue // an earlier pull's, gone from the environment
		}
		if written[key] {
			continue // an older copy of a key it owns
		}
		l, err := envfile.Line(key, values[key])
		if err != nil {
			return fmt.Errorf("%s: %w", key, err)
		}
		out, written[key] = append(out, l), true
	}
	for _, key := range slices.Sorted(maps.Keys(values)) {
		if written[key] {
			continue
		}
		l, err := envfile.Line(key, values[key])
		if err != nil {
			return fmt.Errorf("%s: %w", key, err)
		}
		out = append(out, l)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".env.local-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.WriteString(strings.Join(out, "\n") + "\n"); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// ignoreLocalEnv adds .env.local to dir's .gitignore unless it's ignored
// already - git says, in a checkout (a .gitignore above counts), else the
// folder's own .gitignore - and whether it did.
func ignoreLocalEnv(dir string) (bool, error) {
	if out, err := exec.Command("git", "-C", dir, "rev-parse", "--is-inside-work-tree").Output(); err == nil && strings.TrimSpace(string(out)) == "true" {
		if exec.Command("git", "-C", dir, "check-ignore", "-q", localEnv).Run() == nil {
			return false, nil
		}
	}
	p := filepath.Join(dir, ".gitignore")
	b, err := os.ReadFile(p)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	if source.Covers(string(b), localEnv) {
		return false, nil
	}
	if len(b) > 0 && !strings.HasSuffix(string(b), "\n") {
		b = append(b, '\n')
	}
	return true, os.WriteFile(p, append(b, []byte(localEnv+"\n")...), 0o644)
}
