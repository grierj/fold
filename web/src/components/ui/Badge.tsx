import type { ReactNode } from "react";

export function Badge({ children }: { children: ReactNode }) {
  return <span className="inline-flex shrink-0 items-center rounded-sm bg-sunken px-2 py-0.5 text-xs font-semibold text-text-muted">{children}</span>;
}
