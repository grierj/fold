import { expect, test } from "@playwright/test";
import { signIn } from "./helpers";

// A leader sees admins labeled and gets no Remove for them (DEC-38).
test("leader roster labels the admin and offers no Remove for them", async ({ page }) => {
  await signIn(page, "lee");
  await page.goto("/groups/1");
  await page.getByRole("button", { name: /^members$/i }).click();

  const grier = page.getByRole("listitem").filter({ hasText: "Grier" });
  await expect(grier.getByText("Admin", { exact: true })).toBeVisible();
  await expect(grier.getByRole("button", { name: "Remove", exact: true })).toHaveCount(0);
  await expect(grier.getByRole("button", { name: "Remove as leader" })).toHaveCount(0);

  const sam = page.getByRole("listitem").filter({ hasText: "Sam Member" });
  await expect(sam.getByText("Admin", { exact: true })).toHaveCount(0);
  await expect(sam.getByRole("button", { name: "Remove", exact: true })).toBeVisible();
});
