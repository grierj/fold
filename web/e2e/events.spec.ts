import { expect, test } from "@playwright/test";
import { signIn } from "./helpers";

test.use({ locale: "en-US", timezoneId: "America/New_York" });

// This browser is America/New_York and the church is America/Boise. The
// form starts at the church zone (DEC-50); the browser's zone is the first
// suggestion. Saving keeps 7:00 pm in Boise; the list shows it as 9:00 pm,
// this browser's time, with no zone label (DEC-57).
test("a new series defaults to the church time zone", async ({ page }) => {
  await signIn(page, "grier");
  await page.goto("/");
  await page.getByRole("heading", { name: "Room 201 class" }).click();
  await page.getByRole("link", { name: "Schedule event" }).click();
  await expect(page).toHaveURL(/\/groups\/\d+\/events\/new/);
  const tz = page.getByRole("combobox", { name: "Time zone" });
  await expect(tz).toHaveValue("Boise (Mountain)");
  await tz.click();
  const first = page.getByRole("option").first();
  await expect(first).toContainText("New York (Eastern)");
  await expect(first).toContainText("Yours");
  await expect(page.getByRole("option", { name: /^Boise \(Mountain\)/ })).toHaveCount(1);
  await tz.press("Escape");

  const when = new Date(Date.now() + 20 * 24 * 60 * 60 * 1000);
  const ymd = when.toISOString().slice(0, 10);
  await page.getByLabel("Title (optional)").fill("Wednesday class");
  await page.getByLabel("Date").fill(ymd);
  await page.getByLabel("Start time").fill("19:00");
  await page.getByRole("button", { name: "Schedule event" }).click();

  await expect(page).toHaveURL(/\/groups\/\d+$/);
  await expect(page.getByText("Wednesday class")).toBeVisible();
  await expect(page.getByText(/^9:00 PM/).first()).toBeVisible();
  await expect(page.getByText(/MDT|MST/)).toHaveCount(0);
});
