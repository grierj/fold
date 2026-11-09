import { describe, expect, it } from "vitest";
import { passwordProblem } from "./password";

const SHORT = "Password must be at least 10 characters.";
const LONG = /too long/;

describe("passwordProblem", () => {
  it("counts characters for the minimum, as the server does", () => {
    expect(passwordProblem("a".repeat(9))).toBe(SHORT);
    expect(passwordProblem("a".repeat(10))).toBeNull();
    expect(passwordProblem("😀".repeat(9))).toBe(SHORT);
    expect(passwordProblem("😀".repeat(10))).toBeNull();
    expect(passwordProblem("é".repeat(10))).toBeNull();
  });

  it("counts bytes for the maximum", () => {
    expect(passwordProblem("a".repeat(72))).toBeNull();
    expect(passwordProblem("a".repeat(73))).toMatch(LONG);
    expect(passwordProblem("😀".repeat(18))).toBeNull();
    expect(passwordProblem("😀".repeat(19))).toMatch(LONG);
  });
});
