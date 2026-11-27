# AGENTS.md

The product is **Fold** (`docs/DECISIONS.md` R5, R25). The name lives in one constant,
`internal/brand`, and nowhere else. Pages and emails lead with the church's own name and say
"Powered by Fold" at the foot.

This page is the mission and the way of working. Keep it under 200 lines. Four companions hold
the detail:

| File | What it holds |
|---|---|
| `docs/DECISIONS.md` | The product rules, who decided each, and what is still only proposed |
| `docs/PLAN.md` | What each slice must do to be done, what must be settled first, and the one status table |
| `docs/HAZARDS.md` | Mistakes the predecessor project made or caught, so this one does not repeat them |
| `docs/CARRYOVER.md` | Exactly what to copy from the predecessor, what to adapt, and what to leave behind |

When this page and `docs/DECISIONS.md` disagree, a decision marked as ruled by Grier wins.

## What this is

One place for a church's small groups, events, RSVPs and group announcements. A member finds a
group, sees what is coming up and says whether they will be there. A leader schedules, sees who is
coming, takes attendance and writes to their people. A volunteer admin approves new accounts and
new groups and keeps the site running.

- Free, MIT-licensed, given away.
- One installation serves one church. The church owns its server, its domain and its mail account.
- Installed by a volunteer on a small VPS with one compose file: the app, and Caddy in front of it
  for HTTPS.
- Familiar on purpose: it should feel like Meetup, with the church-admin parts added.
- Careful with children, teens and contact details.

It is not a church management system. No giving, no accounting, no check-in kiosks, no worship
planning, no website builder, no translations.

## Who it is for, and how big

A church with a few paid staff who are the admins, where most groups have one leader. In Grier's
words: "A church with 500 people and 6 staff members would be considered VERY LARGE for this
software."

Use that sentence to refuse machinery. Do not build for many people saving the same thing at the
same instant, for several servers, or for proof that an action happened beyond the audit log.

**It never excuses a security finding.** An attacker is not a church member and is not limited by
the size of the church.

## The four structural choices

1. **One writer.** The database is a single SQLite file. One process writes, one write at a time.
   There are no lock orders and no races between writers, because there is only one.
2. **One program that renders pages.** The server renders HTML. Small updates swap a fragment of
   HTML. There is no JSON API and no separate client application. JavaScript is a few small files
   for things HTML cannot do (rich text, image focal point, resize before upload).
3. **Personal data in one place.** Names, emails, phones and birth dates live in the `people`
   table and in columns explicitly tagged as personal. Everything else stores an id. Deleting a
   person is one small function.
4. **A church before more depth.** The first slice deploys to a real server. A pilot church uses it
   before any feature beyond the build order is started. After that, what they ask for decides.

## How the code is shaped

- **Go, one static binary**, `CGO_ENABLED=0`. The standard library's router. SQL written by hand
  or generated from SQL, no ORM. Templates embedded in the binary.
- **One door for writes.** Every change is a named command run by one executor: begin the write,
  load the actor fresh, ask policy, apply, write the audit row and any notices and jobs in the
  same transaction, commit. No handler writes to the database any other way.
- **One read per page.** One read transaction loads the facts, asks policy, builds the view and
  renders it.
- **Every permission is a pure function** in `internal/policy`, with table tests. Handlers load
  facts and ask. They never decide.
- **The wall clock is the truth for events.** An event stores its local start time and its IANA
  zone. Dates are generated ahead and stored.
- **Notices store a kind and ids.** The words are written when the notice is read, through the same
  visibility rules as any page.
- **Background work is rows in `jobs`**, run by one goroutine. Handlers must be safe to run twice.
- **No network call inside a write.** Mail, DNS and HTTP happen in jobs, after commit.
- **Similar things behave the same.** One component per kind of control, one wording per kind of
  outcome.

## Security comes first

This runs on the public internet, and strangers type into its text boxes when they sign up for an
event. Grier: "security is of primary concern." Every change is judged against these rules.

- **Escape everything.** All output goes through `html/template`. User text is never marked as
  safe HTML. The one exception is rich text that has passed the single allow-list sanitizer.
- **Strict content security policy.** No inline script or style, no `eval`, nothing loaded from
  another site.
- **Only a POST changes anything**, and every POST carries a CSRF token. A link in an email opens a
  page with a button; it never acts by being opened. The only two POSTs without a CSRF token are
  the inbox's one-click unsubscribe and the mail provider's delivery webhook, each proved by its
  own secret.
- **Links are secrets.** Random, stored hashed, never written to a log. A link that signs someone
  in or performs an action is single use and lasts at most 24 hours. A standing link (a calendar feed, an
  unsubscribe, a guest's link to manage one date) does one narrow thing, can be revoked, and
  stops working when its purpose ends.
- **Every read and every write asks policy.** Something a person may not see answers exactly as if
  it did not exist.
- **Public forms give one answer.** They never reveal whether an address has an account, and a
  rate limit answers with the usual success message. The email itself carries any hint.
- **Limit every input**: length, count, body size, and how often. Check the limit before doing
  expensive work.
- **Uploads are images only**: sniffed, re-encoded, size-capped, stored under a name the server
  made up.
- **The server never fetches a URL a person supplied.** The one exception is the mail server
  named at setup (`docs/DECISIONS.md` R22).
- **Every change a person makes writes one row to the sealed audit log**, in the same
  transaction, because the write executor does it. Upkeep such as session renewal does not
  (`docs/DECISIONS.md` R19). The log is chained so tampering shows, is never edited or purged, and no
  page reads it. Opening it is a deliberate, announced act (`docs/DECISIONS.md` R6).
- **Secrets are encrypted at rest** and never sent back to the browser.
- **Few dependencies**, each one justified in the pull request that adds it.

## Budgets

Grier set the table and route ceilings (R13): "I would rather have more of these than try to work
with less than we need." The other rows are starting numbers, proposed and not yet ruled. A change
that would cross a ceiling says what it removes, or asks Grier to raise it.

| Budget | Ceiling | How to measure |
|---|---|---|
| Go and templates, not tests | 25,000 lines | `find . \( -name '*.go' -o -name '*.html' \) ! -name '*_test.go' \| xargs cat \| wc -l` |
| Tables | 35 | `grep -ci 'create table' migrations/*.sql` |
| Routes | 200 | `grep -c 'mux.Handle' internal/app/routes.go` (every route is registered in that one file) |
| Whole test suite | 3 minutes, no containers | `time go test ./...` |
| Containers a church runs | 2 (the app and Caddy) | the compose file |
| Blank VPS to first invited member | 15 minutes | timed by hand at each release |

## Build order

Each slice is deployable and ends on the pilot server. Do not start a slice before the one above
it is merged. `docs/PLAN.md` says what "done" means for each and wins on order.

| Slice | Contents |
|---|---|
| 0 | Binary, SQLite, migrations, the write executor with the sealed audit log, sign-in, setup wizard with a test email, built-in backup and restore, install on a real VPS, accessibility and layout gates, the component gallery |
| 1 | People, groups, memberships, approval of new accounts and new groups, group search, notices (bell and required email) |
| 2 | Events, recurrence, dates, RSVP with capacity and waitlist, guest RSVP, the date page, calendar, ICS feeds, finder filters by day and time. **Then measure against the budgets.** |
| 3 | Announcements to a group or an event, reminders, email choices and unsubscribe, bounces |
| 4 | Spreadsheet import, attendance, admin basics, erasure, export, second factor for admins, the request to open the audit log, legal page templates. **Pilot church goes live; later slices wait for it.** |
| 5 | Households, children as RSVP subjects, childcare counts, teens with guardian consent |
| 6 | Rooms: bookings, conflicts, by-room calendar, suggestions |
| 7 on | Chosen by the pilot church |

## How to work

- **Read first:** this page, then the parts of `docs/DECISIONS.md` and `docs/HAZARDS.md` for the
  area you are touching.
- **Small slices, one pull request each**, database first, then the command and its policy, then
  the page. Every behaviour change has a test that fails without it.
- **One review pass, by a different model from the one that wrote the change.** Security is
  reviewed first and is never skipped: the reviewer follows each new input from the request to
  where it is stored to every place it is shown. Then correctness against the decisions, then
  simplicity, then the page itself (familiar patterns, accessibility, plain warm wording), then
  operations and docs.
- **A finding needs an ordinary scenario**: one person, or one ordinary change by someone else.
  Findings about several people acting in the same instant are out of scope unless they concern
  security, privacy, consent or minors.
- **Score each security finding**: likelihood and severity, each 1 to 10, multiplied. A product of
  4 or less is written down in `docs/ignored-security-issues.md` and not fixed. Anything higher is
  fixed before merge.
- **Structure before guards.** Make the mistake impossible with the shape of the code. Then use a
  type. Write a test that scans source only when nothing else can hold the rule.
- **Decisions are Grier's.** A change to product behaviour, security policy or what a church must
  do to run this stops and asks. Record the answer in `docs/DECISIONS.md` in his words.
- **Challenge the design when it is the problem**, but pause first: say what you would change, why,
  and how large the change is.
- **Do not copy the predecessor's shape.** `docs/CARRYOVER.md` names what may be brought over.
  Anything else in that repository is a reference for a specific question, never a starting point.
- **Comments say only what the code cannot**: the constraint, the decision behind it, the hazard.

## Wording

Plain, warm, second person. Say "Saved" and "Updated". When something could not be saved, say so
gently ("could not be saved") and say what happened. Never tell a person to close and reopen
something: if the page can no longer do what it offered, it updates itself and says why. Render a
person's name with their public name, never their email.
