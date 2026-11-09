import { useCallback, useEffect, useRef, useState, useSyncExternalStore } from "react";
import type { ReasonReviewStore } from "../lib/reasonReviewStore";
import { KEEP, REMOVE } from "../lib/reasonReviewStore";
import type { ErasureReasonItem } from "../types";
import { Button } from "./ui/Button";
import { Field, Input } from "./ui/Field";
import { ReasonReviewList, type ReasonChoice } from "./ReasonReviewList";

// The admin's view of the reasons the deletion panel is reviewing (T07B-2, F6).
// The store holds every reason the walk found; this shows 50 at a time, however
// many the server's pages hold, so the page keeps a bounded number of nodes
// however long the review is. Nothing is preselected and there is no bulk
// control; moving between display pages never chooses anything.

const DISPLAY_PAGE = 50;

const numbers = new Intl.NumberFormat("en-US");
const fmt = (n: number) => numbers.format(n);

type Props = {
  store: ReasonReviewStore;
  disabled: boolean;
  // True while the walk is still loading pages: the total may still grow.
  loading: boolean;
  // Asks the section to show this reason and focus it (after a reload, say).
  jump: { ordinal: number; tick: number } | null;
};

export function ReasonReviewSection({ store, disabled, loading, jump }: Props) {
  const snap = useSyncExternalStore(store.subscribe, store.getSnapshot);
  const [start, setStart] = useState(0);
  const [goTo, setGoTo] = useState("");
  const [announce, setAnnounce] = useState("");
  const [focusTarget, setFocusTarget] = useState<{ ordinal: number; tick: number } | null>(null);
  const root = useRef<HTMLDivElement>(null);
  const ticks = useRef(0);

  const total = snap.total;
  const lastStart = Math.max(0, Math.floor((total - 1) / DISPLAY_PAGE) * DISPLAY_PAGE);
  const from = Math.min(start, lastStart);
  const items = store.slice(from, from + DISPLAY_PAGE);
  const choices: Record<string, ReasonChoice | undefined> = {};
  const changed = new Set<string>();
  items.forEach((it, i) => {
    const c = store.choiceAt(from + i);
    choices[it.requestId] = c === KEEP ? "keep" : c === REMOVE ? "remove" : undefined;
    if (store.isChanged(from + i)) changed.add(it.requestId);
  });

  const onChoose = useCallback(
    (item: ErasureReasonItem, choice: ReasonChoice) => {
      store.choose(store.ordinalOf(item.requestId), choice === "keep" ? KEEP : REMOVE);
    },
    [store],
  );

  // Shows reason `ordinal`, says where the admin is and moves focus to its heading.
  const show = useCallback(
    (ordinal: number) => {
      const n = store.total;
      if (n === 0) return;
      const at = Math.min(Math.max(ordinal, 0), n - 1);
      const first = Math.floor(at / DISPLAY_PAGE) * DISPLAY_PAGE;
      const last = Math.min(first + DISPLAY_PAGE, n);
      setStart(first);
      const need = store.choiceAt(at) === 0 ? ` Reason ${at + 1} needs a choice.` : "";
      setAnnounce(`Showing reasons ${fmt(first + 1)} to ${fmt(last)} of ${fmt(n)}.${need}`);
      setFocusTarget({ ordinal: at, tick: ++ticks.current });
    },
    [store],
  );

  useEffect(() => {
    if (jump) show(jump.ordinal);
  }, [jump, show]);

  useEffect(() => {
    if (!focusTarget) return;
    root.current?.querySelector<HTMLElement>(`[data-reason-heading="${focusTarget.ordinal}"]`)?.focus();
  }, [focusTarget]);

  if (total === 0) return null;
  const last = Math.min(from + DISPLAY_PAGE, total);
  const unsure = loading ? "of at least " : "of ";

  function go() {
    const n = Number(goTo);
    if (!Number.isInteger(n) || n < 1 || n > total) {
      setAnnounce(`Enter a reason number from 1 to ${fmt(total)}.`);
      return;
    }
    show(n - 1);
  }

  function nextWithout(find: (from: number) => number, none: string) {
    const at = find(from);
    if (at < 0) setAnnounce(none);
    else show(at);
  }

  return (
    <div ref={root} className="space-y-3">
      <nav aria-label="Reasons pages" className="space-y-2">
        <p className="font-semibold">{`Reasons ${fmt(from + 1)} to ${fmt(last)} ${unsure}${fmt(total)}`}</p>
        <div className="flex flex-wrap items-end gap-2">
          <Button variant="secondary" disabled={from === 0} onClick={() => show(from - DISPLAY_PAGE)}>
            Previous {DISPLAY_PAGE}
          </Button>
          <Button variant="secondary" disabled={from + DISPLAY_PAGE >= total} onClick={() => show(from + DISPLAY_PAGE)}>
            Next {DISPLAY_PAGE}
          </Button>
          <Button variant="secondary" disabled={snap.unchosen === 0} onClick={() => nextWithout((f) => store.nextUnchosen(f - 1), "Every reason has a choice.")}>
            Next reason without a choice
          </Button>
          {snap.changedOpen > 0 && (
            <Button variant="secondary" onClick={() => nextWithout((f) => store.nextChanged(f - 1), "No reason has changed.")}>
              Next changed reason
            </Button>
          )}
        </div>
        <div className="flex flex-wrap items-end gap-2">
          <Field label="Go to reason number">
            <Input
              type="number"
              inputMode="numeric"
              min={1}
              max={total}
              layout="w-32"
              value={goTo}
              onChange={(e) => setGoTo(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Enter") {
                  e.preventDefault();
                  go();
                }
              }}
            />
          </Field>
          <Button variant="secondary" onClick={go}>
            Go
          </Button>
        </div>
      </nav>
      <p role="status" aria-live="polite" className="sr-only">
        {announce}
      </p>
      <ReasonReviewList items={items} choices={choices} changed={changed} onChoose={onChoose} disabled={disabled} firstOrdinal={from} total={total} />
    </div>
  );
}

// The progress line. Visible at all times; it is told to assistive tech only
// when the admin has paused for two seconds, never once per click.
export function ReasonProgress({ store, done }: { store: ReasonReviewStore; done: boolean }) {
  const snap = useSyncExternalStore(store.subscribe, store.getSnapshot);
  const [said, setSaid] = useState("");
  const line = progressLine(snap.total, snap.unchosen, done);
  useEffect(() => {
    const t = setTimeout(() => setSaid(line), 2000);
    return () => clearTimeout(t);
  }, [line]);
  return (
    <>
      <p className="text-text-muted">{line}</p>
      <p role="status" aria-live="polite" className="sr-only">
        {said}
      </p>
    </>
  );
}

const reasonsWord = (n: number) => `${fmt(n)} ${n === 1 ? "reason" : "reasons"}`;

function progressLine(total: number, unchosen: number, done: boolean): string {
  if (!done) return `${reasonsWord(total - unchosen)} chosen of ${fmt(total)} so far.`;
  return unchosen === 0 ? `All ${reasonsWord(total)} chosen.` : `Choose Keep text or Blank text for ${unchosen === 1 ? "1 more reason" : `${fmt(unchosen)} more reasons`}.`;
}

// What pressing Delete would do, in words.
export function ReasonSummary({ store }: { store: ReasonReviewStore }) {
  const snap = useSyncExternalStore(store.subscribe, store.getSnapshot);
  return (
    <p className="font-semibold">{`Delete this account. Keep ${reasonsWord(snap.keep)} and blank ${fmt(snap.remove)}.`}</p>
  );
}
