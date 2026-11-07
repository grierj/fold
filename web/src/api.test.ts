import { afterEach, describe, expect, it, vi } from "vitest";
import { Auth, Groups, setOnUnauthorized } from "./api";
import type { ErasureReasonItem } from "./types";

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

it("preserves the event window and opaque cursor when requesting another group events page", async () => {
  const response = { events: [], truncated: true, hasMore: true, nextCursor: "next" };
  const fetcher = vi.fn(async () => new Response(JSON.stringify(response), { status: 200 }));
  vi.stubGlobal("fetch", fetcher);
  try {
    await expect(Groups.events(3, "2026-10-01", "2027-03-01", { cursor: "opaque+/=", limit: 100 })).resolves.toEqual(response);
    expect(fetcher).toHaveBeenCalledWith("/api/v1/groups/3/events?from=2026-10-01&to=2027-03-01&cursor=opaque%2B%2F%3D&limit=100", expect.anything());
  } finally {
    vi.unstubAllGlobals();
  }
});

it("fetches a fresh CSRF token after signing in, because the server rotates it", async () => {
  vi.resetModules();
  const { Auth, Groups } = await import("./api");
  let minted = 0;
  const sent: string[] = [];
  const fetcher = vi.fn(async (url: RequestInfo | URL, opts?: RequestInit) => {
    if (String(url) === "/api/v1/auth/csrf") return new Response(JSON.stringify({ csrfToken: `token-${++minted}` }), { status: 200 });
    sent.push((opts?.headers as Record<string, string>)["X-CSRF-Token"]);
    return new Response(JSON.stringify({ user: {} }), { status: 200 });
  });
  vi.stubGlobal("fetch", fetcher);
  try {
    await Auth.login("pat@example.com", "password1234");
    await Groups.join(3);
    expect(sent).toEqual(["token-1", "token-2"]);
  } finally {
    vi.unstubAllGlobals();
  }
});

describe("AdminUsers attempt calls", () => {
  const ID = "0123456789abcdef".repeat(5);
  const csrf = () => new Response(JSON.stringify({ csrfToken: "csrf" }), { status: 200 });
  const stub = (answer: () => Response) => {
    const fetcher = vi.fn(async (url: RequestInfo | URL, _opts?: RequestInit) => (String(url).endsWith("/auth/csrf") ? csrf() : answer()));
    vi.stubGlobal("fetch", fetcher);
    return fetcher;
  };
  afterEach(() => vi.unstubAllGlobals());

  it("mints only a well-formed id, and sends the kind and the subject in the body", async () => {
    const { AdminUsers } = await import("./api");
    const fetcher = stub(() => new Response(JSON.stringify({ attemptId: ID }), { status: 200 }));
    await expect(AdminUsers.mintAttempt(9)).resolves.toEqual({ attemptId: ID });
    const post = fetcher.mock.calls.find(([url]) => String(url) === "/api/v1/admin/operation-attempts");
    expect(JSON.parse((post?.[1] as RequestInit).body as string)).toEqual({ kind: "account_delete", subjectUserId: 9 });
  });

  it.each([
    ["an HTML page", () => new Response("<html>proxy</html>", { status: 200, headers: { "Content-Type": "text/html" } })],
    ["an empty body", () => new Response("", { status: 200 })],
    ["JSON with no id", () => new Response("{}", { status: 200 })],
    ["a short id", () => new Response(JSON.stringify({ attemptId: "abc" }), { status: 200 })],
    ["an id that is not lowercase hex", () => new Response(JSON.stringify({ attemptId: "G".repeat(80) }), { status: 200 })],
    ["a number for an id", () => new Response(JSON.stringify({ attemptId: 5 }), { status: 200 })],
  ])("refuses a mint answered with %s", async (_name, answer) => {
    const { AdminUsers } = await import("./api");
    stub(answer);
    await expect(AdminUsers.mintAttempt(9)).rejects.toThrow();
  });

  it("returns a delete's erasure report and sends the id in the body, not the path", async () => {
    const { AdminUsers } = await import("./api");
    const report = { householdsKept: 1, householdsScrubbed: 0, peopleAnonymized: 2, guardianLinksRevoked: 0, activityEntriesKept: 3, reasonsKept: 1, reasonsRemoved: 2 };
    const fetcher = stub(() => new Response(JSON.stringify({ erasure: report }), { status: 200 }));
    await expect(AdminUsers.remove(9, {}, {}, ID)).resolves.toEqual({ erasure: report });
    const del = fetcher.mock.calls.find(([, opts]) => opts?.method === "DELETE");
    expect(String(del?.[0])).toBe("/api/v1/admin/users/9");
    expect(JSON.parse((del?.[1] as RequestInit).body as string)).toEqual({ successors: {}, hostSuccessors: {}, attemptId: ID });
  });

  it("sends a reason review as reasonReview.items in the DELETE body, and none when there is none", async () => {
    const { AdminUsers } = await import("./api");
    const report = { householdsKept: 0, householdsScrubbed: 0, peopleAnonymized: 0, guardianLinksRevoked: 0, activityEntriesKept: 0, reasonsKept: 1, reasonsRemoved: 1 };
    const fetcher = stub(() => new Response(JSON.stringify({ erasure: report }), { status: 200 }));
    const items = [
      { requestId: "-9223372036854775808", reviewHash: "a".repeat(64), choice: "remove" as const },
      { requestId: "7", reviewHash: "b".repeat(64), choice: "keep" as const },
    ];
    await AdminUsers.remove(9, {}, {}, ID, items);
    await AdminUsers.remove(9, {}, {}, ID);
    const bodies = fetcher.mock.calls.filter(([, opts]) => opts?.method === "DELETE").map(([, opts]) => JSON.parse((opts as RequestInit).body as string));
    expect(bodies[0]).toEqual({ successors: {}, hostSuccessors: {}, attemptId: ID, reasonReview: { items } });
    expect(bodies[1]).not.toHaveProperty("reasonReview");
  });

  it.each([
    ["an HTML page", () => new Response("<html>proxy</html>", { status: 200, headers: { "Content-Type": "text/html" } })],
    ["an empty body", () => new Response("", { status: 200 })],
    ["JSON with no report", () => new Response("{}", { status: 200 })],
    ["a report missing a count", () => new Response(JSON.stringify({ erasure: { householdsKept: 0 } }), { status: 200 })],
  ])("does not call a delete answered with %s a success", async (_name, answer) => {
    const { AdminUsers } = await import("./api");
    stub(answer);
    await expect(AdminUsers.remove(9, {}, {}, ID)).rejects.toThrow();
  });
});

describe("api failure bodies", () => {
  afterEach(() => vi.unstubAllGlobals());
  const failWith = async (response: Response) => {
    const { AdminUsers } = await import("./api");
    vi.stubGlobal("fetch", vi.fn(async (url: RequestInfo | URL, _opts?: RequestInit) => (String(url).endsWith("/auth/csrf") ? new Response(JSON.stringify({ csrfToken: "c" }), { status: 200 }) : response)));
    return AdminUsers.remove(9, {}, {}, "0123456789abcdef".repeat(5)).catch((e: unknown) => e);
  };

  it.each([
    ["an HTML page", () => new Response("<html>bad gateway</html>", { status: 400, headers: { "Content-Type": "text/html" } })],
    ["an empty body", () => new Response("", { status: 400 })],
    ["a JSON array", () => new Response("[]", { status: 400 })],
    ["JSON null", () => new Response("null", { status: 400 })],
  ])("marks a 400 with %s as unreadable", async (_name, make) => {
    expect(await failWith(make())).toMatchObject({ status: 400, unreadable: true });
  });

  it("does not mark a server refusal that came as a JSON object", async () => {
    const refused = await failWith(new Response(JSON.stringify({ error: "cannot delete the last admin", code: "last_admin" }), { status: 400 }));
    expect(refused).toMatchObject({ status: 400, code: "last_admin", unreadable: false });
    const noMessage = await failWith(new Response(JSON.stringify({ code: "needs_successor", groups: [] }), { status: 400 }));
    expect(noMessage).toMatchObject({ status: 400, unreadable: false });
  });
});

describe("AdminUsers.erasureReasonsPage", () => {
  const csrf = () => new Response(JSON.stringify({ csrfToken: "csrf" }), { status: 200 });
  afterEach(() => vi.unstubAllGlobals());

  it("posts the cursor in the body, null for the first page", async () => {
    const { AdminUsers } = await import("./api");
    const item: ErasureReasonItem = {
      requestId: "-9223372036854775808", reviewHash: "a".repeat(64), role: "subject", purpose: "contact", status: "granted",
      createdAt: "2026-11-01T12:00:00Z", counterpartName: "Deleted account", counterpartDeleted: true, counterpartRef: "41",
      reviewerIsParty: false, reason: "Needs a ride.",
    };
    const page = { reviewRequired: true, items: [item], next: "-9223372036854775808" };
    const fetcher = vi.fn(async (url: RequestInfo | URL, _opts?: RequestInit) => (String(url).endsWith("/auth/csrf") ? csrf() : new Response(JSON.stringify(page), { status: 200 })));
    vi.stubGlobal("fetch", fetcher);
    await expect(AdminUsers.erasureReasonsPage(9, null)).resolves.toEqual(page);
    await AdminUsers.erasureReasonsPage(9, "-5");
    const posts = fetcher.mock.calls.filter(([url]) => String(url) === "/api/v1/admin/users/9/erasure-reasons/page");
    expect(posts.map(([, opts]) => JSON.parse((opts as RequestInit).body as string))).toEqual([{ after: null }, { after: "-5" }]);
    expect((posts[0][1] as RequestInit).method).toBe("POST");
  });
});
