import { describe, expect, it } from "vitest";
import type { RoomPickerItem, SeriesBooking, SeriesRoom } from "../types";
import { busyDatesNote, roomOptionLabel, seriesBookingMessage, seriesRoomLabel } from "./roomLabels";

const hall = { id: 1, name: "Fellowship Hall", building: "Main", capacity: 80, tooSmall: false } as const;

describe("room option labels", () => {
  it("writes the name, seats, a busy note, a size note and the suggestion, in that order", () => {
    const room = { ...hall, busy: true, tooSmall: true } as RoomPickerItem;
    expect(roomOptionLabel(room, "used by this group")).toBe("Fellowship Hall · Main (80 seats) — in use at this time — may be too small — used by this group");
    expect(roomOptionLabel({ ...hall, capacity: 1, busy: false } as RoomPickerItem)).toBe("Fellowship Hall · Main (1 seat)");
    expect(roomOptionLabel({ ...hall, capacity: null, busy: false } as RoomPickerItem)).toBe("Fellowship Hall · Main");
  });

  it("counts the busy dates of a series", () => {
    const room = { ...hall, busyDates: 3 } as SeriesRoom;
    expect(seriesRoomLabel(room, 8)).toBe("Fellowship Hall · Main (80 seats) — in use on 3 of 8 dates");
    expect(seriesRoomLabel(room, 1)).toBe("Fellowship Hall · Main (80 seats) — in use at this time");
    expect(seriesRoomLabel({ ...room, busyDates: 0 } as SeriesRoom, 8)).toBe("Fellowship Hall · Main (80 seats)");
  });
});

describe("busyDatesNote", () => {
  const room = (busyDates: number) => ({ ...hall, busyDates }) as SeriesRoom;
  it("says nothing for a free room", () => {
    expect(busyDatesNote(room(0), 8, "edit")).toBeNull();
  });
  it("words the edit form", () => {
    expect(busyDatesNote(room(1), 1, "edit")).toContain("so the date will need a room");
    expect(busyDatesNote(room(8), 8, "edit")).toContain("in use on all 8 dates, so none can be booked yet;");
    expect(busyDatesNote(room(3), 8, "edit")).toContain("booked on the other 5, and the busy dates will be listed");
  });
  it("words the create form", () => {
    expect(busyDatesNote(room(1), 1, "create")).toContain("the event will start without a room");
    expect(busyDatesNote(room(8), 8, "create")).toContain("The event page will list them");
    expect(busyDatesNote(room(3), 8, "create")).toContain("the event page will list the busy dates");
  });
});

describe("seriesBookingMessage", () => {
  const res = (over: Partial<SeriesBooking>) => ({ defaultRoom: { id: 1 }, booked: 0, pending: 0, lost: [], kept: 0, ...over }) as unknown as SeriesBooking;
  it("tells what happened", () => {
    expect(seriesBookingMessage(res({ booked: 1 }))).toBe("Booked for 1 date.");
    expect(seriesBookingMessage(res({ booked: 8 }), 2)).toBe("Booked for 8 dates, 2 of them over another booking.");
    expect(seriesBookingMessage(res({ defaultRoom: null }))).toBe("Series room released.");
    expect(seriesBookingMessage(res({}))).toBe("There are no upcoming dates to book.");
    expect(seriesBookingMessage(res({ booked: 3, pending: 2, kept: 1 }))).toBe("Booked for 3 dates. Sent to an admin for approval: 2 dates. 1 date keeps the room picked for it.");
  });
});
