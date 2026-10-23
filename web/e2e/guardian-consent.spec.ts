import AxeBuilder from "@axe-core/playwright";
import { expect, test, type Page } from "@playwright/test";
import { apiAs, email, PASSWORD, signIn } from "./helpers";

async function proof(page: Page, state: string) {
  for (const width of [390, 1280]) {
    await page.setViewportSize({ width, height: 900 });
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), `${state} overflow at ${width}`).toBe(true);
    const { violations } = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa", "wcag22aa"]).analyze();
    expect(violations.filter((v) => v.impact === "serious" || v.impact === "critical"), `${state} axe at ${width}`).toEqual([]);
    if (process.env.SCREENS_DIR) {
      await page.evaluate(() => window.scrollTo(0, 0));
      await page.screenshot({ path: `${process.env.SCREENS_DIR}/${state}-${width}.png`, fullPage: true });
      if (state === "guardian-confirmed" || state === "guardian-revoked") await page.getByRole("region", { name: "Teen consent", exact: true }).screenshot({ path: `${process.env.SCREENS_DIR}/${state}-controls-${width}.png` });
    }
  }
}

test("a confirmed guardian manages teen account and scope consent", async ({ page, request }) => {
  test.setTimeout(120_000);
  const admin = await apiAs(request, "grier");
  await admin("POST", "/admin/users", { name: "Guardian proof adult", email: email("guardian-proof"), password: PASSWORD, ageBand: "adult" });
  await admin("POST", "/admin/users", { name: "Guardian proof teen", email: email("guardian-proof-teen"), password: PASSWORD, ageBand: "teen", birthDate: "2011-01-01" });
  await signIn(page, "guardian-proof");
  await page.goto("/profile");
  const section = page.getByRole("region", { name: "Teen consent", exact: true });
  await section.getByLabel("Teen's email").fill(email("guardian-proof-teen"));
  await section.getByRole("button", { name: "Request guardian link", exact: true }).click();
  await expect(section.getByRole("status")).toHaveText("If that teen has an account, an admin will review your guardian request.");
  await expect(section.getByRole("heading", { name: "Guardian proof teen" })).toHaveCount(0);
  await signIn(page, "grier"); await page.goto("/admin");
  const card = page.locator("[data-link-card]").filter({ hasText: "Guardian proof teen" });
  await card.getByLabel("How did you check?").fill("Guardian came to the office with their teen");
  await card.getByRole("button", { name: "Confirm link", exact: true }).click();
  await expect(card).toHaveCount(0);
  await signIn(page, "guardian-proof"); await page.goto("/profile");
  await expect(section.getByRole("heading", { name: "Guardian proof teen" })).toBeVisible();
  await proof(page, "guardian-confirmed");
  await section.getByRole("button", { name: "Grant consent", exact: true }).click();
  await expect(section.getByText("Account consent granted.", { exact: true })).toBeVisible();
  for (const scope of ["sms", "photos"]) {
    await section.getByLabel("Consent", { exact: true }).selectOption(scope);
    await section.getByRole("button", { name: "Grant consent", exact: true }).click();
    await expect(section.getByText("Consent granted", { exact: true })).toBeVisible();
  }
  await section.getByLabel("Consent", { exact: true }).selectOption("account");
  await signIn(page, "guardian-proof-teen");
  await expect(page.getByRole("heading", { name: "Waiting for consent" })).toHaveCount(0);
  expect((await page.request.get("/api/v1/groups")).status()).toBe(200);
  await signIn(page, "guardian-proof"); await page.goto("/profile");
  page.once("dialog", (d) => d.accept());
  await section.getByRole("button", { name: "Revoke consent", exact: true }).click();
  await expect(section.getByText("Account consent revoked.", { exact: true })).toBeVisible();
  await proof(page, "guardian-revoked");
  const response = await page.request.get("/api/v1/me/guardian-teens");
  expect((await response.json()).teens).toEqual([expect.objectContaining({ accountConsent: false, smsConsent: true, photosConsent: true })]);
});

test("a consent email previews before its explicit one-use grant", async ({ page, request }) => {
  test.setTimeout(120_000);
  test.skip((await request.get("/dev/outbox")).status() !== 200, "Requires E2E_APP_ENV=dev for consent mail");
  const admin = await apiAs(request, "grier");
  await admin("POST", "/admin/users", { name: "Email guardian", email: email("email-guardian-proof"), password: PASSWORD, ageBand: "adult" });
  const { user: teen } = await admin("POST", "/admin/users", { name: "Email consent teen", email: email("email-teen-proof"), password: PASSWORD, ageBand: "teen", birthDate: "2011-01-01" });
  await signIn(page, "email-guardian-proof"); await page.goto("/profile");
  await page.getByLabel("Teen's email").fill(email("email-teen-proof"));
  await page.getByRole("button", { name: "Request guardian link", exact: true }).click();
  await expect(page.getByText("If that teen has an account, an admin will review your guardian request.")).toBeVisible();
  const { guardianLinks } = await admin("GET", "/admin/guardian-links");
  const link = guardianLinks.find((l: { teenUserId?: number }) => l.teenUserId === teen.id);
  expect(link).toBeDefined();
  await admin("POST", `/admin/guardian-links/${link.id}/confirm`, { checkNote: "Known family" });
  let url = "";
  await expect.poll(async () => {
    const html = await (await page.request.get("/dev/outbox")).text();
    const section = html.split("<section").find((s) => s.includes(`To: ${email("email-guardian-proof")}`));
    url = section?.match(/https?:[^\s<]+\/guardian-consent\?token=[A-Za-z0-9_.-]+/)?.[0] ?? "";
    return url;
  }).not.toBe("");
  const target = new URL(url);
  const path = target.pathname + target.search;
  await page.context().clearCookies(); await page.goto(path);
  await expect(page.getByText("Give account consent for", { exact: false })).toBeVisible();
  await expect(page.getByText("Email consent teen", { exact: true })).toBeVisible();
  const before = await admin("GET", `/admin/users/${teen.id}/teen-consents`);
  expect(before.consents).toEqual([]);
  await proof(page, "guardian-email-preview");
  await page.getByRole("button", { name: "Grant account consent", exact: true }).click();
  await expect(page.getByRole("status").filter({ hasText: "Account consent granted." })).toBeVisible();
  await proof(page, "guardian-email-granted");
  await page.reload();
  await expect(page.getByRole("alert").filter({ hasText: "This link has expired" })).toBeVisible();
  await proof(page, "guardian-email-used");
  await signIn(page, "email-teen-proof");
  expect((await page.request.get("/api/v1/groups")).status()).toBe(200);
});
