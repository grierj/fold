# Guest registration

Guest registration lets a signed-out adult register for one date of a public event with a name and an email. Fold emails a confirmation link, and nothing is registered until that link is redeemed. The confirmation email then carries a manage link that cancels the registration without an account.

## Sub-features

- `guest-open` opens the guest form on a public event date.
- `guest-age-gate` refuses a submit without the 18-or-older tick and keeps the typed fields.
- `guest-submit` answers with the usual check-your-email message and stores only the unverified signup.
- `guest-confirm` redeems the emailed link from its confirm button and shows the registered state.
- `guest-manage` opens the manage link and cancels the registration.

## How to get to it (user POV)

- Open a public event at `/events/<id>` while signed out and choose `Register as a guest` on a date.
- Open a public group's page while signed out and choose `Register as a guest` on one of its public dates.
- Open the confirmation link from the verification email, which lands on `/guest/confirm`.
- Open the manage link from the confirmation email, which lands on `/guest/manage/<token>`.

## Driving it with verify-fold

Preconditions:

- `verify-fold doctor "$RUN_ID"` prints `ok`.
- A public group has a future public event date, and nobody is signed in (the event-page recipe's `Sunday supper` / `Segmented night` fits; the event-visibility recipe leaves `Wednesday class` invite-only, so do not use it).
- Eval `verify-fold env "$RUN_ID"`. Load Playwright with `createRequire` from `$FOLD_ROOT/web/package.json`.
- Save files under `$VERIFY_EVIDENCE/guest-registration/`.

- **Open the form.** Signed out, open the event page and run `page.getByRole("button", { name: "Register as a guest" }).first().click()` (a series has one button per date). The form shows `Full name`, `Email`, an `I am 18 or older` checkbox, an unticked `Also ask for a Fold account` checkbox, and an `Email me a confirmation link` button. Screenshot `guest-registration/form.png`.
- **Trip the age gate.** Fill `Full name` with `Verify Guest` and `Email` with `verify-guest@example.com`, leave the checkbox unticked, and choose `Email me a confirmation link`. The checkbox gets focus and `aria-invalid="true"`, the inline message `Check this box to continue.` appears, nothing is posted, and both fields keep their values. The note that a parent or guardian can register a younger person from their own account is always on the form. Screenshot `guest-registration/age-gate.png`.
- **Submit.** Run `page.getByLabel(/I am 18 or older/).check()` and choose `Email me a confirmation link` again. The page answers `Thanks — check your email for next steps.` and says `Your registration is confirmed when you open the link in that email.` Screenshot `guest-registration/submitted.png`.
- **Prove the boundary in the verify database.** Run `psql "$VERIFY_DATABASE_URL" -c "SELECT email FROM guest_signups; SELECT email FROM users WHERE email='verify-guest@example.com'; SELECT count(*) FROM registrations;"` and save it as `guest-registration/signups.txt`. It shows one signup row and no user row. The registration count is whatever it was before the submit (note it first; other recipes leave rows). Nothing is an attendee yet.
- **Confirm and manage (mail step).** These need the dev outbox: follow "Mail steps" in [README.md](./README.md), submit with a fresh address (`verify-guest3@example.com`) on the dev-mode origin, and read the confirmation email from `/dev/outbox`. Open the `/guest/confirm?token=…` link: the heading `Confirm your registration` explains that the link works once and expires in 24 hours, and the page has confirmed nothing yet. Choose `Confirm my registration`. The page reads `You're registered for <title> with <group>, <date> at <time>.` with `Manage or cancel` and `View the event`. A second email carries `/guest/manage/<token>`. Open it: `Your guest registration`, the event and date, and `You're registered as going.` Choose `Cancel my registration` and accept the browser's confirm (`Cancel your registration for <title>?`). The page reads `Your registration is cancelled.` with `Register again`.
- **Bad links.** On any origin, `/guest/confirm?token=bogus` then `Confirm my registration` shows a message that the link expired or was already used; `/guest/confirm` with no token says `This page needs the link from your email.`; `/guest/manage/bogus` shows `Your guest registration` and the same expired-link message.
- **Record the entry point.** `guest-registration/result.txt` names the sub-features driven and the entry point used (event page or group page). A proof from one entry point does not cover the other: on the group page the same `Register as a guest` button is on each public date card.

## Gotchas

- The prod verify instance has no mail sender, so on it `guest-confirm` and `guest-manage` can only be reached through the mail steps above. A run that only submitted on the prod origin proves `guest-submit` and the boundary, not the confirm or cancel halves.
- The confirm page confirms nothing on load. The button posts the token, so a screenshot of the landed page is not a confirmation.
- The guest button appears only on public dates for a signed-out visitor. If it is missing, check the event's own visibility first, not the group's.
- Submitting again with the same email is refused silently after 3 verification emails in an hour. Use a fresh address per run.
- Pressing `Confirm my registration` shows `Confirming…` for a moment. Wait for the next page text (`Your guest registration` after following the manage link) rather than reading the page right after the click.
