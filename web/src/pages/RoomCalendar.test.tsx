import { fireEvent, render, screen, within } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError } from "../api";
import type { RoomCalendar, RoomCalendarRoom } from "../types";
import { RoomCalendarPage } from "./RoomCalendar";

const { rooms, auth } = vi.hoisted(() => ({ rooms: vi.fn(), auth: vi.fn() }));
vi.mock("../auth", () => ({ useAuth: auth }));
vi.mock("../api", async (importActual) => ({
  ApiError: (await importActual<typeof import("../api")>()).ApiError,
  Calendar: { rooms },
  Site: { get: vi.fn().mockResolvedValue({ roomsEnabled: true }) },
}));

const zone = "America/Denver";
// 7:00–8:30 PM on Wednesday Oct 21 in Denver.
const hall: RoomCalendarRoom = {
  id: 1, name: "Fellowship Hall", building: "Main", capacity: 80, restricted: false,
  bookings: [
    { startsAt: "2026-10-22T01:00:00Z", endsAt: "2026-10-22T02:30:00Z", eventId: 7, date: "2026-10-21", title: "Bible study", groupId: 3, groupName: "Men's group" },
  ],
};
const library: RoomCalendarRoom = {
  id: 2, name: "Library", building: "", capacity: 20, restricted: false,
  bookings: [{ startsAt: "2026-10-21T16:00:00Z", endsAt: "2026-10-21T17:00:00Z", reserved: true }],
};
const day = (o: Partial<RoomCalendar> = {}): RoomCalendar => ({ timezone: zone, from: "2026-10-21", to: "2026-10-21", rooms: [hall, library], truncated: false, ...o });

const show = (path = "/calendar/rooms?date=2026-10-21") =>
  render(<MemoryRouter initialEntries={[path]}><RoomCalendarPage /></MemoryRouter>);

beforeEach(() => {
  rooms.mockReset();
  auth.mockReturnValue({ user: { id: 1, accountType: "adult" }, isAdmin: false });
});

describe("RoomCalendarPage", () => {
  it("shows one column per room with its bookings, in the church's zone", async () => {
    rooms.mockResolvedValue(day());
    show();
    const col = await screen.findByRole("region", { name: /Fellowship Hall/ });
    const link = within(col).getByRole("link", { name: /Bible study, Men's group/ });
    expect(link).toHaveAttribute("href", "/events/7");
    expect(link).toHaveAccessibleName(/^7:00–8:30\sPM/);
    expect(screen.getByText("Times are in Mountain time.")).toBeInTheDocument();
    expect(rooms).toHaveBeenCalledWith({ view: "day", date: "2026-10-21" });
  });

  it("shows a hidden date as Reserved, with no link", async () => {
    rooms.mockResolvedValue(day());
    show();
    const col = await screen.findByRole("region", { name: /Library/ });
    expect(within(col).getByText("Reserved")).toBeInTheDocument();
    expect(within(col).queryByRole("link")).not.toBeInTheDocument();
  });

  it("marks a booking that waits for approval in words", async () => {
    auth.mockReturnValue({ user: { id: 1, accountType: "adult" }, isAdmin: true });
    rooms.mockResolvedValue(day({ rooms: [{ ...hall, bookings: [{ ...hall.bookings[0], waiting: true }] }] }));
    show();
    const col = await screen.findByRole("region", { name: /Fellowship Hall/ });
    expect(within(col).getByText("Waiting for approval")).toBeInTheDocument();
    expect(within(col).getByRole("link")).toHaveAccessibleName(/Waiting for approval$/);
  });

  it("lays out Week as rooms by days, with free days marked", async () => {
    rooms.mockResolvedValue(day({ from: "2026-10-18", to: "2026-10-24" }));
    show("/calendar/rooms?view=week&date=2026-10-21");
    const table = await screen.findByRole("table", { name: "Rooms by day" });
    expect(within(table).getAllByRole("columnheader")).toHaveLength(8);
    const row = within(table).getByRole("row", { name: /Fellowship Hall/ });
    expect(within(row).getByRole("link", { name: /Bible study/ })).toBeInTheDocument();
    expect(within(row).getAllByText("Free")).toHaveLength(6);
    expect(rooms).toHaveBeenCalledWith({ view: "week", date: "2026-10-21" });
  });

  it("on phones shows the picked room as a day list", async () => {
    rooms.mockResolvedValue(day());
    show("/calendar/rooms?date=2026-10-21&room=2");
    const picker = await screen.findByRole("combobox", { name: "Room" });
    expect(picker).toHaveValue("2");
    expect(screen.getAllByText("Reserved").length).toBeGreaterThanOrEqual(2);
    fireEvent.change(picker, { target: { value: "1" } });
    expect(await screen.findByRole("combobox", { name: "Room" })).toHaveValue("1");
  });

  it("says a room is free on a day with nothing booked", async () => {
    rooms.mockResolvedValue(day({ rooms: [{ ...library, bookings: [] }] }));
    show();
    expect(await screen.findAllByText("Nothing booked. Library is free all day.")).not.toHaveLength(0);
  });

  it("with no rooms, points admins to Admin → Rooms", async () => {
    auth.mockReturnValue({ user: { id: 1, accountType: "adult" }, isAdmin: true });
    rooms.mockResolvedValue(day({ rooms: [] }));
    show();
    expect(await screen.findByText("No rooms yet")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Add rooms" })).toHaveAttribute("href", "/admin?tab=rooms");
  });

  it("with no rooms, tells members what will appear", async () => {
    rooms.mockResolvedValue(day({ rooms: [] }));
    show();
    expect(await screen.findByText(/When the church adds its rooms/)).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "Add rooms" })).not.toBeInTheDocument();
  });

  it("says the room calendar isn't available when the server hides it", async () => {
    rooms.mockRejectedValue(new ApiError(404, "not found"));
    show();
    expect(await screen.findByText("The room calendar isn't available")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Open the calendar" })).toHaveAttribute("href", "/calendar");
  });

  it("steps by a day or a week from the date shown", async () => {
    rooms.mockResolvedValue(day());
    show();
    await screen.findByRole("region", { name: /Library/ });
    fireEvent.click(screen.getByRole("button", { name: "Next day" }));
    await vi.waitFor(() => expect(rooms).toHaveBeenLastCalledWith({ view: "day", date: "2026-10-22" }));
  });

  it("waits for the session instead of asking a signed-in person to log in", () => {
    auth.mockReturnValue({ user: null, isAdmin: false, loading: true });
    show();
    expect(screen.queryByRole("link", { name: "Log in" })).not.toBeInTheDocument();
    expect(screen.getByText("Loading the room calendar…")).toBeInTheDocument();
  });

  it("keeps the waiting label in view on a short Day block", async () => {
    auth.mockReturnValue({ user: { id: 1, accountType: "adult" }, isAdmin: true });
    const short = { ...hall.bookings[0], endsAt: "2026-10-22T01:30:00Z", waiting: true };
    rooms.mockResolvedValue(day({ rooms: [{ ...hall, bookings: [short] }] }));
    show();
    const col = await screen.findByRole("region", { name: /Fellowship Hall/ });
    expect(within(col).getByRole("link")).toHaveTextContent(/^7:00\sPM Waiting for approval · Bible study$/);
  });

  it("suggests a narrower range when bookings were left out", async () => {
    rooms.mockResolvedValue(day({ truncated: true }));
    const { unmount } = show();
    expect(await screen.findByText(/Try another day\./)).toBeInTheDocument();
    unmount();
    rooms.mockResolvedValue(day({ from: "2026-10-18", to: "2026-10-24", truncated: true }));
    show("/calendar/rooms?view=week&date=2026-10-21");
    expect(await screen.findByText(/Try the Day view\./)).toBeInTheDocument();
  });

  it("asks a signed-out visitor to log in", () => {
    auth.mockReturnValue({ user: null, isAdmin: false });
    show();
    expect(screen.getByRole("link", { name: "Log in" })).toHaveAttribute("href", "/login");
    expect(rooms).not.toHaveBeenCalled();
  });

  it("keeps the calendar views in the switch, with Rooms current", async () => {
    rooms.mockResolvedValue(day());
    show();
    const nav = screen.getByRole("navigation", { name: "Calendar view" });
    expect(within(nav).getByRole("link", { name: "Rooms" })).toHaveAttribute("aria-current", "page");
    expect(within(nav).getByRole("link", { name: "Week" })).toHaveAttribute("href", "/calendar?view=week&date=2026-10-21");
    await screen.findByRole("region", { name: /Library/ });
  });
});
