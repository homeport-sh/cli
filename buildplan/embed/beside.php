<?php

// homeport: the web, and what runs beside it in the same sandbox - each line
// of .homeport/beside, a command (Inertia's SSR renderer). homeportd runs the
// web through this when the bundle has a .homeport/wrap:
//
//   ./bin php-cli .homeport/beside.php ./bin <the web's args>
//
// What's beside starts first, then the web. One that exits is started again,
// a second later, then longer, up to 30s; one that ran a minute starts over
// at a second. A stop (TERM, INT, HUP) is passed to the web; when the web
// exits, what's beside is stopped, and the web's exit is this one's.

pcntl_async_signals(true);

$say = function (string $message): void {
    fwrite(STDERR, "homeport: {$message}\n");
};
$io = [0 => STDIN, 1 => STDOUT, 2 => STDERR];

$beside = [];
foreach (@file(__DIR__.'/beside', FILE_IGNORE_NEW_LINES | FILE_SKIP_EMPTY_LINES) ?: [] as $line) {
    $cmd = preg_split('/\s+/', trim($line), -1, PREG_SPLIT_NO_EMPTY);
    if ($cmd) {
        $beside[] = ['cmd' => $cmd, 'proc' => null, 'at' => 0.0, 'next' => 0.0, 'wait' => 1.0];
    }
}

$start = function (array &$b) use ($io, $say): void {
    $b['proc'] = @proc_open($b['cmd'], $io, $pipes) ?: null;
    $b['at'] = microtime(true);
    if ($b['proc'] === null) {
        $say(implode(' ', $b['cmd']).' did not start');
    }
};
$down = function (array &$b, array $s) use ($say): void {
    $how = $s['signaled'] ? 'signal '.$s['termsig'] : 'status '.$s['exitcode'];
    proc_close($b['proc']);
    $b['proc'] = null;
    if (microtime(true) - $b['at'] >= 60) {
        $b['wait'] = 1.0;
    }
    $say(implode(' ', $b['cmd'])." exited ({$how}): starting it again in {$b['wait']}s");
    $b['next'] = microtime(true) + $b['wait'];
    $b['wait'] = min($b['wait'] * 2, 30.0);
};

foreach ($beside as &$b) {
    $start($b);
}
unset($b);

$web = @proc_open(array_slice($argv, 1), $io, $pipes);
if ($web === false) {
    $say('the web did not start: '.implode(' ', array_slice($argv, 1)));
    exit(1);
}
$stopping = false;
foreach ([SIGTERM, SIGINT, SIGHUP] as $signal) {
    pcntl_signal($signal, function (int $signal) use (&$web, &$stopping): void {
        $stopping = true;
        proc_terminate($web, $signal);
    });
}

while (true) {
    $s = proc_get_status($web);
    if (! $s['running']) {
        $code = $s['signaled'] ? 128 + $s['termsig'] : ($s['exitcode'] < 0 ? 1 : $s['exitcode']);
        break;
    }
    foreach ($beside as &$b) {
        if ($b['proc'] !== null) {
            $st = proc_get_status($b['proc']);
            if (! $st['running']) {
                $down($b, $st);
            }
        } elseif (! $stopping && microtime(true) >= $b['next']) {
            $start($b);
        }
    }
    unset($b);
    usleep(100000);
}

// the web is gone: so is what was beside it
foreach ($beside as $b) {
    if ($b['proc'] !== null) {
        proc_terminate($b['proc'], SIGTERM);
    }
}
for ($i = 0; $i < 50; $i++) {
    $left = array_filter($beside, fn ($b) => $b['proc'] !== null && proc_get_status($b['proc'])['running']);
    if (! $left) {
        break;
    }
    usleep(100000);
}
foreach ($beside as $b) {
    if ($b['proc'] !== null && proc_get_status($b['proc'])['running']) {
        proc_terminate($b['proc'], SIGKILL);
    }
}
exit($code);
