import { afterEach, expect, it, vi } from "vitest";
import { Messages, previewHTMLPath } from "./messages-api";

afterEach(() => vi.unstubAllGlobals());

it("sends email drafts, schedules for a time or now, and sends CSRF on changes", async () => {
  const fetcher = vi.fn(async (url: string) => new Response(JSON.stringify(url.endsWith("/csrf") ? { csrfToken: "t" } : {})));
  vi.stubGlobal("fetch", fetcher);
  const draft = { subject: "Hi", bodyHtml: "<p>Hi</p>", audience: { type: "group" as const, groupId: 7 }, pushAlert: true };
  await Messages.options();
  await Messages.options(7);
  await Messages.create(draft);
  await Messages.update(5, draft);
  await Messages.schedule(5, "2026-11-05T16:00:00.000Z");
  await Messages.schedule(5);
  await Messages.test(5);
  const calls = fetcher.mock.calls as unknown as [string, RequestInit][];
  const api = calls.filter(([url]) => !url.endsWith("/csrf"));
  expect(api.map(([url, o]) => `${o.method} ${url}`)).toEqual([
    "GET /api/v1/messages/compose", "GET /api/v1/messages/compose?event=7", "POST /api/v1/messages", "PUT /api/v1/messages/5",
    "POST /api/v1/messages/5/schedule", "POST /api/v1/messages/5/schedule", "POST /api/v1/messages/5/test",
  ]);
  expect(JSON.parse(api[2][1].body as string)).toEqual({ channel: "email", ...draft });
  expect(JSON.parse(api[4][1].body as string)).toEqual({ scheduledAt: "2026-11-05T16:00:00.000Z" });
  expect(JSON.parse(api[5][1].body as string)).toEqual({});
  for (const [, o] of api.slice(2)) expect(o.headers).toMatchObject({ "X-CSRF-Token": "t" });
  expect(previewHTMLPath(5)).toBe("/api/v1/messages/5/preview.html");
});
