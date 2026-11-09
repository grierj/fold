import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { describe, expect, it } from "vitest";
import type { CalendarOccurrence } from "../../types";
import { DaySection, EventChip, EventRow } from "./EventRow";
import { StatusBadge } from "./StatusBadge";

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
  it.each([
    ["2026-10-23T01:00:00Z", "2026-10-23T02:30:00Z", "7:00 PM to 8:30 PM"],
    ["2026-10-22T17:30:00Z", "2026-10-22T18:30:00Z", "11:30 AM to 12:30 PM"],
    ["2026-10-23T05:30:00Z", "2026-10-23T06:30:00Z", "11:30 PM to 12:30 AM"],
  ])("names both ends of %s to %s in the viewer's zone", (startsAt, endsAt, expected) => {
    wrap(<EventRow occ={occ({ startsAt, endsAt })} />);
    expect(screen.getByText(expected)).toBeInTheDocument();
    expect(screen.getByRole("link")).toHaveAccessibleName(expect.stringContaining(expected));
  });

  it("marks events with remote seats", () => {
    wrap(<EventRow occ={occ({ remoteAvailable: true })} />);
    expect(screen.getByText("Remote")).toBeInTheDocument();
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
    ["going", "Going"], ["maybe", "Maybe"], ["waitlisted", "Waitlisted"], ["declined", "Can't go"], [null, "Not answered"],
  ] as const)("stacks the viewer's %s answer below the time", (myStatus, label) => {
    wrap(<EventRow occ={occ({ myStatus, ownScope: true })} />);
    const status = screen.getByLabelText(label);
    expect(status).toHaveTextContent(label);
    expect(status.previousElementSibling).toHaveTextContent("7:00 PM to 8:30 PM");
  });

  it("labels unanswered own-scope dates in words", () => {
    wrap(<EventRow occ={occ({ ownScope: true })} />);
    expect(screen.getByText("Not answered")).toBeInTheDocument();
  });

  it("keeps outside-own-scope events ordinary", () => {
    wrap(<EventRow occ={occ({ ownScope: false })} />);
    expect(screen.queryByText("Not answered")).not.toBeInTheDocument();
  });

  it("marks church-wide events", () => {
    wrap(<EventRow occ={occ({ churchWide: true, groupId: undefined, groupName: undefined })} />);
    expect(screen.getByText("Church-wide")).toBeInTheDocument();
  });
});

describe("EventChip", () => {
  it("keeps event links in the ordinary Tab order", async () => {
    wrap(<EventChip occ={occ({})} />);
    await userEvent.tab();
    expect(screen.getByRole("link")).toHaveFocus();
  });
  it("names remote attendance in compact calendar entries too", () => {
    wrap(<EventChip occ={occ({ remoteAvailable: true })} />);
    expect(screen.getByRole("link")).toHaveAccessibleName(/Remote/);
  });
  it.each([
    ["going", "Going"],
    ["maybe", "Maybe"],
    ["waitlisted", "Waitlisted"],
    [null, "Not answered"],
  ] as const)("spells out the %s own-scope state and keeps it in the accessible name", (myStatus, label) => {
    wrap(<EventChip occ={occ({ myStatus, ownScope: true })} />);
    expect(screen.getByRole("link")).toHaveAccessibleName(new RegExp(`Thursday class.*${label}`));
  });

  it("labels Can't go in Everything", () => {
    wrap(<EventChip occ={occ({ myStatus: "declined", ownScope: true })} />);
    const link = screen.getByRole("link");
    expect(link).toHaveAccessibleName(/Thursday class.*Can't go/);
  });

  it("keeps compact context in visible-text order without overriding its content name", () => {
    wrap(<EventChip occ={occ({ myStatus: "maybe", ownScope: true, remoteAvailable: true, childcareAvailable: true })} />);
    const link = screen.getByRole("link");
    expect(link).toHaveAccessibleName(/Thursday class.*Maybe.*Remote.*Childcare/);
    expect(link).toHaveAttribute("title", expect.stringMatching(/Thursday class, Maybe, Remote, Childcare, Room 201 class/));
    expect(link).not.toHaveAttribute("aria-label");
  });

  it("keeps the full title in the accessible name in the week view", () => {
    wrap(<EventChip occ={occ({ title: "A very long evening of hymns and fellowship" })} stacked />);
    const link = screen.getByRole("link");
    expect(link).toHaveAccessibleName(/A very long evening of hymns and fellowship/);
  });
});

describe("EventRow deleted group (DEC-68)", () => {
  it("says the group is deleted when the admin switch listed it", () => {
    wrap(<EventRow occ={occ({ groupDeleted: true })} />);
    expect(screen.getByText("Deleted group")).toBeInTheDocument();
  });
});

it.each([EventRow, EventChip])("marks childcare availability", (Component) => {
  wrap(<Component occ={occ({ childcareAvailable: true })} />);
  expect(screen.getByText("Childcare")).toBeInTheDocument();
});

it.each([EventRow, EventChip])("prefers the server's exact date URL and does not invent a hidden organizer", (Component) => {
  wrap(<Component occ={occ({ dateUrl: "/events/7/dates/2026-10-22", groupName: undefined, groupId: undefined, hostedBy: undefined })} />);
  expect(screen.getByRole("link")).toHaveAttribute("href", "/events/7/dates/2026-10-22");
  expect(screen.queryByText(/Hosted by|undefined/)).not.toBeInTheDocument();
});

describe("StatusBadge (DEC-99: quiet text, DEC-71 words kept)", () => {
  it.each([
    ["maybe", "Maybe"], ["waitlisted", "Waitlisted"], ["declined", "Can't go"], ["not_answered", "Not answered"],
  ] as const)("says %s in words", (state, label) => {
    render(<StatusBadge state={state} />);
    expect(screen.getByLabelText(label)).toHaveTextContent(label);
  });

  it("gives Going its check icon", () => {
    render(<StatusBadge state="going" />);
    const badge = screen.getByLabelText("Going");
    expect(badge).toHaveTextContent("Going");
    expect(badge.querySelector("svg")).not.toBeNull();
  });

  it.each([EventRow, EventChip])("shows Going in words on a row and on a chip", (Component) => {
    wrap(<Component occ={occ({ myStatus: "going", ownScope: true })} />);
    expect(screen.getByText("Going")).toBeInTheDocument();
  });

  it("keeps the clock on Waitlisted and renders nothing without a state", () => {
    const { container, rerender } = render(<StatusBadge state="waitlisted" />);
    expect(container.querySelector("svg")).not.toBeNull();
    rerender(<StatusBadge state={null} />);
    expect(container).toBeEmptyDOMElement();
  });
});
