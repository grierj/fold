# Teen household conversion

A household teen gets an account without losing household management of their event sign-ups. Approval, account consent, and a fresh guardian-link check all precede the transfer.

## Sub-features

- `conversion-invite`: a household adult invites the teen to their own account from Profile → Household.
- `conversion-staging`: after approval, the household card waits; identity is read-only, removal is unavailable, and care notes stay editable.
- `conversion-transfer`: once consent and the fresh guardian check are both done, the same registration moves to the teen's account.

## How to get to it (user POV)

1. As a household adult, open **Profile**, then **Household**. Choose **Add a household member**, pick `Teen` and add a teen aged 13 to 17 with a birth date.
2. As an admin, confirm the original household guardian link (`/admin` → the `Household links` card → **How did you check?** → **Confirm link**).
3. Back on the adult's **Profile**, open **Household**, choose **Create their own account**, fill **Their email address**, and choose **Send account invitation**. The card says `Invitation pending: <email>`.
4. Open the invitation (`/invite?token=…`; it explains that the sign-ups move once consent is granted and the new guardian link is confirmed), fill **Choose a password**, and choose **Create my account**. The teen lands on `Your account is almost ready` and waits for admin approval. The household keeps all sign-ups.
5. As an admin, approve the account request (`/admin`, the card for the teen, `Grant account consent with approval`, evidence, **Approve**). The household card now says **Account waiting for consent and guardian confirmation**. The new teen account guardian link is pending, even though the original household link was confirmed.
6. Grant account consent with admin evidence (step 5 can do it) or as a confirmed guardian. Confirm the new teen-account link to an original household guardian (`/admin`, **Confirm link**). Either order works. Only the last prerequisite moves the sign-ups.
7. Sign in as the teen after all prerequisites are complete. Before then, correct credentials explain the consent requirement without creating a login; public groups and events remain available as a signed-out visitor. The account now has normal teen access. On the household adult's Profile, the teen appears under Accounts (`Accounts: <adult>, <teen>`).

## Driving it with verify-fold

Preconditions: follow the verification map baseline, launch with `verify-fold up`, require `verify-fold doctor "$RUN_ID"` to print `ok`, and load the exported run environment. Steps 3 and 4 need the invitation email, so follow "Mail steps" in [README.md](./README.md) and drive every step against the dev-mode origin. Create the adult (`teen-conversion-parent@example.com`, `Teen Conversion Parent`) through Admin → Users → New user, and use a public event that has a future date (a group page's weekly series works).

- **Register before conversion.** Add the household teen (`Taylor Conversion`) and have the admin confirm the link. As the adult open the event, tick `getByRole('checkbox', {name:'Taylor Conversion', exact:true})` and choose `Save answers`; the result line reads `Taylor Conversion: going in person`. Record the registration ID and answer (`SELECT id, status FROM registrations` in the verify database, or the event JSON's `myHouseholdRegistrations`). Verify the household can still manage the sign-up and update care notes and photo settings.
- **Invite.** On `/profile/household` choose `Create their own account`; the text `The guardian link for the new account needs a fresh admin check` is visible. Fill `Their email address` and choose `Send account invitation`; `Invitation pending: <email>` appears. Read the `/invite?token=…` link from `/dev/outbox`.
- **Accept.** In a separate browser context open the link, fill `Choose a password`, and choose `Create my account`. The heading `Your account is almost ready` is visible and `GET /api/v1/auth/me` returns 401.
- **Approve and stage.** As the admin open `/admin`, find the card for the teen (the request shows `Household account, invited by <adult>`), tick `Grant account consent with approval`, choose `Approve`, fill `Consent evidence`, and choose `Approve` again. As the adult, `/profile/household` shows `Account waiting for consent and guardian confirmation`. While approved staging waits, verify identity inputs are read-only (`First name` has the `readonly` attribute after `Edit <name>`) and removal is unavailable (`Remove <name>` is disabled). Another invitation cannot create another approved account. Save care notes (`Care notes: Care remains with household until ready`).
- **Pending check.** The teen's correct credentials show `waiting for a parent or guardian's consent` and no login; `GET /api/v1/events` stays 200.
- **Complete.** As the admin confirm the fresh guardian link on `/admin` (`Confirm link` with a check note). The teen now signs in (`GET /api/v1/auth/me` is 200). Verify the same registration ID and answer belong to the teen account (`registrations.user_id` is the teen's id, status `going`) and that group memberships did not move. On the adult's `/profile/household` the waiting text is gone and `Accounts: …` lists the teen.
- **Evidence.** Capture the invitation, staged household card, identity/care form, and completed account at 390 and 1280 px. The regression in `web/e2e/teen-conversion.spec.ts` saves both full-page and household-region images when `SCREENS_DIR` is set, checks overflow, and runs axe on the care form. Run the adult conversion regression in `web/e2e/household-conversion.spec.ts` alongside it.

## Gotchas

- Production verification keeps the dev outbox closed. Retrieve the invitation with the "Mail steps" dev-mode process in [README.md](./README.md), or from a configured test inbox. The dedicated browser regression uses dev mode and a private database to exercise the real outbox. Never reuse another run's database, port, or scratch directory.
- Approved staging does not expire. Independent admin suspension delays transfer while the household keeps management. At eighteen, the age job can finish an unsuspended account without teen consent or guardian checks. An unavailable responsible account defers that birthday candidate for retry rather than activating dormant seats.
- The admin's approval queue loads after the page does: wait for the card (`page.locator("[data-link-card]")` or the `Account request from <name>` group) instead of counting it right after `goto`.
- The fresh guardian-link card is the one for the teen's new account, not the original household link, which is already confirmed.
