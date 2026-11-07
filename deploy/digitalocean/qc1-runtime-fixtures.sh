#!/usr/bin/env bash
# Daemon-free regressions for the QC-1 browser runtime and the runner's ownership of
# what it creates: the shared launch module and response accounting (run with node
# when it exists), the browser preflight against a FAKE Playwright package (module
# resolution is real, the browser is not), the lock-file checks on the real
# web/package-lock.json and on mutated copies, the nested launcher's --qc1 mode
# against a stub docker, and the runner's preflight and volume-ownership handling
# against a stub docker and a stub node. No Docker daemon, database or browser is
# started; nothing here proves a real Chromium launches in a real Alpine runner.
# shellcheck disable=SC2016 # patterns keep their dollar signs literal.
set -Eeuo pipefail
exec </dev/null
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
HERE="$ROOT/deploy/digitalocean"
# shellcheck source=deploy/digitalocean/qc1/lib.sh
source "$HERE/qc1/lib.sh"
# shellcheck source=deploy/digitalocean/qc1/browser-runtime.sh
source "$HERE/qc1/browser-runtime.sh"
WORK="$(mktemp -d /tmp/fold-qc1-runtime-fixture.XXXXXX)"
# shellcheck disable=SC2329 # EXIT trap.
finish() { local rc=$?; rm -rf -- "$WORK"; exit "$rc"; }
trap finish EXIT
trap 'printf "qc1 runtime fixture failed at line %s: %s\n" "$LINENO" "$BASH_COMMAND" >&2' ERR
refused() { local needle="$1" rc=0; shift; "$@" >"$WORK/out" 2>&1 || rc=$?; [[ "$rc" != 0 ]]; [[ -z "$needle" ]] || grep -qF -- "$needle" "$WORK/out"; }
code_of() { local rc=0; "$@" >"$WORK/out" 2>"$WORK/err" || rc=$?; echo "$rc"; }

# --- the lock file: what npm ci installs ------------------------------------------------------------------------
[[ -z "$(qc1_browser_lock_problems "$ROOT/web/package.json" "$ROOT/web/package-lock.json")" ]]
printf '%s\n' "$(qc1_browser_apk_packages)" | grep -qx chromium
lockmut() { # name jq-filter  -> $WORK/lock.<name>.json
  jq -c "$2" "$ROOT/web/package-lock.json" >"$WORK/lock.$1.json"
  qc1_browser_lock_problems "$ROOT/web/package.json" "$WORK/lock.$1.json"
}
lockbad() { local out; out="$(lockmut "$1" "$2")"; grep -qF -- "$3" <<<"$out" || { echo "lock mutation $1 not named ($3): $out" >&2; exit 1; }; }
lockbad no-core 'del(.packages["node_modules/playwright-core"])' 'playwright-core is not in the lock file'
lockbad no-test 'del(.packages["node_modules/@playwright/test"])' '@playwright/test is not in the lock file'
lockbad no-integrity 'del(.packages["node_modules/playwright-core"].integrity)' 'playwright-core has no sha512 integrity'
lockbad md5 '.packages["node_modules/@playwright/test"].integrity = "md5-abc"' '@playwright/test has no sha512 integrity'
lockbad host '.packages["node_modules/playwright-core"].resolved = "https://example.invalid/playwright-core.tgz"' 'does not resolve from the public npm registry'
lockbad nohost 'del(.packages["node_modules/playwright-core"].resolved)' 'does not resolve from the public npm registry'
lockbad skew '.packages["node_modules/playwright-core"].version = "1.0.0"' 'pinned to different versions'
lockbad v1 '.lockfileVersion = 1' 'lockfileVersion 1 is too old'
lockbad nover 'del(.lockfileVersion)' 'lockfileVersion none is too old'
lockbad range '.packages["node_modules/@playwright/test"].version = "9.9.9" | .packages["node_modules/playwright-core"].version = "9.9.9"' 'outside the range'
jq '.devDependencies["@playwright/test"] = "latest"' "$ROOT/web/package.json" >"$WORK/pkg.latest.json"
qc1_browser_lock_problems "$WORK/pkg.latest.json" "$ROOT/web/package-lock.json" | grep -qF 'not a pinned or caret version'
jq 'del(.devDependencies["@playwright/test"])' "$ROOT/web/package.json" >"$WORK/pkg.none.json"
qc1_browser_lock_problems "$WORK/pkg.none.json" "$ROOT/web/package-lock.json" | grep -qF 'is not a dependency in package.json'
qc1_browser_lock_problems "$ROOT/web/package.json" "$WORK/absent-lock.json" | grep -qF 'needs a lock file'
qc1_browser_lock_problems "$WORK/absent-package.json" "$ROOT/web/package-lock.json" | grep -qF 'is missing'
# The real pins are the ones the repository's own browser tests use.
[[ "$(jq -r '.packages["node_modules/@playwright/test"].version' "$ROOT/web/package-lock.json")" == "$(jq -r '.packages["node_modules/playwright-core"].version' "$ROOT/web/package-lock.json")" ]]
# web/node_modules is ignored, so npm ci leaves the tree clean for the clean-tree checks.
git -C "$ROOT" check-ignore -q web/node_modules

# --- browser-prepare.sh: the steps, in order, each fatal ------------------------------------------------------------
BP="$HERE/qc1/browser-prepare.sh"
for pat in 'qc1_browser_lock_problems "$WEB/package.json" "$WEB/package-lock.json"' 'apk add --no-cache' 'PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD=1 npm ci --ignore-scripts --omit=optional --no-audit --no-fund' \
  'npm ci changed the lock file' 'npm ci left the tree dirty' 'export QC1_CHROMIUM="$CHROMIUM" QC1_NO_SANDBOX=1 FOLD_DRILL_ISOLATED_DAEMON=1' 'node "$HERE/browser-preflight.mjs"' \
  "jq -e '.ok == true'" 'browser-runtime.json' 'browser.env' 'packageLockSha256'; do
  grep -qF -- "$pat" "$BP" || { echo "browser-prepare.sh lost: $pat" >&2; exit 1; }
done
l_lock="$(grep -n 'qc1_browser_lock_problems "\$WEB' "$BP" | head -n 1 | cut -d: -f1)"; l_apk="$(grep -n '^apk add' "$BP" | head -n 1 | cut -d: -f1)"
l_npm="$(grep -n 'npm ci --ignore-scripts' "$BP" | head -n 1 | cut -d: -f1)"; l_pre="$(grep -n 'browser-preflight.mjs' "$BP" | grep -v '^#' | tail -n 1 | cut -d: -f1)"
[[ -n "$l_lock" && -n "$l_apk" && -n "$l_npm" && -n "$l_pre" && "$l_lock" -lt "$l_apk" && "$l_apk" -lt "$l_npm" && "$l_npm" -lt "$l_pre" ]]
if grep -nE 'npm install|npm i |npx playwright install|--force|--legacy-peer-deps|npm update' "$BP"; then echo 'browser-prepare.sh installs without the lock' >&2; exit 1; fi
# --- the shared launch rules and response accounting (node) ----------------------------------------------------------
# The restore drill's browser proof loads qc1/browser-launch.cjs by path; restore-drill.sh must name it, and the proof helper must use it only when a runner names a Chromium.
RDS="$HERE/restore-drill.sh"
grep -q 'RESTORE_BROWSER_LAUNCH="\$ROOT/deploy/digitalocean/qc1/browser-launch.cjs"' "$RDS"
grep -q 'restoreLaunchOptions(process.env)' "$HERE/testdata/restore-browser.cjs"
if command -v node >/dev/null; then
  for m in browser.mjs browser-preflight.mjs response-accounting.mjs panel-measure.mjs; do node --check "$HERE/qc1/$m"; done
  node --check "$HERE/qc1/browser-launch.cjs"; node --check "$HERE/testdata/restore-browser.cjs"
  nodet() { node --input-type=module -e "$1"; }
  imp="import * as b from '$HERE/qc1/browser.mjs'; import * as r from '$HERE/qc1/response-accounting.mjs'; import { createRequire } from 'node:module'; import assert from 'node:assert/strict'; const cjs = createRequire('$HERE/qc1/x.mjs')('./browser-launch.cjs');"
  nodet "$imp
  // the ESM module and the CommonJS file the restore proof loads are one implementation
  for (const k of ['majorOf', 'nodeEngineProblem', 'chromiumMajorDelta', 'launchOptions', 'restoreLaunchOptions']) assert.equal(b[k], cjs[k], k);
  // launch options: sandbox switches only for the disposable runner, never for a person's own browser
  assert.deepEqual(b.launchOptions({}), { headless: true, args: ['--enable-precise-memory-info'] });
  assert.equal(b.launchOptions({ QC1_CHROMIUM: '/usr/bin/chromium' }).executablePath, '/usr/bin/chromium');
  assert.ok(!b.launchOptions({ QC1_NO_SANDBOX: '1' }).args.includes('--no-sandbox'));
  assert.ok(!b.launchOptions({ FOLD_DRILL_ISOLATED_DAEMON: '1' }).args.includes('--no-sandbox'));
  assert.ok(!b.launchOptions({ QC1_NO_SANDBOX: '0', FOLD_DRILL_ISOLATED_DAEMON: '1' }).args.includes('--no-sandbox'));
  assert.deepEqual(b.launchOptions({ QC1_NO_SANDBOX: '1', FOLD_DRILL_ISOLATED_DAEMON: '1' }).args, ['--enable-precise-memory-info', '--no-sandbox', '--disable-dev-shm-usage']);
  // the restore proof: exactly what it has always launched unless a runner names a Chromium; then the same executable and sandbox rule
  assert.deepEqual(b.restoreLaunchOptions({}), { headless: true });
  assert.deepEqual(b.restoreLaunchOptions({ QC1_NO_SANDBOX: '1', FOLD_DRILL_ISOLATED_DAEMON: '1' }), { headless: true });
  assert.deepEqual(b.restoreLaunchOptions({ FOLD_DRILL_ISOLATED_DAEMON: '1' }), { headless: true });
  const named = { QC1_CHROMIUM: '/usr/bin/chromium', QC1_NO_SANDBOX: '1', FOLD_DRILL_ISOLATED_DAEMON: '1' };
  assert.deepEqual(b.restoreLaunchOptions(named), b.launchOptions(named));
  assert.equal(b.restoreLaunchOptions({ QC1_CHROMIUM: '/usr/bin/chromium' }).executablePath, '/usr/bin/chromium');
  assert.ok(!b.restoreLaunchOptions({ QC1_CHROMIUM: '/usr/bin/chromium' }).args.includes('--no-sandbox'));
  // engine and version helpers
  assert.equal(b.nodeEngineProblem('>=18', '22.3.0'), null);
  assert.equal(b.nodeEngineProblem('>=18', 'v18.0.0'), null);
  assert.equal(b.nodeEngineProblem(null, '22.3.0'), null);
  assert.match(b.nodeEngineProblem('>=24', '22.3.0'), /older than the engine range/);
  assert.match(b.nodeEngineProblem('^18 || ^20', '22.3.0'), /cannot read/);
  assert.match(b.nodeEngineProblem('>=18', 'weird'), /cannot read the node version/);
  assert.equal(b.chromiumMajorDelta('140.0.7339.0', '148.0.7778.5'), 8);
  assert.equal(b.chromiumMajorDelta('148.1.2.3', '148.0.7778.5'), 0);
  assert.equal(b.chromiumMajorDelta('', '148.0.7778.5'), null);
  assert.equal(b.chromiumMajorDelta('140.0.0.1', undefined), null);
  // decoded body sizes: any unreadable body makes the figure unavailable, never zero
  assert.equal(r.totalBodyBytes([10, 20, 30]), 60);
  assert.equal(r.totalBodyBytes([0]), 0);
  for (const bad of [[], [10, null, 30], [10, undefined], [1.5], [-1], ['5'], [NaN], [Infinity], null, undefined, 'x', [10, 20, true]]) assert.equal(r.totalBodyBytes(bad), null, JSON.stringify(bad));
  const ok = { requests: 3, bad: 0, foreignOrigin: 0 };
  assert.equal(r.walkOrigin(ok, 90, 3), 'real');
  assert.equal(r.walkOrigin({ ...ok, bad: 1 }, 90, 3), 'unverified');
  assert.equal(r.walkOrigin({ ...ok, foreignOrigin: 1 }, 90, 3), 'unverified');
  assert.equal(r.walkOrigin(ok, null, 3), 'unverified');
  assert.equal(r.walkOrigin(ok, 90, 2), 'unverified');
  assert.equal(r.walkOrigin(ok, 90, 4), 'unverified');
  assert.equal(r.walkOrigin({ ...ok, requests: 0 }, 0, 0), 'unverified');
  " >/dev/null

  if grep -n -i 'content-length' "$HERE/qc1/panel-measure.mjs"; then echo 'panel-measure.mjs reads Content-Length' >&2; exit 1; fi
  grep -q 'responseBodies.push(r.body().then((body) => body.length).catch(() => null))' "$HERE/qc1/panel-measure.mjs"
  grep -q 'walk = { requests: 0, bad: 0, foreignOrigin: 0, bytes: null }' "$HERE/qc1/panel-measure.mjs"

  # --- the preflight against a fake Playwright: module resolution is real, the browser is not ------------------------------
  # Two fake packages, as the repository has them: @playwright/test (the measurement) and playwright (the restore proof), over one playwright-core.
  fakepkg() { # dir name version
    mkdir -p "$1/node_modules/$2"
    printf '{"name":"%s","version":"%s","main":"index.js"}\n' "$2" "$3" >"$1/node_modules/$2/package.json"
    cat >"$1/node_modules/$2/index.js" <<'JS'
const fs = require("node:fs");
const via = __VIA__;
module.exports = { chromium: { name: () => "chromium", launch: async (opts) => {
  if (process.env.FAKE_OPTS_OUT) fs.writeFileSync(process.env.FAKE_OPTS_OUT + "." + via.replace(/\W/g, "_"), JSON.stringify(opts));
  if (process.env.FAKE_LAUNCH_FAIL) throw new Error("browserType.launch: Executable doesn't exist at " + (opts.executablePath ?? "default") + "\nmore");
  const page = {
    goto: async () => {},
    evaluate: async () => ({ items: Number(process.env.FAKE_ITEMS ?? 2), longtask: process.env.FAKE_NOLONG ? false : true }),
  };
  const context = {
    newPage: async () => page,
    cookies: async () => process.env.FAKE_NOCOOKIE ? [] : [{ name: "fold-session", secure: true, httpOnly: true }],
    newCDPSession: async () => ({ send: async (m) => m === "Performance.getMetrics"
      ? { metrics: [{ name: "JSHeapUsedSize", value: Number(process.env.FAKE_HEAP ?? 1000) }, { name: "Nodes", value: Number(process.env.FAKE_NODES ?? 5) }] } : {} }),
  };
  return { version: () => process.env.FAKE_VERSION ?? "148.0.7778.5", close: async () => {}, newContext: async () => context };
} } };
module.exports.firefox = Object.assign({}, module.exports.chromium, { name: () => "firefox" });
JS
    sed -i "s|__VIA__|\"$2\"|" "$1/node_modules/$2/index.js"
  }
  mkweb() { # dir test-version core-version engine [playwright-version]
    local d="$1"; mkdir -p "$d/node_modules/playwright-core"
    printf '{"name":"web"}\n' >"$d/package.json"
    fakepkg "$d" @playwright/test "$2"; fakepkg "$d" playwright "${5:-$2}"
    printf '{"name":"playwright-core","version":"%s","engines":{"node":"%s"}}\n' "$3" "$4" >"$d/node_modules/playwright-core/package.json"
    printf '{"browsers":[{"name":"chromium","browserVersion":"148.0.7778.5"}]}\n' >"$d/node_modules/playwright-core/browsers.json"
  }
  PF="$HERE/qc1/browser-preflight.mjs"
  mkweb "$WORK/web" 1.64.1 1.64.1 '>=18'
  [[ "$(code_of env -u QC1_WEB_DIR node "$PF")" == 2 ]]
  [[ "$(QC1_WEB_DIR="$WORK/web" QC1_PREFLIGHT_FOR=nonsense code_of node "$PF")" == 2 ]]
  mkdir -p "$WORK/emptyweb"; printf '{}\n' >"$WORK/emptyweb/package.json"
  [[ "$(QC1_WEB_DIR="$WORK/emptyweb" code_of node "$PF")" == 3 ]]; grep -q 'does not resolve' "$WORK/err"
  [[ "$(QC1_WEB_DIR="$WORK/nonexistent" code_of node "$PF")" == 3 ]]
  # Only @playwright/test installed (no `playwright` package): the restore proof could not run, so the preflight does not pass.
  rm -rf "$WORK/halfweb"; cp -r "$WORK/web" "$WORK/halfweb"; rm -rf "$WORK/halfweb/node_modules/playwright"
  [[ "$(QC1_WEB_DIR="$WORK/halfweb" code_of node "$PF")" == 3 ]]
  # Both consumers, each through its own package and its own options.
  rm -f "$WORK"/opts.json.*
  rc="$(QC1_WEB_DIR="$WORK/web" QC1_CHROMIUM=/usr/bin/chromium FAKE_OPTS_OUT="$WORK/opts.json" code_of node "$PF")"
  [[ "$rc" == 0 ]] || { cat "$WORK/err" "$WORK/out" >&2; exit 1; }
  jq -e --arg w "$WORK/web" '.ok == true and .consumers == ["qc1","restore"] and .playwright == "1.64.1" and .playwrightPackage == "1.64.1" and .playwrightCore == "1.64.1" and .chromium == "148.0.7778.5" and .majorDelta == 0
    and (.resolvedFrom | startswith($w + "/node_modules/")) and .sandbox == true
    and .smoke.qc1.via == "@playwright/test" and .smoke.qc1.domItems == 2 and .smoke.qc1.longTaskObserver == true and .smoke.qc1.heapUsedBytes > 0 and .smoke.qc1.domNodes > 0
    and .smoke.restore.via == "playwright" and .smoke.restore.domItems == 2 and .smoke.restore.secureLocalhostCookie == true' "$WORK/out" >/dev/null
  jq -e '.executablePath == "/usr/bin/chromium" and .headless == true and (.args | index("--enable-precise-memory-info") != null) and (.args | index("--no-sandbox") == null)' "$WORK/opts.json._playwright_test" >/dev/null
  jq -e '.executablePath == "/usr/bin/chromium" and .headless == true and (.args | index("--no-sandbox") == null)' "$WORK/opts.json.playwright" >/dev/null
  # Each consumer's launch is the one it will really make: the restore proof, with no runner Chromium named, is exactly { headless: true }.
  rm -f "$WORK"/opts.json.*; [[ "$(QC1_WEB_DIR="$WORK/web" FAKE_OPTS_OUT="$WORK/opts.json" code_of node "$PF")" == 0 ]]
  [[ "$(cat "$WORK/opts.json.playwright")" == '{"headless":true}' ]]
  jq -e '.args | index("--enable-precise-memory-info") != null' "$WORK/opts.json._playwright_test" >/dev/null
  # In a runner (sandbox switches on, Chromium named) both launch the same executable with the same switches.
  rm -f "$WORK"/opts.json.*
  rc="$(QC1_WEB_DIR="$WORK/web" QC1_CHROMIUM=/usr/bin/chromium QC1_NO_SANDBOX=1 FOLD_DRILL_ISOLATED_DAEMON=1 FAKE_OPTS_OUT="$WORK/opts.json" code_of node "$PF")"
  [[ "$rc" == 0 ]] && jq -e '.sandbox == false' "$WORK/out" >/dev/null
  [[ "$(jq -S -c . "$WORK/opts.json.playwright")" == "$(jq -S -c . "$WORK/opts.json._playwright_test")" ]]
  jq -e '.args | index("--no-sandbox") != null' "$WORK/opts.json.playwright" >/dev/null
  # One consumer at a time (what qc1-measure.sh and restore-drill.sh each ask for).
  rm -f "$WORK"/opts.json.*
  rc="$(QC1_WEB_DIR="$WORK/web" QC1_PREFLIGHT_FOR=restore FAKE_OPTS_OUT="$WORK/opts.json" code_of node "$PF")"
  [[ "$rc" == 0 && -e "$WORK/opts.json.playwright" && ! -e "$WORK/opts.json._playwright_test" ]] && jq -e '.consumers == ["restore"] and (.smoke | keys) == ["restore"]' "$WORK/out" >/dev/null
  rm -f "$WORK"/opts.json.*
  rc="$(QC1_WEB_DIR="$WORK/web" QC1_PREFLIGHT_FOR=qc1 FAKE_OPTS_OUT="$WORK/opts.json" code_of node "$PF")"
  [[ "$rc" == 0 && ! -e "$WORK/opts.json.playwright" && -e "$WORK/opts.json._playwright_test" ]]
  # Failures.
  [[ "$(QC1_WEB_DIR="$WORK/web" FAKE_LAUNCH_FAIL=1 code_of node "$PF")" == 3 ]]; grep -q 'Chromium did not launch for qc1' "$WORK/err"
  [[ "$(QC1_WEB_DIR="$WORK/web" QC1_PREFLIGHT_FOR=restore FAKE_LAUNCH_FAIL=1 code_of node "$PF")" == 3 ]]; grep -q 'did not launch for restore' "$WORK/err"
  [[ "$(QC1_WEB_DIR="$WORK/web" FAKE_ITEMS=1 code_of node "$PF")" == 6 ]]; grep -q 'DOM query' "$WORK/err"
  [[ "$(QC1_WEB_DIR="$WORK/web" FAKE_NOLONG=1 code_of node "$PF")" == 6 ]]; grep -q 'longtask observer' "$WORK/err"
  [[ "$(QC1_WEB_DIR="$WORK/web" QC1_PREFLIGHT_FOR=restore FAKE_NOLONG=1 code_of node "$PF")" == 0 ]]   # the restore proof does not use it
  [[ "$(QC1_WEB_DIR="$WORK/web" FAKE_HEAP=0 code_of node "$PF")" == 6 ]]; grep -q 'JSHeapUsedSize' "$WORK/err"
  [[ "$(QC1_WEB_DIR="$WORK/web" FAKE_NODES=0 code_of node "$PF")" == 6 ]]; grep -q 'Nodes' "$WORK/err"
  [[ "$(QC1_WEB_DIR="$WORK/web" QC1_PREFLIGHT_FOR=restore FAKE_NOCOOKIE=1 code_of node "$PF")" == 6 ]]; grep -q 'Secure cookie on localhost' "$WORK/err"
  [[ "$(QC1_WEB_DIR="$WORK/web" QC1_PREFLIGHT_FOR=qc1 FAKE_NOCOOKIE=1 code_of node "$PF")" == 0 ]]
  [[ "$(QC1_WEB_DIR="$WORK/web" FAKE_VERSION=120.0.0.1 code_of node "$PF")" == 7 ]]; grep -q 'major versions from' "$WORK/err"
  jq -e '.ok == false and .majorDelta == 28' "$WORK/out" >/dev/null
  [[ "$(QC1_WEB_DIR="$WORK/web" FAKE_VERSION=120.0.0.1 QC1_CHROMIUM_MAJOR_TOLERANCE=40 code_of node "$PF")" == 0 ]]
  [[ "$(QC1_WEB_DIR="$WORK/web" FAKE_VERSION=145.0.0.1 code_of node "$PF")" == 0 ]]
  mkweb "$WORK/skew" 1.64.1 1.64.0 '>=18'
  [[ "$(QC1_WEB_DIR="$WORK/skew" code_of node "$PF")" == 6 ]]; grep -q 'differ' "$WORK/err"
  mkweb "$WORK/pkgskew" 1.64.1 1.64.1 '>=18' 1.63.0
  [[ "$(QC1_WEB_DIR="$WORK/pkgskew" code_of node "$PF")" == 6 ]]; grep -q 'differ' "$WORK/err"
  mkweb "$WORK/oldnode" 1.64.1 1.64.1 '>=99'
  [[ "$(QC1_WEB_DIR="$WORK/oldnode" code_of node "$PF")" == 3 ]]; grep -q 'older than the engine range' "$WORK/err"
  # Node against .nvmrc (24): recorded, fatal only when asked, so an older Alpine nodejs does not stop a run Playwright itself accepts.
  jq -e '.nvmrc == {"value":null,"major":null,"nodeMajor":'"$(node -p 'process.versions.node.split(".")[0]')"',"matches":null}' <<<"$(QC1_WEB_DIR="$WORK/web" node "$PF" 2>/dev/null)" >/dev/null
  mkdir -p "$WORK/nv/web"; cp -r "$WORK/web/." "$WORK/nv/web/"
  printf '999\n' >"$WORK/nv/.nvmrc"
  [[ "$(QC1_WEB_DIR="$WORK/nv/web" code_of node "$PF")" == 0 ]]; jq -e '.ok == true and .nvmrc.value == "999" and .nvmrc.major == 999 and .nvmrc.matches == false' "$WORK/out" >/dev/null
  [[ "$(QC1_WEB_DIR="$WORK/nv/web" QC1_REQUIRE_NVMRC_NODE=1 code_of node "$PF")" == 8 ]]; grep -q 'is not .nvmrc 999' "$WORK/err"
  node -p 'process.versions.node.split(".")[0]' >"$WORK/nv/.nvmrc"
  [[ "$(QC1_WEB_DIR="$WORK/nv/web" QC1_REQUIRE_NVMRC_NODE=1 code_of node "$PF")" == 0 ]]; jq -e '.nvmrc.matches == true' "$WORK/out" >/dev/null
  printf 'v%s\n' "$(node -p 'process.versions.node.split(".")[0]')" >"$WORK/nv/.nvmrc"
  [[ "$(QC1_WEB_DIR="$WORK/nv/web" QC1_REQUIRE_NVMRC_NODE=1 code_of node "$PF")" == 0 ]]
  # the measurement uses the same module: a missing or unlaunchable browser is exit 3 there too, before any network call
  PM="$HERE/qc1/panel-measure.mjs"
  pmenv() { env QC1_URL=http://127.0.0.1:9 QC1_ADMIN_EMAIL=a QC1_ADMIN_PASSWORD=b QC1_TARGET_EMAIL=c QC1_EXPECT=1 QC1_OUT="$WORK/pm.json" "$@"; }
  [[ "$(code_of pmenv QC1_WEB_DIR="$WORK/emptyweb" node "$PM")" == 3 ]]; grep -q 'does not resolve' "$WORK/err"
  [[ "$(code_of pmenv QC1_WEB_DIR="$WORK/web" FAKE_LAUNCH_FAIL=1 node "$PM")" == 3 ]]; grep -q 'Chromium did not launch' "$WORK/err"
  [[ "$(code_of env -u QC1_URL QC1_WEB_DIR="$WORK/web" QC1_ADMIN_EMAIL=a QC1_ADMIN_PASSWORD=b QC1_TARGET_EMAIL=c QC1_EXPECT=1 QC1_OUT="$WORK/pm.json" node "$PM")" == 2 ]]
  [[ ! -e "$WORK/pm.json" ]]

  # --- the restore drill's browser proof: the helper itself, with a fake `playwright` -----------------------------------------
  RB="$HERE/testdata/restore-browser.cjs"
  rbenv() { env RESTORE_WEB_PACKAGE="$WORK/web/package.json" RESTORE_TARGET_ID=7 RESTORE_BROWSER_ARTIFACTS="$WORK/art" RESTORE_SCREENSHOT="$WORK/s.png" RESTORE_BROWSER_PROOF="$WORK/p.json" FAKE_OPTS_OUT="$WORK/rb.json" FAKE_LAUNCH_FAIL=1 "$@"; }
  rm -f "$WORK"/rb.json.*
  # a host with Playwright's own browsers: the launch is exactly what it always was, even with the launch file named
  [[ "$(code_of rbenv node "$RB")" == 1 ]]; [[ "$(cat "$WORK/rb.json.playwright")" == '{"headless":true}' ]]
  [[ "$(code_of rbenv RESTORE_BROWSER_LAUNCH="$HERE/qc1/browser-launch.cjs" node "$RB")" == 1 ]]; [[ "$(cat "$WORK/rb.json.playwright")" == '{"headless":true}' ]]
  # a runner: the named Chromium and its switches, the same options the preflight launched
  [[ "$(code_of rbenv RESTORE_BROWSER_LAUNCH="$HERE/qc1/browser-launch.cjs" QC1_CHROMIUM=/usr/bin/chromium QC1_NO_SANDBOX=1 FOLD_DRILL_ISOLATED_DAEMON=1 node "$RB")" == 1 ]]
  jq -e '.executablePath == "/usr/bin/chromium" and .headless == true and (.args | index("--no-sandbox") != null) and (.args | index("--disable-dev-shm-usage") != null)' "$WORK/rb.json.playwright" >/dev/null
  # a runner whose launch file was not named falls back to the default: restore-drill.sh must name it (checked above)
  [[ "$(code_of rbenv QC1_CHROMIUM=/usr/bin/chromium node "$RB")" == 1 ]]; [[ "$(cat "$WORK/rb.json.playwright")" == '{"headless":true}' ]]
  # Firefox never takes the Chromium path
  rm -f "$WORK"/rb.json.*
  [[ "$(code_of rbenv RESTORE_BROWSER=firefox RESTORE_BROWSER_LAUNCH="$HERE/qc1/browser-launch.cjs" QC1_CHROMIUM=/usr/bin/chromium node "$RB")" == 1 ]]; [[ "$(cat "$WORK/rb.json.playwright")" == '{"headless":true}' ]]
  # runPrivateBrowser: default options unchanged for existing callers, given options passed through
  node -e "
    const { runPrivateBrowser } = require('$RB'); const assert = require('node:assert/strict');
    const seen = []; const engine = { name: () => 'chromium', launch: async (o) => { seen.push(o); throw new Error('stop'); } };
    const base = { engine, targetId: 7, artifacts: '$WORK/art2', screenshot: '$WORK/s2.png', cookieProof: '$WORK/p2.json' };
    (async () => {
      await assert.rejects(runPrivateBrowser(base), /stop/);
      await assert.rejects(runPrivateBrowser({ ...base, launchOptions: { headless: true, executablePath: '/x' } }), /stop/);
      assert.deepEqual(seen, [{ headless: true }, { headless: true, executablePath: '/x' }]);
    })().catch((e) => { console.error(e); process.exit(1); });
  "
else
  echo 'qc1 runtime fixtures: node is not installed; skipped the module, accounting, preflight and restore-proof tests'
fi
# The preflight and the measurement go through the same two modules whether or not node ran above.
grep -q 'launchOptions(env)' "$HERE/qc1/browser-preflight.mjs"; grep -q 'launchOptions(env)' "$HERE/qc1/panel-measure.mjs"
grep -q 'restoreLaunchOptions(env)' "$HERE/qc1/browser-preflight.mjs"; grep -q 'pw.chromiumViaPlaywright' "$HERE/qc1/browser-preflight.mjs"
grep -q 'resolvePlaywright(env.QC1_WEB_DIR)' "$HERE/qc1/browser-preflight.mjs"; grep -q 'resolvePlaywright(env.QC1_WEB_DIR)' "$HERE/qc1/panel-measure.mjs"
if grep -nE "createRequire|from \"@playwright|require\(.@playwright" "$HERE/qc1/browser-preflight.mjs" "$HERE/qc1/panel-measure.mjs"; then echo 'a script resolves Playwright by itself' >&2; exit 1; fi
grep -q 'createRequire(path.join(webDir, "package.json"))' "$HERE/qc1/browser.mjs"
# The restore proof still requires the `playwright` package the preflight resolves, and nothing else.
grep -q "createRequire(process.env.RESTORE_WEB_PACKAGE)('playwright')" "$HERE/testdata/restore-browser.cjs"

# --- restore-drill.sh checks the browser itself, early, and keeps the evidence -----------------------------------------------
pf_l="$(grep -n 'QC1_PREFLIGHT_FOR=restore QC1_WEB_DIR="\$ROOT/web" node' "$RDS" | head -n 1 | cut -d: -f1)"
iso_l="$(grep -n '^isolation_require_disposable || exit 1' "$RDS" | cut -d: -f1)"
boot_l="$(grep -n '^drill_case_begin bootstrap$' "$RDS" | cut -d: -f1)"
ev_l="$(grep -n 'browser-preflight.json' "$RDS" | head -n 1 | cut -d: -f1)"
proof_l="$(grep -n 'node "\$WORK/private-browser.cjs"' "$RDS" | head -n 1 | cut -d: -f1)"
[[ -n "$pf_l" && -n "$iso_l" && -n "$boot_l" && -n "$ev_l" && -n "$proof_l" && "$iso_l" -lt "$pf_l" && "$pf_l" -lt "$boot_l" && "$boot_l" -lt "$ev_l" && "$ev_l" -lt "$proof_l" ]]
sed -n "${pf_l},$((pf_l+3))p" "$RDS" | grep -q "jq -e '.ok == true'"
sed -n "$((pf_l - 1)),$((pf_l + 1))p" "$RDS" | grep -q 'the browser preflight failed\|BROWSER_PREFLIGHT='
grep -q "printf '%s\\\\n' \"\$BROWSER_PREFLIGHT\" >\"\$WORK/browser-preflight.json\"" "$RDS"
# No flag skips it: there is no --no-browser in the drill.
if sed -n '/^while (( \$# )); do/,/^done/p' "$RDS" | grep -q -- '--no-browser'; then echo 'restore-drill.sh can skip the browser preflight' >&2; exit 1; fi

# --- the nested launcher, both modes, against a stub docker -------------------------------------------------------------------------
mkdir "$WORK/stubbin"
cat >"$WORK/stubbin/docker" <<'STUB'
#!/usr/bin/env bash
printf '%s\n' "$*" >>"$STUB_LOG"
case "$1" in
  info) printf '{"Name":"x","ContainersRunning":0}\n' ;;
  run) printf 'c%.0s' {1..64}; echo ;;
  inspect) case "$*" in *NetworkMode*) echo bridge ;; *Mounts*) echo '/var/lib/docker /v' ;; esac ;;
  exec) case "$*" in *browser-prepare.sh*) exit "${STUB_PREP_RC:-0}" ;; *qc1-measure.sh*) exit "${STUB_QC1_RC:-0}" ;; *restore-drill.sh*) exit "${STUB_DRILL_RC:-0}" ;; esac ;;
esac
exit 0
STUB
chmod +x "$WORK/stubbin/docker"
LAUNCH="$WORK/launcher"; mkdir -p "$LAUNCH/deploy/digitalocean"; cp "$HERE/restore-drill-nested.sh" "$LAUNCH/deploy/digitalocean/"
git init -q "$LAUNCH"; git -C "$LAUNCH" config user.name fixture; git -C "$LAUNCH" config user.email fixture@example.invalid
git -C "$LAUNCH" config commit.gpgsign false; git -C "$LAUNCH" config core.hooksPath /dev/null
git -C "$LAUNCH" add -A; git -C "$LAUNCH" commit -qm launcher
launch() { STUB_LOG="$WORK/stub.log" PATH="$WORK/stubbin:$PATH" bash "$LAUNCH/deploy/digitalocean/restore-drill-nested.sh" "$@"; }
# In EACH mode the browser is prepared once, after the clone and before the run, in the same container, and the run receives the Chromium the preparation found.
check_mode() { # mode-name run-script label
  local clone_l prep_l run_l run_line
  clone_l="$(grep -n 'git clone' "$WORK/stub.log" | head -n 1 | cut -d: -f1)"
  prep_l="$(grep -n 'browser-prepare.sh /work/prep$' "$WORK/stub.log" | head -n 1 | cut -d: -f1)"
  run_l="$(grep -n -- "$2" "$WORK/stub.log" | head -n 1 | cut -d: -f1)"
  [[ -n "$clone_l" && -n "$prep_l" && -n "$run_l" && "$clone_l" -lt "$prep_l" && "$prep_l" -lt "$run_l" ]] || { echo "$1: the browser is not prepared between the clone and the run" >&2; cat "$WORK/stub.log" >&2; exit 1; }
  [[ "$(grep -c 'browser-prepare.sh' "$WORK/stub.log")" == 1 ]] || { echo "$1: the browser is prepared more or less than once" >&2; exit 1; }
  grep -q '^exec -e QC1_WEB_DIR=/work/fold/web -w /work/fold c\{64\} bash deploy/digitalocean/qc1/browser-prepare.sh /work/prep$' "$WORK/stub.log"
  run_line="$(sed -n "${run_l}p" "$WORK/stub.log")"
  grep -q -- '-e FOLD_DRILL_ISOLATED_DAEMON=1 -e QC1_NO_SANDBOX=1 -e QC1_WEB_DIR=/work/fold/web -w /work/fold' <<<"$run_line" || { echo "$1: the run does not get the runner switches" >&2; exit 1; }
  grep -q 'set -a; . /work/prep/browser.env; set +a; fi; exec "$@"' <<<"$run_line" || { echo "$1: the run does not load the prepared Chromium" >&2; exit 1; }
  grep -q "^cp c\{64\}:/work/prep/. $WORK/$3/prep/" "$WORK/stub.log"; grep -q "^cp c\{64\}:/work/out/. $WORK/$3/" "$WORK/stub.log"; grep -qE '^rm -fv c{64}$' "$WORK/stub.log"
}
: >"$WORK/stub.log"; launch --out "$WORK/m-drill" -- --keep >"$WORK/l.out" 2>&1; grep -q 'drill exit=0' "$WORK/l.out"
check_mode drill 'restore-drill.sh --dir /work/out' m-drill
if grep -q 'qc1-measure.sh' "$WORK/stub.log"; then echo 'the drill mode measured' >&2; exit 1; fi
: >"$WORK/stub.log"; launch --out "$WORK/m-qc1" --qc1 -- --sizes 10000 >"$WORK/l.out" 2>&1; grep -q 'qc1 exit=0' "$WORK/l.out"
check_mode qc1 'qc1-measure.sh --dir /work/out' m-qc1
grep -q -- '_ bash deploy/digitalocean/qc1-measure.sh --dir /work/out --sizes 10000$' "$WORK/stub.log"
if grep -q 'restore-drill.sh' "$WORK/stub.log"; then echo 'the qc1 mode ran the restore drill' >&2; exit 1; fi
# No mount, host network or socket on the run line in either mode.
run_line="$(grep '^run ' "$WORK/stub.log")"; [[ "$(grep -c '^run ' "$WORK/stub.log")" == 1 ]]
if grep -qE -- ' -v |--volume|--mount|--network|docker\.sock|--cap-add' <<<"$run_line"; then echo "the runner is given more than it needs: $run_line" >&2; exit 1; fi
# A runtime that cannot be prepared ends EITHER mode before anything runs, with the preparation's exit code and its evidence copied out.
: >"$WORK/stub.log"; rc=0; STUB_PREP_RC=9 launch --out "$WORK/m-drill-fail" >"$WORK/l2.out" 2>&1 || rc=$?
[[ "$rc" == 9 ]] && grep -q 'nothing was run (drill)' "$WORK/l2.out"
if grep -q 'restore-drill.sh' "$WORK/stub.log"; then echo 'the drill ran after a failed browser preparation' >&2; exit 1; fi
grep -q "^cp c\{64\}:/work/prep/. $WORK/m-drill-fail/prep/" "$WORK/stub.log"; grep -qE '^rm -fv c{64}$' "$WORK/stub.log"
: >"$WORK/stub.log"; rc=0; STUB_PREP_RC=9 launch --out "$WORK/m-qc1-fail" --qc1 -- --sizes 10000 >"$WORK/l2.out" 2>&1 || rc=$?
[[ "$rc" == 9 ]] && grep -q 'nothing was run (qc1)' "$WORK/l2.out"
if grep -q 'qc1-measure.sh' "$WORK/stub.log"; then echo 'the measurement ran after a failed browser preparation' >&2; exit 1; fi
# Only a QC-1 run may skip the browser, and only by asking; the same word means nothing to the drill's launcher.
: >"$WORK/stub.log"; launch --out "$WORK/m-qc1-nb" --qc1 -- --no-browser --sizes 10000 >"$WORK/l3.out" 2>&1
if grep -q 'browser-prepare.sh' "$WORK/stub.log"; then echo 'the browser was prepared although --no-browser was given' >&2; exit 1; fi
grep -q 'qc1-measure.sh --dir /work/out' "$WORK/stub.log"
: >"$WORK/stub.log"; launch --out "$WORK/m-drill-nb" -- --no-browser >"$WORK/l3.out" 2>&1
grep -q 'browser-prepare.sh /work/prep$' "$WORK/stub.log"   # the drill prepares regardless; restore-drill.sh itself rejects the unknown option
# The run's own failure comes back as the launcher's exit, in both modes.
: >"$WORK/stub.log"; rc=0; STUB_QC1_RC=4 launch --out "$WORK/m-r1" --qc1 >"$WORK/l4.out" 2>&1 || rc=$?
[[ "$rc" == 4 ]] && grep -q 'qc1 exit=4' "$WORK/l4.out"
: >"$WORK/stub.log"; rc=0; STUB_DRILL_RC=5 launch --out "$WORK/m-r2" >"$WORK/l4.out" 2>&1 || rc=$?
[[ "$rc" == 5 ]] && grep -q 'drill exit=5' "$WORK/l4.out"
# Structure: one preparation call in the script, before the branch that picks the run, not inside either branch of it.
LA="$HERE/restore-drill-nested.sh"
[[ "$(grep -c 'qc1/browser-prepare.sh' <(grep -v '^#' "$LA"))" == 1 ]]
prep_s="$(grep -n 'bash deploy/digitalocean/qc1/browser-prepare.sh /work/prep' "$LA" | cut -d: -f1)"; branch_s="$(grep -n '^if (( QC1 )); then$' "$LA" | tail -n 1 | cut -d: -f1)"
[[ -n "$prep_s" && -n "$branch_s" && "$prep_s" -lt "$branch_s" ]]
grep -q '^wrap=' "$LA"

# --- the runner: browser preflight before anything is created -------------------------------------------------------------
CLONE="$WORK/clone"; mkdir -p "$CLONE/deploy/digitalocean/qc1" "$CLONE/web"
cp "$HERE/qc1-measure.sh" "$HERE/isolation.sh" "$CLONE/deploy/digitalocean/"
cp "$HERE"/qc1/lib.sh "$HERE"/qc1/filler.sql "$HERE"/qc1/scenario.sql "$HERE"/qc1/panel-measure.mjs "$HERE"/qc1/browser.mjs "$HERE"/qc1/browser-launch.cjs "$HERE"/qc1/browser-preflight.mjs "$HERE"/qc1/response-accounting.mjs "$CLONE/deploy/digitalocean/qc1/"
cp "$ROOT/api_profile.go" "$ROOT/main.go" "$CLONE/"
git init -q "$CLONE"; git -C "$CLONE" config user.name fixture; git -C "$CLONE" config user.email fixture@example.invalid
git -C "$CLONE" config commit.gpgsign false; git -C "$CLONE" config core.hooksPath /dev/null
git -C "$CLONE" add -A; git -C "$CLONE" commit -qm clone
CLONE_HEAD="$(git -C "$CLONE" rev-parse HEAD)"
cat >"$WORK/stubbin/docker" <<'STUB'
#!/usr/bin/env bash
printf '%s\n' "$*" >>"$STUB_LOG"
hex() { printf "$1%.0s" {1..64}; echo; }
case "$1" in
  info) echo '{"ID":"s","Name":"x","ContainersRunning":0,"SecurityOptions":[],"CgroupVersion":"2"}' ;;
  image)
    case "$*" in
      *"index .Config.Labels"*) echo "$STUB_LABEL" ;;
      *"json .RepoDigests"*) echo '[]' ;;
      *"{{.Id}}"*) echo "sha256:$(hex c)" ;;
      *"{{.Config.Labels"*) echo ;;
    esac ;;
  network) case "$2" in create) hex n ;; esac ;;
  volume)
    case "$2" in
      create)
        for a in "$@"; do case "$a" in fold.qc1.owner=*) printf '%s' "${a#fold.qc1.owner=}" >"$STUB_DIR/token" ;; esac; done
        printf '%s\n' "${STUB_VOLNAME:-stubvol}" ;;
      inspect)
        case "${STUB_VOLMODE:-own}" in
          own) cat "$STUB_DIR/token"; echo ;;
          none) echo ;;
          other) echo 11111111-2222-3333-4444-555555555555 ;;
        esac ;;
    esac ;;
  run)
    case "$*" in
      *"--rm"*version*) echo '{"recoveryPolicy":2}' ;;
      *"-v "*) exit 1 ;;
      *) hex d ;;
    esac ;;
esac
exit 0
STUB
cat >"$WORK/stubbin/node" <<'STUB'
#!/usr/bin/env bash
printf 'node %s | QC1_WEB_DIR=%s\n' "$*" "${QC1_WEB_DIR-}" >>"$STUB_NODE_LOG"
case "${STUB_NODE_MODE:-ok}" in
  ok) echo '{"ok":true,"chromium":"140.0.7339.0","playwright":"1.64.1"}' ;;
  notok) echo '{"ok":false}' ;;
  fail) echo 'browser-preflight: nope' >&2; exit 3 ;;
esac
STUB
chmod +x "$WORK/stubbin/docker" "$WORK/stubbin/node"
rrun() { STUB_LOG="$WORK/stub.log" STUB_NODE_LOG="$WORK/node.log" STUB_DIR="$WORK" STUB_LABEL="${LABEL:-$CLONE_HEAD}" PATH="$WORK/stubbin:$PATH" bash "$CLONE/deploy/digitalocean/qc1-measure.sh" "$@"; }
: >"$WORK/stub.log"; : >"$WORK/node.log"
rc=0; STUB_NODE_MODE=fail rrun --dir "$WORK/pf1" --image qc1-supplied:latest >"$WORK/pf1.out" 2>&1 || rc=$?
[[ "$rc" == 1 ]] && grep -q 'the browser preflight failed' "$WORK/pf1.out" && [[ ! -e "$WORK/pf1" ]]
[[ "$(wc -l <"$WORK/stub.log")" == 1 && "$(head -n 1 "$WORK/stub.log")" == info* ]]
grep -q "node $CLONE/deploy/digitalocean/qc1/browser-preflight.mjs | QC1_WEB_DIR=$CLONE/web" "$WORK/node.log"
: >"$WORK/stub.log"; rc=0; STUB_NODE_MODE=notok rrun --dir "$WORK/pf2" --image qc1-supplied:latest >"$WORK/pf2.out" 2>&1 || rc=$?
[[ "$rc" == 1 ]] && grep -q 'the browser preflight did not pass' "$WORK/pf2.out" && [[ ! -e "$WORK/pf2" ]]
# Passing the preflight, the run goes on to the image checks (a label that is not HEAD stops it there).
: >"$WORK/stub.log"; rc=0; LABEL=0000000000000000000000000000000000000000 rrun --dir "$WORK/pf3" --image qc1-supplied:latest >"$WORK/pf3.out" 2>&1 || rc=$?
[[ "$rc" == 1 ]] && grep -q 'is labelled revision' "$WORK/pf3.out"
# --no-browser never calls node.
: >"$WORK/node.log"; : >"$WORK/stub.log"; rc=0; LABEL=0000000000000000000000000000000000000000 rrun --dir "$WORK/pf4" --no-browser --image qc1-supplied:latest >"$WORK/pf4.out" 2>&1 || rc=$?
[[ "$rc" == 1 && ! -s "$WORK/node.log" ]]
# The preflight comes before any directory, image work or container.
RUNNER="$HERE/qc1-measure.sh"
pf_line="$(grep -n 'qc1/browser-preflight.mjs' "$RUNNER" | head -n 1 | cut -d: -f1)"; mk_line="$(grep -n '^mkdir -p "\$WORK"$' "$RUNNER" | cut -d: -f1)"; img_line="$(grep -n '^IMAGE_ID=' "$RUNNER" | head -n 1 | cut -d: -f1)"
[[ "$pf_line" -lt "$mk_line" && "$mk_line" -lt "$img_line" ]]

# --- the runner's volume: unnamed, labelled with a random owner token, verified before it is mounted and before it is removed ----
vol_run() { : >"$WORK/stub.log"; rc=0; rrun --dir "$1" --no-browser --image qc1-supplied:latest "${@:2}" >"$WORK/vol.out" 2>&1 || rc=$?; }
vol_run "$WORK/v1"
# Our volume: created without a name, with a uuid label; inspected before the container that mounts it; removed at the end by its name.
create_line="$(grep -n '^volume create' "$WORK/stub.log" | head -n 1 | cut -d: -f1)"
[[ "$(grep -c '^volume create' "$WORK/stub.log")" == 1 ]]
grep -qE '^volume create --label fold\.qc1\.owner=[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$' "$WORK/stub.log"
inspect_line="$(grep -n '^volume inspect --format {{index .Labels "fold.qc1.owner"}} stubvol$' "$WORK/stub.log" | head -n 1 | cut -d: -f1)"
mount_line="$(grep -n '^run -d .* -v stubvol:/var/lib/postgresql/data' "$WORK/stub.log" | head -n 1 | cut -d: -f1)"
[[ -n "$create_line" && -n "$inspect_line" && -n "$mount_line" && "$create_line" -lt "$inspect_line" && "$inspect_line" -lt "$mount_line" ]]
grep -qx 'volume rm -f stubvol' "$WORK/stub.log"; grep -qE '^network rm n{64}$' "$WORK/stub.log"
token1="$(cat "$WORK/token")"; [[ "$token1" =~ ^[0-9a-f-]{36}$ ]]
vol_run "$WORK/v1b"; [[ "$(cat "$WORK/token")" != "$token1" ]]   # a new token every run
# A volume that was there before (the create returned an existing one): unlabelled, or owned by another run. Refused before it is mounted, never removed.
for mode in none other; do
  STUB_VOLMODE=$mode vol_run "$WORK/v2-$mode"
  [[ "$rc" == 1 ]] && grep -q 'volume ownership check failed' "$WORK/vol.out"
  if grep -qE '^run -d |^volume rm' "$WORK/stub.log"; then echo "a volume that is not ours was mounted or removed ($mode)" >&2; cat "$WORK/stub.log" >&2; exit 1; fi
  grep -qE '^network rm n{64}$' "$WORK/stub.log"
done
# A name collision is the same case: the daemon returns the same existing name that another run labelled.
STUB_VOLNAME=the-hosts-pgdata STUB_VOLMODE=other vol_run "$WORK/v3"
[[ "$rc" == 1 ]]; if grep -qE '^run -d |^volume rm' "$WORK/stub.log"; then echo 'a colliding volume was used or removed' >&2; exit 1; fi
# --keep leaves the stack, including the volume.
vol_run "$WORK/v4" --keep; if grep -q '^volume rm\|^network rm' "$WORK/stub.log"; then echo '--keep removed something' >&2; exit 1; fi
# Cleanup is guarded by the label as well as mounting being: no unconditional volume removal.
sed -n '/^cleanup() {/,/^}/p' "$RUNNER" | grep -q 'docker volume inspect --format .{{index .Labels "fold.qc1.owner"}}. "\$VOL"'
if grep -n 'docker volume rm' "$RUNNER" | grep -v 'fold.qc1.owner' | grep -v '^[0-9]*:      docker volume rm -f "\$VOL"'; then echo 'an unguarded volume removal' >&2; exit 1; fi
if grep -nE 'docker volume create [^-]|docker volume create "|--name qc1' "$RUNNER"; then echo 'the volume is named' >&2; exit 1; fi
grep -q '^VOL_TOKEN="\$(cat /proc/sys/kernel/random/uuid)"$' "$RUNNER"
echo 'qc1 runtime fixtures: ok'
