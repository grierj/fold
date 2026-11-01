import { describe, expect, it, vi } from "vitest";
import { fallback, fromPush, keyBytes, openOrFocus, resubscribe, sameOrigin } from "./logic";

const origin = "https://church.example";
const payload = (n: Record<string, unknown>) => JSON.stringify({ web_push: 8030, notification: n });

describe("fromPush", () => {
  it("shows the Declarative Web Push notification", () => {
    const shown = fromPush(payload({ title: "Event updates", body: "Tuesday is cancelled", tag: "n12", navigate: `${origin}/events/4`, app_badge: 3 }), origin);
    expect(shown.title).toBe("Event updates");
    expect(shown.options).toMatchObject({ body: "Tuesday is cancelled", tag: "n12", data: { navigate: `${origin}/events/4` } });
    expect(shown.appBadge).toBe(3);
  });

  it("still shows something when the push can't be read, so Safari keeps the subscription", () => {
    for (const text of [null, "", "not json", "[]", JSON.stringify({ web_push: 8030 }), payload({ title: "" }), payload({ title: 4 })]) {
      expect(fromPush(text, origin)).toEqual(fallback(origin));
    }
    expect(fallback(origin).title).toBe("Open Fold");
    expect(fallback(origin).options.data.navigate).toBe(`${origin}/`);
  });

  it("never links off the worker's origin and drops a nonsense badge", () => {
    const shown = fromPush(payload({ title: "Hi", navigate: "https://evil.example/x", app_badge: -2 }), origin);
    expect(shown.options.data.navigate).toBe(`${origin}/`);
    expect(shown.appBadge).toBeUndefined();
    expect(shown.options.body).toBeUndefined();
  });
});

describe("sameOrigin", () => {
  it("keeps links on this origin and turns everything else into the home page", () => {
    expect(sameOrigin(`${origin}/groups/1?x=1`, origin)).toBe(`${origin}/groups/1?x=1`);
    expect(sameOrigin("/profile/notifications", origin)).toBe(`${origin}/profile/notifications`);
    for (const bad of ["https://church.example.evil.example/", "javascript:alert(1)", "//evil.example/x", "http://church.example/", 42, undefined, ""]) {
      expect(sameOrigin(bad, origin)).toBe(`${origin}/`);
    }
  });
});

describe("openOrFocus", () => {
  const win = (url: string, navigate = vi.fn(async () => undefined)) => ({ url, focus: vi.fn(async () => undefined), navigate });

  it("focuses an open Fold window and takes it to the link", async () => {
    const other = win("https://other.example/");
    const mine = win(`${origin}/groups/1`);
    const open = vi.fn(async () => undefined);
    await openOrFocus(`${origin}/events/2`, origin, [other, mine], open);
    expect(mine.focus).toHaveBeenCalled();
    expect(mine.navigate).toHaveBeenCalledWith(`${origin}/events/2`);
    expect(other.focus).not.toHaveBeenCalled();
    expect(open).not.toHaveBeenCalled();
  });

  it("opens a window when none is open, or the open one won't navigate", async () => {
    const open = vi.fn(async () => undefined);
    await openOrFocus(`${origin}/`, origin, [], open);
    expect(open).toHaveBeenCalledWith(`${origin}/`);
    const stuck = win(`${origin}/`, vi.fn(async () => { throw new TypeError("not controlled"); }));
    await openOrFocus(`${origin}/x`, origin, [stuck], open);
    expect(open).toHaveBeenLastCalledWith(`${origin}/x`);
  });
});

it("decodes the base64url key", () => {
  expect([...keyBytes("AQID_-8")]).toEqual([1, 2, 3, 255, 239]);
});

describe("resubscribe", () => {
  const sub = { endpoint: "https://fcm.googleapis.com/fcm/send/new", toJSON: () => ({ endpoint: "https://fcm.googleapis.com/fcm/send/new", keys: { p256dh: "p", auth: "a" } }) } as unknown as PushSubscription;
  const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status });
  function fetcher(config: unknown, saved = true) {
    return vi.fn(async (url: RequestInfo | URL) => {
      if (url === "/api/v1/auth/csrf") return json({ csrfToken: "tok" });
      if (url === "/api/v1/push/config") return config === 401 ? json({ error: "unauthorized" }, 401) : json(config);
      return json({ saved });
    });
  }

  it("gets a token, reads the key, subscribes again and saves it", async () => {
    const fetch = fetcher({ enabled: true, publicKey: "AQID" });
    const subscribe = vi.fn(async () => sub);
    expect(await resubscribe({ fetch, subscribe }, null)).toBe(true);
    expect([...(subscribe.mock.calls[0] as unknown as [Uint8Array])[0]]).toEqual([1, 2, 3]);
    const put = fetch.mock.calls[2] as unknown as [string, RequestInit];
    expect(put[0]).toBe("/api/v1/push/subscriptions");
    expect(put[1]).toMatchObject({ method: "PUT", headers: { "X-CSRF-Token": "tok" } });
    expect(JSON.parse(put[1].body as string).endpoint).toBe(sub.endpoint);
  });

  it("uses the subscription the browser handed over", async () => {
    const subscribe = vi.fn();
    expect(await resubscribe({ fetch: fetcher({ enabled: true, publicKey: "AQID" }), subscribe }, sub)).toBe(true);
    expect(subscribe).not.toHaveBeenCalled();
  });

  it("saves nothing when signed out or when the church turned notifications off", async () => {
    const subscribe = vi.fn(async () => sub);
    expect(await resubscribe({ fetch: fetcher(401), subscribe }, null)).toBe(false);
    expect(await resubscribe({ fetch: fetcher({ enabled: false, publicKey: "AQID" }), subscribe }, null)).toBe(false);
    expect(subscribe).not.toHaveBeenCalled();
  });
});
