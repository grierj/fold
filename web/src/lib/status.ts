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
