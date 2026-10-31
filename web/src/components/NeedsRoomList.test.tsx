import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, expect, it, vi } from "vitest";
import { ApiError, DateRooms, SeriesRooms, Site } from "../api";
import type { NeedsRoom, RoomPickerItem } from "../types";
import { NeedsRoomList } from "./NeedsRoomList";

vi.mock("../api", async (importActual) => ({
  ...(await importActual<typeof import("../api")>()),
  Site: { get: vi.fn() },
  SeriesRooms: { list: vi.fn() },
  DateRooms: { list: vi.fn(), set: vi.fn() },
}));
vi.mock("../auth", () => ({ useAuth: () => ({ isAdmin: false }) }));

const hall: RoomPickerItem = { id: 1, name: "Fellowship Hall", building: "Main", capacity: 80, features: "", restricted: false, busy: false, tooSmall: false };
const needs: NeedsRoom[] = [{ date: "2026-12-08", roomName: "Fellowship Hall", status: "requested" }, { date: "2026-12-15", roomName: "Chapel", status: "requested" }];

beforeEach(() => {
  vi.resetAllMocks();
  vi.mocked(Site.get).mockResolvedValue({ name: "Fold", timezone: "UTC", selfSignup: "off", roomsEnabled: true });
  vi.mocked(SeriesRooms.list).mockResolvedValue({ rooms: [], dates: 10, defaultRoom: null, needsRoom: needs, pending: 0 });
});

function show(props: Partial<Parameters<typeof NeedsRoomList>[0]> = {}) {
  const onPick = vi.fn();
  const view = render(
    <MemoryRouter>
      <NeedsRoomList eventId={7} revision={0} hasDate={() => true} onPick={onPick} {...props} />
    </MemoryRouter>,
  );
  return { onPick, ...view };
}

it("lists each date with the room it lost and a Pick a room action", async () => {
  const { onPick } = show();
  expect(await screen.findByRole("heading", { name: /Dates that need a room/ })).toBeInTheDocument();
  expect(SeriesRooms.list).toHaveBeenCalledWith(7);
  expect(screen.getByText("Tuesday, December 8")).toBeInTheDocument();
  expect(screen.getByText("Fellowship Hall isn't free then.")).toBeInTheDocument();
  expect(screen.getByText("Chapel isn't free then.")).toBeInTheDocument();
  await userEvent.click(screen.getByRole("button", { name: "Pick a room for Tuesday, December 15" }));
  expect(onPick).toHaveBeenCalledWith("2026-12-15");
});

it("opens the one-date Room control under a date the page doesn't list, and not for one it does", async () => {
  vi.mocked(DateRooms.list).mockResolvedValue({ rooms: [hall], booking: null });
  const { onPick } = show({ hasDate: (d) => d === "2026-12-08" });
  const far = await screen.findByRole("button", { name: "Pick a room for Tuesday, December 15" });
  expect(screen.queryByRole("link")).not.toBeInTheDocument();
  expect(far).toHaveAttribute("aria-expanded", "false");
  await userEvent.click(far);
  expect(far).toHaveAttribute("aria-expanded", "true");
  const room = await screen.findByRole("combobox", { name: /^Room/ });
  expect(DateRooms.list).toHaveBeenCalledWith(7, "2026-12-15");
  await waitFor(() => expect(room).toHaveFocus());
  expect(onPick).not.toHaveBeenCalled();
  // A date the page lists opens its own editor instead, with no control here.
  const near = screen.getByRole("button", { name: "Pick a room for Tuesday, December 8" });
  expect(near).not.toHaveAttribute("aria-expanded");
  await userEvent.click(near);
  expect(onPick).toHaveBeenCalledWith("2026-12-08");
  // Closing it again.
  await userEvent.click(far);
  expect(screen.queryByRole("combobox", { name: /^Room/ })).not.toBeInTheDocument();
});

it("asks again after the inline control books, and says the date has a room now", async () => {
  vi.mocked(DateRooms.list).mockResolvedValue({ rooms: [hall], booking: null });
  vi.mocked(DateRooms.set).mockResolvedValue({ booking: { roomId: 1, roomName: "Fellowship Hall", building: "Main", status: "confirmed", setupMinutes: 0, teardownMinutes: 0, doubleBooked: false, tooSmall: false, pending: false } });
  show({ hasDate: () => false });
  await userEvent.click(await screen.findByRole("button", { name: "Pick a room for Tuesday, December 15" }));
  vi.mocked(SeriesRooms.list).mockResolvedValue({ rooms: [], dates: 10, defaultRoom: null, needsRoom: [needs[0]], pending: 0 });
  await userEvent.selectOptions(await screen.findByRole("combobox", { name: /^Room/ }), "1");
  await waitFor(() => expect(screen.queryByText("Chapel isn't free then.")).not.toBeInTheDocument());
  expect(SeriesRooms.list).toHaveBeenCalledTimes(2);
  expect(await screen.findByText("Tuesday, December 15 has a room now.")).toBeInTheDocument();
});

it("shows the first 10 dates with the count in the heading, and the rest on request", async () => {
  const many: NeedsRoom[] = Array.from({ length: 14 }, (_, i) => ({ date: `2027-01-${`${i + 1}`.padStart(2, "0")}`, roomName: "Chapel", status: "requested" }));
  vi.mocked(SeriesRooms.list).mockResolvedValue({ rooms: [], dates: 30, defaultRoom: null, needsRoom: many, pending: 0 });
  show();
  expect(await screen.findByRole("heading", { name: /Dates that need a room \(14\)/ })).toBeInTheDocument();
  expect(screen.getAllByRole("button", { name: /^Pick a room/ })).toHaveLength(10);
  await userEvent.click(screen.getByRole("button", { name: "Show all 14" }));
  expect(screen.getAllByRole("button", { name: /^Pick a room/ })).toHaveLength(14);
  expect(screen.queryByRole("button", { name: /^Show all/ })).not.toBeInTheDocument();
});

it("reads the site settings once, however often the page asks again", async () => {
  const { rerender } = show();
  await screen.findByText("Chapel isn't free then.");
  rerender(<MemoryRouter><NeedsRoomList eventId={7} revision={1} hasDate={() => true} onPick={() => {}} /></MemoryRouter>);
  await waitFor(() => expect(SeriesRooms.list).toHaveBeenCalledTimes(2));
  expect(Site.get).toHaveBeenCalledTimes(1);
});

it("shows nothing when every date has its room", async () => {
  vi.mocked(SeriesRooms.list).mockResolvedValue({ rooms: [], dates: 10, defaultRoom: null, needsRoom: [], pending: 0 });
  const { container } = show();
  await waitFor(() => expect(SeriesRooms.list).toHaveBeenCalled());
  expect(container).toBeEmptyDOMElement();
});

it("shows nothing, and asks for no rooms, while rooms are off", async () => {
  vi.mocked(Site.get).mockResolvedValue({ name: "Fold", timezone: "UTC", selfSignup: "off", roomsEnabled: false });
  const { container } = show();
  await waitFor(() => expect(Site.get).toHaveBeenCalled());
  expect(container).toBeEmptyDOMElement();
  expect(SeriesRooms.list).not.toHaveBeenCalled();
});

it("asks again when a room changed, and drops a date that now has one", async () => {
  const { rerender } = show();
  await screen.findByText("Chapel isn't free then.");
  vi.mocked(SeriesRooms.list).mockResolvedValue({ rooms: [], dates: 10, defaultRoom: null, needsRoom: [needs[0]], pending: 0 });
  rerender(<MemoryRouter><NeedsRoomList eventId={7} revision={1} hasDate={() => true} onPick={() => {}} /></MemoryRouter>);
  await waitFor(() => expect(screen.queryByText("Chapel isn't free then.")).not.toBeInTheDocument());
  expect(screen.getByText("Fellowship Hall isn't free then.")).toBeInTheDocument();
});

it("offers a retry when the list fails to load", async () => {
  vi.mocked(SeriesRooms.list).mockRejectedValueOnce(new ApiError(500, "down", { code: "server_error" }));
  show();
  await userEvent.click(await screen.findByRole("button", { name: "Try again" }));
  expect(await screen.findByText("Chapel isn't free then.")).toBeInTheDocument();
});

it("words a date an admin didn't approve differently from one that lost its room", async () => {
  vi.mocked(SeriesRooms.list).mockResolvedValue({
    rooms: [], dates: 10, defaultRoom: null, pending: 0,
    needsRoom: [{ date: "2026-12-08", roomName: "Fellowship Hall", status: "rejected" }, { date: "2026-12-15", roomName: "Chapel", status: "requested" }],
  });
  show();
  expect(await screen.findByRole("heading", { name: /^⚠ Dates that need a room|^Dates that need a room/ })).toBeInTheDocument();
  expect(screen.getByText("An admin didn't approve Fellowship Hall for this date.")).toBeInTheDocument();
  expect(screen.getByText("Chapel isn't free then.")).toBeInTheDocument();
});
