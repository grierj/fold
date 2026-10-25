import { afterEach, expect, it, vi } from "vitest";
import { Adds } from "./add-api";

afterEach(() => vi.unstubAllGlobals());
it("keeps add purpose separate and sends opaque selection/exact occurrence/options and CSRF", async () => {
  const fetcher = vi.fn(async (url: string) => new Response(JSON.stringify(url.endsWith("/csrf") ? { csrfToken: "test-csrf" } : {})));
  vi.stubGlobal("fetch", fetcher);
  await Adds.requests(51); await Adds.request(7);
  const body = { selectionId: "opaque/+?", contextType: "event" as const, contextId: 3, occurrenceId: 31, overCapacity: false, attendanceMode: "remote" as const, guestCount: 2, childcareCount: 0, childcareAgesNote: "", reason: "Optional" };
  await Adds.create(body); await Adds.decide(7, "admin", "approved"); await Adds.withdraw(7); await Adds.complete(7);
  const calls = fetcher.mock.calls as unknown as [string, RequestInit][];
  expect(calls.map(([url]) => url)).toEqual(["/api/v1/add-requests?before=51", "/api/v1/add-requests/7", "/api/v1/auth/csrf", "/api/v1/add-requests", "/api/v1/add-requests/7/decision", "/api/v1/add-requests/7/withdraw", "/api/v1/add-requests/7/complete"]);
  expect(JSON.parse(calls[3][1].body as string)).toEqual(body);
  for (const [, options] of calls.filter(([, options]) => options?.method === "POST")) {
    expect(options.credentials).toBe("include"); expect(options.headers).toMatchObject({ "X-CSRF-Token": "test-csrf" });
  }
  expect(calls[0][1].headers).not.toHaveProperty("X-CSRF-Token");
});
