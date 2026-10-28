import { useId, type ReactNode } from "react";
import { DateBadge } from "./DateBadge";

// Date cards share one header layout. Only the title and time sit beside the
// date tile; attendance, answers and editors always get the full card width.
export function OccurrenceCard({ at, title, meta, menu, notice, children }: {
  at: string;
  title: ReactNode;
  meta: ReactNode;
  menu?: ReactNode;
  notice?: ReactNode;
  children: ReactNode;
}) {
  const heading = useId();
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
