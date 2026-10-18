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

const envUsage = `usage: homeport env set NAME=value… [--build | --build-only | --runtime] [--secret | --plain]
       homeport env set NAME… --build | --build-only | --runtime | --secret | --plain
       homeport env set NAME=value… --for ROLE… [--secret | --plain]
       homeport env set [NAME] --from-file <path>
       homeport env unset NAME… [--for ROLE…]
       homeport env ls
       homeport env rebuild
       homeport env pull
  each [--env <environment>] [--team <slug> --app <name>]`

// env is `homeport env`: an environment's variables set, removed and listed
// (a secret's value never shown), and `pull`.
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
	case "rebuild":
		return a.envRebuild(ctx, args[1:])
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

// roleFlags is --for, given once for each role: web, release or a
// process's name.
type roleFlags []string

func (r *roleFlags) String() string { return strings.Join(*r, ",") }

func (r *roleFlags) Set(v string) error {
	if !validRole(v) {
		return fmt.Errorf("%q isn't a part of the app: web, release or a process's name", v)
	}
	if !slices.Contains(*r, v) {
		*r = append(*r, v)
	}
	return nil
}

const forUsage = "a value for one part of the app alone: web, release (the release command) or a process's name; once for each"

// validRole is a role a value may be for, as the platform checks it: a
// cron job gets the values for all.
func validRole(r string) bool {
	if r == "" || len(r) > 15 || r == "job" || r[0] < 'a' || r[0] > 'z' {
		return false
	}
	for _, c := range r {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') {
			return false
		}
	}
	return true
}

// roleSays is a role, in a sentence.
func roleSays(r string) string {
	switch r {
	case "web":
		return "the web"
	case "release":
		return "the release command"
	}
	return r
}

// kindFlags are --secret and --plain: which a value is, a secret (never
// shown again) or a variable (shown). Neither: a value keeps its kind, and
// a new one is a secret.
type kindFlags struct{ secret, plain *bool }

func addKindFlags(fs *flag.FlagSet) kindFlags {
	return kindFlags{
		secret: fs.Bool("secret", false, "a secret: its value is never shown again (a new value is one unless --plain)"),
		plain:  fs.Bool("plain", false, "a variable: its value is shown, in env ls and on the dashboard"),
	}
}

// chosen is the kind named, if one was.
func (k kindFlags) chosen() (*bool, error) {
	switch {
	case *k.secret && *k.plain:
		return nil, usageErr("--secret and --plain are one choice: name one")
	case *k.secret, *k.plain:
		return k.secret, nil
	}
	return nil, nil
}

// kindOf is names each the kind chosen, nil when none was.
func kindOf(secret *bool, names []string) map[string]bool {
	if secret == nil {
		return nil
	}
	out := make(map[string]bool, len(names))
	for _, n := range names {
		out[n] = *secret
	}
	return out
}

// kindSays is what names now are, in a sentence: "" when no kind was chosen.
func kindSays(secret *bool, names []string) string {
	switch {
	case secret == nil:
		return ""
	case len(names) == 1 && *secret:
		return fmt.Sprintf("%s is a secret: its value is never shown again.\n", names[0])
	case len(names) == 1:
		return fmt.Sprintf("%s is a variable: its value is shown.\n", names[0])
	case *secret:
		return fmt.Sprintf("%s are secrets: their values are never shown again.\n", strings.Join(names, ", "))
	}
	return fmt.Sprintf("%s are variables: their values are shown.\n", strings.Join(names, ", "))
}

// envSetFor is `homeport env set NAME=value… --for ROLE…`: values for those
// roles alone, never in the value for all.
func (a *app) envSetFor(ctx context.Context, f envFlags, pos []string, roles roleFlags, secret *bool) error {
	if len(pos) == 0 {
		return usageErr("%s", envUsage)
	}
	set := map[string]string{}
	for _, arg := range pos {
		name, value, ok := strings.Cut(arg, "=")
		if !ok {
			return usageErr("%s: with --for, NAME=value (a value for one part of the app is set as a whole)", arg)
		}
		if strings.ContainsAny(value, "\r\n") {
			return fmt.Errorf("%s: a value is one line", name)
		}
		if _, err := envfile.Line(name, value); err != nil {
			return err
		}
		set[name] = value
	}
	c, t, _, err := a.envTarget(ctx, f)
	if err != nil {
		return err
	}
	names := slices.Sorted(maps.Keys(set))
	ch := cloud.EnvChange{Roles: map[string]cloud.RoleChange{}}
	for _, r := range roles {
		ch.Roles[r] = cloud.RoleChange{Set: set, Secret: kindOf(secret, names)}
	}
	if _, err := c.ChangeEnv(ctx, t.link.Team, t.link.App, ch); err != nil {
		return err
	}
	sorted := slices.Sorted(slices.Values(roles))
	says := make([]string, len(sorted))
	for i, r := range sorted {
		says[i] = roleSays(r)
	}
	var next []string
	if slices.Contains(sorted, "release") {
		next = append(next, "the release command uses it at the next deploy")
	}
	if running := slices.DeleteFunc(slices.Clone(sorted), func(r string) bool { return r == "release" }); len(running) > 0 {
		next = append(next, "the app restarts with it")
	}
	fmt.Fprintf(a.out, "Set %s for %s on %s: %s.\n%s", strings.Join(names, ", "),
		strings.Join(says, ", "), envWhere(t), strings.Join(next, "; "), kindSays(secret, names))
	return nil
}

// envSet is `homeport env set`: plain variables as NAME=value, or one file
// variable from a file here - read here, its bytes sent as they are - and
// who sees them: --build, --build-only or --runtime, else a new one its
// name's (envfile.DefaultAvailable); and which each is: --secret or
// --plain, else its kind kept (a new one a secret). A name alone with one
// of those changes that alone, its value kept. What the platform would
// refuse is refused before anything is sent; a value is never printed.
func (a *app) envSet(ctx context.Context, args []string) error {
	fs, f := a.envFlagSet("env set")
	fromFile := fs.String("from-file", "", "a file variable from this file: the app reads it at "+envfile.SecretsDir+"/NAME, $NAME holds that path")
	build := fs.Bool("build", false, "available to its build and its running app")
	buildOnly := fs.Bool("build-only", false, "available to its build alone")
	runtime := fs.Bool("runtime", false, "available to its running app alone, whatever its name")
	var roles roleFlags
	fs.Var(&roles, "for", forUsage)
	kind := addKindFlags(fs)
	pos, err := parseMixed(fs, args)
	if err != nil {
		return err
	}
	secret, err := kind.chosen()
	if err != nil {
		return err
	}
	if len(roles) > 0 {
		switch {
		case *build || *buildOnly || *runtime:
			return usageErr("a value for one part of the app is its running app's alone: --for takes none of --build, --build-only, --runtime")
		case *fromFile != "":
			return usageErr("a file variable is for every part of the app: --for takes no --from-file")
		}
		return a.envSetFor(ctx, f, pos, roles, secret)
	}
	var chosen envfile.Availability
	for _, c := range []struct {
		on bool
		a  envfile.Availability
	}{{*build, envfile.Build}, {*buildOnly, envfile.BuildOnly}, {*runtime, envfile.Runtime}} {
		if !c.on {
			continue
		}
		if chosen != "" {
			return usageErr("--build, --build-only and --runtime are one choice: name one")
		}
		chosen = c.a
	}
	set := map[string]string{}
	available := map[string]envfile.Availability{}
	var named []string // names alone: their availability or kind changed, their value kept
	var fileName string
	var content []byte
	if *fromFile != "" {
		if chosen != "" {
			return usageErr("a file variable is its running app's alone: --from-file takes none of --build, --build-only, --runtime")
		}
		if *kind.plain {
			return usageErr("a file variable is always a secret: --from-file takes no --plain")
		}
		secret = nil
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
				if chosen == "" && secret == nil {
					return usageErr("%s: NAME=value; a name alone with --build, --build-only, --runtime, --secret or --plain; or a file's content with `homeport env set %s --from-file <path>`", arg, arg)
				}
				if !envfile.ValidName(name) {
					return usageErr("%q isn't a variable's name", name)
				}
				if chosen != "" {
					available[name] = chosen
				}
				named = append(named, name)
				continue
			}
			if strings.ContainsAny(value, "\r\n") {
				return fmt.Errorf("%s: a value is one line; a multi-line one is a file variable: `homeport env set %s --from-file <path>`", name, name)
			}
			if _, err := envfile.Line(name, value); err != nil {
				return err
			}
			set[name] = value
			if chosen != "" {
				available[name] = chosen
			}
		}
		// a build's rules, for a value a build sees: checked here as the
		// platform checks them
		for _, n := range slices.Sorted(maps.Keys(set)) {
			if a, ok := available[n]; ok && !a.BuildSees() || !ok && !envfile.DefaultAvailable(n).BuildSees() {
				continue
			}
			if err := envfile.CheckBuildName(n); err != nil {
				return err
			}
			if err := envfile.CheckBuildValue(n, set[n]); err != nil {
				return err
			}
		}
		for n, a := range available {
			if _, ok := set[n]; !ok && a.BuildSees() {
				if err := envfile.CheckBuildName(n); err != nil {
					return err
				}
			}
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
	kinds := slices.Sorted(slices.Values(append(slices.Collect(maps.Keys(set)), named...)))
	if _, err := c.ChangeEnv(ctx, t.link.Team, t.link.App, cloud.EnvChange{Set: set, Files: files, Available: available, Secret: kindOf(secret, kinds)}); err != nil {
		return err
	}
	if fileName != "" {
		fmt.Fprintf(a.out, "Set %s on %s: %s.\nThe app reads it at %s ($%s holds that path), and restarts to get it.\n",
			fileName, envWhere(t), envfile.Inspect(content).Summary(), secretPath(fileName), fileName)
		return nil
	}
	// who sees each, as the platform has it now (a name's own, kept, when
	// none was chosen)
	env, err := c.Env(ctx, t.link.Team, t.link.App)
	if err != nil {
		return err
	}
	seen := map[string]envfile.Availability{}
	for _, v := range env.Vars {
		seen[v.Name] = v.Available
	}
	var parts []string
	restarts := false
	for _, n := range named {
		if _, ok := set[n]; !ok {
			set[n] = "" // named alone: said with the rest
			// what the running app gets may change with it
			restarts = true
		}
	}
	for _, n := range slices.Sorted(maps.Keys(set)) {
		av := seen[n]
		if av == "" {
			av = available[n]
		}
		if av == "" {
			parts = append(parts, n)
			restarts = true
			continue
		}
		restarts = restarts || av.RuntimeSees()
		why := ""
		if _, ok := available[n]; !ok && av == envfile.Build {
			if p := publicPrefix(n); p != "" {
				why = ", from its " + p + " prefix"
			}
		}
		parts = append(parts, fmt.Sprintf("%s (%s%s)", n, availableSays(av), why))
	}
	next := "."
	if restarts {
		next = ": the app restarts with them."
	}
	fmt.Fprintf(a.out, "Set %s on %s%s\n%s", strings.Join(parts, ", "), envWhere(t), next, kindSays(secret, kinds))
	if env.NeedsRebuild {
		fmt.Fprintf(a.out, "Its build sees a change: rebuild to put it in the built app: `homeport env rebuild`.\n")
	}
	return nil
}

// availableSays is who sees a variable, in a sentence.
func availableSays(a envfile.Availability) string { return strings.ToLower(a.Label()) }

// publicPrefix is the public prefix (envfile.PublicPrefixes) name has, if
// any.
func publicPrefix(name string) string {
	for _, p := range envfile.PublicPrefixes {
		if len(name) > len(p) && strings.HasPrefix(name, p) {
			return p
		}
	}
	return ""
}

// envUnset is `homeport env unset`: variables removed, plain or file; with
// --for, those roles' own values alone.
func (a *app) envUnset(ctx context.Context, args []string) error {
	fs, f := a.envFlagSet("env unset")
	var roles roleFlags
	fs.Var(&roles, "for", "remove one part of the app's own value alone (web, release or a process's name); once for each")
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
	if len(roles) > 0 {
		ch := cloud.EnvChange{Roles: map[string]cloud.RoleChange{}}
		var says []string
		for _, r := range slices.Sorted(slices.Values(roles)) {
			ch.Roles[r] = cloud.RoleChange{Unset: pos}
			says = append(says, r)
		}
		if _, err := c.ChangeEnv(ctx, t.link.Team, t.link.App, ch); err != nil {
			return err
		}
		fmt.Fprintf(a.out, "Removed %s's value of %s from %s: it gets the value for all, if any.\n",
			strings.Join(says, ", "), strings.Join(pos, ", "), envWhere(t))
		return nil
	}
	if _, err := c.SetEnv(ctx, t.link.Team, t.link.App, nil, nil, pos); err != nil {
		return err
	}
	fmt.Fprintf(a.out, "Removed %s from %s: the app restarts without them.\n", strings.Join(pos, ", "), envWhere(t))
	return nil
}

// envLs is `homeport env ls`: every variable an environment gets, by name,
// with who sees it, which it is and where it comes from, a variable's value
// (never a secret's), and of a file variable where the app reads it and
// what it is; and whether it needs a rebuild.
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
	vars := env.Vars
	if len(vars) == 0 { // a platform before Vars: its own, by name
		for _, n := range env.Names {
			vars = append(vars, cloud.EnvVar{Name: n, Source: "own"})
		}
	}
	if len(vars) == 0 {
		fmt.Fprintf(a.out, "%s has no variables.\n", envWhere(t))
		return nil
	}
	files := map[string]envfile.FileMeta{}
	for _, fv := range env.Files {
		files[fv.Name] = fv.Meta
	}
	var table strings.Builder
	tw := tabwriter.NewWriter(&table, 0, 4, 2, ' ', 0)
	fmt.Fprintf(tw, "NAME\tAVAILABLE TO\tFOR\tKIND\tSOURCE\tVALUE\t\n")
	for _, v := range vars {
		source := v.Source
		if v.PerDeploy {
			source += ", each deploy"
		}
		if m, ok := files[v.Name]; ok {
			fmt.Fprintf(tw, "%s\t%s\t%s\tfile\t%s\t%s\t%s\n", v.Name, availableShort(v.Available), forShort(v), source, secretPath(v.Name), m.Summary())
			continue
		}
		kind, value := "secret", ""
		if !v.Secret {
			kind, value = "variable", v.Value
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t\n", v.Name, availableShort(v.Available), forShort(v), kind, source, value)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	for l := range strings.Lines(table.String()) {
		fmt.Fprintln(a.out, strings.TrimRight(l, " \n"))
	}
	if env.NeedsRebuild {
		fmt.Fprintf(a.out, "\n%s needs a rebuild: its build sees a change since its live release was built. `homeport env rebuild`\n", envWhere(t))
	}
	return nil
}

// forShort is which parts of the app get a variable, in a column: all, the
// roles with a value alone, or all with some role's own.
func forShort(v cloud.EnvVar) string {
	switch {
	case len(v.Roles) == 0:
		return "all"
	case v.RolesOnly:
		return strings.Join(v.Roles, ", ")
	}
	return "all; own: " + strings.Join(v.Roles, ", ")
}

// availableShort is who sees a variable, in a column.
func availableShort(a envfile.Availability) string {
	switch a {
	case envfile.Build:
		return "build + runtime"
	case envfile.BuildOnly:
		return "build only"
	case envfile.Runtime:
		return "runtime"
	}
	return "-"
}

// envRebuild is `homeport env rebuild`: the environment's live commit built
// again with its variables as they are now.
func (a *app) envRebuild(ctx context.Context, args []string) error {
	fs, f := a.envFlagSet("env rebuild")
	if err := parse(fs, args); err != nil {
		return err
	}
	c, t, _, err := a.envTarget(ctx, f)
	if err != nil {
		return err
	}
	build, err := c.Rebuild(ctx, t.link.Team, t.link.App)
	if err != nil {
		return err
	}
	fmt.Fprintf(a.out, "Rebuilding %s's live commit with its variables as they are now: build %s.\nThe app moves to the new release when it ships.\n", envWhere(t), build)
	return nil
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
