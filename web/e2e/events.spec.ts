import { expect, test } from "@playwright/test";
import { signIn } from "./helpers";

test.use({ locale: "en-US", timezoneId: "America/New_York" });

// The church zone is America/Boise (seed). A 7:00 pm event stays 7:00 pm
// on the page even though this browser is in New York, and the zone is labeled.
test("a leader schedules a 7:00 pm event in the church zone", async ({ page }) => {
  await signIn(page, "grier");
  await page.goto("/");
  await page.getByRole("heading", { name: "Room 201 class" }).click();
  await page.getByRole("link", { name: "Schedule event" }).click();
  await expect(page).toHaveURL(/\/groups\/\d+\/events\/new/);
  await expect(page.getByLabel("Time zone")).toHaveValue("America/Boise");

  const when = new Date(Date.now() + 20 * 24 * 60 * 60 * 1000);
  const ymd = when.toISOString().slice(0, 10);
  await page.getByLabel("Title (optional)").fill("Wednesday class");
  await page.getByLabel("Date").fill(ymd);
  await page.getByLabel("Start time").fill("19:00");
  await page.getByRole("button", { name: "Schedule event" }).click();

  await expect(page).toHaveURL(/\/groups\/\d+$/);
  await expect(page.getByText("Wednesday class")).toBeVisible();
  await expect(page.getByText(/7:00 PM (MDT|MST)/)).toBeVisible();
});
