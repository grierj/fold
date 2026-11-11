import { people } from "./plural";
import type { AudienceRole, AudienceStatus, AudienceSummary, ComposeOptions, HistoryStatus, Message, MessageAudience, MessageHistoryPush, MessagePushCounts, MessagePushWait, MessageRecipientCounts, MessageRecipients } from "../types";
import { formatShortDay, formatWhenIn } from "./when";
import { registrationStatusLabel } from "./status";

// The composer's words and audience shapes (M3.4c, PLAN 7.2 and 7.3).

// The picker's state. target is "group:<id>", "event:<id>", "occurrence:<id>"
// (a date whose event isn't listed), "all", or "" before a choice; dates is
// "all" for every upcoming date or an occurrence id.
export interface AudienceChoice {
  target: string;
  dates: string;
  statuses: AudienceStatus[];
  roles: AudienceRole[];
}

export const STATUS_CHOICES: { id: AudienceStatus; label: string }[] = [
  { id: "going", label: registrationStatusLabel("going") },
  { id: "waitlisted", label: registrationStatusLabel("waitlisted") },
  { id: "maybe", label: registrationStatusLabel("maybe") },
];
export const ROLE_CHOICES: { id: AudienceRole; label: string }[] = [
  { id: "member", label: "Members" },
  { id: "leader", label: "Leaders" },
];
const DEFAULT_STATUSES: AudienceStatus[] = ["going", "waitlisted"];
const DEFAULT_ROLES: AudienceRole[] = ["member", "leader"];

export const emptyChoice = (): AudienceChoice => ({ target: "", dates: "all", statuses: DEFAULT_STATUSES, roles: DEFAULT_ROLES });

export function toAudience(c: AudienceChoice): MessageAudience | null {
  const [kind, raw] = c.target.split(":");
  const id = Number(raw);
  if (c.target === "all") return { type: "all_members" };
  if (!id) return null;
  if (kind === "group") return c.roles.length ? { type: "group", groupId: id, roles: c.roles } : null;
  if (!c.statuses.length) return null;
  if (kind === "occurrence") return { type: "occurrence", occurrenceId: id, statuses: c.statuses };
  if (kind !== "event") return null;
  return c.dates === "all"
    ? { type: "event_series", eventId: id, statuses: c.statuses }
    : { type: "occurrence", occurrenceId: Number(c.dates), statuses: c.statuses };
}

// fromAudience is the picker's state for a saved audience. A date is shown
// under its event when the event is listed.
export function fromAudience(a: MessageAudience, options: ComposeOptions): AudienceChoice {
  const c: AudienceChoice = { ...emptyChoice(), statuses: a.statuses?.length ? a.statuses : DEFAULT_STATUSES, roles: a.roles?.length ? a.roles : DEFAULT_ROLES };
  switch (a.type) {
    case "all_members": return { ...c, target: "all" };
    case "group": return { ...c, target: `group:${a.groupId}` };
    case "event_series": return { ...c, target: `event:${a.eventId}` };
    case "occurrence": {
      const ev = eventOfDate(options, a.occurrenceId ?? 0);
      return ev ? { ...c, target: `event:${ev.id}`, dates: String(a.occurrenceId) } : { ...c, target: `occurrence:${a.occurrenceId}` };
    }
    default: return c;
  }
}

export function prefillChoice(params: URLSearchParams, options: ComposeOptions): AudienceChoice {
  const group = Number(params.get("group")), event = Number(params.get("event")), occurrence = Number(params.get("occurrence"));
  if (group) return fromAudience({ type: "group", groupId: group }, options);
  if (event) return fromAudience({ type: "event_series", eventId: event }, options);
  if (occurrence) return fromAudience({ type: "occurrence", occurrenceId: occurrence }, options);
  return emptyChoice();
}

function eventOfDate(options: ComposeOptions, occurrenceId: number) {
  return options.events.find((e) => e.dates.some((d) => d.occurrenceId === occurrenceId));
}

// One event's people: event merge fields work (the server's
// message_merge_event rule).
export const isOneEvent = (a: MessageAudience | null) => a?.type === "occurrence" || a?.type === "event_series";

// audienceKey identifies an audience for comparing and for ignoring stale
// counts.
export const audienceKey = (a: MessageAudience | null) => (a ? JSON.stringify(a) : "");

// A native select can't wrap its chosen label, so a long group or event name
// must be cut to fit the roughly 324px of text room the Send to select has at
// 390px (web/e2e/ui-measure.mjs measures the real glyphs; the layout gate's
// clipped-select check, PR #51). The cut is in the middle, since the end is
// often what tells alike names apart ("(North Campus)" and "(South Campus)").
// The full name stays in the title, the accessible description and a line
// under the select.
export const AUDIENCE_LABEL_MAX = 32;
// The text room in ems at the select's 16px font: 20em is 320px.
/** @public tested directly: the width a shortened Send to label is held to. */
export const AUDIENCE_LABEL_WIDTH_EM = 20;

// Widths are whole hundredths of an em so sums are exact. A grapheme's width
// is an estimate. For Latin letters, digits and punctuation each class (W, M,
// m, w, @ and %, other capitals, other lowercase, digits, narrow letters,
// narrow punctuation) is sized at or above the widest advance in it in Inter
// (the shipped font, measured) and in DejaVu Sans, the widest common system
// sans. Other scripts get a flat guess. No estimate is exact (a wider
// fallback font could still overflow), which is why the layout gate measures
// the real select.
const WIDTH_BUDGET = AUDIENCE_LABEL_WIDTH_EM * 100;
const ELLIPSIS = "…";
// The tail (the end of the name) may take half of what the label has room
// for, so it can't be starved and the head still gets the rest.
const TAIL_SHARE = 0.5;

const widthsOf = (chars: string, width: number): [string, number][] => [...chars].map((c) => [c, width]);
const GLYPH_WIDTHS = new Map<string, number>([
  ...widthsOf("W@%—…ÆŒæœ‰", 110),
  ...widthsOf("mM", 100),
  ...widthsOf("w", 85),
  ...widthsOf("ijlftr", 55),
  ...widthsOf(".,:;!'|-()[]/\\\"`", 50),
  ...widthsOf("‘’“”", 65),
  ...widthsOf("0123456789", 70),
]);

// Emoji, flags, East Asian wide and fullwidth forms are about two ems each.
const WIDE_GLYPH = /[\p{Extended_Pictographic}\p{Emoji_Modifier}\p{Regional_Indicator}\u1100-\u115f\u2e80-\ua4cf\ua960-\ua97f\uac00-\ud7a3\uf900-\ufaff\ufe30-\ufe6f\uff00-\uff60\uffe0-\uffe6\u{20000}-\u{3fffd}]/u;

function glyphWidth(cp: string): number {
  if (/[\p{Mn}\p{Me}\p{Cf}]/u.test(cp)) return 0;
  if (WIDE_GLYPH.test(cp)) return 200;
  if (cp === " " || cp === "\u00a0" || /\p{Cc}/u.test(cp)) return 40;
  if (/\s/u.test(cp)) return 100;
  const known = GLYPH_WIDTHS.get(cp);
  if (known !== undefined) return known;
  if (/\p{Script=Latin}/u.test(cp)) return /\p{Lu}/u.test(cp) ? 80 : /\p{Ll}/u.test(cp) ? 66 : 90;
  return /\p{L}/u.test(cp) ? 100 : 90;
}

// Emoji sequences and keycaps usually occupy one wide glyph.
function graphemeWidth(g: string): number {
  if (WIDE_GLYPH.test(g)) return 200;
  let sum = 0;
  for (const cp of g) sum += glyphWidth(cp);
  return g.includes("\u20e3") ? Math.max(sum, 200) : sum;
}

/** @public tested directly: the estimate the Send to label is held to, in ems. */
export const labelWidthEm = (text: string) => graphemes(text).reduce((sum, g) => sum + graphemeWidth(g), 0) / 100;

type GraphemeSegmenter = { segment(text: string): Iterable<{ segment: string }> };

// Intl.Segmenter keeps an accent with its letter and a ZWJ emoji whole. Where
// it is missing, code points join to the one before them by the same rules for
// marks, joiners, skin tones and flag pairs.
function makeSegmenter(): GraphemeSegmenter | null {
  const Segmenter = (Intl as unknown as { Segmenter?: new (locale: undefined, o: { granularity: "grapheme" }) => GraphemeSegmenter }).Segmenter;
  return typeof Segmenter === "function" ? new Segmenter(undefined, { granularity: "grapheme" }) : null;
}
const segmenter = makeSegmenter();

function codePointClusters(text: string): string[] {
  const out: string[] = [];
  for (const cp of text) {
    const prev = out[out.length - 1];
    const joins = prev !== undefined && (/[\p{M}\p{Cf}\p{Emoji_Modifier}]/u.test(cp) || prev.endsWith("\u200d") ||
      (/\p{Regional_Indicator}/u.test(cp) && /^\p{Regional_Indicator}$/u.test(prev)));
    if (joins) out[out.length - 1] = prev + cp;
    else out.push(cp);
  }
  return out;
}

const graphemes = (text: string): string[] =>
  segmenter ? Array.from(segmenter.segment(text), (s) => s.segment) : codePointClusters(text);

// What a cut leaves dangling at its edge: spaces and ,;:- at both, an opening
// bracket after the head and a closing one before the tail.
const HEAD_EDGE = new Set([",", ";", ":", "-", "(", "[", "{"]);
const TAIL_EDGE = new Set([",", ";", ":", "-", ")", "]", "}"]);

const isSpace = (x: string) => /^\s+$/u.test(x);

// cutAround picks where a name is cut for a label of at most max graphemes and
// budget width: the head is g[0, head) and the tail is g[tail, end), around
// one ellipsis.
function cutAround(g: string[], w: number[], max: number, budget: number): { head: number; tail: number } {
  const space = g.map(isSpace);
  const roomCount = max - 1;
  const roomWidth = budget - graphemeWidth(ELLIPSIS);

  // The tail first: the longest whole-word suffix within its share, or when
  // the last word alone is longer than that, a suffix of its graphemes.
  const tailCount = Math.floor(roomCount * TAIL_SHARE);
  const tailWidth = Math.floor(roomWidth * TAIL_SHARE);
  let fit = g.length;
  for (let n = 0, sum = 0; fit > 0 && n < tailCount && sum + w[fit - 1] <= tailWidth; n++) sum += w[--fit];
  let tail = fit;
  for (let i = fit; i < g.length; i++) {
    if (!space[i] && (i === 0 || space[i - 1])) { tail = i; break; }
  }
  while (tail < g.length && (space[tail] || TAIL_EDGE.has(g[tail]))) tail++;

  // The head fills what is left: the longest whole-word prefix, or when the
  // first word is too long, a prefix of its graphemes.
  const headCount = roomCount - (g.length - tail);
  const headWidth = roomWidth - w.slice(tail).reduce((a, b) => a + b, 0);
  let reach = 0;
  for (let n = 0, sum = 0; reach < tail && n < headCount && sum + w[reach] <= headWidth; n++) sum += w[reach++];
  let head = reach;
  for (let e = reach; e > 0; e--) {
    if (!space[e - 1] && (e === g.length || space[e])) { head = e; break; }
  }
  while (head > 0 && (space[head - 1] || HEAD_EDGE.has(g[head - 1]))) head--;
  return { head, tail };
}

// shortLabel is a name as the Send to select shows it: unchanged when it fits
// max graphemes and the width budget, else the start and the end of the name
// at word boundaries around one ellipsis, e.g. "Saint Brigid's…(North
// Campus)". A name with no word boundary to cut at is cut by graphemes. Two
// names alike at both ends can share a label; audienceLabels tells those apart.
export function shortLabel(text: string, max = AUDIENCE_LABEL_MAX): { label: string; shortened: boolean } {
  const g = graphemes(text);
  const w = g.map(graphemeWidth);
  if (g.length <= max && w.reduce((a, b) => a + b, 0) <= WIDTH_BUDGET) return { label: text, shortened: false };
  const { head, tail } = cutAround(g, w, max, WIDTH_BUDGET);
  return { label: g.slice(0, head).join("") + ELLIPSIS + g.slice(tail).join(""), shortened: true };
}

// The first word that tells names apart is shown in the cut, up to this long.
const MID_MAX = 12;
const MID_WIDTH = 800;
// A cut needs this much room left for its two ends to be worth showing.
const MIN_ENDS = 8;

// withMiddle shows each of texts as its two ends around the word where they
// first differ ("Saint…Wednesday…Campus)"), or null when that can't be done
// for all of them, or when the labels still would not be different.
function withMiddle(texts: string[], max: number): Map<string, string> | null {
  const parsed = texts.map((text) => {
    const g = graphemes(text);
    const words: { start: number; end: number; text: string }[] = [];
    g.forEach((x, i) => {
      if (isSpace(x)) return;
      const last = words[words.length - 1];
      if (i > 0 && last && last.end === i) { last.end = i + 1; last.text += x; } else words.push({ start: i, end: i + 1, text: x });
    });
    return { text, g, w: g.map(graphemeWidth), words };
  });
  const shared = Math.min(...parsed.map((p) => p.words.length));
  let at = 0;
  while (at < shared && parsed.every((p) => p.words[at].text === parsed[0].words[at].text)) at++;
  if (at >= shared) return null;

  const out = new Map<string, string>();
  for (const p of parsed) {
    const word = p.words[at];
    const mid: string[] = [];
    let midWidth = 0;
    for (const x of graphemes(word.text)) {
      if (mid.length >= MID_MAX || midWidth + graphemeWidth(x) > MID_WIDTH) break;
      mid.push(x);
      midWidth += graphemeWidth(x);
    }
    const room = max - 1 - mid.length;
    if (room - 1 < MIN_ENDS) return null;
    const { head, tail } = cutAround(p.g, p.w, room, WIDTH_BUDGET - graphemeWidth(ELLIPSIS) - midWidth);
    // The word must sit in what the cut leaves out, or the label would repeat or reorder text.
    if (word.start < head || word.end > tail) return null;
    out.set(p.text, p.g.slice(0, head).join("") + ELLIPSIS + mid.join("") + ELLIPSIS + p.g.slice(tail).join(""));
  }
  return new Set(out.values()).size === texts.length ? out : null;
}

// audienceLabels is shortLabel for every name in one select, in order. Names
// that are cut to the same label but differ in the middle get the word where
// they first differ shown between two ellipses, so an unchosen option can be
// told from its sibling. The same name twice stays the same: two groups
// called alike are the select's own doing, and invented text would mislead.
// Where no such word fits, the labels stay alike and the line under the select
// names the one chosen.
export function audienceLabels(names: string[], max = AUDIENCE_LABEL_MAX): { label: string; shortened: boolean }[] {
  const out = names.map((n) => shortLabel(n, max));
  const alike = new Map<string, number[]>();
  out.forEach((o, i) => { if (o.shortened) alike.set(o.label, [...(alike.get(o.label) ?? []), i]); });
  for (const at of alike.values()) {
    const texts = [...new Set(at.map((i) => names[i]))];
    const labels = texts.length > 1 ? withMiddle(texts, max) : null;
    if (labels) for (const i of at) out[i] = { label: labels.get(names[i])!, shortened: true };
  }
  return out;
}

const statusWords = (s: AudienceStatus[] | undefined) =>
  STATUS_CHOICES.filter((x) => (s ?? DEFAULT_STATUSES).includes(x.id)).map((x) => x.label).join(", ");
const roleWords = (r: AudienceRole[] | undefined) =>
  ROLE_CHOICES.filter((x) => (r ?? DEFAULT_ROLES).includes(x.id)).map((x) => x.label).join(", ");

// audienceLabel says who a message goes to in a line: "Room 201 class
// (Members, Leaders)", "Book night, all upcoming dates (Going, Waitlisted)".
export function audienceLabel(a: MessageAudience, options: ComposeOptions | null): string {
  switch (a.type) {
    case "all_members": return "Everyone at church";
    case "group": {
      const g = options?.groups.find((x) => x.id === a.groupId);
      return `${g ? g.name : "A group that's no longer listed"} (${roleWords(a.roles)})`;
    }
    case "event_series": {
      const ev = options?.events.find((x) => x.id === a.eventId);
      return `${ev ? ev.title : "An event that's no longer listed"}, all upcoming dates (${statusWords(a.statuses)})`;
    }
    case "occurrence": {
      const ev = options ? eventOfDate(options, a.occurrenceId ?? 0) : undefined;
      const d = ev?.dates.find((x) => x.occurrenceId === a.occurrenceId);
      return `${ev && d ? `${ev.title}, ${formatShortDay(d.startsAt)}` : "One date of an event"} (${statusWords(a.statuses)})`;
    }
    case "events": return `${a.eventIds?.length ?? 0} events (${statusWords(a.statuses)})`;
    case "users": return `${a.userIds?.length ?? 0} chosen people`;
  }
}

// Reasons that need a verb to read after a count ("3 have no email
// address"). Any other reason the API sends ("turned off messages from
// leaders", "marked our email as spam") reads as it comes.
const REASONS: Record<string, [one: string, many: string]> = {
  "no email address": ["has no email address", "have no email address"],
  "email not working": ["has an email address that isn't working", "have email addresses that aren't working"],
  "not active": ["isn't active", "aren't active"],
  "no longer on the list": ["is no longer on the list", "are no longer on the list"],
  "guest, hasn't asked for updates": ["is a guest who hasn't asked for updates", "are guests who haven't asked for updates"],
};

// summaryLine is the live count: "142 people; 9 won't get it: 6 turned off
// messages from leaders, 3 have no email address".
export function summaryLine(s: AudienceSummary): string {
  if (s.people === 0) return "No one is on this list yet.";
  const skipped = s.people - s.eligible;
  if (skipped === 0) return `${people(s.people)}; everyone on the list will get it.`;
  return `${people(s.people)}; ${skipped} won't get it: ${reasonList(s.skipped)}`;
}

// reasonList is "6 turned off messages from leaders, 3 have no email
// address", the biggest first.
function reasonList(by: Record<string, number>): string {
  return Object.entries(by)
    .filter(([, n]) => n > 0)
    .sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0]))
    .map(([reason, n]) => `${n} ${REASONS[reason]?.[n === 1 ? 0 : 1] ?? reason}`)
    .join(", ");
}

// historyLine is a sent message's count (M3.5b): "Sent to 132 people · 3
// bounced · 9 skipped (6 turned off messages from leaders, 3 have no email
// address)", or while it goes out, "Sending… 40 of 132 so far" ("Sending
// again…" for a Try again: it was sent before).
export function historyLine(h: Pick<MessageRecipients, "counts" | "skippedBy">, m: Pick<Message, "status" | "sentAt">): string {
  const c = h.counts;
  const done = c.sent + c.delivered + c.bounced + c.complained + c.failed;
  if (m.status === "sending") return `${m.sentAt ? "Sending again" : "Sending"}… ${done} of ${done + c.queued} so far`;
  // Someone who marked it as spam got it.
  const reached = c.sent + c.delivered + c.complained;
  const parts: string[] = [];
  // No email reached anyone, though someone was on the list: said plainly,
  // since its pushes may still have gone (they follow push consent,
  // DEC-122), never "Sent to 0 people".
  if (reached) parts.push(`Sent to ${people(reached)}`);
  else if (c.bounced || c.failed || c.skipped) parts.push("No emails went out");
  if (c.bounced) parts.push(`${c.bounced} bounced`);
  if (c.failed) parts.push(`${c.failed} didn't get it`);
  if (c.skipped) parts.push(`${c.skipped} skipped (${reasonList(h.skippedBy)})`);
  return parts.length ? parts.join(" · ") : "No one was on the list.";
}

const guardianCopies = (n: number) => `${n} guardian ${n === 1 ? "copy" : "copies"}`;

// guardianLine counts a settled message's guardian copies (DEC-19): "2
// guardian copies sent · 1 didn't go out", or "" for none. A leader sees
// only this count, never the guardians' names (DEC-132).
export function guardianLine(c: Pick<MessageRecipientCounts, "guardianCopies" | "guardianCopiesMissed">): string {
  const sent = c.guardianCopies - c.guardianCopiesMissed;
  const parts: string[] = [];
  if (sent) parts.push(`${guardianCopies(sent)} sent`);
  if (c.guardianCopiesMissed) parts.push(`${sent ? c.guardianCopiesMissed : guardianCopies(c.guardianCopiesMissed)} didn't go out`);
  return parts.join(" · ");
}

// failedLine heads a partly sent message's banner: "1 person didn't get
// this", or when only guardian copies failed, "1 guardian copy didn't go
// out", matching the counts below it.
export const failedLine = (failed: number, copies: number) =>
  failed > 0 || copies === 0 ? `${people(failed)} didn't get this` : `${guardianCopies(copies)} didn't go out`;

export const HISTORY_WORDS: Record<HistoryStatus, string> = {
  sent: "Sent",
  delivered: "Delivered",
  missed: "Didn't get it",
  waiting: "Waiting",
};

// retryQuestion is the confirm before "Try again" (DEC-131), in the
// banner's words: people, and guardian copies apart.
export function retryQuestion(failed: number, copies = 0): string {
  if (failed > 0 && copies > 0) return `Send again to the ${people(failed)} and ${guardianCopies(copies)} it didn't reach?`;
  if (failed > 0) return `Send again to the ${people(failed)} it didn't reach?`;
  if (copies > 0) return `Send the ${guardianCopies(copies)} that didn't go out again?`;
  return "Try sending this message again?";
}

// historyTeenLine counts the teens a sent message was for (DEC-20), or "".
export function historyTeenLine(teens: number): string {
  if (teens === 0) return "";
  return teens === 1 ? "1 of the people it was for is a teen." : `${teens} of the people it was for are teens.`;
}

// teenLine counts the teens who will get it (DEC-20), or "" for none.
export function teenLine(s: AudienceSummary): string {
  if (s.teens === 0) return "";
  return s.teens === 1 ? "1 of them is a teen." : `${s.teens} of them are teens.`;
}

// pushLine is the composer's "38 also get a push notification." (PLAN
// 7.3), or "" when nobody would. People with no device aren't skipped:
// they still get the email.
export function pushLine(s: AudienceSummary): string {
  if (!s.push) return "";
  return `${s.push} also ${s.push === 1 ? "gets" : "get"} a push notification.`;
}

// A push row's reason (the pipeline's words) as the history says it.
const PUSH_REASONS: Record<string, [one: string, many: string]> = {
  "device gone": ["device gone", "devices gone"],
  "hourly limit": ["over the hourly limit", "over the hourly limit"],
  "push turned off for messages from leaders": ["turned notifications off for messages from leaders", "turned notifications off for messages from leaders"],
  "push not turned on": ["turned notifications off", "turned notifications off"],
  "push turned off by the church": ["notifications turned off by the church", "notifications turned off by the church"],
  "recipient not active": ["not active", "not active"],
  "no longer current": ["not sent: the message stopped", "not sent: the message stopped"],
  "the push service refused it": ["refused by the push service", "refused by the push service"],
  "the push service couldn't be reached": ["the push service couldn't be reached", "the push service couldn't be reached"],
  "the notification was too large to push": ["too large to push", "too large to push"],
  "didn't go out": ["didn't go out", "didn't go out"],
  // A push reminder's (M3.13d3).
  "push turned off for event updates": ["turned notifications off for event updates", "turned notifications off for event updates"],
  "outside quiet hours": ["skipped: it would have come during quiet hours", "skipped: it would have come during quiet hours"],
  "date cancelled or moved": ["not sent: the date was cancelled or moved", "not sent: the date was cancelled or moved"],
  "push reminder turned off": ["not sent: push reminders were turned off", "not sent: push reminders were turned off"],
  "no longer going": ["no longer going", "no longer going"],
  "can't see this date anymore": ["can't see this date anymore", "can't see this date anymore"],
};

// pushBadge is one person's push in their history row, in words (status
// is never colour alone): "Push sent", "Push waiting until 8:00 AM", or
// "Push didn't go out", with the reason for an admin.
export function pushBadge(p: MessageHistoryPush): string {
  switch (p.status) {
    case "sent":
    case "delivered":
      return "Push sent";
    case "waiting":
      return p.until ? `Push waiting until ${p.until}` : "Push waiting";
  }
  return p.reason ? `Push didn't go out: ${PUSH_REASONS[p.reason]?.[0] ?? p.reason}` : "Push didn't go out";
}

// pushHistoryLine counts a message's companion pushes (M3.13d2): "38
// pushed · 2 waiting until 8:00 AM · 1 device gone", or "" when it had
// none. The email counts above it never include these.
export function pushHistoryLine(c: MessagePushCounts | undefined): string {
  if (!c) return "";
  const parts: string[] = [];
  if (c.sent) parts.push(`${c.sent} pushed`);
  const waiting = (w: MessagePushWait) => (w.until ? `${w.n} waiting until ${w.until}` : `${w.n} waiting`);
  for (const w of c.waiting) parts.push(waiting(w));
  Object.entries(c.missed)
    .filter(([, n]) => n > 0)
    .sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0]))
    .forEach(([reason, n]) => parts.push(`${n} ${PUSH_REASONS[reason]?.[n === 1 ? 0 : 1] ?? reason}`));
  return parts.length ? `Push notifications: ${parts.join(" · ")}` : "";
}

// Times a message goes out read on the church's clock (the schedule is
// picked in it), with its zone named when the viewer's clock differs.

// confirmLine is the question before sending: "Send to 133 people now?"
export function confirmLine(eligible: number, at: string | null, zone: string): string {
  if (!at) return `Send to ${people(eligible)} now?`;
  const when = formatWhenIn(at, zone);
  return `Send to ${people(eligible)} ${/^(Today|Tomorrow) /.test(when) ? when[0].toLowerCase() + when.slice(1) : `on ${when}`}?`;
}

// statusLabel names a message's state in words, never by color alone. A
// scheduled time already reached is "Waiting to send" until the sender
// (M3.5) picks it up; a draft that was never scheduled and then cancelled
// was discarded. A send that reached some people and failed for others is
// "Partly sent". A push reminder (M3.13d3, channel push) is sent from the
// start and its sentAt is its first push: "Pushed today at 5:00 PM", else
// "Waiting to go out" while some of its rows are still to go
// (pushWaiting), and "Nobody got it" once none are.
export function statusLabel(m: Pick<Message, "status" | "scheduledAt" | "sentAt"> & { failedCount?: number; channel?: Message["channel"]; pushWaiting?: number }, zone: string, now = new Date()): string {
  if (m.channel === "push" && m.status === "sent") {
    if (m.sentAt) return `Pushed ${formatWhenIn(m.sentAt, zone, now)}`;
    return m.pushWaiting ? "Waiting to go out" : "Nobody got it";
  }
  switch (m.status) {
    case "draft": return "Draft";
    case "scheduled":
      if (!m.scheduledAt || new Date(m.scheduledAt).getTime() <= now.getTime()) return "Waiting to send";
      return `Scheduled for ${formatWhenIn(m.scheduledAt, zone, now)}`;
    case "sending": return "Sending";
    case "sent": {
      const word = m.failedCount ? "Partly sent" : "Sent";
      return m.sentAt ? `${word} ${formatWhenIn(m.sentAt, zone, now)}` : word;
    }
    case "failed": return "Failed";
    case "cancelled": return m.scheduledAt ? "Cancelled" : "Discarded";
  }
}

// The merge fields the editor's Insert field menu offers (PLAN 7.3). Event
// fields need a message to one event's people.
export const MERGE_FIELDS: { field: string; label: string; event: boolean }[] = [
  { field: "first_name", label: "First name", event: false },
  { field: "event_title", label: "Event title", event: true },
  { field: "event_time", label: "Event date and time", event: true },
  { field: "event_link", label: "Event page link", event: true },
  { field: "manage_link", label: "Link to their sign-up", event: true },
];
