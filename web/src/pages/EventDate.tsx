import { useCallback, useEffect, useId, useRef, useState, type ReactNode } from "react";
import { useParams } from "react-router-dom";
import { ApiError, Events } from "../api";
import { Dates } from "../date-api";
import { GuestRegistrationForm } from "../components/GuestRegistrationForm";
import { EventIntro } from "../components/EventIntro";
import { useAuth } from "../auth";
import type { AttendanceMode, EventDate } from "../types";
import { Button, ButtonLink } from "../components/ui/Button";
import { EmptyState } from "../components/ui/EmptyState";
import { Choices, Field, FormError, Input, Radio } from "../components/ui/Field";
import { SegmentButton } from "../components/ui/SegmentButton";
import { errorText } from "../lib/errors";
import { heldIn } from "../lib/when";
import { dateLine } from "../lib/repeat";
import { placeLine } from "../lib/place";
import { DateBadge } from "../components/DateBadge";
import { answerLine } from "../components/RsvpButtons";
import { DateActions } from "../components/DateActions";
import { composePath, EMAIL_GOING, useCanEmailEvent } from "../components/messages/composeLinks";

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
  const when = occurrence ? dateLine(occurrence.startsAt, occurrence.endsAt) : "";
  const place = occurrence ? placeLine(occurrence.room, occurrence.location) : "";
  const standing = occurrence && user ? standingLine(occurrence.myRegistration) : null;
  const canEmail = useCanEmailEvent(Number(id));
  return <div className="max-w-2xl mx-auto px-4 py-6 space-y-5 wrap-anywhere">
    <div>
      {/* The link says where it goes for screen readers and is also not confused with the nav's Calendar tab. */}
      <ButtonLink variant="link" to="/calendar"><span aria-hidden="true">‹ Calendar</span><span className="sr-only">Back to calendar</span></ButtonLink>
      <h1 className="text-2xl font-bold">{occurrence?.title || "Event date"}</h1>
      {/* hostedBy with no group can be a church-wide event or a hidden group's fallback, so it keeps "Hosted by" (DEC-99). */}
      {occurrence?.visibility && <EventIntro hostedBy={occurrence.hostedBy} groupName={occurrence.groupName} groupId={occurrence.groupId} />}
    </div>
    {!loaded && <p role="status">Loading this date…</p>}
    {loaded && !occurrence && <EmptyState title="This date is not available" body="Your invitation may have ended, or this date may not be available to you. Check your calendar for dates you can open." />}
    {occurrence && <section aria-label="This event date" className="bg-surface border border-border rounded-lg p-4 space-y-4">
      <div className="flex gap-4">
        <DateBadge at={occurrence.startsAt} />
        <div className="min-w-0 grow">
          <p className="font-medium">{when}</p>
          {zone && <p className="text-sm text-text-muted">{zone}</p>}
          {place && <p className="text-text-muted">{place}</p>}
        </div>
      </div>
      <DateActions eventId={occurrence.eventId} date={occurrence.date} sharePath={occurrence.dateUrl} title={occurrence.title} when={when} />
      {!user && occurrence.visibility === "public" && <GuestRegistrationForm instance={occurrence} />}
      {/* Only a real exact-date invitation (DEC-75): canDecline is true for those reads alone. */}
      {occurrence.canDecline && <p className="text-text-muted">You're invited to this date.</p>}
      {occurrence.myAttendanceMode && <p>Attending: {occurrence.myAttendanceMode === "remote" ? "Remote" : "In person"}{occurrence.myGuestCount !== undefined ? ` · Guests: ${occurrence.myGuestCount}` : ""}</p>}
      {occurrence.canSeeVideo && occurrence.videoUrl && <ButtonLink variant="secondary" to={occurrence.videoUrl} external>Join video call (opens in a new tab)</ButtonLink>}
      {occurrence.videoHidden && !(occurrence.canSeeVideo && occurrence.videoUrl) && <p className="text-sm text-text-muted">The video link is not available to you. Check your answer or return to this page to check again.</p>}
      {error && <FormError>{error}</FormError>}
      {(occurrence.canDecline || occurrence.canRegister) && <DateAnswer occurrence={occurrence} busy={busy} onAnswer={answer} />}
      {/* The standing line is this card's one status region, as on the event page (DEC-92). */}
      {user && <p role="status" className="text-sm text-text-muted empty:hidden">{standing}</p>}
      <div className="flex flex-wrap gap-2">
        {occurrence.canDecline && <Button variant="danger-outline" disabled={busy} onClick={() => void end("decline")}>Can't go</Button>}
        {occurrence.canBlock && <Button variant="danger-outline" disabled={busy} onClick={() => void end("block")}>Don't add me again</Button>}
        {canEmail && <ButtonLink variant="secondary" to={composePath({ occurrence: occurrence.id })}>{EMAIL_GOING}</ButtonLink>}
      </div>
    </section>}
  </div>;
}

// The same standing confirmation the event page shows under its answer
// control (DEC-92). The segments show going and maybe by fill alone, so this
// line is what says it in words, and the only place waitlisted and
// cancelled are named.
function standingLine(status: EventDate["myRegistration"]): ReactNode {
  switch (status) {
    case "waitlisted": return "You're on the waitlist. We'll email you if a spot opens.";
    case "cancelled": return "Your sign-up was cancelled.";
    default: return answerLine(status);
  }
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
    {/* The date JSON carries no capacity, so this can only be a general hint; it is moot once Going is saved. */}
    {occurrence.myRegistration !== "going" && occurrence.myRegistration !== "waitlisted" && <p className="text-sm text-text-muted">If it’s full, Going puts you on the waitlist.</p>}
    {(occurrence.remoteAvailable || mode === "remote") && mode !== undefined && <Choices legend="How will you attend?" hint="Choose in person or remotely. Space is checked when you save.">
      <Radio name={modeName} label="In person" checked={mode === "in_person"} disabled={busy} onChange={() => setMode("in_person")} />
      <Radio name={modeName} label="Remote" description={!occurrence.remoteAvailable ? "No longer available. Choose in person to change your answer." : undefined} checked={mode === "remote"} disabled={busy || !occurrence.remoteAvailable} onChange={() => setMode("remote")} />
    </Choices>}
    {showGuests && <Field label="Guests" hint={`Whole number from 0 to ${guestMax}. Guests use spaces too.`} error={!guestValid ? `Enter a whole number from 0 to ${guestMax}.` : undefined}><Input type="number" min={0} max={guestMax} step={1} value={guests} disabled={busy} onChange={(e) => setGuests(e.target.value)} /></Field>}
    <div role="group" aria-label="Your answer" className="flex w-full sm:max-w-sm">
      <SegmentButton pressed={occurrence.myRegistration === "going" || occurrence.myRegistration === "waitlisted"} disabled={busy || (showGuests && !guestValid)} onClick={() => void onAnswer("going", options)}>Going</SegmentButton>
      <SegmentButton pressed={occurrence.myRegistration === "maybe"} disabled={busy || (showGuests && !guestValid)} onClick={() => void onAnswer("maybe", options)}>Maybe</SegmentButton>
      {occurrence.canRegister && !occurrence.canDecline && <SegmentButton pressed={occurrence.myRegistration === "declined"} disabled={busy} onClick={() => void onAnswer("declined", {})}>Can't go</SegmentButton>}
    </div>
  </div>;
}
