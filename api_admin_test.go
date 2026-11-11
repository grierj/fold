package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"fold/internal/audit"
	"fold/internal/jobs"
	"fold/internal/notify"
	"fold/internal/testdb"
	"fold/models"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func TestGuardianLinkListReadBudgetAndValues(t *testing.T) {
	app := testApp(t)
	admin := newTestUser(t, app, "admin", "pw", models.RoleAdmin)
	client := loginAPI(t, app, "admin", "pw")
	h := models.Household{Name: "Family", CreatedBy: admin.ID}
	if err := app.DB.Create(&h).Error; err != nil {
		t.Fatal(err)
	}
	birth := time.Date(2012, 1, 2, 0, 0, 0, 0, time.UTC)
	var baseline int64
	for i := 1; i <= 30; i++ {
		link := models.GuardianLink{GuardianUserID: admin.ID, Status: models.GuardianPending}
		if i%2 == 1 {
			person := models.HouseholdPerson{HouseholdID: h.ID, FirstName: "Child", LastName: fmt.Sprint(i), Kind: models.HouseholdChild, BirthDate: &birth, CreatedBy: admin.ID}
			if err := app.DB.Create(&person).Error; err != nil {
				t.Fatal(err)
			}
			link.PersonID = &person.ID
			// History must retain a soft-deleted person's name and age data.
			if i == 1 {
				if err := app.DB.Delete(&person).Error; err != nil {
					t.Fatal(err)
				}
			}
		} else {
			teen := models.User{Name: fmt.Sprintf("Teen %d", i), Email: fmt.Sprintf("teen%d@example.com", i), Role: models.RoleMember, Status: models.UserPendingConsent, AccountType: models.AccountTeen, BirthDate: &birth}
			if err := app.DB.Create(&teen).Error; err != nil {
				t.Fatal(err)
			}
			link.TeenUserID = &teen.ID
		}
		if err := app.DB.Create(&link).Error; err != nil {
			t.Fatal(err)
		}
		if i != 3 && i != 30 {
			continue
		}
		counter := countDBReads(t, app.DB)
		w := client.do("GET", "/api/v1/admin/guardian-links?status=all", nil, false)
		reads := counter.Reads()
		counter.Stop()
		if w.Code != http.StatusOK {
			t.Fatalf("list: %d %s", w.Code, w.Body.String())
		}
		var body struct {
			Links []guardianLinkJSON `json:"guardianLinks"`
		}
		decodeJSON(t, w, &body)
		if len(body.Links) != i {
			t.Fatalf("got %d links want %d", len(body.Links), i)
		}
		for _, got := range body.Links {
			if got.GuardianName != admin.PublicName() || got.BirthDate != "2012-01-02" || got.PersonName == "" {
				t.Fatalf("wrong link: %+v", got)
			}
			if got.PersonID != nil && (got.HouseholdName != h.Name || got.HouseholdID != h.ID || got.PersonKind != models.HouseholdChild) {
				t.Fatalf("wrong household link: %+v", got)
			}
			if got.TeenUserID != nil && got.PersonKind != models.HouseholdTeen {
				t.Fatalf("wrong teen link: %+v", got)
			}
		}
		if i == 3 {
			baseline = reads
		}
		if reads != baseline || reads > 10 {
			t.Fatalf("%d links: %d reads baseline%d cap10", i, reads, baseline)
		}
	}
}

func TestAdminUserCRUD(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "password1234", "admin")
	newTestUser(t, app, "grier", "password1234", "member")
	admin := loginAPI(t, app, "admin", "password1234")

	// Non-admin is forbidden.
	member := loginAPI(t, app, "grier", "password1234")
	w := member.do("GET", "/api/v1/admin/users", nil, false)
	if w.Code != http.StatusForbidden {
		t.Fatalf("member list users: got %d, want 403", w.Code)
	}

	// Create.
	w = admin.do("POST", "/api/v1/admin/users", map[string]string{"ageBand": "adult",
		"email": "Newbie@Example.com", "name": "Newbie", "password": "password1234", "role": "member",
		"displayName": "Newbie",
	}, true)
	if w.Code != http.StatusCreated {
		t.Fatalf("create user: %d %s", w.Code, w.Body.String())
	}
	var created struct {
		User userJSON `json:"user"`
	}
	decodeJSON(t, w, &created)
	if created.User.Email != "newbie@example.com" || created.User.DisplayName != "Newbie" {
		t.Fatalf("unexpected created user: %+v", created.User)
	}

	// Duplicate email (any case) conflicts.
	w = admin.do("POST", "/api/v1/admin/users", map[string]string{"ageBand": "adult",
		"email": "NEWBIE@example.com", "name": "Again", "password": "password1234",
	}, true)
	if w.Code != http.StatusConflict {
		t.Fatalf("duplicate email: got %d, want 409", w.Code)
	}

	// Short password rejected.
	w = admin.do("POST", "/api/v1/admin/users", map[string]string{"ageBand": "adult",
		"email": "shorty@example.com", "name": "Shorty", "password": "short",
	}, true)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("short password: got %d, want 400", w.Code)
	}

	// List includes the new user.
	w = admin.do("GET", "/api/v1/admin/users", nil, false)
	var list struct {
		Users []userJSON `json:"users"`
	}
	decodeJSON(t, w, &list)
	if len(list.Users) != 3 {
		t.Fatalf("list users: got %d, want 3", len(list.Users))
	}

	// Update profile + promote.
	uid := created.User.ID
	w = admin.do("PUT", "/api/v1/admin/users/"+itoa(uid), map[string]string{
		"displayName": "Newbie Renamed", "role": "admin", "email": "n@example.org",
	}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("update user: %d %s", w.Code, w.Body.String())
	}

	// Cannot demote the last admin... (there are 2 admins now, so demote newbie back)
	w = admin.do("PUT", "/api/v1/admin/users/"+itoa(uid), map[string]string{"role": "member"}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("demote second admin: %d %s", w.Code, w.Body.String())
	}
	// Now only "admin" is admin: demoting them fails.
	var adminUser models.User
	app.DB.Where("email = ?", testEmail("admin")).First(&adminUser)
	w = admin.do("PUT", "/api/v1/admin/users/"+itoa(adminUser.ID), map[string]string{"role": "member"}, true)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("demote last admin: got %d, want 400", w.Code)
	}
	// Cannot delete self.
	w = admin.do("DELETE", "/api/v1/admin/users/"+itoa(adminUser.ID), nil, true)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("delete self: got %d, want 400", w.Code)
	}

	// Delete the new user.
	w = admin.do("DELETE", "/api/v1/admin/users/"+itoa(uid), nil, true)
	if w.Code != http.StatusOK {
		t.Fatalf("delete user: %d %s", w.Code, w.Body.String())
	}
	var gone models.User
	if err := app.DB.Where("email = ?", testEmail("newbie")).First(&gone).Error; err == nil {
		t.Fatalf("user still present after delete")
	}
}

// Exercise the scrub through the real cookie/CSRF DELETE, with one sentinel
// for each reachable attribution class and living-party counterparts. There
// are no RETAIN_ESSENTIAL actions after R4-GR-IP; the aggregation case is
// covered separately without inventing a production retention exception.
func TestAdminDeleteUserScrubsAuditDetails(t *testing.T) {
	s := newCoreErasureScene(t)
	db, u, living := s.app.DB, s.second, s.admin
	teen := newTestUser(t, s.app, "audit-report-teen", erasurePW, models.RoleMember)
	h := models.Household{Name: "Other household", CreatedBy: living.ID}
	erasureMustCreate(t, db, &h)
	p := models.HouseholdPerson{HouseholdID: h.ID, FirstName: "Other person", Kind: models.HouseholdAdult,
		ConvertedUserID: &teen.ID, CreatedBy: living.ID}
	erasureMustCreate(t, db, &p)
	inv := erasureInvitation(t, db, living.ID, u.Email, true, nil)
	conversionInv := erasureInvitation(t, db, living.ID, teen.Email, true, &p.ID)
	request := func(source string, user uint, by *uint) models.AccountRequest {
		r := models.AccountRequest{Name: "Request", Email: "audit-report@example.com", Source: source,
			Status: "approved", ApprovedUserID: &user, RequestedBy: by, DecidedBy: &u.ID,
			ExpiresAt: time.Now().Add(time.Hour)}
		if source == "household_conversion" {
			r.HouseholdPersonID, r.InvitationID = &p.ID, &conversionInv
		}
		erasureMustCreate(t, db, &r)
		return r
	}
	self := request("self_signup", u.ID, nil)
	request("member_invite", teen.ID, &u.ID)
	request("household_conversion", teen.ID, nil)

	var before []models.AuditLog
	blank := map[uint]bool{}
	removed := map[uint][]string{}
	add := func(action, target string, id uint, actor *uint, meta map[string]any, clear bool, keys ...string) models.AuditLog {
		row := auditErasureLog(t, db, action, target, id, actor, meta)
		before = append(before, row)
		blank[row.ID], removed[row.ID] = clear, keys
		return row
	}
	// ACTOR, META_USER, TARGET_USER, LINKED, REQUESTER and C_APPROVAL.
	add(audit.UserCreated, "user", teen.ID, &u.ID, nil, true)
	add(audit.UserCreated, "user", u.ID, &living.ID, nil, false)
	add(audit.InvitationAccepted, "invitation", inv, &living.ID, map[string]any{"userId": u.ID}, true)
	add(audit.InvitationAccepted, "invitation", conversionInv, &u.ID, map[string]any{"userId": teen.ID}, false)
	add(audit.PasswordResetRequested, "user", u.ID, &living.ID, nil, true)
	add(audit.PasswordResetRequested, "user", teen.ID, &u.ID, nil, false)
	add(audit.AccountRequested, "account_request", self.ID, nil, map[string]any{"source": "self_signup"}, true)
	add(audit.GuardianLinkRequested, "guardian_link", 9001, nil, map[string]any{"source": "signup", "teenUserId": teen.ID}, true)
	add(audit.GuardianLinkRequested, "guardian_link", 9002, &living.ID, map[string]any{"source": "household_conversion", "teenUserId": teen.ID}, true)
	// The inviter is the actor on CONVERSION rows, but G-8 keeps both IPs.
	add(audit.HouseholdPersonConverted, "household", h.ID, &u.ID, map[string]any{
		"personId": p.ID, "householdId": h.ID, "invitationId": conversionInv, "userId": teen.ID,
		"accountType": "adult", "transferCount": 1}, false)
	transfer := add(audit.RegistrationChanged, "registration", 9003, &u.ID, map[string]any{
		"personId": p.ID, "invitationId": conversionInv, "userId": teen.ID,
		"formerRegisteredBy": living.ID, "clearedChildcareRequested": false}, false)
	// Conversion.Retained counts entries, including an already-empty IP.
	if err := db.Model(&models.AuditLog{}).Where("id = ?", transfer.ID).Update("ip", "").Error; err != nil {
		t.Fatal(err)
	}
	before[len(before)-1].IP = ""
	// Three subject-linked retained IP rows. The malformed nomination also
	// appears in its diagnostic count; it must contribute only once.
	add("legacy.unknown", "user", u.ID, &u.ID, nil, false)
	add(audit.InvitationAccepted, "invitation", inv, &living.ID, map[string]any{"userId": u.ID, "personId": p.ID}, false)
	add(audit.GuardianLinkRequested, "guardian_link", 9004, nil,
		map[string]any{"source": "household_conversion", "teenUserId": itoa(teen.ID)}, false)
	add("legacy.unknown", "user", teen.ID, &living.ID, nil, false)
	// NONE and already-blank unattributed rows do not add a retained-IP count.
	for _, action := range []string{audit.UserTurned18, "legacy.unknown"} {
		row := add(action, "user", u.ID, nil, nil, false)
		if err := db.Model(&models.AuditLog{}).Where("id = ?", row.ID).Update("ip", "").Error; err != nil {
			t.Fatal(err)
		}
		before[len(before)-1].IP = ""
	}
	// Subject keys are removed even when the IP belongs to a living actor.
	add(audit.UserEmailChanged, "user", u.ID, &living.ID, map[string]any{"oldEmail": "old@example.com", "newEmail": u.Email}, false, "oldEmail", "newEmail")
	add(audit.InvitationCreated, "invitation", inv, &living.ID, map[string]any{"email": u.Email, "groupId": 12}, false, "email")
	add(audit.MemberRemoved, "group", 12, &living.ID, map[string]any{"userId": u.ID, "note": "Subject note"}, false, "note")
	add(audit.RegistrationRemoved, "event", 13, &living.ID, map[string]any{"userId": u.ID, "note": "Subject note"}, false, "note")
	add(audit.PushDeviceOn, "user", u.ID, &u.ID, map[string]any{"label": "Subject device", "deviceId": 14}, true, "label")
	add(audit.GuardianLinkConfirmed, "guardian_link", 9005, &u.ID,
		map[string]any{"teenUserId": teen.ID, "checkNote": "Keep safeguarding evidence"}, true)
	add(audit.TeenConsentGranted, "user", teen.ID, &u.ID,
		map[string]any{"evidenceText": "Keep consent evidence"}, true)

	w := s.ac.do("DELETE", "/api/v1/admin/users/"+itoa(u.ID), nil, true)
	if w.Code != http.StatusOK {
		t.Fatalf("delete: %d %s", w.Code, w.Body.String())
	}
	// Pin the complete public shape too: no class/action diagnostics, IDs or
	// addresses may escape through the report, even when residue remains.
	var body struct {
		Erasure erasureReportJSON `json:"erasure"`
	}
	dec := json.NewDecoder(w.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		t.Fatal(err)
	}
	want := erasureReportJSON{ActivityEntriesKept: 5}
	if body.Erasure != want {
		t.Fatalf("report %+v, want %+v", body.Erasure, want)
	}
	assertAuditErasureRows(t, db, before, blank, removed)
	var gone models.User
	if err := db.Unscoped().First(&gone, u.ID).Error; err != nil || !gone.DeletedAt.Valid {
		t.Fatalf("subject was not deleted: %+v %v", gone, err)
	}
}

func TestAdminResetPassword(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "password1234", "admin")
	newTestUser(t, app, "grier", "password1234", "member")
	admin := loginAPI(t, app, "admin", "password1234")

	var u models.User
	app.DB.Where("email = ?", testEmail("grier")).First(&u)
	w := admin.do("POST", "/api/v1/admin/users/"+itoa(u.ID)+"/password",
		map[string]string{"password": "brand-new-password"}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("reset password: %d %s", w.Code, w.Body.String())
	}
	// Old password fails, new one works.
	bad := loginAPIExpectFail(t, app, "grier", "password1234")
	if bad {
		t.Fatalf("old password still works after reset")
	}
	loginAPI(t, app, "grier", "brand-new-password")

	// Short password rejected.
	w = admin.do("POST", "/api/v1/admin/users/"+itoa(u.ID)+"/password",
		map[string]string{"password": "short"}, true)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("short reset password: got %d, want 400", w.Code)
	}
}

// Only a missing row is "user not found"; a failed read of the target is the
// generic server error and changes nothing.
func TestAdminTargetLookupFailureIsNotAbsence(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "password1234", "admin")
	target := newTestUser(t, app, "grier", "password1234", "member")
	admin := loginAPI(t, app, "admin", "password1234")
	path := "/api/v1/admin/users/" + itoa(target.ID) + "/password"
	body := map[string]string{"password": "brand-new-password"}

	restore := failTargetLookup(t, app.DB, target.ID)
	w := admin.do("POST", path, body, true)
	restore()
	if w.Code != http.StatusInternalServerError || !strings.Contains(w.Body.String(), `"code":"server_error"`) {
		t.Fatalf("reset with a failing lookup: %d %s, want 500 server_error", w.Code, w.Body.String())
	}
	for _, leak := range []string{"injected", "not found"} {
		if strings.Contains(w.Body.String(), leak) {
			t.Errorf("the answer carries %q: %s", leak, w.Body.String())
		}
	}
	if !loginAPIExpectFail(t, app, "grier", "password1234") {
		t.Fatal("the failed lookup changed the password")
	}

	if w := admin.do("POST", "/api/v1/admin/users/987654/password", body, true); w.Code != http.StatusNotFound {
		t.Fatalf("reset of a missing account: %d %s, want 404", w.Code, w.Body.String())
	}
}

func TestAdminGroupMembers(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "password1234", "admin")
	leader := newTestUser(t, app, "grier", "password1234", "member")
	other := newTestUser(t, app, "other", "password1234", "member")
	g := models.Group{Name: "Test Group", LeaderID: leader.ID, Status: models.GroupActive}
	if err := app.DB.Create(&g).Error; err != nil {
		t.Fatalf("create group: %v", err)
	}
	admin := loginAPI(t, app, "admin", "password1234")
	membersPath := "/api/v1/admin/groups/" + itoa(g.ID) + "/members"

	// Add a member.
	w := admin.do("POST", membersPath, map[string]interface{}{
		"userId": other.ID, "role": "member",
	}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("add member: %d %s", w.Code, w.Body.String())
	}
	// Re-adding is idempotent-ish (200).
	w = admin.do("POST", membersPath, map[string]interface{}{
		"userId": other.ID, "role": "member",
	}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("re-add member: %d %s", w.Code, w.Body.String())
	}
	// Promote to leader reassigns group leadership.
	w = admin.do("POST", membersPath, map[string]interface{}{
		"userId": other.ID, "role": "leader",
	}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("promote to leader: %d %s", w.Code, w.Body.String())
	}
	var got models.Group
	app.DB.First(&got, g.ID)
	if got.LeaderID != other.ID {
		t.Fatalf("leader not reassigned: %d", got.LeaderID)
	}
	// Cannot remove the current leader.
	w = admin.do("DELETE", membersPath+"/"+itoa(other.ID), nil, true)
	if w.Code != http.StatusConflict {
		t.Fatalf("remove leader: got %d, want 409", w.Code)
	}
	// Removing a regular member works.
	var oldLeader models.User
	app.DB.Where("email = ?", testEmail("grier")).First(&oldLeader)
	// grier's membership role was demoted to member on reassignment; add first
	w = admin.do("POST", membersPath, map[string]interface{}{
		"userId": oldLeader.ID, "role": "member",
	}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("add old leader as member: %d %s", w.Code, w.Body.String())
	}
	w = confirmDeparture(t, admin, "DELETE", membersPath+"/"+itoa(oldLeader.ID), "/api/v1/groups/"+itoa(g.ID)+"/members/"+itoa(oldLeader.ID)+"/departure-preview", "")
	if w.Code != http.StatusOK {
		t.Fatalf("remove member: %d %s", w.Code, w.Body.String())
	}
	// Non-admin forbidden.
	memberClient := loginAPI(t, app, "other", "password1234")
	w = memberClient.do("POST", membersPath, map[string]interface{}{"userId": oldLeader.ID}, true)
	if w.Code != http.StatusForbidden {
		t.Fatalf("member add: got %d, want 403", w.Code)
	}
}

func TestAdminListGroups(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "password1234", "admin")
	leader := newTestUser(t, app, "grier", "password1234", "member")
	for _, st := range []string{models.GroupActive, models.GroupPending} {
		g := models.Group{Name: "G " + st, LeaderID: leader.ID, Status: st}
		if err := app.DB.Create(&g).Error; err != nil {
			t.Fatalf("create group: %v", err)
		}
	}
	admin := loginAPI(t, app, "admin", "password1234")
	w := admin.do("GET", "/api/v1/admin/groups", nil, false)
	if w.Code != http.StatusOK {
		t.Fatalf("list groups: %d", w.Code)
	}
	var resp struct {
		Groups []groupSummaryJSON `json:"groups"`
	}
	decodeJSON(t, w, &resp)
	if len(resp.Groups) != 2 {
		t.Fatalf("got %d groups, want 2 (all statuses)", len(resp.Groups))
	}
}

func TestAdminGroupsStartActive(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
	adminC := loginAPI(t, app, "admin", "password1234")
	var body struct {
		Group groupSummaryJSON `json:"group"`
	}
	decodeJSON(t, adminC.do("POST", "/api/v1/groups", map[string]any{"name": "Staff"}, true), &body)
	if body.Group.Status != models.GroupActive {
		t.Fatalf("admin-created group status = %q", body.Group.Status)
	}
}

// DEC-38: a disabled account keeps its memberships and RSVPs, hidden and
// uncounted; re-enabling brings them back as plain member, never leader.
func TestDisableKeepsMembershipsHiddenAndReenableRestoresMember(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
	pat := newTestUser(t, app, "pat", "password1234", models.RoleMember)
	co := newTestUser(t, app, "co", "password1234", models.RoleMember)
	viewer := newTestUser(t, app, "viewer", "password1234", models.RoleMember)
	led := newTestGroup(t, app, pat, "Led", models.GroupActive) // pat leads alone
	coled := newTestGroup(t, app, co, "Coled", models.GroupActive)
	addMember(t, app, pat, coled, models.MembershipLeader)
	joined := newTestGroup(t, app, co, "Joined", models.GroupActive)
	addMember(t, app, pat, joined, models.MembershipMember)
	for _, g := range []*models.Group{led, coled, joined} {
		addMember(t, app, viewer, g, models.MembershipMember)
	}
	start := time.Now().UTC().Add(48 * time.Hour).Truncate(time.Hour)
	_, occ := seedEvent(t, app, joined, co.ID, start)
	app.DB.Create(&models.Registration{UserID: &pat.ID, OccurrenceID: occ.ID, Status: models.RegGoing})
	app.DB.Create(&models.Registration{UserID: &viewer.ID, OccurrenceID: occ.ID, Status: models.RegGoing})

	admin := loginAPI(t, app, "admin", "password1234")
	other := loginAPI(t, app, "viewer", "password1234")
	type detail struct {
		Group    groupSummaryJSON `json:"group"`
		Members  []memberJSON     `json:"members"`
		Upcoming []struct {
			Registrations map[string]int64 `json:"registrations"`
		} `json:"upcomingEvents"`
	}
	look := func(g *models.Group) (d detail) {
		t.Helper()
		decodeJSON(t, other.do("GET", "/api/v1/groups/"+itoa(g.ID), nil, false), &d)
		return d
	}
	listed := func(d detail) (role string) {
		for _, m := range d.Members {
			if m.ID == pat.ID {
				return m.Role
			}
		}
		return ""
	}
	before := look(joined)
	if listed(before) != "member" || before.Group.MemberCount != 3 || before.Upcoming[0].Registrations["going"] != 2 {
		t.Fatalf("before: %+v", before)
	}

	w := admin.do("PUT", "/api/v1/admin/users/"+itoa(pat.ID),
		map[string]any{"status": "disabled", "successors": map[string]uint{itoa(led.ID): viewer.ID}}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("disable: %d %s", w.Code, w.Body.String())
	}
	// Hidden from rosters, member counts and RSVP counts; rows kept.
	size := map[*models.Group]int64{led: 2, coled: 3, joined: 3} // with pat
	for _, g := range []*models.Group{led, coled, joined} {
		d := look(g)
		if listed(d) != "" || d.Group.MemberCount != size[g]-1 {
			t.Fatalf("%s while disabled: listed %q, count %d", g.Name, listed(d), d.Group.MemberCount)
		}
		if membershipStatus(app, pat, g) != "active" || relIn(app.DB, pat.ID, g.ID).Leader {
			t.Fatalf("%s: membership should be kept as plain member", g.Name)
		}
	}
	if d := look(joined); d.Upcoming[0].Registrations["going"] != 1 {
		t.Fatalf("disabled registration counted: %+v", d.Upcoming)
	}
	if leadersIn(app.DB, coled.ID) != 1 || !relIn(app.DB, viewer.ID, led.ID).Leader {
		t.Fatal("leadership should have moved")
	}

	// Re-enabled: back as plain member everywhere, RSVP counted, audited.
	if w := admin.do("PUT", "/api/v1/admin/users/"+itoa(pat.ID), map[string]string{"status": "active"}, true); w.Code != http.StatusOK {
		t.Fatalf("enable: %d %s", w.Code, w.Body.String())
	}
	for _, g := range []*models.Group{led, coled, joined} {
		if d := look(g); listed(d) != "member" || d.Group.MemberCount != size[g] {
			t.Fatalf("%s after enable: listed %q, count %d", g.Name, listed(d), d.Group.MemberCount)
		}
	}
	if d := look(joined); d.Upcoming[0].Registrations["going"] != 2 {
		t.Fatalf("registration not restored: %+v", d.Upcoming)
	}
	if rows, metas := auditRows(t, app, audit.MemberRestored); len(rows) != 3 || metas[0].UserID != pat.ID {
		t.Fatalf("restored rows: %+v", rows)
	}
	if rows, _ := auditRows(t, app, audit.UserEnabled); len(rows) != 1 {
		t.Fatalf("user.enabled rows: %d", len(rows))
	}
}

// Disabling a going account frees its seats at once: the next waitlisted
// party is promoted in the disable transaction.
func TestDisablePromotesWaitlistedParty(t *testing.T) {
	app := testApp(t)
	admin, gid := openGroup(t, app, "DisablePromo")
	eventID, date := seedCappedEvent(t, app, admin, gid, intPtr(1), true)

	var g models.Group
	app.DB.Where("id = ?", gid).First(&g)
	u1 := newTestUser(t, app, "d1", "password1234", models.RoleMember)
	u2 := newTestUser(t, app, "d2", "password1234", models.RoleMember)
	addMember(t, app, u1, &g, models.MembershipMember)
	addMember(t, app, u2, &g, models.MembershipMember)
	c1 := loginAPI(t, app, "d1", "password1234")
	c2 := loginAPI(t, app, "d2", "password1234")
	if code, _ := registerAs(c1, eventID, date, "going", nil); code != http.StatusOK {
		t.Fatal("d1 going")
	}
	if code, _ := registerAs(c2, eventID, date, "going", nil); code != http.StatusOK {
		t.Fatal("d2 going->waitlisted")
	}
	var occ models.EventOccurrence
	app.DB.Where("event_id = ?", eventID).First(&occ)

	w := admin.do("PUT", "/api/v1/admin/users/"+itoa(u1.ID), map[string]any{"status": "disabled"}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("disable: %d %s", w.Code, w.Body.String())
	}
	// The disabled account's row is kept but uses no seat; d2 is promoted.
	var r1 models.Registration
	app.DB.Where("user_id = ? AND occurrence_id = ?", u1.ID, occ.ID).First(&r1)
	if r1.Status != models.RegGoing {
		t.Fatalf("disabled row status = %q, want kept as going", r1.Status)
	}
	used, err := seatsUsedForMode(app.DB, occ.ID, models.AttendanceInPerson, 0)
	if err != nil {
		t.Fatal(err)
	}
	if used != 1 {
		t.Fatalf("seats used after disable = %d, want 1 (only d2)", used)
	}
	var r2 models.Registration
	app.DB.Where("user_id = ? AND occurrence_id = ?", u2.ID, occ.ID).First(&r2)
	if r2.Status != models.RegGoing || r2.PromotedAt == nil {
		t.Fatalf("d2 after disable: status=%q promoted_at=%v", r2.Status, r2.PromotedAt)
	}
}

// Re-enabling into room restores the going row; re-enabling into a full
// occurrence deletes it, writes registration.not_restored, and sends the
// ask-a-leader notice.
func TestReenableRestoresOrDropsRegistration(t *testing.T) {
	app := testApp(t)
	admin, gid := openGroup(t, app, "Reenable")
	eventID, date := seedCappedEvent(t, app, admin, gid, intPtr(1), true)

	var g models.Group
	app.DB.Where("id = ?", gid).First(&g)
	u1 := newTestUser(t, app, "e1", "password1234", models.RoleMember)
	u2 := newTestUser(t, app, "e2", "password1234", models.RoleMember)
	addMember(t, app, u1, &g, models.MembershipMember)
	addMember(t, app, u2, &g, models.MembershipMember)
	c1 := loginAPI(t, app, "e1", "password1234")
	c2 := loginAPI(t, app, "e2", "password1234")
	if code, _ := registerAs(c1, eventID, date, "going", nil); code != http.StatusOK {
		t.Fatal("e1 going")
	}
	var occ models.EventOccurrence
	app.DB.Where("event_id = ?", eventID).First(&occ)

	// Disable e1 (no waitlist, so nothing promotes), then re-enable into
	// room: the row is restored.
	w := admin.do("PUT", "/api/v1/admin/users/"+itoa(u1.ID), map[string]any{"status": "disabled"}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("disable: %d %s", w.Code, w.Body.String())
	}
	w = admin.do("PUT", "/api/v1/admin/users/"+itoa(u1.ID), map[string]any{"status": "active"}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("re-enable: %d %s", w.Code, w.Body.String())
	}
	if got := regStatus(t, app, u1.ID, occ.ID); got != models.RegGoing {
		t.Fatalf("restored status = %q, want going", got)
	}

	// Now e2 takes the seat, e1 is disabled again, and re-enabled into a
	// full occurrence: the row is dropped with a notice.
	if code, _ := registerAs(c2, eventID, date, "going", nil); code != http.StatusOK {
		t.Fatalf("e2 going: %d", code)
	}
	w = admin.do("PUT", "/api/v1/admin/users/"+itoa(u1.ID), map[string]any{"status": "disabled"}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("disable again: %d %s", w.Code, w.Body.String())
	}
	// e2's seat is taken; fill it so e1 can't come back.
	w = admin.do("PUT", "/api/v1/admin/users/"+itoa(u1.ID), map[string]any{"status": "active"}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("re-enable into full: %d %s", w.Code, w.Body.String())
	}
	var n int64
	app.DB.Model(&models.Registration{}).Where("user_id = ? AND occurrence_id = ?", u1.ID, occ.ID).Count(&n)
	if n != 0 {
		t.Fatalf("e1 row still exists after drop")
	}
	if c := auditCount(t, app, audit.RegistrationNotRestored); c != 1 {
		t.Fatalf("registration.not_restored rows = %d, want 1", c)
	}
	// Disabling bumped the session, so log back in to read the bell.
	c1b := loginAPI(t, app, "e1", "password1234")
	if got := findKind(feed(t, c1b, ""), "registration.not_restored"); got == nil {
		t.Fatalf("no not_restored notice for e1")
	}
}

// DEC-38/47 for the remote pool: a disabled account's remote
// registration counts in neither pool, and re-enabling into a full
// remote pool leaves the registration out, even with in-person seats
// to spare.
func TestDisabledRemoteRegistrationUncountedAndLeftOutWhenFull(t *testing.T) {
	app := testApp(t)
	admin, gid := openGroup(t, app, "ReenableRemote")
	eventID, date := seedRemoteEvent(t, app, admin, gid, intPtr(10), intPtr(1), true)

	var g models.Group
	app.DB.Where("id = ?", gid).First(&g)
	u1 := newTestUser(t, app, "re1", "password1234", models.RoleMember)
	u2 := newTestUser(t, app, "re2", "password1234", models.RoleMember)
	addMember(t, app, u1, &g, models.MembershipMember)
	addMember(t, app, u2, &g, models.MembershipMember)
	c1 := loginAPI(t, app, "re1", "password1234")
	c2 := loginAPI(t, app, "re2", "password1234")
	if code, body := registerModeAs(c1, eventID, date, "going", models.AttendanceRemote); code != http.StatusOK {
		t.Fatalf("re1 remote going: %d %s", code, body)
	}
	if code, body := registerModeAs(c2, eventID, date, "going", models.AttendanceRemote); code != http.StatusOK {
		t.Fatalf("re2 remote going->waitlisted: %d %s", code, body)
	}
	var occ models.EventOccurrence
	app.DB.Where("event_id = ?", eventID).First(&occ)
	if used, err := seatsUsedForMode(app.DB, occ.ID, models.AttendanceRemote, 0); err != nil || used != 1 {
		t.Fatalf("remote seats before disable = %d (%v), want 1", used, err)
	}

	// Disable re1: the row is kept as going but uses no remote seat,
	// so re2 is promoted into the freed seat and the count stays 1.
	w := admin.do("PUT", "/api/v1/admin/users/"+itoa(u1.ID), map[string]any{"status": "disabled"}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("disable: %d %s", w.Code, w.Body.String())
	}
	var r1 models.Registration
	app.DB.Where("user_id = ? AND occurrence_id = ?", u1.ID, occ.ID).First(&r1)
	if r1.Status != models.RegGoing || r1.AttendanceMode != models.AttendanceRemote {
		t.Fatalf("disabled remote row: status=%q mode=%q, want kept going remote", r1.Status, r1.AttendanceMode)
	}
	if used, err := seatsUsedForMode(app.DB, occ.ID, models.AttendanceRemote, 0); err != nil || used != 1 {
		t.Fatalf("remote seats after disable = %d (%v), want 1 (only re2; re1 uncounted)", used, err)
	}
	var r2 models.Registration
	app.DB.Where("user_id = ? AND occurrence_id = ?", u2.ID, occ.ID).First(&r2)
	if r2.Status != models.RegGoing || r2.PromotedAt == nil {
		t.Fatalf("re2 after disable: status=%q promoted_at=%v", r2.Status, r2.PromotedAt)
	}

	// Re-enable re1: the remote pool is full (re2), so re1's row is
	// dropped with the audit row and notice, despite empty in-person seats.
	w = admin.do("PUT", "/api/v1/admin/users/"+itoa(u1.ID), map[string]any{"status": "active"}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("re-enable: %d %s", w.Code, w.Body.String())
	}
	var n int64
	app.DB.Model(&models.Registration{}).Where("user_id = ? AND occurrence_id = ?", u1.ID, occ.ID).Count(&n)
	if n != 0 {
		t.Fatal("re1 row still exists after re-enable into a full remote pool")
	}
	if c := auditCount(t, app, audit.RegistrationNotRestored); c != 1 {
		t.Fatalf("registration.not_restored rows = %d, want 1", c)
	}
	c1b := loginAPI(t, app, "re1", "password1234")
	if got := findKind(feed(t, c1b, ""), "registration.not_restored"); got == nil {
		t.Fatalf("no not_restored notice for re1")
	}
	if got := regStatus(t, app, u2.ID, occ.ID); got != models.RegGoing {
		t.Fatalf("re2 = %q, want still going", got)
	}
}

// #94: an admin edit can't make a teen an admin, can't activate a teen
// waiting for consent (even one with a password), and re-enabling a
// disabled teen sends it back to waiting for consent.
func TestAdminEditCannotBypassTeenConsent(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "pw", models.RoleAdmin)
	admin := loginAPI(t, app, "admin", "pw")
	teen := models.User{Email: "teen94@example.com", Name: "Teen", Role: models.RoleMember,
		Status: models.UserPendingConsent, AccountType: models.AccountTeen}
	if err := teen.SetPassword("password1234"); err != nil {
		t.Fatal(err)
	}
	if err := app.DB.Create(&teen).Error; err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/admin/users/" + itoa(teen.ID)
	reload := func() models.User {
		var u models.User
		app.DB.First(&u, teen.ID)
		return u
	}

	apiDo(t, admin, "PUT", path, map[string]string{"status": models.UserActive}, http.StatusBadRequest)
	apiDo(t, admin, "PUT", path, map[string]string{"role": models.RoleAdmin}, http.StatusBadRequest)
	if u := reload(); u.Status != models.UserPendingConsent || u.Role != models.RoleMember {
		t.Fatalf("teen changed: %s %s", u.Status, u.Role)
	}

	// Disable, then re-enable: back to pending_consent, not active.
	apiDo(t, admin, "PUT", path, map[string]string{"status": models.UserDisabled}, http.StatusOK)
	apiDo(t, admin, "PUT", path, map[string]string{"status": models.UserActive}, http.StatusOK)
	if u := reload(); u.Status != models.UserPendingConsent {
		t.Fatalf("re-enabled teen: %s, want pending_consent", u.Status)
	}
	if code, _ := tryLogin(t, app, "", teen.Email, "password1234"); code != http.StatusForbidden {
		t.Fatalf("teen signed in without consent: %d", code)
	}
	pending := newAPIClient(t, app)
	if w := pending.do("GET", "/api/v1/groups", nil, false); w.Code != http.StatusOK {
		t.Fatalf("pending teen could not browse publicly: %d", w.Code)
	}

	// An adult member can still be promoted and re-enabled as before.
	adult := newTestUser(t, app, "adult94", "password1234", models.RoleMember)
	apiDo(t, admin, "PUT", "/api/v1/admin/users/"+itoa(adult.ID), map[string]string{"role": models.RoleAdmin}, http.StatusOK)
}

// Guardian links (M1.9 slice 1, DEC-32): a child or teen added to a
// household waits for an admin, who confirms it only with a check note.
func TestAdminGuardianLinksConfirmReject(t *testing.T) {
	app := testApp(t)
	adminUser := newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
	newTestUser(t, app, "pat", "password1234", models.RoleMember)
	admin := loginAPI(t, app, "admin", "password1234")
	pat := loginAPI(t, app, "pat", "password1234")

	child := addMyPerson(t, pat, map[string]any{"firstName": "Kid", "lastName": "Smith", "kind": "child", "birthDate": "2020-05-01"})
	teen := addMyPerson(t, pat, map[string]any{"firstName": "Tess", "kind": "teen", "birthDate": "2012-03-04"})
	addMyPerson(t, pat, map[string]any{"firstName": "Mary", "kind": "adult"})

	// Members never see or decide the queue.
	if w := pat.do("GET", "/api/v1/admin/guardian-links?status=pending", nil, false); w.Code != http.StatusForbidden {
		t.Fatalf("member lists guardian links: %d, want 403", w.Code)
	}

	listLinks := func(status string) []guardianLinkJSON {
		t.Helper()
		w := admin.do("GET", "/api/v1/admin/guardian-links?status="+status, nil, false)
		if w.Code != http.StatusOK {
			t.Fatalf("list guardian links %s: %d %s", status, w.Code, w.Body.String())
		}
		var body struct {
			GuardianLinks []guardianLinkJSON `json:"guardianLinks"`
		}
		decodeJSON(t, w, &body)
		return body.GuardianLinks
	}
	pending := listLinks("pending")
	if len(pending) != 2 {
		t.Fatalf("pending links: %+v", pending)
	}
	var childLink, teenLink guardianLinkJSON
	for _, l := range pending {
		if l.PersonID != nil && *l.PersonID == child.ID {
			childLink = l
		}
		if l.PersonID != nil && *l.PersonID == teen.ID {
			teenLink = l
		}
	}
	if childLink.PersonName != "Kid Smith" || childLink.PersonKind != "child" || childLink.BirthDate != "2020-05-01" || childLink.GuardianName == "" {
		t.Fatalf("child link row: %+v", childLink)
	}
	if teenLink.PersonKind != "teen" {
		t.Fatalf("teen link row: %+v", teenLink)
	}
	if w := admin.do("GET", "/api/v1/admin/guardian-links?status=bogus", nil, false); w.Code != http.StatusBadRequest {
		t.Fatalf("bogus status: %d, want 400", w.Code)
	}

	confirmPath := "/api/v1/admin/guardian-links/" + itoa(childLink.ID) + "/confirm"
	// No check note, no confirmation (DEC-32), and nothing changes.
	w := admin.do("POST", confirmPath, map[string]string{}, true)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("confirm without a note: %d %s, want 400", w.Code, w.Body.String())
	}
	var link models.GuardianLink
	app.DB.First(&link, childLink.ID)
	if link.Status != models.GuardianPending {
		t.Fatalf("link after noteless confirm: %+v", link)
	}

	// With a note: confirmed, by this admin, decided now, task resolved.
	w = admin.do("POST", confirmPath, map[string]string{"checkNote": "Known family; parent came to the office"}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("confirm link: %d %s", w.Code, w.Body.String())
	}
	if n := len(mailJobsOfKind(t, app, kindGuardianConsentMail)); n != 0 {
		t.Fatalf("household-person confirmation queued %d teen-account consent mail jobs, want 0", n)
	}
	var decided struct {
		GuardianLink guardianLinkJSON `json:"guardianLink"`
	}
	decodeJSON(t, w, &decided)
	if decided.GuardianLink.Status != models.GuardianConfirmed || decided.GuardianLink.ConfirmedBy == nil ||
		*decided.GuardianLink.ConfirmedBy != adminUser.ID || decided.GuardianLink.CheckNote == "" || decided.GuardianLink.DecidedAt == nil {
		t.Fatalf("confirmed link: %+v", decided.GuardianLink)
	}
	if rows, _ := auditRows(t, app, audit.GuardianLinkConfirmed); len(rows) != 1 || rows[0].TargetType != "guardian_link" || rows[0].TargetID != childLink.ID {
		t.Fatalf("guardian_link.confirmed audit: %+v", rows)
	}
	if _, _, waiting, _ := badge(t, admin, ""); waiting != 1 {
		t.Fatalf("admin waiting after one confirm: %d, want 1 (the teen)", waiting)
	}
	if h := getMyHousehold(t, pat); h.People[0].LinkStatus != models.GuardianConfirmed {
		found := ""
		for _, p := range h.People {
			if p.ID == child.ID {
				found = p.LinkStatus
			}
		}
		t.Fatalf("holder sees child link as %q, want confirmed", found)
	}
	// Deciding again loses the race cleanly.
	if w := admin.do("POST", confirmPath, map[string]string{"checkNote": "again"}, true); w.Code != http.StatusConflict {
		t.Fatalf("second confirm: %d %s, want 409", w.Code, w.Body.String())
	}

	// Rejecting needs no note; the holder sees the rejection.
	rejectPath := "/api/v1/admin/guardian-links/" + itoa(teenLink.ID) + "/reject"
	w = admin.do("POST", rejectPath, map[string]string{}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("reject link: %d %s", w.Code, w.Body.String())
	}
	decodeJSON(t, w, &decided)
	if decided.GuardianLink.Status != models.GuardianRejected {
		t.Fatalf("rejected link: %+v", decided.GuardianLink)
	}
	if rows, _ := auditRows(t, app, audit.GuardianLinkRejected); len(rows) != 1 {
		t.Fatalf("guardian_link.rejected audit rows: %d", len(rows))
	}
	if _, _, waiting, _ := badge(t, admin, ""); waiting != 0 {
		t.Fatalf("admin waiting after both decisions: %d", waiting)
	}
	if links := listLinks("pending"); len(links) != 0 {
		t.Fatalf("pending after decisions: %+v", links)
	}
	if links := listLinks("confirmed"); len(links) != 1 || links[0].ID != childLink.ID {
		t.Fatalf("confirmed list: %+v", links)
	}
	for _, p := range getMyHousehold(t, pat).People {
		if p.ID == teen.ID && p.LinkStatus != models.GuardianRejected {
			t.Fatalf("holder sees teen link as %q, want rejected", p.LinkStatus)
		}
	}
	// A missing link is a 404 for an admin too.
	if w := admin.do("POST", "/api/v1/admin/guardian-links/999999/confirm", map[string]string{"checkNote": "x"}, true); w.Code != http.StatusNotFound {
		t.Fatalf("missing link: %d, want 404", w.Code)
	}
	if len(mailJobsOfKind(t, app, kindGuardianConsentMail)) != 0 {
		t.Fatal("household-person decisions queued a teen-account consent email")
	}
}

// DEC-79: an admin cannot confirm their own guardian link while another
// active admin exists; another admin can. A sole admin may confirm their
// own link, and the audit row marks it as a self-confirm.
func TestGuardianLinkSelfConfirm(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "ada", "password1234", models.RoleAdmin)
	ada := loginAPI(t, app, "ada", "password1234")
	pendingFor := func(c *apiClient, name string) uint {
		t.Helper()
		p := addMyPerson(t, c, map[string]any{"firstName": name, "kind": "child", "birthDate": "2020-05-01"})
		var link models.GuardianLink
		if err := app.DB.Where("person_id = ?", p.ID).First(&link).Error; err != nil {
			t.Fatal(err)
		}
		return link.ID
	}
	note := map[string]string{"checkNote": "Known family"}

	// Sole admin: allowed and audited as a self-confirm.
	solo := pendingFor(ada, "Solo")
	if w := ada.do("POST", "/api/v1/admin/guardian-links/"+itoa(solo)+"/confirm", note, true); w.Code != http.StatusOK {
		t.Fatalf("sole admin self-confirm: %d %s", w.Code, w.Body.String())
	}
	rows, metas := auditRows(t, app, audit.GuardianLinkConfirmed)
	if len(rows) != 1 || rows[0].TargetID != solo || !metas[0].SelfConfirm {
		t.Fatalf("self-confirm audit: %+v %+v", rows, metas)
	}

	// With a second active admin, self-confirm is refused and nothing changes.
	newTestUser(t, app, "bea", "password1234", models.RoleAdmin)
	bea := loginAPI(t, app, "bea", "password1234")
	second := pendingFor(ada, "Second")
	path := "/api/v1/admin/guardian-links/" + itoa(second) + "/confirm"
	if w := ada.do("POST", path, note, true); w.Code != http.StatusForbidden {
		t.Fatalf("multi-admin self-confirm: %d %s, want 403", w.Code, w.Body.String())
	}
	var link models.GuardianLink
	app.DB.First(&link, second)
	if link.Status != models.GuardianPending {
		t.Fatalf("link after refused self-confirm: %+v", link)
	}
	// The other admin can confirm it, and it is not a self-confirm.
	if w := bea.do("POST", path, note, true); w.Code != http.StatusOK {
		t.Fatalf("other admin confirm: %d %s", w.Code, w.Body.String())
	}
	rows, metas = auditRows(t, app, audit.GuardianLinkConfirmed)
	if len(rows) != 2 || rows[1].TargetID != second || metas[1].SelfConfirm {
		t.Fatalf("other-admin audit: %+v %+v", rows, metas)
	}
}

// DEC-79: only other *active* admins block a self-confirm. A disabled or
// not-yet-active second admin does not (Warden, #106).
func TestGuardianLinkSelfConfirmIgnoresInactiveAdmins(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "ada", "password1234", models.RoleAdmin)
	off := newTestUser(t, app, "off", "password1234", models.RoleAdmin)
	waiting := newTestUser(t, app, "waiting", "password1234", models.RoleAdmin)
	app.DB.Model(&models.User{}).Where("id = ?", off.ID).Update("status", models.UserDisabled)
	app.DB.Model(&models.User{}).Where("id = ?", waiting.ID).Update("status", models.UserInvited)
	ada := loginAPI(t, app, "ada", "password1234")

	p := addMyPerson(t, ada, map[string]any{"firstName": "Kid", "kind": "child", "birthDate": "2020-05-01"})
	var link models.GuardianLink
	if err := app.DB.Where("person_id = ?", p.ID).First(&link).Error; err != nil {
		t.Fatal(err)
	}
	w := ada.do("POST", "/api/v1/admin/guardian-links/"+itoa(link.ID)+"/confirm", map[string]string{"checkNote": "Known family"}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("self-confirm with only inactive other admins: %d %s", w.Code, w.Body.String())
	}
	if _, metas := auditRows(t, app, audit.GuardianLinkConfirmed); len(metas) != 1 || !metas[0].SelfConfirm {
		t.Fatalf("self-confirm audit: %+v", metas)
	}
}

func TestDisabledChildcareUncountedAndLeftOutWhenFull(t *testing.T) {
	app := testApp(t)
	admin, gid := openGroup(t, app, "ChildcareRestore")
	id, date := seedChildcareEvent(t, app, admin, gid, nil, intPtr(1), false)
	u := newTestUser(t, app, "restoreparent", "password1234", models.RoleMember)
	c := loginAPI(t, app, "restoreparent", "password1234")
	r := childcareAs(t, c, id, date, "going", 1, http.StatusOK)
	apiDo(t, admin, "PUT", "/api/v1/admin/users/"+itoa(u.ID), map[string]any{"status": "disabled"}, http.StatusOK)
	if used, err := childcareUsedFor(app.DB, r.OccurrenceID, 0); err != nil || used != 0 {
		t.Fatalf("disabled childcare counts: %d %v", used, err)
	}
	// This going request fits only because the disabled request is ignored.
	childcareAs(t, admin, id, date, "going", 1, http.StatusOK)
	apiDo(t, admin, "PUT", "/api/v1/admin/users/"+itoa(u.ID), map[string]any{"status": "active"}, http.StatusOK)
	var n int64
	app.DB.Model(&models.Registration{}).Where("user_id = ? AND occurrence_id = ?", u.ID, r.OccurrenceID).Count(&n)
	if n != 0 {
		t.Fatal("restored over childcare capacity with unlimited attendance")
	}
	if auditCount(t, app, audit.RegistrationNotRestored) != 1 {
		t.Fatal("missing not-restored audit")
	}
	if findKind(feed(t, loginAPI(t, app, "restoreparent", "password1234"), ""), "registration.not_restored") == nil {
		t.Fatal("missing ask-a-leader notice")
	}
}

// Deleting an account frees its seats, so the next waiter is promoted in
// the same transaction (Whittler, PR 109).
func TestAdminDeleteUserPromotesWaiter(t *testing.T) {
	app := testApp(t)
	admin, gid := openGroup(t, app, "DeletePromote")
	eventID, date := seedCappedEvent(t, app, admin, gid, intPtr(1), true)
	var g models.Group
	app.DB.Where("id = ?", gid).First(&g)
	goer, cGoer := mkUser(t, app, &g, "del-goer")
	waiter, cWaiter := mkUser(t, app, &g, "del-waiter")
	for _, c := range []*apiClient{cGoer, cWaiter} {
		if code, body := registerModeAs(c, eventID, date, "going", models.AttendanceInPerson); code != http.StatusOK {
			t.Fatalf("register: %d %s", code, body)
		}
	}
	var occ models.EventOccurrence
	app.DB.Where("event_id = ?", eventID).First(&occ)
	if got := regStatus(t, app, waiter.ID, occ.ID); got != models.RegWaitlisted {
		t.Fatalf("waiter = %q, want waitlisted", got)
	}
	if w := admin.do("DELETE", "/api/v1/admin/users/"+itoa(goer.ID), nil, true); w.Code != http.StatusOK {
		t.Fatalf("delete: %d %s", w.Code, w.Body.String())
	}
	if got := regStatus(t, app, waiter.ID, occ.ID); got != models.RegGoing {
		t.Fatalf("waiter after delete = %q, want going", got)
	}
}

func TestHouseholdRestoreUsesCumulativePools(t *testing.T) {
	app := testApp(t)
	admin, gid := openGroup(t, app, "HouseholdRestore")
	eventID, date := seedChildcareEvent(t, app, admin, gid, intPtr(2), intPtr(1), true)
	apiDo(t, admin, "PUT", "/api/v1/events/"+itoa(eventID), map[string]any{"remoteAvailable": true, "remoteCapacity": 1}, http.StatusOK)
	holder, c, spouse, child := householdFamily(t, app, admin, "returningfamily")
	result := batchAnswer(t, c, eventID, date, map[string]any{"status": "going", "attendanceMode": "in_person", "people": []map[string]any{{"personId": spouse.ID, "status": "going", "attendanceMode": "remote"}, {"personId": child.ID, "status": "going", "childcareRequested": true}}}, http.StatusOK)
	occurrenceID := result.Registration.OccurrenceID
	apiDo(t, admin, "PUT", "/api/v1/admin/users/"+itoa(holder.ID), map[string]any{"status": "disabled"}, http.StatusOK)
	if used, err := seatsUsedForMode(app.DB, occurrenceID, models.AttendanceInPerson, 0); err != nil || used != 0 {
		t.Fatalf("disabled seats: %d %v", used, err)
	}
	if used, err := seatsUsedForMode(app.DB, occurrenceID, models.AttendanceRemote, 0); err != nil || used != 0 {
		t.Fatalf("disabled remote seats: %d %v", used, err)
	}
	if used, err := childcareUsedFor(app.DB, occurrenceID, 0); err != nil || used != 0 {
		t.Fatalf("disabled care: %d %v", used, err)
	}
	filler := newTestUser(t, app, "filler", "password1234", models.RoleMember)
	code, raw := registerAs(loginAPI(t, app, "filler", "password1234"), eventID, date, "going", nil)
	if code != 200 {
		t.Fatal(raw)
	}
	apiDo(t, admin, "PUT", "/api/v1/admin/users/"+itoa(holder.ID), map[string]any{"status": "active"}, http.StatusOK)
	if got := regStatus(t, app, holder.ID, occurrenceID); got != models.RegGoing {
		t.Fatalf("own answer %s", got)
	}
	if got := regStatus(t, app, filler.ID, occurrenceID); got != models.RegGoing {
		t.Fatalf("filler answer %s", got)
	}
	var rows []models.Registration
	app.DB.Where("registered_by = ?", holder.ID).Find(&rows)
	if len(rows) != 1 || rows[0].HouseholdPersonID == nil || *rows[0].HouseholdPersonID != spouse.ID || rows[0].Status != models.RegGoing {
		t.Fatalf("cumulative restoration %+v", rows)
	}
	page, _ := getEvent(t, admin, eventID, http.StatusOK)
	if len(page.Occurrences[0].RemovedRegistrations) != 1 || page.Occurrences[0].RemovedRegistrations[0].HouseholdPerson == nil || page.Occurrences[0].RemovedRegistrations[0].HouseholdPerson.PersonID != child.ID {
		t.Fatalf("recovery %+v", page.Occurrences[0].RemovedRegistrations)
	}
	apiDo(t, admin, "POST", "/api/v1/events/"+itoa(eventID)+"/registrations/add", map[string]any{"date": date, "personId": child.ID, "registeredBy": holder.ID, "overCapacity": true}, http.StatusOK)
	page, _ = getEvent(t, admin, eventID, http.StatusOK)
	if len(page.Occurrences[0].RemovedRegistrations) != 0 {
		t.Fatal("restored person still appears in recovery")
	}
	if got := findKind(feed(t, loginAPI(t, app, "returningfamily", "password1234"), ""), "registration.not_restored"); got == nil || !strings.Contains(got.Title, "Kit") {
		t.Fatalf("holder notice %+v", got)
	}
}

func TestAdminSelectionAdultAndRawTeenAdd(t *testing.T) {
	app, actor, client := peopleTestApp(t)
	g := newTestGroup(t, app, actor, "Admin selection", models.GroupActive)
	adult := newTestUser(t, app, "admin chosen", "pw", models.RoleMember)
	token, _ := app.newPeopleSelection(actor.ID, adult.ID, time.Now())
	path := "/api/v1/admin/groups/" + itoa(g.ID) + "/members"
	w := client.do("POST", path, map[string]any{"selectionId": token}, true)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var n int64
	app.DB.Model(&models.AuditLog{}).Where("action = ?", audit.MemberAdded).Count(&n)
	if n != 1 {
		t.Fatal(n)
	}
	teen := newTestUser(t, app, "admin teen", "pw", models.RoleMember)
	birth := time.Now().AddDate(-15, 0, 0)
	app.DB.Model(teen).Updates(map[string]any{"account_type": models.AccountTeen, "birth_date": birth})
	w = client.do("POST", path, map[string]any{"userId": teen.ID}, true)
	if w.Code != 409 || !strings.Contains(w.Body.String(), "teen_add_approval_required") {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestAdminMemberAddRefreshesActorAfterContactGate(t *testing.T) {
	for _, targetKind := range []string{"selection", "userId"} {
		for change, revocation := range peopleAddActorRevocations(&models.User{}) {
			t.Run(targetKind+"/"+revocation.name, func(t *testing.T) {
				app, actor, client := peopleTestApp(t)
				leader := newTestUser(t, app, "admin destination leader", "pw", models.RoleMember)
				g := newTestGroup(t, app, leader, "Admin authority", models.GroupActive)
				target := newTestUser(t, app, "private admin target", "pw", models.RoleMember)
				body := map[string]any{"userId": target.ID}
				if targetKind == "selection" {
					token, err := app.newPeopleSelection(actor.ID, target.ID, time.Now())
					if err != nil {
						t.Fatal(err)
					}
					body = map[string]any{"selectionId": token}
				}
				gate := holdContactGate(t, app)
				response := queueContact(func() *httptest.ResponseRecorder {
					return client.do("POST", "/api/v1/admin/groups/"+itoa(g.ID)+"/members", body, true)
				})
				waitContactWaiters(t, app, 1)
				revocation := peopleAddActorRevocations(actor)[change]
				if err := gate.Model(actor).Updates(revocation.updates).Error; err != nil {
					t.Fatal(err)
				}
				if err := gate.Commit().Error; err != nil {
					t.Fatal(err)
				}
				w := awaitContactResponse(t, response)
				// Destination-policy refusal retains the admin endpoint's 400;
				// an invalid session/account fails with a generic 403.
				want := 403
				if revocation.name == "demotion" {
					want = 400
				}
				if w.Code != want || strings.Contains(w.Body.String(), target.Email) {
					t.Fatalf("%s: %d %s", revocation.name, w.Code, w.Body.String())
				}
				if membershipStatus(app, target, g) != "none" || contactCount(t, app, &models.AuditLog{}, "action IN ?", []string{audit.MemberAdded, audit.AddedOverLimit, audit.RoleChanged}) != 0 {
					t.Fatal("revoked admin committed membership or add audit")
				}
			})
		}
	}
}

func TestAdminMemberAddStillChangesExplicitRoleAndKeepsLastLeader(t *testing.T) {
	app, actor, client := peopleTestApp(t)
	leader := newTestUser(t, app, "admin role target", "pw", models.RoleMember)
	g := newTestGroup(t, app, leader, "Explicit admin role", models.GroupActive)
	addMember(t, app, actor, g, models.MembershipLeader)
	path := "/api/v1/admin/groups/" + itoa(g.ID) + "/members"
	w := client.do("POST", path, map[string]any{"userId": actor.ID, "role": models.MembershipMember}, true)
	if w.Code != 200 || relIn(app.DB, actor.ID, g.ID).Leader {
		t.Fatal("admin add did not change explicit role", w.Code, w.Body.String())
	}
	w = client.do("POST", path, map[string]any{"userId": leader.ID, "role": models.MembershipMember}, true)
	if w.Code != 400 || !relIn(app.DB, leader.ID, g.ID).Leader {
		t.Fatal("admin add demoted sole leader", w.Code, w.Body.String())
	}
	if contactCount(t, app, &models.AuditLog{}, "action = ?", audit.RoleChanged) != 1 || contactCount(t, app, &models.AuditLog{}, "action = ?", audit.MemberAdded) != 0 {
		t.Fatal("explicit role change audits changed")
	}
}

func TestAdminCreateUserRechecksAuthorityAtInsertion(t *testing.T) {
	for _, mutation := range []string{"session", "role", "disabled", "deleted"} {
		t.Run(mutation, func(t *testing.T) {
			app := testApp(t)
			actor := newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
			client := loginAPI(t, app, "admin", "password1234")
			gate := holdContactGate(t, app)
			response := queueContact(func() *httptest.ResponseRecorder {
				return client.do("POST", "/api/v1/admin/users", map[string]string{
					"ageBand": "adult", "name": "New account", "email": "new@example.com", "password": "password1234",
				}, true)
			})
			waitContactWaiters(t, app, 1)
			var err error
			switch mutation {
			case "session":
				err = gate.Model(actor).Update("session_version", actor.SessionVersion+1).Error
			case "role":
				err = gate.Model(actor).Update("role", models.RoleMember).Error
			case "disabled":
				err = gate.Model(actor).Update("status", models.UserDisabled).Error
			case "deleted":
				err = gate.Delete(actor).Error
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := gate.Commit().Error; err != nil {
				t.Fatal(err)
			}
			if w := awaitContactResponse(t, response); w.Code != http.StatusUnauthorized {
				t.Fatalf("revoked create: %d %s", w.Code, w.Body.String())
			}
			if contactCount(t, app, &models.User{}, "email = ?", "new@example.com") != 0 ||
				contactCount(t, app, &models.AuditLog{}, "action = ?", audit.UserCreated) != 0 ||
				contactCount(t, app, &models.Notification{}, "1=1") != 0 ||
				contactCount(t, app, &models.MailOutbox{}, "1=1") != 0 {
				t.Fatal("revoked creation left side effects")
			}
		})
	}
}

func TestAdminCreateUserAuditAndRollback(t *testing.T) {
	app := testApp(t)
	actor := newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
	client := loginAPI(t, app, "admin", "password1234")
	body := map[string]string{"ageBand": "adult", "name": "Invited", "email": "invited@example.com"}
	if w := client.do("POST", "/api/v1/admin/users", body, false); w.Code != http.StatusForbidden {
		t.Fatalf("missing CSRF: %d", w.Code)
	}
	if w := client.do("POST", "/api/v1/admin/users", body, true); w.Code != http.StatusCreated {
		t.Fatalf("create invited: %d %s", w.Code, w.Body.String())
	}
	var invited models.User
	if err := app.DB.Where("email = ?", body["email"]).First(&invited).Error; err != nil {
		t.Fatal(err)
	}
	if invited.Status != models.UserInvited || invited.PasswordHash != nil {
		t.Fatal("passwordless account not invited")
	}
	var entry models.AuditLog
	if err := app.DB.Where("action = ?", audit.UserCreated).First(&entry).Error; err != nil {
		t.Fatal(err)
	}
	if entry.ActorID == nil || *entry.ActorID != actor.ID || entry.TargetID != invited.ID || entry.TargetType != "user" || strings.Contains(entry.MetaJSON, "password") {
		t.Fatalf("unexpected creation audit: %+v", entry)
	}
	const callback = "admin-create:fail-audit"
	if err := app.DB.Callback().Create().Before("gorm:create").Register(callback, func(db *gorm.DB) {
		if db.Statement.Table == "audit_logs" {
			db.AddError(errors.New("injected creation audit failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { app.DB.Callback().Create().Remove(callback) })
	body["email"] = "rollback@example.com"
	if w := client.do("POST", "/api/v1/admin/users", body, true); w.Code != http.StatusInternalServerError {
		t.Fatalf("audit failure: %d %s", w.Code, w.Body.String())
	}
	if contactCount(t, app, &models.User{}, "email = ?", body["email"]) != 0 ||
		contactCount(t, app, &models.AuditLog{}, "action = ?", audit.UserCreated) != 1 ||
		contactCount(t, app, &models.Notification{}, "1=1") != 0 ||
		contactCount(t, app, &models.MailOutbox{}, "1=1") != 0 {
		t.Fatal("creation rollback or no-notification contract failed")
	}
}

func TestAdminRoleAndEmailChangesAreAudited(t *testing.T) {
	app := testApp(t)
	admin := newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
	c := loginAPI(t, app, "admin", "password1234")
	newTestUser(t, app, "second admin", "password1234", models.RoleAdmin)
	u := newTestUser(t, app, "plain", "password1234", models.RoleMember)
	path := "/api/v1/admin/users/" + itoa(u.ID)

	apiDo(t, c, "PUT", path, map[string]any{"role": models.RoleAdmin}, http.StatusOK)
	apiDo(t, c, "PUT", path, map[string]any{"role": models.RoleAdmin}, http.StatusOK) // no change, no row
	rows, metas := auditRows(t, app, audit.UserRoleChanged)
	if len(rows) != 1 || rows[0].TargetID != u.ID || rows[0].TargetType != "user" || rows[0].ActorID == nil || *rows[0].ActorID != admin.ID ||
		metas[0].OldRole != models.RoleMember || metas[0].NewRole != models.RoleAdmin {
		t.Fatalf("role audit: %+v %+v", rows, metas)
	}

	apiDo(t, c, "PUT", path, map[string]any{"email": "changed@example.com"}, http.StatusOK)
	rows, _ = auditRows(t, app, audit.UserEmailChanged)
	if len(rows) != 1 || rows[0].TargetID != u.ID || !strings.Contains(rows[0].MetaJSON, "changed@example.com") || !strings.Contains(rows[0].MetaJSON, u.Email) {
		t.Fatalf("email audit: %+v", rows)
	}

	// A name-only edit writes neither.
	apiDo(t, c, "PUT", path, map[string]any{"name": "Renamed"}, http.StatusOK)
	if auditCount(t, app, audit.UserRoleChanged) != 1 || auditCount(t, app, audit.UserEmailChanged) != 1 {
		t.Fatal("name-only edit wrote an audit row")
	}
}

func TestAdminCreateUserAuditRecordsRole(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
	c := loginAPI(t, app, "admin", "password1234")
	apiDo(t, c, "POST", "/api/v1/admin/users", map[string]any{"ageBand": "adult", "email": "newadmin@example.com", "name": "New Admin", "role": models.RoleAdmin}, http.StatusCreated)
	var entry models.AuditLog
	if err := app.DB.Where("action = ?", audit.UserCreated).First(&entry).Error; err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.ReplaceAll(entry.MetaJSON, " ", ""), `"role":"admin"`) {
		t.Fatalf("creation audit meta: %s", entry.MetaJSON)
	}
}

// A leader hears how an admin decided their new group, whether the admin used
// Approve/Reject or edited the group's status (DEC-96).
func TestLeadersHearGroupDecisions(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
	leader := newTestUser(t, app, "leader", "password1234", models.RoleMember)
	admin := loginAPI(t, app, "admin", "password1234")
	lc := loginAPI(t, app, "leader", "password1234")
	create := func(name string) string {
		t.Helper()
		var body struct {
			Group groupSummaryJSON `json:"group"`
		}
		decodeJSON(t, lc.do("POST", "/api/v1/groups", map[string]any{"name": name}, true), &body)
		if body.Group.Status != models.GroupPending {
			t.Fatalf("%s: status %q, want pending", name, body.Group.Status)
		}
		return itoa(body.Group.ID)
	}
	notice := func(kind, gid string) models.Notification {
		t.Helper()
		var n models.Notification
		if err := app.DB.Where("user_id = ? AND kind = ? AND subject_id = ?", leader.ID, kind, gid).First(&n).Error; err != nil {
			t.Fatalf("%s for group %s: %v", kind, gid, err)
		}
		return n
	}

	approved := create("Choir")
	if w := admin.do("POST", "/api/v1/admin/groups/"+approved+"/approve", map[string]any{}, true); w.Code != http.StatusOK {
		t.Fatalf("approve: %d %s", w.Code, w.Body.String())
	}
	if n := notice("group.approved", approved); n.Title != "Choir was approved" || n.Link != "/groups/"+approved {
		t.Fatalf("approved notice: %+v", n)
	}

	rejected := create("Night hikes")
	if w := admin.do("POST", "/api/v1/admin/groups/"+rejected+"/reject", map[string]any{}, true); w.Code != http.StatusOK {
		t.Fatalf("reject: %d %s", w.Code, w.Body.String())
	}
	if n := notice("group.rejected", rejected); n.Title != "Night hikes wasn't approved" {
		t.Fatalf("rejected notice: %+v", n)
	}

	edited := create("Book club")
	if w := admin.do("PUT", "/api/v1/groups/"+edited, map[string]any{"status": models.GroupActive}, true); w.Code != http.StatusOK {
		t.Fatalf("activate by edit: %d %s", w.Code, w.Body.String())
	}
	notice("group.approved", edited)

	// Set back to pending and approved again: a second decision is a second notice.
	if w := admin.do("PUT", "/api/v1/groups/"+approved, map[string]any{"status": models.GroupPending}, true); w.Code != http.StatusOK {
		t.Fatalf("back to pending: %d %s", w.Code, w.Body.String())
	}
	if w := admin.do("POST", "/api/v1/admin/groups/"+approved+"/approve", map[string]any{}, true); w.Code != http.StatusOK {
		t.Fatalf("approve again: %d %s", w.Code, w.Body.String())
	}
	if n := contactCount(t, app, &models.Notification{}, "user_id = ? AND kind = ? AND subject_id = ?", leader.ID, "group.approved", approved); n != 2 {
		t.Fatalf("approved notices after a second decision: %d, want 2", n)
	}

	// A leader archiving their own pending group isn't an admin's decision, so
	// a co-leader hears nothing.
	coLeader := newTestUser(t, app, "co-leader", "password1234", models.RoleMember)
	withdrawn := create("Withdrawn idea")
	var wg models.Group
	app.DB.First(&wg, withdrawn)
	addMember(t, app, coLeader, &wg, models.MembershipLeader)
	if w := lc.do("PUT", "/api/v1/groups/"+withdrawn, map[string]any{"status": models.GroupArchived}, true); w.Code != http.StatusOK {
		t.Fatalf("leader archives own pending group: %d %s", w.Code, w.Body.String())
	}
	if n := contactCount(t, app, &models.Notification{}, "subject_id = ? AND kind IN ?", withdrawn, []string{"group.approved", "group.rejected"}); n != 0 {
		t.Fatalf("a leader's own withdrawal sent %d decision notices", n)
	}

	// The deciding admin, who isn't a leader, gets nothing; nobody else does either.
	if n := contactCount(t, app, &models.Notification{}, "kind IN ? AND user_id NOT IN ?", []string{"group.approved", "group.rejected"}, []uint{leader.ID}); n != 0 {
		t.Fatalf("%d decision notices went to someone other than the leader", n)
	}
}

// A reject may carry a short message to the leaders (DEC-96). It reaches the
// notice and the audit row, and like all notification text it never carries
// an email address or phone number.
func TestRejectMessageReachesLeaders(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
	leader := newTestUser(t, app, "leader", "password1234", models.RoleMember)
	admin := loginAPI(t, app, "admin", "password1234")
	lc := loginAPI(t, app, "leader", "password1234")
	var body struct {
		Group groupSummaryJSON `json:"group"`
	}
	decodeJSON(t, lc.do("POST", "/api/v1/groups", map[string]any{"name": "Late night karaoke"}, true), &body)
	gid := itoa(body.Group.ID)

	for msg, code := range map[string]string{
		"Write to pastor@example.org": "message_has_contact",
		"Call me at 208-555-0143":     "message_has_contact",
		strings.Repeat("x", 501):      "too_long",
	} {
		w := admin.do("POST", "/api/v1/admin/groups/"+gid+"/reject", map[string]any{"message": msg}, true)
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), `"code":"`+code+`"`) {
			t.Fatalf("message %.30q: %d %s, want 400 %s", msg, w.Code, w.Body.String(), code)
		}
	}
	var g models.Group
	app.DB.First(&g, body.Group.ID)
	if g.Status != models.GroupPending {
		t.Fatalf("a refused message still decided the group: %s", g.Status)
	}

	msg := "We already run a similar group on Fridays. Join that one!"
	if w := admin.do("POST", "/api/v1/admin/groups/"+gid+"/reject", map[string]any{"message": "  " + msg + "  "}, true); w.Code != http.StatusOK {
		t.Fatalf("reject: %d %s", w.Code, w.Body.String())
	}
	var n models.Notification
	if err := app.DB.Where("user_id = ? AND kind = ?", leader.ID, "group.rejected").First(&n).Error; err != nil {
		t.Fatal(err)
	}
	if n.Body != "An admin decided not to add it. Their note: "+msg {
		t.Fatalf("notice body %q", n.Body)
	}
	if contactCount(t, app, &models.AuditLog{}, "action = ? AND target_id = ? AND meta_json->>'message' = ?", audit.GroupRejected, body.Group.ID, msg) != 1 {
		t.Fatal("the audit row should keep the message")
	}
}

func TestHasContactDetails(t *testing.T) {
	for s, want := range map[string]bool{
		"Write to pastor@example.org":     true,
		"Call 208-555-0143":               true,
		"Call (208) 555 0143":             true,
		"Call +1 208 555 0143":            true,
		"Text 555-0143 after six":         true,
		"Let's meet 2026-11-05":           false,
		"Try the 2026-2027 season":        false,
		"Starting 10.5.2026":              false,
		"June 1 - 2026":                   false,
		"Room 201, Fridays at 7:00 PM":    false,
		"Capacity 100, ages 5-12":         false,
		"In 2026 11 groups met 12 times.": false,
	} {
		if got := hasContactDetails(s); got != want {
			t.Errorf("hasContactDetails(%q) = %v, want %v", s, got, want)
		}
	}
}

func TestGuardianLinkConsentMailEnqueueIsAtomic(t *testing.T) {
	app, guardian, _, link, _, admin := pendingGuardianEmailSetup(t)
	var before []models.Notification
	if err := app.DB.Order("id").Find(&before).Error; err != nil {
		t.Fatal(err)
	}
	if len(before) == 0 {
		t.Fatal("expected the pending guardian review task")
	}
	path := "/api/v1/admin/guardian-links/" + itoa(link.ID) + "/confirm"
	stop := failJobEnqueue(t, app.DB, kindGuardianConsentMail)
	apiDo(t, admin, "POST", path, map[string]string{"checkNote": "known family"}, http.StatusInternalServerError)
	stop()
	var after []models.Notification
	if err := app.DB.Order("id").Find(&after).Error; err != nil {
		t.Fatal(err)
	}
	var fresh models.GuardianLink
	if err := app.DB.First(&fresh, link.ID).Error; err != nil {
		t.Fatal(err)
	}
	if fresh.Status != models.GuardianPending || fresh.ConfirmedBy != nil || fresh.DecidedAt != nil || fresh.CheckNote != "" ||
		auditCount(t, app, audit.GuardianLinkConfirmed) != 0 || len(mailJobsOfKind(t, app, kindGuardianConsentMail)) != 0 ||
		!reflect.DeepEqual(before, after) || len(accountTokens(t, app, guardian.ID, models.TokenTeenConsent)) != 0 || mailCount(t, app, "") != 0 {
		t.Fatal("enqueue failure committed confirmation, task resolution, audit, token or mail")
	}
	apiDo(t, admin, "POST", path, map[string]string{"checkNote": "known family"}, http.StatusOK)
	jobs := mailJobsOfKind(t, app, kindGuardianConsentMail)
	if len(jobs) != 1 || jobs[0].Status != models.JobQueued || jobs[0].UniqueKey != nil || jobs[0].MaxAttempts != directMailAttempts ||
		!payloadEquals(jobs[0], guardianConsentMailJob{GuardianLinkID: link.ID}) || auditCount(t, app, audit.GuardianLinkConfirmed) != 1 {
		t.Fatal("confirmation did not commit its audit and ID-only job together")
	}
	if len(accountTokens(t, app, guardian.ID, models.TokenTeenConsent)) != 0 || mailCount(t, app, "") != 0 {
		t.Fatal("confirmation minted a token or sent mail before durable delivery")
	}
	if err := app.DB.Order("id").Find(&after).Error; err != nil {
		t.Fatal(err)
	}
	for _, row := range after {
		if row.SubjectID != nil && *row.SubjectID == link.ID && row.Waiting && row.ResolvedAt == nil {
			t.Fatal("confirmation left a review task waiting")
		}
	}
	apiDo(t, admin, "POST", path, map[string]string{"checkNote": "known family"}, http.StatusConflict)
	if len(mailJobsOfKind(t, app, kindGuardianConsentMail)) != 1 {
		t.Fatal("a repeated confirmation queued another duty")
	}
}

func TestGuardianLinkRejectionDoesNotQueueConsentMail(t *testing.T) {
	app, guardian, _, link, _, admin := pendingGuardianEmailSetup(t)
	apiDo(t, admin, "POST", "/api/v1/admin/guardian-links/"+itoa(link.ID)+"/reject", map[string]string{"note": "not confirmed"}, http.StatusOK)
	if len(mailJobsOfKind(t, app, kindGuardianConsentMail)) != 0 || len(accountTokens(t, app, guardian.ID, models.TokenTeenConsent)) != 0 || auditCount(t, app, audit.GuardianLinkRejected) != 1 {
		t.Fatal("rejection queued consent mail or did not record its decision")
	}
}

// deleteUserAsync sends the real admin DELETE on its own goroutine.
func deleteUserAsync(client *apiClient, id uint) <-chan int {
	code := make(chan int, 1)
	go func() { code <- client.do("DELETE", "/api/v1/admin/users/"+itoa(id), nil, true).Code }()
	return code
}

func awaitCode(t *testing.T, code <-chan int) int {
	t.Helper()
	select {
	case c := <-code:
		return c
	case <-time.After(10 * time.Second):
		t.Fatal("request never finished")
		return 0
	}
}

// Deleting an account (the real handler, behind the contact gate, unchanged)
// against a notice being written, in both orders. Sender first: an open
// transaction that has sent the person a notice holds their row, so the delete
// waits for it and then removes the notice with the account. Deletion first:
// a Send already naming the person (and someone else) waits on the delete,
// rechecks, and leaves the deleted person out; the other recipient still gets
// theirs.
func TestAdminDeleteSerializesWithNotices(t *testing.T) {
	msg := func(to ...uint) notify.Msg {
		return notify.Msg{Kind: notify.GroupApproved, To: to, Link: "/", Vars: notify.Vars{Group: "Youth"}}
	}
	t.Run("sender first", func(t *testing.T) {
		app := testApp(t)
		newTestUser(t, app, "admin", "pw", models.RoleAdmin)
		target := newTestUser(t, app, "target", "pw", models.RoleMember)
		client := loginAPI(t, app, "admin", "pw")
		sender, sPID := beginOwnedTx(t, app.DB)
		if ids, err := notify.Send(sender, msg(target.ID)); err != nil || len(ids) != 1 {
			t.Fatalf("send: %v %v", ids, err)
		}
		code := deleteUserAsync(client, target.ID)
		waiterOn(t, app.DB, sPID)
		if err := sender.Commit().Error; err != nil {
			t.Fatal(err)
		}
		if got := awaitCode(t, code); got != http.StatusOK {
			t.Fatalf("delete: %d", got)
		}
		if n, d := rowsFor(t, app.DB, "notifications", target.ID), rowsFor(t, app.DB, "notification_deliveries", target.ID); n != 0 || d != 0 {
			t.Fatalf("the deleted account kept %d notices and %d delivery rows", n, d)
		}
	})
	t.Run("deletion first", func(t *testing.T) {
		app := testApp(t)
		newTestUser(t, app, "admin", "pw", models.RoleAdmin)
		target := newTestUser(t, app, "target", "pw", models.RoleMember)
		other := newTestUser(t, app, "other", "pw", models.RoleMember)
		client := loginAPI(t, app, "admin", "pw")
		// The delete stops inside its transaction, after it has the user row
		// FOR UPDATE and before it removes the account's notices.
		const key = 7401003
		pauseOnStatement(t, app.DB, "BEFORE DELETE", "notifications", key)
		release, hPID := holdAdvisory(t, app.DB, key)
		code := deleteUserAsync(client, target.ID)
		handler := waiterOn(t, app.DB, hPID)
		sender, sPID := beginOwnedTx(t, app.DB)
		var ids []uint
		sent := make(chan error, 1)
		go func() {
			var err error
			ids, err = notify.Send(sender, msg(target.ID, other.ID))
			sent <- err
		}()
		waitDBBlocked(t, app.DB, sPID, handler)
		release()
		if got := awaitCode(t, code); got != http.StatusOK {
			t.Fatalf("delete: %d", got)
		}
		if err := awaitDBResult(t, sent); err != nil {
			t.Fatal(err)
		}
		if err := sender.Commit().Error; err != nil {
			t.Fatal(err)
		}
		if len(ids) != 1 || rowsFor(t, app.DB, "notifications", target.ID) != 0 || rowsFor(t, app.DB, "notifications", other.ID) != 1 {
			t.Fatalf("send returned %v; target has %d notices, other has %d",
				ids, rowsFor(t, app.DB, "notifications", target.ID), rowsFor(t, app.DB, "notifications", other.ID))
		}
	})
}

// N-09 (inventory 9.2.2, a gated entry): an admin's decision on a pending group
// tells the group's leaders in the decision's own transaction, which holds the
// contact gate. The real admin delete takes the same gate, so the two cannot
// interleave. Entry traced: POST /admin/groups/:id/approve|reject ->
// decideGroup -> contactMutation (gate first) -> the status UPDATE, task
// resolve, notifyGroupDecided (leaderIDs read from memberships, then Send with
// its own recipient lock), audit. No user row is locked before the send.
//
// Sender first: the decision stops right after it wrote the erased co-leader's
// notice; the delete waits on the gate, then removes the notice. Deletion first:
// the delete holds the gate and the user row; the decision waits on the gate,
// then reads the leaders after the erasure (their membership went with the
// account), so only the other leader is told. The decision answers 200 in both
// orders: the erased person is only a recipient. The sibling entry
// apiUpdateGroup (a leader's own edit) reaches the same notifyGroupDecided and
// is not run here.
func TestGroupDecisionNoticeSerializesWithRealAdminDelete(t *testing.T) {
	for i, decision := range []string{"approve", "reject"} {
		t.Run(decision, func(t *testing.T) {
			kind, status, action := notify.GroupApproved, models.GroupActive, audit.GroupApproved
			if decision == "reject" {
				kind, status, action = notify.GroupRejected, models.GroupArchived, audit.GroupRejected
			}
			keyA := int64(7402101 + 2*i)
			runGatedErasureBarriers(t, keyA, keyA+1, func(t *testing.T) gatedErasureScene {
				app := testApp(t)
				newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
				newTestUser(t, app, "eraser", "password1234", models.RoleAdmin)
				kept := newTestUser(t, app, "leader", "password1234", models.RoleMember)
				victim := newTestUser(t, app, "coleader", "password1234", models.RoleMember)
				g := newTestGroup(t, app, kept, "Awaiting approval", models.GroupPending)
				addMember(t, app, victim, g, models.MembershipLeader)
				decider, eraser := loginHeld(t, app, "admin", "password1234"), loginHeld(t, app, "eraser", "password1234")
				return gatedErasureScene{app: app, victim: victim, sends: []string{kind},
					run: func() error {
						return doExpect(decider, "POST", "/api/v1/admin/groups/"+itoa(g.ID)+"/"+decision, map[string]any{}, http.StatusOK)
					},
					erase: eraseVia(eraser, victim),
					told: func(t *testing.T) {
						var got models.Group
						if err := app.DB.First(&got, g.ID).Error; err != nil || got.Status != status {
							t.Fatalf("group after the decision: %+v %v, want status %s", got, err, status)
						}
						if n := auditCount(t, app, action); n != 1 {
							t.Fatalf("%d %s audit rows, want 1", n, action)
						}
						wantNotices(t, app, kind, map[*models.User]int64{kept: 1})
					},
				}
			})
		})
	}
}

// N-15 and N-30 (inventory 9.2.2, gated entries): an admin re-enabling a
// disabled account puts back their memberships and sign-ups that still fit and
// tells them about each one that does not (DEC-47). The real operation is
// PUT /admin/users/:id {status: active} -> apiAdminUpdateUser ->
// contactMutation (gate first) -> lockConsentUsers (users NO KEY UPDATE, by id)
// -> restoreMemberships (group row FOR UPDATE; membership removed, audit,
// notifyNotRestored: N-15) -> restoreRegistrations (occurrence row FOR UPDATE;
// registration removed, audit, notifyRegistrationNotRestored: N-30). The
// recipient of both notices is the re-enabled person, so the person the
// erasure removes IS the subject of the request.
//
// The fixture is real routes except two direct inserts: the filler's and
// newbie's memberships (addMember) and the group's member limit, set to the
// current count so the group is full. Sender first: the re-enable stops after
// the first of its two notices to the victim; the delete waits on the gate;
// both notices are recorded (the audit rows precede each send). Deletion first:
// the request waits on the gate, then its locked read no longer finds the
// person and answers 404 "user not found" (it was a 500 before; see the
// errAdminTargetGone edit in api_admin.go), with no restore, no not-restored
// audit, no notice. Not covered: the same restore from a teen-consent grant, a
// household person's registration, and the other callers (decideAccountRequest,
// rolloverAges), which reach the same two notifiers.
func TestReenableNoticesSerializeWithRealAdminDelete(t *testing.T) {
	var order string
	runGatedErasureBarriers(t, 7402105, 7402106, func(t *testing.T) gatedErasureScene {
		app := testApp(t)
		admin, gid := openGroup(t, app, "Reenable race")
		newTestUser(t, app, "eraser", "password1234", models.RoleAdmin)
		victim := newTestUser(t, app, "returning", "password1234", models.RoleMember)
		filler := newTestUser(t, app, "filler", "password1234", models.RoleMember)
		newbie := newTestUser(t, app, "newbie", "password1234", models.RoleMember)
		var g models.Group
		if err := app.DB.Where("id = ?", gid).First(&g).Error; err != nil {
			t.Fatal(err)
		}
		addMember(t, app, victim, &g, models.MembershipMember)
		addMember(t, app, filler, &g, models.MembershipMember)
		eventID, date := seedCappedEvent(t, app, admin, gid, intPtr(1), false)
		if code, body := registerAs(loginAPI(t, app, "returning", "password1234"), eventID, date, "going", nil); code != http.StatusOK {
			t.Fatalf("victim going: %d %s", code, body)
		}
		apiDo(t, admin, "PUT", "/api/v1/admin/users/"+itoa(victim.ID), map[string]any{"status": "disabled"}, http.StatusOK)
		// While they are away someone takes the seat and someone takes the place.
		if code, body := registerAs(loginAPI(t, app, "filler", "password1234"), eventID, date, "going", nil); code != http.StatusOK {
			t.Fatalf("filler going: %d %s", code, body)
		}
		addMember(t, app, newbie, &g, models.MembershipMember)
		limit := membersIn(app.DB, g.ID)
		if err := app.DB.Model(&models.Group{}).Where("id = ?", g.ID).Update("member_limit", limit).Error; err != nil {
			t.Fatal(err)
		}
		admin.holdNotices = true
		eraser := loginHeld(t, app, "eraser", "password1234")
		enable := func(name string, want int) func() error {
			return func() error {
				order = name
				return doExpect(admin, "PUT", "/api/v1/admin/users/"+itoa(victim.ID), map[string]any{"status": "active"}, want)
			}
		}
		return gatedErasureScene{app: app, victim: victim,
			sends:            []string{notify.MemberNotRestored, notify.RegistrationNotRestored},
			run:              enable("sender first", http.StatusOK),
			runDeletionFirst: enable("deletion first", http.StatusNotFound),
			erase:            eraseVia(eraser, victim),
			told: func(t *testing.T) {
				want := int64(1)
				if order == "deletion first" {
					want = 0 // the request found nobody to re-enable
				}
				for _, action := range []string{audit.MemberNotRestored, audit.RegistrationNotRestored, audit.UserEnabled} {
					if n := auditCount(t, app, action); n != want {
						t.Fatalf("%d %s audit rows after %s, want %d", n, action, order, want)
					}
				}
			},
		}
	})
}

// Erasure clears the account's avatar key and queues photo cleanup in its own
// transaction. The request deletes no file; the job removes the folder, even
// the orphans an earlier job did not know about.
func TestAdminDeleteClearsAvatarKeyAndQueuesCleanup(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "pw", models.RoleAdmin)
	target := newTestUser(t, app, "target", "pw", models.RoleMember)
	adminC := loginAPI(t, app, "admin", "pw")
	targetC := loginAPI(t, app, "target", "pw")
	if w := targetC.doMultipart("POST", "/api/v1/me/avatar", "avatar", "me.png", tinyPNG(t)); w.Code != http.StatusOK {
		t.Fatalf("upload: %d", w.Code)
	}
	drainAvatarJobs(t, app) // the upload's job is done, so erasure must queue a fresh one
	writeUploadFile(t, avatarRootDir(app), fmt.Sprintf("avatars/%d/.tmp-orphan", target.ID), "x")

	if w := adminC.do("DELETE", "/api/v1/admin/users/"+itoa(target.ID), nil, true); w.Code != http.StatusOK {
		t.Fatalf("delete: %d %s", w.Code, w.Body.String())
	}
	var gone models.User
	if err := app.DB.Unscoped().First(&gone, target.ID).Error; err != nil {
		t.Fatal(err)
	}
	if gone.AvatarKey != "" || gone.DeletedAt.Time.IsZero() {
		t.Fatalf("after erasure: key %q deleted_at %v", gone.AvatarKey, gone.DeletedAt)
	}
	js := avatarJobsFor(t, app, target.ID)
	if len(js) != 2 || js[0].Status != models.JobDone || js[1].Status != models.JobQueued || js[1].UniqueKey != nil {
		t.Fatalf("jobs after erasure: %+v", js)
	}
	if got := avatarFiles(t, app, target.ID); len(got) != 2 {
		t.Fatalf("the request removed files: %v", got)
	}
	drainAvatarJobs(t, app)
	if got := avatarFiles(t, app, target.ID); got != nil {
		t.Fatalf("folder remains: %v", got)
	}
	var audits []models.AuditLog
	app.DB.Where("action = ? AND target_id = ?", audit.UserDeleted, target.ID).Find(&audits)
	if len(audits) != 1 || strings.Contains(audits[0].MetaJSON, "avatar") {
		t.Fatalf("audit rows: %+v", audits)
	}
	row, err := json.Marshal(audits[0])
	if err != nil {
		t.Fatal(err)
	}
	assertNoAvatarLeak(t, app, "erasure audit row", string(row), gone.Email)
	assertAvatarPayloads(t, app)
	// The erased person's open session is over: an upload now is a 401.
	w := targetC.doMultipart("POST", "/api/v1/me/avatar", "avatar", "me.png", tinyPNG(t))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("upload after erasure: %d %s", w.Code, w.Body.String())
	}
	assertNoAvatarLeak(t, app, "response", w.Body.String())
	if got := avatarFiles(t, app, target.ID); got != nil {
		t.Fatalf("an upload after erasure left %v", got)
	}
}

// A rolled-back erasure leaves the key, the file and the queue as they were.
func TestAdminDeleteRolledBackLeavesPhotoAlone(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "pw", models.RoleAdmin)
	target, cur := avatarUser(t, app, "target", "old.png")
	adminC := loginAPI(t, app, "admin", "pw")
	for _, q := range []string{
		`CREATE FUNCTION fold_test_refuse_erasure() RETURNS trigger LANGUAGE plpgsql AS $$
			BEGIN RAISE EXCEPTION 'erasure refused'; END $$`,
		fmt.Sprintf(`CREATE TRIGGER fold_test_refuse_erasure BEFORE UPDATE ON users FOR EACH ROW
			WHEN (OLD.deleted_at IS NULL AND NEW.deleted_at IS NOT NULL AND OLD.id = %d) EXECUTE FUNCTION fold_test_refuse_erasure()`, target.ID),
	} {
		if err := app.DB.Exec(q).Error; err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		app.DB.Exec("DROP TRIGGER IF EXISTS fold_test_refuse_erasure ON users")
		app.DB.Exec("DROP FUNCTION IF EXISTS fold_test_refuse_erasure()")
	})
	w := adminC.do("DELETE", "/api/v1/admin/users/"+itoa(target.ID), nil, true)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("delete: %d %s", w.Code, w.Body.String())
	}
	assertNoAvatarLeak(t, app, "response", w.Body.String(), cur)
	if avatarKeyOf(t, app, target.ID) != cur || len(avatarJobsFor(t, app, target.ID)) != 0 {
		t.Fatal("a rolled-back erasure changed the key or queued work")
	}
	if got := avatarFiles(t, app, target.ID); len(got) != 2 {
		t.Fatalf("a rolled-back erasure removed files: %v", got)
	}
	if err := app.runAvatarCleanup(context.Background(), target.ID); err != nil {
		t.Fatal(err)
	}
	if got := avatarFiles(t, app, target.ID); len(got) != 1 || got[0] != "current.png" {
		t.Fatalf("files %v", got)
	}
	// The sweep treats the person as live and keeps the photo.
	writeUploadFile(t, avatarRootDir(app), fmt.Sprintf("avatars/%d/.tmp-left", target.ID), "x")
	if err := app.sweepAvatarsLimit(context.Background(), time.Now(), avatarSweepLimit); err != nil {
		t.Fatal(err)
	}
	drainAvatarJobs(t, app)
	if got := avatarFiles(t, app, target.ID); len(got) != 1 || got[0] != "current.png" || avatarKeyOf(t, app, target.ID) != cur {
		t.Fatalf("after the sweep: files %v key %q", got, avatarKeyOf(t, app, target.ID))
	}
	if w := getUpload(t, app, "/uploads/"+cur); w.Code != http.StatusOK {
		t.Fatalf("GET photo: %d", w.Code)
	}
}

// Upload first: the blocked Put holds the user row, erasure (behind the
// contact gate) waits on it, and after the upload commits erasure clears the
// new key and queues cleanup, which removes the new file too.
func TestAdminDeleteWaitsForBlockedUploadThenClearsTheNewPhoto(t *testing.T) {
	app := testApp(t)
	store := installAvatarBarrier(app)
	store.putGate = make(chan struct{})
	newTestUser(t, app, "admin", "pw", models.RoleAdmin)
	target := newTestUser(t, app, "target", "pw", models.RoleMember)
	adminC := loginAPI(t, app, "admin", "pw")
	targetC := loginAPI(t, app, "target", "pw")
	data := tinyPNG(t)
	upload := make(chan int, 1)
	go func() { upload <- targetC.doMultipart("POST", "/api/v1/me/avatar", "avatar", "me.png", data).Code }()
	awaitSignal(t, store.putEntered)
	code := deleteUserAsync(adminC, target.ID)
	waitForRowLockWaiter(t, app)
	close(store.putGate)
	select {
	case c := <-upload:
		if c != http.StatusOK {
			t.Fatalf("upload answered %d", c)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("upload never finished")
	}
	if c := awaitCode(t, code); c != http.StatusOK {
		t.Fatalf("erasure answered %d", c)
	}
	var gone models.User
	if err := app.DB.Unscoped().First(&gone, target.ID).Error; err != nil || gone.AvatarKey != "" {
		t.Fatalf("after erasure: key %q err %v", gone.AvatarKey, err)
	}
	// The upload's job was still queued when erasure ran, so erasure added no
	// second row; the one job reads the cleared key.
	if js := avatarJobsFor(t, app, target.ID); len(js) != 1 || js[0].Status != models.JobQueued {
		t.Fatalf("jobs after erasure: %+v", js)
	}
	drainAvatarJobs(t, app)
	if got := avatarFiles(t, app, target.ID); got != nil {
		t.Fatalf("folder remains: %v", got)
	}
	if js := avatarJobsFor(t, app, target.ID); len(js) != 1 || js[0].Status != models.JobDone {
		t.Fatalf("jobs after the drain: %+v", js)
	}
	if n := auditCount(t, app, audit.UserDeleted); n != 1 {
		t.Fatalf("%d erasure audit rows", n)
	}
	assertAvatarPayloads(t, app)
}

// Cleanup first: a cleanup run holds the user row inside the store. Erasure,
// already holding the contact gate, waits on the row while that run holds
// no gate itself. On release the cleanup finishes and keeps the key it read;
// erasure then commits, adds a fresh job (the earlier one was running) and
// the second run empties the folder.
func TestAdminDeleteWaitsForRunningCleanup(t *testing.T) {
	app := testApp(t)
	store := installAvatarBarrier(app)
	store.cleanGate = make(chan struct{})
	newTestUser(t, app, "admin", "pw", models.RoleAdmin)
	target, cur := avatarUser(t, app, "target", "old.png")
	adminC := loginAPI(t, app, "admin", "pw")
	queueCleanup(t, app, target.ID)

	ran := make(chan error, 1)
	go func() {
		_, err := jobs.NewRunner(app.DB, app.avatarKinds()...).RunOnce(context.Background(), "test:cleanup")
		ran <- err
	}()
	awaitSignal(t, store.cleanEntered)
	code := deleteUserAsync(adminC, target.ID)
	erasurePID := waitForRowLockWaiter(t, app)
	if n := advisoryLocksHeldBy(t, app, erasurePID); n < 1 {
		t.Fatal("the waiting erasure does not hold the contact gate")
	}
	select {
	case c := <-code:
		t.Fatalf("erasure answered %d while the cleanup held the row", c)
	default:
	}
	close(store.cleanGate)
	if err := <-ran; err != nil {
		t.Fatal(err)
	}
	if c := awaitCode(t, code); c != http.StatusOK {
		t.Fatalf("erasure answered %d", c)
	}
	// The run kept the photo it read under the lock; erasure had not yet
	// cleared it.
	js := avatarJobsFor(t, app, target.ID)
	if len(js) != 2 || js[0].Status != models.JobDone || js[1].Status != models.JobQueued || js[1].UniqueKey != nil {
		t.Fatalf("jobs %+v", js)
	}
	if got := avatarFiles(t, app, target.ID); len(got) != 1 || got[0] != "current.png" {
		t.Fatalf("files after the first run %v (key was %s)", got, cur)
	}
	drainAvatarJobs(t, app)
	if got := avatarFiles(t, app, target.ID); got != nil {
		t.Fatalf("folder remains: %v", got)
	}
	var gone models.User
	if err := app.DB.Unscoped().First(&gone, target.ID).Error; err != nil || gone.AvatarKey != "" || gone.DeletedAt.Time.IsZero() {
		t.Fatalf("after erasure: key %q deleted %v err %v", gone.AvatarKey, gone.DeletedAt, err)
	}
	if n := auditCount(t, app, audit.UserDeleted); n != 1 {
		t.Fatalf("%d erasure audit rows, want 1", n)
	}
	assertAvatarPayloads(t, app)
}

// Erasure first, behind a long hold: a trigger sleeps 10 seconds inside the
// erasure's own transaction, which holds the user row. An upload with a
// still-valid session gives up on the row after its 5 second lock timeout:
// 500, no Put, no file, no job. Erasure then commits, and the next upload is
// a 401. The test asserts nothing about the order Postgres serves waiters.
func TestAvatarUploadBehindALongErasureHoldGivesUpThenIsRefused(t *testing.T) {
	app := testApp(t)
	store := installAvatarBarrier(app)
	newTestUser(t, app, "admin", "pw", models.RoleAdmin)
	target, cur := avatarUser(t, app, "target", "old.png")
	adminC := loginAPI(t, app, "admin", "pw")
	targetC := loginAPI(t, app, "target", "pw")
	for _, q := range []string{
		`CREATE FUNCTION fold_test_slow_erasure() RETURNS trigger LANGUAGE plpgsql AS $$
			BEGIN PERFORM pg_sleep(10); RETURN NEW; END $$`,
		fmt.Sprintf(`CREATE TRIGGER fold_test_slow_erasure BEFORE UPDATE ON users FOR EACH ROW
			WHEN (OLD.id = %d AND OLD.deleted_at IS NULL AND NEW.deleted_at IS NOT NULL) EXECUTE FUNCTION fold_test_slow_erasure()`, target.ID),
	} {
		if err := app.DB.Exec(q).Error; err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		app.DB.Exec("DROP TRIGGER IF EXISTS fold_test_slow_erasure ON users")
		app.DB.Exec("DROP FUNCTION IF EXISTS fold_test_slow_erasure()")
	})

	code := deleteUserAsync(adminC, target.ID)
	testdb.WaitUntil(t, "the erasure to start sleeping", 10*time.Second, func() bool {
		var sleeping int
		if err := app.DB.Raw("SELECT count(*) FROM pg_stat_activity WHERE datname = current_database() AND wait_event = 'PgSleep'").Scan(&sleeping).Error; err != nil {
			t.Fatal(err)
		}
		return sleeping > 0
	})
	start := time.Now()
	w := targetC.doMultipart("POST", "/api/v1/me/avatar", "avatar", "me.png", tinyPNG(t))
	d := time.Since(start)
	if w.Code != http.StatusInternalServerError || !strings.Contains(w.Body.String(), `"code":"server_error"`) {
		t.Fatalf("upload behind the hold: %d %s", w.Code, w.Body.String())
	}
	if d < 4*time.Second || d > 8*time.Second {
		t.Fatalf("upload gave up after %s, want about 5s", d)
	}
	assertNoAvatarLeak(t, app, "response", w.Body.String(), cur)
	if store.putCount() != 0 || len(avatarJobsFor(t, app, target.ID)) != 0 || len(avatarFiles(t, app, target.ID)) != 2 {
		t.Fatal("the refused upload left a Put, a job or a file")
	}
	if c := awaitCode(t, code); c != http.StatusOK {
		t.Fatalf("erasure answered %d", c)
	}
	if w := targetC.doMultipart("POST", "/api/v1/me/avatar", "avatar", "me.png", tinyPNG(t)); w.Code != http.StatusUnauthorized {
		t.Fatalf("upload after the erasure: %d %s", w.Code, w.Body.String())
	}
	if store.putCount() != 0 {
		t.Fatal("a Put ran for an erased account")
	}
	drainAvatarJobs(t, app)
	if got := avatarFiles(t, app, target.ID); got != nil {
		t.Fatalf("folder remains: %v", got)
	}
}

// Erasure first with a stale snapshot: storeAvatar called with a session read
// before a committed erasure refuses, makes no Put, creates no folder and
// queues no job.
func TestStoreAvatarWithSnapshotFromBeforeErasureRefuses(t *testing.T) {
	app := testApp(t)
	store := installAvatarBarrier(app)
	newTestUser(t, app, "admin", "pw", models.RoleAdmin)
	target := newTestUser(t, app, "target", "pw", models.RoleMember)
	adminC := loginAPI(t, app, "admin", "pw")
	var snapshot models.User
	if err := app.DB.First(&snapshot, target.ID).Error; err != nil {
		t.Fatal(err)
	}
	if w := adminC.do("DELETE", "/api/v1/admin/users/"+itoa(target.ID), nil, true); w.Code != http.StatusOK {
		t.Fatalf("delete: %d", w.Code)
	}
	data := tinyPNG(t)
	_, err := app.storeAvatar(context.Background(), &snapshot, bytes.NewReader(data), int64(len(data)), "image/png", time.Now().Add(time.Minute))
	if !errors.Is(err, errSecuritySession) {
		t.Fatalf("got %v, want the session error", err)
	}
	if store.putCount() != 0 || avatarFiles(t, app, target.ID) != nil {
		t.Fatal("a stale snapshot stored something")
	}
	for _, j := range avatarJobsFor(t, app, target.ID) {
		if j.Status != models.JobQueued {
			t.Fatalf("job %+v", j)
		}
	}
	if n := len(avatarJobsFor(t, app, target.ID)); n != 1 {
		t.Fatalf("%d jobs, want only the erasure's", n)
	}
}

// The last-admin rule decided on locked facts (M7.4 R4-1-3). Under the contact
// gate with the actor re-read as an active admin distinct from the target, the
// in-gate count is at least 2, so the check refuses nothing today's API can
// build; the tests pin the answers that must not move and the check itself.

// Two admins delete each other at once (F-1, closed by R4-H's actor re-read).
// Both requests queue on the real contact gate, so the order is the gate's:
// the first deletes the other admin, and the second finds its own admin
// deleted and answers signed_out. Exactly one admin remains.
func TestMutualAdminDeletesLeaveOneAdmin(t *testing.T) {
	s := newHostScene(t)
	db := s.app.DB
	gate := holdContactGate(t, s.app)
	aDeletesB := bgDo(s.a, "DELETE", "/api/v1/admin/users/"+itoa(s.adminB.ID), nil)
	bDeletesA := bgDo(s.b, "DELETE", "/api/v1/admin/users/"+itoa(s.adminA.ID), nil)
	waitContactWaiters(t, s.app, 2)
	if err := gate.Rollback().Error; err != nil {
		t.Fatal(err)
	}
	ra, rb := awaitResult(t, aDeletesB), awaitResult(t, bDeletesA)
	var winner, loser *models.User
	var lost httpResult
	switch {
	case ra.code == http.StatusOK && rb.code != http.StatusOK:
		winner, loser, lost = s.adminA, s.adminB, rb
	case rb.code == http.StatusOK && ra.code != http.StatusOK:
		winner, loser, lost = s.adminB, s.adminA, ra
	default:
		t.Fatalf("exactly one delete must succeed: A->B %d %s, B->A %d %s", ra.code, ra.body, rb.code, rb.body)
	}
	if lost.code != http.StatusUnauthorized || !strings.Contains(lost.body, `"code":"signed_out"`) {
		t.Fatalf("the loser answers %d %s, want 401 signed_out", lost.code, lost.body)
	}
	if got := s.app.adminCount(); got != 1 {
		t.Fatalf("%d active admins remain, want 1", got)
	}
	// The loser was erased; the winner, the loser's target, is untouched.
	var w, l models.User
	if err := db.Unscoped().First(&w, winner.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Unscoped().First(&l, loser.ID).Error; err != nil {
		t.Fatal(err)
	}
	if w.DeletedAt.Valid || w.Email != winner.Email || w.PasswordHash == nil || w.Status != models.UserActive {
		t.Fatalf("the surviving admin was changed: %+v", w)
	}
	if !l.DeletedAt.Valid || l.Email != fmt.Sprintf("deleted-%d@deleted.invalid", l.ID) {
		t.Fatalf("the deleted admin was not erased: %+v", l)
	}
}

// An admin who is disabled while the delete waits is no longer an active admin,
// so deleting them is allowed at any count (policy.CanDemoteOrDelete).
func TestDeleteOfAdminDisabledWhileWaiting(t *testing.T) {
	s := newHostScene(t)
	db := s.app.DB
	gate := holdContactGate(t, s.app)
	del := bgDo(s.a, "DELETE", "/api/v1/admin/users/"+itoa(s.adminB.ID), nil)
	waitContactWaiters(t, s.app, 1)
	if err := db.Exec("UPDATE users SET status = 'disabled' WHERE id = ?", s.adminB.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := gate.Rollback().Error; err != nil {
		t.Fatal(err)
	}
	if r := awaitResult(t, del); r.code != http.StatusOK {
		t.Fatalf("delete: %d %s", r.code, r.body)
	}
	var b models.User
	if err := db.Unscoped().First(&b, s.adminB.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !b.DeletedAt.Valid || b.Email != fmt.Sprintf("deleted-%d@deleted.invalid", b.ID) || b.PasswordHash != nil {
		t.Fatalf("the disabled admin was not erased: %+v", b)
	}
	if got := s.app.adminCount(); got != 1 {
		t.Fatalf("%d active admins, want 1 (the actor)", got)
	}
}

// PUT judges the effective change on the locked target. A target that is no
// longer an active admin when the gate opens is edited as before, and the
// effective status (invited, from a missing password) is seen by the check
// without changing today's answer.
func TestUpdateOfAdminDisabledWhileWaiting(t *testing.T) {
	cases := []struct {
		name     string
		prepare  string // SQL run on the target while the request waits
		body     map[string]any
		wantRole string
		check    func(t *testing.T, s hostScene, b models.User)
	}{
		{
			name: "demote", prepare: "UPDATE users SET status = 'disabled' WHERE id = ?",
			body: map[string]any{"role": models.RoleMember}, wantRole: models.RoleMember,
			check: func(t *testing.T, s hostScene, b models.User) {
				if b.Status != models.UserDisabled {
					t.Errorf("status = %q, want disabled", b.Status)
				}
			},
		},
		{
			name: "disable", prepare: "UPDATE users SET status = 'disabled' WHERE id = ?",
			body: map[string]any{"status": models.UserDisabled}, wantRole: models.RoleAdmin,
			check: func(t *testing.T, s hostScene, b models.User) {
				if b.Status != models.UserDisabled || !b.AdministrativelyDisabled {
					t.Errorf("status %q administratively_disabled %v, want disabled and true", b.Status, b.AdministrativelyDisabled)
				}
				if n := countRows(t, s.app.DB, "audit_logs", "action = ? AND target_id = ?", audit.UserDisabled, b.ID); n != 1 {
					t.Errorf("%d user.disabled audit rows, want 1", n)
				}
			},
		},
		{
			name: "effective status invited", prepare: "UPDATE users SET password_hash = NULL WHERE id = ?",
			body: map[string]any{"status": models.UserActive}, wantRole: models.RoleAdmin,
			check: func(t *testing.T, s hostScene, b models.User) {
				if b.Status != models.UserInvited {
					t.Errorf("status = %q, want invited", b.Status)
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newHostScene(t)
			db := s.app.DB
			gate := holdContactGate(t, s.app)
			put := bgDo(s.a, "PUT", "/api/v1/admin/users/"+itoa(s.adminB.ID), tc.body)
			waitContactWaiters(t, s.app, 1)
			if err := db.Exec(tc.prepare, s.adminB.ID).Error; err != nil {
				t.Fatal(err)
			}
			if err := gate.Rollback().Error; err != nil {
				t.Fatal(err)
			}
			if r := awaitResult(t, put); r.code != http.StatusOK {
				t.Fatalf("update: %d %s", r.code, r.body)
			}
			var b models.User
			if err := db.First(&b, s.adminB.ID).Error; err != nil {
				t.Fatal(err)
			}
			if b.Role != tc.wantRole {
				t.Errorf("role = %q, want %q", b.Role, tc.wantRole)
			}
			tc.check(t, s, b)
			// A is the one active admin in every case.
			if got := s.app.adminCount(); got != 1 {
				t.Errorf("%d active admins, want 1", got)
			}
		})
	}
}

// keepsAnAdminIn is the defense-in-depth branch: it counts in the caller's
// transaction, passes for a target that is not an active admin, and its three
// messages answer 400 last_admin.
func TestKeepsAnAdminDecidesOnTheInGateCount(t *testing.T) {
	app := testApp(t)
	a := newTestUser(t, app, "keeps-a", hostPW, models.RoleAdmin)
	b := newTestUser(t, app, "keeps-b", hostPW, models.RoleAdmin)
	// The in-memory actor is an active admin, the database says otherwise, so
	// the in-gate count is 1 (only the target).
	if err := app.DB.Exec("UPDATE users SET status = 'disabled' WHERE id = ?", a.ID).Error; err != nil {
		t.Fatal(err)
	}
	changes := []struct {
		change adminChange
		want   *apiError
	}{{adminDelete, errCannotDeleteLastAdmin}, {adminDemote, errCannotDemoteLastAdmin}, {adminDisable, errCannotDisableLastAdmin}}
	err := app.DB.Transaction(func(tx *gorm.DB) error {
		if n, err := adminCountIn(tx); err != nil || n != 1 {
			t.Fatalf("adminCountIn = %d, %v, want 1", n, err)
		}
		for _, tc := range changes {
			err := keepsAnAdminIn(tx, a, b, tc.change)
			if !errors.Is(err, tc.want) {
				t.Fatalf("n = 1: err = %v, want %q", err, tc.want.Message)
			}
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			fail(c, err)
			if w.Code != http.StatusBadRequest || errorCodeOf(w) != "last_admin" {
				t.Errorf("%q answers %d %q, want 400 last_admin", tc.want.Message, w.Code, errorCodeOf(w))
			}
		}
		// A target that is not an active admin is allowed at any count.
		disabled := *b
		disabled.Status = models.UserDisabled
		if err := keepsAnAdminIn(tx, a, &disabled, adminDelete); err != nil {
			t.Fatalf("a disabled target: %v", err)
		}
		member := *b
		member.Role = models.RoleMember
		if err := keepsAnAdminIn(tx, a, &member, adminDelete); err != nil {
			t.Fatalf("a member target: %v", err)
		}
		// With the actor active in the database too, n = 2 and it passes.
		if err := tx.Exec("UPDATE users SET status = 'active' WHERE id = ?", a.ID).Error; err != nil {
			t.Fatal(err)
		}
		if n, err := adminCountIn(tx); err != nil || n != 2 {
			t.Fatalf("adminCountIn = %d, %v, want 2", n, err)
		}
		if err := keepsAnAdminIn(tx, a, b, adminDelete); err != nil {
			t.Fatalf("n = 2: %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// O-11: an erased admin keeps role admin, but a tombstone is disabled and
// soft-deleted, so neither count includes it and it cannot sign in.
func TestEraseAdminKeepsRole(t *testing.T) {
	s := newCoreErasureScene(t)
	s.erase(t, s.second.ID)
	var got models.User
	if err := s.app.DB.Unscoped().First(&got, s.second.ID).Error; err != nil {
		t.Fatal(err)
	}
	if got.Role != models.RoleAdmin {
		t.Errorf("role = %q, want admin kept", got.Role)
	}
	if got.CanSignIn() {
		t.Error("the tombstone can sign in")
	}
	if loginAPIExpectFail(t, s.app, "erase-second", erasurePW) {
		t.Error("the erased admin signed in")
	}
	n, err := adminCountIn(s.app.DB)
	if err != nil || n != 1 || s.app.adminCount() != 1 {
		t.Errorf("adminCountIn = %d, %v, adminCount = %d, want 1 and 1", n, err, s.app.adminCount())
	}
}

// R4-CTX: what the DELETE route says when the deletion's time runs out or its
// COMMIT goes unanswered. These tests pass unchanged at every later landing
// head from R4-3b-V on: only the commit-phase classification may answer
// delete_outcome_unknown, and nothing here may ever say "nothing was deleted"
// for a COMMIT that was sent.

func setAdminDeleteBounds(t *testing.T, budget, reserve time.Duration) {
	t.Helper()
	oldBudget, oldReserve := adminDeleteBudget, contactCommitReserve
	adminDeleteBudget, contactCommitReserve = budget, reserve
	t.Cleanup(func() { adminDeleteBudget, contactCommitReserve = oldBudget, oldReserve })
}

func (r *ctxRig) deletedUser(id uint) bool {
	r.t.Helper()
	var n int64
	if err := r.app.DB.Raw("SELECT count(*) FROM users WHERE id = ? AND deleted_at IS NOT NULL", id).Scan(&n).Error; err != nil {
		r.t.Fatal(err)
	}
	return n == 1
}

func TestAdminDeleteCommitPhaseAnswersUnknown(t *testing.T) {
	const claim = "idle_in_transaction_session_timeout"
	deferredFailure := func(t *testing.T, app *App, target *models.User) {
		t.Helper()
		for _, q := range []string{
			`CREATE FUNCTION fold_test_fail_at_commit() RETURNS trigger LANGUAGE plpgsql AS $$
				BEGIN RAISE EXCEPTION 'deferred refusal' USING ERRCODE = '23514'; END $$`,
			fmt.Sprintf(`CREATE CONSTRAINT TRIGGER fold_test_fail_at_commit AFTER UPDATE ON users DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
				WHEN (OLD.deleted_at IS NULL AND NEW.deleted_at IS NOT NULL AND OLD.id = %d) EXECUTE FUNCTION fold_test_fail_at_commit()`, target.ID),
		} {
			if err := app.DB.Exec(q).Error; err != nil {
				t.Fatal(err)
			}
		}
		t.Cleanup(func() {
			app.DB.Exec("DROP TRIGGER IF EXISTS fold_test_fail_at_commit ON users")
			app.DB.Exec("DROP FUNCTION IF EXISTS fold_test_fail_at_commit()")
		})
	}
	// The deletion must reach its COMMIT inside the budget, or it answers
	// delete_timed_out, a different outcome from the one under test. Cases b to d
	// end as soon as the COMMIT is answered, so their budget is long; only a
	// waits it out, and its budget is long against the deletion's own statements.
	const reachesCommit = 30 * time.Second
	cases := []struct {
		name    string
		budget  time.Duration
		fault   testdb.Faults
		setup   func(*testing.T, *App, *models.User)
		deleted bool
	}{
		{name: "a: the reply held past the budget", budget: 8 * time.Second, fault: testdb.Faults{Match: claim, Commit: testdb.CommitHoldReply}, deleted: true},
		{name: "b: forwarded, client closed before the reply", budget: reachesCommit, fault: testdb.Faults{Match: claim, Commit: testdb.CommitForwardThenClose}, deleted: true},
		{name: "c: closed without forwarding", budget: reachesCommit, fault: testdb.Faults{Match: claim, Commit: testdb.CommitCloseUnforwarded}, deleted: false},
		{name: "d: a deferred constraint fails at COMMIT", budget: reachesCommit, setup: deferredFailure, deleted: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setAdminDeleteBounds(t, tc.budget, 300*time.Millisecond)
			app := testApp(t)
			newTestUser(t, app, "admin", "pw", models.RoleAdmin)
			target := newTestUser(t, app, "target", "pw", models.RoleMember)
			r := newCtxRigFor(t, app)
			c := loginAPI(t, r.papp, "admin", "pw")
			if tc.setup != nil {
				tc.setup(t, app, target)
			}
			if tc.fault.Match != "" {
				r.proxy.Arm(tc.fault)
			}
			w := c.do("DELETE", "/api/v1/admin/users/"+itoa(target.ID), nil, true)
			if w.Code != http.StatusInternalServerError || !strings.Contains(w.Body.String(), `"code":"delete_outcome_unknown"`) {
				t.Fatalf("delete: %d %s, want 500 delete_outcome_unknown", w.Code, w.Body.String())
			}
			for _, wrong := range []string{"delete_timed_out", "server_error", "nothing"} {
				if strings.Contains(w.Body.String(), wrong) {
					t.Errorf("the body says %q after a COMMIT that was sent: %s", wrong, w.Body.String())
				}
			}
			if tc.deleted {
				deadline := time.Now().Add(5 * time.Second)
				for !r.deletedUser(target.ID) && time.Now().Before(deadline) {
					time.Sleep(10 * time.Millisecond)
				}
				if !r.deletedUser(target.ID) || contactCount(t, app, &models.AuditLog{}, "action = ? AND target_id = ?", audit.UserDeleted, target.ID) != 1 {
					t.Error("the tombstone and the user.deleted row should be visible: the COMMIT reached the server")
				}
				return
			}
			if tc.fault.Match != "" {
				r.awaitReleased(r.pidOf(claim), 10*time.Second)
			}
			if r.deletedUser(target.ID) || contactCount(t, app, &models.AuditLog{}, "action = ? AND target_id = ?", audit.UserDeleted, target.ID) != 0 {
				t.Error("the deletion was stored although its COMMIT never completed")
			}
		})
	}
}

func TestAdminDeleteGateWaitAnswersTimedOut(t *testing.T) {
	setAdminDeleteBounds(t, time.Second, 100*time.Millisecond)
	app := testApp(t)
	newTestUser(t, app, "admin", "pw", models.RoleAdmin)
	target := newTestUser(t, app, "target", "pw", models.RoleMember)
	c := loginAPI(t, app, "admin", "pw")
	release, _ := holdAdvisory(t, app.DB, contactLockID)
	defer release()
	before := erasureWaitSnapshot(t, app.DB)
	w := c.do("DELETE", "/api/v1/admin/users/"+itoa(target.ID), nil, true)
	if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), `"code":"delete_timed_out"`) {
		t.Fatalf("delete: %d %s, want 503 delete_timed_out", w.Code, w.Body.String())
	}
	if after := erasureWaitSnapshot(t, app.DB); after != before {
		t.Error("a timed-out deletion changed the database")
	}
}

// R4-3b-1: what the DELETE route does when a household row is busy. The
// erasure takes its households NOWAIT and runs the whole transaction again, at
// most three attempts inside busyRetryWindow, sharing the request budget.
// Every test goes through the proxy so the statements the server really saw
// (BEGIN, the NOWAIT lock, ROLLBACK) are the evidence. The both-order barriers
// with real household writers are in account_erasure_households_test.go.

// hlRig is the household scene served through the proxy: the admin's client
// talks to r.papp, the scene and every observer to the server directly.
func newHLRig(t *testing.T) (*hlScene, *ctxRig, *apiClient) {
	t.Helper()
	s := newHLScene(t, models.RoleMember)
	r := newCtxRigFor(t, s.app)
	// The route's handle, through the proxy, records the SQLSTATE the server
	// answered each failed statement with (a statement the proxy logged was only
	// forwarded, not necessarily executed).
	var name string
	if err := s.app.DB.Raw("SELECT current_database()").Scan(&name).Error; err != nil {
		t.Fatal(err)
	}
	r.papp.DB, s.states = tracedProxyDB(t, r.proxy, name)
	pc := loginAPI(t, r.papp, "erase-admin", erasurePW)
	pc.holdNotices = true
	// Signing in through the proxy ran transactions of its own (throttle row,
	// session): the shape below starts after them.
	s.proxyMark = len(r.proxy.Events())
	return s, r, pc
}

func hlDeletePath(s *hlScene) string { return "/api/v1/admin/users/" + itoa(s.victim.ID) }

// hlStatementCount is how many statements the server saw that contain text.
func hlStatementCount(r *ctxRig, text string) int {
	n := 0
	for _, e := range r.statements() {
		if strings.Contains(e.SQL, text) {
			n++
		}
	}
	return n
}

// hlTransactionShape is the order of BEGIN, ROLLBACK and COMMIT the server saw
// since the admin signed in (s.proxyMark), as letters: "BRBR" is two attempts
// that each began and rolled back.
func hlTransactionShape(s *hlScene, r *ctxRig) string {
	var out strings.Builder
	events := r.proxy.Events()
	if s.proxyMark <= len(events) {
		events = events[s.proxyMark:]
	}
	for _, e := range events {
		switch {
		case e.Kind == testdb.KindRollback:
			out.WriteString("R")
		case e.Kind == testdb.KindCommit:
			out.WriteString("C")
		case e.Kind == testdb.KindStatement && strings.HasPrefix(strings.ToLower(strings.TrimSpace(e.SQL)), "begin"):
			out.WriteString("B")
		}
	}
	return out.String()
}

func TestAdminDeleteHouseholdBusyExhaustsThreeAttempts(t *testing.T) {
	setHouseholdRetry(t, 2*time.Second, [2]time.Duration{25 * time.Millisecond, 100 * time.Millisecond})
	s, r, pc := newHLRig(t)
	before := hlSnapshot(t, s.app.DB)
	hold := holdHousehold(t, s.app.DB, s.hid)
	w := pc.do("DELETE", hlDeletePath(s), nil, true)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), `"code":"delete_household_busy"`) {
		t.Fatalf("delete: %d %s, want 409 delete_household_busy", w.Code, w.Body.String())
	}
	for _, leak := range []string{"55P03", "NOWAIT", "households", "delete_outcome_unknown", "delete_timed_out"} {
		if strings.Contains(w.Body.String(), leak) {
			t.Errorf("the body says %q: %s", leak, w.Body.String())
		}
	}
	if n := hlStatementCount(r, "FOR UPDATE NOWAIT"); n != 3 {
		t.Errorf("%d household lock attempts, want exactly 3", n)
	}
	if n := s.states.Count("55P03"); n != 3 {
		t.Errorf("the server answered %d lock requests with 55P03, want 3", n)
	}
	// Each retried BEGIN follows the previous ROLLBACK, and nothing committed.
	if got := hlTransactionShape(s, r); got != "BRBRBR" {
		t.Errorf("transactions %q, want BRBRBR: every attempt rolls back before the next begins", got)
	}
	if after := hlSnapshot(t, s.app.DB); after != before {
		t.Error("an exhausted deletion changed the database (a tombstone, audit row, notice or job)")
	}
	// Nothing was half done: once the writer lets go the same request works.
	if err := hold.Rollback().Error; err != nil {
		t.Fatal(err)
	}
	if w := pc.do("DELETE", hlDeletePath(s), nil, true); w.Code != http.StatusOK {
		t.Fatalf("delete after the writer finished: %d %s", w.Code, w.Body.String())
	}
}

// A rollback the server never acknowledged is not a released lock: the loop
// ends with the busy answer and sends no further BEGIN.
func TestAdminDeleteHouseholdBusyRollbackNotAcknowledged(t *testing.T) {
	setHouseholdRetry(t, 2*time.Second, [2]time.Duration{25 * time.Millisecond, 100 * time.Millisecond})
	s, r, pc := newHLRig(t)
	before := hlSnapshot(t, s.app.DB)
	hold := holdHousehold(t, s.app.DB, s.hid)
	r.proxy.Arm(testdb.Faults{Match: "FOR UPDATE NOWAIT", Rollback: testdb.RollbackCloseUnforwarded})
	w := pc.do("DELETE", hlDeletePath(s), nil, true)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), `"code":"delete_household_busy"`) {
		t.Fatalf("delete: %d %s, want 409 delete_household_busy", w.Code, w.Body.String())
	}
	if n := hlStatementCount(r, "FOR UPDATE NOWAIT"); n != 1 {
		t.Errorf("%d household lock attempts, want 1: nothing may begin after an unacknowledged rollback", n)
	}
	if n := s.states.Count("55P03"); n != 1 {
		t.Errorf("the server answered %d lock requests with 55P03, want 1", n)
	}
	if got := hlTransactionShape(s, r); got != "BR" {
		t.Errorf("transactions %q, want a single BEGIN and its ROLLBACK", got)
	}
	// The server let go of the attempt's locks when its connection closed.
	r.awaitReleased(r.pidOf("FOR UPDATE NOWAIT"), 10*time.Second)
	if err := hold.Rollback().Error; err != nil {
		t.Fatal(err)
	}
	if after := hlSnapshot(t, s.app.DB); after != before {
		t.Error("the deletion changed the database")
	}
}

// Attempt 1 is refused with a real 55P03 and its rollback has run on the server;
// attempt 2 commits but its acknowledgment is lost: the answer is
// delete_outcome_unknown, nothing runs a third time, and exactly one deletion is
// stored.
//
// The schedule is a barrier, not a delay. Attempt 1 is stopped before its lock
// request while the test queues for the contact gate behind it, and the
// household is held until the SQLSTATE 55P03 came back from the server and the
// test was granted the gate (which only the finished ROLLBACK frees). Only
// then is the commit fault armed for the next lock request, which is attempt
// 2's: it is after the gate the test still holds. The household is released
// and the gate handed back last.
func TestAdminDeleteHouseholdBusyThenCommitUnknown(t *testing.T) {
	setHouseholdRetry(t, 2*time.Second, [2]time.Duration{25 * time.Millisecond, 100 * time.Millisecond})
	s, r, pc := newHLRig(t)
	hold := holdHousehold(t, s.app.DB, s.hid)
	reached, release := erasureTestPause(t, r.papp.DB, erasureTestPoint{Processor: "row", Step: "gorm:row", SQLContains: "FOR UPDATE NOWAIT"})
	ch := bgDo(pc, "DELETE", hlDeletePath(s), nil)
	awaitDBSignal(t, reached)
	q := queueForGate(t, s.app)
	waitContactWaiters(t, s.app, 1)
	release()
	awaitStates(t, s.states, "55P03", 1)
	q.acquired(t)
	// Attempt 1 is over on the server. The next lock request, attempt 2's, loses
	// its COMMIT reply.
	r.proxy.Arm(testdb.Faults{Match: "FOR UPDATE NOWAIT", Commit: testdb.CommitForwardThenClose})
	if err := hold.Rollback().Error; err != nil {
		t.Fatal(err)
	}
	q.release()
	got := awaitResult(t, ch)
	if got.code != http.StatusInternalServerError || !strings.Contains(got.body, `"code":"delete_outcome_unknown"`) {
		t.Fatalf("delete: %d %s, want 500 delete_outcome_unknown", got.code, got.body)
	}
	for _, wrong := range []string{"delete_household_busy", "delete_timed_out", "nothing"} {
		if strings.Contains(got.body, wrong) {
			t.Errorf("the body says %q after a COMMIT that was sent: %s", wrong, got.body)
		}
	}
	deadline := time.Now().Add(5 * time.Second)
	for !r.deletedUser(s.victim.ID) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !r.deletedUser(s.victim.ID) {
		t.Fatal("attempt 2's COMMIT reached the server, so the deletion should be stored")
	}
	if n := hlStatementCount(r, "FOR UPDATE NOWAIT"); n != 2 {
		t.Errorf("%d household lock attempts, want 2: no third attempt after an unknown commit", n)
	}
	if n := s.states.Count("55P03"); n != 1 {
		t.Errorf("the server answered %d lock requests with 55P03, want only attempt 1's", n)
	}
	if got := hlTransactionShape(s, r); got != "BRBC" {
		t.Errorf("transactions %q, want BRBC: one rollback, then a begin that committed", got)
	}
	if n := contactCount(t, s.app, &models.AuditLog{}, "action = ? AND target_id = ?", audit.UserDeleted, s.victim.ID); n != 1 {
		t.Errorf("%d user.deleted rows, want exactly one committed deletion", n)
	}
}

// Attempts 1 and 2 are busy and attempt 3 starts inside the window but waits on
// a gate a test holds: the request ends at its budget with delete_timed_out,
// nothing is stored, and that attempt's server backend is released later,
// separately, within D + S + I.
func TestAdminDeleteHouseholdBusyBoundedAttempt(t *testing.T) {
	const budget = 3 * time.Second
	setAdminDeleteBounds(t, budget, 100*time.Millisecond)
	setHouseholdRetry(t, 2*time.Second, [2]time.Duration{50 * time.Millisecond, 700 * time.Millisecond})
	s, r, pc := newHLRig(t)
	before := hlSnapshot(t, s.app.DB)
	hold := holdHousehold(t, s.app.DB, s.hid)
	start := time.Now()
	ch := bgDo(pc, "DELETE", hlDeletePath(s), nil)
	awaitStates(t, s.states, "55P03", 2)
	// The server refused attempt 2, which is rolling back; the gate is free once
	// it has. Asking now queues behind attempt 2, ahead of attempt 3, which
	// begins 700 ms later.
	gate := holdContactGate(t, s.app)
	waitContactWaiters(t, s.app, 1)
	var attempt3 int
	if err := s.app.DB.Raw(`SELECT pid FROM pg_stat_activity WHERE datname = current_database()
		AND wait_event = 'advisory' AND query LIKE 'SELECT pg_advisory_xact_lock%'`).Scan(&attempt3).Error; err != nil || attempt3 == 0 {
		t.Fatalf("no backend waits on the gate: %d %v", attempt3, err)
	}
	got := awaitResult(t, ch)
	elapsed := time.Since(start)
	if got.code != http.StatusServiceUnavailable || !strings.Contains(got.body, `"code":"delete_timed_out"`) {
		t.Fatalf("delete: %d %s, want 503 delete_timed_out", got.code, got.body)
	}
	if elapsed < budget-500*time.Millisecond || elapsed > budget+5*time.Second {
		t.Errorf("the request ended after %v, want about its %v budget", elapsed, budget)
	}
	if n := hlStatementCount(r, "FOR UPDATE NOWAIT"); n != 2 {
		t.Errorf("%d household lock attempts, want 2: the third never got past the gate", n)
	}
	// The client gave up at D; the server lets go of attempt 3 by D + S + I.
	r.awaitReleased(attempt3, budget+contactServerCap+budget+2*time.Second)
	if err := gate.Rollback().Error; err != nil {
		t.Fatal(err)
	}
	if err := hold.Rollback().Error; err != nil {
		t.Fatal(err)
	}
	if after := hlSnapshot(t, s.app.DB); after != before {
		t.Error("a timed-out deletion changed the database")
	}
}

// A body that is not JSON is refused, not read as an empty one; an empty body
// still decides (every field is optional).
func TestAdminDecisionBodiesAreNotSilentlyEmpty(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
	admin := loginAPI(t, app, "admin", "password1234")
	w := admin.do("POST", "/api/v1/admin/groups/999/approve", rawJSON("{not json"), true)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("malformed body: %d %s, want 400", w.Code, w.Body.String())
	}
	w = admin.do("POST", "/api/v1/admin/groups/999/approve", nil, true)
	if w.Code != http.StatusNotFound {
		t.Fatalf("empty body: %d %s, want 404 (no such group)", w.Code, w.Body.String())
	}
}
