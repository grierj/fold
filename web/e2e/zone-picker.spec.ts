import { expect, test, type Page } from "@playwright/test";
import { apiAs, signIn, fromMore } from "./helpers";

// Grier's bug (Oct 14): with a value already chosen, the zone list showed
// only that value, hiding "Your time zone". The browser zone here is not in
// the common list, and every create and edit form must offer it first. The
// picker is one searchable combobox everywhere but setup (DEC-65).
test.use({ locale: "en-US", timezoneId: "America/Indiana/Knox" });

function futureDate(days: number): string {
  return new Date(Date.now() + days * 24 * 60 * 60 * 1000).toISOString().slice(0, 10);
}

async function expectBrowserZoneFirst(page: Page, id: string, church = true) {
  const box = page.locator(`#${id}`);
  await expect(box).toHaveAttribute("role", "combobox");
  await expect(box).toHaveValue("Boise (Mountain)");
  await box.click();
  const options = page.getByRole("listbox").getByRole("option");
  await expect(options.first()).toContainText("Knox, Indiana (Central)");
  await expect(options.first()).toContainText("Yours");
  if (church) {
    await expect(options.nth(1)).toContainText("Boise (Mountain)");
    await expect(options.nth(1)).toContainText("Church");
  } else {
    await expect(page.getByRole("option", { name: /Church/ })).toHaveCount(0);
  }
  await expect(page.getByRole("option", { name: /^Boise \(Mountain\)/ })).toHaveCount(1);
  await box.press("Escape");
  await expect(page.getByRole("listbox")).toHaveCount(0);
}

test("church-wide create form lists the browser zone first", async ({ page }) => {
  await signIn(page, "grier");
  await page.goto("/events/new");
  await expectBrowserZoneFirst(page, "church-event-tz");
});

test("typing filters by region and the keyboard picks a zone", async ({ page }) => {
  await signIn(page, "grier");
  await page.goto("/events/new");
  const box = page.getByRole("combobox", { name: "Time zone" });
  await box.click();
  await box.fill("pac");
  await expect(page.getByRole("option").first()).toContainText("Los Angeles (Pacific)");
  await box.press("ArrowDown");
  await box.press("ArrowUp");
  await box.press("Enter");
  await expect(box).toHaveValue("Los Angeles (Pacific)");
  await expect(box).toHaveAttribute("title", /America\/Los_Angeles$/);
  // An offset finds zones too.
  await box.click();
  await box.fill("utc+5:30");
  await expect(page.getByRole("option").first()).toContainText("UTC+5:30");
  await box.press("Escape");
  await expect(box).toHaveValue("Los Angeles (Pacific)");
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
  await fromMore(page, "Edit series");
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
  await page.getByRole("button", { name: "Settings" }).click();
  await expect(page.getByRole("button", { name: "Settings" })).toHaveAttribute("aria-pressed", "true");
  await expectBrowserZoneFirst(page, "church-tz", false);
});
