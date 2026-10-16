import { expect, test } from "@playwright/test";
import { readFileSync, writeFileSync } from "node:fs";
import { signIn } from "./helpers";
import { open, uiPages } from "./ui-pages";
// @ts-expect-error plain JS module shared with .cursor/skills/verify-ui
import { measure } from "./ui-measure.mjs";

// Layout and target size (ENFORCEMENT.md C2, C3) at 390, 768 and 1280.
// Hard gates (PR 5 cleared the last ones): no page scrolls sideways, no
// control is cut off by an overflow box or the screen edge, no
// select's chosen label is cut off (PR #51, the event visibility select),
// and no control is under 24x24 (WCAG 2.5.8; a wrapping or `for` label
// counts), and at 390 the page's last content clears the tab bar. Controls under 44px tall at 390 are the design goal: their kinds
// per page may only fall (ui-baseline.json; UPDATE_UI_BASELINE=1 rewrites it
// after a page gets better). Kinds are element and height, so specs that add
// rows to the shared seed don't move the numbers.
const baselineFile = new URL("./ui-baseline.json", import.meta.url);
type T = { el: string; name: string; w: number; h: number };
type M = { overflow: number; cutOff: { el: string; name: string }[]; clipped: { id: string; label: string; text: number; room: number }[]; under24: T[]; under44: T[] };
const kinds = (ts: T[]) => new Set(ts.map((t) => `${t.el}@${t.h}`)).size;

test("pages fit, selects show their label, targets are big enough", async ({ page, request }) => {
  test.setTimeout(240_000);
  const baseline: Record<string, number> = JSON.parse(readFileSync(baselineFile, "utf8"));
  const under44: Record<string, number> = {};
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
      expect.soft(m.cutOff.map((c) => `${c.el} "${c.name}"`), `${at}: controls cut off by an overflow box or the screen edge; stack them on phones (see AdminUsers cards) instead of hiding or scrolling them`).toEqual([]);
      expect.soft(m.clipped.map((c) => `#${c.id} "${c.label}" needs ${c.text}px, has ${c.room}px`), `${at}: clipped select; widen the field on phones or shorten the option`).toEqual([]);
      expect.soft(m.under24.map((t) => `${t.el} "${t.name}" ${t.w}x${t.h}`), `${at}: targets under 24x24; use components/ui Button sizes, min-h-11 on text actions, or wrap the radio in its label`).toEqual([]);
      if (width === 390) {
        under44[ui.id] = kinds(m.under44);
        // The last content clears the fixed tab bar once scrolled to the
        // bottom, on every page (PR #65 review N4).
        const covered = await page.evaluate(() => {
          const bar = document.querySelector('nav[aria-label="Tabs"]');
          if (!bar || bar.getBoundingClientRect().height === 0) return 0;
          window.scrollTo(0, document.documentElement.scrollHeight);
          const top = bar.getBoundingClientRect().top;
          let bottom = 0;
          for (const e of document.querySelectorAll("main *, footer *")) {
            const r = e.getBoundingClientRect();
            if (r.width > 0 && r.height > 0 && getComputedStyle(e).position !== "fixed") bottom = Math.max(bottom, r.bottom);
          }
          return Math.round(bottom - top);
        });
        expect.soft(covered, `${at}: the last content sits ${covered}px under the tab bar; pad main by the bar's height plus the safe area`).toBeLessThanOrEqual(1); // the bar's 1px top border may overlap
      }
    }
  }
  if (process.env.UPDATE_UI_BASELINE) {
    writeFileSync(baselineFile, JSON.stringify(under44, null, 2) + "\n");
    return;
  }
  for (const [id, n] of Object.entries(under44)) {
    expect.soft(n, `${id} at 390px: more kinds of control under 44px tall than ui-baseline.json (${baseline[id] ?? 0}); use the shared Button/Field sizes`).toBeLessThanOrEqual(baseline[id] ?? 0);
  }
});
