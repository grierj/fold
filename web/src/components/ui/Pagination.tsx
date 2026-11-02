import type { PageControls } from "../../lib/usePage";
import { Button } from "./Button";

export function PageCount({ paging, noun, size = 25, scrollMarginTop }: { paging: PageControls; noun: string; size?: number; scrollMarginTop?: string }) {
  return <div ref={paging.anchor} tabIndex={-1} className="scroll-mt-24 rounded-sm" style={scrollMarginTop ? { scrollMarginTop } : undefined}>
    <p role="status" aria-atomic="true" className="text-sm text-text-muted py-2">
      {paging.total === 0 ? `0 ${noun}` : `Showing ${paging.start + 1} to ${Math.min(paging.start + size, paging.total)} of ${paging.total} ${noun}`}
    </p>
  </div>;
}

export function Pagination({ paging, noun, disabled = false }: { paging: PageControls; noun: string; disabled?: boolean }) {
  if (paging.pages <= 1) return null;
  return <nav aria-label={`${noun} pages`} className="flex flex-wrap items-center gap-3 py-4">
    <Button variant="secondary" disabled={disabled || paging.page <= 1} onClick={() => paging.go(paging.page - 1)} aria-label={`Previous ${noun}`}>Previous</Button>
    <span className="text-sm text-text-muted">Page {paging.page} of {paging.pages}</span>
    <Button variant="secondary" disabled={disabled || paging.page >= paging.pages} onClick={() => paging.go(paging.page + 1)} aria-label={`Next ${noun}`}>Next</Button>
  </nav>;
}
