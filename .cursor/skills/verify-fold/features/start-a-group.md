# Start a group

Start a group lets a signed-in person submit a group. An admin's group is live immediately. A member's group waits until an admin approves it.

## Sub-features

- `start-group-admin` creates a live group and opens it.
- `start-group-member` submits a group that shows `Thanks! An admin will review “<name>”.` and stays out of the public directory.
- `start-group-signed-out` sends a visitor to log in instead of showing the form.

## How to get to it (user POV)

- Choose `New group` in the header (on a phone, the `+ New group` pill).
- Open `/groups/new`.

## Driving it with verify-fold

Preconditions:

- `verify-fold doctor "$RUN_ID"` prints `ok`.
- The sign-in recipe has created `verify-admin@example.com`.
- Eval `verify-fold env "$RUN_ID"`. Load Playwright with `createRequire` from `$FOLD_ROOT/web/package.json`.
- Save files under `$VERIFY_EVIDENCE/start-a-group/`.

- **Signed-out gate.** With no session, open `/groups/new`. The browser goes to `/login` (heading `Log in to Fold`) and the `Group name` field is absent. Signing in from there returns to `/groups/new`.
- **Admin create.** Sign in as `verify-admin@example.com` with password `password1234`. In the main navigation choose `New group`. The heading `Start a group` is visible. Run `page.getByLabel("Group name").fill("Admin's study")` and `page.getByRole("button", { name: "Create group" }).click()`. The level-1 heading `Group created` is visible with `“Admin's study” is live.`, and no text about review. Choose `Go to the group` (`Schedule the first meeting` is the other choice). The level-1 heading reads `Admin's study`.
- **Confirm it is listed.** Choose `Home` in the main navigation. A heading `Admin's study` is visible in the `Groups` section.
- **Member create.** As the admin, open `/admin`, choose the `Users` tab (`page.getByRole("tab", { name: /^Users/ })`; `Approvals` is the default tab), choose `New user`, fill `page.getByLabel("Email (signs in with it)")` with `sam-member@example.com`, `page.getByLabel("Full name", { exact: true })` with `Sam Member` and `page.getByLabel("Password (optional)")` with `password1234` (a blank password makes an invited account that cannot sign in), choose `page.getByRole("radio", { name: "18 or older" })` under `Their age`, and choose `Create user`. The table shows `Sam Member`. Log out, sign in as `sam-member@example.com`, open `/groups/new`, fill `Group name` with `Sam's new study`, and choose `Create group`. The level-1 heading `Thanks! An admin will review “Sam's new study”.` is visible with `We'll let you know in Alerts when it's approved.` and a `Back to Home` link.
- **Confirm the member group is hidden.** Log out. On `/`, `Sam's new study` is not visible. Sign back in as the admin. The header bell, `page.getByRole("button", { name: /Alerts/ })`, is named `1 Alerts, 1 waiting on you`. Opening it shows a row `Sam Member started a group: Sam's new study` under `Waiting on you`.
- **Proof.** `start-a-group/admin-created.png` shows `Group created` and the name `Admin's study`. `start-a-group/member-pending.png` shows `Thanks! An admin will review` and `Sam's new study`. `start-a-group/result.txt` names which entry point was used (`New group` or `/groups/new`).

## Gotchas

- `Create group` is the submit button. `Cancel` returns to `/` and does not create a group.
- Every group form field is labelled (`Group name`, `Description (optional)`, `Location (optional)`); `getByLabel` works for each. `Tags (optional)` and `Member limit (optional)` sit under `More options`, which starts closed (`getByRole("button", { name: /^More options/ })`).
- The `Who can see this group` radios are wrapped by their labels. `page.getByRole("radio", { name: /^Public/ })` selects the public choice. The default is public.
- An admin sees `Group created`. A member sees the review message for the same button. Do not treat one outcome as proof of the other.
- `getByLabel("Full name")` also matches a `More for … Full Name` button once a user's name contains those words; pass `{ exact: true }`.
- The signed-out header has no `New group` link: it shows `Calendar`, `Sign up` and `Log in`.
