import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { Chip } from "./Chip";

describe("Chip (ENFORCEMENT C4)", () => {
  it("marks the selected chip as pressed, which the CSS makes stronger", () => {
    render(<><Chip selected>bible-study</Chip><Chip selected={false}>youth</Chip></>);
    expect(screen.getByRole("button", { name: "bible-study" })).toHaveAttribute("aria-pressed", "true");
    expect(screen.getByRole("button", { name: "youth" })).toHaveAttribute("aria-pressed", "false");
    expect(screen.getByRole("button", { name: "youth" })).toHaveClass("chip");
  });
  it("keeps compact desktop tags at least 44px on phones", () => {
    render(<Chip selected={false} size="sm">x</Chip>);
    expect(screen.getByRole("button")).toHaveClass("min-h-11", "min-w-11", "sm:min-h-6", "sm:min-w-6");
  });
});
