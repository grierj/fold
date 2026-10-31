import { expect, test } from "@playwright/test";
import { signIn } from "./helpers";

// M3.1b: Admin → System lists background tasks that kept failing. A fresh
// server's daily tasks run cleanly, so an admin sees that nothing needs
// attention, and the tab carries no count. Retry and Dismiss are covered by
// Vitest (SystemTab.test.tsx) and the Go handler tests: e2e can't seed a dead
// job through the API.
test("an admin sees that background tasks are running normally", async ({ page }) => {
  await signIn(page, "grier");
  await page.goto("/admin");
  await page.getByRole("tab", { name: /^System\b/ }).click();
  await expect(page).toHaveURL(/tab=system/);
  await expect(page.getByRole("heading", { name: "Background tasks" })).toBeVisible();
  await expect(page.getByText("Nothing needs attention.")).toBeVisible();
  await expect(page.getByText("Background tasks are running normally.")).toBeVisible();
});
