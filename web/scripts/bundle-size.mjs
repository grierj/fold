// Fails the build when the entry JS chunk grows past its budget (#77).
// Run after `vite build`; lower BUDGET_GZIP_KB when the bundle shrinks.
import { readFileSync, readdirSync } from "node:fs";
import { gzipSync } from "node:zlib";

const BUDGET_GZIP_KB = 140;
const html = readFileSync("dist/index.html", "utf8");
const entry = html.match(/<script[^>]+src="\/assets\/([^"]+\.js)"/)?.[1];
if (!entry || !readdirSync("dist/assets").includes(entry)) {
  console.error("bundle-size: no entry chunk found in dist/index.html");
  process.exit(1);
}
const kb = gzipSync(readFileSync(`dist/assets/${entry}`)).length / 1024;
console.log(`bundle-size: ${entry} is ${kb.toFixed(1)} kB gzip (budget ${BUDGET_GZIP_KB} kB)`);
if (kb > BUDGET_GZIP_KB) {
  console.error("bundle-size: over budget. Lazy-load what most pages don't need, or raise the budget on purpose.");
  process.exit(1);
}
