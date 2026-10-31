import { expect, type APIRequestContext, type Locator, type Page } from "@playwright/test";

export const PASSWORD = "password1234";
export const email = (name: string) => `${name}@example.com`;

// Sign in through the real login form, always from a signed-out browser.
// /login redirects a signed-in visitor, so switching users without this
// broke specs twice (hosts.spec.ts in 2722a5d, waitlist.spec.ts in 0a10b04).
// Clearing cookies here makes signIn safe to call at any point; a spec
// only needs signOut when it tests logging out.
export async function signIn(page: Page, name: string) {
  await page.context().clearCookies();
  await page.goto("/login");
  await page.getByLabel("Email").fill(email(name));
  await page.getByLabel("Password").fill(PASSWORD);
  await page.getByRole("button", { name: "Log in" }).click();
  await expect(page).not.toHaveURL(/\/login/);
}

// A pending teen can verify credentials, but cannot establish a login. Public
// browsing stays available; use signIn again after account consent is granted.
export async function attemptPendingSignIn(page: Page, name: string) {
  await page.context().clearCookies();
  await page.goto("/login");
  await page.getByLabel("Email").fill(email(name));
  await page.getByLabel("Password").fill(PASSWORD);
  await page.getByRole("button", { name: "Log in", exact: true }).click();
  await expect(page.getByRole("status")).toContainText("waiting for a parent or guardian's consent");
  await expect(page.getByRole("link", { name: "Browse public events" })).toBeVisible();
  expect((await page.request.get("/api/v1/auth/me")).status()).toBe(401);
}

// Sign out through the header button. Logout ends with a full load of "/"
// (window.location.replace), so wait for that load to finish before the
// next page.goto, or the reload aborts it (the old hosts.spec.ts flake).
export async function signOut(page: Page) {
  await Promise.all([
    page.waitForEvent("load"),
    page.getByRole("button", { name: "Log out" }).click(),
  ]);
  await expect(page).toHaveURL(/\/$/);
  await expect(page.getByRole("link", { name: "Log in" })).toBeVisible();
}

// A signed-in JSON API client for seeding (cookie session + CSRF header).
export async function apiAs(request: APIRequestContext, name: string) {
  const csrf = async () => (await (await request.get("/api/v1/auth/csrf")).json()).csrfToken as string;
  const login = await request.post("/api/v1/auth/login", {
    headers: { "X-CSRF-Token": await csrf() },
    data: { email: email(name), password: PASSWORD },
  });
  expect(login.ok(), await login.text()).toBe(true);
  const token = await csrf();
  return async (method: string, path: string, data?: unknown) => {
    const res = await request.fetch(`/api/v1${path}`, { method, data, headers: { "X-CSRF-Token": token } });
    expect(res.ok(), `${method} ${path}: ${res.status()} ${await res.text()}`).toBe(true);
    return res.json();
  };
}

// Collects CSP violations the browser reports, from the first script on.
export async function watchCSP(page: Page) {
  const seen: string[] = [];
  await page.exposeFunction("__cspViolation", (v: string) => seen.push(v));
  await page.addInitScript(() => {
    document.addEventListener("securitypolicyviolation", (e) =>
      (window as unknown as { __cspViolation: (v: string) => void }).__cspViolation(
        `${e.violatedDirective} ${e.blockedURI} ${e.sourceFile}:${e.lineNumber}`
      )
    );
  });
  page.on("console", (m) => {
    if (/Content Security Policy/i.test(m.text())) seen.push(m.text());
  });
  return seen;
}

// Pick an action from a More menu (components/ui/Menu, DEC-63). The menu
// renders inside its trigger's parent, so `scope` can be the page or a card.
export async function fromMore(scope: Page | Locator, item: string | RegExp, menu: string | RegExp = /^More for/) {
  await scope.getByRole("button", { name: menu }).first().click();
  await scope.getByRole("menuitem", { name: item }).click();
}

// signIn plus the first page.goto, for specs where signing in is only setup.
// Logs in through the JSON API on the page's own cookie jar, then opens `path`,
// so the browser loads the SPA once instead of /login and the post-login home
// page first. The login form itself is still covered by signIn (smoke, session
// and the specs that test sign-in).
export async function signInTo(page: Page, name: string, path: string) {
  // Leave the app first: a page still running it can fetch a new CSRF cookie
  // between the token below and the login POST, which then fails as
  // "CSRF token invalid" (seen on contact-requests.spec.ts in CI).
  await page.goto("about:blank");
  await page.context().clearCookies();
  const csrf = async () => (await (await page.request.get("/api/v1/auth/csrf")).json()).csrfToken as string;
  const login = await page.request.post("/api/v1/auth/login", {
    headers: { "X-CSRF-Token": await csrf() },
    data: { email: email(name), password: PASSWORD },
  });
  expect(login.ok(), await login.text()).toBe(true);
  await gotoReady(page, path);
}

// Waits for two animation frames: a resize or scroll has been laid out and any
// React update it triggered has rendered. Use it instead of a fixed timeout.
export async function nextFrames(page: Page) {
  await page.evaluate(() => new Promise<void>((resolve) => requestAnimationFrame(() => requestAnimationFrame(() => resolve()))));
}

// Waits until the app shell has drawn the current route: <main> has content and
// no "Loading…" skeleton is left. Playwright's locator waits retry on a 0/20/100/
// 100/500ms schedule, so an element that appears 300ms after navigation is only
// noticed at about 800ms; this polls every frame instead, and the locator that
// follows finds its element on the first try. It never fails a test: if the page
// never settles (a held response, say), the assertion that follows reports it.
export async function appReady(page: Page) {
  await page.waitForFunction(() => {
    const main = document.querySelector("main#main");
    return !!main && main.children.length > 0
      && ![...main.querySelectorAll('[role="status"]')].some((s) => /^Loading/.test(s.textContent ?? ""));
  }, null, { timeout: 2_000 }).catch(() => undefined);
}

// page.goto, then appReady.
export async function gotoReady(page: Page, path: string) {
  await page.goto(path);
  await appReady(page);
}

// Locator actions and expect() retry on a 0/20/100/100/500ms schedule, so an
// element that appears 300ms after the click that causes it is only noticed at
// about 800ms. soon() polls every 20ms until the locator is visible (up to
// `timeout`), so the action or assertion that follows finds it at once. Use it as
// `await (await soon(loc)).click()` or `await expect(await soon(loc)).toBeVisible()`. It never
// fails by itself: the real action or assertion after it still decides the test
// and reports the failure.
export async function soon<L extends Locator>(locator: L, timeout = 5_000): Promise<L> {
  const until = Date.now() + timeout;
  try {
    while (Date.now() < until && !(await locator.isVisible())) await new Promise((resolve) => setTimeout(resolve, 20));
  } catch {
    // A strict-mode clash or closed page: let the action or assertion report it.
  }
  return locator;
}

// Picks a room by the start of its option's words. A room a group has used
// also says why it is suggested ("… — Used by this group 2 times", M2.3), and
// other specs on the same server may have booked it, so an exact label would
// depend on what ran first.
export async function pickRoom(select: Locator, name: string) {
  const option = select.locator("option").filter({ hasText: new RegExp(`^${name.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}`) }).first();
  await expect(option).toBeAttached();
  await select.selectOption((await option.getAttribute("value"))!);
}
