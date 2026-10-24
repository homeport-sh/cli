#!/bin/bash
# Builds SvelteKit and Astro apps the way a hosted build does (js-lib.sh),
# then checks the prerendered pages the build put in .homeport/public -
# what homeport's host answers without waking the app - against the app
# itself, running from what the build made (static-pages/parity.mjs): each
# is the app's own answer, byte for byte, from a file, with none of the
# app's code run (no hook, no middleware); and a page the app's code runs
# for is left to the app.
#
#   e2e/static-pages.sh <homeport>
#
# Needs Docker, curl, jq and node.
set -euo pipefail
hp=$(realpath "$1") examples=$(cd "$(dirname "$0")/static-pages" && pwd)
work=$(mktemp -d)
trap 'docker rm -f $(docker ps -aq --filter label=homeport-e2e) >/dev/null 2>&1 || true' EXIT
fail=0
port=18180

# shellcheck source=e2e/js-lib.sh
. "$(dirname "$0")/js-lib.sh"

# pages <name> <marker header> <parity args...>: the build said what it
# serves, and each of it is the app's own answer
pages() {
  local name=$1 marker=$2; shift 2
  local bundle url
  bundle="$work/$name/$(jq -r .artifact "$work/$name.plan.json")"
  if grep -q 'homeport: served without the app' "$work/$name.build.log"; then
    check "$name" "the build log says what's served without the app" ok
  else check "$name" "build log" "no line saying what's served"; fi
  url=$(start "$name")
  if node "$examples/parity.mjs" "$bundle" "$url" "$marker" "$@" >&2; then check "$name" "parity with the app" ok; else check "$name" "parity with the app" "see above"; fi
  stop "$name"
}

for app in sveltekit:x-hook:"--hook /dyn /nope --left /about/ /blog/one/" \
  sveltekit2-base:x-hook:"--hook /docs/dyn --left /docs/about/ /about /docs" \
  astro:x-mw:"--hook /dyn /nope" \
  astro-base-never-file:x-mw:"--hook /docs/dyn --left /docs/about/ /docs/ /about" \
  astro-always-preserve:x-mw:"--hook /dyn/ --left /about /about/"; do
  IFS=: read -r name marker args <<<"$app"
  # shellcheck disable=SC2086
  if build "$name" "$name"; then pages "$name" "$marker" $args; fi
done

if [ "$fail" = 0 ]; then say "all passed"; else
  for f in "$work"/*.serve.log; do say "--- $f"; tail -20 "$f" >&2; done
  exit 1
fi
