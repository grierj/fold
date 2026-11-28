import { expect, type Browser, type Page } from "@playwright/test";
import { gate, watchCSP } from "./gates";

// Slice 2, milestone 1b: one date by hand, opening wider, church-wide
// events and hosts, and each person's time zone. slice2.spec.ts runs it in
// its own session, since a fourth sign-in for Pat within the hour would meet
// the per-address limit: Pat, an admin, co-leads Sam's invite-only Tuesday
// Bible Study.

function day(weekday: number): string {
  const now = new Date();
  const d = new Date(Date.UTC(now.getFullYear(), now.getMonth(), now.getDate() + 7));
  while (d.getUTCDay() !== weekday) d.setUTCDate(d.getUTCDate() + 1);
  return d.toISOString().slice(0, 10);
}

// It hands back Zed's browser and the date it opened to everyone, for 2b.
export async function milestone1b(page: Page, browser: Browser): Promise<{ zed: Page; opened: string }> {
  // A weekly invite-only event, then one date of it.
  await page.goto("/groups");
  await page.getByRole("link", { name: "Tuesday Bible Study" }).click();
  await page.getByRole("link", { name: "Schedule an event" }).click();
  await page.getByLabel("Title").fill("Thursday prayer");
  await page.getByLabel("Date", { exact: true }).fill(day(4));
  await page.getByLabel("Starts at").fill("18:00");
  await page.getByLabel("Every week").check();
  await page.getByLabel("Where").fill("The chapel");
  await page.getByLabel("Video call link (optional)").fill("https://meet.example.org/thursday-prayer");
  await page.locator("summary", { hasText: "More options" }).click();
  await expect(page.getByLabel("Why open it wider than the group? (optional)")).toBeHidden();
  await page.getByLabel("Public").check();
  await expect(page.getByLabel("Why open it wider than the group? (optional)")).toBeVisible();
  await gate(page, "event form opening wider");
  await page.getByLabel("Invite only").check();
  await page.getByRole("button", { name: "Schedule it" }).click();
  await expect(page.getByText("Your event is scheduled.")).toBeVisible();

  await page.getByRole("link", { name: /^Thu, / }).first().click();
  await page.locator("summary", { hasText: "More" }).click();
  await gate(page, "date page more");
  await page.getByRole("button", { name: "Cancel this date" }).click();
  await gate(page, "date cancel dialog");
  await page.getByRole("dialog").getByRole("button", { name: "Cancel this date" }).click();
  await expect(page.getByText("The people running the event cancelled this date.")).toBeVisible();
  await gate(page, "date cancelled by hand");
  await page.locator("summary", { hasText: "More" }).click();
  await page.getByRole("button", { name: "Bring this date back" }).click();
  await page.getByRole("dialog").getByRole("button", { name: "Bring it back" }).click();
  await expect(page.getByText("This date is back on.")).toBeVisible();

  // Open it to everyone, with a reason; a stranger sees only that date.
  await page.locator("summary", { hasText: "More" }).click();
  await page.getByRole("link", { name: "Who can see this date" }).click();
  await gate(page, "date open form");
  await page.getByLabel("Public").check();
  await page.getByLabel("Why open it wider than the group? (optional)").fill("Anyone may come to this one.");
  await gate(page, "date open form with reason");
  await page.getByRole("button", { name: "Save" }).click();
  await expect(page.getByText("open wider than the rest of the event")).toBeVisible();
  const opened = page.url().split("?")[0];
  const stranger = await (await browser.newContext()).newPage();
  await watchCSP(stranger);
  await stranger.goto(opened);
  await expect(stranger.getByRole("heading", { name: "Thursday prayer" })).toBeVisible();
  await expect(stranger.getByText("Tuesday Bible Study")).toHaveCount(0);
  await expect(stranger.getByRole("link", { name: "About this event and its other dates" })).toHaveCount(0);
  await gate(stranger, "opened date to a stranger");

  // A church-wide event, and a host named by name.
  await page.goto("/admin/events");
  await gate(page, "admin church events empty");
  await page.getByRole("link", { name: "Schedule a church-wide event" }).first().click();
  await gate(page, "church-wide event form");
  await page.getByLabel("Title").fill("Wednesday family supper");
  await page.getByLabel("Date", { exact: true }).fill(day(3));
  await page.getByLabel("Starts at").fill("17:30");
  await page.getByLabel("Every week").check();
  await page.getByRole("button", { name: "Schedule it" }).click();
  await expect(page.getByText("Church-wide")).toBeVisible();
  await gate(page, "church-wide event page");
  await page.locator("summary", { hasText: "More" }).click();
  await page.getByRole("link", { name: "Hosts" }).click();
  await gate(page, "hosts empty");
  await page.getByLabel("Find someone by name").fill("Sam");
  await page.getByRole("button", { name: "Find" }).click();
  await gate(page, "hosts search");
  await page.getByRole("button", { name: "Make a host" }).click();
  await expect(page.getByText("They're now a host, and they've been told.")).toBeVisible();
  await gate(page, "hosts named");
  await page.goto("/admin/events");
  await gate(page, "admin church events");

  // Pat's profile: the zone saves when changed, and times follow it.
  await page.goto("/profile");
  await gate(page, "profile time zone");
  await page.getByLabel("Your time zone").selectOption("America/New_York");
  await expect(page.locator("#zone-form").getByRole("status")).toHaveText(/Saved/);
  await page.goto("/admin/events");
  await page.getByRole("link", { name: "Wednesday family supper" }).click();
  await expect(page.getByText("This event keeps Boise time. Times here are shown in New York time.")).toBeVisible();
  await expect(page.getByText(/7:30 to 8:30 pm/).first()).toBeVisible();
  await page.goto("/profile");
  await page.getByLabel("Your time zone").selectOption("America/Boise");
  await expect(page.locator("#zone-form").getByRole("status")).toHaveText(/Saved/);

  // Someone new, signing in from a browser in Chicago, gets Chicago.
  await page.goto("/admin/requests");
  await page.getByLabel("Their name").fill("Zed Zone");
  await page.getByLabel("Their email").fill("zed@example.org");
  await page.getByLabel("They are 18 or older").check();
  await page.getByRole("button", { name: "Send the invitation" }).click();
  await page.goto("/dev/outbox");
  const zedText = await page.locator("li", { hasText: "To zed@example.org" }).first().locator("[data-outbox-text]").innerText();
  const zedLink = zedText.match(/http:\/\/[^\s]+\/invite\/[A-Za-z0-9_-]{43}/)![0];
  const zed = await (await browser.newContext({ timezoneId: "America/Chicago" })).newPage();
  await watchCSP(zed);
  await zed.goto(zedLink);
  await zed.getByLabel("I am 18 or older").check();
  await zed.getByRole("button", { name: "Accept the invitation" }).click();
  await expect(zed.getByRole("heading", { name: /^Good (morning|afternoon|evening), / })).toBeVisible();
  await expect.poll(async () => {
    await zed.goto("/profile");
    return zed.getByLabel("Your time zone").inputValue();
  }, { timeout: 10_000 }).toBe("America/Chicago");
  return { zed, opened };
}
