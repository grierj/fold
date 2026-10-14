package main

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"fold/internal/audit"
	"fold/internal/policy"
	"fold/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Group visibility and its events (DEC-48). An event keeps its own
// visibility. When a group becomes more private, in the same transaction:
//   - events more open than the new value that only inherited the group's
//     visibility are tightened to match, one audit row each;
//   - leader-opened events are not changed automatically: the leader
//     decides per event to keep it open (a logged override) or tighten it;
//   - registrants of a tightened event who can no longer see it are
//     cancelled on upcoming dates and get a notice (M1.13's "made
//     stricter" rule, with the PR #41 privacy rules).
// Making a group more open never changes its events.

// Decisions for leader-opened events when their group becomes more private.
const (
	openedKeep    = "keep"
	openedTighten = "tighten"
)

type impactEventJSON struct {
	ID         uint   `json:"id"`
	Title      string `json:"title"`
	Visibility string `json:"visibility"`
	// Registrants: people registered for an upcoming date who could no
	// longer see the event if it is tightened.
	Registrants int64 `json:"registrants"`
}

// visibilityImpactJSON is what a group visibility change would do to its
// events. Both lists are empty unless the change is more private.
type visibilityImpactJSON struct {
	Visibility   string            `json:"visibility"`
	Tightened    []impactEventJSON `json:"tightened"`
	LeaderOpened []impactEventJSON `json:"leaderOpened"`
}

func (v visibilityImpactJSON) empty() bool {
	return len(v.Tightened) == 0 && len(v.LeaderOpened) == 0
}

// errNeedDecision: a leader-opened event has no keep/tighten decision.
var errNeedDecision = errors.New("leader-opened events need a decision")

// affectedEvents lists g's events more open than newVis, split into the
// ones that inherited the group's value and the leader-opened ones. Empty
// unless newVis is more private than oldVis.
func affectedEvents(tx *gorm.DB, groupID uint, oldVis, newVis string, lock bool) (auto, opened []models.Event, err error) {
	if !policy.MoreOpen(oldVis, newVis) {
		return nil, nil, nil
	}
	q := tx.Where("group_id = ?", groupID).Order("id")
	if lock {
		q = q.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	var evs []models.Event
	if err := q.Find(&evs).Error; err != nil {
		return nil, nil, err
	}
	for _, ev := range evs {
		if !policy.MoreOpen(ev.Visibility, newVis) {
			continue
		}
		if ev.LeaderOpened {
			opened = append(opened, ev)
		} else {
			auto = append(auto, ev)
		}
	}
	return auto, opened, nil
}

// losersQuery selects registrations on upcoming dates of ev whose people
// could not see ev at newVis. Only invite_only shuts anyone out: everyone
// registered has an account, and admins see every event.
func losersQuery(tx *gorm.DB, ev *models.Event, newVis string, now time.Time) *gorm.DB {
	q := tx.Table("registrations").
		Joins("JOIN event_occurrences ON event_occurrences.id = registrations.occurrence_id").
		Joins("JOIN users ON users.id = registrations.user_id").
		Where("event_occurrences.event_id = ? AND event_occurrences.starts_at > ?", ev.ID, now).
		Where("registrations.status IN ?", []string{models.RegGoing, models.RegMaybe})
	if newVis != models.VisibilityInviteOnly || ev.GroupID == nil {
		return q.Where("1 = 0")
	}
	return q.Where("users.role <> ?", models.RoleAdmin).
		Where("NOT EXISTS (SELECT 1 FROM memberships WHERE memberships.user_id = registrations.user_id AND memberships.group_id = ? AND memberships.status = 'active')", *ev.GroupID)
}

func toImpact(tx *gorm.DB, evs []models.Event, newVis string, now time.Time) []impactEventJSON {
	out := []impactEventJSON{}
	for i := range evs {
		var n int64
		losersQuery(tx, &evs[i], newVis, now).Distinct("registrations.user_id").Count(&n)
		out = append(out, impactEventJSON{ID: evs[i].ID, Title: evs[i].Title, Visibility: evs[i].Visibility, Registrants: n})
	}
	return out
}

func impactOf(tx *gorm.DB, g *models.Group, newVis string, lock bool) (visibilityImpactJSON, []models.Event, []models.Event, error) {
	now := time.Now().UTC()
	auto, opened, err := affectedEvents(tx, g.ID, g.Visibility, newVis, lock)
	if err != nil {
		return visibilityImpactJSON{}, nil, nil, err
	}
	return visibilityImpactJSON{
		Visibility:   newVis,
		Tightened:    toImpact(tx, auto, newVis, now),
		LeaderOpened: toImpact(tx, opened, newVis, now),
	}, auto, opened, nil
}

// apiGroupVisibilityImpact previews a visibility change so the SPA can show
// the leader which events tighten and ask about leader-opened ones before
// saving. GET /groups/:id/visibility-impact?visibility=...
func (a *App) apiGroupVisibilityImpact(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	g, _, ok := a.loadManagedGroupID(c, uint(id))
	if !ok {
		return
	}
	vis, msg := normalizeVisibility(c.Query("visibility"))
	if msg != "" {
		jsonError(c, http.StatusBadRequest, msg)
		return
	}
	impact, _, _, err := impactOf(a.DB, g, vis, false)
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not check the group's events")
		return
	}
	c.JSON(http.StatusOK, impact)
}

// applyGroupTightening runs inside the group update transaction, after the
// group row is locked and before it is saved. oldVis is the stored value.
// decisions maps event id to keep or tighten; a leader-opened event without
// one returns errNeedDecision and impact, and nothing is written.
func applyGroupTightening(c *gin.Context, tx *gorm.DB, g *models.Group, oldVis, newVis string, decisions map[string]string) (visibilityImpactJSON, []uint, error) {
	before := *g
	before.Visibility = oldVis
	impact, auto, opened, err := impactOf(tx, &before, newVis, true)
	if err != nil || impact.empty() {
		return impact, nil, err
	}
	for _, ev := range opened {
		if d := decisions[strconv.FormatUint(uint64(ev.ID), 10)]; d != openedKeep && d != openedTighten {
			return impact, nil, errNeedDecision
		}
	}
	actor := apiUser(c)
	now := time.Now().UTC()
	var emailed []uint
	tighten := func(ev *models.Event, reason string) error {
		ids, err := cancelLosers(c, tx, ev, newVis, now)
		emailed = append(emailed, ids...)
		if err != nil {
			return err
		}
		old := ev.Visibility
		if err := tx.Model(ev).Updates(map[string]any{"visibility": newVis, "leader_opened": false}).Error; err != nil {
			return err
		}
		return audit.Write(tx, audit.Entry{Actor: &actor.ID, Action: audit.VisibilityChanged, TargetType: "event", TargetID: ev.ID,
			Meta: map[string]any{"oldVisibility": old, "newVisibility": newVis, "groupVisibility": newVis,
				"oldLeaderOpened": ev.LeaderOpened, "leaderOpened": false, "reason": reason},
			IP: c.ClientIP()})
	}
	for i := range auto {
		if err := tighten(&auto[i], "group_more_private"); err != nil {
			return impact, nil, err
		}
	}
	for i := range opened {
		ev := &opened[i]
		if decisions[strconv.FormatUint(uint64(ev.ID), 10)] == openedTighten {
			if err := tighten(ev, "group_more_private_leader_chose_tighten"); err != nil {
				return impact, nil, err
			}
			continue
		}
		// Kept open: a logged override, with the usual notice (DEC-17, DEC-28).
		if err := audit.Write(tx, audit.Entry{Actor: &actor.ID, Action: audit.VisibilityOverride, TargetType: "event", TargetID: ev.ID,
			Meta: map[string]any{"oldVisibility": ev.Visibility, "newVisibility": ev.Visibility, "groupVisibility": newVis,
				"oldLeaderOpened": true, "leaderOpened": true,
				"reason": "Kept open when the group became " + visWord(newVis) + "."},
			IP: c.ClientIP()}); err != nil {
			return impact, nil, err
		}
		kept := *g
		kept.Visibility = newVis
		ids, err := notifyVisibilityOverride(tx, actor.ID, ev, &kept, ev.Visibility, "Kept open when the group became "+visWord(newVis)+".")
		emailed = append(emailed, ids...)
		if err != nil {
			return impact, nil, err
		}
	}
	return impact, emailed, nil
}

// cancelLosers deletes upcoming registrations of people who could not see
// ev at newVis, writing one registration.cancelled audit row per person;
// the person gets a stored notification. It returns the notification IDs
// for post-commit email delivery.
func cancelLosers(c *gin.Context, tx *gorm.DB, ev *models.Event, newVis string, now time.Time) ([]uint, error) {
	var rows []struct {
		ID     uint
		UserID uint
		Date   string
	}
	if err := losersQuery(tx, ev, newVis, now).
		Select("registrations.id, registrations.user_id, event_occurrences.original_date AS date").
		Order("registrations.user_id, event_occurrences.original_date").Scan(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	regIDs := make([]uint, 0, len(rows))
	dates := map[uint][]string{}
	order := []uint{}
	for _, r := range rows {
		regIDs = append(regIDs, r.ID)
		if _, ok := dates[r.UserID]; !ok {
			order = append(order, r.UserID)
		}
		dates[r.UserID] = append(dates[r.UserID], r.Date)
	}
	if err := tx.Where("id IN ?", regIDs).Delete(&models.Registration{}).Error; err != nil {
		return nil, err
	}
	actor := apiUser(c)
	var emailed []uint
	for _, uid := range order {
		if err := audit.Write(tx, audit.Entry{Actor: &actor.ID, Action: audit.RegistrationCancelled, TargetType: "event", TargetID: ev.ID,
			Meta: map[string]any{"userId": uid, "dates": dates[uid], "newVisibility": newVis, "reason": "event_more_private"},
			IP:   c.ClientIP()}); err != nil {
			return nil, err
		}
		ids, err := notifyRegistrationCancelled(tx, actor.ID, ev, uid)
		emailed = append(emailed, ids...)
		if err != nil {
			return nil, err
		}
	}
	return emailed, nil
}

// eventImpactOf is what setting one event to newVis would do, in the same
// shape as a group change: the event is listed under tightened when newVis
// is more private, with the registrants that would be cancelled.
func eventImpactOf(tx *gorm.DB, ev *models.Event, newVis string) visibilityImpactJSON {
	impact := visibilityImpactJSON{Visibility: newVis, Tightened: []impactEventJSON{}, LeaderOpened: []impactEventJSON{}}
	if policy.Openness(newVis) < policy.Openness(ev.Visibility) {
		impact.Tightened = toImpact(tx, []models.Event{*ev}, newVis, time.Now().UTC())
	}
	return impact
}

// apiEventVisibilityImpact previews a single-event visibility change, like
// apiGroupVisibilityImpact does for a group. Managers only.
// GET /events/:id/visibility-impact?visibility=...
func (a *App) apiEventVisibilityImpact(c *gin.Context) {
	ev, f, ok := a.loadVisibleEvent(c)
	if !ok {
		return
	}
	if !policy.CanManageEvent(asPerson(apiUser(c)), f.view, f.rel) {
		jsonError(c, http.StatusForbidden, "you can't change this event")
		return
	}
	vis, msg := normalizeVisibility(c.Query("visibility"))
	if msg != "" {
		jsonError(c, http.StatusBadRequest, msg)
		return
	}
	c.JSON(http.StatusOK, eventImpactOf(a.DB, ev, vis))
}
