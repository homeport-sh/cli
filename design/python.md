# Python apps (Django, FastAPI, Flask)

- **Status:** proposed. The detection is written and unit-tested on branch
  `python-apps` (homeport-sh/cli#48, draft). The e2e passes in CI on x86-64:
  Django, FastAPI and Flask are built in the plan's image and run read-only
  from the bundle in ubuntu:24.04, with migrate, static files and SIGTERM
  checked. Nothing is released, and the engine is not changed.
- **Date:** 2026-10-22
- **Why:** a Python web app (Django, FastAPI, Flask) can't be deployed today.
  `build-plan` finds no `go.mod`, `composer.json` or lockfile and stops. There
  is no Dockerfile to fall back on, by design.

## The owner's constraints

- No Dockerfiles. An app ships as a binary, a bundle or a static site, plus
  `packages:`.
- Boxes are stateless. The release folder is everything the app has.
- Apps run in gVisor on Ubuntu 24.04 (glibc 2.39). The release is mounted
  read-only. The host's `/lib` and `/usr/lib`, CA certificates and zoneinfo are
  mounted read-only. There is no `/bin`, no shell, and `/tmp` is a 64 MB tmpfs.
- homeport's own engine never runs Python. Python is only ever the customer's
  app, running in its sandbox, as Bun and FrankenPHP are.

## Where the runtime lives: in the bundle, not in a package

The brief proposed shipping CPython as a `packages:` image (`pkg-python`), as
`fonts` is shipped. The code says otherwise:

- A bundle is run as `./bin <args>`. `bin` must be an executable **file** at the
  bundle's top, and a release has no symlinks. A package is unpacked at
  `.homeport/pkgs/<name>/`, and only its `bin/` goes on `PATH`. So with
  Python as a package, `bin` would still have to be something. A copy of the
  interpreter at the top can't find a standard library under
  `.homeport/pkgs/python/` unless homeportd sets `PYTHONHOME`. That is an
  engine change and a new homeportd release, for no gain.
- A JavaScript bundle already carries its runtime: `bin` is a pinned Bun or Node
  binary, fetched in the build and checked against its sha256 (`js.go`
  `fetchBun`, `checkSum`). A PHP bundle carries FrankenPHP the same way. Python
  fits that pattern exactly.

**How it works.** python-build-standalone (astral-sh) builds CPython for Linux
with `libpython` **statically linked** into `bin/python3.X`: 31 MB, needing only
glibc (checked: its `DT_NEEDED` is libc, libm, libpthread, libdl, libutil, librt).
CPython finds its prefix by walking up from the executable's folder until it
finds `lib/python3.X/os.py` (`getpath.py`). So a bundle laid out like this

```
.homeport-bundle/
  bin                       <- python3.X, the interpreter itself
  lib/python3.X/            <- the stdlib, lib-dynload, site-packages (the app's deps)
  manage.py, myproject/ ... <- the app's own files, as in the repository
```

is a working, relocatable Python whose `sys.prefix` is the release folder,
wherever homeportd mounts it. No virtualenv and no `pyvenv.cfg` are needed,
so nothing records an absolute build path. Checked locally with the macOS
build of the same release (it is static too): built in one folder, moved,
made read-only, then `./bin -m gunicorn --bind 0.0.0.0:$PORT app:app` served
a Flask app, reported `sys.prefix` = the new folder, and shut down cleanly on
SIGTERM. The Linux build is checked by the e2e job (below).

**Recommendation: no `pkg-python` image.** Deliverable 3 of the brief, a
package image in homeport-sh/homeport, is not needed. Option B below is the
alternative if the owner wants one.

| | A. Bundle carries Python (recommended) | B. Our own build image `ghcr.io/homeport-sh/python` | C. `pkg-python` package |
|---|---|---|---|
| Engine change | none | none | homeportd sets `PYTHONHOME`; new release |
| homeport-sh/homeport work | none | image recipe and workflow, one image per line or one with all lines | recipe and workflow per version and arch |
| Pinned by | sha256 of each PBS archive, in the CLI (like Bun's) | image digest, in the CLI (like FrankenPHP's) | image digest, in the catalog |
| Per build | ~35 MB PBS and ~20 MB uv download, cached in the app's build cache after the first | none | builder unpacks the package |
| New Python patch | edit 8 sums in `python.go` | rebuild and publish the image, re-pin | rebuild, publish, re-pin |

## Detection

Python is chosen after `composer.json` and `go.mod`, before `package.json`. A
JavaScript app with a stray `requirements.txt` stays JavaScript: Python wins
over a `package.json` only when a Python framework is recognised.

| Files | What's installed | Notes |
|---|---|---|
| `pyproject.toml` + `uv.lock` | `uv export --frozen --no-dev --no-editable`, then `uv pip install --no-deps` of that | preferred. Exact versions with hashes, the project itself when it's a package (`[build-system]`) |
| `requirements.txt` | `uv pip install -r requirements.txt` | as pinned as the file is |
| `pyproject.toml` alone | error: commit `uv.lock` (`uv lock`) | as `composer.lock` is required |
| `Pipfile` / `Pipfile.lock`, `poetry.lock` alone | error that says how to move to uv or `requirements.txt` | out of scope for now |

| Framework | Recognised by | Web (args to `bin`, the interpreter) | Release, detected |
|---|---|---|---|
| Django | `manage.py` + `django` in the deps | `-m gunicorn --bind 0.0.0.0:$PORT <proj>.wsgi`, or with uvicorn and no gunicorn in the deps, `-m uvicorn --host 0.0.0.0 --port $PORT <proj>.asgi:application` | `manage.py migrate --noinput` |
| FastAPI | `fastapi` in the deps; `X = FastAPI(` in the first of `main.py`, `app.py`, `api.py`, `server.py`, `asgi.py`, `app/main.py`, `app/__init__.py`, `src/main.py`, `src/app.py`, `api/main.py` | `-m uvicorn --host 0.0.0.0 --port $PORT <module>:<X>` | `-m alembic upgrade head` when `alembic.ini` and `alembic` |
| Flask | `flask` in the deps; `X = Flask(` in the same files plus `wsgi.py` | `-m gunicorn --bind 0.0.0.0:$PORT <module>:<X>` | the same Alembic rule (Flask-Migrate uses Alembic) |
| Python, nothing recognised | `pyproject.toml` or `requirements.txt` | none: error asking for a start command (args to python, like `-m myapp`) | none |

`<proj>` is read from `manage.py`'s `DJANGO_SETTINGS_MODULE` default
(`"mysite.settings"` gives `mysite`). The deps are read from
`[project].dependencies` in `pyproject.toml` and from `requirements.txt`, with
names normalised (PEP 503).

A Flask app with only an application factory (`create_app()`) isn't started
automatically: gunicorn's `module:create_app()` needs parentheses, which a
start command can't contain (`sandbox.go`). The error says to add a `wsgi.py`
with `app = create_app()`, or to set the start command.

**The server.** The plan names gunicorn or uvicorn. If the app's lockfile has
neither, the build installs a pinned one (gunicorn 26.2.0, uvicorn 0.54.0, by
the wheel's sha256) and says so in the build log. The app's own locked
versions are never changed: the server's dependencies resolve against what's
already installed.

**Python version**, one line from those offered (`pythonReleases`):

1. `.python-version`'s first line (`3.13`, `3.13.5`, `cpython-3.13`), by its
   major.minor;
2. `runtime.txt` (`python-3.12.1`, as Heroku reads it);
3. `requires-python` in `pyproject.toml`: the newest line it allows (PEP 440:
   `>=`, `<=`, `>`, `<`, `==`, `!=`, `~=`, `.*`);
4. otherwise the default, 3.14.

A version that names a line not offered is an error that lists the offered
ones. The patch is always homeport's pinned one, as with Node.

Offered (python-build-standalone release `20261021`, `install_only_stripped`,
x86-64 and arm64 glibc): **3.12.15, 3.13.17, 3.14.9 (default), 3.15.1**. 3.11
is left out (security-only, and less than a year from end of life).

## The build

The image is `buildpack-deps:noble`, pinned by digest: **Ubuntu 24.04, the
boxes' own OS and glibc**, with curl, git, gcc and the common `-dev` headers.
An sdist compiled here links against the same glibc the app runs on. The
upstream uv images are Debian trixie (glibc 2.41) or Alpine, and 0.13.5 has no
bookworm tag. A wheel built on trixie could need symbols that Ubuntu 24.04's
2.39 lacks.

Install (one line in the plan):

1. Fetch uv 0.13.5 and the chosen CPython, each checked against the sha256
   pinned in `python.go`. Each archive is kept in `$HOME/.cache/homeport`
   (the app's build cache volume), so later builds skip the download when the
   sum still matches. Unpack into `.homeport-build/` in the app's folder, not
   `/tmp`: the build's `/tmp` is a 1 GB tmpfs, and torch alone is bigger.
2. Install the deps into that CPython's own `site-packages`, compiling bytecode
   (`--compile-bytecode`). uv's cache is `$HOME/.cache/uv`, which persists per
   app.
3. Make sure the server module imports (see "The server" above).

Command (the build, then the bundle):

1. Django: `python manage.py collectstatic --noinput`, which writes
   `STATIC_ROOT` inside the app's folder, so it ships. If it fails (no
   `STATIC_ROOT`, or settings that need a variable the build doesn't have),
   the build goes on without it and says so, as Laravel's caches do.
2. The bundle: the app's files (no `.git`, `.venv`, `venv`, `node_modules`,
   `.homeport-build`, stale `__pycache__`), copied by the same link-checked
   copy as a JavaScript bundle (`copyTree`, shared with `js.go`). Then the
   stdlib and site-packages into `lib/python3.X/`, without `idlelib`,
   `tkinter`, `turtledemo`, `ensurepip` or pip. Then the interpreter as
   `bin`.
3. `./bin -m compileall --invalidation-mode unchecked-hash` over the bundle.
   The release is read-only, so Python could never write a `__pycache__`, and
   each wake would compile every import again. `unchecked-hash` also skips the
   source `stat` on each import. A file that doesn't compile (a stray
   Python 2 script) is left for Python to report if it's ever imported.

An app with a top-level `bin/` folder is refused with a clear message: `bin` is
the interpreter. This collision exists for JavaScript bundles too, where it
fails less clearly. An app's own `lib/` folder merges harmlessly unless it has a
`lib/python3.X/`.

Size, x86-64: interpreter 31 MB and trimmed stdlib with bytecode about 30 MB,
so **about 60 MB before the app's deps**. Measured in CI (x86-64, unpacked):
Flask 66 MB, FastAPI 74 MB, Django 112 MB. A Node bundle is about 120 MB.

## Running

- **PORT.** Both servers are told `0.0.0.0:$PORT` explicitly; homeportd
  substitutes `$PORT` without a shell.
- **Stop grace.** gunicorn on SIGTERM stops accepting, lets workers finish
  within its `graceful_timeout` (30s by default), then exits. uvicorn on SIGTERM
  stops accepting and waits for open requests, with no limit by default.
  homeportd's stop grace (`stop_grace:`) kills either after it, so nothing
  hangs. A person who wants a longer drain sets `stop_grace:` and, for
  gunicorn, `graceful_timeout` in `gunicorn.conf.py`, which gunicorn reads
  from the cwd.
- **Workers.** No worker count is passed. gunicorn's default is 1 sync
  worker; both servers read `WEB_CONCURRENCY`, and gunicorn reads
  `gunicorn.conf.py`. See open question 2.
- **Static files.** A Django app serves its own static files only with
  WhiteNoise (or similar). With collectstatic and no `whitenoise` in the deps,
  the plan warns that `/static/` will 404. FastAPI's `StaticFiles` and Flask's
  `static/` work as they are.
- **gunicorn 25+'s control socket** goes to `$XDG_RUNTIME_DIR` or
  `~/.gunicorn`, neither writable in the sandbox. gunicorn logs a warning and
  carries on (checked in its source: the error is caught). It can't be
  disabled by flag without breaking gunicorn < 25, which has no
  `--no-control-socket`.
- **TLS out.** PBS's OpenSSL reads the system CA bundle at `/etc/ssl`,
  which the sandbox mounts from the host. To verify on a box.

## Failure modes

| What | What happens |
|---|---|
| A dep has no wheel for the platform and needs a C library not on the box (psycopg2 from source, then libpq at runtime) | Builds, since noble has `libpq-dev`, but fails at import on the box: the box has no `libpq.so.5`. Use `psycopg[binary]` / `psycopg2-binary`, or a future package. Said in the docs. |
| Settings import needs a secret at collectstatic time | Built without collectstatic, with a warning. The app's build variables are passed to the build, so setting it there fixes it. |
| `uv.lock` is out of date with `pyproject.toml` | `uv export --frozen` uses the lock as it is. A stale lock installs what was locked, not an error. `--locked` would refuse instead; see open question 4. |
| GitHub (PBS and uv downloads) is down | The first build of an app fails. Later builds use the cached archives. Option B removes this. |
| A dep compiles for > 15 min or > 4 GB | The build's limits apply as for any toolchain. |
| An app writes into its own folder at runtime (SQLite next to `manage.py`, uploaded media) | Read-only: it fails. Boxes are stateless, so data belongs in Postgres or object storage. Said in the docs. |

## Test plan

- Unit (`buildplan/python_test.go`): detection per framework, entry
  discovery, Django project name, server choice, release detection, version
  choice (each source, the PEP 440 forms, errors), the lockfile and
  Pipfile/Poetry errors, JS-with-requirements.txt staying JS, Files() listing
  the files read, and the plan's install and command shape (pinned sums,
  layout, exclusions).
- e2e (`e2e/python-examples.sh`, in CI's `examples` job): Django (gunicorn,
  WhiteNoise, collectstatic, migrate as the release command against SQLite in
  `/tmp`), FastAPI (uvicorn) and Flask (gunicorn) examples are built in the
  plan's own image with the plan's install and command. Each is then run,
  alone, from the bundle in a bare `ubuntu:24.04` as `./bin <run>`, read-only,
  checked over HTTP, and sent SIGTERM.
- On a box, before release: one Django app deployed through the hosted builder
  on x86-64 and arm64, with a cold wake time measured against a Node app's.

## Open questions for the owner

1. **Python lines and default.** 3.12-3.15 offered, 3.14 the default. 3.15.0
   came out on 2026-10-01; some binary wheels (pydantic-core, psycopg) can lag
   a month or two, so it isn't the default yet.
2. **Worker defaults.** gunicorn's default is 1 sync worker: one slow request
   blocks the app. Pass `--workers 2 --threads 4` (gthread) when nothing is
   set, as PHP Octane defaults to 2 workers? Memory is about 60 MB per Django
   worker.
3. **Option A vs B** (above): accept a GitHub download on each app's first
   build, or own a build image.
4. **A stale `uv.lock`:** install it as it is (`--frozen`, as drafted), or
   fail the build (`--locked`)?
5. **Django static files:** is a warning enough, or should homeport serve
   `STATIC_ROOT` itself at the edge (a platform feature, not app-side)?
6. **Front-end assets** in a Python app (a `package.json` with Tailwind or
   Vite beside Django): not built in v1, because the image has no Node. Laravel
   gets Bun in its base image. Add Bun to the Python build the same way?

## Effort

- Done on the branch: detection, plan, unit tests, examples, an e2e that is
  green in CI, and this note.
- To ship: owner decisions 1-4 (each a constant or one line), the e2e run
  green on CI, then a box test on both archs (half a day). Then the
  dashboard's build settings: a Python framework shows no Octane or Bun/Node
  runtime field. Today `runtime` only accepts bun or node, so a Python version
  override isn't offered; `.python-version` is the way (half a day if wanted).
  Then the website docs (half a day). **About 2 days in all**, no engine
  release.
