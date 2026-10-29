import { afterEach, expect, it, vi } from "vitest";
import { ApiError, setOnUnauthorized } from "./api";
import { ContactHistory, retainedPersonId } from "./contact-history-api";

const filters = { purpose: "contact", action: "contact.reveal", from: "2026-10-01T09:00:00+02:00", until: "2026-11-01T07:00:00Z" };
afterEach(() => { vi.unstubAllGlobals(); setOnUnauthorized(null); });

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

it("exports all submitted filters without page fields, requests no-store and ignores unsafe server filenames", async () => {
  const fetcher = vi.fn().mockResolvedValue(new Response("auditId,at\r\n", { headers: { "Content-Type": "text/csv; charset=utf-8", "Content-Disposition": 'attachment; filename="../../unsafe.csv"' } }));
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

it.each([400, 403, 500])("rejects JSON CSV failure %s without returning a blob", async (status) => {
  vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({ error: "SECRET", code: status === 400 ? "contact_history_filters" : "server_error" }), { status, headers: { "Content-Type": "application/json" } })));
  await expect(ContactHistory.csv("1", filters)).rejects.toMatchObject({ status });
});
it("shares signed-out handling and rejects successful JSON or proxy bodies", async () => {
  const unauthorized = vi.fn(); setOnUnauthorized(unauthorized);
  vi.stubGlobal("fetch", vi.fn().mockResolvedValueOnce(new Response("{}", { status: 401 })).mockResolvedValueOnce(new Response("{}", { headers: { "Content-Type": "application/json" } })).mockResolvedValueOnce(new Response("proxy", { status: 502 })));
  await expect(ContactHistory.csv("1", filters)).rejects.toMatchObject({ status: 401, code: "signed_out" });
  expect(unauthorized).toHaveBeenCalledOnce();
  await expect(ContactHistory.csv("1", filters)).rejects.toBeInstanceOf(ApiError);
  await expect(ContactHistory.csv("1", filters)).rejects.toMatchObject({ status: 502 });
});
it("preserves network failures for friendly UI handling", async () => {
  vi.stubGlobal("fetch", vi.fn().mockRejectedValue(new TypeError("Network secret")));
  await expect(ContactHistory.csv("1", filters)).rejects.toBeInstanceOf(TypeError);
});
it("validates retained decimal IDs without numeric coercion", () => {
  for (const id of ["9007199254740992", "9007199254740993", "9223372036854775807"]) expect(retainedPersonId(id)).toBe(id);
  expect(retainedPersonId("00012")).toBe("12");
  for (const id of ["", "0", "-1", "+1", "1.5", " 12", "9223372036854775808"]) expect(retainedPersonId(id)).toBeNull();
});
