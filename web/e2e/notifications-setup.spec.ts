import { expect, test, type Locator } from "@playwright/test";
import { apiAs, signIn } from "./helpers";

// The directions page (M3.13c2, research 2.7): public, this device's
// section first, and gone when the church turns notifications off
// (DEC-124). The a11y and layout passes cover it through ui-pages.ts.

const IPHONE = "Mozilla/5.0 (iPhone; CPU iPhone OS 17_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Mobile/15E148 Safari/604.1";
const ANDROID = "Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Mobile Safari/537.36";

async function shot(target: Locator, name: string) {
  await target.screenshot({ path: test.info().outputPath(`setup-${name}-390.png`) });
}

test("an iPhone gets the iPhone steps first, an Android phone the Android ones", async ({ browser }) => {
  const baseURL = test.info().project.use.baseURL;
  for (const [ua, heading, name] of [[IPHONE, "Get notifications on your iPhone or iPad", "iphone"], [ANDROID, "Get notifications on your Android phone", "android"]]) {
    const context = await browser.newContext({ baseURL, userAgent: ua, viewport: { width: 390, height: 844 } });
    const page = await context.newPage();
    await page.goto("/notifications/setup");
    await expect(page.getByRole("heading", { name: "Get notifications on your phone", level: 1 })).toBeVisible();
    await expect(page.getByRole("heading", { level: 2 }).first()).toHaveText(heading);
    await expect(page.getByRole("region", { name: "Using a different device?" }).getByRole("heading", { level: 3 })).toHaveCount(3);
    await shot(page.getByRole("region", { name: heading }), name);
    await context.close();
  }
});

// Headless Chromium reports notifications as denied, which is the blocked
// guidance at the top of the page.
test("a blocking browser gets its undo steps at the top", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto("/notifications/setup");
  const top = page.getByText("This device is blocking notifications from Fold e2e. To allow them again:");
  await expect(top).toBeVisible();
  await page.locator("main").screenshot({ path: test.info().outputPath("setup-blocked-390-full.png") });
  await shot(top.locator(".."), "blocked");
});

test("the page and the card are gone while the church has notifications off", async ({ page, request }) => {
  const admin = await apiAs(request, "grier");
  await admin("PUT", "/admin/settings/push-enabled", { on: false });
  try {
    await page.goto("/notifications/setup");
    await expect(page.getByRole("heading", { name: "Page not found", level: 1 })).toBeVisible();
    await signIn(page, "sam");
    await page.goto("/profile/notifications");
    await expect(page.getByRole("checkbox", { name: /^Group updates/ })).toBeVisible();
    await expect(page.getByRole("region", { name: "Notifications on this device" })).toHaveCount(0);
  } finally {
    await admin("PUT", "/admin/settings/push-enabled", { on: true });
  }
  await page.goto("/notifications/setup");
  await expect(page.getByRole("heading", { name: "Get notifications on your phone", level: 1 })).toBeVisible();
});

test("an admin turns notifications on devices off and on again", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await signIn(page, "grier");
  await page.goto("/admin?tab=settings");
  const box = page.getByRole("checkbox", { name: /^Notifications on devices/ });
  await expect(box).toBeChecked();
  await shot(box.locator("xpath=ancestor::div[contains(@class,'max-w-xl')][1]"), "admin-switch");
  await box.uncheck();
  await expect(page.getByRole("status").filter({ hasText: "Saved" })).toBeVisible();
  await page.reload();
  await expect(box).not.toBeChecked();
  await box.check();
  await expect(page.getByRole("status").filter({ hasText: "Saved" })).toBeVisible();
});
