import { act, renderHook, waitFor } from "@testing-library/react";
import { beforeEach, expect, it, vi } from "vitest";
import { useSiteName } from "./useSiteName";

const { get } = vi.hoisted(() => ({ get: vi.fn() }));
vi.mock("../api", () => ({ Site: { get } }));
beforeEach(() => { get.mockReset(); });

it("loads the church name", async () => {
  get.mockResolvedValue({ name: "Grace Church" });
  const { result } = renderHook(useSiteName);
  expect(result.current).toBeUndefined();
  await waitFor(() => expect(result.current).toBe("Grace Church"));
});

it("uses the empty fallback after a failed load", async () => {
  let reject!: (error: Error) => void;
  get.mockReturnValue(new Promise((_, fail) => { reject = fail; }));
  const { result } = renderHook(useSiteName);
  await act(async () => { reject(new Error("offline")); });
  expect(result.current).toBe("");
});

it("ignores a request completed after unmount", async () => {
  let resolve!: (site: { name: string }) => void;
  get.mockReturnValue(new Promise((done) => { resolve = done; }));
  const { result, unmount } = renderHook(useSiteName);
  unmount();
  await act(async () => resolve({ name: "Grace Church" }));
  expect(result.current).toBeUndefined();
});
