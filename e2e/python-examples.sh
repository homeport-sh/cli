#!/bin/bash
# Builds the Python examples the way a hosted build does - the plan's own
# install and command, in the plan's image - then runs what the build made,
# alone and read-only, in a bare Ubuntu 24.04 (the boxes' OS): the bundle's
# bin, which is the interpreter, with the plan's start command. Checks it
# over HTTP, runs the release command in a container of its own, and stops
# the web with SIGTERM.
#
#   e2e/python-examples.sh <homeport> <examples folder>
#
# Needs Docker, curl and jq. Each app is built in a copy, so the folder
# stays clean.
set -euo pipefail
hp=$(realpath "$1") examples=$(realpath "$2")
work=$(mktemp -d)
trap 'docker rm -f $(docker ps -aq --filter label=homeport-e2e-py) >/dev/null 2>&1 || true' EXIT
fail=0
port=18180
runner=ubuntu:24.04

say() { printf '%s\n' "$*" >&2; }
check() { # name, what, ok
  if [ "$3" = ok ]; then say "  ok    $1: $2"; else say "  FAIL  $1: $2 ($3)"; fail=1; fi
}
status() { curl -s -o /dev/null -w '%{http_code}' "$@" || true; }

# build <name>: builds a copy of the example
build() {
  local name=$1 dir="$work/$1"
  cp -R "$examples/$name" "$dir"
  "$hp" build-plan "$dir" > "$work/$name.plan.json"
  local image install command script
  image=$(jq -r .image "$work/$name.plan.json")
  install=$(jq -r '.install // ""' "$work/$name.plan.json")
  command=$(jq -r .command "$work/$name.plan.json")
  say "== $name: $(jq -r '[.framework, .kind, .runtime, .runtime_version] | join(" ")' "$work/$name.plan.json")"
  say "   $(jq -r .runtime_reason "$work/$name.plan.json")"
  script=$command
  [ -n "$install" ] && script="$install && $command"
  if ! docker run --rm -v "$dir:/app" -w /app "$image" sh -ec "$script" > "$work/$name.build.log" 2>&1; then
    tail -60 "$work/$name.build.log" >&2
    check "$name" build "the build failed"
    return 1
  fi
  local b
  b="$dir/$(jq -r .artifact "$work/$name.plan.json")"
  [ -f "$b/bin" ] && [ -x "$b/bin" ] && check "$name" "bin is the interpreter" ok || check "$name" "bin is the interpreter" "no executable bin"
  [ ! -e "$b/.homeport-build" ] && [ ! -e "$b/.venv" ] && check "$name" "nothing of the build's ships" ok || check "$name" "nothing of the build's ships" "it does"
  [ -n "$(find "$b/lib" -name '*.pyc' -print -quit)" ] && check "$name" "compiled" ok || check "$name" "compiled" "no .pyc"
  say "   bundle: $(du -sh "$b" | cut -f1)"
}

# in <name> <args>: runs ./bin <args> from the bundle, read-only, as a
# sandbox does: its own /tmp, PORT and HOST, nothing else of the image's
in_bundle() { # name, docker args..., -- bin args...
  local name=$1; shift
  local dir opts=()
  dir="$work/$name/$(jq -r .artifact "$work/$name.plan.json")"
  while [ "$1" != -- ]; do opts+=("$1"); shift; done
  shift
  docker run --label homeport-e2e-py --read-only --tmpfs /tmp:size=64m -v "$dir:/srv/app:ro" -w /srv/app \
    -e PORT=8080 -e HOST=0.0.0.0 "${opts[@]}" "$runner" ./bin "$@"
}

# start <name>: the web, on a port; prints the base URL
start() {
  local name=$1 run
  run=$(jq -r '.run // ""' "$work/$name.plan.json")
  run=${run//\$PORT/8080}
  port=$((port + 1))
  # shellcheck disable=SC2086
  in_bundle "$name" -d --name "e2e-py-$name" -p "127.0.0.1:$port:8080" -- $run >/dev/null
  for _ in $(seq 1 150); do
    [ "$(status "http://127.0.0.1:$port/")" != 000 ] && break
    sleep 0.2
  done
  echo "http://127.0.0.1:$port"
}

# stop <name>: SIGTERM, then how it went
stop() {
  local name=$1 t0 code
  t0=$(date +%s)
  docker stop -t 20 "e2e-py-$name" >/dev/null
  code=$(docker inspect -f '{{.State.ExitCode}}' "e2e-py-$name")
  [ "$code" = 0 ] && [ $(($(date +%s) - t0)) -lt 15 ] && check "$name" "stops on SIGTERM" ok || check "$name" "stops on SIGTERM" "exit $code"
  docker logs "e2e-py-$name" > "$work/$name.serve.log" 2>&1 || true
  docker rm -f "e2e-py-$name" >/dev/null 2>&1 || true
}

page() { # name, url, what it says
  local body
  body=$(curl -s "$2" | tr -d ' ')
  case "$body" in *"$3"*'"prefix":"/srv/app"'*) check "$1" "/ says $3, from the release's Python" ok ;; *) check "$1" "/ says $3" "got: ${body:0:200}" ;; esac
}
is() { # name, url, status
  local got
  got=$(status "$2")
  [ "$got" = "$3" ] && check "$1" "${2#http://127.0.0.1:*/} is $3" ok || check "$1" "${2#http://127.0.0.1:*/} is $3" "$got"
}

if build django; then
  release=$(jq -r '.release // ""' "$work/django.plan.json")
  # shellcheck disable=SC2086
  if in_bundle django --rm -- $release > "$work/django.release.log" 2>&1; then check django "release: $release" ok; else
    tail -20 "$work/django.release.log" >&2; check django "release: $release" failed; fi
  url=$(start django)
  page django "$url/" '"framework":"django"'
  is django "$url/static/site.css" 200
  # the front-end's build (Tailwind, with Bun), collected and served
  grep -q "tailwindcss -i" "$work/django.build.log" && check django "the front-end built" ok || check django "the front-end built" "tailwindcss didn't run"
  is django "$url/static/build/app.css" 200
  curl -s "$url/static/build/app.css" | grep -q 'text-emerald-600' && check django "its CSS has the page's classes" ok || check django "its CSS has the page's classes" "it doesn't"
  b="$work/django/$(jq -r .artifact "$work/django.plan.json")"
  [ ! -e "$b/node_modules" ] && check django "node_modules doesn't ship" ok || check django "node_modules doesn't ship" "it does"
  is django "$url/nope" 404
  stop django
fi

if build fastapi; then
  grep -q "installing uvicorn" "$work/fastapi.build.log" && check fastapi "uvicorn installed, pinned" ok || check fastapi "uvicorn installed, pinned" "not said"
  url=$(start fastapi)
  page fastapi "$url/" '"python":"3.13.'
  is fastapi "$url/docs" 200
  stop fastapi
fi

if build flask; then
  url=$(start flask)
  page flask "$url/" '"framework":"flask"'
  is flask "$url/static/hello.txt" 200
  is flask "$url/nope" 404
  stop flask
fi

# a uv.lock that pyproject.toml has moved past fails the build, saying so
stale="$work/stale"
cp -R "$examples/fastapi" "$stale"
sed -i 's/dependencies = \[/dependencies = ["httpx", /' "$stale/pyproject.toml"
"$hp" build-plan "$stale" > "$work/stale.plan.json"
if docker run --rm -v "$stale:/app" -w /app "$(jq -r .image "$work/stale.plan.json")" sh -ec "$(jq -r '.install' "$work/stale.plan.json")" > "$work/stale.build.log" 2>&1; then
  check stale-lock "the build fails" "it built"
else
  grep -q "run uv lock" "$work/stale.build.log" && check stale-lock "the build fails, saying to run uv lock" ok || {
    tail -20 "$work/stale.build.log" >&2; check stale-lock "the build fails, saying to run uv lock" "not said"; }
fi

if [ "$fail" != 0 ]; then
  for f in "$work"/*.serve.log; do [ -f "$f" ] && { say "--- $f"; tail -30 "$f" >&2; }; done
fi
exit "$fail"
