import { expect, test } from "@playwright/test";
import { signIn } from "./helpers";

// Re-enabled into a full group (DEC-47): Pat and Quinn were left out and
// told to ask a leader. There's no waiting list; a leader adds by email
// as an explicit over-limit choice.
test("left-out account is told to ask a leader, who adds them over the limit", async ({ page }) => {
  page.on("dialog", (d) => d.accept());

  await signIn(page, "pat");
  await page.getByRole("button", { name: "Alerts" }).click();
  await expect(page.getByText("You're not back in Room 201 class yet")).toBeVisible();
  await expect(page.getByText("Ask a leader to add you.")).toBeVisible();

  await page.context().clearCookies();
  await signIn(page, "lee");
  await page.goto("/groups/1");
  await expect(page.getByText("3 of 3 members")).toBeVisible();
  await page.getByRole("button", { name: /^members$/i }).click();
  await expect(page.locator("#waiting")).toHaveCount(0);
  await expect(page.getByText("Pat Returner")).toHaveCount(0);
  await page.getByLabel("Add a member by email").fill("pat@example.com");
  await page.getByRole("button", { name: "Add member", exact: true }).click();
  await expect(page.getByRole("listitem").filter({ hasText: "Pat Returner" })).toBeVisible();
  await expect(page.getByText("4 of 3 members")).toBeVisible();
});
