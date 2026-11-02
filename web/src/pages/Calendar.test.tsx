import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { CalendarOccurrence } from "../types";
import { CalendarPage } from "./Calendar";

const { list, groups, auth, site } = vi.hoisted(() => ({ list: vi.fn(), groups: vi.fn(), auth: vi.fn(), site: vi.fn() }));
vi.mock("../auth", () => ({ useAuth: auth }));
vi.mock("../api", async (importActual) => ({
  ApiError: (await importActual<typeof import("../api")>()).ApiError,
  Calendar: { list },
  Groups: { list: groups },
  Site: { get: site },
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
  site.mockReset();
  site.mockResolvedValue({ roomsEnabled: false });
  auth.mockReturnValue({ user: { id: 1 }, isAdmin: false });
});

it("bounds a busy agenda and preserves a URL page without refetching", async () => {
  show(Array.from({ length: 60 }, (_, i) => occ(i + 1)), "/calendar?date=2026-10-21&page=2&church=1");
  expect(await screen.findByText("Event 26")).toBeInTheDocument();
  expect(screen.queryByText("Event 1")).not.toBeInTheDocument();
  expect(screen.queryByText("Event 51")).not.toBeInTheDocument();
  expect(list).toHaveBeenCalledWith(expect.objectContaining({ church: true }));
  await userEvent.click(screen.getByRole("button", { name: "Next events" }));
  expect(screen.getByText("Event 60")).toBeInTheDocument();
  expect(list).toHaveBeenCalledTimes(1);
  await userEvent.click(screen.getByRole("button", { name: "Filters" }));
  await userEvent.selectOptions(screen.getByRole("combobox"), "");
  expect(await screen.findByText("Event 1")).toBeInTheDocument();
  expect(list).toHaveBeenLastCalledWith(expect.objectContaining({ church: false }));
});

it("uses a safe first page for a malformed or negative page", async () => {
  show(Array.from({ length: 30 }, (_, i) => occ(i + 1)), "/calendar?date=2026-10-21&page=-4");
  expect(await screen.findByText("Event 1")).toBeInTheDocument();
  expect(screen.queryByText("Event 26")).not.toBeInTheDocument();
});

describe("filter chips (DEC-99)", () => {
  it("hides Remote seats and Childcare when nothing in view has them", async () => {
    show([occ(1)]);
    await screen.findByText("Event 1");
    await userEvent.click(screen.getByRole("button", { name: "Filters" }));
    expect(screen.getByRole("button", { name: "Just mine" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Remote seats" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Childcare" })).not.toBeInTheDocument();
  });

  it("shows each chip only when something in view has that", async () => {
    show([occ(1, { remoteAvailable: true }), occ(2)]);
    await userEvent.click(screen.getByRole("button", { name: "Filters" }));
    expect(await screen.findByRole("button", { name: "Remote seats" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Childcare" })).not.toBeInTheDocument();
  });

  it("shows Childcare when a date offers it", async () => {
    show([occ(1, { childcareAvailable: true })]);
    await userEvent.click(screen.getByRole("button", { name: "Filters" }));
    expect(await screen.findByRole("button", { name: "Childcare" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Remote seats" })).not.toBeInTheDocument();
  });

  it("keeps a selected chip even when nothing in view has it, so it can be turned off", async () => {
    show([], "/calendar?remote=1&childcare=1");
    await userEvent.click(screen.getByRole("button", { name: "Filters" }));
    expect(await screen.findByRole("button", { name: "Remote seats", pressed: true })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Childcare", pressed: true })).toBeInTheDocument();
  });

  it("keeps Just mine and, for admins, Show deleted events", async () => {
    auth.mockReturnValue({ user: { id: 1 }, isAdmin: true });
    show([]);
    await waitFor(() => expect(list).toHaveBeenCalled());
    await userEvent.click(screen.getByRole("button", { name: "Filters" }));
    expect(screen.getByRole("button", { name: "Just mine" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Show deleted events" })).toBeInTheDocument();
  });
});

describe("Filters disclosure", () => {
  it("starts collapsed, reveals controls and returns focus when done", async () => {
    show([occ(1)]);
    await screen.findByText("Event 1");
    const filters = screen.getByRole("button", { name: "Filters" });
    expect(filters).toHaveAttribute("aria-expanded", "false");
    expect(screen.getByText("No filters")).toBeVisible();
    expect(screen.queryByRole("button", { name: "Just mine" })).not.toBeInTheDocument();
    expect(screen.queryByRole("combobox", { name: "Group" })).not.toBeInTheDocument();
    const panel = document.getElementById(filters.getAttribute("aria-controls")!)!;
    expect(panel).toHaveAttribute("hidden");
    await userEvent.click(filters);
    expect(filters).toHaveAttribute("aria-expanded", "true");
    expect(panel).not.toHaveAttribute("hidden");
    expect(screen.getByLabelText("From date")).toBeVisible();
    expect(screen.getByRole("combobox", { name: "Group" })).toBeVisible();
    await userEvent.click(screen.getByRole("button", { name: "Just mine" }));
    await waitFor(() => expect(list).toHaveBeenLastCalledWith(expect.objectContaining({ mine: true })));
    await userEvent.click(screen.getByRole("button", { name: "Done with filters" }));
    expect(filters).toHaveFocus();
    expect(filters).toHaveAttribute("aria-expanded", "false");
    expect(screen.getByTitle("Just mine")).toBeVisible();
  });

  it("summarizes active URL filters with the group's name", async () => {
    groups.mockResolvedValue({ groups: [{ id: 3, name: "Sunrise study" }] });
    show([], "/calendar?mine=1&childcare=1&group=3");
    expect(await screen.findByText("Just mine · Childcare · Sunrise study")).toBeVisible();
    await userEvent.click(screen.getByRole("button", { name: "Filters" }));
    expect(screen.getByRole("button", { name: "Just mine", pressed: true })).toBeVisible();
    expect(screen.getByRole("button", { name: "Childcare", pressed: true })).toBeVisible();
    expect(screen.getByRole("combobox", { name: "Group" })).toHaveValue("3");
    expect(list).toHaveBeenLastCalledWith(expect.objectContaining({ mine: true, childcare: true, group: "3" }));
  });

  it.each(["week", "month"])("keeps %s navigation outside the disclosure", async (view) => {
    show([], `/calendar?view=${view}&date=2026-10-21`);
    await waitFor(() => expect(list).toHaveBeenCalled());
    expect(screen.getByRole("button", { name: `Previous ${view}` })).toBeVisible();
    expect(screen.getByRole("button", { name: `Next ${view}` })).toBeVisible();
    expect(screen.getByRole("button", { name: "Today" })).toBeVisible();
    expect(screen.getByRole("button", { name: "Filters" })).toHaveAttribute("aria-expanded", "false");
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

describe("the Rooms view item (M2.2a)", () => {
  it("shows Rooms to signed-in adults when rooms are on", async () => {
    site.mockResolvedValue({ roomsEnabled: true });
    auth.mockReturnValue({ user: { id: 1, accountType: "adult" }, isAdmin: false });
    show([]);
    expect(await screen.findByRole("link", { name: "Rooms" })).toHaveAttribute("href", "/calendar/rooms");
  });

  it("hides Rooms when rooms are off", async () => {
    auth.mockReturnValue({ user: { id: 1, accountType: "adult" }, isAdmin: false });
    show([]);
    await waitFor(() => expect(site).toHaveBeenCalled());
    await screen.findByRole("link", { name: "Month" });
    expect(screen.queryByRole("link", { name: "Rooms" })).not.toBeInTheDocument();
  });

  it("hides Rooms from teens and signed-out visitors without asking", async () => {
    site.mockResolvedValue({ roomsEnabled: true });
    auth.mockReturnValue({ user: { id: 1, accountType: "teen" }, isAdmin: false });
    const { unmount } = show([]);
    await screen.findByRole("link", { name: "Month" });
    expect(screen.queryByRole("link", { name: "Rooms" })).not.toBeInTheDocument();
    unmount();
    auth.mockReturnValue({ user: null, isAdmin: false });
    show([]);
    await screen.findByRole("link", { name: "Month" });
    expect(screen.queryByRole("link", { name: "Rooms" })).not.toBeInTheDocument();
    expect(site).not.toHaveBeenCalled();
  });
});
