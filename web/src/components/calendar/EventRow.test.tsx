import { render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { describe, expect, it } from "vitest";
import type { CalendarOccurrence } from "../../types";
import { EventChip, EventRow } from "./EventRow";

const occ = (o: Partial<CalendarOccurrence>): CalendarOccurrence => ({
  eventId: 7,
  date: "2026-10-22",
  startsAt: "2026-10-23T01:00:00Z",
  endsAt: "2026-10-23T02:30:00Z",
  title: "Thursday class",
  location: "Room 201",
  groupId: 3,
  groupName: "Room 201 class",
  churchWide: false,
  myStatus: null,
  ...o,
});
const wrap = (ui: React.ReactNode) => render(<MemoryRouter>{ui}</MemoryRouter>);

describe("EventRow", () => {
  it("links to the event and names its group and place", () => {
    wrap(<EventRow occ={occ({})} />);
    const link = screen.getByRole("link");
    expect(link).toHaveAttribute("href", "/events/7");
    expect(link).toHaveTextContent("Thursday class");
    expect(link).toHaveTextContent("Room 201 class · Room 201");
  });

  it("says Going in words beside the check, and Maybe in words", () => {
    const { unmount } = wrap(<EventRow occ={occ({ myStatus: "going" })} />);
    expect(screen.getByText("Going")).toBeInTheDocument();
    unmount();
    wrap(<EventRow occ={occ({ myStatus: "maybe" })} />);
    expect(screen.getByText("Maybe")).toBeInTheDocument();
  });

  it("marks church-wide events", () => {
    wrap(<EventRow occ={occ({ churchWide: true, groupId: undefined, groupName: undefined })} />);
    expect(screen.getByText("Church-wide")).toBeInTheDocument();
  });
});

describe("EventChip", () => {
  it("spells out what the chip abbreviates", () => {
    wrap(<EventChip occ={occ({ myStatus: "going" })} />);
    expect(screen.getByRole("link").getAttribute("aria-label")).toMatch(/Thursday class, Room 201 class, Going$/);
  });

  it("stacks the time over a two-line title in the week view, keeping the full name", () => {
    wrap(<EventChip occ={occ({ title: "A very long evening of hymns and fellowship" })} stacked />);
    const link = screen.getByRole("link");
    expect(screen.getByText("A very long evening of hymns and fellowship")).toHaveClass("line-clamp-2");
    expect(link.getAttribute("aria-label")).toMatch(/A very long evening of hymns and fellowship, Room 201 class$/);
  });
});
