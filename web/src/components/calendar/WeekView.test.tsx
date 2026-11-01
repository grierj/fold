import { render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { expect, it } from "vitest";
import { weekRange } from "../../lib/calendar";
import type { CalendarOccurrence } from "../../types";
import { WeekView, weekCountText } from "./WeekView";

const days = weekRange("2026-10-21");
const occ = (eventId: number, date: string): CalendarOccurrence => ({
  eventId, date, startsAt: `${date}T18:00:00Z`, endsAt: `${date}T19:00:00Z`, title: `Event ${eventId}`, location: "",
  churchWide: true, remoteAvailable: false, childcareAvailable: false, myStatus: null, ownScope: false,
});
const view = (byDay: Map<string, CalendarOccurrence[]>) =>
  render(<MemoryRouter><WeekView days={days} byDay={byDay} today="2026-10-21" /></MemoryRouter>);

it("shrinks days with no events and keeps busy days wide (DEC-99)", () => {
  const { container } = view(new Map([["2026-10-21", [occ(1, "2026-10-21")]]]));
  const columns = [...container.querySelectorAll("section[class*='border']")] as HTMLElement[];
  expect(columns).toHaveLength(7);
  const empty = columns.filter((c) => c.hasAttribute("data-empty"));
  expect(empty).toHaveLength(6);
  for (const c of empty) expect(c).toHaveClass("w-20", "flex-none");
  const busy = columns.find((c) => !c.hasAttribute("data-empty"))!;
  expect(busy).toHaveClass("flex-1");
  expect(busy).toHaveTextContent("Event 1");
  // Empty days explain themselves to sighted people as well as screen readers.
  expect(empty[0]).toHaveTextContent(/^\w{3} \d+No events$/);
  expect(empty[0].querySelector("p")).toBeVisible();
  expect(empty[0].querySelector(".sr-only, [aria-hidden=true]")).toBeNull();
});

it("counts events with the singular and the empty wording", () => {
  expect(weekCountText(0)).toBe("No events this week");
  expect(weekCountText(1)).toBe("1 event this week");
  expect(weekCountText(2)).toBe("2 events this week");
  expect(weekCountText(12)).toBe("12 events this week");
});

it("marks today in words, not color alone", () => {
  view(new Map());
  expect(screen.getAllByText("Today").length).toBeGreaterThan(0);
});
