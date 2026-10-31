import type { ButtonHTMLAttributes, ReactNode } from "react";

type Props = Omit<ButtonHTMLAttributes<HTMLButtonElement>, "className" | "style" | "type" | "aria-label" | "aria-pressed"> & {
  // The accessible name ("Bold"); the visible text can be a short mark.
  label: string;
  // A formatting toggle's state (aria-pressed); leave it out for a button
  // that opens something.
  pressed?: boolean;
  children: ReactNode;
};

// One button in a formatting toolbar (the composer's editor, M3.4c): 44px,
// named for assistive tech, and pressed when the format is on, which shows
// as a filled button with a strong edge, not color alone. A press keeps
// the focus (and the cursor) in the editor it formats.
export function ToolbarButton({ label, pressed, children, ...rest }: Props) {
  return (
    <button type="button" className="toolbar-button" aria-label={label} title={label} aria-pressed={pressed} onMouseDown={(e) => e.preventDefault()} {...rest}>
      {children}
    </button>
  );
}
