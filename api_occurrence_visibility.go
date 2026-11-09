package main

import (
	"encoding/json"
	"errors"
	"net/http"

	"fold/internal/audit"
	"fold/internal/policy"
	"fold/models"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"strconv"
	"time"
)

// canSeeDate is the ordinary, batched date filter. Candidate parents are never
// series authority: callers must separately use eventCandidates/loadVisibleEvent.
func (vs visibleSet) canSeeDate(viewer *models.User, ev *models.Event, occ *models.EventOccurrence) bool {
	if ev == nil {
		return false
	}
	f := vs.of(ev)
	if ev.GroupID != nil && f.group == nil {
		return false
	}
	f.view.Visibility = policy.EffectiveVisibility(ev.Visibility, occ.VisibilityOverride)
	return f.canSee(viewer)
}

func dateEvent(ev *models.Event, occ *models.EventOccurrence) *models.Event {
	copy := *ev
	copy.Visibility = policy.EffectiveVisibility(ev.Visibility, occ.VisibilityOverride)
	return &copy
}

func (a *App) canRegisterDateIn(tx *gorm.DB, u *models.User, ev *models.Event, occurrenceID uint) bool {
	var occ models.EventOccurrence
	if tx.Where("id = ? AND event_id = ? AND status = ?", occurrenceID, ev.ID, models.OccurrenceScheduled).First(&occ).Error != nil {
		return false
	}
	return a.canRegisterDate(tx, u, ev, &occ)
}

// canRegisterDate is canRegisterDateIn for a caller that already holds the
// occurrence row.
func (a *App) canRegisterDate(tx *gorm.DB, u *models.User, ev *models.Event, occ *models.EventOccurrence) bool {
	if occ.EventID != ev.ID || occ.Status != models.OccurrenceScheduled {
		return false
	}
	return a.canRegisterIn(tx, u, dateEvent(ev, occ))
}

var errDateVisibility = errors.New("invalid date visibility")

func parseDateVisibility(raw json.RawMessage) (*string, bool, error) {
	if len(raw) == 0 {
		return nil, false, nil
	}
	if string(raw) == "null" {
		return nil, true, nil
	}
	var value string
	if json.Unmarshal(raw, &value) != nil {
		return nil, true, errDateVisibility
	}
	if value == "series" {
		return nil, true, nil
	}
	if !policy.ValidVisibility(value) {
		return nil, true, errDateVisibility
	}
	return &value, true, nil
}

func impactHasLosers(impact visibilityImpactJSON) bool {
	for _, row := range impact.Tightened {
		if row.Registrants > 0 {
			return true
		}
	}
	return false
}

func dateImpactOf(tx *gorm.DB, ev *models.Event, occ *models.EventOccurrence, next string) (visibilityImpactJSON, error) {
	impact := visibilityImpactJSON{Visibility: next, Tightened: []impactEventJSON{}, LeaderOpened: []impactEventJSON{}}
	if policy.MoreOpen(policy.EffectiveVisibility(ev.Visibility, occ.VisibilityOverride), next) {
		var count int64
		if err := losersForDate(tx, ev, next, time.Now().UTC(), occ.ID).Count(&count).Error; err != nil {
			return impact, err
		}
		impact.Tightened = append(impact.Tightened, impactEventJSON{ID: ev.ID, Title: ev.Title + " · " + models.FormatCalendarDate(occ.OriginalDate), Visibility: policy.EffectiveVisibility(ev.Visibility, occ.VisibilityOverride), Registrants: count})
	}
	return impact, nil
}

// Caller holds the contact gate, event and occurrence locks. Cancellation,
// audit and stored notices share the same transaction as the date edit.
//
// group is the event's group as it will be once the surrounding change is
// saved (a group edit passes its in-memory copy), or nil to read it. keep
// records the date as deliberately kept open: it audits and notifies as an
// override even though nothing changes.
func (a *App) changeDateVisibility(tx *gorm.DB, actor *models.User, ip string, ev *models.Event, occ *models.EventOccurrence, group *models.Group, value *string, reason string, keep bool) error {
	old := occ.VisibilityOverride
	if !keep && ((old == nil && value == nil) || (old != nil && value != nil && *old == *value)) {
		return nil
	}
	before, after := policy.EffectiveVisibility(ev.Visibility, old), policy.EffectiveVisibility(ev.Visibility, value)

	if group == nil {
		f, err := eventFactsIn(tx, actor, ev)
		if err != nil {
			return err
		}
		group = f.group
	}
	action := audit.VisibilityChanged
	if group != nil && policy.MoreOpen(after, group.Visibility) {
		action = audit.VisibilityOverride
	}
	meta := map[string]any{"original_date": models.FormatCalendarDate(occ.OriginalDate), "oldVisibility": old, "newVisibility": value, "reason": reason}
	if group != nil {
		meta["groupVisibility"] = group.Visibility
	}
	if err := audit.Write(tx, audit.Entry{Actor: &actor.ID, Action: action, TargetType: "event", TargetID: ev.ID, Meta: meta, IP: ip}); err != nil {
		return err
	}
	occ.VisibilityOverride = value
	if err := tx.Model(occ).Update("visibility_override", value).Error; err != nil {
		return err
	}
	if policy.MoreOpen(before, after) && !keep {
		if err := a.cancelDateLosers(tx, actor, ip, ev, after, time.Now().UTC(), occ.ID); err != nil {
			return err
		}
	}
	if action == audit.VisibilityOverride && group != nil {
		date := *ev
		date.Title = ev.Title + " · " + models.FormatCalendarDate(occ.OriginalDate)
		if err := notifyVisibilityOverride(tx, actor.ID, &date, group, after, reason); err != nil {
			return err
		}
	}
	return nil
}

func (a *App) apiDateVisibilityImpact(c *gin.Context) {
	ev, ok := a.loadManagedEvent(c)
	if !ok {
		return
	}
	value, _, err := parseDateVisibility(json.RawMessage(strconv.Quote(c.Query("visibility"))))
	if err != nil {
		fail(c, errVisibilityNotAllowed)
		return
	}
	var occ models.EventOccurrence
	if !isCalendarDate(c.Param("date")) || a.DB.Where("event_id = ? AND original_date = ?", ev.ID, c.Param("date")).First(&occ).Error != nil {
		jsonError(c, http.StatusNotFound, "event not found")
		return
	}
	impact, err := dateImpactOf(a.DB, ev, &occ, policy.EffectiveVisibility(ev.Visibility, value))
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not preview visibility")
		return
	}
	c.JSON(http.StatusOK, impact)
}

func eventImpactOfReset(tx *gorm.DB, ev *models.Event, next string, reset bool) (visibilityImpactJSON, error) {
	impact, err := eventImpactOf(tx, ev, next)
	if err != nil {
		return impact, err
	}
	var dates []models.EventOccurrence
	if err := futureOverrides(tx, ev.ID).Find(&dates).Error; err != nil {
		return impact, err
	}
	impact.FutureOverrides = int64(len(dates))
	if reset {
		for i := range dates {
			date, err := dateImpactOf(tx, ev, &dates[i], next)
			if err != nil {
				return impact, err
			}
			impact.Tightened = append(impact.Tightened, date.Tightened...)
		}
	}
	return impact, nil
}

// Explicit dates are reviewed independently of their parent's opened marker.
func openedDates(tx *gorm.DB, groupID uint, oldVis, next string) ([]models.EventOccurrence, map[uint]*models.Event, error) {
	evs := map[uint]*models.Event{}
	if !policy.MoreOpen(oldVis, next) {
		return nil, evs, nil
	}
	var rows []models.Event
	if err := tx.Where("group_id = ?", groupID).Order("id").Find(&rows).Error; err != nil {
		return nil, nil, err
	}
	ids := []uint{}
	for i := range rows {
		evs[rows[i].ID] = &rows[i]
		ids = append(ids, rows[i].ID)
	}
	if len(ids) == 0 {
		return nil, evs, nil
	}
	var dates []models.EventOccurrence
	if err := futureOverrides(tx, ids...).Order("id").Find(&dates).Error; err != nil {
		return nil, nil, err
	}
	out := []models.EventOccurrence{}
	for _, date := range dates {
		if policy.MoreOpen(*date.VisibilityOverride, next) {
			out = append(out, date)
		}
	}
	return out, evs, nil
}

// futureOverrides selects the upcoming dates of the events that carry their own
// visibility.
func futureOverrides(tx *gorm.DB, eventIDs ...uint) *gorm.DB {
	return tx.Model(&models.EventOccurrence{}).
		Where("event_id IN ? AND starts_at > ? AND visibility_override IS NOT NULL", eventIDs, time.Now().UTC())
}

func dateDecisionKey(id uint) string { return "date:" + idString(id) }

func (vs visibleSet) canSeeSeries(viewer *models.User, ev *models.Event) bool {
	return vs.canSeeDate(viewer, ev, &models.EventOccurrence{})
}
