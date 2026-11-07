package main

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
	"testing"
	"time"
)

// homeport dev serves api/ as the build's server does, with the Vite line;
// a handler added while it runs is served without a restart.
func TestDevServesAPI(t *testing.T) {
	if _, err := exec.LookPath("bun"); err != nil {
		t.Skip("no bun")
	}
	dir := t.TempDir()
	write := func(name, body string) {
		_ = os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755)
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("api/hello/[name].ts", "export const GET = (req: Request, { params }) => Response.json({ hi: params.name })\n")
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	u := fmt.Sprintf("http://127.0.0.1:%d", port)
	get := func(path string) (int, string) {
		for range 100 {
			r, err := http.Get(u + path)
			if err == nil {
				b, _ := io.ReadAll(r.Body)
				r.Body.Close()
				return r.StatusCode, strings.TrimSpace(string(b))
			}
			time.Sleep(50 * time.Millisecond)
		}
		return 0, ""
	}
	var out, errs bytes.Buffer
	a := &app{in: strings.NewReader(""), out: &out, err: &errs, wd: dir}
	a.exec = func(c *exec.Cmd) error {
		if err := c.Start(); err != nil {
			return err
		}
		defer func() { _ = c.Process.Kill(); _ = c.Wait() }()
		if s, b := get("/api/hello/ann"); s != 200 || b != `{"hi":"ann"}` {
			t.Errorf("/api/hello/ann: %d %q", s, b)
		}
		if s, _ := get("/api/new"); s != 404 {
			t.Errorf("/api/new before it exists: %d", s)
		}
		write("api/new.ts", "export function POST() { return new Response('made') }\n")
		deadline := time.Now().Add(15 * time.Second)
		for {
			r, err := http.Post(u+"/api/new", "text/plain", nil)
			if err == nil {
				b, _ := io.ReadAll(r.Body)
				r.Body.Close()
				if r.StatusCode == 200 && string(b) == "made" {
					break
				}
			}
			if time.Now().After(deadline) {
				t.Errorf("api/new.ts never served\n%s%s", out.String(), errs.String())
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		return nil
	}
	if code := a.run([]string{"dev", "--port", fmt.Sprint(port)}); code != 0 {
		t.Fatalf("exit %d: %s", code, errs.String())
	}
	if !strings.Contains(errs.String(), fmt.Sprintf("server: { proxy: { '/api': 'http://localhost:%d' } }", port)) {
		t.Errorf("no Vite line:\n%s", errs.String())
	}
	if _, err := os.Stat(filepath.Join(dir, ".homeport-api-entry.mjs")); err == nil {
		t.Errorf("dev wrote into the app")
	}
}

// Without api/ handlers, homeport dev says what it serves.
func TestDevWithoutAPI(t *testing.T) {
	if _, err := exec.LookPath("bun"); err != nil {
		t.Skip("no bun")
	}
	var errs bytes.Buffer
	a := &app{in: strings.NewReader(""), out: io.Discard, err: &errs, wd: t.TempDir(), exec: (*exec.Cmd).Run}
	if code := a.run([]string{"dev"}); code != exitError || !strings.Contains(errs.String(), "no api/ handlers here") {
		t.Fatalf("exit %d: %s", code, errs.String())
	}
}
