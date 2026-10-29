import { render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { describe, expect, it, vi } from "vitest";
import { EventIntro, openedEventWarning } from "./EventIntro";
import { EventVisibilityControl } from "./EventVisibility";

vi.mock("../api", () => ({
  ApiError: class extends Error {},
  Events: { setVisibility: vi.fn(), visibilityImpact: vi.fn(), addHost: vi.fn(), removeHost: vi.fn() },
}));

describe("opened events", () => {
  it("says hosted by the church and does not name the group", () => {
    render(
      <MemoryRouter>
        <EventIntro hostedBy="Fold" groupName="Hidden Care" groupId={9} />
      </MemoryRouter>
    );
    expect(screen.getByText("Hosted by Fold")).toBeInTheDocument();
    expect(screen.queryByText("Hidden Care")).not.toBeInTheDocument();
    expect(screen.queryByRole("link")).not.toBeInTheDocument();
  });

  it("links the group when the viewer can see it", () => {
    render(
      <MemoryRouter>
        <EventIntro groupName="Choir" groupId={3} />
      </MemoryRouter>
    );
    const link = screen.getByRole("link", { name: "Choir" });
    expect(link).toHaveAttribute("href", "/groups/3");
    expect(link).toHaveClass("inline-flex", "min-h-11", "items-center");
    expect(link.parentElement?.className).not.toMatch(/leading-/);
  });

  it("warns before an invite-only group is opened", () => {
    render(
      <EventVisibilityControl
        eventId={1}
        groupName="Hidden Care"
        groupVisibility="invite_only"
        visibility="public"
        leaderOpened
        onSaved={() => {}}
      />
    );
    expect(screen.getByText(openedEventWarning("Hidden Care"))).toBeInTheDocument();
  });
});
