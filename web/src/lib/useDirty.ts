import { useRef } from "react";
import type { EventOptionValues } from "../components/eventOptionValues";
import type { JsonValue } from "./wireValues";

interface FormSnapshot {
  [field: string]: JsonValue | EventOptionValues | undefined;
}

// Whether a form differs from what it started as (feeds useUnsavedChanges).
// Pass a plain-data snapshot of every field. The baseline is the first
// snapshot taken once `ready` is true, so values that arrive after mount (the
// group's location, the church time zone) don't count as edits.
export function useDirty(snapshot: FormSnapshot, ready = true): boolean {
  const base = useRef<string | null>(null);
  const now = JSON.stringify(snapshot);

  if (ready && base.current === null) base.current = now;

  return base.current !== null && base.current !== now;
}
