export type SkeletonKind = "cards" | "lines" | "cover" | "page";

const block = "bg-sunken rounded-md motion-safe:animate-pulse";

function Lines({ count }: { count: number }) {
  return (
    <div className="space-y-3">
      {Array.from({ length: count }, (_, i) => (
        <div key={i} className={`${block} h-4 ${i % 2 === 0 ? "w-full" : "w-5/6"}`} />
      ))}
    </div>
  );
}

export function Skeleton({ kind, count = 3, label = "Loading…" }: { kind: SkeletonKind; count?: number; label?: string }) {
  if (kind === "cards") {
    return (
      <div role="status" className="contents">
        <span className="sr-only">{label}</span>
        {Array.from({ length: count }, (_, i) => (
          <div key={i} aria-hidden="true" className="overflow-hidden rounded-lg border border-border bg-surface">
            <div className={`${block} h-40 w-full rounded-none`} />
            <div className="space-y-2 p-4">
              <div className={`${block} h-5 w-3/4`} />
              <div className={`${block} h-4 w-1/2`} />
            </div>
          </div>
        ))}
      </div>
    );
  }
  return (
    <div role="status">
      <span className="sr-only">{label}</span>
      <div aria-hidden="true">
        {kind === "cover" && <div className={`${block} h-56 sm:h-72 w-full`} />}
        {kind === "lines" && <Lines count={count} />}
        {kind === "page" && (
          <div className="space-y-4">
            <div className={`${block} h-8 w-48`} />
            <Lines count={3} />
          </div>
        )}
      </div>
    </div>
  );
}
