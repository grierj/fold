package main

import (
	"sync"
	"testing"
	"time"

	"fold/internal/audit"
	"fold/internal/notify"
	"fold/models"
)

func rolloverDate(t *testing.T, date string) time.Time {
	t.Helper()
	got, err := time.Parse("2006-01-02", date)
	if err != nil {
		t.Fatal(err)
	}
	return got
}
func rolloverInstant(t *testing.T, instant string) time.Time {
	t.Helper()
	got, err := time.Parse(time.RFC3339, instant)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func rolloverTeenUser(t *testing.T, app *App, name, birth, status string, suspended, password bool) *models.User {
	t.Helper()
	b := rolloverDate(t, birth)
	u := &models.User{Email: name + "@example.com", Name: name, AccountType: models.AccountTeen, BirthDate: &b, Role: models.RoleMember, Status: status, AdministrativelyDisabled: suspended}
	if password {
		if err := u.SetPassword("password1234"); err != nil {
			t.Fatal(err)
		}
	}
	if err := app.DB.Create(u).Error; err != nil {
		t.Fatal(err)
	}
	return u
}

func TestAgeRolloverStatusesAndConsentHistory(t *testing.T) {
	app := testApp(t)
	admin := newTestUser(t, app, "admin", "pw", models.RoleAdmin)
	now := rolloverInstant(t, "2026-10-22T12:00:00Z")
	var users []*models.User
	for _, tc := range []struct {
		name, status, want  string
		suspended, password bool
	}{
		{"active", models.UserActive, models.UserActive, false, true},
		{"pending", models.UserPendingConsent, models.UserActive, false, true},
		{"revoked", models.UserDisabled, models.UserActive, false, true},
		{"suspended", models.UserDisabled, models.UserDisabled, true, true},
		{"no-password", models.UserPendingConsent, models.UserInvited, false, false},
	} {
		u := rolloverTeenUser(t, app, tc.name, "2008-10-22", tc.status, tc.suspended, tc.password)
		users = append(users, u)
		if err := app.DB.Create(&models.TeenConsent{TeenUserID: u.ID, Scope: "account", Status: models.ConsentRevoked, GrantedBy: admin.ID, GranterRole: models.ConsentAdmin}).Error; err != nil {
			t.Fatal(err)
		}
		_ = tc.want
	}
	if n, err := app.rolloverAges(now); err != nil || n != 5 {
		t.Fatalf("rollover %d %v", n, err)
	}
	for i, want := range []string{models.UserActive, models.UserActive, models.UserActive, models.UserDisabled, models.UserInvited} {
		var got models.User
		app.DB.First(&got, users[i].ID)
		if got.AccountType != models.AccountAdult || got.Status != want || got.AdministrativelyDisabled != users[i].AdministrativelyDisabled {
			t.Fatalf("rollover %s: %+v", users[i].Name, got)
		}
	}
	if n, err := app.rolloverAges(now.Add(24 * time.Hour)); err != nil || n != 0 {
		t.Fatalf("rerun %d %v", n, err)
	}
	var count int64
	app.DB.Model(&models.TeenConsent{}).Count(&count)
	if count != 5 {
		t.Fatal("rollover deleted consent history")
	}
	rows, _ := auditRows(t, app, audit.UserTurned18)
	if len(rows) != 5 {
		t.Fatalf("rollover audit count %d", len(rows))
	}
	for _, row := range rows {
		if row.ActorID != nil {
			t.Fatal("system rollover attributed to a person")
		}
	}
}

func TestAgeRolloverChurchMidnightAndLeapBirthday(t *testing.T) {
	app := testApp(t)
	if err := app.DB.Create(&models.Setting{Key: settingTimezone, Value: "America/Los_Angeles"}).Error; err != nil {
		t.Fatal(err)
	}
	u := rolloverTeenUser(t, app, "midnight", "2008-10-22", models.UserPendingConsent, false, true)
	if n, err := app.rolloverAges(rolloverInstant(t, "2026-10-22T06:59:59Z")); err != nil || n != 0 {
		t.Fatalf("before local birthday: %d %v", n, err)
	}
	if n, err := app.rolloverAges(rolloverInstant(t, "2026-10-22T07:00:00Z")); err != nil || n != 1 {
		t.Fatalf("at local birthday: %d %v", n, err)
	}
	app.DB.First(u, u.ID)
	if u.AccountType != models.AccountAdult {
		t.Fatal("birthday did not promote teen")
	}
	leap := rolloverTeenUser(t, app, "leap", "2008-02-29", models.UserActive, false, true)
	if n, err := app.rolloverAges(rolloverInstant(t, "2026-02-28T20:00:00Z")); err != nil || n != 0 {
		t.Fatalf("leap birthday too early: %d %v", n, err)
	}
	if n, err := app.rolloverAges(rolloverInstant(t, "2026-03-01T08:00:00Z")); err != nil || n != 1 {
		t.Fatalf("leap birthday: %d %v", n, err)
	}
	app.DB.First(leap, leap.ID)
	if leap.AccountType != models.AccountAdult {
		t.Fatal("March1 leap birthday did not roll over")
	}
}

func TestNextAgeRolloverUsesLocalMidnightAcrossDST(t *testing.T) {
	zone, err := time.LoadLocation("America/Boise")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ now, want string }{
		{"2026-03-08T07:00:00Z", "2026-03-09T06:00:00Z"},
		{"2026-11-01T06:00:00Z", "2026-11-02T07:00:00Z"},
	} {
		if got := nextAgeRollover(rolloverInstant(t, tc.now), zone); !got.Equal(rolloverInstant(t, tc.want)) {
			t.Fatalf("next midnight after %s: %s", tc.now, got)
		}
	}
}

func TestAgeRolloverFullPlacesAreNotRestored(t *testing.T) {
	app := testApp(t)
	admin, gid := openGroup(t, app, "Birthday seats")
	eventID, _ := seedCappedEvent(t, app, admin, gid, intPtr(1), true)
	var event models.Event
	app.DB.First(&event, eventID)
	if err := app.DB.Model(&models.Group{}).Where("id = ?", event.GroupID).Update("member_limit", 1).Error; err != nil {
		t.Fatal(err)
	}
	teen := rolloverTeenUser(t, app, "full-birthday", "2008-10-22", models.UserDisabled, false, true)
	var occurrence models.EventOccurrence
	app.DB.Where("event_id = ?", eventID).First(&occurrence)
	m := models.Membership{GroupID: *event.GroupID, UserID: teen.ID, Status: "active", Role: models.MembershipMember, JoinedAt: time.Now()}
	if err := app.DB.Create(&m).Error; err != nil {
		t.Fatal(err)
	}
	reg := models.Registration{OccurrenceID: occurrence.ID, UserID: &teen.ID, Status: models.RegGoing, AttendanceMode: models.AttendanceInPerson}
	if err := app.DB.Create(&reg).Error; err != nil {
		t.Fatal(err)
	}
	other := newTestUser(t, app, "current-attendee", "pw", models.RoleMember)
	if err := app.DB.Create(&models.Registration{OccurrenceID: occurrence.ID, UserID: &other.ID, Status: models.RegGoing, AttendanceMode: models.AttendanceInPerson}).Error; err != nil {
		t.Fatal(err)
	}
	if n, err := app.rolloverAges(rolloverInstant(t, "2026-10-22T12:00:00Z")); err != nil || n != 1 {
		t.Fatalf("rollover %d %v", n, err)
	}
	var count int64
	app.DB.Model(&models.Registration{}).Where("id = ?", reg.ID).Count(&count)
	if count != 0 {
		t.Fatal("birthday restoration overfilled event")
	}
	app.DB.Model(&models.Membership{}).Where("id = ?", m.ID).Count(&count)
	if count != 0 {
		t.Fatal("birthday restoration overfilled group")
	}
	for _, kind := range []string{notify.MemberNotRestored, notify.RegistrationNotRestored} {
		var notice models.Notification
		if err := app.DB.Where("user_id = ? AND kind = ?", teen.ID, kind).First(&notice).Error; err != nil {
			t.Fatal(err)
		}
		if notice.ActorID != nil {
			t.Fatal("system restore notice attributed to a person")
		}
	}
}

func TestAgeRolloverConcurrentRunsWriteOnce(t *testing.T) {
	app := testApp(t)
	u := rolloverTeenUser(t, app, "concurrent-birthday", "2008-10-22", models.UserActive, false, true)
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for range 2 {
		wg.Go(func() { _, err := app.rolloverAges(rolloverInstant(t, "2026-10-22T12:00:00Z")); errs <- err })
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var count int64
	app.DB.Model(&models.AuditLog{}).Where("action = ? AND target_id = ?", audit.UserTurned18, u.ID).Count(&count)
	if count != 1 {
		t.Fatalf("concurrent audit count %d", count)
	}
	app.DB.Model(&models.Notification{}).Where("kind = ? AND user_id = ?", notify.AccountTurned18, u.ID).Count(&count)
	if count != 1 {
		t.Fatalf("concurrent notice count %d", count)
	}
}
