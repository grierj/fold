#!/usr/bin/env bash
# Inside the disposable runner (an Alpine container with its own Docker
# daemon, started by restore-drill-nested.sh): install the browser runtime
# and prove it works BEFORE any measurement or drill. Steps, each fatal unless noted:
#   1. web/package-lock.json pins @playwright/test and playwright-core
#      consistently (qc1_browser_lock_problems);
#   2. Alpine's chromium and a font are installed (apk);
#   3. `npm ci` installs web/ exactly as locked, without install scripts, without
#      optional packages and without downloading Playwright's browsers
#      (PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD=1: that build does not run on musl);
#   4. the tree is still clean (node_modules is ignored) and the lock file is
#      unchanged;
#   5. browser-preflight.mjs resolves Playwright from web/node_modules, launches
#      the installed Chromium exactly as the measurement and the recovery proof
#      will and exercises the calls they rely on;
#   6. (restore drill only, QC1_PREPARE_FIREFOX=1) Firefox. Playwright's Firefox is a
#      glibc build and cannot start on this Alpine runner (spawn fails with ENOENT: no
#      /lib64/ld-linux-x86-64.so.2), so firefox.Dockerfile builds a Debian container
#      with the locked Playwright and its Firefox, and the same preflight launches
#      Firefox in it (firefox-runtime.sh). If the image cannot be BUILT (network,
#      registry, apt), the reason is recorded in firefox-runtime.json and the drill
#      writes the first-church hand check with it; if the image builds but Firefox
#      does not start, this preparation FAILS, before any drill is spent.
# It writes browser-runtime.json (versions, paths, the lock file's hash, the
# preflight results) and browser.env (QC1_CHROMIUM=..., and for the drill
# FOLD_FIREFOX_RUNTIME=... and FOLD_FIREFOX_IMAGE=...) into the directory given.
#
#   QC1_WEB_DIR=/work/fold/web [QC1_PREPARE_FIREFOX=1] browser-prepare.sh OUT_DIR
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

# --- Firefox (restore drill only) --------------------------------------------------------------------------------------------
ffjson="$OUT/firefox-runtime.json"
ff_image=""
if [[ "${QC1_PREPARE_FIREFOX:-0}" == 1 ]]; then
  # shellcheck source=deploy/digitalocean/qc1/firefox-runtime.sh
  source "$HERE/firefox-runtime.sh"
  node_image="$(firefox_node_image "$REPO/.nvmrc")" || fail "cannot read the Node major from $REPO/.nvmrc"
  ff_tag="$(firefox_image_tag "$WEB/package-lock.json" "$HERE/firefox.Dockerfile" "$node_image")"
  ctx="$(mktemp -d)"
  cp "$WEB/package.json" "$WEB/package-lock.json" "$ctx/"
  if timeout 1800 docker build --build-arg "NODE_IMAGE=$node_image" -f "$HERE/firefox.Dockerfile" -t "$ff_tag" "$ctx" >"$OUT/firefox-build.log" 2>&1; then
    rm -rf "$ctx"
    mapfile -t ff_flags < <(firefox_run_flags)
    ff_pre="$(docker run "${ff_flags[@]}" -e QC1_WEB_DIR=/app -e QC1_PREFLIGHT_FOR=firefox -v "$HERE:/qc1:ro" "$ff_tag" node /qc1/browser-preflight.mjs 2>"$OUT/firefox-preflight.err")" \
      || { cat "$OUT/firefox-preflight.err" >&2; fail "Firefox was installed in $ff_tag but did not pass its preflight (see above); the drill's Firefox proof would fail"; }
    jq -e '.ok == true' <<<"$ff_pre" >/dev/null || fail "the Firefox preflight did not pass: $ff_pre"
    ff_id="$(docker image inspect --format '{{.Id}}' "$ff_tag")"
    jq -n --arg image "$ff_tag" --arg id "$ff_id" --arg nodeImage "$node_image" --argjson preflight "$ff_pre" \
      '{available:true, image:$image, imageId:$id, nodeImage:$nodeImage, why:"Playwright Firefox is a glibc build; it runs in this Debian container, not on the Alpine runner", preflight:$preflight}' >"$ffjson"
    ff_image="$ff_tag"
  else
    rm -rf "$ctx"
    reason="$(firefox_reason "$OUT/firefox-build.log")"
    jq -n --arg nodeImage "$node_image" --arg reason "$reason" \
      '{available:false, stage:"build", nodeImage:$nodeImage, reason:$reason, handCheck:"Firefox could not be installed in the runner; the first-church hand check covers it"}' >"$ffjson"
    echo "browser-prepare: WARNING: the Firefox runtime could not be built; the drill will record a hand check ($ffjson, $OUT/firefox-build.log): $reason" >&2
  fi
fi

ff_doc='null'
if [[ -f "$ffjson" ]]; then ff_doc="$(jq -c . "$ffjson")"; fi
jq -n --argjson preflight "$preflight" --argjson firefox "$ff_doc" --arg chromium "$CHROMIUM" --arg lock "$lock_sha" \
  --arg apk "$(apk info -v chromium 2>/dev/null | head -n 1)" --arg node "$(node -v)" --arg npm "$(npm -v)" \
  '{chromiumPath:$chromium, chromiumPackage:$apk, node:$node, npm:$npm, packageLockSha256:$lock, installedWith:"npm ci --ignore-scripts --omit=optional, PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD=1", preflight:$preflight, firefox:$firefox}' >"$OUT/browser-runtime.json"
{
  printf 'QC1_CHROMIUM=%s\n' "$CHROMIUM"
  if [[ -f "$ffjson" ]]; then printf 'FOLD_FIREFOX_RUNTIME=%s\n' "$ffjson"; fi
  if [[ -n "$ff_image" ]]; then printf 'FOLD_FIREFOX_IMAGE=%s\n' "$ff_image"; fi
} >"$OUT/browser.env"
echo "browser-prepare: ok ($(jq -r '.preflight.chromium' "$OUT/browser-runtime.json") with playwright $(jq -r '.preflight.playwright' "$OUT/browser-runtime.json"); firefox: $(jq -r 'if .firefox == null then "not requested" elif .firefox.available then "ready in " + .firefox.image else "unavailable, hand check" end' "$OUT/browser-runtime.json"))"
