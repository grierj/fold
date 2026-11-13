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
qc1_browser_lock_problems "$WORK/pkg.latest.json" "$ROOT/web/package-lock.json" | grep -F 'not a pinned or caret version' >/dev/null
jq 'del(.devDependencies["@playwright/test"])' "$ROOT/web/package.json" >"$WORK/pkg.none.json"
qc1_browser_lock_problems "$WORK/pkg.none.json" "$ROOT/web/package-lock.json" | grep -F 'is not a dependency in package.json' >/dev/null
qc1_browser_lock_problems "$ROOT/web/package.json" "$WORK/absent-lock.json" | grep -F 'needs a lock file' >/dev/null
qc1_browser_lock_problems "$WORK/absent-package.json" "$ROOT/web/package-lock.json" | grep -F 'is missing' >/dev/null
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
  for m in browser.mjs browser-preflight.mjs response-accounting.mjs panel-measure.mjs panel-core.mjs panel-driver.mjs page-observer.mjs set-v1.mjs; do node --check "$HERE/qc1/$m"; done
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

  if grep -n -i 'content-length' "$HERE/qc1/panel-measure.mjs" "$HERE/qc1/panel-driver.mjs" "$HERE/qc1/panel-core.mjs" "$HERE/qc1/page-observer.mjs"; then echo 'the panel measurement reads Content-Length' >&2; exit 1; fi
  # The decoded body size is the observer's: read from the body, null (absent) when the body cannot be read, never zero.
  grep -qF 'const body = await r.body();' "$HERE/qc1/page-observer.mjs"; grep -qF 'rec.bytes = body.length;' "$HERE/qc1/page-observer.mjs"; grep -qF 'bytes: null, runes: null, requestMs: null, nodeParseMs: null' "$HERE/qc1/page-observer.mjs"
  # --- the preflight against a fake Playwright: module resolution is real, the browser is not ------------------------------
  # Two fake packages, as the repository has them: @playwright/test (the measurement) and playwright (the restore proof), over one playwright-core.
  fakepkg() { # dir name version
    mkdir -p "$1/node_modules/$2"
    printf '{"name":"%s","version":"%s","main":"index.js"}\n' "$2" "$3" >"$1/node_modules/$2/package.json"
    cat >"$1/node_modules/$2/index.js" <<'JS'
const fs = require("node:fs");
const via = __VIA__;
module.exports = { chromium: { name: () => "chromium", launch: async function (opts) {
  const eng = this.name();
  if (process.env.FAKE_OPTS_OUT) fs.writeFileSync(process.env.FAKE_OPTS_OUT + "." + via.replace(/\W/g, "_") + (eng === "firefox" ? ".firefox" : ""), JSON.stringify(opts));
  if (eng === "firefox" && process.env.FAKE_FF_LAUNCH_FAIL) throw new Error("browserType.launch: Failed to launch: Error: spawn firefox ENOENT\nmore");
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
  return { version: () => eng === "firefox" ? (process.env.FAKE_FF_VERSION ?? "157.0") : (process.env.FAKE_VERSION ?? "148.0.7778.5"), close: async () => {}, newContext: async () => context };
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
    printf '{"browsers":[{"name":"chromium","browserVersion":"148.0.7778.5"},{"name":"firefox","browserVersion":"157.0"}]}\n' >"$d/node_modules/playwright-core/browsers.json"
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
  [[ "$(code_of rbenv RESTORE_BROWSER=firefox RESTORE_BROWSER_LAUNCH="$HERE/qc1/browser-launch.cjs" QC1_CHROMIUM=/usr/bin/chromium node "$RB")" == 1 ]]; [[ ! -e "$WORK/rb.json.playwright" ]]; [[ "$(cat "$WORK/rb.json.playwright.firefox")" == '{"headless":true}' ]]
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
sed -n "${pf_l},$((pf_l+3))p" "$RDS" | grep "jq -e '.ok == true'" >/dev/null
sed -n "$((pf_l - 1)),$((pf_l + 1))p" "$RDS" | grep 'the browser preflight failed\|BROWSER_PREFLIGHT=' >/dev/null
grep -q "printf '%s\\\\n' \"\$BROWSER_PREFLIGHT\" >\"\$WORK/browser-preflight.json\"" "$RDS"
# No flag skips it: there is no --no-browser in the drill.
if sed -n '/^while (( \$# )); do/,/^done/p' "$RDS" | grep -- '--no-browser' >/dev/null; then echo 'restore-drill.sh can skip the browser preflight' >&2; exit 1; fi

# --- the nested launcher, both modes, against a stub docker -------------------------------------------------------------------------
mkdir "$WORK/stubbin"
cat >"$WORK/stubbin/docker" <<'STUB'
#!/usr/bin/env bash
line=; sep=; for a in "$@"; do line+="$sep${a//$'\n'/\\n}"; sep=" "; done; printf '%s\n' "$line" >>"$STUB_LOG"   # one line per call; a newline inside an argument is written as \n
[[ $1 != run ]] || : >"$STUB_LOG.argv"; { sep=; for a in "$@"; do printf '%s%q' "$sep" "$a"; sep=" "; done; printf '\n'; } >>"$STUB_LOG.argv"   # the same call with every argument %q-quoted, so that where one ends and the next begins survives (a launch starts with `run`, which starts the file anew)
case "$1" in
  info) printf '{"Name":"x","ContainersRunning":0}\n' ;;
  run) printf 'c%.0s' {1..64}; echo ;;
  inspect) case "$*" in *NetworkMode*) echo bridge ;; *Mounts*) echo '/var/lib/docker /v' ;; esac ;;
  exec) case "$*" in *fold-owned-scan*) echo 0 ;; *fold-listener-scan*) echo "checked ipv4 ipv6" ;; *browser-prepare.sh*) exit "${STUB_PREP_RC:-0}" ;; *qc1-measure.sh*) exit "${STUB_QC1_RC:-0}" ;; *restore-drill.sh*) exit "${STUB_DRILL_RC:-0}" ;; esac ;;
esac
exit 0
STUB
chmod +x "$WORK/stubbin/docker"
LAUNCH="$WORK/launcher"; mkdir -p "$LAUNCH/deploy/digitalocean"; cp "$HERE/restore-drill-nested.sh" "$LAUNCH/deploy/digitalocean/"
git init -q "$LAUNCH"; git -C "$LAUNCH" config user.name fixture; git -C "$LAUNCH" config user.email fixture@example.invalid
git -C "$LAUNCH" config commit.gpgsign false; git -C "$LAUNCH" config core.hooksPath /dev/null
git -C "$LAUNCH" add -A; git -C "$LAUNCH" commit -qm launcher
launch() { STUB_LOG="$WORK/stub.log" PATH="$WORK/stubbin:$PATH" bash "$LAUNCH/deploy/digitalocean/restore-drill-nested.sh" "$@"; }
# WRAP, the supervisor every docker exec runs the workload under, as the runner defines it (WRAP_ARG). The stub's argv file (stub.log.argv) keeps every argument apart, so the check is
# that WRAP is exactly one argument after bash -c and the workload's words follow it one by one. The supervisor's contract, read from the script itself: it loads the
# Chromium the preparation found, polls for the stop request and TERMs the workload's group; it does not exec the command in its place.
WRAP_ARG="$(sed -n "/^WRAP=\$(cat <<'WRAP'\$/,/^WRAP\$/p" "$HERE/restore-drill-nested.sh" | sed '1d;$d')"
[[ -n "$WRAP_ARG" ]]
grep -qF 'prep/browser.env' <<<"$WRAP_ARG"; grep -qF 'stop-request' <<<"$WRAP_ARG"; grep -qF 'kill -TERM -- "-$g"' <<<"$WRAP_ARG"
if grep -qF 'exec "$@"' <<<"$WRAP_ARG"; then echo 'the supervisor execs the command in its place again' >&2; exit 1; fi
# wrapped_call LOG WORDS...: LOG (the stub's argv file: one call per line, each argument %q-quoted, so no two arguments can read as one) holds a docker exec whose arguments END with
# bash, -c, the WRAP text as exactly ONE argument, _, bash and then each of WORDS as an argument of its own. The negative case below shows that WRAP and the workload's words
# joined into one argument are refused.
WRAP_Q="$(printf '%q' "$WRAP_ARG")"
wrapped_call() { local log=$1 want="bash -c $WRAP_Q _ bash" a l; shift; for a in "$@"; do want+=" $(printf '%q' "$a")"; done; while IFS= read -r l; do [[ "$l" == "exec "*" $want" ]] && return 0; done <"$log"; return 1; }
printf 'exec -w /work/fold c bash -c %q\n' "$WRAP_ARG _ bash x y" >"$WORK/joined.argv"
if wrapped_call "$WORK/joined.argv" x y; then echo 'wrapped_call accepts WRAP and the workload joined into one argument' >&2; exit 1; fi
printf 'exec -w /work/fold c bash -c %q _ bash x y\n' "$WRAP_ARG" >"$WORK/exact.argv"
wrapped_call "$WORK/exact.argv" x y
# In EACH mode the browser is prepared once, after the clone and before the run, in the same container, and the run receives the Chromium the preparation found.
check_mode() { # mode-name run-script label prepare-env-words run-words
  local clone_l prep_l run_l run_line
  clone_l="$(grep -n 'git clone' "$WORK/stub.log" | head -n 1 | cut -d: -f1)"
  prep_l="$(grep -n 'browser-prepare.sh /work/prep$' "$WORK/stub.log" | head -n 1 | cut -d: -f1)"
  run_l="$(grep -n -- "$2" "$WORK/stub.log" | head -n 1 | cut -d: -f1)"
  [[ -n "$clone_l" && -n "$prep_l" && -n "$run_l" && "$clone_l" -lt "$prep_l" && "$prep_l" -lt "$run_l" ]] || { echo "$1: the browser is not prepared between the clone and the run" >&2; cat "$WORK/stub.log" >&2; exit 1; }
  [[ "$(grep -c 'browser-prepare.sh' "$WORK/stub.log")" == 1 ]] || { echo "$1: the browser is prepared more or less than once" >&2; exit 1; }
  grep -q "^exec -e FOLD_NESTED_RUN=[0-9a-f]\{12\} $4-w /work/fold c\{64\} bash -c .* _ bash deploy/digitalocean/qc1/browser-prepare.sh /work/prep\$" "$WORK/stub.log" || { echo "$1: the preparation does not run with the environment $4" >&2; cat "$WORK/stub.log" >&2; exit 1; }
  run_line="$(sed -n "${run_l}p" "$WORK/stub.log")"
  grep -q -- '-e FOLD_DRILL_ISOLATED_DAEMON=1 -e QC1_NO_SANDBOX=1 -e QC1_WEB_DIR=/work/fold/web -w /work/fold' <<<"$run_line" || { echo "$1: the run does not get the runner switches" >&2; exit 1; }
  read -ra run_w <<<"$5"   # the run's words, each its own argument after the supervisor's exact one
  wrapped_call "$WORK/stub.log.argv" deploy/digitalocean/qc1/browser-prepare.sh /work/prep || { echo "$1: the preparation is not under the exact supervisor with its own arguments" >&2; exit 1; }
  wrapped_call "$WORK/stub.log.argv" "${run_w[@]}" || { echo "$1: the run is not under the exact supervisor with its own arguments" >&2; exit 1; }
  grep -q "^cp c\{64\}:/work/prep/. $WORK/$3/prep/" "$WORK/stub.log"; grep -q "^cp c\{64\}:/work/out/. $WORK/$3/" "$WORK/stub.log"; grep -qE '^rm -fv c{64}$' "$WORK/stub.log"
}
: >"$WORK/stub.log"; launch --out "$WORK/m-drill" -- --keep >"$WORK/l.out" 2>&1; grep -q 'drill exit=0' "$WORK/l.out"
check_mode drill 'restore-drill.sh --dir /work/out' m-drill '-e QC1_WEB_DIR=/work/fold/web -e QC1_PREPARE_FIREFOX=1 ' 'deploy/digitalocean/restore-drill.sh --dir /work/out --keep'
if grep -q 'qc1-measure.sh' "$WORK/stub.log"; then echo 'the drill mode measured' >&2; exit 1; fi
: >"$WORK/stub.log"; launch --out "$WORK/m-qc1" --qc1 -- --sizes 10000 >"$WORK/l.out" 2>&1; grep -q 'qc1 exit=0' "$WORK/l.out"
check_mode qc1 'qc1-measure.sh --dir /work/out' m-qc1 '-e QC1_WEB_DIR=/work/fold/web ' 'deploy/digitalocean/qc1-measure.sh --dir /work/out --sizes 10000'
if grep -q 'QC1_PREPARE_FIREFOX' "$WORK/stub.log"; then echo 'the qc1 mode builds the Firefox runtime' >&2; exit 1; fi
grep -q -- '_ bash deploy/digitalocean/qc1-measure.sh --dir /work/out --sizes 10000$' "$WORK/stub.log"
if grep -q 'restore-drill.sh' "$WORK/stub.log"; then echo 'the qc1 mode ran the restore drill' >&2; exit 1; fi
# No mount, host network or socket on the run line in either mode.
run_line="$(grep '^run ' "$WORK/stub.log")"; [[ "$(grep -c '^run ' "$WORK/stub.log")" == 1 ]]
if grep -qE -- ' -v |--volume|--mount|--network|--cap-add' <<<"$run_line"; then echo "the runner is given more than it needs: $run_line" >&2; exit 1; fi
grep -qF -- 'dockerd --host=unix:///var/run/docker.sock' <<<"$run_line"
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
grep -q '^WRAP=' "$LA"

# --- Firefox: a glibc binary cannot start on the Alpine runner, so the drill runs it in a Debian container built and launched BEFORE the drill ------------
# (drill3: `playwright install firefox` downloaded a glibc Firefox into the musl runner; spawn failed with ENOENT, the missing /lib64/ld-linux-x86-64.so.2.)
FRT="$HERE/qc1/firefox-runtime.sh"; FDF="$HERE/qc1/firefox.Dockerfile"
# shellcheck source=deploy/digitalocean/qc1/firefox-runtime.sh
source "$FRT"
printf '24\n' >"$WORK/nvm"; [[ "$(firefox_node_image "$WORK/nvm")" == node:24-bookworm ]]
printf 'v22\n' >"$WORK/nvm"; [[ "$(firefox_node_image "$WORK/nvm")" == node:22-bookworm ]]
printf 'lts/*\n' >"$WORK/nvm"; if firefox_node_image "$WORK/nvm" >/dev/null; then echo 'a non-numeric .nvmrc gave a node image' >&2; exit 1; fi
[[ "$(firefox_node_image "$ROOT/.nvmrc")" == "node:$(tr -d '[:space:]v' <"$ROOT/.nvmrc")-bookworm" ]]
t1="$(firefox_image_tag "$ROOT/web/package-lock.json" "$FDF" node:24-bookworm)"
[[ "$t1" =~ ^fold-firefox-runner:[0-9a-f]{12}$ ]]
[[ "$(firefox_image_tag "$ROOT/web/package-lock.json" "$FDF" node:24-bookworm)" == "$t1" ]]
[[ "$(firefox_image_tag "$ROOT/web/package-lock.json" "$FDF" node:22-bookworm)" != "$t1" ]]
jq -c . "$ROOT/web/package-lock.json" >"$WORK/lock.other"; [[ "$(firefox_image_tag "$WORK/lock.other" "$FDF" node:24-bookworm)" != "$t1" ]]
{ cat "$FDF"; echo '# changed'; } >"$WORK/df.other"; [[ "$(firefox_image_tag "$ROOT/web/package-lock.json" "$WORK/df.other" node:24-bookworm)" != "$t1" ]]
# the run flags: the runner's own network, an init, shared memory; no mount, no privilege, no socket
[[ "$(firefox_run_flags | tr '\n' ' ')" == '--rm --init --ipc=host --network host ' ]]
# The Dockerfile: base from the build arg, nothing from the repository but the two package files, installed as locked, Firefox with its system libraries.
grep -q '^ARG NODE_IMAGE$' "$FDF"; grep -q '^FROM ${NODE_IMAGE}$' "$FDF"
[[ "$(grep -n '^ARG NODE_IMAGE$' "$FDF" | cut -d: -f1)" -lt "$(grep -n '^FROM ' "$FDF" | cut -d: -f1)" ]]
grep -q '^COPY package.json package-lock.json ./$' "$FDF"; [[ "$(grep -c '^COPY ' "$FDF")" == 1 ]]
grep -q 'npm ci --ignore-scripts --omit=optional' "$FDF"; grep -q 'install --with-deps firefox' "$FDF"; grep -q '^ENV PLAYWRIGHT_BROWSERS_PATH=' "$FDF"
if grep -nE '^ADD |COPY \. |curl|wget|npm install|PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD|--force' "$FDF"; then echo 'firefox.Dockerfile installs without the lock or pulls from outside' >&2; exit 1; fi
# Nothing tries the musl-bound download any more.
if grep -nE 'cli\.js install firefox|playwright install firefox' "$HERE/restore-drill-cases.sh" "$HERE/restore-drill.sh"; then echo 'the drill still installs a glibc Firefox on the runner' >&2; exit 1; fi

# restore-drill.sh refuses a runner whose Firefox preparation was skipped, before a stack exists, and keeps the record.
ffchk_l="$(grep -n 'FIREFOX_RUNTIME="${FOLD_FIREFOX_RUNTIME:-}"' "$RDS" | head -n 1 | cut -d: -f1)"; ffcp_l="$(grep -n 'cp "$FIREFOX_RUNTIME" "$WORK/firefox-runtime.json"' "$RDS" | cut -d: -f1)"
boot2_l="$(grep -n '^drill_case_begin bootstrap$' "$RDS" | cut -d: -f1)"
[[ -n "$ffchk_l" && -n "$ffcp_l" && "$ffchk_l" -lt "$boot2_l" && "$boot2_l" -lt "$ffcp_l" ]]
sed -n "${ffchk_l},$((ffchk_l+8))p" "$RDS" | grep 'was not prepared' >/dev/null
sed -n "${ffchk_l},$((ffchk_l+8))p" "$RDS" | grep 'docker image inspect' >/dev/null
sed -n "${ffchk_l},$((ffchk_l+8))p" "$RDS" | grep 'is not the one the preparation proved' >/dev/null

# firefox_proof (the drill's Firefox step), taken out of restore-drill-cases.sh and run against a stub docker.
awk '/^firefox_proof\(\) /{on=1} on{print} on && /^}$/{exit}' "$HERE/restore-drill-cases.sh" >"$WORK/firefox_proof.sh"
[[ -s "$WORK/firefox_proof.sh" ]]
mkdir -p "$WORK/fp/bin" "$WORK/fp/work"
cat >"$WORK/fp/bin/docker" <<'STUB'
#!/usr/bin/env bash
printf '%s\n' "$*" >>"$FP_LOG"
exit "${FP_RC:-0}"
STUB
chmod +x "$WORK/fp/bin/docker"
cat >"$WORK/fp/run.sh" <<'RUN'
set -Eeuo pipefail
source "$FPW/firefox_proof.sh"
firefox_proof 42
RUN
fp() { env -u FOLD_FIREFOX_IMAGE FPW="$WORK" ROOT="$ROOT" WORK="$WORK/fp/work" PATH="$WORK/fp/bin:$PATH" FP_LOG="$WORK/fp/log" "$@" bash "$WORK/fp/run.sh"; }
# (a) no image: an explicit hand check with the recorded reason, the drill goes on
: >"$WORK/fp/log"; : >"$WORK/fp/work/hand-checks.txt"
printf '{"available":false,"stage":"build","reason":"network unreachable: cdn.playwright.dev"}\n' >"$WORK/fp/work/firefox-runtime.json"
fp
grep -q 'R4-6c-V hand check: Firefox' "$WORK/fp/work/hand-checks.txt"; grep -q 'network unreachable: cdn.playwright.dev' "$WORK/fp/work/hand-checks.txt"
grep -q 'could not be installed in the drill runner' "$WORK/fp/work/hand-checks.txt"; [[ ! -s "$WORK/fp/log" ]]
# (b) an image: the proof runs in it with the shared flags, the scratch directory and nothing else mounted; its failure fails the drill
: >"$WORK/fp/log"; : >"$WORK/fp/work/hand-checks.txt"
fp FOLD_FIREFOX_IMAGE=fold-firefox-runner:abc123abc123 FP_RC=0
[[ "$(wc -l <"$WORK/fp/log")" == 1 ]]
line="$(cat "$WORK/fp/log")"
grep -qF -- "run --rm --init --ipc=host --network host -v $WORK/fp/work:$WORK/fp/work -w $WORK/fp/work " <<<"$line"
for want in 'RESTORE_BROWSER=firefox' 'RESTORE_WEB_PACKAGE=/app/package.json' "RESTORE_BROWSER_ARTIFACTS=$WORK/fp/work" 'RESTORE_TARGET_ID=42' "RESTORE_SCREENSHOT=$WORK/fp/work/firefox-delete-390.png" "RESTORE_BROWSER_PROOF=$WORK/fp/work/firefox-cookie-proof.json"; do
  grep -qF -- "-e $want" <<<"$line" || { echo "firefox_proof does not pass $want: $line" >&2; exit 1; }
done
grep -qF -- "fold-firefox-runner:abc123abc123 node $WORK/fp/work/private-browser.cjs" <<<"$line"
if grep -qE -- '--privileged|docker\.sock|--volume|-v /|-v /etc|-v /opt|--pid|--cap-add' <<<"$line" && ! grep -qF -- "-v $WORK/fp/work:$WORK/fp/work" <<<"$line"; then echo "firefox_proof mounts more than the scratch directory: $line" >&2; exit 1; fi
[[ "$(grep -o -- ' -v ' <<<"$line" | wc -l)" == 1 ]]
[[ ! -s "$WORK/fp/work/hand-checks.txt" ]]
rc=0; fp FOLD_FIREFOX_IMAGE=fold-firefox-runner:abc123abc123 FP_RC=3 || rc=$?
[[ "$rc" != 0 ]]; [[ ! -s "$WORK/fp/work/hand-checks.txt" ]]   # a Firefox that starts in the preflight and fails the proof is a failure, never a hand check
# the step is called once, with the target account, in lifecycle_cases
[[ "$(grep -c '^  firefox_proof "\$A"$' "$HERE/restore-drill-cases.sh")" == 1 ]]

if command -v node >/dev/null; then
  # The preflight's third consumer, against the fake Playwright.
  PF="$HERE/qc1/browser-preflight.mjs"
  rm -f "$WORK"/opts.json.*
  rc="$(QC1_WEB_DIR="$WORK/web" QC1_PREFLIGHT_FOR=firefox FAKE_OPTS_OUT="$WORK/opts.json" code_of node "$PF")"
  [[ "$rc" == 0 ]]
  jq -e '.ok == true and .consumers == ["firefox"] and .firefox == "157.0" and .bundledFirefox == "157.0" and .smoke.firefox.via == "playwright" and .smoke.firefox.domItems == 2 and .smoke.firefox.secureLocalhostCookie == true and .smoke.firefox.majorDelta == 0 and .chromium == null' "$WORK/out" >/dev/null
  [[ "$(cat "$WORK/opts.json.playwright.firefox")" == '{"headless":true}' ]]; [[ ! -e "$WORK/opts.json.playwright" && ! -e "$WORK/opts.json._playwright_test" ]]
  [[ "$(QC1_WEB_DIR="$WORK/web" QC1_PREFLIGHT_FOR=firefox FAKE_FF_LAUNCH_FAIL=1 code_of node "$PF")" == 3 ]]; grep -q 'Firefox did not launch for firefox' "$WORK/err"
  [[ "$(QC1_WEB_DIR="$WORK/web" QC1_PREFLIGHT_FOR=firefox FAKE_FF_VERSION=150.0 code_of node "$PF")" == 7 ]]; grep -q 'is not the 157.0 this Playwright bundles' "$WORK/err"
  [[ "$(QC1_WEB_DIR="$WORK/web" QC1_PREFLIGHT_FOR=firefox FAKE_NOCOOKIE=1 code_of node "$PF")" == 6 ]]; grep -q 'Secure cookie on localhost' "$WORK/err"
  [[ "$(QC1_WEB_DIR="$WORK/web" QC1_PREFLIGHT_FOR=firefox FAKE_ITEMS=1 code_of node "$PF")" == 6 ]]
  # the default consumers still do not launch Firefox (the Alpine runner could not)
  rm -f "$WORK"/opts.json.*; [[ "$(QC1_WEB_DIR="$WORK/web" FAKE_OPTS_OUT="$WORK/opts.json" code_of node "$PF")" == 0 ]]; [[ ! -e "$WORK/opts.json.playwright.firefox" ]]

  # --- browser-prepare.sh end to end with stub apk/npm/docker/chromium and a fake Playwright (the real script, the real preflight) ----------------
  PC="$WORK/prepclone"; mkdir -p "$PC/web" "$PC/deploy/digitalocean/qc1"
  cp "$ROOT/web/package.json" "$ROOT/web/package-lock.json" "$PC/web/"; cp "$ROOT/.nvmrc" "$PC/"; printf 'web/node_modules\n' >"$PC/.gitignore"
  cp "$HERE"/qc1/{browser-prepare.sh,browser-runtime.sh,browser-preflight.mjs,browser.mjs,browser-launch.cjs,firefox-runtime.sh,firefox.Dockerfile} "$PC/deploy/digitalocean/qc1/"
  git init -q "$PC"; git -C "$PC" config user.name fixture; git -C "$PC" config user.email fixture@example.invalid; git -C "$PC" config commit.gpgsign false; git -C "$PC" config core.hooksPath /dev/null
  git -C "$PC" add -A; git -C "$PC" commit -qm prepare
  mkweb "$WORK/pcweb" 1.64.1 1.64.1 '>=18'; cp -r "$WORK/pcweb/node_modules" "$PC/web/node_modules"
  mkdir "$WORK/pbin"
  printf '#!/bin/sh\n[ "$1" = info ] && echo chromium-1.0-r0\nexit 0\n' >"$WORK/pbin/apk"
  printf '#!/bin/sh\nexit 0\n' >"$WORK/pbin/chromium"
  printf '#!/bin/sh\n[ "$1" = -v ] && echo 10.0.0\nexit 0\n' >"$WORK/pbin/npm"
  cat >"$WORK/pbin/docker" <<'STUB'
#!/usr/bin/env bash
printf '%s\n' "$*" >>"$STUB_LOG"
case "$1" in
  build) echo "step 1/5"; echo "${STUB_BUILD_MSG:-built}"; exit "${STUB_BUILD_RC:-0}" ;;
  run) exec env QC1_WEB_DIR="$STUB_WEB_DIR" QC1_PREFLIGHT_FOR=firefox node "$STUB_PREFLIGHT" ;;
  image) echo sha256:feedface; exit 0 ;;
esac
exit 0
STUB
  chmod +x "$WORK/pbin"/*
  prep() { # out-dir [env...]
    local out="$1"; shift
    : >"$WORK/pstub.log"
    env PATH="$WORK/pbin:$PATH" STUB_LOG="$WORK/pstub.log" STUB_WEB_DIR="$PC/web" STUB_PREFLIGHT="$PC/deploy/digitalocean/qc1/browser-preflight.mjs" QC1_WEB_DIR="$PC/web" "$@" \
      bash "$PC/deploy/digitalocean/qc1/browser-prepare.sh" "$out"
  }
  # not requested (the QC-1 mode): no Docker call, Chromium only
  rc=0; prep "$WORK/po1" >"$WORK/p1.out" 2>"$WORK/p1.err" || rc=$?
  [[ "$rc" == 0 && ! -s "$WORK/pstub.log" ]] || { cat "$WORK/p1.err" >&2; exit 1; }
  [[ "$(cat "$WORK/po1/browser.env")" == 'QC1_CHROMIUM='* && "$(wc -l <"$WORK/po1/browser.env")" == 1 ]]; jq -e '.firefox == null and .preflight.ok == true' "$WORK/po1/browser-runtime.json" >/dev/null
  grep -q 'firefox: not requested' "$WORK/p1.out"; [[ ! -e "$WORK/po1/firefox-runtime.json" ]]
  # requested and built: the image is built, then launched through the shared flags, and recorded
  rc=0; prep "$WORK/po2" QC1_PREPARE_FIREFOX=1 >"$WORK/p2.out" 2>"$WORK/p2.err" || rc=$?
  [[ "$rc" == 0 ]] || { cat "$WORK/p2.err" >&2; exit 1; }
  b_l="$(grep -n '^build ' "$WORK/pstub.log" | head -n 1 | cut -d: -f1)"; r_l="$(grep -n '^run ' "$WORK/pstub.log" | head -n 1 | cut -d: -f1)"; i_l="$(grep -n '^image inspect' "$WORK/pstub.log" | head -n 1 | cut -d: -f1)"
  [[ -n "$b_l" && -n "$r_l" && -n "$i_l" && "$b_l" -lt "$r_l" && "$r_l" -lt "$i_l" ]]
  grep -qE "^build --build-arg NODE_IMAGE=node:$(tr -d '[:space:]v' <"$ROOT/.nvmrc")-bookworm -f $PC/deploy/digitalocean/qc1/firefox.Dockerfile -t fold-firefox-runner:[0-9a-f]{12} " "$WORK/pstub.log"
  grep -qE "^run --rm --init --ipc=host --network host -e QC1_WEB_DIR=/app -e QC1_PREFLIGHT_FOR=firefox -v $PC/deploy/digitalocean/qc1:/qc1:ro fold-firefox-runner:[0-9a-f]{12} node /qc1/browser-preflight.mjs\$" "$WORK/pstub.log"
  jq -e '.available == true and (.image | test("^fold-firefox-runner:[0-9a-f]{12}$")) and .imageId == "sha256:feedface" and .nodeImage == "node:24-bookworm" and .preflight.ok == true and .preflight.consumers == ["firefox"] and .preflight.firefox == "157.0"' "$WORK/po2/firefox-runtime.json" >/dev/null
  grep -q "^FOLD_FIREFOX_IMAGE=$(jq -r .image "$WORK/po2/firefox-runtime.json")\$" "$WORK/po2/browser.env"; grep -q "^FOLD_FIREFOX_RUNTIME=$WORK/po2/firefox-runtime.json\$" "$WORK/po2/browser.env"; grep -q '^QC1_CHROMIUM=' "$WORK/po2/browser.env"
  jq -e '.firefox.available == true' "$WORK/po2/browser-runtime.json" >/dev/null; grep -q 'firefox: ready in fold-firefox-runner:' "$WORK/p2.out"
  # the image cannot be built (network, registry, apt): an explicit record with the reason, no image variable, no run, preparation still succeeds
  rc=0; prep "$WORK/po3" QC1_PREPARE_FIREFOX=1 STUB_BUILD_RC=1 STUB_BUILD_MSG='E: Failed to fetch http://deb.debian.org (network unreachable)' >"$WORK/p3.out" 2>"$WORK/p3.err" || rc=$?
  [[ "$rc" == 0 ]] || { cat "$WORK/p3.err" >&2; exit 1; }
  jq -e '.available == false and .stage == "build" and (.reason | contains("network unreachable")) and (.handCheck | length > 0)' "$WORK/po3/firefox-runtime.json" >/dev/null
  grep -q 'WARNING: the Firefox runtime could not be built' "$WORK/p3.err"; grep -q 'network unreachable' "$WORK/p3.err"
  if grep -q '^run ' "$WORK/pstub.log"; then echo 'Docker ran after a failed build' >&2; exit 1; fi
  if grep -q '^FOLD_FIREFOX_IMAGE=' "$WORK/po3/browser.env"; then echo 'an unbuilt image was handed on' >&2; exit 1; fi; grep -q "^FOLD_FIREFOX_RUNTIME=$WORK/po3/firefox-runtime.json\$" "$WORK/po3/browser.env"
  grep -q 'firefox: unavailable, hand check' "$WORK/p3.out"; [[ -s "$WORK/po3/firefox-build.log" ]]
  # the image builds but Firefox does not start in it: the preparation FAILS before any drill (no browser.env for the launcher to hand on)
  rc=0; prep "$WORK/po4" QC1_PREPARE_FIREFOX=1 FAKE_FF_LAUNCH_FAIL=1 >"$WORK/p4.out" 2>"$WORK/p4.err" || rc=$?
  [[ "$rc" == 1 ]]; grep -q 'did not pass its preflight' "$WORK/p4.err"; grep -q 'Firefox did not launch for firefox' "$WORK/p4.err"; [[ ! -e "$WORK/po4/browser.env" ]]
  # ...and it fails the same way for a Firefox of the wrong version
  rc=0; prep "$WORK/po5" QC1_PREPARE_FIREFOX=1 FAKE_FF_VERSION=150.0 >"$WORK/p5.out" 2>"$WORK/p5.err" || rc=$?
  [[ "$rc" == 1 && ! -e "$WORK/po5/browser.env" ]]
fi

# --- the runner: browser preflight before anything is created -------------------------------------------------------------
CLONE="$WORK/clone"; mkdir -p "$CLONE/deploy/digitalocean/qc1" "$CLONE/web/src/lib"
cp "$HERE/qc1-measure.sh" "$HERE/isolation.sh" "$CLONE/deploy/digitalocean/"
cp "$HERE"/qc1/app-facts.json "$HERE"/qc1/lib.sh "$HERE"/qc1/filler.sql "$HERE"/qc1/scenario.sql "$HERE"/qc1/panel-measure.mjs "$HERE"/qc1/browser.mjs "$HERE"/qc1/browser-launch.cjs "$HERE"/qc1/browser-preflight.mjs "$HERE"/qc1/response-accounting.mjs "$HERE"/qc1/set-v1.mjs "$HERE"/qc1/panel-core.mjs "$HERE"/qc1/panel-driver.mjs "$HERE"/qc1/page-observer.mjs "$HERE"/qc1/top-statements.sql "$CLONE/deploy/digitalocean/qc1/"; mkdir -p "$CLONE/web/src/lib"; cp "$ROOT/web/src/lib/reasonSetVectors.json" "$CLONE/web/src/lib/"
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
# The body builder's self-test against the shared vectors runs before the preflight; the stub answers it as the real one does.
case "$*" in *set-v1.mjs*selftest*) echo 'ok 4'; exit 0 ;; esac
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
# --no-browser never calls the browser tooling (node still runs the body builder's self-test).
: >"$WORK/node.log"; : >"$WORK/stub.log"; rc=0; LABEL=0000000000000000000000000000000000000000 rrun --dir "$WORK/pf4" --no-browser --image qc1-supplied:latest >"$WORK/pf4.out" 2>&1 || rc=$?
[[ "$rc" == 1 ]] && ! grep -q "browser-preflight\|panel-measure" "$WORK/node.log"   # node is still the body builder's (set-v1 selftest), never the browser
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
sed -n '/^cleanup() {/,/^}/p' "$RUNNER" | grep 'docker volume inspect --format .{{index .Labels "fold.qc1.owner"}}. "\$VOL"' >/dev/null
if grep -n 'docker volume rm' "$RUNNER" | grep -v 'fold.qc1.owner' | grep -v '^[0-9]*:      docker volume rm -f "\$VOL"'; then echo 'an unguarded volume removal' >&2; exit 1; fi
if grep -nE 'docker volume create [^-]|docker volume create "|--name qc1' "$RUNNER"; then echo 'the volume is named' >&2; exit 1; fi
grep -q '^VOL_TOKEN="\$(cat /proc/sys/kernel/random/uuid)"$' "$RUNNER"

# --- auto_explain / log_lock_waits are never left on in the runner's own database: reset on every exit, --keep and signals included ---------------------------------------------
# The runner's own functions (cleanup, autoexplain_on/off/reset, await_setting, psql_db, and capture_plans / explained_order_control_delete with the case helpers stubbed) are taken out of the
# script as they are and run against a stub `docker` that models the one thing that matters here: ALTER SYSTEM writes a pending value, pg_reload_conf makes it active, SHOW reads the active one.
AEB="$WORK/aebin"; mkdir -p "$AEB"
cat >"$AEB/docker" <<'STUB'
#!/usr/bin/env bash
printf '%s\n' "$*" >>"$STUB_LOG"
S="$STUB_STATE"
dflt() { case "$1" in auto_explain.log_min_duration) echo -1 ;; log_lock_waits) echo off ;; *) echo ;; esac; }
case "$1" in
  exec)
    args=("$@"); sqls=()
    for ((i = 0; i < ${#args[@]}; i++)); do [[ "${args[i]}" == -c ]] && sqls+=("${args[i+1]}"); done
    if [[ -n "${STUB_FAIL_RESET:-}" && " ${sqls[*]} " == *"auto_explain.log_min_duration = -1"*"RESET log_lock_waits"*"pg_reload_conf"* ]]; then exit 1; fi
    for sql in "${sqls[@]}"; do
      case "$sql" in
        "ALTER SYSTEM SET "*) k="${sql#ALTER SYSTEM SET }"; printf '%s' "${k#* = }" >"$S/pending.${k%% = *}" ;;
        "ALTER SYSTEM RESET "*) n="${sql#ALTER SYSTEM RESET }"; printf '%s' "$(dflt "$n")" >"$S/pending.$n" ;;
        "SELECT pg_reload_conf()") if [[ -z "${STUB_NO_RELOAD:-}" ]]; then for p in "$S"/pending.*; do [[ -e "$p" ]] && cp "$p" "$S/active.${p##*/pending.}"; done; fi ;;
        *json_object_agg*) echo '{"dynamic_shared_memory_type":"posix","min_dynamic_shared_memory":"0","max_parallel_maintenance_workers":"2","maintenance_work_mem":"65536","shared_buffers":"16384"}' ;;
        "SHOW server_version") echo 16.4 ;;
        df\ -k*) echo 'tmpfs 65536 12 65524 1% /dev/shm' ;;
        *VACUUM*) if [[ -n "${STUB_VACUUM_NEEDS_PARALLEL0:-}" && "$sql" != *"PARALLEL 0"* ]]; then echo 'ERROR:  could not resize shared memory segment "/PostgreSQL.4254608656" to 67145952 bytes: No space left on device' >&2; exit 1; fi ;;
        "SHOW "*) n="${sql#SHOW }"; if [[ -e "$S/active.$n" ]]; then cat "$S/active.$n"; echo; else dflt "$n"; fi ;;
        *max\(id\)*) echo 0 ;;
      esac
    done ;;
  inspect) case "$*" in *HostConfig.ShmSize*) echo 67108864 ;; *HostConfig.Memory*) echo 2147483648 ;; *.Image*) echo sha256:dddd ;; esac ;;
  rm|network|volume) : ;;
esac
exit 0
STUB
chmod +x "$AEB/docker"
cat >"$WORK/ae-driver.sh" <<'DRV'
set -Eeuo pipefail
exec </dev/null
RUNNER="$1"; MODE="$2"
export PATH="$AEB:$PATH"
KEEP="${KEEP:-0}"; SAMPLERS=(); CONTAINERS=(dbcid appcid); NET_ID=netid; VOL=""; VOL_TOKEN=""; DB_CID=dbcid; APP_STARTED_MS=0; ROOT=/nonexistent
mkdir -p "$WORK/ae-work"; echo '[]' >"$WORK/ae-work/plan-items.json"; WORK="$WORK/ae-work"
sleep() { :; }
src() { awk -v f="$1" '$0 ~ "^" f "\\(\\) \\{" { on = 1 } on { print } on && (/^}$/ || /^[a-z_]+\(\) \{.*\}$/) { exit }' "$RUNNER"; }
for fn in autoexplain_reset cleanup await_setting psql_db autoexplain_on autoexplain_off capture_plans explained_order_control_delete; do eval "$(src "$fn")"; done
eval "$(grep -E '^AUTOEXPLAIN_DIRTY=0$' "$RUNNER" | head -n 1)"
eval "$(grep -E '^FINALIZED=0 CURRENT_STEP=' "$RUNNER")"; MEASUREMENTS=""   # no rows in this driver: cleanup finalizes nothing
eval "$(grep -E "^trap (cleanup EXIT|'exit (130|143|129)' (INT|TERM|HUP))\$" "$RUNNER")"
fresh_user() { echo 5; }; load_scenario() { :; }; traverse() { :; }; mint_attempt() { echo id; }; qc1_delete_body_n() { :; }; drop_scenarios() { :; }
qc1_scheduler_wait() { echo 0; }; quiesce() { :; }; ms() { echo 1; }; table_rows() { echo 5; }; do_delete() { return 1; }
case "$MODE" in
  fail7)  autoexplain_on; exit 7 ;;
  err)    autoexplain_on; false ;;
  term)   autoexplain_on; kill -TERM $$; : ;;
  int)    autoexplain_on; kill -INT $$; : ;;
  hup)    autoexplain_on; kill -HUP $$; : ;;
  clean)  autoexplain_on; autoexplain_off; exit 0 ;;
  ok0)    autoexplain_on; exit 0 ;;
  never)  exit 3 ;;
  halfon) AUTOEXPLAIN_DIRTY=1; psql_db -c "ALTER SYSTEM SET auto_explain.log_min_duration = 0" >/dev/null; exit 4 ;;
  offfail) autoexplain_on; export STUB_NO_RELOAD=1; autoexplain_off ;;
  capture) capture_plans 100 ;;
  explained) explained_order_control_delete 100 start ;;
esac
DRV
: >"$WORK/ae.dummy"
# the driver gets its default signal dispositions back (a background job of a non-interactive shell inherits SIGINT ignored, which bash would then refuse to trap)
ae_run() { # mode [env...]  -> rc
  local mode="$1"; shift
  rm -rf "$WORK/ae-state" "$WORK/ae-work"; mkdir -p "$WORK/ae-state"; : >"$WORK/ae-stub.log"
  rc=0; env --default-signal=INT,HUP,TERM "$@" AEB="$AEB" WORK="$WORK" STUB_LOG="$WORK/ae-stub.log" STUB_STATE="$WORK/ae-state" bash "$WORK/ae-driver.sh" "$RUNNER" "$mode" >"$WORK/ae.out" 2>&1 || rc=$?
}
ae_active() { local n; for n in auto_explain.log_min_duration log_lock_waits; do if [[ -e "$WORK/ae-state/active.$n" ]]; then cat "$WORK/ae-state/active.$n"; else case "$n" in auto_explain.log_min_duration) printf -- -1 ;; *) printf off ;; esac; fi; printf ' '; done; }
ae_resets() { grep -c 'ALTER SYSTEM SET auto_explain.log_min_duration = -1.*ALTER SYSTEM RESET log_lock_waits.*pg_reload_conf' "$WORK/ae-stub.log" || true; }
ae_before_rm() { local r d; r="$(grep -n 'ALTER SYSTEM SET auto_explain.log_min_duration = -1.*ALTER SYSTEM RESET log_lock_waits.*pg_reload_conf' "$WORK/ae-stub.log" | head -n 1 | cut -d: -f1)"; d="$(grep -n '^rm -fv' "$WORK/ae-stub.log" | head -n 1 | cut -d: -f1)"; [[ -n "$r" && -n "$d" && "$r" -lt "$d" ]]; }
# a failure after the enable (the exit status is the failure's own), removed stack: reset first, then the removals, the database ends with both off
ae_run fail7; [[ "$rc" == 7 && "$(ae_resets)" == 1 && "$(ae_active)" == '-1 off ' ]]; ae_before_rm; grep -qx 'qc1 autoexplain_reset=ok' "$WORK/ae.out"; grep -qx 'qc1 result=7 artifacts=.* retained_stack=0' "$WORK/ae.out"
[[ "$(grep -c '^rm -fv ' "$WORK/ae-stub.log")" == 2 ]]
# --keep: the same reset, and nothing removed (the stack stays, quiet)
ae_run fail7 KEEP=1; [[ "$rc" == 7 && "$(ae_resets)" == 1 && "$(ae_active)" == '-1 off ' ]]; grep -qx 'qc1 autoexplain_reset=ok' "$WORK/ae.out"; grep -qx 'qc1 result=7 artifacts=.* retained_stack=1' "$WORK/ae.out"
if grep -q '^rm -fv\|^network rm\|^volume rm' "$WORK/ae-stub.log"; then echo '--keep removed something' >&2; exit 1; fi
# an error (set -e), and a successful run that never turned it off: the status stays what it was, the reset runs
ae_run err; [[ "$rc" == 1 && "$(ae_resets)" == 1 && "$(ae_active)" == '-1 off ' ]]
ae_run ok0 KEEP=1; [[ "$rc" == 0 && "$(ae_resets)" == 1 && "$(ae_active)" == '-1 off ' ]]
# signals: the status is the signal's (130, 143, 129), the reset still runs
ae_run term; [[ "$rc" == 143 && "$(ae_resets)" == 1 && "$(ae_active)" == '-1 off ' ]]; ae_before_rm
ae_run int; [[ "$rc" == 130 && "$(ae_resets)" == 1 && "$(ae_active)" == '-1 off ' ]]
ae_run hup KEEP=1; [[ "$rc" == 129 && "$(ae_resets)" == 1 && "$(ae_active)" == '-1 off ' ]]
# a failure halfway through the enable (the first ALTER ran, nothing was proven): the flag was set before it, so the reset is still owed and done
ae_run halfon; [[ "$rc" == 4 && "$(ae_resets)" == 1 && "$(ae_active)" == '-1 off ' ]]
# a clean on/off leaves nothing owed: no reset statement, and the line says so
ae_run clean; [[ "$rc" == 0 && "$(ae_resets)" == 0 && "$(ae_active)" == '-1 off ' ]]; grep -qx 'qc1 autoexplain_reset=not-needed' "$WORK/ae.out"
ae_run never; [[ "$rc" == 3 && "$(ae_resets)" == 0 ]]; grep -qx 'qc1 autoexplain_reset=not-needed' "$WORK/ae.out"
# A failed reset preserves an existing failure and turns success into failure.
ae_run fail7 STUB_FAIL_RESET=1; [[ "$rc" == 7 && "$(ae_resets)" == 3 ]]   # three tries, all refused; grep -qx 'qc1 autoexplain_reset=failed' "$WORK/ae.out"; grep -q 'WARNING: auto_explain / log_lock_waits could not be proven off' "$WORK/ae.out"
[[ "$(grep -c '^rm -fv ' "$WORK/ae-stub.log")" == 2 ]]; grep -q 'the container is removed below' "$WORK/ae.out"; [[ -s "$WORK/ae-work/autoexplain-reset-failed" ]]
ae_run ok0 STUB_FAIL_RESET=1 KEEP=1; [[ "$rc" == 1 ]]; grep -qx 'qc1 autoexplain_reset=failed' "$WORK/ae.out"; grep -q 'goes on logging a plan for every statement until you run: docker exec dbcid psql' "$WORK/ae.out"; grep -q "ALTER SYSTEM SET auto_explain.log_min_duration = -1" "$WORK/ae.out"
[[ -s "$WORK/ae-work/autoexplain-reset-failed" ]]; grep -qx 'qc1 result=1 artifacts=.* retained_stack=1' "$WORK/ae.out"; [[ "$(ae_active)" == '0 on ' ]]   # still on, and the report says so
# the reload does not take effect: turning it off cannot be proven, the run stops (exit 1), the reset cannot be proven either, and that is reported too
ae_run offfail; [[ "$rc" == 1 ]]; grep -qx 'qc1 autoexplain_reset=failed' "$WORK/ae.out"
# the older plan capture (capture_plans) and the explained deletion, each failing at its delete AFTER the enable, in the real functions
ae_run capture; [[ "$rc" != 0 && "$(ae_resets)" == 1 && "$(ae_active)" == '-1 off ' ]]; grep -qx 'qc1 autoexplain_reset=ok' "$WORK/ae.out"
ae_run capture KEEP=1; [[ "$rc" != 0 && "$(ae_resets)" == 1 && "$(ae_active)" == '-1 off ' ]]
ae_run explained; [[ "$rc" != 0 && "$(ae_resets)" == 1 && "$(ae_active)" == '-1 off ' ]]; grep -qx 'qc1 autoexplain_reset=ok' "$WORK/ae.out"
ae_run explained KEEP=1; [[ "$rc" != 0 && "$(ae_resets)" == 1 && "$(ae_active)" == '-1 off ' ]]
# only the runner's own database container is touched: every statement of the reset goes to DB_CID, never to another container or a host database
if grep '^exec ' "$WORK/ae-stub.log" | grep -v '^exec -i dbcid psql -X -U church -d fold ' ; then echo 'a statement went to something other than the runner database' >&2; exit 1; fi
# static: the order and the shape in the real runner
cl_body="$(sed -n '/^cleanup() {/,/^}/p' "$RUNNER")"
l_k="$(grep -n 'kill "\$p"' <<<"$cl_body" | cut -d: -f1)"; l_r="$(grep -n 'autoexplain_reset' <<<"$cl_body" | head -n 1 | cut -d: -f1)"; l_d="$(grep -n 'docker rm -fv' <<<"$cl_body" | cut -d: -f1)"
[[ -n "$l_k$l_r$l_d" && "$l_k" -lt "$l_r" && "$l_r" -lt "$l_d" ]]
grep -qF "trap '' INT TERM HUP" <<<"$cl_body"; grep -qF 'exit "$rc"' <<<"$cl_body"
if ! grep -qF '(( rc != 0 )) || rc=1' <<<"$cl_body"; then
  echo 'cleanup must fail a successful run if the reset fails' >&2
  exit 1
fi
grep -qx "trap 'exit 129' HUP" "$RUNNER"
ar_body="$(sed -n '/^autoexplain_reset() {/,/^}/p' "$RUNNER")"; [[ "$(grep -c 'timeout 20 docker exec -i "\$DB_CID"' <<<"$ar_body")" -ge 3 ]]; grep -qE 'docker (rm|volume|network)|DROP|DELETE|TRUNCATE' <<<"$ar_body" && exit 1
ao_body="$(sed -n '/^autoexplain_on() {/,/^}/p' "$RUNNER")"; [[ "$(sed -n 2p <<<"$ao_body")" == "  AUTOEXPLAIN_DIRTY=1"* ]]                  # the first statement of the enable
cp_body="$(sed -n '/^capture_plans() {/,/^}/p' "$RUNNER")"; l_f="$(grep -n 'AUTOEXPLAIN_DIRTY=1' <<<"$cp_body" | cut -d: -f1)"; l_a="$(grep -n 'ALTER SYSTEM SET auto_explain.log_min_duration = 0' <<<"$cp_body" | cut -d: -f1)"
l_w="$(grep -n 'await_setting auto_explain.log_min_duration -1' <<<"$cp_body" | cut -d: -f1)"; l_c="$(grep -n 'AUTOEXPLAIN_DIRTY=0' <<<"$cp_body" | cut -d: -f1)"; [[ -n "$l_f$l_a$l_w$l_c" && "$l_f" -lt "$l_a" && "$l_w" -lt "$l_c" ]]
[[ "$(grep -c 'AUTOEXPLAIN_DIRTY=1' "$RUNNER")" == 2 && "$(grep -c '^  AUTOEXPLAIN_DIRTY=0' "$RUNNER")" == 2 ]]    # exactly the two places that turn it on and the two that prove it off

# --- a run that stops still leaves its evidence (abort finalization), and the fixture vacuums are not parallel -----------------------------------------------------------------
# The runner's own functions (finalize_evidence and the writers it calls, cleanup, autoexplain_reset, note, psql_db) are taken out of the script as they are and run against the
# stub `docker` above, in a work directory holding four REAL rows of the 100k pilot that stopped at the sparse fixture (testdata/qc1-100k-abort-rows.jsonl: the request-size
# row with its 142,675 ms shell walk and the 100,000-reason browser row with its 138,918.7 ms walk, among them) and that run's real harness.log. The stub fails a VACUUM
# that is not PARALLEL 0 with the pilot's own error, so the statement the runner sends is what decides whether the run goes on.
cat >"$WORK/fin-driver.sh" <<'DRV'
set -Eeuo pipefail
exec </dev/null
RUNNER="$1"; MODE="$2"; HERE="$3"
export PATH="$AEB:$PATH"
source "$HERE/qc1/lib.sh"
OUT="$FIN_DIR"; mkdir -p "$OUT"
cp "$HERE/testdata/qc1-100k-abort-rows.jsonl" "$OUT/measurements.jsonl"
cp "$HERE/testdata/qc1-100k-abort-harness.log" "$OUT/harness.log"
echo '{"gate":{"ok":true}}' >"$OUT/calibration.json"
WORK="$OUT"; MEASUREMENTS="$OUT/measurements.jsonl"
exec >>"$WORK/harness.log" 2>&1
KEEP="${KEEP:-0}"; SAMPLERS=(); CONTAINERS=(dbcid appcid); NET_ID=netid; VOL=""; VOL_TOKEN=""; DB_CID=dbcid; AUTOEXPLAIN_DIRTY=0
SIZES=100000; COUNTS=0,10,100,1000,10000; DENSE=100000; DENSE_LIST=(100000); ALL_DELETE_LIST=(0 10 100 1000 10000 100000); REVISION="$(printf 'a%.0s' {1..40})"; MAX_REQUEST_BYTES=6291456
IMAGE=qc1-fold:test; IMAGE_ID=sha256:test; LEVEL=2; DROPLET=2gb; DB_MEMORY=2g; APP_MEMORY=1g; CPUSET=0; LAYOUT=spread; REASON_LEN=200; TARGET_REASON_LEN=1000
NEXT_CHANGED_PRESSES=1; NEXT_CHANGED_RUNS=1; NEXT_CHANGED_ONLY=0; PROFILE_NEXT_CHANGED=0
SECONDS_PER_REASON=10; SITTING_MINUTES=15; PG_REPO_DIGESTS='["pg-digest"]'; APP_REPO_DIGESTS='["app-digest"]'
APP_CONTAINERS='[{"imageId":"sha256:test","memoryLimitBytes":1024}]'; DAEMON_JSON='{"rootless":false}'; SOURCES='["source"]'; BROWSER_RUNTIME='{"ok":true}'
sleep() { :; }
src() { awk -v f="$1" '$0 ~ "^" f "\\(\\) \\{" { on = 1 } on { print } on && (/^}$/ || /^[a-z_]+\(\) \{.*\}$/) { exit }' "$RUNNER"; }
for fn in jnum jraw fin_psql db_settings_json write_environment write_abort_record finalize_evidence autoexplain_reset cleanup note psql_db; do eval "$(src "$fn")"; done
eval "$(grep -E '^FINALIZED=0 CURRENT_STEP=' "$RUNNER")"
eval "$(grep -E "^trap 'ERR_CMD=" "$RUNNER")"
eval "$(grep -E "^trap (cleanup EXIT|'exit (130|143|129)' (INT|TERM|HUP))\$" "$RUNNER")"
sparse() { sed -n '/note "size \$n: blanking background reasons for the sparse case"/{n;p;n;p}' "$RUNNER"; }
case "$MODE" in
  sparse-real)  note "size 100000: blanking background reasons for the sparse case"; sparse >"$WORK/sparse-step.sh"; source "$WORK/sparse-step.sh"; exit 0 ;;
  sparse-old)   note "size 100000: blanking background reasons for the sparse case"; CURRENT_SIZE=100000; sparse | sed "s/VACUUM (ANALYZE, PARALLEL 0)/VACUUM ANALYZE/" >"$WORK/sparse-step.sh"; source "$WORK/sparse-step.sh" ;;
  fail7)        note "size 100000: a step"; exit 7 ;;
  term)         note "size 100000: a step"; kill -TERM $$; : ;;
  int)          note "size 100000: a step"; kill -INT $$; : ;;
  hup)          note "size 100000: a step"; kill -HUP $$; : ;;
  finfail)      note "size 100000: a step"; mkdir "$WORK/evidence-status.json"; exit 7 ;;
  idem)         note "size 100000: a step"; finalize_evidence aborted 5 ""; sha256sum "$WORK"/*.json >"$WORK/first.sha"; wc -l <"$STUB_LOG" >"$WORK/first.calls"; finalize_evidence aborted 5 ""; sha256sum "$WORK"/*.json >"$WORK/second.sha"; wc -l <"$STUB_LOG" >"$WORK/second.calls"; exit 5 ;;
  complete)     finalize_evidence complete 0; exit 0 ;;
  early)        exit 0 ;;
esac
DRV
fin_run() { # mode [env...]  -> rc, FD (the work directory), the stub log
  local mode="$1"; shift
  FD="$WORK/fin"; rm -rf "$FD"; mkdir -p "$WORK/fin-state"; : >"$WORK/fin-stub.log"
  rc=0; env --default-signal=INT,HUP,TERM "$@" AEB="$AEB" FIN_DIR="$FD" STUB_LOG="$WORK/fin-stub.log" STUB_STATE="$WORK/fin-state" STUB_VACUUM_NEEDS_PARALLEL0=1 bash "$WORK/fin-driver.sh" "$RUNNER" "$mode" "$HERE" >/dev/null 2>&1 || rc=$?
}
# (1) The fixture vacuums are not parallel: every VACUUM the runner sends carries PARALLEL 0, the sparse step is UPDATE then VACUUM, and the runner's real statement does not stop the run.
[[ "$(grep -c 'VACUUM' <(grep -v '^ *#' "$RUNNER" | grep -E "psql_db|-c '" ) )" -ge 2 ]]
if grep -E "'VACUUM[^']*'" "$RUNNER" | grep -v 'PARALLEL 0' | grep -v '^ *#'; then echo 'a fixture VACUUM that can run in parallel' >&2; exit 1; fi
sp="$(sed -n '/note "size \$n: blanking background reasons for the sparse case"/{n;p;n;p}' "$RUNNER")"
[[ "$(sed -n 1p <<<"$sp")" == *"UPDATE contact_requests SET reason = ''"* && "$(sed -n 2p <<<"$sp")" == *"VACUUM (ANALYZE, PARALLEL 0) contact_requests"* ]]
grep -qF "VACUUM (ANALYZE, PARALLEL 0) qc1_clean.contact_requests" "$RUNNER"
fin_run sparse-real; [[ "$rc" == 0 ]]; grep -qF 'VACUUM (ANALYZE, PARALLEL 0) contact_requests' "$WORK/fin-stub.log"
[[ ! -e "$FD/abort.json" && ! -e "$FD/evidence-status.json" ]]; grep -qx 'qc1 evidence=not-needed' "$FD/harness.log"        # nothing to finalize when the run is not failing
# (2) The statement the pilot died on (a bare VACUUM against the 64 MiB /dev/shm): the run stops, the exit status is the failure's own (1), and the evidence is left.
fin_run sparse-old; [[ "$rc" == 1 ]]
jq -e '.outcome == "aborted" and .exitCode == 1 and .signal == null and (.step | test("sparse case")) and .sizeInProgress == 100000 and .failingFunction == "psql_db" and (.calledFrom.text | test("VACUUM ANALYZE contact_requests")) and (.failingCommand | test("docker exec")) and .rowsMeasured == 4 and .lastRow.case == "browser-panel-100000"
  and (.harnessLogTail | map(test("could not resize shared memory segment")) | any) and (.note | test("did not finish"))' "$FD/abort.json" >/dev/null
jq -e '.complete == false and .releaseEvidence == false and .aborted.exitCode == 1 and .aborted.step == "size 100000: blanking background reasons for the sparse case" and .coverage.kind == "pilot"' "$FD/evidence-status.json" >/dev/null
# the findings the retained rows already contain are kept exactly: the shell walk and the browser walk, with their figures, and nothing else
[[ "$(jq -c '[.budgetFailures[] | .failure] | sort' "$FD/evidence-status.json")" == '["browser-traversal","browser-walk","traversal"]' ]]
[[ "$(jq -c '[.budgetFailures[] | select(.failure == "traversal" or .failure == "browser-traversal") | .ms] | sort' "$FD/evidence-status.json")" == '[138918.7,142675]' ]]
# and what was not reached is listed as missing, the legacy rows as pending; the only invalid findings are about the incomplete environment record
jq -e '([.missing[] | select(.item == 1 and .size == 100000 and .case == "sparse")] | length) == 1 and ([.pending[] | select(.item == 4)] | length) > 0 and ([.invalid[] | select(.failure != "environment")] | length) == 0' "$FD/evidence-status.json" >/dev/null
jq -e '.incomplete == true and (.incompleteReason | test("before it finished")) and .database.devShm.totalKb == 65536 and .database.hostConfigShmSizeBytes == 67108864 and .database.settings.maintenance_work_mem == "65536" and .database.settings.max_parallel_maintenance_workers == "2" and .postgresVersion == "16.4" and (.database.note | test("not changed"))' "$FD/environment.json" >/dev/null
jq -e '.evidence.aborted.exitCode == 1 and .judgeRan == true' "$FD/summary.json" >/dev/null
[[ "$(jq -c '[.[] | .failure] | sort' <(jq -s . "$FD/failures.jsonl") 2>/dev/null)" == *traversal* ]]                                   # the judge's output is kept too
grep -qx 'qc1 evidence=aborted' "$FD/harness.log"; grep -qx 'qc1 result=1 artifacts=.* retained_stack=0' "$FD/harness.log"
# the evidence is written before any container is removed (the environment record reads the daemon), and the removals still happen
i_l="$(grep -n '^inspect ' "$WORK/fin-stub.log" | head -n 1 | cut -d: -f1)"; r_l="$(grep -n '^rm -fv ' "$WORK/fin-stub.log" | head -n 1 | cut -d: -f1)"
[[ -n "$i_l" && -n "$r_l" && "$i_l" -lt "$r_l" && "$(grep -c '^rm -fv ' "$WORK/fin-stub.log")" == 2 ]]
# --keep: the same evidence, nothing removed
fin_run sparse-old KEEP=1; [[ "$rc" == 1 && -s "$FD/evidence-status.json" ]]; if grep -q '^rm -fv\|^network rm' "$WORK/fin-stub.log"; then echo '--keep removed something' >&2; exit 1; fi
# (3) The exit status is the original one, whatever the evidence does: another failure's own status, each signal's, and a finalization that cannot write a file.
fin_run fail7; [[ "$rc" == 7 ]]; jq -e '.exitCode == 7 and .failingCommand == null' "$FD/abort.json" >/dev/null
fin_run term; [[ "$rc" == 143 ]]; jq -e '.exitCode == 143 and .signal == "TERM"' "$FD/abort.json" >/dev/null; jq -e '.complete == false and .aborted.signal == "TERM"' "$FD/evidence-status.json" >/dev/null
fin_run int; [[ "$rc" == 130 ]]; jq -e '.signal == "INT"' "$FD/abort.json" >/dev/null
fin_run hup KEEP=1; [[ "$rc" == 129 ]]; jq -e '.signal == "HUP"' "$FD/abort.json" >/dev/null
fin_run finfail; [[ "$rc" == 7 ]]; grep -qx 'qc1 evidence=aborted-incomplete' "$FD/harness.log"; grep -q 'WARNING: the evidence of this aborted run could not be fully written' "$FD/harness.log"; [[ -s "$FD/abort.json" ]]
# (4) It runs once: a second finalization writes nothing new.
fin_run idem; [[ "$rc" == 5 ]]; cmp "$FD/first.sha" "$FD/second.sha"; cmp "$FD/first.calls" "$FD/second.calls"   # the second call reads nothing from the daemon or the database
# (5) A finished run finalizes through the same function and is not marked aborted; a run that exits 0 without having started measuring leaves nothing behind.
fin_run complete; [[ "$rc" == 0 ]]; jq -e 'has("aborted") | not' "$FD/evidence-status.json" >/dev/null
jq -e '.incomplete == false and .recoveryPolicy == 2 and .maxRequestBytes == 6291456 and .settings.reasonLen == 200 and .settings.targetReasonLen == 1000 and .settings.secondsPerReason == 10 and .settings.sittingMinutes == 15 and .appImageId == "sha256:test" and .limits.appMemoryLimitBytes == 1024 and .daemon.rootless == false and .sources == ["source"] and .browserRuntime.ok == true and .postgresRepoDigests == ["pg-digest"] and .appRepoDigests == ["app-digest"]' "$FD/environment.json" >/dev/null
[[ ! -e "$FD/abort.json" ]]
fin_run early; [[ "$rc" == 0 && ! -e "$FD/abort.json" ]]
# (6) Static: cleanup finalizes after the auto_explain reset and before any removal, from the original status, and the normal end calls the same function.
cl_body="$(sed -n '/^cleanup() {/,/^}/p' "$RUNNER")"
l_r="$(grep -n 'autoexplain_reset;' <<<"$cl_body" | head -n 1 | cut -d: -f1)"; l_f="$(grep -n 'finalize_evidence aborted' <<<"$cl_body" | cut -d: -f1)"; l_d="$(grep -n 'docker rm -fv' <<<"$cl_body" | cut -d: -f1)"
[[ -n "$l_r$l_f$l_d" && "$l_r" -lt "$l_f" && "$l_f" -lt "$l_d" ]]; grep -qF 'local orig=$rc' <<<"$cl_body"; grep -qF 'trap - ERR' <<<"$cl_body"; grep -qF 'exit "$rc"' <<<"$cl_body"
grep -qx 'finalize_evidence complete 0 || { echo '"'"'qc1: the evidence of this run could not be written completely'"'"' >&2; exit 1; }' "$RUNNER"
grep -qE '^qc1_judge "\$MEASUREMENTS"|^jq -n --arg image' "$RUNNER" && exit 1                       # the end of the script no longer carries a second copy of the finalization
echo 'qc1 runtime fixtures: ok'
