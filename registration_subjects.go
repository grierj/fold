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

func householdFacts(person *models.HouseholdPerson, links []models.GuardianLink, now time.Time) policy.HouseholdPerson {
	status, _ := effectiveLink(links)
	out := policy.HouseholdPerson{Kind: person.Kind, LinkStatus: status, Deleted: person.DeletedAt.Valid, Converted: person.ConvertedUserID != nil}
	if person.BirthDate != nil {
		age := accounts.AgeOn(*person.BirthDate, now)
		out.AgeYears = &age
	}
	return out
}

// eligibleRegistration reads current eligibility without taking earlier-order
// holder/household locks after the caller has locked an occurrence.
func (a *App) eligibleRegistration(tx *gorm.DB, r models.Registration, ev *models.Event, now time.Time) (bool, error) {
	f, err := a.loadEligibilityFacts(tx, []models.Registration{r}, now, nil)
	if err != nil {
		return false, err
	}
	return f.eligible(a, tx, r, ev, map[uint]bool{}, now)
}

// loadEligibilityFacts loads what eligible reads for regs and pins the household
// clock to the church-local date at now. holder, when given, stands in for that
// account's stored row (a holder who is about to be re-enabled).
func (a *App) loadEligibilityFacts(tx *gorm.DB, regs []models.Registration, now time.Time, holder *models.User) (*subjectFacts, error) {
	today, err := churchClockIn(tx, now)
	if err != nil {
		return nil, err
	}
	f := newSubjectFacts(today)
	if err := f.load(tx, regs, holder); err != nil {
		return nil, err
	}
	if holder != nil {
		f.users[holder.ID] = holder
	}
	return f, nil
}

func (f *subjectFacts) eligible(a *App, tx *gorm.DB, r models.Registration, ev *models.Event, access map[uint]bool, now time.Time) (bool, error) {
	holder := f.users[registrationHolder(r)]
	if holder == nil || holder.Status == models.UserDisabled {
		return false, nil
	}
	allowed, loaded := access[holder.ID]
	if !loaded {
		allowed = a.canRegisterDateIn(tx, holder, ev, r.OccurrenceID)
		access[holder.ID] = allowed
	}
	if !allowed && r.UserID != nil && r.ID != 0 {
		var grant models.Registration
		if err := tx.Where("registrations.id = ? AND "+registrationAccessLiveSQL, r.ID).Limit(1).Find(&grant).Error; err != nil {
			return false, err
		}
		if grant.ID != 0 {
			live, err := liveOccurrenceInvitationIn(tx, grant, now)
			if err != nil {
				return false, err
			}
			allowed = live
		}
	}
	if !allowed || remoteUnavailable(ev, r.AttendanceMode) {
		return false, nil
	}
	if r.HouseholdPersonID == nil {
		return true, nil
	}
	person := f.people[*r.HouseholdPersonID]
	if person == nil || f.householdProblem(holder, person, allowed) != "" {
		return false, nil
	}
	return !r.ChildcareRequested || (person.Kind == models.HouseholdChild && r.AttendanceMode != models.AttendanceRemote), nil
}

// householdHolderOf is the responsible adult of a household registration as
// policy reads it. found says whether acct is the holder's household account;
// it only counts when it belongs to the person's household. access is whether
// the holder may register for the event.
func householdHolderOf(holder *models.User, acct models.HouseholdAccount, found bool, person *models.HouseholdPerson, access bool) policy.HouseholdHolder {
	return policy.HouseholdHolder{
		Person: asPerson(holder),
		Rel:    householdRelOf(acct, found && acct.HouseholdID == person.HouseholdID),
		Active: holder.CanSignIn(),
		Access: access,
	}
}

// householdProblem is why person cannot stand under holder, or "". It is the one
// reading of the household rule for eligible and for the manage link; the
// listedRegistrations SQL only pre-filters by the same holder and person facts
// (TestListedRegistrationsPrefilterMatchesPolicy).
func (f *subjectFacts) householdProblem(holder *models.User, person *models.HouseholdPerson, access bool) string {
	acct, found := f.accounts[holder.ID]
	return policy.CanRegisterHouseholdPerson(householdHolderOf(holder, acct, found, person, access), householdFacts(person, f.links[person.ID], f.day()))
}

func householdPersonName(tx *gorm.DB, id uint) (string, error) {
	var p models.HouseholdPerson
	if err := tx.First(&p, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "Household member", nil
		}
		return "", err
	}
	return p.FullName(), nil
}

// reconcileHouseholdPerson runs while the household is locked, and locks
// affected dates in ID order before cancelling or clearing invalid requests.
func (a *App) reconcileHouseholdPerson(tx *gorm.DB, person *models.HouseholdPerson, deleting bool, actor *uint, ip string) error {
	var regs []models.Registration
	now := time.Now().UTC()
	if err := tx.Joins("JOIN event_occurrences ON event_occurrences.id = registrations.occurrence_id").
		Where("registrations.household_person_id = ? AND event_occurrences.starts_at > ? AND event_occurrences.status = ? AND registrations.status IN ?", person.ID, now, models.OccurrenceScheduled, models.CurrentRegStatuses).Order("registrations.occurrence_id, registrations.id").Find(&regs).Error; err != nil {
		return err
	}
	for _, r := range regs {
		o, err := lockOccurrence(tx, r.OccurrenceID)
		if err != nil {
			return err
		}
		if err := tx.First(&r, r.ID).Error; err != nil {
			return err
		}
		var ev models.Event
		if err := tx.First(&ev, o.EventID).Error; err != nil {
			return err
		}
		old := r
		eligible := false
		if !deleting {
			var err error
			if eligible, err = a.eligibleRegistration(tx, r, &ev, now); err != nil {
				return err
			}
		}
		if !eligible {
			// Changing away from child clears care without cancelling an otherwise
			// eligible confirmed teen or verified adult's seat.
			candidate := r
			candidate.ChildcareRequested = false
			careOK := false
			if !deleting {
				var err error
				if careOK, err = a.eligibleRegistration(tx, candidate, &ev, now); err != nil {
					return err
				}
			}
			if careOK {
				r.ChildcareRequested = false
			} else {
				r.Status = models.RegCancelled
			}
		}
		if r.Status == old.Status && r.ChildcareRequested == old.ChildcareRequested {
			continue
		}
		if err := saveRegistration(tx, &r); err != nil {
			return err
		}
		if err := auditRegistration(tx, actor, audit.RegistrationChanged, ev.ID, old, r, ip); err != nil {
			return err
		}
		if r.Status == models.RegCancelled {
			if err := notifyRegistrationRemoved(tx, &ev, o, r, "This household person's details changed. Check their profile before signing them up again.", actor); err != nil {
				return err
			}
		}
		if old.Status == models.RegGoing {
			if err := a.promoteLocked(tx, &ev, o, now, actor); err != nil {
				return err
			}
		}
	}
	return nil
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
	// loadedAccounts marks users whose household account was looked up,
	// found or not.
	loadedAccounts map[uint]bool
	// today is the one clock for household ages
	// (the church-local date). Eligibility and the displayed age both use it.
	today time.Time
	// clock supplies today on first use, so a request that shows no
	// household person never reads the church time zone.
	clock func() time.Time
}

func (f *subjectFacts) day() time.Time {
	if f.today.IsZero() && f.clock != nil {
		f.today = f.clock()
	}
	return f.today
}

func newSubjectFacts(today time.Time) *subjectFacts {
	return &subjectFacts{people: map[uint]*models.HouseholdPerson{}, links: map[uint][]models.GuardianLink{}, households: map[uint]models.Household{}, users: map[uint]*models.User{}, accounts: map[uint]models.HouseholdAccount{}, loadedAccounts: map[uint]bool{}, today: today}
}

func (a *App) requestFacts() *subjectFacts {
	f := newSubjectFacts(time.Time{})
	f.clock = func() time.Time { return time.Now().UTC().In(churchZoneOrUTC(a.DB)) }
	return f
}

// load adds the facts for regs and viewer that aren't loaded yet, so one
// request can share a single set of facts across its lists.
func (f *subjectFacts) load(db *gorm.DB, regs []models.Registration, viewer *models.User) error {
	var personIDs, userIDs, householdIDs []uint
	seenUser := map[uint]bool{}
	addUser := func(id uint) {
		if _, ok := f.users[id]; !ok && !seenUser[id] {
			seenUser[id] = true
			userIDs = append(userIDs, id)
		}
	}
	if viewer != nil {
		addUser(viewer.ID)
	}
	seenPerson := map[uint]bool{}
	for _, r := range regs {
		addUser(registrationHolder(r))
		if r.HouseholdPersonID != nil {
			if _, ok := f.people[*r.HouseholdPersonID]; !ok && !seenPerson[*r.HouseholdPersonID] {
				seenPerson[*r.HouseholdPersonID] = true
				personIDs = append(personIDs, *r.HouseholdPersonID)
			}
		}
	}
	if len(personIDs) > 0 {
		var people []models.HouseholdPerson
		if err := db.Where("id IN ?", personIDs).Find(&people).Error; err != nil {
			return err
		}
		seenHousehold := map[uint]bool{}
		for i := range people {
			p := &people[i]
			f.people[p.ID] = p
			if _, ok := f.households[p.HouseholdID]; !ok && !seenHousehold[p.HouseholdID] {
				seenHousehold[p.HouseholdID] = true
				householdIDs = append(householdIDs, p.HouseholdID)
			}
		}
		links, err := loadGuardianLinks(db, personIDs)
		if err != nil {
			return err
		}
		for id, l := range links {
			f.links[id] = l
		}
	}
	if len(householdIDs) > 0 {
		var households []models.Household
		if err := db.Where("id IN ?", householdIDs).Find(&households).Error; err != nil {
			return err
		}
		for _, h := range households {
			f.households[h.ID] = h
		}
	}
	if len(userIDs) > 0 {
		var users []models.User
		if err := db.Where("id IN ?", userIDs).Find(&users).Error; err != nil {
			return err
		}
		for i := range users {
			f.users[users[i].ID] = &users[i]
		}
		var accts []models.HouseholdAccount
		if err := db.Where("user_id IN ?", userIDs).Find(&accts).Error; err != nil {
			return err
		}
		for _, acct := range accts {
			f.accounts[acct.UserID] = acct
		}
		for _, id := range userIDs {
			f.loadedAccounts[id] = true
		}
	}
	return nil
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

func (f *subjectFacts) attendee(r models.Registration, o models.EventOccurrence, me *models.User, manager, childcare bool, now time.Time) *attendeeJSON {
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
		if holder.AccountType == models.AccountTeen {
			viewerAccount, viewerFound := models.HouseholdAccount{}, false
			if me != nil {
				viewerAccount, viewerFound = f.accounts[me.ID]
			}
			holderAccount, holderFound := f.accounts[holder.ID]
			rel := householdRelOf(viewerAccount, viewerFound && holderFound && viewerAccount.HouseholdID == holderAccount.HouseholdID)
			if !policy.CanSeePendingTeenAttendee(asPerson(me), rel, manager) {
				return nil
			}
		}
		// The teen badge is for leaders and admins only (DEC-82).
		if manager || policy.IsAdmin(asPerson(me)) {
			out.AccountType = holder.AccountType
		}
		return &out
	}
	person := f.people[*r.HouseholdPersonID]
	if person == nil || person.ConvertedUserID != nil {
		return nil
	}
	rel := f.relation(me, person, manager)
	facts := householdFacts(person, f.links[person.ID], f.day())
	if !policy.CanSeeHouseholdPerson(asPerson(me), facts, rel) {
		return nil
	}
	out.Name = person.FullName()
	hp := &householdAttendeeJSON{PersonID: person.ID, Name: person.FullName(), HouseholdName: f.households[person.HouseholdID].Name, Kind: person.Kind, RegisteredBy: registeredByJSON{UserID: holder.ID, Name: holder.PublicName()}}
	if person.BirthDate != nil {
		age := accounts.AgeOn(*person.BirthDate, f.day())
		hp.Age = &age
	}
	careRel := rel
	careRel.EventManager = manager && r.Status == models.RegGoing && o.Status == models.OccurrenceScheduled
	window := &policy.EventWindow{StartsAt: o.StartsAt, EndsAt: o.EndsAt}
	if r.Status == models.RegGoing && o.Status == models.OccurrenceScheduled && policy.CanSeeCareNotes(asPerson(me), facts, careRel, window, now) {
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

func (a *App) attendeeNames(kept []models.EventOccurrence, show, childcare, managers map[uint]bool, me *models.User, facts *subjectFacts) (map[uint][]attendeeJSON, map[uint][]attendeeJSON, error) {
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
		return out, removed, nil
	}
	var rows []struct {
		models.Registration
		LeaderAdded bool
	}
	// The leader-added flag is the row's own live manager-add provenance: no
	// second table, so the page's dates in the outer filter are the only bound.
	if err := a.DB.Model(&models.Registration{}).Select("registrations.*, (registrations.access_origin = ? AND registrations.access_granted_at IS NOT NULL AND registrations.access_ended_at IS NULL) AS leader_added", policy.OccurrenceOriginManagerAdd).Joins(listedRegistrations()).Where("registrations.occurrence_id IN ? AND registrations.status <> ?", ids, models.RegCancelled).Order("registrations.waitlisted_at ASC NULLS FIRST, registrations.id").Find(&rows).Error; err != nil {
		return nil, nil, err
	}
	registrations := make([]models.Registration, 0, len(rows))
	for _, row := range rows {
		registrations = append(registrations, row.Registration)
	}
	if err := facts.load(a.DB, registrations, me); err != nil {
		return nil, nil, err
	}
	now := time.Now().UTC()
	for _, row := range rows {
		r := row.Registration
		o := byID[r.OccurrenceID]
		if r.Status == models.RegRemoved && !managers[o.EventID] {
			continue
		}
		dto := facts.attendee(r, o, me, managers[o.EventID], childcare[o.EventID], now)
		if dto == nil {
			continue
		}
		if managers[o.EventID] {
			added := row.LeaderAdded
			dto.AddedByLeader = &added
		}
		if r.Status == models.RegRemoved {
			removed[o.ID] = append(removed[o.ID], *dto)
		} else if show[o.EventID] {
			out[o.ID] = append(out[o.ID], *dto)
		}
	}
	if err := a.appendRecoverableRegistrations(kept, managers, me, removed, now, facts); err != nil {
		return nil, nil, err
	}
	return out, removed, nil
}

func (a *App) myHouseholdAnswers(occurrenceIDs []uint, viewer *models.User, facts *subjectFacts) (map[uint][]myHouseholdRegistrationJSON, error) {
	out := map[uint][]myHouseholdRegistrationJSON{}
	if viewer == nil {
		return out, nil
	}
	if err := facts.load(a.DB, nil, viewer); err != nil {
		return nil, err
	}
	acct, found := facts.accounts[viewer.ID]
	if !found || !policy.CanManageHousehold(asPerson(viewer), householdRelOf(acct, found)) {
		return out, nil
	}
	var regs []models.Registration
	if err := a.DB.Joins("JOIN household_people ON household_people.id = registrations.household_person_id AND household_people.deleted_at IS NULL AND household_people.converted_user_id IS NULL").Where("household_people.household_id = ? AND registrations.occurrence_id IN ? AND registrations.status <> ?", acct.HouseholdID, occurrenceIDs, models.RegCancelled).Find(&regs).Error; err != nil {
		return nil, err
	}
	if err := facts.load(a.DB, regs, viewer); err != nil {
		return nil, err
	}
	for _, r := range regs {
		person := facts.people[*r.HouseholdPersonID]
		holder := facts.users[registrationHolder(r)]
		if person == nil || !policy.CanSeeHouseholdPerson(asPerson(viewer), householdFacts(person, facts.links[person.ID], facts.day()), facts.relation(viewer, person, false)) {
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
			if err != nil {
				return nil, err
			}
			pos = &n
		}
		out[r.OccurrenceID] = append(out[r.OccurrenceID], myHouseholdRegistrationJSON{RegistrationID: r.ID, PersonID: person.ID, RegisteredBy: registrationHolder(r), HolderName: name, Status: status, AttendanceMode: r.AttendanceMode, WaitlistPosition: pos, ChildcareRequested: r.ChildcareRequested, Unavailable: unavailable})
	}
	return out, nil
}

func householdGoingMap(db *gorm.DB, occurrenceIDs []uint, viewer *models.User, remoteOnly bool) (map[uint]bool, error) {
	out := map[uint]bool{}
	if viewer == nil || len(occurrenceIDs) == 0 {
		return out, nil
	}
	var ids []uint
	q := db.Model(&models.Registration{}).Joins(listedRegistrations()).Where("registrations.registered_by = ? AND registrations.occurrence_id IN ? AND registrations.status = ?", viewer.ID, occurrenceIDs, models.RegGoing)
	if remoteOnly {
		q = q.Where("registrations.attendance_mode = ?", models.AttendanceRemote)
	}
	if err := q.Pluck("registrations.occurrence_id", &ids).Error; err != nil {
		return nil, err
	}
	for _, id := range ids {
		out[id] = true
	}
	return out, nil
}

// Recovery data comes only from this event's audited dropped rows. It is
// revalidated for the manager and holder before exposing any person facts.
func (a *App) appendRecoverableRegistrations(occs []models.EventOccurrence, managers map[uint]bool, me *models.User, out map[uint][]attendeeJSON, now time.Time, facts *subjectFacts) error {
	if me == nil {
		return nil
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
		return nil
	}
	var logs []models.AuditLog
	if err := a.DB.Where("action = ? AND target_type = 'event' AND target_id IN ?", audit.RegistrationNotRestored, eventIDs).Order("id DESC").Find(&logs).Error; err != nil {
		return err
	}
	var regs []models.Registration
	seen := map[[2]uint]bool{}
	for _, log := range logs {
		var meta droppedRegistrationMeta
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
		if err := subject.query(a.DB, o.ID).Model(&models.Registration{}).Count(&n).Error; err != nil {
			return err
		}
		if n != 0 {
			continue
		}
		var holder models.User
		if err := a.DB.Limit(1).Find(&holder, subject.holderID).Error; err != nil {
			return err
		}
		if holder.ID == 0 || holder.Status != models.UserActive {
			continue
		}
		// A page that cannot read the church zone leaves the row out.
		if ok, err := a.eligibleRegistration(a.DB, r, &o.Event, now); err != nil || !ok {
			continue
		}
		regs = append(regs, r)
	}
	if err := facts.load(a.DB, regs, me); err != nil {
		return err
	}
	for _, r := range regs {
		o := byOcc[r.OccurrenceID]
		dto := facts.attendee(r, o, me, true, false, now)
		if dto != nil {
			out[o.ID] = append(out[o.ID], *dto)
		}
	}
	return nil
}

func householdSubjectFrom(person *models.HouseholdPerson, acct models.HouseholdAccount) (registrationSubject, error) {
	if person == nil || person.ID == 0 || person.DeletedAt.Valid || person.ConvertedUserID != nil || acct.UserID == 0 || acct.HouseholdID != person.HouseholdID {
		return registrationSubject{}, errRegNotFound
	}
	id := person.ID
	return registrationSubject{personID: &id, holderID: acct.UserID}, nil
}

type attendeeJSON struct {
	AddedByLeader      *bool                  `json:"addedByLeader,omitempty"`
	Email              *string                `json:"email,omitempty"`
	Phone              *string                `json:"phone,omitempty"`
	AccountType        string                 `json:"accountType,omitempty"`
	RegistrationID     uint                   `json:"registrationId"`
	HouseholdPerson    *householdAttendeeJSON `json:"householdPerson,omitempty"`
	ChildcareRequested *bool                  `json:"childcareRequested,omitempty"`
	UserID             *uint                  `json:"userId,omitempty"`
	Name               string                 `json:"name"`
	Status             string                 `json:"status"`
	// GuestCount is the extra unnamed guests this party brings (M1.5).
	GuestCount int `json:"guestCount"`
	// AttendanceMode is in_person or remote (M1.8 slice 2).
	AttendanceMode    string  `json:"attendanceMode"`
	ChildcareCount    *int    `json:"childcareCount,omitempty"`
	ChildcareAgesNote *string `json:"childcareAgesNote,omitempty"`
}
