package main

import (
	"log"
	"net/http"

	"github.com/gin-gonic/gin"
)

// Error responses carry a stable "code" next to the English "error"
// (UI makeover PR 4, DEC-61). The SPA shows its own wording for the code
// (web/src/lib/errors.ts) and never the "error" text, which stays for logs
// and API clients. A message people can act on gets its own code below; any
// other message gets the code for its status, and the SPA says something
// general ("We couldn't save that. Please try again.").
//
// The table is keyed by the exact message, so call sites stay as they are.
// TestErrorCodesKeysAreUsed fails when a message is reworded and its key
// goes stale; TestWebErrorCopyCoversEveryCode keeps the SPA's map in step.
var errorCodes = map[string]string{
	// Signing in and passwords
	"invalid email or password":                          "bad_credentials",
	"too many sign-in attempts; try again in 15 minutes": "locked_out",
	"current password is incorrect":                      "wrong_password",
	"password must be at least 10 characters":            "password_too_short",
	"that link is invalid or has expired":                "link_expired",
	"that invitation is invalid or has expired":          "link_expired",
	"CSRF token invalid":                                 "session_expired",

	// Email addresses and accounts
	"enter a valid email address":                                      "invalid_email",
	"a valid email is required":                                        "invalid_email",
	"an account with that email already exists":                        "email_taken",
	"that email already has an account":                                "email_taken",
	"that email already has an account; sign in to register":           "email_taken_sign_in",
	"that email was already invited":                                   "already_invited",
	"invitation limit reached (20 per day)":                            "invite_limit",
	"self-signup is turned off; ask your church office for an account": "signup_off",
	"Fold accounts start at 13. A parent or guardian can add a younger child to their household and sign them up for events.": "under_13",
	"pick 18 or older, or 13 to 17 with a birth date that matches":                                                            "age_mismatch",
	"birth date must be a real date (YYYY-MM-DD)":                                                                             "invalid_birth_date",
	"Ask a parent or guardian to register you. Guests registering on their own must be 18 or older.":                          "guest_under_18",
	"cannot demote the last admin":                           "last_admin",
	"cannot disable the last admin":                          "last_admin",
	"cannot delete the last admin":                           "last_admin",
	"you cannot demote yourself":                             "not_yourself",
	"you cannot disable your own account":                    "not_yourself",
	"you cannot delete your own account":                     "not_yourself",
	"every group this person leads alone needs a new leader": "needs_successor",

	// Groups and members
	"This group is full":                                                                "group_full",
	"this group is invite only":                                                         "invite_only",
	"you can't join this group":                                                         "cannot_join",
	"only members can see who is in this group":                                         "members_only",
	"that group is no longer active":                                                    "group_inactive",
	"the group still has other members":                                                 "group_has_members",
	"A leader removed you from this group":                                              "removed_from_group",
	"you are the only leader — make someone else a leader first":                        "only_leader",
	"only adults can be leaders, and a group must keep at least one leader":             "leader_rule",
	"only adult members can be made leaders, and a group must keep at least one leader": "leader_rule",
	"leaders can't remove an admin, and nobody can remove the group's only leader (make someone else a leader first)": "remove_rule",
	"that request has expired":                       "request_expired",
	"that request needs review in the admin queue":   "needs_admin_queue",
	"that action can't be taken from a notification": "open_page_instead",

	// Events and registrations
	"This event is full":                                                                 "event_full",
	"that party doesn't fit in the seats left":                                           "party_too_big",
	"that event date is no longer open for guest registration":                           "date_closed",
	"a leader or host removed your registration for this date; ask them to add you back": "registration_removed",
	"a church-wide event needs at least one host":                                        "needs_host",
	"a church-wide event can't be invite-only":                                           "church_event_invite_only",
	"that visibility isn't allowed on this event":                                        "visibility_not_allowed",
	"that account can't host an event":                                                   "cannot_host",
	"teens can't host an event":                                                          "teen_host",
	"no account with that email":                                                         "no_account",
	"duration must be between 1 minute and 7 days":                                       "invalid_duration",
	"startsLocal must be YYYY-MM-DDTHH:MM":                                               "invalid_start",
	"capacity must be a positive whole number, or empty for no limit":                    "invalid_capacity",
	"guestsAllowed must be a whole number from 0 to 5, or empty for none":                "invalid_guests",
	"timezone must be an IANA name, for example America/Denver":                          "invalid_timezone",
	"video link must be a full https:// address":                                         "invalid_video_url",
	"videoProvider must be none, link, or jitsi":                                         "invalid_video_provider",
	"videoReveal must be always or registered_only":                                      "invalid_video_reveal",
	"video link is too long":                                                             "too_long",
	"this event doesn't offer childcare":                                                 "childcare_not_available",
	"childcare is full for this date":                                                    "childcare_full",
	"uncheck childcare instead of a 0 childcare limit":                                   "childcare_capacity_zero",
	"childcare limit must be a whole number from 1 to 100000, or empty for no limit":     "invalid_childcare_capacity",
	"childcare notes must be 500 characters or fewer":                                    "childcare_notes_too_long",
	"childcare ages must be 100 characters or fewer":                                     "childcare_ages_too_long",
	"childcareCount must be a whole number from 0 to 20":                                 "invalid_childcare_count",
	"childcare age note must be 200 characters or fewer":                                 "childcare_age_note_too_long",
	"remote attendance isn't available for this event":                                   "remote_not_available",
	"attendanceMode must be in_person or remote":                                         "invalid_attendance_mode",
	"remote seat limit must be a whole number from 1 to 100000, or empty for no limit":   "invalid_remote_capacity",
	"remote seat limit for this date must be a whole number from 0 to 100000, or empty":  "invalid_remote_capacity_override",
	"Uncheck remote attendance instead of a 0 remote seat limit":                         "remote_capacity_zero",

	// Households and guardian links (M1.9, DEC-32)
	"kind must be child, teen, or adult":                "invalid_kind",
	"first name is required":                            "missing_field",
	"care notes are too long":                           "too_long",
	"address is too long":                               "too_long",
	"a check note is required to confirm this link":     "check_note_required",
	"another admin must confirm your own guardian link": "self_confirm_blocked",
	"check note must be at most 500 characters":         "too_long",

	// Calendar feeds
	"scope must be mine or all": "invalid_feed_scope",

	// Uploads
	"file too large (max 5MB)":                         "file_too_large",
	"request too large":                                "file_too_large",
	"only JPEG, PNG, GIF, and WebP images are allowed": "file_type",
	"empty file":                                       "file_unreadable",
	"missing file":                                     "file_unreadable",
	"cannot read file":                                 "file_unreadable",

	// Fields that are too long or missing (the forms limit these already)
	"name is too long":                            "too_long",
	"description is too long":                     "too_long",
	"location is too long":                        "too_long",
	"tags are too long":                           "too_long",
	"title is too long":                           "too_long",
	"note is too long":                            "too_long",
	"reason is too long":                          "too_long",
	"recurrence rule is too long":                 "too_long",
	"name must be at most 100 characters":         "too_long",
	"display name must be at most 100 characters": "too_long",
	"phone must be at most 30 characters":         "too_long",
	"note must be at most 500 characters":         "too_long",
	"message must be at most 1000 characters":     "too_long",
	"name is required":                            "missing_field",
	"name is required (at most 100 characters)":   "missing_field",
	"email is required":                           "missing_field",
	"title is required":                           "missing_field",
	"timezone is required":                        "missing_field",
	"password is required":                        "missing_field",
}

// statusCodes name the general code for a status with no specific code.
var statusCodes = map[int]string{
	http.StatusBadRequest:            "invalid",
	http.StatusUnauthorized:          "signed_out",
	http.StatusForbidden:             "not_allowed",
	http.StatusNotFound:              "not_found",
	http.StatusConflict:              "conflict",
	http.StatusRequestEntityTooLarge: "file_too_large",
	http.StatusTooManyRequests:       "rate_limited",
}

// errorCode is the code for msg sent with status.
func errorCode(status int, msg string) string {
	if code, ok := errorCodes[msg]; ok {
		return code
	}
	if code, ok := statusCodes[status]; ok {
		return code
	}
	return "server_error"
}

// errorBody is the JSON for an error response: the English message and its
// code. A 5xx message is logged and replaced, so no internal detail leaves.
func errorBody(status int, msg string) gin.H {
	if status >= 500 {
		log.Printf("api error %d: %s", status, msg)
		return gin.H{"error": "server error", "code": "server_error"}
	}
	return gin.H{"error": msg, "code": errorCode(status, msg)}
}

func jsonError(c *gin.Context, status int, msg string) {
	c.JSON(status, errorBody(status, msg))
}

// jsonErrorCode answers with an explicit code, for a message built at run
// time (it names a number) that the table cannot key.
func jsonErrorCode(c *gin.Context, status int, code, msg string) {
	c.JSON(status, gin.H{"error": msg, "code": code})
}
