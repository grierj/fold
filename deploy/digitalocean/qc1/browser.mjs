// The one place the QC-1 browser is found and launched, shared by
// browser-preflight.mjs (run before anything is measured or drilled) and
// panel-measure.mjs (the measurement). The restore drill's browser proof
// (testdata/restore-browser.cjs) loads the same launch rules from
// browser-launch.cjs, so the preflight proves exactly the launch each consumer
// will make.
//
// Resolution: @playwright/test (the measurement) and playwright (the restore
// proof) are resolved from the repository's web/ directory (QC1_WEB_DIR), where
// `npm ci` installed them from web/package-lock.json. Nothing is resolved from
// the directory of this script or from the current directory.
// Browser: QC1_CHROMIUM names an installed Chromium (the nested runner's Alpine
// package; Playwright's own download is a glibc build that does not run there).
// Without it Playwright uses the browser it downloaded itself.
import { createRequire } from "node:module";
import { readFileSync } from "node:fs";
import path from "node:path";

const launch = createRequire(import.meta.url)("./browser-launch.cjs");
export const { majorOf, nodeEngineProblem, chromiumMajorDelta, launchOptions, restoreLaunchOptions } = launch;

// Throws when web/node_modules does not hold Playwright. Returns both entry
// points the repository uses: chromium from @playwright/test and chromium from
// playwright (the package restore-browser.cjs requires).
export function resolvePlaywright(webDir) {
  const req = createRequire(path.join(webDir, "package.json"));
  const corePkgPath = req.resolve("playwright-core/package.json");
  const core = JSON.parse(readFileSync(corePkgPath, "utf8"));
  const test = JSON.parse(readFileSync(req.resolve("@playwright/test/package.json"), "utf8"));
  const pkg = JSON.parse(readFileSync(req.resolve("playwright/package.json"), "utf8"));
  const browsers = JSON.parse(readFileSync(path.join(path.dirname(corePkgPath), "browsers.json"), "utf8"));
  const bundled = (browsers.browsers ?? []).find((b) => b.name === "chromium")?.browserVersion ?? null;
  return {
    chromium: req("@playwright/test").chromium,
    chromiumViaPlaywright: req("playwright").chromium,
    playwrightVersion: test.version,
    playwrightPackageVersion: pkg.version,
    coreVersion: core.version,
    nodeEngine: core.engines?.node ?? null,
    corePath: path.dirname(corePkgPath),
    bundledChromiumVersion: bundled,
  };
}
