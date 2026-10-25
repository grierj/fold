import { useCallback, useEffect, useId, useRef, useState } from "react";
import { useParams } from "react-router-dom";
import { ApiError, Events } from "../api";
import { Dates } from "../date-api";
import { GuestRegistrationForm } from "../components/GuestRegistrationForm";
import { useAuth } from "../auth";
import type { AttendanceMode, EventDate } from "../types";
import { Button, ButtonLink } from "../components/ui/Button";
import { EmptyState } from "../components/ui/EmptyState";
import { Choices, Field, FormError, Input, Radio } from "../components/ui/Field";
import { errorText } from "../lib/errors";
import { formatWhen, heldIn } from "../lib/when";

export function EventDatePage() {
  const { user } = useAuth();
  const { id = "", date = "" } = useParams();
  return <DateDetail key={`${user?.id ?? "anonymous"}:${id}:${date}`} id={id} date={date} />;
}

function DateDetail({ id, date }: { id: string; date: string }) {
  const { user } = useAuth();
  const [occurrence, setOccurrence] = useState<EventDate | null>(null);
  const [loaded, setLoaded] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [revision, setRevision] = useState(0);
  const generation = useRef(0);
  const mounted = useRef(false);
  const clear = useCallback(() => { setOccurrence(null); setBusy(false); setError(""); }, []);
  const refresh = useCallback(() => {
    // Invalidate synchronously, before React commits: a late mutation must not
    // restore disclosures even if it resolves in the same turn as focus.
    generation.current++; clear(); setLoaded(false); setRevision((n) => n + 1);
  }, [clear]);
  useEffect(() => {
    mounted.current = true;
    const current = ++generation.current;
    clear(); setLoaded(false);
    Dates.get(id, date).then(({ occurrence: row }) => {
      if (generation.current === current) { setOccurrence(row); setLoaded(true); }
    }).catch(() => {
      // Failed authority refreshes always fail closed, including network errors.
      if (generation.current === current) { clear(); setLoaded(true); }
    });
    window.addEventListener("focus", refresh);
    return () => { mounted.current = false; generation.current++; window.removeEventListener("focus", refresh); };
  }, [id, date, revision, refresh, clear]);

  async function answer(status: "going" | "maybe" | "declined", options: { attendanceMode?: AttendanceMode; guestCount?: number }) {
    if (!occurrence || !(occurrence.canDecline || occurrence.canRegister) || busy || (status === "declined" && (!occurrence.canRegister || occurrence.canDecline))) return;
    const current = generation.current;
    setBusy(true); setError("");
    try {
      if (occurrence.canDecline && status !== "declined") await Dates.register(id, date, { status, ...options });
      else await Events.register(Number(id), { date, status, ...options });
      if (generation.current === current) refresh();
    } catch (e) {
      if (!mounted.current) return;
      if (e instanceof ApiError && [401, 403, 404].includes(e.status)) { generation.current++; clear(); setLoaded(true); }
      else if (generation.current === current) { setBusy(false); setError(errorText(e)); }
    }
  }
  async function end(action: "decline" | "block") {
    if (!occurrence || busy || !(action === "block" ? occurrence.canBlock : occurrence.canDecline)) return;
    const copy = action === "decline"
      ? "Can't go to this date? This ends your invitation and access to this date only. Other accepted dates stay yours."
      : "Don't add me again? This ends your invitation to this date and stops future manager adds from this organizer (the group's leaders, or this event's hosts). Other accepted dates stay yours. It does not stop you choosing to register where you are eligible.";
    if (!window.confirm(copy)) return;
    const current = generation.current;
    setBusy(true); setError("");
    try {
      await Dates.end(id, date, action);
      // A successful end revokes this date even if a focus read raced it.
      // Unlike an answer response, it must also invalidate newer pending reads.
      if (mounted.current) { generation.current++; clear(); setLoaded(true); }
    } catch (e) {
      if (!mounted.current) return;
      if (e instanceof ApiError && [401, 403, 404].includes(e.status)) { generation.current++; clear(); setLoaded(true); }
      else if (generation.current === current) { setBusy(false); setError(errorText(e)); }
    }
  }
  const zone = occurrence && heldIn(occurrence.startsAt, occurrence.tzid);
  return <div className="max-w-2xl mx-auto px-4 py-6 space-y-5">
    <h1 className="text-2xl font-bold">Event date</h1>
    <div className="flex flex-wrap gap-2"><ButtonLink variant="link" to="/calendar">Your calendar</ButtonLink><Button variant="secondary" onClick={refresh}>Refresh</Button></div>
    {!loaded && <p role="status">Loading this date…</p>}
    {loaded && !occurrence && <EmptyState title="This date is not available" body="Your invitation may have ended, or this date may not be available to you. Check your calendar for dates you can open." />}
    {occurrence && <section aria-label="This event date" className="bg-surface border border-border rounded-lg p-4 space-y-4 wrap-anywhere">
      <h2 className="text-xl font-bold">{occurrence.title || "One event date"}</h2>
      <p>{formatWhen(occurrence.startsAt)} – {formatWhen(occurrence.endsAt)}</p>
      {zone && <p className="text-sm text-text-muted">{zone}</p>}
      {occurrence.location && <p>{occurrence.location}</p>}
      {occurrence.hostedBy && <p className="text-sm text-text-muted">Hosted by {occurrence.hostedBy}</p>}
      {!user && occurrence.visibility === "public" && <GuestRegistrationForm instance={occurrence} />}
      <p className="text-text-muted">This page is for one date only. It does not give access to a group, a series or other dates.</p>
      {user && <p role="status" className="font-semibold">Your answer: {occurrence.myRegistration === "going" ? "Going" : occurrence.myRegistration === "maybe" ? "Maybe" : occurrence.myRegistration === "waitlisted" ? "Waitlisted" : occurrence.myRegistration === "declined" ? "Can't go" : occurrence.myRegistration === "cancelled" ? "Cancelled" : "Not answered"}</p>}
      {occurrence.myAttendanceMode && <p>Attending: {occurrence.myAttendanceMode === "remote" ? "Remote" : "In person"}{occurrence.myGuestCount !== undefined ? ` · Guests: ${occurrence.myGuestCount}` : ""}</p>}
      {occurrence.canSeeVideo && occurrence.videoUrl && <ButtonLink variant="secondary" to={occurrence.videoUrl} external>Join video call (opens in a new tab)</ButtonLink>}
      {occurrence.videoHidden && !(occurrence.canSeeVideo && occurrence.videoUrl) && <p className="text-sm text-text-muted">The video link is not available to you. Check your answer or refresh to see your current access.</p>}
      {error && <FormError>{error}</FormError>}
      {(occurrence.canDecline || occurrence.canRegister) && <DateAnswer occurrence={occurrence} busy={busy} onAnswer={answer} />}
      <div className="flex flex-wrap gap-2">
        {occurrence.canRegister && !occurrence.canDecline && <Button variant="secondary" disabled={busy} onClick={() => void answer("declined", {})}>Can't go</Button>}
        {occurrence.canDecline && <Button variant="danger-outline" disabled={busy} onClick={() => void end("decline")}>Can't go</Button>}
        {occurrence.canBlock && <Button variant="danger-outline" disabled={busy} onClick={() => void end("block")}>Don't add me again</Button>}
      </div>
    </section>}
  </div>;
}

function DateAnswer({ occurrence, busy, onAnswer }: { occurrence: EventDate; busy: boolean; onAnswer: (status: "going" | "maybe" | "declined", options: { attendanceMode?: AttendanceMode; guestCount?: number }) => Promise<void> }) {
  const [mode, setMode] = useState<AttendanceMode | undefined>(occurrence.myAttendanceMode ?? (occurrence.canRegister && !occurrence.canDecline ? "in_person" : undefined));
  const [guests, setGuests] = useState(occurrence.myGuestCount === undefined ? (occurrence.canRegister && !occurrence.canDecline ? "0" : "") : String(occurrence.myGuestCount));
  const modeName = useId();
  const guestMax = occurrence.guestsAllowed;
  const guestValid = guests !== "" && Number.isInteger(Number(guests)) && Number(guests) >= 0 && Number(guests) <= guestMax;
  const showGuests = (occurrence.myGuestCount !== undefined || (occurrence.canRegister && !occurrence.canDecline)) && (guestMax > 0 || (occurrence.myGuestCount ?? 0) > 0);
  const options = { ...(mode === undefined ? {} : { attendanceMode: mode }), ...(showGuests ? { guestCount: Number(guests) } : {}) };
  return <div className="space-y-3">
    <p className="text-sm text-text-muted">Answer for yourself only. Going depends on available space; you may be waitlisted. Care options cannot be edited here.</p>
    {(occurrence.remoteAvailable || mode === "remote") && mode !== undefined && <Choices legend="How will you attend?" hint="Choose in person or remotely. Space is checked when you save.">
      <Radio name={modeName} label="In person" checked={mode === "in_person"} disabled={busy} onChange={() => setMode("in_person")} />
      <Radio name={modeName} label="Remote" description={!occurrence.remoteAvailable ? "No longer available. Choose in person to change your answer." : undefined} checked={mode === "remote"} disabled={busy || !occurrence.remoteAvailable} onChange={() => setMode("remote")} />
    </Choices>}
    {showGuests && <Field label="Guests" hint={`Whole number from 0 to ${guestMax}. Guests use spaces too.`} error={!guestValid ? `Enter a whole number from 0 to ${guestMax}.` : undefined}><Input type="number" min={0} max={guestMax} step={1} value={guests} disabled={busy} onChange={(e) => setGuests(e.target.value)} /></Field>}
    <div className="flex flex-wrap gap-2"><Button variant="primary" disabled={busy || (showGuests && !guestValid)} onClick={() => void onAnswer("going", options)}>Going</Button><Button variant="secondary" disabled={busy || (showGuests && !guestValid)} onClick={() => void onAnswer("maybe", options)}>Maybe</Button></div>
  </div>;
}
