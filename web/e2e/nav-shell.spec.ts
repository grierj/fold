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
