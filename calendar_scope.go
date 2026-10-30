package main

import (
	"fold/internal/policy"
	"fold/models"
	"time"

	"gorm.io/gorm"
)

// lookupChunk caps the ids in one IN list, well under PostgreSQL's 65535
// bind parameters.
const lookupChunk = 5000

// eachIDChunk calls fn on slices of at most lookupChunk ids.
func eachIDChunk(ids []uint, fn func([]uint) error) error {
	for len(ids) > 0 {
		n := min(len(ids), lookupChunk)
		if err := fn(ids[:n]); err != nil {
			return err
		}
		ids = ids[n:]
	}
	return nil
}

// visibleOccurrenceIDs is the scheduled dates this viewer may see, either by
// ordinary access or through an exact-date grant. Cancelled and hidden dates
// never reach a per-date lookup.
func visibleOccurrenceIDs(vs visibleSet, viewer *models.User, occs []models.EventOccurrence, scoped map[uint]*occurrenceRead) []uint {
	ids := make([]uint, 0, len(occs))
	for i := range occs {
		occ := &occs[i]
		if occ.Status != models.OccurrenceScheduled {
			continue
		}
		if scoped[occ.ID] != nil || vs.canSeeDate(viewer, vs.ByID[occ.EventID], occ) {
			ids = append(ids, occ.ID)
		}
	}
	return ids
}

// householdGoingChunked is householdGoingMap over lists of any length.
func householdGoingChunked(db *gorm.DB, ids []uint, viewer *models.User, remoteOnly bool) (map[uint]bool, error) {
	out := map[uint]bool{}
	err := eachIDChunk(ids, func(chunk []uint) error {
		part, err := householdGoingMap(db, chunk, viewer, remoteOnly)
		for id := range part {
			out[id] = true
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// calendarScopeBatch holds only the viewer's own facts. Household rows are a
// separate boolean on purpose: they put a date in scope without becoming the
// viewer's RSVP state.
type calendarScopeBatch struct {
	ownStatus map[uint]string
	household map[uint]bool
}

func (a *App) loadCalendarScopeBatch(viewer *models.User, occurrenceIDs []uint) (calendarScopeBatch, error) {
	out := calendarScopeBatch{ownStatus: map[uint]string{}, household: map[uint]bool{}}
	if viewer == nil || len(occurrenceIDs) == 0 {
		return out, nil
	}
	err := eachIDChunk(occurrenceIDs, func(chunk []uint) error {
		var own []models.Registration
		if err := a.DB.Select("occurrence_id", "status").Where(
			"user_id = ? AND occurrence_id IN ? AND status IN ?", viewer.ID, chunk,
			[]string{models.RegGoing, models.RegMaybe, models.RegWaitlisted, models.RegDeclined},
		).Find(&own).Error; err != nil {
			return err
		}
		for _, row := range own {
			out.ownStatus[row.OccurrenceID] = row.Status
		}
		var householdIDs []uint
		if err := a.DB.Model(&models.Registration{}).Joins(listedRegistrations()).
			Where("registrations.registered_by = ? AND registrations.household_person_id IS NOT NULL AND registrations.occurrence_id IN ? AND registrations.status IN ?", viewer.ID, chunk,
				[]string{models.RegGoing, models.RegMaybe, models.RegWaitlisted}).
			Distinct().Pluck("registrations.occurrence_id", &householdIDs).Error; err != nil {
			return err
		}
		for _, id := range householdIDs {
			out.household[id] = true
		}
		return nil
	})
	return out, err
}

func calendarScopeDecision(scope policy.CalendarScope, authorized bool, ev *models.Event, occ *models.EventOccurrence, rel policy.Rel, hosted, household, exact bool, ownStatus string, now time.Time) policy.CalendarDecision {
	publicDate := false
	churchWide := false
	if ev != nil {
		churchWide = ev.GroupID == nil
		publicDate = policy.EffectiveVisibility(ev.Visibility, occ.VisibilityOverride) == models.VisibilityPublic
	}
	return policy.DecideCalendar(scope, policy.CalendarFacts{
		Authorized: authorized, GroupMember: rel.Member, ChurchWide: churchWide,
		Host: hosted, OwnStatus: ownStatus, HouseholdDate: household,
		ExactDate: exact, PublicDate: publicDate, StartsAt: occ.StartsAt,
	}, now)
}
