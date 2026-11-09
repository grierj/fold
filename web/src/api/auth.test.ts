import { afterEach, expect, it, vi } from "vitest";
import { Auth } from "./auth";
import { configureApi } from "./core";

afterEach(() => {
  configureApi({ onUnauthorized: null });
  vi.unstubAllGlobals();
});

it("treats a wrong password as a failed sign-in, not as a session that ended", async () => {
  const onUnauthorized = vi.fn();
  configureApi({ onUnauthorized });
  vi.stubGlobal("fetch", vi.fn(async (url: string) => String(url).endsWith("/csrf")
    ? new Response(JSON.stringify({ csrfToken: "c" }))
    : new Response(JSON.stringify({ error: "invalid email or password", code: "bad_credentials" }), { status: 401 })));
  await expect(Auth.login("pat@example.com", "nope")).rejects.toMatchObject({ status: 401, code: "bad_credentials" });
  expect(onUnauthorized).not.toHaveBeenCalled();
});

it("fetches a fresh CSRF token after signing in, because the server rotates it", async () => {
  vi.resetModules();
  const { Auth } = await import("./auth");
  const { api } = await import("./core");
  let minted = 0;
  const sent: string[] = [];
  vi.stubGlobal("fetch", vi.fn(async (url: RequestInfo | URL, opts?: RequestInit) => {
    if (String(url) === "/api/v1/auth/csrf") return new Response(JSON.stringify({ csrfToken: `token-${++minted}` }), { status: 200 });
    sent.push((opts?.headers as Record<string, string>)["X-CSRF-Token"]);
    return new Response(JSON.stringify({ user: {} }), { status: 200 });
  }));
  await Auth.login("pat@example.com", "password1234");
  await api("/groups/3/join", { method: "POST" });
  expect(sent).toEqual(["token-1", "token-2"]);
});
