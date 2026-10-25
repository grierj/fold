import { afterEach, describe, expect, it, vi } from "vitest";
import { Auth, Groups, setOnUnauthorized } from "./api";

describe("api 401 handling", () => {
  afterEach(() => {
    setOnUnauthorized(null);
    vi.unstubAllGlobals();
  });

  it("sends people to log in on a 401, except for the boot-time session check", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => new Response("{}", { status: 401 })));
    const onUnauthorized = vi.fn();
    setOnUnauthorized(onUnauthorized);

    await expect(Auth.me()).rejects.toMatchObject({ status: 401 });
    expect(onUnauthorized).not.toHaveBeenCalled(); // signed-out visitors keep public pages

    await expect(Groups.mine()).rejects.toMatchObject({ status: 401 });
    expect(onUnauthorized).toHaveBeenCalledOnce();
  });
});

it("sends only opaque selections and existing options to the three adult add endpoints", async () => {
  const { Groups, Events, AdminGroups, People } = await import("./api");
  const fetcher = vi.fn(async (_url: RequestInfo | URL, _opts?: RequestInit) => new Response(JSON.stringify({ csrfToken: "csrf" }), { status: 200 }));
  vi.stubGlobal("fetch", fetcher);
  await People.search("Pat & Lee");
  expect(fetcher).toHaveBeenCalledWith("/api/v1/people/search?q=Pat%20%26%20Lee", expect.anything());
  await Groups.addMember(3, { selectionId: "opaque" }, true);
  await Events.addRegistration(7, "2026-12-01", { selectionId: "opaque" }, true, { childcareCount: 2, childcareAgesNote: "3 and 5" });
  await AdminGroups.addMember(3, { selectionId: "opaque" }, "member", true);
  const posts = fetcher.mock.calls.filter(([, opts]) => opts?.method === "POST");
  expect(posts.map(([, opts]) => JSON.parse(opts!.body as string))).toEqual([
    { selectionId: "opaque", overLimit: true },
    { date: "2026-12-01", selectionId: "opaque", overCapacity: true, childcareCount: 2, childcareAgesNote: "3 and 5" },
    { selectionId: "opaque", role: "member", overLimit: true },
  ]);
  vi.unstubAllGlobals();
});
