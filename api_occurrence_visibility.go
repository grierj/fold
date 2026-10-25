package main

import (
	"encoding/json"
	"errors"
	"fold/internal/audit"
	"fold/internal/policy"
	"fold/models"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"strconv"
	"time"
)

// canSeeDate is the ordinary, batched date filter. Candidate parents are never
// series authority: callers must separately use visibleEvents/loadVisibleEvent.
func (vs visibleSet) canSeeDate(viewer *models.User, ev *models.Event, occ *models.EventOccurrence) bool {
	if ev == nil {
		return false
	}
	g, rel := groupAndRel(ev, vs.Groups, vs.Rels)
	if ev.GroupID != nil && g == nil {
		return false
	}
	gp := policy.Group{}
	if g != nil {
		gp = asGroup(g)
	}
	view := viewOf(ev, vs.Hosted[ev.ID])
	view.Visibility = policy.EffectiveVisibility(ev.Visibility, occ.VisibilityOverride)
	return policy.CanSeeEvent(asPerson(viewer), view, gp, rel)
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
	return a.canRegisterIn(tx, u, dateEvent(ev, &occ))
}

func laterTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
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
		impact.Tightened = append(impact.Tightened, impactEventJSON{ID: ev.ID, Title: ev.Title + " · " + occ.OriginalDate, Visibility: policy.EffectiveVisibility(ev.Visibility, occ.VisibilityOverride), Registrants: count})
	}
	return impact, nil
}

// Caller holds the contact gate, event and occurrence locks. Cancellation,
// audit and stored notices share the same transaction as the date edit.
func changeDateVisibility(c *gin.Context, tx *gorm.DB, ev *models.Event, occ *models.EventOccurrence, value *string, reason string, keep bool, groupVisibility ...string) ([]uint, error) {
	old := occ.VisibilityOverride
	if !keep && ((old == nil && value == nil) || (old != nil && value != nil && *old == *value)) {
		return nil, nil
	}
	before, after := policy.EffectiveVisibility(ev.Visibility, old), policy.EffectiveVisibility(ev.Visibility, value)
	var mailed []uint
	if policy.MoreOpen(before, after) && !keep {
		ids, err := cancelDateLosers(c, tx, ev, after, time.Now().UTC(), occ.ID)
		if err != nil {
			return nil, err
		}
		mailed = append(mailed, ids...)
	}
	f := eventFactsIn(tx, apiUser(c), ev)
	if f.group != nil && len(groupVisibility) > 0 {
		f.group.Visibility = groupVisibility[0]
	}
	action := audit.VisibilityChanged
	if f.group != nil && policy.MoreOpen(after, f.group.Visibility) {
		action = audit.VisibilityOverride
	}
	meta := map[string]any{"original_date": occ.OriginalDate, "oldVisibility": old, "newVisibility": value, "reason": reason}
	if f.group != nil {
		meta["groupVisibility"] = f.group.Visibility
	}
	if err := audit.Write(tx, audit.Entry{Actor: &apiUser(c).ID, Action: action, TargetType: "event", TargetID: ev.ID, Meta: meta, IP: c.ClientIP()}); err != nil {
		return nil, err
	}
	occ.VisibilityOverride = value
	if err := tx.Model(occ).Update("visibility_override", value).Error; err != nil {
		return nil, err
	}
	if action == audit.VisibilityOverride && f.group != nil {
		date := *ev
		date.Title = ev.Title + " · " + occ.OriginalDate
		ids, err := notifyVisibilityOverride(tx, apiUser(c).ID, &date, f.group, after, reason)
		if err != nil {
			return nil, err
		}
		mailed = append(mailed, ids...)
	}
	return mailed, nil
}

func (a *App) apiDateVisibilityImpact(c *gin.Context) {
	ev, ok := a.loadManagedEvent(c)
	if !ok {
		return
	}
	value, _, err := parseDateVisibility(json.RawMessage(strconv.Quote(c.Query("visibility"))))
	if err != nil {
		jsonError(c, 400, "that visibility isn't allowed on this event")
		return
	}
	var occ models.EventOccurrence
	if a.DB.Where("event_id = ? AND original_date = ?", ev.ID, c.Param("date")).First(&occ).Error != nil {
		jsonError(c, 404, "event not found")
		return
	}
	impact, err := dateImpactOf(a.DB, ev, &occ, policy.EffectiveVisibility(ev.Visibility, value))
	if err != nil {
		jsonError(c, 500, "could not preview visibility")
		return
	}
	c.JSON(200, impact)
}

func eventImpactOfReset(tx *gorm.DB, ev *models.Event, next string, reset bool) (visibilityImpactJSON, error) {
	impact, err := eventImpactOf(tx, ev, next)
	if err != nil {
		return impact, err
	}
	var dates []models.EventOccurrence
	if err := tx.Where("event_id = ? AND starts_at > ? AND visibility_override IS NOT NULL", ev.ID, time.Now().UTC()).Find(&dates).Error; err != nil {
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
func openedDates(tx *gorm.DB, groupID uint, oldVis, next string, lock bool) ([]models.EventOccurrence, map[uint]*models.Event, error) {
	evs := map[uint]*models.Event{}
	if !policy.MoreOpen(oldVis, next) {
		return nil, evs, nil
	}
	var rows []models.Event
	q := tx.Where("group_id = ?", groupID).Order("id")
	if lock {
		q = q.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	if err := q.Find(&rows).Error; err != nil {
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
	q = tx.Where("event_id IN ? AND starts_at > ? AND visibility_override IS NOT NULL", ids, time.Now().UTC()).Order("id")
	if lock {
		q = q.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	if err := q.Find(&dates).Error; err != nil {
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

func dateDecisionKey(id uint) string { return "date:" + strconv.FormatUint(uint64(id), 10) }

func (vs visibleSet) canSeeSeries(viewer *models.User, ev *models.Event) bool {
	return vs.canSeeDate(viewer, ev, &models.EventOccurrence{})
}

// Preserve the established full-series wire contract for authorized readers,
// but do not serialize even empty series keys on an opened hidden-series date.
func (o occurrenceJSON) MarshalJSON() ([]byte, error) {
	type wire occurrenceJSON
	body, err := json.Marshal(wire(o))
	if err != nil || !o.hideSeries {
		return body, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		return nil, err
	}
	for _, key := range []string{"recurrenceRule", "rruleUntil", "seriesStartsLocal", "seriesTitle", "seriesLocation", "seriesDurationMinutes", "seriesVisibility"} {
		delete(fields, key)
	}
	return json.Marshal(fields)
}
