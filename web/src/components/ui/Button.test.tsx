import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { Button } from "./Button";
import { EmptyState } from "./EmptyState";

describe("Button", () => {
  it("takes its look from the variant and defaults to type=button", () => {
    render(<Button variant="danger-outline">Reject</Button>);
    const b = screen.getByRole("button", { name: "Reject" });
    expect(b).toHaveClass("btn-danger-outline");
    expect(b).toHaveAttribute("type", "button");
  });
  it("keeps compact table actions at least 44px high", () => {
    render(<Button variant="secondary" size="sm">Edit</Button>);
    expect(screen.getByRole("button", { name: "Edit" })).toHaveClass("min-h-11", "text-sm");
  });
  it("renders a neutral empty state", () => {
    render(<EmptyState title="No events yet" body="When a leader schedules one, it shows here." />);
    expect(screen.getByText("No events yet")).not.toHaveClass("text-danger");
  });
});
