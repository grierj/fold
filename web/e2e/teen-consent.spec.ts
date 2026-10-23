import { expect, test, type Page } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";
import { apiAs, fromMore, PASSWORD, signIn } from "./helpers";
// @ts-expect-error shared browser measurement is plain JavaScript
import { measure } from "./ui-measure.mjs";

async function shoot(page: Page, name: string) {
  if (!process.env.SCREENS_DIR) return;
  for (const width of [390, 768, 1280]) {
    await page.setViewportSize({ width, height: 900 });
    const result = await page.evaluate(measure);
    expect(result.overflow, `${name}@${width} overflow`).toBeLessThanOrEqual(1);
    expect(result.cutOff, `${name}@${width} cut off`).toEqual([]);
    expect(result.clipped, `${name}@${width} clipped`).toEqual([]);
    expect(result.under24, `${name}@${width} small targets`).toEqual([]);
    const { violations } = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa", "wcag22aa"]).analyze();
    expect(violations.filter((item) => item.impact === "serious" || item.impact === "critical"), `${name}@${width} axe`).toEqual([]);
    await page.screenshot({ path: `${process.env.SCREENS_DIR}/${name}-${width}.png`, fullPage: true });
  }
  await page.setViewportSize({ width: 1280, height: 900 });
}

const birthDate = `${new Date().getUTCFullYear() - 15}-01-01`;

test("admin scoped consent activates a waiting session, revokes it, and keeps history", async ({ page, request, browser }) => {
  test.setTimeout(process.env.SCREENS_DIR ? 90_000 : 30_000);
  const admin = await apiAs(request, "grier");
  const { user } = await admin("POST", "/admin/users", { name: "Consent Casey", email: "consent-casey@example.com", password: PASSWORD, ageBand: "teen", birthDate });
  const teenContext = await browser.newContext();
  const teenPage = await teenContext.newPage();
  try {
    await signIn(teenPage, "consent-casey");
    await expect(teenPage.getByRole("heading", { name: "Waiting for consent" })).toBeVisible();
    await signIn(page, "grier");
    await page.goto("/admin?tab=users");
    await fromMore(page, "Manage consent", "More for Consent Casey");
    const panel = page.locator('[aria-label="Consent for Consent Casey"]');
    await panel.getByRole("button", { name: "Grant consent" }).click();
    await expect(panel.getByLabel("Consent evidence")).toBeFocused();
    await shoot(page, "consent-evidence-required");
    await panel.getByRole("radio", { name: "SMS", exact: true }).click();
    await panel.getByLabel("Consent evidence").fill("Parent signed SMS form at church office");
    await panel.getByRole("button", { name: "Grant consent" }).click();
    await expect(panel.getByRole("status")).toHaveText("SMS consent granted.");
    await teenPage.getByRole("button", { name: "Check account status" }).click();
    await expect(teenPage.getByRole("heading", { name: "Waiting for consent" })).toBeVisible();
    await panel.getByRole("radio", { name: "Account access" }).click();
    await panel.getByLabel("Consent evidence").fill("Parent signed account form at church office");
    await panel.getByRole("button", { name: "Grant consent" }).click();
    await expect(panel.getByRole("status")).toHaveText("Account access consent granted.");
    await teenPage.getByRole("button", { name: "Check account status" }).click();
    await expect(teenPage.getByRole("heading", { name: "Waiting for consent" })).toHaveCount(0);
    expect((await teenPage.request.get("/api/v1/groups")).status()).toBe(200);
    await panel.getByText("Consent history (2)").click();
    await expect(panel.getByText("Parent signed account form at church office")).toBeVisible();
    await shoot(page, "consent-granted-history");
    page.once("dialog", (dialog) => dialog.accept());
    await panel.getByRole("button", { name: "Revoke consent" }).click();
    await expect(panel.getByRole("status")).toHaveText("Account access consent revoked.");
    expect((await teenPage.request.get("/api/v1/auth/me")).status()).toBe(401);
    const { users } = await admin("GET", "/admin/users");
    expect(users.find((row: { id: number }) => row.id === user.id).status).toBe("disabled");
    await shoot(page, "consent-revoked");
    await page.reload();
    await fromMore(page, "Manage consent", "More for Consent Casey");
    await panel.getByText("Consent history (3)").click();
    await expect(panel.getByText("Parent signed SMS form at church office")).toBeVisible();
  } finally { await teenContext.close(); }
});

test("admin approves a teen request and records account consent together", async ({ page }) => {
  test.setTimeout(process.env.SCREENS_DIR ? 90_000 : 30_000);
  await page.goto("/join");
  await page.getByLabel("13 to 17", { exact: true }).click();
  await page.getByLabel("Birth date").fill(birthDate);
  await page.getByLabel("Full name").fill("Approval Jordan");
  await page.getByLabel("Email", { exact: true }).fill("consent-jordan@example.com");
  await page.getByRole("button", { name: "Request an account" }).click();
  await expect(page.getByText(/check your email/i)).toBeVisible();
  await signIn(page, "grier");
  await page.goto("/admin");
  const card = page.getByText("Approval Jordan", { exact: true }).locator("xpath=ancestor::div[contains(@class,'rounded-lg')][1]");
  await card.getByRole("checkbox", { name: /Grant account consent with approval/ }).check();
  await card.getByRole("button", { name: "Approve", exact: true }).click();
  await expect(card.getByLabel("Consent evidence")).toBeFocused();
  await card.getByLabel("Consent evidence").fill("Signed parent form received today");
  await shoot(page, "consent-with-approval");
  await card.getByRole("button", { name: "Approve", exact: true }).click();
  await expect(card).toHaveCount(0);
  await page.goto("/admin?tab=users");
  await fromMore(page, "Manage consent", "More for Approval Jordan");
  const panel = page.locator('[aria-label="Consent for Approval Jordan"]');
  await expect(panel.getByText("They still need to set a password before they can sign in.")).toBeVisible();
  await panel.getByText("Consent history (1)").click();
  await expect(panel.getByText("Signed parent form received today")).toBeVisible();
});
