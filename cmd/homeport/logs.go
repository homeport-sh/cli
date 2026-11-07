package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/homeport-sh/cli/internal/cloud"
)

// logsEvery is how often --follow reads what's new: the dashboard's panel's
// pace.
const logsEvery = 2 * time.Second

// logs is `homeport logs`: an environment's runtime logs (what the app
// printed), its newest lines - the API's page, up to 500 - and with
// --follow what's new after them, read on from the API's cursor until
// Ctrl-C. --process and --errors narrow what's printed, as the dashboard's
// panel does; the API reads every process's lines either way.
func (a *app) logs(ctx context.Context, args []string) error {
	fs, f := a.envFlagSet("logs")
	follow := fs.Bool("follow", false, "keep reading what's new until Ctrl-C")
	fs.BoolVar(follow, "f", false, "the same as --follow")
	process := fs.String("process", "", "one process's lines alone: web, or a process by name")
	errorsOnly := fs.Bool("errors", false, "errors alone")
	if err := parse(fs, args); err != nil {
		return err
	}
	c, t, _, err := a.envTarget(ctx, f)
	if err != nil {
		return err
	}
	if *follow {
		var stop context.CancelFunc
		ctx, stop = signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
		defer stop()
	}
	shown, cursor := false, ""
	for {
		page, err := c.Logs(ctx, t.link.Team, t.link.App, cursor)
		if err != nil {
			if ctx.Err() != nil {
				return nil // stopped mid-read
			}
			return err
		}
		for _, l := range page.Lines {
			if (*process == "" || l.Process == *process) && (!*errorsOnly || l.Level == "error") {
				a.logLine(l)
				shown = true
			}
		}
		if !shown && cursor == "" {
			if *follow {
				fmt.Fprintf(a.err, "No logs from %s yet: waiting for some (Ctrl-C stops).\n", envWhere(t))
			} else {
				fmt.Fprintf(a.err, "No logs from %s yet.\n", envWhere(t))
			}
		}
		if !*follow {
			return nil
		}
		cursor = page.Cursor
		if err := a.sleep(ctx, logsEvery); err != nil {
			return nil // Ctrl-C
		}
	}
}

// logLine prints one line: when, its process (the web's goes unsaid, as on
// the dashboard), what - an error red and a warning yellow at a terminal.
func (a *app) logLine(l cloud.LogLine) {
	who := ""
	if l.Process != "" && l.Process != "web" {
		who = l.Process + " "
	}
	color, reset := "", ""
	if a.tty {
		switch l.Level {
		case "error":
			color, reset = "\x1b[31m", "\x1b[0m"
		case "warning":
			color, reset = "\x1b[33m", "\x1b[0m"
		}
	}
	fmt.Fprintf(a.out, "%s%s %s%s%s\n", color, l.Time.Local().Format("Jan _2 15:04:05"), who, l.Message, reset)
}
