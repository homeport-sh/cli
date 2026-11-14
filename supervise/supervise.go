// Package supervise runs what a bundle's web is, in the web's one sandbox
// (design/backend-beside.md in homeport cloud): `homeport supervise`, which
// the builder puts in the bundle as .homeport/supervise and its
// .homeport/wrap runs. homeportd runs the web as
//
//	.homeport/supervise supervise [<runner>] ./bin <the web's args>
//
// and the supervisor runs, from the bundle's folder, in order:
//
//   - each line of .homeport/beside (Inertia's SSR renderer), a command
//     started however often it exits: the web serves without it;
//   - its args: the web, or with a renderer the backend, on BackendPort
//     (PORT says so), waited for until it accepts a connection;
//   - .homeport/renderer's line, when there is one: the web in front of the
//     backend, on the app's $PORT, told the backend's address
//     (RSC_BACKEND). A backend with paths (.homeport/backend-paths) the host
//     sends to it must listen on the sandbox's address, not on loopback
//     alone: checked before the renderer starts.
//
// One that exits is started again, a second later, then longer, up to 30s;
// one that ran a minute starts over at a second. The web, backend or
// renderer exiting five times in a minute stops the rest, and the
// supervisor exits 1, for the host to see the app fail. A stop (TERM, INT,
// HUP) goes to each in the reverse order, one after another exits, and the
// supervisor exits as the web did (the renderer, when there's one).
//
// In a line, %heap% is a quarter of the app's memory (homeportd's
// HOMEPORT_MEMORY_MB) in MB, at least 64, 128 when it isn't known, and
// %heapbytes% the same in bytes; a word KEY=value before the command is its
// environment. It reads nothing else: no config, no network but the
// backend's port.
package supervise

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// What the bundle says runs, relative to its folder.
const (
	// BesideFile: what runs beside the web, a command a line
	BesideFile = ".homeport/beside"
	// RendererFile: the renderer in front of a backend, one line
	RendererFile = ".homeport/renderer"
	// BackendPathsFile: the paths the host sends to the backend, a path a
	// line (none: the renderer's calls alone reach it)
	BackendPathsFile = ".homeport/backend-paths"
)

// BackendPort is where a backend beside a renderer listens, in every app's
// sandbox: rsc-kit's own default (RSC_BACKEND=http://127.0.0.1:8080).
const BackendPort = 8080

// What it waits, by default.
const (
	baseBackoff = time.Second
	maxBackoff  = 30 * time.Second
	// up this long, a backoff starts over; this many exits in it stop the app
	window   = time.Minute
	maxExits = 5
)

// LoopbackOnly is what the supervisor says of a backend with paths that
// listens on loopback alone.
const LoopbackOnly = "the backend listens on 127.0.0.1 only, and it has backend paths: listen on :$PORT"

// Run is `homeport supervise <args>`: how it ended, as an exit status.
func Run(args []string, stderr io.Writer) int {
	return run(args, stderr, options{port: BackendPort, backoff: baseBackoff, tick: 50 * time.Millisecond})
}

type options struct {
	port    int           // the backend's
	backoff time.Duration // the first wait before a start again
	tick    time.Duration // how often it looks for what's due
}

// proc is one of what it runs.
type proc struct {
	line    string // for people: the command, as it runs
	args    []string
	env     []string
	beside  bool // started again however often it exits
	process *os.Process
	up      time.Time // when it last started
	wait    time.Duration
	next    time.Time   // when to start it again
	exits   []time.Time // in the last window
	status  int         // its last exit's
	stopped bool        // sent the stop
}

// exited is how long to wait before starting p again, after it exited at
// now: base, doubling to maxBackoff, and base again once it ran a window.
func (p *proc) exited(now time.Time, base time.Duration) time.Duration {
	if p.wait == 0 || now.Sub(p.up) >= window {
		p.wait = base
	}
	w := p.wait
	p.wait = min(p.wait*2, maxBackoff)
	return w
}

type exit struct {
	pid    int
	status syscall.WaitStatus
}

func run(args []string, stderr io.Writer, o options) int {
	say := func(format string, a ...any) { fmt.Fprintf(stderr, "homeport: "+format+"\n", a...) }
	if len(args) == 0 {
		say("supervise: nothing to run (usage: homeport supervise <command> [args])")
		return 2
	}
	heap := heapMB(os.Getenv("HOMEPORT_MEMORY_MB"))
	var procs []*proc
	add := func(line string, beside bool, fixed ...string) *proc {
		env, args := parse(line, heap)
		if len(args) == 0 {
			return nil
		}
		p := &proc{line: strings.Join(args, " "), args: args, env: environ(os.Environ(), env, fixed...), beside: beside}
		procs = append(procs, p)
		return p
	}
	lines, err := readLines(BesideFile)
	if err != nil {
		say("supervise: %v", err)
		return 1
	}
	for _, l := range lines {
		add(l, true)
	}
	renderer, err := readLines(RendererFile)
	if err != nil {
		say("supervise: %v", err)
		return 1
	}
	backendPort := "PORT=" + strconv.Itoa(o.port)
	var web, backend *proc
	if len(renderer) == 0 {
		web = &proc{line: strings.Join(args, " "), args: args, env: os.Environ()}
		procs = append(procs, web)
	} else {
		backend = &proc{line: strings.Join(args, " "), args: args, env: environ(os.Environ(), nil, backendPort)}
		procs = append(procs, backend)
		if web = add(renderer[0], false, fmt.Sprintf("RSC_BACKEND=http://127.0.0.1:%d", o.port)); web == nil {
			say("supervise: %s names no command", RendererFile)
			return 1
		}
	}
	paths, err := readLines(BackendPathsFile)
	if err != nil {
		say("supervise: %v", err)
		return 1
	}

	signals := make(chan os.Signal, 4)
	signal.Notify(signals, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	exits, started := reap()

	start := func(p *proc) {
		p.up, p.stopped = time.Now(), false
		path := p.args[0]
		if !strings.Contains(path, "/") {
			if found, err := exec.LookPath(path); err == nil {
				path = found
			}
		}
		pr, err := os.StartProcess(path, p.args, &os.ProcAttr{Env: p.env, Files: []*os.File{os.Stdin, os.Stdout, os.Stderr}})
		if err != nil {
			say("%s did not start: %v", p.line, err)
			p.status = 127
			return
		}
		p.process = pr
		select {
		case started <- struct{}{}:
		default:
		}
	}
	// stopping: the signal it goes on, and whether it's because one kept
	// exiting
	var stopping os.Signal
	failed := false
	// stop sends the stop to the last running one not sent it yet, in the
	// reverse of the order they started: done when none runs
	stop := func() (done bool) {
		for i := len(procs) - 1; i >= 0; i-- {
			p := procs[i]
			if p.process == nil {
				continue
			}
			if !p.stopped {
				p.stopped = true
				_ = p.process.Signal(stopping)
			}
			return false
		}
		return true
	}
	finish := func() int {
		if failed {
			return 1
		}
		return web.status
	}
	// down is what happens when p exits (or didn't start), at now: started
	// again after a backoff, or, a fifth time in a window, everything stopped
	down := func(p *proc, now time.Time) {
		if !p.beside {
			p.exits = append(p.exits, now)
			for len(p.exits) > 0 && now.Sub(p.exits[0]) >= window {
				p.exits = p.exits[1:]
			}
			if len(p.exits) >= maxExits {
				say("%s exited %d times in %s: stopping", p.line, maxExits, window)
				failed, stopping = true, syscall.SIGTERM
				return
			}
		}
		w := p.exited(now, o.backoff)
		p.next = now.Add(w)
		say("%s exited (%s): starting it again in %s", p.line, how(p.status), w)
	}
	// in order, the renderer once its backend accepts
	ready := backend == nil
	for _, p := range procs {
		if p == web && !ready {
			break
		}
		if start(p); p.process == nil {
			down(p, time.Now())
		}
	}
	tick := time.NewTicker(o.tick)
	defer tick.Stop()
	for {
		select {
		case s := <-signals:
			if stopping == nil {
				stopping = s
			} else {
				// another: to the one stopping now
				for i := len(procs) - 1; i >= 0; i-- {
					if procs[i].process != nil {
						_ = procs[i].process.Signal(s)
						break
					}
				}
			}
			if stop() {
				return finish()
			}
		case e := <-exits:
			var p *proc
			for _, x := range procs {
				if x.process != nil && x.process.Pid == e.pid {
					p = x
				}
			}
			if p == nil {
				continue // an orphan it was given, reaped
			}
			p.process, p.status = nil, code(e.status)
			if stopping == nil {
				down(p, time.Now())
			}
			if stopping != nil && stop() {
				return finish()
			}
		case now := <-tick.C:
			if stopping != nil {
				continue
			}
			for _, p := range procs {
				if p.process != nil || now.Before(p.next) || p == web && !ready {
					continue
				}
				start(p)
				if p.process == nil {
					down(p, now)
					if stopping != nil {
						if stop() {
							return finish()
						}
						break
					}
				}
			}
			if !ready && backend.process != nil && accepts(fmt.Sprintf("127.0.0.1:%d", o.port)) {
				ready = true
				if len(paths) > 0 {
					if ip := outside(); ip != nil && !accepts(net.JoinHostPort(ip.String(), strconv.Itoa(o.port))) {
						say("%s", LoopbackOnly)
						failed, stopping = true, syscall.SIGTERM
						if stop() {
							return finish()
						}
						continue
					}
				}
				start(web)
				if web.process == nil {
					down(web, now)
				}
			}
		}
	}
}

// reap is each child's exit, whichever it is (the supervisor may be the
// sandbox's first process, given orphans), and where to say one started,
// for it to wait again once it had none.
func reap() (<-chan exit, chan<- struct{}) {
	exits, started := make(chan exit, 16), make(chan struct{}, 1)
	go func() {
		for {
			var ws syscall.WaitStatus
			pid, err := syscall.Wait4(-1, &ws, 0, nil)
			switch {
			case errors.Is(err, syscall.ECHILD):
				<-started
			case err != nil:
				time.Sleep(10 * time.Millisecond)
			default:
				exits <- exit{pid, ws}
			}
		}
	}()
	return exits, started
}

// code is an exit's status, as a shell says it.
func code(ws syscall.WaitStatus) int {
	if ws.Signaled() {
		return 128 + int(ws.Signal())
	}
	return ws.ExitStatus()
}

func how(status int) string {
	if status > 128 {
		return "signal " + strconv.Itoa(status-128)
	}
	return "status " + strconv.Itoa(status)
}

// accepts says whether something listens at addr.
func accepts(addr string) bool {
	c, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
	if err != nil {
		return false
	}
	c.Close()
	return true
}

// outside is the sandbox's own address, which the host reaches it on: its
// first that isn't loopback; nil when it has none.
func outside() net.IP {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}
	var v6 net.IP
	for _, a := range addrs {
		n, ok := a.(*net.IPNet)
		if !ok || n.IP.IsLoopback() || n.IP.IsLinkLocalUnicast() || n.IP.IsMulticast() {
			continue
		}
		if n.IP.To4() != nil {
			return n.IP
		}
		if v6 == nil {
			v6 = n.IP
		}
	}
	return v6
}

// readLines is the file's lines that aren't blank, none when there's no file.
func readLines(name string) ([]string, error) {
	b, err := os.ReadFile(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, l := range bytes.Split(b, []byte("\n")) {
		if s := strings.TrimSpace(string(l)); s != "" {
			out = append(out, s)
		}
	}
	return out, nil
}

// heapMB is %heap%: a quarter of memory (MB), at least 64; 128 unknown.
func heapMB(memory string) int {
	mb, err := strconv.Atoi(memory)
	if err != nil || mb <= 0 {
		return 128
	}
	return max(64, mb/4)
}

var envWord = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)

// parse is a line's environment (its words KEY=value before the command)
// and its args, %heap% and %heapbytes% filled in.
func parse(line string, heap int) (env, args []string) {
	line = strings.NewReplacer("%heapbytes%", strconv.Itoa(heap*1048576), "%heap%", strconv.Itoa(heap)).Replace(line)
	words := strings.Fields(line)
	for len(words) > 0 && envWord.MatchString(words[0]) {
		env, words = append(env, words[0]), words[1:]
	}
	return env, words
}

// environ is base, with line's over it and fixed's over both: each name
// once, at its last.
func environ(base, line []string, fixed ...string) []string {
	all := append(append(append([]string{}, base...), line...), fixed...)
	last := map[string]int{}
	for i, kv := range all {
		k, _, _ := strings.Cut(kv, "=")
		last[k] = i
	}
	var out []string
	for i, kv := range all {
		k, _, _ := strings.Cut(kv, "=")
		if last[k] == i {
			out = append(out, kv)
		}
	}
	return out
}
