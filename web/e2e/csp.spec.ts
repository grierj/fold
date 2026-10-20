import { expect, test } from "@playwright/test";
import { signIn, watchCSP } from "./helpers";

// The strict CSP (DEC-37) must not block anything the SPA does.
test("no CSP violations across the main pages, and the check itself works", async ({ page }) => {
  const violations = await watchCSP(page);
  await page.goto("/setup"); // done already: redirects, still under the CSP
  await signIn(page, "grier");
  for (const path of ["/", "/groups/new", "/profile", "/admin", "/admin?tab=users", "/calendar", "/calendar?view=week", "/calendar?view=month", "/groups/1"]) {
    await page.goto(path);
    await page.waitForLoadState("networkidle");
  }
  await page.getByRole("button", { name: /^members$/i }).click();
  await expect(page.getByText("Sam Member")).toBeVisible();
  expect(violations).toEqual([]);

  // Self-check: an inline style attribute must be reported as a violation.
  await page.evaluate(() => document.body.setAttribute("style", "color: red"));
  await expect.poll(() => violations.length).toBeGreaterThan(0);
});
