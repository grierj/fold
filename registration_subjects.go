package main

import (
	"encoding/json"
	"errors"
	"time"

	"fold/internal/accounts"
	"fold/internal/audit"
	"fold/internal/policy"
	"fold/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// registrationSubject keeps the two identity namespaces separate. Only the
// constructors below assemble subjects; household ownership is checked in DB.
type registrationSubject struct {
	userID   *uint
	personID *uint
	holderID uint
}

func userSubject(id uint) registrationSubject {
	return registrationSubject{userID: &id, holderID: id}
}

func householdSubject(db *gorm.DB, personID, holderID uint) (registrationSubject, error) {
	var n int64
	err := db.Model(&models.HouseholdPerson{}).
		Joins("JOIN household_accounts ON household_accounts.household_id = household_people.household_id AND household_accounts.user_id = ?", holderID).
		Where("household_people.id = ? AND household_people.converted_user_id IS NULL", personID).Count(&n).Error
	if err != nil {
		return registrationSubject{}, err
	}
	if n != 1 {
		return registrationSubject{}, errRegNotFound
	}
	return registrationSubject{personID: &personID, holderID: holderID}, nil
}

func subjectOf(r models.Registration) (registrationSubject, error) {
	if r.UserID != nil && r.HouseholdPersonID == nil && r.RegisteredBy == nil {
		return userSubject(*r.UserID), nil
	}
	if r.UserID == nil && r.HouseholdPersonID != nil && r.RegisteredBy != nil {
		return registrationSubject{personID: r.HouseholdPersonID, holderID: *r.RegisteredBy}, nil
	}
	return registrationSubject{}, errors.New("invalid registration subject")
}

func (s registrationSubject) query(db *gorm.DB, occurrenceID uint) *gorm.DB {
	q := db.Where("occurrence_id = ?", occurrenceID)
	if s.userID != nil {
		return q.Where("user_id = ?", *s.userID)
	}
	return q.Where("household_person_id = ?", *s.personID)
}

func (s registrationSubject) row(occurrenceID uint) models.Registration {
	r := models.Registration{OccurrenceID: occurrenceID, UserID: s.userID, HouseholdPersonID: s.personID}
	if s.personID != nil {
		r.RegisteredBy = &s.holderID
	}
	return r
}

func registrationHolder(r models.Registration) uint {
	s, err := subjectOf(r)
	if err != nil {
		return 0
	}
	return s.holderID
}

// listedRegistrations includes the responsible account for both variants.
// Membership lists deliberately keep their existing account-only query.
func listedRegistrations() string {
	return `LEFT JOIN household_people AS registration_person ON registration_person.id = registrations.household_person_id
 JOIN users ON users.id = COALESCE(registrations.user_id, registrations.registered_by)
 AND users.deleted_at IS NULL AND users.status <> 'disabled'
 AND (registrations.household_person_id IS NULL OR
 (registration_person.deleted_at IS NULL AND registration_person.converted_user_id IS NULL
 AND users.status = 'active' AND users.account_type = 'adult'
 AND EXISTS (SELECT 1 FROM household_accounts ha WHERE ha.user_id = users.id
 AND ha.household_id = registration_person.household_id AND ha.relation IN ('adult','guardian'))))`
}

func lockHousehold(tx *gorm.DB, id uint) error {
	var h models.Household
	return tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&h, id).Error
}

func householdFacts(person *models.HouseholdPerson, links []models.GuardianLink, now time.Time) policy.HouseholdPerson {
	status, _ := effectiveLink(links)
	out := policy.HouseholdPerson{Kind: person.Kind, LinkStatus: status, Deleted: person.DeletedAt.Valid, Converted: person.ConvertedUserID != nil}
	if person.BirthDate != nil {
		age := accounts.AgeOn(*person.BirthDate, now)
		out.AgeYears = &age
	}
	return out
}

func (a *App) householdClock(now time.Time) time.Time {
	loc, err := time.LoadLocation(a.churchTimezone())
	if err != nil {
		return now.UTC()
	}
	return now.In(loc)
}

// eligibleRegistration reads current eligibility without taking earlier-order
// holder/household locks after the caller has locked an occurrence.
func (a *App) eligibleRegistration(tx *gorm.DB, r models.Registration, ev *models.Event, now time.Time) bool {
	return a.eligibleRegistrationForHolder(tx, r, ev, now, nil)
}

func (a *App) eligibleRegistrationForHolder(tx *gorm.DB, r models.Registration, ev *models.Event, now time.Time, override *models.User) bool {
	f := loadSubjectFacts(tx, []models.Registration{r}, nil)
	if override != nil {
		f.users[override.ID] = override
	}
	return f.eligible(a, tx, r, ev, a.householdClock(now), map[uint]bool{})
}

func (f subjectFacts) eligible(a *App, tx *gorm.DB, r models.Registration, ev *models.Event, today time.Time, access map[uint]bool) bool {
	holder := f.users[registrationHolder(r)]
	if holder == nil || holder.Status == models.UserDisabled {
		return false
	}
	allowed, loaded := access[holder.ID]
	if !loaded {
		allowed = a.canRegisterIn(tx, holder, ev)
		access[holder.ID] = allowed
	}
	if !allowed || (r.AttendanceMode == models.AttendanceRemote && !ev.RemoteAvailable) {
		return false
	}
	if r.HouseholdPersonID == nil {
		return true
	}
	person := f.people[*r.HouseholdPersonID]
	if person == nil {
		return false
	}
	acct, found := f.accounts[holder.ID]
	rel := householdRelOf(acct, found && acct.HouseholdID == person.HouseholdID)
	if policy.CanRegisterHouseholdPerson(asPerson(holder), asPerson(holder), rel, householdFacts(person, f.links[person.ID], today), holder.Status == models.UserActive, allowed, allowed) != "" {
		return false
	}
	return !r.ChildcareRequested || (person.Kind == models.HouseholdChild && r.AttendanceMode != models.AttendanceRemote)
}

func householdPersonName(tx *gorm.DB, id uint) string {
	var p models.HouseholdPerson
	if tx.First(&p, id).Error != nil {
		return "Household member"
	}
	return p.FullName()
}

// reconcileHouseholdPerson runs while the household is locked, and locks
// affected dates in ID order before cancelling or clearing invalid requests.
func (a *App) reconcileHouseholdPerson(tx *gorm.DB, person *models.HouseholdPerson, deleting bool, actor uint, ip string) ([]uint, error) {
	var regs []models.Registration
	now := time.Now().UTC()
	if err := tx.Joins("JOIN event_occurrences ON event_occurrences.id = registrations.occurrence_id").
		Where("registrations.household_person_id = ? AND event_occurrences.starts_at > ? AND event_occurrences.status = ? AND registrations.status IN ?", person.ID, now, models.OccurrenceScheduled, []string{models.RegGoing, models.RegMaybe, models.RegWaitlisted}).Order("registrations.occurrence_id, registrations.id").Find(&regs).Error; err != nil {
		return nil, err
	}
	var mailed []uint
	for _, r := range regs {
		o, err := lockOccurrence(tx, r.OccurrenceID)
		if err != nil {
			return nil, err
		}
		if err := tx.First(&r, r.ID).Error; err != nil {
			return nil, err
		}
		var ev models.Event
		if err := tx.First(&ev, o.EventID).Error; err != nil {
			return nil, err
		}
		old := r
		if deleting || !a.eligibleRegistration(tx, r, &ev, now) {
			// Changing away from child clears care without cancelling an otherwise
			// eligible confirmed teen or verified adult's seat.
			candidate := r
			candidate.ChildcareRequested = false
			if !deleting && a.eligibleRegistration(tx, candidate, &ev, now) {
				r.ChildcareRequested = false
			} else {
				r.Status = models.RegCancelled
				r.ManageTokenHash = ""
				r.WaitlistedAt = nil
				r.PromotedAt = nil
			}
		}
		if r.Status == old.Status && r.ChildcareRequested == old.ChildcareRequested {
			continue
		}
		if err := tx.Omit("User", "Occurrence").Save(&r).Error; err != nil {
			return nil, err
		}
		if err := auditRegistration(tx, actor, audit.RegistrationChanged, ev.ID, old, r, ip); err != nil {
			return nil, err
		}
		if r.Status == models.RegCancelled {
			ids, err := notifyRegistrationRemoved(tx, &ev, o, r, "This household person's details changed. Check their profile before signing them up again.", actor)
			if err != nil {
				return nil, err
			}
			mailed = append(mailed, ids...)
		}
		if old.Status == models.RegGoing {
			ids, err := a.promoteLocked(tx, &ev, o, now, &actor)
			if err != nil {
				return nil, err
			}
			mailed = append(mailed, ids...)
		}
	}
	return mailed, nil
}

type registeredByJSON struct {
	UserID uint   `json:"userId"`
	Name   string `json:"name"`
}

type householdAttendeeJSON struct {
	PersonID              uint             `json:"personId"`
	Name                  string           `json:"name"`
	HouseholdName         string           `json:"householdName"`
	Kind                  string           `json:"kind"`
	Age                   *int             `json:"age,omitempty"`
	RegisteredBy          registeredByJSON `json:"registeredBy"`
	CareNotes             *string          `json:"careNotes,omitempty"`
	CareNotesVisibleUntil *time.Time       `json:"careNotesVisibleUntil,omitempty"`
}

type myHouseholdRegistrationJSON struct {
	RegistrationID     uint   `json:"registrationId"`
	PersonID           uint   `json:"personId"`
	RegisteredBy       uint   `json:"registeredBy"`
	HolderName         string `json:"holderName"`
	Status             string `json:"status"`
	AttendanceMode     string `json:"attendanceMode"`
	WaitlistPosition   *int   `json:"waitlistPosition"`
	ChildcareRequested bool   `json:"childcareRequested"`
	Unavailable        bool   `json:"unavailable"`
}

// subjectFacts loads mixed attendee subjects in batches, without turning a
// household person into an authenticated policy actor.
type subjectFacts struct {
	people     map[uint]*models.HouseholdPerson
	links      map[uint][]models.GuardianLink
	households map[uint]models.Household
	users      map[uint]*models.User
	accounts   map[uint]models.HouseholdAccount
}

func loadSubjectFacts(db *gorm.DB, regs []models.Registration, viewer *models.User) subjectFacts {
	f := subjectFacts{people: map[uint]*models.HouseholdPerson{}, households: map[uint]models.Household{}, users: map[uint]*models.User{}, accounts: map[uint]models.HouseholdAccount{}}
	var personIDs, userIDs, householdIDs []uint
	if viewer != nil {
		userIDs = append(userIDs, viewer.ID)
	}
	for _, r := range regs {
		userIDs = append(userIDs, registrationHolder(r))
		if r.HouseholdPersonID != nil {
			personIDs = append(personIDs, *r.HouseholdPersonID)
		}
	}
	var people []models.HouseholdPerson
	if len(personIDs) > 0 {
		db.Where("id IN ?", personIDs).Find(&people)
	}
	for i := range people {
		p := &people[i]
		f.people[p.ID] = p
		householdIDs = append(householdIDs, p.HouseholdID)
	}
	f.links = guardianLinksForPeople(db, personIDs)
	var households []models.Household
	if len(householdIDs) > 0 {
		db.Where("id IN ?", householdIDs).Find(&households)
	}
	for _, h := range households {
		f.households[h.ID] = h
	}
	var users []models.User
	if len(userIDs) > 0 {
		db.Where("id IN ?", userIDs).Find(&users)
	}
	for i := range users {
		f.users[users[i].ID] = &users[i]
	}
	var accts []models.HouseholdAccount
	if len(userIDs) > 0 {
		db.Where("user_id IN ?", userIDs).Find(&accts)
	}
	for _, acct := range accts {
		f.accounts[acct.UserID] = acct
	}
	return f
}

func (f subjectFacts) relation(viewer *models.User, person *models.HouseholdPerson, manager bool) policy.HouseholdRel {
	rel := policy.HouseholdRel{EventManager: manager}
	if viewer == nil {
		return rel
	}
	acct, found := f.accounts[viewer.ID]
	own := householdRelOf(acct, found && acct.HouseholdID == person.HouseholdID)
	rel.Account, rel.Adult = own.Account, own.Adult
	for _, link := range f.links[person.ID] {
		if link.GuardianUserID == viewer.ID && link.Status == models.GuardianConfirmed {
			rel.Guardian = true
		}
	}
	return rel
}

func (f subjectFacts) attendee(r models.Registration, o models.EventOccurrence, me *models.User, manager, childcare bool, now time.Time) *attendeeJSON {
	holder := f.users[registrationHolder(r)]
	if holder == nil {
		return nil
	}
	out := attendeeJSON{RegistrationID: r.ID, UserID: r.UserID, Name: holder.PublicName(), Status: r.Status, GuestCount: r.GuestCount, AttendanceMode: attendanceModeOf(r.AttendanceMode)}
	if childcare {
		out.ChildcareCount = &r.ChildcareCount
		out.ChildcareAgesNote = &r.ChildcareAgesNote
	}
	if r.HouseholdPersonID == nil {
		return &out
	}
	person := f.people[*r.HouseholdPersonID]
	if person == nil || person.ConvertedUserID != nil {
		return nil
	}
	rel := f.relation(me, person, manager)
	facts := householdFacts(person, f.links[person.ID], now)
	if !policy.CanSeeHouseholdPerson(asPerson(me), facts, rel) {
		return nil
	}
	out.Name = person.FullName()
	hp := &householdAttendeeJSON{PersonID: person.ID, Name: person.FullName(), HouseholdName: f.households[person.HouseholdID].Name, Kind: person.Kind, RegisteredBy: registeredByJSON{UserID: holder.ID, Name: holder.PublicName()}}
	if person.BirthDate != nil {
		loc, err := time.LoadLocation(o.Event.TZID)
		if err != nil {
			loc = time.UTC
		}
		age := accounts.AgeOn(*person.BirthDate, o.StartsAt.In(loc))
		hp.Age = &age
	}
	careRel := rel
	careRel.EventManager = manager && r.Status == models.RegGoing && o.Status == models.OccurrenceScheduled
	window := &policy.EventWindow{StartsAt: o.StartsAt, EndsAt: o.EndsAt}
	if policy.CanSeeCareNotes(asPerson(me), facts, careRel, window, now) {
		hp.CareNotes = &person.CareNotes
		if !policy.IsAdmin(asPerson(me)) && !rel.Guardian {
			until := o.EndsAt.Add(24 * time.Hour)
			hp.CareNotesVisibleUntil = &until
		}
	}
	if childcare || rel.Adult || rel.Guardian {
		out.ChildcareRequested = &r.ChildcareRequested
	}
	out.HouseholdPerson = hp
	return &out
}

func (a *App) attendeeNames(kept []models.EventOccurrence, show, childcare, managers map[uint]bool, me *models.User) (map[uint][]attendeeJSON, map[uint][]attendeeJSON) {
	out, removed := map[uint][]attendeeJSON{}, map[uint][]attendeeJSON{}
	var ids []uint
	byID := map[uint]models.EventOccurrence{}
	for _, o := range kept {
		if show[o.EventID] || managers[o.EventID] {
			ids = append(ids, o.ID)
			byID[o.ID] = o
		}
	}
	if len(ids) == 0 {
		return out, removed
	}
	var rows []models.Registration
	a.DB.Joins(listedRegistrations()).Where("registrations.occurrence_id IN ? AND registrations.status <> ?", ids, models.RegCancelled).Order("registrations.waitlisted_at ASC NULLS FIRST, registrations.id").Find(&rows)
	facts := loadSubjectFacts(a.DB, rows, me)
	now := time.Now().UTC()
	for _, r := range rows {
		o := byID[r.OccurrenceID]
		// The age uses the occurrence's local calendar date.
		if r.Status == models.RegRemoved && !managers[o.EventID] {
			continue
		}
		dto := facts.attendee(r, o, me, managers[o.EventID], childcare[o.EventID], now)
		if dto == nil {
			continue
		}
		if r.Status == models.RegRemoved {
			removed[o.ID] = append(removed[o.ID], *dto)
		} else if show[o.EventID] {
			out[o.ID] = append(out[o.ID], *dto)
		}
	}
	a.appendRecoverableRegistrations(kept, managers, me, removed, now)
	return out, removed
}

func (a *App) myHouseholdAnswers(occurrenceIDs []uint, viewer *models.User) map[uint][]myHouseholdRegistrationJSON {
	out := map[uint][]myHouseholdRegistrationJSON{}
	if viewer == nil {
		return out
	}
	acct, found := householdAccountIn(a.DB, viewer.ID)
	if !found || !policy.CanManageHousehold(asPerson(viewer), householdRelOf(acct, found)) {
		return out
	}
	var regs []models.Registration
	a.DB.Joins("JOIN household_people ON household_people.id = registrations.household_person_id AND household_people.deleted_at IS NULL AND household_people.converted_user_id IS NULL").Where("household_people.household_id = ? AND registrations.occurrence_id IN ? AND registrations.status <> ?", acct.HouseholdID, occurrenceIDs, models.RegCancelled).Find(&regs)
	facts := loadSubjectFacts(a.DB, regs, viewer)
	for _, r := range regs {
		person := facts.people[*r.HouseholdPersonID]
		holder := facts.users[registrationHolder(r)]
		if person == nil || !policy.CanSeeHouseholdPerson(asPerson(viewer), householdFacts(person, facts.links[person.ID], time.Now().UTC()), facts.relation(viewer, person, false)) {
			continue
		}
		unavailable := holder == nil || holder.Status != models.UserActive
		name := "Member"
		if holder != nil {
			name = holder.PublicName()
		}
		status := r.Status
		if unavailable {
			status = "unavailable"
		}
		var pos *int
		if !unavailable && status == models.RegWaitlisted {
			n, err := waitlistPosition(a.DB, r.OccurrenceID, r.ID)
			if err == nil {
				pos = &n
			}
		}
		out[r.OccurrenceID] = append(out[r.OccurrenceID], myHouseholdRegistrationJSON{RegistrationID: r.ID, PersonID: person.ID, RegisteredBy: registrationHolder(r), HolderName: name, Status: status, AttendanceMode: r.AttendanceMode, WaitlistPosition: pos, ChildcareRequested: r.ChildcareRequested, Unavailable: unavailable})
	}
	return out
}

func householdGoingMap(db *gorm.DB, occurrenceIDs []uint, viewer *models.User, remoteOnly bool) map[uint]bool {
	out := map[uint]bool{}
	if viewer == nil || len(occurrenceIDs) == 0 {
		return out
	}
	var ids []uint
	q := db.Model(&models.Registration{}).Joins(listedRegistrations()).Where("registrations.registered_by = ? AND registrations.occurrence_id IN ? AND registrations.status = ?", viewer.ID, occurrenceIDs, models.RegGoing)
	if remoteOnly {
		q = q.Where("registrations.attendance_mode = ?", models.AttendanceRemote)
	}
	q.Pluck("registrations.occurrence_id", &ids)
	for _, id := range ids {
		out[id] = true
	}
	return out
}

// Recovery data comes only from this event's audited dropped rows. It is
// revalidated for the manager and holder before exposing any person facts.
func (a *App) appendRecoverableRegistrations(occs []models.EventOccurrence, managers map[uint]bool, me *models.User, out map[uint][]attendeeJSON, now time.Time) {
	if me == nil {
		return
	}
	var eventIDs []uint
	byOcc := map[uint]models.EventOccurrence{}
	for _, o := range occs {
		if managers[o.EventID] {
			eventIDs = append(eventIDs, o.EventID)
			byOcc[o.ID] = o
		}
	}
	if len(eventIDs) == 0 {
		return
	}
	var logs []models.AuditLog
	a.DB.Where("action = ? AND target_type = 'event' AND target_id IN ?", audit.RegistrationNotRestored, eventIDs).Order("id DESC").Find(&logs)
	var regs []models.Registration
	seen := map[[2]uint]bool{}
	for _, log := range logs {
		var meta struct {
			RegistrationID uint   `json:"registrationId"`
			PersonID       *uint  `json:"personId"`
			UserID         *uint  `json:"userId"`
			RegisteredBy   *uint  `json:"registeredBy"`
			OccurrenceID   uint   `json:"occurrenceId"`
			AttendanceMode string `json:"attendanceMode"`
		}
		if json.Unmarshal([]byte(log.MetaJSON), &meta) != nil {
			continue
		}
		o, ok := byOcc[meta.OccurrenceID]
		if !ok || o.EventID != log.TargetID {
			continue
		}
		r := models.Registration{ID: meta.RegistrationID, OccurrenceID: o.ID, UserID: meta.UserID, HouseholdPersonID: meta.PersonID, RegisteredBy: meta.RegisteredBy, Status: "not_restored", AttendanceMode: attendanceModeOf(meta.AttendanceMode)}
		subject, err := subjectOf(r)
		if err != nil {
			continue
		}
		// Existing ordinary-user audit rows have no registration ID; the email
		// add path remains their recovery control.
		if r.HouseholdPersonID == nil {
			continue
		}
		key := [2]uint{o.ID, *r.HouseholdPersonID}
		if seen[key] {
			continue
		}
		seen[key] = true
		var n int64
		subject.query(a.DB, o.ID).Model(&models.Registration{}).Count(&n)
		if n != 0 {
			continue
		}
		var holder models.User
		if a.DB.First(&holder, subject.holderID).Error != nil || holder.Status != models.UserActive {
			continue
		}
		if !a.eligibleRegistration(a.DB, r, &o.Event, now) {
			continue
		}
		regs = append(regs, r)
	}
	facts := loadSubjectFacts(a.DB, regs, me)
	for _, r := range regs {
		o := byOcc[r.OccurrenceID]
		dto := facts.attendee(r, o, me, true, false, now)
		if dto != nil {
			out[o.ID] = append(out[o.ID], *dto)
		}
	}
}
