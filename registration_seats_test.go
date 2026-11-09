package main

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"fold/internal/policy"
	"fold/models"
)

// The ledger counts party size and childcare demand in SQL (for pools and
// totals) and in Go (for a row in hand). They must agree for every kind of row.
func TestSeatSQLMatchesGoRules(t *testing.T) {
	app := testApp(t)
	admin, gid := openGroup(t, app, "Ledger")
	eid, date := seedChildcareEvent(t, app, admin, gid, nil, nil, false)
	app.DB.Model(&models.Event{}).Where("id = ?", eid).Update("remote_available", true)
	var ev models.Event
	if err := app.DB.First(&ev, eid).Error; err != nil {
		t.Fatal(err)
	}
	var occ models.EventOccurrence
	if err := app.DB.Where("event_id = ? AND original_date = ?", eid, date).First(&occ).Error; err != nil {
		t.Fatal(err)
	}
	holder := newTestUser(t, app, "holder", "password1234", models.RoleMember)
	h := models.Household{Name: "Household", CreatedBy: holder.ID}
	app.DB.Create(&h)
	app.DB.Create(&models.HouseholdAccount{HouseholdID: h.ID, UserID: holder.ID, Relation: models.HouseholdRelationAdult})
	birth := time.Now().AddDate(-6, 0, 0)
	kid := models.HouseholdPerson{HouseholdID: h.ID, FirstName: "Kid", Kind: models.HouseholdChild, BirthDate: &birth, CreatedBy: holder.ID}
	kid2 := models.HouseholdPerson{HouseholdID: h.ID, FirstName: "Kid2", Kind: models.HouseholdChild, BirthDate: &birth, CreatedBy: holder.ID}
	app.DB.Create(&kid)
	app.DB.Create(&kid2)

	var rows []models.Registration
	for i, spec := range []struct {
		mode   string
		guests int
		care   int
	}{{models.AttendanceInPerson, 2, 2}, {models.AttendanceRemote, 1, 3}, {models.AttendanceInPerson, 0, 0}} {
		u := newTestUser(t, app, "member"+itoa(uint(i)), "password1234", models.RoleMember)
		rows = append(rows, models.Registration{UserID: &u.ID, OccurrenceID: occ.ID, Status: models.RegGoing, AttendanceMode: spec.mode, GuestCount: spec.guests, ChildcareCount: spec.care})
	}
	for _, p := range []struct {
		person *models.HouseholdPerson
		mode   string
		care   bool
	}{{&kid, models.AttendanceInPerson, true}, {&kid2, models.AttendanceRemote, false}} {
		hid := holder.ID
		pid := p.person.ID
		rows = append(rows, models.Registration{HouseholdPersonID: &pid, RegisteredBy: &hid, OccurrenceID: occ.ID, Status: models.RegGoing, AttendanceMode: p.mode, ChildcareRequested: p.care})
	}
	wantSeats := map[string]int{}
	wantCare := 0
	for i := range rows {
		if err := app.DB.Create(&rows[i]).Error; err != nil {
			t.Fatal(err)
		}
		wantSeats[rows[i].AttendanceMode] += partySeats(rows[i])
		wantCare += effectiveChildcare(&ev, rows[i])
	}
	for mode, want := range wantSeats {
		if got, err := seatsUsedForMode(app.DB, occ.ID, mode, 0); err != nil || got != want {
			t.Fatalf("%s seats: SQL %d, Go %d, %v", mode, got, want, err)
		}
	}
	if got, err := childcareUsedFor(app.DB, occ.ID, 0); err != nil || got != wantCare {
		t.Fatalf("childcare: SQL %d, Go %d, %v", got, wantCare, err)
	}
	// A row's own seat and places do not count against it.
	own := rows[0]
	if got, _ := seatsUsedForMode(app.DB, occ.ID, own.AttendanceMode, own.ID); got != wantSeats[own.AttendanceMode]-partySeats(own) {
		t.Fatalf("seats leave the row's own out: %d", got)
	}
	if got, _ := childcareUsedFor(app.DB, occ.ID, own.ID); got != wantCare-effectiveChildcare(&ev, own) {
		t.Fatalf("childcare leaves the row's own out: %d", got)
	}
}

func TestPlaceOnWaitlistKeepsPlaceInLine(t *testing.T) {
	now := time.Now().UTC()
	earlier := now.Add(-time.Hour)
	for _, tt := range []struct {
		name      string
		old, next models.Registration
		want      time.Time
	}{
		{"new row", models.Registration{}, models.Registration{AttendanceMode: models.AttendanceInPerson}, now},
		{"keeps its place", models.Registration{AttendanceMode: models.AttendanceInPerson, WaitlistedAt: &earlier}, models.Registration{AttendanceMode: models.AttendanceInPerson, WaitlistedAt: &earlier}, earlier},
		{"moving pools joins the back", models.Registration{AttendanceMode: models.AttendanceInPerson, WaitlistedAt: &earlier}, models.Registration{AttendanceMode: models.AttendanceRemote, WaitlistedAt: &earlier}, now},
	} {
		t.Run(tt.name, func(t *testing.T) {
			next := tt.next
			placeOnWaitlist(&next, tt.old, now)
			if next.Status != models.RegWaitlisted || next.WaitlistedAt == nil || !next.WaitlistedAt.Equal(tt.want) {
				t.Fatalf("got %s %v, want waitlisted at %v", next.Status, next.WaitlistedAt, tt.want)
			}
		})
	}
}

// The restore's recovery and previouslyRegisteredPerson find dropped answers in
// the audit log by key; the writer must keep writing exactly those keys.
func TestRegistrationAuditKeysAreShared(t *testing.T) {
	uid, pid, by := uint(1), uint(2), uint(3)
	meta := registrationMeta(models.Registration{ID: 9, UserID: &uid, HouseholdPersonID: &pid, RegisteredBy: &by, OccurrenceID: 4})
	read := map[string]bool{}
	typ := reflect.TypeOf(droppedRegistrationMeta{})
	for i := 0; i < typ.NumField(); i++ {
		key := strings.Split(typ.Field(i).Tag.Get("json"), ",")[0]
		read[key] = true
		if _, ok := meta[key]; !ok {
			t.Errorf("registrationMeta does not write %q, which recovery reads", key)
		}
	}
	for _, key := range []string{metaRegistrationID, metaUserID, metaPersonID, metaRegisteredBy, metaOccurrenceID, metaAttendanceMode} {
		if _, ok := meta[key]; !ok {
			t.Errorf("registrationMeta does not write the shared key %q", key)
		}
		if !read[key] {
			t.Errorf("shared key %q is not read by droppedRegistrationMeta", key)
		}
	}
	b, err := json.Marshal(meta)
	if err != nil {
		t.Fatal(err)
	}
	var got droppedRegistrationMeta
	if err := json.Unmarshal(b, &got); err != nil || got.RegistrationID != 9 || got.OccurrenceID != 4 || got.PersonID == nil || *got.PersonID != pid || got.RegisteredBy == nil || *got.RegisteredBy != by || got.AttendanceMode != models.AttendanceInPerson {
		t.Fatalf("recovery reads %+v (%v) from what registrationMeta writes", got, err)
	}
}

// listedRegistrations is only a pre-filter on the holder and the person: a row
// the household rule accepts must be listed, and a listed row differs from the
// rule only where the SQL knows less (a holder without a password, the event
// access, the person's age).
func TestListedRegistrationsPrefilterMatchesPolicy(t *testing.T) {
	app := testApp(t)
	admin, gid := openGroup(t, app, "Prefilter")
	eid, date := seedCappedEvent(t, app, admin, gid, nil, false)
	var occ models.EventOccurrence
	if err := app.DB.Where("event_id = ? AND original_date = ?", eid, date).First(&occ).Error; err != nil {
		t.Fatal(err)
	}
	birth := time.Now().AddDate(-40, 0, 0)
	n := 0
	for _, status := range []string{models.UserActive, models.UserDisabled, models.UserGuest} {
		for _, acctType := range []string{models.AccountAdult, models.AccountTeen} {
			for _, relation := range []string{"", models.HouseholdRelationAdult, models.HouseholdRelationGuardian, models.HouseholdRelationTeen} {
				for _, personState := range []string{"live", "deleted", "converted"} {
					for _, password := range []bool{true, false} {
						n++
						holder := newTestUser(t, app, "holder"+itoa(uint(n)), "password1234", models.RoleMember)
						updates := map[string]any{"status": status, "account_type": acctType}
						if !password {
							updates["password_hash"] = nil
						}
						if err := app.DB.Model(holder).Updates(updates).Error; err != nil {
							t.Fatal(err)
						}
						if err := app.DB.First(holder, holder.ID).Error; err != nil {
							t.Fatal(err)
						}
						h := models.Household{Name: "H", CreatedBy: holder.ID}
						app.DB.Create(&h)
						if relation != "" {
							app.DB.Create(&models.HouseholdAccount{HouseholdID: h.ID, UserID: holder.ID, Relation: relation})
						}
						person := models.HouseholdPerson{HouseholdID: h.ID, FirstName: "P", Kind: models.HouseholdAdult, BirthDate: &birth, CreatedBy: holder.ID}
						if err := app.DB.Create(&person).Error; err != nil {
							t.Fatal(err)
						}
						switch personState {
						case "deleted":
							app.DB.Delete(&person)
						case "converted":
							other := newTestUser(t, app, "conv"+itoa(uint(n)), "password1234", models.RoleMember)
							app.DB.Model(&person).Update("converted_user_id", other.ID)
						}
						hid, pid := holder.ID, person.ID
						reg := models.Registration{HouseholdPersonID: &pid, RegisteredBy: &hid, OccurrenceID: occ.ID, Status: models.RegGoing, AttendanceMode: models.AttendanceInPerson}
						if err := app.DB.Create(&reg).Error; err != nil {
							t.Fatal(err)
						}
						var listed int64
						app.DB.Model(&models.Registration{}).Joins(listedRegistrations()).Where("registrations.id = ?", reg.ID).Count(&listed)

						facts := newSubjectFacts(time.Now())
						if err := facts.load(app.DB, []models.Registration{reg}, nil); err != nil {
							t.Fatal(err)
						}
						// A deleted person is not loaded at all, which eligible reads as unavailable.
						p := facts.people[pid]
						ok := p != nil
						if ok {
							acct, found := facts.accounts[holder.ID]
							h2 := householdHolderOf(holder, acct, found, p, true)
							// The person's age and link are not what the SQL knows; the policy sees a clean adult.
							age := 40
							pf := policy.HouseholdPerson{Kind: policy.KindAdult, AgeYears: &age, LinkStatus: policy.LinkConfirmed, Converted: personState == "converted"}
							ok = policy.CanRegisterHouseholdPerson(h2, pf) == ""
						}
						if ok && listed != 1 {
							t.Errorf("status=%s type=%s relation=%q person=%s password=%t: policy accepts the holder but the SQL pre-filter drops the row", status, acctType, relation, personState, password)
						}
						if !ok && listed == 1 && password {
							t.Errorf("status=%s type=%s relation=%q person=%s: SQL lists a row the policy refuses", status, acctType, relation, personState)
						}
					}
				}
			}
		}
	}
}
