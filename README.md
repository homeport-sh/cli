# homeport CLI

The command-line tool for [homeport](https://homeport.sh), the hosted
platform for single-binary and static apps.

```sh
curl -fsSL https://homeport.sh/install.sh | sh
homeport login      # approve this device on the dashboard
homeport link       # pick the team, app and environment for this folder
homeport deploy     # upload the working tree, build it, follow it live
```

## Signing in

`homeport login` shows a code and opens `app.homeport.sh/cli`, where you
check the code matches and click **Approve** (you must be signed in there).
The terminal then signs itself in. Nothing is typed into the terminal, and
the token it receives is never printed.

- The sign-in is kept in `~/.config/homeport/credentials` (or under
  `$XDG_CONFIG_HOME`), readable by you alone (mode 0600). The CLI refuses
  that file if others can read it.
- Each device's sign-in is named after it (`--name` to choose) and listed in
  **Account → CLI sessions**, where you can revoke it. It ends after 90 days
  unused, and a year after it began whatever.
- `homeport logout` revokes it on homeport.sh and forgets it here.
- `homeport whoami` says who you are signed in as, and in which teams.
- In CI, set `HOMEPORT_TOKEN` to a token made for it: `homeport token create
  --name github-actions` prints one, once (or Account → CLI sessions →
  Create a token for CI). It's listed and revoked like any session.
- A sign-in is sent only to the API that gave it. Pointing `HOMEPORT_API`
  elsewhere counts as signed out there, and the API must be https (or
  http on this computer).

A sign-in from the CLI can do what you can do and nothing more: an
owner-only action stays owner-only.

## Linking and deploying

`homeport link` writes `.homeport/link.json`, which ignores itself in git,
naming the environment `homeport deploy` deploys. It asks which team, app
and environment, or takes `--team`, `--app` and `--env`. Only environments
homeport builds are offered. One that your own CI deploys is deployed from
your CI.

`homeport deploy` uploads the **working tree**: what git would commit,
meaning tracked files plus untracked ones that aren't ignored, as they are
on disk now, uncommitted changes included. homeport builds and releases it
exactly as it would a pushed commit. In a git checkout the whole repository
goes, from its top, and the app's folder within it is the app's setting.
Outside git, the folder goes, with its `.gitignore`s honoured. `.git` and
`.homeport` are never uploaded, and neither is anything that looks like a
secret, even when git doesn't ignore it: `.env` and `.env.*` files (but
`.env.example`), `*.pem`, and `id_rsa`, `id_ed25519` and the like. The
command says what it left out. Variables belong on the environment. The upload is capped by your plan (the CLI
stops at 2,000 MB). The build is named after HEAD when the tree is clean,
and otherwise after a digest of what was uploaded.

```sh
homeport deploy --run "./server --debug"          # this deploy alone
homeport deploy --release "./migrate" --save      # and every deploy after
homeport deploy --process worker="./work --queue" # replaces the processes
homeport deploy --save --unset processes          # back to homeport.yaml's
homeport deploy --team acme --app web --env staging   # without a link
homeport deploy --detach                          # don't wait
```

These follow the dashboard's **Redeploy with changes**: a change applies to
this deploy alone unless you `--save` it, and emptying a saved setting
(`--unset run|release|processes|octane`) needs `--save`.

It prints the build's log as it arrives, then the release's steps, then the
address once it's live. Exit codes, for CI:

| Code | Meaning |
| --- | --- |
| 0 | live |
| 1 | an error: refused, unreachable, a tree that can't be packed |
| 2 | usage: a bad flag, or a question with no terminal to ask in |
| 3 | not signed in, or the sign-in ended (`homeport login`) |
| 4 | the build failed, or was canceled or superseded |
| 5 | built, but the release didn't go live |
| 6 | still going when `--timeout` (30m) ran out; it carries on |

## Developing against an environment's add-ons

```sh
homeport env pull --env staging
```

This writes an environment's **add-on credentials** to `.env.local`: its
database's `DATABASE_URL` and parts, and its storage's `AWS_*` and bucket.
Those are the values the dashboard's reveal shows, so you can run the app
on your laptop against staging's database and bucket.

- Production's are never pulled, and there is no flag to change that.
- The app's own variables are never pulled: they stay write-only.
- `.env.local` is written at mode 0600. Only the keys it pulls are
  replaced; every other line is left as it was.
- `.env.local` is added to `.gitignore` if nothing there covers it.
- No value is printed.
- Each pull is recorded, and a person can pull 30 times an hour.

`logs` and `cron` from the terminal come next.

## MCP: homeport in your editor

`homeport mcp` is an [MCP](https://modelcontextprotocol.io) server on stdio,
for Claude Code, Cursor or any agent on your computer. It signs in the way
`homeport login` does, with the same credentials file and the same token,
so it can do what you can do and nothing more. Sign in first.

```sh
claude mcp add homeport -- homeport mcp
```

Cursor (`~/.cursor/mcp.json`, or `.cursor/mcp.json` in a project):

```json
{ "mcpServers": { "homeport": { "command": "homeport", "args": ["mcp"] } } }
```

| Tool | What it does |
| --- | --- |
| `list_apps` | your teams, their apps and environments (reads) |
| `deploy` | deploys **the folder the server runs in**, as `homeport link` linked it, like `homeport deploy`, and answers the address (**changes it**) |
| `deploy_status` | a build's status and log, then its release's (reads) |
| `runtime_logs` | an environment's runtime logs (reads) |
| `list_variables` | an environment's variables, **names only** (reads) |
| `set_variables` | sets variables (**changes it**) |
| `unset_variables` | removes variables, for good (**changes it**) |
| `get_database` | an environment's database, never its password (reads) |
| `create_database`, `attach_database`, `detach_database` | its database (**change it**) |
| `usage` | a team's month so far and its estimate (reads) |

An agent can be talked into things by what it reads, such as a log line or
a file. So **a change is never the agent's to approve: you are asked**,
through your editor, what exactly will change and where. The approval holds
for that change, once. An editor that can't ask (MCP elicitation) gets no
changes unless you start the server with `homeport mcp --allow-changes`.
**With `--allow-changes` nobody is asked: the agent's changes are made as
it asks, production included.** Use it only for an editor that can't ask,
and only if you'd let that agent change production unattended.
No tool takes a folder: `deploy` deploys the one the server runs in. No
tool returns a variable's value or a secret, and none deletes: apps,
databases and domains are deleted from the dashboard. The server makes at
most 20 changes a minute.

## `build-plan`

`homeport build-plan [--settings file.json] [dir]` reads a repository,
with or without a configuration file, and says how a hosted build will
build it. It uses the same detection the platform's builders run.
[docs/detection.md](docs/detection.md) says what it detects - Go, PHP,
static sites, and JavaScript apps on Node or Bun (Next.js, Nuxt, SvelteKit,
Astro, React Router, Remix, NestJS, Elysia, Hono, Fastify, Express) - and
how each runs.

The Go packages here (`buildplan`, `envfile`, `artifact`, `cidr`) are shared
with the platform.

## Licence

[Functional Source License, Version 1.1, ALv2 Future License](LICENSE.md):
use, modify and self-host it freely, except to offer a competing service;
each release becomes Apache 2.0 two years after it ships.
