import { expect, type Page } from "@playwright/test";
import { gate } from "./gates";

// Slice 2, milestone 2a: answers. Runs in slice 2's session after 1b: Pat
// co-leads the Tuesday Bible Study, whose "Thursday prayer" carries a video
// link.

export async function milestone2a(page: Page) {
  await page.goto("/groups");
  await page.getByRole("link", { name: "Tuesday Bible Study" }).click();
  await page.getByRole("link", { name: "Thursday prayer" }).first().click();
  await expect(page.getByRole("heading", { name: "Are you going?" })).toBeVisible();
  await expect(page.getByText("New dates added later won't be answered for you.")).toBeVisible();
  await gate(page, "date answer");

  // One date, swapped in place, with the line that stays (DEC-92).
  await page.getByRole("button", { name: "Going", exact: true }).click();
  await expect(page.getByText("You're going. See you there!")).toBeVisible();
  await expect(page.getByText("1 going")).toBeVisible();
  await expect(page.getByRole("link", { name: "Join the video call" })).toHaveAttribute("href", "https://meet.example.org/thursday-prayer");
  await page.locator("#answer summary", { hasText: "Show who's coming" }).click();
  await gate(page, "date answered");

  // Can't go for every date to come asks first.
  await page.getByLabel("All dates still to come").check();
  await page.getByRole("button", { name: "Can't go" }).click();
  await expect(page.getByText(/Say you can't go to \d+ dates\?/)).toBeVisible();
  await gate(page, "answer can't go confirm");
  await page.getByRole("button", { name: /^Yes, I can't go to/ }).click();
  await expect(page.getByText("Got it. Only the people running the event see this.")).toBeVisible();
  await expect(page.getByText(/1 can't go/)).toBeVisible();

  // Leaving asks first.
  await page.goto("/groups");
  await page.getByRole("link", { name: "Tuesday Bible Study" }).click();
  await expect(page.getByText("You can't go").first()).toBeVisible();
  await gate(page, "group with my answers");
  await page.locator("summary", { hasText: "More" }).click();
  await page.getByRole("button", { name: "Leave the group" }).click();
  await gate(page, "group leave dialog");
  await page.getByRole("dialog").getByRole("button", { name: "Stay" }).click();
}
