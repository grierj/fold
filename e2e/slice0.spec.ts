import { expect, test, type Page } from "@playwright/test";
import { execFileSync } from "node:child_process";
import { cspViolations, gate, watchCSP } from "./gates";

// Slice 0 from a fresh install to a signed-in admin, passing the gates on
// every page along the way (docs/PLAN.md, slice 0 checks).

const admin = "pat@cloverdale.example";

function setupCode(): string {
  return execFileSync("../bin/fold", ["setup-code"], { env: { ...process.env, DATA_DIR: process.env.E2E_DATA_DIR } }).toString().trim();
}

async function latestLink(page: Page, to: string): Promise<string> {
  await page.goto("/dev/outbox");
  const item = page.locator("li", { hasText: `To ${to}` }).first();
  const text = await item.locator("[data-outbox-text]").innerText();
  const m = text.match(/http:\/\/[^\s]+\/l\/[A-Za-z0-9_-]{43}/);
  expect(m, "a link in the newest email").not.toBeNull();
  return m![0];
}

test.describe.configure({ mode: "serial" });

test("setup, sign in and out, with every page through the gates", async ({ page, context, browser }) => {
  // A second browser stands in for a mail scanner or another device: it sees
  // an emailed link's button page, while the browser that asked for the link
  // is signed in by opening it (docs/DECISIONS.md R26).
  const elsewhere = await (await browser.newContext()).newPage();
  await watchCSP(elsewhere);
  await watchCSP(page);

  await page.goto("/");
  await expect(page).toHaveURL(/\/setup$/);
  await gate(page, "setup code");

  await page.getByLabel("Setup code").fill("AAAA-BBBB-CCCC-DDDD");
  await page.getByRole("button", { name: "Continue" }).click();
  await expect(page.getByText("That code does not match")).toBeVisible();
  await gate(page, "setup code wrong");

  await page.getByLabel("Setup code").fill(setupCode());
  await page.getByRole("button", { name: "Continue" }).click();
  await expect(page.getByRole("heading", { name: "Set up your site" })).toBeVisible();
  await gate(page, "setup form");

  await page.getByRole("button", { name: "Send the test message" }).click();
  await expect(page.getByText("Check these, then try again")).toBeVisible();
  await gate(page, "setup form errors");

  await page.getByLabel("Church name").fill("Cloverdale Church of God");
  await page.getByLabel("Time zone").fill("America/Boise");
  await page.getByLabel("Your name").fill("Pat Admin");
  await page.getByLabel("Your email").fill(admin);
  await page.getByLabel("I am 18 or older").check();
  await page.getByLabel("Development outbox").check();
  await page.getByLabel("Send email from").fill("office@cloverdale.example");
  await page.getByRole("button", { name: "Send the test message" }).click();
  await expect(page.getByRole("heading", { name: "Check your email" })).toBeVisible();
  // The status updates itself (htmx polling) once the job has sent it.
  await expect(page.getByText("We sent the test message")).toBeVisible({ timeout: 15_000 });
  await gate(page, "setup sent");

  const setupLink = await latestLink(page, admin);
  await gate(page, "dev outbox");
  await elsewhere.goto(setupLink);
  await expect(elsewhere.getByRole("heading", { name: "Finish setting up" })).toBeVisible();
  await gate(elsewhere, "setup link");
  await page.goto(setupLink);
  await expect(page.getByRole("heading", { name: /^Good (morning|afternoon|evening), Pat Admin$/ })).toBeVisible();
  await expect(page.getByText("You're not in any groups yet")).toBeVisible();
  await gate(page, "home signed in");

  // The gallery: every control, and a fragment update under the strict policy.
  await page.goto("/admin/gallery");
  await gate(page, "gallery");
  const toggle = page.locator("#g-toggle");
  await toggle.getByRole("button", { name: "Remind me" }).click();
  await expect(toggle.getByRole("status")).toHaveText(/Saved/);
  await expect(toggle.getByRole("button", { name: "Remind me" })).toHaveAttribute("aria-pressed", "true");
  await page.getByRole("button", { name: "More" }).click();
  await expect(page.getByRole("link", { name: "Edit" })).toBeVisible();
  await page.keyboard.press("Escape");
  await page.getByRole("button", { name: "Delete this" }).click();
  await expect(page.getByRole("dialog", { name: "Delete this group?" })).toBeVisible();
  await page.getByRole("button", { name: "Keep it" }).click();
  await expect(page.getByRole("dialog")).toBeHidden();
  expect(await cspViolations(page)).toEqual([]);

  await page.goto("/no-such-page");
  await gate(page, "not found");

  await page.goto("/");
  await page.getByRole("button", { name: "Sign out" }).click();
  await expect(page.getByText("You're signed out.")).toBeVisible();
  await gate(page, "signed out");

  await page.goto("/");
  await gate(page, "home signed out");

  await page.goto("/signin");
  await page.getByLabel("Email").fill("not an address");
  await page.getByRole("button", { name: "Email me a sign-in link" }).click();
  await gate(page, "sign in error");
  await page.getByLabel("Email").fill(admin);
  await page.getByRole("button", { name: "Email me a sign-in link" }).click();
  await expect(page.getByRole("heading", { name: "Check your email" })).toBeVisible();
  await gate(page, "sign in sent");

  await expect.poll(async () => (await latestLink(page, admin)) !== setupLink, { timeout: 10_000 }).toBe(true);
  const signin = await latestLink(page, admin);
  await elsewhere.goto(signin);
  await gate(elsewhere, "sign in link");
  await page.goto(signin);
  await expect(page.getByRole("heading", { name: /^Good (morning|afternoon|evening), Pat Admin$/ })).toBeVisible();

  await page.goto(signin);
  await expect(page.getByText("This link no longer works")).toBeVisible();
  await gate(page, "link used");

  // A form left open across a sign-out says nothing was sent.
  await page.goto("/signin");
  await context.clearCookies();
  await page.getByLabel("Email").fill(admin);
  await page.getByRole("button", { name: "Email me a sign-in link" }).click();
  await expect(page.getByRole("heading", { name: "That page was out of date" })).toBeVisible();
  await gate(page, "stale form");
});
