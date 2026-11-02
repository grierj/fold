import { useId, type ReactNode } from "react";
import { DateBadge } from "./DateBadge";

// Agenda rows share one schedule header. Only the title and time sit beside
// the date badge; attendance, answers and editors keep the full row width.
// `compact` is for a page whose header already says the title, place and time
// (a single-date event, DEC-92): the tile and heading go, the title stays as
// the card's accessible name, and `notice`, `meta` and `menu` keep a quiet
// top row only when there is something to put in it.
export function OccurrenceCard({ at, title, meta, menu, notice, compact, headingLevel = 3, children }: {
  at: string;
  title: ReactNode;
  meta: ReactNode;
  menu?: ReactNode;
  notice?: ReactNode;
  compact?: boolean;
  headingLevel?: 2 | 3;
  children: ReactNode;
}) {
  const heading = useId();
  const Heading = headingLevel === 2 ? "h2" : "h3";
  if (compact) {
    return <article aria-labelledby={heading} className="fold-list-row border-t border-border wrap-anywhere">
      <Heading id={heading} className="sr-only">{title}</Heading>
      {(notice || meta || menu) && <div className="mb-3 flex items-start justify-between gap-2">
        <div className="min-w-0 grow">{notice}{meta}</div>
        {menu && <div className="shrink-0">{menu}</div>}
      </div>}
      <div className="space-y-3">{children}</div>
    </article>;
  }
  return <article aria-labelledby={heading} className="fold-list-row wrap-anywhere sm:grid sm:grid-cols-[minmax(0,1fr)_minmax(0,1.5fr)] sm:gap-6">
    <div className="flex gap-3">
      <DateBadge at={at} />
      <div className="grow min-w-0">
        {notice}
        <div className="flex items-start justify-between gap-2">
          <Heading id={heading} className="min-w-0 text-base font-semibold wrap-anywhere">{title}</Heading>
          {menu && <div className="shrink-0">{menu}</div>}
        </div>
        {meta}
      </div>
    </div>
    <div className="mt-4 min-w-0 space-y-3 sm:mt-0">{children}</div>
  </article>;
}
