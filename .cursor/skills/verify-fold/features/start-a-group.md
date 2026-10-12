# Start a group

Start a group lets a signed-in person submit a group. An admin's group is live immediately. A member's group waits until an admin approves it.

## Sub-features

- `start-group-admin` creates a live group and opens it.
- `start-group-member` submits a group that shows `Pending approval` and stays out of the public directory.
- `start-group-signed-out` sends a visitor to log in instead of showing the form.

## How to get to it (user POV)

- Choose `Start a group` in the header.
- Open `/groups/new`.

## Driving it with verify-fold

Preconditions:

- `verify-fold doctor "$RUN_ID"` prints `ok`.
- The sign-in drive has created `verify-admin@example.com`.
- Eval `verify-fold env "$RUN_ID"`. Load Playwright with `createRequire` from `$FOLD_ROOT/web/package.json`.
- Save files under `$VERIFY_EVIDENCE/start-a-group/`.

- **Signed-out gate.** With no session, open `/groups/new`. The page says `You need to log in to start a group.` The `Group name` field is absent.
- **Admin create.** Sign in as `verify-admin@example.com` with password `password1234`. Choose `Start a group`. Fill `Group name` with `Admin's study`. Run `page.getByLabel("Group name").fill("Admin's study")` and `page.getByRole("button", { name: "Create group" }).click()`. The heading `Group created` is visible and `Pending approval` is absent. Choose `Go to the group`. The level-1 heading reads `Admin's study`.
- **Confirm it is listed.** Choose the `Fold` logo. A heading `Admin's study` is visible on `/`.
- **Member create.** As the admin, open `/admin`, choose `New user`, fill the email input `page.locator('input[type=email]')` with `sam-member@example.com`, fill `page.locator('input[required]').last()` with `Sam Member` (the Full name field is required and its label is not tied to the input), fill the password input with `password1234`, and choose `Create user`. The table shows `Sam Member`. Log out, sign in as `sam-member@example.com`, open `/groups/new`, fill `Group name` with `Sam's new study`, and choose `Create group`. The heading `Pending approval` is visible.
- **Confirm the member group is hidden.** Log out. On `/`, `Sam's new study` is not visible. Sign back in as the admin. The header `Notifications` button opens a link whose name matches `/1 group needs approval/`.
- **Proof.** `start-a-group/admin-created.png` shows `Group created` and the name `Admin's study`. `start-a-group/member-pending.png` shows `Pending approval` and `Sam's new study`. `start-a-group/result.txt` names which entry point was used (`Start a group` or `/groups/new`).

## Gotchas

- `Create group` is the submit button. `Cancel` returns to `/` and does not create a group.
- The `Group name` label is tied to its input. The Description, Location, and Tags labels are not. Use the tags placeholder `e.g. young-adults, bible-study` when a tag is required.
- The `Who can see this group` radios are wrapped by their labels. `page.getByRole("radio", { name: /^Public/ })` selects the public choice. The default is public.
- An admin sees `Group created`. A member sees `Pending approval` for the same button. Do not treat one outcome as proof of the other.
- The admin user form's Email, Full name, and Password labels are not tied to their inputs. Use the input types above rather than `getByLabel` for those fields.
