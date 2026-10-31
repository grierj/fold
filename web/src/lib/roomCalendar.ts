import type { RoomCalendarRoom, RoomSlot } from "../types";
import { addDays } from "./calendar";
import { dayOf, formatTimeRange, minuteOfDay } from "./when";

// The by-room view (M2.2a, 8.1, DEC-108). Pure helpers so the page stays
// layout and the rules here have unit tests.

export type RoomView = "day" | "week";

// Where a visible booking leads: its exact-date page when the server scopes
// it to one date, else the event page (as the calendar's occurrenceLink).
export function slotLink(slot: RoomSlot): string | null {
  if (slot.reserved || !slot.eventId) return null;
  return slot.dateUrl || `/events/${slot.eventId}`;
}

// The secondary line under a booking's title: its group, or who hosts an
// opened event of a group the viewer can't see.
export function slotByline(slot: RoomSlot): string {
  if (slot.groupName) return slot.groupName;
  if (slot.churchWide) return "Church-wide";
  if (slot.hostedBy) return `Hosted by ${slot.hostedBy}`;
  return "";
}

// What a booking says, in reading order: "7:00–8:30 PM, Bible study, Men's
// group, Waiting for approval" or "7:00–8:30 PM, Reserved". It is the
// block's accessible name and its tooltip; the visible words come first.
export function slotLabel(slot: RoomSlot, zone: string): string {
  const time = formatTimeRange(slot.startsAt, slot.endsAt, zone);
  if (slot.reserved) return `${time}, Reserved`;
  return [time, slot.title, slotByline(slot), slot.waiting && "Waiting for approval"].filter(Boolean).join(", ");
}

// The days a view covers, first to last.
export function viewDays(from: string, to: string): string[] {
  const out: string[] = [];
  for (let d = from; d <= to && out.length < 7; d = addDays(d, 1)) out.push(d);
  return out;
}

// Bookings bucketed by the church day they start on, kept in time order.
export function slotsByDay(slots: RoomSlot[], zone: string): Map<string, RoomSlot[]> {
  const out = new Map<string, RoomSlot[]>();
  for (const s of [...slots].sort(byStart)) {
    const d = dayOf(s.startsAt, zone);
    out.set(d, [...(out.get(d) ?? []), s]);
  }
  return out;
}

function byStart(a: RoomSlot, b: RoomSlot): number {
  return a.startsAt < b.startsAt ? -1 : a.startsAt > b.startsAt ? 1 : a.endsAt < b.endsAt ? -1 : 1;
}

// A day's bookings in one room, placed on the time grid: minutes from
// midnight, clamped to the day, and side-by-side lanes for overlaps (an
// admin's double-booking, or a request waiting beside a booking).
export type PlacedSlot = { slot: RoomSlot; start: number; end: number; lane: number; lanes: number };

export function placeSlots(slots: RoomSlot[], day: string, zone: string): PlacedSlot[] {
  const placed = slots
    .map((slot) => ({ slot, start: minuteOfDay(slot.startsAt, day, zone), end: minuteOfDay(slot.endsAt, day, zone), lane: 0, lanes: 1 }))
    .filter((p) => p.end > p.start)
    .sort((a, b) => a.start - b.start || a.end - b.end);
  let cluster: PlacedSlot[] = [];
  let clusterEnd = -1;
  let laneEnds: number[] = [];
  const close = () => {
    for (const p of cluster) p.lanes = laneEnds.length;
    cluster = [];
    laneEnds = [];
  };
  for (const p of placed) {
    if (p.start >= clusterEnd) close();
    let lane = laneEnds.findIndex((end) => end <= p.start);
    if (lane < 0) {
      lane = laneEnds.length;
      laneEnds.push(p.end);
    } else {
      laneEnds[lane] = p.end;
    }
    p.lane = lane;
    cluster.push(p);
    clusterEnd = Math.max(clusterEnd, p.end);
  }
  close();
  return placed;
}

// The hours a day grid shows: 7 AM to 10 PM, widened to fit any booking
// outside them.
const DAY_START_HOUR = 7;
const DAY_END_HOUR = 22;

export function hourRange(rooms: RoomCalendarRoom[], day: string, zone: string): { start: number; end: number } {
  let start = DAY_START_HOUR;
  let end = DAY_END_HOUR;
  for (const r of rooms) {
    for (const p of placeSlots(r.bookings, day, zone)) {
      start = Math.min(start, Math.floor(p.start / 60));
      end = Math.max(end, Math.ceil(p.end / 60));
    }
  }
  return { start, end };
}

// A room's name with its building, as the room pickers write it.
export function roomName(r: { name: string; building: string }): string {
  return r.building ? `${r.name} · ${r.building}` : r.name;
}
