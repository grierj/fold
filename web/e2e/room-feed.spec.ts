import { expect, test } from "@playwright/test";
import { signIn } from "./helpers";

// M2.2b: an admin makes a calendar link for the Fellowship Hall from the
// Rooms tab, a calendar app can read it, and turning it off stops it.
test("an admin makes a room's calendar link, it serves ICS, and turning it off stops it", async ({ page, request }) => {
  await signIn(page, "grier");
  await page.goto("/admin?tab=rooms");
  await page.getByRole("button", { name: "More for Fellowship Hall" }).click();
  await page.getByRole("menuitem", { name: "Calendar feed for Fellowship Hall" }).click();
  const panel = page.getByRole("region", { name: "Calendar feed for Fellowship Hall" });
  // A link from an earlier run on this server may already be on; a new one works either way.
  const make = panel.getByRole("button", { name: "Create calendar link" });
  const remake = panel.getByRole("button", { name: "Make a new link" });
  await expect(make.or(remake)).toBeVisible();
  if (await remake.isVisible()) {
    page.once("dialog", (d) => void d.accept());
    await remake.click();
  } else {
    await make.click();
  }
  const link = panel.getByRole("textbox", { name: "Calendar link" });
  await expect(link).toHaveValue(/\/cal\/rooms\/\d+\/[A-Za-z0-9_-]+\.ics$/);
  const url = new URL(await link.inputValue());

  const feed = await request.get(url.pathname);
  expect(feed.status()).toBe(200);
  expect(feed.headers()["content-type"]).toContain("text/calendar");
  expect(await feed.text()).toContain("BEGIN:VCALENDAR");

  page.once("dialog", (d) => void d.accept());
  await panel.getByRole("button", { name: "Turn off calendar link" }).click();
  await expect(panel.getByRole("status").filter({ hasText: "Calendar link turned off." })).toBeVisible();
  expect((await request.get(url.pathname)).status()).toBe(404);

  await panel.getByRole("button", { name: "Done" }).click();
  await expect(page.getByRole("button", { name: "More for Fellowship Hall" })).toBeFocused();
});
