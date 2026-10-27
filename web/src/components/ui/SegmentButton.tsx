import type { ButtonHTMLAttributes, ReactNode } from "react";

type Props = Omit<ButtonHTMLAttributes<HTMLButtonElement>, "aria-pressed" | "className" | "style" | "type"> & {
  pressed: boolean;
  children: ReactNode;
};

// One option in a joined, single-choice button group. The parent supplies the
// group's accessible name; native button behavior provides Enter/Space
// activation and prevents activation while disabled.
export function SegmentButton({ pressed, children, ...rest }: Props) {
  return (
    <button type="button" className="segment" aria-pressed={pressed} {...rest}>
      {children}
    </button>
  );
}
