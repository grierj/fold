import { ChildcareRequestField, childcareCountProblem } from "./ChildcareRequestField";
import { Button } from "./ui/Button";
import { useId, useRef, useState } from "react";
import { Events } from "../api";
import type { AttendanceMode, EventOccurrence, RegistrationResponse, RegistrationStatus } from "../types";
import { errorCode, errorText } from "../lib/errors";
import { Choices, Field, FormError, Input, Radio, showFieldErrors } from "./ui/Field";

const OPTIONS: { value: RegistrationStatus; label: string }[] = [
  { value: "going", label: "Going" },
  { value: "maybe", label: "Maybe" },
  { value: "declined", label: "Can't go" },
];

const MODES: { value: AttendanceMode; label: string }[] = [
  { value: "in_person", label: "In person" },
  { value: "remote", label: "Remote" },
];

// Registration for one event date, as a Going / Maybe / Can't go
// segmented control. Knows the M1.5
// states: going, waitlisted with position, full with no waitlist, and
// removed. The guest count input appears when the event allows guests.
export function RsvpButtons({
  instance,
  onChange,
}: {
  instance: EventOccurrence;
  onChange: (reg: RegistrationResponse) => void;
}) {
  const [busy, setBusy] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [guests, setGuests] = useState(instance.myGuestCount ?? 0);
  const offersRemote = instance.remoteAvailable && instance.remoteCapacity !== 0;
  const [pickedMode, setMode] = useState<AttendanceMode>(instance.myAttendanceMode ?? "in_person");
  const mode = offersRemote ? pickedMode : "in_person";
  const [statusMessage, setStatusMessage] = useState("");
  const modeName = useId();
  const root = useRef<HTMLDivElement>(null);
  const savedStatus = useRef<HTMLDivElement>(null);
  const [children, setChildren] = useState(String(instance.myChildcareCount ?? 0));
  const [ages, setAges] = useState(instance.myChildcareAgesNote ?? "");
  const [childcareError, setChildcareError] = useState<string | null>(null);
  const [childcareFullError, setChildcareFullError] = useState(false);

  const mine = instance.myRegistration;
  const removed = mine === "removed";
  const waitlisted = mine === "waitlisted";
  // Full is judged in the pool you picked: remote seats are their own.
  const myMode = instance.myAttendanceMode ?? "in_person";
  const poolFull =
    mode === "remote"
      ? instance.remoteCapacity !== null && (instance.remoteSpotsLeft ?? 1) <= 0
      : instance.capacity !== null && (instance.spotsLeft ?? 1) <= 0;
  const fullNoWaitlist =
    poolFull &&
    !instance.waitlistEnabled &&
    !(mine === "going" && myMode === mode) &&
    mine !== "waitlisted";

  const remoteFull = mode === "remote" && poolFull && !(mine === "going" && myMode === mode);
  const inPersonFull = instance.capacity !== null && (instance.spotsLeft ?? 1) <= 0;
  const remoteFullMessage = inPersonFull
    ? `Remote seats are full.${instance.waitlistEnabled ? " You can join the remote waitlist." : ""}`
    : instance.waitlistEnabled
      ? "Remote seats are full. You can still come in person, or join the remote waitlist."
      : "Remote seats are full. You can still come in person.";

  async function choose(status: RegistrationStatus, attendanceMode = mode, switching = false,
    childcare?: Parameters<typeof Events.register>[5]) {
    setBusy(status);
    setError(null);
    setChildcareError(null);
    setChildcareFullError(false);
    setStatusMessage("");
    try {
      const guestCount = status === "going" ? guests : 0;
      const { registration } = await Events.register(
        instance.eventId,
        status,
        instance.date,
        guestCount,
        instance.remoteAvailable ? attendanceMode : undefined,
        status === "going" && instance.childcareAvailable
          ? childcare ?? (mine !== "going" ? { childcareCount: 0, childcareAgesNote: "" } : undefined)
          : undefined
      );
      onChange(registration);
      if (switching) {
        setStatusMessage(registration.status === "waitlisted"
          ? `You're on the ${attendanceMode === "remote" ? "remote" : "in-person"} waitlist.`
          : `Switched to ${attendanceMode === "remote" ? "remote" : "in person"}`);
      } else if (instance.childcareAvailable && status === "going") {
        setChildren(String(registration.childcareCount));
        setAges(registration.childcareAgesNote);
        setStatusMessage(registration.status === "waitlisted"
          ? `You're on the ${attendanceMode === "remote" ? "remote" : "in-person"} waitlist.`
          : `Saved: going, ${registration.childcareCount > 0
            ? `${registration.childcareCount} ${registration.childcareCount === 1 ? "child" : "children"} in childcare`
            : "no childcare"}`);
        savedStatus.current?.focus();
      }
    } catch (e) {
      if (switching) setMode(myMode);
      if (errorCode(e) === "childcare_full") {
        showFieldErrors(root.current, () => {
          setBusy(null);
          setChildcareError(errorText(e, "We couldn't save your childcare request."));
          setChildcareFullError(true);
        });
        return;
      }
      setError(attendanceMode === "remote" && errorCode(e) === "event_full"
        ? remoteFullMessage
        : errorText(e, "We couldn't save your answer."));
    } finally {
      setBusy(null);
    }
  }

  function saveChildcare() {
    const problem = childcareCountProblem(children);
    if (problem) {
      showFieldErrors(root.current, () => setChildcareError(problem));
      return;
    }
    void choose("going", mode, false, { childcareCount: Number(children), childcareAgesNote: ages.trim() });
  }

  if (removed) {
    return (
      <div className="text-sm text-text-muted">
        A leader or host removed your registration for this date. Ask them to add you back.
      </div>
    );
  }

  return (
    <div ref={root}>
      {waitlisted && instance.myWaitlistPosition != null && (
        <div className="text-sm font-semibold text-text-muted mb-2">
          You&apos;re on the waitlist — position {instance.myWaitlistPosition}. We&apos;ll email you if a spot
          opens.
        </div>
      )}
      {remoteFull ? (
        <div className="text-sm font-semibold text-text-muted mb-2">
          {remoteFullMessage}
        </div>
      ) : fullNoWaitlist && <div className="text-sm font-semibold text-text-muted mb-2">This event is full.</div>}
      {offersRemote && (
        <div className="mb-2">
          <Choices legend="How you'll attend">
            {MODES.map((m) => (
              <Radio
                key={m.value}
                name={modeName}
                label={m.label}
                checked={mode === m.value}
                disabled={busy !== null}
                onChange={() => {
                  setMode(m.value);
                  if (mine === "going") void choose("going", m.value, true);
                }}
              />
            ))}
          </Choices>
          <div aria-live="polite" className="text-sm text-text-muted mt-2">
            {mine !== "going" && `Tap Going to attend ${mode === "remote" ? "remotely" : "in person"}.`}
          </div>
        </div>
      )}
      <div ref={savedStatus} role="status" tabIndex={-1} className="text-sm text-text-muted mb-2">{statusMessage}</div>
      <div className="flex gap-2 flex-wrap items-center">
        {/* Segmented control (DEC-63): one joined row, the chosen answer filled. */}
        <div role="group" aria-label="Your answer" className="flex w-full sm:max-w-sm">
        {OPTIONS.map((o) => {
          const active = mine === o.value;
          const disabled = busy !== null || (o.value === "going" && fullNoWaitlist);
          return (
            <button
              key={o.value}
              type="button"
              aria-pressed={active}
              disabled={disabled}
              onClick={() => void choose(o.value)}
              className="segment"
            >
              {busy === o.value ? "…" : o.label}
            </button>
          );
        })}
        </div>
        {instance.guestsAllowed > 0 && (
          <Field label="Extra guests" hint={`Up to ${instance.guestsAllowed}.`}>
            <Input
              type="number"
              min={0}
              max={instance.guestsAllowed}
              value={guests}
              onChange={(e) => setGuests(Math.max(0, Math.min(instance.guestsAllowed, Number(e.target.value))))}
              layout="w-28"
            />
          </Field>
        )}
      </div>
      {instance.childcareAvailable && mine === "going" && (
        <div className="mt-3 space-y-3">
          <p className="font-semibold">Add childcare</p>
          <ChildcareRequestField count={children} ages={ages} onCount={setChildren} onAges={setAges}
            error={childcareError} disabled={busy !== null} full={instance.childcareFull} />
          <div className="flex flex-wrap gap-2">
            <Button variant="primary" disabled={busy !== null} onClick={saveChildcare}>
              {busy === "going" ? "Saving…" : "Save"}
            </Button>
            {childcareFullError && <Button variant="secondary" disabled={busy !== null}
              onClick={() => void choose("going", mode, false, { childcareCount: 0, childcareAgesNote: "" })}>
              Go without childcare
            </Button>}
          </div>
        </div>
      )}
      {error && <FormError layout="mt-1">{error}</FormError>}
    </div>
  );
}
