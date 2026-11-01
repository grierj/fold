import { useEffect, useId, useRef, useState } from "react";
import { Messages } from "../../messages-api";
import type { AudienceRecipient, AudienceSummary, ComposeOptions } from "../../types";
import { errorText } from "../../lib/errors";
import { audienceKey, ROLE_CHOICES, STATUS_CHOICES, pushLine, summaryLine, teenLine, toAudience, type AudienceChoice } from "../../lib/messages";
import { formatWhen } from "../../lib/when";
import { SmartphoneIcon } from "../icons";
import { Badge } from "../ui/Badge";
import { Chip } from "../ui/Chip";
import { Field, Select } from "../ui/Field";

// How long the picker waits after a change before counting again.
export const COUNT_DELAY_MS = 300;
// GET /messages/compose lists each event's next 10 dates (maxComposeDates).
const LISTED_DATES = 10;

// The composer's "Who gets it" (M3.4c, PLAN 7.3): only what GET
// /messages/compose offers, Going/Waitlisted/Maybe chips for an event's
// people and Members/Leaders chips for a group, and a live count after
// consent filtering with a teen count and Teen badges (DEC-20). Names and
// counts only: the API never sends an address (DEC-31). A count that comes
// back after a newer change is dropped. push: "Push notification too" is
// on (and the church allows it), so the count says who also gets a push
// and the list marks them with a phone and words (M3.13d2).
export function AudiencePicker({ options, choice, onChange, error, id, push = false }: {
  options: ComposeOptions;
  choice: AudienceChoice;
  onChange: (c: AudienceChoice) => void;
  error?: string;
  id: string;
  push?: boolean;
}) {
  const chipsLabel = useId();
  const [kind, raw] = choice.target.split(":");
  const event = kind === "event" ? options.events.find((e) => e.id === Number(raw)) : undefined;
  const audience = toAudience(choice);
  const key = audienceKey(audience);
  const [summary, setSummary] = useState<{ key: string; summary: AudienceSummary } | null>(null);
  const [countError, setCountError] = useState<{ key: string; text: string } | null>(null);
  const latest = useRef("");

  useEffect(() => {
    latest.current = key;
    if (!audience) return;
    const timer = setTimeout(() => {
      Messages.audiencePreview(audience)
        .then(({ summary: s }) => { if (latest.current === key) setSummary({ key, summary: s }); })
        .catch((e) => { if (latest.current === key) setCountError({ key, text: errorText(e, "We couldn't count who gets this.") }); });
    }, COUNT_DELAY_MS);
    return () => clearTimeout(timer);
    // audience is derived from key, so key alone says when to count again.
  }, [key]);

  const listed = (target: string) => {
    const [k, v] = target.split(":");
    const n = Number(v);
    return target === "" || (target === "all" && options.canMessageEveryone) ||
      (k === "group" && options.groups.some((g) => g.id === n)) || (k === "event" && options.events.some((e) => e.id === n));
  };
  const datesListed = choice.dates === "all" || !!event?.dates.some((d) => String(d.occurrenceId) === choice.dates);
  const current = summary?.key === key ? summary.summary : null;
  const failed = countError?.key === key ? countError.text : "";
  const isGroup = kind === "group";
  const chips = isGroup ? ROLE_CHOICES : STATUS_CHOICES;
  const picked: string[] = isGroup ? choice.roles : choice.statuses;
  const toggle = (value: string) => {
    const next = picked.includes(value) ? picked.filter((v) => v !== value) : [...picked, value];
    onChange(isGroup ? { ...choice, roles: next as AudienceChoice["roles"] } : { ...choice, statuses: next as AudienceChoice["statuses"] });
  };

  return (
    <div className="space-y-4">
      <Field id={id} label="Send to" hint="Your groups and the events you lead or host." error={error}>
        <Select value={choice.target} onChange={(e) => onChange({ ...choice, target: e.target.value, dates: "all" })}>
          <option value="">Choose a group or event</option>
          {!listed(choice.target) && (
            <option value={choice.target}>{kind === "group" ? "The group you chose earlier" : kind === "event" ? "The event you chose earlier" : "The date you chose earlier"}</option>
          )}
          {options.groups.length > 0 && (
            <optgroup label="Groups">
              {options.groups.map((g) => <option key={g.id} value={`group:${g.id}`}>{g.name}</option>)}
            </optgroup>
          )}
          {options.events.length > 0 && (
            <optgroup label="Events">
              {options.events.map((e) => <option key={e.id} value={`event:${e.id}`}>{e.title}</option>)}
            </optgroup>
          )}
          {options.canMessageEveryone && <option value="all">Everyone at church</option>}
        </Select>
      </Field>

      {event && (
        <Field label="Which dates" hint={[
          event.churchWide ? "A church-wide event." : event.groupName ? `An event of ${event.groupName}.` : "",
          event.dates.length >= LISTED_DATES ? `Showing the next ${LISTED_DATES} dates.` : "",
        ].filter(Boolean).join(" ") || undefined}>
          <Select value={choice.dates} onChange={(e) => onChange({ ...choice, dates: e.target.value })}>
            <option value="all">All upcoming dates</option>
            {!datesListed && <option value={choice.dates}>The date you chose earlier</option>}
            {event.dates.map((d) => <option key={d.occurrenceId} value={String(d.occurrenceId)}>{formatWhen(d.startsAt)}</option>)}
          </Select>
        </Field>
      )}

      {choice.target !== "" && choice.target !== "all" && (
        <div role="group" aria-labelledby={chipsLabel}>
          <p id={chipsLabel} className="label">{isGroup ? "Who in the group" : "Who's signed up"}</p>
          <p className="field-hint">{picked.length ? "Choose one or more." : "Choose at least one."}</p>
          <div className="flex flex-wrap gap-2">
            {chips.map((c) => <Chip key={c.id} selected={picked.includes(c.id)} onClick={() => toggle(c.id)}>{c.label}</Chip>)}
          </div>
        </div>
      )}

      {/* The live region holds only the result; while counting it is busy,
          so a screen reader hears the count, not "Counting". */}
      {audience && !current && !failed && <p className="text-sm text-text-muted">Counting who gets it…</p>}
      <div role="status" aria-live="polite" aria-busy={!!audience && !current && !failed} className="text-sm">
        {failed && <p className="text-text-muted">{failed}</p>}
        {current && <>
          <p className="font-medium text-text">{summaryLine(current)}</p>
          {push && pushLine(current) && <p className="text-text-muted">{pushLine(current)}</p>}
          {teenLine(current) && <p className="text-text-muted">{teenLine(current)}</p>}
        </>}
      </div>
      {current && current.recipients.length > 0 && (
        <details>
          <summary className="inline-flex min-h-11 cursor-pointer items-center text-sm font-semibold text-primary">See who's on the list</summary>
          <ul className="mt-2 divide-y divide-border rounded-md border border-border bg-surface text-sm">
            {current.recipients.map((r, i) => <RecipientRow key={`${r.name}-${i}`} r={r} push={push} />)}
            {current.more && <li className="px-3 py-2 text-text-muted">And {current.people - current.recipients.length} more</li>}
          </ul>
        </details>
      )}
    </div>
  );
}

// One person on the list: a name, a Teen badge (DEC-20), and in words
// whether they won't get it, with the reason when the API gives one
// (admins). With push on, a phone and words mark who gets a push
// notification, never the colour or the glyph alone: "Push too", or for
// someone the email won't reach, "Push only" (push follows its own
// consent, DEC-122).
function RecipientRow({ r, push }: { r: AudienceRecipient; push: boolean }) {
  const pushed = push && !!r.push;
  const what = pushed ? "the email" : "this";
  return (
    <li className="flex flex-wrap items-center gap-2 px-3 py-2">
      <span className={r.skipped ? "text-text-muted" : "text-text"}>{r.name}</span>
      {r.teen && <Badge>Teen</Badge>}
      {pushed && (
        <span className="inline-flex items-center gap-1 text-text-muted">
          <SmartphoneIcon className="h-4 w-4" />{r.skipped ? "Push only" : "Push too"}
        </span>
      )}
      {r.skipped && <span className="text-text-muted">· {r.skipped === "won't get this" ? `Won't get ${what}` : `Won't get ${what}: ${r.skipped}`}</span>}
    </li>
  );
}
