import { expect, test } from "@playwright/test";
import { signIn } from "./helpers";

// M0.4 smoke test: sign in, see the groups page.
test("a member signs in and sees the groups page", async ({ page }) => {
  await signIn(page, "sam");
  await expect(page).toHaveURL("/");
  const card = page.getByRole("heading", { name: "Room 201 class" });
  await expect(card).toBeVisible();
  await card.click();
  await expect(page).toHaveURL(/\/groups\/\d+$/);
  await expect(page.getByRole("heading", { level: 1, name: "Room 201 class" })).toBeVisible();
});
