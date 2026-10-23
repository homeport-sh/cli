package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/homeport-sh/cli/buildplan"
	"github.com/homeport-sh/cli/internal/cloud"
)

func ptr[T any](v T) *T { return &v }

// `homeport queue status`: how the environment's queue runs, what its last
// report said, and its recent drains.
func TestQueueStatusSaysHowItRunsAndItsDrains(t *testing.T) {
	f, h := linkedHarness(t)
	started := time.Date(2026, 10, 22, 14, 0, 0, 0, time.UTC)
	f.queue = &cloud.Queue{Mode: "on_demand", Backend: "postgres", Command: "queue:drain", TimeoutSeconds: 600,
		Waiting: ptr(3), NextDue: ptr(started.Add(time.Hour)), ReportedAt: ptr(started.Add(2 * time.Minute)),
		Drains: []cloud.QueueDrain{
			{ID: "qd2", Trigger: "due", Status: "running", StartedAt: ptr(started.Add(time.Hour))},
			{ID: "qd1", Trigger: "activity", Status: "succeeded", StartedAt: ptr(started), DurationMS: ptr(int64(95_000)),
				Processed: ptr(12), Failed: ptr(1), Waiting: ptr(3), Reported: true},
		}}
	if code := h.run("queue", "status"); code != 0 {
		t.Fatalf("%d %s", code, h.said())
	}
	said := h.said()
	for _, want := range []string{"alice/blog (production)", "on demand", "postgres", "queue:drain", "10m", "3 waiting",
		"TRIGGER", "STATUS", "due", "running", "activity", "succeeded", "1m35s", "12", "1"} {
		if !strings.Contains(said, want) {
			t.Errorf("didn't say %q:\n%s", want, said)
		}
	}
	if got := strings.Join(f.queueCalls, "|"); got != "GET " {
		t.Errorf("calls %s", got)
	}

	// always on: no drains to show, and it says so
	f.queue = &cloud.Queue{Mode: "always_on", Backend: "postgres", Drains: []cloud.QueueDrain{}}
	if code := h.run("queue", "status"); code != 0 || !strings.Contains(h.said(), "always on") {
		t.Fatalf("always on: %d %s", code, h.said())
	}

	// none: the platform says why
	f.queue = nil
	if code := h.run("queue", "status"); code == 0 || !strings.Contains(h.said(), "has no queue") {
		t.Fatalf("none: %d %s", code, h.said())
	}
}

// `homeport queue drain --remote` starts a drain on homeport now; one
// that's running is said plainly. `retry-failed --remote` runs the queue's
// retry the same way.
func TestQueueDrainAndRetryOnHomeport(t *testing.T) {
	f, h := linkedHarness(t)
	f.queue = &cloud.Queue{Mode: "on_demand", Backend: "postgres", Command: "queue:drain", TimeoutSeconds: 600}
	if code := h.run("queue", "drain", "--remote"); code != 0 || !strings.Contains(h.said(), "alice/blog (production)") || !strings.Contains(h.said(), "homeport queue status") {
		t.Fatalf("drain: %d %s", code, h.said())
	}
	f.queueBusy = true
	if code := h.run("queue", "drain", "--remote"); code == 0 || !strings.Contains(h.said(), "a drain is running") {
		t.Fatalf("busy: %d %s", code, h.said())
	}
	if code := h.run("queue", "retry-failed", "--remote"); code != 0 || !strings.Contains(h.said(), "retry") {
		t.Fatalf("retry: %d %s", code, h.said())
	}
	if got := strings.Join(f.queueCalls, "|"); got != "POST /drain|POST /drain|POST /retry-failed" {
		t.Errorf("calls %s", got)
	}
	if code := h.run("queue", "nope"); code != exitUsage {
		t.Errorf("unknown: %d %s", code, h.said())
	}
}

// ran is what a local drain ran: the command and where.
type ran struct {
	args []string
	dir  string
	// the queue runner, as the drain read it
	runner string
}

// localHarness is the CLI in a folder holding files, its commands caught
// (exit code: what they return), not run.
func localHarness(t *testing.T, files map[string]string, code int) (*harness, *[]ran) {
	t.Helper()
	h := newHarness(t, newFakeAPI(t))
	for name, body := range files {
		p := filepath.Join(h.a.wd, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var runs []ran
	h.a.exec = func(c *exec.Cmd) error {
		r := ran{args: c.Args, dir: c.Dir}
		for _, a := range c.Args {
			if strings.HasSuffix(a, "/queue.php") {
				b, _ := os.ReadFile(a)
				r.runner = string(b)
			}
		}
		runs = append(runs, r)
		if code != 0 {
			return exec.Command("sh", "-c", "exit "+strconv.Itoa(code)).Run() // an exit status of its own
		}
		return nil
	}
	return h, &runs
}

// `homeport queue drain` alone runs the app's drain here, as homeport would
// - with the local .env, which Laravel and Bun read themselves and Node is
// told to - for the queue in its own local database. No sign-in needed.
func TestQueueDrainRunsTheDrainHere(t *testing.T) {
	const command = "export default async () => {}\n"
	for name, c := range map[string]struct {
		files map[string]string
		args  func(wd string) []string
	}{
		"Laravel, its runner": {map[string]string{"composer.json": `{"require":{"laravel/framework":"^13.0"}}`, "composer.lock": "{}",
			"app/Jobs/Send.php": "<?php class Send implements ShouldQueue {}\n"},
			nil},
		"pg-boss on Bun": {map[string]string{"bun.lock": "{}", "package.json": `{"scripts":{"start":"bun src/index.ts","queue:drain":"bun scripts/drain.ts"},"dependencies":{"hono":"^4","pg-boss":"^12"}}`},
			func(string) []string { return []string{"bun", "scripts/drain.ts"} }},
		"pg-boss on Node, its .env": {map[string]string{"package-lock.json": `{"lockfileVersion":3}`, ".env": "DATABASE_URL=postgres://localhost/app\n",
			"package.json": `{"scripts":{"start":"node server.js","queue:drain":"node dist/drain.js"},"dependencies":{"express":"^5","pg-boss":"^12"}}`},
			func(string) []string { return []string{"node", "--env-file=.env", "dist/drain.js"} }},
		"rsc-kit's command": {map[string]string{"bun.lock": "{}", "package.json": `{"scripts":{"build":"bun --bun vite build"},"dependencies":{"@rsc-kit/core":"^0.30","pg-boss":"^12"}}`,
			"src/commands/queue:drain.mts": command},
			func(string) []string { return []string{"bun", "src/commands/queue:drain.mts"} }},
	} {
		h, runs := localHarness(t, c.files, 0)
		if code := h.run("queue", "drain"); code != 0 {
			t.Errorf("%s: %d %s", name, code, h.said())
			continue
		}
		if len(*runs) != 1 || (*runs)[0].dir != h.a.wd {
			t.Errorf("%s: ran %+v", name, *runs)
			continue
		}
		r := (*runs)[0]
		if c.args == nil { // Laravel: the queue runner, from a file of its own
			if len(r.args) != 3 || r.args[0] != "php" || r.args[2] != "./bin" || r.runner != buildplan.QueueRunnerPHP() {
				t.Errorf("%s: ran %q", name, r.args)
			}
			if _, err := os.Stat(r.args[1]); err == nil {
				t.Errorf("%s: the runner's file is left behind", name)
			}
			continue
		}
		if want := c.args(h.a.wd); !slices.Equal(r.args, want) {
			t.Errorf("%s: ran %q, not %q", name, r.args, want)
		}
	}

	// the queue's retry the same way
	h, runs := localHarness(t, map[string]string{"bun.lock": "{}",
		"package.json": `{"scripts":{"start":"bun src/index.ts","queue:drain":"bun d.ts","queue:retry":"bun r.ts --all"},"dependencies":{"hono":"^4","pg-boss":"^12"}}`}, 0)
	if code := h.run("queue", "retry-failed"); code != 0 || len(*runs) != 1 || !slices.Equal((*runs)[0].args, []string{"bun", "r.ts", "--all"}) {
		t.Errorf("retry: %d %+v %s", code, *runs, h.said())
	}

	// the drain's failure is the command's
	h, _ = localHarness(t, map[string]string{"bun.lock": "{}",
		"package.json": `{"scripts":{"start":"bun src/index.ts","queue:drain":"bun d.ts"},"dependencies":{"hono":"^4","pg-boss":"^12"}}`}, 3)
	if code := h.run("queue", "drain"); code != 3 {
		t.Errorf("failed: %d %s", code, h.said())
	}

	// no drain: why, from the plan; no queue: how to say one
	for name, c := range map[string]struct {
		files map[string]string
		says  string
	}{
		"no drain": {map[string]string{"bun.lock": "{}", "package.json": `{"scripts":{"start":"bun src/index.ts"},"dependencies":{"hono":"^4","pg-boss":"^12"}}`}, "@homeport-sh/queue"},
		"no retry": {map[string]string{"bun.lock": "{}", "package.json": `{"scripts":{"start":"bun src/index.ts","queue:drain":"bun d.ts"},"dependencies":{"hono":"^4","pg-boss":"^12"}}`}, "retry"},
		"no queue": {map[string]string{"bun.lock": "{}", "package.json": `{"scripts":{"start":"bun src/index.ts"},"dependencies":{"hono":"^4"}}`}, "queue:"},
	} {
		h, runs := localHarness(t, c.files, 0)
		cmd := "drain"
		if name == "no retry" {
			cmd = "retry-failed"
		}
		if code := h.run("queue", cmd); code == 0 || len(*runs) != 0 || !strings.Contains(h.said(), c.says) {
			t.Errorf("%s: %d %+v %s", name, code, *runs, h.said())
		}
	}
}
