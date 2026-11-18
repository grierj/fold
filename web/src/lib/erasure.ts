import { isNumber, isJsonObject, type JsonValue } from "./wireValues";
import type { ErasureReport } from "../types";

// True for a body that carries every count of a deletion's erasure report. The
// keys mirror ErasureReport (types.ts); a 2xx without them is not a success.
export function isErasureReport(v: JsonValue | undefined): v is ErasureReport & JsonValue {
  if (!isJsonObject(v)) return false;

  return ["householdsKept", "householdsScrubbed", "peopleAnonymized", "guardianLinksRevoked", "activityEntriesKept", "reasonsKept", "reasonsRemoved"].every((k) => isNumber(v[k]));
}
