#!/bin/sh
# Bundles server.mjs with srvx, pinned and checked, once per runtime:
# node.mjs (srvx's node adapter) and bun.mjs (its Bun one). Needs Bun.
set -eu
cd "$(dirname "$0")"
BUN=1.4.2
[ "$(bun --version)" = "$BUN" ] || { echo "gen.sh: needs Bun $BUN (CI's), not $(bun --version)" >&2; exit 1; }
SRVX=1.0.5
SUM=sha512-KvSKRpgPG/oaq3cyT614OQ2bAa7DynuGamivCZeoZzIUULOdbQz6wUtnnVuC4+DCx2Zglzo8v5gBYmWIWYhDxA==
t=$(mktemp -d)
trap 'rm -rf "$t"' EXIT
curl -fsSL "https://registry.npmjs.org/srvx/-/srvx-$SRVX.tgz" -o "$t/srvx.tgz"
[ "sha512-$(openssl dgst -sha512 -binary "$t/srvx.tgz" | base64 | tr -d '\n')" = "$SUM" ] || { echo "srvx $SRVX: not the published tarball" >&2; exit 1; }
mkdir -p "$t/node_modules/srvx" && tar -xzf "$t/srvx.tgz" -C "$t/node_modules/srvx" --strip-components=1
cp server.mjs "$t/server.mjs"
for rt in node bun; do
  bun build "$t/server.mjs" --target="$rt" --format=esm --minify-syntax --minify-whitespace --outfile "$t/$rt.mjs" >/dev/null
  { printf '// homeport: server.mjs with srvx %s (%s), bundled by gen.sh with Bun %s - do not edit\n' "$SRVX" "$rt" "$BUN"; cat "$t/$rt.mjs"; } > "$rt.mjs"
done
