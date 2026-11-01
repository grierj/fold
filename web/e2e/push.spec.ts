import AxeBuilder from "@axe-core/playwright";
import { expect, test, type BrowserContext, type Page } from "@playwright/test";
import { apiAs, email, PASSWORD, signIn, signOut, watchCSP } from "./helpers";

// Notifications on this device (M3.13c1, research 2.9). Headless Chromium
// has no push service, so the card runs against a stubbed PushManager and
// service worker registration whose subscribe() returns the RFC 8291
// Appendix A subscription on an allow-listed endpoint. Everything after the
// browser (PUT, DELETE, logout) is the real server; only "Send a test
// notification" is answered here, so no test reaches a real push service.
// Notification.permission is stubbed too (see fakePush); the answer to the
// tap still comes from context.grantPermissions.

const RFC_P256DH = "BCVxsr7N_eNgVRqvHtD0zTZsEc6-VV-JvLexhqUzORcxaOzi6-AYWXvTBHm4bjyPjs7Vd8pZGH6SRpkNtoIAiw4";
const RFC_AUTH = "BTBZMqHH6r4Tts7J_aSIgg";

async function fakePush(context: BrowserContext, opts: { permission?: NotificationPermission; noPush?: boolean } = {}) {
  await context.addInitScript(({ p256dh, auth, permission, noPush }) => {
    if (noPush) {
      delete (window as unknown as Record<string, unknown>).PushManager;
      return;
    }
    // Headless Chromium reports "denied" even after grantPermissions, while
    // requestPermission answers from the grant. The page sees the answer.
    let state: NotificationPermission = permission ?? "default";
    const ask = Notification.requestPermission.bind(Notification);
    Object.defineProperty(Notification, "permission", { get: () => state, configurable: true });
    Notification.requestPermission = async () => (state = await ask());
    const endpoint = `https://fcm.googleapis.com/fcm/send/e2e-${Math.random().toString(36).slice(2)}`;
    let current: unknown = null;
    const sub = (key: ArrayBuffer) => ({
      endpoint, expirationTime: null, options: { userVisibleOnly: true, applicationServerKey: key },
      toJSON: () => ({ endpoint, expirationTime: null, keys: { p256dh, auth } }),
      unsubscribe: async () => { current = null; return true; },
    });
    const pushManager = {
      getSubscription: async () => current,
      subscribe: async (o: { applicationServerKey: Uint8Array }) => (current = sub(new Uint8Array(o.applicationServerKey).buffer)),
    };
    const registration = { scope: `${location.origin}/`, pushManager, getNotifications: async () => [] };
    let registered = false;
    const fake = {
      register: async (url: string, o: unknown) => {
        (window as unknown as { __registered: unknown }).__registered = [url, o];
        registered = true;
        return registration;
      },
      getRegistration: async () => (registered ? registration : undefined),
      get ready() { return Promise.resolve(registration); },
    };
    Object.defineProperty(navigator, "serviceWorker", { get: () => fake, configurable: true });
  }, { p256dh: RFC_P256DH, auth: RFC_AUTH, permission: opts.permission, noPush: opts.noPush });
}

const login = `push-${Date.now() % 100000}`;
test.beforeAll(async ({ request }) => {
  const admin = await apiAs(request, "grier");
  await admin("POST", "/admin/users", { name: "Push Member", email: email(login), password: PASSWORD, ageBand: "adult" });
});

const card = (page: Page) => page.getByRole("region", { name: "Notifications on this device" });

async function devices(page: Page) {
  const res = await page.request.get("/api/v1/me/notification-settings");
  return ((await res.json()).settings.push.devices as { label: string }[]);
}

// A 390px picture of the card for review, and axe on the page in that
// state at 390 and 1280 (the shared a11y pass sees only what headless
// Chromium shows without the stubs).
async function shot(page: Page, name: string) {
  await card(page).screenshot({ path: test.info().outputPath(`push-card-${name}-390.png`) });
  for (const width of [1280, 390]) {
    await page.setViewportSize({ width, height: 900 });
    const { violations } = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa", "wcag22aa"]).analyze();
    const bad = violations.filter((v) => v.impact === "moderate" || v.impact === "serious" || v.impact === "critical");
    expect.soft(bad.map((v) => `${v.impact} ${v.id}: ${v.nodes.map((n) => n.target.join(" ")).join(", ")}`), `${name} at ${width}px`).toEqual([]);
  }
}

test("the card turns on, sends a test, turns off, and logout removes the device", async ({ page, context }) => {
  await context.grantPermissions(["notifications"]);
  await fakePush(context);
  await page.setViewportSize({ width: 390, height: 844 });
  await signIn(page, login);
  await page.goto("/profile/notifications");

  const turnOn = card(page).getByRole("button", { name: "Turn on notifications" });
  await expect(turnOn).toBeVisible();
  expect(await page.evaluate(() => (window as unknown as { __registered?: unknown }).__registered)).toBeUndefined();
  await shot(page, "ready");

  const put = page.waitForRequest((r) => r.url().endsWith("/api/v1/push/subscriptions") && r.method() === "PUT");
  await turnOn.click();
  const body = (await put).postDataJSON();
  expect(body.endpoint).toMatch(/^https:\/\/fcm\.googleapis\.com\/fcm\/send\/e2e-/);
  expect(body.keys).toEqual({ p256dh: RFC_P256DH, auth: RFC_AUTH });
  expect(await page.evaluate(() => (window as unknown as { __registered?: unknown }).__registered)).toEqual(["/sw.js", { scope: "/" }]);
  await expect(card(page).getByText("Notifications are on for this device.", { exact: true })).toBeVisible();
  expect((await devices(page)).map((d) => d.label)).toEqual([expect.stringMatching(/^Chrome on /)]);

  // The test answer is stubbed: the endpoint is a fake one.
  let tested: unknown;
  await page.route("**/api/v1/push/test", async (route) => {
    tested = route.request().postDataJSON();
    await route.fulfill({ json: { sent: true } });
  });
  await card(page).getByRole("button", { name: "Send a test notification" }).click();
  await expect(card(page).getByRole("status")).toHaveText("Sent. It should show on this device in a moment.");
  expect(tested).toEqual({ endpoint: body.endpoint });
  await expect(card(page).getByRole("group", { name: "What comes to your devices" })).toBeVisible();
  await shot(page, "on");

  const del = page.waitForRequest((r) => r.url().endsWith("/api/v1/push/subscriptions") && r.method() === "DELETE");
  await card(page).getByRole("button", { name: "Turn off notifications on this device" }).click();
  expect((await del).postDataJSON()).toEqual({ endpoint: body.endpoint });
  await expect(card(page).getByRole("button", { name: "Turn on notifications" })).toBeVisible();
  expect(await devices(page)).toEqual([]);

  // On again, then Log out: the logout request names the device.
  await card(page).getByRole("button", { name: "Turn on notifications" }).click();
  await expect(card(page).getByText("Notifications are on for this device.", { exact: true })).toBeVisible();
  const out = page.waitForRequest((r) => r.url().endsWith("/api/v1/auth/logout"));
  await page.setViewportSize({ width: 1280, height: 900 });
  await signOut(page);
  expect((await out).postDataJSON()).toEqual({ pushEndpoint: body.endpoint });
  await signIn(page, login);
  expect(await devices(page)).toEqual([]);
});

test("turning off everywhere asks first", async ({ page, context }) => {
  await context.grantPermissions(["notifications"]);
  await fakePush(context);
  await signIn(page, login);
  await page.goto("/profile/notifications");
  await card(page).getByRole("button", { name: "Turn on notifications" }).click();
  await expect(card(page).getByText("Notifications are on for this device.", { exact: true })).toBeVisible();
  page.once("dialog", (d) => d.accept());
  await card(page).getByRole("button", { name: "Turn off notifications everywhere" }).click();
  await expect(card(page).getByRole("status")).toHaveText("Notifications are off on all your devices.");
  expect(await devices(page)).toEqual([]);
});

test("a blocked device shows the undo steps and no button", async ({ page, context }) => {
  await fakePush(context, { permission: "denied" });
  await page.setViewportSize({ width: 390, height: 844 });
  await signIn(page, login);
  await page.goto("/profile/notifications");
  await expect(card(page).getByText("This device is blocking notifications from Fold e2e. To allow them again:")).toBeVisible();
  await expect(card(page).getByText("Click the icon left of the address bar, find Notifications, and set it to Allow.")).toBeVisible();
  await expect(card(page).getByRole("link", { name: "More help with notifications" })).toHaveAttribute("href", "/notifications/setup");
  await expect(card(page).getByRole("button")).toHaveCount(0);
  await shot(page, "blocked");
});

test("a browser that can't receive notifications says to use email", async ({ page, context }) => {
  await fakePush(context, { noPush: true });
  await page.setViewportSize({ width: 390, height: 844 });
  await signIn(page, login);
  await page.goto("/profile/notifications");
  await expect(card(page).getByText(/On this device, use email\./)).toBeVisible();
  await expect(card(page).getByRole("button")).toHaveCount(0);
  await shot(page, "unavailable");
});

// The real worker: served from the root with no-cache, allowed by the CSP,
// and active with the whole site as its scope. The manifest names the church.
test("the real service worker registers and the manifest carries the church name", async ({ page, request }) => {
  const csp = await watchCSP(page);
  const sw = await request.get("/sw.js");
  expect(sw.headers()["cache-control"]).toBe("no-cache");
  expect(sw.headers()["content-type"]).toMatch(/^text\/javascript/);
  expect(await sw.text()).not.toMatch(/addEventListener\(\s*[`'"]fetch/);
  const manifest = await (await request.get("/manifest.webmanifest")).json();
  expect(manifest).toMatchObject({ name: "Fold e2e", short_name: "Fold e2e", start_url: "/?source=homescreen", display: "standalone" });

  await page.goto("/");
  await expect(page.locator('link[rel="manifest"]')).toHaveAttribute("href", "/manifest.webmanifest");
  const scope = await page.evaluate(async () => {
    const reg = await navigator.serviceWorker.register("/sw.js", { scope: "/" });
    await navigator.serviceWorker.ready;
    const scope = reg.scope;
    await reg.unregister();
    return scope;
  });
  expect(scope).toBe(new URL("/", page.url()).href);
  expect(csp).toEqual([]);
});

test("the Home Screen app opens on Log in when signed out", async ({ page }) => {
  await page.context().clearCookies();
  await page.goto("/?source=homescreen");
  await expect(page).toHaveURL(/\/login$/);
  await expect(page.getByRole("heading", { name: /^Log in/, level: 1 })).toBeVisible();
});
