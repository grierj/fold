# Notifications: consolidated UX reference

Reconciled 2026-10-25. The October 10 vendor research led to DEC-42–DEC-45:
Fold builds notifications in PostgreSQL, with no notification vendor or new
service. Knock is a UX reference only. This document retains the useful design
rules, not the rejected schema/API sketches or old rose/Tailwind palette.
[PLAN 5.12](../PLAN.md#512-notifications-dec-42-dec-43) owns product policy;
[API.md](../../API.md#notifications) describes the installed endpoints.

## Delivery status

- M1.1c: stored feed, badge, read/archive, task resolution and polling (merged).
- M1.2: minimal SMTP/devoutbox email after commit (merged).
- M1.5: event-change/waitlist notifications (merged; not reminders).
- M3.12: category/channel preferences, delayed non-urgent email and SMS (future).
- M6.16: once-a-day digest (future). Weekly digests are not planned.

Knock was rejected because each church would need another SaaS account and
configuration, with duplicated consent/privacy rules. Novu was rejected because
its MongoDB/Redis/multi-service deployment violates Fold's simplicity goal.
No Knock or Novu adapter is planned. A future push channel may use the existing
`notify.Send` entry point if a church asks.

## 6. Notification UX

The section numbers below retain references from PLAN. Styling follows
DEC-60–DEC-66: role tokens, shared UI controls, self-hosted Inter, and 44px
control targets. Earlier brand-pink classes, palette values, `!` overrides and
unlabelled/raw controls are obsolete.

### 6.1 Bell and badge

One count: unread items plus unresolved items waiting on you, without double
counting, capped at 9+. Opening the panel does not clear it. Reading ordinary
items does; waiting items stay counted until resolved. No needs-action dot.
The accessible name includes unread and waiting counts. Use primary role tokens,
not danger styling; no badge at zero or attention-seeking animation.

### 6.2 Desktop panel

At 640px and wider, the bell opens a labelled disclosure region, not a modal.
Keep the panel inside the viewport, with a scrolling list, a Notifications
heading, Mark all as read, Waiting on you, Recent and See all notifications.
Load 20 with Show more/cursor pagination. Escape/outside click closes; keyboard
focus returns appropriately. The settings link waits for M3.12.

### 6.3 Item anatomy and privacy

Plain-text title and short body; relative time with an absolute equivalent;
unread and waiting states expressed in text as well as visual treatment. Put
row navigation, action buttons and Archive outside one another's hit targets.
Use at most two actions, emitted by the registry's allow-list.

Never put email, phone, birth date, care notes or private message bodies in a
notification. Never expose a hidden group's identity, place or link. Sensitive
contact, add and safeguarding kinds use names-free, navigation-only wording in
email; some use that wording in the feed too. Opening a link grants no access
or approval. The destination checks current policy. Household people have no
feed; the responsible account receives their notices. Pending-consent teens
have no ordinary feed.

### 6.4 States

- Unread: visible emphasis plus accessible Unread text.
- Waiting: a Waiting on you label; counted until resolved.
- Resolved: remove stale buttons and show the decision/actor. A lost inline race
  shows Already handled, not an unexplained error.
- Archive: server state, Undo and a durable Archived filter. A timed Undo should
  pause on focus/hover; it is not the only way to restore an item.
- Loading: shared Skeleton; loading more keeps existing content.
- Failure: shared friendly error/retry controls. A failed refresh retains the
  last loaded feed **in memory only**, with a stale-state explanation.
- Offline: explain the state and disable mutations. Logout clears all feed data.

### 6.5 Batching

Merge eligible unread items by recipient/group key within the kind's batch
window. Never batch waiting tasks, safety/contact items or cancellations.
No production kind currently enables batching; the mechanism is tested.

### 6.5a Event items

Cancellations, time/place changes and waitlist promotions go to going/waitlisted
people. Event items become Archived at occurrence end, then age out under
retention. Reminders and routine upcoming dates never enter the bell (DEC-44).

### 6.6 Actions

Inline approval is limited to a new group and a clean adult account request.
Teen/flagged account requests require Review; the server checks eligibility,
not just the notification action. No inline action for minors, consent, contact,
roles, deletion, outbound messages, visibility or RSVP. Use the existing domain
endpoint, policy, audit and task-resolution transaction. Notification text never
shows the account's email; authorized full review can show the necessary details.

### 6.7 Empty states

Use EmptyState with a short explanation of the updates that would appear.
Waiting and Archived filters have their own neutral empty wording. Do not
create a fake welcome notification just to fill the list. Offer only actions
that exist; no settings link before M3.12 (DEC-97 since brings the "Turn off
all optional email" switch early, so Alerts links to Me > Notifications).

### 6.8 Preferences (future M3.12)

In-app is always on. Categories have Email and Text switches; required account,
safety and responsible-party request email cannot be disabled. Switches map to
Consent rows, not a second preferences truth. No per-group settings or digest
choice in v1. Turn off all optional email leaves required mail on. Text controls
require verified ownership and SMS opt-in; teens also need SMS consent. Show the
effective recipient zone and read-only guardian-copy setting where relevant.
Email ignores SMS quiet hours. M6.16 adds the daily digest setting later.

### 6.9 Accessibility

Use labelled regions/lists, keyboard-reachable controls, visible focus, textual
status and polite announcements for action results. Shared components and
verify-ui enforce token contrast, minimum targets and layout. Axe is useful
but does not replace keyboard and visual inspection.

### 6.10 Mobile

Below 640px, use `/notifications`, not the desktop popover. The Notifications
bottom tab is the entry point (DEC-62/67). Filter chips use `?filter=` and back
navigation preserves the previous view. Waiting is relevant to any person with
a task, including teen/adult approval subjects, not just leaders/admins.

### 6.11 CSP

Same-origin requests and assets only; no websocket dependency or CDN. Render
plain text, validate internal links, and format times through the shared
`lib/when.ts` helpers. Use shared components rather than inline markup styles.

### 6.12 Research sources

Desk research originally read October 10, 2026; not revalidated by this cleanup.
Do not treat vendor descriptions or pricing as current procurement advice.

- Knock states: https://docs.knock.app/send-notifications/message-statuses
- Knock feed: https://docs.knock.app/integrations/in-app/feeds
- Knock batching: https://docs.knock.app/designing-workflows/batch-function
- Knock preferences: https://docs.knock.app/preferences/overview
- Knock pricing: https://knock.app/pricing
- Novu self-hosting: https://docs.novu.co/community/self-hosting-novu/overview
- Meetup notifications: https://help.meetup.com/hc/en-us/articles/40705340432525-How-Meetup-notifications-work
- Meetup notification kinds: https://help.meetup.com/hc/en-us/articles/40708711818637-What-notifications-Meetup-can-send
- ChurchTrac broadcasts: https://www.churchtrac.com/support/people/push-notifications

The signed-in Meetup bell was not inspected; this is not a claim of visual
parity. ChurchTrac's documented broadcasts are not a per-member feed model.

### 6.13 Technical resolutions

1. Keep `seen_at` for future escalation; the badge uses unread/waiting.
2. Read-all skips unresolved waiting items; they remain counted.
3. Event expiry means archive, not immediate deletion.
4. Waiting, safety and cancellation kinds have no batching window.
5. The registry and domain endpoints enforce inline eligibility.
6. Conditional decisions return structured already-handled outcomes.
7. Last-loaded feed lives in memory, never localStorage.
8. Poll every 60 seconds while visible, plus focus/navigation/action refresh.
9. Sensitive kinds have names-free email variants.
10. Optional channel settings use Consent; digest storage waits for M6.16.
11. Settings expose the saved recipient zone, falling back to the church zone.
12. General guardian copies are future messaging work, distinct from existing
    transactional safeguarding notices to confirmed guardians.
13. Mobile uses the full page and query-string filters.

### 6.14 Product resolutions

DEC-44 settled badge, reminders, clean-adult inline approval, category-only
settings, immediate-only v1 email, SMS-only quiet hours and optional-email opt-out.
DEC-45 settled retention: 90 days for read/archived rows, 180 for unread rows.
AuditLog remains the record. No notification product question from the original
research remains open; implementation of later delivery/preferences still does.
