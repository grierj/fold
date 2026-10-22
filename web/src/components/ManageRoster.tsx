import { useEffect, useState } from "react";
import { Events, attendeeSubject } from "../api";
import type { Attendee, EventOccurrence } from "../types";
import { errorCode, errorText } from "../lib/errors";
import { AttendeeDetails } from "./AttendeeDetails";
import { Button } from "./ui/Button";
import { Field, FormError, Input } from "./ui/Field";
import { EmptyState } from "./ui/EmptyState";

export function ManageRoster({ instance, onChanged, onClose }: { instance: EventOccurrence; onChanged: () => void; onClose: () => void }) {
  const [busy, setBusy] = useState<number | "add" | null>(null);
  const [email, setEmail] = useState("");
  const [error, setError] = useState<string | null>(null);
  // Refresh at the next care boundary and whenever a background tab returns.
  useEffect(() => {
    const boundaries = (instance.attendees ?? []).flatMap((a) => a.householdPerson?.careNotesVisibleUntil ? [new Date(a.householdPerson.careNotesVisibleUntil).getTime()] : []).filter((t) => t >= Date.now());
    const timer = boundaries.length ? window.setTimeout(onChanged, Math.max(0, Math.min(...boundaries) - Date.now() + 1)) : undefined;
    window.addEventListener("focus", onChanged);
    return () => { window.removeEventListener("focus", onChanged); if (timer !== undefined) window.clearTimeout(timer); };
  }, [instance.attendees, onChanged]);

  async function action(a: Attendee, kind: "remove" | "promote" | "restore") {
    if (kind === "remove" && !confirm(`Remove ${a.name}'s registration for this date? They won't be able to re-register on their own.`)) return;
    const full = a.attendanceMode === "remote" ? instance.remoteCapacity !== null && (instance.remoteSpotsLeft ?? 1) <= 0 : instance.capacity !== null && (instance.spotsLeft ?? 1) <= 0;
    if (kind === "restore" && full && !confirm(`This date's ${a.attendanceMode === "remote" ? "remote" : "in-person"} seats are full. Add ${a.name} over capacity?`)) return;
    setBusy(a.registrationId); setError(null);
    try {
      if (kind === "remove") await Events.removeRegistration(instance.eventId, instance.date, a.registrationId);
      else if (kind === "promote") await Events.promoteRegistration(instance.eventId, instance.date, a.registrationId);
      else {
        const subject = attendeeSubject(a);
        const target = subject?.kind === "household" ? { personId: subject.person.personId, registeredBy: subject.person.registeredBy.userId } : subject?.kind === "user" ? { userId: subject.userId } : null;
        if (!target) return;
        try { await Events.restoreRegistration(instance.eventId, instance.date, target, a.attendanceMode, full); }
        catch (e) {
          if (errorCode(e) !== "event_full" || full || !confirm(`This date's ${a.attendanceMode === "remote" ? "remote" : "in-person"} seats filled. Add ${a.name} over capacity?`)) throw e;
          await Events.restoreRegistration(instance.eventId, instance.date, target, a.attendanceMode, true);
        }
      }
      onChanged();
    } catch (e) { setError(errorText(e, "We couldn't update that registration.")); }
    finally { setBusy(null); }
  }

  async function add(e: React.FormEvent) {
    e.preventDefault(); if (!email.trim()) return;
    const full = instance.capacity !== null && (instance.spotsLeft ?? 1) <= 0;
    if (full && !confirm("This date is full. Add them over capacity?")) return;
    setBusy("add"); setError(null);
    try { await Events.addRegistration(instance.eventId, instance.date, email.trim(), full || undefined); setEmail(""); onChanged(); }
    catch (e) { setError(errorText(e, "We couldn't add that registration.")); }
    finally { setBusy(null); }
  }

  const roster = (instance.attendees ?? []).filter((a) => ["going", "maybe", "waitlisted"].includes(a.status));
  return <section aria-label="Manage sign-ups" className="mt-3 border-t border-border pt-3">
    <div className="flex items-center justify-between gap-3"><h3 className="font-semibold">Manage sign-ups</h3><Button variant="link" onClick={onClose}>Close</Button></div>
    {error && <FormError>{error}</FormError>}
    {roster.length === 0 && <EmptyState title="No sign-ups yet" body="People who sign up for this date will appear here. You can add someone by email below." />}
    <ul className="space-y-3 my-3">{roster.map((a) => <li key={a.registrationId} className="flex flex-col sm:flex-row sm:items-start sm:justify-between gap-3">
      <div className="min-w-0 w-full sm:flex-1"><AttendeeDetails attendee={a} childcare={instance.childcareAvailable} status={statusLabel(a, roster)} showAccountType /></div>
      <div className="flex flex-wrap gap-2">
        {a.status === "waitlisted" && <Button variant="secondary" disabled={busy !== null} onClick={() => void action(a, "promote")}>Promote</Button>}
        <Button variant="danger-outline" aria-label={`Remove ${a.name}`} disabled={busy !== null} onClick={() => void action(a, "remove")}>Remove</Button>
      </div>
    </li>)}</ul>
    {(instance.removedRegistrations?.length ?? 0) > 0 && <section aria-label="Removed or left out" className="my-4">
      <h4 className="font-semibold">Removed or left out</h4>
      <ul className="space-y-3 mt-2">{instance.removedRegistrations?.map((a) => <li key={a.registrationId} className="flex flex-col sm:flex-row sm:items-start sm:justify-between gap-3">
        <div className="min-w-0 w-full sm:flex-1"><AttendeeDetails attendee={a} childcare={false} showAccountType /></div><Button variant="secondary" aria-label={`Add back ${a.name}`} disabled={busy !== null} onClick={() => void action(a, "restore")}>Add back</Button>
      </li>)}</ul>
    </section>}
    <form onSubmit={add} className="space-y-2"><Field label="Add someone" hint="Their email address"><Input type="email" value={email} onChange={(e) => setEmail(e.target.value)} /></Field><Button type="submit" variant="secondary" disabled={busy !== null}>{busy === "add" ? "Adding…" : "Add"}</Button></form>
  </section>;
}

// Roster status in the RSVP form's words (Illuminator, PR 110 N2).
// The API lists waiters in waitlist order; each attendance pool has its own line.
function statusLabel(a: Attendee, roster: Attendee[]): string {
  switch (a.status) {
    case "going": return a.attendanceMode === "remote" ? "Going remotely" : "Going";
    case "maybe": return "Maybe";
    case "waitlisted": {
      const line = roster.filter((r) => r.status === "waitlisted" && r.attendanceMode === a.attendanceMode);
      return `Waitlist position ${line.indexOf(a) + 1}`;
    }
    case "declined": return "Can't go";
    default: return "Removed";
  }
}
