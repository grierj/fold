import { fireEvent, render, screen } from "@testing-library/react";
import { expect, it, vi } from "vitest";
import { ToolbarButton } from "./ToolbarButton";

it("is a named toggle that keeps the focus where it was", () => {
  const onClick = vi.fn();
  render(<ToolbarButton label="Bold" pressed onClick={onClick}>B</ToolbarButton>);
  const b = screen.getByRole("button", { name: "Bold" });
  expect(b).toHaveAttribute("aria-pressed", "true");
  expect(b).toHaveAttribute("type", "button");
  expect(fireEvent.mouseDown(b)).toBe(false); // default prevented: the editor keeps focus
  fireEvent.click(b);
  expect(onClick).toHaveBeenCalled();
});
