package main

import (
	"encoding/json"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"fold/internal/policy"
	"fold/models"

	"gorm.io/gorm"
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

// listedRegistrations is only a pre-filter on the holder and the person's row
// state, and the household rule (policy.CanRegisterHouseholdPerson) is the
// authority. For every holder (status, account type, household relation,
// password) and person state (live, deleted, converted) the test lists the row
// with the SQL, then asks the policy about the same holder and person for every
// kind, age (the 12/13/17/18 boundaries on both sides of the birthday), missing
// or future birth date, guardian-link state (none, pending, confirmed, rejected,
// revoked) and event access. Two things must hold:
//   - SQL is a superset: whenever the policy accepts, the row is listed;
//   - the SQL over-includes only where it knows less: a listed row the policy
//     refuses is refused for the person's age, birth date or link, or because the
//     holder cannot sign in (no password) or may not see the event. A refusal as
//     "holder unavailable" for any other reason means the SQL lists a row it
//     should have dropped.
func TestListedRegistrationsPrefilterMatchesPolicy(t *testing.T) {
	app := testApp(t)
	admin, gid := openGroup(t, app, "Prefilter")
	eid, date := seedCappedEvent(t, app, admin, gid, nil, false)
	var occ models.EventOccurrence
	if err := app.DB.Where("event_id = ? AND original_date = ?", eid, date).First(&occ).Error; err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	type ageCase struct {
		name  string
		birth *time.Time
	}
	at := func(years, days int) *time.Time { b := now.AddDate(-years, 0, days); return &b }
	var ages []ageCase
	for _, y := range []int{5, 12, 13, 17, 18, 40} {
		ages = append(ages,
			ageCase{"turns " + itoa(uint(y)) + " today", at(y, 0)},
			ageCase{"turns " + itoa(uint(y)) + " tomorrow", at(y, 1)})
	}
	ages = append(ages, ageCase{"no birth date", nil}, ageCase{"future birth date", at(0, 30)})
	kinds := []string{models.HouseholdAdult, models.HouseholdTeen, models.HouseholdChild}
	linkStates := []string{"", models.GuardianPending, models.GuardianConfirmed, models.GuardianRejected, models.GuardianRevoked}
	ageCodes := map[string]bool{"household_birth_date_required": true, "household_age_mismatch": true,
		"household_link_pending": true, "household_link_unconfirmed": true}
	birth := at(40, 0)
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
						person := models.HouseholdPerson{HouseholdID: h.ID, FirstName: "P", Kind: models.HouseholdAdult, BirthDate: birth, CreatedBy: holder.ID}
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

						facts := newSubjectFacts(now)
						if err := facts.load(app.DB, []models.Registration{reg}, nil); err != nil {
							t.Fatal(err)
						}
						acct, found := facts.accounts[holder.ID]
						where := "status=" + status + " type=" + acctType + " relation=" + relation + " person=" + personState + " password=" + strconv.FormatBool(password)
						for _, access := range []bool{true, false} {
							for _, kind := range kinds {
								for _, age := range ages {
									for _, link := range linkStates {
										// The person as the facts see it: deleted and converted people are
										// the SQL's business, age and link are the policy's.
										p := models.HouseholdPerson{ID: pid, HouseholdID: h.ID, Kind: kind, BirthDate: age.birth}
										switch personState {
										case "deleted":
											p.DeletedAt = gorm.DeletedAt{Time: now, Valid: true}
										case "converted":
											conv := holder.ID
											p.ConvertedUserID = &conv
										}
										var links []models.GuardianLink
										if link != "" {
											links = []models.GuardianLink{{PersonID: &pid, Status: link}}
										}
										got := policy.CanRegisterHouseholdPerson(householdHolderOf(holder, acct, found, &p, access), householdFacts(&p, links, now))
										if got == "" && listed != 1 {
											t.Errorf("%s kind=%s %s link=%q access=%t: the policy accepts but the SQL pre-filter drops the row", where, kind, age.name, link, access)
										}
										if got != "" && listed == 1 && !ageCodes[got] && password && access {
											t.Errorf("%s kind=%s %s link=%q access=%t: SQL lists a row the policy refuses as %q", where, kind, age.name, link, access, got)
										}
									}
								}
							}
						}
					}
				}
			}
		}
	}
}
