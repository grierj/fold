import { expect, test } from "@playwright/test";
import { signIn } from "./helpers";

// Deep links load the SPA and its assets from any path (base "/").
test("deep links load directly, signed out and signed in", async ({ page }) => {
  await page.goto("/groups/1");
  await expect(page.getByRole("heading", { level: 1, name: "Room 201 class" })).toBeVisible();

  await signIn(page, "grier");
  await page.goto("/admin?tab=users");
  await expect(page.getByText("Pat Returner")).toBeVisible();
  await page.reload();
  await expect(page.getByText("Pat Returner")).toBeVisible();
});
