package supervise

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// The test binary is the supervisor (SUPERVISE_TEST, the args as JSON) and
// each of its children (HELPER, below), so nothing outside the test runs.
func TestMain(m *testing.M) {
	if a := os.Getenv("SUPERVISE_TEST"); a != "" {
		var args []string
		if err := json.Unmarshal([]byte(a), &args); err != nil {
			panic(err)
		}
		port, _ := strconv.Atoi(os.Getenv("SUPERVISE_PORT"))
		// its children are helpers, not supervisors again
		os.Unsetenv("SUPERVISE_TEST")
		os.Exit(run(args, os.Stderr, options{port: port, backoff: 20 * time.Millisecond, tick: 10 * time.Millisecond}))
	}
	if name := os.Getenv("HELPER"); name != "" {
		helper(name)
		return
	}
	os.Exit(m.Run())
}

// helper is a child: it says it ran, with what it was given; listens on
// HELPER_LISTEN's host at $PORT when it's set, after HELPER_DELAY; exits
// with HELPER_EXIT when it's set; and on a stop says so and exits with
// HELPER_STOP_EXIT.
func helper(name string) {
	say := func(f string, a ...any) { fmt.Fprintf(os.Stderr, name+" "+f+"\n", a...) }
	say("ran args=%s port=%s backend=%s ram=%s pid=%d", strings.Join(os.Args[1:], ","), os.Getenv("PORT"), os.Getenv("RSC_BACKEND"), os.Getenv("BUN_JSC_forceRAMSize"), os.Getpid())
	if c := os.Getenv("HELPER_EXIT"); c != "" {
		n, _ := strconv.Atoi(c)
		os.Exit(n)
	}
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	if d := os.Getenv("HELPER_DELAY"); d != "" {
		t, _ := time.ParseDuration(d)
		time.Sleep(t)
	}
	if host, ok := os.LookupEnv("HELPER_LISTEN"); ok {
		l, err := net.Listen("tcp", net.JoinHostPort(host, os.Getenv("PORT")))
		if err != nil {
			say("listen: %v", err)
			os.Exit(1)
		}
		go func() {
			for {
				c, err := l.Accept()
				if err != nil {
					return
				}
				c.Close()
			}
		}()
		say("up")
	}
	s := <-stop
	say("stopped by %s", s)
	n, _ := strconv.Atoi(os.Getenv("HELPER_STOP_EXIT"))
	os.Exit(n)
}

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *syncBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

// bundle is a bundle's folder with files (name: content) in it, and a free
// port for its backend.
func bundle(t *testing.T, files map[string]string) (string, int) {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return dir, l.Addr().(*net.TCPAddr).Port
}

// child is a line that runs the test binary as the helper name, with env.
func child(name string, env ...string) string {
	exe, _ := os.Executable()
	return strings.Join(append(append([]string{"HELPER=" + name}, env...), exe), " ")
}

// supervise runs the supervisor in dir as homeportd runs a bundle's web,
// with args after it and env (KEY=value) around it.
func supervise(t *testing.T, dir string, port int, args []string, env ...string) (*exec.Cmd, *syncBuf) {
	t.Helper()
	exe, _ := os.Executable()
	a, _ := json.Marshal(args)
	out := &syncBuf{}
	cmd := exec.Command(exe)
	cmd.Dir, cmd.Stdout, cmd.Stderr = dir, out, out
	cmd.Env = append(append(slices.DeleteFunc(os.Environ(), func(e string) bool { return strings.HasPrefix(e, "HELPER") }),
		"SUPERVISE_TEST="+string(a), "SUPERVISE_PORT="+strconv.Itoa(port)), env...)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	return cmd, out
}

// argv is a child's line as the supervisor's own args: the helper name, by
// its environment (the supervisor's children inherit it).
func argv(name string) ([]string, string) {
	exe, _ := os.Executable()
	return []string{exe, "a1", "a2"}, "HELPER=" + name
}

func waitFor(t *testing.T, ok func() bool, out *syncBuf) {
	t.Helper()
	for end := time.Now().Add(15 * time.Second); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		if ok() {
			return
		}
	}
	t.Fatalf("timed out:\n%s", out)
}

func exitCode(t *testing.T, cmd *exec.Cmd, out *syncBuf) int {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err == nil {
			return 0
		}
		if ee, ok := err.(*exec.ExitError); ok {
			return ee.ExitCode()
		}
		t.Fatalf("wait: %v", err)
	case <-time.After(15 * time.Second):
		t.Fatalf("still running after 15s:\n%s", out)
	}
	return -1
}

// firstPid is the pid name first ran as in out, 0 when it never ran.
func firstPid(out, name string) int {
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, name+" ran ") {
			if _, p, ok := strings.Cut(l, " pid="); ok {
				n, _ := strconv.Atoi(p)
				return n
			}
		}
	}
	return 0
}

// before says whether a is in out, and before b.
func before(out, a, b string) bool {
	i, j := strings.Index(out, a), strings.Index(out, b)
	return i >= 0 && j >= 0 && i < j
}

// A backend beside a renderer: the backend (the supervisor's args, as
// homeportd gives them: ./bin and the web's args) starts first on the
// backend's port, and only once it accepts a connection does the renderer
// (.homeport/renderer) start, on the app's $PORT, told where its backend
// is. A stop goes to the renderer first, then the backend, and the
// supervisor exits as the renderer did.
func TestABackendStartsBeforeItsRenderer(t *testing.T) {
	dir, port := bundle(t, map[string]string{RendererFile: child("renderer", "HELPER_LISTEN=127.0.0.1", "HELPER_STOP_EXIT=3") + "\n"})
	args, env := argv("backend")
	cmd, out := supervise(t, dir, port, args, env, "HELPER_LISTEN=", "HELPER_DELAY=300ms", "PORT=0")
	// PORT=0 is the app's port as homeportd gives it: the renderer's
	waitFor(t, func() bool { return strings.Contains(out.String(), "renderer up") }, out)
	s := out.String()
	if !before(s, "backend up", "renderer ran") {
		t.Errorf("the renderer started before its backend accepted:\n%s", s)
	}
	if !strings.Contains(s, fmt.Sprintf("backend ran args=a1,a2 port=%d backend= ", port)) {
		t.Errorf("the backend's port, args:\n%s", s)
	}
	if !strings.Contains(s, fmt.Sprintf("renderer ran args= port=0 backend=http://127.0.0.1:%d ", port)) {
		t.Errorf("the renderer's port, or its backend:\n%s", s)
	}
	_ = cmd.Process.Signal(syscall.SIGTERM)
	if c := exitCode(t, cmd, out); c != 3 {
		t.Errorf("exit %d, want the renderer's 3:\n%s", c, out)
	}
	if s := out.String(); !before(s, "renderer stopped by terminated", "backend stopped by terminated") {
		t.Errorf("not stopped renderer first, then backend:\n%s", s)
	}
}

// A backend with paths is reached by the host on the sandbox's address, not
// on loopback: one listening on 127.0.0.1 alone is refused before the
// renderer starts, saying how to fix it - not a 502 later. With no paths,
// loopback is all it needs.
func TestABackendWithPathsListensOnEveryAddress(t *testing.T) {
	if outside() == nil {
		t.Skip("no address here but loopback")
	}
	t.Run("loopback only, paths", func(t *testing.T) {
		dir, port := bundle(t, map[string]string{RendererFile: child("renderer", "HELPER_LISTEN=") + "\n", BackendPathsFile: "/api\n"})
		args, env := argv("backend")
		cmd, out := supervise(t, dir, port, args, env, "HELPER_LISTEN=127.0.0.1", "PORT=0")
		if c := exitCode(t, cmd, out); c == 0 {
			t.Errorf("exit 0:\n%s", out)
		}
		s := out.String()
		if !strings.Contains(s, "homeport: the backend listens on 127.0.0.1 only, and it has backend paths: listen on :$PORT\n") {
			t.Errorf("not said:\n%s", s)
		}
		if strings.Contains(s, "renderer ran") || !strings.Contains(s, "backend stopped") {
			t.Errorf("the renderer started, or the backend wasn't stopped:\n%s", s)
		}
	})
	for name, files := range map[string]map[string]string{
		"every address, paths":   {BackendPathsFile: "/api\n"},
		"loopback only, none":    {BackendPathsFile: ""},
		"loopback only, no file": {},
	} {
		t.Run(name, func(t *testing.T) {
			files[RendererFile] = child("renderer", "HELPER_LISTEN=") + "\n"
			dir, port := bundle(t, files)
			args, env := argv("backend")
			listen := "HELPER_LISTEN=127.0.0.1"
			if strings.HasPrefix(name, "every") {
				listen = "HELPER_LISTEN="
			}
			cmd, out := supervise(t, dir, port, args, env, listen, "PORT=0")
			waitFor(t, func() bool { return strings.Contains(out.String(), "renderer up") }, out)
			_ = cmd.Process.Signal(syscall.SIGTERM)
			if c := exitCode(t, cmd, out); c != 0 {
				t.Errorf("exit %d:\n%s", c, out)
			}
		})
	}
}

// A backend that keeps exiting is started again, a backoff later, up to
// five exits in a minute: then the supervisor stops what's left and exits,
// for the host to see the app fail rather than a renderer answering errors.
func TestABackendThatKeepsExitingStopsTheApp(t *testing.T) {
	dir, port := bundle(t, map[string]string{RendererFile: child("renderer", "HELPER_LISTEN=") + "\n"})
	args, env := argv("backend")
	cmd, out := supervise(t, dir, port, args, env, "HELPER_EXIT=4", "PORT=0")
	if c := exitCode(t, cmd, out); c != 1 {
		t.Errorf("exit %d, want 1:\n%s", c, out)
	}
	s := out.String()
	if n := strings.Count(s, "backend ran"); n != 5 {
		t.Errorf("ran %d times, want 5:\n%s", n, s)
	}
	for _, want := range []string{"exited (status 4): starting it again in 20ms", "exited (status 4): starting it again in 40ms",
		"exited 5 times in 1m0s: stopping"} {
		if !strings.Contains(s, want) {
			t.Errorf("no %q:\n%s", want, s)
		}
	}
	if strings.Contains(s, "renderer ran") {
		t.Errorf("the renderer started:\n%s", s)
	}
}

// A renderer that exits is started again; its backend serves on.
func TestARendererThatExitsIsStartedAgain(t *testing.T) {
	dir, port := bundle(t, map[string]string{RendererFile: child("renderer", "HELPER_EXIT=7") + "\n"})
	args, env := argv("backend")
	cmd, out := supervise(t, dir, port, args, env, "HELPER_LISTEN=", "PORT=0")
	if c := exitCode(t, cmd, out); c != 1 {
		t.Errorf("exit %d, want 1:\n%s", c, out)
	}
	s := out.String()
	if strings.Count(s, "renderer ran") != 5 || strings.Count(s, "backend ran") != 1 || !strings.Contains(s, "backend stopped") {
		t.Errorf("not the renderer alone started again, then everything stopped:\n%s", s)
	}
}

// Without a renderer, what's beside the web (.homeport/beside: Inertia's SSR
// renderer) starts first, then the web, the supervisor's args. One beside
// it that exits is started again however often: the web serves without it.
// A stop goes to the web, then what's beside it, and the supervisor exits
// as the web did.
func TestWhatsBesideTheWebStartsFirst(t *testing.T) {
	dir, port := bundle(t, map[string]string{BesideFile: child("ssr", "HELPER_EXIT=7") + " --max-old-space-size=%heap%\n" +
		child("ssr2") + "\n\n"})
	args, env := argv("web")
	cmd, out := supervise(t, dir, port, args, env, "PORT=8000", "HOMEPORT_MEMORY_MB=1024", "HELPER_STOP_EXIT=5")
	waitFor(t, func() bool { return strings.Count(out.String(), "ssr ran") >= 7 }, out)
	s := out.String()
	// started before, by pid: each is forked in turn, and pids go up; when
	// each says it ran is up to the scheduler
	if ssr, ssr2, web := firstPid(s, "ssr"), firstPid(s, "ssr2"), firstPid(s, "web"); ssr == 0 || ssr2 == 0 || web == 0 || ssr > web || ssr2 > web {
		t.Errorf("the web started before what's beside it:\n%s", s)
	}
	if !strings.Contains(s, "ssr ran args=--max-old-space-size=256 port=8000 ") || !strings.Contains(s, "web ran args=a1,a2 port=8000 backend= ") {
		t.Errorf("args or port:\n%s", s)
	}
	_ = cmd.Process.Signal(syscall.SIGTERM)
	if c := exitCode(t, cmd, out); c != 5 {
		t.Errorf("exit %d, want the web's 5:\n%s", c, out)
	}
	if s := out.String(); !before(s, "web stopped by terminated", "ssr2 stopped by terminated") {
		t.Errorf("not stopped web first:\n%s", s)
	}
}

// One that can't start is said, and counts as an exit.
func TestOneThatCantStartIsSaid(t *testing.T) {
	dir, port := bundle(t, map[string]string{BesideFile: "bootstrap/ssr/missing\n"})
	args, env := argv("web")
	cmd, out := supervise(t, dir, port, args, env)
	waitFor(t, func() bool {
		s := out.String()
		return strings.Contains(s, "web ran") && strings.Count(s, "homeport: bootstrap/ssr/missing did not start") >= 2
	}, out)
	_ = cmd.Process.Signal(syscall.SIGTERM)
	if c := exitCode(t, cmd, out); c != 0 {
		t.Errorf("exit %d:\n%s", c, out)
	}
}

// %heap% is a quarter of the app's memory (homeportd's HOMEPORT_MEMORY_MB) in
// MB, at least 64, 128 when it isn't known; %heapbytes% the same in bytes. A
// word KEY=value before the command is its environment.
func TestALinesHeapAndEnvironment(t *testing.T) {
	for mem, want := range map[string]int{"1024": 256, "256": 64, "128": 64, "": 128, "x": 128} {
		if got := heapMB(mem); got != want {
			t.Errorf("heapMB(%q) = %d, want %d", mem, got, want)
		}
	}
	env, args := parse("  A=1 BUN_JSC_forceRAMSize=%heapbytes% ./x --max-old-space-size=%heap% B=2 ", 256)
	if !slices.Equal(env, []string{"A=1", "BUN_JSC_forceRAMSize=268435456"}) || !slices.Equal(args, []string{"./x", "--max-old-space-size=256", "B=2"}) {
		t.Errorf("env %q, args %q", env, args)
	}
	if env, args := parse("A=1", 64); len(args) != 0 || len(env) != 1 {
		t.Errorf("env %q, args %q", env, args)
	}
}

// A backoff starts at the base and doubles to 30s; one that ran a minute
// starts over at the base.
func TestBackoff(t *testing.T) {
	c := &proc{}
	base, now := time.Second, time.Now()
	var got []time.Duration
	for range 7 {
		c.up = now.Add(-time.Second)
		got = append(got, c.exited(now, base))
	}
	if want := []time.Duration{1, 2, 4, 8, 16, 30, 30}; !slices.EqualFunc(got, want, func(a, b time.Duration) bool { return a == b*time.Second }) {
		t.Errorf("backoff %v", got)
	}
	c.up = now.Add(-61 * time.Second)
	if w := c.exited(now, base); w != time.Second {
		t.Errorf("after a minute up: %v", w)
	}
}

// The environment a child gets: the supervisor's, its line's over it, and
// what the supervisor sets over both.
func TestEnviron(t *testing.T) {
	got := environ([]string{"PORT=1", "A=0", "B=0"}, []string{"A=1", "PORT=2"}, "PORT=8080")
	if !slices.Equal(got, []string{"B=0", "A=1", "PORT=8080"}) {
		t.Errorf("%q", got)
	}
}
