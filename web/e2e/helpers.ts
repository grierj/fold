import { expect, type APIRequestContext, type Locator, type Page } from "@playwright/test";

const PASSWORD = "password1234";
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
