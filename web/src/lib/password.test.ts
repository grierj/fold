import { describe, expect, it } from "vitest";
import { passwordLengthProblem } from "./password";

describe("passwordLengthProblem", () => {
  it("counts characters for the minimum, as the server does", () => {
    expect(passwordLengthProblem("a".repeat(9))).toBe("short");
    expect(passwordLengthProblem("a".repeat(10))).toBeNull();
    expect(passwordLengthProblem("😀".repeat(9))).toBe("short");
    expect(passwordLengthProblem("😀".repeat(10))).toBeNull();
    expect(passwordLengthProblem("é".repeat(10))).toBeNull();
  });

  it("counts bytes for the maximum", () => {
    expect(passwordLengthProblem("a".repeat(72))).toBeNull();
    expect(passwordLengthProblem("a".repeat(73))).toBe("long");
    expect(passwordLengthProblem("😀".repeat(18))).toBeNull();
    expect(passwordLengthProblem("😀".repeat(19))).toBe("long");
  });
});
