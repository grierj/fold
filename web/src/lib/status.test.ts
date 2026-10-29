import { expect, it } from "vitest";
import { accountStatusLabel, groupStatusLabel, roleLabel } from "./status";

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
