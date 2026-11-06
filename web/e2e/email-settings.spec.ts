import AxeBuilder from "@axe-core/playwright";
import { expect, test, type APIRequestContext, type Page } from "@playwright/test";
import { apiAs, email, nextFrames, PASSWORD, signIn } from "./helpers";
// @ts-expect-error plain JS module shared with .cursor/skills/verify-ui
import { measure } from "./ui-measure.mjs";

const turnOnMessage = "This link can only turn email off. To turn email back on, sign in, or use the link in a newer email from us.";
const emailPreviewSelector = 'iframe[title="Email preview"]';

async function settingsPath(request: APIRequestContext, login: string) {
  let path = "";
  await expect.poll(async () => {
    const html = await (await request.get("/dev/outbox")).text();
    const section = html.split("<section").find((s) => s.includes(`To: ${email(login)}`));
    const url = section?.match(/https?:[^\s<]+\/email-settings\/[A-Za-z0-9_-]+/)?.[0] ?? "";
    path = url ? new URL(url).pathname : "";
    return path;
  }).not.toBe("");
  return path;
}

// Signed links aren't in ui-pages.ts: each test needs its own real email.
// Keep their accessibility, layout and screenshots beside the flow that
// creates the link, using the same checks as the shared UI coverage.
async function checkUI(page: Page, state: string, options: { emailPreview?: boolean } = {}) {
  for (const width of [390, 1280]) {
    await page.setViewportSize({ width, height: 900 });
    await nextFrames(page);
    const wcagScan = new AxeBuilder({ page })
      .withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa", "wcag22aa"]);
    const headingScan = new AxeBuilder({ page }).withRules(["heading-order"]);
    if (options.emailPreview) {
      const preview = page.locator(emailPreviewSelector);
      await expect(preview).toBeVisible();
      await expect(preview).toHaveAttribute("sandbox", "allow-popups allow-popups-to-escape-sandbox");
      // axe.runPartial hangs inside this deliberately script-free sandbox.
      // Scan the composer and its error, leaving only the email document out.
      // Keep the preview present for layout measurements and screenshots.
      wcagScan.exclude(emailPreviewSelector);
      headingScan.exclude(emailPreviewSelector);
    }
    const wcag = await wcagScan.analyze();
    const headings = await headingScan.analyze();
    const bad = [...wcag.violations, ...headings.violations]
      .filter((v) => v.impact === "moderate" || v.impact === "serious" || v.impact === "critical");
    expect.soft(bad.map((v) => `${v.id}: ${v.help}`), `${state} at ${width}px`).toEqual([]);
    const layout: {
      overflow: number;
      cutOff: unknown[];
      clipped: unknown[];
      under24: unknown[];
      under44: { el: string }[];
    } = await page.evaluate(measure);
    expect.soft(layout.overflow, `${state} at ${width}px: sideways scroll`).toBeLessThanOrEqual(0);
    expect.soft(layout.cutOff, `${state} at ${width}px: clipped controls`).toEqual([]);
    expect.soft(layout.clipped, `${state} at ${width}px: clipped choices`).toEqual([]);
    expect.soft(layout.under24, `${state} at ${width}px: targets under 24px`).toEqual([]);
    expect.soft(layout.under44.filter((t) => /^input\[(radio|checkbox)\]$/.test(t.el)), `${state} at ${width}px: rows under 44px`).toEqual([]);
    await page.screenshot({ path: test.info().outputPath(`${state}-${width}.png`), fullPage: true, animations: "disabled" });
  }
}

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
  const path = await settingsPath(page.request, login);

  await page.context().clearCookies();
  await page.goto(path);
  await expect(page.getByRole("heading", { name: "Email settings", level: 1 })).toBeVisible();
  await expect(page.getByText("You came here from a group update", { exact: false })).toBeVisible();
  await expect(page.getByText(turnOnMessage, { exact: true })).toHaveCount(0);
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

test("a new holder sees the address's effective choice and can turn event updates back on", async ({ page, request, browser }) => {
  const login = `email-settings-reuse-${Date.now()}`;
  const admin = await apiAs(request, "grier");
  const original = await admin("POST", "/admin/users", { name: "Original Email Holder", email: email(login), password: PASSWORD, ageBand: "adult" });
  const originalID: unknown = original.user.id;
  if (typeof originalID !== "number") throw new Error("The original account needs a user id");
  const originalContext = await browser.newContext({ baseURL: test.info().project.use.baseURL });
  try {
    const member = await apiAs(originalContext.request, login);
    await member("PUT", "/me/notification-settings", { category: "events", off: true });
  } finally {
    await originalContext.close();
  }
  await admin("DELETE", `/admin/users/${originalID}`);
  await admin("POST", "/admin/users", { name: "Current Email Holder", email: email(login), password: PASSWORD, ageBand: "adult" });

  await signIn(page, login);
  await page.goto("/profile/notifications");
  const events = page.getByRole("checkbox", { name: /^Event updates/ });
  await expect(events).not.toBeChecked();
  await expect(page.getByRole("checkbox", { name: /^Group updates/ })).toBeChecked();
  await events.check();
  await expect(page.getByRole("status").filter({ hasText: "Saved" })).toBeVisible();
  await page.reload();
  await expect(events).toBeChecked();
});

test("a moved-address link explains turn-off-only access, refuses turn-on and still saves turn-off", async ({ page, request, browser }) => {
  test.setTimeout(90_000);
  test.skip((await request.get("/dev/outbox")).status() !== 200, "Reads the email from the devoutbox (APP_ENV=dev)");
  const login = `email-settings-moved-${Date.now()}`;
  const movedLogin = `${login}-new`;
  const admin = await apiAs(request, "grier");
  const created = await admin("POST", "/admin/users", { name: "Moved Email Member", email: email(login), password: PASSWORD, ageBand: "adult" });
  const userID: unknown = created.user.id;
  if (typeof userID !== "number") throw new Error("The member needs a user id");
  const memberContext = await browser.newContext({ baseURL: test.info().project.use.baseURL });
  let groupID: number;
  try {
    const member = await apiAs(memberContext.request, login);
    await member("PUT", "/me/notification-settings", { category: "events", off: true });
    await member("PUT", "/me/notification-settings", { category: "messages", off: true });
    const createdGroup = await member("POST", "/groups", { name: `Club ${Date.now().toString(36)}`, description: "", tags: [], visibility: "public" });
    const id: unknown = createdGroup.group.id;
    if (typeof id !== "number") throw new Error("The member needs a group id");
    groupID = id;
  } finally {
    await memberContext.close();
  }
  await admin("POST", `/admin/groups/${groupID}/approve`, {});
  const path = await settingsPath(request, login);
  await admin("PUT", `/admin/users/${userID}`, { email: email(movedLogin) });

  await page.context().clearCookies();
  await page.goto(path);
  await expect(page.getByText(turnOnMessage, { exact: true })).toBeVisible();
  const events = page.getByRole("checkbox", { name: /^Event updates/ });
  const groups = page.getByRole("checkbox", { name: /^Group updates/ });
  await expect(events).not.toBeChecked();
  await expect(groups).toBeChecked();
  await checkUI(page, "email-settings-turn-off-only");

  // Use the keyboard: a rejected save must keep both state and focus.
  await events.focus();
  const [refused] = await Promise.all([
    page.waitForResponse((response) => new URL(response.url()).pathname === `/api/v1${path}` && response.request().method() === "PUT"),
    page.keyboard.press("Space"),
  ]);
  expect(refused.status()).toBe(403);
  expect(await refused.json()).toMatchObject({ code: "email_link_turn_on" });
  await expect(page.getByRole("alert")).toHaveText(turnOnMessage);
  await expect(events).not.toBeChecked();
  await expect(events).toBeFocused();
  await checkUI(page, "email-settings-turn-on-refused");

  await groups.uncheck();
  await expect(page.getByRole("status").filter({ hasText: "Saved" })).toBeVisible();
  await expect(page.getByRole("alert")).toHaveCount(0);
  await page.reload();
  await expect(groups).not.toBeChecked();
  await expect(events).not.toBeChecked();
  await expect(page.getByText(turnOnMessage, { exact: true })).toBeVisible();

  // The same effective state must explain a refused composer test, with
  // its existing FormError under the test control at both viewport sizes.
  await signIn(page, movedLogin);
  await page.goto(`/messages/new?group=${groupID}`);
  await page.getByRole("textbox", { name: "Subject" }).fill(`Settings test ${login}`);
  await page.getByRole("textbox", { name: "Message", exact: true }).fill("A test message for this group.");
  await page.getByRole("button", { name: "Preview", exact: true }).click();
  await expect(page).toHaveURL(/\/messages\/\d+$/);
  await page.getByRole("button", { name: "Send a test to me" }).click();
  await expect(page.getByRole("alert").filter({ hasText: "This kind of email is turned off in your notification settings" })).toHaveText("This kind of email is turned off in your notification settings, so we didn't send the test. Turn it on in Me → Notifications, then try again.");
  await expect(page.frameLocator(emailPreviewSelector).getByText("A test message for this group.", { exact: true })).toBeVisible();
  await checkUI(page, "composer-email-turned-off", { emailPreview: true });
});
