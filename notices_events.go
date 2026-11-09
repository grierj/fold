package main

import (
	"fmt"

	"fold/internal/notify"
	"fold/models"

	"gorm.io/gorm"
)

// notifyOccurrences tells everyone going or waitlisted on these dates that
// the date was cancelled (OccurrenceCancelled; M1.5: maybe no longer gets one)
// or, after a series edit had cancelled it, is back on (OccurrenceRestored;
// their registrations were never touched, so that notice says the sign-up is
// still there). A registrant who can't see the group gets no group name,
// place, or link (PR #41). One stored item per person per date; each archives
// itself when the date would have ended.
func notifyOccurrences(tx *gorm.DB, actor uint, occIDs []uint, kind string) error {
	if len(occIDs) == 0 {
		return nil
	}
	var rows []struct {
		UserID uint
		OccID  uint
	}
	if err := tx.Table("registrations").Joins(listedRegistrations()).Distinct("users.id AS user_id, registrations.occurrence_id AS occ_id").
		Where("registrations.occurrence_id IN ? AND registrations.status IN ?", occIDs,
			[]string{models.RegGoing, models.RegWaitlisted}).
		Order("occ_id, user_id").Scan(&rows).Error; err != nil {
		return err
	}
	if len(rows) == 0 {
		return nil
	}
	// Load the occurrences, their events and every recipient's facts
	// once (issue #76); the loop below only writes notices.
	var occRows []models.EventOccurrence
	if err := tx.Where("id IN ?", occIDs).Find(&occRows).Error; err != nil {
		return err
	}
	occByID := make(map[uint]*models.EventOccurrence, len(occRows))
	eventIDs := make([]uint, 0, len(occRows))
	for i := range occRows {
		occByID[occRows[i].ID] = &occRows[i]
		eventIDs = append(eventIDs, occRows[i].EventID)
	}
	evByID := map[uint]*models.Event{}
	var groupIDs []uint
	if len(eventIDs) > 0 {
		var evRows []models.Event
		if err := tx.Where("id IN ?", eventIDs).Find(&evRows).Error; err != nil {
			return err
		}
		for i := range evRows {
			evByID[evRows[i].ID] = &evRows[i]
			if evRows[i].GroupID != nil {
				groupIDs = append(groupIDs, *evRows[i].GroupID)
			}
		}
	}
	userIDs := make([]uint, 0, len(rows))
	for _, r := range rows {
		userIDs = append(userIDs, r.UserID)
	}
	facts, err := loadNoticeFacts(tx, groupIDs, userIDs)
	if err != nil {
		return err
	}
	accesses, err := occurrenceNoticeAccesses(tx, occIDs)
	if err != nil {
		return err
	}
	// One bulk lock for the whole fan-out (ascending user ID), rather than a
	// lock query inside each per-person Send below.
	return notify.WithRecipients(tx, userIDs, func(scope *notify.Scope) error {
		for _, r := range rows {
			o := occByID[r.OccID]
			if o == nil {
				continue
			}
			ev := evByID[o.EventID]
			if ev == nil {
				continue
			}
			if _, ok := accesses[o.ID][r.UserID]; ok {
				// Lifecycle reconciliation owns the single invitation-ended notice.
				// Ordinary registrants still receive the cancellation notice below.
				continue
			}
			vars, link := eventNoticeVars(ev, o, r.UserID, facts)
			end := o.EndsAt
			if end.IsZero() {
				end = o.StartsAt
			}
			_, err := notify.Send(tx, notify.Msg{
				Kind: kind, To: []uint{r.UserID}, Actor: &actor,
				SubjectType: "occurrence", SubjectID: o.ID, Link: link, Vars: vars,
				Data: map[string]any{"eventId": ev.ID, "date": models.FormatCalendarDate(o.OriginalDate)},
				// A date can be cancelled, restored and cancelled again, so the key
				// carries the moment of this change (the row was just updated).
				DedupeKey: fmt.Sprintf("%s:%d:%d", kind, o.ID, o.UpdatedAt.UnixNano()), ExpiresAt: &end,
				Scope: scope,
			})
			if err != nil {
				return err
			}
		}
		return nil
	})
}

// notifyRegistrationCancelled tells someone who can no longer see an event
// that their registrations were cancelled. It names neither the group nor
// the place and links nowhere private (PR #41).
func notifyRegistrationCancelled(tx *gorm.DB, actor uint, ev *models.Event, userID uint) error {
	u, err := userIn(tx, userID)
	if err != nil || u == nil || u.Status == models.UserDisabled {
		return err
	}
	_, err = notify.Send(tx, notify.Msg{
		Kind: notify.RegistrationCancelled, To: []uint{userID}, Actor: &actor,
		SubjectType: "event", SubjectID: ev.ID, Link: "/", Vars: notify.Vars{Event: ev.Title},
	})
	return err
}
