import { expect, type Browser, type Page } from "@playwright/test";
import { gate, watchCSP } from "./gates";

// Slice 2, milestone 3a: a stranger RSVPs to a public date as a guest. Runs
// in slice 2's session after 2b: Pat is an admin.

function day(weekday: number): string {
  const now = new Date();
  const d = new Date(Date.UTC(now.getFullYear(), now.getMonth(), now.getDate() + 7));
  while (d.getUTCDay() !== weekday) d.setUTCDate(d.getUTCDate() + 1);
  return d.toISOString().slice(0, 10);
}

async function mailTo(page: Page, to: string, pattern: RegExp): Promise<string> {
  await page.goto("/dev/outbox");
  const text = await page.locator("li", { hasText: `To ${to}` }).first().locator("[data-outbox-text]").innerText();
  return text.match(pattern)![0];
}

export async function milestone3a(page: Page, browser: Browser) {
  // A public church-wide open house.
  await page.goto("/admin/events");
  await page.getByRole("link", { name: "Schedule a church-wide event" }).first().click();
  await page.getByLabel("Title").fill("Open house");
  await page.getByLabel("Date", { exact: true }).fill(day(6));
  await page.getByLabel("Starts at").fill("10:00");
  await page.locator("summary", { hasText: "More options" }).click();
  await page.getByLabel("Public").check();
  await page.getByRole("button", { name: "Schedule it" }).click();
  await expect(page.getByText("Your event is scheduled.")).toBeVisible();
  await page.getByRole("link", { name: /^Sat, / }).first().click();
  const date = page.url().split("?")[0];

  // A stranger finds it and asks to RSVP.
  const asker = await (await browser.newContext()).newPage();
  await watchCSP(asker);
  await asker.goto(date);
  await asker.getByRole("link", { name: "RSVP as a guest" }).click();
  await expect(asker.getByRole("heading", { name: "RSVP to Open house" })).toBeVisible();
  await gate(asker, "guest form");
  await asker.getByRole("button", { name: "Email me a link to confirm" }).click();
  await expect(asker.getByText("Check these, then try again")).toBeVisible();
  await gate(asker, "guest form errors");
  await asker.getByLabel("Your name").fill("Vera Visitor");
  await asker.getByLabel("Your email").fill("vera@example.org");
  await asker.getByLabel("I am 18 or older").check();
  await asker.getByRole("button", { name: "Email me a link to confirm" }).click();
  await expect(asker.getByRole("heading", { name: "Check your email" })).toBeVisible();
  await gate(asker, "guest check your email");

  // Opened in another browser, the link asks first (R26).
  let confirm = "";
  await expect.poll(async () => (confirm = await mailTo(page, "vera@example.org", /http:\/\/[^\s]+\/l\/[A-Za-z0-9_-]{43}/)).length, { timeout: 10_000 }).toBeGreaterThan(0);
  const elsewhere = await (await browser.newContext()).newPage();
  await watchCSP(elsewhere);
  await elsewhere.goto(confirm);
  await expect(elsewhere.getByRole("heading", { name: "Confirm your RSVP" })).toBeVisible();
  await gate(elsewhere, "guest confirm");
  await elsewhere.getByRole("button", { name: "Confirm my RSVP" }).click();
  await expect(elsewhere.getByText("You're going. We've emailed you a link")).toBeVisible();
  await expect(elsewhere.getByRole("button", { name: "Cancel my RSVP" })).toBeVisible();
  await gate(elsewhere, "date as a guest");
  await elsewhere.goto("/");
  await expect(elsewhere.getByRole("heading", { name: "Your RSVPs" })).toBeVisible();
  await gate(elsewhere, "guest home");

  // The manage link shows the date and a Cancel, and dies once used.
  let manage = "";
  await expect.poll(async () => (manage = await mailTo(page, "vera@example.org", /http:\/\/[^\s]+\/g\/[A-Za-z0-9_-]{43}/)).length, { timeout: 10_000 }).toBeGreaterThan(0);
  await asker.goto(manage);
  await expect(asker.getByText("You're going.")).toBeVisible();
  await gate(asker, "guest manage");
  await asker.getByRole("button", { name: "Cancel my RSVP" }).click();
  await expect(asker.getByRole("heading", { name: "Your RSVP is cancelled" })).toBeVisible();
  await gate(asker, "guest cancelled");
  await asker.goto(manage);
  await expect(asker.getByRole("heading", { name: "This link no longer works" })).toBeVisible();
  await gate(asker, "guest manage link gone");
}
