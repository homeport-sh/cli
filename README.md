# homeport CLI

The command-line tool for [homeport](https://homeport.sh), the hosted
platform for single-binary and static apps.

```sh
curl -fsSL https://homeport.sh/install | sh   # or, on macOS: brew install homeport-sh/tap/homeport
homeport login      # approve this device on the dashboard
homeport deploy     # upload the working tree, build it, follow it live
                    # (a new folder: asks to create its app, then links it)
```

| Command | What it does |
| --- | --- |
| `homeport login` | sign in: approve this device at `app.homeport.sh/cli` (`--name`, `--no-browser`) |
| `homeport logout` | sign out: this device's session is revoked |
| `homeport whoami` | who you're signed in as, and your teams |
| `homeport link` | link this folder to an app's environment (`--team`, `--app`, `--env`); an `--app` the team hasn't is created, asked first (`--yes`) |
| `homeport deploy` | upload the working tree, build and release it, follow it until it's live; in a folder nothing links, its app is created first, asked first (`--yes`) |
| `homeport token create --name <where>` | a token for CI (`HOMEPORT_TOKEN`), printed once: for one environment - this folder's link's, or `--team`, `--app`, `--env` - and it acts there alone |
| `homeport rollback [<deploy\|commit>]` | lists the earlier releases you can roll back to; with one, ships its build again without building (asks first; `--yes`, `--run-release`) |
| `homeport env set NAME=value…` | sets an environment's variables; the app restarts to get them (`--env`, `--team`, `--app`) |
| `homeport env set [NAME] --from-file <path>` | a file variable: a certificate, key or any multi-line or binary value, read at `/run/secrets/NAME` |
| `homeport env unset NAME…` | removes variables, plain or file |
| `homeport env set NAME=value… --build` | available to the build too (`--build-only`: the build alone; `--runtime`: the running app alone). Names with a public prefix (`NEXT_PUBLIC_`, `VITE_`, `PUBLIC_`, …) are build and runtime by default; `homeport env set NAME --build` changes who sees one, its value kept |
| `homeport env set NAME=value… --for <role>` | a value for one part of the app alone: `release` (the release command), `web`, or a process by name; `--for` once for each. `homeport env unset NAME --for <role>` removes that part's own value alone. Needs homeportd 0.36.0 where the app runs |
| `homeport env set NAME=value… --secret` | a secret: its value is never shown again (`--plain`: a variable, its value shown in `env ls` and on the dashboard). Neither: a value keeps its kind, and a new one is a secret. `homeport env set NAME --secret` changes a variable to a secret, its value kept; a secret becomes a variable only with a new value |
| `homeport env ls` | every variable an environment gets, who sees it (runtime, build + runtime, build only), which parts of the app get it (`all`, `release`, `all; own: worker`), which it is (secret, variable, file) and where it comes from (its own, global, an add-on's, homeport's), the variables its value names (REF), a variable's value, and what each file variable is; never a secret's value |
| `homeport env get NAME [--resolved]` | a variable's value as written (`${{APP_URL}}/app`), or with `--resolved` as the app gets it (`https://example.com/app`); never a secret's |
| `homeport env rebuild` | builds the live commit again with the variables as they are now: a change the build sees needs one |
| `homeport env pull [--env <name>]` | an environment's database and storage credentials into `.env.local`, never production's |
| `homeport domains ls` | an app's platform domains (its tenants' own, which the app adds through the Domains API): each one's status and why it isn't live, how many are live, and how many the plan includes |
| `homeport domains add <domain>` | adds one, and says the DNS records to set at its provider (the same one again is the one there) |
| `homeport domains get <domain>` | one's status, why it isn't live, and its DNS records |
| `homeport domains remove <domain>` | removes one: it's no longer served (asks first; `--yes`) |
| `homeport queue status` | how an environment's background jobs run (on demand, always on), what the last drain said is waiting and when the next delayed job is due, and its recent drains |
| `homeport queue drain` | runs the app's drain here, with its `.env`, against your local database; `--remote` starts one on homeport now |
| `homeport queue retry-failed` | runs the queue's retry here; `--remote` on homeport |
| `homeport mcp` | an MCP server on stdio for your editor or agent |
| `homeport build-plan` | what a hosted build runs for this repository (JSON) |
| `homeport version` | this CLI's version |

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
  Create a token for CI). It's made for one environment - this folder's
  link's, the one `--team`/`--app`/`--env` name, or the one you pick when
  asked - and acts as you there alone: a token leaked from one repository
  can't touch your other apps or teams. It's listed and revoked like any
  session.
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

No app yet? `homeport deploy` in a folder nothing links creates one, named
after the folder (`--app` to name it), in the team `--team` names (asked,
when you're in several). It asks first:

```
No app "rsc-sharp" on team "acme". Create it? [y/N]
```

`--yes` creates it without asking, and is needed with no terminal to ask
in (CI); without it, nothing is created. The app has no repository: it's
deployed from your computer with `homeport deploy`, its production
environment on its own address, held to your plan's apps like any other.
The folder is then linked to it. `homeport link --app <name>` and
`homeport deploy --app <name>` create an app the team hasn't the same way.
A folder named after an app the team already has isn't deployed to it
until you link it.

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
homeport deploy --no-release --save               # no release command, not even a detected one
homeport deploy --save --unset processes          # back to homeport.yaml's
homeport deploy --team acme --app web --env staging   # without a link
homeport deploy --team acme --app web --yes      # creates web if acme hasn't it
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

## Rolling back

```sh
homeport rollback                     # what you can roll back to
homeport rollback 3f7a1c2             # by commit (or a deploy id), asks first
homeport rollback 3f7a1c2 --yes       # in CI: no terminal to ask in
homeport rollback 3f7a1c2 --run-release
```

The dashboard's **Roll back to this**: an earlier release that went live
ships its stored build again, with no build, as a deploy of its own. The
build is the old one (its code and the build variables it was given);
runtime variables and settings stay as they are now. Builds are kept 30
days. Its release command is skipped unless you pass `--run-release`:
migrations go forward. It follows the release as `deploy` does, with the
same exit codes.

## Variables

```sh
homeport env set API_KEY=… MAIL_FROM=hello@example.com
homeport env set --from-file do-ca.crt          # DO_CA_CRT, from the file's name
homeport env set NODE_EXTRA_CA_CERTS --from-file ca.pem --env staging
homeport env set NEXT_PUBLIC_API_URL=https://api.example.com   # build and runtime, from its prefix
homeport env set NPM_TOKEN=… --build-only       # the build's alone
homeport env set MIGRATION_DATABASE_URL=… --for release   # the release command's alone
homeport env set DATABASE_POOL_MAX=4 --for worker         # the worker's own value
homeport env set TZ=America/Jamaica LOG_LEVEL=info --plain  # variables: shown in env ls
homeport env set 'VITE_APP_URL=${{APP_URL}}' --build --plain   # APP_URL's value, kept in step
homeport env rebuild                            # put the change in the built app
homeport env ls
homeport env get VITE_APP_URL --resolved
homeport env unset API_KEY
```

A value can name another with `${{NAME}}` (single-quote it in the
shell): the app gets the other's value in its place, and a change of
that value reaches every value naming it. `$${{` is the text itself. A
value naming a secret is a secret, and a build value names only build
values; anything else is refused when it's set, never at deploy.

A value is one line. Anything else - a CA certificate, a client
certificate and key, a service account's JSON, a keystore - is a **file
variable**: the app reads it at `/run/secrets/NAME`, read-only, and
`$NAME` holds that path (`NODE_EXTRA_CA_CERTS`, `PGSSLROOTCERT` and
`GOOGLE_APPLICATION_CREDENTIALS` take a path already).

- `--from-file` reads the file on this computer and sends its bytes as they
  are, text or binary. Without a name, the name comes from the file's:
  upper case, anything but letters and digits `_` (`do-ca.crt` is
  `DO_CA_CRT`).
- A file holds 1 byte to 256 KB; an environment's files, 1 MB and 32 files
  at most. The same on every plan. What the platform would refuse is
  refused before anything is sent.
- `env ls` shows a certificate's subject and expiry (certificates are
  public) and of anything else what it is - never a value or a file's
  content.
- Setting or removing a variable restarts the app to get it.

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
- If production uses an add-on too (a database attached to both), that
  add-on's variables are withheld, and the command says which.
- `.env.local` is written at mode 0600. Its first line records the keys
  `env pull` owns: those are replaced, or removed once the environment no
  longer has them. Every other line is left as it was.
- `.env.local` is made ignored first (git decides, in a checkout; a
  `.gitignore` above counts). If it can't be, nothing is written.
- A token for CI can't pull.
- No value is printed.
- Each pull is recorded, and a person can pull 30 times an hour.

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
| `deploy` | deploys **the folder `homeport mcp` runs in**, as `homeport link` linked it, like `homeport deploy`, and answers the address (**changes it**) |
| `create_app` | creates an app in a team (its name), deployed from your computer, and links the folder `homeport mcp` runs in to it when nothing links it yet (**changes it**) |
| `deploy_status` | a build's status and log, then its release's (reads) |
| `runtime_logs` | an environment's runtime logs (reads) |
| `list_variables` | an environment's variables, **names only**, and what each file variable is (reads) |
| `set_variables` | sets variables, and file variables from a file's path here, read by the CLI so its content never passes through the agent (**changes it**) |
| `unset_variables` | removes variables, for good (**changes it**) |
| `get_database` | an environment's database, never its password (reads) |
| `create_database`, `attach_database`, `detach_database` | its database (**change it**) |
| `usage` | a team's month so far and its estimate (reads) |
| `list_domains`, `get_domain` | an app's platform domains, their status and DNS records (reads) |
| `queue_status` | an environment's queue: how it runs, what's waiting, its recent drains (reads) |
| `add_domain` | adds a platform domain to an app (**changes it**) |

An agent can be talked into things by what it reads, such as a log line or
a file. So **a change is never the agent's to approve: you are asked**,
through your editor, what exactly will change and where. The approval holds
for that change, once. An editor that can't ask (MCP elicitation) gets no
changes unless you start the server with `homeport mcp --allow-changes`.
**With `--allow-changes` nobody is asked: the agent's changes are made as
it asks, production included.** Use it only for an editor that can't ask,
and only if you'd let that agent change production unattended.
No tool takes a folder: `deploy` deploys the one `homeport mcp` runs in. No
tool returns a variable's value or a secret, and none deletes: apps,
databases and domains are deleted from the dashboard. `homeport mcp` makes
at most 20 changes a minute.

## `build-plan`

`homeport build-plan [--settings file.json] [dir]` reads a repository,
with or without a configuration file, and says how a hosted build will
build it. It uses the same detection the platform's builders run.
[docs/detection.md](docs/detection.md) says what it detects - Go, PHP,
static sites, and JavaScript apps on Node or Bun (Next.js, Nuxt, SvelteKit,
TanStack Start, Astro, React Router, Remix, NestJS, Elysia, Hono, Fastify,
Express) - and how each runs.
[docs/headers.md](docs/headers.md) is an app's response headers
(`headers:` in `homeport.yaml`).

The Go packages here (`buildplan`, `envfile`, `artifact`, `cidr`) are shared
with the platform.

## Licence

[Functional Source License, Version 1.1, ALv2 Future License](LICENSE.md):
use, modify and self-host it freely, except to offer a competing service;
each release becomes Apache 2.0 two years after it ships.
