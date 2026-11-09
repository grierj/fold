import { expect, it } from "vitest";
import { retainedPersonId } from "./retainedPersonId";

it("validates retained decimal IDs without numeric coercion", () => {
  for (const id of ["9007199254740992", "9007199254740993", "9223372036854775807"]) expect(retainedPersonId(id)).toBe(id);
  expect(retainedPersonId("00012")).toBe("12");
  for (const id of ["", "0", "-1", "+1", "1.5", " 12", "9223372036854775808"]) expect(retainedPersonId(id)).toBeNull();
});
