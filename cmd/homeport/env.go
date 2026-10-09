package main

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
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
		t = &target{link: *l}
		if *envName != "" && *envName != l.Environment {
			if t, err = a.resolve(ctx, c, l.TeamSlug, l.Project, *envName, false); err != nil {
				return err
			}
		}
	}
	values, err := c.PullEnv(ctx, t.link.Team, t.link.App)
	if err != nil {
		return err
	}
	names := slices.Sorted(maps.Keys(values))
	if len(names) == 0 {
		fmt.Fprintf(a.out, "%s/%s (%s) has no database or storage: nothing to pull.\n", t.link.TeamSlug, t.link.Project, t.link.Environment)
		return nil
	}
	path := filepath.Join(dir, localEnv)
	if err := writeLocalEnv(path, values); err != nil {
		return err
	}
	ignored, err := ignoreLocalEnv(dir)
	if err != nil {
		return err
	}
	fmt.Fprintf(a.out, "Wrote %s's add-on credentials to %s: %s.\n", t.link.Environment, localEnv, strings.Join(names, ", "))
	if ignored {
		fmt.Fprintf(a.out, "Added %s to .gitignore: never commit it.\n", localEnv)
	}
	return nil
}

// writeLocalEnv puts values in the env file at path, readable by its owner
// alone: the keys it pulls replaced where they are (and once), every other
// line left as it was, new keys after.
func writeLocalEnv(path string, values map[string]string) error {
	old, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	written := map[string]bool{}
	var out []string
	if len(old) > 0 {
		for _, line := range strings.Split(strings.TrimSuffix(string(old), "\n"), "\n") {
			key := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "export "))
			key, _, _ = strings.Cut(key, "=")
			key = strings.TrimSpace(key)
			if _, ours := values[key]; !ours {
				out = append(out, line)
				continue
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

// ignoreLocalEnv adds .env.local to dir's .gitignore unless something there
// covers it already; whether it did.
func ignoreLocalEnv(dir string) (bool, error) {
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
