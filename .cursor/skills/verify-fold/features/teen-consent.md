# Teen consent

An admin records separate account, SMS, and photo consent, revokes consent, and reviews its history. A teen waiting for consent enters Fold after an account grant. Account revocation ends their session.

## Sub-features

- `admin-consent`: evidence-required grant, revoke confirmation, scoped status, and durable history.
- `approval-consent`: an admin approves a teen request and records account consent in one action.
- `waiting-activation`: a restricted teen session enters Fold through Check account status after consent is granted.
- `consent-suspension`: consent never clears an independent administrator suspension.

## How to get to it (user POV)

- Admin → Users → More for the teen → Manage consent.
- Admin → Approvals → teen account request → Grant account consent with approval.
- Teen login → Waiting for consent → Check account status.
- Admin → Users → More for a former teen → Consent history.

## Driving it with verify-fold

Preconditions: follow the verification map baseline, launch with `verify-fold up`, require `verify-fold doctor "$RUN_ID"` to print `ok`, and load the exported run environment. Create a teen account through Admin New user with a password. Keep the teen signed in in a separate browser context.

- **Grant:** open the teen's More menu and Manage consent. Click `getByRole('button', {name:'Grant consent'})` without evidence. Check that Consent evidence receives focus. Choose the SMS radio and fill Consent evidence, then grant. The teen's Check account status stays on Waiting for consent. Choose Account access, record evidence, and grant. The same teen session enters Fold through Check account status.
- **Revoke:** open Consent history and check both evidence records. Click Revoke consent with Account access chosen. Accept the confirmation. Check Account access consent revoked, then check the teen's `/api/v1/auth/me` response is 401. Reload Admin and reopen history. Both grants and the revocation remain.
- **Approve:** create a teen request through Join. In Admin Approvals, choose Grant account consent with approval. Approve without evidence and check focus stays on Consent evidence. Fill the field and approve. The request disappears. Find the account in Users and check the consent history contains the approval evidence. A passwordless account still needs password setup.
- **Suspension:** disable an active, consented teen through the Users More menu. Revoke and grant their account consent. The consent panel still says an admin has disabled the account. Enable through Users. The account becomes active only with current account consent.
- **Evidence:** capture required-evidence, granted-history, revoked, and approval-with-consent states at 390, 768, and 1280 pixels. Run verify-ui measurements and axe, inspect each 390-pixel image, and save a result.txt with the entry points, session result, and persisted history.

## Gotchas

- Use verify-fold's isolated database. Never run interactive proof against the development or shared e2e server.
- SMS consent does not grant account access and does not replace the teen's own SMS opt-in. SMS and galleries arrive in later milestones.
- A first account grant keeps the waiting session valid. Revocation and independent suspension invalidate it.
- A former teen's history remains readable. New consent changes require a teen account.
