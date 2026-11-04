import { useEffect, useRef, useState } from "react";
import { AdminUsers, ApiError } from "../api";
import { useAuth } from "../auth";
import type { HandoffCandidate, SoleHostedEvent, SoleLedGroup, User } from "../types";
import { errorCode, errorText } from "../lib/errors";
import { formatEventDay } from "../lib/when";
import { Button } from "./ui/Button";
import { Field, FormError, Select } from "./ui/Field";

// Confirms deleting or disabling an account. For every group the person
// leads alone, the admin picks a new leader first: a member of that group or any admin, with
// the deleting admin preselected (DEC-36). Deleting also needs a new host for
// every church-wide event the person hosts alone, and that one is never
// preselected: nobody becomes a host without being chosen (M7.4, R4-H).

type HostPicks = Record<string, number>;

const isRecord = (v: unknown): v is Record<string, unknown> => typeof v === "object" && v !== null;

function isCandidate(v: unknown): v is HandoffCandidate {
  return isRecord(v) && typeof v.id === "number" && typeof v.name === "string" && typeof v.admin === "boolean";
}

// An event row from a 400 body; the date is dropped unless it is a string.
function soleEventFrom(v: unknown): SoleHostedEvent | null {
  if (!isRecord(v) || typeof v.id !== "number" || typeof v.title !== "string") return null;
  return typeof v.nextStartsAt === "string" ? { id: v.id, title: v.title, nextStartsAt: v.nextStartsAt } : { id: v.id, title: v.title };
}

function isSoleLedGroup(v: unknown): v is SoleLedGroup {
  return isRecord(v) && typeof v.id === "number" && typeof v.name === "string" && typeof v.status === "string"
    && Array.isArray(v.candidates) && v.candidates.every(isCandidate);
}

// The admin is the default for a group, else the first person listed.
function groupPick(g: SoleLedGroup, meId: number | undefined): number | undefined {
  return (g.candidates.find((c) => c.id === meId) ?? g.candidates[0])?.id;
}

function initialGroupPicks(groups: SoleLedGroup[], meId: number | undefined): Record<string, number> {
  const picks: Record<string, number> = {};
  for (const g of groups) {
    const pick = groupPick(g, meId);
    if (pick !== undefined) picks[String(g.id)] = pick;
  }
  return picks;
}

const candidateLabel = (c: HandoffCandidate, meId: number | undefined) =>
  `${c.id === meId ? `Me (${c.name})` : c.name}${c.admin && c.id !== meId ? " · admin" : ""}`;

export function DeleteUserPanel({
  target,
  action,
  onDone,
}: {
  target: User;
  action: "delete" | "disable";
  onDone: (done: boolean) => void;
}) {
  const { user: me } = useAuth();
  const meId = me?.id;
  const deleting = action === "delete";
  const [groups, setGroups] = useState<SoleLedGroup[] | null>(null);
  const [events, setEvents] = useState<SoleHostedEvent[] | null>(null);
  const [candidates, setCandidates] = useState<HandoffCandidate[]>([]);
  const [picks, setPicks] = useState<Record<string, number>>({});
  const [hostPicks, setHostPicks] = useState<HostPicks>({});
  const [everyHost, setEveryHost] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [focusTick, setFocusTick] = useState(0);
  const panel = useRef<HTMLDivElement>(null);
  const alertBox = useRef<HTMLDivElement>(null);

  useEffect(() => {
    let live = true;
    // Disabling needs no host: their events keep their host row (DEC-38).
    const hostRequest = deleting ? AdminUsers.soleHostEvents(target.id) : Promise.resolve(null);
    Promise.all([AdminUsers.soleLeaderGroups(target.id), hostRequest])
      .then(([led, hostedEvents]) => {
        if (!live) return;
        setGroups(led.groups);
        setPicks(initialGroupPicks(led.groups, meId));
        if (hostedEvents) {
          setEvents(hostedEvents.events);
          setCandidates(hostedEvents.candidates);
        }
      })
      .catch((e) => {
        if (live) setError(errorText(e, deleting ? "We couldn't load their groups and events." : "We couldn't load the groups."));
      });
    return () => {
      live = false;
    };
  }, [target.id, deleting, meId]);

  // After a stale answer has rendered: the first event still without a host,
  // else the message.
  useEffect(() => {
    if (focusTick === 0) return;
    const open = [...(panel.current?.querySelectorAll<HTMLSelectElement>("select[data-host-event]") ?? [])].find((s) => s.value === "");
    (open ?? alertBox.current)?.focus();
  }, [focusTick]);

  // The server's list of events and candidates changed under us. Keep a pick
  // only while its event and its person both still appear.
  function applyStaleHosts(data: Record<string, unknown>) {
    if (!Array.isArray(data.events) || !Array.isArray(data.candidates)) return;
    const nextEvents = data.events.flatMap((v) => soleEventFrom(v) ?? []);
    const nextCandidates = data.candidates.filter(isCandidate);
    const kept: HostPicks = {};
    for (const ev of nextEvents) {
      const pick = hostPicks[String(ev.id)];
      if (pick !== undefined && nextCandidates.some((c) => c.id === pick)) kept[String(ev.id)] = pick;
    }
    setEvents(nextEvents);
    setCandidates(nextCandidates);
    setHostPicks(kept);
    setEveryHost("");
    setFocusTick((n) => n + 1);
  }

  function applyStaleGroups(data: Record<string, unknown>) {
    if (!Array.isArray(data.groups)) return;
    const next = data.groups.filter(isSoleLedGroup);
    const kept: Record<string, number> = {};
    for (const g of next) {
      const old = picks[String(g.id)];
      const pick = old !== undefined && g.candidates.some((c) => c.id === old) ? old : groupPick(g, meId);
      if (pick !== undefined) kept[String(g.id)] = pick;
    }
    setGroups(next);
    setPicks(kept);
  }

  function chooseEvery(value: string) {
    setEveryHost(value);
    if (value === "") return;
    const all: HostPicks = {};
    for (const ev of events ?? []) all[String(ev.id)] = Number(value);
    setHostPicks(all);
  }

  function chooseHost(eventId: number, value: string) {
    setEveryHost("");
    setHostPicks((p) => {
      const next = { ...p };
      if (value === "") delete next[String(eventId)];
      else next[String(eventId)] = Number(value);
      return next;
    });
  }

  async function remove() {
    setBusy(true);
    setError(null);
    try {
      if (deleting) await AdminUsers.remove(target.id, picks, hostPicks);
      else await AdminUsers.update(target.id, { status: "disabled", successors: picks });
      onDone(true);
    } catch (e) {
      const code = errorCode(e);
      if (e instanceof ApiError) {
        if (code === "needs_host_successor") applyStaleHosts(e.data);
        else if (code === "needs_successor") applyStaleGroups(e.data);
      }
      setError(errorText(e, "That didn't work."));
    } finally {
      setBusy(false);
    }
  }

  const name = target.displayName || target.name;
  const ready = groups !== null && (!deleting || events !== null);
  const hosted = events ?? [];
  const missing = hosted.filter((ev) => hostPicks[String(ev.id)] === undefined).length;
  const hostsDone = hosted.length === 0 || (candidates.length > 0 && missing === 0);
  return (
    <div ref={panel} className="mt-3 rounded-md border border-danger-border bg-danger-subtle p-4 text-sm space-y-3">
      <p className="font-semibold text-danger-on-subtle">
        {deleting ? "Delete" : "Disable"} {name}? They will be signed out and can't sign in.
      </p>
      {action === "disable" && (
        <p className="text-text-muted">
          Their groups and registrations are kept but hidden until you enable them again; they come back as a member, not a
          leader.
        </p>
      )}
      {!ready && !error && (
        <p role="status" className="text-text-muted">
          {deleting ? "Checking their groups and events…" : "Checking their groups…"}
        </p>
      )}
      {groups && groups.length > 0 && (
        <div className="space-y-2">
          <p className="text-text-muted">They are the only leader of these groups. Choose who leads each one next:</p>
          {groups.map((g) => (
            <Field key={g.id} label={`Next leader of ${g.name}`}>
              <Select
                value={picks[String(g.id)] ?? ""}
                onChange={(e) => setPicks((p) => ({ ...p, [String(g.id)]: Number(e.target.value) }))}
                layout="sm:max-w-xs"
              >
                {g.candidates.map((c) => (
                  <option key={c.id} value={c.id}>
                    {candidateLabel(c, meId)}
                  </option>
                ))}
              </Select>
            </Field>
          ))}
        </div>
      )}
      {deleting && hosted.length > 0 && (
        <div className="space-y-3">
          <p className="text-text-muted">They are the only host of these church-wide events. Choose who hosts each one next:</p>
          {candidates.length === 0 ? (
            <>
              <ul className="list-disc space-y-1 pl-5">
                {hosted.map((ev) => (
                  <li key={ev.id}>
                    <span className="wrap-anywhere">{ev.title}</span>
                    <span className="text-text-muted"> · {nextDate(ev)}</span>
                  </li>
                ))}
              </ul>
              <p className="text-text-muted">No one else can host these events yet. Add or enable an adult account, then come back.</p>
            </>
          ) : (
            <>
              {hosted.length > 1 && (
                <Field label="Host for every event" optional>
                  <Select value={everyHost} onChange={(e) => chooseEvery(e.target.value)} layout="sm:max-w-xs">
                    <option value="">Choose for each event</option>
                    {candidates.map((c) => (
                      <option key={c.id} value={c.id}>
                        {candidateLabel(c, meId)}
                      </option>
                    ))}
                  </Select>
                </Field>
              )}
              {hosted.map((ev) => (
                <Field key={ev.id} label={`New host for ${ev.title}`} hint={nextDate(ev)}>
                  <Select
                    value={hostPicks[String(ev.id)] ?? ""}
                    onChange={(e) => chooseHost(ev.id, e.target.value)}
                    layout="sm:max-w-xs"
                    data-host-event={ev.id}
                  >
                    <option value="" disabled>
                      Choose a host
                    </option>
                    {candidates.map((c) => (
                      <option key={c.id} value={c.id}>
                        {candidateLabel(c, meId)}
                      </option>
                    ))}
                  </Select>
                </Field>
              ))}
              <p aria-live="polite" className="text-text-muted">
                {missing === 0 ? "" : `Choose a new host for ${missing === 1 ? "1 more event" : `${missing} more events`}.`}
              </p>
            </>
          )}
        </div>
      )}
      {error && (
        <div ref={alertBox} tabIndex={-1}>
          <FormError>{error}</FormError>
        </div>
      )}
      <div className="flex gap-3">
        <Button variant="danger" onClick={remove} disabled={busy || !ready || (deleting && !hostsDone)}>
          {busy ? "Working…" : deleting ? "Delete account" : "Disable account"}
        </Button>
        <Button variant="secondary" onClick={() => onDone(false)}>
          Cancel
        </Button>
      </div>
    </div>
  );
}

function nextDate(ev: SoleHostedEvent): string {
  return ev.nextStartsAt ? `Next date: ${formatEventDay(ev.nextStartsAt)}` : "No upcoming dates";
}
