# Competitor teardown: social-coordination products vs Fold's current surfaces

Reconciled 2026-11-03 against Fold master `dc15921` (PR #268) and the
approved Audit 2026-11 briefs in
[`docs/plan/briefs/audit-2026-11/`](../briefs/audit-2026-11/00-index.md).
This is a research document. It owns no product policy: PLAN.md and the
DEC log win on any conflict, and DEC-145 (surface freeze till live)
governs every "net-new" item below — they are recorded here so they are
not lost, not proposed for the current waves.

Sources: a teardown matrix built 2026-11-03 scoring six products across
Fold's twelve current surfaces (interactive version held by Grier /
Winston outside the repo), plus the products' public documentation.
Competitor facts are as of November 2026 and will drift; re-verify
before citing one in a brief.

## The set, and why

| Product | Why it is in the set |
|---|---|
| Meetup | Baseline. Fold's primary UI reference per SUMMARY.md. |
| Heylo | Closest modern Meetup analogue: branded group space, recurring events, topic + event chat, member directory, waitlists, payments. Built for run clubs / activity groups — recurring, real-world, leader-run. |
| Church Center (Planning Center Groups) | The church incumbent for group finding and leader admin: group finder with filters, request-to-join, resources, attendance. ChurchTrac informs Fold's admin screens; Church Center is its closest UX comparable. |
| BAND | Leader-coordination container for non-technical leaders (coaches, school, faith groups): board + calendar + RSVP + polls + attendance + sign-up sheets in one group home, plus a "Read by" receipt on announcements. |
| Luma (lu.ma) | Best-in-class single-event page and host tooling: subscribable calendars instead of groups, approval-required registration, QR check-in, guest-list tooling. |
| Partiful | Lowest-friction guest flow: RSVP from a link with a phone number, no account or app, text blasts and automatic reminders, capacity + automatic waitlist. |

Explicitly out of the set: community/course platforms (Circle, Mighty
Networks, Skool — feed/course-first, wrong shape) and stranger-matching
products (Timeleft, 222, Bumble BFF — a discovery marketplace, which
Fold is not; see R8 below).

## Verdict in one paragraph

Most of what the consumer products do well, Fold either already does or
has already briefed out of Audit 2026-11. The two largest teardown
themes — "a leader should be able to run the weekly rhythm from one
place" and "a newcomer should find and join the right group with almost
no friction" — map onto approved briefs (R10, R14, R15) rather than onto
missing features. Fold is *ahead* of the whole set on four surfaces
(per-date visibility, contact privacy, series RSVP, rooms), and the
audit has deliberately frozen two of those (DEC-141). The genuinely new
material is small, chat-shaped, and blocked by the DEC-145 surface
freeze until Fold is live. Details and dispositions follow.

## Cross-check against Audit 2026-11

Dispositions: **Briefed** = already an approved audit brief; do not
re-brief. **Frozen** = audit/DEC explicitly holds this surface.
**Named gap** = the audit index lists it as not-in-briefs, to be
scheduled separately. **Post-live candidate** = net-new here; requires
a DEC after the DEC-145 freeze lifts.

### 1. Directory discoverability — Briefed (R15 / M7.9)

Teardown finding: Church Center's finder is the one to beat — narrow by
the facts that decide whether attendance is possible. Heylo and Meetup
both bury those facts in free text.

Audit status: this is R15's stated goal almost verbatim — directory
filters for meeting day, time of day, remote seats and childcare,
sharing one filter hook and vocabulary with Calendar, plus the
signed-out Home reorder (directory first, account request second).
Nothing to add. Any competitor-inspired filter beyond those four (e.g.
Heylo-style topic tags as a primary facet) would widen R15's scope,
whose Out list already protects `/groups` ordering and the tag model.

### 2. Leader weekly rhythm: attendance — Briefed (R10 / M6.3)

Teardown finding: BAND and Church Center both treat attendance as the
leader's closing action for a meeting — one roll, taken on a phone,
distinct from who *said* they were coming.

Audit status: R10 specifies exactly this ("Mark everyone present",
uncheck absentees, guest headcount, save once; registered-going shown
next to actually-present, never merged) as M6.3, Wave 4. The teardown
independently confirms the placement R10 chose: the roll belongs with
ManageRoster on the date card, not on the group page. No new work.

### 3. Sign-up sheets — Briefed (R14 / M6.2)

Teardown finding: slot-claiming (potluck, setup crew, supplies) is the
most common coordination job in small groups and the one most products
bolt on worst; BAND's sign-up sheets attached to events are the best
consumer implementation, and SignUpGenius is the incumbent churches
actually use.

Audit status: R14 specifies per-date sheets with quantity-safe claims
as M6.2, sequenced after R10, Wave 4. One teardown note worth keeping
for R14's implementation: BAND attaches the sheet to the *event post*
members already read, not to a separate tool surface — consistent with
R14's placement under the date card's RSVP block. Guest claiming stays
out, per R14's own evidence on the guest machine's weight.

### 4. Event page and guest flow — Mostly built; one Named gap

Teardown finding: Luma's event page (cover, one-tap register,
subscribable calendar) and Partiful's no-account, phone-number RSVP are
the two guest-side benchmarks. Partiful's text reminders "actually
arrive" because SMS is the channel, not an app notification.

Fold status: the substance is already shipped — verified guest
registration with confirm/manage pages (M1.4), ICS feeds and calendar
subscription (M1.7), capacity/waitlist with automatic promotion (M1.5).
Luma's approval-required registration has its Fold analogue in
request-to-join and account requests. Do not import Luma's
calendar-as-primitive model; Fold's group → series → date model is the
product's core bet (reaffirmed by R13 / DEC-139).

Named gap (audit index, "Not in these briefs"): token-page a11y/390px
coverage for `/invite`, `/set-password`, `/guest/*`. This is where the
Partiful/Luma comparison actually bites — those flows are Fold's
lowest-friction promise and currently its least-covered pages. If a
small brief is written before the freeze lifts, this is the one the
teardown supports; it is a coverage fix on existing surface, not new
surface.

SMS note: R7 was **rejected** — web push is not frozen and SMS stays a
headline goal. Partiful is therefore a legitimate design reference for
the future SMS work (text-first invites, reminders, blasts) in a way it
would not be had R7 passed. Record the reference here; the work itself
is Phase 3 / M3 territory already in PLAN.

### 5. Messages and group communication — Partially briefed; rest Post-live

Teardown finding: the consumer pattern that works is a *group home*
where announcements, chat, events and the roster are one place (Heylo's
branded space; BAND's board with "Read by" receipts; WhatsApp
Communities' announcement channel separated from chatter). Fold's
Messages exist but sit apart from the group page, and there is no
per-event or per-topic conversation.

Audit status, in three parts:

- **Briefed:** R15 moves Messages into primary navigation on phone and
  tablet for people who can compose — the approved slice of this theme.
  R6 (M7.5, notification outbox) covers delivery reliability underneath
  it. Notification UX rules already live in
  [`notifications.md`](notifications.md); Knock remains a UX reference
  only there, and the same "reference, not vendor" rule applies to
  every product in this document.
- **Frozen-adjacent:** nothing in the audit briefs adds chat rooms,
  boards or read receipts, and DEC-145 freezes new surface till live.
- **Post-live candidates** (record, do not brief yet): (a) an
  announcement/board distinction inside a group — leader posts pinned
  above member conversation, BAND-style; (b) "Read by" receipts on
  leader announcements, the single BAND feature leaders cite most;
  (c) per-date event chat, Heylo-style, which would need a hard look
  against Fold's contact-privacy machinery (DEC-141 froze direct-contact
  expansion; event chat is adjacent surface, not a loophole in it).

### 6. Rooms — Fold leads; Frozen (R19 / DEC-141)

No product in the set does room booking at all; the nearest analogues
are facility tools, not social-coordination products. Fold's
RoomCalendar, conflict handling and suggestions have no competitor
pattern worth importing. DEC-141 freezes room-suggestion expansion;
the teardown concurs — there is nothing out there to catch up to.

### 7. People, privacy, households — Fold leads; governed by R2/R3/R4

No consumer product in the set has contact-hidden profiles with
audited reveal, household people as registration subjects, or teen
accounts with guardian consent. Church Center's directory assumes a
ChMS-grade people database behind it — which is precisely the "rest of
ChurchTrac" Fold declines to be. The audit's people cluster owns this
territory: R2 generic CSV import (DEC-143), R3 TOTP + people/own-data
export, R4 erasure (DEC-144), R19's freeze on direct-contact expansion.
Competitor research contributes one caution only: Heylo collects
registration questions and dietary-style details very casually. Fold's
approval-first, purpose-separated contact machinery is a feature, and
competitor convenience here is an anti-pattern to avoid, not a gap.

### 8. Admin and finder operations — Briefed (R2 / M4.5), rest aligned

Church Center's admin strength is bulk people operations behind the
finder. The audit's answer is R2's generic CSV import (Wave 3), which
is the piece that makes a Church Center switcher's first week
survivable. Teardown adds nothing; in particular, do not import
Church Center's two-sided split (public app vs separate admin site).
Fold's single-surface, role-gated admin is simpler and matches the
one-binary product bet (R13 / DEC-139).

### 9. Discovery marketplace and open signup — Rejected (R8), not a gap

Meetup's remaining moat is stranger discovery. The teardown confirms
it is also the only thing Meetup still does that nobody else in the set
replicates. It is nonetheless out: R8 (open signup mode) was rejected,
and public-event discovery outside a person's own groups remains an
open product question (DEC-76), not a UI backlog item. Stranger-matching
products are excluded from this set for the same reason.

### 10. Performance and interface consistency — Briefed (R15 / R16)

The teardown's polish complaints about Fold's class of app — heavy
detail pages, inconsistent confirm dialogs, refetching — are R15
(lazy detail chunks, GET cache, shared Dialog/ConfirmDialog in Fold's
own words) and R16 (generated types) almost point for point. Consumer
products feel faster mostly because their data model is shallower;
the transferable lesson is the lazy-boundary and dialog-consistency
work, both already briefed as M7.9 / M7.10, Wave 5.

## Where Fold already leads (keep, don't dilute)

From the matrix's "where Fold leads" filter — surfaces where every
competitor scored below Fold:

1. **Per-date visibility override** (M1.13) — no competitor can hide or
   reveal one date of a series without touching the rest.
2. **Series RSVP with Undo** (M1.14) — Luma and Partiful are
   single-event; Meetup/Heylo recurring RSVP is per-instance only.
3. **Contact privacy** (M1.11) — hidden by default, audited reveal,
   purpose-separated requests. Nothing in the set is close.
4. **Rooms** (M2) — no analogue in the set.
5. **Households and guardian consent** (M1.9/M1.10) — no analogue in
   the set; Church Center defers this to the wider Planning Center
   suite, i.e. to exactly the ChMS weight Fold excludes.

## Net-new candidates, post-freeze (summary)

Recorded for a future DEC conversation once DEC-145's freeze lifts.
None is briefed, estimated or scheduled by this document.

| Candidate | Source | Why it might matter | Known tension |
|---|---|---|---|
| Announcement/board split inside groups | BAND, WhatsApp Communities | Leader signal drowns in chat-shaped tools | New surface; DEC-145 |
| "Read by" receipts on leader announcements | BAND | Leaders' most-cited coordination feature | Privacy posture; notification rules in `notifications.md` |
| Per-date event chat | Heylo | Attendees coordinate around one meeting | Adjacent to DEC-141 contact freeze; moderation load |
| Member-directory ordering by participation (new joins, top attendees, leaders) | Heylo | Helps leaders notice drift early | Attendance data sensitivity (R10's manager-only precedent) |
| Icebreaker/introduction prompts on join | Heylo, Geneva | Newcomer integration into small groups | Account-request flow is safeguarding-adjacent; teen rules |
| Partiful-style text-first invite/reminder design | Partiful | SMS is a headline goal (R7 rejected) | Phase 3 scope, not current waves |

## Recommendation

No new briefs from this teardown in the current waves. The overlap
with Audit 2026-11 is high enough that the matrix's main value is
confirmatory: R15, R10 and R14 are aimed at the right surfaces, in a
defensible order. Two small actions are worth taking when convenient:

1. When the audit index's named token-page a11y gap is scheduled,
   cite §4 — the guest/token flows are where Fold's friction promise
   is judged against Partiful and Luma.
2. When DEC-145's freeze lifts, start the post-live conversation from
   the candidate table above rather than from a fresh teardown; the
   competitor facts will need re-verifying, but the Fold-side analysis
   will still hold.
