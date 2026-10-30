import { expect, test } from "@playwright/test";
import { signIn, signOut } from "./helpers";

// M1.3 in the browser: /join with the age gate, the login page's request
// link, invite-a-friend wording, the admin queue and self-signup
// setting, inline approval from the bell, and the set-password page.
// Requests made here land in the shared seed database, so each test
// uses its own email address, and the admin's bell is left with nothing
// waiting or unread for the specs that count them.

// Marks the admin's notifications read through the API, leaving the
// bell clean for the specs that count waiting and unread items.
async function markAllRead(page: import("@playwright/test").Page) {
  await page.evaluate(async () => {
    const csrf = (await (await fetch("/api/v1/auth/csrf")).json()).csrfToken as string;
    await fetch("/api/v1/notifications/read-all", {
      method: "POST",
      headers: { "X-CSRF-Token": csrf },
    });
  });
}

test("join page: age gate, self wording, and a stored request", async ({ page }) => {
  await page.goto("/");
  const joinLink = page.locator("header").getByRole("link", { name: "Join", exact: true });
  await expect(joinLink).toBeVisible();
  await joinLink.click();
  await expect(page.getByRole("heading", { name: /Join/ })).toBeVisible();
  await expect(page.getByText("This is the address you will sign in with.")).toBeVisible();

  // Under 13: the household explanation replaces the submit button.
  await page.getByLabel("Under 13").click();
  await expect(page.getByText(/accounts start at 13/i)).toBeVisible();
  await expect(page.getByRole("button", { name: "Request an account" })).toHaveCount(0);

  // 13 to 17 needs a birth date.
  await page.getByLabel("13 to 17").click();
  await expect(page.getByLabel("Birth date")).toBeVisible();

  await page.getByLabel("18 or older").click();
  await page.getByLabel("Full name").fill("Browser Beth");
  await page.getByLabel("Email").fill("beth@example.com");
  await page.getByRole("button", { name: "Request an account" }).click();
  await expect(page.getByText(/check your email/i)).toBeVisible();
});

test("login page links to the request form", async ({ page }) => {
  await page.goto("/login");
  await page.getByRole("link", { name: "Request an account" }).click();
  await expect(page).toHaveURL(/\/join/);
  await expect(page.getByRole("button", { name: "Request an account" })).toBeVisible();
});

test("invite a friend speaks about them, then the admin queue decides", async ({ page }) => {
  await signIn(page, "sam");
  await page.goto("/profile/invite");
  await expect(page.getByRole("heading", { name: "Invite someone", level: 1 })).toBeVisible();
  await expect(page.getByText("This is the address they will sign in with.")).toBeVisible();
  await expect(page.getByText("Their age")).toBeVisible();
  await page.getByLabel("Full name").last().fill("Friend Fran");
  await page.getByLabel("Email").last().fill("fran-e2e@example.com");
  await page.getByRole("button", { name: "Ask an admin to invite them" }).click();
  await expect(page.getByText(/admin will review/i)).toBeVisible();
  await signOut(page);

  await signIn(page, "grier");
  await page.goto("/admin");
  await page.getByRole("tab", { name: "Settings" }).click();
  await expect(page.getByRole("tab", { name: "Settings" })).toHaveAttribute("aria-selected", "true");
  // Self-signup is two radio rows (DEC-65), On chosen.
  const mode = page.getByRole("group", { name: "Self-signup" });
  await expect(mode).toBeVisible();
  await expect(mode.getByRole("radio", { name: /^On/ })).toBeChecked();
  await page.getByRole("tab", { name: /^Approvals/ }).click();
  await expect(page.getByRole("tab", { name: /^Approvals/ })).toHaveAttribute("aria-selected", "true");
  await expect(page.getByText("Account requests")).toBeVisible();
  await expect(page.getByText("Friend Fran")).toBeVisible();
  await expect(page.getByText(/Invited by Sam/)).toBeVisible();
  const card = page.getByText("Friend Fran").locator("xpath=ancestor::div[contains(@class,'rounded-lg')][1]");
  await card.getByRole("button", { name: "Approve" }).click();
  await expect(page.getByText("Friend Fran")).toHaveCount(0);
  // Beth (first test) is still waiting; approve her too so later specs
  // that count waiting items start clean.
  const beth = page.getByText("Browser Beth").locator("xpath=ancestor::div[contains(@class,'rounded-lg')][1]");
  await beth.getByRole("button", { name: "Approve" }).click();
  await expect(page.getByText("Browser Beth")).toHaveCount(0);
  await markAllRead(page);
});

test("clean adult request approves inline from the bell", async ({ page }) => {
  await page.goto("/join");
  await page.getByLabel("Full name").fill("Clean Carl");
  await page.getByLabel("Email").fill("carl-e2e@example.com");
  await page.getByRole("button", { name: "Request an account" }).click();
  await expect(page.getByText(/check your email/i)).toBeVisible();

  await signIn(page, "grier");
  await page.getByRole("button", { name: /Alerts/ }).click();
  const row = page.locator("li", { hasText: "Clean Carl requested an account" });
  await expect(row).toBeVisible();
  await row.getByRole("button", { name: "Approve" }).click();
  await expect(row).toContainText(/You approved this|Already approved/);
  await page.getByRole("button", { name: /Alerts/ }).click(); // close the panel
  await markAllRead(page);
});

test("set-password page explains bad and missing links", async ({ page }) => {
  await page.goto("/set-password");
  await expect(page.getByText("This link is missing its token.")).toBeVisible();
  await page.goto("/set-password?token=bogus");
  await page.getByLabel("Password", { exact: true }).fill("longenoughpassword");
  await page.getByLabel("Confirm password").fill("longenoughpassword");
  await page.getByRole("button", { name: "Set password" }).click();
  await expect(page.getByText(/expired or was already used/i)).toBeVisible();
});
