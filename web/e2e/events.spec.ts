import { expect, test } from "@playwright/test";
import { signIn } from "./helpers";

test.use({ locale: "en-US", timezoneId: "America/New_York" });

// This browser is America/New_York. The form starts there, not at UTC or
// the church zone. Picking America/Boise still keeps 7:00 pm and labels the zone.
test("a new series defaults to the browser time zone", async ({ page }) => {
  await signIn(page, "grier");
  await page.goto("/");
  await page.getByRole("heading", { name: "Room 201 class" }).click();
  await page.getByRole("link", { name: "Schedule event" }).click();
  await expect(page).toHaveURL(/\/groups\/\d+\/events\/new/);
  await expect(page.getByLabel("Time zone")).toHaveValue("America/New_York");

  const when = new Date(Date.now() + 20 * 24 * 60 * 60 * 1000);
  const ymd = when.toISOString().slice(0, 10);
  await page.getByLabel("Title (optional)").fill("Wednesday class");
  await page.getByLabel("Date").fill(ymd);
  await page.getByLabel("Start time").fill("19:00");
  await page.getByLabel("Time zone").fill("America/Boise");
  await page.getByRole("button", { name: "Schedule event" }).click();

  await expect(page).toHaveURL(/\/groups\/\d+$/);
  await expect(page.getByText("Wednesday class")).toBeVisible();
  await expect(page.getByText(/7:00 PM (MDT|MST)/)).toBeVisible();
});
