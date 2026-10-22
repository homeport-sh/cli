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
  homeport link       link this folder to an app's environment; an --app
                      the team hasn't is created, asked first
                      [--team <slug>] [--app <name>] [--env <name>] [--yes]
  homeport deploy     upload this folder's working tree (what git would
                      commit, uncommitted changes included) and have
                      homeport build and release it; follows it until
                      it's live, then prints its address. In a folder
                      nothing links, its app (named after the folder, or
                      --app) is created, asked first, and linked
                      [--run <cmd>] [--release <cmd> | --no-release]
                      [--process name=cmd]…
                      [--save] [--unset run|release|processes|octane]…
                      [--team <slug> --app <name> [--env <name>]]
                      [--yes] [--detach] [--timeout 30m]
  homeport rollback   lists the earlier releases you can roll back to;
                      rollback <deploy|commit> ships that build again, no
                      build, as the dashboard's Roll back to this; asks
                      first [--yes] [--run-release] [--env <name>]
                      [--detach] [--timeout 30m]
  homeport env set    sets an environment's variables: NAME=value…, or a
                      file variable (a certificate, a key, any multi-line
                      or binary value) the app reads at /run/secrets/NAME:
                      [NAME] --from-file <path>
                      [--env <name>] [--team <slug> --app <name>]
  homeport env unset  removes variables: NAME…
  homeport env ls     an environment's variables by name; never a value
  homeport env get    a variable's value: NAME [--resolved]; never a secret's
  homeport env pull   an environment's database and storage credentials into
                      .env.local (0600), to develop against; never
                      production's, never your app's own variables
                      [--env <name>]
  homeport domains ls an app's platform domains (its tenants' own, which
                      the app adds through the Domains API): each one's
                      status, and why it isn't live
  homeport domains add <domain>
                      adds one, and says the DNS records to set
  homeport domains get <domain>
                      one's status and DNS records
  homeport domains remove <domain>
                      removes one: it stops being served; asks first [--yes]
                      each [--env <name>] [--team <slug> --app <name>]
  homeport token create --name <where>
                      a token for CI (HOMEPORT_TOKEN), printed once: for
                      one environment, and it acts there alone
                      [--team <slug>] [--app <name>] [--env <name>]
  homeport mcp        an MCP server on stdio, for your editor or agent:
                      claude mcp add homeport -- homeport mcp
                      every change is asked of you through the editor
                      [--allow-changes] makes changes with nobody asked -
                      production included; only for an editor that can't ask
  homeport build-plan [--settings file.json] [dir]
                      what a hosted build runs for this repository (JSON):
                      detected without a homeport.yaml, or from one
  homeport version    this CLI's version

--run, --release and --process change this deploy alone, as the dashboard's
"Redeploy with changes" does; --save keeps them for every deploy after it.
--yes creates an app the team hasn't without asking: needed in CI, where
there's no terminal to ask in.

In CI, set HOMEPORT_TOKEN to a token from homeport token create. Exit codes:
0 live, 1 error, 2 usage, 3 not signed in, 4 build failed, 5 release
failed, 6 timed out.
`)
}
