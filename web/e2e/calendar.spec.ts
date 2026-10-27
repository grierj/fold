import AxeBuilder from "@axe-core/playwright";
import { expect, test, type Page } from "@playwright/test";
import { mkdirSync, writeFileSync } from "node:fs";
import { apiAs, signIn, signOut } from "./helpers";

// The calendar (M1.6): a smoke test per view, Just mine, invite-only
// privacy, and keyboard order.
const day = (n: number) => new Date(Date.now() + n * 864e5).toISOString().slice(0, 10);

async function proveView(page: Page, name: string) {
  for (const width of [390, 1280]) {
    await page.setViewportSize({ width, height: 900 });
    const { violations } = await new AxeBuilder({ page })
      .withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa", "wcag22aa"])
      .analyze();
    expect(violations.filter((v) => v.impact === "serious" || v.impact === "critical"), `${name}@${width} axe`).toEqual([]);
    if (width === 390) {
      const targets = await page.locator("main a[href], main button, main select").evaluateAll((controls) => controls.flatMap((control) => {
        const rect = control.getBoundingClientRect();
        if (rect.width === 0 || rect.height === 0 || getComputedStyle(control).visibility === "hidden") return [];
        return [{ name: control.getAttribute("aria-label") || control.textContent?.trim().replace(/\s+/g, " ").slice(0, 60) || control.tagName, width: Math.round(rect.width), height: Math.round(rect.height) }];
      }));
      expect(targets.filter((target) => target.width < 44 || target.height < 44), `${name}@390 calendar targets`).toEqual([]);
      if (process.env.SCREENS_DIR) {
        mkdirSync(process.env.SCREENS_DIR, { recursive: true });
        writeFileSync(`${process.env.SCREENS_DIR}/calendar-${name}-targets-390.json`, JSON.stringify(targets, null, 2));
      }
    }
    if (process.env.SCREENS_DIR) {
      await page.screenshot({ path: `${process.env.SCREENS_DIR}/calendar-${name}-${width}.png`, fullPage: true, animations: "disabled" });
    }
  }
}

test("the calendar shows what sam may see, in all three views", async ({ page, request }) => {
  const admin = await apiAs(request, "grier");
  const { group: secret } = (await admin("POST", "/groups", {
    name: "Calendar secret", description: "", tags: [], imageUrl: "", location: "", visibility: "invite_only", memberLimit: null,
  })) as { group: { id: number } };
  await admin("POST", `/groups/${secret.id}/events`, {
    title: "Secret supper", startsLocal: `${day(2)}T18:00`, durationMinutes: 60, tzid: "America/Boise", location: "", recurrenceRule: "",
  });
  async function churchEvent(title: string, hour: string, extra: Record<string, unknown> = {}) {
    const { event } = (await admin("POST", "/events", {
      title, startsLocal: `${day(2)}T${hour}:00`, durationMinutes: 60, tzid: "America/Boise", location: "Park",
      recurrenceRule: "", visibility: "public", ...extra,
    })) as { event: { id: number } };
    return event;
  }
  const picnic = await churchEvent("Calendar picnic", "12");
  const maybe = await churchEvent("Calendar maybe", "13");
  const declined = await churchEvent("Calendar declined", "14");
  await churchEvent("Calendar unanswered", "15");
  const waitlist = await churchEvent("Calendar waitlist", "16", { capacity: 1, waitlistEnabled: true });
  const sam = await apiAs(request, "sam");
  await sam("PUT", `/events/${picnic.id}/registrations`, { status: "going", date: day(2) });
  await sam("PUT", `/events/${maybe.id}/registrations`, { status: "maybe", date: day(2) });
  await sam("PUT", `/events/${declined.id}/registrations`, { status: "declined", date: day(2) });
  const lee = await apiAs(request, "lee");
  await lee("PUT", `/events/${waitlist.id}/registrations`, { status: "going", date: day(2) });
  const samAgain = await apiAs(request, "sam");
  await samAgain("PUT", `/events/${waitlist.id}/registrations`, { status: "going", date: day(2) });

  await signIn(page, "sam");
  await page.goto("/calendar");
  await expect(page.getByRole("heading", { name: "Calendar", level: 1 })).toBeVisible();
  await expect(page.getByRole("link", { name: /Calendar picnic/ }).first()).toContainText("Going");
  await expect(page.getByRole("link", { name: /Calendar maybe/ }).first()).toContainText("Maybe");
  await expect(page.getByRole("link", { name: /Calendar declined/ }).first()).toContainText("Can't go");
  await expect(page.getByRole("link", { name: /Calendar unanswered/ }).first()).toContainText("Not answered");
  await expect(page.getByRole("link", { name: /Calendar waitlist/ }).first()).toContainText("Waitlisted");
  await expect(page.getByText("Secret supper")).toHaveCount(0);

  // Church-wide dates are standing own scope inside the 30-day window. Just
  // mine retains every state except Can't go.
  await page.getByRole("button", { name: "Just mine" }).click();
  await expect(page).toHaveURL(/mine=1/);
  await expect(page.getByRole("link", { name: /Calendar picnic/ }).first()).toContainText("Going");
  await expect(page.getByRole("link", { name: /Calendar maybe/ }).first()).toContainText("Maybe");
  await expect(page.getByRole("link", { name: /Calendar unanswered/ }).first()).toContainText("Not answered");
  await expect(page.getByRole("link", { name: /Calendar waitlist/ }).first()).toContainText("Waitlisted");
  await expect(page.getByRole("link", { name: /Calendar declined/ })).toHaveCount(0);
  await page.getByRole("button", { name: "Just mine" }).click();
  await proveView(page, "agenda");

  await page.getByRole("link", { name: "Week", exact: true }).click();
  await expect(page).toHaveURL(/view=week/);
  await expect(page.getByRole("heading", { level: 2 }).filter({ hasText: /\d{4}/ })).toBeVisible();
  await page.getByRole("button", { name: "Next week" }).click();
  await page.getByRole("button", { name: "Previous week" }).click();
  await proveView(page, "week");
  await page.getByRole("link", { name: "Month", exact: true }).click();
  await expect(page.getByRole("table")).toBeVisible();
  await expect(page.getByText("Secret supper")).toHaveCount(0);
  const phoneGrid = page.getByRole("table");
  await expect(phoneGrid.locator(".bg-primary").first()).toBeAttached();
  await expect(phoneGrid.locator(".bg-transparent").first()).toBeAttached();
  await proveView(page, "month");

  // Keyboard: the view switch, then period nav, then the chip and picker.
  await page.getByRole("link", { name: "Agenda", exact: true }).focus();
  const order: string[] = [];
  for (let i = 0; i < 7; i++) {
    await page.keyboard.press("Tab");
    order.push(await page.evaluate(() => document.activeElement?.getAttribute("aria-label") || document.activeElement?.textContent?.trim() || ""));
  }
  expect(order.slice(0, 6)).toEqual(["Week", "Month", "Previous month", "Next month", "Today", "Just mine"]);
  await signOut(page);
});

test("signed out, the calendar shows public events and says so", async ({ page }) => {
  await page.context().clearCookies();
  await page.goto("/calendar");
  await expect(page.getByText("Showing public events.")).toBeVisible();
  await expect(page.getByRole("button", { name: "Just mine" })).toHaveCount(0);
});
