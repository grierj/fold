import type { ReactNode } from "react";

// A form's buttons (DEC-65): one primary, then Cancel beside it. Full width
// and stacked on phones, left-aligned in a row from sm up. Buttons inside
// use layout="w-full sm:w-auto".
export function FormActions({ children }: { children: ReactNode }) {
  return <div className="flex flex-col gap-3 sm:flex-row sm:items-center">{children}</div>;
}
