import AxeBuilder from "@axe-core/playwright";
import { expect, test, type Page } from "@playwright/test";
import { signIn, signOut } from "./helpers";
import { open, uiPages, type UIPage } from "./ui-pages";

// axe on the main pages at phone and desktop widths (UI makeover PR 3).
// Serious and critical issues fail; minor and moderate ones are listed in
// the test output so they can be fixed as pages are redone.
async function scan(page: Page, name: string) {
  for (const width of [390, 1280]) {
    await page.setViewportSize({ width, height: 900 });
    const { violations } = await new AxeBuilder({ page })
      .withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa", "wcag22aa"])
      .analyze();
    const bad = violations.filter((v) => v.impact === "serious" || v.impact === "critical");
    for (const v of violations.filter((v) => !bad.includes(v))) {
      console.log(`axe ${v.impact} ${name}@${width}: ${v.id} (${v.nodes.length})`);
    }
    expect.soft(
      bad.map((v) => `${v.impact} ${v.id}: ${v.help} — ${v.nodes.map((n) => n.target.join(" ")).join(", ")}`),
      `${name} at ${width}px`,
    ).toEqual([]);
  }
}

test("main pages have no serious or critical axe issues", async ({ page, request }) => {
  test.setTimeout(240_000);
  // The shared page list (ui-pages.ts, also used by layout.spec.ts) plus the
  // pages only this check covers.
  const extra: UIPage[] = [
    { id: "forgot-password", who: null, path: "/forgot-password", ready: (p) => p.getByRole("heading", { name: "Reset your password" }) },
    { id: "notifications", who: "grier", path: "/notifications", ready: (p) => p.getByRole("heading", { name: "Alerts" }) },
    { id: "admin groups", who: "grier", path: "/admin?tab=groups", ready: (p) => p.getByRole("heading", { name: "Admin" }) },
    { id: "admin invite", who: "grier", path: "/admin?tab=invite", ready: (p) => p.getByRole("heading", { name: "Admin" }) },
  ];
  let who: string | null = null;
  for (const ui of [...(await uiPages(request)), ...extra]) {
    if (ui.who !== who) {
      await page.context().clearCookies();
      if (ui.who) await signIn(page, ui.who);
      who = ui.who;
    }
    await open(page, ui);
    await scan(page, ui.id);
  }
  await signOut(page);
});

test("the time zone picker has no serious or critical axe issues when open and filtered", async ({ page }) => {
  await signIn(page, "grier");
  await page.goto("/events/new");
  const box = page.getByRole("combobox", { name: "Time zone" });
  await box.click();
  await box.fill("pac");
  await expect(page.getByRole("listbox")).toBeVisible();
  await scan(page, "zone picker open");
  await signOut(page);
});
