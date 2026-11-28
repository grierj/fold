import { expect, type Page } from "@playwright/test";
import { gate } from "./gates";

// Slice 2, milestone 2b: seats, the waitlist and telling the people signed
// up. Runs in slice 2's session after 2a: Pat, an admin, co-leads the
// Tuesday Bible Study; Zed, a member in no group, is signed in in his own
// browser; one Thursday prayer date is open to everyone.

function day(weekday: number): string {
  const now = new Date();
  const d = new Date(Date.UTC(now.getFullYear(), now.getMonth(), now.getDate() + 7));
  while (d.getUTCDay() !== weekday) d.setUTCDate(d.getUTCDate() + 1);
  return d.toISOString().slice(0, 10);
}

export async function milestone2b(page: Page, zed: Page, opened: string) {
  // A church-wide class with one seat (DEC-95).
  await page.goto("/admin/events");
  await page.getByRole("link", { name: "Schedule a church-wide event" }).first().click();
  await page.getByLabel("Title").fill("Membership class");
  await page.getByLabel("Date", { exact: true }).fill(day(5));
  await page.getByLabel("Starts at").fill("18:00");
  await page.locator("summary", { hasText: "More options" }).click();
  await page.getByLabel("Seats (optional)").fill("1");
  await gate(page, "event form with seats");
  await page.getByRole("button", { name: "Schedule it" }).click();
  await expect(page.getByText("Your event is scheduled.")).toBeVisible();
  await page.getByRole("link", { name: /^Fri, / }).first().click();
  const date = page.url().split("?")[0];
  await page.getByRole("button", { name: "Going", exact: true }).click();
  await expect(page.getByText("You're going. See you there!")).toBeVisible();

  // Full: Zed joins the waitlist and sees his place.
  await zed.goto(date);
  await expect(zed.getByText("The one seat is taken.")).toBeVisible();
  await zed.getByRole("button", { name: "Join the waitlist" }).click();
  await expect(zed.getByText("You're on the waitlist, number 1.")).toBeVisible();
  await expect(zed.getByText("1 going · 1 on the waitlist")).toBeVisible();
  await gate(zed, "date full with the waitlist line");

  // Pat takes himself off; Zed moves up and is told (DEC-84).
  await page.reload();
  await page.locator("#answer summary", { hasText: "Show who's coming" }).click();
  await expect(page.getByText("On the waitlist, in order")).toBeVisible();
  await gate(page, "date who's coming with remove");
  await page.getByRole("button", { name: "Remove Pat Admin" }).click();
  await expect(page.getByRole("dialog")).toBeVisible();
  await gate(page, "date remove confirmation");
  await page.getByRole("dialog").getByRole("button", { name: "Remove" }).click();
  await expect(page.getByText("Pat Admin is off this date.")).toBeVisible();
  await zed.goto("/notices");
  await expect(zed.getByText(/A seat opened, so you're going to “Membership class”/)).toBeVisible();

  // Deleting it says how many will be told, and tells Zed (DEC-175).
  await page.goto(date);
  await page.getByRole("link", { name: "About this event and its other dates" }).click();
  await page.locator("summary", { hasText: "More" }).click();
  await page.getByRole("button", { name: "Delete the event" }).click();
  await expect(page.getByRole("dialog").getByText("1 other person signed up will be told.")).toBeVisible();
  await gate(page, "event delete confirmation with count");
  await page.getByRole("dialog").getByRole("button", { name: "Delete" }).click();
  await zed.goto("/notices");
  await expect(zed.getByText(/“Membership class” was deleted/)).toBeVisible();

  // R3: closing the opened date again removes nobody, and lists Zed, who
  // answered it, with Remove beside him.
  await zed.goto(opened);
  await zed.getByRole("button", { name: "Going", exact: true }).click();
  await expect(zed.getByText("You're going. See you there!")).toBeVisible();
  await page.goto(opened);
  await page.locator("summary", { hasText: "More" }).click();
  await page.getByRole("link", { name: "Who can see this date" }).click();
  await page.getByLabel("The same as the event").check();
  await page.getByRole("button", { name: "Save" }).click();
  await expect(page.getByText("Nobody is removed.")).toBeVisible();
  await expect(page.getByText("Zed Zone", { exact: true })).toBeVisible();
  await gate(page, "date more private list");
  await page.getByRole("button", { name: "Remove Zed Zone" }).click();
  await page.getByRole("dialog").getByRole("button", { name: "Remove" }).click();
  await expect(page.getByText("Zed Zone is off this date.")).toBeVisible();
  await page.getByRole("button", { name: "Make it Invite only" }).click();
  await expect(page.getByText("Saved. This date is back to who can see the rest of the event.")).toBeVisible();
  await gate(page, "date closed again");
}
