import { useLayoutEffect, useRef, useState } from "react";

// Presentation only: callers supply already-authorized, filtered, sorted rows.
// A changed query starts at page one; a removal clamps the current page.
export function usePage<T>(rows: T[], queryKey: string, size = 25, controlled?: { page: number; onChange: (page: number) => void }) {
  const [position, setPosition] = useState({ key: queryKey, page: 1 });
  if (position.key !== queryKey) setPosition({ key: queryKey, page: 1 });
  const pages = Math.max(1, Math.ceil(rows.length / size));
  const requested = controlled?.page ?? (position.key === queryKey ? position.page : 1);
  const page = Math.max(1, Math.min(Number.isSafeInteger(requested) ? requested : 1, pages));
  const start = (page - 1) * size;
  const anchor = useRef<HTMLDivElement>(null);
  const moveFocus = useRef(false);
  if (!controlled && position.key === queryKey && position.page !== page) {
    setPosition({ key: queryKey, page });
    moveFocus.current = true;
  }
  useLayoutEffect(() => {
    if (!moveFocus.current) return;
    moveFocus.current = false;
    anchor.current?.scrollIntoView?.({ block: "start" });
    anchor.current?.focus({ preventScroll: true });
  }, [page]);
  function go(next: number) {
    const target = Math.max(1, Math.min(next, pages));
    moveFocus.current = target !== page;
    if (controlled) controlled.onChange(target);
    else setPosition({ key: queryKey, page: target });
  }
  return { rows: rows.slice(start, start + size), total: rows.length, start, page, pages, go, anchor };
}
export type PageControls = Pick<ReturnType<typeof usePage>, "total" | "start" | "page" | "pages" | "go" | "anchor">;
