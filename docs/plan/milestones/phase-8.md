# Fold milestones: Phase 8

### 14.11 Phase 8: Balance pass (teardown recommendations, promoted Nov 18, 2026)

Source: Winston's competitor teardown of Nov 3, 2026 — six products (Meetup,
Heylo, Church Center, BAND, Luma, Partiful) scored across Fold's twelve
surfaces, with eight prioritised recommendations (two P0, three P1, three
P2). The research record is
[`research/competitor-teardown-2026-11.md`](../research/competitor-teardown-2026-11.md);
the interactive matrix is held outside the repo.

Why now: the simple deployment path works (M5.1/M5.2/M5.4, with real
DigitalOcean provision, backup, restore and upgrade checks on Nov 17, 2026).
The limiting factor is no longer getting Fold running; it is that Fold is
unbalanced — strongest exactly where no competitor plays (per-date
visibility, contact privacy, series RSVP, rooms, households and consent) and
weakest on the ordinary surfaces a member, a guest and a leader touch every
week. On Nov 18, 2026 Grier directed that the teardown's eight identified
items be treated as high priority and written into this plan. This phase is
that promotion.

Each milestone below states the **end goal**: the finished, user-visible
state that counts as done, in specific terms. It deliberately does not
prescribe the implementation. Where an approved Audit 2026-11 brief already
specifies the work (R10, R14, R15), the brief still owns slices, files and
tests; this phase owns the sequence and adds the end-state criteria the
brief does not cover. Where no brief exists, a brief at the R10/R14 level is
written and approved before building starts.

**Freeze note — proposed DEC-153 (for Grier to approve; not in force until
it lands in [`decisions.md`](../decisions.md)):** DEC-145's "no new product
surface until a first church is live" is lifted for the two named items in
this phase that are new surface — M8.6 (announcements vs discussion) and the
discussion lane it creates — and for no other surface. Every other item in
this phase is a change to existing surface, a promotion of an already
approved milestone, or presentation of already-built behaviour. The
candidates in "Not promoted" below stay out.

#### Order, and the two deliberate swaps

Build in this order: M8.1, M8.2, M8.3, M8.4, M8.5, M8.6, M8.7, M8.8.
M8.7 and M8.8 are small and may interleave as filler between larger items;
their position means "they polish strengths, they do not fix a broken
flow", not "they must wait idle".

Two swaps against the teardown's original P1/P2 tiers, made deliberately:

- **M8.4 attendance (originally P2) builds before M8.5 pulse and M8.6
  announcements (originally P1).** Attendance is fully briefed (R10),
  was already pulled ahead by DEC-145, and needs no new decision. Pulse
  has no brief yet, and announcements is new surface that needs DEC-153
  and a brief first. A ready item does not wait behind unbriefed ones.
- **Sign-up sheets (M6.2, R14) keep their existing place immediately
  after attendance.** Sheets were a teardown finding, not one of the
  eight recommendations, so they get no M8 number — but the leader's
  weekly rhythm is not balanced without them, and R14 already sequences
  them after R10. Do not let M8.5 or M8.6 displace them.

Coordination: M8.1 touches the same pages as M7.9's lazy-chunk slice
(R15-1). Land R15-1 for `EventDetail`/`EventDate` with or before M8.1,
never after it, so those pages are not rebuilt twice. M7.10 (generated
types) still lands between feature waves, not during M8.1's page edits.

---

**M8.1 — EventDate passes the 3-second test (teardown P0; Luma)**
Existing surface. No brief yet for the visual end-state; R15-1 owns the
lazy/bundle slice for these pages.

End goal — a member opening one date of a series, on a 390px phone,
can answer all of the following from the first viewport, without
scrolling, and the RSVP action stays reachable in a sticky bar while
the rest of the page scrolls:

- What is this? Cover image, event title, group name, leader/host line.
- When? Date, start–end time, and the time zone named when it differs
  from the viewer's.
- Where? Place or room as recorded, and for remote/hybrid dates the
  remote state (link available now, available at meeting time, or not
  remote) — never only buried in the description text.
- Am I in? The member's own state as the page's hero element, in one of
  three plain sentences: in for the series, in for this date only, or
  not in. Where the series state applies, Undo is offered at the same
  prominence as the RSVP action itself, for as long as Undo exists.
- Who else is coming? Count first, then names to exactly the extent
  existing attendee-privacy rules allow that viewer.
- What is different about this date? A cancelled date, and any date
  whose time, place, capacity or visibility differs from the series,
  says so in a plain sentence on its own page. Per-date exceptions are
  never only discoverable from the series page.

Further end-state criteria:

- Capacity is honest at a glance: in-person seats, remote seats and
  childcare are shown as separate counts when they exist, never merged
  into one number.
- A full date shows the viewer's waitlist state on the page — that they
  are waiting, and what happens next in plain words (promotion needs
  their explicit confirmation, and by when) — not only inside a later
  notification.
- The page meets M7.9's criteria for these routes: lazy chunk, bundle
  budget, no `window.confirm`/`alert`, axe-clean at 390/768/1280.
- Deliberately avoided: one-off party framing, hype copy, themed event
  styling (Luma/Partiful). Fold dates are recurring and pastoral; the
  language stays warm and plain. The series page and the per-date page
  are never collapsed into one.

**M8.2 — The guest and token flows feel account-free (teardown P0; Partiful)**
Existing surface. Closes the Audit 2026-11 index's named gap: token-page
a11y/390px coverage for `/invite`, `/set-password`, `/guest/*`.

End goal — a guest who receives a one-date invitation link finishes
in this state:

- From landing on the link to confirmed takes one tap on the primary
  action, with their name already filled in from the invitation. No
  account is offered, suggested or required before they have confirmed.
  All verification stays underneath, invisible: no guest ever sees the
  words token, verification or confirmation link as something they must
  understand or manage.
- The confirmation screen reads as "you are expected", not "you were
  processed": it names the group, the event, the date and time, and
  where to go (or how remote joining works), and on that same screen
  offers add-to-calendar and the way to change or cancel.
- The manage page a guest returns to shows that same event summary,
  and cancelling is one tap plus one explicit confirmation in the
  shared `ConfirmDialog`. A guest receives the same reminder for the
  date that a member receives, carrying their manage link and no
  account prompt.
- An expired, used or wrong link ends in a plain sentence about what
  happened and exactly one next step (ask for a new link, or contact
  the leader) — never a raw error, a dead end, or a login wall.

The same end-state applies to the other two token doors, `/invite` and
`/set-password`: one clear primary action per page, everything Fold
already knows pre-filled, and full a11y/390px test coverage — those
pages stop being Fold's least-covered surface.

Unchanged underneath, deliberately: guest verification, approval and
pruning rules; guest lists stay leader/member-scoped and are never
public (Partiful's public guest list is an anti-pattern here).

**M8.3 — A newcomer finds their group in 10 seconds (teardown P1; Church Center)**
This is the directory half of M7.9 (R15-3 and R15-5), pulled forward out
of the M7.9 bundle and built as its own sequence. The rest of M7.9
(cache, dialogs, Messages in nav) keeps its own place.

End goal — a newcomer, signed out or signed in, looking for a group:

- Can narrow the directory by the four facts that decide whether
  attendance is possible: meeting day, time of day, remote seats
  available, and childcare available. Those four facets, plus the
  existing search and tags, are the whole filter set — no more. Active
  filters are visible as removable chips, the matching-group count
  updates with them, and an empty result suggests a concrete change
  ("try another day, or clear the filters") in plain words.
- Hears the same words for the same filter in the directory and on the
  Calendar. A filter learned in one place means the same thing in the
  other (R15's shared hook and vocabulary, as an end-state promise).
- Can judge every group card in one glance: photo, name, meeting
  rhythm (day and time in words, e.g. "Tuesdays · 7 pm"), next meeting
  date, location as the group states it, member count, and childcare /
  remote badges when they are true.
- On signed-out Home, meets the directory first: the directory is the
  first content below a compact hero, church events follow it, and
  "Request an account" comes after a person has seen what there is to
  join (R15-5, as an end-state promise).

Scope boundary, stated plainly: area of town and life stage are **not**
facets in this milestone. Fold holds neither as structured data (tags
are free text; location is event text), so a facet for either would
filter on nothing. They are satisfied as scannable card information
above. Adding either as a real facet requires structured group fields
and its own DEC; it is not smuggled in here.

Privacy end-state: filtering never leaks. A group or a date a viewer
may not see does not affect their results, their counts, or an empty
state's wording (R15's facet-privacy danger, as an end-state promise).

**M8.4 — Attendance is a 30-second job (teardown P2; BAND, Church Center)**
This is M6.3 (R10), sequenced here. R10 owns the model, the API and the
tests; this entry owns its place in the order and restates its end goal
so the finished state can be judged without reading the brief.

End goal — a leader, host or admin, on a phone, after a meeting:

- Opens that date's roll from the date itself, taps "Mark everyone
  present", unticks the people who were not there, enters a guest
  headcount, and saves once. For a group of up to about 20 people the
  whole pass takes under 30 seconds, one-handed.
- Afterwards holds two numbers that are never merged: who *said* they
  were coming (registrations) and who *was* there (the saved roll).
  Both are visible to managers side by side; neither overwrites the
  other.
- Can see, as a manager only, a short recent summary for the event
  (the brief's last-8-dates summary) — counts, not a league table.

Boundaries that are part of the end goal, not fine print: only people
who can manage the event can see or mark a roll; there is no
member-facing view of anyone's attendance and no per-person attendance
history page in this milestone; attendance is religious-observance
data and is treated with at least the care Fold already gives RSVP
names. M6.2 sign-up sheets follow this milestone immediately (see the
order note above); their end goal stays R14's: a per-date sheet under
the date card's RSVP block, quantity-safe claims, no guest claiming
in v1.

**M8.5 — A group feels lived-in: GroupDetail and Home pulse (teardown P1; Heylo)**
Existing surface, existing data only. No brief yet.

End goal — a member opening their group sees, above the roster and
management tools, a pulse block that answers "what is happening with
my group?" in about five seconds:

- The next meeting is pinned: date, time, place, the member's own
  RSVP state, and a one-tap way to change that state.
- The group's latest leader announcement appears there (once M8.6
  exists; before M8.6, the latest leader broadcast Fold already sent
  for this group, shown by its subject and date).
- Recent movement is stated in plain sentences built only from what
  members may already see — for example new RSVPs and new members in
  the last seven days, with names only where existing privacy rules
  already show names. A quiet group says so plainly ("Nothing new this
  week — next meeting Tuesday 7 pm"); there are no empty boxes, no
  zeroes presented as activity, and no invented content.
- A leader resources area, when the group has resources, is reachable
  from the group page itself, not only from admin.

The same pulse, per group, is what signed-in Home's Coming Up shows:
my groups only, chronological, scannable as a week. No recommendations,
no discovery content, no algorithmic ranking, ever.

Boundaries: roster, households and contact-privacy tools are unchanged
and undisplaced — the pulse sits above them and the tabs stay
task-led. GroupDetail does not become a social feed: no likes, no
reaction counts, no comment stream appears here (discussion, when it
comes, is M8.6's lane, not this page's). Hidden contacts stay hidden
in every pulse sentence; teen rules apply unchanged.

**M8.6 — Announcements are not discussion (teardown P1; BAND, WhatsApp Communities)**
New surface. Requires DEC-153 (proposed above) and a brief before
building. Absorbs M6.6 (group discussions): M6.6 does not also get
built as a separate, undifferentiated board.

End goal — inside a group there are two lanes a member can tell apart
at a glance, by label and by treatment:

- **Announcements.** Only leaders, hosts and admins post. Every member
  of the group can read them. An announcement is delivered through
  Fold's existing composer and send pipeline — same scheduling,
  guardian copies, notification preferences and sent record as today's
  broadcasts — and stays pinned at the top of the group home (M8.5's
  pulse shows the latest one).
- **Discussion.** Members post and reply, in one thread stream for the
  group. Discussion is in-app by default: it does not email-blast the
  group, and push follows the member's existing notification
  preferences.
- Replying to an announcement happens in discussion, linked to that
  announcement. The announcement lane itself stays one-way and
  uncluttered, however active discussion gets.
- A leader or admin can remove a discussion post, and the removal is
  audited. Contact privacy and teen rules apply in discussion exactly
  as everywhere else in Fold: no contact details are exposed by
  posting, hidden stays hidden, teens are badged, and discussion is
  not a route around DEC-141's contact freeze.

Deliberately not in the end goal: per-person "Read by" receipts on
announcements (a leader sees the send pipeline's delivery counts, not
who opened what — see "Not promoted"), and per-date event chat
(discussion is group-level in this milestone).

**M8.7 — Calendar subscribe is found, not hunted (teardown P2; Luma)**
Existing surface. The feeds are built (M1.7); this milestone is about
their visibility. Small; may interleave.

End goal — a person never has to know the letters "ICS" to put Fold
on their calendar:

- Everywhere commitment happens — GroupDetail, the confirmation shown
  right after joining a group, My Groups, and the Calendar page —
  offers the same one-tap action, labelled in plain words ("Add to my
  calendar"), with one sentence under it: meetings from this group
  appear in their calendar app automatically, including changes.
- Where two feeds could apply, the choice is one sentence each at the
  point of choice: this group's meetings, or my own RSVP-aware
  calendar of everything I am in for.
- Short, plain setup help for the common calendar apps sits at the
  point of subscribing, not only in documentation.
- Settings lists every feed the person has switched on, what it
  covers, and a one-tap revoke beside each — and revoking stops that
  feed. A feed a person has forgotten is a leak; the end state makes
  forgotten feeds findable.

Visibility is unchanged: a feed only ever carries what its existing
token already entitles it to, and subscribing is offered only for
groups and events the viewer may already see.

**M8.8 — Fold's moats are named where they are met (teardown P2)**
Presentation only. No privacy, consent or room behaviour changes in
this milestone. Small; may interleave.

End goal — at the exact moment a person meets one of Fold's
differentiators, one short plain-language sentence names it:

- On a roster or profile where contacts are hidden: contacts are
  hidden by default, leaders reach people through Fold, and every
  reveal is recorded.
- In the teen/guardian consent flow, written for a parent, not a
  lawyer: what the consent covers, what a teen can and cannot do, and
  what happens after the parent confirms — stated before the parent
  is asked to confirm, so consent is a confirmation of something
  understood, not a form completed.
- Where a leader is offered a suggested room: one line saying why that
  room is suggested, in terms of the group's own past use. This
  explains the existing suggestion; it does not expand what is
  suggested (DEC-141's freeze on room-suggestion expansion stands).
- On the per-date and series RSVP states (with M8.1): that answering
  for the whole series, and undoing it, is something Fold does and
  the sentence a member sees says so plainly.

And in one place an admin can send a pastor to: the admin home (and,
when M4.2 lands, the setup wizard) carries a short "What Fold does
differently" list — contact privacy, guardian consent, per-date
visibility, series RSVP with Undo, and room booking — each in one
plain sentence, no marketing language.

Boundaries: an explainer never blocks the task it sits beside, never
appears as a repeated popup, and is written in Fold's warm, plain
voice. If wording and behaviour ever disagree, the behaviour (and the
DEC behind it) wins and the wording is the bug.

#### Not promoted (recorded so they are not re-litigated)

From the teardown's post-live candidate table, these stay out of this
phase, each for its stated reason:

| Candidate | Why it stays out |
|---|---|
| "Read by" receipts on announcements | Conflicts with Fold's privacy posture; leaders get delivery counts (M8.6), not per-person open tracking. Revisit only with production evidence leaders need more. |
| Per-date event chat | Adjacent to DEC-141's contact freeze and a new moderation load. M8.6's discussion is group-level; per-date threads need their own DEC and brief. |
| Directory ordering by participation | Built on attendance data (M8.4) that does not exist yet and is sensitive; R10's manager-only precedent governs any later use. |
| Icebreaker / introduction prompts on join | The account-request flow is safeguarding-adjacent and teen rules apply; not worth the risk for a nicety. |
| Stranger discovery / open signup | Rejected with R8; DEC-4 and DEC-76 still govern. Not a gap. |
| Public guest lists, automatic waitlist backfill without confirmation | Anti-patterns for Fold's verified-guest and explicit-confirmation posture (M8.1, M8.2). |

#### Releases

Phase 8 items ship in the order above, interleaved with the remaining
v0.5 work only where a slice is genuinely parallel-safe. In the release
table (PLAN 14.2) they are the "Balance pass" row: M8.1–M8.8, with
M6.3 and the directory slice of M7.9 counted where they are sequenced
here (M8.4, M8.3).
