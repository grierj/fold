import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, expect, it, vi } from "vitest";
import { ApiError, SeriesRooms, Site } from "../api";
import { NeedsRoomList } from "./NeedsRoomList";

vi.mock("../api", async (importActual) => ({
  ...(await importActual<typeof import("../api")>()),
  Site: { get: vi.fn() },
  SeriesRooms: { list: vi.fn() },
}));

const needs = [{ date: "2026-12-08", roomName: "Fellowship Hall" }, { date: "2026-12-15", roomName: "Chapel" }];

beforeEach(() => {
  vi.resetAllMocks();
  vi.mocked(Site.get).mockResolvedValue({ name: "Fold", timezone: "UTC", selfSignup: "off", roomsEnabled: true });
  vi.mocked(SeriesRooms.list).mockResolvedValue({ rooms: [], dates: 10, defaultRoom: null, needsRoom: needs });
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

it("links to the date's page when the event page doesn't list that date", async () => {
  show({ hasDate: (d) => d === "2026-12-08" });
  const link = await screen.findByRole("link", { name: "Pick a room for Tuesday, December 15" });
  expect(link).toHaveAttribute("href", "/events/7/dates/2026-12-15");
  expect(screen.getByRole("button", { name: "Pick a room for Tuesday, December 8" })).toBeInTheDocument();
});

it("shows nothing when every date has its room", async () => {
  vi.mocked(SeriesRooms.list).mockResolvedValue({ rooms: [], dates: 10, defaultRoom: null, needsRoom: [] });
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
  vi.mocked(SeriesRooms.list).mockResolvedValue({ rooms: [], dates: 10, defaultRoom: null, needsRoom: [needs[0]] });
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
