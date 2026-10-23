# Birthday rollover

Teen accounts become adult accounts on the church-local eighteenth birthday. Guardians receive one notice when a household child reaches 13. Existing history, household management, and capacity limits remain intact.

## Sub-features

- `age18`: account adulthood, waiting-session refresh, password setup, and consent history.
- `household-adult`: household account access at 18 without automatic guardian confirmation.
- `child13`: one guardian notice, with existing sign-ups and no new login.
- `staged-birthday`: suspended and unavailable-holder conversion behavior.

## How to get to it (user POV)

- A teen's eighteenth birthday → sign in, or Waiting for consent → Check account status.
- Guardian → Alerts → household age notice → Profile → My household.
- Admin → Users → former teen's More menu → Consent history.
- Admin → Users → Enable for a separately suspended staged account.

## Driving it with verify-fold

Preconditions: follow the verification map baseline, launch with `verify-fold up`, require `verify-fold doctor "$RUN_ID"` to print `ok`, and load the exported run environment. Birthday actions run automatically at church-local midnight. The deterministic checks use the same rollover function with a fake clock in `go test ./... -run 'TestAgeRollover|TestNextAgeRollover'` against isolated test databases.

- **Adulthood:** before the local birthday, a waiting teen's `getByRole('heading', {name:'Waiting for consent'})` remains visible. After rollover, click `getByRole('button', {name:'Check account status'})`. The same session enters Fold. The admin's Users row loses its Teen badge, and More → Consent history still shows the old consent records.
- **Household access:** after a converted teen reaches 18, open Profile and rename their household. The change saves. Adding a household child creates a pending link that still needs an admin's confirmation.
- **Guardian notice:** open Alerts as a confirmed guardian and follow the household age notice into Profile. The notice asks you to review the age group. The child keeps their existing sign-ups and has no account. Reading or archiving the notice does not create another notice on later days.
- **Suspended conversion:** after the staged person's eighteenth birthday, the household still sees and manages their adult household person while the account remains disabled. Admin Enable moves the existing registration IDs, answers, and waitlist places once. If the original responsible account is disabled, the move waits until that account is enabled. Other birthdays still proceed.
- **Evidence:** save the resulting account status, retained consent history, household details, notices, and registration IDs. The fake-clock suite covers midnight in the church's zone, daylight saving changes, leap birthdays, repeated and concurrent runs, full places, confirmed-link history, and rollback/retry when a registration holder is inactive.

## Gotchas

- There is no public button or API to advance birthdays. Do not change the host clock or seed a production database for proof.
- A passwordless adult still needs password setup. A separate administrator suspension still blocks sign-in.
- A child turning 13 receives guidance through their confirmed guardians. The job leaves their saved kind unchanged, so existing registrations remain manageable.
- A blocked staged transfer retries at startup and midnight. The job log identifies the user ID and refusal reason without contact details.
