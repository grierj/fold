import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, expect, it, vi } from "vitest";
import { ApiError, SeriesRooms, Site } from "../api";
import type { SeriesBooking, SeriesRoom } from "../types";
import { Form } from "./ui/Form";
import { SeriesRoomField } from "./SeriesRoomField";

const actor = { isAdmin: false };
vi.mock("../auth", () => ({ useAuth: () => actor }));
vi.mock("../api", async (importActual) => ({
  ...(await importActual<typeof import("../api")>()),
  Site: { get: vi.fn() },
  SeriesRooms: { list: vi.fn(), set: vi.fn() },
}));

const room = (over: Partial<SeriesRoom>): SeriesRoom => ({ id: 1, name: "Fellowship Hall", building: "Main", capacity: 80, features: "", restricted: false, busyDates: 0, tooSmall: false, ...over });
const hall = room({});
const chapel = room({ id: 2, name: "Chapel", building: "", capacity: null, busyDates: 2 });
const closet = room({ id: 3, name: "Closet", building: "Annex", capacity: 1, tooSmall: true });
const booking = (over: Partial<SeriesBooking> = {}): SeriesBooking => ({ defaultRoom: { id: 1, name: "Fellowship Hall", building: "Main" }, booked: 10, lost: [], kept: 0, ...over });

beforeEach(() => {
  vi.resetAllMocks();
  actor.isAdmin = false;
  vi.mocked(Site.get).mockResolvedValue({ name: "Fold", timezone: "UTC", selfSignup: "off", roomsEnabled: true });
  vi.mocked(SeriesRooms.list).mockResolvedValue({ rooms: [hall, chapel, closet], dates: 10, defaultRoom: null, needsRoom: [] });
});

function show(onChanged = vi.fn()) {
  render(<Form noValidate onSubmit={(e) => e.preventDefault()}><SeriesRoomField eventId={7} idPrefix="t" onChanged={onChanged} /></Form>);
  return onChanged;
}
const select = () => screen.findByRole("combobox", { name: /^Room for every date/ });
// Choosing is not booking: the button after the select books.
async function pickAndBook(value: string, button = "Book for every date") {
  await userEvent.selectOptions(await select(), value);
  await userEvent.click(screen.getByRole("button", { name: button }));
}

it("words each option like the one-date Room, with how many dates are busy", async () => {
  show();
  const box = await select();
  expect(SeriesRooms.list).toHaveBeenCalledWith(7);
  expect(Array.from((box as HTMLSelectElement).options).map((o) => o.textContent)).toEqual([
    "No series room",
    "Fellowship Hall · Main (80 seats)",
    "Chapel — in use on 2 of 10 dates",
    "Closet · Annex (1 seat) — may be too small",
  ]);
  expect(box).toHaveValue("none");
  expect(screen.getByText(/Choose a room, then book it for every upcoming date./)).toBeInTheDocument();
  expect(screen.getByText(/“No series room” releases the rooms it booked./)).toBeInTheDocument();
});

it("shows the series room that is already set", async () => {
  vi.mocked(SeriesRooms.list).mockResolvedValue({ rooms: [hall], dates: 10, defaultRoom: { id: 1, name: "Fellowship Hall", building: "Main" }, needsRoom: [] });
  show();
  expect(await select()).toHaveValue("1");
});

it("books every date at once and says how many", async () => {
  vi.mocked(SeriesRooms.set).mockResolvedValue(booking({ booked: 8 }));
  const onChanged = show();
  await pickAndBook("1");
  expect(SeriesRooms.set).toHaveBeenCalledWith(7, { roomId: 1 });
  expect(await screen.findByRole("status")).toHaveTextContent("Booked for 8 dates.");
  expect(await select()).toHaveValue("1");
  expect(onChanged).toHaveBeenCalledTimes(1);
});

it("says which dates are busy and which keep their own room", async () => {
  vi.mocked(SeriesRooms.set).mockResolvedValue(booking({ booked: 7, lost: ["2026-12-08", "2026-12-15"], kept: 1 }));
  show();
  await pickAndBook("2");
  expect(await screen.findByRole("status")).toHaveTextContent(
    "Booked for 7 dates. 2 dates are busy; pick another room for them under “Dates that need a room” on the event page. 1 date keeps the room picked for it.",
  );
  // A leader has no way to double-book.
  expect(screen.queryByRole("button", { name: "Book anyway" })).not.toBeInTheDocument();
});

it("uses the singular for one date", async () => {
  vi.mocked(SeriesRooms.set).mockResolvedValue(booking({ booked: 1, lost: ["2026-12-08"] }));
  show();
  await pickAndBook("1");
  expect(await screen.findByRole("status")).toHaveTextContent("Booked for 1 date. 1 date is busy; pick another room for it");
});

it("releases the series room and says what stays", async () => {
  vi.mocked(SeriesRooms.list).mockResolvedValue({ rooms: [hall], dates: 10, defaultRoom: { id: 1, name: "Fellowship Hall", building: "Main" }, needsRoom: [] });
  vi.mocked(SeriesRooms.set).mockResolvedValue({ defaultRoom: null, booked: 0, lost: [], kept: 2 });
  show();
  await pickAndBook("none", "Stop the series room");
  expect(SeriesRooms.set).toHaveBeenCalledWith(7, { roomId: null });
  expect(await screen.findByRole("status")).toHaveTextContent("Series room released. 2 dates keep the room picked for them.");
  expect(await select()).toHaveValue("none");
});

it("shows the error, books nothing, and leaves the button to try again", async () => {
  vi.mocked(SeriesRooms.set).mockRejectedValue(new ApiError(403, "no", { code: "room_restricted" }));
  show();
  await pickAndBook("1");
  expect(await screen.findByRole("alert")).toHaveTextContent("Only an admin can book that room.");
  expect(screen.getByRole("button", { name: "Book for every date" })).toBeEnabled();
  expect(screen.getByRole("status")).toBeEmptyDOMElement();
});

it("offers an admin Book anyway for the busy dates, with a required reason", async () => {
  actor.isAdmin = true;
  vi.mocked(SeriesRooms.set)
    .mockResolvedValueOnce(booking({ defaultRoom: { id: 2, name: "Chapel", building: "" }, booked: 8, lost: ["2026-12-08", "2026-12-15"] }))
    .mockResolvedValueOnce(booking({ defaultRoom: { id: 2, name: "Chapel", building: "" }, booked: 10 }));
  show();
  await pickAndBook("2");
  expect(SeriesRooms.set).toHaveBeenLastCalledWith(7, { roomId: 2 });
  const reason = await screen.findByRole("textbox", { name: /^Reason for double-booking/ });
  expect(reason).toHaveAttribute("maxlength", "500");
  expect(screen.getByRole("alert")).toHaveTextContent("2 dates are in use. Give a reason to book them anyway.");
  await waitFor(() => expect(reason).toHaveFocus());
  await userEvent.click(screen.getByRole("button", { name: "Book anyway" }));
  expect(await screen.findByText("Say why you are double-booking this room.")).toBeInTheDocument();
  expect(reason).toHaveFocus();
  expect(SeriesRooms.set).toHaveBeenCalledTimes(1);
  await userEvent.type(reason, "Choir moved");
  await userEvent.click(screen.getByRole("button", { name: "Book anyway" }));
  await waitFor(() => expect(SeriesRooms.set).toHaveBeenLastCalledWith(7, { roomId: 2, overlapReason: "Choir moved" }));
  expect(await screen.findByRole("status")).toHaveTextContent("Booked for 10 dates, 2 of them over another booking.");
  expect(screen.queryByRole("textbox", { name: /^Reason/ })).not.toBeInTheDocument();
});

it("asks an admin for nothing when every date was free", async () => {
  actor.isAdmin = true;
  vi.mocked(SeriesRooms.set).mockResolvedValue(booking());
  show();
  await pickAndBook("1");
  expect(await screen.findByRole("status")).toHaveTextContent("Booked for 10 dates.");
  expect(screen.queryByRole("button", { name: "Book anyway" })).not.toBeInTheDocument();
});

it("names a series room this person can't list", async () => {
  vi.mocked(SeriesRooms.list).mockResolvedValue({ rooms: [hall], dates: 3, defaultRoom: { id: 9, name: "Nursery", building: "Annex" }, needsRoom: [] });
  show();
  const box = await select();
  expect(box).toHaveValue("9");
  expect(screen.getByRole("option", { name: "Nursery · Annex" })).toBeInTheDocument();
});

it("shows nothing, and asks for no rooms, while rooms are off", async () => {
  vi.mocked(Site.get).mockResolvedValue({ name: "Fold", timezone: "UTC", selfSignup: "off", roomsEnabled: false });
  const { container } = render(<SeriesRoomField eventId={7} idPrefix="t" />);
  await waitFor(() => expect(Site.get).toHaveBeenCalled());
  expect(container).toBeEmptyDOMElement();
  expect(SeriesRooms.list).not.toHaveBeenCalled();
});

it("shows nothing, not an error, when the site settings fail to load", async () => {
  vi.mocked(Site.get).mockRejectedValue(new ApiError(500, "down", { code: "server_error" }));
  const { container } = render(<SeriesRoomField eventId={7} idPrefix="t" />);
  await waitFor(() => expect(Site.get).toHaveBeenCalled());
  expect(container).toBeEmptyDOMElement();
});

it("offers a retry when the rooms fail to load", async () => {
  vi.mocked(SeriesRooms.list).mockRejectedValueOnce(new ApiError(500, "down", { code: "server_error" }));
  show();
  await userEvent.click(await screen.findByRole("button", { name: "Try again" }));
  expect(await select()).toBeInTheDocument();
  expect(SeriesRooms.list).toHaveBeenCalledTimes(2);
});

it("books nothing when the select changes, however many keys are pressed", async () => {
  show();
  const box = await select();
  await userEvent.selectOptions(box, "1");
  await userEvent.selectOptions(box, "3");
  await userEvent.selectOptions(box, "none");
  expect(SeriesRooms.set).not.toHaveBeenCalled();
});

it("names the button for what it does, and disables it when the choice is what is booked", async () => {
  vi.mocked(SeriesRooms.list).mockResolvedValue({ rooms: [hall, closet], dates: 10, defaultRoom: { id: 1, name: "Fellowship Hall", building: "Main" }, needsRoom: [] });
  show();
  const box = await select();
  expect(screen.getByRole("button", { name: "Book for every date" })).toBeDisabled();
  await userEvent.selectOptions(box, "3");
  expect(screen.getByRole("button", { name: "Book for every date" })).toBeEnabled();
  await userEvent.selectOptions(box, "none");
  expect(screen.queryByRole("button", { name: "Book for every date" })).not.toBeInTheDocument();
  expect(screen.getByRole("button", { name: "Stop the series room" })).toBeEnabled();
  await userEvent.selectOptions(box, "1");
  expect(screen.getByRole("button", { name: "Book for every date" })).toBeDisabled();
});

it("disables the button with no series room and No series room chosen", async () => {
  show();
  await select();
  expect(screen.getByRole("button", { name: "Stop the series room" })).toBeDisabled();
});

it("says before booking how many dates the chosen room is busy on, to anyone", async () => {
  for (const admin of [false, true]) {
    actor.isAdmin = admin;
    const { unmount } = render(<SeriesRoomField eventId={7} idPrefix="t" />);
    await userEvent.selectOptions(await select(), "2");
    expect(screen.getByRole("status")).toHaveTextContent(
      "Chapel is in use on 2 of 10 dates. It will be booked on the other 8, and the busy dates will be listed under “Dates that need a room”.",
    );
    expect(SeriesRooms.set).not.toHaveBeenCalled();
    await userEvent.selectOptions(screen.getByRole("combobox"), "1");
    expect(screen.getByRole("status")).toBeEmptyDOMElement();
    unmount();
  }
});

it("says when the series room holds no date", async () => {
  vi.mocked(SeriesRooms.list).mockResolvedValue({
    rooms: [chapel], dates: 2, defaultRoom: { id: 2, name: "Chapel", building: "" },
    needsRoom: [{ date: "2026-12-08", roomName: "Chapel" }, { date: "2026-12-15", roomName: "Chapel" }],
  });
  show();
  await select();
  expect(screen.getByRole("status")).toHaveTextContent("Not booked on any date yet: every date is busy.");
});

it("says so after a booking that every date refused", async () => {
  vi.mocked(SeriesRooms.list).mockResolvedValue({ rooms: [room({ id: 2, name: "Chapel", building: "", busyDates: 2 })], dates: 2, defaultRoom: null, needsRoom: [] });
  vi.mocked(SeriesRooms.set).mockResolvedValue(booking({ defaultRoom: { id: 2, name: "Chapel", building: "" }, booked: 0, lost: ["2026-12-08", "2026-12-15"] }));
  show();
  await pickAndBook("2");
  await waitFor(() => expect(screen.getByRole("status")).toHaveTextContent("Not booked on any date yet: every date is busy."));
});

it("says nothing about an empty room while some date has it", async () => {
  vi.mocked(SeriesRooms.list).mockResolvedValue({ rooms: [chapel], dates: 3, defaultRoom: { id: 2, name: "Chapel", building: "" }, needsRoom: [{ date: "2026-12-08", roomName: "Chapel" }] });
  show();
  await select();
  expect(screen.getByRole("status")).toBeEmptyDOMElement();
});
