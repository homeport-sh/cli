package buildplan

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// fakeLaravel stands in for a Laravel app, as the queue runner boots one:
// vendor/autoload.php, and bootstrap/app.php returning its container. Its
// queue:work fires the events $RUNS scripts - runs split by ;, jobs by ,
// (ok processed, fail failed) - and returns $STATUS; after run i the jobs
// table has $READY[i] jobs ready and the earliest later one due at $DUE.
// What it's asked is logged to calls: each queue:work's options, each
// query's bindings.
const fakeLaravel = `<?php
final class FakeApp {
    public array $listeners = [];
    public int $run = 0;
    public function make(string $id) {
        $app = $this;
        return match ($id) {
            'Illuminate\Contracts\Console\Kernel' => new class($app) {
                public function __construct(private FakeApp $app) {}
                public function bootstrap(): void {}
                public function call(string $cmd, array $params = [], $out = null): int {
                    file_put_contents('calls', $cmd.' '.json_encode($params)."\n", FILE_APPEND);
                    $runs = explode(';', getenv('RUNS') ?: '');
                    foreach (array_filter(explode(',', $runs[$this->app->run] ?? '')) as $job) {
                        $event = $job === 'ok' ? 'Illuminate\Queue\Events\JobProcessed' : 'Illuminate\Queue\Events\JobFailed';
                        foreach ($this->app->listeners[$event] ?? [] as $fn) { $fn(); }
                    }
                    $this->app->run++;
                    return (int) (getenv('STATUS') ?: 0);
                }
            },
            'events' => new class($app) {
                public function __construct(private FakeApp $app) {}
                public function listen(string $event, callable $fn): void { $this->app->listeners[$event][] = $fn; }
            },
            'config' => new class {
                public function get(string $key) {
                    return match ($key) {
                        'queue.default' => getenv('DRIVER') ?: 'database',
                        'queue.connections.database' => ['driver' => 'database', 'table' => 'jobs', 'queue' => 'default', 'retry_after' => 90],
                        'queue.connections.redis' => ['driver' => 'redis', 'queue' => 'default'],
                        default => null,
                    };
                }
            },
            'db' => new class($app) {
                public function __construct(private FakeApp $app) {}
                public function connection($name = null) {
                    $app = $this->app;
                    return new class($app) {
                        public function __construct(private FakeApp $app) {}
                        public function getTablePrefix(): string { return 'p_'; }
                        public function selectOne(string $sql, array $bindings) {
                            file_put_contents('calls', 'sql '.(str_contains($sql, 'from p_jobs') ? 'p_jobs ' : '').json_encode($bindings)."\n", FILE_APPEND);
                            if (str_contains($sql, 'count(')) {
                                $ready = explode(';', getenv('READY') ?: '0');
                                return (object) ['n' => (string) ($ready[$this->app->run - 1] ?? end($ready))];
                            }
                            return (object) ['t' => getenv('DUE') ?: null];
                        }
                    };
                }
            },
        };
    }
}
return new FakeApp;
`

// The queue runner (.homeport/queue.php), run as a drain runs it - ./bin
// php-cli .homeport/queue.php ./bin --max-time=<n> - here with the PHP on
// PATH and a fake Laravel app. It works the queue in the app's own process
// (queue:work --stop-when-empty --max-time, the time it has left), works it
// again while jobs are ready and time is left, and prints the report line:
// what it processed and failed, counted from the queue's events, and for
// the database driver what's ready now and when the earliest later job is
// due. It exits as queue:work did.
func TestTheQueueRunnerDrainsAndReports(t *testing.T) {
	php, err := exec.LookPath("php")
	if err != nil {
		t.Skip("no php here")
	}
	type result struct {
		stdout, calls string
		code          int
	}
	run := func(t *testing.T, env []string, args ...string) result {
		t.Helper()
		dir := t.TempDir()
		write(t, dir, queuePHPF, queuePHP, 0o644)
		write(t, dir, "vendor/autoload.php", "<?php\n", 0o644)
		write(t, dir, "bootstrap/app.php", fakeLaravel, 0o644)
		cmd := exec.Command(php, append([]string{queuePHPF, "./bin"}, args...)...)
		cmd.Dir, cmd.Env = dir, append(os.Environ(), env...)
		var out, e strings.Builder
		cmd.Stdout, cmd.Stderr = &out, &e
		code := 0
		if err := cmd.Run(); err != nil {
			x, ok := err.(*exec.ExitError)
			if !ok {
				t.Fatal(err)
			}
			code = x.ExitCode()
		}
		if e.Len() > 0 {
			t.Logf("stderr: %s", e.String())
		}
		b, _ := os.ReadFile(filepath.Join(dir, "calls"))
		return result{out.String(), string(b), code}
	}
	works := regexp.MustCompile(`(?m)^queue:work (.*)$`)
	maxTime := func(t *testing.T, call string) int {
		t.Helper()
		m := regexp.MustCompile(`"--max-time":(\d+)`).FindStringSubmatch(call)
		if m == nil || !strings.Contains(call, `"--stop-when-empty":true`) {
			t.Fatalf("not a drain: %s", call)
		}
		n, _ := strconv.Atoi(m[1])
		return n
	}
	lastLine := func(s string) string {
		lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
		return lines[len(lines)-1]
	}

	// one run: two processed, one failed; nothing ready, the next due later
	r := run(t, []string{"RUNS=ok,ok,fail", "READY=0", "DUE=1793481600"}, "--max-time=600")
	if lastLine(r.stdout) != "homeport-queue: processed=2 failed=1 waiting=0 next_due=1793481600" || r.code != 0 {
		t.Fatalf("one run: %q %d\n%s", r.stdout, r.code, r.calls)
	}
	w := works.FindAllStringSubmatch(r.calls, -1)
	if len(w) != 1 {
		t.Fatalf("runs: %s", r.calls)
	}
	if n := maxTime(t, w[0][1]); n < 590 || n > 600 {
		t.Errorf("max time: %d", n)
	}
	// the queries read the app's table (its prefix) and queue
	if !strings.Contains(r.calls, `sql p_jobs ["default",`) {
		t.Errorf("queries: %s", r.calls)
	}

	// no --max-time: 540s (a 10m drain, less a minute to report)
	r = run(t, []string{"RUNS=ok"})
	if w := works.FindAllStringSubmatch(r.calls, -1); len(w) != 1 || maxTime(t, w[0][1]) > 540 || maxTime(t, w[0][1]) < 530 {
		t.Errorf("default max time: %s", r.calls)
	}
	if lastLine(r.stdout) != "homeport-queue: processed=1 failed=0 waiting=0 next_due=0" {
		t.Errorf("none due: %q", r.stdout)
	}

	// jobs ready after a run (queued while it worked): it works again
	r = run(t, []string{"RUNS=ok;ok,ok,fail", "READY=3;0"}, "--max-time=600")
	if len(works.FindAllString(r.calls, -1)) != 2 || lastLine(r.stdout) != "homeport-queue: processed=3 failed=1 waiting=0 next_due=0" {
		t.Errorf("again: %q\n%s", r.stdout, r.calls)
	}
	// ... but not once a run gets nothing done: it says what's waiting
	r = run(t, []string{"RUNS=ok;", "READY=3;3"}, "--max-time=600")
	if len(works.FindAllString(r.calls, -1)) != 2 || lastLine(r.stdout) != "homeport-queue: processed=1 failed=0 waiting=3 next_due=0" {
		t.Errorf("stuck: %q\n%s", r.stdout, r.calls)
	}

	// queue:work's failure is the drain's, after its line; it isn't run again
	r = run(t, []string{"RUNS=ok", "READY=2", "STATUS=12"}, "--max-time=600")
	if r.code != 12 || len(works.FindAllString(r.calls, -1)) != 1 || lastLine(r.stdout) != "homeport-queue: processed=1 failed=0 waiting=2 next_due=0" {
		t.Errorf("failed: %q %d\n%s", r.stdout, r.code, r.calls)
	}

	// another driver (Redis): the queue isn't read, waiting and due are 0
	r = run(t, []string{"RUNS=ok", "DRIVER=redis", "READY=5"}, "--max-time=600")
	if strings.Contains(r.calls, "sql ") || lastLine(r.stdout) != "homeport-queue: processed=1 failed=0 waiting=0 next_due=0" {
		t.Errorf("redis: %q\n%s", r.stdout, r.calls)
	}

	// the worker's options pass through: the queues it works are the ones
	// counted
	r = run(t, []string{"RUNS=ok", "READY=0"}, "--max-time=600", "--queue=high,default", "--tries=3")
	if w := works.FindAllStringSubmatch(r.calls, -1); len(w) != 1 || !strings.Contains(w[0][1], `"--queue":"high,default"`) || !strings.Contains(w[0][1], `"--tries":"3"`) {
		t.Errorf("options: %s", r.calls)
	}
	if !strings.Contains(r.calls, `sql p_jobs ["high","default",`) {
		t.Errorf("queues counted: %s", r.calls)
	}
	// what isn't an option is refused, before anything boots
	if r := run(t, nil, "--max-time=600", "rm"); r.code == 0 || r.calls != "" {
		t.Errorf("not an option: %d %s", r.code, r.calls)
	}
}
