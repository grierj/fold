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
  act(() => result.current.retry());
  expect(result.current.loading).toBe(true);
  await waitFor(() => expect(result.current.data).toBe(1));
  act(() => result.current.reload());
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
    result.current.reload();
    rerender({ key: "b" });
  });
  await waitFor(() => expect(result.current.error).toBeTruthy());
  expect(result.current.data).toBeUndefined();
});
