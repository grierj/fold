#!/usr/bin/env bash
# On a disposable glibc Ubuntu host (a droplet created for the run, declared with
# FOLD_DRILL_ISOLATED_DAEMON=1; it installs system packages and needs root), install
# the browser runtime and prove it works BEFORE any measurement or drill. Steps, each
# fatal unless noted:
#   1. web/package-lock.json pins @playwright/test and playwright-core
#      consistently (qc1_browser_lock_problems);
#   2. `npm ci` installs web/ exactly as locked, without install scripts, without
#      optional packages and without downloading Playwright's browsers yet;
#   3. the locked Playwright installs its own Chromium and the system libraries it
#      needs (`playwright install --with-deps chromium`, apt);
#   4. the tree is still clean (node_modules is ignored) and the lock file is
#      unchanged;
#   5. browser-preflight.mjs resolves Playwright from web/node_modules, launches
#      that Chromium exactly as the measurement and the recovery proof will and
#      exercises the calls they rely on;
#   6. (restore drill only, QC1_PREPARE_FIREFOX=1) Firefox. firefox.Dockerfile builds
#      a Debian container with the locked Playwright and its Firefox, and the same
#      preflight launches Firefox in it (firefox-runtime.sh), so the proof does not
#      depend on this host's libraries. If the image cannot be BUILT (network,
#      registry, apt), the reason is recorded in firefox-runtime.json and the drill
#      writes the first-church hand check with it; if the image builds but Firefox
#      does not start, this preparation FAILS, before any drill is spent.
# It writes browser-runtime.json (versions, paths, the lock file's hash, the
# preflight results) and browser.env (QC1_CHROMIUM=... and the explicit root sandbox
# setting QC1_NO_SANDBOX=1, and for the drill FOLD_FIREFOX_RUNTIME=... and
# FOLD_FIREFOX_IMAGE=...) into the directory given. Source browser.env
# (`set -a; . OUT/browser.env; set +a`) before the drill or the measurement.
#
#   FOLD_DRILL_ISOLATED_DAEMON=1 QC1_WEB_DIR=$PWD/web [QC1_PREPARE_FIREFOX=1] browser-prepare.sh OUT_DIR
set -Eeuo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
# shellcheck source=deploy/digitalocean/qc1/browser-runtime.sh
source "$HERE/browser-runtime.sh"
# shellcheck source=deploy/digitalocean/isolation.sh
source "$HERE/../isolation.sh"
OUT="${1:?output directory required}"
WEB="${QC1_WEB_DIR:?QC1_WEB_DIR is required}"
REPO="$(git -C "$WEB" rev-parse --show-toplevel)"
fail() { echo "browser-prepare: $*" >&2; exit 1; }

[[ "${FOLD_DRILL_ISOLATED_DAEMON:-}" == 1 ]] || fail 'FOLD_DRILL_ISOLATED_DAEMON=1 is required for a disposable host'
if [[ "${QC1_PREPARE_FIREFOX:-0}" == 1 ]]; then
  isolation_pin_local || exit 1
  info="$(docker info --format '{{json .}}' 2>/dev/null)" || fail 'cannot inspect the local Docker daemon'
  problems="$(isolation_daemon_problems "$info" "$(hostname)")"
  [[ -z "$problems" ]] || fail "$problems"
fi
mkdir -p "$OUT"

problems="$(qc1_browser_lock_problems "$WEB/package.json" "$WEB/package-lock.json")"
# shellcheck disable=SC2001 # a per-line prefix, not a substitution
[[ -z "$problems" ]] || fail "the lock file is not sound:"$'\n'"$(sed 's/^/  - /' <<<"$problems")"
lock_sha="$(sha256sum <"$WEB/package-lock.json" | cut -d' ' -f1)"

( cd "$WEB" && PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD=1 npm ci --ignore-scripts --omit=optional --no-audit --no-fund --loglevel=error ) || fail "npm ci failed"
( cd "$WEB" && node node_modules/playwright/cli.js install --with-deps chromium ) || fail "installing the locked Playwright Chromium failed"
CHROMIUM="$(cd "$WEB" && node -e 'process.stdout.write(require("playwright").chromium.executablePath())')" || fail "cannot resolve the Chromium path from the locked Playwright"
[[ -n "$CHROMIUM" && -x "$CHROMIUM" ]] || fail "no executable Chromium at ${CHROMIUM:-<empty>} after the install"
[[ -z "$(git --no-optional-locks -C "$REPO" status --porcelain)" ]] || fail "npm ci left the tree dirty"
[[ "$(sha256sum <"$WEB/package-lock.json" | cut -d' ' -f1)" == "$lock_sha" ]] || fail "npm ci changed the lock file"

export QC1_CHROMIUM="$CHROMIUM" QC1_NO_SANDBOX=1
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
      '{available:true, image:$image, imageId:$id, nodeImage:$nodeImage, why:"Playwright Firefox runs in this Debian container, so the proof does not depend on the host libraries", preflight:$preflight}' >"$ffjson"
    ff_image="$ff_tag"
  else
    rm -rf "$ctx"
    reason="$(firefox_reason "$OUT/firefox-build.log")"
    jq -n --arg nodeImage "$node_image" --arg reason "$reason" \
      '{available:false, stage:"build", nodeImage:$nodeImage, reason:$reason, handCheck:"Firefox could not be installed on the host; the first-church hand check covers it"}' >"$ffjson"
    echo "browser-prepare: WARNING: the Firefox runtime could not be built; the drill will record a hand check ($ffjson, $OUT/firefox-build.log): $reason" >&2
  fi
fi

ff_doc='null'
if [[ -f "$ffjson" ]]; then ff_doc="$(jq -c . "$ffjson")"; fi
jq -n --argjson preflight "$preflight" --argjson firefox "$ff_doc" --arg chromium "$CHROMIUM" --arg lock "$lock_sha" \
  --arg node "$(node -v)" --arg npm "$(npm -v)" \
  '{chromiumPath:$chromium, node:$node, npm:$npm, packageLockSha256:$lock, installedWith:"npm ci --ignore-scripts --omit=optional, then playwright install --with-deps chromium", preflight:$preflight, firefox:$firefox}' >"$OUT/browser-runtime.json"
{
  printf 'QC1_CHROMIUM=%s\nQC1_NO_SANDBOX=1\n' "$CHROMIUM"
  if [[ -f "$ffjson" ]]; then printf 'FOLD_FIREFOX_RUNTIME=%s\n' "$ffjson"; fi
  if [[ -n "$ff_image" ]]; then printf 'FOLD_FIREFOX_IMAGE=%s\n' "$ff_image"; fi
} >"$OUT/browser.env"
echo "browser-prepare: ok ($(jq -r '.preflight.chromium' "$OUT/browser-runtime.json") with playwright $(jq -r '.preflight.playwright' "$OUT/browser-runtime.json"); firefox: $(jq -r 'if .firefox == null then "not requested" elif .firefox.available then "ready in " + .firefox.image else "unavailable, hand check" end' "$OUT/browser-runtime.json"))"
