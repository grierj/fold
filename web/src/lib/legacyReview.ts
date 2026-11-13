import { ApiError } from "../api/core";
import { ERROR_COPY } from "./errors";

// Words and answers the deleted-accounts review shares between Admin and its
// page (T07B-3b). The deletion flow's own copy says "delete", so the review
// keeps its own lines instead of reusing those (ERROR_COPY.reason_review_not_allowed).

export const LEGACY_REVIEW_TITLE = "Deleted accounts with reasons to review";
export const LEGACY_REVIEW_PATH = "/admin/deleted-reasons";

// Only requests the account made or was asked about are listed (R4-C07).
export const LEGACY_SEARCH_LIMIT =
  "Fold lists only requests this person made or was asked about. Reasons in other people's requests that mention them aren't listed here.";

// The server decides who may review (adults); the page repeats no age rule, so
// the copy names the role the server asks for.
const LEGACY_NOT_ALLOWED = "Only an adult admin can review these reasons. Ask another adult admin to review them.";

// What a failed read means. "locked" cannot be fixed by asking again; "retry"
// is every other failure, whatever its status, code or body.
export type LegacyReadFailure = { kind: "locked"; text: string } | { kind: "retry"; text: string };

export function legacyReadFailure(e: unknown): LegacyReadFailure {
  // A gateway page with a 401 or 403 status is not the server's answer.
  if (e instanceof ApiError && !e.unreadable) {
    if (e.status === 401) return { kind: "locked", text: ERROR_COPY.signed_out };
    if (e.status === 403) return { kind: "locked", text: LEGACY_NOT_ALLOWED };
  }
  // The busy line also covers a malformed answer (ApiError 502 from the client)
  // and a dropped connection: never the wrapper's or the server's own message.
  return { kind: "retry", text: ERROR_COPY.reason_review_busy };
}
