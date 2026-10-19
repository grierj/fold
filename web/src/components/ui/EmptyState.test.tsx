import { render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { describe, expect, it } from "vitest";
import { EmptyState } from "./EmptyState";
import { ButtonLink } from "./Button";

describe("EmptyState", () => {
  it("renders the title as a paragraph by default", () => {
    render(<EmptyState title="No groups yet" body="Groups will show up here." />);
    expect(screen.queryByRole("heading", { name: "No groups yet" })).not.toBeInTheDocument();
    expect(screen.getByText("No groups yet")).toBeInTheDocument();
  });
  it("renders the title as h1 or h2 when level is set", () => {
    const { rerender } = render(<EmptyState title="Page not found" level={1} />);
    expect(screen.getByRole("heading", { level: 1, name: "Page not found" })).toBeInTheDocument();
    rerender(<EmptyState title="Page not found" level={2} />);
    expect(screen.getByRole("heading", { level: 2, name: "Page not found" })).toBeInTheDocument();
  });
  it("renders its actions", () => {
    render(
      <MemoryRouter>
        <EmptyState
          title="No groups yet"
          action={
            <ButtonLink to="/groups/new" variant="primary">
              Start a group
            </ButtonLink>
          }
        />
      </MemoryRouter>
    );
    expect(screen.getByRole("link", { name: "Start a group" })).toHaveAttribute("href", "/groups/new");
  });
  it("drops the frame when framed is false", () => {
    const { container } = render(<EmptyState title="All caught up" framed={false} />);
    expect(container.firstChild).not.toHaveClass("border");
  });
});
