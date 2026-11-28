import { expect, test } from "@playwright/test";
import { readFileSync } from "node:fs";

// The colour roles in web/css/app.css meet WCAG contrast: text 4.5:1,
// control edges and the focus ring 3:1. Adapted from Fold's token check.
const css = readFileSync(new URL("../web/css/app.css", import.meta.url), "utf8");
const theme = css.slice(css.indexOf("@theme"), css.indexOf("\n}", css.indexOf("@theme")));
const vars: Record<string, string> = {};
for (const m of theme.matchAll(/--color-([\w-]+):\s*([^;]+);/g)) vars[m[1]] = m[2].trim();

const resolve = (n: string, d = 0): string | null => {
  const v = vars[n];
  if (!v || d > 5) return null;
  const r = v.match(/^var\(--color-([\w-]+)\)$/);
  return r ? resolve(r[1], d + 1) : v;
};
const lin = (c: number) => (c <= 0.04045 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4);
const lum = (h: string) => {
  const c = [1, 3, 5].map((i) => lin(parseInt(h.slice(i, i + 2), 16) / 255));
  return 0.2126 * c[0] + 0.7152 * c[1] + 0.0722 * c[2];
};
const ratio = (a: string, b: string) => {
  const [x, y] = [lum(a), lum(b)].sort((p, q) => p - q);
  return (y + 0.05) / (x + 0.05);
};

const pairs: [string, string, number][] = [
  ["primary", "page", 4.5], ["primary", "surface", 4.5], ["primary-hover", "surface", 4.5],
  ["primary-foreground", "primary", 4.5], ["primary-foreground", "primary-hover", 4.5], ["primary-foreground", "primary-active", 4.5],
  ["primary-on-subtle", "primary-subtle", 4.5], ["primary-border", "surface", 3],
  ["text", "primary-subtle", 4.5], ["text-muted", "primary-subtle", 4.5], ["primary-border", "primary-subtle", 3],
  ["border-strong", "surface", 3], ["border-strong", "page", 3],
  ["text", "page", 4.5], ["text", "surface", 4.5], ["text", "sunken", 4.5],
  ["text-muted", "page", 4.5], ["text-muted", "surface", 4.5], ["text-muted", "sunken", 4.5],
  ["text-subtle", "page", 4.5], ["text-subtle", "surface", 4.5],
  ["danger", "surface", 4.5], ["danger", "page", 4.5], ["danger", "danger-subtle", 4.5],
  ["danger-foreground", "danger", 4.5], ["danger-foreground", "danger-hover", 4.5],
  ["danger-on-subtle", "danger-subtle", 4.5], ["success-on-subtle", "success-subtle", 4.5],
  ["warning-on-subtle", "warning-subtle", 4.5], ["info-on-subtle", "info-subtle", 4.5],
  ["focus", "page", 3], ["focus", "surface", 3],
];

test("colour roles meet contrast", () => {
  const fails: string[] = [];
  for (const [f, b, min] of pairs) {
    const F = resolve(f), B = resolve(b);
    if (!F || !B) { fails.push(`missing ${f} or ${b}`); continue; }
    const r = ratio(F, B);
    if (r < min) fails.push(`${f} on ${b}: ${r.toFixed(2)}:1 < ${min}`);
  }
  expect(fails).toEqual([]);
});
