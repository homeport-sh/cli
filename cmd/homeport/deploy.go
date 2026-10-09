package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/homeport-sh/cli/buildplan"
	"github.com/homeport-sh/cli/internal/cloud"
	"github.com/homeport-sh/cli/internal/config"
	"github.com/homeport-sh/cli/internal/source"
)

// maxUpload is the most `homeport deploy` packs before giving up: the
// largest any plan takes. The plan's own limit is the API's to say.
const maxUpload = 2000 << 20

// bigUpload is when it says the tree is large: build output or
// dependencies that .gitignore doesn't leave out, most likely.
const bigUpload = 50 << 20

// pollEvery is how often a deploy's progress is read.
const pollEvery = 2 * time.Second

// strings is a repeatable flag.
type stringList []string

func (s *stringList) String() string     { return strings.Join(*s, ", ") }
func (s *stringList) Set(v string) error { *s = append(*s, v); return nil }

// deploy uploads the working tree - what git would commit, uncommitted
// changes included - and has homeport build and release it as it would a
// pushed commit, following it until it's live.
func (a *app) deploy(ctx context.Context, args []string) error {
	fs := a.flags("deploy")
	team := fs.String("team", "", "the team, by its address (instead of the link)")
	appName := fs.String("app", "", "the app, by name (instead of the link)")
	env := fs.String("env", "", "the environment (with --app; default production)")
	run := fs.String("run", "", "how the app starts, for this deploy (its start command's args)")
	release := fs.String("release", "", "the release command, run before it goes live (migrations)")
	var procs, unset stringList
	fs.Var(&procs, "process", "a process beside the web, name=command (repeatable; replaces the app's processes)")
	fs.Var(&unset, "unset", "empty a saved setting: run, release, processes or octane (repeatable; with --save)")
	save := fs.Bool("save", false, "save these changes as the app's settings, for every deploy after this one")
	detach := fs.Bool("detach", false, "start the deploy and don't wait for it")
	timeout := fs.Duration("timeout", 30*time.Minute, "how long to wait for it to go live")
	if err := parse(fs, args); err != nil {
		return err
	}
	settings := buildplan.Settings{Run: strings.TrimSpace(*run), Release: strings.TrimSpace(*release)}
	for _, p := range procs {
		name, cmd, ok := strings.Cut(p, "=")
		if !ok || strings.TrimSpace(name) == "" || strings.TrimSpace(cmd) == "" {
			return usageErr("--process %q: give it as name=command", p)
		}
		settings.Processes = append(settings.Processes, buildplan.Process{Name: strings.TrimSpace(name), Run: strings.TrimSpace(cmd)})
	}
	if len(unset) > 0 && !*save {
		return usageErr("--unset empties a saved setting: pass --save too")
	}

	c, err := a.client()
	if err != nil {
		return err
	}
	// what to deploy: the flags, or this folder's link
	var t *target
	dir := a.wd
	if *appName != "" || *team != "" {
		if t, err = a.resolve(ctx, c, *team, *appName, *env, true); err != nil {
			return err
		}
	} else {
		l, at, err := config.FindLink(a.wd)
		if errors.Is(err, config.ErrNotLinked) {
			return withCode(exitUsage, err)
		}
		if err != nil {
			return err
		}
		dir = at
		if t, err = a.linked(ctx, c, *l, true); err != nil {
			return err
		}
	}

	tree, err := source.Collect(dir)
	if err != nil {
		return err
	}
	if len(tree.Secrets) > 0 {
		fmt.Fprintf(a.out, "    left out %s: they look like secrets. Set variables on the environment instead.\n", strings.Join(tree.Secrets, ", "))
	}
	if len(tree.Files) == 0 {
		return fmt.Errorf("there's nothing to upload in %s", tree.Root)
	}
	f, err := os.CreateTemp("", "homeport-source-*.tar.gz")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	sum, err := source.Pack(tree, f, maxUpload)
	if errors.Is(err, source.ErrTooLarge) {
		return fmt.Errorf("%s is over %d MB packed: leave build output and dependencies out with .gitignore", tree.Root, maxUpload>>20)
	}
	if err != nil {
		return err
	}
	sha, what := sum.Digest, "the working tree, with uncommitted changes"
	if tree.Clean {
		sha, what = tree.Head, "commit "+tree.Head[:12]
	} else if !tree.Git {
		what = "the folder (not a git checkout)"
	}
	l := t.link
	fmt.Fprintf(a.out, "Deploying %s to %s/%s (%s)\n", what, l.TeamSlug, l.Project, l.Environment)
	fmt.Fprintf(a.out, "==> uploading %d files (%s)\n", sum.Files, size(sum.Bytes))
	if sum.Bytes > bigUpload {
		fmt.Fprintf(a.out, "    that's a lot: build output or dependencies that .gitignore doesn't leave out?\n")
	}
	slot, err := c.StartSource(ctx, l.Team, l.App, sum.Bytes)
	if err != nil {
		return err
	}
	if _, err := f.Seek(0, 0); err != nil {
		return err
	}
	if err := c.Upload(ctx, slot.URL, f, sum.Bytes); err != nil {
		return err
	}
	build, err := c.Deploy(ctx, l.Team, l.App, cloud.DeployRequest{Source: slot.ID, SHA: sha, Settings: settings, Unset: unset, Save: *save})
	if err != nil {
		return err
	}
	page := fmt.Sprintf("%s/%s/%s/%s/builds/%s", t.dashboard, l.TeamSlug, l.Project, l.Environment, build)
	fmt.Fprintf(a.out, "==> building: %s\n", page)
	if *save {
		fmt.Fprintln(a.out, "    the changes are saved: every deploy after this one has them too")
	}
	if *detach {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	return a.follow(ctx, c, t, build, page)
}

// follow prints the build's log as it comes, then the release's steps,
// until it's live (its address) or it isn't (why, and a code that says
// which).
func (a *app) follow(ctx context.Context, c *cloud.Client, t *target, build, page string) error {
	l := t.link
	stillGoing := func() error {
		return withCode(exitTimeout, fmt.Errorf("still going after --timeout; it carries on: %s", page))
	}
	var printed string
	var deploy string
	for deploy == "" {
		b, err := c.Build(ctx, l.Team, build)
		if ctx.Err() != nil {
			return stillGoing()
		}
		if err != nil {
			return err
		}
		fmt.Fprint(a.out, logDelta(printed, b.Log))
		printed = b.Log
		switch b.Status {
		case "succeeded":
			if b.Deploy == "" {
				return errors.New("the build succeeded but made no deploy: see " + page)
			}
			deploy = b.Deploy
			continue
		case "failed", "canceled", "superseded":
			why := b.Reason
			if why == "" {
				why = "the build " + b.Status
			}
			return withCode(exitBuildFailed, fmt.Errorf("%s: %s", why, page))
		}
		if err := a.sleep(ctx, pollEvery); err != nil {
			return stillGoing()
		}
	}
	last := ""
	for {
		d, err := c.DeployStatus(ctx, l.Team, deploy)
		if ctx.Err() != nil {
			return stillGoing()
		}
		if err != nil {
			return err
		}
		if step := strings.TrimSpace(d.Status + " " + d.Detail); step != last && d.Status != "failed" {
			fmt.Fprintf(a.out, "==> %s\n", step)
			last = step
		}
		switch d.Status {
		case "live":
			app, err := c.App(ctx, l.Team, l.App)
			if err == nil && app.Domain != "" {
				t.domain = app.Domain
			}
			if t.domain != "" {
				fmt.Fprintf(a.out, "Live: https://%s\n", t.domain)
			} else {
				fmt.Fprintln(a.out, "Live.")
			}
			return nil
		case "failed", "superseded":
			why := d.Detail
			if why == "" {
				why = "the deploy " + d.Status
			}
			return withCode(exitDeployFailed, fmt.Errorf("%s: %s", why, page))
		}
		if err := a.sleep(ctx, pollEvery); err != nil {
			return stillGoing()
		}
	}
}

// logDelta is what's new in a build's log: cur is its end now, prev its end
// when last read. The end is a window that moves, so what's new is what
// follows the longest end of prev that cur starts with; nothing in common
// means more came than the window holds.
func logDelta(prev, cur string) string {
	if prev == "" {
		return cur
	}
	if n := overlap(prev, cur); n > 0 {
		return cur[n:]
	}
	return "…\n" + cur
}

// overlap is the length of the longest suffix of a that's a prefix of b
// (Knuth-Morris-Pratt's failure function over b, then a).
func overlap(a, b string) int {
	if len(a) > len(b) {
		a = a[len(a)-len(b):]
	}
	fail := make([]int, len(b))
	for i, k := 1, 0; i < len(b); i++ {
		for k > 0 && b[i] != b[k] {
			k = fail[k-1]
		}
		if b[i] == b[k] {
			k++
		}
		fail[i] = k
	}
	k := 0
	for i := 0; i < len(a); i++ {
		for k > 0 && (k == len(b) || a[i] != b[k]) {
			k = fail[k-1]
		}
		if k < len(b) && a[i] == b[k] {
			k++
		}
	}
	return k
}

func size(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d bytes", n)
}
