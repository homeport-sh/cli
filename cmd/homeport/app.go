package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/homeport-sh/cli/internal/cloud"
	"github.com/homeport-sh/cli/internal/config"
)

// Exit codes, for scripts and CI to tell apart.
const (
	exitOK           = 0
	exitError        = 1 // anything else: refused, unreachable, a bad tree
	exitUsage        = 2 // a bad flag, or a question there's no terminal to ask
	exitSignedOut    = 3 // not signed in, or the sign-in ended: homeport login
	exitBuildFailed  = 4 // the build failed, or was canceled or superseded
	exitDeployFailed = 5 // built, but the release didn't go live
	exitTimeout      = 6 // still going when --timeout ran out
)

// exit is an error with the code the CLI ends with.
type exit struct {
	code int
	err  error
}

func (e *exit) Error() string { return e.err.Error() }
func (e *exit) Unwrap() error { return e.err }

func withCode(code int, err error) error { return &exit{code: code, err: err} }

func usageErr(format string, a ...any) error { return withCode(exitUsage, fmt.Errorf(format, a...)) }

// app is the CLI with its world: the terminal, the browser, the clock.
type app struct {
	in       io.Reader
	out, err io.Writer
	tty      bool // a person at a terminal: a browser may open, questions may be asked
	wd       string
	open     func(url string) error
	sleep    func(ctx context.Context, d time.Duration) error
	hostname func() (string, error)

	lines *bufio.Reader
}

func newApp() *app {
	wd, _ := os.Getwd()
	return &app{in: os.Stdin, out: os.Stdout, err: os.Stderr, tty: isTerminal(os.Stdin) && isTerminal(os.Stdout), wd: wd,
		open: openBrowser, sleep: sleepCtx, hostname: os.Hostname}
}

// run runs one command and says how it ended.
func (a *app) run(args []string) int {
	if len(args) == 0 {
		a.usage()
		return exitOK
	}
	cmd, rest := args[0], args[1:]
	ctx := context.Background()
	var err error
	switch cmd {
	case "login":
		err = a.login(ctx, rest)
	case "logout":
		err = a.logout(ctx, rest)
	case "whoami":
		err = a.whoami(ctx, rest)
	case "link":
		err = a.link(ctx, rest)
	case "deploy":
		err = a.deploy(ctx, rest)
	case "build-plan":
		err = cmdBuildPlan(rest)
	case "mcp":
		err = errors.New("the MCP server comes back soon, on `homeport login`'s sign-in")
	case "version", "-v", "--version":
		fmt.Fprintln(a.out, "homeport", version)
	case "help", "-h", "--help":
		a.usage()
	default:
		err = usageErr("unknown command %q (try: homeport help)", cmd)
	}
	if err == nil {
		return exitOK
	}
	if errors.Is(err, flag.ErrHelp) {
		return exitOK
	}
	fmt.Fprintf(a.err, "\x1b[1;31merror:\x1b[0m %v\n", err)
	var e *exit
	switch {
	case errors.As(err, &e):
		return e.code
	case errors.Is(err, cloud.ErrSignedOut), errors.Is(err, config.ErrNoCredentials):
		return exitSignedOut
	}
	return exitError
}

// flags is a command's flag set, its errors and help written to stderr.
func (a *app) flags(name string) *flag.FlagSet {
	fs := flag.NewFlagSet("homeport "+name, flag.ContinueOnError)
	fs.SetOutput(a.err)
	return fs
}

// parse parses args, a bad one a usage error.
func parse(fs *flag.FlagSet, args []string) error {
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return err
		}
		return withCode(exitUsage, err)
	}
	if fs.NArg() > 0 {
		return usageErr("%s takes no arguments (%q)", fs.Name(), fs.Arg(0))
	}
	return nil
}

// apiBase is where the API is: HOMEPORT_API, else where the sign-in was
// made, else homeport.sh's.
func apiBase(c *config.Credentials) string {
	if v := os.Getenv("HOMEPORT_API"); v != "" {
		return strings.TrimRight(v, "/")
	}
	if c != nil && c.API != "" {
		return c.API
	}
	return cloud.DefaultAPI
}

func userAgent() string {
	return "homeport/" + version + " (" + runtime.GOOS + "/" + runtime.GOARCH + ")"
}

// client is the API as the signed-in CLI: HOMEPORT_TOKEN (CI) or the saved
// sign-in.
func (a *app) client() (*cloud.Client, error) {
	if tok := os.Getenv("HOMEPORT_TOKEN"); tok != "" {
		return &cloud.Client{Base: apiBase(nil), Token: tok, UserAgent: userAgent()}, nil
	}
	c, err := config.LoadCredentials()
	if err != nil {
		return nil, err
	}
	return &cloud.Client{Base: apiBase(c), Token: c.Token, UserAgent: userAgent()}, nil
}

// line reads one answer from the person.
func (a *app) line() (string, error) {
	if a.lines == nil {
		a.lines = bufio.NewReader(a.in)
	}
	s, err := a.lines.ReadString('\n')
	if err != nil && (s == "" || !errors.Is(err, io.EOF)) {
		return "", errors.New("no answer")
	}
	return strings.TrimSpace(s), nil
}

// choose asks which of options (by number or by name); def is the answer
// to an empty line (-1: none). Without a terminal it asks nothing: flag
// says what to pass instead.
func (a *app) choose(what, flag string, options []string, def int) (int, error) {
	if !a.tty {
		return 0, usageErr("which %s? pass %s (one of: %s) - there's no terminal to ask in", what, flag, strings.Join(options, ", "))
	}
	fmt.Fprintf(a.out, "Which %s?\n", what)
	for i, o := range options {
		mark := " "
		if i == def {
			mark = "*"
		}
		fmt.Fprintf(a.out, " %s %d) %s\n", mark, i+1, o)
	}
	for range 3 {
		fmt.Fprint(a.out, "> ")
		s, err := a.line()
		if err != nil {
			return 0, usageErr("which %s? pass %s", what, flag)
		}
		if s == "" && def >= 0 {
			return def, nil
		}
		if n, err := strconv.Atoi(s); err == nil && n >= 1 && n <= len(options) {
			return n - 1, nil
		}
		for i, o := range options {
			if o == s {
				return i, nil
			}
		}
		fmt.Fprintf(a.out, "%q isn't one of them.\n", s)
	}
	return 0, usageErr("which %s? pass %s", what, flag)
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// openBrowser opens url in the person's browser, if there's one to open.
func openBrowser(url string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", url).Start()
	case "linux":
		if os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
			return errors.New("no display")
		}
		return exec.Command("xdg-open", url).Start()
	}
	return errors.New("no browser to open")
}

func isTerminal(f *os.File) bool {
	info, err := f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}
