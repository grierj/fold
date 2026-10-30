import { useCallback, useEffect, useRef, useState } from "react";
import { DateRooms, Site, type DateRoomInput } from "../api";
import { useAuth } from "../auth";
import { errorCode, errorText } from "../lib/errors";
import { roomName } from "../lib/place";
import type { RoomBooking, RoomPickerItem } from "../types";
import { ErrorNotice } from "./ErrorNotice";
import { Field, FormError, Input, Select, Textarea } from "./ui/Field";
import { Button } from "./ui/Button";
import { Skeleton } from "./ui/Skeleton";

const NONE = "none";
const REASON_MAX = 500;

// A room as the picker words it: "Fellowship Hall · Main (80 seats)", then
// what a leader should know before choosing it.
function optionLabel(room: RoomPickerItem): string {
  const seats = room.capacity === null ? "" : ` (${room.capacity} ${room.capacity === 1 ? "seat" : "seats"})`;
  return `${roomName(room)}${seats}${room.busy ? " — booked then" : ""}${room.tooSmall ? " — too small" : ""}`;
}

// The Room control of "Edit this date" and the single-date "Edit event"
// (M2.1b, DEC-95: beside Location, not under More options). Booking is its own
// action with its own conflict answer, so a choice is saved at once and never
// waits for the form's Save. While it loads or fails, the rest of the form
// still works. It shows nothing while rooms are off.
export function RoomField({ eventId, date, idPrefix, onChanged }: {
  eventId: number;
  // The occurrence's original YYYY-MM-DD.
  date: string;
  idPrefix: string;
  // A booking was made, changed or released: the page behind may show the new place.
  onChanged?: () => void;
}) {
  const { isAdmin } = useAuth();
  const [enabled, setEnabled] = useState(false);
  const [loading, setLoading] = useState(true);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [rooms, setRooms] = useState<RoomPickerItem[]>([]);
  const [booking, setBooking] = useState<RoomBooking | null>(null);
  const [status, setStatus] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  // An admin's conflict answer: the booking to resend with a reason.
  const [pending, setPending] = useState<DateRoomInput | null>(null);
  const [reason, setReason] = useState("");
  const [reasonError, setReasonError] = useState<string | null>(null);
  const [padOpen, setPadOpen] = useState(false);
  const [setup, setSetup] = useState("");
  const [teardown, setTeardown] = useState("");
  const [padError, setPadError] = useState<string | null>(null);
  const alive = useRef(true);
  useEffect(() => {
    alive.current = true;
    return () => { alive.current = false; };
  }, []);

  const load = useCallback(async () => {
    setLoading(true);
    setLoadError(null);
    try {
      const site = await Site.get();
      if (!alive.current) return;
      if (!site.roomsEnabled) {
        setEnabled(false);
        setLoading(false);
        return;
      }
      setEnabled(true);
      const res = await DateRooms.list(eventId, date);
      if (!alive.current) return;
      setRooms(res.rooms);
      setBooking(res.booking);
    } catch (e) {
      if (alive.current) setLoadError(errorText(e, "We couldn't load the rooms."));
    } finally {
      if (alive.current) setLoading(false);
    }
  }, [eventId, date]);
  useEffect(() => { void load(); }, [load]);

  // A new booking prefills the padding override with what it holds.
  useEffect(() => {
    setSetup(booking ? String(booking.setupMinutes) : "");
    setTeardown(booking ? String(booking.teardownMinutes) : "");
    setPadError(null);
  }, [booking]);

  async function send(body: DateRoomInput, done: (b: RoomBooking | null) => string) {
    setBusy(true);
    setError(null);
    setStatus("");
    try {
      const res = await DateRooms.set(eventId, date, body);
      if (!alive.current) return;
      setBooking(res.booking);
      setPending(null);
      setReason("");
      setReasonError(null);
      setStatus(done(res.booking));
      onChanged?.();
    } catch (e) {
      if (!alive.current) return;
      if (errorCode(e) === "room_busy" && isAdmin && body.roomId !== null) {
        // An admin may book over a conflict, with a reason.
        setPending(body);
      } else {
        setPending(null);
        setError(errorText(e, "We couldn't update the room."));
      }
    } finally {
      if (alive.current) setBusy(false);
    }
  }

  function choose(value: string) {
    if (busy) return;
    setPending(null);
    setReasonError(null);
    if (value === "") return;
    if (value === NONE) {
      void send({ roomId: null }, () => "Room released.");
      return;
    }
    const room = rooms.find((r) => String(r.id) === value);
    if (!room) return;
    void send({ roomId: room.id }, () => `Booked ${room.name}.`);
  }

  function bookAnyway() {
    if (!pending) return;
    const why = reason.trim();
    if (!why) {
      setReasonError("Say why you are double-booking this room.");
      return;
    }
    if (why.length > REASON_MAX) {
      setReasonError(`Use ${REASON_MAX} characters or fewer.`);
      return;
    }
    setReasonError(null);
    const room = rooms.find((r) => r.id === pending.roomId);
    void send({ ...pending, overlapReason: why }, () => `Booked ${room?.name ?? "the room"} over another booking.`);
  }

  function savePadding() {
    if (!booking) return;
    const s = Number(setup);
    const t = Number(teardown);
    const ok = (n: number, raw: string) => raw.trim() !== "" && Number.isInteger(n) && n >= 0 && n <= 240;
    if (!ok(s, setup) || !ok(t, teardown)) {
      setPadError("Enter a whole number of minutes from 0 to 240 for both.");
      return;
    }
    setPadError(null);
    void send({ roomId: booking.roomId, setupMinutes: s, teardownMinutes: t }, () => "Setup and cleanup times saved.");
  }

  if (!enabled && !loadError) return null;

  const requested = booking?.status === "requested";
  const value = booking && booking.status === "confirmed" ? String(booking.roomId) : requested ? "" : NONE;
  const id = `${idPrefix}-room`;

  return (
    <div className="space-y-2">
      {loading && <Skeleton kind="lines" count={1} label="Loading rooms…" />}
      {loadError && <ErrorNotice message={loadError} onRetry={() => void load()} />}
      {!loading && !loadError && (
        <>
          <Field id={id} label="Room" optional hint="Booked as soon as you choose it. You don't need to press Save.">
            <Select value={value} aria-busy={busy || undefined} onChange={(e) => choose(e.target.value)}>
              {requested && <option value="">Pick a room</option>}
              <option value={NONE}>No room</option>
              {rooms.map((r) => (
                <option key={r.id} value={String(r.id)} disabled={r.busy && !isAdmin}>{optionLabel(r)}</option>
              ))}
            </Select>
          </Field>
          <p role="status" className="text-sm text-text-muted empty:hidden">{status}</p>
          {error && <FormError>{error}</FormError>}
          {requested && (
            <p className="rounded-md border border-warning-border bg-warning-subtle p-3 text-sm text-warning-on-subtle">
              <span aria-hidden="true">⚠ </span>Lost when the date moved. Pick another room.
            </p>
          )}
          {booking?.status === "confirmed" && booking.tooSmall && (
            <p className="text-sm text-warning-on-subtle">
              <span aria-hidden="true">⚠ </span>This room seats fewer people than this date allows.
            </p>
          )}
          {pending && (
            <div className="space-y-2 rounded-md border border-warning-border bg-warning-subtle p-3">
              <p className="text-sm text-warning-on-subtle"><span aria-hidden="true">⚠ </span>That room is already booked then. As an admin you can book it anyway, with a reason.</p>
              <Field id={`${id}-reason`} label="Reason for double-booking" error={reasonError}>
                <Textarea rows={2} maxLength={REASON_MAX} value={reason} onChange={(e) => setReason(e.target.value)} />
              </Field>
              <Button variant="secondary" disabled={busy} onClick={bookAnyway}>Book anyway</Button>
            </div>
          )}
          {isAdmin && booking?.status === "confirmed" && (
            <div>
              <Button variant="link" aria-expanded={padOpen} aria-controls={`${id}-padding`} onClick={() => setPadOpen((v) => !v)}>
                Setup and cleanup times
              </Button>
              <div id={`${id}-padding`} hidden={!padOpen} className="mt-2 space-y-2">
                {padOpen && (
                  <>
                    <p className="text-sm text-text-muted">The room holds this long before and after the date. It starts at the room's usual times; change it for this booking only.</p>
                    <div className="grid gap-3 sm:grid-cols-2">
                      <Field id={`${id}-setup`} label="Setup minutes" error={padError}>
                        <Input type="number" inputMode="numeric" min={0} max={240} value={setup} onChange={(e) => setSetup(e.target.value)} />
                      </Field>
                      <Field id={`${id}-teardown`} label="Cleanup minutes">
                        <Input type="number" inputMode="numeric" min={0} max={240} value={teardown} onChange={(e) => setTeardown(e.target.value)} />
                      </Field>
                    </div>
                    <Button variant="secondary" disabled={busy} onClick={savePadding}>Save times</Button>
                  </>
                )}
              </div>
            </div>
          )}
        </>
      )}
    </div>
  );
}
