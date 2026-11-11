# Fold milestones: Phase 1

### 14.4 Phase 1: Events and people

Phase 1 (v0.3) is done. The full as-delivered text is in [provenance/delivered-phases-0-2.md](../provenance/delivered-phases-0-2.md); the design sections, [docs/API.md](../../API.md) and the code are the current truth.

**M1.1a Events, time zones, occurrences, series edits**
Done (PR #38).
- Events keep their wall clock and IANA zone; stored occurrences are generated 18 months ahead and extended daily (C2, 5.3, 5.9).
- Series edits move future dates that have no per-date change and cancel dates the rule drops (5.3).
- The shared `SeriesEditor` and `ZonePicker`, with the church zone as the default (DEC-50, DEC-65).

**M1.1b Church-wide hosts, event visibility, audit, attendee privacy**
Done (PR #39, #41).
- Church-wide events run by their hosts (DEC-7).
- Each event stores its own visibility; opening one wider than its group is audited and notified, and tightening previews, confirms and cancels (DEC-6, DEC-17, DEC-28, DEC-48, DEC-51).
- An opened event of an invite-only group never shows the group, its place or its leaders to outsiders.

**M1.1c Stored in-app notifications**
Done (PR #46).
- The `notifications` table, `internal/notify` with its kind registry, and the bell, panel and `/notifications` page (5.12, DEC-42, DEC-44).
- Tasks that resolve for every admin at once, and server-checked inline actions.

**M1.2 Email basics, password reset, invitations**
Done (PR #47, #48).
- `internal/mail` with the dev outbox and SMTP (5.6).
- Password reset, verified email change and admin and leader invitations (5.3, 10.1).
- Email for notification kinds that allow it, with link-only text for sensitive kinds (5.12).

**M1.3 Self-signup, account requests, age gate**
Done (PR #49).
- Self-signup and "invite a friend" create account requests that an admin approves (5.10, DEC-4, DEC-30).
- The age gate on every door; public forms never reveal an account and throttle silently (DEC-52).

**M1.4 Guest registration**
Done (PR #55).
- Signed-out adults register for public events through an emailed confirmation link (C5, DEC-53 to DEC-56).
- A manage link lets the guest cancel.

**M1.5 Capacity, waitlist, pruning**
Done (PR #57, #101).
- Capacity and an automatic waitlist under a per-date lock, with promotion and the two-hour cutoff (5.4, DEC-20).
- Leaders and hosts remove registrations; event changes reach going and waitlisted people in the bell and by email (5.12).

**M1.6 Calendar views**
Done (PR #73).
- Agenda, Week and Month at `/calendar`, in the viewer's time zone (8.1, DEC-57, DEC-67).

**M1.7 ICS feeds**
Done (landed directly on `master`; review fixes in PR #80).
- Public, group and personal ICS feeds with revocable tokens (8.2).

**M1.8 Video, remote, childcare**
Done (PR #102, #105, #108, #109).
- Video links, including generated Jitsi links, shown per `video_reveal` (5.8, DEC-16, DEC-70).
- Remote attendance and childcare, each with its own capacity and filter (F11 to F13).

**M1.9 Households and household people**
Done (PR #106, #107, #110, #111).
- Households, household people and guardian links that an admin confirms (5.10, DEC-8, DEC-32, DEC-79).
- Holders register household people under the age and care-note rules, and a household person can become their own account (DEC-81 to DEC-83).
- Member guidance in [docs/MEMBERS.md](../../MEMBERS.md).

**M1.10 Teen accounts, consent, badges**
Done (PR #114 to #118). Verification: [M1.10-VERIFICATION.md](../provenance/M1.10-VERIFICATION.md).
- Teen accounts that sign in only after `account` consent, given by a confirmed guardian or an admin (5.10, 10.3, DEC-8, DEC-85).
- Teen and Child badges for leaders, hosts and admins, and the daily age rollover (DEC-18, DEC-69).

**M1.11 Contact privacy and direct-contact requests**
Done (PR #122). Verification: [M1.11-VERIFICATION.md](../provenance/M1.11-VERIFICATION.md).
- Teens' contact details stay hidden from leaders and hosts; adults may hide theirs (5.11, DEC-31).
- Direct-contact requests and grants, with every step audited and exportable.
- The name picker and "Ask to add" for people who can't be added directly (DEC-75, DEC-78).

**M1.12 Frontend toolchain upgrades**
Done (PR #59).
- Vite 8, React 19, Tailwind CSS 4 (configured in CSS), TypeScript 7, Vitest 5 and Node 24 from `.nvmrc`.

**M1.13 Per-date event visibility (DEC-46)**
Done (PR #123). Verification: [M1.13-VERIFICATION.md](../provenance/M1.13-VERIFICATION.md).
- One date of a series may be more or less visible than the series (`visibility_override`, 5.3).
- Every read path judges each date by its own effective visibility (`policy.EffectiveVisibility`).

**M1.14 Series RSVP and RSVP-aware calendar (DEC-71)**
Done (PR #129, #130, #131). Verification: [M1.14-VERIFICATION.md](../provenance/M1.14-VERIFICATION.md), [finish tasks](../provenance/M1.14-FINISH-TASKS.md), [review fixes](../provenance/M1.14-REVIEW-FIXES.md).
- The calendar and personal feed show the viewer's own answer on each date (DEC-71 to DEC-77, 8.2).
- Answering many dates of a series at once, with Undo (DEC-71, DEC-92).
- Leaving or removal previews and confirms which upcoming dates end (DEC-74, DEC-86).
