# Teen household conversion

A household teen gets an account without losing household management of their event sign-ups. Approval, account consent, and a fresh guardian-link check all precede the transfer.

## How to get to it

1. As a household adult, open **Profile**, then **My household**. Add a teen aged 13 to 17.
2. As an admin, confirm the original household guardian link.
3. Back on the adult's Profile, choose **Create their own account** and send an invitation to the teen's own email.
4. Open the invitation, choose a password, and submit. The teen waits for admin approval. The household keeps all sign-ups.
5. As an admin, approve the account request. The household card now says **Account waiting for consent and guardian confirmation**. The new teen account guardian link is pending, even though the original household link was confirmed.
6. Grant account consent with admin evidence or as a confirmed guardian. Confirm the new teen-account link to an original household guardian. Either order works. Only the last prerequisite moves the sign-ups.
7. Sign in as the teen after all prerequisites are complete. Before then, correct credentials explain the consent requirement without creating a login; public groups and events remain available as a signed-out visitor. The account now has normal teen access. On the household adult's Profile, the teen appears under Accounts.

## Proof

- Before completion, record the registration ID and answer. Verify the household can still manage the sign-up and update care notes and photo settings.
- While approved staging waits, verify identity inputs are read-only and removal is unavailable. Another invitation cannot create another approved account.
- After completion, verify the same registration ID and answer belong to the teen account. Check that group memberships did not move.
- Capture the invitation, staged household card, identity/care form, and completed account at 390 and 1280 px. The regression in `web/e2e/teen-conversion.spec.ts` saves both full-page and household-region images when `SCREENS_DIR` is set, checks overflow, and runs axe on the care form.
- Run the adult conversion regression in `web/e2e/household-conversion.spec.ts` alongside it.

Production verification keeps the dev outbox closed. Retrieve the invitation from a configured test inbox. The dedicated browser regression uses dev mode and a private database to exercise the real outbox. Never reuse another run's database, port, or scratch directory.

Approved staging does not expire. Independent admin suspension delays transfer while the household keeps management. At eighteen, the age job can finish an unsuspended account without teen consent or guardian checks. An unavailable responsible account defers that birthday candidate for retry rather than activating dormant seats.
