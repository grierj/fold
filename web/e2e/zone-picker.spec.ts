import { expect, test } from "@playwright/test";
import { apiAs, signIn } from "./helpers";

// Grier's bug (Oct 14): with a value already chosen, the zone list showed
// only that value, hiding "Your time zone". The browser zone here is not in
// the common list, and every create and edit form must offer it first.
test.use({ locale: "en-US", timezoneId: "America/Indiana/Knox" });

function futureDate(days: number): string {
  return new Date(Date.now() + days * 24 * 60 * 60 * 1000).toISOString().slice(0, 10);
}

async function expectBrowserZoneFirst(page: import("@playwright/test").Page, selectId: string) {
  const sel = page.locator(`select#${selectId}`);
  await expect(sel).toHaveValue("America/Boise");
  await expect(sel.locator("option").first()).toHaveText("Knox, Indiana — Central Time (yours)");
  await expect(sel.locator("option").nth(1)).toHaveText("Boise — Mountain Time (church)");
  await expect(sel.locator('option[value="America/Boise"]')).toHaveCount(1);
}

test("church-wide create form lists the browser zone first", async ({ page }) => {
  await signIn(page, "grier");
  await page.goto("/events/new");
  await expectBrowserZoneFirst(page, "church-event-tz");
});

test("series edit form lists the browser zone first", async ({ page, request }) => {
  const admin = await apiAs(request, "grier");
  const { group } = (await admin("POST", "/groups", {
    name: "Zone club", description: "", tags: [], location: "", visibility: "public", memberLimit: null,
  })) as { group: { id: number } };
  const { event } = (await admin("POST", `/groups/${group.id}/events`, {
    title: "Zone night", startsLocal: `${futureDate(5)}T19:00`, durationMinutes: 60, tzid: "America/Boise",
    location: "", recurrenceRule: "FREQ=WEEKLY",
  })) as { event: { id: number } };
  await signIn(page, "grier");
  await page.goto(`/groups/${group.id}`);
  await page.getByRole("button", { name: "Edit series" }).first().click();
  const id = await page.getByLabel("Time zone").getAttribute("id");
  await expectBrowserZoneFirst(page, id!);
  await page.goto(`/groups/${group.id}/events/new`);
  await expectBrowserZoneFirst(page, "event-tz");
  await page.goto(`/events/${event.id}`);
  await page.getByRole("button", { name: "Edit series" }).click();
  await expectBrowserZoneFirst(page, `s-tz-ev${event.id}`);
  // Same shared editor as the group page: visible labels, same fields.
  for (const label of ["First date", "Start time", "Duration (minutes)", "Title", "Location", "Description", "Repeat until (optional)"]) {
    await expect(page.locator("label", { hasText: label }).first()).toBeVisible();
  }
});

test("admin church time zone setting lists the browser zone first", async ({ page }) => {
  await signIn(page, "grier");
  await page.goto("/admin");
  const sel = page.locator("select#church-tz");
  await expect(sel).toHaveValue("America/Boise");
  await expect(sel.locator("option").first()).toHaveText("Knox, Indiana — Central Time (yours)");
  await expect(sel.locator('option[value="America/Boise"]')).toHaveCount(1);
  await expect(sel.locator("option", { hasText: "church" })).toHaveCount(0);
});
