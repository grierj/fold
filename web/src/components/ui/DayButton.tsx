import type { ReactNode } from "react";

// A day in a calendar grid you can pick (the month view). It's a toggle:
// aria-pressed says which day's events show in the panel, and the label
// names the day and how many events it has, since the cell shows only a
// number and dots. `layout` takes size and placement classes.
export function DayButton({
  label,
  pressed,
  onClick,
  layout,
  children,
}: {
  label: string;
  pressed: boolean;
  onClick: () => void;
  layout?: string;
  children: ReactNode;
}) {
  return (
    <button type="button" aria-label={label} aria-pressed={pressed} onClick={onClick} className={`rounded-md hover:bg-sunken aria-pressed:ring-2 aria-pressed:ring-inset aria-pressed:ring-primary ${layout ?? ""}`}>
      {children}
    </button>
  );
}
