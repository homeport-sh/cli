package buildplan_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/homeport-sh/cli/buildplan"
)

const (
	shouldQueue = "<?php\nnamespace App\\Jobs;\n\nuse Illuminate\\Contracts\\Queue\\ShouldQueue;\n\nclass SendInvoice implements ShouldQueue {}\n"
	jobsTable   = "<?php\nreturn new class extends Migration {\n    public function up(): void\n    {\n        Schema::create('jobs', function (Blueprint $table) {});\n    }\n};\n"
)

// laravelQueue is a Laravel app's queue as detection says it: the database
// driver, in the environment's Postgres, drained on demand by the runner
// (.homeport/queue.php, which prints the report line), or worked always on
// by queue:work, given Laravel's job timeout and 30s to finish.
var laravelQueue = buildplan.Queue{Framework: "Laravel", Backend: "postgres", Drain: "php-cli .homeport/queue.php ./bin", Timeout: "10m",
	Retry: "php-cli artisan queue:retry all", Worker: "php-cli artisan queue:work --tries=3 --timeout=60", StopGrace: "90s", Source: "detected"}

// A Laravel app queues work when a class in app/ implements ShouldQueue (a
// job, a mail, a notification, a listener), or it has the jobs table's
// migration. Detection says so in the plan, and puts the runner in the
// bundle; it never adds a process: on demand or always on is the person's
// choice, in Settings.
func TestALaravelAppsQueueIsDetected(t *testing.T) {
	for name, files := range map[string]map[string]string{
		"ShouldQueue":     laravel(nil, "app/Jobs/SendInvoice.php", shouldQueue),
		"a mail":          laravel(nil, "app/Mail/Deep/Welcome.php", "<?php\nclass Welcome extends Mailable implements \\Illuminate\\Contracts\\Queue\\ShouldQueue {}\n"),
		"jobs migration":  laravel(nil, "database/migrations/0001_01_01_000002_create_jobs_table.php", jobsTable),
		"jobs, renamed":   laravel(nil, "database/migrations/2026_01_01_000000_queue.php", jobsTable),
		"jobs, by name":   laravel(nil, "database/migrations/2024_05_01_000000_create_jobs_table.php", "<?php\n"),
		"double-quoted":   laravel(nil, "database/migrations/x.php", `<?php Schema::create("jobs", fn ($t) => null);`),
		"with processes":  laravel(nil, "app/Jobs/SendInvoice.php", shouldQueue, "homeport.yaml", "processes:\n  worker: php-cli artisan queue:work\n"),
		"Octane, Reverb":  laravel([]string{"laravel/octane", "laravel/reverb"}, "app/Jobs/SendInvoice.php", shouldQueue),
		"the jobs, first": laravel(nil, "app/Jobs/SendInvoice.php", shouldQueue, "database/migrations/x.php", jobsTable),
	} {
		p := detect(t, files, buildplan.Settings{})
		if p.Queue == nil || *p.Queue != laravelQueue {
			t.Errorf("%s: %+v", name, p.Queue)
			continue
		}
		if !strings.Contains(p.Command, "/.homeport/queue.php") {
			t.Errorf("%s: the runner isn't in the bundle: %s", name, p.Command)
		}
		if err := buildplan.CheckRelease(p.Queue.Drain); err != nil {
			t.Errorf("%s: the drain isn't args the sandbox runs: %v", name, err)
		}
	}
	// an app in a folder of the repository: its own app/
	sub := map[string]string{}
	for f, body := range laravel(nil, "app/Jobs/SendInvoice.php", shouldQueue) {
		sub["api/"+f] = body
	}
	if p := detect(t, sub, buildplan.Settings{Root: "api"}); p.Queue == nil || *p.Queue != laravelQueue {
		t.Errorf("in a folder: %+v", p.Queue)
	}

	// detection adds no process, and leaves the app's alone
	p := detect(t, laravel(nil, "app/Jobs/SendInvoice.php", shouldQueue), buildplan.Settings{})
	if len(p.Processes) != 0 {
		t.Errorf("processes: %+v", p.Processes)
	}
	p = detect(t, laravel(nil, "app/Jobs/SendInvoice.php", shouldQueue, "homeport.yaml", "processes:\n  worker: php-cli artisan queue:work\n"), buildplan.Settings{})
	if len(p.Processes) != 1 || !strings.HasSuffix(p.Processes[0].Run, " php-cli artisan queue:work") {
		t.Errorf("the app's processes: %+v", p.Processes)
	}

	// no queue: nothing said, though the runner ships (homeport.yaml may
	// name it)
	for name, files := range map[string]map[string]string{
		"plain":            laravel(nil),
		"a word":           laravel(nil, "app/Models/User.php", "<?php // NotShouldQueueable\nclass User {}\n"),
		"another table":    laravel(nil, "database/migrations/x.php", "<?php Schema::create('job_batches', fn ($t) => null);"),
		"outside app/":     laravel(nil, "tests/Jobs/X.php", shouldQueue),
		"not PHP":          laravel(nil, "app/Jobs/notes.md", shouldQueue),
		"not Laravel":      {"composer.json": `{"require":{"slim/slim":"^4"}}`, "composer.lock": "{}", "app/Jobs/X.php": shouldQueue},
		"a static Laravel": laravel(nil, "app/Jobs/X.php", shouldQueue, "homeport.yaml", "static: public\n"),
	} {
		if p := detect(t, files, buildplan.Settings{}); p.Queue != nil {
			t.Errorf("%s: %+v", name, p.Queue)
		}
	}
	if p := detect(t, laravel(nil), buildplan.Settings{}); !strings.Contains(p.Command, "/.homeport/queue.php") {
		t.Errorf("the runner ships with every Laravel bundle: %s", p.Command)
	}

	// a build a person wrote has no runner in its bundle: queue:work drains
	// it, without the report line
	p = detect(t, laravel(nil, "app/Jobs/X.php", shouldQueue), buildplan.Settings{Command: "frankenphp-bundle . .homeport-bundle"})
	if p.Queue == nil || p.Queue.Drain != "php-cli artisan queue:work --stop-when-empty" || strings.Contains(p.Command, "queue.php") {
		t.Errorf("a person's build: %+v %s", p.Queue, p.Command)
	}
}

// Horizon supervises its workers, in Redis: it has no drain, so it runs
// always on, and the plan says Redis isn't offered yet.
func TestLaravelHorizonIsAlwaysOnInRedis(t *testing.T) {
	p := detect(t, laravel([]string{"laravel/horizon"}, "app/Jobs/X.php", shouldQueue), buildplan.Settings{})
	q := p.Queue
	if q == nil || q.Framework != "Laravel Horizon" || q.Backend != "redis" || q.Drain != "" || q.Timeout != "" || q.Worker != "php-cli artisan horizon" ||
		q.StopGrace != "90s" || q.Retry != "php-cli artisan queue:retry all" || q.Source != "detected" || !strings.Contains(q.Note, "Redis") {
		t.Fatalf("horizon: %+v", q)
	}
	if len(p.Processes) != 0 {
		t.Errorf("processes: %+v", p.Processes)
	}
	// it requires Horizon: that's its queue, jobs or none
	if p := detect(t, laravel([]string{"laravel/horizon"}), buildplan.Settings{}); p.Queue == nil || p.Queue.Framework != "Laravel Horizon" {
		t.Errorf("horizon, no jobs seen: %+v", p.Queue)
	}
}

// homeport.yaml's queue: none turns detection off, as release: none does;
// a map says the queue's backend, drain, timeout and retry, over what was
// detected, checked as the sandbox runs them.
func TestHomeportYAMLSaysTheQueue(t *testing.T) {
	job := []string{"app/Jobs/X.php", shouldQueue}
	for _, none := range []string{"queue: none\n", "queue: \"\"\n", "queue: \"none\"\n"} {
		if p := detect(t, laravel(nil, append(job, "homeport.yaml", none)...), buildplan.Settings{}); p.Queue != nil {
			t.Errorf("%q: %+v", none, p.Queue)
		}
	}

	// over what was detected: what it says, the rest detection's
	p := detect(t, laravel(nil, append(job, "homeport.yaml", "queue:\n  timeout: 20m\n  drain: php-cli artisan queue:work --stop-when-empty --queue=high,default\n")...), buildplan.Settings{})
	want := laravelQueue
	want.Timeout, want.Drain, want.Source = "20m", "php-cli artisan queue:work --stop-when-empty --queue=high,default", "config"
	if p.Queue == nil || *p.Queue != want {
		t.Errorf("over detection: %+v", p.Queue)
	}

	// nothing detected: what it says, its timeout 10m when it drains
	p = detect(t, map[string]string{"go.mod": "module x\n\ngo 1.26\n", "homeport.yaml": "queue:\n  backend: postgres\n  drain: drain --once\n  retry: retry-failed\n"}, buildplan.Settings{})
	if p.Queue == nil || *p.Queue != (buildplan.Queue{Backend: "postgres", Drain: "drain --once", Timeout: "10m", Retry: "retry-failed", Source: "config"}) {
		t.Errorf("a Go app's: %+v", p.Queue)
	}
	p = detect(t, map[string]string{"go.mod": "module x\n\ngo 1.26\n", "homeport.yaml": "queue:\n  backend: redis\n"}, buildplan.Settings{})
	if p.Queue == nil || *p.Queue != (buildplan.Queue{Backend: "redis", Source: "config"}) {
		t.Errorf("a backend alone: %+v", p.Queue)
	}
	// a static site runs nothing
	if p := detect(t, map[string]string{"index.html": "<h1>hi</h1>", "homeport.yaml": "queue:\n  drain: drain\n"}, buildplan.Settings{}); p.Queue != nil {
		t.Errorf("static: %+v", p.Queue)
	}

	// what the sandbox can't run, or the platform can't give, is refused
	for yaml, says := range map[string]string{
		"queue:\n  timeout: 30s\n  drain: d\n":        "1m to 60m",
		"queue:\n  timeout: 61m\n  drain: d\n":        "1m to 60m",
		"queue:\n  timeout: 1h\n  drain: d\n":         "1m to 60m",
		"queue:\n  timeout: 10\n  drain: d\n":         "1m to 60m",
		"queue:\n  backend: mysql\n":                  "postgres or redis",
		"queue:\n  drain: ./bin artisan queue:work\n": "args to the app's binary",
		"queue:\n  drain: work && echo done\n":        "without a shell",
		"queue:\n  drain: work --port=$PORT\n":        "without a shell",
		"queue:\n  retry: retry; rm -rf /\n":          "without a shell",
		"queue: sometimes\n":                          "none, or",
		"queue:\n  drian: d\n":                        "drian",
	} {
		_, err := buildplan.Detect(repo(laravel(nil, append(job, "homeport.yaml", yaml)...)), buildplan.Settings{})
		if err == nil || !strings.Contains(err.Error(), says) || !strings.Contains(err.Error(), "queue") {
			t.Errorf("%q: %v", yaml, err)
		}
	}
	for _, ok := range []string{"1m", "60m", "90s", "3600s", "15m"} {
		if _, err := buildplan.Detect(repo(laravel(nil, append(job, "homeport.yaml", "queue:\n  timeout: "+ok+"\n")...)), buildplan.Settings{}); err != nil {
			t.Errorf("%s refused: %v", ok, err)
		}
	}
}

// The plan's queue is JSON for the control plane, in the shape it reads.
func TestThePlansQueueIsItsJSON(t *testing.T) {
	b := planJSON(t, detect(t, laravel([]string{"laravel/horizon"}), buildplan.Settings{}))
	for _, want := range []string{`"queue":{"framework":"Laravel Horizon","backend":"redis","retry":"php-cli artisan queue:retry all","worker":"php-cli artisan horizon","stop_grace":"90s","note":`, `"source":"detected"}`} {
		if !strings.Contains(b, want) {
			t.Errorf("no %s in %s", want, b)
		}
	}
	if b := planJSON(t, detect(t, laravel(nil), buildplan.Settings{})); strings.Contains(b, `"queue"`) {
		t.Errorf("none: %s", b)
	}
}

func planJSON(t *testing.T, p buildplan.Plan) string {
	t.Helper()
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// The scan reads the app's own files: never through a link, which on a
// builder could point anywhere.
func TestTheQueueScanFollowsNoLinks(t *testing.T) {
	outside := t.TempDir()
	if err := os.MkdirAll(filepath.Join(outside, "Jobs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "Jobs", "X.php"), []byte(shouldQueue), 0o644); err != nil {
		t.Fatal(err)
	}
	for name, link := range map[string]string{"app": "app", "a folder in app": "app/Jobs", "database": "database", "a file": "app/X.php"} {
		dir := t.TempDir()
		for f, body := range laravel(nil, "database/migrations/.keep", "") {
			if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, f)), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, f), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		target := outside
		switch link {
		case "app/Jobs":
			target = filepath.Join(outside, "Jobs")
		case "database":
			target = filepath.Join(t.TempDir(), "database")
			if err := os.MkdirAll(filepath.Join(target, "migrations"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(target, "migrations", "x.php"), []byte(jobsTable), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.RemoveAll(filepath.Join(dir, "database")); err != nil {
				t.Fatal(err)
			}
		case "app/X.php":
			target = filepath.Join(outside, "Jobs", "X.php")
		}
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, link)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(dir, link)); err != nil {
			t.Fatal(err)
		}
		p, err := buildplan.Detect(os.DirFS(dir), buildplan.Settings{})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if p.Queue != nil {
			t.Errorf("%s: read through a link: %+v", name, p.Queue)
		}
	}
}
