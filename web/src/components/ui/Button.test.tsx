import { fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { describe, expect, it, vi } from "vitest";
import { Button, ButtonLink } from "./Button";

describe("Button", () => {
  it("does not submit the form around it unless it asks to", async () => {
    const onSubmit = vi.fn((e: React.FormEvent) => e.preventDefault());
    render(
      <form onSubmit={onSubmit}>
        <Button variant="secondary">Cancel</Button>
        <Button variant="primary" type="submit">Save</Button>
      </form>,
    );
    await userEvent.click(screen.getByRole("button", { name: "Cancel" }));
    expect(onSubmit).not.toHaveBeenCalled();
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(onSubmit).toHaveBeenCalledTimes(1);
  });

  it("passes clicks through and ignores them while disabled", () => {
    const onClick = vi.fn();
    const { rerender } = render(<Button variant="danger-outline" onClick={onClick}>Reject</Button>);
    fireEvent.click(screen.getByRole("button", { name: "Reject" }));
    expect(onClick).toHaveBeenCalledTimes(1);
    rerender(<Button variant="danger-outline" onClick={onClick} disabled>Reject</Button>);
    fireEvent.click(screen.getByRole("button", { name: "Reject" }));
    expect(onClick).toHaveBeenCalledTimes(1);
  });
});

describe("ButtonLink", () => {
  it("is a link to an app route", () => {
    render(<MemoryRouter><ButtonLink to="/groups/new" variant="primary">Start a group</ButtonLink></MemoryRouter>);
    expect(screen.getByRole("link", { name: "Start a group" })).toHaveAttribute("href", "/groups/new");
  });

  it("opens an address outside Fold in a new tab that cannot reach back to the opener", () => {
    render(<MemoryRouter><ButtonLink to="https://meet.example/room" variant="secondary" external>Join the call</ButtonLink></MemoryRouter>);
    const link = screen.getByRole("link", { name: "Join the call" });
    expect(link).toHaveAttribute("href", "https://meet.example/room");
    expect(link).toHaveAttribute("target", "_blank");
    expect(link.getAttribute("rel")).toMatch(/noopener/);
    expect(link.getAttribute("rel")).toMatch(/noreferrer/);
  });
});
