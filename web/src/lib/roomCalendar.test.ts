import { describe, expect, it } from "vitest";
import type { RoomCalendarRoom, RoomSlot } from "../types";
import { hourRange, placeSlots, roomName, slotByline, slotLabel, slotLink, slotsByDay, viewDays } from "./roomCalendar";

const zone = "America/Denver";
// Denver is UTC-6 in October: 19:00 local is 01:00Z the next day.
const slot = (start: string, end: string, o: Partial<RoomSlot> = {}): RoomSlot => ({ startsAt: start, endsAt: end, eventId: 1, title: "Bible study", ...o });
const room = (bookings: RoomSlot[]): RoomCalendarRoom => ({ id: 1, name: "Hall", building: "", capacity: null, restricted: false, bookings });

describe("slotLink", () => {
  it("opens the exact date when the server scopes it, else the event", () => {
    expect(slotLink(slot("a", "b", { dateUrl: "/events/1/dates/2026-10-21" }))).toBe("/events/1/dates/2026-10-21");
    expect(slotLink(slot("a", "b"))).toBe("/events/1");
  });
  it("never links a reserved booking", () => {
    expect(slotLink({ startsAt: "a", endsAt: "b", reserved: true })).toBeNull();
  });
});

describe("slotByline and slotLabel", () => {
  it("names the group, church-wide, or the host", () => {
    expect(slotByline(slot("a", "b", { groupName: "Men's group" }))).toBe("Men's group");
    expect(slotByline(slot("a", "b", { churchWide: true }))).toBe("Church-wide");
    expect(slotByline(slot("a", "b", { hostedBy: "Grace Church" }))).toBe("Hosted by Grace Church");
  });
  it("says Reserved and nothing else for a hidden date", () => {
    const label = slotLabel({ startsAt: "2026-10-22T01:00:00Z", endsAt: "2026-10-22T02:30:00Z", reserved: true }, zone);
    expect(label).toMatch(/^7:00–8:30\sPM, Reserved$/);
  });
  it("says a booking waits for approval", () => {
    expect(slotLabel(slot("2026-10-22T01:00:00Z", "2026-10-22T02:00:00Z", { waiting: true, groupName: "Youth" }), zone)).toMatch(
      /, Bible study, Youth, Waiting for approval$/
    );
  });
});

describe("viewDays and slotsByDay", () => {
  it("lists the days from first to last", () => {
    expect(viewDays("2026-10-18", "2026-10-24")).toHaveLength(7);
    expect(viewDays("2026-10-21", "2026-10-21")).toEqual(["2026-10-21"]);
  });
  it("buckets by the church day, in time order", () => {
    const late = slot("2026-10-22T03:00:00Z", "2026-10-22T04:00:00Z", { title: "late" });
    const early = slot("2026-10-21T15:00:00Z", "2026-10-21T16:00:00Z", { title: "early" });
    const m = slotsByDay([late, early], zone);
    expect(m.get("2026-10-21")!.map((s) => s.title)).toEqual(["early", "late"]);
  });
});

describe("placeSlots", () => {
  it("puts overlapping bookings side by side and lone ones full width", () => {
    const a = slot("2026-10-22T01:00:00Z", "2026-10-22T02:00:00Z", { title: "a" });
    const b = slot("2026-10-22T01:30:00Z", "2026-10-22T02:30:00Z", { title: "b" });
    const c = slot("2026-10-22T03:00:00Z", "2026-10-22T04:00:00Z", { title: "c" });
    const placed = placeSlots([c, b, a], "2026-10-21", zone);
    const by = Object.fromEntries(placed.map((p) => [p.slot.title, p]));
    expect([by.a.lane, by.a.lanes]).toEqual([0, 2]);
    expect([by.b.lane, by.b.lanes]).toEqual([1, 2]);
    expect([by.c.lane, by.c.lanes]).toEqual([0, 1]);
    expect(by.a.start).toBe(19 * 60);
  });
  it("lets touching bookings share a lane", () => {
    const a = slot("2026-10-22T01:00:00Z", "2026-10-22T02:00:00Z");
    const b = slot("2026-10-22T02:00:00Z", "2026-10-22T03:00:00Z");
    expect(placeSlots([a, b], "2026-10-21", zone).map((p) => p.lanes)).toEqual([1, 1]);
  });
});

describe("hourRange", () => {
  it("is 7 AM to 10 PM unless a booking is outside it", () => {
    expect(hourRange([room([])], "2026-10-21", zone)).toEqual({ start: 7, end: 22 });
    // 6:15 AM to 11:30 PM local.
    const early = slot("2026-10-21T12:15:00Z", "2026-10-21T13:00:00Z");
    const late = slot("2026-10-22T04:00:00Z", "2026-10-22T05:30:00Z");
    expect(hourRange([room([early, late])], "2026-10-21", zone)).toEqual({ start: 6, end: 24 });
  });
});

describe("roomName", () => {
  it("adds the building when there is one", () => {
    expect(roomName({ name: "Hall", building: "Main" })).toBe("Hall · Main");
    expect(roomName({ name: "Hall", building: "" })).toBe("Hall");
  });
});
