package main

import (
	"fmt"
	"strings"
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
	cases := []struct {
		name, status, want  string
		suspended, password bool
	}{
		{"active", models.UserActive, models.UserActive, false, true},
		{"pending", models.UserPendingConsent, models.UserActive, false, true},
		{"revoked", models.UserDisabled, models.UserActive, false, true},
		{"suspended", models.UserDisabled, models.UserDisabled, true, true},
		{"no-password", models.UserPendingConsent, models.UserInvited, false, false},
	}
	for _, tc := range cases {
		u := rolloverTeenUser(t, app, tc.name, "2008-10-22", tc.status, tc.suspended, tc.password)
		users = append(users, u)
		if err := app.DB.Create(&models.TeenConsent{TeenUserID: u.ID, Scope: "account", Status: models.ConsentRevoked, GrantedBy: admin.ID, GranterRole: models.ConsentAdmin}).Error; err != nil {
			t.Fatal(err)
		}
	}
	if n, err := app.rolloverAges(now); err != nil || n != 5 {
		t.Fatalf("rollover %d %v", n, err)
	}
	for i, tc := range cases {
		var got models.User
		app.DB.First(&got, users[i].ID)
		if got.AccountType != models.AccountAdult || got.Status != tc.want || got.AdministrativelyDisabled != users[i].AdministrativelyDisabled {
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

func TestAgeRolloverChild13NoticeIsDurableAndKeepsRegistration(t *testing.T) {
	app := testApp(t)
	admin, gid := openGroup(t, app, "Household birthday")
	eventID, date := seedCappedEvent(t, app, admin, gid, intPtr(5), true)
	holder := newTestUser(t, app, "birthday-parent", "pw", models.RoleMember)
	parent := loginAPI(t, app, "birthday-parent", "pw")
	person := addMyPerson(t, parent, map[string]any{"firstName": "Birthday Child", "kind": "child", "birthDate": "2013-10-22"})
	apiDo(t, admin, "POST", "/api/v1/admin/guardian-links/"+itoa2(*person.GuardianLinkID)+"/confirm", map[string]string{"checkNote": "Known family"}, 200)
	path := "/api/v1/events/" + itoa2(eventID) + "/registrations"
	apiDo(t, parent, "PUT", path, map[string]any{"date": date, "people": []map[string]any{{"personId": person.ID, "status": "going"}}}, 200)
	if n, err := app.rolloverAges(rolloverInstant(t, "2026-10-21T12:00:00Z")); err != nil || n != 0 {
		t.Fatalf("early child notice %d %v", n, err)
	}
	if n, err := app.rolloverAges(rolloverInstant(t, "2026-10-22T12:00:00Z")); err != nil || n != 1 {
		t.Fatalf("child birthday %d %v", n, err)
	}
	var notice models.Notification
	if err := app.DB.Where("kind = ? AND user_id = ?", notify.HouseholdChildTurned13, holder.ID).First(&notice).Error; err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(notice.Body, "age group") || strings.Contains(notice.EmailBody, person.Name) || notice.ActorID != nil {
		t.Fatalf("child notice %+v", notice)
	}
	var saved models.HouseholdPerson
	app.DB.First(&saved, person.ID)
	if saved.Kind != models.HouseholdChild || saved.ConvertedUserID != nil {
		t.Fatal("child13 notice changed identity or created a login")
	}
	apiDo(t, parent, "PUT", path, map[string]any{"date": date, "people": []map[string]any{{"personId": person.ID, "status": "going"}}}, 200)
	// Retention can delete bell rows. The permanent audit prevents a new
	// notice every day after that deletion.
	if err := app.DB.Delete(&notice).Error; err != nil {
		t.Fatal(err)
	}
	if n, err := app.rolloverAges(rolloverInstant(t, "2026-10-23T12:00:00Z")); err != nil || n != 0 {
		t.Fatalf("repeat notice %d %v", n, err)
	}
	var count int64
	app.DB.Model(&models.Notification{}).Where("kind = ?", notify.HouseholdChildTurned13).Count(&count)
	if count != 0 {
		t.Fatal("deleted notice was recreated")
	}
}

func TestAgeRolloverChildCatchUpNeverSuggestsWrongAgeGroup(t *testing.T) {
	app := testApp(t)
	parent := newTestUser(t, app, "catch-up-parent", "pw", models.RoleMember)
	household := models.Household{Name: "Catch-up family", CreatedBy: parent.ID}
	if err := app.DB.Create(&household).Error; err != nil {
		t.Fatal(err)
	}
	birth := rolloverDate(t, "2005-10-22")
	person := models.HouseholdPerson{HouseholdID: household.ID, FirstName: "Old child row", Kind: models.HouseholdChild, BirthDate: &birth, CreatedBy: parent.ID}
	if err := app.DB.Create(&person).Error; err != nil {
		t.Fatal(err)
	}
	link := models.GuardianLink{GuardianUserID: parent.ID, PersonID: &person.ID, Status: models.GuardianPending}
	if err := app.DB.Create(&link).Error; err != nil {
		t.Fatal(err)
	}
	if n, err := app.rolloverAges(rolloverInstant(t, "2026-10-22T12:00:00Z")); err != nil || n != 0 {
		t.Fatalf("unconfirmed guardian got notice %d %v", n, err)
	}
	if err := app.DB.Model(&link).Update("status", models.GuardianConfirmed).Error; err != nil {
		t.Fatal(err)
	}
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
	var notices []models.Notification
	app.DB.Where("kind = ?", notify.HouseholdChildTurned13).Find(&notices)
	if len(notices) != 1 || notices[0].UserID != parent.ID || strings.Contains(notices[0].Body, "type to Teen") {
		t.Fatalf("catch-up notice %+v", notices)
	}
	var count int64
	app.DB.Model(&models.AuditLog{}).Where("action = ? AND target_id = ?", audit.HouseholdChildTurned13, person.ID).Count(&count)
	if count != 1 {
		t.Fatalf("concurrent milestone audit %d", count)
	}
}

func TestAgeRolloverPromotesHouseholdRelationWithoutGuardianship(t *testing.T) {
	app := testApp(t)
	user := rolloverTeenUser(t, app, "household-adult", "2008-10-22", models.UserActive, false, true)
	household := models.Household{Name: "Family", CreatedBy: user.ID}
	if err := app.DB.Create(&household).Error; err != nil {
		t.Fatal(err)
	}
	account := models.HouseholdAccount{HouseholdID: household.ID, UserID: user.ID, Relation: models.HouseholdRelationTeen}
	if err := app.DB.Create(&account).Error; err != nil {
		t.Fatal(err)
	}
	client := loginAPI(t, app, "household-adult", "password1234")
	apiDo(t, client, "PUT", "/api/v1/me/household", map[string]string{"name": "Renamed"}, 403)
	if n, err := app.rolloverAges(rolloverInstant(t, "2026-10-22T12:00:00Z")); err != nil || n != 1 {
		t.Fatalf("rollover %d %v", n, err)
	}
	apiDo(t, client, "PUT", "/api/v1/me/household", map[string]string{"name": "Renamed"}, 200)
	app.DB.First(&account, account.ID)
	if account.Relation != models.HouseholdRelationAdult {
		t.Fatalf("household relation %s", account.Relation)
	}
	child := addMyPerson(t, client, map[string]any{"firstName": "New child", "kind": "child", "birthDate": "2020-01-01"})
	if child.LinkStatus != models.GuardianPending {
		t.Fatal("birthday granted automatic guardian authority")
	}
	var count int64
	app.DB.Model(&models.GuardianLink{}).Where("guardian_user_id = ? AND status = ?", user.ID, models.GuardianConfirmed).Count(&count)
	if count != 0 {
		t.Fatal("birthday auto-confirmed a guardian link")
	}
	if auditCount(t, app, audit.HouseholdAccountTurnedAdult) != 1 {
		t.Fatal("missing household relation rollover audit")
	}
}

func setStagedBirthdayToday(t *testing.T, app *App, f teenConversionFixture) time.Time {
	t.Helper()
	today := app.householdClock(time.Now().UTC())
	birth := today.AddDate(-18, 0, 0).Format("2006-01-02")
	for _, entry := range []struct {
		model any
		id    uint
	}{{&models.User{}, f.user.ID}, {&models.HouseholdPerson{}, f.person.ID}, {&models.AccountRequest{}, f.request.ID}} {
		if err := app.DB.Model(entry.model).Where("id = ?", entry.id).Update("birth_date", birth).Error; err != nil {
			t.Fatal(err)
		}
	}
	return today
}

func TestAgeRolloverSuspendedStageKeepsHouseholdUntilEnable(t *testing.T) {
	app := testApp(t)
	admin, gid := openGroup(t, app, "Suspended birthday")
	f := pendingTeenConversion(t, app, admin, gid, "birthday-stage", true)
	apiDo(t, admin, "PUT", "/api/v1/admin/users/"+itoa2(f.user.ID), map[string]string{"status": "disabled"}, 200)
	now := setStagedBirthdayToday(t, app, f)
	if n, err := app.rolloverAges(now); err != nil || n != 1 {
		t.Fatalf("suspended stage rollover %d %v", n, err)
	}
	f.unchanged(t, app)
	var person models.HouseholdPerson
	app.DB.First(&person, f.person.ID)
	app.DB.First(&f.user, f.user.ID)
	if person.Kind != models.HouseholdAdult || f.user.AccountType != models.AccountAdult || f.user.Status != models.UserDisabled || !f.user.AdministrativelyDisabled {
		t.Fatalf("suspended adulthood: %+v %+v", person, f.user)
	}
	if loginAPIExpectFail(t, app, "birthday-stage-own", "password1234") {
		t.Fatal("suspended adult signed in")
	}
	var occurrence models.EventOccurrence
	app.DB.First(&occurrence, f.rows[0].OccurrenceID)
	var event models.Event
	app.DB.First(&event, occurrence.EventID)
	apiDo(t, f.parent, "PUT", "/api/v1/events/"+itoa2(event.ID)+"/registrations", map[string]any{"date": occurrence.OriginalDate, "people": []map[string]any{{"personId": person.ID, "status": "going"}}}, 200)
	apiDo(t, admin, "PUT", "/api/v1/admin/users/"+itoa2(f.user.ID), map[string]string{"status": "active"}, 200)
	app.DB.First(&person, person.ID)
	if person.ConvertedUserID == nil || *person.ConvertedUserID != f.user.ID {
		t.Fatal("admin Enable did not finish adult transfer")
	}
	for _, before := range f.rows {
		var after models.Registration
		app.DB.First(&after, before.ID)
		if after.UserID == nil || *after.UserID != f.user.ID || after.HouseholdPersonID != nil || after.Status != before.Status {
			t.Fatalf("birthday transfer changed registration %d: %+v", before.ID, after)
		}
	}
	if n, err := app.rolloverAges(now); err != nil || n != 0 {
		t.Fatalf("repeat birthday %d %v", n, err)
	}
	apiDo(t, admin, "PUT", "/api/v1/admin/users/"+itoa2(f.user.ID), map[string]string{"status": "active"}, 200)
	if auditCount(t, app, audit.HouseholdPersonConverted) != 1 {
		t.Fatal("adult transferred more than once")
	}
}

func TestAgeRolloverUnavailableStageDoesNotBlockOtherBirthdays(t *testing.T) {
	app := testApp(t)
	admin, gid := openGroup(t, app, "Blocked birthday")
	f := pendingTeenConversion(t, app, admin, gid, "blocked-birthday", true)
	now := setStagedBirthdayToday(t, app, f)
	apiDo(t, admin, "PUT", "/api/v1/admin/users/"+itoa2(f.holder.ID), map[string]string{"status": "disabled"}, 200)
	other := rolloverTeenUser(t, app, "later-birthday", now.AddDate(-18, 0, 0).Format("2006-01-02"), models.UserActive, false, true)
	n, err := app.rolloverAges(now)
	if n != 1 || err == nil || !strings.Contains(err.Error(), fmt.Sprintf("user %d:", f.user.ID)) {
		t.Fatalf("isolated blocked stage: %d %v", n, err)
	}
	f.unchanged(t, app)
	var pendingLink models.GuardianLink
	app.DB.First(&pendingLink, f.link.ID)
	if pendingLink.Status != models.GuardianPending {
		t.Fatal("blocked birthday closed guardian history")
	}
	var pendingTask models.Notification
	if err := app.DB.Where("kind = ? AND subject_id = ?", notify.HouseholdLinkPending, f.link.ID).First(&pendingTask).Error; err != nil {
		t.Fatal(err)
	}
	if pendingTask.ResolvedAt != nil {
		t.Fatal("blocked birthday resolved guardian task")
	}
	var stillTeen models.User
	app.DB.First(&stillTeen, f.user.ID)
	if stillTeen.AccountType != models.AccountTeen || stillTeen.Status != models.UserPendingConsent {
		t.Fatal("blocked stage partially committed birthday")
	}
	app.DB.First(other, other.ID)
	if other.AccountType != models.AccountAdult {
		t.Fatal("blocked stage starved later birthday")
	}
	apiDo(t, admin, "PUT", "/api/v1/admin/users/"+itoa2(f.holder.ID), map[string]string{"status": "active"}, 200)
	if n, err := app.rolloverAges(now); err != nil || n != 1 {
		t.Fatalf("retry birthday %d %v", n, err)
	}
	var person models.HouseholdPerson
	app.DB.First(&person, f.person.ID)
	if person.ConvertedUserID == nil || *person.ConvertedUserID != f.user.ID {
		t.Fatal("retry did not transfer")
	}
	if auditCount(t, app, audit.HouseholdPersonConverted) != 1 {
		t.Fatal("retry transferred more than once")
	}
}

func TestAgeRolloverClosesOnlyPendingGuardianTasks(t *testing.T) {
	app, _, teen, confirmed, _, _ := guardianEmailSetup(t)
	admin := loginAPI(t, app, "admin", "pw")
	other := newTestUser(t, app, "another-guardian", "pw", models.RoleMember)
	claimant := loginAPI(t, app, "another-guardian", "pw")
	apiDo(t, claimant, "POST", "/api/v1/me/guardian-claims", map[string]string{"email": teen.Email}, 200)
	var pending models.GuardianLink
	if err := app.DB.Where("teen_user_id = ? AND guardian_user_id = ?", teen.ID, other.ID).First(&pending).Error; err != nil {
		t.Fatal(err)
	}
	if err := app.DB.Model(teen).Update("birth_date", "2008-10-22").Error; err != nil {
		t.Fatal(err)
	}
	if n, err := app.rolloverAges(rolloverInstant(t, "2026-10-22T12:00:00Z")); err != nil || n != 1 {
		t.Fatalf("rollover %d %v", n, err)
	}
	var after models.GuardianLink
	app.DB.First(&after, confirmed.ID)
	if after.Status != models.GuardianConfirmed || after.ConfirmedBy == nil || *after.ConfirmedBy != *confirmed.ConfirmedBy || after.CheckNote != confirmed.CheckNote {
		t.Fatal("birthday changed confirmed guardian history")
	}
	app.DB.First(&pending, pending.ID)
	if pending.Status != models.GuardianRejected || pending.DecidedAt == nil || !strings.Contains(pending.CheckNote, "no longer needed") {
		t.Fatalf("pending link not closed: %+v", pending)
	}
	var task models.Notification
	if err := app.DB.Where("kind = ? AND subject_id = ?", notify.HouseholdLinkPending, pending.ID).First(&task).Error; err != nil {
		t.Fatal(err)
	}
	if task.ResolvedAt == nil || task.Resolution != "withdrawn" || task.ResolvedByID != nil {
		t.Fatalf("pending task not resolved by system: %+v", task)
	}
	apiDo(t, admin, "POST", "/api/v1/admin/guardian-links/"+itoa2(pending.ID)+"/confirm", map[string]string{"checkNote": "Checked identity"}, 403)
	var row models.AuditLog
	if err := app.DB.Where("action = ? AND target_id = ?", audit.GuardianLinkRejected, pending.ID).First(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.ActorID != nil || !strings.Contains(row.MetaJSON, "turned_18") {
		t.Fatalf("missing system age reason: %+v", row)
	}
}
