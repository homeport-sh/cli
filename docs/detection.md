# How homeport detects a build

`homeport build-plan [dir]` reads a repository and prints, as JSON, how it
builds and what it produces. It runs nothing from the repository. The
platform's builders run the same code, so what it prints is what a hosted
build does.

What a person sets in the app's build settings wins over `homeport.yaml`,
and `homeport.yaml` wins over detection.

| Files | Toolchain | Produces |
|---|---|---|
| `go.mod` | Go (`golang:<go.mod's version>`) | a binary |
| `composer.json` + `composer.lock` | PHP on homeport's FrankenPHP base | a bundle, FrankenPHP as `bin` |
| `package.json` + a lockfile | Node or Bun (see below) | a bundle, a static site, or a binary when the build compiles one |
| `index.html` alone | none | the folder, as a static site |

## JavaScript apps

A JavaScript app runs what its build produces. A server ships as a
**bundle**: the framework's documented production output, or the app's
files with only its production dependencies, plus the runtime the project
uses as `bin`. homeport runs the app the way its start script does.

It ships a **binary** only when the project's own build compiles its
server. That happens in three ways:

- **A tool that compiles it.** next-bun-compile is a Next.js build adapter
  (set by `adapterPath` in next.config, or `NEXT_ADAPTER_PATH=next-bun-compile`)
  and writes `./server`. svelte-smol is a SvelteKit adapter that writes
  `<out>/server`, or the `outfile`/`name` you give it. On SvelteKit 2 it
  serves `client/` and `prerendered/` from beside the binary, so homeport
  ships that folder as a bundle, with the binary as its `bin`.
- **`bun build --compile` of the framework's own server output**, such as
  Nitro's `.output/server/index.mjs` from `nuxt build --preset bun`.
  homeport runs the file that `--outfile` names.
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
| SvelteKit | `@sveltejs/adapter-node` | the app with production dependencies | `build/index.js` (or the adapter's `out`, from `svelte.config.js` or, in SvelteKit 3, the Vite config) |
| Astro | `@astrojs/node`, standalone mode | the app with production dependencies | `dist/server/entry.mjs` |
| React Router | `@react-router/*` | the app with production dependencies | the start script (`react-router-serve …`) |
| Remix | `@remix-run/*` | the app with production dependencies | the start script (`remix-serve …`) |
| NestJS | `@nestjs/core` | the app with production dependencies | `start:prod`, else `dist/main.js` |
| Elysia | `elysia` | the app with production dependencies | the start script, else `dev`, else `module`/`main` |
| Hono, Fastify, Express, Koa | the package | the app with production dependencies | the start script, else `main` |
| Bun, Node | nothing above | the app with production dependencies | the start script, else `main` |

Static sites are still detected first: SvelteKit with `adapter-static`,
Astro without a server adapter, Vite, and Next.js with `output: "export"`
(served from `out/`).

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
   Node wins, as with any app.
6. **Otherwise Node.**

The lockfile decides only how dependencies install, never what runs. A
Next.js app with `bun.lock` and `next start` installs with Bun and runs on
Node. `packageManager` names an installer, so it isn't read as a runtime
either.

The plan says which runtime it chose and why: `runtime`, `runtime_version`,
`runtime_reason` (for example "its start script runs bun src/index.ts").

**Bun compatibility.** homeport runs a framework on Bun only when your
project does. Next.js, Nuxt, NestJS and the other Node frameworks target
Node and run on Node by default. Running them with `bun --bun`, or with the
runtime set to `bun`, uses Bun's Node compatibility, which is incomplete.
Test that before you rely on it. Elysia and Bun's own `Bun.serve` need Bun.

### Versions, pinned

- **Node.** homeport runs the latest release of 22, 24 or 26, chosen from
  `.nvmrc`, `.node-version` or `engines.node`, in that order. The current
  LTS is 24 and is the default. A bare version (`22`, `v22.11.0`) runs that
  line's pinned release. `lts/*` runs the default, `lts/<codename>` runs
  that line, `node` runs the newest, and a range (`>=20`, `^22`) runs the
  default if it's in the range, else the newest that is. Any other version
  is refused.
- **Bun.** homeport runs 1.4.2. A version the project names
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
  `PROTOCOL_HEADER` and `HOST_HEADER`, so it knows its origin from
  homeport's proxy.
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
Nuxt (Nitro), SvelteKit, Astro and React Router read these. Your own server
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

### Overriding

- **Build command**: replaces the build. The bundle is still made from what
  it leaves.
- **Start command**: the args to `bin`.
- **Output**, or **kind** `binary`: you build the binary yourself, and the
  bundle isn't made. With no build script, set a build command too.
- **Kind** `static`: the output folder is served as a site.
- **Runtime** (`bun` or `node`): see above.

Not handled yet: workspace members, whose lockfile is above the app's
folder (refused, saying so), Yarn Plug'n'Play at runtime, and Deno.

## PHP and Laravel apps

A PHP app ships as a **bundle**: its files, installed with Composer
(`--no-dev`), its front-end assets built with Bun (`bun run build`), and
FrankenPHP as `bin`. Nothing is compiled per app. It's served with
`php-server --root public --listen :$PORT`, or, when it requires
`laravel/octane`, through Octane's worker in FrankenPHP's worker mode.

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
    runtime ships. With no `--target`, the command is run once more with
    `--target=bun-linux-x64` (or `-arm64`), so the binary is for the Linux
    the app runs on (glibc). A `-musl` target is refused.
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

The renderer's memory is part of the app's.

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
bundle as `.homeport/node` or `.homeport/bun`, checked against its sha256: Node from nodejs.org's
tarball, at the version `.nvmrc`, `.node-version` or `engines.node` asks
for (24 by default), or Bun 1.4.2. The plan says which runtime it chose and
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

Requests to the app's `/app` and `/apps` paths, on each of its domains, go
to it: WebSocket connections and the HTTP API that Laravel broadcasts
through. homeport sets the variables Laravel's Reverb and Echo configuration
read, unless you set them yourself:

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
it into the page. If you change the app's domain, set `REVERB_HOST` to it,
or remove it and the next deploy sets it again.

Reverb is billed as any process is, for its memory the whole time it runs.
Processes run only while the app is always on (at least 1 copy), so
Reverb does too, and an open connection never finds it asleep. To run
Reverb with options of your own, declare a process named `reverb`: yours
replaces homeport's. It counts toward the four processes an app may have.

### Processes and their port

Each process has a port of its own. Like the start command, a process
may name `$PORT` and `$HOST`, and no other variable.
