import { render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { CalendarOccurrence } from "../types";
import { CalendarPage } from "./Calendar";

const { list, groups, auth } = vi.hoisted(() => ({ list: vi.fn(), groups: vi.fn(), auth: vi.fn() }));
vi.mock("../auth", () => ({ useAuth: auth }));
vi.mock("../api", async (importActual) => ({
  ApiError: (await importActual<typeof import("../api")>()).ApiError,
  Calendar: { list },
  Groups: { list: groups },
}));

const occ = (eventId: number, o: Partial<CalendarOccurrence> = {}): CalendarOccurrence => ({
  eventId, date: "2026-10-21", startsAt: "2026-10-21T18:00:00Z", endsAt: "2026-10-21T19:00:00Z", title: `Event ${eventId}`,
  location: "", churchWide: true, remoteAvailable: false, childcareAvailable: false, myStatus: null, ownScope: false, ...o,
});
const show = (occurrences: CalendarOccurrence[], path = "/calendar") => {
  list.mockResolvedValue({ occurrences, truncated: false });
  return render(<MemoryRouter initialEntries={[path]}><CalendarPage /></MemoryRouter>);
};

beforeEach(() => {
  list.mockReset();
  groups.mockReset();
  groups.mockResolvedValue({ groups: [] });
  auth.mockReturnValue({ user: { id: 1 }, isAdmin: false });
});

describe("filter chips (DEC-99)", () => {
  it("hides Remote seats and Childcare when nothing in view has them", async () => {
    show([occ(1)]);
    await screen.findByText("Event 1");
    expect(screen.getByRole("button", { name: "Just mine" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Remote seats" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Childcare" })).not.toBeInTheDocument();
  });

  it("shows each chip only when something in view has that", async () => {
    show([occ(1, { remoteAvailable: true }), occ(2)]);
    expect(await screen.findByRole("button", { name: "Remote seats" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Childcare" })).not.toBeInTheDocument();
  });

  it("shows Childcare when a date offers it", async () => {
    show([occ(1, { childcareAvailable: true })]);
    expect(await screen.findByRole("button", { name: "Childcare" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Remote seats" })).not.toBeInTheDocument();
  });

  it("keeps a selected chip even when nothing in view has it, so it can be turned off", async () => {
    show([], "/calendar?remote=1&childcare=1");
    expect(await screen.findByRole("button", { name: "Remote seats", pressed: true })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Childcare", pressed: true })).toBeInTheDocument();
  });

  it("keeps Just mine and, for admins, Show deleted events", async () => {
    auth.mockReturnValue({ user: { id: 1 }, isAdmin: true });
    show([]);
    await waitFor(() => expect(list).toHaveBeenCalled());
    expect(screen.getByRole("button", { name: "Just mine" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Show deleted events" })).toBeInTheDocument();
  });
});

describe("week count line", () => {
  const week = "/calendar?view=week&date=2026-10-21";
  it.each([
    [[], "No events this week"],
    [[occ(1)], "1 event this week"],
    [[occ(1), occ(2, { date: "2026-10-22" })], "2 events this week"],
  ])("says %j as %s", async (occs, text) => {
    show(occs as CalendarOccurrence[], week);
    expect(await screen.findByText(text)).toBeInTheDocument();
  });
});
