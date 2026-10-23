import { ApiError } from "../api";

// What people read when something goes wrong (UI makeover PR 4, DEC-61).
// The API sends a stable code with every error; this is the only place
// that turns a code into words. The server's English "error" text is for
// logs and is never shown (guards_test.go checks that no page reads an
// error's .message). Each line says what happened and what to do next.
//
// DEC-52: errors nobody can act on, or that would reveal an account or a
// rate limit, never get here: those endpoints answer with their normal
// success message. Sign-in's lockout and the invitation "already has an
// account" are the deliberate exceptions.
export const ERROR_COPY = {
  consent_existing_account: "This email already has an account. Approve without granting consent, then review the account in Users.",
  consent_teen_only: "This account is no longer a teen account. Refresh the user list.",
  consent_evidence_required: "Record how you received consent before granting it.",
  consent_invalid: "Choose account, SMS or photos, then choose grant or revoke.",
  consent_evidence_long: "Keep the consent evidence to 1,000 characters or fewer.",
  teen_avatar: "Teen accounts do not have a public profile picture. You can still update your name and other profile details.",
  consent_required: "Your account is waiting for a parent or guardian's consent. Sign in to see the next steps.",
  invalid_guardian_email: "Enter a parent or guardian's email address, different from your own.",
  household_conversion_details: "This person's details changed after they accepted. Reject this request and ask the household to send a new invitation.",
  household_conversion_adult: "This person no longer meets the adult account rules. Check their birth date and account type. Reject this request before asking for a new invitation.",
  household_conversion_inviter: "The person who sent the invitation can no longer manage this household. Reject this request and ask an active household adult or admin to send a new invitation.",
  household_conversion_retry: "This household changed while you were approving the account. Try approving it again.",
  household_conversion_consent: "This account is waiting for consent and guardian confirmation. You can still manage sign-ups, care notes, and photo settings. Ask your church office for help with identity changes or removal.",
  household_conversion_unavailable: "This person can't create an account yet. Check their birth date and account type, or ask your church office for help.",
  household_conversion_email: "That email isn't available. Use a different email address.",
  household_conversion_person: "This household person was removed or already has an account. Reject this request; their household can ask for help if needed.",
  household_conversion_approval: "This account is waiting for an admin. Wait for their decision before sending another invitation.",
  household_conversion_open: "An invitation is already open. Cancel it before choosing a different email.",
  household_conversion_holder: "A responsible account isn't available. Ask your church office to check it before trying again.",
  household_person_converted: "This person has their own account now. Use their account instead.",
  household_link_pending: "Waiting for an admin to confirm. Ask your church office to check the household link before signing them up.",
  household_link_unconfirmed: "This household link isn't confirmed. Ask your church office to check it before signing them up.",
  household_birth_date_required: "Add a birth date in Profile before signing this person up.",
  household_age_mismatch: "The birth date doesn't match an adult. Check their birth date and choose Child or Teen in Profile if needed.",
  household_remote_childcare: "Remote attendees can't request childcare. Clear their childcare choice before switching to remote.",
  household_registration_holder_unavailable: "The responsible account can't sign this person up right now. Ask your church office to check their account and household.",
  // Signing in and passwords
  bad_credentials: "That email and password don't match. Check them and try again.",
  locked_out: "Too many sign-in attempts. Please wait 15 minutes, then try again.",
  wrong_password: "Your current password isn't right. Try typing it again.",
  password_too_short: "Your password needs at least 10 characters.",
  link_expired: "This link has expired or was already used. Ask for a new one and try again.",
  session_expired: "Your session timed out. Reload the page and try again.",
  signed_out: "You've been signed out. Please sign in again.",

  // Email addresses and accounts
  invalid_email: "That doesn't look like an email address. Check it and try again.",
  email_taken: "That email already has an account. They can sign in instead.",
  email_taken_sign_in: "That email already has an account. Sign in to register.",
  already_invited: "That email has already been invited. They can use the link in their email.",
  invite_limit: "You've sent 20 invitations today. You can send more tomorrow.",
  signup_off: "New accounts are paused right now. Ask your church office to set one up for you.",
  under_13: "Fold accounts start at 13. A parent or guardian can add a younger child to their household and sign them up for events.",
  age_mismatch: "Pick 18 or older, or 13 to 17 with a birth date that matches.",
  invalid_birth_date: "That birth date isn't a real date. Check it and try again.",
  guest_under_18: "Guests registering on their own must be 18 or older. Ask a parent or guardian to register you.",
  last_admin: "Fold needs at least one admin. Make someone else an admin first.",
  not_yourself: "You can't do that to your own account. Ask another admin.",
  needs_successor: "Some groups this person leads have no other leader. Pick a new leader for each one first.",

  // Groups and members
  group_full: "This group is full.",
  invite_only: "This group is invite only. Ask a leader to invite you.",
  cannot_join: "You can't join this group. Ask a leader if you think that's a mistake.",
  members_only: "Only members can see who's in this group.",
  group_inactive: "This group isn't active anymore.",
  group_has_members: "This group still has other members. Remove them or hand it to a new leader first.",
  removed_from_group: "A leader removed you from this group. Ask them if you'd like to come back.",
  only_leader: "You're the only leader. Make someone else a leader first.",
  leader_rule: "Only adults can be leaders, and every group needs at least one leader.",
  remove_rule: "Leaders can't remove an admin, and a group's only leader can't be removed. Make someone else a leader first.",
  request_expired: "That request has expired. Nothing was changed.",
  needs_admin_queue: "This one needs a closer look. Open the admin page to decide.",
  open_page_instead: "That can't be done from the bell. Open the page to do it there.",

  // Events and registrations
  event_full: "This event is full.",
  party_too_big: "There aren't enough seats left for everyone in your party. Try fewer guests.",
  too_many_guests: "That's more guests than this event allows. Try fewer.",
  date_closed: "This date isn't open for registration anymore.",
  registration_removed: "A leader or host removed your registration for this date. Ask them to add you back.",
  needs_host: "A church-wide event needs at least one host.",
  church_event_invite_only: "Church-wide events can be public or private, but not invite only.",
  visibility_not_allowed: "That visibility isn't available for this event. Pick another.",
  cannot_host: "That account can't host an event.",
  teen_host: "Hosts need to be adults.",
  no_account: "There's no account with that email. Check the spelling, or invite them first.",
  invalid_duration: "An event can last from 1 minute to 7 days.",
  invalid_start: "That start date or time isn't valid. Check it and try again.",
  invalid_capacity: "The seat limit needs to be a whole number, or leave it empty for no limit.",
  invalid_guests: "Guests allowed needs to be a number from 0 to 5.",
  invalid_timezone: "Pick a time zone from the list.",
  invalid_feed_scope: "That feed choice isn't available. Pick My events or Everything I can see.",
  invalid_kind: "Pick who this person is: a child, a teen, or an adult.",
  check_note_required: "Write how you checked this link first. A short note is fine, like “Known family; parent came to the office.”",
  self_confirm_blocked: "Another active admin needs to check your own guardian link. Ask them to review it.",
  invalid_video_url: "That video link needs to be a full address starting with https://. Paste the whole link and try again.",
  invalid_video_provider: "Pick no video link, paste a link, or a Jitsi link.",
  invalid_video_reveal: "Pick who sees the video link: registered people only, or everyone who can see the event.",
  childcare_not_available: "This event doesn't offer childcare. Set the number of children to 0 to save your answer.",
  childcare_full: "Childcare is full for this date. Your answers haven't changed. Try without childcare or ask a leader.",
  childcare_capacity_zero: "Uncheck childcare instead of a 0 childcare limit.",
  invalid_childcare_capacity: "The childcare limit needs to be a whole number from 1 to 100,000, or leave it empty for no limit.",
  childcare_notes_too_long: "The notes for parents are too long. Shorten them to 500 characters.",
  childcare_ages_too_long: "The childcare ages are too long. Shorten them to 100 characters.",
  invalid_childcare_count: "Enter a whole number from 0 to 20 children, then save your answer.",
  childcare_age_note_too_long: "The children's ages are too long. Shorten them to 200 characters.",
  remote_not_available: "This event doesn't offer remote attendance. Register in person instead.",
  invalid_attendance_mode: "Pick in person or remote.",
  invalid_remote_capacity: "The remote seat limit needs to be a whole number from 1 to 100,000, or leave it empty for no limit.",
  invalid_remote_capacity_override: "The remote seat limit for this date needs to be a whole number from 0 to 100,000, or leave it empty to use the event’s limit.",
  remote_capacity_zero: "Uncheck remote attendance instead of a 0 remote seat limit.",

  // Uploads
  file_too_large: "That picture is too big. Please use one under 5 MB.",
  file_type: "That file isn't a picture we can use. Try a JPEG, PNG, GIF, or WebP.",
  file_unreadable: "We couldn't read that file. Try another picture.",

  // Form fields
  too_long: "Some of what you typed is too long. Shorten it and try again.",
  missing_field: "Please fill in the required fields.",

  // General: the status's code when nothing more specific applies
  invalid: "Something in that didn't work. Check what you entered and try again.",
  not_allowed: "You don't have permission to do that.",
  not_found: "We couldn't find that. It may have been removed.",
  conflict: "Something changed while you were working. Reload the page and try again.",
  rate_limited: "Too many tries in a row. Please wait a few minutes, then try again.",
  server_error: "Something went wrong on our end. Please try again in a minute.",
} as const;

export type ErrorCode = keyof typeof ERROR_COPY;

const NETWORK = "We couldn't reach Fold. Check your connection and try again.";

function isCode(code: string): code is ErrorCode {
  return Object.prototype.hasOwnProperty.call(ERROR_COPY, code);
}

// errorText is what to show for a caught error. `fallback` is the page's
// own wording for this action ("We couldn't save your profile."), used for
// the general codes, where it says more than the general line does. A
// specific code always wins, and anything that isn't an API error (a
// dropped connection, a bug) gets the connection line.
export function errorText(e: unknown, fallback?: string): string {
  if (!(e instanceof ApiError)) return NETWORK;
  const code = isCode(e.code) ? e.code : "server_error";
  const general = code === "invalid" || code === "server_error" || code === "conflict";
  if (general && fallback) {
    return code === "server_error" ? `${fallback} Please try again in a minute.` : `${fallback} Please try again.`;
  }
  return ERROR_COPY[code];
}

// errorCode is the code of a caught API error, for pages that react to one
// (a full event, an expired link), or "" for anything else.
export function errorCode(e: unknown): ErrorCode | "" {
  return e instanceof ApiError && isCode(e.code) ? e.code : "";
}
