import type { ReactNode } from "react";

// Says what would appear here and offers the next step (ENFORCEMENT.md A).
// Neutral on purpose: there is no tone prop, so an empty list is never red.
export function EmptyState({
  title,
  body,
  action,
  illustration,
  level,
  framed = true,
}: {
  title: string;
  body?: ReactNode;
  action?: ReactNode;
  illustration?: ReactNode;
  level?: 1 | 2;
  framed?: boolean;
}) {
  const Title = level === 1 ? "h1" : level === 2 ? "h2" : "p";
  return (
    <div
      className={
        framed
          ? "flex flex-col items-center rounded-lg border border-border bg-surface p-6 sm:p-8 text-center"
          : "flex flex-col items-center p-6 sm:p-8 text-center"
      }
    >
      {illustration && <div className="mb-4">{illustration}</div>}
      <Title className="text-lg font-semibold text-text">{title}</Title>
      {body && <p className="mt-2 text-sm text-text-muted max-w-prose">{body}</p>}
      {action && (
        <div className="mt-4 flex w-full flex-col gap-3 sm:w-auto sm:flex-row sm:justify-center">{action}</div>
      )}
    </div>
  );
}
