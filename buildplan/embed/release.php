<?php

// homeport: a Laravel app's release command, run once a deploy, in the
// sandbox the release serves from, before it goes live:
//
//   ./bin php-cli .homeport/release.php ./bin [the app's release command]
//
// The app's own release command (its migrations) runs first; it failing
// fails the release, and the deploy stops. Then artisan view:cache compiles
// the app's Blade views into storage/framework/views, the release's own, at
// the path the app serves from (Blade names a compiled view by its
// template's path): no wake compiles them. A view:cache that fails is said,
// and the deploy goes on - views compile as they're first shown.

$io = [0 => STDIN, 1 => STDOUT, 2 => STDERR];
$run = function (array $cmd) use ($io): int {
    $p = @proc_open($cmd, $io, $pipes);
    return $p === false ? 127 : proc_close($p);
};

$bin = $argv[1] ?? './bin';
$release = array_slice($argv, 2);
if ($release) {
    $code = $run(array_merge([$bin], $release));
    if ($code !== 0) {
        exit($code);
    }
}
if ($run([$bin, 'php-cli', 'artisan', 'view:cache']) !== 0) {
    fwrite(STDERR, "homeport: artisan view:cache failed: the app's views compile as they're first shown\n");
}
exit(0);
