import { expect, type Page } from "@playwright/test";

// Signing in by the emailed link, read from the development outbox.

export async function latestLink(page: Page, to: string): Promise<string> {
  await page.goto("/dev/outbox");
  const text = await page.locator("li", { hasText: `To ${to}` }).first().locator("[data-outbox-text]").innerText();
  return text.match(/http:\/\/[^\s]+\/l\/[A-Za-z0-9_-]{43}/)![0];
}

export async function signIn(page: Page, email: string) {
  await page.goto("/signin");
  await page.getByLabel("Email").fill(email);
  await page.getByRole("button", { name: "Email me a sign-in link" }).click();
  await expect(page.getByRole("heading", { name: "Check your email" })).toBeVisible();
  await expect.poll(async () => (await latestLink(page, email)).length, { timeout: 10_000 }).toBeGreaterThan(0);
  await page.goto(await latestLink(page, email));
}
