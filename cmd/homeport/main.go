// Command homeport is the CLI of homeport, the hosted platform at
// homeport.sh. Today it plans builds: `homeport build-plan` is what every
// hosted build runs on a checkout, and anyone can run it to see what a build
// will do. Signing in, deploying and reading logs from a terminal come with
// `homeport login`.
//
// The hosted platform runs apps; this is its command-line tool.
package main

import (
	"errors"
	"fmt"
	"os"
)

// version is the release version, stamped from the git tag at build time via
// -ldflags "-X main.version=…" (GoReleaser). Plain `go build` reports "dev".
var version = "dev"

func main() {
	args := os.Args[1:]
	if len(args) == 0 {
		usage()
		return
	}
	cmd, rest := args[0], args[1:]
	var err error
	switch cmd {
	case "build-plan":
		err = cmdBuildPlan(rest)
	case "mcp":
		// the AI-agent server returns with the platform's commands
		err = errors.New("the MCP server comes back with `homeport login` - deploy, logs and env on homeport.sh")
	case "version", "-v", "--version":
		fmt.Println("homeport", version)
	case "help", "-h", "--help":
		usage()
	default:
		err = fmt.Errorf("unknown command %q (try: homeport help)", cmd)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "\x1b[1;31merror:\x1b[0m %v\n", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Print(`homeport — the CLI of homeport.sh

  homeport build-plan [--settings file.json] [dir]
                      what a hosted build runs for this repository (JSON):
                      detected without a homeport.yaml, or from one
  homeport version    this CLI's version

Deploy on https://app.homeport.sh: connect a repository, or paste a public
one. Signing in, deploying, logs and env from the terminal come with
` + "`homeport login`" + `.
`)
}
