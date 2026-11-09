import { expect, it } from "vitest";
import { count, people } from "./plural";

it("counts with the right noun", () => {
  expect(count(1, "date")).toBe("1 date");
  expect(count(0, "date")).toBe("0 dates");
  expect(count(8, "upcoming date")).toBe("8 upcoming dates");
  expect(people(1)).toBe("1 person");
  expect(people(2)).toBe("2 people");
});
