package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"fold/internal/guardkit"

	"github.com/gin-gonic/gin"
)

// goSources is every non-test Go file's text, for the checks below.
func goSources(t *testing.T) string {
	t.Helper()
	var b strings.Builder
	production := guardkit.Production(t)
	for _, path := range production.Names() {
		b.WriteString(production[path])
	}
	return b.String()
}

// Every error the message-keyed table used to code is declared once, with the
// status it was sent with, the code the table gave it and its message.
func TestDeclaredAPIErrorsKeepTheirAnswers(t *testing.T) {
	for _, old := range oldErrorTable {
		found := false
		for _, e := range declaredAPIErrors {
			if e.Message == old.message && e.Status == old.status && e.Code == old.code {
				found = true
			}
		}
		if !found {
			t.Errorf("no declared apiError answers %d %q %q", old.status, old.code, old.message)
		}
	}
}

// Every declared error is pinned: either it is in the old table or here. These
// are the ones the old table did not hold: answers sent with jsonErrorCode or
// a status-only code, and the sentinels that now carry their own answer.
var newErrorTable = []struct {
	status  int
	code    string
	message string
}{
	{http.StatusConflict, "needs_reason_review", "the account has contact-request reasons to review"},
	{http.StatusConflict, "reason_review_changed", "the contact-request reasons changed since they were reviewed"},
	{http.StatusForbidden, "reason_review_not_allowed", "only an adult admin can review these reasons"},
	{http.StatusConflict, "reason_review_outdated", "the page that made this review is out of date"},
	{http.StatusConflict, "delete_reason_busy", "someone was changing a request that involves this person, so nothing was deleted"},
	{http.StatusServiceUnavailable, "reason_review_busy", "could not load the reasons"},
	{http.StatusUnauthorized, "signed_out", "unauthorized"},
	{http.StatusConflict, "admin_account_busy", "your own account was being updated, so nothing was changed"},
	{http.StatusForbidden, "not_allowed", "forbidden"},
	{http.StatusConflict, "attempt_closed", "this attempt was already used or settled"},
	{http.StatusServiceUnavailable, "attempt_busy", "too many attempts are being tracked"},
	{http.StatusConflict, "attempt_expired", "this attempt is too old to run"},
	{http.StatusInternalServerError, "delete_outcome_unknown", "could not confirm whether the account was deleted"},
	{http.StatusServiceUnavailable, "delete_timed_out", "deleting took too long, so nothing was deleted"},
	{http.StatusConflict, "delete_household_busy", "someone was changing this person's household, so nothing was deleted"},
	{http.StatusBadRequest, "invalid", "a teen account waiting for consent becomes active only when consent is granted"},
	{http.StatusNotFound, "not_found", "user not found"},
	{http.StatusNotFound, "not_found", "group not found"},
	{http.StatusBadRequest, "too_many_guests", "too many guests"},
	{http.StatusBadRequest, "invalid", "server error"},
	{http.StatusNotFound, "not_found", "event not found"},
	{http.StatusBadRequest, "recurrence_too_many", "that repeat rule makes too many dates"},
	{http.StatusBadRequest, "recurrence_too_frequent", "that repeat rule repeats more than once a day"},
	{http.StatusNotFound, "not_found", "household person not found"},
	{http.StatusNotFound, "not_found", "person not found"},
	{http.StatusNotFound, "not_found", "that date is not on this event"},
	{http.StatusNotFound, "not_found", "room not found"},
	{http.StatusNotFound, "not_found", "series not found"},
	{http.StatusInternalServerError, "server_error", "Session error"},
	{http.StatusInternalServerError, "server_error", "Failed to generate CSRF token"},
	{http.StatusInternalServerError, "server_error", "could not complete contact operation"},
	{http.StatusServiceUnavailable, "restore_detected", "the database was restored while Fold was running"},
}

// No declared error is unpinned: each is in one of the two tables.
func TestEveryDeclaredAPIErrorIsPinned(t *testing.T) {
	for _, e := range declaredAPIErrors {
		pinned := false
		for _, old := range oldErrorTable {
			pinned = pinned || (old.message == e.Message && old.status == e.Status && old.code == e.Code)
		}
		for _, n := range newErrorTable {
			pinned = pinned || (n.message == e.Message && n.status == e.Status && n.code == e.Code)
		}
		if !pinned {
			t.Errorf("declared apiError %d %q %q is in neither table; add it to newErrorTable", e.Status, e.Code, e.Message)
		}
	}
	for _, n := range newErrorTable {
		found := false
		for _, e := range declaredAPIErrors {
			found = found || (n.message == e.Message && n.status == e.Status && n.code == e.Code)
		}
		if !found {
			t.Errorf("newErrorTable row %d %q %q is not declared", n.status, n.code, n.message)
		}
	}
}

// A declared error is a real answer: an error status, a code and a message.
func TestDeclaredAPIErrorsAreComplete(t *testing.T) {
	for _, e := range declaredAPIErrors {
		if e.Status < 400 || e.Code == "" || e.Message == "" {
			t.Errorf("declared apiError %+v needs a 4xx/5xx status, a code and a message", e)
		}
	}
}

var (
	webCopyKeyRe = regexp.MustCompile(`(?m)^  ([a-z_0-9]+): "`)
	// apiErrf takes its code as the second argument, written out where the
	// error is built.
	runtimeCodeRe = regexp.MustCompile(`apiErrf\([^,]+, "([a-z_0-9]+)"`)
)

// Every code the API can send has words in web/src/lib/errors.ts, and the
// map has nothing the API never sends.
func TestWebErrorCopyCoversEveryCode(t *testing.T) {
	codes := map[string]bool{"server_error": true}
	for _, e := range declaredAPIErrors {
		codes[e.Code] = true
	}
	for _, c := range statusCodes {
		codes[c] = true
	}
	for _, m := range runtimeCodeRe.FindAllStringSubmatch(goSources(t), -1) {
		codes[m[1]] = true
	}
	for _, e := range householdRegistrationRefusals {
		codes[e.Code] = true
	}
	b, err := os.ReadFile("web/src/lib/errors.ts")
	if err != nil {
		t.Fatal(err)
	}
	web := map[string]bool{}
	for _, m := range webCopyKeyRe.FindAllStringSubmatch(string(b), -1) {
		web[m[1]] = true
	}
	var missing, extra []string
	for c := range codes {
		if !web[c] {
			missing = append(missing, c)
		}
	}
	for c := range web {
		if !codes[c] {
			extra = append(extra, c)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	if len(missing)+len(extra) > 0 {
		t.Fatalf("ERROR_COPY in web/src/lib/errors.ts: missing %v, never sent %v", missing, extra)
	}
}

// A coded message is written once, in its declaration. A second copy (a
// jsonError call, a helper's argument, a constant) would answer the general
// code for its status and the SPA would lose the words: send the declared
// value with fail instead. A declared value whose code is its status's general
// code (errActorForbidden, "forbidden") is the same answer either way.
func TestDeclaredMessagesAreWrittenOnce(t *testing.T) {
	coded := map[string]bool{}
	for _, e := range declaredAPIErrors {
		if e.Code != statusCode(e.Status) {
			coded[e.Message] = true
		}
	}
	parsed := guardkit.Production(t).Parse(0)
	for _, problem := range parsed.Problems {
		t.Error(problem)
	}
	declaration := map[*ast.BasicLit]bool{}
	for _, pf := range parsed.Package("main") {
		ast.Inspect(pf.AST, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			switch guardkit.Callee(call) {
			case "apiErr", "apiErrExtra", "apiErrf":
				for _, arg := range call.Args {
					if lit, ok := arg.(*ast.BasicLit); ok {
						declaration[lit] = true
					}
				}
			}
			return true
		})
	}
	checked := 0
	for _, pf := range parsed.Package("main") {
		ast.Inspect(pf.AST, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			checked++
			if msg, err := strconv.Unquote(lit.Value); err == nil && coded[msg] && !declaration[lit] {
				t.Errorf("%s: %q is a declared apiError's message; use the declared value (fail(c, ...)) instead of a second copy",
					pf.Path+":"+strconv.Itoa(parsed.Fset.Position(lit.Pos()).Line), msg)
			}
			return true
		})
	}
	if checked < 1000 {
		t.Errorf("only %d string literals seen; the guard is not reading the tree", checked)
	}
}

// apiErr registers its value for the tables, so it runs once, as a package
// variable's initializer, never inside a function (a handler would register
// a copy per request).
func TestAPIErrorsAreDeclaredAtPackageLevel(t *testing.T) {
	parsed := guardkit.Production(t).Parse(0)
	for _, problem := range parsed.Problems {
		t.Error(problem)
	}
	for _, pf := range parsed.Package("main") {
		if pf.Path == "api_errors.go" { // the constructors call each other
			continue
		}
		guardkit.InspectStack(pf.AST, func(n ast.Node, stack []ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			switch guardkit.Callee(call) {
			case "apiErr", "apiErrExtra", "apiErrWith":
			default:
				return true
			}
			for _, parent := range stack {
				if fn, ok := parent.(*ast.FuncDecl); ok {
					t.Errorf("%s: %s is called inside %s; declare it as a package variable (apiErrf is for messages built at run time)",
						pf.Path+":"+strconv.Itoa(parsed.Fset.Position(call.Pos()).Line), guardkit.Callee(call), fn.Name.Name)
				}
			}
			return true
		})
	}
}

func TestFailAnswersAnAPIError(t *testing.T) {
	answer := func(err error) (int, map[string]any) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		fail(c, err)
		var body map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		return w.Code, body
	}
	code, body := answer(errEventFull)
	if code != http.StatusConflict || body["code"] != "event_full" || body["error"] != "This event is full" || body["full"] != nil {
		t.Errorf("errEventFull: %d %v", code, body)
	}
	// A wrapped value answers as itself.
	code, body = answer(fmt.Errorf("saving: %w", errEventFull))
	if code != http.StatusConflict || body["code"] != "event_full" {
		t.Errorf("wrapped: %d %v", code, body)
	}
	// The same refusal with its extra key, and still equal to its base.
	code, body = answer(errEventFullNoWaitlist)
	if code != http.StatusConflict || body["full"] != true || !errors.Is(errEventFullNoWaitlist, errEventFull) {
		t.Errorf("errEventFullNoWaitlist: %d %v", code, body)
	}
	// An answerer builds its own, with data.
	code, body = answer(errNeedHostSuccessors{data: soleHostEventsJSON{Events: []soleHostEventJSON{}, Candidates: []successorJSON{}}})
	if code != http.StatusBadRequest || body["code"] != "needs_host_successor" || body["events"] == nil || !errors.Is(errNeedHostSuccessors{}, errNeedsHostSuccessor) {
		t.Errorf("errNeedHostSuccessors: %d %v", code, body)
	}
	code, body = answer(householdRegistrationError{code: "household_age_mismatch", personID: 7})
	if code != http.StatusConflict || body["code"] != "household_age_mismatch" || body["personId"] != float64(7) {
		t.Errorf("household refusal: %d %v", code, body)
	}
	// An unplanned error is logged, not sent: no detail leaves.
	code, body = answer(errors.New("pq: relation \"users\" does not exist"))
	if code != http.StatusInternalServerError || body["code"] != "server_error" || body["error"] != "server error" {
		t.Errorf("unplanned: %d %v", code, body)
	}
	// A declared 5xx is real copy on purpose (the deletion outcomes).
	code, body = answer(errDeleteOutcomeUnknown)
	if code != http.StatusInternalServerError || body["code"] != "delete_outcome_unknown" {
		t.Errorf("errDeleteOutcomeUnknown: %d %v", code, body)
	}
}

func TestErrorBody(t *testing.T) {
	cases := []struct {
		status    int
		msg, code string
	}{
		{http.StatusNotFound, "group not found", "not_found"},
		{http.StatusBadRequest, "invalid request body", "invalid"},
		{http.StatusForbidden, "forbidden", "not_allowed"},
		{http.StatusUnauthorized, "unauthorized", "signed_out"},
		{http.StatusConflict, "something else", "conflict"},
		{http.StatusRequestEntityTooLarge, "request too large", "file_too_large"},
	}
	for _, tc := range cases {
		b := errorBody(tc.status, tc.msg)
		if b["code"] != tc.code || b["error"] != tc.msg {
			t.Errorf("%d %q: got %v", tc.status, tc.msg, b)
		}
	}
	// A server error never sends its detail.
	b := errorBody(http.StatusInternalServerError, "pq: relation \"users\" does not exist")
	if b["code"] != "server_error" || b["error"] != "server error" {
		t.Errorf("500 leaked: %v", b)
	}
}

// The pre-change contents of the message-keyed error table that api_errors.go
// used to hold, with the status each message was sent with. Every value in
// the table must still be declared with exactly this status, code and message
// (TestDeclaredAPIErrorsKeepTheirAnswers), so declaring errors beside the code
// that raises them cannot change what a client sees.
var oldErrorTable = []struct {
	status  int
	code    string
	message string
}{
	{400, "add_reason_long", "add reason must be at most 1000 characters"},
	{403, "add_request_unavailable", "this ask to add is unavailable; refresh the request"},
	{400, "age_mismatch", "pick 18 or older, or 13 to 17 with a birth date that matches"},
	{409, "already_invited", "that email was already invited"},
	{401, "bad_credentials", "invalid email or password"},
	{400, "cannot_host", "that account can't host an event"},
	{403, "cannot_join", "you can't join this group"},
	{400, "check_note_required", "a check note is required to confirm this link"},
	{400, "childcare_age_note_too_long", "childcare age note must be 200 characters or fewer"},
	{400, "childcare_ages_too_long", "childcare ages must be 100 characters or fewer"},
	{400, "childcare_capacity_zero", "uncheck childcare instead of a 0 childcare limit"},
	{409, "childcare_full", "childcare is full for this date"},
	{400, "childcare_not_available", "this event doesn't offer childcare"},
	{400, "childcare_notes_too_long", "childcare notes must be 500 characters or fewer"},
	{400, "church_address_characters", "the address has a character that can't go in an email"},
	{400, "church_address_long", "the address must be at most 300 characters on 6 lines"},
	{400, "church_event_invite_only", "a church-wide event can't be invite-only"},
	{400, "consent_evidence_long", "consent evidence must be at most 1000 characters"},
	{400, "consent_evidence_required", "record the consent evidence before granting consent"},
	{409, "consent_existing_account", "that email already has an account; approve without consent and review the account separately"},
	{400, "consent_invalid", "choose account, sms or photos and granted or revoked"},
	{403, "consent_required", "your account is waiting for consent"},
	{409, "consent_teen_only", "consent is only available for teen accounts; refresh the user list"},
	{400, "contact_history_filters", "invalid contact history filters"},
	{400, "contact_reason_long", "contact reason must be at most 1000 characters"},
	{403, "contact_unavailable", "direct-contact access is unavailable; refresh the request"},
	{409, "date_add_approval_required", "adding this person needs approval; send an ask to add for this date"},
	{409, "date_add_throttled", "too many date additions; wait a minute before trying again"},
	{409, "date_closed", "that event date is no longer open for guest registration"},
	{409, "departure_stale", "this departure changed; review the updated dates before trying again"},
	{409, "departure_unavailable", "this departure is no longer available; refresh the group and check membership and leadership"},
	{400, "email_kind_unknown", "that kind of email can't be changed here"},
	{403, "email_link_turn_on", "this link can only turn email off; sign in or use a newer email to turn it on"},
	{409, "email_taken", "an account with that email already exists"},
	{409, "email_taken_sign_in", "that email already has an account; sign in to register"},
	{409, "event_full", "This event is full"},
	{400, "event_reminder_time", "reminderEmailMinutes must be 0 (off), 120, 1440, 2880 or 10080"},
	{400, "file_too_large", "file too large (max 5MB)"},
	{413, "file_too_large", "request too large"},
	{400, "file_type", "only JPEG, PNG, and GIF images are allowed"},
	{400, "file_unreadable", "cannot read file"},
	{400, "file_unreadable", "empty file"},
	{400, "file_unreadable", "missing file"},
	{409, "group_add_not_approved", "this group isn't approved yet; you can add people once an admin approves it"},
	{409, "group_full", "This group is full"},
	{400, "group_has_members", "the group still has other members"},
	{409, "group_inactive", "that group is no longer active"},
	{409, "group_not_approved", "this group isn't approved yet; you can invite people once an admin approves it"},
	{400, "group_upcoming_page", "invalid upcoming date page; reload the group"},
	{400, "guest_under_18", "Ask a parent or guardian to register you. Guests registering on their own must be 18 or older."},
	{409, "household_age_mismatch", "the birth date does not match an adult; correct it or choose child or teen in profile"},
	{409, "household_birth_date_required", "add a birth date in profile before signing this person up"},
	{409, "household_conversion_adult", "this person no longer meets the adult account rules; check their birth date and account type before sending a new invitation"},
	{409, "household_conversion_approval", "this account is waiting for an admin; wait for their decision"},
	{409, "household_conversion_consent", "this approved account is waiting for consent and guardian confirmation; ask your church office before changing their identity or removing them"},
	{409, "household_conversion_details", "this person's details changed after they accepted; ask the household to send a new invitation"},
	{409, "household_conversion_email", "that email is unavailable; use a different email"},
	{409, "household_conversion_holder", "a responsible account is unavailable; ask your church office to check it"},
	{409, "household_conversion_inviter", "the person who sent the invitation can no longer manage this household; ask an active household adult or admin to send a new invitation"},
	{409, "household_conversion_open", "cancel the current invitation before choosing a different email"},
	{409, "household_conversion_person", "this household person was removed or already has an account; reject this request"},
	{409, "household_conversion_retry", "this household changed while approving; try again"},
	{409, "household_conversion_unavailable", "this person cannot create an account yet; check their birth date, account type and guardian link"},
	{409, "household_exists", "this account already has a household; use its existing household"},
	{409, "household_guardian", "choose an active adult account in this household as guardian, not the acting admin"},
	{409, "household_holder", "choose another active adult account to hold this household"},
	{409, "household_link_pending", "waiting for an admin to confirm; ask your church office to check the household link"},
	{409, "household_link_unconfirmed", "this household link is not confirmed; ask your church office to check it"},
	{409, "household_person_converted", "this person has their own account; use their account instead"},
	{409, "household_registration_holder_unavailable", "the responsible account cannot register this person; ask your church office to check their account and household"},
	{409, "household_remote_childcare", "remote attendees cannot request childcare; clear the childcare choice first"},
	{409, "household_required", "create this account's household before adding people"},
	{400, "image_too_large", "that image is too large to process"},
	{400, "image_unsupported", "WebP images are not supported; use a JPEG or PNG"},
	{400, "invalid", "room capacity must be a whole number from 1 to 100000"},
	{400, "invalid", "setup and cleanup minutes must be from 0 to 240"},
	{400, "invalid_attendance_mode", "attendanceMode must be in_person or remote"},
	{400, "invalid_birth_date", "birth date must be a real date (YYYY-MM-DD)"},
	{400, "invalid_capacity", "capacity must be a positive whole number, or empty for no limit"},
	{400, "invalid_childcare_capacity", "childcare limit must be a whole number from 1 to 100000, or empty for no limit"},
	{400, "invalid_childcare_count", "childcareCount must be a whole number from 0 to 20"},
	{400, "invalid_duration", "duration must be between 1 minute and 7 days"},
	{400, "invalid_email", "a valid email is required"},
	{400, "invalid_email", "enter a valid email address"},
	{400, "invalid_feed_scope", "scope must be mine or all"},
	{400, "invalid_guardian_email", "enter a different valid email for the guardian"},
	{400, "invalid_guests", "guestsAllowed must be a whole number from 0 to 5, or empty for none"},
	{400, "invalid_kind", "kind must be child, teen, or adult"},
	{400, "invalid_remote_capacity", "remote seat limit must be a whole number from 1 to 100000, or empty for no limit"},
	{400, "invalid_remote_capacity_override", "remote seat limit for this date must be a whole number from 0 to 100000, or empty"},
	{400, "invalid_start", "startsLocal must be YYYY-MM-DDTHH:MM"},
	{400, "invalid_timezone", "timezone must be an IANA name, for example America/Denver"},
	{400, "invalid_video_provider", "videoProvider must be none, link, or jitsi"},
	{400, "invalid_video_reveal", "videoReveal must be always or registered_only"},
	{400, "invalid_video_url", "video link must be a full https:// address"},
	{429, "invite_limit", "invitation limit reached (20 per day)"},
	{403, "invite_only", "this group is invite only"},
	{409, "job_handled", "that task was already handled; refresh the list"},
	{400, "last_admin", "cannot delete the last admin"},
	{400, "last_admin", "cannot demote the last admin"},
	{400, "last_admin", "cannot disable the last admin"},
	{400, "leader_rule", "only adult members can be made leaders, and a group must keep at least one leader"},
	{400, "leader_rule", "only adults can be leaders, and a group must keep at least one leader"},
	{400, "link_expired", "that invitation is invalid or has expired"},
	{400, "link_expired", "that link is invalid or has expired"},
	{429, "locked_out", "too many sign-in attempts; try again in 15 minutes"},
	{403, "members_only", "only members can see who is in this group"},
	{400, "message_audience", "choose who gets this message"},
	{404, "message_audience_gone", "that group or event isn't there anymore"},
	{400, "message_body", "write a message of under about 50,000 characters"},
	{400, "message_channel", "texting isn't set up yet; send this as an email"},
	{400, "message_daily_limit", "you've sent 5 messages to these people today"},
	{400, "message_has_contact", "leave contact details out of the message"},
	{409, "message_locked", "this message is already sending or sent, so it can't change"},
	{400, "message_merge_event", "event merge fields only work in a message to one event's people"},
	{400, "message_merge_unknown", "use only the merge fields the composer offers"},
	{400, "message_nobody", "no one would get this message; choose people who can get email"},
	{403, "message_not_yours", "you can't send to those people"},
	{409, "message_nothing_to_retry", "there's nothing to send again; everyone it could reach got it"},
	{400, "message_push_only", "an announcement always goes by email"},
	{409, "message_reminder", "automatic reminders can't be changed here; change the event's reminder time instead"},
	{403, "message_retry_refused", "its author can no longer write to these people, so it can't be sent again"},
	{409, "message_retry_sending", "it's already sending again"},
	{400, "message_subject", "add a subject on one line, up to 200 characters"},
	{400, "message_test_email", "your own email address can't get mail right now"},
	{502, "message_test_failed", "we couldn't send the test"},
	{400, "message_test_limit", "you've sent 10 tests in the last hour"},
	{400, "message_test_opted_out", "this kind of email is turned off in your notification settings"},
	{400, "message_when", "pick a time from now to a year ahead"},
	{400, "missing_field", "email is required"},
	{400, "missing_field", "first name is required"},
	{400, "missing_field", "name is required"},
	{400, "missing_field", "name is required (at most 100 characters)"},
	{400, "missing_field", "password is required"},
	{400, "missing_field", "room name is required"},
	{400, "missing_field", "timezone is required"},
	{400, "missing_field", "title is required"},
	{403, "needs_admin_queue", "that request needs review in the admin queue"},
	{400, "needs_host", "a church-wide event needs at least one host"},
	{400, "needs_host_successor", "every church-wide event this person hosts alone needs a new host"},
	{400, "needs_successor", "every group this person leads alone needs a new leader"},
	{404, "no_account", "no account with that email"},
	{403, "not_allowed", "only an admin can change a booking's setup and cleanup time"},
	{403, "not_allowed", "only an admin can change this booking"},
	{400, "not_yourself", "you cannot delete your own account"},
	{400, "not_yourself", "you cannot demote yourself"},
	{400, "not_yourself", "you cannot disable your own account"},
	{403, "open_page_instead", "that action can't be taken from a notification"},
	{409, "party_too_big", "that party doesn't fit in the seats left"},
	{400, "password_too_long", "password must be at most 72 bytes"},
	{400, "password_too_short", "password must be at least 10 characters"},
	{400, "people_selection_unavailable", "that selection is unavailable; search for the person again"},
	{403, "push_church_off", "your church has turned off notifications on devices"},
	{403, "push_consent_required", "notifications wait until your account's consent is complete"},
	{404, "push_device_unknown", "this device doesn't have notifications on"},
	{403, "push_guest", "notifications on a device need a Fold account"},
	{400, "push_kind_locked", "that kind of notification can't be turned off"},
	{400, "push_service_unknown", "that browser's notification service isn't one we can send to"},
	{409, "registration_removed", "a leader or host removed your registration for this date; ask them to add you back"},
	{403, "reminder_retry_refused", "this date has started or was cancelled, or its event's reminders are off, so its reminder can't be sent again"},
	{400, "remote_capacity_zero", "Uncheck remote attendance instead of a 0 remote seat limit"},
	{400, "remote_not_available", "remote attendance isn't available for this event"},
	{403, "removed_from_group", "A leader removed you from this group"},
	{400, "reply_to_invalid", "enter one email address, or leave it empty"},
	{409, "request_expired", "that request has expired"},
	{409, "request_open_limit", "you already have 10 open requests; withdraw one or wait for a decision"},
	{409, "room_busy", "that room is already booked then"},
	{409, "room_name_taken", "a room with that name already exists in that building"},
	{403, "room_restricted", "only an admin can book this room"},
	{403, "self_confirm_blocked", "another admin must confirm your own guardian link"},
	{400, "series_answer_invalid", "status and scope are required"},
	{400, "series_date_invalid", "date must be YYYY-MM-DD"},
	{409, "series_no_dates", "this series has no future dates to answer"},
	{400, "series_scope_invalid", "scope must be all_future or this_and_following"},
	{403, "session_expired", "CSRF token invalid"},
	{403, "signup_off", "self-signup is turned off; ask your church office for an account"},
	{409, "teen_add_approval_required", "adding a teen needs their approval and an admin's approval; ask your church office for help"},
	{403, "teen_avatar", "teen accounts cannot have a public profile picture"},
	{400, "teen_host", "teens can't host an event"},
	{400, "too_long", "address is too long"},
	{400, "too_long", "building is too long"},
	{400, "too_long", "care notes are too long"},
	{400, "too_long", "check note must be at most 500 characters"},
	{400, "too_long", "description is too long"},
	{400, "too_long", "display name must be at most 100 characters"},
	{400, "too_long", "features is too long"},
	{400, "too_long", "location is too long"},
	{400, "too_long", "message must be 500 characters or fewer"},
	{400, "too_long", "message must be at most 1000 characters"},
	{400, "too_long", "name is too long"},
	{400, "too_long", "name must be at most 100 characters"},
	{400, "too_long", "note is too long"},
	{400, "too_long", "note must be at most 500 characters"},
	{400, "too_long", "notes is too long"},
	{400, "too_long", "phone must be at most 30 characters"},
	{400, "too_long", "reason is too long"},
	{400, "too_long", "recurrence rule is too long"},
	{400, "too_long", "tags are too long"},
	{400, "too_long", "the reason must be 500 characters or fewer"},
	{400, "too_long", "title is too long"},
	{400, "too_long", "video link is too long"},
	{400, "under_13", "Fold accounts start at 13. A parent or guardian can add a younger child to their household and sign them up for events."},
	{409, "undo_authority_revoked", "you can no longer answer this series"},
	{409, "undo_changed", "an answer changed after this action; refresh before trying again"},
	{409, "undo_date_ended", "one of these dates is no longer open"},
	{409, "undo_expired", "the Undo time has ended"},
	{404, "undo_not_found", "Undo not found"},
	{403, "undo_other_actor", "this Undo belongs to someone else"},
	{400, "undo_token_required", "undo token is required"},
	{409, "undo_used", "this Undo was already used"},
	{400, "visibility_not_allowed", "that visibility isn't allowed on this event"},
	{400, "wrong_password", "current password is incorrect"},
}
