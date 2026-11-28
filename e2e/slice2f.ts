import { expect, type Browser, type Page } from "@playwright/test";
import { gate, watchCSP } from "./gates";

// Slice 2, milestone 4a: the calendar, Home's Coming up, and Add to calendar
// and Share. Runs in slice 2's session after 3a: Pat is an admin, and the
// public church-wide Open house is on the first Saturday a week out.

function day(weekday: number): string {
  const now = new Date();
  const d = new Date(Date.UTC(now.getFullYear(), now.getMonth(), now.getDate() + 7));
  while (d.getUTCDay() !== weekday) d.setUTCDate(d.getUTCDate() + 1);
  return d.toISOString().slice(0, 10);
}

export async function milestone4a(page: Page, browser: Browser) {
  const saturday = day(6);

  await page.goto("/");
  await expect(page.getByRole("heading", { name: "Coming up for you" })).toBeVisible();
  await gate(page, "home coming up");

  await page.getByRole("link", { name: "See calendar" }).click();
  await expect(page.getByRole("heading", { name: "Calendar", level: 1 })).toBeVisible();
  await expect(page.getByRole("link", { name: "Just mine" })).toHaveAttribute("aria-current", "true");
  await gate(page, "calendar just mine");
  await page.getByRole("link", { name: "Everything" }).click();
  await expect(page.getByRole("link", { name: /Open house/ }).first()).toBeVisible();
  await gate(page, "calendar agenda");
  await page.getByRole("link", { name: "Show 30 more days" }).click();
  await expect(page).toHaveURL(/days=60/);

  await page.goto(`/calendar?view=week&date=${saturday}`);
  await expect(page.getByRole("link", { name: /Open house/ })).toBeVisible();
  await gate(page, "calendar week");
  await page.getByRole("link", { name: "Next week" }).click();
  await gate(page, "calendar next week");

  await page.goto(`/calendar?view=month&date=${saturday}`);
  await expect(page.locator("#picked").getByRole("link", { name: /Open house/ })).toBeVisible();
  await gate(page, "calendar month");
  // On a phone, the month's days are buttons and the picked day is listed below.
  await page.setViewportSize({ width: 390, height: 900 });
  const friday = new Date(`${saturday}T12:00:00Z`);
  friday.setUTCDate(friday.getUTCDate() - 1);
  await page.locator(`.cal-daybtn[href*="date=${friday.toISOString().slice(0, 10)}&"]`).click();
  await expect(page.locator("#picked")).not.toContainText("Open house");
  await page.locator(`.cal-daybtn[href*="date=${saturday}&"]`).click();
  await expect(page.locator("#picked").getByRole("link", { name: /Open house/ })).toBeVisible();
  await expect(page.locator(".cal-daybtn[aria-current=true]")).toHaveCount(1);
  await gate(page, "calendar month picked day");

  // The date page: Add to calendar downloads the one date; Share offers its link.
  await page.locator("#picked").getByRole("link", { name: /Open house/ }).click();
  const add = page.getByRole("link", { name: "Add to calendar" });
  await expect(add).toBeVisible();
  const ics = await page.request.get((await add.getAttribute("href"))!);
  expect(ics.headers()["content-type"]).toBe("text/calendar; charset=utf-8");
  expect(await ics.text()).toContain("SUMMARY:Open house");
  await expect(page.getByLabel("Link to this date")).toHaveValue(page.url().split("?")[0]);
  await expect(page.locator("#share-copy")).toBeVisible();
  await gate(page, "date add to calendar and share");

  // Signed out: the church's front page, and its public events.
  const visitor = await (await browser.newContext()).newPage();
  await watchCSP(visitor);
  await visitor.goto("/");
  await expect(visitor.getByRole("heading", { name: /^Welcome to / })).toBeVisible();
  await expect(visitor.getByRole("link", { name: /Open house/ })).toBeVisible();
  await gate(visitor, "home signed out with events");
  await visitor.getByRole("link", { name: "Browse public events" }).click();
  await expect(visitor.getByRole("link", { name: "Just mine" })).toHaveCount(0);
  await gate(visitor, "calendar signed out");
  await visitor.goto(`/calendar?view=month&date=${saturday}`);
  await gate(visitor, "calendar month signed out");
}
