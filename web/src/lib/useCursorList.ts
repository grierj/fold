import { useCallback, useEffect, useRef, useState } from "react";
import { errorText } from "./errors";

// The server's page size for the lists read by exclusive `before` cursor
// (add requests, contact requests and grants). A full page means there may be
// more; a shorter one is the end. The server sends no `more` flag, so this
// mirrors its limit in one place.
const CURSOR_PAGE_SIZE = 100;

interface CursorState<T> {
  items: T[];
  loaded: boolean;
  more: boolean;
  error: string;
  busy: boolean;
}

// useCursorList reads a list newest-first by `before` id: the first page on
// mount, again whenever the window regains focus or `refresh` is called, and
// older pages on `older`. A generation counter drops every answer that
// arrives after a refresh or unmount, so a slow page never lands on a newer
// list. A refresh starts from an empty, unloaded list (and `revision` changes,
// for keying rows that hold their own state).
export function useCursorList<T extends { id: number }>(fetchPage: (before?: number) => Promise<T[]>) {
  const [state, setState] = useState<CursorState<T>>({ items: [], loaded: false, more: false, error: "", busy: false });
  const [revision, setRevision] = useState(0);
  const generation = useRef(0);
  const latest = useRef({ fetchPage, state });
  latest.current = { fetchPage, state };

  const refresh = useCallback(() => {
    generation.current++;
    setRevision((n) => n + 1);
  }, []);

  useEffect(() => {
    const current = ++generation.current;
    setState({ items: [], loaded: false, more: false, error: "", busy: false });
    latest.current.fetchPage().then(
      (rows) => { if (current === generation.current) setState({ items: rows, loaded: true, more: rows.length === CURSOR_PAGE_SIZE, error: "", busy: false }); },
      (e: unknown) => { if (current === generation.current) setState((s) => ({ ...s, error: errorText(e) })); },
    );
    window.addEventListener("focus", refresh);
    return () => { generation.current++; window.removeEventListener("focus", refresh); };
  }, [revision, refresh]);

  const older = useCallback(async () => {
    const { fetchPage: fetch, state: s } = latest.current;
    const cursor = s.items[s.items.length - 1]?.id;
    if (!s.loaded || !s.more || s.busy || cursor === undefined) return;
    const current = generation.current;
    setState((old) => ({ ...old, busy: true, error: "" }));
    try {
      const rows = await fetch(cursor);
      if (current !== generation.current) return;
      setState((old) => ({ ...old, items: [...old.items, ...rows], more: rows.length === CURSOR_PAGE_SIZE, busy: false }));
    } catch (e) {
      if (current === generation.current) setState((old) => ({ ...old, error: errorText(e), busy: false }));
    }
  }, []);

  return { ...state, revision, refresh, older };
}
