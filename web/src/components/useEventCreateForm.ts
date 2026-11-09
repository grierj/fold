import { useRef, useState } from "react";
import type { Visibility } from "../types";
import { errorText } from "../lib/errors";
import { useEventZone } from "../lib/useEventZone";
import { useWhenState } from "../lib/useWhenState";
import { scheduleFields, scheduleReady } from "./CreateRoomField";
import { emptyEventOptions, eventOptionsBody, optionFieldOfError, validateEventOptions, type OptionErrors } from "./eventOptionValues";
import { showFieldErrors } from "./ui/Field";

// The state and checks both create forms share (a group's event and a
// church-wide one): the same fields, the same order of checks, the same body.
// The forms differ in their title rule, a visibility reason or a host, and
// where they go afterwards, which they keep.
export function useEventCreateForm(kind: "group" | "church") {
  const form = useRef<HTMLFormElement>(null);
  const [title, setTitle] = useState("");
  const [description, setDescription] = useState("");
  const when = useWhenState({ date: "", time: "19:00", duration: 90, recurring: false, days: [] });
  const { tzid, setTzid } = useEventZone();
  const [location, setLocation] = useState("");
  const [visibility, setVisibility] = useState<Visibility>("public");
  const [options, setOptions] = useState(emptyEventOptions);
  // The room for every date; the event page lists any dates it is busy on.
  const [roomId, setRoomId] = useState<number | null>(null);
  // Closed on create; a problem inside it opens it (DEC-95).
  const [moreOpen, setMoreOpen] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [optionErrors, setOptionErrors] = useState<OptionErrors>({});

  // Everything a person typed, for the unsaved-changes check.
  const snapshot = { title, description, when: { date: when.date, time: when.time, duration: when.duration, recurring: when.recurring, days: when.days }, tzid, location, visibility, options, roomId };

  // Runs the checks in the order the form reads; on a problem it shows it and
  // moves focus there, and on success it marks the form busy.
  function begin(): boolean {
    if (kind === "church" && !title.trim()) return fail("Give the event a title.");
    if (!when.date) return fail("Pick a date.");
    if (!tzid.trim()) return fail("Pick a time zone.");
    if (when.recurring && when.days.length === 0) return fail("Pick at least one day to repeat weekly.");
    const problem = validateEventOptions(options);
    if (problem) {
      showFieldErrors(form.current, () => {
        setError(problem.message);
        setOptionErrors({ [problem.field]: problem.message });
        setMoreOpen(true);
      });
      return false;
    }
    setOptionErrors({});
    setBusy(true);
    setError(null);
    return true;
  }

  function fail(message: string): false {
    setError(message);
    return false;
  }

  // Shows a server error, under the option it is about when it names one.
  function reject(err: unknown, fallback: string) {
    const message = errorText(err, fallback);
    const field = optionFieldOfError(err);
    if (!field) {
      setError(message);
      return;
    }
    showFieldErrors(form.current, () => {
      setError(message);
      setOptionErrors({ [field]: message });
      setMoreOpen(true);
    });
  }

  return {
    form,
    title, setTitle, description, setDescription, when, tzid, setTzid, location, setLocation,
    visibility, setVisibility, options, setOptions, roomId, setRoomId,
    moreOpen, setMoreOpen, busy, setBusy, error, optionErrors,
    snapshot, begin, reject,
    // The room check needs the schedule once it is filled in enough.
    schedule: scheduleReady(when, tzid, options.seats) ? scheduleFields(when, tzid, options.seats) : null,
    // The fields every create request carries; the form adds its own.
    body: () => ({
      title: title.trim(),
      description: description.trim(),
      ...scheduleFields(when, tzid, options.seats),
      location: location.trim(),
      visibility,
      ...eventOptionsBody(options),
      ...(roomId !== null ? { roomId } : {}),
    }),
  };
}
