import { wallDate } from "../lib/wall";

// Meetup-style date badge. `local` is the event's wall clock
// (YYYY-MM-DDTHH:MM), so the day does not jump when that clock is the next
// UTC date.
export function DateBadge({ local, size = "md" }: { local: string; size?: "sm" | "md" }) {
  const d = wallDate(local);
  const month = d.toLocaleDateString(undefined, { month: "short" });
  const day = d.getDate();
  const box =
    size === "sm"
      ? "w-12 h-12 text-lg"
      : "w-16 h-16 text-2xl";
  return (
    <div
      className={`${box} flex flex-col items-center justify-center rounded-xl bg-brand-light text-brand-dark shrink-0`}
    >
      <span className="text-[10px] font-bold uppercase tracking-wide leading-none">{month}</span>
      <span className="font-extrabold leading-none mt-0.5">{day}</span>
    </div>
  );
}
