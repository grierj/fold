import { expect, type APIRequestContext, type Page } from "@playwright/test";

export const PASSWORD = "password1234";
export const email = (name: string) => `${name}@example.com`;

// Sign in through the real login form.
export async function signIn(page: Page, name: string) {
  await page.goto("/login");
  await page.getByLabel("Email").fill(email(name));
  await page.getByLabel("Password").fill(PASSWORD);
  await page.getByRole("button", { name: "Log in" }).click();
  await expect(page).not.toHaveURL(/\/login/);
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
