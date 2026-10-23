# Fold verification map

This directory is the maintained source for verifying Fold's user-facing behavior. Read the index before driving the app, then use the matching feature file as the recipe.

## Baseline preconditions

- Start the instance with `.cursor/skills/verify-fold/verify-fold up` and keep its `RUN_ID`.
- Run `.cursor/skills/verify-fold/verify-fold doctor "$RUN_ID"` and require `ok`, the printed URL, and a database name that starts with `fold_verify_`.
- A fresh database opens on `/setup`. Complete that wizard before any feature that needs an account. The packaged sign-in drive does this itself.
- Do not drive a server you did not start with `up`. Do not use the dev database named `fold`, and do not use `make e2e`.
- Load `eval "$(.cursor/skills/verify-fold/verify-fold env "$RUN_ID")"` before a one-off Playwright script. Load Playwright with `createRequire` from `$FOLD_ROOT/web/package.json`, as the skill's Drive section shows. `NODE_PATH` does not resolve an ESM import.

## Driving conventions

- Start every recipe from the baseline state unless its preconditions say otherwise.
- Prefer ARIA roles and accessible names. Use the setup form's `name` attributes, which the existing browser tests use.
- The directory search box has no accessible name. Use the placeholder `Search groups…` (one ellipsis character).
- Secondary actions live in a `More` menu (DEC-63). Open it with `getByRole("button", { name: /^More for/ })` (a date, a member, an admin user row) or `{ name: "More group actions" }` (the group header), then choose `getByRole("menuitem", { name })`.
- Treat every command as literal. Keep quoted names and flags unchanged.
- Run the packaged sign-in proof with `verify-fold drive "$RUN_ID" sign-in`.
- Run other browser actions with Playwright from `web/node_modules` against `VERIFY_URL`.
- Restore or discard mutated groups inside the verify database. Do not remove proof artifacts during cleanup.

## Proof and skip reporting

- Capture the user action and the resulting state, not only the final screen.
- UI proof includes a screenshot that shows the Fold header or the `Log in to Fold` heading.
- Session proof includes `GET /api/v1/auth/me` from the same browser and, when the action creates a user, the `users` row in the verify database.
- Record the feature id and the entry point used with every artifact, in a `result.txt` beside the files.
- Report an unreachable path with the attempted command and the unmet precondition.
- Do not report a skipped entry point as verified through a different path.

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
- [Start a group](./start-a-group.md) covers the admin's live group and a member's group that waits for approval.
- [Schedule an event](./schedule-event.md) covers a leader scheduling a group event, seeing it on the group page in the viewer's zone, and the event page's "Held in" line.
- [Log out](./log-out.md) covers the header control clearing the session, including after Back.
- [Event visibility](./event-visibility.md) covers one event narrower or wider than its group and the review when a group becomes more private.
- [Notifications](./notifications.md) covers the bell's count, approving a waiting group inline, archive with Undo that survives signing out, and the phone-width page.
- [Event page](./event-page.md) covers the segmented RSVP, attendance counts with the names expand, a series as its next date plus `N more dates in the next 90 days`, and the leader's More menus.
- [Guest registration](./guest-registration.md) covers the signed-out guest form on a public event, its age gate, and the unverified-signup boundary in the database.
- [Household registration](./household-registration.md) covers Profile household name/address, both RSVP entry points, independent named care/modes, mixed outcomes, manager recovery and care-note privacy.
- [Teen consent](./teen-consent.md) covers admin scoped grant/revoke, consent with approval, waiting-session activation, suspension, and retained history.
