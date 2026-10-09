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
	"syscall"
	"testing"
	"time"
)

// The wrapper an Inertia SSR app's web runs through (.homeport/ssr.mjs), run
// as homeportd runs it - <runtime> .homeport/ssr.mjs ./bin <the web's args> -
// on Node and on Bun: Inertia's SSR server starts in it, then the web as its
// child; a stop reaches the web, and the web's exit is its exit. An SSR bundle
// that fails to load leaves the web serving, pages rendered in the browser.
func TestTheSSRWrapperRunsTheWebBesideInertiasServer(t *testing.T) {
	for _, rt := range []string{"node", "bun"} {
		t.Run(rt, func(t *testing.T) {
			bin, err := exec.LookPath(rt)
			if err != nil {
				t.Skipf("no %s here", rt)
			}
			// the web: says its args, and how it was stopped, then exits
			// with what EXIT_WITH says
			web := "#!/bin/sh\necho \"web $*\"\ntrap 'echo web-stopped; exit 0' TERM\n" +
				"[ -n \"$EXIT_WITH\" ] && exit \"$EXIT_WITH\"\nwhile :; do sleep 0.05; done\n"
			ssr := `import http from "node:http";` +
				`http.createServer((q, s) => s.end("rendered")).listen(Number(process.env.SSR_TEST_PORT), "127.0.0.1");`

			// stopped: the web gets the TERM, and the wrapper ends with it
			dir, port := app(t, web, map[string]string{"bootstrap/ssr/ssr.mjs": ssr})
			cmd, out := wrapper(t, bin, dir, port, "")
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

			// the web's exit is the wrapper's
			dir, port = app(t, web, map[string]string{"bootstrap/ssr/ssr.js": ssr})
			cmd, out = wrapper(t, bin, dir, port, "3")
			err = waitExit(cmd)
			if ee, ok := err.(*exec.ExitError); !ok || ee.ExitCode() != 3 {
				t.Errorf("exit: %v\n%s", err, out)
			}

			// an SSR bundle that throws: the web still serves, and it's said
			dir, port = app(t, web, map[string]string{"bootstrap/ssr/ssr.mjs": `throw new Error("boom");`})
			cmd, out = wrapper(t, bin, dir, port, "")
			waitFor(t, func() bool { return strings.Contains(out.String(), "web php-server") }, out)
			if !strings.Contains(out.String(), "render in the browser") || !strings.Contains(out.String(), "boom") {
				t.Errorf("not said: %s", out)
			}
			_ = cmd.Process.Signal(syscall.SIGTERM)
			_ = waitExit(cmd)
		})
	}
}

func app(t *testing.T, web string, files map[string]string) (string, int) {
	t.Helper()
	dir := t.TempDir()
	files[".homeport/ssr.mjs"] = ssrWrapper
	files["package.json"] = `{"type":"module"}`
	for name, body := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "bin"), []byte(web), 0o755); err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	return dir, port
}

type syncBuf struct {
	mu chan struct{}
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu <- struct{}{}
	defer func() { <-s.mu }()
	return s.b.Write(p)
}
func (s *syncBuf) String() string { s.mu <- struct{}{}; defer func() { <-s.mu }(); return s.b.String() }

func wrapper(t *testing.T, bin, dir string, port int, exitWith string) (*exec.Cmd, *syncBuf) {
	t.Helper()
	out := &syncBuf{mu: make(chan struct{}, 1)}
	cmd := exec.Command(bin, ".homeport/ssr.mjs", "./bin", "php-server", "--listen", ":8080")
	cmd.Dir, cmd.Stdout, cmd.Stderr = dir, out, out
	cmd.Env = append(os.Environ(), fmt.Sprintf("SSR_TEST_PORT=%d", port), "EXIT_WITH="+exitWith)
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
