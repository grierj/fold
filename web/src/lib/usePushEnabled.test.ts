import { renderHook, waitFor } from "@testing-library/react";
import { beforeEach, expect, it, vi } from "vitest";
import { forgetPushEnabled, usePushEnabled } from "./usePushEnabled";

const { get } = vi.hoisted(() => ({ get: vi.fn() }));
vi.mock("../api", () => ({ Site: { get } }));
beforeEach(() => { get.mockReset(); forgetPushEnabled(); });

it("starts off and shares one /site read across mounts", async () => {
  get.mockResolvedValue({ pushEnabled: true });
  const first = renderHook(usePushEnabled);
  const second = renderHook(usePushEnabled);
  expect(first.result.current).toBe(false);
  await waitFor(() => expect(first.result.current).toBe(true));
  await waitFor(() => expect(second.result.current).toBe(true));
  // A later mount knows at once, without asking again.
  const third = renderHook(usePushEnabled);
  expect(third.result.current).toBe(true);
  expect(get).toHaveBeenCalledTimes(1);
});

it("stays off when the church turned push off", async () => {
  get.mockResolvedValue({ pushEnabled: false });
  const { result } = renderHook(usePushEnabled);
  await waitFor(() => expect(get).toHaveBeenCalled());
  await Promise.resolve();
  expect(result.current).toBe(false);
});

it("turns on after a failed read, and asks again next time", async () => {
  get.mockRejectedValueOnce(new Error("offline")).mockResolvedValue({ pushEnabled: false });
  const { result } = renderHook(usePushEnabled);
  await waitFor(() => expect(result.current).toBe(true));
  const again = renderHook(usePushEnabled);
  await waitFor(() => expect(get).toHaveBeenCalledTimes(2));
  await waitFor(() => expect(again.result.current).toBe(false));
});
