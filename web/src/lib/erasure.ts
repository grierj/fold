import type { ErasureReport } from "../types";

// True for a body that carries every count of a deletion's erasure report. The
// keys mirror ErasureReport (types.ts); a 2xx without them is not a success.
export function isErasureReport(v: unknown): v is ErasureReport {
  if (typeof v !== "object" || v === null) return false;
  const r = v as Record<string, unknown>;
  return ["householdsKept", "householdsScrubbed", "peopleAnonymized", "guardianLinksRevoked", "activityEntriesKept", "reasonsKept", "reasonsRemoved"].every((k) => typeof r[k] === "number");
}
