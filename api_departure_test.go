package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"fold/internal/audit"
	"fold/internal/mail"
	"fold/internal/notify"
	"fold/internal/policy"
	"fold/migrations"
	"fold/models"

	"gorm.io/gorm"
)

type blockingDepartureMailSender struct {
	delegate mail.Sender
	target   string
	reached  chan struct{}
	release  chan struct{}
	once     sync.Once
}

func (s *blockingDepartureMailSender) Send(ctx context.Context, message mail.Message) error {
	if message.To == s.target {
		s.once.Do(func() { close(s.reached) })
		<-s.release
	}
	return s.delegate.Send(ctx, message)
}

type departureTestFixture struct {
	app      *App
	leader   *models.User
	subject  *models.User
	group    *models.Group
	leaderC  *apiClient
	subjectC *apiClient
}

func newDepartureTestFixture(t *testing.T) departureTestFixture {
	t.Helper()
	app := testApp(t)
	app.Config.SessionSecret = []byte("departure-preview-test-secret-32-bytes")
	leader := newTestUser(t, app, "departure leader", "password1234", models.RoleMember)
	subject := newTestUser(t, app, "departure subject", "password1234", models.RoleMember)
	group := newTestGroup(t, app, leader, "Hidden departure group", models.GroupActive)
	addMember(t, app, subject, group, models.MembershipMember)
	return departureTestFixture{
		app: app, leader: leader, subject: subject, group: group,
		leaderC:  loginAPI(t, app, leader.Email, "password1234"),
		subjectC: loginAPI(t, app, subject.Email, "password1234"),
	}
}

func addDepartureRegistration(t *testing.T, f departureTestFixture, subject *models.User, title, visibility string, override *string, start, end time.Time, occurrenceStatus, registrationStatus string) (models.EventOccurrence, models.Registration) {
	t.Helper()
	ev := models.Event{
		GroupID: &f.group.ID, Title: title, Description: "", Visibility: visibility,
		StartsLocal: start.UTC().Format("2006-01-02T15:04"), DurationMinutes: int(end.Sub(start) / time.Minute),
		TZID: "UTC", RRule: "", Location: "", CreatedBy: f.leader.ID,
	}
	if err := f.app.DB.Create(&ev).Error; err != nil {
		t.Fatal(err)
	}
	occ := models.EventOccurrence{
		EventID: ev.ID, OriginalDate: start.UTC().Format("2006-01-02"), StartsAt: start.UTC(), EndsAt: end.UTC(),
		Status: occurrenceStatus, VisibilityOverride: override,
	}
	if err := f.app.DB.Create(&occ).Error; err != nil {
		t.Fatal(err)
	}
	uid := subject.ID
	reg := models.Registration{OccurrenceID: occ.ID, UserID: &uid, Status: registrationStatus, AttendanceMode: models.AttendanceInPerson, Source: models.RegSourceWeb}
	if err := f.app.DB.Create(&reg).Error; err != nil {
		t.Fatal(err)
	}
	return occ, reg
}

func addDepartureProxyRegistration(t *testing.T, f departureTestFixture, occurrence models.EventOccurrence) {
	t.Helper()
	household := models.Household{Name: "Proxy household", CreatedBy: f.subject.ID}
	if err := f.app.DB.Create(&household).Error; err != nil {
		t.Fatal(err)
	}
	person := models.HouseholdPerson{HouseholdID: household.ID, FirstName: "Proxy", LastName: "Person", Kind: models.HouseholdAdult, CreatedBy: f.subject.ID}
	if err := f.app.DB.Create(&person).Error; err != nil {
		t.Fatal(err)
	}
	holder := f.subject.ID
	reg := models.Registration{OccurrenceID: occurrence.ID, HouseholdPersonID: &person.ID, RegisteredBy: &holder, Status: models.RegGoing, AttendanceMode: models.AttendanceInPerson, Source: models.RegSourceWeb}
	if err := f.app.DB.Create(&reg).Error; err != nil {
		t.Fatal(err)
	}
}

func getDeparturePreview(t *testing.T, c *apiClient, path string, want int) departurePreviewJSON {
	t.Helper()
	w := c.do("GET", path, nil, false)
	if w.Code != want {
		t.Fatalf("preview %s: %d %s", path, w.Code, w.Body.String())
	}
	if want != http.StatusOK {
		return departurePreviewJSON{}
	}
	var body struct {
		Departure departurePreviewJSON `json:"departure"`
	}
	decodeJSON(t, w, &body)
	return body.Departure
}

func confirmDeparturePreview(t *testing.T, c *apiClient, method, mutationPath string, preview departurePreviewJSON, keep []uint, note string) *httptest.ResponseRecorder {
	t.Helper()
	body := map[string]any{"snapshot": preview.Snapshot, "keepOccurrenceIds": keep}
	if note != "" {
		body["note"] = note
	}
	return c.do(method, mutationPath, body, true)
}

func readdDepartureTeen(t *testing.T, f departureTestFixture, adminC *apiClient) {
	t.Helper()
	token, err := f.app.newPeopleSelection(f.leader.ID, f.subject.ID, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	w := f.leaderC.do("POST", "/api/v1/add-requests", map[string]any{
		"selectionId": token, "contextType": policy.ContactGroup, "contextId": f.group.ID, "reason": "Please rejoin",
	}, true)
	if w.Code != http.StatusCreated {
		t.Fatalf("create teen re-add request: %d %s", w.Code, w.Body.String())
	}
	var body struct {
		Request addRequestJSON `json:"request"`
	}
	decodeJSON(t, w, &body)
	if w := f.subjectC.do("POST", "/api/v1/add-requests/"+itoa(body.Request.ID)+"/decision", map[string]string{"party": "subject", "decision": "approved"}, true); w.Code != http.StatusOK {
		t.Fatalf("teen approval: %d %s", w.Code, w.Body.String())
	}
	if w := adminC.do("POST", "/api/v1/add-requests/"+itoa(body.Request.ID)+"/decision", map[string]string{"party": "admin", "decision": "approved"}, true); w.Code != http.StatusOK {
		t.Fatalf("admin approval: %d %s", w.Code, w.Body.String())
	}
	if membershipStatus(f.app, f.subject, f.group) != "active" {
		t.Fatal("approved teen re-add did not reactivate membership")
	}
}

func departureWriteCounts(t *testing.T, app *App) map[string]int64 {
	t.Helper()
	out := map[string]int64{}
	for name, model := range map[string]any{
		"groups": &models.Group{}, "memberships": &models.Membership{}, "registrations": &models.Registration{},
		"accesses": &models.OccurrenceAccess{}, "audits": &models.AuditLog{}, "notices": &models.Notification{},
	} {
		var count int64
		if err := app.DB.Unscoped().Model(model).Count(&count).Error; err != nil {
			t.Fatal(err)
		}
		out[name] = count
	}
	return out
}

func TestDeparturePreviewDateRulesParityAndReadOnly(t *testing.T) {
	f := newDepartureTestFixture(t)
	now := time.Now().UTC()
	addDepartureRegistration(t, f, f.subject, "Past attendance", models.VisibilityPublic, nil, now.Add(-3*time.Hour), now.Add(-2*time.Hour), models.OccurrenceScheduled, models.RegGoing)
	addDepartureRegistration(t, f, f.subject, "Underway private", models.VisibilityPrivate, nil, now.Add(-30*time.Minute), now.Add(30*time.Minute), models.OccurrenceScheduled, models.RegGoing)
	addDepartureRegistration(t, f, f.subject, "Cutoff invite only", models.VisibilityInviteOnly, nil, now.Add(time.Hour), now.Add(2*time.Hour), models.OccurrenceScheduled, models.RegMaybe)
	publicOverride := models.VisibilityPublic
	publicOcc, _ := addDepartureRegistration(t, f, f.subject, "Opened public date", models.VisibilityInviteOnly, &publicOverride, now.Add(4*time.Hour), now.Add(5*time.Hour), models.OccurrenceScheduled, models.RegWaitlisted)
	addDepartureRegistration(t, f, f.subject, "Future private", models.VisibilityPrivate, nil, now.Add(5*time.Hour), now.Add(6*time.Hour), models.OccurrenceScheduled, models.RegGoing)
	addDepartureRegistration(t, f, f.subject, "Already can't go", models.VisibilityPublic, nil, now.Add(6*time.Hour), now.Add(7*time.Hour), models.OccurrenceScheduled, models.RegDeclined)
	addDepartureRegistration(t, f, f.subject, "Already removed", models.VisibilityPublic, nil, now.Add(7*time.Hour), now.Add(8*time.Hour), models.OccurrenceScheduled, models.RegRemoved)
	addDepartureRegistration(t, f, f.subject, "Already cancelled", models.VisibilityPublic, nil, now.Add(8*time.Hour), now.Add(9*time.Hour), models.OccurrenceScheduled, models.RegCancelled)
	addDepartureRegistration(t, f, f.subject, "Cancelled occurrence", models.VisibilityPublic, nil, now.Add(9*time.Hour), now.Add(10*time.Hour), models.OccurrenceCancelled, models.RegGoing)
	addDepartureProxyRegistration(t, f, publicOcc)

	before := departureWriteCounts(t, f.app)
	selfPath := "/api/v1/groups/" + itoa(f.group.ID) + "/departure-preview"
	self := getDeparturePreview(t, f.subjectC, selfPath, http.StatusOK)
	raw := f.subjectC.do("GET", selfPath, nil, false).Body.String()
	for _, forbidden := range []string{`"eventId"`, `"attendees"`, `"location"`, `"recurrenceRule"`, "Future private", "Past attendance", "Already can't go"} {
		if strings.Contains(raw, forbidden) {
			t.Fatalf("preview leaked %q: %s", forbidden, raw)
		}
	}
	if self.Action != "leave" || self.GroupID != f.group.ID || self.GroupName != f.group.Name || self.SubjectID != f.subject.ID || self.SubjectName != f.subject.PublicName() {
		t.Fatalf("self context: %+v", self)
	}
	if self.AffectedCount != 4 || self.GoingCount != 2 || self.MaybeCount != 1 || self.WaitlistedCount != 1 || self.KeepableCount != 3 || self.DefaultKeepCount != 2 {
		t.Fatalf("counts: %+v", self)
	}
	if len(self.Dates) != 3 || self.Snapshot == "" {
		t.Fatalf("dates/snapshot: %+v", self)
	}
	want := map[string]struct {
		defaultKeep bool
		reason      string
		visibility  string
	}{
		"Underway private":   {true, policy.DepartureKeepUnderway, models.VisibilityPrivate},
		"Cutoff invite only": {true, policy.DepartureKeepCutoff, models.VisibilityInviteOnly},
		"Opened public date": {false, policy.DepartureKeepPublic, models.VisibilityPublic},
	}
	for _, d := range self.Dates {
		w, ok := want[d.Title]
		if !ok || d.DefaultKeep != w.defaultKeep || d.KeepReason != w.reason || d.Visibility != w.visibility {
			t.Fatalf("date %+v want %+v", d, w)
		}
		if strings.Contains(d.Title, "Past") || strings.Contains(d.Title, "Proxy") {
			t.Fatalf("historical/proxy date leaked: %+v", d)
		}
	}

	removePath := "/api/v1/groups/" + itoa(f.group.ID) + "/members/" + itoa(f.subject.ID) + "/departure-preview"
	removed := getDeparturePreview(t, f.leaderC, removePath, http.StatusOK)
	if removed.Action != "remove" || removed.AffectedCount != self.AffectedCount || removed.GoingCount != self.GoingCount || len(removed.Dates) != len(self.Dates) {
		t.Fatalf("self/removal parity: self=%+v remove=%+v", self, removed)
	}
	if after := departureWriteCounts(t, f.app); !mapsEqualCounts(before, after) {
		t.Fatalf("GET wrote rows: before=%v after=%v", before, after)
	}
	if membershipStatus(f.app, f.subject, f.group) != "active" {
		t.Fatal("preview changed membership")
	}
}

func mapsEqualCounts(a, b map[string]int64) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func TestDeparturePreviewZeroDatesAuthorizationAndPrivacy(t *testing.T) {
	f := newDepartureTestFixture(t)
	path := "/api/v1/groups/" + itoa(f.group.ID) + "/departure-preview"
	zero := getDeparturePreview(t, f.subjectC, path, http.StatusOK)
	if zero.AffectedCount != 0 || zero.KeepableCount != 0 || len(zero.Dates) != 0 || zero.Snapshot == "" {
		t.Fatalf("zero-date context: %+v", zero)
	}

	outsider := newTestUser(t, f.app, "departure outsider", "password1234", models.RoleMember)
	outsiderC := loginAPI(t, f.app, outsider.Email, "password1234")
	removePath := "/api/v1/groups/" + itoa(f.group.ID) + "/members/" + itoa(f.subject.ID) + "/departure-preview"
	w := outsiderC.do("GET", removePath, nil, false)
	if w.Code != http.StatusForbidden || strings.Contains(w.Body.String(), f.group.Name) || strings.Contains(w.Body.String(), f.subject.PublicName()) {
		t.Fatalf("unauthorized preview leaked context: %d %s", w.Code, w.Body.String())
	}

	f.app.DB.Model(f.group).Update("visibility", models.VisibilityInviteOnly)
	w = outsiderC.do("GET", removePath, nil, false)
	if w.Code != http.StatusNotFound || strings.Contains(w.Body.String(), f.group.Name) {
		t.Fatalf("hidden group preview: %d %s", w.Code, w.Body.String())
	}

	beforeAudit := int64(0)
	f.app.DB.Model(&models.AuditLog{}).Count(&beforeAudit)
	f.app.DB.Model(f.subject).Update("status", models.UserDisabled)
	w = f.subjectC.do("GET", path, nil, false)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("disabled actor: %d %s", w.Code, w.Body.String())
	}
	var afterAudit int64
	f.app.DB.Model(&models.AuditLog{}).Count(&afterAudit)
	if afterAudit != beforeAudit {
		t.Fatalf("disabled GET wrote audit: %d -> %d", beforeAudit, afterAudit)
	}
}

func TestDeparturePreviewSoleLeaderAndAdminTarget(t *testing.T) {
	app := testApp(t)
	app.Config.SessionSecret = []byte("departure-preview-test-secret-32-bytes")
	leader := newTestUser(t, app, "sole departure leader", "password1234", models.RoleMember)
	group := newTestGroup(t, app, leader, "Sole leader group", models.GroupActive)
	leaderC := loginAPI(t, app, leader.Email, "password1234")
	w := leaderC.do("GET", "/api/v1/groups/"+itoa(group.ID)+"/departure-preview", nil, false)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), `"code":"departure_unavailable"`) {
		t.Fatalf("sole leader preview: %d %s", w.Code, w.Body.String())
	}

	adminActor := newTestUser(t, app, "departure admin actor", "password1234", models.RoleAdmin)
	adminTarget := newTestUser(t, app, "departure admin target", "password1234", models.RoleAdmin)
	addMember(t, app, adminTarget, group, models.MembershipMember)
	removePath := "/api/v1/groups/" + itoa(group.ID) + "/members/" + itoa(adminTarget.ID) + "/departure-preview"
	w = leaderC.do("GET", removePath, nil, false)
	if w.Code != http.StatusConflict {
		t.Fatalf("leader removing admin target: %d %s", w.Code, w.Body.String())
	}
	adminC := loginAPI(t, app, adminActor.Email, "password1234")
	preview := getDeparturePreview(t, adminC, removePath, http.StatusOK)
	if preview.Action != "remove" || preview.SubjectID != adminTarget.ID {
		t.Fatalf("admin removal context: %+v", preview)
	}
}

func loadDepartureFactsForTest(t *testing.T, f departureTestFixture, actor, subject *models.User, now time.Time) departureFacts {
	t.Helper()
	var facts departureFacts
	err := f.app.DB.Transaction(func(tx *gorm.DB) error {
		var err error
		facts, err = loadDepartureFacts(tx, actor, f.group.ID, subject.ID, now)
		return err
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	return facts
}

func TestDepartureSnapshotTamperBindingAndChangeDetection(t *testing.T) {
	t.Run("tamper and actor binding", func(t *testing.T) {
		f := newDepartureTestFixture(t)
		now := time.Now().UTC().Truncate(time.Second)
		addDepartureRegistration(t, f, f.subject, "Snapshot date", models.VisibilityPublic, nil, now.Add(4*time.Hour), now.Add(5*time.Hour), models.OccurrenceScheduled, models.RegGoing)
		facts := loadDepartureFactsForTest(t, f, f.subject, f.subject, now)
		token, err := f.app.newDepartureSnapshot(facts)
		if err != nil {
			t.Fatal(err)
		}
		snapshot, err := f.app.parseDepartureSnapshot(token, f.subject.ID, f.subject.ID, f.group.ID)
		if err != nil || !departureSnapshotMatches(snapshot, facts) {
			t.Fatalf("valid snapshot: %+v %v", snapshot, err)
		}
		sealed, err := base64.RawURLEncoding.DecodeString(token)
		if err != nil {
			t.Fatal(err)
		}
		crypt, err := f.app.departureSnapshotCipher()
		if err != nil {
			t.Fatal(err)
		}
		if len(sealed) <= crypt.NonceSize() {
			t.Fatalf("snapshot ciphertext length = %d, nonce size = %d", len(sealed), crypt.NonceSize())
		}
		original := append([]byte(nil), sealed...)
		sealed[crypt.NonceSize()] ^= 1
		if bytes.Equal(sealed, original) {
			t.Fatal("tamper did not change decoded snapshot bytes")
		}
		tampered := base64.RawURLEncoding.EncodeToString(sealed)
		if tampered == token {
			t.Fatal("tamper did not change encoded snapshot")
		}
		if _, err := f.app.parseDepartureSnapshot(tampered, f.subject.ID, f.subject.ID, f.group.ID); err == nil {
			t.Fatal("tampered snapshot accepted")
		}
		if _, err := f.app.parseDepartureSnapshot(token, f.leader.ID, f.subject.ID, f.group.ID); err == nil {
			t.Fatal("snapshot accepted for another actor")
		}
		if _, err := f.app.parseDepartureSnapshot(token, f.subject.ID, f.leader.ID, f.group.ID); err == nil {
			t.Fatal("snapshot accepted for another subject")
		}
		if _, err := f.app.parseDepartureSnapshot(token, f.subject.ID, f.subject.ID, f.group.ID+1); err == nil {
			t.Fatal("snapshot accepted for another group")
		}
	})

	for _, tc := range []struct {
		name   string
		change func(t *testing.T, f departureTestFixture, occ models.EventOccurrence, reg models.Registration, now time.Time)
	}{
		{"new eligible date", func(t *testing.T, f departureTestFixture, _ models.EventOccurrence, _ models.Registration, now time.Time) {
			addDepartureRegistration(t, f, f.subject, "Added date", models.VisibilityPublic, nil, now.Add(6*time.Hour), now.Add(7*time.Hour), models.OccurrenceScheduled, models.RegMaybe)
		}},
		{"removed eligible date", func(t *testing.T, f departureTestFixture, _ models.EventOccurrence, reg models.Registration, _ time.Time) {
			if err := f.app.DB.Model(&reg).Update("status", models.RegDeclined).Error; err != nil {
				t.Fatal(err)
			}
		}},
		{"membership role", func(t *testing.T, f departureTestFixture, _ models.EventOccurrence, _ models.Registration, _ time.Time) {
			addMember(t, f.app, newTestUser(t, f.app, "second departure leader", "password1234", models.RoleMember), f.group, models.MembershipLeader)
			if err := f.app.DB.Model(&models.Membership{}).Where("user_id = ? AND group_id = ?", f.subject.ID, f.group.ID).Update("role", models.MembershipLeader).Error; err != nil {
				t.Fatal(err)
			}
		}},
		{"effective visibility", func(t *testing.T, f departureTestFixture, occ models.EventOccurrence, _ models.Registration, _ time.Time) {
			private := models.VisibilityPrivate
			if err := f.app.DB.Model(&occ).Update("visibility_override", &private).Error; err != nil {
				t.Fatal(err)
			}
		}},
		{"registration facts", func(t *testing.T, f departureTestFixture, _ models.EventOccurrence, reg models.Registration, _ time.Time) {
			if err := f.app.DB.Model(&reg).Update("guest_count", 1).Error; err != nil {
				t.Fatal(err)
			}
		}},
		{"access provenance", func(t *testing.T, f departureTestFixture, occ models.EventOccurrence, reg models.Registration, now time.Time) {
			issuer := f.leader.ID
			if err := f.app.DB.Create(&models.OccurrenceAccess{SubjectUserID: f.subject.ID, OccurrenceID: occ.ID, EventID: occ.EventID,
				OrganizerType: policy.ContactGroup, OrganizerID: f.group.ID, Origin: policy.OccurrenceOriginManagerAdd,
				IssuerID: &issuer, RegistrationID: reg.ID, Status: policy.OccurrenceAccessActive, GrantedAt: now}).Error; err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newDepartureTestFixture(t)
			now := time.Now().UTC().Truncate(time.Second)
			occ, reg := addDepartureRegistration(t, f, f.subject, "Snapshot date", models.VisibilityPublic, nil, now.Add(4*time.Hour), now.Add(5*time.Hour), models.OccurrenceScheduled, models.RegGoing)
			before := loadDepartureFactsForTest(t, f, f.subject, f.subject, now)
			token, err := f.app.newDepartureSnapshot(before)
			if err != nil {
				t.Fatal(err)
			}
			snapshot, err := f.app.parseDepartureSnapshot(token, f.subject.ID, f.subject.ID, f.group.ID)
			if err != nil {
				t.Fatal(err)
			}
			tc.change(t, f, occ, reg, now)
			after := loadDepartureFactsForTest(t, f, f.subject, f.subject, now)
			if departureSnapshotMatches(snapshot, after) {
				t.Fatal("changed facts matched the old snapshot")
			}
		})
	}
}

func TestDepartureSnapshotIgnoresUnrelatedResourcesAndUsesCurrentPromotionMath(t *testing.T) {
	f := newDepartureTestFixture(t)
	now := time.Now().UTC().Truncate(time.Second)
	occ, subjectReg := addDepartureRegistration(t, f, f.subject, "Live promotion date", models.VisibilityPublic, nil, now.Add(5*time.Hour), now.Add(6*time.Hour), models.OccurrenceScheduled, models.RegGoing)
	one := 1
	if err := f.app.DB.Model(&models.Event{}).Where("id = ?", occ.EventID).Updates(map[string]any{
		"capacity": one, "waitlist_enabled": true, "guests_allowed": 5,
		"childcare_available": true, "childcare_capacity": one,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.app.DB.Model(&subjectReg).Updates(map[string]any{"childcare_count": 1, "childcare_ages_note": "age 5"}).Error; err != nil {
		t.Fatal(err)
	}

	counter := countDBReads(t, f.app.DB)
	before := loadDepartureFactsForTest(t, f, f.subject, f.subject, now)
	counter.Stop()
	beforeReads := counter.Reads()
	beforeDigest, err := departureFactsDigest(before)
	if err != nil {
		t.Fatal(err)
	}
	preview := getDeparturePreview(t, f.subjectC, "/api/v1/groups/"+itoa(f.group.ID)+"/departure-preview", http.StatusOK)

	other := newTestUser(t, f.app, "departure live other", "password1234", models.RoleMember)
	waiter := newTestUser(t, f.app, "departure live waiter", "password1234", models.RoleMember)
	otherID, waiterID := other.ID, waiter.ID
	waitedAt := now.Add(-time.Hour)
	if err := f.app.DB.Create(&models.Registration{OccurrenceID: occ.ID, UserID: &otherID, Status: models.RegGoing,
		AttendanceMode: models.AttendanceInPerson, GuestCount: 1, ChildcareCount: 1,
		ChildcareAgesNote: "age 6", Source: models.RegSourceWeb}).Error; err != nil {
		t.Fatal(err)
	}
	waitReg := models.Registration{OccurrenceID: occ.ID, UserID: &waiterID, Status: models.RegWaitlisted,
		AttendanceMode: models.AttendanceInPerson, ChildcareCount: 1,
		ChildcareAgesNote: "age 4", WaitlistedAt: &waitedAt, Source: models.RegSourceWeb}
	if err := f.app.DB.Create(&waitReg).Error; err != nil {
		t.Fatal(err)
	}
	three, two := 3, 2
	if err := f.app.DB.Model(&models.Event{}).Where("id = ?", occ.EventID).Updates(map[string]any{
		"capacity": three, "childcare_capacity": two,
	}).Error; err != nil {
		t.Fatal(err)
	}
	// Add enough unrelated rows that accidentally hashing attendee resources
	// would make the stable facts materially larger and stale the chooser.
	for i := 0; i < 100; i++ {
		n := uint(i + 1)
		u := models.User{Name: "Departure unrelated " + itoa(n), Email: "departure-unrelated-" + itoa(n) + "@example.com",
			PasswordHash: f.subject.PasswordHash, Status: models.UserActive, Role: models.RoleMember, AccountType: models.AccountAdult}
		if err := f.app.DB.Create(&u).Error; err != nil {
			t.Fatal(err)
		}
		uid := u.ID
		if err := f.app.DB.Create(&models.Registration{OccurrenceID: occ.ID, UserID: &uid, Status: models.RegWaitlisted,
			AttendanceMode: models.AttendanceRemote, GuestCount: i % 3, WaitlistedAt: &waitedAt, Source: models.RegSourceWeb}).Error; err != nil {
			t.Fatal(err)
		}
	}

	counter = countDBReads(t, f.app.DB)
	after := loadDepartureFactsForTest(t, f, f.subject, f.subject, now)
	counter.Stop()
	afterReads := counter.Reads()
	afterDigest, err := departureFactsDigest(after)
	if err != nil {
		t.Fatal(err)
	}
	if beforeDigest != afterDigest {
		t.Fatal("unrelated RSVP/resource changes altered the departure snapshot")
	}
	if afterReads != beforeReads {
		t.Fatalf("departure fact reads grew with unrelated attendees: before=%d after=%d", beforeReads, afterReads)
	}

	w := confirmDeparturePreview(t, f.subjectC, "POST", "/api/v1/groups/"+itoa(f.group.ID)+"/leave", preview, nil, "")
	if w.Code != http.StatusOK {
		t.Fatalf("confirmation after unrelated resource changes: %d %s", w.Code, w.Body.String())
	}
	if got := regStatus(t, f.app, waiter.ID, occ.ID); got != models.RegGoing {
		t.Fatalf("current locked promotion math left waiter %q", got)
	}
	var promoted models.Registration
	if err := f.app.DB.First(&promoted, waitReg.ID).Error; err != nil || promoted.PromotedAt == nil {
		t.Fatalf("waiter promotion not persisted: %+v err=%v", promoted, err)
	}
}

func TestDepartureConfirmationSelfKeepsCleansAndNotifies(t *testing.T) {
	f := newDepartureTestFixture(t)
	f.app.DB.Model(f.group).Update("visibility", models.VisibilityInviteOnly)
	secondLeader := newTestUser(t, f.app, "departure second leader", "password1234", models.RoleMember)
	addMember(t, f.app, secondLeader, f.group, models.MembershipLeader)
	now := time.Now().UTC()
	past, pastReg := addDepartureRegistration(t, f, f.subject, "Past stays", models.VisibilityPrivate, nil, now.Add(-3*time.Hour), now.Add(-2*time.Hour), models.OccurrenceScheduled, models.RegGoing)
	_ = past
	cutoff, cutoffReg := addDepartureRegistration(t, f, f.subject, "Keep private", models.VisibilityPrivate, nil, now.Add(time.Hour), now.Add(2*time.Hour), models.OccurrenceScheduled, models.RegGoing)
	public, _ := addDepartureRegistration(t, f, f.subject, "Keep public", models.VisibilityPublic, nil, now.Add(4*time.Hour), now.Add(5*time.Hour), models.OccurrenceScheduled, models.RegMaybe)
	ended, endedReg := addDepartureRegistration(t, f, f.subject, "End private", models.VisibilityPrivate, nil, now.Add(5*time.Hour), now.Add(6*time.Hour), models.OccurrenceScheduled, models.RegGoing)
	addDepartureProxyRegistration(t, f, ended)

	issuer := f.leader.ID
	existing := models.OccurrenceAccess{SubjectUserID: f.subject.ID, OccurrenceID: cutoff.ID, EventID: cutoff.EventID,
		OrganizerType: policy.ContactGroup, OrganizerID: f.group.ID, Origin: policy.OccurrenceOriginManagerAdd,
		IssuerID: &issuer, RegistrationID: cutoffReg.ID, Status: policy.OccurrenceAccessActive, GrantedAt: now}
	if err := f.app.DB.Create(&existing).Error; err != nil {
		t.Fatal(err)
	}
	previewPath := "/api/v1/groups/" + itoa(f.group.ID) + "/departure-preview"
	preview := getDeparturePreview(t, f.subjectC, previewPath, http.StatusOK)
	w := confirmDeparturePreview(t, f.subjectC, "POST", "/api/v1/groups/"+itoa(f.group.ID)+"/leave", preview, []uint{cutoff.ID, public.ID}, "")
	if w.Code != http.StatusOK {
		t.Fatalf("confirm: %d %s", w.Code, w.Body.String())
	}
	var response struct {
		Departure departureConfirmationJSON `json:"departure"`
	}
	decodeJSON(t, w, &response)
	if response.Departure.Action != "leave" || response.Departure.KeptCount != 2 || response.Departure.EndedCount != 1 {
		t.Fatalf("result: %+v", response.Departure)
	}
	if membershipStatus(f.app, f.subject, f.group) != models.MembershipLeft {
		t.Fatal("voluntary departure was not marked left")
	}
	if got := regStatus(t, f.app, f.subject.ID, past.ID); got != models.RegGoing {
		t.Fatalf("past registration=%q", got)
	}
	if got := regStatus(t, f.app, f.subject.ID, ended.ID); got != models.RegCancelled {
		t.Fatalf("ended registration=%q", got)
	}
	var refreshed models.OccurrenceAccess
	if err := f.app.DB.First(&refreshed, existing.ID).Error; err != nil || refreshed.Status != policy.OccurrenceAccessEnded ||
		refreshed.EndReason != policy.OccurrenceDepartureSuperseded || refreshed.EndedBy == nil || *refreshed.EndedBy != f.subject.ID ||
		refreshed.Origin != policy.OccurrenceOriginManagerAdd || refreshed.IssuerID == nil || *refreshed.IssuerID != issuer || refreshed.RegistrationID != cutoffReg.ID {
		t.Fatalf("manager access history not preserved: %+v err=%v", refreshed, err)
	}
	var retained []models.OccurrenceAccess
	if err := f.app.DB.Where("subject_user_id = ? AND occurrence_id IN ?", f.subject.ID, []uint{cutoff.ID, public.ID}).Order("occurrence_id, id").Find(&retained).Error; err != nil {
		t.Fatal(err)
	}
	activeByOccurrence := map[uint]models.OccurrenceAccess{}
	for _, row := range retained {
		if row.Status == policy.OccurrenceAccessActive {
			activeByOccurrence[row.OccurrenceID] = row
		}
	}
	for _, occurrenceID := range []uint{cutoff.ID, public.ID} {
		row := activeByOccurrence[occurrenceID]
		if row.Origin != policy.OccurrenceOriginDepartureSelf || row.DepartureMembershipID == nil || row.DepartureActorID == nil || *row.DepartureActorID != f.subject.ID {
			t.Fatalf("retained provenance for %d: %+v", occurrenceID, row)
		}
	}
	var proxyCount int64
	if err := f.app.DB.Model(&models.Registration{}).Where("occurrence_id = ? AND household_person_id IS NOT NULL AND status = ?", ended.ID, models.RegGoing).Count(&proxyCount).Error; err != nil || proxyCount != 1 {
		t.Fatalf("proxy changed: %d %v", proxyCount, err)
	}
	var leftNotices, subjectNotices, perDateNotices int64
	f.app.DB.Model(&models.Notification{}).Where("kind = ?", notify.MembershipLeft).Count(&leftNotices)
	f.app.DB.Model(&models.Notification{}).Where("kind = ? AND user_id = ?", notify.MembershipDeparture, f.subject.ID).Count(&subjectNotices)
	f.app.DB.Model(&models.Notification{}).Where("kind = ?", notify.OccurrenceUpdated).Count(&perDateNotices)
	if leftNotices != 2 || subjectNotices != 0 || perDateNotices != 0 {
		t.Fatalf("notices left=%d subject=%d per-date=%d", leftNotices, subjectNotices, perDateNotices)
	}
	if auditCount(t, f.app, audit.RoleChanged) != 1 || auditCount(t, f.app, audit.RegistrationChanged) != 1 ||
		auditCount(t, f.app, audit.OccurrenceAccessAdded) != 2 || auditCount(t, f.app, audit.OccurrenceAccessEnded) != 1 {
		t.Fatal("departure audits missing")
	}
	if w := f.subjectC.do("GET", "/api/v1/groups/"+itoa(f.group.ID), nil, false); w.Code != http.StatusNotFound {
		t.Fatalf("hidden parent group leaked: %d %s", w.Code, w.Body.String())
	}
	if w := f.subjectC.do("GET", "/api/v1/events/"+itoa(cutoff.EventID)+"/dates/"+cutoff.OriginalDate, nil, false); w.Code != http.StatusOK {
		t.Fatalf("kept exact date unavailable: %d %s", w.Code, w.Body.String())
	}
	var unchanged models.Registration
	if err := f.app.DB.First(&unchanged, endedReg.ID).Error; err != nil || unchanged.RemovedBy != nil {
		t.Fatalf("self leave got manager removal fields: %+v %v", unchanged, err)
	}
	_ = pastReg
}

func TestDepartureSupersedesManagerAccessForExactEndLifecycle(t *testing.T) {
	for _, action := range []string{"self", "manager"} {
		t.Run(action, func(t *testing.T) {
			f := newDepartureTestFixture(t)
			if err := f.app.DB.Model(f.group).Update("visibility", models.VisibilityInviteOnly).Error; err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC().Truncate(time.Second)
			occ, reg := addDepartureRegistration(t, f, f.subject, "Superseded retained date", models.VisibilityInviteOnly, nil,
				now.Add(time.Hour), now.Add(2*time.Hour), models.OccurrenceScheduled, models.RegGoing)
			issuer := f.leader.ID
			manager := models.OccurrenceAccess{SubjectUserID: f.subject.ID, OccurrenceID: occ.ID, EventID: occ.EventID,
				OrganizerType: policy.ContactGroup, OrganizerID: f.group.ID, Origin: policy.OccurrenceOriginManagerAdd,
				IssuerID: &issuer, RegistrationID: reg.ID, Status: policy.OccurrenceAccessActive, GrantedAt: now.Add(-time.Hour)}
			var requestID uint
			if err := f.app.DB.Transaction(func(tx *gorm.DB) error {
				if action == "manager" {
					created, decided, completed := now.Add(-3*time.Hour), now.Add(-2*time.Hour), now.Add(-90*time.Minute)
					expires, occurrenceID, registrationID, subjectID := now.Add(24*time.Hour), occ.ID, reg.ID, f.subject.ID
					request := models.ContactRequest{RequesterID: issuer, SubjectUserID: subjectID, ContextType: policy.ContactEvent,
						ContextID: occ.EventID, Purpose: policy.RequestOccurrenceAdd, OccurrenceID: &occurrenceID,
						AddAttendanceMode: models.AttendanceInPerson, CompletedTargetID: &registrationID,
						SubjectDecision: policy.ContactApproved, SubjectDecidedBy: &subjectID, SubjectDecidedAt: &decided,
						AdminDecision: policy.ContactNotNeeded, Status: policy.ContactGranted,
						CreatedAt: created, ExpiresAt: expires, CompletedAt: &completed}
					if err := tx.Create(&request).Error; err != nil {
						return err
					}
					requestID, manager.RequestID = request.ID, &request.ID
				}
				return tx.Create(&manager).Error
			}); err != nil {
				t.Fatal(err)
			}

			previewPath := "/api/v1/groups/" + itoa(f.group.ID) + "/departure-preview"
			mutationPath := "/api/v1/groups/" + itoa(f.group.ID) + "/leave"
			client := f.subjectC
			actorID := f.subject.ID
			wantOrigin := policy.OccurrenceOriginDepartureSelf
			if action == "manager" {
				previewPath = "/api/v1/groups/" + itoa(f.group.ID) + "/members/" + itoa(f.subject.ID) + "/departure-preview"
				mutationPath = "/api/v1/groups/" + itoa(f.group.ID) + "/members/" + itoa(f.subject.ID) + "/departure"
				client = f.leaderC
				actorID = f.leader.ID
				wantOrigin = policy.OccurrenceOriginDepartureRemoval
			}
			preview := getDeparturePreview(t, client, previewPath, http.StatusOK)
			w := confirmDeparturePreview(t, client, "POST", mutationPath, preview, []uint{occ.ID}, "")
			if w.Code != http.StatusOK {
				t.Fatalf("confirm: %d %s", w.Code, w.Body.String())
			}
			if strings.Contains(w.Body.String(), `"members"`) {
				t.Fatalf("departure response included roster: %s", w.Body.String())
			}

			var history []models.OccurrenceAccess
			if err := f.app.DB.Where("subject_user_id = ? AND occurrence_id = ?", f.subject.ID, occ.ID).Order("id").Find(&history).Error; err != nil {
				t.Fatal(err)
			}
			if len(history) != 2 {
				t.Fatalf("access history=%+v", history)
			}
			old, current := history[0], history[1]
			if old.ID != manager.ID || old.Origin != policy.OccurrenceOriginManagerAdd || old.IssuerID == nil || *old.IssuerID != issuer ||
				old.RegistrationID != reg.ID || ptrUint(old.RequestID) != requestID || old.Status != policy.OccurrenceAccessEnded ||
				old.EndReason != policy.OccurrenceDepartureSuperseded || old.EndedBy == nil || *old.EndedBy != actorID {
				t.Fatalf("manager history changed: %+v", old)
			}
			if current.Origin != wantOrigin || current.Status != policy.OccurrenceAccessActive || current.DepartureMembershipID == nil ||
				current.DepartureActorID == nil || current.RegistrationID != reg.ID {
				t.Fatalf("departure successor: %+v", current)
			}
			var active int64
			if err := f.app.DB.Model(&models.OccurrenceAccess{}).Where("subject_user_id = ? AND occurrence_id = ? AND status = ?", f.subject.ID, occ.ID, policy.OccurrenceAccessActive).Count(&active).Error; err != nil || active != 1 {
				t.Fatalf("active access=%d err=%v", active, err)
			}
			var perDate int64
			if err := f.app.DB.Model(&models.Notification{}).Where("kind = ?", notify.OccurrenceUpdated).Count(&perDate).Error; err != nil || perDate != 0 {
				t.Fatalf("per-date departure notices=%d err=%v", perDate, err)
			}

			boundary := occ.EndsAt
			before := boundary.Add(-time.Nanosecond)
			dateBefore, err := f.app.scopedOccurrenceReads(f.subject, f.app.DB.Where("id = ?", occ.ID), before)
			if err != nil || dateBefore[occ.ID] == nil {
				t.Fatalf("date access before end: %v %+v", err, dateBefore)
			}
			dateAt, err := f.app.scopedOccurrenceReads(f.subject, f.app.DB.Where("id = ?", occ.ID), boundary)
			if err != nil || dateAt[occ.ID] != nil {
				t.Fatalf("date access at exact end: %v %+v", err, dateAt)
			}
			calendarBefore, err := f.app.scopedOccurrenceReads(f.subject, f.app.DB.Where("starts_at >= ? AND starts_at < ?", now, boundary.Add(time.Hour)), before)
			if err != nil || calendarBefore[occ.ID] == nil {
				t.Fatalf("calendar access before end: %v %+v", err, calendarBefore)
			}
			calendarAt, err := f.app.scopedOccurrenceReads(f.subject, f.app.DB.Where("starts_at >= ? AND starts_at < ?", now, boundary.Add(time.Hour)), boundary)
			if err != nil || calendarAt[occ.ID] != nil {
				t.Fatalf("calendar access at exact end: %v %+v", err, calendarAt)
			}
			if feed := mustBuildFeed(t, f.app, f.subject, feedOptions{personal: true, scope: models.CalendarFeedMine}, before); len(feed.Singles) != 1 {
				t.Fatalf("feed before end=%+v", feed.Singles)
			}
			if feed := mustBuildFeed(t, f.app, f.subject, feedOptions{personal: true, scope: models.CalendarFeedMine}, boundary); len(feed.Singles) != 0 {
				t.Fatalf("feed at exact end=%+v", feed.Singles)
			}

			// Simulate a missed daily sweep. Read-time authorization is already
			// closed; the next event mutation persists expiry before moving the
			// occurrence into the future, so the move cannot resurrect access.
			past := time.Now().UTC().Add(-time.Hour)
			if err := f.app.DB.Model(&models.EventOccurrence{}).Where("id = ?", occ.ID).Updates(map[string]any{
				"starts_at": past.Add(-time.Hour), "ends_at": past,
			}).Error; err != nil {
				t.Fatal(err)
			}
			future := time.Now().UTC().AddDate(0, 0, 30)
			if err := f.app.contactMutation(contactChange{EventID: occ.EventID}, func(tx *gorm.DB) error {
				return tx.Model(&models.EventOccurrence{}).Where("id = ?", occ.ID).Updates(map[string]any{
					"starts_at": future, "ends_at": future.Add(time.Hour),
				}).Error
			}); err != nil {
				t.Fatal(err)
			}
			if err := f.app.DB.First(&current, current.ID).Error; err != nil || current.Status != policy.OccurrenceAccessEnded || current.EndReason != policy.OccurrenceEnded {
				t.Fatalf("missed-sweep move did not persist expiry: %+v err=%v", current, err)
			}
			apiDo(t, f.subjectC, "GET", "/api/v1/events/"+itoa(occ.EventID)+"/dates/"+occ.OriginalDate, nil, http.StatusNotFound)

			wantAdd := http.StatusConflict // repeat adds still need a new approved request
			if action == "self" {
				if err := f.app.DB.Create(&models.ManagerAddBlock{SubjectUserID: f.subject.ID, OrganizerType: policy.ContactGroup, OrganizerID: f.group.ID}).Error; err != nil {
					t.Fatal(err)
				}
				wantAdd = http.StatusForbidden // organizer blocks still win
			}
			w = f.leaderC.do("POST", "/api/v1/events/"+itoa(occ.EventID)+"/registrations/add", map[string]any{
				"userId": f.subject.ID, "date": occ.OriginalDate,
			}, true)
			if w.Code != wantAdd {
				t.Fatalf("manager safeguard after supersession: %d %s, want %d", w.Code, w.Body.String(), wantAdd)
			}
			var historyCount int64
			if err := f.app.DB.Model(&models.OccurrenceAccess{}).Where("subject_user_id = ? AND occurrence_id = ?", f.subject.ID, occ.ID).Count(&historyCount).Error; err != nil || historyCount != 2 {
				t.Fatalf("manager refusal changed access history: %d err=%v", historyCount, err)
			}
		})
	}
}

func TestDepartureKeepsExistingDepartureOrigin(t *testing.T) {
	f := newDepartureTestFixture(t)
	now := time.Now().UTC().Truncate(time.Second)
	occ, reg := addDepartureRegistration(t, f, f.subject, "Already retained date", models.VisibilityPrivate, nil,
		now.Add(time.Hour), now.Add(2*time.Hour), models.OccurrenceScheduled, models.RegGoing)
	var membership models.Membership
	if err := f.app.DB.Where("user_id = ? AND group_id = ?", f.subject.ID, f.group.ID).First(&membership).Error; err != nil {
		t.Fatal(err)
	}
	actorID := f.subject.ID
	existing := models.OccurrenceAccess{SubjectUserID: f.subject.ID, OccurrenceID: occ.ID, EventID: occ.EventID,
		OrganizerType: policy.ContactGroup, OrganizerID: f.group.ID, Origin: policy.OccurrenceOriginDepartureSelf,
		RegistrationID: reg.ID, DepartureMembershipID: &membership.ID, DepartureActorID: &actorID,
		Status: policy.OccurrenceAccessActive, GrantedAt: now}
	if err := f.app.DB.Create(&existing).Error; err != nil {
		t.Fatal(err)
	}
	preview := getDeparturePreview(t, f.subjectC, "/api/v1/groups/"+itoa(f.group.ID)+"/departure-preview", http.StatusOK)
	w := confirmDeparturePreview(t, f.subjectC, "POST", "/api/v1/groups/"+itoa(f.group.ID)+"/leave", preview, []uint{occ.ID}, "")
	if w.Code != http.StatusOK {
		t.Fatalf("confirm: %d %s", w.Code, w.Body.String())
	}
	var rows []models.OccurrenceAccess
	if err := f.app.DB.Where("subject_user_id = ? AND occurrence_id = ?", f.subject.ID, occ.ID).Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].ID != existing.ID || rows[0].Origin != policy.OccurrenceOriginDepartureSelf || rows[0].Status != policy.OccurrenceAccessActive {
		t.Fatalf("existing departure provenance was rewritten: %+v", rows)
	}
}

func TestDepartureConfirmationRemovalSummaryGuardianAndAdminParity(t *testing.T) {
	t.Run("leader removes teen", func(t *testing.T) {
		f := newDepartureTestFixture(t)
		if err := f.app.DB.Model(f.subject).Update("account_type", models.AccountTeen).Error; err != nil {
			t.Fatal(err)
		}
		guardian := newTestUser(t, f.app, "departure guardian", "password1234", models.RoleMember)
		pending := newTestUser(t, f.app, "departure pending guardian", "password1234", models.RoleMember)
		teenID := f.subject.ID
		if err := f.app.DB.Create(&models.GuardianLink{GuardianUserID: guardian.ID, TeenUserID: &teenID, Status: models.GuardianConfirmed}).Error; err != nil {
			t.Fatal(err)
		}
		if err := f.app.DB.Create(&models.GuardianLink{GuardianUserID: pending.ID, TeenUserID: &teenID, Status: models.GuardianPending}).Error; err != nil {
			t.Fatal(err)
		}
		now := time.Now().UTC()
		occ, reg := addDepartureRegistration(t, f, f.subject, "Private youth date", models.VisibilityPrivate, nil, now.Add(5*time.Hour), now.Add(6*time.Hour), models.OccurrenceScheduled, models.RegGoing)
		previewPath := "/api/v1/groups/" + itoa(f.group.ID) + "/members/" + itoa(f.subject.ID) + "/departure-preview"
		preview := getDeparturePreview(t, f.leaderC, previewPath, http.StatusOK)
		mutation := "/api/v1/groups/" + itoa(f.group.ID) + "/members/" + itoa(f.subject.ID) + "/departure"
		w := confirmDeparturePreview(t, f.leaderC, "POST", mutation, preview, nil, "private note")
		if w.Code != http.StatusOK {
			t.Fatalf("confirm: %d %s", w.Code, w.Body.String())
		}
		var saved models.Registration
		f.app.DB.First(&saved, reg.ID)
		if saved.Status != models.RegRemoved || saved.RemovedBy == nil || *saved.RemovedBy != f.leader.ID || saved.RemovalNote != "private note" {
			t.Fatalf("removed registration: %+v", saved)
		}
		var notices []models.Notification
		if err := f.app.DB.Where("kind = ?", notify.MembershipDeparture).Order("user_id").Find(&notices).Error; err != nil {
			t.Fatal(err)
		}
		if len(notices) != 2 || notices[0].UserID != f.subject.ID || notices[1].UserID != guardian.ID {
			t.Fatalf("guardian recipients: %+v", notices)
		}
		for _, n := range notices {
			if strings.Contains(n.Title+n.Body+n.Data, f.group.Name) || strings.Contains(n.Title+n.Body+n.Data, f.subject.PublicName()) ||
				n.SubjectType != "membership" || n.SubjectID == nil || strings.Contains(n.Data, "groupId") || !strings.Contains(n.Body, "0 upcoming dates were kept and 1 ended") {
				t.Fatalf("unsafe summary: %+v", n)
			}
		}
		var perDate int64
		f.app.DB.Model(&models.Notification{}).Where("kind = ?", notify.OccurrenceUpdated).Count(&perDate)
		if perDate != 0 || regStatus(t, f.app, f.subject.ID, occ.ID) != models.RegRemoved {
			t.Fatalf("per-date=%d status=%s", perDate, regStatus(t, f.app, f.subject.ID, occ.ID))
		}
	})

	t.Run("admin uses the same removal contract", func(t *testing.T) {
		f := newDepartureTestFixture(t)
		admin := newTestUser(t, f.app, "departure parity admin", "password1234", models.RoleAdmin)
		client := loginAPI(t, f.app, admin.Email, "password1234")
		previewPath := "/api/v1/groups/" + itoa(f.group.ID) + "/members/" + itoa(f.subject.ID) + "/departure-preview"
		preview := getDeparturePreview(t, client, previewPath, http.StatusOK)
		legacy := "/api/v1/admin/groups/" + itoa(f.group.ID) + "/members/" + itoa(f.subject.ID)
		w := confirmDeparturePreview(t, client, "DELETE", legacy, preview, nil, "")
		if w.Code != http.StatusOK || membershipStatus(f.app, f.subject, f.group) != memberRemoved {
			t.Fatalf("admin removal: %d %s status=%s", w.Code, w.Body.String(), membershipStatus(f.app, f.subject, f.group))
		}
	})
}

func TestDepartureRemovalResponseStaysPrivateDuringDelayedMailDemotion(t *testing.T) {
	f := newDepartureTestFixture(t)
	if err := f.app.DB.Model(f.group).Update("visibility", models.VisibilityInviteOnly).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.app.DB.Model(f.subject).Update("account_type", models.AccountTeen).Error; err != nil {
		t.Fatal(err)
	}
	coLeader := newTestUser(t, f.app, "departure privacy co-leader", "password1234", models.RoleMember)
	addMember(t, f.app, coLeader, f.group, models.MembershipLeader)
	coLeaderC := loginAPI(t, f.app, coLeader.Email, "password1234")
	remainingTeen := newTestUser(t, f.app, "departure private remaining teen", "password1234", models.RoleMember)
	if err := f.app.DB.Model(remainingTeen).Updates(map[string]any{"account_type": models.AccountTeen, "phone": "+15550001111"}).Error; err != nil {
		t.Fatal(err)
	}
	addMember(t, f.app, remainingTeen, f.group, models.MembershipMember)
	hiddenAdult := newTestUser(t, f.app, "departure private hidden adult", "password1234", models.RoleMember)
	if err := f.app.DB.Model(hiddenAdult).Updates(map[string]any{"phone": "+15550002222", "hide_contact_from_leaders": true}).Error; err != nil {
		t.Fatal(err)
	}
	addMember(t, f.app, hiddenAdult, f.group, models.MembershipMember)

	blocker := &blockingDepartureMailSender{
		delegate: f.app.Mailer, target: f.subject.Email,
		reached: make(chan struct{}), release: make(chan struct{}),
	}
	f.app.Mailer = blocker
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(blocker.release) }) }
	defer release()
	previewPath := "/api/v1/groups/" + itoa(f.group.ID) + "/members/" + itoa(f.subject.ID) + "/departure-preview"
	removePath := "/api/v1/groups/" + itoa(f.group.ID) + "/members/" + itoa(f.subject.ID) + "/departure"
	preview := getDeparturePreview(t, f.leaderC, previewPath, http.StatusOK)
	client := cloneContactClient(f.leaderC)
	response := queueContact(func() *httptest.ResponseRecorder {
		return confirmDeparturePreview(t, client, "POST", removePath, preview, nil, "")
	})
	select {
	case <-blocker.reached:
	case <-time.After(5 * time.Second):
		t.Fatal("departure did not reach delayed post-commit mail")
	}
	rolePath := "/api/v1/groups/" + itoa(f.group.ID) + "/members/" + itoa(f.leader.ID) + "/role"
	if w := coLeaderC.do("PUT", rolePath, map[string]any{"role": models.MembershipMember}, true); w.Code != http.StatusOK {
		release()
		t.Fatalf("demote during mail: %d %s", w.Code, w.Body.String())
	}
	release()
	w := awaitContactResponse(t, response)
	if w.Code != http.StatusOK {
		t.Fatalf("departure response: %d %s", w.Code, w.Body.String())
	}
	for _, secret := range []string{`"members"`, remainingTeen.PublicName(), remainingTeen.Email, remainingTeen.Phone, hiddenAdult.Email, hiddenAdult.Phone} {
		if strings.Contains(w.Body.String(), secret) {
			t.Fatalf("departure response disclosed %q after demotion: %s", secret, w.Body.String())
		}
	}

	get := f.leaderC.do("GET", "/api/v1/groups/"+itoa(f.group.ID), nil, false)
	if get.Code != http.StatusOK || !strings.Contains(get.Body.String(), `"canManage":false`) {
		t.Fatalf("authorized GET did not apply current role: %d %s", get.Code, get.Body.String())
	}
	for _, secret := range []string{remainingTeen.PublicName(), remainingTeen.Email, remainingTeen.Phone, hiddenAdult.Email, hiddenAdult.Phone} {
		if strings.Contains(get.Body.String(), secret) {
			t.Fatalf("current GET disclosed %q to demoted actor: %s", secret, get.Body.String())
		}
	}
}

func TestDepartureConfirmationRejectsStaleForgedAndMissingAtomically(t *testing.T) {
	f := newDepartureTestFixture(t)
	now := time.Now().UTC()
	occ, reg := addDepartureRegistration(t, f, f.subject, "Stale date", models.VisibilityPublic, nil, now.Add(4*time.Hour), now.Add(5*time.Hour), models.OccurrenceScheduled, models.RegGoing)
	privateOcc, _ := addDepartureRegistration(t, f, f.subject, "Not keepable", models.VisibilityPrivate, nil, now.Add(6*time.Hour), now.Add(7*time.Hour), models.OccurrenceScheduled, models.RegGoing)
	path := "/api/v1/groups/" + itoa(f.group.ID) + "/leave"
	previewPath := "/api/v1/groups/" + itoa(f.group.ID) + "/departure-preview"
	preview := getDeparturePreview(t, f.subjectC, previewPath, http.StatusOK)
	before := departureWriteCounts(t, f.app)
	for name, body := range map[string]map[string]any{
		"missing":      {},
		"tampered":     {"snapshot": preview.Snapshot + "x", "keepOccurrenceIds": []uint{}},
		"forged":       {"snapshot": preview.Snapshot, "keepOccurrenceIds": []uint{occ.ID + 9999}},
		"not-keepable": {"snapshot": preview.Snapshot, "keepOccurrenceIds": []uint{privateOcc.ID}},
	} {
		t.Run(name, func(t *testing.T) {
			w := f.subjectC.do("POST", path, body, true)
			if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), `"code":"departure_stale"`) {
				t.Fatalf("%s: %d %s", name, w.Code, w.Body.String())
			}
			if after := departureWriteCounts(t, f.app); !mapsEqualCounts(before, after) || membershipStatus(f.app, f.subject, f.group) != "active" || regStatus(t, f.app, f.subject.ID, occ.ID) != models.RegGoing {
				t.Fatalf("%s wrote domain state: before=%v after=%v", name, before, after)
			}
		})
	}
	wrongContextPath := "/api/v1/groups/" + itoa(f.group.ID) + "/members/" + itoa(f.subject.ID) + "/departure"
	if w := f.leaderC.do("POST", wrongContextPath, map[string]any{"snapshot": preview.Snapshot, "keepOccurrenceIds": []uint{}}, true); w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), `"code":"departure_stale"`) {
		t.Fatalf("wrong context: %d %s", w.Code, w.Body.String())
	}
	if after := departureWriteCounts(t, f.app); !mapsEqualCounts(before, after) || membershipStatus(f.app, f.subject, f.group) != "active" {
		t.Fatalf("wrong-context confirmation wrote: %v -> %v", before, after)
	}
	if err := f.app.DB.Model(&reg).Update("guest_count", 1).Error; err != nil {
		t.Fatal(err)
	}
	changedBaseline := departureWriteCounts(t, f.app)
	w := confirmDeparturePreview(t, f.subjectC, "POST", path, preview, nil, "")
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), `"code":"departure_stale"`) {
		t.Fatalf("changed facts: %d %s", w.Code, w.Body.String())
	}
	if after := departureWriteCounts(t, f.app); !mapsEqualCounts(changedBaseline, after) || membershipStatus(f.app, f.subject, f.group) != "active" {
		t.Fatalf("stale confirmation wrote: %v -> %v", changedBaseline, after)
	}
}

func TestDepartureConfirmationReleasesCapacityChildcareAndPromotes(t *testing.T) {
	f := newDepartureTestFixture(t)
	now := time.Now().UTC()
	occ, subjectReg := addDepartureRegistration(t, f, f.subject, "Promotion date", models.VisibilityPublic, nil, now.Add(5*time.Hour), now.Add(6*time.Hour), models.OccurrenceScheduled, models.RegGoing)
	capacity, childcareCapacity := 2, 1
	if err := f.app.DB.Model(&models.Event{}).Where("id = ?", occ.EventID).Updates(map[string]any{
		"capacity": capacity, "waitlist_enabled": true, "guests_allowed": 1,
		"childcare_available": true, "childcare_capacity": childcareCapacity,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.app.DB.Model(&subjectReg).Updates(map[string]any{"guest_count": 1, "childcare_count": 1}).Error; err != nil {
		t.Fatal(err)
	}
	waiting := newTestUser(t, f.app, "departure waiting", "password1234", models.RoleMember)
	waitedAt := now.Add(-time.Hour)
	waitReg := models.Registration{UserID: &waiting.ID, OccurrenceID: occ.ID, Status: models.RegWaitlisted, AttendanceMode: models.AttendanceInPerson,
		ChildcareCount: 1, WaitlistedAt: &waitedAt, Source: models.RegSourceWeb, CreatedAt: now, UpdatedAt: now}
	if err := f.app.DB.Create(&waitReg).Error; err != nil {
		t.Fatal(err)
	}
	cutoffOcc, _ := addDepartureRegistration(t, f, f.subject, "Cutoff date", models.VisibilityPublic, nil, now.Add(time.Hour), now.Add(2*time.Hour), models.OccurrenceScheduled, models.RegGoing)
	cutoffCapacity := 1
	if err := f.app.DB.Model(&models.Event{}).Where("id = ?", cutoffOcc.EventID).Updates(map[string]any{"capacity": cutoffCapacity, "waitlist_enabled": true}).Error; err != nil {
		t.Fatal(err)
	}
	cutoffWaiting := newTestUser(t, f.app, "departure cutoff waiting", "password1234", models.RoleMember)
	cutoffReg := models.Registration{UserID: &cutoffWaiting.ID, OccurrenceID: cutoffOcc.ID, Status: models.RegWaitlisted, AttendanceMode: models.AttendanceInPerson,
		WaitlistedAt: &waitedAt, Source: models.RegSourceWeb, CreatedAt: now, UpdatedAt: now}
	if err := f.app.DB.Create(&cutoffReg).Error; err != nil {
		t.Fatal(err)
	}
	preview := getDeparturePreview(t, f.subjectC, "/api/v1/groups/"+itoa(f.group.ID)+"/departure-preview", http.StatusOK)
	w := confirmDeparturePreview(t, f.subjectC, "POST", "/api/v1/groups/"+itoa(f.group.ID)+"/leave", preview, nil, "")
	if w.Code != http.StatusOK {
		t.Fatalf("confirm: %d %s", w.Code, w.Body.String())
	}
	if regStatus(t, f.app, f.subject.ID, occ.ID) != models.RegCancelled || regStatus(t, f.app, waiting.ID, occ.ID) != models.RegGoing {
		t.Fatalf("statuses subject=%s waiting=%s", regStatus(t, f.app, f.subject.ID, occ.ID), regStatus(t, f.app, waiting.ID, occ.ID))
	}
	if regStatus(t, f.app, f.subject.ID, cutoffOcc.ID) != models.RegCancelled || regStatus(t, f.app, cutoffWaiting.ID, cutoffOcc.ID) != models.RegWaitlisted {
		t.Fatalf("cutoff statuses subject=%s waiting=%s", regStatus(t, f.app, f.subject.ID, cutoffOcc.ID), regStatus(t, f.app, cutoffWaiting.ID, cutoffOcc.ID))
	}
	var cutoffNotices int64
	f.app.DB.Model(&models.Notification{}).Where("kind = ?", notify.WaitlistCutoff).Count(&cutoffNotices)
	if cutoffNotices == 0 {
		t.Fatal("departure release inside cutoff did not notify leaders")
	}
	var promoted models.Registration
	if err := f.app.DB.First(&promoted, waitReg.ID).Error; err != nil || promoted.PromotedAt == nil {
		t.Fatalf("promotion: %+v err=%v", promoted, err)
	}
}

func TestDepartureConfirmationDatabaseFailureRollsBack(t *testing.T) {
	f := newDepartureTestFixture(t)
	now := time.Now().UTC()
	keep, _ := addDepartureRegistration(t, f, f.subject, "Keep on rollback", models.VisibilityPublic, nil, now.Add(4*time.Hour), now.Add(5*time.Hour), models.OccurrenceScheduled, models.RegGoing)
	end, _ := addDepartureRegistration(t, f, f.subject, "End on rollback", models.VisibilityPrivate, nil, now.Add(5*time.Hour), now.Add(6*time.Hour), models.OccurrenceScheduled, models.RegGoing)
	previewPath := "/api/v1/groups/" + itoa(f.group.ID) + "/departure-preview"
	preview := getDeparturePreview(t, f.subjectC, previewPath, http.StatusOK)
	before := departureWriteCounts(t, f.app)
	name := "departure_membership_failure"
	if err := f.app.DB.Callback().Update().Before("gorm:update").Register(name, func(db *gorm.DB) {
		if db.Statement.Table == "memberships" {
			db.AddError(errors.New("forced departure membership failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	w := confirmDeparturePreview(t, f.subjectC, "POST", "/api/v1/groups/"+itoa(f.group.ID)+"/leave", preview, []uint{keep.ID}, "")
	f.app.DB.Callback().Update().Remove(name)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("failure response: %d %s", w.Code, w.Body.String())
	}
	if after := departureWriteCounts(t, f.app); !mapsEqualCounts(before, after) || membershipStatus(f.app, f.subject, f.group) != "active" ||
		regStatus(t, f.app, f.subject.ID, keep.ID) != models.RegGoing || regStatus(t, f.app, f.subject.ID, end.ID) != models.RegGoing {
		t.Fatalf("failed transaction did not roll back: before=%v after=%v", before, after)
	}
}

func TestDepartureConfirmationConcurrentNewRSVPStalesPreview(t *testing.T) {
	f := newDepartureTestFixture(t)
	now := time.Now().UTC()
	ev := models.Event{GroupID: &f.group.ID, Title: "Concurrent date", Visibility: models.VisibilityPublic,
		StartsLocal: now.Add(4 * time.Hour).Format("2006-01-02T15:04"), DurationMinutes: 60, TZID: "UTC", CreatedBy: f.leader.ID}
	if err := f.app.DB.Create(&ev).Error; err != nil {
		t.Fatal(err)
	}
	occ := models.EventOccurrence{EventID: ev.ID, OriginalDate: now.Add(4 * time.Hour).Format("2006-01-02"), StartsAt: now.Add(4 * time.Hour), EndsAt: now.Add(5 * time.Hour), Status: models.OccurrenceScheduled}
	if err := f.app.DB.Create(&occ).Error; err != nil {
		t.Fatal(err)
	}
	preview := getDeparturePreview(t, f.subjectC, "/api/v1/groups/"+itoa(f.group.ID)+"/departure-preview", http.StatusOK)
	if preview.AffectedCount != 0 {
		t.Fatalf("preview unexpectedly affected: %+v", preview)
	}
	blocker := f.app.DB.Begin()
	if blocker.Error != nil {
		t.Fatal(blocker.Error)
	}
	if _, err := lockOccurrence(blocker, occ.ID); err != nil {
		t.Fatal(err)
	}
	rsvpClient := cloneContactClient(f.subjectC)
	rsvp := queueContact(func() *httptest.ResponseRecorder {
		return rsvpClient.do("PUT", "/api/v1/events/"+itoa(ev.ID)+"/registrations", map[string]any{
			"date": occ.OriginalDate, "status": models.RegGoing, "attendanceMode": models.AttendanceInPerson,
		}, true)
	})
	waitOccurrenceDomainWaiter(t, f.app)
	departClient := cloneContactClient(f.subjectC)
	depart := queueContact(func() *httptest.ResponseRecorder {
		return confirmDeparturePreview(t, departClient, "POST", "/api/v1/groups/"+itoa(f.group.ID)+"/leave", preview, nil, "")
	})
	if err := blocker.Commit().Error; err != nil {
		t.Fatal(err)
	}
	if w := awaitContactResponse(t, rsvp); w.Code != http.StatusOK {
		t.Fatalf("rsvp: %d %s", w.Code, w.Body.String())
	}
	if w := awaitContactResponse(t, depart); w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), `"code":"departure_stale"`) {
		t.Fatalf("departure: %d %s", w.Code, w.Body.String())
	}
	if membershipStatus(f.app, f.subject, f.group) != "active" || regStatus(t, f.app, f.subject.ID, occ.ID) != models.RegGoing {
		t.Fatal("race lost the new registration or membership")
	}
}

func TestDepartureSelfLeaveCancelsRatherThanDeclines(t *testing.T) {
	f := newDepartureTestFixture(t)
	if err := f.app.DB.Model(f.group).Update("visibility", models.VisibilityPublic).Error; err != nil {
		t.Fatal(err)
	}
	start := time.Now().UTC().AddDate(0, 0, 60)
	occ, _ := addDepartureRegistration(t, f, f.subject, "Ended public answer", models.VisibilityPublic, nil, start, start.Add(time.Hour), models.OccurrenceScheduled, models.RegGoing)
	path := "/api/v1/groups/" + itoa(f.group.ID)
	preview := getDeparturePreview(t, f.subjectC, path+"/departure-preview", http.StatusOK)
	if w := confirmDeparturePreview(t, f.subjectC, "POST", path+"/leave", preview, nil, ""); w.Code != http.StatusOK {
		t.Fatalf("leave: %d %s", w.Code, w.Body.String())
	}
	if got := regStatus(t, f.app, f.subject.ID, occ.ID); got != models.RegCancelled {
		t.Fatalf("leave recorded %s instead of cancelling the answer", got)
	}
	w := f.subjectC.do("GET", "/api/v1/calendar?from="+start.AddDate(0, 0, -1).Format("2006-01-02")+"&to="+start.AddDate(0, 0, 1).Format("2006-01-02"), nil, false)
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), "Ended public answer") {
		t.Fatalf("cancelled answer bypassed discovery window: %d %s", w.Code, w.Body.String())
	}
	if countRegs(t, f.app, occ.ID, models.RegDeclined) != 0 {
		t.Fatal("leave counted as Can't go on leader roster")
	}
	// Public self-registration is allowed again, not manager-only Removed.
	if code, body := registerAs(f.subjectC, occ.EventID, occ.OriginalDate, models.RegGoing, nil); code != http.StatusOK {
		t.Fatalf("public self-RSVP after leave: %d %s", code, body)
	}
}

func TestDepartureLeftMigrationRepairsOnlyAuditedSelfLeaves(t *testing.T) {
	f := newDepartureTestFixture(t)
	managerRemoved := newTestUser(t, f.app, "historical manager removal", "password1234", models.RoleMember)
	unknown := newTestUser(t, f.app, "historical unknown departure", "password1234", models.RoleMember)
	for _, user := range []*models.User{managerRemoved, unknown} {
		addMember(t, f.app, user, f.group, models.MembershipMember)
	}
	selfAudit := func(user *models.User) {
		t.Helper()
		if err := audit.Write(f.app.DB, audit.Entry{Actor: &user.ID, Action: audit.RoleChanged,
			TargetType: "group", TargetID: f.group.ID, Meta: map[string]any{
				"userId": user.ID, "oldRole": "member", "newRole": "none", "keptCount": 0, "endedCount": 0,
			}}); err != nil {
			t.Fatal(err)
		}
	}
	selfAudit(f.subject)
	selfAudit(managerRemoved)
	if err := audit.Write(f.app.DB, audit.Entry{Actor: &f.leader.ID, Action: audit.MemberRemoved,
		TargetType: "group", TargetID: f.group.ID, Meta: map[string]any{"userId": managerRemoved.ID, "oldRole": "member"}}); err != nil {
		t.Fatal(err)
	}
	if err := f.app.DB.Model(&models.Membership{}).Where("group_id = ? AND user_id IN ?", f.group.ID,
		[]uint{f.subject.ID, managerRemoved.ID, unknown.ID}).Update("status", memberRemoved).Error; err != nil {
		t.Fatal(err)
	}
	var original models.Membership
	if err := f.app.DB.Where("user_id = ? AND group_id = ?", f.subject.ID, f.group.ID).First(&original).Error; err != nil {
		t.Fatal(err)
	}
	sqlDB, err := f.app.DB.DB()
	if err != nil {
		t.Fatal(err)
	}
	provider, err := migrations.NewProvider(sqlDB)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := provider.Up(context.Background()); err != nil {
			t.Errorf("restore latest migrations: %v", err)
		}
	})
	if _, err := provider.DownTo(context.Background(), 38); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(context.Background(), 39); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		user   *models.User
		status string
	}{{f.subject, models.MembershipLeft}, {managerRemoved, memberRemoved}, {unknown, memberRemoved}, {f.leader, "active"}} {
		if got := membershipStatus(f.app, tc.user, f.group); got != tc.status {
			t.Fatalf("user %d status=%q, want %q", tc.user.ID, got, tc.status)
		}
	}
	var corrected models.Membership
	if err := f.app.DB.First(&corrected, original.ID).Error; err != nil || corrected.Tenure != original.Tenure || !corrected.JoinedAt.Equal(original.JoinedAt) {
		t.Fatalf("migration changed membership identity/tenure: %+v err=%v", corrected, err)
	}
	var logs []models.AuditLog
	if err := f.app.DB.Where("action = ?", "membership.departure_corrected").Find(&logs).Error; err != nil || len(logs) != 1 || logs[0].ActorID != nil {
		t.Fatalf("correction audit=%+v err=%v", logs, err)
	}
	if _, err := provider.DownTo(context.Background(), 38); err == nil || !strings.Contains(err.Error(), "left memberships exist; cannot downgrade safely") {
		t.Fatalf("unsafe downgrade allowed: %v", err)
	}
	if version, err := provider.GetDBVersion(context.Background()); err != nil || version != 39 {
		t.Fatalf("version after refused downgrade=%d err=%v", version, err)
	}
}

func TestDepartureSelfRejoinKeepsNormalGroupRules(t *testing.T) {
	for _, tc := range []struct {
		name, visibility string
		removed, full    bool
		want             int
	}{
		{"public", models.VisibilityPublic, false, false, http.StatusOK},
		{"private", models.VisibilityPrivate, false, false, http.StatusOK},
		{"invite only", models.VisibilityInviteOnly, false, false, http.StatusNotFound},
		{"full", models.VisibilityPublic, false, true, http.StatusConflict},
		{"manager removal", models.VisibilityPublic, true, false, http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newDepartureTestFixture(t)
			if err := f.app.DB.Model(f.group).Update("visibility", tc.visibility).Error; err != nil {
				t.Fatal(err)
			}
			if tc.full {
				if err := f.app.DB.Model(f.group).Update("member_limit", 1).Error; err != nil {
					t.Fatal(err)
				}
			}
			var original models.Membership
			if err := f.app.DB.Where("user_id = ? AND group_id = ?", f.subject.ID, f.group.ID).First(&original).Error; err != nil {
				t.Fatal(err)
			}
			client := f.subjectC
			base := "/api/v1/groups/" + itoa(f.group.ID)
			previewPath, confirmPath := base+"/departure-preview", base+"/leave"
			wantStatus := models.MembershipLeft
			if tc.removed {
				client = f.leaderC
				previewPath = base + "/members/" + itoa(f.subject.ID) + "/departure-preview"
				confirmPath = base + "/members/" + itoa(f.subject.ID) + "/departure"
				wantStatus = memberRemoved
			}
			preview := getDeparturePreview(t, client, previewPath, http.StatusOK)
			if w := confirmDeparturePreview(t, client, "POST", confirmPath, preview, nil, ""); w.Code != http.StatusOK {
				t.Fatalf("departure: %d %s", w.Code, w.Body.String())
			}
			var left models.Membership
			if err := f.app.DB.First(&left, original.ID).Error; err != nil {
				t.Fatal(err)
			}
			rel := membershipRel(left)
			if left.Status != wantStatus || left.Tenure != original.Tenure || !left.JoinedAt.Equal(original.JoinedAt) ||
				rel.Member || rel.Leader || rel.Removed != tc.removed {
				t.Fatalf("departure facts: membership=%+v rel=%+v", left, rel)
			}
			w := f.subjectC.do("POST", base+"/join", nil, true)
			if w.Code != tc.want {
				t.Fatalf("rejoin: %d %s, want %d", w.Code, w.Body.String(), tc.want)
			}
			var after models.Membership
			if err := f.app.DB.First(&after, original.ID).Error; err != nil {
				t.Fatal(err)
			}
			if !after.JoinedAt.Equal(original.JoinedAt) {
				t.Fatal("rejoin changed first-ever join date")
			}
			if tc.want == http.StatusOK {
				if after.Status != "active" || after.Role != models.MembershipMember || after.Tenure != original.Tenure+1 {
					t.Fatalf("reactivation: %+v", after)
				}
			} else if after.Status != wantStatus || after.Tenure != original.Tenure {
				t.Fatalf("refused rejoin changed membership: %+v", after)
			}
		})
	}
}

func TestDepartureSelfLeaveRejoinRejectsOldSnapshotAndNotifiesAgain(t *testing.T) {
	f := newDepartureTestFixture(t)
	secondLeader := newTestUser(t, f.app, "departure repeat co-leader", "password1234", models.RoleMember)
	addMember(t, f.app, secondLeader, f.group, models.MembershipLeader)

	var original models.Membership
	if err := f.app.DB.Where("user_id = ? AND group_id = ?", f.subject.ID, f.group.ID).First(&original).Error; err != nil {
		t.Fatal(err)
	}
	previewPath := "/api/v1/groups/" + itoa(f.group.ID) + "/departure-preview"
	leavePath := "/api/v1/groups/" + itoa(f.group.ID) + "/leave"
	old := getDeparturePreview(t, f.subjectC, previewPath, http.StatusOK)
	if w := confirmDeparturePreview(t, f.subjectC, "POST", leavePath, old, nil, ""); w.Code != http.StatusOK {
		t.Fatalf("first leave: %d %s", w.Code, w.Body.String())
	}
	if w := f.subjectC.do("POST", "/api/v1/groups/"+itoa(f.group.ID)+"/join", nil, true); w.Code != http.StatusOK {
		t.Fatalf("self-rejoin: %d %s", w.Code, w.Body.String())
	}

	var reactivated models.Membership
	if err := f.app.DB.Where("user_id = ? AND group_id = ?", f.subject.ID, f.group.ID).First(&reactivated).Error; err != nil {
		t.Fatal(err)
	}
	if reactivated.ID != original.ID || !reactivated.JoinedAt.Equal(original.JoinedAt) || reactivated.Tenure != original.Tenure+1 {
		t.Fatalf("reactivation tenure/identity: before=%+v after=%+v", original, reactivated)
	}
	// The real provider may roll back newer migrations first, but migration 36
	// must preserve tenure 2 and its version before this old tenure-1 snapshot is
	// tested through the ordinary confirmation endpoint.
	sqlDB, err := f.app.DB.DB()
	if err != nil {
		t.Fatal(err)
	}
	provider, err := migrations.NewProvider(sqlDB)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := provider.Up(context.Background()); err != nil {
			t.Errorf("restore latest migrations: %v", err)
		}
	})
	if _, err := provider.DownTo(context.Background(), 35); err == nil || !strings.Contains(err.Error(), "advanced membership tenure exists; cannot downgrade safely") {
		t.Fatalf("attempted rollback did not preserve advanced tenure: %v", err)
	}
	version, err := provider.GetDBVersion(context.Background())
	if err != nil || version != 36 {
		t.Fatalf("version after refused tenure rollback=%d err=%v, want 36", version, err)
	}
	if err := f.app.DB.First(&reactivated, reactivated.ID).Error; err != nil || reactivated.Tenure != original.Tenure+1 {
		t.Fatalf("tenure after refused rollback=%d err=%v", reactivated.Tenure, err)
	}
	if _, err := provider.Up(context.Background()); err != nil {
		t.Fatal(err)
	}
	beforeReplay := departureWriteCounts(t, f.app)
	if w := confirmDeparturePreview(t, f.subjectC, "POST", leavePath, old, nil, ""); w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), `"code":"departure_stale"`) {
		t.Fatalf("old self-leave replay: %d %s", w.Code, w.Body.String())
	}
	if after := departureWriteCounts(t, f.app); !mapsEqualCounts(beforeReplay, after) || membershipStatus(f.app, f.subject, f.group) != "active" {
		t.Fatalf("old self-leave replay wrote state: before=%v after=%v", beforeReplay, after)
	}
	now := time.Now().UTC()
	addDepartureRegistration(t, f, f.subject, "Second-tenure self date", models.VisibilityPrivate, nil, now.Add(5*time.Hour), now.Add(6*time.Hour), models.OccurrenceScheduled, models.RegGoing)
	fresh := getDeparturePreview(t, f.subjectC, previewPath, http.StatusOK)
	if w := confirmDeparturePreview(t, f.subjectC, "POST", leavePath, fresh, nil, ""); w.Code != http.StatusOK {
		t.Fatalf("fresh second leave: %d %s", w.Code, w.Body.String())
	}
	for _, leaderID := range []uint{f.leader.ID, secondLeader.ID} {
		var n int64
		if err := f.app.DB.Model(&models.Notification{}).Where("kind = ? AND user_id = ?", notify.MembershipLeft, leaderID).Count(&n).Error; err != nil || n != 2 {
			t.Fatalf("leader %d leave notices=%d err=%v, want 2", leaderID, n, err)
		}
	}
	if got := auditCount(t, f.app, audit.RoleChanged); got != 2 {
		t.Fatalf("self-leave audits=%d, want 2", got)
	}
	var perDate int64
	f.app.DB.Model(&models.Notification{}).Where("kind = ?", notify.OccurrenceUpdated).Count(&perDate)
	if perDate != 0 {
		t.Fatalf("self departures emitted %d per-date notices", perDate)
	}
}

func TestDepartureRemovalReaddRejectsOldSnapshotAndNotifiesTeenAgain(t *testing.T) {
	f := newDepartureTestFixture(t)
	birthDate := time.Now().UTC().AddDate(-15, 0, 0)
	if err := f.app.DB.Model(f.subject).Updates(map[string]any{"account_type": models.AccountTeen, "birth_date": birthDate}).Error; err != nil {
		t.Fatal(err)
	}
	f.subject.AccountType, f.subject.BirthDate = models.AccountTeen, &birthDate
	guardian := newTestUser(t, f.app, "departure repeat guardian", "password1234", models.RoleMember)
	teenID := f.subject.ID
	if err := f.app.DB.Create(&models.GuardianLink{GuardianUserID: guardian.ID, TeenUserID: &teenID, Status: models.GuardianConfirmed}).Error; err != nil {
		t.Fatal(err)
	}
	admin := newTestUser(t, f.app, "departure repeat admin", "password1234", models.RoleAdmin)
	adminC := loginAPI(t, f.app, admin.Email, "password1234")
	previewPath := "/api/v1/groups/" + itoa(f.group.ID) + "/members/" + itoa(f.subject.ID) + "/departure-preview"
	removePath := "/api/v1/groups/" + itoa(f.group.ID) + "/members/" + itoa(f.subject.ID) + "/departure"

	old := getDeparturePreview(t, f.leaderC, previewPath, http.StatusOK)
	if w := confirmDeparturePreview(t, f.leaderC, "POST", removePath, old, nil, "first"); w.Code != http.StatusOK {
		t.Fatalf("first removal: %d %s", w.Code, w.Body.String())
	}
	readdDepartureTeen(t, f, adminC)
	beforeReplay := departureWriteCounts(t, f.app)
	if w := confirmDeparturePreview(t, f.leaderC, "POST", removePath, old, nil, "replay"); w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), `"code":"departure_stale"`) {
		t.Fatalf("old removal replay: %d %s", w.Code, w.Body.String())
	}
	if after := departureWriteCounts(t, f.app); !mapsEqualCounts(beforeReplay, after) || membershipStatus(f.app, f.subject, f.group) != "active" {
		t.Fatalf("old removal replay wrote state: before=%v after=%v", beforeReplay, after)
	}
	now := time.Now().UTC()
	addDepartureRegistration(t, f, f.subject, "Second-tenure removal date", models.VisibilityPrivate, nil, now.Add(5*time.Hour), now.Add(6*time.Hour), models.OccurrenceScheduled, models.RegGoing)
	fresh := getDeparturePreview(t, f.leaderC, previewPath, http.StatusOK)
	if w := confirmDeparturePreview(t, f.leaderC, "POST", removePath, fresh, nil, "second"); w.Code != http.StatusOK {
		t.Fatalf("fresh second removal: %d %s", w.Code, w.Body.String())
	}
	for _, recipient := range []uint{f.subject.ID, guardian.ID} {
		var n int64
		if err := f.app.DB.Model(&models.Notification{}).Where("kind = ? AND user_id = ?", notify.MembershipDeparture, recipient).Count(&n).Error; err != nil || n != 2 {
			t.Fatalf("recipient %d departure notices=%d err=%v, want 2", recipient, n, err)
		}
	}
	if got := auditCount(t, f.app, audit.MemberRemoved); got != 2 {
		t.Fatalf("removal audits=%d, want 2", got)
	}
	var perDate int64
	f.app.DB.Model(&models.Notification{}).Where("kind = ?", notify.OccurrenceUpdated).Count(&perDate)
	if perDate != 0 {
		t.Fatalf("removals emitted %d per-date notices", perDate)
	}
}

func TestDepartureDisabledSubjectPreviewEndsRegistrationsWithoutRetainedAccess(t *testing.T) {
	f := newDepartureTestFixture(t)
	admin := newTestUser(t, f.app, "departure disable admin", "password1234", models.RoleAdmin)
	adminC := loginAPI(t, f.app, admin.Email, "password1234")
	now := time.Now().UTC()
	cutoff, cutoffReg := addDepartureRegistration(t, f, f.subject, "Disabled cutoff", models.VisibilityPrivate, nil, now.Add(time.Hour), now.Add(2*time.Hour), models.OccurrenceScheduled, models.RegGoing)
	future, futureReg := addDepartureRegistration(t, f, f.subject, "Disabled future", models.VisibilityPrivate, nil, now.Add(5*time.Hour), now.Add(6*time.Hour), models.OccurrenceScheduled, models.RegGoing)
	for _, occurrenceID := range []uint{cutoff.ID, future.ID} {
		if err := f.app.DB.Model(&models.Event{}).Where("id = (SELECT event_id FROM event_occurrences WHERE id = ?)", occurrenceID).
			Updates(map[string]any{"capacity": 1, "waitlist_enabled": true}).Error; err != nil {
			t.Fatal(err)
		}
	}
	cutoffWaiter := newTestUser(t, f.app, "disabled cutoff waiter", "password1234", models.RoleMember)
	futureWaiter := newTestUser(t, f.app, "disabled future waiter", "password1234", models.RoleMember)
	waitedAt := now.Add(-time.Hour)
	for occurrenceID, userID := range map[uint]uint{cutoff.ID: cutoffWaiter.ID, future.ID: futureWaiter.ID} {
		uid := userID
		if err := f.app.DB.Create(&models.Registration{OccurrenceID: occurrenceID, UserID: &uid, Status: models.RegWaitlisted,
			AttendanceMode: models.AttendanceInPerson, WaitlistedAt: &waitedAt, Source: models.RegSourceWeb}).Error; err != nil {
			t.Fatal(err)
		}
	}
	previewPath := "/api/v1/groups/" + itoa(f.group.ID) + "/members/" + itoa(f.subject.ID) + "/departure-preview"
	removePath := "/api/v1/groups/" + itoa(f.group.ID) + "/members/" + itoa(f.subject.ID) + "/departure"
	activePreview := getDeparturePreview(t, adminC, previewPath, http.StatusOK)
	if activePreview.AffectedCount != 2 || activePreview.KeepableCount != 1 || activePreview.DefaultKeepCount != 1 {
		t.Fatalf("active preview=%+v", activePreview)
	}
	apiDo(t, adminC, "PUT", "/api/v1/admin/users/"+itoa(f.subject.ID), map[string]any{"status": models.UserDisabled}, http.StatusOK)
	if got := regStatus(t, f.app, futureWaiter.ID, future.ID); got != models.RegGoing {
		t.Fatalf("future waiter=%q, want promoted going", got)
	}
	if got := regStatus(t, f.app, cutoffWaiter.ID, cutoff.ID); got != models.RegWaitlisted {
		t.Fatalf("cutoff waiter=%q, want waitlisted", got)
	}
	afterDisable := departureWriteCounts(t, f.app)
	if w := confirmDeparturePreview(t, adminC, "POST", removePath, activePreview, []uint{cutoff.ID}, ""); w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), `"code":"departure_stale"`) {
		t.Fatalf("pre-disable preview confirmation: %d %s", w.Code, w.Body.String())
	}
	if after := departureWriteCounts(t, f.app); !mapsEqualCounts(afterDisable, after) || membershipStatus(f.app, f.subject, f.group) != "active" {
		t.Fatalf("stale disabled confirmation wrote state: before=%v after=%v", afterDisable, after)
	}
	disabledPreview := getDeparturePreview(t, adminC, previewPath, http.StatusOK)
	if disabledPreview.AffectedCount != 2 || disabledPreview.KeepableCount != 0 || disabledPreview.DefaultKeepCount != 0 || len(disabledPreview.Dates) != 0 {
		t.Fatalf("disabled preview offered retained authority: %+v", disabledPreview)
	}
	w := confirmDeparturePreview(t, adminC, "POST", removePath, disabledPreview, nil, "cleanup")
	if w.Code != http.StatusOK {
		t.Fatalf("disabled removal: %d %s", w.Code, w.Body.String())
	}
	var result struct {
		Departure departureConfirmationJSON `json:"departure"`
	}
	decodeJSON(t, w, &result)
	if result.Departure.KeptCount != 0 || result.Departure.EndedCount != 2 {
		t.Fatalf("disabled removal result=%+v", result.Departure)
	}
	if membershipStatus(f.app, f.subject, f.group) != memberRemoved || regStatus(t, f.app, f.subject.ID, cutoff.ID) != models.RegRemoved || regStatus(t, f.app, f.subject.ID, future.ID) != models.RegRemoved {
		t.Fatal("disabled cleanup did not remove membership and registrations")
	}
	var activeAccess int64
	if err := f.app.DB.Model(&models.OccurrenceAccess{}).Where("subject_user_id = ? AND status = ?", f.subject.ID, policy.OccurrenceAccessActive).Count(&activeAccess).Error; err != nil || activeAccess != 0 {
		t.Fatalf("disabled subject active access=%d err=%v", activeAccess, err)
	}
	var savedCutoff, savedFuture models.Registration
	f.app.DB.First(&savedCutoff, cutoffReg.ID)
	f.app.DB.First(&savedFuture, futureReg.ID)
	if savedCutoff.Status != models.RegRemoved || savedFuture.Status != models.RegRemoved {
		t.Fatalf("disabled registrations: cutoff=%s future=%s", savedCutoff.Status, savedFuture.Status)
	}
}

func TestDepartureConfirmationDoubleSubmitAndCSRF(t *testing.T) {
	f := newDepartureTestFixture(t)
	preview := getDeparturePreview(t, f.subjectC, "/api/v1/groups/"+itoa(f.group.ID)+"/departure-preview", http.StatusOK)
	path := "/api/v1/groups/" + itoa(f.group.ID) + "/leave"
	body := map[string]any{"snapshot": preview.Snapshot, "keepOccurrenceIds": []uint{}}
	if w := f.subjectC.do("POST", path, body, false); w.Code != http.StatusForbidden {
		t.Fatalf("missing CSRF: %d %s", w.Code, w.Body.String())
	}
	if w := f.subjectC.do("POST", path, body, true); w.Code != http.StatusOK {
		t.Fatalf("first: %d %s", w.Code, w.Body.String())
	}
	before := departureWriteCounts(t, f.app)
	if w := f.subjectC.do("POST", path, body, true); w.Code != http.StatusConflict {
		t.Fatalf("second: %d %s", w.Code, w.Body.String())
	}
	if after := departureWriteCounts(t, f.app); !mapsEqualCounts(before, after) {
		t.Fatalf("double submit wrote: %v -> %v", before, after)
	}
}

func TestDepartureSnapshotClockBoundariesOnly(t *testing.T) {
	f := newDepartureTestFixture(t)
	now := time.Now().UTC().Truncate(time.Second)
	start, end := now.Add(3*time.Hour), now.Add(4*time.Hour)
	addDepartureRegistration(t, f, f.subject, "Clock date", models.VisibilityPublic, nil, start, end, models.OccurrenceScheduled, models.RegGoing)
	before := loadDepartureFactsForTest(t, f, f.subject, f.subject, now)
	token, err := f.app.newDepartureSnapshot(before)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := f.app.parseDepartureSnapshot(token, f.subject.ID, f.subject.ID, f.group.ID)
	if err != nil {
		t.Fatal(err)
	}
	if later := loadDepartureFactsForTest(t, f, f.subject, f.subject, now.Add(time.Minute)); !departureSnapshotMatches(snapshot, later) {
		t.Fatal("ordinary wall-clock advance staled snapshot before any boundary")
	}
	if cutoff := loadDepartureFactsForTest(t, f, f.subject, f.subject, start.Add(-2*time.Hour)); departureSnapshotMatches(snapshot, cutoff) {
		t.Fatal("crossing the keepability cutoff did not stale snapshot")
	}
	ended, err := func() (departureFacts, error) {
		var facts departureFacts
		err := f.app.DB.Transaction(func(tx *gorm.DB) error {
			var loadErr error
			facts, loadErr = loadDepartureFacts(tx, f.subject, f.group.ID, f.subject.ID, end)
			return loadErr
		}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
		return facts, err
	}()
	if err != nil && !errors.Is(err, errDepartureUnavailable) {
		t.Fatal(err)
	}
	if err == nil && departureSnapshotMatches(snapshot, ended) {
		t.Fatal("crossing the date end did not stale snapshot")
	}
}

func TestDepartureLeaderMayRemoveDisabledAdminMember(t *testing.T) {
	f := newDepartureTestFixture(t)
	disabledAdmin := newTestUser(t, f.app, "departure disabled admin", "password1234", models.RoleAdmin)
	addMember(t, f.app, disabledAdmin, f.group, models.MembershipMember)
	if err := f.app.DB.Model(disabledAdmin).Update("status", models.UserDisabled).Error; err != nil {
		t.Fatal(err)
	}
	previewPath := "/api/v1/groups/" + itoa(f.group.ID) + "/members/" + itoa(disabledAdmin.ID) + "/departure-preview"
	removePath := "/api/v1/groups/" + itoa(f.group.ID) + "/members/" + itoa(disabledAdmin.ID) + "/departure"
	// A disabled admin has no admin powers (asPerson), so a leader may remove
	// them, and the confirm step must agree with the preview.
	preview := getDeparturePreview(t, f.leaderC, previewPath, http.StatusOK)
	if w := confirmDeparturePreview(t, f.leaderC, "POST", removePath, preview, nil, ""); w.Code != http.StatusOK {
		t.Fatalf("leader confirming removal of a disabled admin: %d %s", w.Code, w.Body.String())
	}
	if got := membershipStatus(f.app, disabledAdmin, f.group); got != memberRemoved {
		t.Fatalf("membership status=%q, want removed", got)
	}
}
