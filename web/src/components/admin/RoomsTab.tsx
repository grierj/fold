import { useEffect, useRef, useState } from "react";
import { AdminRooms, type RoomInput } from "../../api";
import type { Room } from "../../types";
import { Badge } from "../ui/Badge";
import { Button } from "../ui/Button";
import { EmptyState } from "../ui/EmptyState";
import { Checkbox, Field, FormError, focusFirstInvalid, Input, Textarea } from "../ui/Field";
import { Form } from "../ui/Form";
import { FormActions } from "../ui/FormActions";
import { ErrorNotice } from "../ErrorNotice";
import { RoomFeedPanel } from "./RoomFeedPanel";
import { Skeleton } from "../ui/Skeleton";
import { errorCode, errorText } from "../../lib/errors";
import { useUnsavedChanges } from "../../lib/unsavedChanges";

// The room catalog (M2.1a). Active rooms first, then building and name, the
// same order the server sends.
export function sortRooms(rooms: Room[]): Room[] {
  return [...rooms].sort((a, b) =>
    Number(b.active) - Number(a.active) || a.building.localeCompare(b.building) || a.name.localeCompare(b.name));
}

const details = (r: Room) =>
  [r.building, r.capacity !== null ? `Seats ${r.capacity}` : "", r.features].filter(Boolean).join(" · ");

interface RoomForm {
  name: string;
  building: string;
  capacity: string;
  features: string;
  notes: string;
  restricted: boolean;
  setup: string;
  cleanup: string;
}

const EMPTY: RoomForm = { name: "", building: "", capacity: "", features: "", notes: "", restricted: false, setup: "", cleanup: "" };

const fromRoom = (r: Room): RoomForm => ({
  name: r.name,
  building: r.building,
  capacity: r.capacity === null ? "" : String(r.capacity),
  features: r.features,
  notes: r.notes,
  restricted: r.restricted,
  setup: String(r.defaultSetupMinutes),
  cleanup: String(r.defaultTeardownMinutes),
});

function RoomFormPanel({ room, onSaved, onCancel }: {
  room?: Room;
  onSaved: (room: Room) => void;
  onCancel: () => void;
}) {
  const initial = room ? fromRoom(room) : EMPTY;
  const [form, setForm] = useState<RoomForm>(initial);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [nameError, setNameError] = useState<string | null>(null);
  const formEl = useRef<HTMLFormElement>(null);
  const dirty = (Object.keys(initial) as (keyof RoomForm)[]).some((k) => form[k] !== initial[k]);
  const { guard, markSaved } = useUnsavedChanges(dirty);
  const set = <K extends keyof RoomForm>(key: K, value: RoomForm[K]) => setForm((f) => ({ ...f, [key]: value }));

  // The button that opened the form is gone, so focus starts on the first field.
  useEffect(() => {
    formEl.current?.querySelector<HTMLElement>("input, textarea, select")?.focus();
  }, []);

  // The name error renders after the failed save; focus its field then.
  useEffect(() => {
    if (nameError) focusFirstInvalid(formEl.current);
  }, [nameError]);

  async function save(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError(null);
    setNameError(null);
    const minutes = (value: string) => (value.trim() === "" ? undefined : Number(value));
    const body: RoomInput = {
      name: form.name.trim(),
      building: form.building.trim(),
      features: form.features.trim(),
      notes: form.notes.trim(),
      restricted: form.restricted,
    };
    if (room) {
      // Editing sends every field, so a cleared capacity goes as null and a
      // cleared padding goes back to 0.
      body.capacity = form.capacity.trim() === "" ? null : Number(form.capacity);
      body.defaultSetupMinutes = minutes(form.setup) ?? 0;
      body.defaultTeardownMinutes = minutes(form.cleanup) ?? 0;
    } else {
      if (form.capacity.trim() !== "") body.capacity = Number(form.capacity);
      const setup = minutes(form.setup);
      const cleanup = minutes(form.cleanup);
      if (setup !== undefined) body.defaultSetupMinutes = setup;
      if (cleanup !== undefined) body.defaultTeardownMinutes = cleanup;
    }
    try {
      const saved = room ? await AdminRooms.update(room.id, body) : await AdminRooms.create(body);
      markSaved();
      onSaved(saved.room);
    } catch (err) {
      if (errorCode(err) === "room_name_taken") setNameError(errorText(err));
      else setError(errorText(err, "We couldn't save that room."));
    } finally {
      setBusy(false);
    }
  }

  const idPrefix = room ? `room-${room.id}` : "room-new";
  return (
    <Form ref={formEl} aria-label={room ? `Edit ${room.name}` : "Add room"} noValidate onSubmit={save}
      className="bg-surface rounded-lg border border-border p-6 space-y-4 max-w-xl">
      {guard}
      {error && <FormError>{error}</FormError>}
      <Field id={`${idPrefix}-name`} label="Name" error={nameError}>
        <Input value={form.name} maxLength={100} required
          onChange={(e) => { set("name", e.target.value); setNameError(null); }} />
      </Field>
      <Field id={`${idPrefix}-building`} label="Building" optional>
        <Input value={form.building} maxLength={100} onChange={(e) => set("building", e.target.value)} />
      </Field>
      <Field id={`${idPrefix}-capacity`} label="Capacity" optional hint="Leave blank for no limit">
        <Input type="number" inputMode="numeric" min={1} max={100000} step={1} layout="w-28"
          value={form.capacity} onChange={(e) => set("capacity", e.target.value)} />
      </Field>
      <Field id={`${idPrefix}-features`} label="Features" optional hint="For example: projector, piano, kitchen">
        <Input value={form.features} maxLength={500} onChange={(e) => set("features", e.target.value)} />
      </Field>
      <Field id={`${idPrefix}-notes`} label="Notes for admins" optional hint="Only admins see this">
        <Textarea rows={3} value={form.notes} maxLength={2000} onChange={(e) => set("notes", e.target.value)} />
      </Field>
      <Checkbox id={`${idPrefix}-restricted`} label="Only admins can book this room" description="For rooms like the nursery"
        checked={form.restricted} onChange={(e) => set("restricted", e.target.checked)} />
      <Field id={`${idPrefix}-setup`} label="Setup minutes" optional hint="Held before each booking. Leave blank for none.">
        <Input type="number" inputMode="numeric" min={0} max={240} step={1} layout="w-28"
          value={form.setup} onChange={(e) => set("setup", e.target.value)} />
      </Field>
      <Field id={`${idPrefix}-cleanup`} label="Cleanup minutes" optional hint="Held after each booking. Leave blank for none.">
        <Input type="number" inputMode="numeric" min={0} max={240} step={1} layout="w-28"
          value={form.cleanup} onChange={(e) => set("cleanup", e.target.value)} />
      </Field>
      <FormActions>
        <Button variant="primary" layout="w-full sm:w-auto" type="submit" disabled={busy}>
          {busy ? "Saving…" : room ? "Save room" : "Add room"}
        </Button>
        <Button variant="secondary" layout="w-full sm:w-auto" onClick={onCancel}>Cancel</Button>
      </FormActions>
    </Form>
  );
}

// The list lives in AdminPage (it also feeds the tab count), so a save hands
// the room back and the one list updates.
export function RoomsTab({ rooms, error, reload, onSaved }: {
  rooms: Room[] | null;
  error: string | null;
  reload: () => void;
  onSaved: (room: Room) => void;
}) {
  // "new" is the add form; a number is the room being edited.
  const [open, setOpen] = useState<"new" | number | null>(null);
  const [status, setStatus] = useState("");
  const [actionError, setActionError] = useState<string | null>(null);
  const [busyId, setBusyId] = useState<number | null>(null);
  const statusRef = useRef<HTMLParagraphElement>(null);
  const rootEl = useRef<HTMLDivElement>(null);
  // Which opener button gets focus back after Cancel: "new" or a room id.
  const [returnTo, setReturnTo] = useState<"new" | number | null>(null);
  // The room whose calendar-feed panel is open (M2.2b), and whose
  // "Calendar feed" button gets focus back when it closes.
  const [feedOpen, setFeedOpen] = useState<number | null>(null);
  const [feedReturn, setFeedReturn] = useState<number | null>(null);

  useEffect(() => {
    if (feedReturn === null || feedOpen !== null) return;
    rootEl.current?.querySelector<HTMLElement>(`[data-feed-opener="${feedReturn}"]`)?.focus();
    setFeedReturn(null);
  }, [feedReturn, feedOpen]);

  function closeFeed() {
    setFeedReturn(feedOpen);
    setFeedOpen(null);
  }

  useEffect(() => {
    if (returnTo === null || open !== null) return;
    rootEl.current?.querySelector<HTMLElement>(`[data-opener="${returnTo}"]`)?.focus();
    setReturnTo(null);
  }, [returnTo, open]);

  function cancel() {
    setReturnTo(open);
    setOpen(null);
  }

  // The form and its trigger go away on save, so focus moves to the line that says what happened.
  useEffect(() => {
    if (status) statusRef.current?.focus();
  }, [status]);

  function saved(room: Room, verb: string) {
    onSaved(room);
    setOpen(null);
    setStatus(`“${room.name}” ${verb}.`);
  }

  async function setActive(room: Room, active: boolean) {
    setBusyId(room.id);
    setActionError(null);
    setStatus("");
    try {
      const r = await AdminRooms.update(room.id, { active });
      onSaved(r.room);
      setStatus(`“${r.room.name}” ${active ? "reactivated" : "deactivated"}.`);
    } catch (e) {
      setActionError(errorText(e, `We couldn't ${active ? "reactivate" : "deactivate"} that room.`));
    } finally {
      setBusyId(null);
    }
  }

  if (!rooms) return error ? <ErrorNotice message={error} onRetry={reload} /> : <Skeleton kind="lines" />;

  function startAdding() {
    setStatus("");
    setOpen("new");
  }

  return (
    <div ref={rootEl} className="space-y-3">
      {actionError && <FormError>{actionError}</FormError>}
      {/* Always rendered so screen readers announce the text when it arrives; empty, it takes no room. */}
      <p ref={statusRef} role="status" tabIndex={-1} className={status ? "text-sm text-text-muted focus:outline-none" : "sr-only"}>{status}</p>
      {open === "new" ? (
        <RoomFormPanel onSaved={(r) => saved(r, "added")} onCancel={cancel} />
      ) : rooms.length > 0 && (
        // While another room's form is open, switching would drop its edits, so the openers wait.
        <div><Button variant="secondary" data-opener="new" disabled={open !== null} onClick={startAdding}>Add room</Button></div>
      )}
      {sortRooms(rooms).map((r) => open === r.id ? (
        <RoomFormPanel key={r.id} room={r} onSaved={(saved_) => saved(saved_, "saved")} onCancel={cancel} />
      ) : (
        <div key={r.id} className="bg-surface rounded-lg border border-border p-5">
          <div className="flex items-center gap-3 flex-wrap">
            <div className="grow min-w-0">
              <div className="flex items-center gap-2 flex-wrap">
                <span className="font-bold">{r.name}</span>
                {r.restricted && <Badge>Restricted</Badge>}
                {!r.active && <Badge>Inactive</Badge>}
              </div>
              {details(r) && <p className="text-sm text-text-muted mt-0.5">{details(r)}</p>}
            </div>
            <Button variant="secondary" data-opener={r.id} aria-label={`Edit ${r.name}`} disabled={open !== null}
              onClick={() => { setStatus(""); setOpen(r.id); }}>Edit</Button>
            {r.active && (
              <Button variant="secondary" data-feed-opener={r.id} aria-label={`Calendar feed for ${r.name}`}
                disabled={feedOpen !== null} onClick={() => { setStatus(""); setFeedOpen(r.id); }}>Calendar feed</Button>
            )}
            <Button variant="secondary" aria-label={`${r.active ? "Deactivate" : "Reactivate"} ${r.name}`} disabled={busyId === r.id}
              onClick={() => void setActive(r, !r.active)}>{r.active ? "Deactivate" : "Reactivate"}</Button>
          </div>
          {feedOpen === r.id && r.active && <div className="mt-4"><RoomFeedPanel room={r} onClose={closeFeed} /></div>}
        </div>
      ))}
      {rooms.length === 0 && open !== "new" && (
        <EmptyState
          title="No rooms yet"
          body="Add the rooms your church books, like the fellowship hall or classrooms."
          action={<Button variant="primary" data-opener="new" onClick={startAdding}>Add room</Button>}
        />
      )}
    </div>
  );
}
