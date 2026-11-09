import { expect, it } from "vitest";
import { accountStatusLabel, contactRequestStatusLabel, decisionLabel, groupStatusLabel, registrationStatusLabel, roleLabel } from "./status";

it.each([
  ["active", "Active"], ["invited", "Invited"], ["guest", "Guest"],
  ["disabled", "Disabled"], ["pending_consent", "Waiting for parent consent"],
])("labels account status %s in plain words", (value, label) => {
  expect(accountStatusLabel(value)).toBe(label);
});
it.each([["active", "Active"], ["pending", "Pending approval"], ["archived", "Archived"]])("labels group status %s", (value, label) => {
  expect(groupStatusLabel(value)).toBe(label);
});
it.each([["member", "Member"], ["admin", "Admin"], ["leader", "Leader"]])("labels role %s", (value, label) => {
  expect(roleLabel(value)).toBe(label);
});
it.each(["future_internal_code", "constructor"])("does not print unknown internal values: %s", (value) => {
  expect(accountStatusLabel(value)).toBe("Unknown status");
  expect(groupStatusLabel(value)).toBe("Unknown status");
  expect(roleLabel(value)).toBe("Unknown role");
});
it.each([
  ["going", "Going"], ["maybe", "Maybe"], ["waitlisted", "Waitlisted"], ["declined", "Can't go"], ["not_answered", "Not answered"],
])("labels registration status %s", (value, label) => {
  expect(registrationStatusLabel(value)).toBe(label);
});
it("labels decisions and contact requests, and keeps unknown codes out of sight", () => {
  expect(decisionLabel("not_needed")).toBe("Not needed");
  expect(contactRequestStatusLabel("granted")).toBe("Approved");
  expect(contactRequestStatusLabel("future_code")).toBe("Unknown status");
  expect(registrationStatusLabel("constructor")).toBe("Unknown status");
});
