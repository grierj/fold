import { ContactDetails } from "./ContactDetails";
import { attendeeSubject } from "../api";
import { useEffect, useId, useLayoutEffect, useRef, useState } from "react";
import type { Attendee, HouseholdAttendee, RegisteredBy } from "../types";
import { Button } from "./ui/Button";
import { Badge } from "./ui/Badge";
import { RemoteBadge } from "./RemoteBadge";
import { ChildcareBadge } from "./ChildcareBadge";

// The API has already authorized each field. Notes are also cleared locally
// when time-limited access expires or the tab returns after that boundary.
// showAccountType is for the leader roster only: the teen badge is for
// leaders and admins (DEC-82), and the API also leaves it out for others.
export function AttendeeDetails({ attendee: a, childcare = true, status, showAccountType = false }: { attendee: Attendee; childcare?: boolean; status?: string; showAccountType?: boolean }) {
  const subject = attendeeSubject(a);
  const person: HouseholdAttendee | undefined = subject?.kind === "household" ? subject.person : undefined;
  const holder: RegisteredBy | undefined = person?.registeredBy;
  const [clock, setClock] = useState(Date.now());
  useEffect(() => {
    const update = () => setClock(Date.now());
    window.addEventListener("focus", update);
    const until = person?.careNotesVisibleUntil;
    const timer = until ? window.setTimeout(update, Math.max(0, new Date(until).getTime() - Date.now() + 1)) : undefined;
    return () => { window.removeEventListener("focus", update); if (timer !== undefined) window.clearTimeout(timer); };
  }, [person?.careNotesVisibleUntil]);
  const notes = person?.careNotesVisibleUntil && clock > new Date(person.careNotesVisibleUntil).getTime() ? undefined : person?.careNotes;
  if (!subject) return null;
  return <div className="min-w-0 break-words">
    <span>{a.name}{person && showHousehold(person.householdName, holder?.name) && <> · {person.householdName}</>}</span>
    <span className="inline-flex flex-wrap items-center gap-1 ml-1">
      {person && person.kind !== "adult" && <Badge>{person.kind === "child" ? "Child" : "Teen"}</Badge>}
      {!person && showAccountType && a.accountType === "teen" && <Badge>Teen</Badge>}
      {person?.age !== undefined && <span>Age {person.age}</span>}
      {a.attendanceMode === "remote" && <RemoteBadge />}
      {a.guestCount > 0 && <span>+{a.guestCount}</span>}
      {childcare && (a.childcareRequested || (a.childcareCount ?? 0) > 0) && <ChildcareBadge count={a.childcareRequested ? 1 : a.childcareCount} />}
    </span>
    {person && holder && <div className="text-sm text-text-muted">Signed up by {holder.name}</div>}
    {status && <div className="text-sm text-text-muted">{status}</div>}
    <ContactDetails email={a.email} phone={a.phone} />
    {childcare && a.childcareAgesNote && <Note text={a.childcareAgesNote} label="Ages" name={a.name} />}
    {notes && <Note text={notes} label="Care notes" name={a.name} />}
  </div>;
}

// The household name only adds something when it isn't the signer's
// surname household ("Smith household" under John Smith) (PR 110 N1).
function showHousehold(household: string, holder?: string): boolean {
  const last = holder?.trim().split(/\s+/).pop()?.toLowerCase();
  return !last || household.trim().toLowerCase() !== `${last} household`;
}

function Note({ text, label, name }: { text: string; label: string; name: string }) {
  const [open, setOpen] = useState(false);
  const [clamped, setClamped] = useState(text.length > 120);
  const id = useId();
  const ref = useRef<HTMLDivElement>(null);
  // Offer the toggle only when the clamp actually cuts the note (PR 110 N3).
  useLayoutEffect(() => {
    const el = ref.current;
    if (el && !open && el.clientHeight > 0) setClamped(el.scrollHeight > el.clientHeight + 1);
  }, [text, open]);
  return <div className="mt-1 text-sm text-text-muted">
    <div ref={ref} id={id} className={`whitespace-pre-wrap wrap-anywhere ${open ? "" : "line-clamp-3"}`}>{label}: {text}</div>
    {(clamped || open) && <Button variant="link" aria-expanded={open} aria-controls={id} onClick={() => setOpen(!open)}>
      {open ? `Hide ${label.toLowerCase()}` : `Show full ${label.toLowerCase()} for ${name}`}
    </Button>}
  </div>;
}
