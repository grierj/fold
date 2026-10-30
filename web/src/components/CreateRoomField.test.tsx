import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError, SeriesRooms, Site } from "../api";
import type { SeriesRoom } from "../types";
import { emptySeats } from "./SeatsField";
import { CreateRoomField, scheduleFields, scheduleReady, type Schedule } from "./CreateRoomField";

vi.mock("../api", async (importActual) => ({
  ...(await importActual<typeof import("../api")>()),
  Site: { get: vi.fn() },
  SeriesRooms: { check: vi.fn() },
}));

const room = (over: Partial<SeriesRoom>): SeriesRoom => ({ id: 1, name: "Fellowship Hall", building: "Main", capacity: 80, features: "", restricted: false, busyDates: 0, tooSmall: false, ...over });
const hall = room({});
const chapel = room({ id: 2, name: "Chapel", building: "", capacity: null, busyDates: 2 });
const closet = room({ id: 3, name: "Closet", building: "Annex", capacity: 1, tooSmall: true });
const weekly: Schedule = { startsLocal: "2026-12-01T19:00", durationMinutes: 90, tzid: "UTC", recurrenceRule: "FREQ=WEEKLY;BYDAY=TU", capacity: null };
const once: Schedule = { ...weekly, recurrenceRule: "" };

beforeEach(() => {
  vi.resetAllMocks();
  vi.mocked(Site.get).mockResolvedValue({ name: "Fold", timezone: "UTC", selfSignup: "off", roomsEnabled: true });
  vi.mocked(SeriesRooms.check).mockResolvedValue({ rooms: [hall, chapel, closet], dates: 10 });
});

// The page keeps the chosen room, as both create forms do.
function Harness({ schedule, groupId, onChange }: { schedule: Schedule | null; groupId?: number; onChange?: (id: number | null, busy: number) => void }) {
  const [roomId, setRoomId] = useState<number | null>(null);
  return <CreateRoomField id="t-room" schedule={schedule} groupId={groupId} roomId={roomId} onChange={(id, busy) => { setRoomId(id); onChange?.(id, busy); }} />;
}
const select = () => screen.findByRole("combobox", { name: /^Room/ });

describe("CreateRoomField", () => {
  it("shows nothing, and checks nothing, while rooms are off", async () => {
    vi.mocked(Site.get).mockResolvedValue({ name: "Fold", timezone: "UTC", selfSignup: "off", roomsEnabled: false });
    const { container } = render(<Harness schedule={weekly} />);
    await waitFor(() => expect(Site.get).toHaveBeenCalled());
    expect(container).toBeEmptyDOMElement();
    expect(SeriesRooms.check).not.toHaveBeenCalled();
  });

  it("waits for the dates: disabled, with a hint, and no check", async () => {
    render(<Harness schedule={null} />);
    const box = await select();
    expect(box).toBeDisabled();
    expect(screen.getByText("Pick a date and time first to see which rooms are free.")).toBeInTheDocument();
    expect(SeriesRooms.check).not.toHaveBeenCalled();
  });

  it("checks a group event's dates with its group, and words busy as N of M dates", async () => {
    render(<Harness schedule={weekly} groupId={5} />);
    const box = await select();
    await waitFor(() => expect(Array.from((box as HTMLSelectElement).options)).toHaveLength(4));
    expect(SeriesRooms.check).toHaveBeenCalledTimes(1);
    expect(SeriesRooms.check).toHaveBeenCalledWith({ ...weekly, groupId: 5 });
    expect(Array.from((box as HTMLSelectElement).options).map((o) => o.textContent)).toEqual([
      "No room",
      "Fellowship Hall · Main (80 seats)",
      "Chapel — in use on 2 of 10 dates",
      "Closet · Annex (1 seat) — may be too small",
    ]);
    expect(box).toBeEnabled();
    expect(screen.getByText("Booked for every date when you schedule the event.")).toBeInTheDocument();
  });

  it("leaves groupId out for a church-wide event and says in use at this time for one date", async () => {
    vi.mocked(SeriesRooms.check).mockResolvedValue({ rooms: [room({ busyDates: 1 })], dates: 1 });
    render(<Harness schedule={once} />);
    const box = await select();
    await screen.findByRole("option", { name: "Fellowship Hall · Main (80 seats) — in use at this time" });
    expect(SeriesRooms.check).toHaveBeenCalledWith(once);
    expect(box).toBeEnabled();
    expect(screen.getByText("Booked when you schedule the event.")).toBeInTheDocument();
  });

  it("asks once after the time, repeat or seat limit stop changing", async () => {
    const { rerender } = render(<Harness schedule={weekly} groupId={5} />);
    await select();
    rerender(<Harness schedule={{ ...weekly, startsLocal: "2026-12-01T18:00" }} groupId={5} />);
    rerender(<Harness schedule={{ ...weekly, startsLocal: "2026-12-01T18:30" }} groupId={5} />);
    rerender(<Harness schedule={{ ...weekly, startsLocal: "2026-12-01T18:30", capacity: 12 }} groupId={5} />);
    await waitFor(() => expect(SeriesRooms.check).toHaveBeenLastCalledWith({ ...weekly, startsLocal: "2026-12-01T18:30", capacity: 12, groupId: 5 }));
    expect(SeriesRooms.check).toHaveBeenCalledTimes(1);
    rerender(<Harness schedule={{ ...weekly, startsLocal: "2026-12-01T18:30", capacity: 12, recurrenceRule: "" }} groupId={5} />);
    await waitFor(() => expect(SeriesRooms.check).toHaveBeenCalledTimes(2));
  });

  it("reports the chosen room and tells the leader what busy dates mean", async () => {
    const onChange = vi.fn();
    render(<Harness schedule={weekly} groupId={5} onChange={onChange} />);
    const box = await select();
    await screen.findByRole("option", { name: /^Chapel/ });
    await userEvent.selectOptions(box, "2");
    expect(onChange).toHaveBeenLastCalledWith(2, 2);
    expect(box).toHaveValue("2");
    expect(await screen.findByRole("status")).toHaveTextContent(
      "Chapel is in use on 2 of 10 dates. It will be booked on the other 8, and the event page will list the busy dates under “Dates that need a room”.",
    );
    await userEvent.selectOptions(box, "3");
    expect(onChange).toHaveBeenLastCalledWith(3, 0);
    expect(screen.getByRole("status")).toHaveTextContent("This room may seat fewer people than the seat limit allows.");
    await userEvent.selectOptions(box, "none");
    expect(onChange).toHaveBeenLastCalledWith(null, 0);
    expect(screen.getByRole("status")).toBeEmptyDOMElement();
  });

  it("says so when the room is busy on every date, or at the one time", async () => {
    vi.mocked(SeriesRooms.check).mockResolvedValue({ rooms: [room({ busyDates: 10 })], dates: 10 });
    const { unmount } = render(<Harness schedule={weekly} />);
    await screen.findByRole("option", { name: /in use on 10 of 10 dates/ });
    await userEvent.selectOptions(await select(), "1");
    expect(await screen.findByRole("status")).toHaveTextContent("Fellowship Hall · Main is in use on all 10 dates, so none can be booked yet.");
    unmount();
    vi.mocked(SeriesRooms.check).mockResolvedValue({ rooms: [room({ busyDates: 1 })], dates: 1 });
    render(<Harness schedule={once} />);
    await screen.findByRole("option", { name: /in use at this time/ });
    await userEvent.selectOptions(await select(), "1");
    expect(await screen.findByRole("status")).toHaveTextContent("is in use at this time, so the event will start without a room.");
  });

  it("refreshes the busy count of a chosen room after a new check, and drops a room that is gone", async () => {
    const onChange = vi.fn();
    const { rerender } = render(<Harness schedule={weekly} onChange={onChange} />);
    await screen.findByRole("option", { name: /^Chapel/ });
    await userEvent.selectOptions(await select(), "2");
    vi.mocked(SeriesRooms.check).mockResolvedValue({ rooms: [room({ id: 2, name: "Chapel", building: "", busyDates: 5 })], dates: 10 });
    rerender(<Harness schedule={{ ...weekly, durationMinutes: 60 }} onChange={onChange} />);
    await waitFor(() => expect(onChange).toHaveBeenLastCalledWith(2, 5));
    vi.mocked(SeriesRooms.check).mockResolvedValue({ rooms: [hall], dates: 10 });
    rerender(<Harness schedule={{ ...weekly, durationMinutes: 45 }} onChange={onChange} />);
    await waitFor(() => expect(onChange).toHaveBeenLastCalledWith(null, 0));
  });

  it("offers a retry when the check fails, without blocking the form", async () => {
    vi.mocked(SeriesRooms.check).mockRejectedValueOnce(new ApiError(500, "down", { code: "server_error" }));
    render(<Harness schedule={weekly} />);
    await userEvent.click(await screen.findByRole("button", { name: "Try again" }));
    await screen.findByRole("option", { name: /^Chapel/ });
    expect(SeriesRooms.check).toHaveBeenCalledTimes(2);
  });
});

describe("scheduleFields and scheduleReady", () => {
  const when = { date: "2026-12-01", time: "19:00", duration: 90, recurring: true, days: ["TU", "TH"] };
  it("builds the create body's date fields from the form values", () => {
    expect(scheduleFields(when, " UTC ", { ...emptySeats, capacity: "12" })).toEqual({
      startsLocal: "2026-12-01T19:00", durationMinutes: 90, tzid: "UTC", recurrenceRule: "FREQ=WEEKLY;BYDAY=TU,TH", capacity: 12,
    });
    expect(scheduleFields({ ...when, recurring: false }, "UTC", emptySeats)).toMatchObject({ recurrenceRule: "", capacity: null });
  });
  it("is ready only with a date, a zone, a day when repeating, and a valid seat limit", () => {
    expect(scheduleReady(when, "UTC", emptySeats)).toBe(true);
    expect(scheduleReady({ ...when, date: "" }, "UTC", emptySeats)).toBe(false);
    expect(scheduleReady(when, " ", emptySeats)).toBe(false);
    expect(scheduleReady({ ...when, days: [] }, "UTC", emptySeats)).toBe(false);
    expect(scheduleReady({ ...when, days: [], recurring: false }, "UTC", emptySeats)).toBe(true);
    expect(scheduleReady(when, "UTC", { ...emptySeats, capacity: "1e" })).toBe(false);
  });
});
