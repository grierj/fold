# Fold milestones: Phase 4

### 14.7 Phase 4: Setup and providers

**M4.1 Encrypted provider settings**
- Depends on: M3.2
- Scope: `FOLD_SECRET_KEY`, AES-GCM helpers, `ProviderConfig` table, env override, admin API that never returns secrets (C8). Accept hex or base64 keys. Today every provider setting comes from the environment (`FOLD_EMAIL_PROVIDER`, `FOLD_POSTMARK_*`, `FOLD_SMTP_URL`), and the one encrypted setting, the VAPID private key, uses a key derived from `SESSION_SECRET` (`deriveKey`).
- Acceptance:
  - [ ] Secrets are unreadable in a database dump (a test greps the dump)
  - [ ] A wrong key gives a clear startup error, not garbage
  - [ ] An env variable overrides the database value, and the UI shows "set by environment"
  - [ ] `fold secrets rotate --new-key` re-encrypts all rows
- Tests: round-trip, tamper detection, rotation.

**M4.2 SPA setup wizard and setup state**
- Depends on: M4.1, M1.3
- Scope: `SetupState`; the first-admin gate reads it from the database on each request; the SPA wizard at `/admin/setup` for steps 1, 2, 5–8 and 10 of 11.3; the setup checklist card. Today first-run setup is the server-rendered `/setup` page (`templates/setup.html`), unlocked by `FOLD_SETUP_TOKEN` (M7.1). It asks for the church name, time zone and first admin, and creates them in one transaction under an advisory lock (`createFirstAdmin`, `setup.go`). Once any user exists, each process caches "setup done" until restart.
- Acceptance:
  - [ ] Wizard state survives a browser close and an app restart, and resumes at the right step
  - [x] With two app processes, only one first admin can be created (the gate is not a cached flag) (already true of `/setup`: the transaction counts users under the lock, `TestConcurrentSetupCreatesOneAdmin`)
  - [ ] Step 1 sets the account policy (`request` by default, or `off`), and the first admin confirms they are 18 or older (`/setup` already requires the 18-or-older confirmation; the policy is the `self_signup` admin setting today)
  - [ ] The safeguarding step sets `guardian_copies` and is audited (the setting and its audited admin switch exist, M3.5b)
- Tests: API tests for each step; Playwright run-through.
- Later task (DEC-77): an admin setting `calendar_unanswered_window_days` (30, 60 or 90; default 30) for how far ahead unanswered public and church-wide dates show in Just mine, personal feeds and Everything, audited like other settings. Today the window is a fixed 30 days (`internal/policy/calendar.go`).
- Carry-overs:
  - Keep the wizard lazy-loaded and within the bundle budget (`npm run bundle-size`), as the admin pages already are.
  - Keep the single "powered by Fold" header attribution when adding brand settings. Logo, accent color and header style belong to **M4.2b**.

**M4.2b Branding and asset customization** (draft; product DEC-137; Warden + Wright settled; `x/image` approved)
- Depends on: M4.2 (wizard and settings), existing public `storage.Store` uploads. S3 (M4.7) is not required; logos stay public like covers. Gilder's Fold-wide defaults (OG image, email header, category covers, favicon, push badge, signed-out hero, setup welcome) have shipped and stay the fallback: `web/public/assets/brand/` (served at `/assets/brand/`), `web/public/favicon.ico` and the covers in `web/src/assets/covers/` (DEC-66).
- Scope: let each church brand Fold with its own logo, an optional accent color, and a header style, without breaking CSP or same-origin rules and without a CDN. Admins only. Expand-and-contract via additive `Setting` keys (14.1). Thin slices (14.1): settings keys → upload/API and derivatives → admin UI and header → email / OG / manifest / favicon wiring → optional wizard step.
  - **Settings** (Wright, Nov 1): additive `Setting` keys only — `brand_logo_revision`, `brand_header_style`, `brand_accent` — no `BrandSettings` table. Missing keys mean today's look. Values are not stored in `SetupState` except step progress.
  - **Church logo (admin upload).** One source file. Server validates type and size (Warden rules below), generates the derivatives Fold needs, and serves them from versioned public upload keys. Email templates that show the logo use `alt` = the church name (`site_name`). Clearing the logo (clear revision) falls every surface back to Gilder's defaults. No remote image URLs (DEC-49).
  - **Derivative paths and replace** (Wright, Nov 1): write under `/uploads/brand/site/<rev>-*.png` (header, email, `og-1200x630`, icon sizes including maskable / apple-touch / favicon via the same store). Never overwrite a key; bump `brand_logo_revision` on each replace so long-cache is safe. Bundled DEC-66 files under `public/assets/brand/` remain Fold defaults only.
  - **Synchronous derivatives** (Wright, Nov 1): on upload, decode once, write all derivative blobs, then commit the new revision and audit in one short DB transaction. Commit the revision only after the files exist. On failure, leave the old revision live and delete orphan blobs. No job/queue for this rare admin path.
  - **Server resize** (Wright, Nov 1): there is no server-side resize path today (`internal/images` decodes and re-encodes avatars and covers to strip metadata, but never resizes them; the browser helper must not be the logo path). Add a small server helper with the standard library plus `golang.org/x/image/draw` only (proportional fit with padding, PNG-encode; compose the 1200×630 OG on a fixed background). **Approved** (Grier, Nov 1, 2026, 8:44 PM MT: "totally fine"; DEC-137). Do not pull a bigger imaging framework, ImageMagick/CGO, or refactor avatar/cover uploads in this milestone.
  - **Logo upload security** (Warden review, Nov 1): v1 accepts PNG and JPEG only (WebP optional); no SVG. A later SVG path, if ever added, must rasterize on the server to PNG and store only that PNG. Cap uploads at 2 MB with the request body limit before parse. Sniff the type from bytes, never from `Content-Type` or the filename. Read dimensions from the image header before a full decode; reject anything over `4096×4096` or over 16 megapixels. Always re-encode on the server (strips EXIF, GPS and other payloads); store only the re-encoded bytes and the resized derivatives Fold actually uses. Rate-limit about 10 uploads per admin per hour, plus a per-church cap. Admin-only writes with CSRF like other admin forms.
  - **Optional accent color** (DEC-137; Grier, Nov 1, 2026; wiring Wright, Nov 1): Admin may set one. Server checks WCAG AA contrast against white and against stone. A color that fails is auto-darkened until it passes, and the admin is shown the adjusted color. If unset, Fold Violet (`#7f47c7`) stays. The accent colors buttons, links and focus rings. Extend `GET /site` with accent (and logo / header style as needed). The client applies it with CSSOM `document.documentElement.style.setProperty` on the existing role tokens (`--color-primary` and related, plus focus if in scope), the same pattern as `--fold-header-height` in Layout; clearing restores Fold Violet. No `<style>` injection, no `unsafe-inline`, CSP unchanged.
  - **Header style** (Grier, Oct 14 direction; DEC-137): (1) church name only, with a tiny Fold credit kept in the footer; (2) church name with a quiet "powered by Fold" subscript (today's default); (3) big Fold mark with the church name. Manifest `name` / `short_name` rules stay as today (church name from `site_name`, DEC-124); only the icons swap when a logo is present.
  - **What is church-configurable vs Fold-wide.** Configurable: logo (and its derivatives), accent color, header style. Fold-wide defaults (Gilder, DEC-66): category covers, empty/error/setup illustrations, push badge, default favicon and OG image. No credentials in assets.
  - **Admin UI and wizard** (Wright, Nov 1): Settings live on an Admin branding page (or a Branding card on Admin Settings). Upload, preview, remove logo; pick header style; pick or clear accent. Optional skippable `SetupState` step `branding` right after church identity (after first-admin / §11.3 step 1 material), before Address and site, and well before Safeguarding (step 8); skipping branding must not skip safeguarding. Keep the first-admin form free of uploads.
  - **OG priority** (Wright, Nov 1): when those exist, content cover → church OG derivative → Fold default (`/assets/brand/og-default-1200x630.png`). Today the event-shell injector (`eventImageMeta`, `internal/httpx/spa.go`) always uses the Fold default. **M4.2b only** wires the event-shell injector's fallback to church derivative → Fold default; it does not invent event covers or pull group covers into this milestone.
  - **Serving and CSP** (Warden review, Nov 1): same-origin under the versioned upload keys; correct image `Content-Type`; `X-Content-Type-Options: nosniff`; `Content-Disposition: inline` with a safe filename. Keeps CSP `img-src 'self'` and `style-src 'self'`. No CDN.
  - **Audit log** (Warden review, Nov 1): every logo or branding change records actor, church, time, old and new content hashes, size, dimensions and type — never file bytes.
  - **Review gate:** when the code lands, Warden does a backend security review of the upload, re-encode, serving and audit path before merge.
- Acceptance:
  - [ ] With no logo and no accent, every surface matches today's Fold defaults (header, email, OG, favicon, manifest icons, push badge)
  - [ ] Branding uses `Setting` keys `brand_logo_revision`, `brand_header_style`, `brand_accent` (no new table); missing keys keep today's look
  - [ ] An admin can upload a logo; derivatives land at versioned `/uploads/brand/site/<rev>-*.png` and are never overwritten; revision commits only after files exist; `alt` on the email logo is the church name
  - [ ] Clearing the logo revision restores Gilder defaults on every surface without a redeploy
  - [ ] Accent unset → Fold Violet; a failing accent is auto-darkened until AA passes against white and stone, and the admin sees the adjusted color
  - [ ] Accent colors buttons, links and focus rings via CSS variables set from JS (Layout `/site` pattern); CSP unchanged
  - [ ] Each of the three header styles renders at 390 / 768 / 1280 without clipping the church name or breaking the 44px targets; style (1) still shows a tiny Fold credit in the footer
  - [ ] Non-admins cannot change branding (403); CSRF required on writes
  - [ ] Uploads reject SVG, oversize bodies (>2 MB), wrong sniffed types, and images over `4096×4096` or 16 MP; only re-encoded PNG/JPEG (or optional WebP) are stored
  - [ ] Served brand files are same-origin with correct `Content-Type`, `nosniff`, and safe inline `Content-Disposition`; CSP `img-src 'self'` holds
  - [ ] Rate limit (~10 uploads/admin/hour) and the per-church cap refuse excess uploads
  - [ ] Audit rows record actor, church, time, old/new content hash, size, dimensions and type, and never bytes
  - [ ] Wizard `branding` step is optional/skippable after church identity and before Address / Safeguarding, and resumes correctly after a browser close
  - [ ] OG fallback for M4.2b is church derivative → Fold default (content covers stay out of this milestone)
  - [ ] No CSP / CDN regression; `npm run build` and typecheck stay green
  - [ ] Warden backend security review of the landed upload/serve/audit path is done before merge
  - [ ] `golang.org/x/image` (`draw`) is the only new Go dependency for logo resize (approved Grier, Nov 1, 2026; DEC-137)
- Tests: upload reject/accept tests (SVG, type sniff vs spoofed headers, 2 MB body limit, dimension and megapixel caps, non-admin, CSRF, rate limit); re-encode strips EXIF/GPS; derivative smoke tests (revision not committed if a write fails); settings round-trip; header-style component tests at the three widths; email template golden or render test with logo `alt`; accent contrast unit tests and CSSOM apply/clear; OG fallback order; audit-log assertions (hashes present, no bytes); wizard step order.
- **Open questions:** none for M4.2b planning. `golang.org/x/image` is approved (Grier, Nov 1, 2026; DEC-137). Settled: DEC-137 product rules; Warden upload/serve/audit rules (Nov 1); Wright architecture (Nov 1). Remaining Warden work is the backend review when code lands.

**M4.3 Legal pages**
- Depends on: M4.2
- Scope: generated `/legal/privacy` and `/legal/sms-terms` with admin review (10.4). The privacy page states whether guardian copies are on.
- Acceptance:
  - [ ] Pages show church name, address, contact, message types, STOP/HELP, and "Msg & data rates may apply", and stay hidden until an admin accepts them
- Tests: golden-file tests.

**M4.4 Email provisioning**
- Depends on: M4.2, M3.2
- Scope: 11.3 step 3 with `EmailProvisioner` (Postmark; SMTP has no provisioning) and the Cloudflare and manual DNS providers.
- Acceptance:
  - [ ] With a Cloudflare token, DNS records are created and verification is polled to green with no manual steps (against fakes)
  - [ ] Without a token, the records table can be copied and is checked by DNS lookup
  - [ ] The webhook is created with a secret, and a test email arrives in the fake
- Tests: fakes for Postmark and Cloudflare.

**M4.5 People CSV import**
- Brief: [`briefs/audit-2026-11/R2-R3-R4-people.md`](../briefs/audit-2026-11/R2-R3-R4-people.md) (R2; generic spreadsheet first per DEC-143, ChurchTrac preset later).
- Depends on: M4.2, M1.10
- Scope: 11.3 step 9.
- Acceptance:
  - [ ] Rows under 13 are refused; teens start `pending_consent`; imported phones are not opted in
  - [ ] Two rows with one email become one account plus an offer to add the others as adult household people (DEC-23)
  - [ ] Imported adults get a set-password email, not a password in the file
- Tests: import tests with sample CSVs.

**M4.6 SMS provisioning**
- Depends on: M4.3, M3.7
- Scope: 11.3 step 4.
- Acceptance:
  - [ ] Nothing is bought unless the admin typed `BUY` in that request (a test asserts the fake got no purchase call otherwise)
  - [ ] Telnyx is preselected; skipping SMS leaves the rest of Fold working
  - [ ] The wizard asks about the EIN and sets `sms_registration_path`; it never asks for 501(c)(3) proof and never offers the Charity use case (DEC-3)
  - [ ] With no EIN on Telnyx, it explains an EIN is required and links the free IRS EIN page; it does not offer sole proprietor
  - [ ] Registration forms are pre-filled from settings and legal pages, and sample messages contain no donation asks
  - [ ] SMS sending is blocked until registration is approved, with a banner explaining why
- Tests: fakes for Telnyx.

**M4.7 S3 storage and private assets**
- Cut until a church asks (DEC-140). Private assets, when needed, land on local disk behind `storage.Store` first. Kept below as the scope record.
- Depends on: M4.1
- Scope: 5.7 and C9: `Asset` table, private `/files/<key>` handler, S3Store, `fold storage migrate`.
- Acceptance:
  - [ ] Private files cannot be fetched without permission (local and S3 signed URLs)
  - [ ] Moving local → S3 → local keeps every file and can resume after interruption
  - [ ] CI runs the S3 tests against a MinIO service container
- Tests: store contract suite run on both stores.

**M4.8 `fold setup` CLI**
- Depends on: M4.4, M4.5, M4.6 (no storage step: M4.7 is cut, DEC-140)
- Scope: non-interactive setup from `setup.yaml`, `--dry-run`, `--json` (11.4).
- Acceptance:
  - [ ] `--dry-run` makes no external calls and prints every planned action
  - [ ] Running the same config twice changes nothing the second time
  - [ ] Purchases still need `confirm_purchase: BUY` in the YAML plus an interactive confirmation, or the `--yes-buy` flag
- Tests: CLI golden-output tests.

**M4.9 Admin two-factor sign-in (TOTP)**
- Brief: [`briefs/audit-2026-11/R2-R3-R4-people.md`](../briefs/audit-2026-11/R2-R3-R4-people.md) (R3, including people/own-data export).
- Depends on: M4.1, M1.2
- Scope: TOTP (RFC 6238) for every admin account, required from v0.5 (final review, finding 7). Ten one-time recovery codes, stored hashed. The TOTP secret is encrypted like provider secrets (C8). An admin without TOTP can reach only the enrollment page. Another admin can reset a lost device; a sole admin uses a recovery code or `fold user reset-2fa` on the server.
- Acceptance:
  - [ ] A newly made admin must enroll before reaching any admin page; promoting someone to admin ends their other sessions
  - [ ] Sign-in needs a valid code (with a small clock-skew window), and each recovery code works once
  - [ ] Code attempts are throttled like passwords (M0.3)
  - [ ] Enrolling, resetting, and using a recovery code write `AuditLog` rows and end other sessions
- Tests: TOTP tests with a fake clock; API tests; a Playwright enrollment test.

