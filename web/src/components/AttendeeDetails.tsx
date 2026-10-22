import { useEffect, useId, useState } from "react";
import type { Attendee, HouseholdAttendee, RegisteredBy } from "../types";
import { Button } from "./ui/Button";
import { Badge } from "./ui/Badge";
import { RemoteBadge } from "./RemoteBadge";
import { ChildcareBadge } from "./ChildcareBadge";

// The API has already authorized each field. Notes are also cleared locally
// when time-limited access expires or the tab returns after that boundary.
export function AttendeeDetails({ attendee: a, childcare = true }: { attendee: Attendee; childcare?: boolean }) {
  const person: HouseholdAttendee | undefined = a.householdPerson;
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
  return <div className="min-w-0 break-words">
    <span>{a.name}{person && <> · {person.householdName} (via {holder?.name})</>}</span>
    <span className="inline-flex flex-wrap items-center gap-1 ml-1">
      {person && person.kind !== "adult" && <Badge>{person.kind === "child" ? "Child" : "Teen"}</Badge>}
      {person?.age !== undefined && <span>Age {person.age}</span>}
      {a.attendanceMode === "remote" && <RemoteBadge />}
      {a.guestCount > 0 && <span>+{a.guestCount}</span>}
      {childcare && (a.childcareRequested || (a.childcareCount ?? 0) > 0) && <ChildcareBadge count={a.childcareRequested ? 1 : a.childcareCount} />}
    </span>
    {childcare && a.childcareAgesNote && <Note text={a.childcareAgesNote} label="Ages" name={a.name} />}
    {notes && <Note text={notes} label="Care notes" name={a.name} />}
  </div>;
}

function Note({ text, label, name }: { text: string; label: string; name: string }) {
  const [open, setOpen] = useState(false);
  const id = useId();
  return <div className="mt-1 text-sm text-text-muted">
    <div id={id} className={`whitespace-pre-wrap wrap-anywhere ${open ? "" : "line-clamp-3"}`}>{label}: {text}</div>
    <Button variant="link" aria-expanded={open} aria-controls={id} onClick={() => setOpen(!open)}>
      {open ? `Hide ${label.toLowerCase()}` : `Show full ${label.toLowerCase()} for ${name}`}
    </Button>
  </div>;
}
