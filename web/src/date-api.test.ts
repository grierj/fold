import { afterEach, expect, it, vi } from "vitest";
import { Dates } from "./date-api";

afterEach(() => vi.unstubAllGlobals());
it("independently encodes date path segments and uses only scoped mutations with omitted care fields", async () => {
  const fetcher = vi.fn(async (url: string) => new Response(JSON.stringify(url.endsWith("/csrf") ? { csrfToken: "date-csrf" } : {})));
  vi.stubGlobal("fetch", fetcher);
  const id = "7/8?x", date = "2026-12-01/#?";
  await Dates.get(id, date);
  await Dates.register(id, date, { status: "going", attendanceMode: "remote", guestCount: 2 });
  await Dates.register(id, date, { status: "maybe" });
  await Dates.end(id, date, "decline"); await Dates.end(id, date, "block");
  const calls = fetcher.mock.calls as unknown as [string, RequestInit][];
  const path = "/api/v1/events/7%2F8%3Fx/dates/2026-12-01%2F%23%3F";
  expect(calls.map(([url]) => url)).toEqual([path, "/api/v1/auth/csrf", `${path}/registration`, `${path}/registration`, `${path}/decline`, `${path}/block`]);
  expect(JSON.parse(calls[2][1].body as string)).toEqual({ status: "going", attendanceMode: "remote", guestCount: 2 });
  expect(JSON.parse(calls[3][1].body as string)).toEqual({ status: "maybe" });
  for (const [, options] of calls.filter(([, options]) => options.method === "PUT" || options.method === "POST")) {
    expect(options.credentials).toBe("include"); expect(options.headers).toMatchObject({ "X-CSRF-Token": "date-csrf" });
    expect(options.body || "").not.toMatch(/childcare|personId|registeredBy|overCapacity/);
  }
});
