package buildplan

import (
	_ "embed"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

// An app's background jobs. homeport never reads a queue: the framework's
// own worker does, in the backend its driver names (an add-on: the
// environment's Postgres now, Redis later). What the plan says is how to
// run that worker, two ways, chosen in Settings, not here:
//
//   - on demand: a drain, args to ./bin in a sandbox of its own, works the
//     queue until it's empty and exits, printing one line last -
//     homeport-queue: processed=<n> failed=<n> waiting=<n> next_due=<unix|0>
//     - which says when to drain again (the control plane appends
//     --max-time=<its timeout less a minute> to the command);
//   - always on: the worker as a process (paid plans), given its jobs'
//     timeout and 30s to finish them after SIGTERM.
//
// Detection fills it, and never adds or changes a process: an app with a
// worker process and on demand on would work each job twice.
// homeport.yaml's queue: says it instead (none, or a map), over detection.

// Queue is how the app's background jobs run: Framework, what was
// recognised (for people); Backend, the add-on kind its queue is in
// (postgres or redis); Drain, args to ./bin that work the queue until it's
// empty and exit, "" when it has none (always on only); Timeout, a drain's
// longest run (1m to 60m); Retry, args to ./bin that retry its failed
// jobs; Worker, args to ./bin that work it always on, with StopGrace
// (CheckStopGrace); Note, what a person should know; Source, detected or
// config (homeport.yaml).
type Queue struct {
	Framework string `json:"framework,omitempty"`
	Backend   string `json:"backend,omitempty"`
	Drain     string `json:"drain,omitempty"`
	Timeout   string `json:"timeout,omitempty"`
	Retry     string `json:"retry,omitempty"`
	Worker    string `json:"worker,omitempty"`
	StopGrace string `json:"stop_grace,omitempty"`
	Note      string `json:"note,omitempty"`
	Source    string `json:"source,omitempty"`
}

// Where a queue came from (Queue.Source).
const (
	QueueDetected = "detected"
	QueueConfig   = "config"
)

// QueueBackends are the add-on kinds a queue can be in.
var QueueBackends = []string{"postgres", "redis"}

// DefaultQueueTimeout is a drain's longest run when nothing says.
const DefaultQueueTimeout = "10m"

// A drain runs at least a minute (it gets its timeout less one, to report)
// and at most an hour.
const (
	MinQueueTimeoutSecs = 60
	MaxQueueTimeoutSecs = 60 * 60
)

// QueueTimeoutSeconds is a drain's timeout ("10m", "90s") in seconds, or
// why it isn't one.
func QueueTimeoutSeconds(v string) (int, error) {
	g := stopGraceRe.FindStringSubmatch(v) // the same shape: whole s or m
	n := 0
	if g != nil {
		n, _ = strconv.Atoi(g[1]) // at most 4 digits: no overflow
		if g[2] == "m" {
			n *= 60
		}
	}
	if n < MinQueueTimeoutSecs || n > MaxQueueTimeoutSecs {
		return 0, fmt.Errorf("queue timeout %q: whole seconds or minutes, 1m to 60m, like 10m", v)
	}
	return n, nil
}

// CheckQueue says whether q is a queue the platform can run: its backend
// one there is, its commands args to ./bin the sandbox runs, its timeout
// and its worker's grace in bounds.
func CheckQueue(q Queue) error {
	for name, v := range map[string]string{"queue framework": q.Framework, "queue backend": q.Backend, "queue drain command": q.Drain,
		"queue timeout": q.Timeout, "queue retry command": q.Retry, "queue worker": q.Worker, "queue stop grace": q.StopGrace,
		"queue note": q.Note, "queue source": q.Source} {
		if err := checkText(name, v); err != nil {
			return err
		}
	}
	if q.Backend != "" && !slices.Contains(QueueBackends, q.Backend) {
		return fmt.Errorf("queue backend %q: postgres or redis", q.Backend)
	}
	for name, v := range map[string]string{"queue drain command": q.Drain, "queue retry command": q.Retry, "queue worker": q.Worker} {
		if err := checkArgs(name, v); err != nil {
			return err
		}
	}
	if q.Timeout != "" {
		if _, err := QueueTimeoutSeconds(q.Timeout); err != nil {
			return err
		}
	}
	return CheckStopGrace("the queue's worker", q.StopGrace)
}

// queueConfig is homeport.yaml's queue: none, or what it says of it.
type queueConfig struct {
	none                           bool
	Backend, Drain, Timeout, Retry string
}

// UnmarshalYAML: queue: none (or ""), or a map of backend, drain, timeout
// and retry - nothing else, so a misspelt key isn't quietly ignored.
func (q *queueConfig) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		if v := strings.TrimSpace(n.Value); v == "" || v == "none" {
			q.none = true
			return nil
		}
		return fmt.Errorf("queue: %q: none, or a map of backend, drain, timeout and retry", n.Value)
	}
	if n.Kind != yaml.MappingNode {
		return errors.New("queue: none, or a map of backend, drain, timeout and retry")
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		var v *string
		switch k := n.Content[i].Value; k {
		case "backend":
			v = &q.Backend
		case "drain":
			v = &q.Drain
		case "timeout":
			v = &q.Timeout
		case "retry":
			v = &q.Retry
		default:
			return fmt.Errorf("queue: %q isn't one of its settings: backend, drain, timeout or retry", k)
		}
		if err := n.Content[i+1].Decode(v); err != nil {
			return fmt.Errorf("queue: %s: %w", n.Content[i].Value, err)
		}
	}
	return nil
}

// over is the queue homeport.yaml says, over the one detected (nil when
// none was): what it says, the rest detection's; on postgres, and drained
// for DefaultQueueTimeout, when neither says.
func (c *queueConfig) over(detected *Queue) (*Queue, error) {
	if c.none {
		return nil, nil
	}
	var q Queue
	if detected != nil {
		q = *detected
	}
	for _, f := range []struct {
		to *string
		v  string
	}{{&q.Backend, c.Backend}, {&q.Drain, c.Drain}, {&q.Timeout, c.Timeout}, {&q.Retry, c.Retry}} {
		if v := strings.TrimSpace(f.v); v != "" {
			*f.to = v
		}
	}
	if q.Backend == "" {
		q.Backend = "postgres"
	}
	if q.Drain != "" && q.Timeout == "" {
		q.Timeout = DefaultQueueTimeout
	}
	q.Source = QueueConfig
	if err := CheckQueue(q); err != nil {
		return nil, err
	}
	return &q, nil
}

// Laravel's queue. The database driver (Laravel's default since 11) keeps
// jobs in the app's own database: a drain is the runner below; always on,
// queue:work, with Laravel's job timeout (60s) and 30s to finish it.
// Horizon supervises workers in Redis: always on only. homeport/laravel
// kicks a drain when a job is queued (paid plans); without it, jobs wait
// for the next one, and the note says so.
const (
	queuePHPF          = ".homeport/queue.php"
	laravelQueueDrain  = "php-cli " + queuePHPF + " ./bin"
	laravelQueuePlain  = "php-cli artisan queue:work --stop-when-empty"
	laravelQueueRetry  = "php-cli artisan queue:retry all"
	laravelQueueWorker = "php-cli artisan queue:work --tries=3 --timeout=60"
	laravelHorizon     = "php-cli artisan horizon"
	laravelQueueGrace  = "90s"
	laravelKick        = "homeport/laravel"
	kickNote           = "Jobs wait for the next drain: `composer require " + laravelKick + "` runs them within seconds on paid plans."
	horizonNote        = "Horizon needs Redis, which homeport doesn't offer yet: run it always on with a Redis URL of your own as a variable, or use Laravel's database queue driver, which runs in your database"
)

// The queue runner, put in every Laravel bundle the build makes: it works
// the queue in the app's own process and prints the report line (embed/queue.php).
//
//go:embed embed/queue.php
var queuePHP string

// QueueRunnerFile is where a Laravel bundle has the queue runner, as its
// drain names it.
const QueueRunnerFile = queuePHPF

// QueueRunnerPHP is the queue runner's source, for a drain run outside a
// bundle (homeport queue drain).
func QueueRunnerPHP() string { return queuePHP }

// queueRunner puts the queue runner in the bundle b.
func queueRunner(b string) string {
	return join("mkdir -p "+b+"/.homeport",
		"printf '%s' '"+base64.StdEncoding.EncodeToString([]byte(queuePHP))+"' | base64 -d > "+b+"/"+queuePHPF)
}

var (
	// a class that's queued: a job, mail, notification or listener
	shouldQueueRe = regexp.MustCompile(`\bShouldQueue(AfterCommit)?\b`)
	// the jobs table's migration
	jobsTableRe = regexp.MustCompile(`Schema::create\(\s*['"]jobs['"]`)
)

// laravelQueue is a Laravel app's queue, if it has one: it requires
// Horizon, or something in app/ is queued, or it migrates the jobs table.
// runner: the bundle has the queue runner (homeport's build made it).
func (r reader) laravelQueue(runner bool) *Queue {
	if r.requires("laravel/horizon") {
		return &Queue{Framework: "Laravel Horizon", Backend: "redis", Retry: laravelQueueRetry, Worker: laravelHorizon,
			StopGrace: laravelQueueGrace, Note: horizonNote, Source: QueueDetected}
	}
	seen := r.anyPHP("app", func(name string, b []byte) bool { return shouldQueueRe.Match(b) }) ||
		r.anyPHP("database/migrations", func(name string, b []byte) bool {
			return strings.HasSuffix(name, "_create_jobs_table.php") || jobsTableRe.Match(b)
		})
	if !seen {
		return nil
	}
	drain := laravelQueuePlain // no runner: drained, without the report line
	if runner {
		drain = laravelQueueDrain
	}
	note := ""
	if !r.requires(laravelKick) {
		note = kickNote
	}
	return &Queue{Framework: "Laravel", Backend: "postgres", Drain: drain, Timeout: DefaultQueueTimeout, Retry: laravelQueueRetry,
		Worker: laravelQueueWorker, StopGrace: laravelQueueGrace, Note: note, Source: QueueDetected}
}

// maxScanned bounds the files a scan of a folder reads.
const maxScanned = 5000

// anyPHP says whether a .php file under dir is one match says is (anyFile).
func (r reader) anyPHP(dir string, match func(name string, b []byte) bool) bool {
	return r.anyFile(dir, []string{".php"}, match)
}

// anyFile says whether a file under dir with one of exts (regular files
// only, never through a link) is one match says is.
func (r reader) anyFile(dir string, exts []string, match func(name string, b []byte) bool) bool {
	// each folder down to it a folder, not a link (WalkDir follows its root)
	d := ""
	for _, part := range strings.Split(dir, "/") {
		d = path.Join(d, part)
		if st, err := lstat(r.fsys, r.name(d)); err != nil || !st.IsDir() {
			return false
		}
	}
	found, n := false, 0
	_ = fs.WalkDir(r.fsys, r.name(dir), func(p string, d fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return fs.SkipDir
		case found || n >= maxScanned:
			return fs.SkipAll
		case d.IsDir() || !d.Type().IsRegular() || !slices.Contains(exts, path.Ext(p)):
			return nil
		}
		n++
		rel := p
		if r.root != "." {
			rel = strings.TrimPrefix(p, r.root+"/")
		}
		if b, err := r.read(rel); err == nil && match(d.Name(), b) {
			found = true
		}
		return nil
	})
	return found
}

// A JavaScript app's queue is its library's. pg-boss and Graphile Worker
// keep jobs in the environment's Postgres; BullMQ and Bee-Queue in Redis,
// which isn't offered yet. A drain is what the app names: its queue:drain
// script, run as the bundle runs a script's file, or rsc-kit's queue:drain
// command (src/commands/queue:drain.ts, ./app queue:drain) - pg-boss has no
// drain of its own, and @homeport-sh/queue's drain() is one - or Graphile
// Worker's own --once. queue:retry and queue:work (rsc-kit: work, too) are
// its retry and worker the same way.
const (
	queueDrainName = "queue:drain"
	queueRetryName = "queue:retry"
	queueWorkName  = "queue:work"
	rscKitWorkName = "work"
	graphileCLI    = "node_modules/graphile-worker/dist/cli.js"
	redisQueueNote = " needs Redis, which homeport doesn't offer yet: pg-boss or Graphile Worker run in your database"
	scheduleNote   = "pg-boss runs schedule() jobs only while a worker runs: drained on demand, move them to cron jobs."
)

// queueUse: a queue script, run as a drain; Graphile Worker's command by
// its file.
var queueUse = scriptUse{"queue drain", "drain.js", map[string]string{"graphile-worker": "dist/cli.js"}}

// queueFiles are the files JavaScript queue detection reads (Files).
func queueFiles() []string {
	return RSCKitCommandFiles(queueDrainName, queueRetryName, queueWorkName, rscKitWorkName)
}

// a pg-boss schedule: boss.schedule(name, cron)
var scheduleRe = regexp.MustCompile(`\.schedule\(`)

// jsQueue is a JavaScript app's queue, if its dependencies have a queue
// library.
func (r reader) jsQueue(p *Plan) *Queue {
	deps := r.deps()
	q := Queue{Backend: "postgres", Source: QueueDetected}
	switch {
	case deps["pg-boss"]:
		q.Framework = "pg-boss"
	case deps["graphile-worker"]:
		q.Framework = "Graphile Worker"
	case deps["bullmq"]:
		return &Queue{Framework: "BullMQ", Backend: "redis", Note: "BullMQ" + redisQueueNote, Source: QueueDetected}
	case deps["bee-queue"]:
		return &Queue{Framework: "Bee-Queue", Backend: "redis", Note: "Bee-Queue" + redisQueueNote, Source: QueueDetected}
	default:
		return nil
	}
	var notes []string
	if p.rscKitApp {
		// ./app is the server, whoever builds it: a command is all it runs
		q.Drain, q.Retry = r.rscKitCommand(queueDrainName), r.rscKitCommand(queueRetryName)
		if q.Worker = r.rscKitCommand(queueWorkName); q.Worker == "" {
			q.Worker = r.rscKitCommand(rscKitWorkName)
		}
		if q.Drain == "" {
			notes = append(notes, "To drain the queue on demand, add src/commands/queue:drain.ts that works it with @homeport-sh/queue's drain(); "+
				"until then, run its worker always on.")
		}
	} else {
		pkg := r.pkg()
		for _, s := range []struct {
			name string
			to   *string
		}{{queueDrainName, &q.Drain}, {queueRetryName, &q.Retry}, {queueWorkName, &q.Worker}} {
			if pkg.Scripts[s.name] == "" {
				continue
			}
			args, _, warning := r.scriptArgs(p, pkg, s.name, queueUse)
			*s.to = args
			if warning != "" {
				notes = append(notes, warning+".")
			}
		}
		if q.Framework == "Graphile Worker" {
			switch {
			case !r.prodDeps()["graphile-worker"]:
				notes = append(notes, "graphile-worker is in devDependencies, and the bundle ships dependencies only: move it to dependencies for homeport to run it.")
			case q.Drain == "" && pkg.Scripts[queueDrainName] == "":
				q.Drain = graphileCLI + " --once"
			}
			if q.Worker == "" && r.prodDeps()["graphile-worker"] {
				q.Worker = graphileCLI
			}
		}
		if q.Drain == "" && pkg.Scripts[queueDrainName] == "" && q.Framework == "pg-boss" {
			notes = append(notes, "To drain the queue on demand, add a queue:drain script that works it with @homeport-sh/queue's drain(); "+
				"until then, run its worker always on.")
		}
	}
	if q.Framework == "pg-boss" && r.anyFile("src", []string{".ts", ".mts", ".cts", ".tsx", ".js", ".mjs", ".cjs", ".jsx"},
		func(_ string, b []byte) bool { return scheduleRe.Match(b) }) {
		notes = append(notes, scheduleNote)
	}
	if q.Drain != "" {
		q.Timeout = DefaultQueueTimeout
	}
	q.Note = strings.Join(notes, " ")
	return &q
}

// rscKitCommand is the rsc-kit command name, if the app has it.
func (r reader) rscKitCommand(name string) string {
	for _, f := range RSCKitCommandFiles(name) {
		if r.exists(f) {
			return name
		}
	}
	return ""
}
