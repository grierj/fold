import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { describe, expect, it, vi } from "vitest";
import { GroupCard } from "./GroupCard";
import type { GroupSummary } from "../types";

const group: GroupSummary = {
  id: 7, name: "Tuesday Bible Study", description: "", tags: ["bible-study", "adults"],
  imageUrl: "", coverFocalX: 50, coverFocalY: 50, location: "Room 201",
  leaderName: "Grier", memberCount: 4, nextEventAt: null, status: "active",
  leaderId: 1, visibility: "invite_only", deletionRequested: false, memberLimit: null,
};

function renderCard(props: Partial<Parameters<typeof GroupCard>[0]> = {}) {
  return render(
    <MemoryRouter>
      <GroupCard group={group} {...props} />
    </MemoryRouter>
  );
}

describe("GroupCard", () => {
  it("links to the group and shows its name and badges", () => {
    renderCard();
    expect(screen.getByRole("heading", { name: "Tuesday Bible Study" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /Tuesday Bible Study/ })).toHaveAttribute("href", "/groups/7");
    expect(screen.getByText("Invite only")).toBeInTheDocument();
    expect(screen.queryByText("Pending approval")).not.toBeInTheDocument();
    expect(screen.getByText(/No upcoming events/)).toBeInTheDocument();
  });

  it("dates the next event in the viewer's zone (Boise in tests), not by its UTC date", () => {
    render(
      <MemoryRouter>
        <GroupCard
          group={{
            ...group,
            nextEventAt: "2035-06-16T01:00:00Z",
          }}
        />
      </MemoryRouter>
    );
    expect(screen.getByText(/Jun 15/)).toBeInTheDocument();
    expect(screen.queryByText(/Jun 16/)).not.toBeInTheDocument();
  });

  it("toggles the directory filter from a tag", async () => {
    const onTagClick = vi.fn();
    renderCard({ onTagClick, selectedTags: ["adults"] });
    await userEvent.click(screen.getByRole("button", { name: /bible-study/ }));
    expect(onTagClick).toHaveBeenCalledWith("bible-study");
    expect(screen.getByRole("button", { name: "adults" })).toHaveAttribute("aria-pressed", "true");
    expect(screen.getByRole("button", { name: "adults" })).toHaveTextContent("✓adults");
    expect(screen.getByRole("button", { name: "bible-study" })).not.toHaveTextContent("✓");
  });

  it("marks a selected tag whatever its case, like the filter", () => {
    renderCard({ onTagClick: vi.fn(), selectedTags: ["ADULTS"] });
    expect(screen.getByRole("button", { name: "adults" })).toHaveAttribute("aria-pressed", "true");
    expect(screen.getByRole("button", { name: "adults" })).toHaveTextContent("✓adults");
  });
});
