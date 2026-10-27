import { useEffect, useId, useRef } from "react";
import { Button } from "./ui/Button";

export type SeriesRsvpScope = "just_this_date" | "this_and_following" | "all_future";

export function SeriesRsvpDialog({ answer, onChoose, onCancel }: {
  answer: "Going" | "Maybe" | "Can't go";
  onChoose: (scope: SeriesRsvpScope) => void;
  onCancel: () => void;
}) {
  const titleId = useId();
  const panel = useRef<HTMLDivElement>(null);

  useEffect(() => {
    const returnTo = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    panel.current?.querySelector<HTMLElement>("button")?.focus();
    return () => returnTo?.focus();
  }, []);

  function keyDown(event: React.KeyboardEvent<HTMLDivElement>) {
    if (event.key === "Escape") {
      event.preventDefault();
      onCancel();
      return;
    }
    if (event.key !== "Tab") return;
    const controls = Array.from(panel.current?.querySelectorAll<HTMLElement>('button:not([disabled])') ?? []);
    if (!controls.length) return;
    const firstControl = controls[0];
    const lastControl = controls[controls.length - 1];
    if (event.shiftKey && document.activeElement === firstControl) {
      event.preventDefault();
      lastControl.focus();
    } else if (!event.shiftKey && document.activeElement === lastControl) {
      event.preventDefault();
      firstControl.focus();
    }
  }

  return <div className="fixed inset-0 z-50 flex items-end sm:items-center justify-center bg-black/40 p-0 sm:p-4" onMouseDown={(event) => {
    if (event.target === event.currentTarget) onCancel();
  }}>
    <div ref={panel} role="dialog" aria-modal="true" aria-labelledby={titleId} onKeyDown={keyDown}
      className="w-full sm:max-w-md rounded-t-2xl sm:rounded-2xl bg-surface border border-border p-5 shadow-xl space-y-3">
      <h2 id={titleId} className="text-xl font-bold">How far should “{answer}” apply?</h2>
      <p className="text-sm text-text-muted">Choose which dates in this series you want to answer.</p>
      <div className="grid gap-2">
        <Button variant="secondary" layout="w-full min-h-11 justify-start text-left" onClick={() => onChoose("just_this_date")}>Just this date</Button>
        <Button variant="secondary" layout="w-full min-h-11 justify-start text-left" onClick={() => onChoose("this_and_following")}>This and following dates</Button>
        <Button variant="secondary" layout="w-full min-h-11 justify-start text-left" onClick={() => onChoose("all_future")}>All future dates</Button>
      </div>
      <Button variant="secondary" layout="w-full" onClick={onCancel}>Cancel</Button>
    </div>
  </div>;
}
