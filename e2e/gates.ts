import AxeBuilder from "@axe-core/playwright";
import { expect, type Page } from "@playwright/test";
import { mkdirSync } from "node:fs";

// The three gates every page passes at phone and desktop width: axe finds no
// moderate, serious or critical issue; nothing scrolls sideways or is cut
// off; every control is big enough to tap. The strict content security
// policy must never refuse anything.

export const widths = [390, 1280];
const screens = process.env.SCREENS_DIR ?? "test-results/screens";
mkdirSync(screens, { recursive: true });

export async function watchCSP(page: Page) {
  await page.addInitScript(() => {
    (window as any).__csp = [];
    document.addEventListener("securitypolicyviolation", (e) => {
      (window as any).__csp.push(`${e.violatedDirective} ${e.blockedURI}`);
    });
  });
}

export async function cspViolations(page: Page): Promise<string[]> {
  return page.evaluate(() => (window as any).__csp ?? []);
}

type Box = { el: string; name: string; w: number; h: number };

function measure() {
  const vw = document.documentElement.clientWidth;
  const out = { overflow: document.documentElement.scrollWidth - vw, cutOff: [] as Box[], small: [] as Box[], short: [] as Box[] };
  const visible = (e: Element) => {
    const s = getComputedStyle(e);
    const r = e.getBoundingClientRect();
    return s.visibility !== "hidden" && s.display !== "none" && r.width > 0 && r.height > 0;
  };
  for (const e of document.querySelectorAll("a[href], button, input, select, textarea, summary")) {
    if ((e as HTMLInputElement).type === "hidden" || !visible(e) || e.closest("[popover]:not(:popover-open), dialog:not([open])")) continue;
    let target: Element = e;
    const type = (e as HTMLInputElement).type;
    if (e.tagName === "INPUT" && (type === "checkbox" || type === "radio")) target = e.closest("label") ?? e;
    const r = target.getBoundingClientRect();
    const name = (e.textContent || (e as HTMLInputElement).name || e.id || "").trim().slice(0, 40);
    const box = { el: e.tagName.toLowerCase() + (type ? `[${type}]` : ""), name, w: Math.round(r.width), h: Math.round(r.height) };
    if (r.right > vw + 1 || r.left < -1) out.cutOff.push(box);
    if (r.width < 24 || r.height < 24) out.small.push(box);
    // The design's 44px floor, for everything but a link inside running text.
    const inline = e.tagName === "A" && !e.className && e.closest("p, li");
    if (!inline && r.height < 43.5) out.short.push(box);
  }
  return out;
}

export async function gate(page: Page, name: string) {
  for (const width of widths) {
    await page.setViewportSize({ width, height: 900 });
    await page.waitForLoadState("load");
    const at = `${name} at ${width}px`;

    const wcag = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa", "wcag22aa"]).analyze();
    const best = await new AxeBuilder({ page }).withRules(["heading-order", "landmark-one-main", "page-has-heading-one", "region"]).analyze();
    const bad = [...wcag.violations, ...best.violations].filter((v) => v.impact && v.impact !== "minor");
    expect.soft(bad.map((v) => `${v.impact} ${v.id}: ${v.help} at ${v.nodes.map((n) => n.target.join(" ")).join(", ")}`), `${at}: accessibility`).toEqual([]);

    const m = await page.evaluate(measure);
    expect.soft(m.overflow, `${at}: the page scrolls sideways`).toBeLessThanOrEqual(0);
    expect.soft(m.cutOff.map((b) => `${b.el} "${b.name}"`), `${at}: controls past the screen edge`).toEqual([]);
    expect.soft(m.small.map((b) => `${b.el} "${b.name}" ${b.w}x${b.h}`), `${at}: targets under 24x24`).toEqual([]);
    expect.soft(m.short.map((b) => `${b.el} "${b.name}" ${b.w}x${b.h}`), `${at}: controls under 44px tall`).toEqual([]);
    expect.soft(await cspViolations(page), `${at}: content security policy violations`).toEqual([]);

    await page.screenshot({ path: `${screens}/${name.replaceAll(" ", "-")}-${width}.png`, fullPage: true, animations: "disabled" });
  }
}
