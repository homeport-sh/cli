#!/bin/bash
# Builds JavaScript examples the way a hosted build does - the plan's own
# install and command, in the plan's image, with its GNU find and tar - then
# starts what the build made, alone, in a bare Debian (the bundle's bin, or
# the binary), and checks it over HTTP: a server-rendered page, an asset, a
# 404, and a server function or form action.
#
#   e2e/js-examples.sh <homeport> <examples checkout>
#
# Needs Docker and curl. Each app is built in a copy, so the checkout stays
# clean.
set -euo pipefail
hp=$(realpath "$1") examples=$(realpath "$2")
work=$(mktemp -d)
trap 'docker rm -f $(docker ps -aq --filter label=homeport-e2e) >/dev/null 2>&1 || true' EXIT
fail=0
port=18080

say() { printf '%s\n' "$*" >&2; }
check() { # name, what, ok
  if [ "$3" = ok ]; then say "  ok    $1: $2"; else say "  FAIL  $1: $2 ($3)"; fail=1; fi
}
status() { curl -s -o /dev/null -w '%{http_code}' "$@" || true; }

# build <name> <example folder> [settings json]: builds a copy, prints the plan's path
build() {
  local name=$1 dir="$work/$1"
  cp -R "$examples/$2" "$dir"
  local args=()
  if [ -n "${3:-}" ]; then printf '%s' "$3" > "$work/$name.settings.json"; args=(--settings "$work/$name.settings.json"); fi
  "$hp" build-plan "${args[@]}" "$dir" > "$work/$name.plan.json"
  local image install command
  image=$(jq -r .image "$work/$name.plan.json")
  install=$(jq -r '.install // ""' "$work/$name.plan.json")
  command=$(jq -r .command "$work/$name.plan.json")
  say "== $name: $(jq -r '[.framework, .kind, .runtime, .runtime_version] | join(" ")' "$work/$name.plan.json")"
  say "   $(jq -r .runtime_reason "$work/$name.plan.json")"
  local script=$command
  [ -n "$install" ] && script="$install && $command"
  if ! docker run --rm -v "$dir:/app" -w /app "$image" sh -c "$script" > "$work/$name.build.log" 2>&1; then
    tail -40 "$work/$name.build.log" >&2
    check "$name" build "the build failed"
    return 1
  fi
}

# start <name>: runs what the build made, alone, on a port; prints the base URL
start() {
  local name=$1 dir="$work/$1" kind artifact run
  kind=$(jq -r .kind "$work/$name.plan.json")
  artifact=$(jq -r .artifact "$work/$name.plan.json")
  run=$(jq -r '.run // ""' "$work/$name.plan.json")
  port=$((port + 1))
  local mount cmd
  if [ "$kind" = bundle ]; then
    mount="$dir/$artifact" cmd="./bin $run"
  else
    # a binary: only it, in an empty folder
    mkdir -p "$work/$name.bin" && cp "$dir/$artifact" "$work/$name.bin/app"
    mount="$work/$name.bin" cmd="./app"
  fi
  # shellcheck disable=SC2086
  docker run -d --label homeport-e2e --name "e2e-$name" -v "$mount:/srv" -w /srv -p "127.0.0.1:$port:8080" \
    -e PORT=8080 -e HOST=0.0.0.0 -e HOSTNAME=0.0.0.0 -e NODE_ENV=production debian:bookworm-slim $cmd >/dev/null
  for _ in $(seq 1 100); do
    [ "$(status "http://127.0.0.1:$port/")" != 000 ] && break
    sleep 0.2
  done
  echo "http://127.0.0.1:$port"
}

stop() { docker logs "e2e-$1" > "$work/$1.serve.log" 2>&1 || true; docker rm -f "e2e-$1" >/dev/null 2>&1 || true; }

# a TanStack Start app: SSR, an asset, a 404, its server function
tanstack() {
  local name=$1 url=$2 runtime=$3 body asset id
  body=$(curl -s "$url/")
  case "$body" in *"Rendered on the server ("*"$runtime "*) check "$name" "SSR / on $runtime" ok ;; *) check "$name" "SSR / on $runtime" "no server-rendered page, or the wrong runtime" ;; esac
  asset=$(grep -o '/assets/[^"]*\.js' <<<"$body" | head -1 || true)
  [ -n "$asset" ] && [ "$(status "$url$asset")" = 200 ] && check "$name" "asset $asset" ok || check "$name" "asset $asset" "not 200"
  [ "$(status "$url/nope")" = 404 ] && check "$name" "404" ok || check "$name" "404" "not 404"
  id=$(grep -rhoE 'id: "[0-9a-f]{64}"' --exclude-dir=node_modules "$work/$name/$(jq -r .artifact "$work/$name.plan.json")" | head -1 | cut -d'"' -f2 || true)
  [ -n "$id" ] && [ "$(status -H 'Sec-Fetch-Site: same-origin' -H 'x-tsr-serverFn: true' "$url/_serverFn/$id")" = 200 ] &&
    check "$name" "server function" ok || check "$name" "server function" "not 200 (id ${id:-none})"
  [ "$(status -H 'Origin: https://evil.example' -H 'x-tsr-serverFn: true' "$url/_serverFn/$id")" = 403 ] &&
    check "$name" "a cross-site server function call is refused" ok || check "$name" "cross-site" "not 403"
}

# the SvelteKit example: SSR, an asset, a 404, its form action
sveltekit() {
  local name=$1 url=$2 body asset host=app.example.com
  body=$(curl -s -H "Host: $host" "$url/")
  case "$body" in *"Rendered on the server"*"https://$host"*) check "$name" "SSR /, its origin https://$host" ok ;; *) check "$name" "SSR /" "no page, or the wrong origin" ;; esac
  asset=$(grep -o '_app/immutable/[^"]*\.js' <<<"$body" | head -1 || true)
  [ -n "$asset" ] && [ "$(status "$url/$asset")" = 200 ] && check "$name" "asset /$asset" ok || check "$name" "asset /$asset" "not 200"
  [ "$(status "$url/nope")" = 404 ] && check "$name" "404" ok || check "$name" "404" "not 404"
  case "$(curl -s -X POST -H 'Accept: text/html' -H "Host: $host" -H 'X-Forwarded-Proto: https' -H "Origin: https://$host" -d name=Ramon "$url/?/greet")" in
    *"Hello, Ramon."*) check "$name" "form action" ok ;; *) check "$name" "form action" "no greeting" ;; esac
  [ "$(status -X POST -H "Host: $host" -H 'Origin: https://evil.example' -d name=x "$url/?/greet")" = 403 ] &&
    check "$name" "a cross-site form post is refused" ok || check "$name" "cross-site post" "not 403"
  case "$(curl -s -H "Host: $host" -H 'X-Forwarded-Host: evil.example' "$url/")" in
    *evil.example*) check "$name" "X-Forwarded-Host" "a visitor's X-Forwarded-Host became the origin" ;; *) check "$name" "a spoofed X-Forwarded-Host is ignored" ok ;; esac
}

run() { # name, folder, settings, checker, checker args...
  local name=$1 folder=$2 settings=$3 checker=$4; shift 4
  if build "$name" "$folder" "$settings"; then
    local url; url=$(start "$name")
    "$checker" "$name" "$url" "$@"
    stop "$name"
  fi
}

run tanstack-nitro tanstack-start-app "" tanstack Node
run tanstack-nitro-bun tanstack-start-bun-app "" tanstack Bun
run sveltekit-bun sveltekit-bun-app "" sveltekit
# the same app, not compiled: adapter-bun's bundle
mkdir -p "$work/variants"
cp -R "$examples/sveltekit-bun-app" "$work/variants/sveltekit-bun-bundle"
sed -i 's/adapter({ buildOptions: { compile: true } })/adapter()/' "$work/variants/sveltekit-bun-bundle/vite.config.ts"
grep -q 'adapter: adapter()' "$work/variants/sveltekit-bun-bundle/vite.config.ts"
examples=$work/variants run sveltekit-bun-bundle sveltekit-bun-bundle "" sveltekit

if [ "$fail" = 0 ]; then say "all passed"; else
  for f in "$work"/*.serve.log; do say "--- $f"; tail -20 "$f" >&2; done
  exit 1
fi
