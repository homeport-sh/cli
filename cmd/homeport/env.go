package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"text/tabwriter"

	"github.com/homeport-sh/cli/envfile"
	"github.com/homeport-sh/cli/internal/cloud"
	"github.com/homeport-sh/cli/internal/config"
	"github.com/homeport-sh/cli/internal/source"
)

// localEnv is where `homeport env pull` writes.
const localEnv = ".env.local"

const envUsage = `usage: homeport env set NAME=value…
       homeport env set [NAME] --from-file <path>
       homeport env unset NAME…
       homeport env ls
       homeport env pull
  each [--env <environment>] [--team <slug> --app <name>]`

// env is `homeport env`: an environment's variables set, removed and listed
// (never a value shown), and `pull`.
func (a *app) env(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return usageErr("%s", envUsage)
	}
	switch args[0] {
	case "pull":
		return a.envPull(ctx, args[1:])
	case "set":
		return a.envSet(ctx, args[1:])
	case "unset":
		return a.envUnset(ctx, args[1:])
	case "ls":
		return a.envLs(ctx, args[1:])
	}
	return usageErr("%s", envUsage)
}

// envFlags are the flags naming the environment, every env command's.
type envFlags struct{ env, team, app *string }

func (a *app) envFlagSet(name string) (*flag.FlagSet, envFlags) {
	fs := a.flags(name)
	return fs, envFlags{
		env:  fs.String("env", "", "the environment (default: the linked one)"),
		team: fs.String("team", "", "the team, by its address (instead of the link)"),
		app:  fs.String("app", "", "the app, by name (instead of the link)"),
	}
}

// parseMixed parses args with flags among the arguments (`env set KEY
// --from-file f --env staging`), and answers the arguments.
func parseMixed(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return nil, err
			}
			return nil, withCode(exitUsage, err)
		}
		if fs.NArg() == 0 {
			return pos, nil
		}
		pos, args = append(pos, fs.Arg(0)), fs.Args()[1:]
	}
}

// envTarget is the environment the flags name, else the folder's link's,
// and the folder the link is in (a.wd without one).
func (a *app) envTarget(ctx context.Context, f envFlags) (*cloud.Client, *target, string, error) {
	c, err := a.client()
	if err != nil {
		return nil, nil, "", err
	}
	if *f.app != "" || *f.team != "" {
		t, err := a.resolve(ctx, c, *f.team, *f.app, *f.env, false)
		return c, t, a.wd, err
	}
	l, dir, err := config.FindLink(a.wd)
	if errors.Is(err, config.ErrNotLinked) {
		return nil, nil, "", withCode(exitUsage, err)
	}
	if err != nil {
		return nil, nil, "", err
	}
	// named as the API names the link's ids, never by its labels
	t, err := a.linked(ctx, c, *l, false)
	if err != nil {
		return nil, nil, "", err
	}
	if *f.env != "" && *f.env != t.link.Environment {
		if t, err = a.resolve(ctx, c, t.link.TeamSlug, t.link.Project, *f.env, false); err != nil {
			return nil, nil, "", err
		}
	}
	return c, t, dir, nil
}

func envWhere(t *target) string {
	return fmt.Sprintf("%s/%s (%s)", t.link.TeamSlug, t.link.Project, t.link.Environment)
}

// secretPath is where the app reads file variable name.
func secretPath(name string) string { return envfile.SecretsDir + "/" + name }

// readFileVar reads the file at path (from dir, when relative) as file
// variable name - its name from the file's when name is "" - checked as
// the platform would.
func readFileVar(dir, name, path string) (string, []byte, error) {
	if name == "" {
		if name = envfile.NameFromFile(path); name == "" {
			return "", nil, usageErr("no variable name comes of %q: name it, `homeport env set NAME --from-file %s`", path, path)
		}
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(dir, path)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", nil, err
	}
	return name, b, envfile.CheckFile(name, b)
}

// envSet is `homeport env set`: plain variables as NAME=value, or one file
// variable from a file here - read here, its bytes sent as they are. What
// the platform would refuse is refused before anything is sent; a value is
// never printed.
func (a *app) envSet(ctx context.Context, args []string) error {
	fs, f := a.envFlagSet("env set")
	fromFile := fs.String("from-file", "", "a file variable from this file: the app reads it at "+envfile.SecretsDir+"/NAME, $NAME holds that path")
	pos, err := parseMixed(fs, args)
	if err != nil {
		return err
	}
	set := map[string]string{}
	var fileName string
	var content []byte
	if *fromFile != "" {
		if len(pos) > 1 {
			return usageErr("--from-file sets one name: %s", strings.Join(pos, " "))
		}
		name := ""
		if len(pos) == 1 {
			if name = pos[0]; strings.Contains(name, "=") {
				return usageErr("with --from-file, the name alone: %s", name)
			}
		}
		if fileName, content, err = readFileVar(a.wd, name, *fromFile); err != nil {
			return err
		}
	} else {
		if len(pos) == 0 {
			return usageErr("%s", envUsage)
		}
		for _, arg := range pos {
			name, value, ok := strings.Cut(arg, "=")
			if !ok {
				return usageErr("%s: NAME=value, or a file's content with `homeport env set %s --from-file <path>`", arg, arg)
			}
			if strings.ContainsAny(value, "\r\n") {
				return fmt.Errorf("%s: a value is one line; a multi-line one is a file variable: `homeport env set %s --from-file <path>`", name, name)
			}
			if _, err := envfile.Line(name, value); err != nil {
				return err
			}
			set[name] = value
		}
	}
	c, t, _, err := a.envTarget(ctx, f)
	if err != nil {
		return err
	}
	var files map[string][]byte
	if fileName != "" {
		files = map[string][]byte{fileName: content}
	}
	if _, err := c.SetEnv(ctx, t.link.Team, t.link.App, set, files, nil); err != nil {
		return err
	}
	if fileName != "" {
		fmt.Fprintf(a.out, "Set %s on %s: %s.\nThe app reads it at %s ($%s holds that path), and restarts to get it.\n",
			fileName, envWhere(t), envfile.Inspect(content).Summary(), secretPath(fileName), fileName)
		return nil
	}
	fmt.Fprintf(a.out, "Set %s on %s: the app restarts to get them.\n", strings.Join(slices.Sorted(maps.Keys(set)), ", "), envWhere(t))
	return nil
}

// envUnset is `homeport env unset`: variables removed, plain or file.
func (a *app) envUnset(ctx context.Context, args []string) error {
	fs, f := a.envFlagSet("env unset")
	pos, err := parseMixed(fs, args)
	if err != nil {
		return err
	}
	if len(pos) == 0 {
		return usageErr("%s", envUsage)
	}
	for _, n := range pos {
		if !envfile.ValidName(n) {
			return usageErr("%q isn't a variable's name", n)
		}
	}
	c, t, _, err := a.envTarget(ctx, f)
	if err != nil {
		return err
	}
	if _, err := c.SetEnv(ctx, t.link.Team, t.link.App, nil, nil, pos); err != nil {
		return err
	}
	fmt.Fprintf(a.out, "Removed %s from %s: the app restarts without them.\n", strings.Join(pos, ", "), envWhere(t))
	return nil
}

// envLs is `homeport env ls`: an environment's variables by name, and of a
// file variable where the app reads it and what it is. No value.
func (a *app) envLs(ctx context.Context, args []string) error {
	fs, f := a.envFlagSet("env ls")
	if err := parse(fs, args); err != nil {
		return err
	}
	c, t, _, err := a.envTarget(ctx, f)
	if err != nil {
		return err
	}
	env, err := c.Env(ctx, t.link.Team, t.link.App)
	if err != nil {
		return err
	}
	if len(env.Names) == 0 {
		fmt.Fprintf(a.out, "%s has no variables.\n", envWhere(t))
		return nil
	}
	files := map[string]envfile.FileMeta{}
	for _, fv := range env.Files {
		files[fv.Name] = fv.Meta
	}
	tw := tabwriter.NewWriter(a.out, 0, 4, 2, ' ', 0)
	for _, n := range env.Names {
		if m, ok := files[n]; ok {
			fmt.Fprintf(tw, "%s\tfile\t%s\t%s\n", n, secretPath(n), m.Summary())
			continue
		}
		fmt.Fprintf(tw, "%s\tplain\t\t\n", n)
	}
	return tw.Flush()
}

// envPull is `homeport env pull`: an environment's add-on credentials - its
// database's and storage's variables - into .env.local, to develop against
// them. Never production's (the platform refuses), never the app's own
// variables, and no value is ever printed.
func (a *app) envPull(ctx context.Context, args []string) error {
	fs, f := a.envFlagSet("env pull")
	if err := parse(fs, args); err != nil {
		return err
	}
	c, t, dir, err := a.envTarget(ctx, f)
	if err != nil {
		return err
	}
	values, withheld, err := c.PullEnv(ctx, t.link.Team, t.link.App)
	if err != nil {
		return err
	}
	where := envWhere(t)
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
