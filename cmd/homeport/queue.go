package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/homeport-sh/cli/buildplan"
	"github.com/homeport-sh/cli/internal/cloud"
)

const queueUsage = `usage: homeport queue status
       homeport queue drain [--remote]
       homeport queue retry-failed [--remote]
  alone, drain and retry-failed run the app's own command here, with its .env;
  --remote and status are on homeport, each [--env <environment>] [--team <slug> --app <name>]`

// queue is `homeport queue`: an environment's background jobs (design/
// queues.md in homeport cloud) - how they run and what the last drains
// said, a drain or a retry of the failed jobs started on homeport, or the
// app's own drain run here against its local database.
func (a *app) queue(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return usageErr("%s", queueUsage)
	}
	switch args[0] {
	case "status":
		return a.queueStatus(ctx, args[1:])
	case "drain", "retry-failed":
		return a.queueRun(ctx, args[0], args[1:])
	}
	return usageErr("%s", queueUsage)
}

// queueModes: how a queue's jobs run, as a person says it.
var queueModes = map[string]string{"on_demand": "on demand", "always_on": "always on (its worker process)", "off": "off"}

// queueStatus is `homeport queue status`.
func (a *app) queueStatus(ctx context.Context, args []string) error {
	fs, f := a.envFlagSet("queue status")
	if err := parse(fs, args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return usageErr("%s", queueUsage)
	}
	c, t, _, err := a.envTarget(ctx, f)
	if err != nil {
		return err
	}
	q, err := c.Queue(ctx, t.link.Team, t.link.App)
	if err != nil {
		return err
	}
	mode := queueModes[q.Mode]
	if mode == "" {
		mode = q.Mode
	}
	fmt.Fprintf(a.out, "%s's queue: %s, in %s\n", envWhere(t), mode, q.Backend)
	if q.Mode != "on_demand" {
		return nil
	}
	command := q.Command
	if command == "" {
		command = "the detected one"
	}
	fmt.Fprintf(a.out, "  drain    %s, up to %s\n", command, queueTimeout(q.TimeoutSeconds))
	switch {
	case q.Waiting == nil:
		fmt.Fprintln(a.out, "  report   none yet")
	default:
		due := "none due later"
		if q.NextDue != nil {
			due = "the next due " + q.NextDue.Local().Format("Jan 2 15:04")
		}
		at := ""
		if q.ReportedAt != nil {
			at = " (reported " + q.ReportedAt.Local().Format("Jan 2 15:04") + ")"
		}
		fmt.Fprintf(a.out, "  report   %d waiting, %s%s\n", *q.Waiting, due, at)
	}
	if len(q.Drains) == 0 {
		return nil
	}
	fmt.Fprintln(a.out)
	tw := tabwriter.NewWriter(a.out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "STARTED\tTRIGGER\tSTATUS\tTOOK\tPROCESSED\tFAILED\tWAITING")
	for _, d := range q.Drains {
		started, took := "-", "-"
		if d.StartedAt != nil {
			started = d.StartedAt.Local().Format("Jan 2 15:04")
		}
		if d.DurationMS != nil {
			took = (time.Duration(*d.DurationMS) * time.Millisecond).Round(time.Second).String()
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", started, d.Trigger, d.Status, took, count(d.Processed), count(d.Failed), count(d.Waiting))
	}
	return tw.Flush()
}

// queueTimeout is a drain's longest run, as homeport.yaml says it.
func queueTimeout(secs int) string {
	if secs%60 == 0 {
		return fmt.Sprintf("%dm", secs/60)
	}
	return fmt.Sprintf("%ds", secs)
}

func count(n *int) string {
	if n == nil {
		return "-"
	}
	return fmt.Sprint(*n)
}

// queueRun is `homeport queue drain` and `homeport queue retry-failed`:
// started on homeport with --remote, else the app's command run here.
func (a *app) queueRun(ctx context.Context, verb string, args []string) error {
	fs, f := a.envFlagSet("queue " + verb)
	remote := fs.Bool("remote", false, "on homeport, not here")
	if err := parse(fs, args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return usageErr("%s", queueUsage)
	}
	if !*remote {
		return a.queueHere(ctx, verb)
	}
	c, t, _, err := a.envTarget(ctx, f)
	if err != nil {
		return err
	}
	var run *cloud.QueueRun
	what := "a drain of " + envWhere(t) + "'s queue"
	if verb == "drain" {
		run, err = c.DrainQueue(ctx, t.link.Team, t.link.App)
	} else {
		what = "a retry of " + envWhere(t) + "'s failed jobs"
		run, err = c.RetryFailedQueue(ctx, t.link.Team, t.link.App)
	}
	if err != nil {
		return err
	}
	fmt.Fprintf(a.out, "Started %s on homeport (%s): `homeport queue status` shows how it went.\n", what, run.ID)
	return nil
}

// queueHere runs the queue's drain (or retry) here, as homeport runs it in
// a sandbox: the folder's plan says what it is.
func (a *app) queueHere(ctx context.Context, verb string) error {
	p, err := planBuild(a.wd, buildplan.Settings{})
	if err != nil {
		return err
	}
	q := p.Queue
	if q == nil {
		return errors.New("no queue here: homeport sees Laravel's, pg-boss's and Graphile Worker's; say yours in homeport.yaml's queue:")
	}
	command, what := q.Drain, "drain"
	if verb == "retry-failed" {
		command, what = q.Retry, "retry"
	}
	if command == "" {
		why := q.Note
		if verb == "retry-failed" {
			why = "add a queue:retry script or command, or say it in homeport.yaml's queue: retry"
		} else if why == "" {
			why = "say it in homeport.yaml's queue: drain"
		}
		return fmt.Errorf("%s's queue has no %s command: %s", q.Framework, what, why)
	}
	argv, done, err := localQueueCommand(a.wd, p, command)
	if err != nil {
		return err
	}
	defer done()
	c := exec.CommandContext(ctx, argv[0], argv[1:]...)
	c.Dir, c.Stdin, c.Stdout, c.Stderr = a.wd, a.in, a.out, a.err
	if err := a.exec(c); err != nil {
		var x *exec.ExitError
		if errors.As(err, &x) {
			return withCode(x.ExitCode(), fmt.Errorf("the %s exited %d", what, x.ExitCode()))
		}
		return err
	}
	return nil
}

// localQueueCommand is command - args to the app's bin - as it runs here:
// php-cli as PHP (the queue runner from a file of its own), an rsc-kit
// command as its file on Bun (the dev server's way), and args to Node or
// Bun as the runtime's, Node told to read .env (Laravel and Bun read it
// themselves). done removes what it made.
func localQueueCommand(dir string, p buildplan.Plan, command string) (argv []string, done func(), err error) {
	f, done := strings.Fields(command), func() {}
	switch {
	case p.Toolchain == "php":
		if f[0] != "php-cli" {
			return nil, nil, fmt.Errorf("%q runs in the app's own FrankenPHP: run it from a build, as ./bin %s", command, command)
		}
		argv = append([]string{"php"}, f[1:]...)
		if len(argv) > 1 && argv[1] == buildplan.QueueRunnerFile {
			tmp, err := os.MkdirTemp("", "homeport-queue-")
			if err != nil {
				return nil, nil, err
			}
			done = func() { os.RemoveAll(tmp) }
			if err := os.WriteFile(filepath.Join(tmp, "queue.php"), []byte(buildplan.QueueRunnerPHP()), 0o600); err != nil {
				done()
				return nil, nil, err
			}
			argv[1] = filepath.Join(tmp, "queue.php")
		}
	case p.RSCKit:
		for _, file := range buildplan.RSCKitCommandFiles(f[0]) {
			if st, err := os.Stat(filepath.Join(dir, file)); err == nil && st.Mode().IsRegular() {
				return append([]string{"bun", file}, f[1:]...), done, nil
			}
		}
		return nil, nil, fmt.Errorf("%q is an rsc-kit command, and src/commands has no %s.ts", command, f[0])
	case p.Runtime == "node":
		argv = []string{"node"}
		if st, err := os.Stat(filepath.Join(dir, ".env")); err == nil && st.Mode().IsRegular() {
			argv = append(argv, "--env-file=.env")
		}
		argv = append(argv, f...)
	case p.Runtime == "bun":
		argv = append([]string{"bun"}, f...)
	default:
		return nil, nil, fmt.Errorf("%q is args to the app's binary: build it, and run ./bin %s", command, command)
	}
	return argv, done, nil
}
