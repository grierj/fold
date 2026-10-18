import { describe, expect, it } from "vitest";
import { coverSrc } from "./covers";

describe("coverSrc", () => {
  it("gives the same cover for the same id", () => {
    expect(coverSrc({ id: 7 })).toBe(coverSrc({ id: 7 }));
  });
  it("gives distinct covers for ids 1 to 6", () => {
    const got = [1, 2, 3, 4, 5, 6].map((id) => coverSrc({ id }));
    expect(new Set(got).size).toBe(6);
  });
  it("handles zero and negative ids", () => {
    expect(coverSrc({ id: 0 })).toBeTruthy();
    expect(coverSrc({ id: -1 })).toBe(coverSrc({ id: 5 }));
    expect(coverSrc({ id: -6 })).toBe(coverSrc({ id: 0 }));
  });
  it("prefers the group's own image", () => {
    expect(coverSrc({ id: 1, imageUrl: "  /uploads/x.jpg  " })).toBe("/uploads/x.jpg");
  });
  it("falls back when the image is only whitespace", () => {
    expect(coverSrc({ id: 1, imageUrl: "   " })).toBe(coverSrc({ id: 1 }));
  });
});
