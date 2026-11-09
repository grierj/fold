import { afterEach, describe, expect, it, vi } from "vitest";
import type { ErasureReasonItem } from "../types";
import { AdminUsers } from "./adminUsers";

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
    stub(answer);
    await expect(AdminUsers.mintAttempt(9)).rejects.toThrow();
  });

  it("returns a delete's erasure report and sends the id in the body, not the path", async () => {
    const report = { householdsKept: 1, householdsScrubbed: 0, peopleAnonymized: 2, guardianLinksRevoked: 0, activityEntriesKept: 3, reasonsKept: 1, reasonsRemoved: 2 };
    const fetcher = stub(() => new Response(JSON.stringify({ erasure: report }), { status: 200 }));
    await expect(AdminUsers.remove(9, {}, {}, ID)).resolves.toEqual({ erasure: report });
    const del = fetcher.mock.calls.find(([, opts]) => opts?.method === "DELETE");
    expect(String(del?.[0])).toBe("/api/v1/admin/users/9");
    expect(JSON.parse((del?.[1] as RequestInit).body as string)).toEqual({ successors: {}, hostSuccessors: {}, attemptId: ID });
  });

  it("sends the set-v1 reasonReview in the DELETE body, and none when there is none", async () => {
    const report = { householdsKept: 0, householdsScrubbed: 0, peopleAnonymized: 0, guardianLinksRevoked: 0, activityEntriesKept: 0, reasonsKept: 1, reasonsRemoved: 1 };
    const fetcher = stub(() => new Response(JSON.stringify({ erasure: report }), { status: 200 }));
    const reasonReview = { format: "set-v1" as const, count: "2", digest: "a".repeat(64), choices: "Ag==" };
    await AdminUsers.remove(9, {}, {}, ID, reasonReview);
    await AdminUsers.remove(9, {}, {}, ID);
    const bodies = fetcher.mock.calls.filter(([, opts]) => opts?.method === "DELETE").map(([, opts]) => JSON.parse((opts as RequestInit).body as string));
    expect(bodies[0]).toEqual({ successors: {}, hostSuccessors: {}, attemptId: ID, reasonReview });
    expect(Object.keys(bodies[0].reasonReview).sort()).toEqual(["choices", "count", "digest", "format"]);
    expect(bodies[1]).not.toHaveProperty("reasonReview");
  });

  it.each([
    ["an HTML page", () => new Response("<html>proxy</html>", { status: 200, headers: { "Content-Type": "text/html" } })],
    ["an empty body", () => new Response("", { status: 200 })],
    ["JSON with no report", () => new Response("{}", { status: 200 })],
    ["a report missing a count", () => new Response(JSON.stringify({ erasure: { householdsKept: 0 } }), { status: 200 })],
  ])("does not call a delete answered with %s a success", async (_name, answer) => {
    stub(answer);
    await expect(AdminUsers.remove(9, {}, {}, ID)).rejects.toThrow();
  });
});

describe("AdminUsers.erasureReasonsPage", () => {
  const csrf = () => new Response(JSON.stringify({ csrfToken: "csrf" }), { status: 200 });
  afterEach(() => vi.unstubAllGlobals());

  it("posts the cursor in the body, null for the first page", async () => {
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
