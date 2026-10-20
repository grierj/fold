import type { KeyboardEvent, ReactNode } from "react";

// A day in a calendar grid you can pick (the month view). It's a toggle:
// aria-pressed says which day's events show in the panel, and the label
// names the day and how many events it has, since the cell shows only a
// number and dots. `layout` takes size and placement classes.
export function DayButton({
  label,
  pressed,
  onClick,
  onKeyDown,
  day,
  focusable,
  layout,
  children,
}: {
  label: string;
  pressed: boolean;
  onClick: () => void;
  onKeyDown?: (e: KeyboardEvent<HTMLButtonElement>) => void;
  // For a roving tabindex: only one day in the grid is in the Tab order.
  day?: string;
  focusable?: boolean;
  layout?: string;
  children: ReactNode;
}) {
  return (
    <button type="button" aria-label={label} aria-pressed={pressed} onClick={onClick} onKeyDown={onKeyDown} data-day={day} tabIndex={focusable === false ? -1 : 0} className={`rounded-md hover:bg-sunken aria-pressed:ring-2 aria-pressed:ring-inset aria-pressed:ring-primary ${layout ?? ""}`}>
      {children}
    </button>
  );
}
