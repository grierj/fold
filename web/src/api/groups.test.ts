import { afterEach, expect, it, vi } from "vitest";
import { Groups } from "./groups";

afterEach(() => vi.unstubAllGlobals());

const stub = () => {
  const fetcher = vi.fn(async () => new Response(JSON.stringify({ csrfToken: "csrf" })));
  vi.stubGlobal("fetch", fetcher);
  return fetcher;
};

it("sends an add's opaque selection with the over-limit choice", async () => {
  const fetcher = stub();
  await Groups.addMember(3, { selectionId: "opaque" }, true);
  const post = (fetcher.mock.calls as unknown as [string, RequestInit][]).find(([, opts]) => opts?.method === "POST");
  expect(JSON.parse(post![1].body as string)).toEqual({ selectionId: "opaque", overLimit: true });
});

it("builds the list and page queries from only what is set", async () => {
  const fetcher = stub();
  await Groups.list("", []);
  await Groups.list("lake view", ["youth", "", "music"]);
  await Groups.get(3);
  await Groups.get(3, { cursor: "opaque+/=", limit: 100 });
  expect((fetcher.mock.calls as unknown as [string][]).map(([url]) => url)).toEqual([
    "/api/v1/groups",
    "/api/v1/groups?search=lake%20view&tag=youth&tag=music",
    "/api/v1/groups/3",
    "/api/v1/groups/3?cursor=opaque%2B%2F%3D&limit=100",
  ]);
});
