# Fold redesign inventory

Source: `web/src/App.tsx:81-123`, `main.go:271-276`. Baseline commit: `82323bf`.

All React routes are **app screens**: they serve discovery, attendance or management tasks. The signed-out Home has a promotional opening but remains the working group directory, not a new marketing site. Do not impose landing-page section counts, oversized heroes, stock photography or decorative motion. Keep routing and each component's logic in place.

Paths below are relative to `web/src/pages/` unless stated otherwise. Route parameters and the catch-all are preserved.

| Route | Owner | Classification |
|---|---|---|
| `/` | `Home.tsx` | App screen, public/member variants |
| `/login` | `Login.tsx` | App screen |
| `/waiting-for-consent` | `WaitingForConsent.tsx` | App screen |
| `/forgot-password` | `ForgotPassword.tsx` | App screen |
| `/reset-password` | `ResetPassword.tsx` | App screen |
| `/invite` | `Invite.tsx` | App screen |
| `/join` | `Join.tsx` | App screen |
| `/set-password` | `SetPassword.tsx` | App screen |
| `/verify-email` | `VerifyEmail.tsx` | App screen |
| `/cancel-email-change` | `CancelEmailChange.tsx` | App screen |
| `/guardian-consent` | `GuardianConsent.tsx` | App screen |
| `/guest/confirm` | `GuestConfirm.tsx` | App screen |
| `/guest/manage/:token` | `GuestManage.tsx` | App screen |
| `/email-settings/:token` | `EmailSettings.tsx` | App screen |
| `/groups/new` | `NewGroup.tsx` | App screen |
| `/groups/:id` | `GroupDetail.tsx` | App screen, role-dependent states |
| `/groups/:id/events/new` | `NewEvent.tsx` | App screen |
| `/events/new` | `NewChurchEvent.tsx` | App screen |
| `/events/:id` | `EventDetail.tsx` | App screen, role-dependent states |
| `/events/:id/dates/:date` | `EventDate.tsx` | App screen, exact-date access |
| `/calendar` | `Calendar.tsx` | App screen, agenda/week/month |
| `/calendar/rooms` | `RoomCalendar.tsx` | App screen |
| `/my-groups` | `MyGroups.tsx` | App screen |
| `/profile/:section?` | `Profile.tsx` | App screens, profile/settings sections |
| `/add-requests` | `AddRequests.tsx` | App screen |
| `/add-requests/:id` | `AddRequests.tsx` | App screen |
| `/contact-requests` | `ContactRequests.tsx` | App screen |
| `/contact-requests/:id` | `ContactRequests.tsx` | App screen |
| `/admin/contact-history` | `ContactHistory.tsx` | App screen |
| `/admin/users/:id/contact-history` | `ContactHistory.tsx` | App screen |
| `/admin` | `Admin.tsx` | App screens, query-selected tabs |
| `/notifications` | `Notifications.tsx` | App screen |
| `/notifications/setup` | `NotificationsSetup.tsx` | App screen |
| `/messages` | `Messages.tsx` | App screen |
| `/messages/new` | `MessageComposer.tsx` | App screen |
| `/messages/:id` | `MessageComposer.tsx` | App screen |
| `/messages/:id/read` | `MessageRead.tsx` | App screen |
| `*` | `NotFound.tsx` | App state |
| `/setup` | `templates/setup.html` (repo root) | Server-rendered app screen |
| `/dev/outbox` | `templates/outbox.html` (repo root) | Development-only app screen |
| `/dev/outbox/:id/html` | Generated email preview | Content preview, not SPA chrome |

## Shared surfaces

- `web/src/index.css`: semantic roles, self-hosted fonts and controls; also consumed by server templates.
- `web/src/components/Layout.tsx`: responsive shell, skip link, navigation, account and notification state.
- `web/src/components/ui/`: existing form, button, chip, menu, empty, error and loading contracts.
- Group cards, date badges, event cards, RSVP controls, calendars, notifications, household and admin components are shared across routes. Changes must preserve all callers and role variants.

## Baseline proof

- Isolated `verify-fold` run: `20261101T185403Z-3129426`.
- Screenshots and results: `/tmp/fold-verify-proof/20261101T185403Z-3129426/ui/`.
- 25 screen/state variants at 390, 768 and 1280 pixels: 75 checks, zero hard failures, no controls below 44px in the captured 390px views.
- Initial full verification stopped at a stale locator: `profile-requests` expects `Requests`, while the app renders `Contact and add requests`. The successful rerun explicitly excluded that entry; no tests were changed.
- Calendar, room calendar, tokenized email links, all admin/settings tabs and all dynamic states are **not** covered by the successful verify-ui run. More coverage is required for the redesign.
- Hallmark recorded source wiring in `.hallmark/before.json` and static findings in `.hallmark/before.txt`. Its renderer could not start without `puppeteer-core`; this is not a Hallmark render pass.

## Pending user direction

Ask once: what must stay; what feels wrong and the desired feeling; any new material. Until answered, only pre-flight artifacts exist. No app source changes, route deletions, component-tree replacements or test edits are authorized by this inventory.
