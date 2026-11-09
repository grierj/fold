import { act, renderHook, waitFor } from "@testing-library/react";
import { expect, it, vi } from "vitest";
import { useLoad } from "./useLoad";

it("loads on mount and reports the answer", async () => {
  const { result } = renderHook(() => useLoad(async () => 7, []));
  expect(result.current.loading).toBe(true);
  await waitFor(() => expect(result.current.data).toBe(7));
  expect(result.current.loading).toBe(false);
  expect(result.current.error).toBeNull();
});

it("drops an answer that arrives after the inputs changed", async () => {
  const slow: Record<string, (v: string) => void> = {};
  const fn = (key: string) => new Promise<string>((done) => { slow[key] = done; });
  const { result, rerender } = renderHook(({ key }) => useLoad(() => fn(key), [key]), { initialProps: { key: "a" } });
  rerender({ key: "b" });
  await act(async () => slow.b("B"));
  await act(async () => slow.a("A"));
  expect(result.current.data).toBe("B");
});

it("ignores an answer that arrives after unmount", async () => {
  let done!: (v: number) => void;
  const { result, unmount } = renderHook(() => useLoad(() => new Promise<number>((r) => { done = r; }), []));
  unmount();
  await act(async () => done(1));
  expect(result.current.data).toBeUndefined();
});

it("retry shows loading again; reload keeps the data and clears the error as it starts", async () => {
  const fn = vi.fn().mockRejectedValueOnce(new Error("x")).mockResolvedValueOnce(1).mockRejectedValueOnce(new Error("y"));
  const { result } = renderHook(() => useLoad(fn, []));
  await waitFor(() => expect(result.current.error).toBeTruthy());
  act(() => { void result.current.retry(); });
  expect(result.current.loading).toBe(true);
  await waitFor(() => expect(result.current.data).toBe(1));
  act(() => { void result.current.reload(); });
  expect(result.current.loading).toBe(false);
  expect(result.current.data).toBe(1);
  await waitFor(() => expect(result.current.error).toBeTruthy());
  expect(result.current.data).toBe(1);
});

it("loads nothing while disabled", async () => {
  const fn = vi.fn().mockResolvedValue(1);
  const { result, rerender } = renderHook(({ on }) => useLoad(fn, [], { enabled: on }), { initialProps: { on: false } });
  expect(result.current.loading).toBe(false);
  expect(fn).not.toHaveBeenCalled();
  rerender({ on: true });
  await waitFor(() => expect(result.current.data).toBe(1));
});

it("a reload batched with new inputs does not keep the old inputs' answer", async () => {
  const fn = (key: string) => (key === "a" ? Promise.resolve("A") : Promise.reject(new Error("b failed")));
  const { result, rerender } = renderHook(({ key }) => useLoad(() => fn(key), [key]), { initialProps: { key: "a" } });
  await waitFor(() => expect(result.current.data).toBe("A"));
  act(() => {
    void result.current.reload();
    rerender({ key: "b" });
  });
  await waitFor(() => expect(result.current.error).toBeTruthy());
  expect(result.current.data).toBeUndefined();
});

it("retry drops the old answer in the same render and says how the attempt ended", async () => {
  const fn = vi.fn().mockResolvedValueOnce("old").mockResolvedValueOnce("new").mockRejectedValueOnce(new Error("x"));
  const { result } = renderHook(() => useLoad(fn, []));
  await waitFor(() => expect(result.current.data).toBe("old"));
  let outcome: unknown;
  let asked: Promise<unknown> = Promise.resolve();
  act(() => { asked = result.current.retry(); });
  expect(result.current.data).toBeUndefined();
  await act(async () => { outcome = await asked; });
  expect(outcome).toBe("loaded");
  expect(result.current.data).toBe("new");
  act(() => { asked = result.current.reload(); });
  await act(async () => { outcome = await asked; });
  expect(outcome).toBe("failed");
  expect(result.current.data).toBe("new");
});

it("an ask that something newer replaced ends stale", async () => {
  const slow: Array<(v: number) => void> = [];
  const { result } = renderHook(() => useLoad(() => new Promise<number>((r) => { slow.push(r); }), []));
  await waitFor(() => expect(slow).toHaveLength(1));
  let first: Promise<unknown> = Promise.resolve();
  act(() => { first = result.current.retry(); });
  await waitFor(() => expect(slow).toHaveLength(2));
  act(() => { void result.current.retry(); });
  await waitFor(() => expect(slow).toHaveLength(3));
  await act(async () => { slow[1](2); slow[2](3); });
  expect(await first).toBe("stale");
  expect(result.current.data).toBe(3);
});

it("the epoch moves with every ask, close and change of inputs", async () => {
  const { result, rerender } = renderHook(({ key }) => useLoad(async () => key, [key]), { initialProps: { key: "a" } });
  await waitFor(() => expect(result.current.data).toBe("a"));
  let stamp = result.current.epoch();
  act(() => { void result.current.reload(); });
  expect(result.current.epoch()).not.toBe(stamp);
  await waitFor(() => expect(result.current.loading).toBe(false));
  stamp = result.current.epoch();
  rerender({ key: "b" });
  expect(result.current.epoch()).not.toBe(stamp);
  await waitFor(() => expect(result.current.data).toBe("b"));
  stamp = result.current.epoch();
  act(() => result.current.close());
  expect(result.current.epoch()).not.toBe(stamp);
});

it("close drops the answer and a request in flight, and stops loading", async () => {
  let done!: (v: number) => void;
  const { result } = renderHook(() => useLoad(() => new Promise<number>((r) => { done = r; }), []));
  act(() => result.current.close());
  expect(result.current.loading).toBe(false);
  await act(async () => done(1));
  expect(result.current.data).toBeUndefined();
  expect(result.current.loading).toBe(false);
});

it("refreshOnFocus fails closed and asks again when the window regains focus", async () => {
  const fn = vi.fn().mockResolvedValueOnce("a").mockResolvedValueOnce("b");
  const { result, unmount } = renderHook(() => useLoad(fn, [], { refreshOnFocus: true }));
  await waitFor(() => expect(result.current.data).toBe("a"));
  act(() => { window.dispatchEvent(new Event("focus")); });
  expect(result.current.data).toBeUndefined();
  await waitFor(() => expect(result.current.data).toBe("b"));
  unmount();
  window.dispatchEvent(new Event("focus"));
  expect(fn).toHaveBeenCalledTimes(2);
});
