import { expect, test } from "@playwright/test";
import { readFileSync, writeFileSync } from "node:fs";
import { signIn } from "./helpers";
import { open, partSelected, UI_PARTS, uiPages, uiPart } from "./ui-pages";
// @ts-expect-error plain JS module shared with .cursor/skills/verify-ui
import { measure } from "./ui-measure.mjs";

// Layout and target size (ENFORCEMENT.md C2, C3) at 390, 768 and 1280.
// Hard gates (PR 5 cleared the last ones): no page scrolls sideways, no
// control is cut off by an overflow box or the screen edge, no
// select's chosen label is cut off (PR #51, the event visibility select),
// and no control is under 24x24 (WCAG 2.5.8; a wrapping or `for` label
// counts), no radio or checkbox row is under 44px tall (DEC-65), and at 390 the page's last content clears the tab bar. Controls under 44px tall at 390 are the design goal: their kinds
// per page may only fall (ui-baseline.json; UPDATE_UI_BASELINE=1 rewrites it
// after a page gets better). Kinds are element and height, so specs that add
// rows to the shared seed don't move the numbers.
const baselineFile = new URL("./ui-baseline.json", import.meta.url);
type T = { el: string; name: string; w: number; h: number };
type M = { overflow: number; cutOff: { el: string; name: string }[]; clipped: { id: string; label: string; text: number; room: number }[]; under24: T[]; under44: T[] };
const kinds = (ts: T[]) => new Set(ts.map((t) => `${t.el}@${t.h}`)).size;

// The page list is checked in contiguous parts (keeping each person's pages
// together) so e2e/run.sh can spread them across shards. Every page is
// still checked once at every width.
test.describe.configure({ mode: "parallel" });
for (let part = 0; part < UI_PARTS; part++) {
  if (partSelected("layout", part)) test(`pages fit, selects show their label, targets are big enough (part ${part + 1} of ${UI_PARTS})`, async ({ page, request }) => {
    test.setTimeout(240_000);
    const baseline: Record<string, number> = JSON.parse(readFileSync(baselineFile, "utf8"));
    const under44: Record<string, number> = {};
    let who: string | null = null;
    for (const ui of uiPart(await uiPages(request), part)) {
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
        expect.soft(m.clipped.map((c) => `#${c.id} "${c.label}" needs ${c.text}px, has ${c.room}px`), `${at}: clipped select or time zone picker; widen the field on phones or shorten the option`).toEqual([]);
        // Radio and checkbox rows are hard-gated at 44px tall (DEC-65): the
        // whole row is the label.
        expect.soft(m.under44.filter((t) => /^input\[(radio|checkbox)\]$/.test(t.el)).map((t) => `${t.el} "${t.name}" ${t.w}x${t.h}`), `${at}: radio or checkbox rows under 44px; use <Radio>/<Checkbox> from components/ui/Field`).toEqual([]);
        expect.soft(m.under24.map((t) => `${t.el} "${t.name}" ${t.w}x${t.h}`), `${at}: targets under 24x24; use components/ui Button sizes, min-h-11 on text actions, or wrap the radio in its label`).toEqual([]);
        // The sticky header sits flush at the top once scrolled, and
        // scroll-padding-top covers it (a focused field never lands under it)
        // without leaving a big gap (PR #66 review F3).
        const head = await page.evaluate(() => {
          const h = document.querySelector("body header") as HTMLElement | null;
          if (!h) return null;
          window.scrollTo(0, 200);
          const r = h.getBoundingClientRect();
          const pad = parseFloat(getComputedStyle(document.documentElement).scrollPaddingTop) || 0;
          window.scrollTo(0, 0);
          return { top: Math.round(r.top), height: Math.round(r.height), pad: Math.round(pad), scrolls: document.documentElement.scrollHeight > innerHeight };
        });
        if (head?.scrolls) {
          expect.soft(head.top, `${at}: the sticky header is ${head.top}px from the top once scrolled`).toBe(0);
          expect.soft(head.pad - head.height, `${at}: scroll-padding-top ${head.pad}px vs header ${head.height}px; keep it at the header height plus up to 16px`).toBeGreaterThanOrEqual(0);
          expect.soft(head.pad - head.height, `${at}: scroll-padding-top ${head.pad}px vs header ${head.height}px; keep it at the header height plus up to 16px`).toBeLessThanOrEqual(16);
        }
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
      // Each part rewrites only its own pages. Run with E2E_SHARDS=1 so the
      // parts update the file one after another.
      const current: Record<string, number> = JSON.parse(readFileSync(baselineFile, "utf8"));
      writeFileSync(baselineFile, JSON.stringify({ ...current, ...under44 }, null, 2) + "\n");
      return;
    }
    for (const [id, n] of Object.entries(under44)) {
      expect.soft(n, `${id} at 390px: more kinds of control under 44px tall than ui-baseline.json (${baseline[id] ?? 0}); use the shared Button/Field sizes`).toBeLessThanOrEqual(baseline[id] ?? 0);
    }
  });
}
