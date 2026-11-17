package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"

	"github.com/homeport-sh/cli/buildplan"
)

// dev serves this folder's api/ handlers on :3000 as the hosted build's
// server does - the same server, its entry generated into a temp folder -
// with Bun's --watch: an edited handler reloads, and a handler added or
// removed regenerates the entry. API only: the frontend's own dev server
// proxies /api to it, the line printed for Vite.
func (a *app) dev(ctx context.Context, args []string) error {
	fs := a.flags("dev")
	port := fs.Int("port", 3000, "the port to serve on")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return usageErr("usage: homeport dev [--port 3000]")
	}
	// TypeScript, run as is: Bun, whichever runtime the app deploys on
	bun, err := exec.LookPath("bun")
	if err != nil {
		return errors.New("homeport dev runs your handlers with Bun, and there's none on PATH: install it (https://bun.sh)")
	}
	tmp, err := os.MkdirTemp("", "homeport-dev-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	self, entry := filepath.Join(tmp, "api-server.mjs"), filepath.Join(tmp, "entry.mjs")
	if err := os.WriteFile(self, []byte(buildplan.APIServer()), 0o644); err != nil {
		return err
	}
	if st, err := os.Stat(filepath.Join(a.wd, "api")); err != nil || !st.IsDir() {
		return errors.New(noAPI)
	}
	gen := func() ([]byte, error) {
		p, err := planFolder(a.wd, buildplan.Settings{})
		if err != nil {
			return nil, err
		}
		if p.API == nil {
			return nil, errors.New(noAPI)
		}
		j, _ := json.Marshal(map[string]any{"routes": p.API.Routes, "middleware": p.API.Middleware, "entry": entry, "self": self})
		c := exec.CommandContext(ctx, bun, self, "--homeport-api-dev", base64.StdEncoding.EncodeToString(j))
		c.Dir = a.wd
		if out, err := c.CombinedOutput(); err != nil {
			return nil, fmt.Errorf("%v: %s", err, bytes.TrimSpace(out))
		}
		return j, nil
	}
	last, err := gen()
	if err != nil {
		return err
	}
	fmt.Fprintf(a.err, "==> api/ on http://localhost:%d; in vite.config, so the frontend's dev server sends /api here:\n    server: { proxy: { '/api': 'http://localhost:%d' } }\n", *port, *port)

	ctx, stop := context.WithCancel(ctx)
	defer stop()
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
			}
			p, err := planFolder(a.wd, buildplan.Settings{})
			if err != nil || p.API == nil {
				continue // mid-edit; the server keeps what it has
			}
			j, _ := json.Marshal(map[string]any{"routes": p.API.Routes, "middleware": p.API.Middleware, "entry": entry, "self": self})
			if bytes.Equal(j, last) {
				continue
			}
			if _, err := gen(); err != nil {
				fmt.Fprintln(a.err, "homeport dev:", err)
				continue
			}
			last = j
			fmt.Fprintln(a.err, "==> api/ changed: the routes are regenerated")
		}
	}()
	c := exec.CommandContext(ctx, bun, "--watch", entry)
	c.Dir = a.wd
	c.Env = append(os.Environ(), "PORT="+strconv.Itoa(*port))
	c.Stdin, c.Stdout, c.Stderr = a.in, a.out, a.err
	return a.exec(c)
}

const noAPI = "no api/ handlers here: homeport dev serves a folder's api/ (api/hello.ts exporting GET, say)"
