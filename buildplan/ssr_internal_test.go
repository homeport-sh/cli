package buildplan

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// The supervisor an Inertia SSR app's web runs through (.homeport/beside.php),
// run as homeportd runs it - ./bin php-cli .homeport/beside.php ./bin <the
// web's args> - here with the PHP on PATH as the bin's php-cli. What's beside
// the web (.homeport/beside) is each of the three an SSR renderer ships as:
// the bundle on Node, on Bun, and a binary bun build --compile made. Each
// starts, then the web; a stop reaches the web and the renderer, and the
// web's exit is the supervisor's; a renderer that exits is started again,
// and one that can't start leaves the web serving.
func TestTheSupervisorRunsTheWebBesideItsRenderer(t *testing.T) {
	php, err := exec.LookPath("php")
	if err != nil {
		t.Skip("no php here")
	}
	if out, err := exec.Command(php, "-r", `echo function_exists("pcntl_async_signals") ? "y" : "n";`).Output(); err != nil || string(out) != "y" {
		t.Skip("php here has no pcntl")
	}
	// the renderer: answers on SSR_TEST_PORT, says it started
	ssr := `import http from "node:http"; console.error("renderer up");` +
		`http.createServer((q, s) => s.end("rendered")).listen(Number(process.env.SSR_TEST_PORT), "127.0.0.1");`
	kinds := map[string]func(t *testing.T, dir string) string{}
	for _, rt := range []string{"node", "bun"} {
		kinds[rt] = func(t *testing.T, dir string) string {
			bin, err := exec.LookPath(rt)
			if err != nil {
				t.Skipf("no %s here", rt)
			}
			write(t, dir, "bootstrap/ssr/ssr.mjs", ssr, 0o644)
			return bin + " bootstrap/ssr/ssr.mjs"
		}
	}
	kinds["compiled"] = func(t *testing.T, dir string) string {
		bun, err := exec.LookPath("bun")
		if err != nil {
			t.Skip("no bun here")
		}
		write(t, dir, "src/ssr.mjs", ssr, 0o644)
		compile := exec.Command(bun, "build", "--compile", "src/ssr.mjs", "--outfile", "bootstrap/ssr/server")
		compile.Dir = dir // its temporary files too
		if out, err := compile.CombinedOutput(); err != nil {
			t.Fatalf("compile: %v\n%s", err, out)
		}
		return "bootstrap/ssr/server"
	}
	for kind, make := range kinds {
		t.Run(kind, func(t *testing.T) {
			// stopped: the web gets the TERM and the supervisor ends with it
			dir, port := supervised(t, php)
			besides(t, dir, make(t, dir))
			cmd, out := supervise(t, php, dir, port, "")
			waitFor(t, func() bool {
				return get(port) == "rendered" && strings.Contains(out.String(), "web php-server --listen :8080")
			}, out)
			_ = cmd.Process.Signal(syscall.SIGTERM)
			if err := waitExit(cmd); err != nil {
				t.Fatalf("stopped: %v\n%s", err, out)
			}
			if !strings.Contains(out.String(), "web-stopped") {
				t.Errorf("the web wasn't stopped: %s", out)
			}
			if get(port) != "" {
				t.Errorf("the renderer outlived the web")
			}
		})
	}

	t.Run("exits", func(t *testing.T) {
		node, err := exec.LookPath("node")
		if err != nil {
			t.Skip("no node here")
		}
		// the web's exit is the supervisor's
		dir, port := supervised(t, php)
		write(t, dir, "bootstrap/ssr/ssr.mjs", ssr, 0o644)
		besides(t, dir, node+" bootstrap/ssr/ssr.mjs")
		cmd, out := supervise(t, php, dir, port, "3")
		if ee, ok := waitExit(cmd).(*exec.ExitError); !ok || ee.ExitCode() != 3 {
			t.Errorf("exit: %v\n%s", err, out)
		}

		// a renderer that dies is started again; the web serves on
		dir, port = supervised(t, php)
		write(t, dir, "bootstrap/ssr/ssr.mjs", ssr+`setTimeout(() => process.exit(7), 300);`, 0o644)
		besides(t, dir, node+" bootstrap/ssr/ssr.mjs")
		cmd, out = supervise(t, php, dir, port, "")
		waitFor(t, func() bool { return strings.Count(out.String(), "renderer up") >= 2 }, out)
		if !strings.Contains(out.String(), "status 7") || strings.Contains(out.String(), "web-stopped") {
			t.Errorf("not restarted: %s", out)
		}
		_ = cmd.Process.Signal(syscall.SIGTERM)
		_ = waitExit(cmd)

		// one that can't start: said, and the web serves
		dir, port = supervised(t, php)
		besides(t, dir, "bootstrap/ssr/missing")
		cmd, out = supervise(t, php, dir, port, "")
		waitFor(t, func() bool {
			return strings.Contains(out.String(), "web php-server") && strings.Contains(out.String(), "bootstrap/ssr/missing")
		}, out)
		_ = cmd.Process.Signal(syscall.SIGTERM)
		if err := waitExit(cmd); err != nil {
			t.Errorf("stopped: %v\n%s", err, out)
		}
	})
}

// %heap% in what's beside the web is a quarter of the app's memory
// (HOMEPORT_MEMORY_MB, homeportd's), at least 64, 128 when it isn't known.
func TestTheSupervisorSizesTheRenderersHeap(t *testing.T) {
	php, err := exec.LookPath("php")
	if err != nil {
		t.Skip("no php here")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("no node here")
	}
	for mem, want := range map[string]string{"1024": "256", "256": "64", "128": "64", "": "128"} {
		dir, port := supervised(t, php)
		write(t, dir, "args.mjs", `console.error("heap " + process.argv.slice(2).join(" "));`, 0o644)
		besides(t, dir, node+" args.mjs --max-old-space-size=%heap%")
		cmd, out := supervise(t, php, dir, port, "", "HOMEPORT_MEMORY_MB="+mem)
		waitFor(t, func() bool { return strings.Contains(out.String(), "heap ") }, out)
		if !strings.Contains(out.String(), "heap --max-old-space-size="+want+"\n") {
			t.Errorf("%q MB: %s", mem, out)
		}
		// and says what it started, sized
		if !strings.Contains(out.String(), "homeport: started "+node+" args.mjs --max-old-space-size="+want) {
			t.Errorf("%q MB: not said: %s", mem, out)
		}
		_ = cmd.Process.Signal(syscall.SIGTERM)
		_ = waitExit(cmd)
	}
}

// A word KEY=value before the command is its environment (a compiled
// renderer's heap: BUN_JSC_forceRAMSize=%heapbytes%).
func TestTheSupervisorGivesTheRendererItsEnvironment(t *testing.T) {
	php, err := exec.LookPath("php")
	if err != nil {
		t.Skip("no php here")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("no node here")
	}
	dir, port := supervised(t, php)
	write(t, dir, "env.mjs", `console.error("ram " + process.env.BUN_JSC_forceRAMSize);`, 0o644)
	besides(t, dir, "BUN_JSC_forceRAMSize=%heapbytes% "+node+" env.mjs")
	cmd, out := supervise(t, php, dir, port, "", "HOMEPORT_MEMORY_MB=1024")
	waitFor(t, func() bool { return strings.Contains(out.String(), "ram ") }, out)
	if !strings.Contains(out.String(), "ram 268435456\n") {
		t.Errorf("env: %s", out)
	}
	_ = cmd.Process.Signal(syscall.SIGTERM)
	_ = waitExit(cmd)
}

func write(t *testing.T, dir, name, body string, mode os.FileMode) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
}

// supervised is a bundle's folder: the supervisor, and a bin that is the web
// when asked to be one (it says its args, and how it was stopped, then exits
// with EXIT_WITH) and php-cli otherwise.
func supervised(t *testing.T, php string) (string, int) {
	t.Helper()
	dir := t.TempDir()
	write(t, dir, ".homeport/beside.php", besidePHP, 0o644)
	write(t, dir, "package.json", `{"type":"module"}`, 0o644)
	write(t, dir, "bin", "#!/bin/sh\nif [ \"$1\" = php-cli ]; then shift; exec '"+php+"' \"$@\"; fi\n"+
		"echo \"web $*\"\ntrap 'echo web-stopped; exit 0' TERM\n"+
		"[ -n \"$EXIT_WITH\" ] && exit \"$EXIT_WITH\"\nwhile :; do sleep 0.05; done\n", 0o755)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	return dir, port
}

func besides(t *testing.T, dir, line string) { write(t, dir, ".homeport/beside", line+"\n", 0o644) }

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *syncBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

func supervise(t *testing.T, php, dir string, port int, exitWith string, env ...string) (*exec.Cmd, *syncBuf) {
	t.Helper()
	out := &syncBuf{}
	cmd := exec.Command("./bin", "php-cli", ".homeport/beside.php", "./bin", "php-server", "--listen", ":8080")
	cmd.Dir, cmd.Stdout, cmd.Stderr = dir, out, out
	cmd.Env = append(append(os.Environ(), fmt.Sprintf("SSR_TEST_PORT=%d", port), "EXIT_WITH="+exitWith), env...)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	return cmd, out
}

func get(port int) string {
	c := http.Client{Timeout: 500 * time.Millisecond}
	r, err := c.Get(fmt.Sprintf("http://127.0.0.1:%d/", port))
	if err != nil {
		return ""
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(r.Body)
	return string(b)
}

func waitFor(t *testing.T, ok func() bool, out *syncBuf) {
	t.Helper()
	for end := time.Now().Add(15 * time.Second); time.Now().Before(end); time.Sleep(50 * time.Millisecond) {
		if ok() {
			return
		}
	}
	t.Fatalf("timed out:\n%s", out)
}

func waitExit(cmd *exec.Cmd) error {
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return err
	case <-time.After(10 * time.Second):
		return fmt.Errorf("still running after 10s")
	}
}
