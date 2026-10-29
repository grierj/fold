import { render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { describe, expect, it } from "vitest";
import type { CalendarOccurrence } from "../../types";
import { DaySection, EventChip, EventRow } from "./EventRow";

const occ = (o: Partial<CalendarOccurrence>): CalendarOccurrence => ({
  eventId: 7,
  date: "2026-10-22",
  startsAt: "2026-10-23T01:00:00Z",
  endsAt: "2026-10-23T02:30:00Z",
  title: "Thursday class",
  location: "Room 201",
  groupId: 3,
  groupName: "Room 201 class",
  churchWide: false, remoteAvailable: false, childcareAvailable: false,
  myStatus: null,
  ownScope: false,
  ...o,
});
const wrap = (ui: React.ReactNode) => render(<MemoryRouter>{ui}</MemoryRouter>);

it("uses second-level day headings directly below the calendar page heading", () => {
  wrap(<DaySection heading="Thursday" occs={[]} />);
  expect(screen.getByRole("heading", { name: "Thursday", level: 2 })).toBeInTheDocument();
});

describe("EventRow", () => {
  it("marks events with remote seats using the neutral badge", () => {
    wrap(<EventRow occ={occ({ remoteAvailable: true })} />);
    expect(screen.getByText("Remote")).toHaveClass("bg-sunken", "text-text-muted");
  });

  it("does not offer a remote badge for an in-person-only date", () => {
    wrap(<EventRow occ={occ({ remoteAvailable: false })} />);
    expect(screen.queryByText("Remote")).not.toBeInTheDocument();
  });
  it("links to the event and names its group and place", () => {
    wrap(<EventRow occ={occ({})} />);
    const link = screen.getByRole("link");
    expect(link).toHaveAttribute("href", "/events/7");
    expect(link).toHaveTextContent("Thursday class");
    expect(link).toHaveTextContent("Room 201 class · Room 201");
  });

  it.each([
    ["going", "Going"], ["maybe", "Maybe"], ["waitlisted", "Waitlisted"], ["declined", "Can't go"],
  ] as const)("labels the viewer's %s answer", (myStatus, label) => {
    wrap(<EventRow occ={occ({ myStatus, ownScope: true })} />);
    expect(screen.getByText(label)).toBeInTheDocument();
  });

  it("labels unanswered own-scope dates and ghosts them without fading text", () => {
    wrap(<EventRow occ={occ({ ownScope: true })} />);
    const link = screen.getByRole("link");
    expect(screen.getByText("Not answered")).toBeInTheDocument();
    expect(link).toHaveClass("border-dashed", "bg-sunken", "text-text");
    expect(link.className).not.toMatch(/opacity/);
  });

  it("keeps outside-own-scope events ordinary", () => {
    wrap(<EventRow occ={occ({ ownScope: false })} />);
    expect(screen.queryByText("Not answered")).not.toBeInTheDocument();
    expect(screen.getByRole("link")).toHaveClass("bg-surface");
  });

  it("marks church-wide events", () => {
    wrap(<EventRow occ={occ({ churchWide: true, groupId: undefined, groupName: undefined })} />);
    expect(screen.getByText("Church-wide")).toBeInTheDocument();
  });
});

describe("EventChip", () => {
  it.each([false, true])("keeps a 44px target when stacked=%s", (stacked) => {
    wrap(<EventChip occ={occ({})} stacked={stacked} />);
    expect(screen.getByRole("link")).toHaveClass("min-h-11");
  });
  it("names remote attendance in compact calendar entries too", () => {
    wrap(<EventChip occ={occ({ remoteAvailable: true })} />);
    expect(screen.getByText("Remote")).toHaveClass("bg-sunken");
    expect(screen.getByRole("link")).toHaveAccessibleName(/Remote/);
  });
  it.each([
    ["going", "Going", "bg-primary"],
    ["maybe", "Maybe", "border-dashed"],
    ["waitlisted", "Waitlisted", "border-dashed"],
    [null, "Not answered", "border-dashed"],
  ] as const)("spells out the %s own-scope state and keeps it in the accessible name", (myStatus, label, style) => {
    wrap(<EventChip occ={occ({ myStatus, ownScope: true })} />);
    const link = screen.getByRole("link");
    expect(link).toHaveAccessibleName(new RegExp(`Thursday class.*${label}`));
    expect(link).toHaveClass(style);
    expect(link.className).not.toMatch(/opacity/);
  });

  it("labels Can't go in Everything without applying the ghosted treatment", () => {
    wrap(<EventChip occ={occ({ myStatus: "declined", ownScope: true })} />);
    const link = screen.getByRole("link");
    expect(link).toHaveAccessibleName(/Thursday class.*Can't go/);
    expect(link).not.toHaveClass("border-dashed", "bg-primary-subtle");
    expect(link).toHaveClass("border-border", "bg-surface", "text-text-muted");
  });

  it("keeps compact context in visible-text order without overriding its content name", () => {
    wrap(<EventChip occ={occ({ myStatus: "maybe", ownScope: true, remoteAvailable: true, childcareAvailable: true })} />);
    const link = screen.getByRole("link");
    expect(link).toHaveAccessibleName(/Thursday class.*Maybe.*Remote.*Childcare/);
    expect(link).toHaveAttribute("title", expect.stringMatching(/Thursday class, Maybe, Remote, Childcare, Room 201 class/));
    expect(link).not.toHaveAttribute("aria-label");
  });

  it("stacks the time over a two-line title in the week view, keeping the full name", () => {
    wrap(<EventChip occ={occ({ title: "A very long evening of hymns and fellowship" })} stacked />);
    const link = screen.getByRole("link");
    expect(screen.getByText("A very long evening of hymns and fellowship")).toHaveClass("line-clamp-2");
    expect(link).toHaveAccessibleName(/A very long evening of hymns and fellowship/);
  });
});

describe("EventRow deleted group (DEC-68)", () => {
  it("says the group is deleted when the admin switch listed it", () => {
    wrap(<EventRow occ={occ({ groupDeleted: true })} />);
    expect(screen.getByText("Deleted group")).toHaveClass("bg-sunken", "text-text-muted");
  });
});

it.each([EventRow, EventChip])("marks childcare availability with a neutral calendar badge", (Component) => {
  wrap(<Component occ={occ({ childcareAvailable: true })} />);
  expect(screen.getByText("Childcare")).toHaveClass("bg-sunken", "text-text-muted");
});

it.each([EventRow, EventChip])("prefers the server's exact date URL and does not invent a hidden organizer", (Component) => {
  wrap(<Component occ={occ({ dateUrl: "/events/7/dates/2026-10-22", groupName: undefined, groupId: undefined, hostedBy: undefined })} />);
  expect(screen.getByRole("link")).toHaveAttribute("href", "/events/7/dates/2026-10-22");
  expect(screen.queryByText(/Hosted by|undefined/)).not.toBeInTheDocument();
});
