// Fails when the entry and its eager modulepreloads exceed the budget (#152).
// Run after `vite build`; lower BUDGET_GZIP_KB when the bundle shrinks.
import { readFileSync, readdirSync } from "node:fs";
import { gzipSync } from "node:zlib";

// Raised from 140 for the product-audit slices (Grier, Oct 30, 2026); trim later.
const BUDGET_GZIP_KB = 160;
const html = readFileSync("dist/index.html", "utf8");
const entry = html.match(/<script[^>]+src="\/assets\/([^"]+\.js)"/)?.[1];
if (!entry || !readdirSync("dist/assets").includes(entry)) {
  console.error("bundle-size: no entry chunk found in dist/index.html");
  process.exit(1);
}
const chunks = new Set([entry]);
for (const tag of html.matchAll(/<link\b[^>]*>/g)) {
  if (!/\brel="modulepreload"/.test(tag[0])) continue;
  const chunk = tag[0].match(/\bhref="\/assets\/([^"/]+\.js)"/)?.[1];
  if (chunk) chunks.add(chunk);
}
let bytes = 0;
for (const chunk of chunks) {
  const size = gzipSync(readFileSync(`dist/assets/${chunk}`)).length;
  bytes += size;
  console.log(`bundle-size: ${chunk}: ${(size / 1024).toFixed(1)} kB gzip`);
}
const kb = bytes / 1024;
console.log(`bundle-size: ${chunks.size} eager chunks total ${kb.toFixed(1)} kB gzip (budget ${BUDGET_GZIP_KB} kB)`);
if (kb > BUDGET_GZIP_KB) {
  console.error("bundle-size: over budget. Lazy-load what most pages don't need, or raise the budget on purpose.");
  process.exit(1);
}
