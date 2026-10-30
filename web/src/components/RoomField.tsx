import { useCallback, useEffect, useRef, useState, type KeyboardEvent } from "react";
import { DateRooms, Site, type DateRoomInput } from "../api";
import { useAuth } from "../auth";
import { errorCode, errorText } from "../lib/errors";
import { roomName } from "../lib/place";
import type { RoomBooking, RoomPickerItem } from "../types";
import { Field, FormError, Input, Select, Textarea } from "./ui/Field";
import { Button } from "./ui/Button";
import { Skeleton } from "./ui/Skeleton";

const NONE = "none";
const REASON_MAX = 500;

// A room as the picker words it: "Fellowship Hall · Main (80 seats)", then
// what a leader should know before choosing it.
function optionLabel(room: RoomPickerItem): string {
  const seats = room.capacity === null ? "" : ` (${room.capacity} ${room.capacity === 1 ? "seat" : "seats"})`;
  return `${roomName(room)}${seats}${room.busy ? " — in use at this time" : ""}${room.tooSmall ? " — may be too small" : ""}`;
}

// The Room control of "Edit this date" and the single-date "Edit event"
// (M2.1b, DEC-95: beside Location, not under More options). Booking is its own
// action with its own conflict answer, so a choice is saved at once and never
// waits for the form's Save. While it loads or fails, the rest of the form
// still works. It shows nothing while rooms are off.
export function RoomField({ eventId, date, idPrefix, onChanged, focusOnLoad }: {
  eventId: number;
  // The occurrence's original YYYY-MM-DD.
  date: string;
  idPrefix: string;
  // A booking was made, changed or released: the page behind may show the new place.
  onChanged?: () => void;
  // Put the cursor in the Room select once the rooms load (a "Pick a room" action opened this).
  focusOnLoad?: boolean;
}) {
  const { isAdmin } = useAuth();
  const id = `${idPrefix}-room`;
  const reasonId = `${id}-reason`;
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
  const [padError, setPadError] = useState<{ field: "setup" | "teardown"; text: string } | null>(null);
  const setupRef = useRef<HTMLInputElement>(null);
  const teardownRef = useRef<HTMLInputElement>(null);
  const alive = useRef(true);
  useEffect(() => {
    alive.current = true;
    return () => { alive.current = false; };
  }, []);

  const load = useCallback(async () => {
    setLoading(true);
    setLoadError(null);
    // A church with rooms off, or a site we can't read, sees no rooms control at all.
    let site;
    try {
      site = await Site.get();
    } catch {
      if (alive.current) { setEnabled(false); setLoading(false); }
      return;
    }
    if (!alive.current) return;
    if (!site.roomsEnabled) {
      setEnabled(false);
      setLoading(false);
      return;
    }
    setEnabled(true);
    try {
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

  const focused = useRef(false);
  useEffect(() => {
    if (!focusOnLoad || focused.current || loading || loadError || !enabled) return;
    focused.current = true;
    document.getElementById(id)?.focus();
  }, [focusOnLoad, loading, loadError, enabled, id]);

  // An admin's conflict answer moves focus to the reason it asks for.
  useEffect(() => {
    if (pending) document.getElementById(reasonId)?.focus();
  }, [pending, reasonId]);

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
      document.getElementById(reasonId)?.focus();
      return;
    }
    if (why.length > REASON_MAX) {
      setReasonError(`Use ${REASON_MAX} characters or fewer.`);
      document.getElementById(reasonId)?.focus();
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
      const field = ok(s, setup) ? "teardown" : "setup";
      setPadError({ field, text: "Enter a whole number of minutes from 0 to 240 for both." });
      (field === "setup" ? setupRef : teardownRef).current?.focus();
      return;
    }
    setPadError(null);
    void send({ roomId: booking.roomId, setupMinutes: s, teardownMinutes: t }, () => "Setup and cleanup times saved.");
  }

  if (!enabled && !loadError) return null;

  const requested = booking?.status === "requested";
  const value = booking && booking.status === "confirmed" ? String(booking.roomId) : requested ? "" : NONE;
  const anyBusy = rooms.some((r) => r.busy);
  const onPaddingEnter = (e: KeyboardEvent<HTMLInputElement>) => {
    // Enter here is "save the times", not "submit the editor around this field".
    if (e.key !== "Enter") return;
    e.preventDefault();
    savePadding();
  };

  return (
    <div className="space-y-2">
      {loading && <Skeleton kind="lines" count={1} label="Loading rooms…" />}
      {loadError && (
        <div className="space-y-2">
          <FormError>{loadError}</FormError>
          <Button variant="secondary" onClick={() => void load()}>Try again</Button>
        </div>
      )}
      {!loading && !loadError && (
        <>
          <Field id={id} label="Room" optional hint="Booked as soon as you choose it, so you don't need to press Save. Closing this form won't undo it.">
            <Select value={value} aria-busy={busy || undefined} onChange={(e) => choose(e.target.value)}>
              {requested && <option value="">Pick a room</option>}
              <option value={NONE}>No room</option>
              {rooms.map((r) => (
                <option key={r.id} value={String(r.id)} disabled={r.busy && !isAdmin}>{optionLabel(r)}</option>
              ))}
            </Select>
          </Field>
          {!isAdmin && anyBusy && (
            <p className="text-sm text-text-muted">A room that's in use can only be booked by an admin. Ask an admin if you need it.</p>
          )}
          <div role="status" className="space-y-2 empty:hidden">
            {status && <p className="text-sm text-text-muted">{status}</p>}
            {requested && (
              <p className="rounded-md border border-warning-border bg-warning-subtle p-3 text-sm text-warning-on-subtle">
                <span aria-hidden="true">⚠ </span>{booking?.roomName} was taken at the new time, so this date has no room. Pick another room.
              </p>
            )}
            {booking?.status === "confirmed" && booking.tooSmall && (
              <p className="text-sm text-warning-on-subtle">
                <span aria-hidden="true">⚠ </span>This room seats fewer people than this date allows.
              </p>
            )}
          </div>
          {error && <FormError>{error}</FormError>}
          {pending && (
            <div className="space-y-2 rounded-md border border-warning-border bg-warning-subtle p-3">
              <p role="alert" className="text-sm text-warning-on-subtle"><span aria-hidden="true">⚠ </span>That room is in use at this time. Give a reason to book it anyway.</p>
              <Field id={reasonId} label="Reason for double-booking" error={reasonError}>
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
                    <p className="text-sm text-text-muted">These start as the room's usual times. Changing them affects this booking only.</p>
                    <div className="grid gap-3 sm:grid-cols-2">
                      <Field id={`${id}-setup`} label="Setup minutes" error={padError?.field === "setup" ? padError.text : null}>
                        <Input ref={setupRef} type="number" inputMode="numeric" min={0} max={240} value={setup} onChange={(e) => setSetup(e.target.value)} onKeyDown={onPaddingEnter} />
                      </Field>
                      <Field id={`${id}-teardown`} label="Cleanup minutes" error={padError?.field === "teardown" ? padError.text : null}>
                        <Input ref={teardownRef} type="number" inputMode="numeric" min={0} max={240} value={teardown} onChange={(e) => setTeardown(e.target.value)} onKeyDown={onPaddingEnter} />
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
