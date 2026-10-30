import { useId, useState, type ReactNode } from "react";
import { Button } from "./ui/Button";
import { AttendeeDetails } from "./AttendeeDetails";
import type { EventOccurrence } from "../types";

// Attendance for one date (UI makeover PR 6, DEC-63): counts first, then
// an expand that lists names, shown only when the server says this viewer
// may see them (attendeesVisible; the rules live in api_event_access.go).
// "Can't go" is never among attendees. Only the event's leaders and admins
// get a declined count and names from the server (DEC-64); for them they
// show after the attendees, under "Can't go".
export function Attendance({ instance }: { instance: EventOccurrence }) {
  const [open, setOpen] = useState(false);
  const listId = useId();
  const going = instance.registrations.going ?? 0;
  const maybe = instance.registrations.maybe ?? 0;
  const declined = instance.registrations.declined; // undefined unless you run the event
  const cant = (instance.attendees ?? []).filter((a) => a.status === "declined");
  const coming = instance.attendeesVisible
    ? (instance.attendees ?? []).filter((a) => a.status === "going" || a.status === "maybe")
    : [];
  // These section counts describe visible names, not the total seats above.
  const sections = [
    { label: "Going", people: coming.filter((a) => a.status === "going") },
    { label: "Maybe", people: coming.filter((a) => a.status === "maybe") },
    { label: "Can't go", people: cant },
  ];
  return (
    <div className="mt-1 text-sm text-text-muted">
      <div>
        {going === 0 && maybe === 0 ? "No one yet" : `${going} going${maybe ? ` · ${maybe} maybe` : ""}`}
        {declined ? ` · ${declined} can't go` : ""}
        {instance.capacity !== null && <> · {seatsLeft(instance.spotsLeft ?? 0, instance.capacity, "")}</>}
        {instance.remoteAvailable && instance.remoteCapacity === 0 && " · No remote seats on this date"}
        {instance.remoteAvailable &&
          instance.remoteCapacity !== null &&
          instance.remoteCapacity > 0 &&
          ` · ${seatsLeft(instance.remoteSpotsLeft ?? 0, instance.remoteCapacity, "remote ")}`}
      </div>
      {instance.remoteAvailable && instance.remoteCapacity === null && <div>Remote attendance available</div>}
      {instance.childcareFull && <div>Childcare is full for this date</div>}
      {instance.childcareAvailable && instance.childcareUsed != null && (
        <div>{instance.childcareCapacity == null
          ? `${instance.childcareUsed} children in childcare`
          : `${Math.max(0, instance.childcareCapacity - instance.childcareUsed)} of ${instance.childcareCapacity} childcare spots left`}</div>
      )}
      {going + maybe > coming.length && <p>Some people's names are private.</p>}
      {coming.length + cant.length > 0 && (
        <>
          <Button variant="link" aria-expanded={open} aria-controls={listId} onClick={() => setOpen((v) => !v)}>
            {open ? "Hide names" : "Show who's coming"}
          </Button>
          {open && (
            <div id={listId} className="space-y-3">
              {sections.filter((section) => section.people.length > 0).map((section) => (
                <section key={section.label} aria-label={`${section.label} names`}>
                  <h4 className="font-semibold">{section.label} ({section.people.length})</h4>
                  <ul className="space-y-2">
                    {section.people.map((a) => <li key={a.registrationId}><AttendeeDetails attendee={a} childcare={instance.childcareAvailable} /></li>)}
                  </ul>
                </section>
              ))}
            </div>
          )}
        </>
      )}
    </div>
  );
}

// "0 of 1 seat left", "In-person seats full" (Illuminator, PR 110 N4). One to
// five in-person seats left read "Only 3 seats left" in the warning tone
// (DEC-99); the words carry the meaning, color only echoes it. Remote seats
// keep the plain count.
function seatsLeft(left: number, capacity: number, kind: "" | "remote "): ReactNode {
  if (left <= 0) return kind ? "Remote seats full" : "Seats full";
  if (!kind && left <= 5) return <span className="font-semibold text-warning-on-subtle">Only {left} {left === 1 ? "seat" : "seats"} left</span>;
  return `${left} of ${capacity} ${kind}${capacity === 1 ? "seat" : "seats"} left`;
}
