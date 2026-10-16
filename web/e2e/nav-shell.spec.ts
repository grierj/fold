import { expect, test } from "@playwright/test";
import { signIn, signOut } from "./helpers";

// The nav shell (UI makeover PR 5): tabs on phones, a top bar on desktop,
// the church name in the header, and a tab bar that never covers a field.
test("phones get a tab bar that reaches every main page, and it never covers a focused field", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 700 });
  await signIn(page, "sam");
  const tabs = page.getByRole("navigation", { name: "Tabs" });
  await expect(tabs).toBeVisible();
  await expect(page.getByRole("link", { name: "powered by Fold" }).or(page.getByText("powered by Fold")).first()).toBeVisible();

  await tabs.getByRole("link", { name: "My groups" }).click();
  await expect(page).toHaveURL(/\/my-groups$/);
  await tabs.getByRole("link", { name: /notification/i }).click();
  await expect(page).toHaveURL(/\/notifications$/);
  await tabs.getByRole("link", { name: "Me", exact: true }).click();
  await expect(page.getByRole("heading", { name: "Your profile" })).toBeVisible();

  // Every tab is a full 44px target.
  for (const link of await tabs.getByRole("link").all()) {
    const box = (await link.boundingBox())!;
    expect(box.height).toBeGreaterThanOrEqual(44);
  }

  // Focus each text field on the profile page: none ends up under the bar.
  const barTop = (await tabs.boundingBox())!.y;
  for (const field of await page.locator("main input:not([type=hidden]):not([type=file]), main select, main textarea").all()) {
    if (!(await field.isVisible())) continue;
    await field.focus();
    const box = (await field.boundingBox())!;
    expect(box.y + box.height, `${await field.getAttribute("id")} sits under the tab bar`).toBeLessThanOrEqual(barTop);
  }

  // Log out lives on the Me page on phones.
  await page.getByRole("button", { name: "Log out" }).click();
  await expect(page.getByRole("link", { name: "Log in" })).toBeVisible();
  await expect(tabs).toBeHidden();
});

test("desktop keeps a top bar and no tab bar", async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 900 });
  await signIn(page, "sam");
  await expect(page.getByRole("navigation", { name: "Tabs" })).toBeHidden();
  const main = page.getByRole("navigation", { name: "Main" });
  await expect(main.getByRole("link", { name: "My groups" })).toBeVisible();
  await expect(main.getByRole("link", { name: "Start a group" })).toBeVisible();
  await signOut(page);
});

// PR #65 review F2/F3: phone header words, Admin on the Me page, and no
// Disable or Delete on your own row (the server refuses both anyway).
test("an admin on a phone reaches Admin from Me and can't disable or delete themselves", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 800 });
  await signIn(page, "grier");
  await expect(page.getByRole("banner").getByRole("link", { name: "New group" })).toBeVisible();
  await expect(page.getByRole("banner").getByRole("link", { name: "Admin" })).toBeHidden();
  await page.getByRole("navigation", { name: "Tabs" }).getByRole("link", { name: "Me", exact: true }).click();
  await page.getByRole("main").getByRole("link", { name: "Admin" }).click();
  await page.getByRole("tab", { name: "Users" }).or(page.getByRole("button", { name: "Users" })).first().click();
  const own = page.getByRole("row").filter({ hasText: "grier@" });
  await expect(own.getByRole("button", { name: "Edit" })).toBeVisible();
  await expect(own.getByRole("button", { name: "Disable" })).toHaveCount(0);
  await expect(own.getByRole("button", { name: "Delete" })).toHaveCount(0);
  const other = page.getByRole("row").filter({ hasText: "sam@" }).first();
  await other.scrollIntoViewIfNeeded();
  for (const name of ["Edit", "Password", "Disable", "Delete"]) await expect(other.getByRole("button", { name })).toBeInViewport({ ratio: 1 });
});
