import { badgeParts } from "../lib/when";

// Meetup-style date badge for a UTC instant, dated in the viewer's zone so
// it agrees with the time printed beside it.
export function DateBadge({ at, size = "md" }: { at: string; size?: "sm" | "md" }) {
  const { month, day } = badgeParts(at);
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
