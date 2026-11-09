import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiError, api, apiCSV, apiIfChanged, apiUpload, configureApi, path, query } from "./core";

afterEach(() => {
  configureApi({ onUnauthorized: null });
  vi.unstubAllGlobals();
});

const csrfOr = (answer: (url: string, init?: RequestInit) => Response, token = () => "csrf") =>
  vi.fn(async (url: RequestInfo | URL, init?: RequestInit) =>
    String(url) === "/api/v1/auth/csrf" ? new Response(JSON.stringify({ csrfToken: token() })) : answer(String(url), init));

describe("path and query", () => {
  it("encodes every interpolated value as one path segment", () => {
    expect(path`/events/${"7/8?x"}/dates/${"2026-12-01/#?"}`).toBe("/events/7%2F8%3Fx/dates/2026-12-01%2F%23%3F");
    expect(path`/groups/${12}/members/${3}`).toBe("/groups/12/members/3");
  });

  it("builds a query string, leaving out empty values and repeating lists", () => {
    expect(query({ a: "x y", b: undefined, c: "", d: false, e: true, n: 0, tag: ["p", "", "q&r"] })).toBe("?a=x%20y&e=1&n=0&tag=p&tag=q%26r");
    expect(query({ a: undefined, b: null })).toBe("");
  });
});

describe("api", () => {
  it("sends the CSRF token on unsafe requests only, and always the cookie", async () => {
    const fetcher = csrfOr(() => new Response("{}"), () => "test-csrf");
    vi.stubGlobal("fetch", fetcher);
    await api("/things");
    await api("/things", { method: "POST", body: { a: 1 } });
    const calls = fetcher.mock.calls as unknown as [string, RequestInit][];
    expect(calls.map(([url]) => url)).toEqual(["/api/v1/things", "/api/v1/auth/csrf", "/api/v1/things"]);
    expect(calls[0][1].headers).not.toHaveProperty("X-CSRF-Token");
    expect(calls[2][1]).toMatchObject({ method: "POST", credentials: "include", body: '{"a":1}', headers: { "X-CSRF-Token": "test-csrf", "Content-Type": "application/json" } });
  });

  it("sends people to log in on a 401, except when the caller says anonymous is fine", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => new Response("{}", { status: 401 })));
    const onUnauthorized = vi.fn();
    configureApi({ onUnauthorized });
    await expect(api("/auth/me", { anonymousOK: true })).rejects.toMatchObject({ status: 401, code: "signed_out" });
    expect(onUnauthorized).not.toHaveBeenCalled();
    await expect(api("/me/groups")).rejects.toMatchObject({ status: 401, code: "signed_out" });
    expect(onUnauthorized).toHaveBeenCalledOnce();
  });

  it("keeps the server's own code on a 401 (a wrong password is not a sign-out)", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify({ error: "invalid email or password", code: "bad_credentials" }), { status: 401 })));
    await expect(api("/auth/login", { anonymousOK: true })).rejects.toMatchObject({ status: 401, code: "bad_credentials" });
  });

  it("fetches a fresh CSRF token after a request that rotates it", async () => {
    vi.resetModules();
    const { api } = await import("./core");
    let minted = 0;
    const sent: string[] = [];
    vi.stubGlobal("fetch", csrfOr((_url, init) => {
      sent.push((init?.headers as Record<string, string>)["X-CSRF-Token"]);
      return new Response("{}");
    }, () => `token-${++minted}`));
    await api("/a", { method: "POST", rotatesCsrf: true });
    await api("/b", { method: "POST" });
    expect(sent).toEqual(["token-1", "token-2"]);
  });

  it("only passes the status and an unreadable flag when the body is not a JSON object", async () => {
    const fail = async (response: Response) => {
      vi.stubGlobal("fetch", vi.fn(async () => response));
      return api("/x").catch((e: unknown) => e);
    };
    for (const make of [() => new Response("<html>bad gateway</html>", { status: 400 }), () => new Response("", { status: 400 }), () => new Response("[]", { status: 400 }), () => new Response("null", { status: 400 })]) {
      expect(await fail(make())).toMatchObject({ status: 400, unreadable: true });
    }
    expect(await fail(new Response(JSON.stringify({ error: "no", code: "last_admin" }), { status: 400 }))).toMatchObject({ status: 400, code: "last_admin", unreadable: false });
    expect(await fail(new Response("proxy", { status: 502 }))).toMatchObject({ status: 502, code: "server_error", unreadable: true });
  });

  it("reads a success body that is not JSON as an empty object", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => new Response("<html></html>")));
    expect(await api("/x")).toEqual({});
  });

  it("lets a network failure through untouched", async () => {
    vi.stubGlobal("fetch", vi.fn().mockRejectedValue(new TypeError("Network secret")));
    await expect(api("/x")).rejects.toBeInstanceOf(TypeError);
  });
});

describe("apiUpload", () => {
  const file = () => new File(["x"], "a.png", { type: "image/png" });

  it("posts multipart with the CSRF token and no JSON content type", async () => {
    const fetcher = csrfOr(() => new Response(JSON.stringify({ avatarUrl: "/u/1" })));
    vi.stubGlobal("fetch", fetcher);
    expect(await apiUpload("/me/avatar", "avatar", file())).toEqual({ avatarUrl: "/u/1" });
    const [, init] = fetcher.mock.calls[fetcher.mock.calls.length - 1] as unknown as [string, RequestInit];
    expect(init.body).toBeInstanceOf(FormData);
    expect(init.headers).toMatchObject({ "X-CSRF-Token": expect.any(String), Accept: "application/json" });
    expect(init.headers).not.toHaveProperty("Content-Type");
  });

  it("handles a failure like any other request: 401 signs out, an unreadable body is flagged", async () => {
    const onUnauthorized = vi.fn();
    configureApi({ onUnauthorized });
    vi.stubGlobal("fetch", csrfOr(() => new Response("{}", { status: 401 })));
    await expect(apiUpload("/me/avatar", "avatar", file())).rejects.toMatchObject({ status: 401, code: "signed_out" });
    expect(onUnauthorized).toHaveBeenCalledOnce();
    vi.stubGlobal("fetch", csrfOr(() => new Response("proxy", { status: 413 })));
    await expect(apiUpload("/me/avatar", "avatar", file())).rejects.toMatchObject({ status: 413, unreadable: true });
  });
});

describe("apiCSV", () => {
  it("asks for no-store CSV and returns the blob", async () => {
    const fetcher = vi.fn().mockResolvedValue(new Response("a,b\r\n", { headers: { "Content-Type": "text/csv; charset=utf-8" } }));
    vi.stubGlobal("fetch", fetcher);
    expect(await (await apiCSV("/x.csv")).text()).toBe("a,b\r\n");
    expect(fetcher.mock.calls[0][1]).toMatchObject({ credentials: "include", cache: "no-store", headers: { Accept: "text/csv" } });
  });

  it.each([400, 403, 500])("rejects a JSON failure %s without returning a blob", async (status) => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({ error: "SECRET", code: "server_error" }), { status })));
    await expect(apiCSV("/x.csv")).rejects.toMatchObject({ status });
  });

  it("shares signed-out handling and rejects successful JSON or proxy bodies", async () => {
    const onUnauthorized = vi.fn();
    configureApi({ onUnauthorized });
    vi.stubGlobal("fetch", vi.fn().mockResolvedValueOnce(new Response("{}", { status: 401 })).mockResolvedValueOnce(new Response("{}", { headers: { "Content-Type": "application/json" } })).mockResolvedValueOnce(new Response("proxy", { status: 502 })));
    await expect(apiCSV("/x.csv")).rejects.toMatchObject({ status: 401, code: "signed_out" });
    expect(onUnauthorized).toHaveBeenCalledOnce();
    await expect(apiCSV("/x.csv")).rejects.toBeInstanceOf(ApiError);
    await expect(apiCSV("/x.csv")).rejects.toMatchObject({ status: 502 });
  });
});

describe("apiIfChanged", () => {
  it("sends the last ETag, answers null on 304 and returns the new ETag otherwise", async () => {
    const fetcher = vi.fn().mockResolvedValueOnce(new Response(JSON.stringify({ unread: 2 }), { headers: { ETag: '"2-0"' } })).mockResolvedValueOnce(new Response(null, { status: 304 }));
    vi.stubGlobal("fetch", fetcher);
    expect(await apiIfChanged("/n", "")).toEqual({ data: { unread: 2 }, etag: '"2-0"' });
    expect(await apiIfChanged("/n", '"2-0"')).toBeNull();
    expect(fetcher.mock.calls[0][1].headers).not.toHaveProperty("If-None-Match");
    expect(fetcher.mock.calls[1][1].headers).toMatchObject({ "If-None-Match": '"2-0"' });
  });

  it("treats a 401 like every other 401", async () => {
    const onUnauthorized = vi.fn();
    configureApi({ onUnauthorized });
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response("{}", { status: 401 })));
    await expect(apiIfChanged("/n", "")).rejects.toMatchObject({ status: 401, code: "signed_out" });
    expect(onUnauthorized).toHaveBeenCalledOnce();
  });
});
