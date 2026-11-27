import { expect, test, type Page } from "@playwright/test";
import { gate, watchCSP } from "./gates";

// Slice 1 pages through the gates. Runs after slice0.spec.ts, on the same
// site: the church is set up and Pat is its first admin.

const admin = "pat@cloverdale.example";

async function latestLink(page: Page, to: string): Promise<string> {
  await page.goto("/dev/outbox");
  const text = await page.locator("li", { hasText: `To ${to}` }).first().locator("[data-outbox-text]").innerText();
  return text.match(/http:\/\/[^\s]+\/l\/[A-Za-z0-9_-]{43}/)![0];
}

async function signIn(page: Page, email: string) {
  await page.goto("/signin");
  await page.getByLabel("Email").fill(email);
  await page.getByRole("button", { name: "Email me a sign-in link" }).click();
  await expect(page.getByRole("heading", { name: "Check your email" })).toBeVisible();
  await expect.poll(async () => (await latestLink(page, email)).length, { timeout: 10_000 }).toBeGreaterThan(0);
  await page.goto(await latestLink(page, email));
}

test("account requests and notices, through the gates", async ({ page, browser }) => {
  await watchCSP(page);
  const visitor = await (await browser.newContext()).newPage();
  await watchCSP(visitor);

  await visitor.goto("/request");
  await gate(visitor, "request form");
  await visitor.getByRole("button", { name: "Ask for an account" }).click();
  await expect(visitor.getByText("Check these, then try again")).toBeVisible();
  await gate(visitor, "request form errors");
  await visitor.getByLabel("Your name").fill("Young Person");
  await visitor.getByLabel("Your email").fill("young@example.org");
  await visitor.getByLabel("I am under 18").check();
  await visitor.getByRole("button", { name: "Ask for an account" }).click();
  await expect(visitor.getByRole("heading", { name: "Accounts for under-18s aren't open yet" })).toBeVisible();
  await gate(visitor, "request under 18");

  await visitor.goto("/request");
  await visitor.getByLabel("Your name").fill("Sam Member");
  await visitor.getByLabel("Your email").fill("sam@example.org");
  await visitor.getByLabel("I am 18 or older").check();
  await visitor.getByRole("button", { name: "Ask for an account" }).click();
  await expect(visitor.getByRole("heading", { name: "Thanks for asking" })).toBeVisible();
  await gate(visitor, "request sent");

  await signIn(page, admin);
  await expect(page.getByRole("heading", { name: "Welcome, Pat Admin" })).toBeVisible();
  await expect(page.locator(".badge")).toHaveText(/1/);
  await page.goto("/notices");
  await expect(page.getByText("Sam Member asked for an account.")).toBeVisible();
  await gate(page, "notices");
  await page.getByRole("button", { name: /Sam Member asked for an account/ }).click();
  await expect(page.getByRole("heading", { name: "Account requests" })).toBeVisible();
  await gate(page, "admin requests");
  await page.getByRole("button", { name: "Approve" }).click();
  await expect(page.getByText("Approved. They've been sent a sign-in link.")).toBeVisible();
  await gate(page, "admin requests empty");
  await page.goto("/notices");
  await page.getByRole("button", { name: "Mark all as read" }).isHidden();
  await gate(page, "notices read");

  // The visitor's browser asked, so the approval link signs them in at once.
  await visitor.goto(await latestLink(page, "sam@example.org"));
  await expect(visitor.getByRole("heading", { name: "Welcome, Sam Member" })).toBeVisible();
  await visitor.goto("/notices");
  await gate(visitor, "notices empty");
});
