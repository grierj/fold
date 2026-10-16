import type { ButtonHTMLAttributes } from "react";

// A toggle chip for filters and tags (ENFORCEMENT.md A). `selected` is
// required and drives aria-pressed, which gives the check mark, the strong
// primary-border edge and semibold text (`.chip[aria-pressed]` in index.css).
type Props = Omit<ButtonHTMLAttributes<HTMLButtonElement>, "className" | "style" | "aria-pressed"> & {
  selected: boolean;
  // "sm" for tags on cards: at least 24x24.
  size?: "md" | "sm";
};

export function Chip({ selected, size = "md", type = "button", ...rest }: Props) {
  const cls = size === "sm" ? "chip min-h-6 min-w-6 sm:min-h-6 px-2 text-xs" : "chip";
  return <button type={type} aria-pressed={selected} className={cls} {...rest} />;
}
