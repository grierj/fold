import { afterEach, describe, expect, it, vi } from "vitest";
import type { ErasureReasonItem, LegacyReasonDecision } from "../types";
import { AdminUsers } from "./adminUsers";
import { ApiError } from "./core";

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

describe("AdminUsers legacy reason review", () => {
  const ID = "fedcba9876543210".repeat(5);
  const HASH = "a".repeat(64);
  const csrf = () => new Response(JSON.stringify({ csrfToken: "csrf" }), { status: 200 });
  const stub = (answer: () => Response) => {
    const fetcher = vi.fn(async (url: RequestInfo | URL, _opts?: RequestInit) => (String(url).endsWith("/auth/csrf") ? csrf() : answer()));
    vi.stubGlobal("fetch", fetcher);
    return fetcher;
  };
  const json = (body: unknown, status = 200) => () => new Response(JSON.stringify(body), { status });
  const sent = (fetcher: ReturnType<typeof stub>) => fetcher.mock.calls.filter(([url]) => !String(url).endsWith("/auth/csrf"));
  const bodyOf = (call: [RequestInfo | URL, RequestInit?]) => JSON.parse((call[1] as RequestInit).body as string);
  afterEach(() => vi.unstubAllGlobals());

  describe("legacyReasons", () => {
    const list = { accounts: [{ userId: "9223372036854775807", name: "Deleted account", items: [{ requestId: "-9223372036854775808" }] }], next: "-9223372036854775808" };

    it("reads a unit with a GET, the cursor in the query only when there is one", async () => {
      const fetcher = stub(json(list));
      await expect(AdminUsers.legacyReasons()).resolves.toEqual(list);
      await AdminUsers.legacyReasons(null);
      await AdminUsers.legacyReasons("-5");
      expect(sent(fetcher).map(([url, opts]) => [String(url), opts?.method])).toEqual([
        ["/api/v1/admin/erasure-reasons/legacy", "GET"],
        ["/api/v1/admin/erasure-reasons/legacy", "GET"],
        ["/api/v1/admin/erasure-reasons/legacy?after=-5", "GET"],
      ]);
      // A read needs no CSRF token.
      expect(fetcher.mock.calls.some(([url]) => String(url).endsWith("/auth/csrf"))).toBe(false);
    });

    it("accepts the last unit, and an empty one that still has a next", async () => {
      stub(json({ accounts: [], next: null }));
      await expect(AdminUsers.legacyReasons()).resolves.toEqual({ accounts: [], next: null });
      stub(json({ accounts: [], next: "7" }));
      await expect(AdminUsers.legacyReasons()).resolves.toEqual({ accounts: [], next: "7" });
    });

    it.each([
      ["an HTML page", () => new Response("<html>proxy</html>", { status: 200, headers: { "Content-Type": "text/html" } })],
      ["an empty body", () => new Response("", { status: 200 })],
      ["JSON with no accounts", json({ next: null })],
      ["accounts that are not a list", json({ accounts: {}, next: null })],
      ["no cursor field", json({ accounts: [] })],
      ["a numeric cursor", json({ accounts: [], next: 5 })],
      ["an empty cursor", json({ accounts: [], next: "" })],
      ["a zero-padded cursor", json({ accounts: [], next: "007" })],
      ["a fractional cursor", json({ accounts: [], next: "1.5" })],
      ["an out-of-range cursor", json({ accounts: [], next: "9223372036854775808" })],
      ["a null account", json({ accounts: [null], next: null })],
      ["a numeric account id", json({ accounts: [{ userId: 41, name: "Deleted account", items: [] }], next: null })],
      ["a zero-padded account id", json({ accounts: [{ userId: "041", name: "Deleted account", items: [] }], next: null })],
      ["a zero account id", json({ accounts: [{ userId: "0", name: "Deleted account", items: [] }], next: null })],
      ["a negative account id", json({ accounts: [{ userId: "-1", name: "Deleted account", items: [] }], next: null })],
      ["an out-of-range account id", json({ accounts: [{ userId: "9223372036854775808", name: "Deleted account", items: [] }], next: null })],
      ["an account id that is not a path id", json({ accounts: [{ userId: "4/../5?x=1", name: "Deleted account", items: [] }], next: null })],
      ["an account with no name", json({ accounts: [{ userId: "41", items: [] }], next: null })],
      ["an account with no items", json({ accounts: [{ userId: "41", name: "Deleted account" }], next: null })],
      ["an empty discovery item", json({ accounts: [{ userId: "41", name: "Deleted account", items: [{}] }], next: null })],
      ["a null discovery item", json({ accounts: [{ userId: "41", name: "Deleted account", items: [null] }], next: null })],
      ["a numeric discovery request id", json({ accounts: [{ userId: "41", name: "Deleted account", items: [{ requestId: 5 }] }], next: null })],
      ["a zero-padded discovery request id", json({ accounts: [{ userId: "41", name: "Deleted account", items: [{ requestId: "007" }] }], next: null })],
      ["an out-of-range discovery request id", json({ accounts: [{ userId: "41", name: "Deleted account", items: [{ requestId: "-9223372036854775809" }] }], next: null })],
    ])("does not read %s as an empty list", async (_name, answer) => {
      stub(answer);
      await expect(AdminUsers.legacyReasons()).rejects.toBeInstanceOf(ApiError);
    });

    it("keeps signed boundary ids as strings", async () => {
      const unit = { accounts: [{ userId: "9223372036854775807", name: "Deleted account", items: [{ requestId: "-9223372036854775808" }] }], next: "-9223372036854775808" };
      stub(json(unit));
      await expect(AdminUsers.legacyReasons()).resolves.toEqual(unit);
    });
  });

  describe("legacyReasonsProbe", () => {
    it("asks with a GET and returns found", async () => {
      const fetcher = stub(json({ found: true }));
      await expect(AdminUsers.legacyReasonsProbe()).resolves.toEqual({ found: true });
      expect(String(sent(fetcher)[0][0])).toBe("/api/v1/admin/erasure-reasons/legacy/probe");
      stub(json({ found: false }));
      await expect(AdminUsers.legacyReasonsProbe()).resolves.toEqual({ found: false });
    });

    it.each([
      ["an empty body", () => new Response("", { status: 200 })],
      ["an object with no found", json({})],
      ["a string for found", json({ found: "false" })],
      ["null for found", json({ found: null })],
    ])("does not read %s as found: false", async (_name, answer) => {
      stub(answer);
      await expect(AdminUsers.legacyReasonsProbe()).rejects.toBeInstanceOf(ApiError);
    });
  });

  describe("legacyReasonsPage", () => {
    const item: ErasureReasonItem = {
      requestId: "-9223372036854775808", reviewHash: HASH, role: "requester", purpose: "contact", status: "granted",
      createdAt: "2026-11-01T12:00:00Z", counterpartName: "Ana", counterpartDeleted: false, partyRef: "41",
      reviewerIsParty: false, reason: "Needs a ride.",
    };

    it("posts the cursor in the body to the account's own path, string or number", async () => {
      const page = { reviewRequired: true, items: [item], next: "-9223372036854775808" };
      const fetcher = stub(json(page));
      await expect(AdminUsers.legacyReasonsPage("41", null)).resolves.toEqual(page);
      await AdminUsers.legacyReasonsPage(41, "-5");
      const calls = sent(fetcher);
      expect(calls.map(([url, opts]) => [String(url), opts?.method])).toEqual([
        ["/api/v1/admin/erasure-reasons/legacy/41/page", "POST"],
        ["/api/v1/admin/erasure-reasons/legacy/41/page", "POST"],
      ]);
      expect(calls.map((c) => bodyOf(c))).toEqual([{ after: null }, { after: "-5" }]);
    });

    it.each(["4/../5?x=1", "041", "0", "-1", "", "abc", "1.5", "1e3", " 41", "41 ", "+41", "9223372036854775808"])(
      "never reads another account for %j: no HTTP request is sent",
      async (id) => {
        const fetcher = stub(json({ items: [], next: null }));
        await expect(AdminUsers.legacyReasonsPage(id, null)).rejects.toThrow("not a usable account id");
        expect(fetcher).not.toHaveBeenCalled();
      },
    );

    it.each([0, -1, 1.5, Number.NaN, Number.POSITIVE_INFINITY, 2 ** 53])(
      "never reads another account for the number %s: no HTTP request is sent",
      async (id) => {
        const fetcher = stub(json({ items: [], next: null }));
        await expect(AdminUsers.legacyReasonsPage(id, null)).rejects.toThrow("not a usable account id");
        expect(fetcher).not.toHaveBeenCalled();
      },
    );

    it("keeps a full-width account id as the exact string in the path", async () => {
      const fetcher = stub(json({ items: [], next: null }));
      await AdminUsers.legacyReasonsPage("9223372036854775807", null);
      expect(String(sent(fetcher)[0][0])).toBe("/api/v1/admin/erasure-reasons/legacy/9223372036854775807/page");
    });

    it("accepts an empty page that omits reviewRequired", async () => {
      stub(json({ items: [], next: null }));
      await expect(AdminUsers.legacyReasonsPage(41, null)).resolves.toEqual({ items: [], next: null });
    });

    it("accepts reviewRequired with no items: a found unit whose reasons blanked since", async () => {
      stub(json({ reviewRequired: true, items: [], next: "7" }));
      await expect(AdminUsers.legacyReasonsPage(41, null)).resolves.toEqual({ reviewRequired: true, items: [], next: "7" });
    });

    it.each([
      ["an empty body", () => new Response("", { status: 200 })],
      ["JSON with no items", json({ next: null })],
      ["no cursor field", json({ items: [] })],
      ["an empty cursor", json({ items: [], next: "" })],
      ["a zero-padded cursor", json({ items: [], next: "007" })],
      ["a fractional cursor", json({ items: [], next: "1.5" })],
      ["an out-of-range cursor", json({ items: [], next: "9223372036854775808" })],
      ["an empty item", json({ items: [{}], next: null })],
      ["a null item", json({ items: [null], next: null })],
      ["a numeric request id", json({ items: [{ ...item, requestId: 5 }], next: null })],
      ["a zero-padded request id", json({ items: [{ ...item, requestId: "007" }], next: null })],
      ["an out-of-range request id", json({ items: [{ ...item, requestId: "9223372036854775808" }], next: null })],
      ["no review hash", json({ items: [{ ...item, reviewHash: undefined }], next: null })],
      ["a short hash", json({ items: [{ ...item, reviewHash: "abc" }], next: null })],
      ["an uppercase hash", json({ items: [{ ...item, reviewHash: "A".repeat(64) }], next: null })],
      ["an unknown role", json({ items: [{ ...item, role: "admin" }], next: null })],
      ["a numeric purpose", json({ items: [{ ...item, purpose: 5 }], next: null })],
      ["no counterpartDeleted flag", json({ items: [{ ...item, counterpartDeleted: undefined }], next: null })],
      ["no reviewerIsParty flag", json({ items: [{ ...item, reviewerIsParty: undefined }], next: null })],
      ["no partyRef", json({ items: [{ ...item, partyRef: undefined }], next: null })],
      ["a numeric partyRef", json({ items: [{ ...item, partyRef: 41 }], next: null })],
      ["a zero partyRef", json({ items: [{ ...item, partyRef: "0" }], next: null })],
      ["a malformed counterpartRef", json({ items: [{ ...item, counterpartDeleted: true, counterpartRef: "abc" }], next: null })],
      ["a non-boolean reviewRequired", json({ reviewRequired: "yes", items: [], next: null })],
    ])("does not read %s as an empty page", async (_name, answer) => {
      stub(answer);
      await expect(AdminUsers.legacyReasonsPage(41, null)).rejects.toBeInstanceOf(ApiError);
    });

    it("passes a refusal through with its status and code", async () => {
      stub(json({ error: "only an adult admin can review these reasons", code: "reason_review_not_allowed" }, 403));
      await expect(AdminUsers.legacyReasonsPage(41, null)).rejects.toMatchObject({ status: 403, code: "reason_review_not_allowed" });
    });
  });

  describe("saveLegacyReasons", () => {
    const decisions: LegacyReasonDecision[] = [
      { requestId: "-9223372036854775808", reviewHash: HASH, choice: "remove" },
      { requestId: "12", reviewHash: "b".repeat(64), choice: "keep" },
    ];

    it("puts the id and the decisions in the body, never the path, and returns the counts", async () => {
      const fetcher = stub(json({ kept: 1, removed: 1 }));
      await expect(AdminUsers.saveLegacyReasons("41", ID, decisions)).resolves.toEqual({ kept: 1, removed: 1 });
      const [call] = sent(fetcher);
      expect(String(call[0])).toBe("/api/v1/admin/erasure-reasons/legacy/41");
      expect((call[1] as RequestInit).method).toBe("PUT");
      expect(JSON.stringify(call[0])).not.toContain(ID);
      expect(bodyOf(call)).toEqual({ attemptId: ID, decisions });
      expect(Object.keys(bodyOf(call)).sort()).toEqual(["attemptId", "decisions"]);
      expect(((call[1] as RequestInit).headers as Record<string, string>)["X-CSRF-Token"]).toBe("csrf");
    });

    it.each([
      ["an HTML page", () => new Response("<html>proxy</html>", { status: 200, headers: { "Content-Type": "text/html" } })],
      ["an empty body", () => new Response("", { status: 200 })],
      ["JSON with no counts", json({})],
      ["one count only", json({ kept: 1 })],
      ["counts as strings", json({ kept: "1", removed: "0" })],
      ["a negative count", json({ kept: -1, removed: 0 })],
      ["a fractional count", json({ kept: 0.5, removed: 0 })],
    ])("does not call a save answered with %s a success, and leaves it uncertain", async (_name, answer) => {
      stub(answer);
      const err = await AdminUsers.saveLegacyReasons(41, ID, decisions).catch((e: unknown) => e);
      expect(err).toBeInstanceOf(Error);
      // A plain Error, not an ApiError: the caller cannot tell it from a lost
      // answer, so it settles the id instead of saying nothing was saved.
      expect(err).not.toBeInstanceOf(ApiError);
    });

    it.each(["4/../5?x=1", "041", "0", "-1", "", "abc", "1.5", "1e3", " 41", "41 ", "+41", "9223372036854775808"])(
      "never saves for %j: no HTTP request is sent",
      async (id) => {
        const fetcher = stub(json({ kept: 1, removed: 1 }));
        await expect(AdminUsers.saveLegacyReasons(id, ID, decisions)).rejects.toThrow("not a usable account id");
        expect(fetcher).not.toHaveBeenCalled();
      },
    );

    it.each([0, -1, 1.5, Number.NaN, Number.POSITIVE_INFINITY, 2 ** 53])(
      "never saves for the number %s: no HTTP request is sent",
      async (id) => {
        const fetcher = stub(json({ kept: 1, removed: 1 }));
        await expect(AdminUsers.saveLegacyReasons(id, ID, decisions)).rejects.toThrow("not a usable account id");
        expect(fetcher).not.toHaveBeenCalled();
      },
    );

    it("keeps a full-width account id as the exact string in the save path", async () => {
      const fetcher = stub(json({ kept: 0, removed: 1 }));
      await AdminUsers.saveLegacyReasons("9223372036854775807", ID, decisions);
      expect(String(sent(fetcher)[0][0])).toBe("/api/v1/admin/erasure-reasons/legacy/9223372036854775807");
    });

    it.each([
      [409, "reason_review_changed"],
      [409, "legacy_save_busy"],
      [409, "admin_account_busy"],
      [409, "attempt_closed"],
      [403, "reason_review_not_allowed"],
      [503, "legacy_save_timed_out"],
      [503, "restore_detected"],
      [500, "legacy_save_outcome_unknown"],
    ])("passes a %i %s answer through as an ApiError", async (status, code) => {
      stub(json({ error: "x", code }, status));
      await expect(AdminUsers.saveLegacyReasons(41, ID, decisions)).rejects.toMatchObject({ status, code, unreadable: false });
    });

    it("flags a failure with no readable body, so the caller treats it as uncertain", async () => {
      stub(() => new Response("<html>bad gateway</html>", { status: 502 }));
      await expect(AdminUsers.saveLegacyReasons(41, ID, decisions)).rejects.toMatchObject({ status: 502, unreadable: true });
    });
  });

  describe("attempt kinds", () => {
    it("mints and settles a legacy save for the deleted account, the id still in the body", async () => {
      const fetcher = stub((() => {
        let n = 0;
        return () => (n++ === 0 ? new Response(JSON.stringify({ attemptId: ID }), { status: 200 }) : new Response(JSON.stringify({ result: "not_applied" }), { status: 200 }));
      })());
      await expect(AdminUsers.mintAttempt("41", "legacy_reason_save")).resolves.toEqual({ attemptId: ID });
      await expect(AdminUsers.settleAttempt(ID, "41", "legacy_reason_save")).resolves.toEqual({ result: "not_applied" });
      const [mint, settle] = sent(fetcher);
      expect(String(mint[0])).toBe("/api/v1/admin/operation-attempts");
      expect(bodyOf(mint)).toEqual({ kind: "legacy_reason_save", subjectUserId: 41 });
      expect(String(settle[0])).toBe("/api/v1/admin/operation-attempts/settle");
      expect(bodyOf(settle)).toEqual({ attemptId: ID, kind: "legacy_reason_save", subjectUserId: 41 });
    });

    it("keeps the deletion kind when no kind is passed", async () => {
      const fetcher = stub(json({ result: "applied" }));
      await AdminUsers.settleAttempt(ID, 9);
      expect(bodyOf(sent(fetcher)[0])).toEqual({ attemptId: ID, kind: "account_delete", subjectUserId: 9 });
    });

    it("validates a mint for the legacy kind exactly as for a deletion", async () => {
      stub(json({ attemptId: "abc" }));
      await expect(AdminUsers.mintAttempt(41, "legacy_reason_save")).rejects.toThrow();
    });

    it.each(["9007199254740993", "9223372036854775807", "041", "0", "-1", "1.5", "1e3", " 41", "41 ", "", "abc"])(
      "never sends an account id that does not survive as a JSON number: %j",
      async (id) => {
        const fetcher = stub(json({ attemptId: ID }));
        await expect(AdminUsers.mintAttempt(id, "legacy_reason_save")).rejects.toThrow("not a usable account id");
        await expect(AdminUsers.settleAttempt(ID, id, "legacy_reason_save")).rejects.toThrow("not a usable account id");
        expect(fetcher).not.toHaveBeenCalled();
      },
    );

    it.each([0, -1, 1.5, Number.NaN, 2 ** 53])("refuses the number %s as a subject", async (id) => {
      const fetcher = stub(json({ attemptId: ID }));
      await expect(AdminUsers.mintAttempt(id)).rejects.toThrow("not a usable account id");
      expect(fetcher).not.toHaveBeenCalled();
    });
  });
});
