import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, expect, it, vi } from "vitest";
import { ApiError, DateRooms, Site } from "../api";
import type { RoomBooking, RoomPickerItem } from "../types";
import { Form } from "./ui/Form";
import { RoomField } from "./RoomField";

const actor = { isAdmin: false };
vi.mock("../auth", () => ({ useAuth: () => actor }));
vi.mock("../api", async (importActual) => ({
  ...(await importActual<typeof import("../api")>()),
  Site: { get: vi.fn() },
  DateRooms: { list: vi.fn(), set: vi.fn() },
}));

const hall: RoomPickerItem = { id: 1, name: "Fellowship Hall", building: "Main", capacity: 80, features: "", restricted: false, busy: false, tooSmall: false };
const chapel: RoomPickerItem = { id: 2, name: "Chapel", building: "", capacity: null, features: "", restricted: false, busy: true, tooSmall: false };
const closet: RoomPickerItem = { id: 3, name: "Closet", building: "Annex", capacity: 1, features: "", restricted: false, busy: false, tooSmall: true };
const booked = (over: Partial<RoomBooking> = {}): RoomBooking => ({ roomId: 1, roomName: "Fellowship Hall", building: "Main", status: "confirmed", setupMinutes: 30, teardownMinutes: 0, doubleBooked: false, tooSmall: false, pending: false, ...over });
const busy = () => new ApiError(409, "busy", { code: "room_busy" });

beforeEach(() => {
  vi.resetAllMocks();
  actor.isAdmin = false;
  vi.mocked(Site.get).mockResolvedValue({ name: "Fold", timezone: "UTC", selfSignup: "off", roomsEnabled: true, pushEnabled: true });
  vi.mocked(DateRooms.list).mockResolvedValue({ rooms: [hall, chapel, closet], booking: null });
});

// Inside a Form, as in both editors, so nothing here can submit it by accident.
function show(onChanged = vi.fn()) {
  render(<Form noValidate onSubmit={(e) => e.preventDefault()}><RoomField eventId={7} date="2026-12-01" idPrefix="t" onChanged={onChanged} /></Form>);
  return onChanged;
}
const select = () => screen.findByRole("combobox", { name: /^Room/ });

it("loads the rooms for the date and words each option", async () => {
  show();
  const box = await select();
  expect(DateRooms.list).toHaveBeenCalledWith(7, "2026-12-01");
  const labels = Array.from((box as HTMLSelectElement).options).map((o) => o.textContent);
  expect(labels).toEqual([
    "No room",
    "Fellowship Hall · Main (80 seats)",
    "Chapel — in use at this time",
    "Closet · Annex (1 seat) — may be too small",
  ]);
  expect(box).toHaveValue("none");
  expect(screen.getByText("A room that's in use can only be booked by an admin. Ask an admin if you need it.")).toBeInTheDocument();
  expect(screen.getByText(/If bookings need an admin's approval, it waits for one\./)).toBeInTheDocument();
});

it("shows a loading line, not a blocked editor, until the rooms arrive", async () => {
  let release!: (v: { rooms: RoomPickerItem[]; booking: null }) => void;
  vi.mocked(DateRooms.list).mockReturnValue(new Promise((r) => { release = r; }));
  show();
  expect(await screen.findByText("Loading rooms…")).toBeInTheDocument();
  release({ rooms: [hall], booking: null });
  expect(await select()).toBeInTheDocument();
});

it("disables a busy room for a leader and keeps a too-small room selectable", async () => {
  show();
  await select();
  expect(screen.getByRole("option", { name: /^Chapel/ })).toBeDisabled();
  expect(screen.getByRole("option", { name: /^Closet/ })).toBeEnabled();
});

it("lets an admin pick a busy room", async () => {
  actor.isAdmin = true;
  show();
  await select();
  expect(screen.queryByText(/can only be booked by an admin/)).not.toBeInTheDocument();
  expect(screen.getByRole("option", { name: /^Chapel/ })).toBeEnabled();
});

it("books the chosen room at once and says so", async () => {
  vi.mocked(DateRooms.set).mockResolvedValue({ booking: booked() });
  const onChanged = show();
  await userEvent.selectOptions(await select(), "1");
  expect(DateRooms.set).toHaveBeenCalledWith(7, "2026-12-01", { roomId: 1 });
  expect(await screen.findByRole("status")).toHaveTextContent("Booked Fellowship Hall.");
  expect(await select()).toHaveValue("1");
  expect(onChanged).toHaveBeenCalledTimes(1);
});

it("releases the room when No room is chosen", async () => {
  vi.mocked(DateRooms.list).mockResolvedValue({ rooms: [hall], booking: booked() });
  vi.mocked(DateRooms.set).mockResolvedValue({ booking: null });
  show();
  const box = await select();
  expect(box).toHaveValue("1");
  await userEvent.selectOptions(box, "none");
  expect(DateRooms.set).toHaveBeenCalledWith(7, "2026-12-01", { roomId: null });
  expect(await screen.findByRole("status")).toHaveTextContent("Room released.");
});

it("shows a leader the error for a room that is taken, with no way around it", async () => {
  actor.isAdmin = false;
  vi.mocked(DateRooms.set).mockRejectedValue(busy());
  show();
  await userEvent.selectOptions(await select(), "3");
  expect(await screen.findByRole("alert")).toHaveTextContent("That room is already booked then.");
  expect(screen.queryByRole("button", { name: "Book anyway" })).not.toBeInTheDocument();
  expect(await select()).toHaveValue("none");
});

it("offers an admin Book anyway with a required reason, and resends it", async () => {
  actor.isAdmin = true;
  vi.mocked(DateRooms.set).mockRejectedValueOnce(busy()).mockResolvedValueOnce({ booking: booked({ roomId: 2, roomName: "Chapel", building: "", doubleBooked: true }) });
  show();
  await userEvent.selectOptions(await select(), "2");
  const reason = await screen.findByRole("textbox", { name: /^Reason for double-booking/ });
  expect(reason).toHaveAttribute("maxlength", "500");
  expect(screen.getByRole("alert")).toHaveTextContent("That room is in use at this time. Give a reason to book it anyway.");
  await waitFor(() => expect(reason).toHaveFocus());
  await userEvent.click(screen.getByRole("button", { name: "Book anyway" }));
  expect(await screen.findByText("Say why you are double-booking this room.")).toBeInTheDocument();
  expect(reason).toHaveFocus();
  expect(DateRooms.set).toHaveBeenCalledTimes(1);
  await userEvent.type(reason, "Choir moved");
  await userEvent.click(screen.getByRole("button", { name: "Book anyway" }));
  await waitFor(() => expect(DateRooms.set).toHaveBeenLastCalledWith(7, "2026-12-01", { roomId: 2, overlapReason: "Choir moved" }));
  expect(await screen.findByRole("status")).toHaveTextContent("Booked Chapel over another booking.");
  expect(screen.queryByRole("textbox", { name: /^Reason/ })).not.toBeInTheDocument();
});

it("warns, in words, when a date lost its room by moving", async () => {
  vi.mocked(DateRooms.list).mockResolvedValue({ rooms: [hall], booking: booked({ status: "requested" }) });
  show();
  const box = await select();
  expect(screen.getByText(/Fellowship Hall was taken at the new time, so this date has no room\. Pick another room\./)).toBeInTheDocument();
  expect(box).toHaveValue("");
  expect(screen.getByRole("status")).toHaveTextContent("Pick another room.");
  expect(screen.queryByRole("button", { name: "Setup and cleanup times" })).not.toBeInTheDocument();
});

it("lets an admin, and only an admin, override setup and cleanup minutes", async () => {
  vi.mocked(DateRooms.list).mockResolvedValue({ rooms: [hall], booking: booked() });
  vi.mocked(DateRooms.set).mockResolvedValue({ booking: booked({ setupMinutes: 45, teardownMinutes: 15 }) });
  actor.isAdmin = true;
  show();
  await select();
  const toggle = screen.getByRole("button", { name: "Setup and cleanup times" });
  expect(toggle).toHaveAttribute("aria-expanded", "false");
  await userEvent.click(toggle);
  const setup = screen.getByRole("spinbutton", { name: "Setup minutes" });
  const cleanup = screen.getByRole("spinbutton", { name: "Cleanup minutes" });
  expect(setup).toHaveValue(30);
  expect(cleanup).toHaveValue(0);
  await userEvent.clear(setup);
  await userEvent.type(setup, "45");
  await userEvent.clear(cleanup);
  await userEvent.type(cleanup, "15");
  await userEvent.click(screen.getByRole("button", { name: "Save times" }));
  expect(DateRooms.set).toHaveBeenCalledWith(7, "2026-12-01", { roomId: 1, setupMinutes: 45, teardownMinutes: 15 });
  expect(await screen.findByRole("status")).toHaveTextContent("Setup and cleanup times saved.");
});

it("never shows a leader the padding override", async () => {
  vi.mocked(DateRooms.list).mockResolvedValue({ rooms: [hall], booking: booked() });
  show();
  await select();
  expect(screen.queryByRole("button", { name: "Setup and cleanup times" })).not.toBeInTheDocument();
});

it("refuses minutes outside 0 to 240 without asking the server", async () => {
  vi.mocked(DateRooms.list).mockResolvedValue({ rooms: [hall], booking: booked() });
  actor.isAdmin = true;
  show();
  await select();
  await userEvent.click(screen.getByRole("button", { name: "Setup and cleanup times" }));
  const setup = screen.getByRole("spinbutton", { name: "Setup minutes" });
  await userEvent.clear(setup);
  await userEvent.type(setup, "300");
  await userEvent.click(screen.getByRole("button", { name: "Save times" }));
  expect(await screen.findByText("Enter a whole number of minutes from 0 to 240 for both.")).toBeInTheDocument();
  expect(setup).toHaveFocus();
  expect(DateRooms.set).not.toHaveBeenCalled();
});

it("saves the times on Enter without submitting the editor around them", async () => {
  vi.mocked(DateRooms.list).mockResolvedValue({ rooms: [hall], booking: booked() });
  vi.mocked(DateRooms.set).mockResolvedValue({ booking: booked({ setupMinutes: 45 }) });
  actor.isAdmin = true;
  const onSubmit = vi.fn((e: { preventDefault: () => void }) => e.preventDefault());
  render(<Form noValidate onSubmit={onSubmit}><RoomField eventId={7} date="2026-12-01" idPrefix="t" /></Form>);
  await select();
  await userEvent.click(screen.getByRole("button", { name: "Setup and cleanup times" }));
  const setup = screen.getByRole("spinbutton", { name: "Setup minutes" });
  await userEvent.clear(setup);
  await userEvent.type(setup, "45{Enter}");
  expect(DateRooms.set).toHaveBeenCalledWith(7, "2026-12-01", { roomId: 1, setupMinutes: 45, teardownMinutes: 0 });
  expect(onSubmit).not.toHaveBeenCalled();
});

it("announces the too-small warning in the status region", async () => {
  vi.mocked(DateRooms.set).mockResolvedValue({ booking: booked({ roomId: 3, roomName: "Closet", tooSmall: true }) });
  show();
  await userEvent.selectOptions(await select(), "3");
  await waitFor(() => expect(screen.getByRole("status")).toHaveTextContent("This room seats fewer people than this date allows."));
  expect(screen.getAllByRole("status")).toHaveLength(1);
});

it("shows nothing, and asks for no rooms, while rooms are off", async () => {
  vi.mocked(Site.get).mockResolvedValue({ name: "Fold", timezone: "UTC", selfSignup: "off", roomsEnabled: false, pushEnabled: true });
  const { container } = render(<RoomField eventId={7} date="2026-12-01" idPrefix="t" />);
  await waitFor(() => expect(Site.get).toHaveBeenCalled());
  expect(container).toBeEmptyDOMElement();
  expect(DateRooms.list).not.toHaveBeenCalled();
});

it("shows nothing, not an error, when the site settings fail to load", async () => {
  vi.mocked(Site.get).mockRejectedValue(new ApiError(500, "down", { code: "server_error" }));
  const { container } = render(<RoomField eventId={7} date="2026-12-01" idPrefix="t" />);
  await waitFor(() => expect(Site.get).toHaveBeenCalled());
  expect(container).toBeEmptyDOMElement();
});

it("offers a retry when the rooms fail to load", async () => {
  vi.mocked(DateRooms.list).mockRejectedValueOnce(new ApiError(500, "down", { code: "server_error" }));
  show();
  await userEvent.click(await screen.findByRole("button", { name: "Try again" }));
  expect(await select()).toBeInTheDocument();
  expect(DateRooms.list).toHaveBeenCalledTimes(2);
});

it("puts the cursor in the Room select once the rooms load, only when asked", async () => {
  let release!: (v: { rooms: RoomPickerItem[]; booking: null }) => void;
  vi.mocked(DateRooms.list).mockReturnValue(new Promise((r) => { release = r; }));
  render(<RoomField eventId={7} date="2026-12-01" idPrefix="t" focusOnLoad />);
  expect(await screen.findByText("Loading rooms…")).toBeInTheDocument();
  release({ rooms: [hall], booking: null });
  const box = await select();
  await waitFor(() => expect(box).toHaveFocus());
});

it("leaves focus alone without focusOnLoad", async () => {
  show();
  expect(await select()).not.toHaveFocus();
});

it("says an admin didn't approve a date, keeps No room chosen, and asks for another room", async () => {
  vi.mocked(DateRooms.list).mockResolvedValue({ rooms: [hall], booking: booked({ status: "rejected" }) });
  show();
  const box = await select();
  expect(box).toHaveValue("none");
  expect(screen.getByRole("status")).toHaveTextContent("An admin didn't approve Fellowship Hall for this date. Pick another room.");
});

it("says in words that a booking waits for an admin, and shows its room", async () => {
  vi.mocked(DateRooms.list).mockResolvedValue({ rooms: [hall], booking: booked({ status: "requested", pending: true }) });
  show();
  const box = await select();
  expect(box).toHaveValue("1");
  expect(screen.getByRole("status")).toHaveTextContent("Waiting for an admin to approve Fellowship Hall.");
  // Not the "lost its room" warning.
  expect(screen.queryByText(/was taken at the new time/)).not.toBeInTheDocument();
});

it("shows the waiting line, not Booked, after choosing a room that needs approval", async () => {
  vi.mocked(DateRooms.set).mockResolvedValue({ booking: booked({ status: "requested", pending: true }) });
  show();
  await userEvent.selectOptions(await select(), "1");
  await waitFor(() => expect(screen.getByRole("status")).toHaveTextContent("Waiting for an admin to approve Fellowship Hall."));
  expect(screen.getByRole("status")).not.toHaveTextContent("Booked Fellowship Hall.");
  expect(await select()).toHaveValue("1");
});

// Each <optgroup> of the select, with its options' words (M2.3, DEC-110).
function groupsOf(box: HTMLElement) {
  return Array.from(box.querySelectorAll("optgroup")).map((g) => ({
    label: g.label,
    options: Array.from(g.querySelectorAll("option")).map((o) => o.textContent),
  }));
}

it("lists suggested rooms first with why, then every other room once", async () => {
  vi.mocked(DateRooms.list).mockResolvedValue({
    rooms: [hall, chapel, closet], booking: null,
    suggestions: { rooms: [{ roomId: 3, uses: 6, reason: "Used by this group 6 times", busyDates: 0 }, { roomId: 2, uses: 1, reason: "Used once", busyDates: 1 }], otherKinds: false, dates: 1 },
  });
  show();
  const box = await select();
  expect((box as HTMLSelectElement).options[0].textContent).toBe("No room");
  expect(groupsOf(box)).toEqual([
    { label: "Suggested", options: ["Closet · Annex (1 seat) — may be too small — Used by this group 6 times", "Chapel — in use at this time — Used once"] },
    { label: "Other rooms", options: ["Fellowship Hall · Main (80 seats)"] },
  ]);
  // A busy suggestion is still only an admin's to book.
  expect(screen.getByRole("option", { name: /^Chapel/ })).toBeDisabled();
  await userEvent.selectOptions(box, "3");
  expect(DateRooms.set).toHaveBeenCalledWith(7, "2026-12-01", { roomId: 3 });
});

it("labels suggestions from other kinds of events, and shows no groups without suggestions", async () => {
  vi.mocked(DateRooms.list).mockResolvedValue({
    rooms: [hall, chapel], booking: null,
    suggestions: { rooms: [{ roomId: 1, uses: 2, reason: "Used 2 times", busyDates: 0 }], otherKinds: true, dates: 1 },
  });
  const { unmount } = render(<Form noValidate onSubmit={(e) => e.preventDefault()}><RoomField eventId={7} date="2026-12-01" idPrefix="t" /></Form>);
  expect(groupsOf(await select()).map((g) => g.label)).toEqual(["Used by other kinds of events", "Other rooms"]);
  unmount();
  vi.mocked(DateRooms.list).mockResolvedValue({ rooms: [hall, chapel], booking: null, suggestions: { rooms: [], otherKinds: false, dates: 1 } });
  show();
  expect(groupsOf(await select())).toEqual([]);
});
