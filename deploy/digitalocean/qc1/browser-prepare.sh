#!/usr/bin/env bash
# Inside the disposable QC-1 runner (an Alpine container with its own Docker
# daemon, started by restore-drill-nested.sh --qc1): install the browser runtime
# and prove it works BEFORE any measurement. Steps, each fatal:
#   1. web/package-lock.json pins @playwright/test and playwright-core
#      consistently (qc1_browser_lock_problems);
#   2. Alpine's chromium and a font are installed (apk);
#   3. `npm ci` installs web/ exactly as locked, without install scripts, without
#      optional packages and without downloading Playwright's browsers
#      (PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD=1: that build does not run on musl);
#   4. the tree is still clean (node_modules is ignored) and the lock file is
#      unchanged;
#   5. browser-preflight.mjs resolves Playwright from web/node_modules, launches
#      the installed Chromium exactly as the measurement will and exercises the
#      calls it relies on.
# It writes browser-runtime.json (versions, paths, the lock file's hash, the
# preflight result) and browser.env (QC1_CHROMIUM=...) into the directory given.
#
#   QC1_WEB_DIR=/work/fold/web browser-prepare.sh OUT_DIR
set -Eeuo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
# shellcheck source=deploy/digitalocean/qc1/browser-runtime.sh
source "$HERE/browser-runtime.sh"
OUT="${1:?output directory required}"
WEB="${QC1_WEB_DIR:?QC1_WEB_DIR is required}"
REPO="$(git -C "$WEB" rev-parse --show-toplevel)"
mkdir -p "$OUT"
fail() { echo "browser-prepare: $*" >&2; exit 1; }

problems="$(qc1_browser_lock_problems "$WEB/package.json" "$WEB/package-lock.json")"
# shellcheck disable=SC2001 # a per-line prefix, not a substitution
[[ -z "$problems" ]] || fail "the lock file is not sound:"$'\n'"$(sed 's/^/  - /' <<<"$problems")"
lock_sha="$(sha256sum <"$WEB/package-lock.json" | cut -d' ' -f1)"

# shellcheck disable=SC2046 # a word list of package names
apk add --no-cache $(qc1_browser_apk_packages | tr '\n' ' ') >/dev/null || fail "apk add failed"
CHROMIUM="$(command -v chromium || command -v chromium-browser || true)"
[[ -n "$CHROMIUM" && -x "$CHROMIUM" ]] || fail "no chromium binary after apk add"

( cd "$WEB" && PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD=1 npm ci --ignore-scripts --omit=optional --no-audit --no-fund --loglevel=error ) || fail "npm ci failed"
[[ -z "$(git --no-optional-locks -C "$REPO" status --porcelain)" ]] || fail "npm ci left the tree dirty"
[[ "$(sha256sum <"$WEB/package-lock.json" | cut -d' ' -f1)" == "$lock_sha" ]] || fail "npm ci changed the lock file"

export QC1_CHROMIUM="$CHROMIUM" QC1_NO_SANDBOX=1 FOLD_DRILL_ISOLATED_DAEMON=1
preflight="$(node "$HERE/browser-preflight.mjs")" || fail "the browser preflight failed (see above)"
jq -e '.ok == true' <<<"$preflight" >/dev/null || fail "the browser preflight did not pass: $preflight"

jq -n --argjson preflight "$preflight" --arg chromium "$CHROMIUM" --arg lock "$lock_sha" \
  --arg apk "$(apk info -v chromium 2>/dev/null | head -n 1)" --arg node "$(node -v)" --arg npm "$(npm -v)" \
  '{chromiumPath:$chromium, chromiumPackage:$apk, node:$node, npm:$npm, packageLockSha256:$lock, installedWith:"npm ci --ignore-scripts --omit=optional, PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD=1", preflight:$preflight}' >"$OUT/browser-runtime.json"
printf 'QC1_CHROMIUM=%s\n' "$CHROMIUM" >"$OUT/browser.env"
echo "browser-prepare: ok ($(jq -r '.preflight.chromium' "$OUT/browser-runtime.json") with playwright $(jq -r '.preflight.playwright' "$OUT/browser-runtime.json"))"
