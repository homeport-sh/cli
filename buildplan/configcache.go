package buildplan

import (
	"fmt"
	"slices"
	"strings"
)

// A Laravel app's config is cached as each of its servers starts, from the
// environment it starts with - never at build, where it would hold the
// build's environment: homeport restarts an app whose environment changed
// without building it again. The bundle's .homeport/start.php (homeport's
// FrankenPHP base writes it, frankenphp-bundle) caches it, then runs the
// command:
//
//	./bin php-cli .homeport/start.php ./bin <the command's args>
//
// The web starts through it by the bundle's own .homeport/wrap, which
// homeportd reads, so its start command is as it was; the plan runs each
// process through it. A cron job or the release command, which don't start
// through it, run with the config uncached, as before.

// StartRunner is what a Laravel app's processes run through: args to the
// bundle's bin, the process's own after it.
const StartRunner = "php-cli .homeport/start.php ./bin"

// startPHPF is the runner, in the bundle.
const startPHPF = ".homeport/start.php"

// cacheConfigOnStart runs each of the plan's processes through the runner -
// unless it does already, or it won't fit, when it runs as it is and the
// plan says why its config isn't cached. One the sandbox can't run is left
// as it is, for the check that refuses it, saying why.
func cacheConfigOnStart(p *Plan) {
	p.Processes = slices.Clone(p.Processes)
	for i, x := range p.Processes {
		if strings.HasPrefix(x.Run, StartRunner+" ") || CheckProcesses([]Process{x}) != nil {
			continue
		}
		run := StartRunner + " " + x.Run
		if len(run) > maxCommandSz {
			p.Warnings = append(p.Warnings, fmt.Sprintf("process %s runs with Laravel's config uncached: its command is too long to run beside the cache (at most %d characters)",
				x.Name, maxCommandSz-len(StartRunner)-1))
			continue
		}
		p.Processes[i].Run = run
	}
}

// ssrWrap writes the bundle b's .homeport/wrap for Inertia's SSR renderer:
// the supervisor that runs it beside the web, behind the config's runner
// when the bundle has one (homeport's FrankenPHP base writes it).
func ssrWrap(b string) string {
	write := func(args string) string { return "printf '%s\\n' '" + args + "' > " + b + "/" + wrapFile }
	return "if [ -f " + b + "/" + startPHPF + " ]; then " + write("bin "+StartRunner+" php-cli "+besidePHPF) + "; else " + write(wrapArgs) + "; fi"
}
