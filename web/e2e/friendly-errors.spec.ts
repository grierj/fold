import { expect, test, type Page } from "@playwright/test";
import { email, signIn, signOut } from "./helpers";

// Friendly errors (UI makeover PR 4, DEC-61): the screen shows Fold's own
// words for the API's error code, never the server's text. With
// SCREENS_DIR set, each state is also captured at 390 and 1280 px.
async function shoot(page: Page, name: string) {
  const dir = process.env.SCREENS_DIR;
  if (!dir) return;
  for (const w of [390, 1280]) {
    await page.setViewportSize({ width: w, height: 900 });
    await page.evaluate(() => window.scrollTo(0, 0));
    await page.screenshot({ path: `${dir}/error-${name}-${w}.png`, fullPage: true, animations: "disabled", caret: "hide" });
  }
}

test("a server failure on home reads as plain words with a way to retry", async ({ page }) => {
  await page.route("**/api/v1/groups*", (route) =>
    route.fulfill({ status: 500, contentType: "application/json", body: JSON.stringify({ error: "boom", code: "server_error" }) }),
  );
  await page.goto("/");
  const alert = page.getByRole("alert");
  await expect(alert).toHaveText(/We couldn't load the groups\. Please try again in a minute\./);
  await expect(page.getByText("boom")).toHaveCount(0);
  await expect(alert.getByRole("button", { name: "Try again" })).toBeVisible();
  await shoot(page, "home-server");
});

test("a dropped connection says so", async ({ page }) => {
  await page.route("**/api/v1/groups*", (route) => route.abort("internetdisconnected"));
  await page.goto("/");
  await expect(page.getByRole("alert")).toHaveText(/couldn't reach Fold\. Check your connection/);
  await shoot(page, "home-offline");
});

test("a wrong password names no account and says what to do", async ({ page }) => {
  await page.goto("/login");
  await page.getByLabel("Email").fill("nobody-e2e@example.com");
  await page.getByLabel("Password").fill("not-the-password");
  await page.getByRole("button", { name: "Log in" }).click();
  await expect(page.getByText("That email and password don't match. Check them and try again.")).toBeVisible();
  await shoot(page, "login");
});

test("an expired reset link offers the next step", async ({ page }) => {
  await page.goto("/reset-password?token=not-a-real-token");
  await page.getByLabel("New password", { exact: true }).fill("password1234");
  await page.getByLabel(/Confirm/).fill("password1234");
  await page.getByRole("button", { name: /password/i }).click();
  await expect(page.getByText("This link has expired or was already used. Request a new one below.")).toBeVisible();
  await shoot(page, "reset-link");
});

test("inviting someone who already has an account says so (kept on purpose)", async ({ page }) => {
  await signIn(page, "grier");
  await page.goto("/admin?tab=invite");
  await page.getByLabel("Email").fill(email("sam"));
  await page.getByLabel("Name").fill("Sam Again");
  await page.getByRole("button", { name: "Send invitation" }).click();
  await expect(page.getByText("That email already has an account. They can sign in instead.")).toBeVisible();
  await shoot(page, "invite-existing");
  await signOut(page);
});
