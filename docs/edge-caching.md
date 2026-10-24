# Edge caching on Homeport

An app on Homeport sleeps when nobody visits it, and the first request after
that wakes it. A page that's the same for every visitor shouldn't need the app
to wake. This page says which pages Homeport answers without the app, for each
framework, and what a framework of your own adds so its pages get the same
treatment.

Nothing here needs a setting or a change to your app.

## Prerendered pages and assets: SvelteKit, Astro, Next.js, React Router

When the build detects one of these frameworks, it copies the pages and assets
the framework prerendered into the bundle, at the URLs the framework's own
production server answers them. The host then answers them straight from the
release. The app doesn't wake for them and never sees the request.

| Framework | Automatic | What's answered without the app |
|---|---|---|
| SvelteKit, `@sveltejs/adapter-node` 5 and 6 | yes | prerendered pages (`export const prerender = true`), prerendered endpoints (`/api/data.json`), `static/` files, `_app/immutable/` assets |
| Astro, `@astrojs/node` with `mode: 'standalone'` | yes | prerendered pages, `public/` files, `_astro/` assets |
| Astro, `@astrojs/node` with `staticHeaders: true` | assets only | `_astro/` assets. The adapter adds headers of its own to each page (a CSP), so the pages stay with the app. |
| Next.js, `output: 'standalone'` | assets only | `/_next/static/` assets. A URL your middleware (or proxy) matches, or that your config's `headers`, `redirects` or `beforeFiles` rewrites match, stays with the app. With `assetPrefix` or `i18n` set, everything goes to the app. |
| React Router 7 and Remix 2 (Vite), started with `react-router-serve` or `remix-serve` | assets only | `assets/` under the build's `publicPath`. Remix's classic compiler and React Router's RSC build go to the app. |
| SvelteKit, `@sveltejs/adapter-bun` | no | everything goes to the app |
| Nuxt, Nitro, TanStack Start, Express and other servers | no | everything goes to the app. Their request hooks or route rules can run before any file. |
| Laravel (FrankenPHP) | no | everything goes to the app. It sends no `Cache-Control` for its files, so serving them from the host would change their headers. |
| rsc-kit, Next.js (next-bun-compile) | through the edge contract | see [A framework with a server of its own](#a-framework-with-a-server-of-its-own) |
| An app with its own start command (`run:` in `homeport.yaml`, or the start command in its settings) | no | everything goes to the app. Its command might run code of its own first. |
| A static site (no server) | already | the whole site is files, and it never sleeps |

The build log says what it found:

```
homeport: served without the app, before any of its code runs (as adapter-node serves them): 4 prerendered page(s), 3 file(s), 17 immutable asset(s)
homeport:   pages: / /about /blog/one /blog/two
homeport:   anything else - server-rendered pages, other methods, the other trailing-slash form - is the app's, and wakes it
```

When the build skips a file the framework would serve, it adds a line for
it, such as `left to the app: /LICENSE - no file extension to give it
a type`.

When the build can't read the framework's output, it says why and leaves
every page to the app, which wakes for them as before:

```
homeport: prerendered pages are served by the app, which wakes for them: the adapter is @astrojs/vercel, not @astrojs/node
```

That message never fails the build.

### Trailing slashes, base paths and i18n

A page is answered at exactly the URL forms the framework answers with that
page. Any other form goes to the app, which redirects it as it always has.

- **SvelteKit.** `trailingSlash` decides which form a page gets: `/about` by
  default, `/about/` with `'always'`. The other form is the adapter's 308
  redirect, so it goes to the app. `paths.base` is part of every URL
  (`/docs/about`). The base on its own (`/docs`) goes to the app.
- **Astro.**
  - `trailingSlash: 'ignore'` answers a page at both `/about` and `/about/`.
  - `'never'` answers only `/about`.
  - `'always'` answers only `/about/`. Files with an extension (`/robots.txt`) are answered as they are under every setting.
  - With `build.format: 'file'` or `'preserve'`, `/about` is `about.html`, as the adapter serves it.
  - `base` is part of every URL. The base on its own without a slash goes to the app.
- **i18n.** A localized page that's prerendered (`/fr/about`) is a page like
  any other. A redirect that picks a locale from `Accept-Language` or a cookie
  runs in the app (Astro's i18n middleware, SvelteKit's `reroute` or `handle`
  hook, Paraglide), so any URL that redirects goes to the app. Neither
  framework's server runs that code for a page it prerendered, and the host
  doesn't either.
- **Dotfiles.** These go to the app, with one exception: `/.well-known/` is
  answered where the framework answers it (adapter-node 6 and Astro). Under
  adapter-node 5 it goes to the app.

### Headers

- `Content-Type` comes from the file's extension.
- `Cache-Control`:
  - Fingerprinted assets (`_app/immutable/`, `_astro/`, `_next/static/`, `assets/`) get `public, max-age=31536000, immutable`, the same header the framework's server sends for them.
  - Pages and every other file get `no-cache`. A browser or CDN may keep them but asks again every time, so a deploy shows at once.
- `ETag` and `Last-Modified` are sent. A request with `If-None-Match` or `If-Modified-Since` that still matches gets a 304 with no body.
- Responses are compressed, as the app's are. The build writes each text file's Brotli (quality 11) and gzip copies once, on the builder, and the host sends the copy the browser accepts, with `Vary: Accept-Encoding` and an `ETag` of its own. The edge keeps the same copies, so neither the host nor the edge compresses these files again. A file too small or one that doesn't shrink has no copy and is compressed as it's sent (zstd or gzip). `Range` requests work.
- The response headers you set in `homeport.yaml` (`headers:`, see [headers.md](headers.md)) apply to these answers as they do to the app's.

### Deploys

Each release carries its own pages. When a deploy goes live, the host answers
from the new release at once and the old release's pages are gone:

- A page the new build dropped goes to the app.
- A page it changed is answered as changed.

There's no cache to purge and no window where the old build's page is served.
Rolling back serves the older release's pages again.

### Only GET and HEAD

A `POST`, `PUT` or any other method goes to the app, even at a prerendered
URL, so a form action or an endpoint's other methods work as before.

### At the edge too

The same pages are also kept at Homeport's edge, nearer your visitors. The
edge reads them from the same files the host serves, so nothing is built or
stored twice:

- The build writes `.output/edge.json` listing the prerendered pages, up to
  200 of them, shortest URL first. The build log says how many:

  ```
  homeport: edge.json: 4 of the 4 page(s) kept at the edge as well
  ```

  Pages past the 200 are still answered by the host without the app; only
  the edge's copy is capped.
- The edge fetches each listed page and the assets it links from the host's
  files, never from the app, so filling the edge doesn't wake a sleeping
  app. A URL the host has no file for gets a 404 from the host, not the
  app, and the edge doesn't keep it.
- Nothing to switch on: no setting, no token, no endpoint in your app.
- When a new release goes live, the edge drops the old release's pages and
  fetches the new ones.

If your framework writes its own `.output/edge.json` (see
[A framework with a server of its own](#a-framework-with-a-server-of-its-own)),
the build keeps it as it is and the host still answers the prerendered
files.

## Security

**A prerendered page is answered without the app only where the framework's
own production server also answers it without running any of the app's code
at request time.**

That code includes hooks, middleware, redirects, response headers, locale
negotiation, base-path rewrites and trailing-slash redirects. If any of it
would run for a URL, the host doesn't answer that URL from files. The app
does, and the build log names the URL and the reason.

So the host can't skip your auth, because the framework's own server doesn't
run your auth for these files either. A page you guard in a hook or
middleware is a page the framework renders on each request. It isn't
prerendered, so it's never copied. The checks below come from the adapters'
sources, in the versions the build detects:

- **SvelteKit, adapter-node 6.0.0.**
  - `src/handler.js:188` is `handler = (req, res, next) => serve(req, res, () => ssr(req, res, next))`: the asset table answers first, and SvelteKit (and with it `handle`, `reroute` and every hook) only runs in `ssr`, for a URL the table doesn't have.
  - The table is built in `src/static.js:138-151`: immutable caching only under `/<appDir>/immutable/`, and client files win over prerendered ones.
  - `src/static.js:175-176` answers `files.get(pathname)`, else calls `next()`.
  - The build reads that same table (`build/adapter-node.js`), and nothing else.
- **SvelteKit, adapter-node 5.5.7.**
  - `files/handler.js:240-242` is `sequence([serve(client), serve_prerendered(), ssr])`: sirv on `client/`, then the prerendered folder for the exact paths in SvelteKit's prerendered set (`:79`, `prerendered.has(pathname)`), then SvelteKit.
  - The other trailing-slash form is a 308 redirect (`:85`), so it goes to the app.
- **Astro, @astrojs/node 11.1.9 (standalone).**
  - `dist/standalone.js:44-55` runs `staticHandler(req, res, () => appHandler(req, res))`: files in `dist/client` answer first, and middleware only runs in the app handler.
  - `dist/serve-static.js:83-117` is the trailing-slash switch the build mirrors: the redirected form is never copied.
  - `:120` denies dotfiles except `/.well-known/`.
  - `:125` gives clean URLs only for `build.format` `file` and `preserve`.
  - `:63-75` adds headers from `_headers.json` (`staticHeaders`, `dist/server.js:9`) to pages. When that file exists, the build copies only the immutable assets.
  - Middleware ran for a prerendered page at build time, and its result is the file.
  - `mode: 'middleware'` needs a server of yours, so the build refuses it.
- **Next.js 16.4.3 (standalone).**
  - `dist/server/lib/router-utils/resolve-routes.js:50-82` orders a request's routes: the config's `headers` and `redirects`, then middleware (its matchers), then `beforeFiles` rewrites, then the files (`check_fs`). The build reads the same lists (`.next/routes-manifest.json`, `.next/server/middleware-manifest.json`, `.next/server/functions-config-manifest.json`) and leaves any asset URL one of them matches to the app. A matcher with `has` or `missing` counts as a match.
  - `dist/server/lib/router-server.js:483-494` sends `public, max-age=31536000, immutable` for `/_next/static/`, except `/_next/static/service-worker/` (`max-age=0`), which stays with the app.
  - `public/` files get `max-age=0` from Next.js, so they stay with the app.
- **React Router 7.18.4 (`react-router-serve`) and Remix 2.17.5 (`remix-serve`).**
  - `dist/cli.js` runs `express.static(<assetsBuildDirectory>/assets, { immutable: true, maxAge: "1y" })` at `<publicPath>assets` before the app's request handler. Only `compression` runs before it. The build reads `assetsBuildDirectory` and `publicPath` from the server build it's given.
  - The other files in the client build get no `Cache-Control` (React Router) or an hour (`public/`), so they stay with the app.

A few more rules hold on top of these:

- Only URLs made of plain characters are copied: no escapes, no `.` or `..` segments.
- The bundle's `.homeport` folder can't be written by the app at runtime.
- The host matches only `GET` and `HEAD` for an app it would otherwise proxy.
- Paused apps stay paused, and the response headers you set still apply.
- An app with its own start command is left alone, because its command could run code before the framework.

The edge keeps nothing the host wouldn't answer: it lists only pages from
the copied files, fetches them from the host's files with a header only the
edge sends (edges strip it from visitors), and the host answers that fetch
from the files or with a 404, never by passing it to the app.

Each case is tested with a real build of each framework. The test checks that:

- Every URL the build copied is answered by the running app with the same bytes and without a header its hook or middleware adds. An asset the host sends as immutable is one the app sends with that same `Cache-Control`.
- A page behind a hook or middleware (a 401 without a session) is not copied, and the app still answers it with that 401.
- Every page `.output/edge.json` lists is one of the copied pages, and no page behind a hook or middleware, or left to the app, is listed.

## A framework with a server of its own

Frameworks that render on request (rsc-kit, Next.js through next-bun-compile,
or one you write) can still have pages that are the same for everyone. These
reach Homeport's edge through the
[edge contract](edge-contract.md).

A framework adds two fields:

1. **`static` in `.output/edge.json`.** The paths whose whole document is the
   same for every visitor, host and request in this build:

   ```json
   {
     "v": 2,
     "build": "8b8e06161e600e70",
     "shells": ["/invite/[token]"],
     "static": ["/", "/privacy", "/terms"]
   }
   ```

   The rules for a path:
   - Only exact paths, no patterns.
   - HTML documents only.
   - Listed in the trailing-slash form the app answers with 200.
   - Never also listed in `shells`.
   - At most 200 paths.
   - Never a page that middleware, a redirect or rewrite, the host, a cookie or a request header could change.
   - Never a page that regenerates on a timer.

2. **`"complete": true` in the answer to `GET /_edge/page`.** For a path in
   `static`, the answer carries the whole document, with nothing to resume:

   ```json
   { "v": 2, "build": "8b8e06161e600e70", "html": "<!DOCTYPE html>…</html>", "state": "", "complete": true }
   ```

   It may also send `headers` the page needs, from the contract's allow list.

The edge keeps these pages and answers them without waking the app.
Homeport drops them when another build goes live. Your framework never
purges anything.

The rule from [Security](#security) applies here too. Leaving a page out of
`static` is always safe, and listing one is a promise that no code of the
app's would change it at request time.
