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

Preconditions: launch an isolated instance with `verify-fold up`, complete setup, and create an adult and a teen through Admin: `/admin` → `Users` → `New user`, with `Password (optional)` filled so the account can sign in, `Their age` set to `18 or older` or `13 to 17`, and for the teen a `Birth date` that makes them 13 to 17. Sign in through the real forms when switching people.

- **Nominate.** Signed out, open `/join`, choose `13 to 17`, fill `Full name`, `Email`, `Birth date` and the optional `Parent or guardian's email`, and choose `Request an account`. The page says to check your email. Unknown nominations wait in the admin's approvals queue with instructions; no adult account is created.
- **Request a link.** Open the adult's **Profile**, then **Parent or guardian link**. Under **Teen consent**, use `page.getByLabel("Teen's email").fill(teenEmail)` and click `page.getByRole("button", { name: "Request guardian link", exact: true })`. Expect **If that teen has an account, an admin will review your guardian request.** The pending teen has no consent controls yet.
- **Confirm the link.** On Admin, locate `page.locator("[data-link-card]").filter({ hasText: teenName })`. Fill **How did you check?** and click **Confirm link**. Expect the pending card to disappear.
- **Grant consent.** Return to the adult's **Profile**, then **Parent or guardian link**. In `page.getByRole("region", { name: "Teen consent", exact: true })`, choose **Account** in **Consent** (a native select: `getByLabel("Consent", { exact: true }).selectOption("account")`), then click **Grant consent**. Expect **Account consent granted.** Choose **SMS** or **Photos** (`selectOption("sms")`, `selectOption("photos")`) to manage those scopes independently; each answers **Consent granted**.
- **Revoke consent.** Click **Revoke consent** and accept the confirmation dialog (`Revoke account consent for <teen>? They will be signed out and unable to use Fold until consent is granted again.`). Expect **Account consent revoked.** A teen session loses access; SMS and photo decisions stay as they were (`GET /api/v1/me/guardian-teens` shows `accountConsent` false and `smsConsent` and `photosConsent` true).
- **Review the email.** Open the emailed `/guardian-consent?token=...` URL (mail step: see "Mail steps" in [README.md](./README.md)). Expect `Review account consent`, `Give account consent for <teen>?`, the line `Opening this page has not given consent.` and the link's expiry. The admin consent history for the teen (`Consent history (0)` in Users → More → Manage consent) is unchanged before the click.
- **Submit the email.** Click **Grant account consent**. Expect **Account consent granted.** Reload the email URL and expect the alert `This link has expired`. The teen can now sign in.
- **Capture proof.** At 390 and 1280 px, capture the confirmed controls, revoked controls, email preview, email success, and used-link screen. Run axe and the shared UI measurements at each width. The rerunnable browser scenarios live in `web/e2e/guardian-consent.spec.ts`.

## Gotchas

- Production verification keeps `/dev/outbox` closed. Use the "Mail steps" dev-mode process in [README.md](./README.md), or a configured test SMTP inbox, to retrieve emails. The dedicated browser scenarios also run in dev mode against their own database to test the real outbox.
- A nomination does not create an adult account. The admin queue retains unknown nominations with instructions to help the adult join and request a link.
- An email becomes invalid after a later account-consent decision, guardian identity change, guardian suspension, link revocation, or the teen becoming an adult.
- A resend replaces the email for that teen's link. Emails for the guardian's other teens remain usable.
- Each scenario creates and resolves its own pending guardian task so it does not affect other approval-count checks.
- The Admin New user form's `Birth date` only appears after `13 to 17` is chosen.
