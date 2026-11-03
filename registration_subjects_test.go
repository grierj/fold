package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"fold/internal/policy"
	"fold/models"

	"gorm.io/gorm"
)

func TestAttendeeProvenanceReadIsBoundedToPageDates(t *testing.T) {
	app := testApp(t)
	admin, gid := openGroup(t, app, "Attendee query")
	eid, date := seedCappedEvent(t, app, admin, gid, nil, true)
	var occ models.EventOccurrence
	if err := app.DB.Where("event_id = ? AND original_date = ?", eid, date).First(&occ).Error; err != nil {
		t.Fatal(err)
	}
	var sql string
	const callback = "test:attendee_provenance_sql"
	if err := app.DB.Callback().Query().After("gorm:query").Register(callback, func(tx *gorm.DB) {
		if strings.Contains(tx.Statement.SQL.String(), "AS leader_added") {
			sql = tx.Statement.SQL.String()
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer app.DB.Callback().Query().Remove(callback)
	app.attendeeNames([]models.EventOccurrence{occ}, map[uint]bool{eid: true}, nil, map[uint]bool{eid: true}, nil, app.requestFacts())
	// The flag is the row's own provenance, so there is no second table to bound:
	// the one read is limited to the page's dates and joins no access table.
	if !strings.Contains(sql, "registrations.occurrence_id IN (") || strings.Contains(sql, "occurrence_accesses") || !strings.Contains(sql, "registrations.access_ended_at IS NULL") {
		t.Fatalf("the read must use the page's dates and the row's own provenance: %s", sql)
	}
	var index string
	if err := app.DB.Raw("SELECT indexdef FROM pg_indexes WHERE schemaname = 'public' AND indexname = 'idx_registrations_live_access'").Scan(&index).Error; err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(index, "(occurrence_id)") || !strings.Contains(index, "access_ended_at IS NULL") || !strings.Contains(index, "access_origin <> ''") {
		t.Fatalf("missing partial index for live grants on page dates: %s", index)
	}
}

func TestRegistrationSubjectIdentity(t *testing.T) {
	user, person, holder := uint(7), uint(7), uint(9)
	for _, tt := range []struct {
		name   string
		r      models.Registration
		holder uint
		valid  bool
	}{
		{"user", models.Registration{UserID: &user}, user, true},
		{"household shares numeric id", models.Registration{HouseholdPersonID: &person, RegisteredBy: &holder}, holder, true},
		{"neither", models.Registration{}, 0, false},
		{"both", models.Registration{UserID: &user, HouseholdPersonID: &person, RegisteredBy: &holder}, 0, false},
		{"missing holder", models.Registration{HouseholdPersonID: &person}, 0, false},
		{"user has household holder", models.Registration{UserID: &user, RegisteredBy: &holder}, 0, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s, err := subjectOf(tt.r)
			if (err == nil) != tt.valid {
				t.Fatalf("subject: %+v %v", s, err)
			}
			if tt.valid && s.holderID != tt.holder {
				t.Fatalf("holder %d", s.holderID)
			}
		})
	}
}

func TestListedRegistrationSubjects(t *testing.T) {
	app := testApp(t)
	holder := newTestUser(t, app, "holder", "password1234", models.RoleMember)
	other := newTestUser(t, app, "other", "password1234", models.RoleMember)
	h := models.Household{Name: "Household", CreatedBy: holder.ID}
	if err := app.DB.Create(&h).Error; err != nil {
		t.Fatal(err)
	}
	if err := app.DB.Create(&models.HouseholdAccount{HouseholdID: h.ID, UserID: holder.ID, Relation: models.HouseholdRelationAdult}).Error; err != nil {
		t.Fatal(err)
	}
	birth := time.Date(1990, 1, 1, 0, 0, 0, 0, time.UTC)
	person := models.HouseholdPerson{HouseholdID: h.ID, FirstName: "Mary", Kind: models.HouseholdAdult, BirthDate: &birth, CreatedBy: holder.ID}
	if err := app.DB.Create(&person).Error; err != nil {
		t.Fatal(err)
	}
	subject, err := householdSubject(app.DB, person.ID, holder.ID)
	if err != nil {
		t.Fatal(err)
	}
	if subject.holderID != holder.ID {
		t.Fatal("wrong holder")
	}
	if _, err := householdSubject(app.DB, person.ID, other.ID); err == nil {
		t.Fatal("unrelated holder admitted")
	}
	// A real event supplies foreign keys without going through a fake user.
	admin, gid := openGroup(t, app, "Subjects")
	eventID, date := seedCappedEvent(t, app, admin, gid, nil, true)
	var occ models.EventOccurrence
	if err := app.DB.Where("event_id = ? AND original_date = ?", eventID, date).First(&occ).Error; err != nil {
		t.Fatal(err)
	}
	r := subject.row(occ.ID)
	r.Status = models.RegGoing
	r.AttendanceMode = models.AttendanceInPerson
	if err := app.DB.Create(&r).Error; err != nil {
		t.Fatal(err)
	}
	assertSeats := func(want int) {
		t.Helper()
		got, err := seatsUsed(app.DB, occ.ID)
		if err != nil || got != want {
			t.Fatalf("seats %d want %d: %v", got, want, err)
		}
	}
	assertSeats(1)
	if err := app.DB.Model(holder).Update("status", models.UserDisabled).Error; err != nil {
		t.Fatal(err)
	}
	assertSeats(0)
	app.DB.Model(holder).Update("status", models.UserActive)
	assertSeats(1)
	app.DB.Model(&person).Update("converted_user_id", other.ID)
	assertSeats(0)
	app.DB.Model(&person).Update("converted_user_id", nil)
	assertSeats(1)
	app.DB.Delete(&person)
	assertSeats(0)
}

func TestHouseholdAttendeeCareProjection(t *testing.T) {
	holderID, personID := uint(1), uint(2)
	start := time.Date(2026, 11, 1, 18, 0, 0, 0, time.UTC)
	birth := time.Date(2020, 11, 2, 0, 0, 0, 0, time.UTC)
	person := models.HouseholdPerson{ID: personID, HouseholdID: 3, FirstName: "Kit", Kind: models.HouseholdChild, BirthDate: &birth, CareNotes: "Private care\n" + strings.Repeat("long", 100)}
	holder := models.User{ID: holderID, Name: "John Smith", Status: models.UserActive}
	manager := models.User{ID: 4, Name: "Host", Status: models.UserActive}
	f := subjectFacts{people: map[uint]*models.HouseholdPerson{personID: &person}, links: map[uint][]models.GuardianLink{personID: {{PersonID: &personID, GuardianUserID: holderID, Status: models.GuardianConfirmed}}}, households: map[uint]models.Household{3: {ID: 3, Name: "Leader household", Address: "Never expose address"}}, users: map[uint]*models.User{holderID: &holder}, accounts: map[uint]models.HouseholdAccount{holderID: {HouseholdID: 3, UserID: holderID, Relation: models.HouseholdRelationGuardian}}}
	r := models.Registration{ID: 10, HouseholdPersonID: &personID, RegisteredBy: &holderID, Status: models.RegGoing, AttendanceMode: models.AttendanceInPerson, ChildcareRequested: true}
	o := models.EventOccurrence{ID: 5, EventID: 6, Event: models.Event{TZID: "UTC"}, StartsAt: start, EndsAt: start.Add(time.Hour), Status: models.OccurrenceScheduled}
	for _, tt := range []struct {
		name          string
		now           time.Time
		status        string
		manager       bool
		viewer        *models.User
		visible, care bool
	}{
		{"manager exact opening", start.Add(-24 * time.Hour), models.RegGoing, true, &manager, true, true},
		{"manager exact closing", start.Add(25 * time.Hour), models.RegGoing, true, &manager, true, true},
		{"manager too early", start.Add(-24*time.Hour - time.Nanosecond), models.RegGoing, true, &manager, true, false},
		{"manager too late", start.Add(25*time.Hour + time.Nanosecond), models.RegGoing, true, &manager, true, false},
		{"wrong event manager", start, models.RegGoing, false, &manager, false, false},
		{"waitlisted", start, models.RegWaitlisted, true, &manager, true, false},
		{"maybe", start, models.RegMaybe, true, &manager, true, false},
		{"removed", start, models.RegRemoved, true, &manager, true, false},
		{"guardian outside window", start.Add(-40 * time.Hour), models.RegGoing, false, &holder, true, true},
		{"anonymous", start, models.RegGoing, false, nil, false, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r.Status = tt.status
			f.today = start // the age clock is fixed; only the care window moves
			dto := f.attendee(r, o, tt.viewer, tt.manager, true, tt.now)
			if (dto != nil) != tt.visible {
				t.Fatalf("visibility: %+v", dto)
			}
			if dto == nil {
				return
			}
			hp := dto.HouseholdPerson
			if (hp.CareNotes != nil) != tt.care {
				t.Fatalf("care: %+v", hp)
			}
			if hp.Age == nil || *hp.Age != 5 || dto.UserID != nil || hp.RegisteredBy.Name != "John Smith" {
				t.Fatalf("identity or age: %+v", dto)
			}
			raw, _ := json.Marshal(dto)
			for _, secret := range []string{"2020-11-02", "Never expose address"} {
				if strings.Contains(string(raw), secret) {
					t.Fatalf("private data %s", raw)
				}
			}
		})
	}
}

func TestHouseholdClockInUsesTransactionTimezone(t *testing.T) {
	app := testApp(t)
	if err := app.DB.Create(&models.Setting{Key: settingTimezone, Value: "UTC"}).Error; err != nil {
		t.Fatal(err)
	}
	tx := app.DB.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	defer tx.Rollback()
	if err := tx.Model(&models.Setting{}).Where("key = ?", settingTimezone).Update("value", "America/Boise").Error; err != nil {
		t.Fatal(err)
	}

	// The church date changes at local midnight, including across DST.
	for _, tt := range []struct {
		instant, want string
	}{
		{"2026-10-22T00:30:00Z", "2026-10-21T18:30:00-06:00"},
		{"2026-11-01T07:30:00Z", "2026-11-01T01:30:00-06:00"},
		{"2026-11-01T08:30:00Z", "2026-11-01T01:30:00-07:00"},
	} {
		now, err := time.Parse(time.RFC3339, tt.instant)
		if err != nil {
			t.Fatal(err)
		}
		got, err := householdClockIn(tx, now)
		if err != nil {
			t.Fatal(err)
		}
		if !got.Equal(now) || got.Format(time.RFC3339) != tt.want || got.Location().String() != "America/Boise" {
			t.Fatalf("transaction clock for %s = %s in %s; want %s in America/Boise", tt.instant, got.Format(time.RFC3339), got.Location(), tt.want)
		}
		if outside := app.householdClock(now); outside.Location() != time.UTC || !outside.Equal(now) {
			t.Fatalf("request clock read uncommitted timezone: %v", outside)
		}
	}
}

func TestHouseholdClockInFallbackAndReadFailure(t *testing.T) {
	for _, tt := range []struct {
		name, value string
	}{
		{name: "missing"},
		{name: "empty"},
		{name: "server local", value: "Local"},
		{name: "invalid", value: "Not/AZone"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			app := testApp(t)
			if tt.name != "missing" {
				if err := app.DB.Create(&models.Setting{Key: settingTimezone, Value: tt.value}).Error; err != nil {
					t.Fatal(err)
				}
			}
			now := time.Date(2026, 10, 22, 0, 30, 0, 0, time.FixedZone("input", 2*60*60))
			got, err := householdClockIn(app.DB, now)
			if err != nil || !got.Equal(now) || got.Location() != time.UTC {
				t.Fatalf("fallback clock = %v, %v; want %v in UTC", got, err, now.UTC())
			}
		})
	}

	app := testApp(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	db := app.DB.WithContext(ctx)
	now := time.Date(2026, 10, 22, 0, 30, 0, 0, time.FixedZone("input", 2*60*60))
	if got, err := householdClockIn(db, now); !errors.Is(err, context.Canceled) || !got.IsZero() {
		t.Fatalf("failed transaction clock = %v, %v; want zero time and context cancellation", got, err)
	}
	requestApp := &App{DB: db}
	if got := requestApp.householdClock(now); !got.Equal(now) || got.Location() != time.UTC {
		t.Fatalf("failed request clock = %v; want %v in UTC", got, now.UTC())
	}
}

func TestSubjectFactsChurchLocalBirthday(t *testing.T) {
	app := testApp(t)
	holder := newTestUser(t, app, "clockholder", "password1234", models.RoleMember)
	household := models.Household{Name: "Clock family", CreatedBy: holder.ID}
	if err := app.DB.Create(&household).Error; err != nil {
		t.Fatal(err)
	}
	if err := app.DB.Create(&models.HouseholdAccount{HouseholdID: household.ID, UserID: holder.ID, Relation: models.HouseholdRelationAdult}).Error; err != nil {
		t.Fatal(err)
	}
	birth := time.Date(2008, 10, 22, 0, 0, 0, 0, time.UTC)
	person := models.HouseholdPerson{HouseholdID: household.ID, FirstName: "Birthday", Kind: models.HouseholdAdult, BirthDate: &birth, CreatedBy: holder.ID}
	if err := app.DB.Create(&person).Error; err != nil {
		t.Fatal(err)
	}
	row := models.Registration{ID: 1, HouseholdPersonID: &person.ID, RegisteredBy: &holder.ID, Status: models.RegGoing, AttendanceMode: models.AttendanceInPerson}
	for _, tt := range []struct {
		zone, instant string
		age           int
		eligible      bool
	}{
		{"America/Boise", "2026-10-22T00:30:00Z", 17, false},
		{"Pacific/Kiritimati", "2026-10-21T23:30:00Z", 18, true},
		{"America/Boise", "2026-10-22T06:00:00Z", 18, true},
	} {
		t.Run(tt.zone+tt.instant, func(t *testing.T) {
			app.DB.Where(models.Setting{Key: settingTimezone}).Assign(models.Setting{Value: tt.zone}).FirstOrCreate(&models.Setting{})
			instant, err := time.Parse(time.RFC3339, tt.instant)
			if err != nil {
				t.Fatal(err)
			}
			f := app.loadSubjectFacts(app.DB, []models.Registration{row}, holder)
			if !f.today.IsZero() || f.clock == nil {
				t.Fatal("facts eagerly chose UTC instead of a lazy church clock")
			}
			f.clock = func() time.Time { return app.householdClock(instant) }
			facts := householdFacts(f.people[person.ID], nil, f.day())
			if facts.AgeYears == nil || *facts.AgeYears != tt.age {
				t.Fatalf("age: %+v", facts)
			}
			if got := policy.HouseholdRegistrationProblem(facts) == ""; got != tt.eligible {
				t.Fatalf("birthday eligibility: %t", got)
			}
			attendee := f.attendee(row, models.EventOccurrence{Status: models.OccurrenceScheduled}, holder, false, false, instant)
			if attendee == nil || attendee.HouseholdPerson == nil || attendee.HouseholdPerson.Age == nil || *attendee.HouseholdPerson.Age != tt.age {
				t.Fatalf("display disagrees: %+v", attendee)
			}
		})
	}
	// Account-only facts never read the church time zone, including eligibility.
	f := app.loadSubjectFacts(app.DB, []models.Registration{{UserID: &holder.ID}}, holder)
	called := false
	f.clock = func() time.Time { called = true; return time.Now() }
	_ = f.attendee(models.Registration{UserID: &holder.ID}, models.EventOccurrence{}, holder, false, false, time.Now())
	if called {
		t.Fatal("account-only rendering read the church clock")
	}
}

func TestPendingTeenRegistrationNamesStayPrivate(t *testing.T) {
	teen := models.User{ID: 3, Name: "Taylor", Status: models.UserPendingConsent, AccountType: models.AccountTeen}
	member := models.User{ID: 4, Status: models.UserActive, AccountType: models.AccountAdult}
	f := newSubjectFacts(time.Now())
	f.users[teen.ID] = &teen
	f.accounts[teen.ID] = models.HouseholdAccount{HouseholdID: 9, UserID: teen.ID, Relation: models.HouseholdRelationTeen}
	row := models.Registration{ID: 5, UserID: &teen.ID, Status: models.RegGoing}
	if dto := f.attendee(row, models.EventOccurrence{}, &member, false, false, time.Now()); dto != nil {
		t.Fatal("unrelated member saw pending teen")
	}
	if dto := f.attendee(row, models.EventOccurrence{}, &member, true, false, time.Now()); dto == nil || dto.AccountType != models.AccountTeen {
		t.Fatal("manager lost pending teen visibility")
	}
	f.accounts[member.ID] = models.HouseholdAccount{HouseholdID: 9, UserID: member.ID, Relation: models.HouseholdRelationAdult}
	if dto := f.attendee(row, models.EventOccurrence{}, &member, false, false, time.Now()); dto == nil {
		t.Fatal("household lost visibility")
	}
}

// DEC-82: the teen badge is for event leaders and admins only.
func TestTeenAccountTypeOnlyForLeadersAndAdmins(t *testing.T) {
	teen := models.User{ID: 3, Name: "Taylor", Status: models.UserActive, AccountType: models.AccountTeen}
	member := models.User{ID: 4, Status: models.UserActive, Role: models.RoleMember, AccountType: models.AccountAdult}
	admin := models.User{ID: 5, Status: models.UserActive, Role: models.RoleAdmin, AccountType: models.AccountAdult}
	f := newSubjectFacts(time.Now())
	f.users[teen.ID] = &teen
	row := models.Registration{ID: 5, UserID: &teen.ID, Status: models.RegGoing}
	if dto := f.attendee(row, models.EventOccurrence{}, &member, false, false, time.Now()); dto != nil {
		t.Fatalf("ordinary attendee got the minor identity: %+v", dto)
	}
	if dto := f.attendee(row, models.EventOccurrence{}, &member, true, false, time.Now()); dto == nil || dto.AccountType != models.AccountTeen {
		t.Fatal("leader lost the badge")
	}
	if dto := f.attendee(row, models.EventOccurrence{}, &admin, false, false, time.Now()); dto == nil || dto.AccountType != models.AccountTeen {
		t.Fatal("admin lost the badge")
	}
}
