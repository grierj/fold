# Birthday rollover

Teen accounts become adult accounts on the church-local eighteenth birthday. Guardians receive one notice when a household child reaches 13. Existing history, household management, and capacity limits remain intact.

## Sub-features

- `age18`: account adulthood, waiting-session refresh, password setup, and consent history.
- `household-adult`: household account access at 18 without automatic guardian confirmation.
- `child13`: one guardian notice, with existing sign-ups and no new login.
- `staged-birthday`: suspended and unavailable-holder conversion behavior.

## How to get to it (user POV)

- A pending teen's eighteenth birthday → **Back to log in** on the public account explanation (`/waiting-for-consent`), or **Log in** from the header.
- Guardian → Alerts → household age notice → Profile → Household.
- Admin → Users → former teen's More menu → Consent history.
- Admin → Users → Enable for a separately suspended staged account.

## Driving it with verify-fold

Preconditions: follow the verification map baseline, launch with `verify-fold up`, require `verify-fold doctor "$RUN_ID"` to print `ok`, and load the exported run environment. Birthday actions run automatically at church-local midnight. The deterministic checks use the same rollover function with a fake clock in `go test . -run 'TestAgeRollover|TestNextAgeRollover'` against isolated test databases (`DATABASE_URL` pointing at the dev Postgres, as `make test` does; 11 tests).

- **Adulthood:** before the local birthday, a pending teen cannot log in. Correct credentials on `/login` show `waiting for a parent or guardian's consent` with a `Browse public events` link. After password setup the public explanation at `/waiting-for-consent` has `getByRole('heading', {name:'Your account is almost ready'})`, `Browse public events` and `Back to log in`, and public browsing stays available. After rollover, choose **Back to log in** and submit the password to start a fresh session, subject to any separate approval or suspension requirements. A former waiting cookie never becomes a login (DEC-85). The admin's Users row loses its Teen badge (before rollover the row reads `Teen` and its More menu offers `Manage consent`). After rollover that menu offers `Consent history` instead (`getByRole('menuitem', {name:'Consent history', exact:true})`, shown for an adult who has teen consent history); it opens the `Consent for <name>` panel with `This person is now an adult. Their teen consent history stays on file.` and `Consent history (N)`.
- **Household access:** after a converted teen reaches 18, open **Profile**, then **Household**, and rename their household (`Rename`, `Household name`, `Save household`). The change saves. Adding a household child creates a pending link that still needs an admin's confirmation.
- **Guardian notice:** open Alerts as a confirmed guardian and follow the household age notice into Profile. The notice asks you to review the age group. The child keeps their existing sign-ups and has no account. Reading or archiving the notice does not create another notice on later days.
- **Suspended conversion:** after the staged person's eighteenth birthday, the household still sees and manages their adult household person while the account remains disabled. Admin Enable moves the existing registration IDs, answers, and waitlist places once. If the original responsible account is disabled, the move waits until that account is enabled. Other birthdays still proceed.
- **Evidence:** save the resulting account status, retained consent history, household details, notices, and registration IDs. The fake-clock suite covers midnight in the church's zone, daylight saving changes, leap birthdays, repeated and concurrent runs, full places, confirmed-link history, and rollback/retry when a registration holder is inactive.

## Gotchas

- There is no public button or API to advance birthdays. Do not change the host clock or seed a production database for proof.
- **Database-assisted `age18` drive (isolated verify database only).** The real job can run once in the browser's database: with the teen granted consent and the verify process paused as in the README's Mail steps, run `psql "$(printf %s "$VERIFY_DATABASE_URL" | tr -d '\\')" -c "update users set birth_date = '<church-local today minus 18 years>' where email = '<teen>'" -c "delete from jobs where kind = 'accounts.age_rollover'"`, then start the dev process from that section (it queues the day's daily jobs at start). Its log (`$WORK/dev-outbox-server.log`) prints `age rollover: updated 1 people`; the user is now `adult`/`active`. Then drive the admin and login steps above against `VERIFY_URL=http://127.0.0.1:$DEV_PORT`. Report it as database-assisted, not as a pure browser drive: the birth date and the job row were set with psql. Stop the dev process and resume the verify process afterwards.
- The `child13` notice and `staged-birthday` transfer have no browser drive here; they are proven by the fake-clock Go tests, so say so rather than reporting them as browser-verified.
- A passwordless adult still needs password setup. A separate administrator suspension still blocks sign-in.
- A child turning 13 receives guidance through their confirmed guardians. The job leaves their saved kind unchanged, so existing registrations remain manageable.
- A blocked staged transfer retries at startup and midnight. The job log identifies the user ID and refusal reason without contact details.
- A signed-in visit to `/waiting-for-consent` goes to `/`; open it signed out.
