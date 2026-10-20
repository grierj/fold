import { render } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { Illustration } from "./Illustration";

describe("Illustration", () => {
  it("is decorative with empty alt text", () => {
    const { container } = render(<Illustration name="not-found" />);
    const img = container.querySelector("img");
    expect(img).toHaveAttribute("alt", "");
    expect(img).toHaveAttribute("aria-hidden", "true");
    expect(img).toHaveAttribute("width", "240");
    expect(img).toHaveAttribute("height", "180");
  });
});
