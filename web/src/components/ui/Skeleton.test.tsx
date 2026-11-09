import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { Skeleton } from "./Skeleton";

describe("Skeleton", () => {
  it("announces loading and hides the blocks", () => {
    const { container } = render(<Skeleton kind="lines" count={2} />);
    expect(screen.getByRole("status")).toHaveTextContent("Loading…");
    expect(container.querySelector('[aria-hidden="true"]')).not.toBeNull();
  });
  it("uses the given label", () => {
    render(<Skeleton kind="page" label="Loading group…" />);
    expect(screen.getByRole("status")).toHaveTextContent("Loading group…");
  });
  it("renders three cards by default", () => {
    render(<Skeleton kind="cards" />);
    expect(screen.getByRole("status")).toHaveTextContent("Loading…");
    expect(screen.getByRole("status").querySelectorAll(':scope > [aria-hidden="true"]')).toHaveLength(3);
  });
});
