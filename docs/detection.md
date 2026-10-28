# How homeport detects a build

`homeport build-plan [dir]` reads a repository and prints, as JSON, how it
builds and what it produces. It runs nothing from the repository. The
platform's builders run the same code, so what it prints is what a hosted
build does.

What a person sets in the app's build settings wins over `homeport.yaml`,
and `homeport.yaml` wins over detection.

Response headers (`headers:` in `homeport.yaml`) are in
[headers.md](headers.md).

Which prerendered pages are answered without waking the app (SvelteKit,
Astro) is in [edge-caching.md](edge-caching.md).

A **stop grace** is how long the web or a process gets to finish after
SIGTERM before it's killed: on a deploy, a restart, a scale-down or a move.
`stop_grace:` at the top of `homeport.yaml` is the web's, and a process's
own sits beside its `run:`. Whole seconds or minutes, `1s` to `15m`; unset
is the platform's default. The build settings' (`stop_grace` on the
settings and on each process) win, as everything else there does.

```yaml
run: serve --port $PORT
stop_grace: 60s        # long requests get a minute
processes:
  worker:
    run: queue:work
    stop_grace: 10m    # finish the job it holds
```

`health:` at the top of `homeport.yaml` is the path a deploy's health
check asks for, like `health: /healthz`: a path on the app, starting with
`/`, no query or spaces, no `..`, at most 200 characters. On the platform
the file's wins over the environment's setting; unset, the setting's is
used, else `/`.

| Files | Toolchain | Produces |
|---|---|---|
| `go.mod` | Go (`golang:<go.mod's version>`) | a binary |
| `composer.json` + `composer.lock` | PHP on homeport's FrankenPHP base | a bundle, FrankenPHP as `bin` |
| `package.json` + a lockfile | Node or Bun (see below) | a bundle, a static site, or a binary when the build compiles one |
| `pyproject.toml` + `uv.lock`, or `requirements.txt` | Python, installed with uv | a bundle, the interpreter as `bin` |
| `index.html` alone | none | the folder, as a static site |

## JavaScript apps

A JavaScript app runs what its build produces. A server ships as a
**bundle**: the framework's documented production output, or the app's
files with only its production dependencies, plus the runtime the project
uses as `bin`. homeport runs the app the way its start script does.

It ships a **binary** only when the project's own build compiles its
server. That happens in three ways:

- **A tool that compiles it.**
  - SvelteKit's own Bun adapter, `@sveltejs/adapter-bun`, with
    `buildOptions.compile` set (`true`, a target, or options) writes one
    executable with the client assets in it: `<out>/server`, or the
    `outfile` you give it. It builds with `bun run --bun build`, as the
    adapter needs. A target must be a Linux glibc one for the machine it
    builds on (`bun-linux-x64` on x86_64, `bun-linux-arm64` on aarch64,
    checked before the build), and `envPrefix` is refused with `compile`
    (see *SvelteKit on Bun* below). svelte-smol isn't supported: a project
    importing it is refused, pointing at adapter-bun.
  - next-bun-compile is a Next.js build adapter (set by `adapterPath` in
    next.config, or `NEXT_ADAPTER_PATH=next-bun-compile`) and writes
    `./server`.
- **`bun build --compile` of the framework's own server output**: Nitro's
  `.output/server/index.mjs`, from Nuxt (`nuxt build --preset bun`) or
  TanStack Start (`nitro({ preset: 'bun' })`). homeport runs the file that
  `--outfile` names. Nitro serves its public assets from a folder beside
  its server, and a binary has nothing beside it, so this is refused unless
  Nitro inlines them: set `serveStatic: 'inline'` in Nuxt's `nitro`
  options, or in the `nitro()` plugin's options in `vite.config`. For Nuxt,
  compile with `--production`, so Bun resolves the production files Nitro
  kept:

  ```
  "build": "nuxt build --preset bun && bun build --compile --production .output/server/index.mjs --outfile dist/app"
  ```
- **`bun build --compile` in an app with no framework**, when it has no
  start script or its start script runs the compiled file. If the start
  script runs something else (`node dist/server.js`), the compile made a
  tool, and the app is a bundle.

A Next.js app whose build also compiles a worker is still a Next.js bundle,
because the worker isn't the server. Builds that compile run with the
pinned Bun, and the binary runs on the Bun it was compiled with: setting
the runtime to `node` for one is refused. Compiling an app that doesn't compile itself can leave
out files it loads at runtime: native modules (sharp, bcrypt, Prisma's
engines), workers, and lookups by node_modules path.

### Presets

The framework is detected from `package.json`'s dependencies. Each preset
sets defaults only: the build command, what ships, how it starts, and the
health path (`/`). Each one can be overridden in the build settings.

| Preset | Detected by | Ships | Starts |
|---|---|---|---|
| Next.js | `next` | `.next/standalone` with `.next/static` and `public/` | `server.js` |
| Nuxt | `nuxt` | Nitro's `.output/` | `server/index.mjs` |
| TanStack Start, with Nitro | `@tanstack/react-start` or `@tanstack/solid-start`, and `nitro` | Nitro's `.output/` | `server/index.mjs` |
| TanStack Start, without Nitro | `@tanstack/react-start` or `@tanstack/solid-start` | the app with production dependencies | its start script; with none, it's refused (add Nitro) |
| SvelteKit on Bun | `@sveltejs/adapter-bun` | the app with production dependencies, on Bun | `build/index.js` (or the adapter's `out`) |
| SvelteKit | `@sveltejs/adapter-node` | the app with production dependencies | `build/index.js` (or the adapter's `out`, from `svelte.config.js` or, in SvelteKit 3, the Vite config) |
| Astro | `@astrojs/node`, standalone mode | the app with production dependencies | `dist/server/entry.mjs` |
| React Router | `@react-router/*` | the app with production dependencies | the start script (`react-router-serve …`) |
| Remix | `@remix-run/*` | the app with production dependencies | the start script (`remix-serve …`) |
| NestJS | `@nestjs/core` | the app with production dependencies | `start:prod`, else `dist/main.js` |
| Elysia | `elysia` | the app with production dependencies | the start script, else `dev`, else `module`/`main` |
| Hono, Fastify, Express, Koa | the package | the app with production dependencies | the start script, else `main` |
| Bun, Node | nothing above | the app with production dependencies | the start script, else `main` |

**Which SvelteKit adapter.** The adapter the config imports is the one the
app builds with. One that's installed but not imported is ignored, so a
config importing `adapter-static` is a site even with `adapter-node` or
`adapter-bun` installed. With both server adapters installed and no
config to say, it's adapter-node. The adapter and its options (`out`,
adapter-static's `pages`, adapter-bun's `envPrefix` and
`buildOptions.compile`) are read the same from SvelteKit 3's Vite config
(`sveltekit({ adapter: adapter(...) })` in `vite.config.*`) and from
SvelteKit 2's `svelte.config.js`.

Static sites are still detected first: SvelteKit with `adapter-static`,
Astro without a server adapter, Vite, and Next.js with `output: "export"`
(served from `out/`). A Vite app with TanStack Router but not TanStack
Start is a single-page app, so it's a static site too.

**TanStack Start.** Its `vite build` makes a server only with Nitro's Vite
plugin (`nitro` installed, `nitro()` from `nitro/vite` in `vite.config`'s
plugins), as TanStack's Node and Bun hosting guides use. homeport then
ships Nitro's `.output/` and starts `server/index.mjs`, on Node, or on Bun
with `nitro({ preset: 'bun' })` (or a start script that runs it with
`bun`). The preset is read from the `nitro()` plugin's options, then
`nitro.config`. `node-server`, `node-cluster` and `bun` run here; a preset
for another platform (`vercel`, `netlify`, …) is refused. Server functions
check that a caller is same-origin by the browser's `Sec-Fetch-Site`
header, or else its `Origin` against the request's URL. Nitro builds that
URL from the connection, plain HTTP behind the edge, so current browsers,
which all send `Sec-Fetch-Site`, pass, but an `Origin`-only caller gets a
403.

Without Nitro, which is how TanStack's starter (`@tanstack/cli create`)
comes, the build's `dist/server/server.js` is a request handler with no
server. TanStack's hosting guide has Node, Bun and the hosts follow its
Nitro instructions, so homeport does too: such an app is refused, saying
to `npm install nitro` (or your package manager's add) and add `nitro()`
from `nitro/vite` to `vite.config`'s plugins, with
`nitro({ preset: 'bun' })` to run on Bun. If the app has a start script
that serves the handler (`srvx --prod -s ../client dist/server/server.js`,
or TanStack's `bun run server.ts`), that runs, as the app with its
production dependencies. Those build the request URL from the connection,
plain HTTP behind the edge, so an `Origin`-only caller gets a 403 there
too, as with Nitro. Don't turn on srvx's `trustProxy` for it: that reads
`X-Forwarded-Host`, which a visitor can set.

**SvelteKit on Bun.** `@sveltejs/adapter-bun` writes a `Bun.serve` server,
so it always runs on Bun: an `.nvmrc` doesn't change that, and setting the
runtime to `node` is refused. It builds with `bun run --bun build`, since
the adapter calls Bun's build API and Vite's own command would run on
Node. Its options are read from its call in the config that imports it
(`vite.config` or `svelte.config.js`), whatever the import is named
(`import bun from '@sveltejs/adapter-bun'`), and the build fails if the
adapter didn't write the file the plan starts. homeport's edge terminates
TLS and sets `X-Forwarded-Proto`, so the bundle gets `PROTOCOL_HEADER`
(below), and homeport's proxy sets `X-Real-IP` to the visitor's address,
so it gets `ADDRESS_HEADER` too ([Your visitor's IP](#your-visitors-ip)).
The host is the request's `Host`, the one the edge routed on.
`HOST_HEADER` isn't set, for adapter-bun or adapter-node, because a
visitor can set `X-Forwarded-Host`. A compiled server, which has no boot,
relies on the adapter's own default, the `Host` header with `https`,
which is what the edge sends. With `envPrefix`, the adapter reads
`<prefix>PORT` and `<prefix>HOST` and fails on unknown names with that
prefix, so the bundle sets `<prefix>PORT` and `<prefix>HOST` from
homeport's `PORT` and `HOST`, and `<prefix>PROTOCOL_HEADER` and
`<prefix>ADDRESS_HEADER`. A compiled server can't be given them, so `envPrefix` with
`compile` is refused. Set `BODY_SIZE_LIMIT` (default `512K`), with the
prefix if you use one, as an environment variable of the app.

**Next.js.** If `next.config` doesn't set `output: "standalone"`, the build
sets Next's default to standalone with `NEXT_PRIVATE_STANDALONE=1`. This
variable is undocumented. If a Next.js release ignores it, the build fails
and asks you to add `output: "standalone"` to your config.

**Astro** in `middleware` mode needs a server of yours to run it, so it's
refused: use `mode: 'standalone'`.

### Which runtime: Bun or Node

The runtime is what you run the app with locally. The first rule that
applies wins:

1. **The build settings**, or `runtime: bun | node` in `homeport.yaml`.
2. **`package.json`'s `engines`**, when it names only `bun` or only `node`.
3. **A version file**: `.bun-version` means Bun; `.nvmrc` or
   `.node-version` means Node.
4. **What the start script actually invokes.** For a Bun app with no start
   script, the `dev` script is read instead.
   - `bun src/index.ts`, `bun run src/index.ts` or `bun --bun next start`
     runs on Bun.
   - `node server.js` runs on Node.
   - A package's command, such as `next start`, `nest start` or
     `react-router-serve`, runs on Node. Its `#!/usr/bin/env node` makes
     `bun run start` run it on Node too, unless the script says
     `bun --bun`.
   - `npm run x`, `bun run x`, `pnpm x` and `yarn x` are followed to the
     script they name.
5. **A framework that only runs on one runtime**: Elysia runs on Bun when
   nothing above said otherwise. A version file or start script that says
   Node wins, as with any app. SvelteKit's adapter-bun is the exception: it
   builds a `Bun.serve` server, so it runs on Bun whatever rules 2 to 4
   say, and only the build settings or `homeport.yaml` can say otherwise,
   which is refused.
6. **Otherwise Node.**

The lockfile decides only how dependencies install, never what runs. A
Next.js app with `bun.lock` and `next start` installs with Bun and runs on
Node. `packageManager` names an installer, so it isn't read as a runtime
either.

The plan says which runtime it chose and why: `runtime`, `runtime_version`,
`runtime_reason` (for example "its start script runs bun src/index.ts").

**Bun compatibility.** homeport runs a framework on Bun only when your
project does. Next.js, Nuxt, TanStack Start, NestJS and the other Node
frameworks target Node and run on Node by default. Running them with `bun --bun`, or with the
runtime set to `bun`, uses Bun's Node compatibility, which is incomplete.
Test that before you rely on it. Elysia, SvelteKit's adapter-bun and Bun's own `Bun.serve` need Bun.

### Versions, pinned

- **Node.** homeport runs the latest release of 22, 24 or 26, chosen from
  `.nvmrc`, `.node-version` or `engines.node`, in that order. The current
  LTS is 24 and is the default. A bare version (`22`, `v22.11.0`) runs that
  line's pinned release. `lts/*` runs the default, `lts/<codename>` runs
  that line, `node` runs the newest, and a range (`>=20`, `^22`) runs the
  default if it's in the range, else the newest that is. Any other version
  is refused.
- **Bun.** homeport runs 1.4.4. A version the project names
  (`packageManager`, `.bun-version`, `engines.bun`) must be that one's: `1`,
  `1.4`, `1.4.x`, or a range it's in. Any other is refused, because every
  Bun homeport runs is pinned by digest and checksum.

The build image is the official one, pinned by digest (`node:<v>-bookworm`,
`oven/bun:<v>`). The binary that ships as `bin` is the image's, checked
against the sha256 of the official release binary. For Node, that hash
comes from nodejs.org's tarballs, which are themselves checked against
`SHASUMS256.txt`. If the binary doesn't match, the build fails.

### Installing

Each package manager installs with a frozen lockfile:

| | Install | Production dependencies |
|---|---|---|
| npm | `npm ci` | `npm prune --omit=dev` |
| pnpm | `pnpm install --frozen-lockfile --config.node-linker=hoisted` | `pnpm prune --prod` |
| Yarn 1 | `yarn install --frozen-lockfile` | `yarn install --production` |
| Yarn 2+ | `yarn install --immutable` (node-modules linker) | `yarn workspaces focus --all --production` |
| Bun | `bun install --frozen-lockfile --linker=hoisted` | `bun install --production`, keeping what the build generated in `node_modules`' dot folders (`node_modules/.prisma`) |

The package manager is the lockfile's. With lockfiles of more than one
manager, `package.json`'s `packageManager` picks among them. Without it,
homeport goes by the order it always had (bun, pnpm, Yarn, npm), and the
plan's `warnings` say which lockfile it used. A `packageManager` whose lockfile isn't there is refused,
and so is an app with no lockfile. If you set an install command, it
replaces the install, not what the image needs first (Bun or corepack). pnpm and
Yarn run through corepack, which is installed when the image lacks it
(Node 25 stopped shipping it). For a Node app, Bun is fetched into the Node
image and its checksum checked.

### The bundle

```
.homeport-bundle/
  bin                  node or bun, the pinned official binary
  .homeport/boot.mjs   runs before the app (--import / --preload)
  .homeport/start.mjs  only when the start script runs a package's command
  .homeport/writable   homeport-cache (Node only)
  homeport-cache/      Node's compile cache
  …                    what the framework or the app needs to run
```

The bundle holds only files: links are copied as the files they point to,
and a link out of the app's folder fails the build. There's no
`node_modules/.bin`. It holds
no source maps unless the app starts with `--enable-source-maps`. Its
`run` is the args to `bin`, for example
`--import ./.homeport/boot.mjs server.js`.

`boot.mjs` does three things before the app starts:

- It sets the framework's env defaults. Your own env wins. SvelteKit gets
  `PROTOCOL_HEADER=x-forwarded-proto`, so it knows its scheme from
  homeport's proxy, and `ADDRESS_HEADER=x-real-ip`, so
  `getClientAddress()` is the visitor's address. With adapter-bun's
  `envPrefix`, those names carry the prefix, and
  `<prefix>PORT` and `<prefix>HOST` are set from `PORT` and `HOST`.
- It turns on Node's compile cache in `homeport-cache/`. That's a writable
  folder of the release, so it survives the app sleeping and waking. The
  cache is flushed 5s and 60s after the app starts, because a stopped app
  is sent SIGTERM, which wouldn't flush it.
- If a server listens on `$PORT` on `localhost` only (Fastify's default),
  it's made to listen on every address instead, because nothing else in
  the sandbox can reach it. This covers `node:net` servers and, on Bun,
  its own `http` and `https` servers.

### Binding

homeport sets `PORT`, and sets `HOST` and `HOSTNAME` to `0.0.0.0`. Next.js,
Nuxt and TanStack Start (Nitro), SvelteKit (adapter-node and adapter-bun),
Astro and React Router read these. Your own server
must listen on `$PORT`.

### The start command, without a shell

The app runs as args to `bin`, without a shell. A start script with `&&`,
pipes or variables (`prisma migrate deploy && node server.js`) is refused,
with a message. So is one that sets a variable before its command
(`NODE_OPTIONS=… node server.js`), unless you've set a start command that
replaces it. Set the variable as an environment variable of the app
instead. `NODE_ENV=production`, `PORT=…` and `HOST=…` are dropped, because
homeport sets them anyway. A start script's runtime flags (`bun --smol`) are dropped when
another rule picks the other runtime. Set a start command instead, and run anything that comes
before it, such as migrations, as the release command:

```
release: node_modules/prisma/build/index.js migrate deploy
```

A command from a dependency is run by its file under `node_modules`, since
`node_modules/.bin` isn't shipped. It runs as the main module, so
`require.main` and `import.meta.main` are the command itself. On Node that's
`Module.runMain`. On Bun, `Bun.main` is set to the command before it's
required.

### The release command, detected

The release command runs before each release goes live, as args to the
app's `bin`, and its failure stops the deploy. When neither the build
settings nor `homeport.yaml` set one, it's the framework's obvious
migration: the plan's `release_detected` has it as you would set it, and
its `release_reason` says why. Detection gives one command at most.

| The app | Release command |
|---|---|
| Laravel (requires `laravel/framework`) | `php-cli artisan migrate --force` |
| rsc-kit with `src/commands/migrate.ts` (or `.tsx`, `.js`, `.mjs`, `.mts`) | `migrate` (`./app migrate`) |
| A bundle with a `migrate` or `db:migrate` script that runs a file | that file and its args (`node scripts/migrate.js` is `scripts/migrate.js`) |
| A bundle whose `migrate` or `db:migrate` script runs `prisma` or `drizzle-kit` (also through `bunx` or `npx`) | `node_modules/prisma/build/index.js …` or `node_modules/drizzle-kit/bin.cjs …` |
| A bundle with Prisma's migrations (`prisma/migrations/migration_lock.toml`) | `node_modules/prisma/build/index.js migrate deploy` |

A bundle ships `dependencies` only, so a tool in `devDependencies` isn't
there to run: the plan's `warnings` say to move it, or to set the release
command. So does a script that needs a shell, or runs another tool. An
rsc-kit app's binary is its own whatever builds it, so a build command,
output or kind you set leaves its `migrate` detected. A binary other than
rsc-kit's, a Go app and a static site get none.

### Several release commands

`release` may be a list. The commands run in order, each before the next,
and the first that fails stops the deploy: the deploy's log names it, and
the release before keeps serving. All of them together get the ten minutes
a release command gets. At most 10.

```
release:
  - php-cli artisan migrate --force
  - php-cli artisan db:seed --class=RolesSeeder --force
```

In the dashboard's build settings, put one command on each line. With the
CLI, give `--release` once for each: `homeport deploy --release "php-cli
artisan migrate --force" --release "php-cli artisan db:seed --force"`. One
command is still written as a string, in the build settings and the plan,
and several as a list. Each is checked as one command is.

To have no release command at all, choose **None** for it in the
dashboard's build settings, deploy with `homeport deploy --no-release
--save`, or set `release: none` (or `release: ""`, or `release: []`) in `homeport.yaml`. That
is apart from setting nothing, which detects one: the build settings keep
it as `"no_release": true`. The plan then has `"no_release": true`, a
`release_reason` saying who set it, and still the command detection found
in `release_detected`, which doesn't run. A command set in the build settings
wins over `homeport.yaml`'s none, and the build settings' none wins over
`homeport.yaml`'s command. A Laravel app still has its views cached on
release (below), with no command of its own after it.

### Overriding

- **Build command**: replaces the build. The bundle is still made from what
  it leaves.
- **Start command**: the args to `bin`.
- **Output**, or **kind** `binary`: you build the binary yourself, and the
  bundle isn't made. With no build script, set a build command too.
- **Kind** `static`: the output folder is served as a site.
- **Runtime** (`bun` or `node`): see above.

### Workspace members

An app folder whose lockfile is above it, in a bun, npm, pnpm or Yarn
workspace (`apps/web` in a monorepo), is a workspace member. Its workspace
is the nearest folder above it with a lockfile, and that folder must list
it: package.json's `workspaces`, or pnpm's `pnpm-workspace.yaml`. The
install runs at the workspace's root, only for the member where the package
manager can say so:

| Package manager | Install, at the workspace's root |
| --- | --- |
| Bun | `bun install --frozen-lockfile --filter ./<member>` |
| npm | `npm ci --workspace=<member> --include-workspace-root` |
| pnpm, Yarn | the whole workspace, as above |

Everything else happens in the member's folder: detection, the build, and
the output and artifact paths, which are relative to it. A bundle ships the
dependencies the workspace hoisted to its root, with the member's own over
them, and workspace packages are copied in as files. A Next.js member's
standalone server is at `.next/standalone/<member>/server.js`, since Next.js
traces from the workspace's root.

**The repository's root of a workspace.** With no root directory set, no
`homeport.yaml` at the root, and no build command, output or kind in the
build settings, a root whose `package.json` lists workspaces (or that has
`pnpm-workspace.yaml`) and isn't an app itself (no start script, no
framework) is planned as the app it builds:

1. The member its build script names: `bun --filter web build`,
   `bun run --filter web compile`, `pnpm --filter ./apps/web build`,
   `npm run build --workspace=apps/web`, `yarn workspace web build` or
   `turbo run build --filter=web`, by package name or folder. A script
   other than `build` that it runs there (`compile`) is the member's build.
2. Else the one member that's an app: a framework or server package, and a
   build or start script. With more than one, it's refused, naming them.

The plan's `root` is that member's folder, and its `warnings` say why it
was chosen. Set the root directory to build another.

With a build command, output or kind set at such a root (`cd apps/web &&
bun run compile`, output `apps/web/dist/app`), the root is planned as you
said, and detection reads the root's folder, not the app's: it finds no
queue or release command there. The plan's `suggested_root` is the member
that's the app - the one the output is in, else the one the root's build
script builds - and its `warnings` say to set the root directory to it,
with the build command and output relative to it.

Not handled yet: Yarn Plug'n'Play at runtime, and Deno.

## PHP and Laravel apps

A PHP app ships as a **bundle**: its files, installed with Composer
(`--no-dev`), its front-end assets built with Bun (`bun run build`), and
FrankenPHP as `bin`. Nothing is compiled per app. It's served by the
bundle's own config for FrankenPHP (`run --config .homeport/web.caddyfile
--adapter caddyfile`: `public/` on `$PORT`), or, when it requires
`laravel/octane`, through Octane's worker in FrankenPHP's worker mode.

Either way the app sees its visitor as homeport's proxy says - the
visitor's address (`REMOTE_ADDR`, so `request()->ip()`) and https
(`HTTPS=on`, so `isSecure()` and `https://` URLs) - whatever proxies it
trusts, with nothing of the app's changed: no `trustProxies` call, no
`config/trustedproxy.php`. From anywhere but homeport's proxy, the
forwarding headers a request came with are dropped.

### Laravel's caches

A Laravel app (one that requires `laravel/framework`) is built with
`php artisan event:cache` and `route:cache`. If artisan can't boot without
the app's environment, the build goes on without them and the log says so.

Its config is cached as the app starts, not at build: it holds the
environment, and a change to the environment restarts the app without a
build. The web and each process start through the bundle's
`.homeport/start.php`, which runs `php artisan config:cache` with the
environment it was started with, then the command. The web goes through it by
the bundle's `.homeport/wrap`; a process's command shows it:

```
php-cli .homeport/start.php ./bin php-cli artisan queue:work
```

Set only your own command, for example `php-cli artisan queue:work`.
homeport adds the runner itself.

Each environment's cache is a file of its own,
`bootstrap/cache/homeport-config-<hash>.php`, which Laravel reads through
`APP_CONFIG_CACHE`. So a restart with a changed environment caches the new
one at once, and one cached already (a wake, another copy) isn't cached
again. If `config:cache` fails, for example on a closure in your config, the
app runs with its config uncached and its log says why. Set
`HOMEPORT_CONFIG_CACHE=off` to switch it off. Cron jobs and the release
command run with the config uncached. As with any cached config, `env()`
returns only the real environment outside your config files, not `.env`.

Its Blade views are compiled once per deploy, by the release command, not
at build and not as the app starts. Blade names a compiled view by its
template's absolute path, and the build's path isn't the one the app serves
from, but the release runs in the app's own sandbox at that path. The
release command runs `.homeport/release.php`, which runs your own release
command first (your migrations), then `php artisan view:cache` into
`storage/framework/views`, the release's own folder:

```
php-cli .homeport/release.php ./bin php-cli artisan migrate --force
```

With no release command set, yours is `php-cli artisan migrate --force`
(see *The release command, detected*). Set only your own command as the
release command. Never set the wrapped line above: homeport adds the
wrapper itself. A release command the sandbox can't run, such as one
starting with `./bin`, is refused at build, saying what to write.

With several release commands, only the last runs through the wrapper, so
the views are compiled once, after every command has run.

If your release command fails, the deploy stops, as before. If
`view:cache` fails, the deploy goes on, and views compile as they're
first shown. None of this happens when you set your own build command.

Views aren't precompiled when the release command doesn't run: a bundle
uploaded from CI, or the live release shipped again (a change of settings,
or homeport moving it). There, each view compiles the first time it's
shown, and is kept for the rest of that release.

### Inertia server-side rendering

An app renders its Inertia pages before sending them when it requires
`inertiajs/inertia-laravel` and has an SSR build: a `build:ssr` script in
`package.json`, or an SSR entry in its Vite config (`ssr: 'resources/js/ssr.jsx'`
for `laravel-vite-plugin` or `@inertiajs/vite`). Then:

- **The build** runs `bun run build:ssr` in place of `bun run build`, since
  that script builds both. With no `build:ssr`, it runs `bun run build`, then
  `vite build --ssr`.
- **What runs** is one of three things, as for a JavaScript app:
  - **The binary your SSR build compiles.** If `build:ssr`, or a script it
    runs, has `bun build --compile`, that binary runs as it is, and no
    runtime ships. Since the build's own Bun is the Alpine (musl) one, the
    command is run once more on the pinned Bun for the Linux the app runs
    on (glibc), checked by its sha256, with the build's architecture as its
    `--target`. A `-musl` target is refused.
  - **Otherwise the SSR bundle** that Vite made (`bootstrap/ssr/ssr.js`,
    `app.js`, `ssr.mjs` or `app.mjs`, in the order Inertia looks), bundled
    again into one file with every package it imports, at
    `bootstrap/ssr/ssr.mjs`, on Node or Bun (below). No `node_modules`
    ships, and a cold start reads one file.
- **It runs beside the web, in the same sandbox**, so PHP reaches it at
  Inertia's default address, `http://127.0.0.1:13714`, with nothing to
  configure, whichever of the three it is. homeport starts the web through
  `.homeport/beside.php`, a small supervisor in the app's own PHP. It starts
  what `.homeport/beside` lists (the renderer), then the web. A renderer
  that exits is started again after a second, then longer, up to 30
  seconds. A stop reaches the web, and when the web exits, so does the
  renderer. Each copy of the app has its own. An app that sleeps when idle
  sleeps and wakes with its renderer.
- **While the renderer isn't answering** (it failed, or a request came in
  as it started), Inertia renders those pages in the browser instead, and
  the app logs why.

**Its memory is the app's.** The renderer runs within the app's memory,
beside the web: about 60-70 MB for a small app, more as its pages grow. Size
the app with that in mind (the plan says so). An app at the smallest size,
256 MB, renders, with less room left for the web.

- **On Node**, its heap is capped at a quarter of the app's memory, and at
  least 64 MB (`--max-old-space-size`). Past that, it's Node that stops,
  not the web. Its compile cache is kept with the release, so a cold start
  after the first doesn't compile the bundle again.
- **On Bun**, and for a compiled renderer, it's tuned to collect garbage
  sooner (`--smol`, and the JavaScript engine's RAM size set to a quarter
  of the app's memory), but it isn't capped. A renderer that grows past
  what's left of the app's memory takes the app down with it, and it's
  restarted. If that's a risk, run the renderer on Node.

**Which runtime: Node or Bun.** The rules are a JavaScript app's (see
*Which runtime* above), applied to the SSR renderer:

1. The build settings' runtime, or `runtime:` in `homeport.yaml`.
2. `package.json`'s `engines`, when it names only `bun` or only `node`.
3. A version file: `.bun-version` means Bun; `.nvmrc` or `.node-version`
   means Node.
4. What the project runs its SSR bundle with: a `package.json` script that
   runs `bootstrap/ssr/…` (`bun bootstrap/ssr/ssr.js`), else a Composer
   script's `inertia:start-ssr --runtime=bun` (or `node`).
5. A Bun lockfile (`bun.lock`, `bun.lockb`).
6. Otherwise Node.

Unless the renderer is compiled, the pinned official binary ships in the
bundle as `.homeport/node` or `.homeport/bun`, checked against its
sha256: Node from nodejs.org's tarball, at the version `.nvmrc`, `.node-version` or `engines.node` asks
for (24 by default), or Bun 1.4.4. The plan says which runtime it chose and
why (`runtime`, `runtime_version`, `runtime_reason`; a compiled renderer
is `bun`, and its reason says it's compiled), and `ssr` is `inertia`.

If you set a build command, it replaces the build and the bundle, so the
renderer isn't added.

### Laravel Reverb

An app that requires `laravel/reverb` runs Reverb as a process of its own,
named `reverb`:

```
reverb: php-cli artisan reverb:start --host=$HOST --port=$PORT
```

On each of the app's domains, WebSocket connections to `/app/…` and
signed calls to Reverb's API on `/apps/…`, which Laravel broadcasts
through, go to it. Other requests under `/app` and `/apps` stay the app's.
Open connections stay open when homeport's routing changes. A deploy
restarts Reverb, and Echo reconnects on its own.

homeport sets the variables Laravel's Reverb and Echo configuration read,
unless you set them yourself:

| Variable | Value |
|---|---|
| `REVERB_APP_ID`, `REVERB_APP_KEY`, `REVERB_APP_SECRET` | generated for the environment, once |
| `REVERB_HOST` | the app's domain |
| `REVERB_PORT` | `443` |
| `REVERB_SCHEME` | `https` |
| `BROADCAST_CONNECTION` | `reverb` |

The build gets `VITE_REVERB_APP_KEY`, `VITE_REVERB_HOST`,
`VITE_REVERB_PORT` and `VITE_REVERB_SCHEME` from those, as Laravel's
`.env.example` does, so Echo connects as generated by
`php artisan install:broadcasting` with no edits. Any variable starting
with `VITE_` that you set is given to the build too, since Vite builds
it into the page. While `REVERB_HOST` is still the value homeport set, a
change of the app's domain changes it too, at the next deploy. One you set
yourself is left as it is.

Reverb's process has 128 MB of its own, not the app's whole size, and is
billed as any process is, for its memory the whole time it runs.
Processes run only while the app is always on (at least 1 copy), so
Reverb does too, and an open connection never finds it asleep. **On a plan
where apps sleep when idle (Free), processes don't run, so Reverb doesn't
either, and Echo won't connect.**

To run Reverb with options of your own, or another size, declare a process
named `reverb`: yours replaces homeport's. It must listen on `$PORT`
(`--port=$PORT`) and every address (`--host=$HOST`), since that's where its
requests go. It counts toward the four processes an app may have; when the
app has four already, Reverb isn't run, and the plan says so. To not run
Reverb at all though the app requires it, set `BROADCAST_CONNECTION` to
another connection (`log`, `null`, …): the next deploy leaves its process
out.

### Processes and their port

Each process has a port of its own. Like the start command, a process
may name `$PORT` and `$HOST`, and no other variable.

## Python apps

Django, FastAPI and Flask apps ship as a **bundle whose `bin` is the
interpreter**: python-build-standalone's CPython, pinned by sha256, with the
standard library and the app's dependencies in `lib/python3.X/` beside it.
There's no virtualenv: the release is a whole Python wherever it runs. The
start command, the release command and each process are args to it. The
design, and what's still open, is in
[design/python.md](../design/python.md).

| Files | Installed with |
|---|---|
| `pyproject.toml` + `uv.lock` | `uv export --locked --no-dev`, then `uv pip install` of exactly that. A `uv.lock` that `pyproject.toml` has moved past fails the build: run `uv lock` and commit it |
| `requirements.txt` | `uv pip install -r requirements.txt` |
| `pyproject.toml` alone, `poetry.lock`, `Pipfile` | refused, with how to move to `uv.lock` or `requirements.txt` |

A Python app beside a `package.json` is Python only when a Python framework
is recognised. When that `package.json` has a `build` script, its front-end
is built with homeport's pinned Bun (1.4.4, fetched by its sha256, as a
Laravel app's is): `bun install` (`--frozen-lockfile` with a `bun.lock`),
then `bun run build`, after the Python dependencies and before
`collectstatic`, so Django collects what it built. `node_modules` never
ships.

| Framework | Recognised by | Start command | Release command |
|---|---|---|---|
| Django | `manage.py` and `django` | `-m gunicorn --bind 0.0.0.0:$PORT -c .homeport/gunicorn.conf.py <project>.wsgi`; with uvicorn and no gunicorn, `-m uvicorn ... <project>.asgi:application` | `manage.py migrate --noinput` |
| FastAPI | `fastapi`, and `app = FastAPI(` at the top of `main.py`, `app.py`, `app/main.py`, ... | `-m uvicorn --host 0.0.0.0 --port $PORT main:app` | `-m alembic upgrade head`, with `alembic.ini` and `alembic` |
| Flask | `flask`, and `app = Flask(` in the same files or `wsgi.py` | `-m gunicorn --bind 0.0.0.0:$PORT -c .homeport/gunicorn.conf.py app:app` | the same, for Alembic (Flask-Migrate) |
| anything else | `pyproject.toml` or `requirements.txt` | set it: args to python, like `-m myapp` | none |

When the app's dependencies have no gunicorn or uvicorn, the build installs
a pinned one. A Django build runs `manage.py collectstatic --noinput`, and
goes on without it, saying so, if it fails. Serving `/static/` is the app's
job: add WhiteNoise (the build warns when it's missing).

gunicorn runs 2 worker processes of 4 threads each (gthread), from a
config file homeport writes into the bundle (`.homeport/gunicorn.conf.py`).
The app's variables win: `WEB_CONCURRENCY` sets the workers (a value that
isn't a positive number is 2), and `GUNICORN_CMD_ARGS` (`--threads 8`)
overrides either. An app with its own `gunicorn.conf.py`, or its own start
command, gets no homeport config: its own says.

**Python version**: the build settings' (a line, like `3.13`), else
`.python-version`, else `runtime.txt`, else the
default (3.14) if `requires-python` allows it, else the newest it allows.
Offered: 3.12, 3.13, 3.14 and 3.15, each at homeport's pinned patch.

The release is read-only, so everything is compiled at build. Data goes to
a database or object storage, never into the app's folder. An app with a
top-level `bin/` folder is refused, because `bin` is the interpreter.

## Your visitor's IP

Every request reaches your app through homeport's proxy, so the socket's
address is the proxy's, not the visitor's. The proxy sets `X-Real-IP` and
`X-Forwarded-For` to the visitor's address - one address, never a list -
and `X-Forwarded-Proto` to `https` or `http`.

- **PHP (Laravel, Symfony, any PHP app):** automatic. The bundle's own
  config for FrankenPHP (`.homeport/web.caddyfile`) sets `REMOTE_ADDR` to
  the visitor's address and `HTTPS=on` for an https visit, so
  `request()->ip()`, `isSecure()` and `https://` URLs are right whatever
  proxies the app trusts. You don't need `trustProxies` or
  `config/trustedproxy.php`.
- **SvelteKit (adapter-node, adapter-bun):** automatic. The bundle sets
  `ADDRESS_HEADER=x-real-ip`, so `getClientAddress()` is the visitor's
  address. Your own `ADDRESS_HEADER` wins.
- **Next.js, Nuxt, Remix, React Router, Astro:** read the `x-real-ip`
  header (or `x-forwarded-for`, the same single address). For example
  `(await headers()).get('x-real-ip')` in Next.js,
  `getRequestHeader(event, 'x-real-ip')` in Nuxt,
  `request.headers.get('x-real-ip')` in a Remix or React Router loader, and
  `Astro.request.headers.get('x-real-ip')` in Astro.
- **Express, Fastify, Koa, Hono, Bun.serve, Go, Python:** the socket
  address (`req.socket.remoteAddress`, Hono's `getConnInfo`,
  `server.requestIP(req)`, Go's `r.RemoteAddr`, `REMOTE_ADDR`) is
  homeport's proxy. Read `X-Real-IP`, or have the framework trust exactly
  one proxy:
  - Express: `app.set('trust proxy', 1)`, then `req.ip`
  - Fastify: `Fastify({ trustProxy: 1 })`, then `request.ip`
  - Koa: `new Koa({ proxy: true, maxIpsCount: 1 })`, then `ctx.ip`
  - Flask: `app.wsgi_app = ProxyFix(app.wsgi_app, x_for=1, x_proto=1)`
    (`werkzeug.middleware.proxy_fix`), then `request.remote_addr`
  - Hono, Bun.serve, Go, Django, FastAPI: read the header, for example
    `c.req.header('x-real-ip')`, `req.headers.get('x-real-ip')`,
    `r.Header.Get("X-Real-IP")`, `request.META['HTTP_X_REAL_IP']`,
    `request.headers['x-real-ip']`

**Why it's safe.** Each hop only believes the hop right before it. A
visitor can send its own `X-Real-IP` or `X-Forwarded-For`, but homeport's
proxy overwrites both with the address the connection came from. Your app
is reached only through that proxy, so trusting exactly one hop (or
reading `X-Real-IP`) always gives the address homeport saw.

## Background jobs (queues)

The plan's `queue` says how the app's background jobs run, when it has any.
homeport never reads the queue itself: the framework's own worker does, in
the backend its driver names (`backend`: `postgres`, the environment's
database; `redis` later). There are two ways to run it, chosen in the
environment's settings, not here:

- **On demand** (every plan): a **drain** runs the `drain` command, args to
  `./bin` in a sandbox of its own, when there may be work. It works the
  queue until it's empty, for at most `timeout`, and exits. Its last line
  says what's left, so homeport knows when to drain again:

  ```
  homeport-queue: processed=12 failed=1 waiting=0 next_due=1793481600
  ```

  homeport adds `--max-time=<timeout less a minute>` to the command. A
  drain that prints no line still works; it just can't say when a delayed
  job is due.
- **Always on** (paid plans): `worker` runs as a process, and gets
  `stop_grace` after SIGTERM to finish the job it holds.

Detection never adds a process: running a worker process and draining on
demand would work the same jobs twice.

| The app | Seen by | `backend` | `drain` | `worker` |
|---|---|---|---|---|
| Laravel | a class in `app/` that `implements ShouldQueue`, or the `jobs` table's migration | `postgres` | `php-cli .homeport/queue.php ./bin` | `php-cli artisan queue:work --tries=3 --timeout=60`, `stop_grace: 90s` |
| Laravel Horizon | `laravel/horizon` in `composer.json` | `redis` | none: Horizon supervises its own workers | `php-cli artisan horizon` |
| pg-boss | `pg-boss` in `package.json` | `postgres` | the app's: a `queue:drain` script, or rsc-kit's `queue:drain` command | a `queue:work` script; rsc-kit's `queue:work` or `work` command |
| Graphile Worker | `graphile-worker` in `package.json` | `postgres` | a `queue:drain` script, else `node_modules/graphile-worker/dist/cli.js --once` | `node_modules/graphile-worker/dist/cli.js` |
| BullMQ, Bee-Queue | `bullmq` or `bee-queue` in `package.json` | `redis` | none | none |

A Laravel bundle that homeport builds has `.homeport/queue.php`. It boots
the app once and runs `artisan queue:work --stop-when-empty` in the same
process, and runs it again while jobs are ready and time is left. It counts
`processed` and `failed` from Laravel's queue events. On the `database`
driver it also reads `waiting` (jobs ready now) and `next_due` (the
earliest delayed job, retry or lapsed reservation) from the jobs table; on
any other driver both are 0. Options after `./bin` go to `queue:work`
(`--queue=high,default`). A build command you set makes its own bundle,
which doesn't have the runner, so its drain is `php-cli artisan queue:work
--stop-when-empty`. `retry` (`php-cli artisan queue:retry all`) retries the
failed jobs.

A drained queue's jobs wait for the next drain. The
[`homeport/laravel`](https://github.com/homeport-sh/laravel) package
(Laravel 12 and 13) kicks a drain when a job is queued, so on paid plans it
runs within seconds; without it in `composer.json`'s `require`, the plan's
`note` suggests it. Horizon runs always on, so it gets no such note.

A JavaScript app's drain is one it names. A `queue:drain` script runs as
the bundle runs a script's file (`bun scripts/drain.ts` is
`scripts/drain.ts`); a script that needs a shell can't, and the plan's
`note` says so. An rsc-kit app's command `src/commands/queue:drain.ts` runs
as `./app queue:drain`, whatever builds it: a build command, output or kind
you set (or `homeport.yaml`'s `build`) leaves its queue detected, since the
binary is still the app's. A bundle's build command does too; a site has no
queue. pg-boss has no drain of its own: `drain()` from
`@homeport-sh/queue` is one.

```ts
// src/commands/queue:drain.ts
import { PgBoss } from "pg-boss";
import { drain } from "@homeport-sh/queue";
import { handlers } from "../jobs";

export default async () => {
  const boss = new PgBoss(process.env.DATABASE_URL!);
  await boss.start();
  await drain(boss, { handlers });
  await boss.stop();
};
```

It works each queue a handler is given for until a pass finds nothing,
then prints the line from `waiting` and `next_due` in pg-boss's job table.
Without a drain the queue runs always on only, and the `note` says how to
add one. pg-boss runs `schedule()` jobs only while a worker runs, so an app
that calls it is told to move them to cron jobs to drain on demand. A
`queue:retry` script or command is `retry`. Graphile Worker's `--once` is
its drain; its `crontab` isn't run by one. BullMQ and Bee-Queue need Redis,
which homeport doesn't offer yet.

The plan's `source` says where the queue came from: `detected`, or
`config`. `queue:` in `homeport.yaml` says it instead. `none` (or `""`)
means no queue. A map sets `backend`, `drain`, `timeout` and `retry` over
what was detected:

```yaml
queue:
  backend: postgres     # postgres; redis later
  drain: php-cli artisan queue:work --stop-when-empty --queue=high,default
  timeout: 20m          # 1m to 60m (10m by default)
  retry: php-cli artisan queue:retry all
```

`drain` and `retry` are args to `./bin`, run without a shell, as the
release command is.

## System packages

An app can ship with executables from a short list homeport curates and
patches, chosen in the build settings or in `homeport.yaml`:

```yaml
packages: [lightpanda]
```

The build settings' list, when it has any, replaces the file's. Only a
package on the list is accepted (`buildplan.Packages`), each at most once,
at most eight; a static site can't have any, since nothing of it runs.
The build unpacks each package into the release, at
`.homeport/pkgs/<name>/`, and the app finds its executables on `PATH`.
Nothing is installed on the server: the release carries them.

| Package | Version | Provides | Licence |
|---|---|---|---|
| `lightpanda` | 1.0.0 | `lightpanda`, a headless browser (CDP) | AGPL-3.0 |
| `fonts` | 2.37 | no executable: DejaVu Sans, Sans Mono and Serif, and a fontconfig config | Bitstream-Vera |

Lightpanda is published (`ghcr.io/homeport-sh/pkg-lightpanda`). It's unlisted
while its licence is confirmed: you can choose it by name, but the
dashboard's list doesn't show it. homeport turns its telemetry off
(`LIGHTPANDA_DISABLE_TELEMETRY=true`) wherever it ships.

`fonts` is a package of data alone, for an app that draws text: sharp's
text and SVG (libvips, librsvg, pango), or anything else on fontconfig. A
server here has no fonts of its own, so without it text renders blank. It
ships DejaVu's core faces (the set Debian calls `fonts-dejavu-core`) and a
fontconfig config, at `.homeport/pkgs/fonts/share/`. homeport sets
`FONTCONFIG_FILE` to that config wherever it ships, unless the app sets
`FONTCONFIG_FILE` itself. `sans-serif`, `serif` and `monospace` are
DejaVu's; any other family asked for (Arial, Helvetica) falls back to DejaVu
Sans. It isn't published yet, so it can't be chosen until it is.
