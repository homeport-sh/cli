// Command homeport is the CLI of homeport, the hosted platform at
// homeport.sh: sign in from a terminal (`homeport login`), link a folder to
// an app (`homeport link`), and deploy it (`homeport deploy`) - the working
// tree, built and released as a pushed commit would be. `homeport
// build-plan` is what every hosted build runs on a checkout, and anyone can
// run it to see what a build will do.
package main

import (
	"fmt"
	"os"
)

// version is the release version, stamped from the git tag at build time via
// -ldflags "-X main.version=…" (GoReleaser). Plain `go build` reports "dev".
var version = "dev"

func main() {
	os.Exit(newApp().run(os.Args[1:]))
}

func (a *app) usage() {
	fmt.Fprint(a.out, `homeport — the CLI of homeport.sh

  homeport login      sign in: approve this device on the dashboard
                      [--name <device>] [--no-browser]
  homeport whoami     who you're signed in as, and your teams
  homeport logout     sign out: this device's session is revoked
  homeport link       link this folder to an app's environment
                      [--team <slug>] [--app <name>] [--env <name>]
  homeport deploy     upload this folder's working tree (what git would
                      commit, uncommitted changes included) and have
                      homeport build and release it; follows it until
                      it's live, then prints its address
                      [--run <cmd>] [--release <cmd>] [--process name=cmd]…
                      [--save] [--unset run|release|processes|octane]…
                      [--team <slug> --app <name> [--env <name>]]
                      [--detach] [--timeout 30m]
  homeport build-plan [--settings file.json] [dir]
                      what a hosted build runs for this repository (JSON):
                      detected without a homeport.yaml, or from one
  homeport version    this CLI's version

--run, --release and --process change this deploy alone, as the dashboard's
"Redeploy with changes" does; --save keeps them for every deploy after it.

In CI, set HOMEPORT_TOKEN to a CLI token instead of signing in. Exit codes:
0 live, 1 error, 2 usage, 3 not signed in, 4 build failed, 5 release
failed, 6 timed out.

Logs, env and cron from the terminal come next.
`)
}
