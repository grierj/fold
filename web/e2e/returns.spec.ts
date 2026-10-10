import { expect, test } from "@playwright/test";
import { signIn } from "./helpers";

// Re-enabled accounts waiting to return to a full group (DEC-38, DEC-39):
// a leader approves from the roster, an admin declines from Approvals.
test("leader approves and admin declines a return", async ({ page }) => {
  page.on("dialog", (d) => d.accept());

  await signIn(page, "lee");
  await page.goto("/groups/1");
  await expect(page.getByText("3 of 3 members")).toBeVisible();
  await page.getByRole("button", { name: /^members$/i }).click();
  const waiting = page.locator("#waiting");
  await expect(waiting.getByRole("listitem")).toHaveCount(2);
  await waiting.getByRole("listitem").filter({ hasText: "Pat Returner" }).getByRole("button", { name: "Approve" }).click();
  await expect(waiting.getByRole("listitem")).toHaveCount(1);
  await expect(page.getByRole("listitem").filter({ hasText: "Pat Returner" }).getByText("member", { exact: true })).toBeVisible();
  await expect(page.getByText("4 of 3 members")).toBeVisible();

  await page.context().clearCookies();
  await signIn(page, "grier");
  await page.goto("/admin?tab=approvals");
  const quinn = page.getByRole("listitem").filter({ hasText: "Quinn Returner" });
  await expect(quinn).toContainText("Room 201 class");
  await quinn.getByRole("button", { name: "Decline" }).click();
  await expect(page.getByText("Nothing pending. All caught up.")).toBeVisible();

  await page.goto("/groups/1");
  await page.getByRole("button", { name: /^members$/i }).click();
  await expect(page.locator("#waiting")).toHaveCount(0);
  await expect(page.getByText("Quinn Returner")).toHaveCount(0);
});
