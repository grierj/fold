import { afterEach, expect, it, vi } from "vitest";
import { Dates, Events } from "./events";

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
    expect(options.body || "").not.toMatch(/childcare|personId|registeredBy|overCapacity/);
  }
});

it("encodes the date of every other per-date call the same way", async () => {
  const fetcher = vi.fn(async (_url: string) => new Response("{}"));
  vi.stubGlobal("fetch", fetcher);
  await Events.dateVisibilityImpact(7, "2026-12-01/#", "series");
  await Events.updateOccurrence(7, "2026-12-01/#", { title: "x" });
  expect(fetcher.mock.calls.map(([url]) => url)).toContain("/api/v1/events/7/occurrences/2026-12-01%2F%23/visibility-impact?visibility=series");
  expect(fetcher.mock.calls.map(([url]) => url)).toContain("/api/v1/events/7/occurrences/2026-12-01%2F%23");
});

it("sends an add's opaque selection and options, never an email, for a selected person", async () => {
  const fetcher = vi.fn(async () => new Response(JSON.stringify({ csrfToken: "csrf" })));
  vi.stubGlobal("fetch", fetcher);
  await Events.addRegistration(7, "2026-12-01", { selectionId: "opaque" }, true, { childcareCount: 2, childcareAgesNote: "3 and 5" });
  const post = (fetcher.mock.calls as unknown as [string, RequestInit][]).find(([, opts]) => opts?.method === "POST");
  expect(JSON.parse(post![1].body as string)).toEqual({ date: "2026-12-01", selectionId: "opaque", overCapacity: true, childcareCount: 2, childcareAgesNote: "3 and 5" });
});
