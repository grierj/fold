import { useCallback, useEffect, useRef, useState, type DependencyList } from "react";

export interface Load<T> {
  // The latest answer, undefined until the first one arrives (and again
  // whenever `deps` change, so a stale answer never shows under new inputs).
  data: T | undefined;
  // The error of the latest attempt, or null.
  error: unknown;
  // True from a start (mount, deps change, retry) until its answer.
  loading: boolean;
  // Ask again and show the loading state: for "Try again" after an error.
  retry: () => void;
  // Ask again without a loading state: keeps showing `data` meanwhile, and
  // keeps it if this attempt fails (error is set so the caller can say so).
  // The previous error is cleared as the attempt starts.
  reload: () => void;
  // Replace the answer after a local change, without asking the server.
  setData: (next: T | undefined | ((old: T | undefined) => T | undefined)) => void;
}

interface State<T> {
  data: T | undefined;
  error: unknown;
  loading: boolean;
}

// useLoad runs `fn` on mount and whenever `deps` change and returns the
// answer. An answer that arrives after the inputs changed, a retry started,
// or the component unmounted is dropped, so a slow earlier request can't
// overwrite a newer one. `enabled: false` loads nothing (not loading, no data).
export function useLoad<T>(fn: () => Promise<T>, deps: DependencyList, options: { enabled?: boolean } = {}): Load<T> {
  const enabled = options.enabled !== false;
  const [state, setState] = useState<State<T>>({ data: undefined, error: null, loading: enabled });
  const [tick, setTick] = useState(0);
  const quiet = useRef(false);
  const latest = useRef(fn);
  latest.current = fn;

  useEffect(() => {
    if (!enabled) {
      setState((s) => (s.data === undefined && s.error === null && !s.loading ? s : { data: undefined, error: null, loading: false }));
      return;
    }
    let current = true;
    const keep = quiet.current;
    quiet.current = false;
    setState((s) => (keep ? (s.error === null ? s : { ...s, error: null }) : { data: undefined, error: null, loading: true }));
    latest.current().then(
      (data) => { if (current) setState({ data, error: null, loading: false }); },
      (error: unknown) => { if (current) setState((s) => ({ data: keep ? s.data : undefined, error, loading: false })); },
    );
    return () => { current = false; };
    // Restarts on the caller's deps, which are not known statically.
  }, [...deps, enabled, tick]);

  const retry = useCallback(() => setTick((n) => n + 1), []);
  const reload = useCallback(() => { quiet.current = true; setTick((n) => n + 1); }, []);
  const setData = useCallback((next: T | undefined | ((old: T | undefined) => T | undefined)) => {
    setState((s) => ({ ...s, data: typeof next === "function" ? (next as (old: T | undefined) => T | undefined)(s.data) : next }));
  }, []);
  // Loading also covers the render before the effect has started the request.
  const loading = state.loading || (enabled && state.data === undefined && state.error === null);
  return { ...state, loading, retry, reload, setData };
}
