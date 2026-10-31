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

function show(list: Room[] | null = rooms) {
  return render(<MemoryRouter><MatchLocations rooms={list} /></MemoryRouter>);
}
const row = (location: string) => screen.getByText(location).closest("li") as HTMLElement;

beforeEach(() => {
  vi.resetAllMocks();
  vi.mocked(AdminRooms.locations).mockResolvedValue({ locations: [hallPlace, gym] });
});

describe("MatchLocations", () => {
  it("explains itself and lists each typed place with its counts and a room picker", async () => {
    show();
    expect(await screen.findByRole("heading", { name: "Match old locations" })).toBeInTheDocument();
    expect(screen.getByText("Before rooms, places were typed. Pick the room each typed place means, and its upcoming dates are booked there. Typed places stay as they are.")).toBeInTheDocument();
    const a = row("fellowship hall");
    expect(within(a).getByText("12 upcoming dates across 3 events")).toBeInTheDocument();
    expect(within(row("The gym")).getByText("1 upcoming date in 1 event")).toBeInTheDocument();
    const select = within(a).getByRole("combobox", { name: "Room for “fellowship hall”" });
    // Active rooms only, restricted ones included (admins may book them).
    expect(Array.from((select as HTMLSelectElement).options).map((o) => o.textContent)).toEqual([
      "Choose a room", "Fellowship Hall · Main", "Chapel", "Nursery",
    ]);
  });

  it("picks the room a place names, ignoring case, and leaves the rest unchosen with the button off", async () => {
    show();
    await screen.findByText("fellowship hall");
    const hallSelect = within(row("fellowship hall")).getByRole("combobox");
    expect(hallSelect).toHaveValue("1");
    expect(within(row("fellowship hall")).getByRole("button", { name: /^Book these dates/ })).toBeEnabled();
    expect(within(row("The gym")).getByRole("combobox")).toHaveValue("");
    expect(within(row("The gym")).getByRole("button", { name: /^Book these dates/ })).toBeDisabled();
  });

  it("does not guess when two rooms share the name", async () => {
    show([hall, room(9, "Fellowship Hall", { building: "Annex" })]);
    await screen.findByText("fellowship hall");
    expect(within(row("fellowship hall")).getByRole("combobox")).toHaveValue("");
  });

  it("books nothing when the select changes, only on the button", async () => {
    show();
    await screen.findByText("The gym");
    const r = row("The gym");
    await userEvent.selectOptions(within(r).getByRole("combobox"), "2");
    expect(AdminRooms.matchLocation).not.toHaveBeenCalled();
    expect(within(r).getByRole("button", { name: /^Book these dates/ })).toBeEnabled();
  });

  it("books on the button, says what happened, refetches, and moves the cursor to the message", async () => {
    vi.mocked(AdminRooms.matchLocation).mockResolvedValue({ booked: 11, busy: [], busyTotal: 0, series: 1 });
    vi.mocked(AdminRooms.locations).mockResolvedValueOnce({ locations: [hallPlace, gym] }).mockResolvedValueOnce({ locations: [gym] });
    show();
    await screen.findByText("fellowship hall");
    await userEvent.click(within(row("fellowship hall")).getByRole("button", { name: "Book these dates for “fellowship hall”" }));
    expect(AdminRooms.matchLocation).toHaveBeenCalledWith("fellowship hall", 1);
    const status = await screen.findByRole("status");
    expect(status).toHaveTextContent("Booked Fellowship Hall · Main for 11 dates. 1 series will book its later dates there too.");
    expect(status).toHaveFocus();
    expect(AdminRooms.locations).toHaveBeenCalledTimes(2);
    await waitFor(() => expect(screen.queryByText("fellowship hall")).not.toBeInTheDocument());
    expect(screen.getByText("The gym")).toBeInTheDocument();
  });

  it("lists the dates left alone, with links, and says how many more", async () => {
    vi.mocked(AdminRooms.matchLocation).mockResolvedValue({
      booked: 9, series: 2, busyTotal: 103,
      busy: [{ eventId: 7, eventTitle: "Choir practice", date: "2026-11-10" }, { eventId: 8, eventTitle: "Potluck", date: "2026-11-17" }],
    });
    show();
    await screen.findByText("fellowship hall");
    await userEvent.click(within(row("fellowship hall")).getByRole("button", { name: /^Book these dates/ }));
    const status = await screen.findByRole("status");
    expect(status).toHaveTextContent("Booked Fellowship Hall · Main for 9 dates. 2 series will book their later dates there too. 103 dates were taken, so they were left alone:");
    expect(within(status).getByRole("link", { name: "Choir practice" })).toHaveAttribute("href", "/events/7");
    expect(within(status).getByText(/Tuesday, November 17/)).toBeInTheDocument();
    expect(within(status).getByText("and 101 more")).toBeInTheDocument();
  });

  it("says a single taken date in the singular, and when nothing was booked", async () => {
    vi.mocked(AdminRooms.matchLocation).mockResolvedValue({ booked: 0, series: 0, busyTotal: 1, busy: [{ eventId: 7, eventTitle: "Choir practice", date: "2026-11-10" }] });
    show();
    await screen.findByText("The gym");
    const r = row("The gym");
    await userEvent.selectOptions(within(r).getByRole("combobox"), "2");
    await userEvent.click(within(r).getByRole("button", { name: /^Book these dates/ }));
    const status = await screen.findByRole("status");
    expect(status).toHaveTextContent("No dates were booked in Chapel. 1 date was taken, so it was left alone:");
    expect(within(status).queryByText(/more$/)).not.toBeInTheDocument();
  });

  it("shows the neutral empty state when no typed place is left", async () => {
    vi.mocked(AdminRooms.locations).mockResolvedValue({ locations: [] });
    show();
    expect(await screen.findByText("No typed places left to match")).toBeInTheDocument();
    expect(screen.getByText("Every upcoming date with a typed place has a room, or none has a place.")).toBeInTheDocument();
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

  it("keeps the row and says what went wrong when booking fails", async () => {
    vi.mocked(AdminRooms.matchLocation).mockRejectedValue(new ApiError(500, "private server text"));
    show();
    await screen.findByText("fellowship hall");
    const r = row("fellowship hall");
    await userEvent.click(within(r).getByRole("button", { name: /^Book these dates/ }));
    expect(await within(r).findByRole("alert")).toHaveTextContent("We couldn't book those dates.");
    expect(screen.queryByText("private server text")).not.toBeInTheDocument();
    expect(within(r).getByRole("button", { name: /^Book these dates/ })).toBeEnabled();
  });
});
