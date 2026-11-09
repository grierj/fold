import { afterEach, expect, it, vi } from "vitest";
import { ContactHistory } from "./contactHistory";

const filters = { purpose: "contact", action: "contact.reveal", from: "2026-10-01T09:00:00+02:00", until: "2026-11-01T07:00:00Z" };
afterEach(() => vi.unstubAllGlobals());

it("encodes people lookup parameters while preserving exact IDs and email values", async () => {
  const fetcher = vi.fn().mockResolvedValue(new Response(JSON.stringify({ people: [{ id: "9007199254740993", name: "Pat", email: "Pat+tag@example.com" }] })));
  vi.stubGlobal("fetch", fetcher);
  await expect(ContactHistory.people({ q: "Pat + Lee", id: "9007199254740993", email: "Pat+tag@example.com" })).resolves.toEqual({
    people: [{ id: "9007199254740993", name: "Pat", email: "Pat+tag@example.com" }],
  });
  const url = new URL(fetcher.mock.calls[0][0], "https://fold.example");
  expect(url.pathname).toBe("/api/v1/admin/contact-history/people");
  expect([...url.searchParams.entries()]).toEqual([
    ["q", "Pat + Lee"], ["id", "9007199254740993"], ["email", "Pat+tag@example.com"],
  ]);
});

it("keeps signed-bigint person IDs/cursors exact and encodes timezone offsets", async () => {
  const fetcher = vi.fn().mockResolvedValue(new Response(JSON.stringify({ entries: [], nextBefore: "9007199254740993" })));
  vi.stubGlobal("fetch", fetcher);
  expect(await ContactHistory.list("9223372036854775807", filters, "9007199254740993")).toEqual({ entries: [], nextBefore: "9007199254740993" });
  const [path, options] = fetcher.mock.calls[0];
  const url = new URL(path, "https://fold.example");
  expect(url.pathname).toBe("/api/v1/admin/users/9223372036854775807/contact-history");
  expect(url.searchParams.get("before")).toBe("9007199254740993");
  expect(url.searchParams.get("from")).toBe(filters.from);
  expect(url.search).toContain("%2B02%3A00");
  expect(options.credentials).toBe("include");
});

it("exports all submitted filters without page fields and requests no-store CSV", async () => {
  const fetcher = vi.fn().mockResolvedValue(new Response("auditId,at\r\n", { headers: { "Content-Type": "text/csv; charset=utf-8" } }));
  vi.stubGlobal("fetch", fetcher);
  const blob = await ContactHistory.csv("9007199254740993", { ...filters, before: "11", limit: "50" } as typeof filters);
  expect(await blob.text()).toBe("auditId,at\r\n");
  const [path, options] = fetcher.mock.calls[0];
  const url = new URL(path, "https://fold.example");
  expect(url.pathname.endsWith("/9007199254740993/contact-history.csv")).toBe(true);
  expect([...url.searchParams.keys()]).toEqual(["purpose", "action", "from", "until"]);
  expect(url.searchParams.get("from")).toBe(filters.from);
  expect(options).toMatchObject({ credentials: "include", cache: "no-store", headers: { Accept: "text/csv" } });
});
