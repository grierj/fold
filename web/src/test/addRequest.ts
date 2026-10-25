import type { AddRequest } from "../types";

export function addRequest(over: Partial<AddRequest> = {}): AddRequest {
  return { id: 7, purpose: "group_add", reason: "", overLimit: false, needsAdmin: true, subjectDecision: "pending", adminDecision: "pending", status: "pending", createdAt: "2026-10-01T12:00:00Z", expiresAt: "2026-10-15T12:00:00Z", endReason: "", canSubjectDecide: false, canAdminDecide: false, canWithdraw: false, canComplete: false, ...over };
}
