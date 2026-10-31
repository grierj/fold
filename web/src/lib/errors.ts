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
  group_upcoming_page: "Those dates couldn't be loaded. Reload the group and try again.",
  contact_history_filters: "Those history filters aren't valid. Check the dates and filters, then try again.",
  people_selection_unavailable: "That selection is no longer available. Search for the person again.",
  teen_add_approval_required: "Adding a teen needs their approval and an admin's approval. Ask your church office for help.",
  add_reason_long: "Your reason is too long. Use 1000 characters or fewer, then try again.",
  add_request_unavailable: "This ask to add is no longer available to you. Refresh the page to see its current status.",
  request_open_limit: "You have 10 open requests. Withdraw one or wait for a decision before sending another.",
  contact_unavailable: "This contact request or grant is no longer available to you. Refresh the page to see its current status.",
  date_add_approval_required: "This date needs their approval. Send an ask to add instead.",
  date_add_throttled: "You have made several date additions. Wait a minute, then try again.",
  contact_reason_long: "Your reason is too long. Use 1000 characters or fewer, then try again.",
  invalid_guardian_email: "Enter a parent or guardian's email address, different from your own.",
  consent_existing_account: "This email already has an account. Approve without granting consent, then review the account in Users.",
  consent_teen_only: "This account is no longer a teen account. Refresh the user list.",
  consent_evidence_required: "Record how you received consent before granting it.",
  consent_invalid: "Choose account, SMS or photos, then choose grant or revoke.",
  consent_evidence_long: "Keep the consent evidence to 1,000 characters or fewer.",
  teen_avatar: "Teen accounts do not have a public profile picture. You can still update your name and other profile details.",
  consent_required: "Your account is waiting for a parent or guardian's consent. Your church office can help. You can browse public groups and events without logging in.",
  household_conversion_details: "This person's details changed after they accepted. Reject this request and ask the household to send a new invitation.",
  household_conversion_adult: "This person no longer meets the account age and guardian rules. Check their birth date, account type, and guardian link. Reject this request before asking for a new invitation.",
  household_conversion_inviter: "The person who sent the invitation can no longer manage this household. Reject this request and ask an active household adult or admin to send a new invitation.",
  household_conversion_retry: "This household changed while you were approving the account. Try approving it again.",
  household_conversion_consent: "This account is waiting for consent and guardian confirmation. You can still manage sign-ups, care notes, and photo settings. Ask your church office for help with identity changes or removal.",
  household_conversion_unavailable: "This person can't create an account yet. Check their birth date and account type, or ask your church office for help.",
  household_conversion_email: "That email isn't available. Use a different email address.",
  household_conversion_person: "This household person was removed or already has an account. Reject this request; their household can ask for help if needed.",
  household_conversion_approval: "This account is waiting for an admin. Wait for their decision before sending another invitation.",
  household_conversion_open: "An invitation is already open. Cancel it before choosing a different email.",
  household_holder: "This household needs another active adult account. Choose an eligible account holder, not your own admin account.",
  household_exists: "This account already has a household. Use the existing household instead of creating another.",
  household_required: "This account has no household yet. Create its household before adding people.",
  household_guardian: "This person needs an eligible guardian. Choose an active adult account in the same household, not your own admin account.",
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
  password_too_long: "That password is too long. Use at most 72 bytes — about 72 letters, fewer with accents or emoji.",
  message_has_contact: "Leave email addresses and phone numbers out of the message.",
  room_name_taken: "There's already a room with that name in that building. Pick another name.",
  room_busy: "That room is already booked then. Pick another room, or ask an admin.",
  room_restricted: "Only an admin can book that room.",
  job_handled: "Someone already handled this task. Refresh to see the latest.",
  church_address_long: "Keep the address to 6 lines and 300 characters.",
  church_address_characters: "Take out any special characters, like tabs or symbols that don't print, and try again.",
  message_subject: "Add a subject on one line, up to 200 characters.",
  message_body: "Write your message first. If you already have, it's too long: shorten it to under about 50,000 characters.",
  message_nobody: "No one would get this yet. Choose people who can get email.",
  message_locked: "This message is already sending or sent, so it can't change. Write a new one if you need to send something else.",
  message_when: "Pick a time from now to a year ahead.",
  message_channel: "Texting isn't set up yet. Send this as an email.",
  message_not_yours: "You can't send to those people. Choose one of your own groups or events.",
  message_audience: "Choose who gets this message.",
  message_audience_gone: "That group or event isn't there anymore. Choose another.",
  message_merge_unknown: "One of the merge fields isn't one Fold knows. Use the fields under Insert field, spelled the same way.",
  message_merge_event: "Event fields like the event's title or time only work when you write to one event's people. Remove them, or choose an event.",
  message_test_limit: "You've sent 10 tests in the last hour. Try again a little later.",
  message_test_email: "We can't send to your email address right now, usually because earlier mail to it bounced. Ask an admin to check it, then try again.",
  message_test_failed: "We couldn't send the test. Try again in a few minutes.",
  message_daily_limit: "You've already sent 5 messages to these people today. Schedule this one for tomorrow, or ask an admin to send it.",
  event_reminder_time: "Pick a reminder time from the list: off, or 2 hours, 1 day, 2 days or 1 week before.",
  message_nothing_to_retry: "There's nothing to send again: everyone it could reach got it.",
  message_retry_refused: "Its author can no longer write to these people, so it can't be sent again. Write a new message if it still needs to go out.",
  message_retry_sending: "It's already sending again.",
  message_reminder: "Automatic reminders can't be changed here. To change when they go, edit the event's reminder time.",
  reminder_retry_refused: "This date has started or was cancelled, so its reminder can't be sent again.",
  reply_to_invalid: "Enter one email address, like office@yourchurch.org, or leave it empty.",
  email_kind_unknown: "That kind of email can't be changed here. Refresh and try again.",
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
  group_not_approved: "This group isn't approved yet. You can invite people once an admin approves it.",
  group_add_not_approved: "This group isn't approved yet. You can add people once an admin approves it.",
  group_has_members: "This group still has other members. Remove them or hand it to a new leader first.",
  removed_from_group: "A leader removed you from this group. Ask them if you'd like to come back.",
  leader_rule: "Only adults can be leaders, and every group needs at least one leader.",
  departure_unavailable: "That group departure isn't available anymore. Refresh the group and check the membership and leaders before trying again.",
  departure_stale: "Those departure details changed. Review the updated dates before trying again.",
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
  recurrence_too_many: "That repeat rule makes too many dates. Choose a rule that repeats at most a few times a day.",
  recurrence_too_frequent: "Events can repeat at most once a day. Choose a daily, weekly, or monthly repeat.",
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
  series_answer_invalid: "Choose Going, Maybe, or Can't go and try again.",
  series_scope_invalid: "Choose which future dates this answer should cover, then try again.",
  series_date_invalid: "That starting date isn't valid. Refresh the event and try again.",
  series_no_dates: "This series has no upcoming dates to answer. Refresh the event to see its current dates.",
  undo_token_required: "That Undo link is incomplete. Refresh the event and try again.",
  undo_other_actor: "That Undo belongs to someone else. Use Undo from your own answer.",
  undo_expired: "The 10-second Undo time has ended. You can change your answers again from the event.",
  undo_used: "That Undo was already used. Refresh the event to see your current answers.",
  undo_changed: "An answer changed after that action. Refresh the event before changing it again.",
  undo_authority_revoked: "You can no longer answer this series. Refresh the event or ask a leader for help.",
  undo_date_ended: "One of those dates is no longer open. Refresh the event to see the dates you can answer.",
  undo_not_found: "That Undo is no longer available. Refresh the event to see your current answers.",

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
