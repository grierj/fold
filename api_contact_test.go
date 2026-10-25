package main

import (
	"encoding/json"
	"errors"
	"fold/internal/audit"
	"fold/internal/notify"
	"gorm.io/gorm"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"fold/internal/policy"
	"fold/models"
)

func assertContactKeys(t *testing.T, row map[string]any, subject *models.User, visible bool) {
	t.Helper()
	for key, want := range map[string]string{"email": subject.Email, "phone": subject.Phone} {
		got, present := row[key]
		if present != visible || (visible && got != want) {
			t.Fatalf("%s: got %v (present=%v), visible=%v, want %q; row=%v", key, got, present, visible, want, row)
		}
	}
}

func TestContactDetailsJSONKeys(t *testing.T) {
	adult := &models.User{ID: 1, AccountType: models.AccountAdult, Email: "adult@example.com", Phone: "123"}
	hidden := *adult
	hidden.HideContactFromLeaders = true
	teen := *adult
	teen.AccountType = models.AccountTeen
	viewer := &models.User{ID: 2, AccountType: models.AccountAdult}
	admin := &models.User{ID: 3, Role: models.RoleAdmin, Status: models.UserActive}
	for _, tc := range []struct {
		name            string
		viewer, subject *models.User
		context         policy.Contact
		visible         bool
	}{
		{"signed out", nil, adult, policy.Contact{GroupLeader: true, GroupMember: true}, false},
		{"self", &hidden, &hidden, policy.Contact{}, true},
		{"teen self", &teen, &teen, policy.Contact{}, true},
		{"admin", admin, &teen, policy.Contact{}, true},
		{"own group", viewer, adult, policy.Contact{GroupLeader: true, GroupMember: true}, true},
		{"hidden", viewer, &hidden, policy.Contact{GroupLeader: true, GroupMember: true}, false},
		{"teen", viewer, &teen, policy.Contact{GroupLeader: true, GroupMember: true}, false},
		{"confirmed guardian", viewer, &teen, policy.Contact{ConfirmedGuardian: true}, true},
		{"pending guardian", viewer, &teen, policy.Contact{}, false},
		{"unrelated leader", viewer, adult, policy.Contact{GroupLeader: true}, false},
		{"own host", viewer, adult, policy.Contact{EventManager: true, EventRegistrant: true}, true},
		{"unrelated host", viewer, adult, policy.Contact{EventManager: true}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			email, phone := contactDetails(tc.viewer, tc.subject, tc.context)
			body, err := json.Marshal(memberJSON{Email: email, Phone: phone})
			if err != nil {
				t.Fatal(err)
			}
			var row map[string]any
			if err := json.Unmarshal(body, &row); err != nil {
				t.Fatal(err)
			}
			assertContactKeys(t, row, tc.subject, tc.visible)
		})
	}
}

func TestContactPrivacyRosterAndAttendees(t *testing.T) {
	app := testApp(t)
	leader := newTestUser(t, app, "leader", "password1234", models.RoleMember)
	admin := newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
	adult := newTestUser(t, app, "adult", "password1234", models.RoleMember)
	hidden := newTestUser(t, app, "hidden", "password1234", models.RoleMember)
	teen := newTestUser(t, app, "teen", "password1234", models.RoleMember)
	outsider := newTestUser(t, app, "outsider", "password1234", models.RoleMember)
	for _, u := range []*models.User{adult, hidden, teen} {
		u.Phone = "555-" + itoa(u.ID)
		u.AvatarKey = "test-avatar.png"
		if u == hidden {
			u.HideContactFromLeaders = true
		}
		if u == teen {
			u.AccountType = models.AccountTeen
			birth := time.Now().UTC().AddDate(-15, 0, 0)
			u.BirthDate = &birth
		}
		if err := app.DB.Save(u).Error; err != nil {
			t.Fatal(err)
		}
	}
	g := newTestGroup(t, app, leader, "Group", models.GroupActive)
	// Even leadership of another group shared with this adult does not confer
	// contact rights in the group/event currently being presented.
	otherGroup := newTestGroup(t, app, outsider, "Other group", models.GroupActive)
	if err := app.DB.Create(&models.Membership{UserID: adult.ID, GroupID: otherGroup.ID, Status: "active", Role: models.MembershipMember}).Error; err != nil {
		t.Fatal(err)
	}
	for _, u := range []*models.User{adult, hidden, teen, outsider} {
		if err := app.DB.Create(&models.Membership{UserID: u.ID, GroupID: g.ID, Status: "active", Role: models.MembershipMember}).Error; err != nil {
			t.Fatal(err)
		}
	}
	ev, occ := seedEvent(t, app, g, leader.ID, time.Now().UTC().Add(24*time.Hour))
	for _, u := range []*models.User{adult, hidden, teen} {
		if err := app.DB.Create(&models.Registration{UserID: &u.ID, OccurrenceID: occ.ID, Status: models.RegGoing}).Error; err != nil {
			t.Fatal(err)
		}
	}
	lc := loginAPI(t, app, leader.Email, "password1234")
	ac := loginAPI(t, app, admin.Email, "password1234")
	oc := loginAPI(t, app, outsider.Email, "password1234")
	sc := newAPIClient(t, app)
	for _, tc := range []struct {
		name            string
		client          *apiClient
		manage, isAdmin bool
	}{
		{"own leader", lc, true, false}, {"admin", ac, true, true}, {"unrelated leader", oc, false, false}, {"signed out", sc, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := tc.client.do("GET", "/api/v1/groups/"+itoa(g.ID), nil, false)
			if w.Code != http.StatusOK {
				t.Fatalf("group: %d %s", w.Code, w.Body.String())
			}
			var body struct {
				Members []map[string]any `json:"members"`
			}
			decodeJSON(t, w, &body)
			if tc.client == sc && len(body.Members) != 0 {
				t.Fatal("signed-out viewer received a roster")
			}
			if tc.client != sc && len(body.Members) < 3 {
				t.Fatalf("expected adult roster identities: %v", body.Members)
			}
			if tc.manage && len(body.Members) != 5 {
				t.Fatalf("manager must see every roster identity: %v", body.Members)
			}
			for _, row := range body.Members {
				for _, u := range []*models.User{adult, hidden, teen} {
					if row["name"] != u.PublicName() {
						continue
					}
					assertContactKeys(t, row, u, tc.isAdmin || (tc.manage && u == adult))
					if u == teen && row["avatarUrl"] != "" {
						t.Fatalf("teen avatar leaked: %v", row)
					}
				}
			}
			w = tc.client.do("GET", "/api/v1/events/"+itoa(ev.ID), nil, false)
			if w.Code != http.StatusOK {
				t.Fatalf("event: %d %s", w.Code, w.Body.String())
			}
			var page struct {
				Occurrences []struct {
					Attendees []map[string]any `json:"attendees"`
				} `json:"occurrences"`
			}
			decodeJSON(t, w, &page)
			if len(page.Occurrences) != 1 {
				t.Fatalf("occurrences: %+v", page)
			}
			wantAttendees := 2
			if tc.manage {
				wantAttendees = 3
			}
			if tc.client == sc {
				wantAttendees = 0
			}
			if len(page.Occurrences[0].Attendees) != wantAttendees {
				t.Fatalf("attendees: got %v, want %d identities", page.Occurrences[0].Attendees, wantAttendees)
			}
			for _, row := range page.Occurrences[0].Attendees {
				for _, u := range []*models.User{adult, hidden, teen} {
					if row["name"] == u.PublicName() {
						assertContactKeys(t, row, u, tc.isAdmin || (tc.manage && u == adult))
					}
				}
			}
			if !tc.isAdmin {
				for _, u := range []*models.User{hidden, teen} {
					if strings.Contains(w.Body.String(), u.Email) || strings.Contains(w.Body.String(), u.Phone) {
						t.Fatalf("hidden contact leaked: %s", w.Body.String())
					}
				}
			}
		})
	}
	// The same leader gets guardian rights only from a confirmed link.
	link := models.GuardianLink{GuardianUserID: leader.ID, TeenUserID: &teen.ID, Status: models.GuardianPending}
	if err := app.DB.Create(&link).Error; err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{models.GuardianPending, models.GuardianConfirmed, models.GuardianRevoked, models.GuardianRejected} {
		if err := app.DB.Model(&link).Update("status", status).Error; err != nil {
			t.Fatal(err)
		}
		for _, path := range []string{"/api/v1/groups/" + itoa(g.ID), "/api/v1/events/" + itoa(ev.ID)} {
			w := lc.do("GET", path, nil, false)
			visible := status == models.GuardianConfirmed
			if w.Code != 200 || strings.Contains(w.Body.String(), teen.Email) != visible || strings.Contains(w.Body.String(), teen.Phone) != visible {
				t.Fatalf("guardian status=%s %s: %d %s", status, path, w.Code, w.Body.String())
			}
		}
	}
}

func TestContactPrivacyChurchEventHost(t *testing.T) {
	app := testApp(t)
	host := newTestUser(t, app, "host", "password1234", models.RoleMember)
	otherHost := newTestUser(t, app, "otherhost", "password1234", models.RoleMember)
	adult := newTestUser(t, app, "adult", "password1234", models.RoleMember)
	hidden := newTestUser(t, app, "hidden", "password1234", models.RoleMember)
	if err := app.DB.Model(hidden).Updates(map[string]any{"hide_contact_from_leaders": true, "phone": "hidden-phone"}).Error; err != nil {
		t.Fatal(err)
	}
	g := newTestGroup(t, app, host, "Seed group", models.GroupActive)
	ev, occ := seedEvent(t, app, g, host.ID, time.Now().UTC().Add(24*time.Hour))
	other, _ := seedEvent(t, app, g, otherHost.ID, time.Now().UTC().Add(24*time.Hour))
	if err := app.DB.Model(&models.Event{}).Where("id IN ?", []uint{ev.ID, other.ID}).Update("group_id", nil).Error; err != nil {
		t.Fatal(err)
	}
	for _, pair := range []struct{ event, user uint }{{ev.ID, host.ID}, {other.ID, otherHost.ID}} {
		if err := app.DB.Create(&models.EventHost{EventID: pair.event, UserID: pair.user, AddedBy: pair.user}).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, u := range []*models.User{adult, hidden} {
		if err := app.DB.Create(&models.Registration{UserID: &u.ID, OccurrenceID: occ.ID, Status: models.RegGoing}).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		user    *models.User
		visible bool
	}{{host, true}, {otherHost, false}} {
		client := loginAPI(t, app, tc.user.Email, "password1234")
		w := client.do("GET", "/api/v1/events/"+itoa(ev.ID), nil, false)
		if w.Code != 200 {
			t.Fatalf("event: %d %s", w.Code, w.Body.String())
		}
		var page struct {
			Event       map[string]any `json:"event"`
			Occurrences []struct {
				Attendees []map[string]any `json:"attendees"`
			} `json:"occurrences"`
		}
		decodeJSON(t, w, &page)
		if _, ok := page.Event["hosts"]; ok {
			t.Fatal("non-admin received host-management output")
		}
		if len(page.Occurrences) != 1 {
			t.Fatalf("occurrences: %+v", page)
		}
		for _, row := range page.Occurrences[0].Attendees {
			if row["name"] == adult.PublicName() {
				assertContactKeys(t, row, adult, tc.visible)
			}
			if row["name"] == hidden.PublicName() {
				assertContactKeys(t, row, hidden, false)
			}
		}
	}
	// Being a host is not itself a registration/contact relationship.
	rows := []attendeeJSON{{UserID: &adult.ID, Status: models.RegRemoved}}
	presentAttendeeContacts(rows, host, map[uint]*models.User{adult.ID: adult}, nil, true)
	if rows[0].Email != nil || rows[0].Phone != nil {
		t.Fatal("removed registration authorized contact")
	}
}

func TestContactPresentationDoesNotUseUnrestrictedUserJSON(t *testing.T) {
	for _, path := range []string{"api_groups.go", "api_events.go", "api_event_access.go", "api_contact.go", "registration_subjects.go"} {
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(src), ".toUserJSON(") {
			t.Errorf("%s uses unrestricted user presentation", path)
		}
	}
}

type contactAPIFixture struct {
	app                                        *App
	leader, subject, admin, guardian, outsider *models.User
	group                                      *models.Group
	lc, sc, ac, gc, oc                         *apiClient
}

func newContactAPIFixture(t *testing.T, teen bool) *contactAPIFixture {
	t.Helper()
	a := testApp(t)
	f := &contactAPIFixture{app: a}
	f.leader = newTestUser(t, a, "contact-leader", "password1234", models.RoleMember)
	f.subject = newTestUser(t, a, "contact-subject", "password1234", models.RoleMember)
	f.admin = newTestUser(t, a, "contact-admin", "password1234", models.RoleAdmin)
	f.guardian = newTestUser(t, a, "contact-guardian", "password1234", models.RoleMember)
	f.outsider = newTestUser(t, a, "contact-outsider", "password1234", models.RoleMember)
	f.subject.HideContactFromLeaders = true
	f.subject.Phone = "private-phone"
	if teen {
		f.subject.AccountType = models.AccountTeen
		birth := time.Now().UTC().AddDate(-16, 0, 0)
		f.subject.BirthDate = &birth
	}
	if err := a.DB.Save(f.subject).Error; err != nil {
		t.Fatal(err)
	}
	f.group = newTestGroup(t, a, f.leader, "Contact group", models.GroupActive)
	if err := a.DB.Create(&models.Membership{UserID: f.subject.ID, GroupID: f.group.ID, Status: "active", Role: models.MembershipMember}).Error; err != nil {
		t.Fatal(err)
	}
	if teen {
		if err := a.DB.Create(&models.GuardianLink{GuardianUserID: f.guardian.ID, TeenUserID: &f.subject.ID, Status: models.GuardianConfirmed}).Error; err != nil {
			t.Fatal(err)
		}
	}
	f.lc = loginAPI(t, a, f.leader.Email, "password1234")
	f.sc = loginAPI(t, a, f.subject.Email, "password1234")
	f.ac = loginAPI(t, a, f.admin.Email, "password1234")
	f.gc = loginAPI(t, a, f.guardian.Email, "password1234")
	f.oc = loginAPI(t, a, f.outsider.Email, "password1234")
	return f
}
func (f *contactAPIFixture) create(t *testing.T, fields, reason string) contactRequestJSON {
	t.Helper()
	w := f.lc.do("POST", "/api/v1/contact-requests", map[string]any{"subjectUserId": f.subject.ID, "contextType": "group", "contextId": f.group.ID, "fields": fields, "reason": reason}, true)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	var b struct {
		Request contactRequestJSON `json:"request"`
	}
	decodeJSON(t, w, &b)
	return b.Request
}
func contactDecision(c *apiClient, id uint, party, decision string) *httptest.ResponseRecorder {
	return c.do("POST", "/api/v1/contact-requests/"+itoa(id)+"/decision", map[string]string{"party": party, "decision": decision}, true)
}
func (f *contactAPIFixture) grant(t *testing.T, r contactRequestJSON) models.ContactGrant {
	t.Helper()
	if r.NeedsAdmin {
		w := contactDecision(f.ac, r.ID, "admin", "approved")
		if w.Code != 200 {
			t.Fatalf("admin: %d %s", w.Code, w.Body.String())
		}
	}
	w := contactDecision(f.sc, r.ID, "subject", "approved")
	if w.Code != 200 {
		t.Fatalf("subject: %d %s", w.Code, w.Body.String())
	}
	var g models.ContactGrant
	if err := f.app.DB.Where("request_id = ?", r.ID).First(&g).Error; err != nil {
		t.Fatal(err)
	}
	return g
}
func (f *contactAPIFixture) reveal(c *apiClient, g models.ContactGrant, fields string) *httptest.ResponseRecorder {
	return c.do("POST", "/api/v1/contact-grants/"+itoa(g.ID)+"/reveal", map[string]any{"subjectUserId": g.SubjectUserID, "contextType": g.ContextType, "contextId": g.ContextID, "fields": fields}, true)
}
func contactCount(t *testing.T, a *App, model any, where string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := a.DB.Model(model).Where(where, args...).Count(&n).Error; err != nil {
		t.Fatal(err)
	}
	return n
}
func TestContactRequestAdultExplicitReveal(t *testing.T) {
	f := newContactAPIFixture(t, false)
	w := f.lc.do("POST", "/api/v1/contact-requests", map[string]any{}, false)
	if w.Code != 403 {
		t.Fatalf("CSRF %d", w.Code)
	}
	r := f.create(t, "email", strings.Repeat("界", 1000))
	if r.NeedsAdmin || r.AdminDecision != "not_needed" || r.ExpiresAt.Sub(r.CreatedAt) != policy.ContactRequestLifetime {
		t.Fatalf("request %#v", r)
	}
	for _, c := range []*apiClient{f.gc, f.oc} {
		w = c.do("GET", "/api/v1/contact-requests/"+itoa(r.ID), nil, false)
		if w.Code != 404 {
			t.Fatalf("unauthorized review: %d %s", w.Code, w.Body.String())
		}
	}
	w = contactDecision(f.ac, r.ID, "admin", "approved")
	if w.Code != 403 {
		t.Fatalf("adult admin must not decide: %d", w.Code)
	}
	// GET review has not approved, shared contact keys or exposed the reason to audit.
	w = f.sc.do("GET", "/api/v1/contact-requests/"+itoa(r.ID), nil, false)
	if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("review %d", w.Code)
	}
	var raw map[string]any
	decodeJSON(t, w, &raw)
	for _, key := range []string{"email", "phone"} {
		if _, ok := raw["request"].(map[string]any)[key]; ok {
			t.Fatalf("review has %s", key)
		}
	}
	g := f.grant(t, r)
	w = f.reveal(f.lc, g, "email")
	if w.Code != 200 {
		t.Fatalf("reveal %d %s", w.Code, w.Body.String())
	}
	var reveal map[string]any
	decodeJSON(t, w, &reveal)
	if reveal["email"] != f.subject.Email || len(reveal) != 1 {
		t.Fatalf("field scope %v", reveal)
	}
	if contactCount(t, f.app, &models.AuditLog{}, "action = ?", audit.ContactReveal) != 1 {
		t.Fatal("missing reveal audit")
	}
	for _, c := range []*apiClient{f.sc, f.ac, f.oc} {
		if w = f.reveal(c, g, "email"); w.Code != 403 {
			t.Fatalf("nonrequester reveal %d", w.Code)
		}
	}
	if w = f.reveal(f.lc, g, "both"); w.Code != 403 {
		t.Fatalf("widen reveal %d", w.Code)
	}
	w = f.sc.do("POST", "/api/v1/contact-grants/"+itoa(g.ID)+"/revoke", nil, true)
	if w.Code != 200 {
		t.Fatalf("revoke %d %s", w.Code, w.Body.String())
	}
	if w = f.reveal(f.lc, g, "email"); w.Code != 403 {
		t.Fatalf("revoked reveal %d", w.Code)
	}
	var saved models.ContactRequest
	f.app.DB.First(&saved, r.ID)
	if saved.Status != "granted" {
		t.Fatal("revoke rewrote history")
	}
	if contactCount(t, f.app, &models.AuditLog{}, "meta_json::text LIKE ?", "%"+strings.Repeat("界", 20)+"%") != 0 {
		t.Fatal("reason in audit")
	}
}
func TestContactRequestTeenEitherOrderAndGuardian(t *testing.T) {
	for _, first := range []string{"subject", "admin"} {
		t.Run(first, func(t *testing.T) {
			f := newContactAPIFixture(t, true)
			r := f.create(t, "both", "")
			if w := contactDecision(f.gc, r.ID, "subject", "approved"); w.Code != 403 {
				t.Fatalf("guardian approved %d", w.Code)
			}
			firstClient, secondClient, second := f.sc, f.ac, "admin"
			if first == "admin" {
				firstClient, secondClient, second = f.ac, f.sc, "subject"
			}
			w := contactDecision(firstClient, r.ID, first, "approved")
			if w.Code != 200 {
				t.Fatalf("first %d %s", w.Code, w.Body.String())
			}
			if contactCount(t, f.app, &models.ContactGrant{}, "request_id = ?", r.ID) != 0 {
				t.Fatal("one party granted")
			}
			firstKind, secondKind := notify.ContactSubjectReview, notify.ContactAdminReview
			if first == "admin" {
				firstKind, secondKind = secondKind, firstKind
			}
			if contactCount(t, f.app, &models.Notification{}, "subject_id = ? AND kind = ? AND resolved_at IS NULL", r.ID, firstKind) != 0 {
				t.Fatal("party task not resolved")
			}
			if contactCount(t, f.app, &models.Notification{}, "subject_id = ? AND kind = ? AND resolved_at IS NULL", r.ID, secondKind) == 0 {
				t.Fatal("other party task resolved early")
			}
			w = contactDecision(secondClient, r.ID, second, "approved")
			if w.Code != 200 {
				t.Fatalf("second %d %s", w.Code, w.Body.String())
			}
			var g models.ContactGrant
			f.app.DB.Where("request_id = ?", r.ID).First(&g)
			w = f.gc.do("GET", "/api/v1/contact-grants", nil, false)
			if w.Code != 200 || !strings.Contains(w.Body.String(), "\"canRevoke\":true") {
				t.Fatalf("guardian grants %d %s", w.Code, w.Body.String())
			}
			w = f.gc.do("GET", "/api/v1/contact-requests/"+itoa(r.ID), nil, false)
			if w.Code != 404 {
				t.Fatalf("guardian request history %d", w.Code)
			}
			if contactCount(t, f.app, &models.Notification{}, "user_id = ? AND kind = ? AND subject_id = ?", f.guardian.ID, notify.ContactUpdated, r.ID) != 1 {
				t.Fatal("guardian missing grant notice")
			}
			w = f.gc.do("POST", "/api/v1/contact-grants/"+itoa(g.ID)+"/revoke", nil, true)
			if w.Code != 200 {
				t.Fatalf("guardian revoke %d %s", w.Code, w.Body.String())
			}
			f.app.DB.First(&g, g.ID)
			if g.EndReason != policy.ContactRevokedByGuardian || g.EndedBy == nil || *g.EndedBy != f.guardian.ID {
				t.Fatalf("guardian facts %#v", g)
			}
			w = f.gc.do("GET", "/api/v1/contact-grants", nil, false)
			if strings.Contains(w.Body.String(), "\"requestId\"") {
				t.Fatal("guardian sees ended history")
			}
		})
	}
}
func TestContactRequestLimitsAndRefusalAudit(t *testing.T) {
	f := newContactAPIFixture(t, false)
	w := f.lc.do("POST", "/api/v1/contact-requests", map[string]any{"reason": strings.Repeat("界", 1001), "fields": "secret@example.com"}, true)
	if w.Code != 400 || !strings.Contains(w.Body.String(), "contact_reason_long") {
		t.Fatalf("reason %d %s", w.Code, w.Body.String())
	}
	if contactCount(t, f.app, &models.AuditLog{}, "action = ?", audit.ContactRefused) != 1 {
		t.Fatal("refusal not durable")
	}
	var rows []models.AuditLog
	f.app.DB.Where("action = ?", audit.ContactRefused).Find(&rows)
	if strings.Contains(rows[0].MetaJSON, "secret@example.com") {
		t.Fatal("untrusted fields in audit")
	}
	for i := 0; i < 10; i++ {
		f.create(t, "phone", "")
	}
	w = f.lc.do("POST", "/api/v1/contact-requests", map[string]any{"subjectUserId": f.subject.ID, "contextType": "group", "contextId": f.group.ID, "fields": "phone"}, true)
	if w.Code != 409 || !strings.Contains(w.Body.String(), "contact_open_limit") {
		t.Fatalf("cap %d %s", w.Code, w.Body.String())
	}
}
func TestContactRequestAdminRequesterStillNeedsTeen(t *testing.T) {
	f := newContactAPIFixture(t, true)
	if err := f.app.DB.Create(&models.Membership{UserID: f.admin.ID, GroupID: f.group.ID, Role: models.MembershipLeader, Status: "active"}).Error; err != nil {
		t.Fatal(err)
	}
	f.lc = f.ac
	r := f.create(t, "email", "")
	if w := contactDecision(f.ac, r.ID, "admin", "approved"); w.Code != 200 {
		t.Fatalf("admin %d %s", w.Code, w.Body.String())
	}
	if contactCount(t, f.app, &models.ContactGrant{}, "request_id = ?", r.ID) != 0 {
		t.Fatal("admin requester bypassed teen")
	}
	if w := contactDecision(f.sc, r.ID, "subject", "denied"); w.Code != 200 {
		t.Fatalf("denied %d", w.Code)
	}
	if w := contactDecision(f.sc, r.ID, "subject", "approved"); w.Code != 403 {
		t.Fatalf("denial resurrected %d", w.Code)
	}
}

func TestContactMiddlewareRefusalsAuditedOnce(t *testing.T) {
	f := newContactAPIFixture(t, false)
	anonymous := newAPIClient(t, f.app)
	anonymous.fetchCSRF()
	paths := []string{"/api/v1/contact-requests", "/api/v1/contact-requests/not-a-number/decision", "/api/v1/contact-requests/123/withdraw", "/api/v1/contact-grants/123/revoke", "/api/v1/contact-grants/not-a-number/reveal"}
	for _, path := range paths {
		before := contactCount(t, f.app, &models.AuditLog{}, "action = ?", audit.ContactRefused)
		w := anonymous.do("POST", path+"?token=must-not-log-query", map[string]any{"email": "must-not-log@example.com", "reason": "must-not-log-body", "requesterId": f.admin.ID}, true)
		if w.Code != 401 || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("signed out %s: %d %s", path, w.Code, w.Body.String())
		}
		if contactCount(t, f.app, &models.AuditLog{}, "action = ?", audit.ContactRefused) != before+1 {
			t.Fatal("missing or duplicate auth refusal")
		}
		w = f.lc.do("POST", path, map[string]any{"phone": "must-not-log-phone"}, false)
		if w.Code != 403 || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("CSRF %s: %d %s", path, w.Code, w.Body.String())
		}
		if contactCount(t, f.app, &models.AuditLog{}, "action = ?", audit.ContactRefused) != before+2 {
			t.Fatal("missing or duplicate CSRF refusal")
		}
	}
	var rows []models.AuditLog
	f.app.DB.Where("action = ?", audit.ContactRefused).Order("id").Find(&rows)
	for i, row := range rows {
		if strings.Contains(row.MetaJSON, "must-not-log") || strings.Contains(row.MetaJSON, "not-a-number") || row.TargetID != 0 || row.TargetType != "contact_endpoint" {
			t.Fatalf("unsafe refusal metadata %#v", row)
		}
		if i%2 == 0 && row.ActorID != nil {
			t.Fatal("anonymous supplied actor trusted")
		}
		if i%2 == 1 && (row.ActorID == nil || *row.ActorID != f.leader.ID) {
			t.Fatal("trusted session actor missing")
		}
	}
	before := len(rows)
	anonymous.do("POST", "/api/v1/me/password", map[string]string{}, false)
	anonymous.do("GET", "/api/v1/contact-grants", nil, false)
	if contactCount(t, f.app, &models.AuditLog{}, "action = ?", audit.ContactRefused) != int64(before) {
		t.Fatal("unrelated route or list omission audited")
	}
	if contactCount(t, f.app, &models.ContactRequest{}, "1=1") != 0 || contactCount(t, f.app, &models.ContactGrant{}, "1=1") != 0 {
		t.Fatal("rejected middleware ran handler")
	}
}
func TestContactMiddlewareAuditFailureFailsClosed(t *testing.T) {
	f := newContactAPIFixture(t, false)
	anonymous := newAPIClient(t, f.app)
	anonymous.fetchCSRF()
	name := "contact:middleware_audit_failure"
	if err := f.app.DB.Callback().Create().Before("gorm:create").Register(name, func(db *gorm.DB) {
		if db.Statement.Table == "audit_logs" {
			db.AddError(errors.New("injected audit failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer f.app.DB.Callback().Create().Remove(name)
	for _, tc := range []struct {
		client *apiClient
		csrf   bool
	}{{anonymous, true}, {f.lc, false}} {
		w := tc.client.do("POST", "/api/v1/contact-grants/999/reveal", map[string]string{"email": "private@example.com"}, tc.csrf)
		if w.Code != 500 || strings.Contains(w.Body.String(), "private@example.com") {
			t.Fatalf("audit failure %d %s", w.Code, w.Body.String())
		}
	}
	if contactCount(t, f.app, &models.ContactRequest{}, "1=1") != 0 {
		t.Fatal("audit failure ran handler")
	}
}
