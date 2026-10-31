import { expect, test } from "@playwright/test";
import { apiAs, email, PASSWORD, signIn } from "./helpers";

// M3.3b: an optional email's link opens the email settings page without
// signing in; a switch changed there is the same switch on Me > Notifications.
// This spec is the page's coverage: it isn't in ui-pages.ts because a valid
// token only exists server-side, in a sent email.
test("an email's settings link works signed out and matches Me > Notifications", async ({ page, request, browser }) => {
  test.skip((await request.get("/dev/outbox")).status() !== 200, "Reads the email from the devoutbox (APP_ENV=dev)");
  const login = `email-settings-${Date.now() % 100000}`;
  const admin = await apiAs(request, "grier");
  await admin("POST", "/admin/users", { name: "Email Settings Member", email: email(login), password: PASSWORD, ageBand: "adult" });
  // Its own context: one request context holds one session.
  const memberContext = await browser.newContext({ baseURL: test.info().project.use.baseURL });
  const member = await apiAs(memberContext.request, login);
  const { group } = (await member("POST", "/groups", { name: `Settings club ${login}`, description: "", tags: [], visibility: "public" })) as { group: { id: number } };
  await admin("POST", `/admin/groups/${group.id}/approve`, {});
  await memberContext.close();

  // The approval is an optional email (group updates) with the settings link.
  let path = "";
  await expect.poll(async () => {
    const html = await (await page.request.get("/dev/outbox")).text();
    const section = html.split("<section").find((s) => s.includes(`To: ${email(login)}`));
    const url = section?.match(/https?:[^\s<]+\/email-settings\/[A-Za-z0-9_-]+/)?.[0] ?? "";
    path = url ? new URL(url).pathname : "";
    return path;
  }).not.toBe("");

  await page.context().clearCookies();
  await page.goto(path);
  await expect(page.getByRole("heading", { name: "Email settings", level: 1 })).toBeVisible();
  await expect(page.getByText("You came here from a group update", { exact: false })).toBeVisible();
  const groups = page.getByRole("checkbox", { name: /^Group updates/ });
  await expect(groups).toBeChecked();
  await groups.uncheck();
  await expect(page.getByRole("status").filter({ hasText: "Saved" })).toBeVisible();

  await signIn(page, login);
  await page.goto("/profile/notifications");
  await expect(page.getByRole("checkbox", { name: /^Group updates/ })).not.toBeChecked();
  await expect(page.getByRole("checkbox", { name: /^Event updates/ })).toBeChecked();

  // A link that was tampered with is a friendly dead end, not an error.
  await page.context().clearCookies();
  await page.goto(path + "x");
  await expect(page.getByText("This link doesn't work anymore")).toBeVisible();
});
