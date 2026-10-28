# Guardian consent

An adult requests a guardian link from Profile. An admin checks the link before the adult can grant or revoke a teen's account, SMS, or photo consent. Consent emails preview an account grant before the guardian submits it.

## Sub-features

- `guardian-nomination`: teen self-signup retains the nominated guardian email for admin review.
- `guardian-claim`: an adult requests a link without learning whether the supplied teen email exists.
- `guardian-confirm`: an admin confirms a pending link with a check note; DEC-79 applies.
- `guardian-consent`: a confirmed guardian grants or revokes each consent scope.
- `guardian-email`: a signed email previews an account grant; submitting grants once.

## How to get to it (user POV)

- Open **Join**, choose **13 to 17**, and enter **Parent or guardian's email**.
- As an adult, open **Profile**, then **Parent or guardian link**.
- As an admin, open **Admin**, then the pending guardian link in **Approvals**.
- Open the guardian consent email after an admin confirms the link, or choose **Email me a consent link** from Profile.

## Driving it with verify-fold

Preconditions: launch an isolated instance with `verify-fold up`, complete setup, and create an adult and a teen through Admin. Give the teen a password and a birth date aged 13 to 17. Sign in through the real forms when switching people.

- **Request a link.** Open the adult's **Profile**, then **Parent or guardian link**. Under **Teen consent**, use `page.getByLabel("Teen's email").fill(teenEmail)` and click `page.getByRole("button", { name: "Request guardian link", exact: true })`. Expect **If that teen has an account, an admin will review your guardian request.** The pending teen has no consent controls yet.
- **Confirm the link.** On Admin, locate `page.locator("[data-link-card]").filter({ hasText: teenName })`. Fill **How did you check?** and click **Confirm link**. Expect the pending card to disappear.
- **Grant consent.** Return to the adult's **Profile**, then **Parent or guardian link**. In `page.getByRole("region", { name: "Teen consent", exact: true })`, choose **Account** in **Consent**, then click **Grant consent**. Expect **Account consent granted.** Choose **SMS** or **Photos** to manage those scopes independently.
- **Revoke consent.** Click **Revoke consent** and accept the confirmation dialog. Expect **Account consent revoked.** A teen session loses access; SMS and photo decisions stay as they were.
- **Review the email.** Open the emailed `/guardian-consent?token=...` URL. Expect the teen's name, **Grant account consent**, and the explanation that opening the page has not given consent. The admin consent history is unchanged before the click.
- **Submit the email.** Click **Grant account consent**. Expect **Account consent granted.** Reload the email URL and expect the expired-link message.
- **Capture proof.** At 390 and 1280 px, capture the confirmed controls, revoked controls, email preview, email success, and used-link screen. Run axe and the shared UI measurements at each width. The rerunnable browser scenarios live in `web/e2e/guardian-consent.spec.ts`.

## Gotchas

- Production verification keeps `/dev/outbox` closed. Use a configured test SMTP inbox to retrieve emails. The dedicated browser scenarios also run in dev mode against their own database to test the real outbox.
- A nomination does not create an adult account. The admin queue retains unknown nominations with instructions to help the adult join and request a link.
- An email becomes invalid after a later account-consent decision, guardian identity change, guardian suspension, link revocation, or the teen becoming an adult.
- A resend replaces the email for that teen's link. Emails for the guardian's other teens remain usable.
- Each scenario creates and resolves its own pending guardian task so it does not affect other approval-count checks.
