# The edge contract

The edge contract is how a web framework lets an edge (the proxy in front
of an app) answer some of the app's pages without the app. The framework
says at build time which pages qualify, and answers two private endpoints.
The edge does the rest: it keeps those pages, serves them, and asks the app
only for what is different per visitor.

Two kinds of page can be served this way:

- **A static page** is the same document for every visitor, on every
  request, for the whole build. The edge serves it whole. Your app is not
  asked, and an app that sleeps is not woken.
- **A partially prerendered page** (PPR) has a build-time part, the
  **shell**, and parts rendered per request, the **holes**. The edge sends
  the shell the moment a visitor asks, then asks the app to **resume** the
  page: to render only the holes, which the edge streams in behind the
  shell.

The contract names no platform. homeport's edge implements it, and any
platform can. This page is the whole contract: a framework that does what
it says works on any edge that speaks it.

This is version 2 of the contract.

Contents:

1. [The marker: `.output/edge.json`](#1-the-marker-outputedgejson)
2. [Which pages may be listed](#2-which-pages-may-be-listed)
3. [Switched on by `EDGE_TOKEN`](#3-switched-on-by-edge_token)
4. [The page: `GET /_edge/page`](#4-the-page-get-_edgepage)
5. [The resume: `POST /_edge/resume`](#5-the-resume-post-_edgeresume)
6. [Assets](#6-assets)
7. [What the edge guarantees](#7-what-the-edge-guarantees)
8. [Versions and compatibility](#8-versions-and-compatibility)
9. [Conformance](#9-conformance)

## 1. The marker: `.output/edge.json`

**Every server build writes it,** at `.output/edge.json` in the app's
folder (the folder the build runs in). A build with no page to list still
writes it, with empty lists. That lets a platform tell "this app has no
such pages" from "this framework version doesn't speak the contract". A
static export (no server) writes none.

```json
{
  "v": 2,
  "build": "8b8e06161e600e70",
  "shells": ["/invite/[token]", "/docs/[slug]"],
  "static": ["/", "/pricing", "/terms"],
  "bypass": {
    "headers": ["RSC", "Next-Router-Prefetch", "Next-Action"],
    "query": ["_rsc"],
    "cookies": ["__prerender_bypass"]
  }
}
```

A JSON object, UTF-8, at most 512 KiB.

### `v`

`2`. A platform refuses a marker with any other `v`: the deploy fails and
says why. A field the platform doesn't know is ignored, so a later
revision of v2 can add optional fields without breaking older edges.

### `build`

The build's id. 1-128 bytes of `[A-Za-z0-9._-]`. It may be `""` only when
both `shells` and `static` are empty.

**It must change whenever any listed page, or any shell's state, can
change.** That includes a change only to server code, if that code changes
what a shell or a static page contains. The edge pairs a cached shell with a
resume by `build` alone, so a stale id lets an old shell be finished by a
new server. A change that leaves every listed page and every shell's state
byte-identical may keep the id. Hashing every prerendered document and
every shell's state into it satisfies the rule.

Both endpoints send it back as the `Edge-Build` response header.

### `shells`

The paths whose page the edge may answer from a shell, then a resume.

- **Absolute, and decoded once:** `/über`, not `/%C3%BCber`. No query, no
  fragment, no control character, no empty, `.` or `..` segment. Valid
  UTF-8.
- **Matched exactly and case-sensitively, trailing slash included.** List a
  path in the form your server answers it with a 200, following your own
  trailing-slash setting. A request in the other form goes to the app,
  which redirects as it always does.
- **Patterns.** A segment `[name]` matches any one non-empty segment. A last
  segment `[...name]` matches one or more non-empty segments. A name is
  letters, digits and `_`. A bracket anywhere else in a segment is refused
  (`/a[b]`), as is an optional catch-all (`[[...name]]`) and a catch-all
  followed by a trailing slash. **List a pattern only if resuming at every
  path it matches gives that path's page.** If that isn't true for your
  framework, list concrete paths instead; they are always valid.
- **A pattern says which paths are partially prerendered, not which shell
  to use.** The edge fetches and keeps a shell per requested path, so
  `/docs/a` and `/docs/b` may have different shells.
- At most 200 entries, each at most 200 bytes, no duplicates.

### `static`

The paths whose whole document the edge may serve. The rules of `shells`
apply, with two more:

- **Concrete paths only, no patterns.** A static page is one prerendered
  document per path; a pattern would ask the edge to fetch paths the build
  never rendered. A static page with a parameter is listed as each of its
  prerendered paths.
- **Never also in `shells`.** A path in both lists fails the marker.

At most 200 entries, each at most 200 bytes, no duplicates (separate from
`shells`' 200). Optional: a missing `static` is an empty list.

### `bypass`

Your framework's own requests on a page's path that are not a visitor
loading the page: client-side navigation data, prefetches, server actions,
draft or preview mode. A request carrying any listed header (with any
value), query parameter or cookie goes to the app untouched, even on a
listed path.

- `headers`, `query`, `cookies`: lists of names, each optional (missing is
  empty), at most 32 names each. A name is an HTTP token of 1-64 bytes.
  Header names match case-insensitively; query and cookie names exactly.
- List everything your client sends on a page's own URL that expects
  something other than the page's HTML. An omission means the edge may
  answer such a request with HTML.

### Limits, and what happens when you break one

Over a limit, leave paths out (keep the shallowest) and say so in the build
output. Leaving a path out is always safe: the page is served by the app as
usual. A marker that breaks a rule above (bad JSON, a bad path, a
duplicate, a path in both lists, too many entries) fails the deploy, with
the reason.

**When a list comes out empty, say why in the build output.** A catch-all
middleware with no matcher rightly keeps every page out, and the developer's
next question is "why is nothing listed?". Name the file that kept pages
out. A platform may surface build-output lines that mention `edge.json`.

## 2. Which pages may be listed

The safety rule: **list a page only if your framework's own production
server would serve it without running request-time code that could change
the answer.** The edge repeats an answer it was given once; anything that
would have made the next visitor's answer different is lost.

So never list a **guarded** page, one whose response any of these can
change per request:

- middleware, or a proxy or interceptor whose matcher covers the path;
- a redirect or rewrite rule that applies to the path;
- the request's host (a page bound to a hostname);
- for a static page also: a cookie, or any request header.

The page endpoint refuses (404) exactly what the lists leave out, so the
marker and the endpoint never disagree.

**A static page, in addition:**

- **Answers 200.** A page whose stored answer is a redirect, a 404 or any
  other status is not static.
- **Sets no cookie, and varies on no request header** (`Accept-Language`
  included), whatever the framework calls it at build time.
- **Is an HTML document.** `robots.txt`, `sitemap.xml`, a generated image
  or any other non-HTML route is never listed.
- **Has no time-based revalidation.** A page that regenerates on a timer
  (Next.js ISR's `revalidate: N`) changes when nobody asks, so an edge
  can't keep it without waking the app; leave it out. A page that only
  on-demand revalidation changes is static, and must send an `ETag`
  (section 4).
- **Sends only headers the contract can carry.** A page that needs a
  response header outside section 4's list, or a header rule with a
  condition on the request, is not static.
- **Boots without the origin** (section 6): everything its document needs to
  become interactive is linked from it at immutable URLs.

**A shell's holes, in addition, can't set the visitor's status, headers or
cookies** (section 5). A page whose per-request part needs to set a cookie,
redirect or change the status is guarded and is not listed.

## 3. Switched on by `EDGE_TOKEN`

The platform sets the environment variable `EDGE_TOKEN` in the app's
environment. Treat it as an opaque secret string; homeport's is 64
lowercase hex characters, and stays the same across the app's releases.

- Both endpoints require the request header `Edge-Token` equal to
  `EDGE_TOKEN`, compared in constant time.
- **With `EDGE_TOKEN` unset or empty, or the header missing or wrong, both
  endpoints answer 404,** exactly as a path that doesn't exist would. A
  framework is then safe wherever it runs: only the platform can fetch a
  shell or post a resume.
- The framework reserves the path prefix `/_edge/`. **Any other path under
  it answers 404,** token or not.
- **Every answer from `/_edge/*`, 404s included, carries
  `Cache-Control: private, no-store`** (both directives).

The variable's presence must not change anything else about the app. The
marker is written whether or not it is set, and the pages are served
normally either way.

## 4. The page: `GET /_edge/page`

```
GET /_edge/page?path=%2Fdocs%2Fgetting-started
Host: example.com
X-Forwarded-Host: example.com
X-Forwarded-Proto: https
Accept: application/json
Edge-Token: <EDGE_TOKEN>
```

`path` is a path from the marker as a visitor requested it, decoded,
then percent-encoded as a query value. It has no query string. The request
carries **no cookies** and nothing from a visitor. The edge allows it 5
seconds and does not follow redirects. It may add `If-None-Match`
(below).

### 200: a shell

For a path listed in `shells`, or matched by one of its patterns:

```
HTTP/1.1 200 OK
Content-Type: application/json
Cache-Control: private, no-store
Edge-Build: 8b8e06161e600e70
ETag: "…"            (optional, see below)

{ "v": 2, "build": "8b8e06161e600e70", "html": "<!DOCTYPE html>…", "state": "…" }
```

- **`html`:** the start of the page's one document, everything before the
  first hole. The same for every visitor and every host.
- **`state`:** a string, opaque to the edge, sent back byte for byte as the
  resume's body. `""` when the framework needs none. A framework may ignore
  what comes back and load its own state from the build. It travels in the
  JSON body, never a header, since a framework's postponed state can grow
  past header limits.
- **`build`:** equals the marker's `build`, as does `Edge-Build`.
- **`complete`:** omitted, or `false`.
- **`headers`:** optional, as for a static page below.

### 200: a static page

For a path listed in `static`:

```json
{ "v": 2, "build": "8b8e06161e600e70", "html": "<!DOCTYPE html>…</html>", "state": "", "complete": true }
```

- **`complete: true`** says `html` is the whole document and there is
  nothing to resume. Send it for a path in `static`, and only there. An
  edge rejects a `complete` answer for a path not in `static`, and an
  answer without it for one that is.
- **`html` is the document your server sends** when the page is requested
  directly, byte for byte.
- `state` is `""`.

### `headers`

Optional, on either kind of answer. An object of response headers the page
needs besides `Content-Type`, which the edge sends with its copy of the
page. At most 16, each a string value of at most 8 KiB, each **exactly
what your server sends** on the page, and each fixed for the build. Only
these names are allowed:

`Content-Language`, `Link`, `Cache-Control`, `X-Robots-Tag`,
`Content-Security-Policy`, `Permissions-Policy`, `Referrer-Policy`,
`Strict-Transport-Security`, `X-Frame-Options`, `X-Content-Type-Options`,
`Cross-Origin-Opener-Policy`, `Cross-Origin-Embedder-Policy`,
`Cross-Origin-Resource-Policy`, `X-DNS-Prefetch-Control`.

An edge ignores any other name. A page that needs one is not static, and
its holes can't set one either, so it is not listed at all. A header rule
that sets only allowed headers, unconditionally, doesn't disqualify a page.

On a shell, the edge sends `Cache-Control: private, no-store` whatever
`headers` says: the finished page is the visitor's own. On a static page
the edge sends the `Cache-Control` you give, or none.

The page is sent as `Content-Type: text/html; charset=utf-8`, so listed
documents must be UTF-8.

### `ETag` and `If-None-Match`

- **Send an `ETag` when the page can change while the build stands:** an
  on-demand revalidation (Next.js `revalidatePath` or `revalidateTag`, or
  your framework's equivalent) of a shell or a static page. A page answered
  without an `ETag` is taken to be fixed for the build.
- **When the request's `If-None-Match` matches the current `ETag`, answer
  304** with no body. The edge revalidates this way.
- Answer 200 with the new page when it no longer matches, and 404 when the
  path is no longer one the edge may serve.

### 404

For any path that is not listed here: not in either list, guarded, or not
a page of this kind at this path. An edge forgets a path that answers 404,
and doesn't ask for it again for some minutes. Any other status is an
error: the edge keeps nothing and the visitor's request goes to the app.

### Limits

`html` and `state` together at most 2 MiB. A larger answer is not kept,
and that path is always served by the app.

## 5. The resume: `POST /_edge/resume`

Sent for a visitor's request that the edge answered from a shell, through
the site's normal proxy, after the shell has gone out.

```
POST /_edge/resume?url=%2Fdocs%2Fgetting-started%3Futm_source%3Dnews
Host: example.com
Cookie: <the visitor's>
…the visitor's other headers…
Accept-Encoding: identity
Content-Type: application/octet-stream
Edge-Token: <EDGE_TOKEN>
Edge-Build: <the shell's build>

<the shell's state, raw; empty if it was "">
```

- **`url`:** the visitor's decoded path plus their query string exactly as
  they sent it, percent-encoded as a query value. The holes may read search
  parameters.
- **The visitor's headers and cookies,** so the holes render for them,
  minus conditional and range headers (`If-None-Match`, `If-Modified-Since`,
  `If-Match`, `If-Unmodified-Since`, `If-Range`, `Range`).
- **`Accept-Encoding: identity`.** The edge compresses the shell and the
  holes as one stream.
- **`Edge-Build`:** the build of the shell the visitor already has.

### 200: the holes

```
HTTP/1.1 200 OK
Content-Type: text/html; charset=utf-8
Cache-Control: private, no-store
Edge-Build: <the build of the server answering>
```

- **The body is only the holes:** the bytes that follow `html` in the page,
  so that `html` followed by the body is one valid document. **It never
  starts a second document:** no `<!DOCTYPE` and no `<html>`.
- **Plain bytes, never compressed,** whatever the request's
  `Accept-Encoding` says.
- **Streamed as it renders.** Flush each part as it is ready; the visitor
  sees it as it arrives.
- `Edge-Build` is your server's own build. If it differs from the request's
  `Edge-Build`, the edge discards the body: answer anyway, the edge decides.

The edge appends the body only when the status is 200, the type
`text/html` and `Edge-Build` the shell's. Otherwise the visitor's page
ends at the shell, with its fallbacks showing where the holes would be.

**The edge forwards no other response header.** The visitor's status and
headers went out with the shell. A `Set-Cookie`, a redirect or another
status from the resume can't reach the visitor; a `Set-Cookie` is dropped.
That is why a page whose holes need any of these is guarded (section 2).

A resume on a path that isn't a shell here (a static page included), or
with a missing or wrong token, is a 404.

A resume should be safe to send twice: if the connection to the app is
reset or refused before any answer, the edge sends it once more.

The edge waits up to 60 seconds for the first byte, enough for a cold
start.

## 6. Assets

A page served from the edge links stylesheets and scripts. If those had to
come from the app, the first visitor's browser would wake a sleeping app
for its CSS, and paint only after. So the edge keeps assets too.

**An asset is kept when your server answers it with:**

- status 200;
- `Cache-Control` containing `immutable` and `max-age` of at least 86400
  (one day), and none of `no-store`, `no-cache` or `private`;
- no `Set-Cookie`;
- no `Vary` other than `Accept-Encoding`;
- uncompressed bytes when asked with `Accept-Encoding: identity`;
- at most 1 MiB.

Every major framework already serves its hashed build assets this way;
there is nothing to configure. An asset is kept by its path and query until
the release changes, so its URL must change whenever its content does
(a content hash in the name).

**Fetched before the first visitor.** When a release goes live, the edge
fetches each `static` path and each concrete `shells` path, and then the
assets each document links with:

- `<script src="…">`
- `<link rel="stylesheet" href="…">`
- `<link rel="modulepreload" href="…">`
- `<link rel="preload" as="fetch" href="…">`

Only **absolute same-origin paths** are followed (`/assets/app-3f9a.js`;
not `assets/app.js`, not `//cdn.example.com/…`, not a full URL), at most 64
per document. Any other immutable asset (a lazily loaded chunk) is kept
after the first time a visitor's request for it reaches the app.

**A static page boots without the origin.** Everything its document needs
to become interactive (scripts, styles, a serialized payload your client
reads at startup) must be linked from the document at an immutable,
hashed, same-origin URL, as above. Never fetch it with a `bypass` header or
from a URL that isn't an immutable asset: that request goes to the app, and
wakes it, on every visit from a browser that runs JavaScript. A payload
your client fetches at boot is linked with
`<link rel="preload" as="fetch" crossorigin>` and served as an immutable
asset; it needs no field in the contract.

This covers becoming interactive, not every request a page makes. A link
prefetch, a router's navigation data, an icon served with `max-age=0`, an
optimized image or a fetch from your own code may still reach the app.
"No wake" holds for the document, its boot, crawlers, and any client
without JavaScript.

## 7. What the edge guarantees

What a framework can rely on from an edge that speaks this contract.

**Which requests it answers.** Only a visitor's document request on a
listed path:

- method `GET` or `HEAD`;
- the decoded path matches `static` or `shells` (a path whose encoded form
  contains an encoded `/`, `%2F`, never does);
- `Accept` includes `text/html`;
- no `Range`;
- `Sec-Fetch-Dest` absent or `document`;
- nothing named in `bypass`;
- no query string, or one made only of ad and campaign tags: `utm_*`,
  `gclid`, `gbraid`, `wbraid`, `fbclid`, `msclkid`, `ttclid`, `twclid`,
  `li_fat_id`, `mc_cid`, `mc_eid`, `_ga`, `_gl`, `ref`. So a listed page
  must not depend on any of those. A resume still receives the whole query.

Everything else goes to the app untouched.

**A static page** it holds is served whole, status 200, with its `headers`,
compressed for the visitor. **No request reaches the app, and a sleeping
app is not woken.** The edge answers `If-None-Match` from the visitor
itself, with a validator of its own, so revalidating browsers get a 304
from the edge.

**A shell** is written and flushed to the visitor at once, before anything
is asked of the app, then the resume streams in behind it. A `HEAD` gets
the headers alone and never wakes the app. If the resume fails (a non-200,
another build, no first byte in time), the response ends cleanly at the
shell, and on an error or another build the edge drops that shell and
fetches it again.

**One copy serves every hostname.** A page is fetched once through one of
the app's own hostnames and served on all of them. That is why a page
bound to the host is never listed.

**Releases.** Nothing kept from one build is served once another release
goes live: pages and assets are dropped and fetched again for the new
build. A platform does this when it makes a release live, so a framework
never has to purge anything. `ETag` revalidation only covers changes within
one release.

**Revalidation.** A page sent with an `ETag` is asked again in the
background, with `If-None-Match`, about once a minute while the app is
awake: 304 keeps it, 200 replaces it, 404 drops it. **Never while the app
sleeps:** nothing can have changed, because what invalidates a page runs
in the app. When the app next wakes, its stale pages are checked once.

**Sleep and misses.** The edge never wakes an app to fetch, refill or
revalidate a page. A request for a listed page the edge doesn't hold yet
goes to the app as usual (and wakes it if it sleeps); the edge fetches the
page in the background once the app is awake.

**Cookies.** If the app's own answer to a listed page's document request
sets a cookie, the edge stops serving that page from its copy, and sends
its requests to the app.

**Isolation.** A visitor's request for `/_edge/*` is answered 404 by the
edge and never reaches the app, on a site with listed pages. Every request
header starting with `Edge-` is removed from a visitor's request before it
is proxied, so only the edge can send `Edge-Token` or `Edge-Build`. The
token check in your endpoints is still required: on a site with nothing
listed, a visitor's request for `/_edge/*` reaches the app.

**The platform's own headers.** Response header rules the site's owner
configures on the platform apply to pages served from the edge, as they do
to the app's answers. (On homeport, those are [`headers:`](headers.md).)

## 8. Versions and compatibility

- The marker's `v` and the page answer's `v` are the contract's major
  version, `2`. A change that an older edge or framework would get wrong
  is a new version.
- Within v2, new fields are optional and ignored by those that don't know
  them. Don't rely on an edge reading a field this page doesn't list.
- **v1** used `.output/ppr.json`, `/_ppr/shell`, `/_ppr/resume`,
  `PPR_TOKEN` and `PPR-*` headers, and had no `static`. It is retired: an
  edge that speaks v2 ignores a build that has only `ppr.json`, and serves
  that app with no pages from the edge (the deploy succeeds and says to
  upgrade the framework). A framework should not write both.
- A framework may keep endpoints of its own beside `/_edge/*`. They are not
  part of the contract, and an edge never calls them.

## 9. Conformance

A framework conforms when all of these hold for a production build of an
example app, run as it would be deployed with `EDGE_TOKEN` set.

**Marker**

- [ ] `.output/edge.json` exists after every server build, with `[]` lists
      when nothing qualifies.
- [ ] `v` is 2; `build` is 1-128 bytes of `[A-Za-z0-9._-]` (or `""` with
      both lists empty).
- [ ] Every path is absolute, decoded, at most 200 bytes; patterns only in
      `shells`; at most 200 per list; no duplicates; no path in both
      lists; at most 32 names per `bypass` list.
- [ ] Rebuilding with only a server-rendered change to a listed page gives
      a new `build`.
- [ ] A guarded page (under middleware, say) is in neither list, and an
      empty list is explained in the build output.

**Endpoints**

- [ ] Without `EDGE_TOKEN`, or with a missing or wrong `Edge-Token`, both
      endpoints are 404.
- [ ] Any other path under `/_edge/`, with the token, is 404.
- [ ] Every `/_edge/*` answer, 404s included, has
      `Cache-Control: private, no-store`.
- [ ] An unlisted path and a guarded path are 404 from `/_edge/page`.

**Shells**, for every concrete listed path and a sample path for each
pattern:

- [ ] `/_edge/page` is 200, `{v, build, html, state}` with `v` 2, `build`
      and `Edge-Build` the marker's, no `complete`.
- [ ] With an `ETag`, `If-None-Match` with it is 304.
- [ ] The resume, with the shell's `state` and `Accept-Encoding: gzip`
      sent anyway, is 200 `text/html`, `Edge-Build` the marker's,
      uncompressed, with no `<!DOCTYPE` or `<html>` in the body.
- [ ] `html` plus the resume's body is one document, and it is the page
      served directly.
- [ ] The holes render for the visitor's query (resume a URL with a query
      parameter the page prints).

**Static pages**, for every listed path:

- [ ] `/_edge/page` is 200 with `complete: true`, `state: ""`, `build` and
      `Edge-Build` the marker's, and `html` a whole document.
- [ ] `html` is byte for byte what the page's own URL returns, with a 200.
- [ ] With an `ETag`, `If-None-Match` with it is 304.
- [ ] A resume on the path is 404.
- [ ] Every script, stylesheet, module preload and fetch preload the
      document links is a 200 immutable asset.
- [ ] Loaded in a browser with the app stopped (only the edge's copies of
      the page and its assets available), the page becomes interactive.
      Other requests that fail then (prefetches, images, icons) are worth
      reporting, but don't fail conformance.

**Headers**, on either kind of answer:

- [ ] `headers`, if sent, has at most 16 entries, all from the allowed
      list, each equal to the header the page's own URL returns.

### Test scripts

Two framework-neutral scripts check most of the list above against a
running server. Each reads only the marker and speaks only the contract's
endpoints:

- [`scripts/edge-conformance.mjs`](https://github.com/rsc-kit/rsc-kit/blob/main/scripts/edge-conformance.mjs)
  in rsc-kit:

  ```sh
  EDGE_TOKEN=<token> node edge-conformance.mjs .output http://localhost:3000 \
    --guarded /dashboard --unlisted /api/health \
    --sample '/docs/[slug]=/docs/intro' --query '/search?q=edge-check'
  ```

- [`test/regression/edge-conformance.mjs`](https://github.com/ramonmalcolm10/next-bun-compile/blob/main/test/regression/edge-conformance.mjs)
  in next-bun-compile.

Start the server with the same `EDGE_TOKEN` you pass the script. Run one in
your framework's CI against an example app with at least one static page,
one shell (a pattern, if you list patterns), one guarded page and one page
whose holes read the query.

### Reference implementations

- **[rsc-kit](https://github.com/rsc-kit/rsc-kit)** lists patterns, sends no
  `ETag` (its pages are fixed for the build, and `build` hashes every stored
  page and its postponed state), sends `state: ""` and ignores the resume
  body. Its static pages boot from a serialized payload at a hashed
  immutable URL, linked with `<link rel="preload" as="fetch">`.
- **[next-bun-compile](https://github.com/ramonmalcolm10/next-bun-compile)**
  (Next.js) lists concrete paths only, makes `build` from Next's `BUILD_ID`
  plus a digest of every listed page (so a rebuild with new build-time data
  is a new build even under a fixed `generateBuildId`), sends an `ETag` for
  on-demand revalidation, and carries Next's postponed state in `state`. It
  derives `headers` from the app's `headers()` rules.
