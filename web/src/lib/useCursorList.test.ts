import { act, renderHook, waitFor } from "@testing-library/react";
import { expect, it, vi } from "vitest";
import { useCursorList } from "./useCursorList";

// The server sends 100 rows a page.
const CURSOR_PAGE_SIZE = 100;

const rows = (from: number, n: number) => Array.from({ length: n }, (_, i) => ({ id: from - i }));

it("pages by the last id and stops on a short page", async () => {
  const page = vi.fn().mockResolvedValueOnce(rows(500, CURSOR_PAGE_SIZE)).mockResolvedValueOnce(rows(400, 3));
  const { result } = renderHook(() => useCursorList(page));
  await waitFor(() => expect(result.current.loaded).toBe(true));
  expect(result.current.more).toBe(true);
  await act(async () => { await result.current.older(); });
  expect(page).toHaveBeenLastCalledWith(401);
  expect(result.current.items).toHaveLength(CURSOR_PAGE_SIZE + 3);
  expect(result.current.more).toBe(false);
});

it("drops a page that arrives after a refresh, and refreshes when the window regains focus", async () => {
  let first!: (r: { id: number }[]) => void;
  const page = vi.fn().mockReturnValueOnce(new Promise((done) => { first = done; })).mockResolvedValue([{ id: 2 }]);
  const { result } = renderHook(() => useCursorList(page));
  act(() => { window.dispatchEvent(new Event("focus")); });
  await waitFor(() => expect(result.current.items).toEqual([{ id: 2 }]));
  await act(async () => first([{ id: 1 }]));
  expect(result.current.items).toEqual([{ id: 2 }]);
  expect(result.current.revision).toBe(1);
});

it("reports a failed first page and keeps the list unloaded", async () => {
  const page = vi.fn().mockRejectedValue(new TypeError("offline"));
  const { result } = renderHook(() => useCursorList(page));
  await waitFor(() => expect(result.current.error).not.toBe(""));
  expect(result.current.loaded).toBe(false);
});
