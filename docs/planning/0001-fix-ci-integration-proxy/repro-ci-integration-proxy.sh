#!/usr/bin/env bash
# Reproduce the CI-only failure of the Integration job on this machine.
#
# On a developer machine portless's proxy gets its routes through fs.watch and the
# route is served within ~100ms, so the suite is green. On the GitHub runner the
# watcher is unavailable and portless falls back to polling every 3s, so vroom's
# single immediate probe reads the stale cache and reports {degraded,
# route_not_served}. This script forces a 0.15.6 install into that same
# polling-only mode (by making the fs.watch setup throw, exactly like an
# inotify-starved runner) WITHOUT touching the global portless, ~/.portless or the
# main checkout. It only writes under a scratch dir.
#
# Usage:
#   bash docs/planning/0001-fix-ci-integration-proxy/repro-ci-integration-proxy.sh            # reproduce (forced polling)
#   VROOM_REPRO_TEST_RE='TestRouteSurvivesAProxyRestart' bash .../repro-ci-integration-proxy.sh # narrow
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
SCRATCH="${VROOM_REPRO_SCRATCH:-/tmp/opencode/vroom-ci-repro}"
NODE="$(command -v node)"
NPM="$(command -v npm)"

if [ -z "$NODE" ] || [ -z "$NPM" ]; then
  echo "node/npm not on PATH; the CI job installs Node 24 via setup-node" >&2
  exit 1
fi

mkdir -p "$SCRATCH"
cd "$SCRATCH"

# Fetch the exact pinned version, never the global one, and never install -g.
if [ ! -f "portless-0.15.6.tgz" ]; then
  "$NPM" pack portless@0.15.6 --silent >/dev/null
fi
rm -rf package
tar -xzf portless-0.15.6.tgz   # -> package/dist/cli.js

# Force portless's proxy into its polling fallback: the same branch it takes on the
# runner when fs.watch is unavailable. The `throw` is caught by portless and turns
# into `setInterval(reloadRoutes, 3000)`. Without this, the bug is invisible here.
"$NODE" -e '
const fs = require("fs");
const p = "package/dist/cli.js";
const s = fs.readFileSync(p, "utf8");
const needle = "  try {\n    watcher = fs10.watch(routesPath, () => {";
const repl = "  try {\n    throw new Error(\"repro: forcing portless polling fallback\");\n    watcher = fs10.watch(routesPath, () => {";
if (!s.includes(needle)) { console.error("portless dist changed shape; update the patch"); process.exit(1); }
fs.writeFileSync(p, s.replace(needle, repl));
'

PORTLESS_BIN="$SCRATCH/portless-poll"
cat > "$PORTLESS_BIN" <<EOF
#!/usr/bin/env bash
exec "$NODE" "$SCRATCH/package/dist/cli.js" "\$@"
EOF
chmod +x "$PORTLESS_BIN"

echo "forced-polling portless: $(PORTLESS_BIN="$PORTLESS_BIN" "$PORTLESS_BIN" --version)"
cd "$REPO_ROOT"

ARGS=(-race -count=1 ./internal/portless/)
if [ -n "${VROOM_REPRO_TEST_RE:-}" ]; then
  ARGS=(-count=1 -v -run "$VROOM_REPRO_TEST_RE" ./internal/portless/)
fi

echo "+ PORTLESS_BIN=$PORTLESS_BIN VROOM_PORTLESS_INTEGRATION=1 VROOM_PORTLESS_INTEGRATION_STRICT=1 go test ${ARGS[*]}"
PORTLESS_BIN="$PORTLESS_BIN" \
  VROOM_PORTLESS_INTEGRATION=1 \
  VROOM_PORTLESS_INTEGRATION_STRICT=1 \
  go test "${ARGS[@]}"
