import { afterEach, expect, it, vi } from "vitest";
import { Admin, AdminGroups } from "./admin";

afterEach(() => vi.unstubAllGlobals());

const posted = (fetcher: ReturnType<typeof vi.fn>) =>
  (fetcher.mock.calls as unknown as [string, RequestInit][]).filter(([, opts]) => opts?.method === "POST").map(([url, opts]) => ({ url, body: JSON.parse(opts.body as string) }));

it("sends an admin add's opaque selection with its role and over-limit choice", async () => {
  const fetcher = vi.fn(async () => new Response(JSON.stringify({ csrfToken: "csrf" })));
  vi.stubGlobal("fetch", fetcher);
  await AdminGroups.addMember(3, { selectionId: "opaque" }, "member", true);
  expect(posted(fetcher)[0].body).toEqual({ selectionId: "opaque", role: "member", overLimit: true });
});

it("approves a group from the bell with the notification id, and from the queue with tags", async () => {
  const fetcher = vi.fn(async () => new Response(JSON.stringify({ csrfToken: "csrf" })));
  vi.stubGlobal("fetch", fetcher);
  await Admin.approve(9, undefined, 2);
  await Admin.approve(9, ["youth"]);
  expect(posted(fetcher).map((p) => p.body)).toEqual([{ fromNotification: 2 }, { tags: ["youth"] }]);
});
