import { expect, type Page } from "@playwright/test";
import { gate } from "./gates";

// Slice 2, milestone 4b: the personal calendar feed on the profile, and the
// group finder's day and time filters. Runs in slice 2's session after 4a.

export async function milestone4b(page: Page) {
  await page.goto("/profile");
  await expect(page.getByRole("heading", { name: "Calendar feed" })).toBeVisible();
  await gate(page, "profile feed off");
  await page.getByRole("button", { name: "Turn on the calendar feed" }).click();
  await expect(page.getByText("This link is shown only now")).toBeVisible();
  const first = await page.getByLabel("Your calendar's address").inputValue();
  expect(first).toMatch(/\/feed\/[A-Za-z0-9_-]{43}\/calendar\.ics$/);
  const webcal = first.replace(/^https?:/, "webcal:");
  await expect(page.getByRole("link", { name: "Subscribe on iPhone or Mac" })).toHaveAttribute("href", webcal);
  await expect(page.getByRole("link", { name: "Add to Google Calendar" })).toHaveAttribute("href", `https://calendar.google.com/calendar/r?cid=${encodeURIComponent(webcal)}`);
  await expect(page.getByRole("img", { name: "QR code for your calendar feed" })).toBeVisible();
  // Kept for checking the code scans (zbarimg), by hand or in CI.
  await page.getByRole("img", { name: "QR code for your calendar feed" }).screenshot({ path: "test-results/screens/feed-qr.png" });
  await gate(page, "profile feed made");
  const feed = await page.request.get(first);
  expect(feed.status()).toBe(200);
  expect(feed.headers()["content-type"]).toBe("text/calendar; charset=utf-8");
  expect(feed.headers()["set-cookie"]).toBeUndefined();
  expect(await feed.text()).toContain("BEGIN:VCALENDAR");

  await page.goto("/profile");
  await expect(page.getByText("Your calendar feed is on.")).toBeVisible();
  await expect(page.getByLabel("Your calendar's address")).toHaveCount(0);
  await gate(page, "profile feed on");
  await page.getByRole("button", { name: "Make a new link" }).click();
  await expect(page.getByRole("dialog")).toBeVisible();
  await gate(page, "profile feed new link dialog");
  await page.getByRole("dialog").getByRole("button", { name: "Make a new link" }).click();
  const second = await page.getByLabel("Your calendar's address").inputValue();
  expect(second).not.toBe(first);
  expect((await page.request.get(first)).status()).toBe(404);
  await page.getByRole("button", { name: "Turn off" }).click();
  await page.getByRole("dialog").getByRole("button", { name: "Turn off" }).click();
  await expect(page.getByText("Your calendar feed is off.")).toBeVisible();
  expect((await page.request.get(second)).status()).toBe(404);

  // From the calendar, the way to the feed.
  await page.goto("/calendar");
  await page.getByRole("link", { name: "Put your calendar on your phone" }).click();
  await expect(page).toHaveURL(/\/profile#feed$/);

  await page.goto("/groups");
  await gate(page, "groups by next meeting");
  await page.getByLabel("Meets on").selectOption("tue");
  await page.getByLabel("Time of day").selectOption("evening");
  await page.getByRole("button", { name: "Search" }).click();
  await expect(page).toHaveURL(/day=tue/);
  await gate(page, "groups filtered");
  await page.goto("/groups?q=nothing-matches-this&day=sun&time=morning");
  await expect(page.getByRole("heading", { name: "No groups match that search on those days and times" })).toBeVisible();
  await expect(page.getByRole("link", { name: "Clear filters" })).toHaveAttribute("href", "/groups?q=nothing-matches-this");
  await gate(page, "groups filtered none");
}
