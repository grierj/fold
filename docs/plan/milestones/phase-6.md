# Fold milestones: Phase 6

### 14.9 Phase 6: Community features (after v0.8)

Each item is one milestone. None is built yet. Each still needs acceptance criteria written into this file before work starts.

- M6.1 Directory with per-field privacy (10.2)
- M6.2 Sign-up sheets: volunteer, potluck, supplies (one model: sheets, slots, claims). Brief ready: [`briefs/audit-2026-11/R10-R14-features.md`](../briefs/audit-2026-11/R10-R14-features.md) (R14; after M6.3)
- M6.3 Attendance roll for leaders. Brief ready: [`briefs/audit-2026-11/R10-R14-features.md`](../briefs/audit-2026-11/R10-R14-features.md) (R10; pulled ahead of the rest of Phase 6, DEC-145)
- M6.4 (out, DEC-140) QR / self check-in: child check-in kiosks are confirmed out, not later.
- M6.5 Prayer requests
- M6.6 Group discussions
- M6.7 (moved to M1.9 and M1.10: households and teen accounts are core, DEC-8)
- M6.8 Background-check status flags on slots
- M6.9 Photo galleries (private by default)
- M6.10 (moved to M4.9: admin TOTP is required from v0.5)
- M6.11 ChurchTrac CSV mapping preset (a column preset on the generic M4.5 pipeline; still wants a real sample export, DEC-143)
- M6.12 Custom registration questions
- M6.13 (dropped: no translations or i18n framework, DEC-13)
- M6.14 (cut, DEC-140) Durable, tamper-evident audit log: the regular `AuditLog` is the answer; re-enters only for a named church with a legal retention need. Reasoning record stays at 10.7 / DEC-33.
- M6.15 (cut, DEC-140) Roster sync with church management software: generic CSV import (M4.5) is the answer; re-enters only for a named church. Research record stays at `research/chms-sync.md` / DEC-40.
- M6.16 Notification email digests (DEC-44). Later, not v1. A once-a-day email (7 AM recipient-local) of unread, unresolved notifications not already emailed: a `notification.digest` job beside today's `notify.deliver`, a `notification_settings` table with per-user `email_mode immediate|daily`, and the email-timing choice on the settings page. Weekly stays cut.

Built only when a church asks: Resend and Mailgun email adapters, a Twilio or Plivo SMS adapter, an S3 storage adapter, a Zoom video adapter, and a moderator role (DEC-17, DEC-140).

