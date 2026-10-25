import { afterEach, expect, it, vi } from "vitest";
import { Contacts } from "./contact-api";

afterEach(() => vi.unstubAllGlobals());
it("uses exclusive cursors and sends CSRF on every contact mutation, never on GET", async () => {
  const fetcher = vi.fn(async (url: string) => new Response(JSON.stringify(url.endsWith("/csrf") ? { csrfToken: "test-csrf" } : {}), { status: 200 }));
  vi.stubGlobal("fetch", fetcher);
  await Contacts.requests(51); await Contacts.grants(71); await Contacts.request(5);
  const scope = { subjectUserId: 2, contextType: "event" as const, contextId: 3, fields: "email" as const };
  await Contacts.create({ ...scope, reason: "Optional" });
  await Contacts.decide(5, "subject", "approved");
  await Contacts.withdraw(5); await Contacts.revoke(7); await Contacts.reveal(7, scope);
  const calls = fetcher.mock.calls as unknown as [string, RequestInit][];
  expect(calls.map(([url]) => url)).toEqual([
    "/api/v1/contact-requests?before=51", "/api/v1/contact-grants?before=71", "/api/v1/contact-requests/5", "/api/v1/auth/csrf",
    "/api/v1/contact-requests", "/api/v1/contact-requests/5/decision", "/api/v1/contact-requests/5/withdraw", "/api/v1/contact-grants/7/revoke", "/api/v1/contact-grants/7/reveal",
  ]);
  for (const [, options] of calls.filter(([, options]) => options?.method === "POST")) {
    expect(options.credentials).toBe("include");
    expect(options.headers).toMatchObject({ "X-CSRF-Token": "test-csrf" });
  }
  expect(calls[0][1].headers).not.toHaveProperty("X-CSRF-Token");
  expect(JSON.parse(calls[calls.length - 1][1].body as string)).toEqual(scope);
});
