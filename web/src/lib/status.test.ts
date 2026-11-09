import { expect, it } from "vitest";
import { accountStatusLabel, contactRequestStatusLabel, decisionLabel, groupStatusLabel, registrationStatusLabel, roleLabel } from "./status";

// A status the server adds later, or a key every object inherits, must never reach an admin screen as an internal code.
it.each(["future_internal_code", "constructor", "__proto__", "toString"])("does not print an unknown internal value: %s", (value) => {
  expect(accountStatusLabel(value)).toBe("Unknown status");
  expect(groupStatusLabel(value)).toBe("Unknown status");
  expect(registrationStatusLabel(value)).toBe("Unknown status");
  expect(contactRequestStatusLabel(value)).toBe("Unknown status");
  expect(decisionLabel(value as Parameters<typeof decisionLabel>[0])).toBe("Unknown status");
  expect(roleLabel(value)).toBe("Unknown role");
});
