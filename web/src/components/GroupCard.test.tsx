import { render, screen } from "@testing-library/react";
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
  it("can use h2 directly under the My groups page title", () => {
    renderCard({ headingLevel: 2 });
    expect(screen.getByRole("heading", { level: 2 })).toHaveTextContent(group.name);
  });
  it("links to the group and shows its name and badges", () => {
    renderCard();
    expect(screen.getByRole("heading", { name: "Tuesday Bible Study", level: 3 })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /Tuesday Bible Study/ })).toHaveAttribute("href", "/groups/7");
    expect(screen.getByText("Invite only")).toBeInTheDocument();
    expect(screen.queryByText("Pending approval")).not.toBeInTheDocument();
    expect(screen.getByText(/No upcoming events/)).toBeInTheDocument();
  });

  it("shows Today in the success tone with a separate time, and Tonight from 5 PM", () => {
    vi.useFakeTimers({ toFake: ["Date"] });
    try {
      vi.setSystemTime(new Date("2035-06-15T15:00:00Z")); // 9 AM in Boise
      const { unmount } = renderCard({ group: { ...group, nextEventAt: "2035-06-15T22:01:00Z" } }); // 4:01 PM
      const today = screen.getByText("Today · 4:01 PM");
      expect(today.parentElement).toHaveClass("text-success-on-subtle");
      unmount();
      renderCard({ group: { ...group, nextEventAt: "2035-06-16T01:00:00Z" } }); // 7:00 PM
      expect(screen.getByText("Tonight · 7:00 PM")).toBeInTheDocument();
    } finally {
      vi.useRealTimers();
    }
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

  it("shows tags as quiet text, not controls, and no organizer line", () => {
    renderCard();
    expect(screen.getByText("bible-study · adults")).toBeInTheDocument();
    expect(screen.queryByRole("button")).not.toBeInTheDocument();
    expect(screen.queryByText(/Organized by/)).not.toBeInTheDocument();
  });

  it("is one link and lists the next meeting right under the name, then members and place", () => {
    renderCard({ group: { ...group, nextEventAt: "2035-06-16T01:00:00Z" } });
    expect(screen.getAllByRole("link")).toHaveLength(1);
    const lines = screen.getByRole("heading", { level: 3 }).parentElement!.children;
    const texts = Array.from(lines).map((el) => el.textContent);
    const at = (needle: RegExp) => texts.findIndex((t) => needle.test(t ?? ""));
    expect(at(/Tuesday Bible Study/)).toBeLessThan(at(/Jun 15/));
    expect(at(/Jun 15/)).toBeLessThan(at(/4 members · Room 201/));
    expect(at(/4 members · Room 201/)).toBeLessThan(at(/bible-study · adults/));
    expect(at(/Jun 15/)).toBe(at(/Tuesday Bible Study/) + 1);
  });

  it("says whether you lead or are in the group", () => {
    const { unmount } = renderCard({ group: { ...group, myRole: "leader" } });
    expect(screen.getByText("You lead")).toBeInTheDocument();
    unmount();
    const second = renderCard({ group: { ...group, myRole: "member" } });
    expect(screen.getByText("You're in")).toBeInTheDocument();
    second.unmount();
    renderCard({ group });
    expect(screen.queryByText(/You lead|You're in/)).not.toBeInTheDocument();
  });

  it("puts the role on its own line below members and place, with the check hidden from readers", () => {
    renderCard({ group: { ...group, myRole: "leader" } });
    const memberLine = screen.getByText("4 members · Room 201");
    expect(memberLine).not.toHaveTextContent("You lead");
    const role = screen.getByText("You lead");
    expect(role).not.toBe(memberLine);
    expect(role.parentElement).toBe(memberLine.parentElement);
    expect(memberLine.compareDocumentPosition(role) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(role.querySelector('[aria-hidden="true"]')).toHaveTextContent("✓");
    expect(screen.getByRole("link")).toHaveAccessibleName(/You lead/);
  });

  it("showRole off keeps only You lead", () => {
    const { unmount } = renderCard({ group: { ...group, myRole: "member" }, showRole: false });
    expect(screen.queryByText("You're in")).not.toBeInTheDocument();
    unmount();
    renderCard({ group: { ...group, myRole: "leader" }, showRole: false });
    expect(screen.getByText("You lead")).toBeInTheDocument();
  });
});
