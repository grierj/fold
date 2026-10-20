import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { SearchBox } from "./SearchBox";

describe("SearchBox", () => {
  it("is a labeled search field with a decorative icon", async () => {
    const onChange = vi.fn();
    const { container } = render(<SearchBox id="s" label="Search groups" value="" onChange={onChange} />);
    const box = screen.getByRole("searchbox", { name: "Search groups" });
    expect(box).toHaveAttribute("placeholder", "Search groups");
    expect(container.querySelector("label")).toHaveClass("sr-only");
    expect(container.querySelector("svg")).toHaveAttribute("aria-hidden", "true");
    await userEvent.type(box, "p");
    expect(onChange).toHaveBeenCalledWith("p");
  });
});
