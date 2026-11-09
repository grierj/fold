import { act, renderHook, waitFor } from "@testing-library/react";
import { beforeEach, expect, it, vi } from "vitest";
import { churchName, invalidateSite, useChurchZoneLoad, usePushEnabled, useRoomsEnabled, useSite } from "./useSite";

const { get } = vi.hoisted(() => ({ get: vi.fn() }));
vi.mock("../api/site", () => ({ Site: { get } }));
beforeEach(() => get.mockReset());

const site = { name: "Grace Church", timezone: "America/Denver", selfSignup: "request", roomsEnabled: true, pushEnabled: true };

it("shares one read across every reader and later mounts", async () => {
  get.mockResolvedValue(site);
  const first = renderHook(useSite);
  const second = renderHook(useRoomsEnabled);
  expect(first.result.current.loading).toBe(true);
  expect(second.result.current).toBeNull();
  await waitFor(() => expect(first.result.current.site?.name).toBe("Grace Church"));
  expect(second.result.current).toBe(true);
  // A later mount knows at once, without asking again.
  expect(renderHook(useSite).result.current.site?.timezone).toBe("America/Denver");
  expect(get).toHaveBeenCalledTimes(1);
});

it("fails safe: rooms off, push off, zone unknown but answered, and asks again next mount", async () => {
  get.mockRejectedValueOnce(new Error("offline"));
  const rooms = renderHook(useRoomsEnabled);
  const push = renderHook(usePushEnabled);
  const zone = renderHook(useChurchZoneLoad);
  await waitFor(() => expect(rooms.result.current).toBe(false));
  expect(push.result.current).toBe(false);
  expect(zone.result.current).toEqual({ zone: null, done: true });
  get.mockResolvedValue(site);
  const again = renderHook(useRoomsEnabled);
  await waitFor(() => expect(again.result.current).toBe(true));
  expect(get).toHaveBeenCalledTimes(2);
});

it("retries on request, and stays quiet instead of looping after a failure", async () => {
  get.mockRejectedValueOnce(new Error("offline")).mockResolvedValue(site);
  const { result } = renderHook(useSite);
  await waitFor(() => expect(result.current.error).toBeTruthy());
  expect(get).toHaveBeenCalledTimes(1);
  act(() => result.current.retry());
  await waitFor(() => expect(result.current.site?.name).toBe("Grace Church"));
  expect(result.current.error).toBeNull();
});

it("keeps the shown answer while a saved setting is re-read, with the saved values at once", async () => {
  get.mockResolvedValue(site);
  const { result } = renderHook(useSite);
  await waitFor(() => expect(result.current.site).not.toBeNull());
  let finish!: (s: typeof site) => void;
  get.mockReturnValue(new Promise((done) => { finish = done; }));
  act(() => invalidateSite({ roomsEnabled: false }));
  expect(result.current.site?.roomsEnabled).toBe(false);
  expect(result.current.loading).toBe(false);
  await act(async () => finish({ ...site, roomsEnabled: false, timezone: "UTC" }));
  expect(result.current.site?.timezone).toBe("UTC");
});

it("treats the default name as no church name", () => {
  expect(churchName({ name: "Fold" })).toBe("");
  expect(churchName({ name: "Grace" })).toBe("Grace");
  expect(churchName(null)).toBe("");
});
