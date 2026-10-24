# shellcheck shell=bash disable=SC2034,SC2154
# js-lib.sh: building a JavaScript app the way a hosted build does, and
# running what it made - sourced by js-examples.sh and static-pages.sh, which
# set hp (the homeport binary), examples (the apps' folder), work (a scratch
# folder), fail=0 and port.

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
