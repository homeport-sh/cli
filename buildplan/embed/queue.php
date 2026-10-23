<?php

// homeport: a drain of a Laravel app's queue, run in a sandbox of its own
// when there may be work, never kept running:
//
//   ./bin php-cli .homeport/queue.php ./bin --max-time=<seconds> [queue:work's options]
//
// It boots the app once and runs artisan queue:work --stop-when-empty in
// its process, for at most --max-time seconds (540 by default: a 10-minute
// drain, less a minute to report), and again while jobs are ready and time
// is left (queued while it worked). Then it prints one line, last, which
// says when to drain again:
//
//   homeport-queue: processed=<n> failed=<n> waiting=<n> next_due=<unix|0>
//
// processed and failed are the queue's own events (JobProcessed,
// JobFailed); waiting is the jobs ready now and next_due the earliest one
// due later (a delayed job, a retry's backoff, a reservation that lapses),
// read from the jobs table for the database driver - 0 for any other. It
// exits as queue:work did.

$maxTime = 540;
$options = [];
foreach (array_slice($argv, 2) as $arg) {
    if (!preg_match('/^--([a-z][a-z-]*)(?:=(.*))?$/', $arg, $m)) {
        fwrite(STDERR, "homeport: the queue runner takes queue:work's options (--name or --name=value), not ".$arg."\n");
        exit(2);
    }
    if ($m[1] === 'max-time') {
        $maxTime = max(1, (int) ($m[2] ?? 0));
    } else {
        $options['--'.$m[1]] = $m[2] ?? true;
    }
}
$started = microtime(true);

require getcwd().'/vendor/autoload.php';
$app = require getcwd().'/bootstrap/app.php';
$kernel = $app->make('Illuminate\Contracts\Console\Kernel');
$kernel->bootstrap();

$processed = $failed = 0;
$events = $app->make('events');
$events->listen('Illuminate\Queue\Events\JobProcessed', function () use (&$processed) {
    $processed++;
});
$events->listen('Illuminate\Queue\Events\JobFailed', function () use (&$failed) {
    $failed++;
});

// [ready now, the earliest due later] of the database driver's jobs table
$pending = function () use ($app, $options): array {
    $config = $app->make('config');
    $name = $config->get('queue.default');
    $c = $config->get('queue.connections.'.$name);
    if (!is_array($c) || ($c['driver'] ?? '') !== 'database') {
        return [0, 0];
    }
    try {
        $db = $app->make('db')->connection($c['connection'] ?? null);
        $table = $db->getTablePrefix().($c['table'] ?? 'jobs');
        $queues = explode(',', (string) ($options['--queue'] ?? $c['queue'] ?? 'default'));
        $in = implode(',', array_fill(0, count($queues), '?'));
        $now = time();
        $retryAfter = (int) ($c['retry_after'] ?? 90);
        // a reserved job is free again once its reservation lapses
        $ready = $db->selectOne("select count(*) as n from $table where queue in ($in) and available_at <= ? and (reserved_at is null or reserved_at <= ?)",
            array_merge($queues, [$now, $now - $retryAfter]));
        $due = $db->selectOne("select min(case when reserved_at is null then available_at else reserved_at + ? end) as t from $table".
            " where queue in ($in) and ((reserved_at is null and available_at > ?) or reserved_at > ?)",
            array_merge([$retryAfter], $queues, [$now, $now - $retryAfter]));
        return [(int) ($ready->n ?? 0), (int) ($due->t ?? 0)];
    } catch (Throwable $e) {
        fwrite(STDERR, "homeport: couldn't read what's left in the queue: ".$e->getMessage()."\n");
        return [0, 0];
    }
};

// the line, last, however it ends (a worker that exits itself, too)
$left = null;
register_shutdown_function(function () use (&$processed, &$failed, &$left, $pending) {
    [$waiting, $due] = $left ?? $pending();
    fwrite(STDOUT, sprintf("homeport-queue: processed=%d failed=%d waiting=%d next_due=%d\n", $processed, $failed, $waiting, $due));
});

$output = class_exists('Symfony\Component\Console\Output\ConsoleOutput') ? new Symfony\Component\Console\Output\ConsoleOutput() : null;
do {
    $done = $processed + $failed;
    $status = $kernel->call('queue:work', array_merge($options, ['--stop-when-empty' => true, '--max-time' => max(1, (int) ($started + $maxTime - microtime(true)))]), $output);
    $left = $pending();
    // again while jobs are ready, time is left, and the last run got something done
} while ($status === 0 && $left[0] > 0 && $processed + $failed > $done && $started + $maxTime - microtime(true) >= 1);
exit($status);
