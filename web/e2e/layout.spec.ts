import { expect, test } from "@playwright/test";
import { readFileSync, writeFileSync } from "node:fs";
import { signIn } from "./helpers";
import { open, uiPages } from "./ui-pages";
// @ts-expect-error plain JS module shared with .cursor/skills/verify-ui
import { measure } from "./ui-measure.mjs";

// Layout and target size (ENFORCEMENT.md C2, C3) at 390, 768 and 1280:
// no page scrolls sideways. Three counts per page may only fall, never rise
// (ui-baseline.json; UPDATE_UI_BASELINE=1 rewrites it after a page gets
// better): selects whose chosen label is cut off (PR #51), controls under
// 24x24 at any width (WCAG 2.5.8), and controls under 44px tall at 390 (the
// design goal). Master still has some of each; PRs 5-7 bring them to 0.
const baselineFile = new URL("./ui-baseline.json", import.meta.url);
type Counts = { clipped: number; under24: number; under44: number };
type M = { overflow: number; clipped: { id: string; label: string; text: number; room: number }[]; under24: { el: string; name: string; w: number; h: number }[]; under44: unknown[] };

test("pages fit, selects show their label, targets are big enough", async ({ page, request }) => {
  test.setTimeout(240_000);
  const baseline: Record<string, Counts> = JSON.parse(readFileSync(baselineFile, "utf8"));
  const counts: Record<string, Counts> = {};
  const found: Record<string, string[]> = {};
  let who: string | null = null;
  for (const ui of await uiPages(request)) {
    if (ui.who !== who) {
      await page.context().clearCookies();
      if (ui.who) await signIn(page, ui.who);
      who = ui.who;
    }
    for (const width of [390, 768, 1280]) {
      await page.setViewportSize({ width, height: 900 });
      await open(page, ui);
      const m = (await page.evaluate(measure)) as M;
      const at = `${ui.id} at ${width}px`;
      expect.soft(m.overflow, `${at}: page scrolls sideways`).toBeLessThanOrEqual(0);
      const c = (counts[ui.id] ??= { clipped: 0, under24: 0, under44: 0 });
      c.clipped = Math.max(c.clipped, m.clipped.length);
      c.under24 = Math.max(c.under24, m.under24.length);
      if (width === 390) c.under44 = m.under44.length;
      (found[ui.id] ??= []).push(
        ...m.clipped.map((x) => `${width}px clipped select #${x.id} "${x.label}" needs ${x.text}px, has ${x.room}px`),
        ...m.under24.map((t) => `${width}px under 24x24: ${t.el} "${t.name}" ${t.w}x${t.h}`),
      );
    }
  }
  if (process.env.UPDATE_UI_BASELINE) {
    writeFileSync(baselineFile, JSON.stringify(counts, null, 2) + "\n");
    return;
  }
  for (const [id, n] of Object.entries(counts)) {
    const b = baseline[id] ?? { clipped: 0, under24: 0, under44: 0 };
    const detail = [...new Set(found[id])].join("\n  ");
    expect.soft(n.clipped, `${id}: more clipped selects than ui-baseline.json (${b.clipped}); widen the field or shorten the option\n  ${detail}`).toBeLessThanOrEqual(b.clipped);
    expect.soft(n.under24, `${id}: more targets under 24x24 than ui-baseline.json (${b.under24}); use the shared Button/Chip sizes (ENFORCEMENT.md A)\n  ${detail}`).toBeLessThanOrEqual(b.under24);
    expect.soft(n.under44, `${id} at 390px: more controls under 44px tall than ui-baseline.json (${b.under44}); use the shared Button/Field sizes`).toBeLessThanOrEqual(b.under44);
  }
});
