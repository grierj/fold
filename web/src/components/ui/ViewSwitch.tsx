import { Link } from "react-router-dom";

// Joined links that switch between views of one page (the calendar's
// Agenda | Week | Month). It's navigation, not a toggle: the current view
// is aria-current="page" and shares the .segment look (DEC-63).
export function ViewSwitch({ label, items }: { label: string; items: { label: string; to: string; current: boolean }[] }) {
  return (
    <nav aria-label={label} className="flex w-full sm:w-auto">
      {items.map((it) => (
        <Link key={it.label} to={it.to} aria-current={it.current ? "page" : undefined} className="segment sm:flex-none sm:px-4">
          {it.label}
        </Link>
      ))}
    </nav>
  );
}
