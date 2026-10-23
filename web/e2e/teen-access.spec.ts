import { expect, test } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";
import { apiAs, email, PASSWORD, signIn, signOut } from "./helpers";

async function capture(page: import("@playwright/test").Page, name: string) {
  if (!process.env.SCREENS_DIR) return;
  for (const width of [390, 1280]) {
    await page.setViewportSize({ width, height: 900 });
    await page.screenshot({ path: `${process.env.SCREENS_DIR}/${name}-${width}.png`, fullPage: true });
  }
}

test("a teen created through the admin form waits on every route, logs out, and leaves adult access intact", async ({ page, request }) => {
  await signIn(page, "grier");
  await page.goto("/admin?tab=users");
  await page.getByRole("button", { name: "New user", exact: true }).click();
  await page.getByLabel("Email (signs in with it)").fill(email("waiting-teen"));
  await page.getByLabel("Full name").fill("Waiting Teen");
  await page.getByLabel(/^Password/).fill(PASSWORD);
  await page.getByLabel("Under 13", { exact: true }).check();
  await expect(page.getByRole("button", { name: "Create user", exact: true })).toBeDisabled();
  await capture(page, "admin-age-under13");
  await page.getByLabel("13 to 17", { exact: true }).check();
  const birth = new Date(); birth.setFullYear(birth.getFullYear() - 15);
  await page.getByLabel("Birth date").fill(birth.toISOString().slice(0, 10));
  await capture(page, "admin-age-teen");
  await page.getByRole("button", { name: "Create user", exact: true }).click();
  await expect(page.getByText("Waiting Teen", { exact: true })).toBeVisible();
  await signOut(page);
  await page.goto("/login");
  await page.getByLabel("Email", { exact: true }).fill(email("waiting-teen"));
  await page.getByLabel("Password", { exact: true }).fill(PASSWORD);
  await page.getByRole("button", { name: "Log in", exact: true }).click();
  const heading = page.getByRole("heading", { name: "Waiting for consent" });
  await expect(heading).toBeVisible();
  await expect(heading).toBeFocused();
  await expect(page.getByRole("navigation")).toHaveCount(0);
  await capture(page, "teen-waiting");
  const axe = await new AxeBuilder({ page }).analyze();
  expect(axe.violations.filter((v) => v.impact === "serious" || v.impact === "critical")).toEqual([]);
  for (const path of ["/calendar", "/groups/1", "/events/1", "/profile", "/admin"]) {
    await page.goto(path);
    await expect(heading).toBeVisible();
    await expect(page.getByRole("navigation")).toHaveCount(0);
  }
  for (const path of ["/groups", "/calendar", "/events", "/events/1", "/me/household", "/notifications"]) {
    const response = await page.request.get(`/api/v1${path}`);
    expect(response.status()).toBe(403);
    expect(response.headers()["cache-control"]).toBe("no-store");
    expect(await response.json()).toMatchObject({ code: "consent_required" });
  }
  await page.reload();
  await expect(heading).toBeVisible();
  await page.getByRole("button", { name: "Check account status" }).click();
  await expect(heading).toBeVisible();
  await page.getByRole("button", { name: "Log out", exact: true }).click();
  await expect(page.getByRole("link", { name: "Log in", exact: true })).toBeVisible();
  await page.goBack();
  await expect(heading).toHaveCount(0);
  await signIn(page, "sam");
  await expect(page.getByRole("heading", { name: "Members-only circle" })).toBeVisible();
  const admin = await apiAs(request, "grier");
  const { users } = await admin("GET", "/admin/users");
  expect(users.find((user: { email: string; status: string }) => user.email === email("waiting-teen"))).toMatchObject({ status: "pending_consent", accountType: "teen" });
});

test("an ordinary invitation asks the recipient's age and opens a restricted teen session", async ({ page, request }) => {
  test.skip((await request.get("/dev/outbox")).status() !== 200, "Requires dev outbox to open the emailed invitation");
  const admin = await apiAs(request, "grier");
  await admin("POST", "/invitations", { name: "Invited Teen", email: email("invited-teen") });
  const html = await (await request.get("/dev/outbox")).text();
  const section = html.split("<section").find((part) => part.includes(`To: ${email("invited-teen")}`));
  const link = section?.match(/https?:[^\s<]+\/invite\?token=[A-Za-z0-9_-]+/)?.[0];
  expect(link).toBeTruthy();
  if (!link) throw new Error("Invitation email did not contain its link");
  await page.goto(new URL(link).pathname + new URL(link).search);
  await page.getByLabel("Under 13", { exact: true }).check();
  await expect(page.getByRole("button", { name: "Create my account" })).toBeDisabled();
  await page.getByLabel("13 to 17", { exact: true }).check();
  const birth = new Date(); birth.setFullYear(birth.getFullYear() - 15);
  await page.getByLabel("Birth date").fill(birth.toISOString().slice(0, 10));
  await page.getByLabel("Choose a password").fill(PASSWORD);
  await capture(page, "ordinary-invitation-age");
  await page.getByRole("button", { name: "Create my account" }).click();
  await expect(page.getByRole("heading", { name: "Waiting for consent" })).toBeVisible();
  expect((await page.request.get("/api/v1/events")).status()).toBe(403);
});
