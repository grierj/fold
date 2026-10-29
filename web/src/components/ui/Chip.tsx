import type { ButtonHTMLAttributes } from "react";

// A toggle chip for filters and tags (ENFORCEMENT.md A). `selected` is
// required and drives aria-pressed, which gives the check mark, the strong
// primary-border edge and semibold text (`.chip[aria-pressed]` in index.css).
type Props = Omit<ButtonHTMLAttributes<HTMLButtonElement>, "className" | "style" | "aria-pressed"> & {
  selected: boolean;
  // "sm" uses compact type and padding, but keeps 44px targets at every width.
  size?: "md" | "sm";
};

export function Chip({ selected, size = "md", type = "button", ...rest }: Props) {
  const cls = size === "sm" ? "chip min-h-11 min-w-11 px-2 text-xs" : "chip";
  return <button type={type} aria-pressed={selected} className={cls} {...rest} />;
}
