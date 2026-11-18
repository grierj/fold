# Fold verification map

This directory is the maintained source for verifying Fold's user-facing behavior. Read the index before driving the app, then use the matching feature file as the recipe.

## Baseline preconditions

- Start the instance with `.cursor/skills/verify-fold/verify-fold up` and keep its `RUN_ID`.
- Run `.cursor/skills/verify-fold/verify-fold doctor "$RUN_ID"` and require `ok`, the printed URL, and a database name that starts with `fold_verify_`.
- A fresh database opens on `/setup`. Complete that wizard before any feature that needs an account. The packaged sign-in drive does this itself.
- The isolation rules (never the dev database `fold`, `make dev` or `make e2e`; only a server you started with `up`) and how to load Playwright in a one-off script are in `../SKILL.md` and apply to every recipe.

## Driving conventions

- Start every recipe from the baseline state unless its preconditions say otherwise.
- Prefer ARIA roles and accessible names. Use the setup form's `name` attributes, which the existing browser tests use.
- The directory search box is labelled `Search groups`: use `getByLabel("Search groups")`.
- Form fields use the shared `Field`, so `getByLabel` finds them. An optional field's label ends in ` (optional)` (`Title (optional)`, `Password (optional)`, `Tags (optional)`), and `getByLabel` matches a substring. A label can also match a button's `aria-label`: with a user named `Casey General Full Name`, `getByLabel("Full name")` finds the form field and that row's `More for …` button, so pass `{ exact: true }`.
- The header is `getByRole("banner")`. The page body can have its own `<header>`, so `page.locator("header")` finds two. The header's home link shows the church name (`Fold verify` on a verify instance) over `powered by Fold`: use `getByRole("link", { name: /powered by Fold/ })`, or the `Home` link in `getByRole("navigation", { name: "Main" })`. Signed-in people also get a `New group` link there.
- The bell is named `Alerts` (with counts: `1 Alerts, 1 waiting on you`), and on a phone it is the `Alerts` tab in `getByRole("navigation", { name: "Tabs" })`.
- Some actions ask with the browser's own `confirm()` (Make leader, Cancel this date, Revoke consent, Remove a registration, Add back over capacity, Cancel my registration). Playwright dismisses one that has no handler, which cancels the action: register `page.once("dialog", (d) => d.accept())` first.
- The signed-in home page opens on a greeting (`Good evening, Verify`), not on the directory heading. The directory is the `Groups` section below it. The signed-out home page opens on `Find your people. Grow in faith together.`
- An unknown path such as `/cal/u/undefined.ics` answers 200 with the app shell, so a status of 200 does not prove a feed or file exists. Read the real URL from the page.
- Secondary actions live in a `More` menu (DEC-63). Open it with `getByRole("button", { name: /^More for/ })` (a date, a member, an admin user row) or `{ name: "More group actions" }` (the group header), then choose `getByRole("menuitem", { name })`.
- Treat every command as literal. Keep quoted names and flags unchanged.
- Run the packaged sign-in proof with `verify-fold drive "$RUN_ID" sign-in`.
- Run other browser actions with Playwright from `web/node_modules` against `VERIFY_URL`.
- Restore or discard mutated groups inside the verify database. Do not remove proof artifacts during cleanup.

## Proof and skip reporting

- Capture the user action and the resulting state, not only the final screen.
- UI proof includes a screenshot that shows the Fold header or the `Log in to Fold` heading. A full-page screenshot draws the sticky header wherever the scroll position was, so use a viewport screenshot (no `fullPage`) when the header must be at the top.
- Session proof includes `GET /api/v1/auth/me` from the same browser and, when the action creates a user, the `users` row in the verify database.
- Record the feature id and the entry point used with every artifact, in a `result.txt` beside the files.
- Report an unreachable path with the attempted command and the unmet precondition.
- Do not report a skipped entry point as verified through a different path.

## Mail steps

The verify instance runs `APP_ENV=prod` with no mail sender, so `/dev/outbox` answers 404 and emailed links cannot be read. Steps that open an emailed link (guest confirm and manage, a guardian consent email, a teen conversion invitation) need a dev-mode process on the same verify database for as long as they run. A job runs in whichever process claims it, so the prod process must not be running at the same time. Pause it first and resume it afterwards:

```bash
eval "$(.cursor/skills/verify-fold/verify-fold env "$RUN_ID")"
WORK=/tmp/fold-verify-$RUN_ID
PROD_PID=$(grep '^PID=' "$WORK/state.env" | tail -1 | cut -d= -f2)   # state.env has an empty PID= line first
DEV_DB=$(printf %s "$VERIFY_DATABASE_URL" | tr -d '\\')
case "$DEV_DB" in */fold_verify_*) ;; *) echo "not a verify database: $DEV_DB" >&2; exit 1 ;; esac
# A port nothing listens on, chosen for this run, so two runs never share one.
DEV_PORT=$(for p in $(shuf -i 20000-59999 -n 200); do ss -Hltn "sport = :$p" | grep -q . || { echo "$p"; break; }; done)
kill -STOP "$PROD_PID"
(export APP_ENV=dev PORT="$DEV_PORT" DATABASE_URL="$DEV_DB" \
  SESSION_SECRET=verify-only-session-secret-0123456789abcdef UPLOAD_DIR="$WORK/uploads" \
  FOLD_SETUP_TOKEN=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
 setsid "$WORK/fold" serve >>"$WORK/dev-outbox-server.log" 2>&1 & echo $! >"$WORK/dev.pid")
DEV_PID=$(cat "$WORK/dev.pid")
# Wait until that pid, not another process, owns the listener.
for _ in $(seq 50); do ss -Hltnp "sport = :$DEV_PORT" | grep -q "pid=$DEV_PID," && break; sleep 0.2; done
ss -Hltnp "sport = :$DEV_PORT" | grep -q "pid=$DEV_PID," || { echo "pid $DEV_PID does not own :$DEV_PORT" >&2; kill "$DEV_PID"; kill -CONT "$PROD_PID"; exit 1; }
# It must run in dev mode against this run's own database.
tr '\0' '\n' <"/proc/$DEV_PID/environ" | grep -E '^(APP_ENV|PORT)='                     # APP_ENV=dev, PORT=$DEV_PORT
[ "$(tr '\0' '\n' <"/proc/$DEV_PID/environ" | sed -n 's/^DATABASE_URL=//p')" = "$DEV_DB" ] && echo "database ok"
curl -s -o /dev/null -w '%{http_code}\n' "http://127.0.0.1:$DEV_PORT/healthz"           # 200
curl -s -o /dev/null -w '%{http_code}\n' "http://127.0.0.1:$DEV_PORT/dev/outbox"        # 200
```

Drive with `VERIFY_URL=http://127.0.0.1:$DEV_PORT`; sessions are per origin, so sign in again. The port is chosen per run on purpose: a fixed one makes two concurrent runs collide. Read a link from `/dev/outbox`: split the HTML on `<section`, keep the one containing `To: <address>`, and match the URL (`&amp;` becomes `&`). When done, check that the pid is the dev process (`tr '\0' '\n' </proc/$(cat "$WORK/dev.pid")/environ | grep -E '^(APP_ENV|PORT)='`), `kill` it, then `kill -CONT "$PROD_PID"` and run `verify-fold doctor "$RUN_ID"`. The dev process also queues the daily jobs (the age rollover included) when it starts, and [age-rollover.md](./age-rollover.md) uses that. Never start this against a database whose name does not start with `fold_verify_` (the `case` above refuses one).

## Feature entry contract

Each feature file starts with an H1 title and one paragraph describing the user-visible behavior. It then uses exactly four H2 sections in this order.

1. `Sub-features` lists short IDs with one line for each behavior.
2. `How to get to it (user POV)` lists every user entry point.
3. `Driving it with verify-fold` starts with `Preconditions:` and uses labeled bullets that pair each user action with an exact command and observable result.
4. `Gotchas` lists traps that can waste or invalidate a verification run.

Keep implementation details out of the map. Name only user paths, stable handles, required state, commands, and observable proof.

## Features

- [Sign in](./sign-in.md) covers first-run setup, a rejected password, a successful login, and the session surviving reload.
- [Directory](./directory.md) covers opening a public group, search, tag chips, and what a signed-out visitor can see.
- [Start a group](./start-a-group.md) covers the admin's live group and a member's group that waits for an admin's review.
- [Schedule an event](./schedule-event.md) covers a leader scheduling a group event, seeing it on the group page in the viewer's zone, and the event page's "Held in" line.
- [Log out](./log-out.md) covers the header control clearing the session, including after Back.
- [Event visibility](./event-visibility.md) covers one event narrower or wider than its group and the review when a group becomes more private.
- [Notifications](./notifications.md) covers the Alerts bell's count, approving a waiting group inline, archive with Undo that survives signing out, and the phone-width Alerts tab.
- [Event page](./event-page.md) covers the segmented RSVP and its scope dialog, attendance counts with the names expand, a series as its next date plus a `See all N dates` link, and the leader's More menus.
- [Guest registration](./guest-registration.md) covers the signed-out guest form on a public event, its age gate, and the unverified-signup boundary in the database.
- [Household registration](./household-registration.md) covers Profile household name/address, both RSVP entry points, independent named care/modes, mixed outcomes, manager recovery and care-note privacy.
- [Guardian consent](./guardian-consent.md) covers guardian claims, admin confirmation, scope grants and revocation, and signed email preview and redemption.
- [Teen consent](./teen-consent.md) covers admin scoped grant/revoke, consent with approval, public browsing before consent, sign-in after consent, suspension, and retained history.
- [Birthday rollover](./age-rollover.md) covers church-local adulthood, guardian age notices, household access, retained history, and staged transfer retries.
- [Teen household conversion](./teen-conversion.md) covers approval before account creation, continued household management, consent plus a fresh guardian check, and the atomic sign-up transfer.
