import { expect, test } from "@playwright/test";
import { gate, watchCSP } from "./gates";
import { signIn } from "./session";

// Slice 2, milestone 1a: events and dates through the gates. Runs after
// slice1.spec.ts on the same site: Pat, an admin, co-leads Sam's
// invite-only Tuesday Bible Study. The site runs on the real clock, so dates
// are counted from today.

const admin = "pat@cloverdale.example";

// day is the first date at least a week from today on the given weekday
// (0 is Sunday), as YYYY-MM-DD.
function day(weekday: number, from = new Date()): string {
  const d = new Date(Date.UTC(from.getFullYear(), from.getMonth(), from.getDate() + 7));
  while (d.getUTCDay() !== weekday) d.setUTCDate(d.getUTCDate() + 1);
  return d.toISOString().slice(0, 10);
}

// springForward is the second Sunday of next March, when Boise's clocks
// skip from 2:00 to 3:00 am.
function springForward(): string {
  const now = new Date();
  const d = new Date(Date.UTC(now.getFullYear() + 1, 2, 1));
  while (d.getUTCDay() !== 0) d.setUTCDate(d.getUTCDate() + 1);
  d.setUTCDate(d.getUTCDate() + 7);
  return d.toISOString().slice(0, 10);
}

test("scheduling, the event and date pages, and the series edit, through the gates", async ({ page }) => {
  await watchCSP(page);
  await signIn(page, admin);

  await page.goto("/groups");
  await page.getByRole("link", { name: "Tuesday Bible Study" }).click();
  await expect(page.getByRole("heading", { name: "Coming up" })).toBeVisible();
  await gate(page, "group nothing scheduled");
  await page.getByRole("link", { name: "Schedule an event" }).click();
  await expect(page.getByRole("heading", { name: "Schedule an event" })).toBeVisible();
  await gate(page, "event form");
  await page.getByRole("button", { name: "Schedule it" }).click();
  await expect(page.getByText("Check these, then try again")).toBeVisible();
  await gate(page, "event form errors");

  // A time the clocks skip is warned about once, under When (R34).
  await page.getByLabel("Title").fill("Early prayer");
  await page.getByLabel("Date", { exact: true }).fill(springForward());
  await page.getByLabel("Starts at").fill("02:30");
  await page.getByRole("button", { name: "Schedule it" }).click();
  await expect(page.getByText("The clocks change on a night this meets")).toBeVisible();
  await gate(page, "event form daylight saving");

  // A weekly study.
  await page.goto("/groups");
  await page.getByRole("link", { name: "Tuesday Bible Study" }).click();
  await page.getByRole("link", { name: "Schedule an event" }).click();
  await page.getByLabel("Title").fill("Tuesday study night");
  await page.getByLabel("Description").fill("We read a chapter a week.\nCoffee at 6:45.");
  await page.getByLabel("Date", { exact: true }).fill(day(2));
  await page.getByLabel("Starts at").fill("19:00");
  await page.getByLabel("Hours").fill("1");
  await page.getByLabel("Minutes").fill("30");
  await page.getByLabel("Every week").check();
  await expect(page.getByLabel("Last date (optional)")).toBeVisible();
  await expect(page.getByText("Which week of the month")).toBeHidden();
  await page.getByLabel("Where").fill("Room 4, by the library");
  await page.getByRole("button", { name: "Schedule it" }).click();
  await expect(page.getByText("Your event is scheduled.")).toBeVisible();
  await expect(page.getByText("Every Tuesday, 7:00 to 8:30 pm")).toBeVisible();
  await expect(page.getByRole("link", { name: "Open in maps" })).toHaveAttribute("href", /^https:\/\/www\.google\.com\/maps\/search\//);
  await gate(page, "event page");
  await page.locator("summary", { hasText: "More" }).click();
  await gate(page, "event page more");

  // The group page lists it, and a date opens.
  await page.getByRole("link", { name: "Tuesday Bible Study" }).click();
  await expect(page.getByRole("link", { name: "Tuesday study night" }).first()).toBeVisible();
  await gate(page, "group coming up");
  await page.getByRole("link", { name: "Tuesday study night" }).first().click();
  await expect(page.getByText("7:00 to 8:30 pm")).toBeVisible();
  await gate(page, "date page");

  // The series edit: a later time saves at once.
  await page.getByRole("link", { name: "About this event and its other dates" }).click();
  await page.locator("summary", { hasText: "More" }).click();
  await page.getByRole("link", { name: "Edit the event" }).click();
  await expect(page.getByText("Times are in Boise time (America/Boise).")).toBeVisible();
  await gate(page, "event edit");
  await page.getByLabel("Starts at").fill("19:30");
  await page.getByRole("button", { name: "Save" }).click();
  await expect(page.getByText("Saved.")).toBeVisible();
  await expect(page.getByText("Every Tuesday, 7:30 to 9:00 pm")).toBeVisible();

  // Moving it to Wednesdays cancels the Tuesdays, so it asks first.
  await page.locator("summary", { hasText: "More" }).click();
  await page.getByRole("link", { name: "Edit the event" }).click();
  await page.getByLabel("Date", { exact: true }).fill(day(3));
  await page.getByRole("button", { name: "Save" }).click();
  await expect(page.getByRole("heading", { name: /^Cancel \d+ dates\?$/ })).toBeVisible();
  await gate(page, "event drop dates");
  await page.getByRole("button", { name: /^Save and cancel/ }).click();
  await expect(page.getByText("Saved.")).toBeVisible();
  await expect(page.getByText("Every Wednesday, 7:30 to 9:00 pm")).toBeVisible();
  await page.getByRole("link", { name: "See all dates" }).click();
  await gate(page, "event all dates");

  // Delete it; the group page says so.
  await page.goto(page.url().replace("?all=1", ""));
  await page.locator("summary", { hasText: "More" }).click();
  await page.getByRole("button", { name: "Delete the event" }).click();
  await gate(page, "event delete dialog");
  await page.getByRole("dialog").getByRole("button", { name: "Delete" }).click();
  await expect(page.getByText("The event is deleted.")).toBeVisible();
});
