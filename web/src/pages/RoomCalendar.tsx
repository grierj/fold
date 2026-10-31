import { useEffect, useState, type CSSProperties, type ReactNode } from "react";
import { Link, useSearchParams } from "react-router-dom";
import { ApiError, Calendar } from "../api";
import { useAuth } from "../auth";
import type { RoomCalendar, RoomCalendarRoom, RoomSlot } from "../types";
import { addDays } from "../lib/calendar";
import { errorText } from "../lib/errors";
import {
  hourRange,
  placeSlots,
  roomName,
  slotByline,
  slotLabel,
  slotLink,
  slotsByDay,
  viewDays,
  type RoomView,
} from "../lib/roomCalendar";
import { dayOf, formatDayHeading, formatDayLong, formatHour, formatPeriod, formatTime, formatTimeRange, formatWeekday, zoneNote } from "../lib/when";
import { Button, ButtonLink } from "../components/ui/Button";
import { EmptyState } from "../components/ui/EmptyState";
import { Field, Select } from "../components/ui/Field";
import { Skeleton } from "../components/ui/Skeleton";
import { ViewSwitch } from "../components/ui/ViewSwitch";
import { ErrorNotice } from "../components/ErrorNotice";
import { ChevronLeftIcon, ChevronRightIcon } from "../components/icons";
import { CalendarViewSwitch } from "../components/calendar/CalendarViews";

// The by-room view (M2.2a, 8.1): what each room holds over one day or one
// week, in the church's zone (the rooms are there). From sm up, Day is one
// column per room on a time grid and Week is rooms × days (DEC-108); on
// phones it is one room at a time as a day list (DEC-67's pattern). A date
// the viewer can't see is a grey "Reserved" block with no title. Admins
// also see bookings waiting for approval, dashed and labelled (DEC-109).
// The URL holds the state: ?view=week&date=&room=.

// One hour of the day grid, in pixels: a 30-minute booking is 32px tall.
const HOUR_PX = 64;
// A Day block shorter than this has room for one line only: time and title
// take 40px, and a waiting label needs a third line.
const shortBlock = (px: number, waiting?: boolean) => px < (waiting ? 56 : 40);

export function RoomCalendarPage() {
  const { user, isAdmin, loading: authLoading } = useAuth();
  const [params, setParams] = useSearchParams();
  const view: RoomView = params.get("view") === "week" ? "week" : "day";
  const dateParam = /^\d{4}-\d{2}-\d{2}$/.test(params.get("date") ?? "") ? params.get("date")! : "";
  const [data, setData] = useState<RoomCalendar | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [unavailable, setUnavailable] = useState(false);
  const [retry, setRetry] = useState(0);

  useEffect(() => {
    if (!user) return;
    let alive = true;
    setLoading(true);
    setError(null);
    Calendar.rooms({ view, date: dateParam || undefined })
      .then((r) => {
        if (!alive) return;
        setData(r);
        setUnavailable(false);
      })
      .catch((e) => {
        if (!alive) return;
        if (e instanceof ApiError && e.status === 404) setUnavailable(true);
        else setError(errorText(e, "We couldn't load the room calendar."));
      })
      .finally(() => alive && setLoading(false));
    return () => {
      alive = false;
    };
  }, [user, view, dateParam, retry]);

  function link(next: Record<string, string | null>): string {
    const p = new URLSearchParams(params);
    for (const [k, v] of Object.entries(next)) {
      if (v === null) p.delete(k);
      else p.set(k, v);
    }
    const q = p.toString();
    return `/calendar/rooms${q ? `?${q}` : ""}`;
  }
  function go(next: Record<string, string | null>) {
    setParams(new URLSearchParams(link(next).split("?")[1] ?? ""));
  }
  const calendarLink = (v: string) => {
    if (v === "rooms") return link({});
    const p = new URLSearchParams();
    if (v !== "agenda") p.set("view", v);
    if (v !== "agenda" && dateParam) p.set("date", dateParam);
    const q = p.toString();
    return `/calendar${q ? `?${q}` : ""}`;
  };

  const header = (
    <>
      <h1 className="text-3xl font-bold text-text">Calendar</h1>
      <div className="mt-6">
        <CalendarViewSwitch current="rooms" rooms to={calendarLink} />
      </div>
    </>
  );

  // While the session loads, the page waits rather than asking a signed-in
  // person to log in.
  if (authLoading) {
    return (
      <div className="max-w-6xl mx-auto px-4 py-8">
        {header}
        <div className="mt-6">
          <Skeleton kind="lines" count={6} label="Loading the room calendar…" />
        </div>
      </div>
    );
  }
  if (!user) {
    return (
      <div className="max-w-6xl mx-auto px-4 py-8">
        {header}
        <div className="mt-6">
          <EmptyState
            title="Log in to see the rooms"
            body="The room calendar shows what's booked in each of the church's rooms."
            action={<ButtonLink to="/login" variant="primary">Log in</ButtonLink>}
          />
        </div>
      </div>
    );
  }
  if (unavailable) {
    return (
      <div className="max-w-6xl mx-auto px-4 py-8">
        {header}
        <div className="mt-6">
          <EmptyState
            title="The room calendar isn't available"
            body="The church doesn't share its room calendar here. You can still see every event on the calendar."
            action={<ButtonLink to="/calendar" variant="secondary">Open the calendar</ButtonLink>}
          />
        </div>
      </div>
    );
  }

  const zone = data?.timezone ?? "";
  const today = zone ? dayOf(new Date().toISOString(), zone) : "";
  const base = dateParam || data?.from || "";
  const days = data ? viewDays(data.from, data.to) : [];
  const title = data ? (view === "week" ? formatPeriod("week", data.from, data.to) : formatDayLong(data.from)) : "";
  const rooms = data?.rooms ?? [];
  const roomParam = params.get("room") ?? "";
  const phoneRoom = rooms.find((r) => String(r.id) === roomParam) ?? rooms[0];
  const unit = view === "week" ? "week" : "day";

  return (
    <div className="max-w-6xl mx-auto px-4 py-8">
      {header}
      <div className="mt-3 flex flex-col gap-3 sm:flex-row sm:flex-wrap sm:items-center">
        <ViewSwitch
          label="Room calendar view"
          items={(["day", "week"] as RoomView[]).map((v) => ({
            label: v === "day" ? "Day" : "Week",
            to: link({ view: v === "day" ? null : v }),
            current: v === view,
          }))}
        />
        <div className="flex items-center gap-2">
          <Button variant="secondary" aria-label={`Previous ${unit}`} disabled={!base} onClick={() => go({ date: addDays(base, view === "week" ? -7 : -1) })}>
            <ChevronLeftIcon />
          </Button>
          <h2 aria-live="polite" className="min-w-0 flex-1 text-center font-semibold text-text sm:min-w-56">
            {title}
          </h2>
          <Button variant="secondary" aria-label={`Next ${unit}`} disabled={!base} onClick={() => go({ date: addDays(base, view === "week" ? 7 : 1) })}>
            <ChevronRightIcon />
          </Button>
          <Button variant="ghost" onClick={() => go({ date: null })}>
            Today
          </Button>
        </div>
      </div>
      {/* The line keeps its height before the zone is known, so nothing shifts. */}
      <p className="mt-3 min-h-5 text-sm text-text-muted">{zone && zoneNote(zone)}</p>
      {data?.truncated && !error && (
        <p className="mt-2 text-sm text-text-muted">
          There are too many bookings to show at once, so some are left out. {view === "week" ? "Try the Day view." : "Try another day."}
        </p>
      )}

      <div className="mt-6">
        {error ? (
          <ErrorNotice message={error} onRetry={() => setRetry((n) => n + 1)} />
        ) : !data ? (
          loading && <Skeleton kind="lines" count={6} label="Loading the room calendar…" />
        ) : rooms.length === 0 ? (
          <EmptyState
            title="No rooms yet"
            body={
              isAdmin
                ? "Add the church's rooms, and what's booked in each one shows up here."
                : "When the church adds its rooms, you'll see what's booked in each one here."
            }
            action={isAdmin ? <ButtonLink to="/admin?tab=rooms" variant="secondary">Add rooms</ButtonLink> : undefined}
          />
        ) : (
          <>
            <div aria-busy={loading || undefined} className={`hidden sm:block ${loading ? "opacity-60" : ""}`}>
              {view === "day" ? (
                <DayGrid rooms={rooms} day={data.from} zone={zone} />
              ) : (
                <WeekGrid rooms={rooms} days={days} zone={zone} today={today} />
              )}
            </div>
            <div aria-busy={loading || undefined} className={`sm:hidden ${loading ? "opacity-60" : ""}`}>
              <div className="mb-4">
                <Field label="Room">
                  <Select
                    value={phoneRoom ? String(phoneRoom.id) : ""}
                    onChange={(e) => go({ room: e.target.value })}
                    options={rooms.map((r) => ({ value: String(r.id), label: roomName(r) }))}
                  />
                </Field>
              </div>
              {phoneRoom && <PhoneList room={phoneRoom} days={days} zone={zone} />}
            </div>
          </>
        )}
      </div>
    </div>
  );
}

// The look of a booking: a visible one in the primary tint, one waiting for
// approval dashed, a reserved one grey. Each also says so in words.
function slotTone(slot: RoomSlot): string {
  if (slot.reserved) return "border-border bg-sunken text-text-muted";
  if (slot.waiting) return "border-dashed border-border-strong bg-surface text-text";
  return "border-transparent bg-primary-subtle text-primary-on-subtle hover:underline";
}

// A booking as a block or chip: a link when the viewer may open the date,
// plain text when it is reserved.
function SlotBox({ slot, zone, className, children, style }: { slot: RoomSlot; zone: string; className: string; children: ReactNode; style?: CSSProperties }) {
  const to = slotLink(slot);
  const label = slotLabel(slot, zone);
  const cls = `block overflow-hidden rounded-sm border ${slotTone(slot)} ${className}`;
  return to ? (
    <Link to={to} title={label} aria-label={label} className={cls} style={style}>
      {children}
    </Link>
  ) : (
    <div title={label} className={cls} style={style}>
      {children}
    </div>
  );
}

// A booking's words. `short` is a Day block with room for one line: the
// start time, then any waiting label, then the title, so the status stays
// in view (DEC-109) and only the title is cut. `cell` is a Week cell
// (titles wrap to two lines); otherwise it is a phone row.
function SlotWords({ slot, zone, mode }: { slot: RoomSlot; zone: string; mode: "short" | "block" | "cell" | "row" }) {
  const byline = slotByline(slot);
  if (mode === "short") {
    return (
      <span className="block truncate text-xs">
        <span className="tabular-nums">{formatTime(slot.startsAt, zone)}</span>{" "}
        {slot.waiting && <span className="font-semibold">Waiting for approval · </span>}
        <span className="font-semibold">{slot.reserved ? "Reserved" : slot.title}</span>
      </span>
    );
  }
  const titleClass = mode === "block" ? "truncate text-xs" : mode === "cell" ? "line-clamp-2 text-xs" : "line-clamp-2 text-sm";
  return (
    <>
      <span className="block text-xs tabular-nums">{formatTimeRange(slot.startsAt, slot.endsAt, zone)}</span>
      {slot.waiting && <span className="block text-xs font-semibold">Waiting for approval</span>}
      <span className={`block font-semibold ${titleClass}`}>{slot.reserved ? "Reserved" : slot.title}</span>
      {!slot.reserved && byline && <span className="block truncate text-xs">{byline}</span>}
    </>
  );
}

// Day, from sm up: one column per room, time running down. Many rooms
// scroll sideways inside the grid, never the page.
function DayGrid({ rooms, day, zone }: { rooms: RoomCalendarRoom[]; day: string; zone: string }) {
  const { start, end } = hourRange(rooms, day, zone);
  const hours = Array.from({ length: end - start }, (_, i) => start + i);
  const height = (end - start) * HOUR_PX;
  const top = (min: number) => ((min - start * 60) / 60) * HOUR_PX;
  return (
    <div tabIndex={0} role="region" aria-label="Rooms by time" className="overflow-x-auto rounded-lg border border-border bg-surface">
      <div className="flex min-w-max">
        <div className="sticky left-0 z-10 w-16 flex-none border-r border-border bg-surface" aria-hidden="true">
          <div className="h-12 border-b border-border" />
          <div className="relative" style={{ height }}>
            {hours.map((h) => (
              <span key={h} className="absolute right-2 -translate-y-1/2 text-xs text-text-muted" style={{ top: (h - start) * HOUR_PX }}>
                {h === start ? "" : formatHour(h)}
              </span>
            ))}
          </div>
        </div>
        {rooms.map((r) => {
          const placed = placeSlots(r.bookings, day, zone);
          const headingId = `room-${r.id}-day`;
          return (
            <section key={r.id} aria-labelledby={headingId} className="w-44 flex-none border-r border-border last:border-r-0 lg:w-auto lg:min-w-44 lg:flex-1">
              <h3 id={headingId} className="flex h-12 flex-col justify-center border-b border-border px-2 text-sm font-semibold text-text">
                <span className="truncate">{r.name}</span>
                {(r.building || r.restricted) && (
                  <span className="truncate text-xs font-normal text-text-muted">
                    {[r.building, r.restricted && "Admins only"].filter(Boolean).join(" · ")}
                  </span>
                )}
              </h3>
              <div className="relative" style={{ height }}>
                {hours.map((h) => (
                  <div key={h} aria-hidden="true" className="absolute inset-x-0 border-t border-border" style={{ top: (h - start) * HOUR_PX }} />
                ))}
                {placed.length === 0 ? (
                  <p className="sr-only">Nothing booked. {r.name} is free all day.</p>
                ) : (
                  <ul>
                    {placed.map((p, i) => {
                      const h = Math.max(top(p.end) - top(p.start), 24);
                      return (
                      <li
                        key={`${p.slot.startsAt}-${i}`}
                        className="absolute px-0.5"
                        style={{
                          top: top(p.start),
                          height: h,
                          left: `${(p.lane / p.lanes) * 100}%`,
                          width: `${100 / p.lanes}%`,
                        }}
                      >
                        <SlotBox slot={p.slot} zone={zone} className="h-full min-h-6 px-1.5 py-0.5">
                          <SlotWords slot={p.slot} zone={zone} mode={shortBlock(h, p.slot.waiting) ? "short" : "block"} />
                        </SlotBox>
                      </li>
                      );
                    })}
                  </ul>
                )}
              </div>
            </section>
          );
        })}
      </div>
    </div>
  );
}

// Week, from sm up: one row per room, one column per day (DEC-108), so a
// free room or a free evening shows at a glance.
function WeekGrid({ rooms, days, zone, today }: { rooms: RoomCalendarRoom[]; days: string[]; zone: string; today: string }) {
  return (
    <div tabIndex={0} role="region" aria-label="Rooms by day" className="overflow-x-auto rounded-lg border border-border bg-surface">
      <table className="w-full min-w-[60rem] table-fixed border-collapse text-left">
        <caption className="sr-only">Rooms by day</caption>
        <thead>
          <tr>
            <th scope="col" className="sticky left-0 z-10 w-36 border-b border-border bg-surface p-2 text-sm font-semibold text-text">
              Room
            </th>
            {days.map((d) => (
              <th key={d} scope="col" className={`border-b border-l border-border p-2 text-sm font-semibold ${d === today ? "text-primary" : "text-text"}`}>
                {formatWeekday(d)}
                {d === today && <span className="block text-xs font-semibold">Today</span>}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {rooms.map((r) => {
            const byDay = slotsByDay(r.bookings, zone);
            return (
              <tr key={r.id}>
                <th scope="row" className="sticky left-0 z-10 border-t border-border bg-surface p-2 align-top text-sm font-semibold text-text">
                  <span className="block">{r.name}</span>
                  {(r.building || r.restricted) && (
                    <span className="block text-xs font-normal text-text-muted">
                      {[r.building, r.restricted && "Admins only"].filter(Boolean).join(" · ")}
                    </span>
                  )}
                </th>
                {days.map((d) => {
                  const list = byDay.get(d) ?? [];
                  return (
                    <td key={d} className="border-l border-t border-border p-1 align-top">
                      {list.length === 0 ? (
                        <p className="p-1 text-sm text-text-muted">Free</p>
                      ) : (
                        <ul className="space-y-1">
                          {list.map((s, i) => (
                            <li key={`${s.startsAt}-${i}`}>
                              <SlotBox slot={s} zone={zone} className="min-h-11 px-1.5 py-1">
                                <SlotWords slot={s} zone={zone} mode="cell" />
                              </SlotBox>
                            </li>
                          ))}
                        </ul>
                      )}
                    </td>
                  );
                })}
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}

// Phones: one room at a time as a day list. A day with nothing booked says
// the room is free.
function PhoneList({ room, days, zone }: { room: RoomCalendarRoom; days: string[]; zone: string }) {
  const byDay = slotsByDay(room.bookings, zone);
  return (
    <div className="space-y-6">
      {days.map((d) => {
        const list = byDay.get(d) ?? [];
        return (
          <section key={d}>
            {days.length > 1 && <h3 className="py-2 font-semibold text-text">{formatDayHeading(d, zone)}</h3>}
            {list.length === 0 ? (
              <p className="text-sm text-text-muted">Nothing booked. {room.name} is free all day.</p>
            ) : (
              <ul className="space-y-2">
                {list.map((s, i) => (
                  <li key={`${s.startsAt}-${i}`}>
                    <SlotBox slot={s} zone={zone} className="min-h-11 rounded-lg p-3">
                      <SlotWords slot={s} zone={zone} mode="row" />
                    </SlotBox>
                  </li>
                ))}
              </ul>
            )}
          </section>
        );
      })}
    </div>
  );
}

