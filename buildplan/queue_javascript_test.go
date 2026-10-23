package buildplan_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/homeport-sh/cli/buildplan"
)

// pgBoss is a Hono app on Bun queueing with pg-boss, its scripts given.
func pgBoss(scripts string, files ...string) map[string]string {
	return js(`{"scripts":{"start":"bun src/index.ts"`+scripts+`},"dependencies":{"hono":"^4","pg-boss":"^12"}}`, append([]string{"bun.lock", "{}"}, files...)...)
}

// rscKitBoss is an rsc-kit app queueing with pg-boss, its commands given.
func rscKitBoss(files ...string) map[string]string {
	m := map[string]string{"package.json": `{"scripts":{"build":"bun --bun vite build"},"dependencies":{"@rsc-kit/core":"^0.30","pg-boss":"^12"}}`, "bun.lock": bunLock}
	for i := 0; i+1 < len(files); i += 2 {
		m[files[i]] = files[i+1]
	}
	return m
}

const command = "export default async () => {}\n"

// A JavaScript app's queue is its library's: pg-boss and Graphile Worker in
// the environment's Postgres, BullMQ and Bee-Queue in Redis (not offered
// yet). A drain is what the app names - a queue:drain script, run as its
// bundle runs a script's file, or rsc-kit's queue:drain command - or, for
// Graphile Worker, its own --once. Detection never adds a process.
func TestAJavaScriptAppsQueueIsDetected(t *testing.T) {
	for name, c := range map[string]struct {
		files map[string]string
		want  buildplan.Queue
	}{
		"pg-boss, its scripts": {pgBoss(`,"queue:drain":"bun scripts/drain.ts","queue:retry":"bun scripts/retry.ts --all","queue:work":"bun --smol scripts/work.ts"`),
			buildplan.Queue{Framework: "pg-boss", Backend: "postgres", Drain: "scripts/drain.ts", Timeout: "10m", Retry: "scripts/retry.ts --all", Worker: "--smol scripts/work.ts", Source: "detected"}},
		"pg-boss on Node": {js(`{"scripts":{"start":"node server.js","queue:drain":"node dist/drain.js"},"dependencies":{"express":"^5","pg-boss":"^12"}}`),
			buildplan.Queue{Framework: "pg-boss", Backend: "postgres", Drain: "dist/drain.js", Timeout: "10m", Source: "detected"}},
		"rsc-kit's commands": {rscKitBoss("src/commands/queue:drain.ts", command, "src/commands/queue:retry.ts", command, "src/commands/work.ts", command),
			buildplan.Queue{Framework: "pg-boss", Backend: "postgres", Drain: "queue:drain", Timeout: "10m", Retry: "queue:retry", Worker: "work", Source: "detected"}},
		"rsc-kit's queue:work over work": {rscKitBoss("src/commands/queue:drain.mts", command, "src/commands/queue:work.js", command, "src/commands/work.ts", command),
			buildplan.Queue{Framework: "pg-boss", Backend: "postgres", Drain: "queue:drain", Timeout: "10m", Worker: "queue:work", Source: "detected"}},
		"Graphile Worker": {js(`{"scripts":{"start":"node server.js"},"dependencies":{"express":"^5","graphile-worker":"^0.16"}}`),
			buildplan.Queue{Framework: "Graphile Worker", Backend: "postgres", Drain: "node_modules/graphile-worker/dist/cli.js --once", Timeout: "10m",
				Worker: "node_modules/graphile-worker/dist/cli.js", Source: "detected"}},
		"Graphile Worker, its own script": {js(`{"scripts":{"start":"node server.js","queue:drain":"node worker.js --once"},"dependencies":{"express":"^5","graphile-worker":"^0.16"}}`),
			buildplan.Queue{Framework: "Graphile Worker", Backend: "postgres", Drain: "worker.js --once", Timeout: "10m",
				Worker: "node_modules/graphile-worker/dist/cli.js", Source: "detected"}},
	} {
		p := detect(t, c.files, buildplan.Settings{})
		if p.Queue == nil || *p.Queue != c.want {
			t.Errorf("%s: %+v", name, p.Queue)
			continue
		}
		if len(p.Processes) != 0 {
			t.Errorf("%s: processes %+v", name, p.Processes)
		}
		for _, cmd := range []string{p.Queue.Drain, p.Queue.Retry, p.Queue.Worker} {
			if err := buildplan.CheckRelease(cmd); err != nil {
				t.Errorf("%s: %q isn't args the sandbox runs: %v", name, cmd, err)
			}
		}
	}

	// no drain the app names: none, and the note says how to add one
	for name, c := range map[string]struct {
		files map[string]string
		says  []string
	}{
		"pg-boss":                     {pgBoss(""), []string{"queue:drain", "@homeport/queue", "drain()"}},
		"rsc-kit":                     {rscKitBoss("src/commands/migrate.ts", command), []string{"src/commands/queue:drain.ts", "@homeport/queue"}},
		"a script that needs a shell": {pgBoss(`,"queue:drain":"bun scripts/drain.ts && echo done"`), []string{"queue:drain", "shell"}},
		"Graphile, a dev dependency": {js(`{"scripts":{"start":"node server.js"},"dependencies":{"express":"^5"},"devDependencies":{"graphile-worker":"^0.16"}}`),
			[]string{"devDependencies"}},
	} {
		p := detect(t, c.files, buildplan.Settings{})
		if p.Queue == nil || p.Queue.Drain != "" || p.Queue.Timeout != "" || p.Queue.Backend != "postgres" {
			t.Errorf("%s: %+v", name, p.Queue)
			continue
		}
		for _, want := range c.says {
			if !strings.Contains(p.Queue.Note, want) {
				t.Errorf("%s: the note doesn't say %q: %s", name, want, p.Queue.Note)
			}
		}
	}

	// pg-boss's schedules run only while a worker does: the note says so
	p := detect(t, pgBoss(`,"queue:drain":"bun scripts/drain.ts"`, "src/jobs.ts", "await boss.schedule('cleanup', '0 3 * * *')\n"), buildplan.Settings{})
	if p.Queue == nil || p.Queue.Drain != "scripts/drain.ts" || !strings.Contains(p.Queue.Note, "schedule()") || !strings.Contains(p.Queue.Note, "cron") {
		t.Errorf("schedules: %+v", p.Queue)
	}

	// Redis's: always on only, said plainly
	for lib, framework := range map[string]string{"bullmq": "BullMQ", "bee-queue": "Bee-Queue"} {
		p := detect(t, js(`{"scripts":{"start":"node server.js"},"dependencies":{"express":"^5","`+lib+`":"*"}}`), buildplan.Settings{})
		want := buildplan.Queue{Framework: framework, Backend: "redis", Source: "detected",
			Note: framework + " needs Redis, which homeport doesn't offer yet: pg-boss or Graphile Worker run in your database"}
		if p.Queue == nil || *p.Queue != want {
			t.Errorf("%s: %+v", lib, p.Queue)
		}
	}

	// none: no queue library, or a static site
	for name, files := range map[string]map[string]string{
		"express": js(`{"scripts":{"start":"node server.js"},"dependencies":{"express":"^5"}}`),
		"a site":  js(`{"scripts":{"build":"vite build"},"devDependencies":{"vite":"^7","pg-boss":"^12"}}`),
	} {
		if p := detect(t, files, buildplan.Settings{}); p.Queue != nil {
			t.Errorf("%s: %+v", name, p.Queue)
		}
	}
}

// cloudtrain's shape: an rsc-kit app on pg-boss with an always-on worker
// command (work) and no drain. It's detected as pg-boss in Postgres with no
// drain - nothing pushes it onto on demand - and its worker is offered.
func TestAnRSCKitWorkerWithoutADrainStaysAlwaysOn(t *testing.T) {
	p := detect(t, rscKitBoss("src/commands/migrate.ts", command, "src/commands/work.ts", command, "src/commands/discord-gateway.ts", command,
		"src/lib/worker/worker.ts", "await boss.schedule(CLEANUP_QUEUE, \"0 3 * * *\");\n"), buildplan.Settings{})
	q := p.Queue
	if q == nil || q.Framework != "pg-boss" || q.Backend != "postgres" || q.Drain != "" || q.Timeout != "" || q.Worker != "work" || q.Source != "detected" {
		t.Fatalf("%+v", q)
	}
	if !strings.Contains(q.Note, "schedule()") || len(p.Processes) != 0 {
		t.Errorf("%+v %+v", q, p.Processes)
	}
}

// The control plane fetches what JavaScript queue detection reads of
// rsc-kit's commands.
func TestQueueCommandFilesAreFetched(t *testing.T) {
	for _, f := range []string{"src/commands/queue:drain.ts", "src/commands/queue:retry.mjs", "src/commands/queue:work.js", "src/commands/work.ts"} {
		if !slices.Contains(buildplan.Files(), f) {
			t.Errorf("%s isn't in Files", f)
		}
	}
}
