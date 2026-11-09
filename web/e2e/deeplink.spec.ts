import { expect, test } from "@playwright/test";
import { signIn } from "./helpers";

// Deep links load the SPA and its assets from any path (base "/").
test("deep links load directly, signed out and signed in", async ({ page }) => {
  await page.goto("/groups/1");
  await expect(page.getByRole("heading", { level: 1, name: "Room 201 class" })).toBeVisible();

  await signIn(page, "grier");
  // The Users list shows 25 people a page and the specs before this one in a shard add people,
  // so a seeded user can sit on page 2. Search for one, which filters the whole list.
  for (const load of [() => page.goto("/admin?tab=users"), () => page.reload()]) {
    await load();
    await expect(page.getByRole("tab", { name: /^Users/ })).toHaveAttribute("aria-selected", "true");
    await page.getByLabel("Search by name or email").fill("Pat Returner");
    await expect(page.getByText("Pat Returner")).toBeVisible();
  }
});
