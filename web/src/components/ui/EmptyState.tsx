import type { ReactNode } from "react";

// Says what would appear here and offers the next step (ENFORCEMENT.md A).
// Neutral on purpose: there is no tone prop, so an empty list is never red.
export function EmptyState({ title, body, action, illustration }: { title: string; body?: ReactNode; action?: ReactNode; illustration?: ReactNode }) {
  return (
    <div className="flex flex-col items-center gap-2 rounded-lg border border-border bg-surface p-6 text-center">
      {illustration}
      <p className="font-semibold text-text">{title}</p>
      {body && <p className="text-sm text-text-muted">{body}</p>}
      {action}
    </div>
  );
}
