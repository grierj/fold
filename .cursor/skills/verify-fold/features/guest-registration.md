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
- `Verify Admin` has created a public group with a future public event date (use the directory and schedule-event recipes), then signed out.
- Eval `verify-fold env "$RUN_ID"`. Load Playwright with `createRequire` from `$FOLD_ROOT/web/package.json`.
- Save files under `$VERIFY_EVIDENCE/guest-registration/`.

- **Open the form.** Signed out, open the event page and run `page.getByRole("button", { name: "Register as a guest" }).click()`. The form shows `Full name`, `Email`, an `I am 18 or older` checkbox, and an `Email me a confirmation link` button. Screenshot `guest-registration/form.png` with the Fold header in frame.
- **Trip the age gate.** Fill `Full name` with `Verify Guest` and `Email` with `verify-guest@example.com`, leave the checkbox unticked, and choose `Email me a confirmation link`. The page says to ask a parent or guardian to register you, and both fields keep their values. Screenshot `guest-registration/age-gate.png`.
- **Submit.** Run `page.getByLabel(/I am 18 or older/).check()` and choose `Email me a confirmation link` again. The page answers `Thanks — check your email for next steps.` and says the registration is confirmed when the emailed link is opened. Screenshot `guest-registration/submitted.png`.
- **Prove the boundary in the verify database.** Run `psql "$VERIFY_DATABASE_URL" -c "SELECT email FROM guest_signups; SELECT email FROM users WHERE email='verify-guest@example.com'; SELECT count(*) FROM registrations;"` and save it as `guest-registration/signups.txt`. It shows one signup row, no user row, and no registration. Nothing is an attendee yet.
- **Record the entry point.** `guest-registration/result.txt` names the sub-features driven and the entry point used (event page or group page). A proof from one entry point does not cover the other.

## Gotchas

- The verify instance runs `APP_ENV=prod` with no mail sender, so the verification email is never delivered there and `guest-confirm` and `guest-manage` cannot be driven end to end in this environment. Their live proof is the Go handler tests (`TestGuestRegistrationFlow` and neighbours in `api_guest_test.go`), which read the real links out of the devoutbox. Do not report the confirm or cancel halves as browser-verified from a verify-fold run.
- Opening `/guest/confirm?token=bogus` and pressing `Confirm my registration` proves only the bad-link message. That is what `web/e2e/guest-registration.spec.ts` covers, for the same no-sender reason.
- The confirm page confirms nothing on load. The button posts the token, so a screenshot of the landed page is not a confirmation.
- The guest button appears only on public dates for a signed-out visitor. If it is missing, check the event's own visibility first, not the group's.
- Submitting again with the same email is refused silently after 3 verification emails in an hour. Use a fresh address per run.
