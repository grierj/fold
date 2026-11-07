// How the QC-1 measurement and the restore drill's browser proof launch Chromium,
// in one dependency-free CommonJS file so both can load it: qc1/browser.mjs
// (ESM) imports it, and testdata/restore-browser.cjs requires it by the absolute
// path restore-drill.sh gives it (RESTORE_BROWSER_LAUNCH), because that helper is
// copied out of the repository before it runs.
//
// A disposable runner (restore-drill-nested.sh) has Alpine's Chromium, named by
// QC1_CHROMIUM, and Playwright's own download does not run there (glibc). On a
// host with Playwright's browsers installed QC1_CHROMIUM is unset and the
// restore proof launches exactly as it always has: { headless: true }.
"use strict";

function majorOf(version) {
  const m = /^(\d+)\./.exec(String(version ?? ""));
  return m ? Number(m[1]) : null;
}

// engine is a package.json "engines.node" range. Only ">=N" forms matter for
// Playwright; anything else is reported as unreadable rather than guessed.
// Returns a problem string, or null when satisfied.
function nodeEngineProblem(engine, nodeVersion) {
  if (engine == null) return null;
  const have = majorOf(`${nodeVersion}.`.replace(/^v/, ""));
  const want = /^>=\s*(\d+)/.exec(String(engine).trim());
  if (!want) return `cannot read the node engine range ${JSON.stringify(engine)}`;
  if (have === null) return `cannot read the node version ${nodeVersion}`;
  return have >= Number(want[1]) ? null : `node ${nodeVersion} is older than the engine range ${engine} Playwright asks for`;
}

// How far apart two Chromium major versions are; null if either is unreadable.
function chromiumMajorDelta(systemVersion, bundledVersion) {
  const a = majorOf(systemVersion), b = majorOf(bundledVersion);
  return a === null || b === null ? null : Math.abs(a - b);
}

// The measurement's launch options. --no-sandbox and --disable-dev-shm-usage are
// needed to run as root in a container with a small /dev/shm; they are added only
// when the disposable-runner flag set by restore-drill-nested.sh is present
// together with QC1_NO_SANDBOX=1, never for a browser on a person's own machine.
function launchOptions(env) {
  const args = ["--enable-precise-memory-info"];
  const options = { headless: true, args };
  if (env.QC1_CHROMIUM) options.executablePath = env.QC1_CHROMIUM;
  if (env.QC1_NO_SANDBOX === "1" && env.FOLD_DRILL_ISOLATED_DAEMON === "1") args.push("--no-sandbox", "--disable-dev-shm-usage");
  return options;
}

// The restore proof's launch options: the same executable and sandbox rule when a
// runner names its Chromium, and otherwise exactly what the proof has always used.
function restoreLaunchOptions(env) {
  return env.QC1_CHROMIUM ? launchOptions(env) : { headless: true };
}

module.exports = { majorOf, nodeEngineProblem, chromiumMajorDelta, launchOptions, restoreLaunchOptions };
