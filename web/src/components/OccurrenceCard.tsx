import { useId, type ReactNode } from "react";
import { DateBadge } from "./DateBadge";

// Date cards share one header layout. Only the title and time sit beside the
// date tile; attendance, answers and editors always get the full card width.
// `compact` is for a page whose header already says the title, place and time
// (a single-date event, DEC-92): the tile and heading go, the title stays as
// the card's accessible name, and `notice`, `meta` and `menu` keep a quiet
// top row only when there is something to put in it.
export function OccurrenceCard({ at, title, meta, menu, notice, compact, children }: {
  at: string;
  title: ReactNode;
  meta: ReactNode;
  menu?: ReactNode;
  notice?: ReactNode;
  compact?: boolean;
  children: ReactNode;
}) {
  const heading = useId();
  if (compact) {
    return <article aria-labelledby={heading} className="bg-surface rounded-lg border border-border p-4 wrap-anywhere">
      <h3 id={heading} className="sr-only">{title}</h3>
      {(notice || meta || menu) && <div className="mb-3 flex items-start justify-between gap-2">
        <div className="min-w-0 grow">{notice}{meta}</div>
        {menu && <div className="shrink-0">{menu}</div>}
      </div>}
      <div className="space-y-3">{children}</div>
    </article>;
  }
  return <article aria-labelledby={heading} className="bg-surface rounded-lg border border-border p-4 wrap-anywhere">
    <div className="flex gap-4">
      <DateBadge at={at} />
      <div className="grow min-w-0">
        {notice}
        <div className="flex items-start justify-between gap-2">
          <h3 id={heading} className="min-w-0 text-lg font-bold wrap-anywhere">{title}</h3>
          {menu && <div className="shrink-0">{menu}</div>}
        </div>
        {meta}
      </div>
    </div>
    <div className="mt-3 space-y-3">{children}</div>
  </article>;
}
