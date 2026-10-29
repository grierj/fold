// Token contrast (ENFORCEMENT.md B7): resolves the @theme roles in index.css
// and fails when a text pair is under 4.5:1 or a control edge under 3:1.
// Usage: node scripts/token-contrast.mjs [src/index.css]   (npm run contrast)
import { readFileSync } from "node:fs";
const css = readFileSync(process.argv[2] ?? new URL("../src/index.css", import.meta.url), "utf8");
const theme = css.slice(css.indexOf("@theme"), css.indexOf("\n}", css.indexOf("@theme")));
const vars = {};
for (const m of theme.matchAll(/--color-([\w-]+):\s*([^;]+);/g)) vars[m[1]] = m[2].trim();
const resolve = (n, d = 0) => { const v = vars[n]; if (!v || d > 5) return null; const r = v.match(/^var\(--color-([\w-]+)\)$/); return r ? resolve(r[1], d + 1) : v; };
const lin = c => c <= 0.04045 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4;
const lum = h => { const c = [1, 3, 5].map(i => lin(parseInt(h.slice(i, i + 2), 16) / 255)); return .2126 * c[0] + .7152 * c[1] + .0722 * c[2]; };
const ratio = (a, b) => { const [x, y] = [lum(a), lum(b)].sort((p, q) => p - q); return (y + .05) / (x + .05); };
// [foreground role, background role, minimum, why]
const pairs = [
  ["primary", "page", 4.5, "links on page"], ["primary", "surface", 4.5, "links on cards"],
  ["primary-foreground", "primary", 4.5, "button label"], ["primary-foreground", "primary-hover", 4.5, "button hover"],
  ["primary-foreground", "primary-active", 4.5, "button pressed"],
  ["primary-on-subtle", "primary-subtle", 4.5, "selected chip text"],
  ["primary-border", "surface", 3, "selected chip edge (must be at least as strong as border-strong)"],
  ["border-strong", "surface", 3, "input and unselected chip edge"], ["border-strong", "page", 3, "input edge on page"],
  ["text", "page", 4.5], ["text", "surface", 4.5], ["text-muted", "page", 4.5], ["text-muted", "surface", 4.5], ["text-muted", "sunken", 4.5],
  ["text-subtle", "page", 4.5], ["text-subtle", "surface", 4.5],
  ["danger", "surface", 4.5, "inline error and outline-danger text"], ["danger", "page", 4.5],
  ["danger-foreground", "danger", 4.5], ["danger-foreground", "danger-hover", 4.5],
  ["danger-on-subtle", "danger-subtle", 4.5], ["success-on-subtle", "success-subtle", 4.5], ["success-foreground", "success", 4.5],
  ["warning-on-subtle", "warning-subtle", 4.5], ["warning-foreground", "warning", 4.5],
  ["info-on-subtle", "info-subtle", 4.5], ["info-foreground", "info", 4.5], ["inverse-foreground", "inverse", 4.5],
  ["focus", "page", 3, "focus ring"], ["focus", "surface", 3, "focus ring"],
];
// Component rules: which role a class uses for a non-text edge that must reach 3:1.
const edges = [...css.matchAll(/\.(btn-[\w-]+|chip[^{]*?)\s*\{\s*@apply([^;]*);/g)].flatMap(([, cls, body]) =>
  [...body.matchAll(/(?:^|\s)border-([a-z-]+)(?=\s|$)/g)].map(m => m[1]).filter(r => vars[r] && r !== "border").map(r => [cls.trim(), r]));
const animatedDisabledPairs = [...css.matchAll(/\.(btn-[\w-]+)\s*\{\s*@apply([^;]*);/g)]
  .filter(([, , body]) => /(?:^|\s)transition-(?:colors|all)(?=\s|$)/.test(body) && /(?:^|\s)disabled:text-[\w-]+(?=\s|$)/.test(body) && /(?:^|\s)disabled:bg-[\w-]+(?=\s|$)/.test(body));
let fail = 0;
for (const [f, b, min, why = ""] of pairs) {
  const F = resolve(f), B = resolve(b);
  if (!F || !B) { console.log(`MISSING  ${f} on ${b}`); fail++; continue; }
  const r = ratio(F, B); const ok = r >= min; if (!ok) fail++;
  console.log(`${ok ? "pass" : "FAIL"} ${r.toFixed(2).padStart(5)}:1 >= ${min}  ${f} on ${b}  ${why}`);
}
for (const [cls, role] of edges) {
  const r = ratio(resolve(role), resolve("surface")); const ok = r >= 3; if (!ok) fail++;
  console.log(`${ok ? "pass" : "FAIL"} ${r.toFixed(2).padStart(5)}:1 >= 3  .${cls} edge border-${role} on surface`);
}
for (const [, cls] of animatedDisabledPairs) {
  fail++;
  console.log(`FAIL .${cls} animates both disabled text and background; contrasting text can fade through an unreadable intermediate state`);
}
process.exit(fail ? 1 : 0);
