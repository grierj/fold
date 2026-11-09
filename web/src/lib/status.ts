// Human-readable labels for stored account and group values. Unknown values
// stay neutral rather than printing an internal code into an admin screen.
const accountStatuses = new Map([
  ["active", "Active"], ["invited", "Invited"], ["guest", "Guest"],
  ["disabled", "Disabled"], ["pending_consent", "Waiting for parent consent"],
]);
const groupStatuses = new Map([
  ["active", "Active"], ["pending", "Pending approval"], ["archived", "Archived"],
]);
const roles = new Map([["member", "Member"], ["admin", "Admin"], ["leader", "Leader"]]);

export function accountStatusLabel(status: string): string {
  return accountStatuses.get(status) ?? "Unknown status";
}
export function groupStatusLabel(status: string): string {
  return groupStatuses.get(status) ?? "Unknown status";
}
export function roleLabel(role: string): string {
  return roles.get(role) ?? "Unknown role";
}

// A person's answer on a date, in the words the RSVP form uses. The plain
// words are the same wherever an answer is shown; a screen that adds detail
// ("Going remotely", "Waitlist position 2") builds on these.
const registrationStatuses = new Map([
  ["going", "Going"], ["maybe", "Maybe"], ["waitlisted", "Waitlisted"], ["declined", "Can't go"],
  ["not_answered", "Not answered"], ["removed", "Removed"], ["cancelled", "Cancelled"],
]);
export function registrationStatusLabel(status: string): string {
  return registrationStatuses.get(status) ?? "Unknown status";
}

const decisions = new Map([["pending", "Waiting"], ["approved", "Approved"], ["denied", "Denied"], ["not_needed", "Not needed"]]);
export type DecisionState = "pending" | "approved" | "denied" | "not_needed";
export function decisionLabel(state: DecisionState): string {
  return decisions.get(state) ?? "Unknown status";
}

const contactRequestStatuses = new Map([
  ["pending", "Waiting for a decision"], ["granted", "Approved"], ["denied", "Denied"],
  ["withdrawn", "Withdrawn"], ["expired", "Expired"], ["ended", "Ended"],
]);
export function contactRequestStatusLabel(status: string): string {
  return contactRequestStatuses.get(status) ?? "Unknown status";
}
