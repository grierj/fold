package main

import (
	"fold/internal/policy"
	"fold/models"
	"time"
)

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
	var own []models.Registration
	if err := a.DB.Select("occurrence_id", "status").Where(
		"user_id = ? AND occurrence_id IN ? AND status IN ?", viewer.ID, occurrenceIDs,
		[]string{models.RegGoing, models.RegMaybe, models.RegWaitlisted, models.RegDeclined},
	).Find(&own).Error; err != nil {
		return out, err
	}
	for _, row := range own {
		out.ownStatus[row.OccurrenceID] = row.Status
	}
	var householdIDs []uint
	if err := a.DB.Model(&models.Registration{}).Joins(listedRegistrations()).
		Where("registrations.registered_by = ? AND registrations.household_person_id IS NOT NULL AND registrations.occurrence_id IN ? AND registrations.status IN ?", viewer.ID, occurrenceIDs,
			[]string{models.RegGoing, models.RegMaybe, models.RegWaitlisted}).
		Distinct().Pluck("registrations.occurrence_id", &householdIDs).Error; err != nil {
		return out, err
	}
	for _, id := range householdIDs {
		out.household[id] = true
	}
	return out, nil
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
