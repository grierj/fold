# Teen consent

An admin records separate account, SMS, and photo consent, revokes consent, and reviews its history. A teen waiting for consent enters Fold after an account grant. Account revocation ends their session.

## Sub-features

- `admin-consent`: evidence-required grant, revoke confirmation, scoped status, and durable history.
- `approval-consent`: an admin approves a teen request and records account consent in one action.
- `waiting-activation`: a pending teen browses public pages without logging in, then can log in after consent is granted.
- `consent-suspension`: consent never clears an independent administrator suspension.
- `active-teen-access`: a consented teen joins a general group and signs up for visible general and church-wide events while role restrictions remain.
- `teen-identity`: ordinary members cannot see teen roster identities; leaders, hosts, and admins see the teen name and badge without public contact details or avatars.
- `teen-calendar-status`: an existing personal calendar URL works only while the teen account is active.

## How to get to it (user POV)

- Admin → Users → More for the teen → Manage consent.
- Admin → Approvals → teen account request → Grant account consent with approval.
- Teen login → consent explanation → Browse public events; log in again after account consent is granted.
- Admin → Users → More for a former teen → Consent history.
- Active teen → general group → Join this group → event → Going.
- Active teen → Profile → Calendar feed → Create calendar link.

## Driving it with verify-fold

Preconditions: follow the verification map baseline, launch with `verify-fold up`, require `verify-fold doctor "$RUN_ID"` to print `ok`, and load the exported run environment. Create a teen account through Admin New user with a password. Use a separate browser context for the teen; before consent, correct credentials must explain consent without creating a login.

- **Grant:** open the teen's More menu and Manage consent. Click `getByRole('button', {name:'Grant consent'})` without evidence. Check that Consent evidence receives focus. Choose the SMS radio and fill Consent evidence, then grant. A teen login attempt still explains the consent requirement, while public events remain available. Choose Account access, record evidence, and grant. The teen can now log in normally; no pre-consent cookie becomes an authenticated session.
- **Revoke:** open Consent history and check both evidence records. Click Revoke consent with Account access chosen. Accept the confirmation. Check Account access consent revoked, then check the teen's `/api/v1/auth/me` response is 401. Reload Admin and reopen history. Both grants and the revocation remain.
- **Approve:** create a teen request through Join. In Admin Approvals, choose Grant account consent with approval. Approve without evidence and check focus stays on Consent evidence. Fill the field and approve. The request disappears. Find the account in Users and check the consent history contains the approval evidence. A passwordless account still needs password setup.
- **Suspension:** disable an active, consented teen through the Users More menu. Revoke and grant their account consent. The consent panel still says an admin has disabled the account. Enable through Users. The account becomes active only with current account consent.
- **Evidence:** capture required-evidence, granted-history, revoked, and approval-with-consent states at 390, 768, and 1280 pixels. Run verify-ui measurements and axe, inspect each 390-pixel image, and save a result.txt with the entry points, session result, and persisted history.
- **Active access:** after account consent is granted and the teen logs in, open a public group that is not a youth group and click Join this group. Open its general event and a church-wide event and click Going on each. Both event payloads report `myRegistration: "going"`. The teen cannot create groups or events, become a leader, host, or admin, or upload a public avatar. The acceptance scenario uses each real API boundary to prove these refusals.
- **Identity:** view the teen's sign-ups as an ordinary member, the group's leader, the church event's host, and an admin. Ordinary members see neither the teen's name nor a Teen badge. Manage sign-ups shows both to leaders, hosts, and admins. The group roster hides the teen from ordinary members and returns a Teen row with no avatar to managers. Event and roster payloads contain no teen email or phone.
- **Calendar:** create the teen's personal calendar URL while active and fetch it. Revoke account consent and check the same URL returns 404. Enabling without consent keeps it unavailable. Grant consent and check it returns 200. Independently disable the account, grant consent again, and check it remains unavailable until an admin enables the account. The token is unchanged throughout. These checks are rerunnable in `web/e2e/teen-access-acceptance.spec.ts`.

## Gotchas

- Use verify-fold's isolated database. Never run interactive proof against the development or shared e2e server.
- SMS consent does not grant account access and does not replace the teen's own SMS opt-in. SMS and galleries arrive in later milestones.
- Every account-status transition ends old sessions, including legacy pending cookies. After a first grant, log in afresh (DEC-85).
- A former teen's history remains readable. New consent changes require a teen account.
- The acceptance scenario creates its own general group, events, and accounts. It does not rely on a youth-only event or modify the shared baseline actors.
