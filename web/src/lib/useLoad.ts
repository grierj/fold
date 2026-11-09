import { useCallback, useEffect, useRef, useState, type DependencyList } from "react";

// How an attempt that a caller asked for ended: it brought an answer, it failed,
// or something newer (another ask, new inputs, unmount, close) replaced it first.
/** @public how an ask ended; callers read it from the promise, never name it. */
export type LoadOutcome = "loaded" | "failed" | "stale";

export interface Load<T> {
  // The latest answer, undefined until the first one arrives (and again
  // whenever `deps` change, so a stale answer never shows under new inputs).
  data: T | undefined;
  // The error of the latest attempt, or null.
  error: unknown;
  // True from a start (mount, deps change, retry) until its answer.
  loading: boolean;
  // Ask again and show the loading state: for "Try again" after an error, and
  // for a refresh that must not keep showing what it replaces (it fails
  // closed: the old answer is gone this very render, and an error leaves none).
  // Resolves when the attempt ends.
  retry: () => Promise<LoadOutcome>;
  // Ask again without a loading state: keeps showing `data` meanwhile, and
  // keeps it if this attempt fails (error is set so the caller can say so).
  // The previous error is cleared as the attempt starts. Resolves when the attempt ends.
  reload: () => Promise<LoadOutcome>;
  // Replace the answer after a local change, without asking the server.
  setData: (next: T | undefined | ((old: T | undefined) => T | undefined)) => void;
  // Drop the answer and any request in flight, and stop loading: the thing is
  // known to be gone (a mutation answered 401, 403 or 404).
  close: () => void;
  // A number that changes whenever the answer is asked for again, replaced,
  // closed or the inputs change. A caller that works from `data` across an
  // await reads it first and drops its result if it moved.
  epoch: () => number;
}

interface State<T> {
  data: T | undefined;
  error: unknown;
  loading: boolean;
  // True after close(): nothing is wanted until the next ask or new inputs.
  closed: boolean;
}

interface Options {
  enabled?: boolean;
  // Ask again, failing closed (like `retry`), whenever the window regains
  // focus: for a page whose rights may have changed while it was away.
  refreshOnFocus?: boolean;
}

// useLoad runs `fn` on mount and whenever `deps` change and returns the
// answer. An answer that arrives after the inputs changed, a retry started,
// or the component unmounted is dropped, so a slow earlier request can't
// overwrite a newer one. `enabled: false` loads nothing (not loading, no data).
export function useLoad<T>(fn: () => Promise<T>, deps: DependencyList, options: Options = {}): Load<T> {
  const enabled = options.enabled !== false;
  const refreshOnFocus = options.refreshOnFocus === true;
  const [state, setState] = useState<State<T>>({ data: undefined, error: null, loading: enabled, closed: false });
  const [tick, setTick] = useState(0);
  const quiet = useRef(false);
  const lastDeps = useRef<DependencyList | null>(null);
  const latest = useRef(fn);
  latest.current = fn;
  const generation = useRef(0);
  const waiters = useRef<Array<(outcome: LoadOutcome) => void>>([]);

  useEffect(() => {
    // The asks that triggered this run are answered by it.
    const mine = waiters.current;
    waiters.current = [];
    const settle = (outcome: LoadOutcome) => { for (const done of mine.splice(0)) done(outcome); };
    const run = ++generation.current;
    if (!enabled) {
      setState((s) => (s.data === undefined && s.error === null && !s.loading ? s : { data: undefined, error: null, loading: false, closed: false }));
      settle("stale");
      return;
    }
    // A quiet reload keeps the old answer only under the same inputs.
    const prev = lastDeps.current;
    const sameDeps = prev !== null && prev.length === deps.length && deps.every((d, i) => Object.is(d, prev[i]));
    lastDeps.current = deps;
    const keep = quiet.current && sameDeps;
    quiet.current = false;
    setState((s) => (keep ? (s.error === null && !s.closed ? s : { ...s, error: null, closed: false }) : { data: undefined, error: null, loading: true, closed: false }));
    latest.current().then(
      (data) => { if (generation.current === run) { setState({ data, error: null, loading: false, closed: false }); settle("loaded"); } },
      (error: unknown) => { if (generation.current === run) { setState((s) => ({ data: keep ? s.data : undefined, error, loading: false, closed: false })); settle("failed"); } },
    );
    return () => { generation.current++; settle("stale"); };
    // Restarts on the caller's deps, which are not known statically.
  }, [...deps, enabled, tick]);

  // Asks still waiting for an effect run when the component goes away.
  useEffect(() => () => { for (const done of waiters.current.splice(0)) done("stale"); }, []);

  const ask = useCallback((keepData: boolean) => new Promise<LoadOutcome>((done) => {
    generation.current++;
    quiet.current = keepData;
    if (!keepData) setState({ data: undefined, error: null, loading: true, closed: false });
    waiters.current.push(done);
    setTick((n) => n + 1);
  }), []);
  const retry = useCallback(() => ask(false), [ask]);
  const reload = useCallback(() => ask(true), [ask]);
  const setData = useCallback((next: T | undefined | ((old: T | undefined) => T | undefined)) => {
    setState((s) => ({ ...s, data: typeof next === "function" ? (next as (old: T | undefined) => T | undefined)(s.data) : next }));
  }, []);
  const close = useCallback(() => {
    generation.current++;
    setState({ data: undefined, error: null, loading: false, closed: true });
  }, []);
  const epoch = useCallback(() => generation.current, []);

  useEffect(() => {
    if (!refreshOnFocus || !enabled) return;
    const onFocus = () => { void retry(); };
    window.addEventListener("focus", onFocus);
    return () => window.removeEventListener("focus", onFocus);
  }, [refreshOnFocus, enabled, retry]);

  // Loading also covers the render before the effect has started the request.
  const { closed, ...answer } = state;
  const loading = state.loading || (enabled && !closed && state.data === undefined && state.error === null);
  return { ...answer, loading, retry, reload, setData, close, epoch };
}
