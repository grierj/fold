import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { AdminRooms, ApiError } from "../../api";
import type { Room, TypedLocation } from "../../types";
import { MatchLocations } from "./MatchLocations";

vi.mock("../../api", async (actual) => ({
  ...(await actual<typeof import("../../api")>()),
  AdminRooms: { locations: vi.fn(), matchLocation: vi.fn() },
}));

const room = (id: number, name: string, extra: Partial<Room> = {}): Room => ({
  id, name, building: "", capacity: null, features: "", notes: "", active: true, restricted: false,
  defaultSetupMinutes: 0, defaultTeardownMinutes: 0, ...extra,
});
const hall = room(1, "Fellowship Hall", { building: "Main" });
const chapel = room(2, "Chapel");
const nursery = room(3, "Nursery", { restricted: true });
const closed = room(4, "Old Annex", { active: false });
const rooms = [hall, chapel, nursery, closed];

const typed = (location: string, dates: number, events: number): TypedLocation => ({ location, dates, events });
const hallPlace = typed("fellowship hall", 12, 3);
const gym = typed("The gym", 1, 1);
const noBusy = { booked: 1, busy: [], busyTotal: 0, series: 0 };

function show(list: Room[] | null = rooms) {
  return render(<MemoryRouter><MatchLocations rooms={list} /></MemoryRouter>);
}
const row = (location: string) => screen.getByText(location).closest("li") as HTMLElement;
const btn = (location: string) => within(row(location)).getByRole("button");
const choose = (location: string, roomId: string) => userEvent.selectOptions(within(row(location)).getByRole("combobox"), roomId);

function deferred<T>() {
  let resolve!: (v: T) => void;
  const promise = new Promise<T>((r) => { resolve = r; });
  return { promise, resolve };
}

beforeEach(() => {
  vi.resetAllMocks();
  vi.mocked(AdminRooms.locations).mockResolvedValue({ locations: [hallPlace, gym] });
});

describe("MatchLocations", () => {
  it("explains itself and lists each typed place with its counts and a room picker", async () => {
    show();
    expect(await screen.findByRole("heading", { name: "Match old locations" })).toBeInTheDocument();
    expect(screen.getByText("Before rooms, people typed a place for each event. Pick the room each one means, and its upcoming dates are booked there. Typed places stay as they are.")).toBeInTheDocument();
    const a = row("fellowship hall");
    expect(within(a).getByText("12 upcoming dates across 3 events")).toBeInTheDocument();
    expect(within(row("The gym")).getByText("1 upcoming date in 1 event")).toBeInTheDocument();
    const select = within(a).getByRole("combobox", { name: "Room for “fellowship hall”" });
    // Active rooms only, restricted ones included (admins may book them).
    expect(Array.from((select as HTMLSelectElement).options).map((o) => o.textContent)).toEqual([
      "Choose a room", "Fellowship Hall · Main", "Chapel", "Nursery",
    ]);
  });

  it("picks the room a place names, ignoring case, and words each button for what it will do", async () => {
    show();
    await screen.findByText("fellowship hall");
    expect(within(row("fellowship hall")).getByRole("combobox")).toHaveValue("1");
    expect(btn("fellowship hall")).toHaveTextContent("Book 12 dates in Fellowship Hall · Main");
    expect(btn("fellowship hall")).toBeEnabled();
    expect(within(row("The gym")).getByRole("combobox")).toHaveValue("");
    expect(btn("The gym")).toHaveTextContent("Pick a room");
    expect(btn("The gym")).toBeDisabled();
    await choose("The gym", "2");
    expect(btn("The gym")).toHaveTextContent("Book 1 date in Chapel");
    expect(btn("The gym")).toBeEnabled();
  });

  it("names the typed place in the button for a screen reader", async () => {
    show();
    await screen.findByText("fellowship hall");
    expect(screen.getByRole("button", { name: "Book 12 dates in Fellowship Hall · Main for “fellowship hall”" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Pick a room for “The gym”" })).toBeInTheDocument();
  });

  it("does not guess when two rooms share the name", async () => {
    show([hall, room(9, "Fellowship Hall", { building: "Annex" })]);
    await screen.findByText("fellowship hall");
    expect(within(row("fellowship hall")).getByRole("combobox")).toHaveValue("");
  });

  it("books nothing when the select changes, only on the button", async () => {
    show();
    await screen.findByText("The gym");
    await choose("The gym", "2");
    expect(AdminRooms.matchLocation).not.toHaveBeenCalled();
    expect(btn("The gym")).toBeEnabled();
  });

  it("books on the button, says what happened, refetches, and moves the cursor to the message", async () => {
    vi.mocked(AdminRooms.matchLocation).mockResolvedValue({ booked: 11, busy: [], busyTotal: 0, series: 1 });
    vi.mocked(AdminRooms.locations).mockResolvedValueOnce({ locations: [hallPlace, gym] }).mockResolvedValueOnce({ locations: [gym] });
    show();
    await screen.findByText("fellowship hall");
    await userEvent.click(btn("fellowship hall"));
    expect(AdminRooms.matchLocation).toHaveBeenCalledWith("fellowship hall", 1);
    const status = await screen.findByRole("status");
    expect(status).toHaveTextContent("Booked Fellowship Hall · Main for 11 dates. 1 repeating event now uses Fellowship Hall · Main for its later dates too.");
    expect(status).toHaveFocus();
    expect(AdminRooms.locations).toHaveBeenCalledTimes(2);
    await waitFor(() => expect(screen.queryByText("fellowship hall")).not.toBeInTheDocument());
    expect(screen.getByText("The gym")).toBeInTheDocument();
  });

  it("announces only the summary and keeps the dates left alone outside the live region", async () => {
    vi.mocked(AdminRooms.matchLocation).mockResolvedValue({
      booked: 9, series: 2, busyTotal: 103,
      busy: [{ eventId: 7, eventTitle: "Choir practice", date: "2026-11-10" }, { eventId: 8, eventTitle: "Potluck", date: "2026-11-17" }],
    });
    show();
    await screen.findByText("fellowship hall");
    await userEvent.click(btn("fellowship hall"));
    const status = await screen.findByRole("status");
    expect(status).toHaveTextContent("Booked Fellowship Hall · Main for 9 dates. 2 repeating events now use Fellowship Hall · Main for their later dates too. 103 dates were taken, so they were left alone:");
    expect(status.tagName).toBe("P");
    expect(within(status).queryByRole("link")).not.toBeInTheDocument();
    const choir = screen.getByRole("link", { name: "Choir practice" });
    expect(choir).toHaveAttribute("href", "/events/7");
    expect(screen.getByText(/Tuesday, November 17/)).toBeInTheDocument();
    expect(status.contains(choir)).toBe(false);
    expect(screen.getByText("and 101 more").tagName).toBe("P");
    expect(screen.getByText("Open an event to pick another room for its date.")).toBeInTheDocument();
  });

  it("says a single taken date in the singular, and nothing about a list when none were left alone", async () => {
    vi.mocked(AdminRooms.matchLocation).mockResolvedValue({ booked: 0, series: 0, busyTotal: 1, busy: [{ eventId: 7, eventTitle: "Choir practice", date: "2026-11-10" }] });
    show();
    await screen.findByText("The gym");
    await choose("The gym", "2");
    await userEvent.click(btn("The gym"));
    const status = await screen.findByRole("status");
    expect(status).toHaveTextContent("No dates were booked in Chapel. 1 date was taken, so it was left alone:");
    expect(screen.queryByText(/more$/)).not.toBeInTheDocument();
  });

  it("says the dates already have rooms when nothing was booked and nothing was taken", async () => {
    vi.mocked(AdminRooms.matchLocation).mockResolvedValue({ booked: 0, series: 0, busyTotal: 0, busy: [] });
    show();
    await screen.findByText("The gym");
    await choose("The gym", "2");
    await userEvent.click(btn("The gym"));
    expect(await screen.findByRole("status")).toHaveTextContent("No dates were booked in Chapel. They already have rooms.");
    expect(screen.queryByText("Open an event to pick another room for its date.")).not.toBeInTheDocument();
  });

  it("replaces the first result with the second", async () => {
    vi.mocked(AdminRooms.matchLocation)
      .mockResolvedValueOnce({ booked: 12, busy: [], busyTotal: 0, series: 0 })
      .mockResolvedValueOnce({ booked: 1, busy: [{ eventId: 7, eventTitle: "Choir practice", date: "2026-11-10" }], busyTotal: 1, series: 0 });
    vi.mocked(AdminRooms.locations).mockResolvedValue({ locations: [hallPlace, gym] });
    show();
    await screen.findByText("fellowship hall");
    await userEvent.click(btn("fellowship hall"));
    await screen.findByText("Booked Fellowship Hall · Main for 12 dates.");
    await choose("The gym", "2");
    await userEvent.click(btn("The gym"));
    await screen.findByText(/^Booked Chapel for 1 date\./);
    expect(screen.queryByText("Booked Fellowship Hall · Main for 12 dates.")).not.toBeInTheDocument();
    expect(screen.getAllByRole("status")).toHaveLength(1);
    expect(screen.getByRole("status")).toHaveFocus();
  });

  it("disables every Book button while one booking is in flight", async () => {
    const pending = deferred<{ booked: number; busy: never[]; busyTotal: number; series: number }>();
    vi.mocked(AdminRooms.matchLocation).mockReturnValue(pending.promise);
    show();
    await screen.findByText("fellowship hall");
    await choose("The gym", "2");
    await userEvent.click(btn("fellowship hall"));
    expect(btn("fellowship hall")).toHaveTextContent("Booking…");
    expect(btn("fellowship hall")).toBeDisabled();
    expect(btn("The gym")).toBeDisabled();
    pending.resolve({ booked: 1, busy: [], busyTotal: 0, series: 0 });
    await waitFor(() => expect(btn("The gym")).toBeEnabled());
  });

  it("keeps a picked room through the refetch that follows a booking", async () => {
    vi.mocked(AdminRooms.matchLocation).mockResolvedValue(noBusy);
    vi.mocked(AdminRooms.locations).mockResolvedValue({ locations: [hallPlace, gym] });
    show();
    await screen.findByText("fellowship hall");
    await choose("The gym", "3");
    await userEvent.click(btn("fellowship hall"));
    await screen.findByRole("status");
    await waitFor(() => expect(AdminRooms.locations).toHaveBeenCalledTimes(2));
    expect(within(row("The gym")).getByRole("combobox")).toHaveValue("3");
    expect(btn("The gym")).toHaveTextContent("Book 1 date in Nursery");
  });

  it("keeps the result and shows the neutral empty state when booking uses up the last typed place", async () => {
    vi.mocked(AdminRooms.matchLocation).mockResolvedValue(noBusy);
    vi.mocked(AdminRooms.locations).mockResolvedValueOnce({ locations: [gym] }).mockResolvedValueOnce({ locations: [] });
    show();
    await screen.findByText("The gym");
    await choose("The gym", "2");
    await userEvent.click(btn("The gym"));
    expect(await screen.findByText("No typed places left to match")).toBeInTheDocument();
    expect(screen.getByText("Places typed into upcoming events will show up here until they have a room.")).toBeInTheDocument();
    expect(screen.getByRole("status")).toHaveTextContent("Booked Chapel for 1 date.");
  });

  it("is hidden when the first load finds no typed place", async () => {
    vi.mocked(AdminRooms.locations).mockResolvedValue({ locations: [] });
    const { container } = show();
    await waitFor(() => expect(AdminRooms.locations).toHaveBeenCalled());
    await waitFor(() => expect(container).toBeEmptyDOMElement());
  });

  it("shows nothing before there is an active room to match to", async () => {
    const { container, unmount } = show([closed]);
    expect(container).toBeEmptyDOMElement();
    unmount();
    const second = show(null);
    expect(second.container).toBeEmptyDOMElement();
    expect(AdminRooms.locations).not.toHaveBeenCalled();
  });

  it("offers a retry when the list fails to load, in the shared words", async () => {
    vi.mocked(AdminRooms.locations).mockRejectedValueOnce(new ApiError(500, "private server text"));
    show();
    expect(await screen.findByRole("alert")).toHaveTextContent("We couldn't load the typed places.");
    expect(screen.queryByText("private server text")).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByText("The gym")).toBeInTheDocument();
  });

  it("keeps the row, says what went wrong, and puts the cursor back on its button when booking fails", async () => {
    vi.mocked(AdminRooms.matchLocation).mockRejectedValue(new ApiError(500, "private server text"));
    show();
    await screen.findByText("fellowship hall");
    const r = row("fellowship hall");
    await userEvent.click(btn("fellowship hall"));
    expect(await within(r).findByRole("alert")).toHaveTextContent("We couldn't book those dates.");
    expect(screen.queryByText("private server text")).not.toBeInTheDocument();
    expect(btn("fellowship hall")).toBeEnabled();
    await waitFor(() => expect(btn("fellowship hall")).toHaveFocus());
  });
});
