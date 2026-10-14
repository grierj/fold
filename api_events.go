package main

import (
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"fold/internal/events"
	"fold/internal/policy"
	"fold/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// How far a list request may look, and how many dates of one series it
// returns. Stored occurrences go out 18 months; this only caps the response.
const (
	maxEventWindow       = 366 * 24 * time.Hour
	maxOccurrencesListed = 100
	settingTimezone      = "timezone"
)

func (a *App) churchTimezone() string {
	var s models.Setting
	if err := a.DB.Where("key = ?", settingTimezone).First(&s).Error; err != nil {
		return "UTC"
	}
	if _, err := time.LoadLocation(strings.TrimSpace(s.Value)); err != nil {
		return "UTC"
	}
	return s.Value
}

func (a *App) apiSite(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"name": a.siteName(), "timezone": a.churchTimezone()})
}

func (a *App) apiSetTimezone(c *gin.Context) {
	var body struct {
		Timezone string `json:"timezone"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		jsonError(c, http.StatusBadRequest, "timezone is required")
		return
	}
	tz := strings.TrimSpace(body.Timezone)
	if _, err := time.LoadLocation(tz); err != nil || tz == "" {
		jsonError(c, http.StatusBadRequest, "timezone must be an IANA name, for example America/Denver")
		return
	}
	if err := a.DB.Where(models.Setting{Key: settingTimezone}).
		Assign(models.Setting{Value: tz}).
		FirstOrCreate(&models.Setting{}).Error; err != nil {
		jsonError(c, http.StatusInternalServerError, "could not save timezone")
		return
	}
	c.JSON(http.StatusOK, gin.H{"timezone": tz})
}

// ---------------------------------------------------------------------------
// Occurrence JSON
// ---------------------------------------------------------------------------

func (a *App) buildOccurrences(groupIDs []uint, from, to string, startAfter *time.Time, me *models.User) []occurrenceJSON {
	if len(groupIDs) == 0 {
		return []occurrenceJSON{}
	}
	var ids []uint
	if err := a.DB.Model(&models.Event{}).Where("group_id IN ?", groupIDs).Pluck("id", &ids).Error; err != nil || len(ids) == 0 {
		return []occurrenceJSON{}
	}
	return a.occurrencesForEvents(ids, from, to, startAfter, me)
}

// occurrencesForEvents lists scheduled dates of these events and attaches
// attendee names only where policy allows. A caller who cannot see an
// event's group gets "Hosted by <church>" and no group id or name.
func (a *App) occurrencesForEvents(eventIDs []uint, from, to string, startAfter *time.Time, me *models.User) []occurrenceJSON {
	out := []occurrenceJSON{}
	if len(eventIDs) == 0 {
		return out
	}
	var evs []models.Event
	if err := a.DB.Where("id IN ?", eventIDs).Find(&evs).Error; err != nil || len(evs) == 0 {
		return out
	}
	byID := make(map[uint]*models.Event, len(evs))
	gset := map[uint]struct{}{}
	for i := range evs {
		byID[evs[i].ID] = &evs[i]
		if evs[i].GroupID != nil {
			gset[*evs[i].GroupID] = struct{}{}
		}
	}
	groups := a.groupsByID(gset)
	rels := a.relsFor(me, gset)
	hosted := a.hostedEventIDs(me, eventIDs)

	// The one place event dates are listed: drop every event this viewer
	// can't see, so no caller has to remember to (hardening review).
	visibleIDs := make([]uint, 0, len(evs))
	for i := range evs {
		ev := &evs[i]
		g, rel := groupAndRel(ev, groups, rels)
		gp := policy.Group{}
		if g != nil {
			gp = asGroup(g)
		}
		if policy.CanSeeEvent(asPerson(me), viewOf(ev, hosted[ev.ID]), gp, rel) {
			visibleIDs = append(visibleIDs, ev.ID)
		} else {
			delete(byID, ev.ID)
		}
	}
	if len(visibleIDs) == 0 {
		return out
	}
	eventIDs = visibleIDs

	q := a.DB.Where("event_id IN ? AND status = ? AND original_date >= ? AND original_date <= ?",
		eventIDs, models.OccurrenceScheduled, from, to)
	if startAfter != nil {
		q = q.Where("starts_at >= ?", *startAfter)
	}
	var occs []models.EventOccurrence
	q.Order("starts_at").Find(&occs)

	perEvent := map[uint]int{}
	kept := make([]models.EventOccurrence, 0, len(occs))
	for _, o := range occs {
		if perEvent[o.EventID] >= maxOccurrencesListed {
			continue
		}
		perEvent[o.EventID]++
		kept = append(kept, o)
	}
	if len(kept) == 0 {
		return out
	}

	ids := make([]uint, len(kept))
	for i := range kept {
		ids[i] = kept[i].ID
	}
	type countRow struct {
		OccurrenceID uint
		Status       string
		N            int64
	}
	var counts []countRow
	a.DB.Model(&models.Registration{}).Joins(listedUsers("registrations")).
		Select("registrations.occurrence_id, registrations.status, count(*) as n").
		Where("registrations.occurrence_id IN ?", ids).
		Group("registrations.occurrence_id, registrations.status").
		Scan(&counts)
	countOf := map[uint]map[string]int64{}
	for _, r := range counts {
		if countOf[r.OccurrenceID] == nil {
			countOf[r.OccurrenceID] = map[string]int64{}
		}
		countOf[r.OccurrenceID][r.Status] = r.N
	}
	mine := map[uint]string{}
	if me != nil {
		var rows []models.Registration
		a.DB.Where("user_id = ? AND occurrence_id IN ?", me.ID, ids).Find(&rows)
		for _, r := range rows {
			mine[r.OccurrenceID] = r.Status
		}
	}

	showNames := map[uint]bool{}
	p := asPerson(me)
	for i := range evs {
		ev := &evs[i]
		g, rel := groupAndRel(ev, groups, rels)
		gp := policy.Group{}
		if g != nil {
			gp = asGroup(g)
		}
		showNames[ev.ID] = policy.CanSeeAttendeeNames(p, viewOf(ev, hosted[ev.ID]), gp, rel)
	}
	names := a.attendeeNames(kept, showNames)

	for i := range kept {
		o := &kept[i]
		ev := byID[o.EventID]
		if ev == nil {
			continue
		}
		g, rel := groupAndRel(ev, groups, rels)
		// The group's meeting place is the default location only for people
		// who can see the group; an opened invite-only event must not reveal it.
		loc := ""
		if shown := a.groupShownTo(me, g, rel); shown != nil {
			loc = shown.Location
		}
		inst := occurrenceToJSON(ev, o, loc)
		inst.Visibility = ev.Visibility
		inst.LeaderOpened = ev.LeaderOpened
		inst.ChurchWide = ev.GroupID == nil
		inst.Registrations = map[string]int64{"going": 0, "maybe": 0, "declined": 0}
		for s, n := range countOf[o.ID] {
			inst.Registrations[s] = n
		}
		if s, ok := mine[o.ID]; ok {
			inst.MyRegistration = &s
		}
		inst.AttendeesVisible = showNames[ev.ID]
		if showNames[ev.ID] {
			inst.Attendees = names[o.ID]
		}
		a.presentGroup(&inst, ev, g, me, rel)
		out = append(out, inst)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartsAt.Before(out[j].StartsAt) })
	return out
}

// attendeeNames loads listed registrants for the occurrences whose events
// allow names. Disabled accounts stay out via listedUsers.
func (a *App) attendeeNames(kept []models.EventOccurrence, show map[uint]bool) map[uint][]attendeeJSON {
	out := map[uint][]attendeeJSON{}
	ids := []uint{}
	for _, o := range kept {
		if show[o.EventID] {
			ids = append(ids, o.ID)
		}
	}
	if len(ids) == 0 {
		return out
	}
	type row struct {
		OccurrenceID uint
		UserID       uint
		Name         string
		DisplayName  string
		Status       string
	}
	var rows []row
	a.DB.Model(&models.Registration{}).Joins(listedUsers("registrations")).
		Select("registrations.occurrence_id, registrations.user_id, users.name, users.display_name, registrations.status").
		Where("registrations.occurrence_id IN ?", ids).
		Order("users.display_name, users.name, users.id").
		Scan(&rows)
	for _, r := range rows {
		name := r.DisplayName
		if name == "" {
			name = r.Name
		}
		if name == "" {
			name = "Member"
		}
		out[r.OccurrenceID] = append(out[r.OccurrenceID], attendeeJSON{
			UserID: r.UserID, Name: name, Status: r.Status,
		})
	}
	return out
}

func occurrenceToJSON(ev *models.Event, o *models.EventOccurrence, groupLoc string) occurrenceJSON {
	loc, err := time.LoadLocation(ev.TZID)
	if err != nil {
		loc = time.UTC
	}
	dur := ev.DurationMinutes
	if o.DurationOverride != nil {
		dur = *o.DurationOverride
	}
	start := o.StartsAt.In(loc)
	end := start.Add(time.Duration(dur) * time.Minute)
	title := ev.Title
	if o.TitleOverride != nil {
		title = *o.TitleOverride
	}
	location := ev.Location
	if o.LocationOverride != nil {
		location = *o.LocationOverride
	} else if location == "" {
		location = groupLoc
	}
	until := ""
	if ev.RRuleUntil != nil {
		until = *ev.RRuleUntil
	}
	return occurrenceJSON{
		ID: o.ID, EventID: ev.ID, Date: o.OriginalDate,
		StartsAt: start.UTC(), EndsAt: end.UTC(),
		StartsLocal: events.FormatWall(start, loc), EndsLocal: events.FormatWall(end, loc),
		TZID: ev.TZID, DurationMinutes: dur, Location: location, Title: title,
		Description:    ev.Description,
		RecurrenceRule: ev.RRule, RRuleUntil: until, SeriesStartsLocal: ev.StartsLocal,
		Overridden:  o.TimeFrozen() || o.TitleOverride != nil || o.LocationOverride != nil,
		SeriesTitle: ev.Title, SeriesLocation: ev.Location, SeriesDurationMinutes: ev.DurationMinutes,
		Registrations: map[string]int64{"going": 0, "maybe": 0, "declined": 0},
	}
}

func toEventJSON(ev *models.Event) eventJSON {
	start, _, err := events.ParseWall(ev.StartsLocal, ev.TZID)
	if err != nil {
		start = ev.CreatedAt
	}
	end := start.Add(time.Duration(ev.DurationMinutes) * time.Minute)
	until := ""
	if ev.RRuleUntil != nil {
		until = *ev.RRuleUntil
	}
	return eventJSON{
		ID: ev.ID, GroupID: ev.GroupID, Title: ev.Title, Description: ev.Description,
		StartsLocal: ev.StartsLocal, DurationMinutes: ev.DurationMinutes, TZID: ev.TZID,
		StartsAt: start.UTC(), EndsAt: end.UTC(),
		Location: ev.Location, RecurrenceRule: ev.RRule, RRuleUntil: until,
		Visibility: ev.Visibility, LeaderOpened: ev.LeaderOpened,
		ChurchWide: ev.GroupID == nil,
	}
}

func (a *App) groupSummary(g *models.Group) groupSummaryJSON {
	members := memberCount(a.DB, g.ID)
	var leader models.User
	leaderName := ""
	if err := a.DB.First(&leader, g.LeaderID).Error; err == nil {
		leaderName = leader.PublicName()
	}
	var next *time.Time
	nextLocal, nextTZ := "", ""
	var row struct {
		StartsAt time.Time
		TZID     string
	}
	err := a.DB.Table("event_occurrences").
		Select("event_occurrences.starts_at, events.tzid").
		Joins("JOIN events ON events.id = event_occurrences.event_id AND events.deleted_at IS NULL").
		Where("events.group_id = ? AND event_occurrences.status = ? AND event_occurrences.starts_at >= ?",
			g.ID, models.OccurrenceScheduled, time.Now().UTC()).
		Order("event_occurrences.starts_at").Limit(1).Scan(&row).Error
	if err == nil && !row.StartsAt.IsZero() {
		t := row.StartsAt.UTC()
		next = &t
		nextTZ = row.TZID
		if loc, lerr := time.LoadLocation(row.TZID); lerr == nil {
			nextLocal = events.FormatWall(t, loc)
		}
	}
	return groupSummaryJSON{
		ID: g.ID, Name: g.Name, Description: g.Description, // group-ok: groupSummary is served only after CanSeeGroup
		Tags: splitTags(g.Tags), ImageURL: g.ImageURL,
		CoverFocalX: g.CoverFocalX, CoverFocalY: g.CoverFocalY,
		Location: g.Location, LeaderName: leaderName, MemberCount: members, // group-ok: as above
		NextEventAt: next, NextEventLocal: nextLocal, NextEventTzid: nextTZ,
		Status: g.Status, LeaderID: g.LeaderID, Visibility: g.Visibility,
		DeletionRequested: g.DeletionRequested, MemberLimit: g.MemberLimit,
	}
}

// ---------------------------------------------------------------------------
// Event endpoints
// ---------------------------------------------------------------------------

type eventBody struct {
	Title           *string `json:"title"`
	Description     *string `json:"description"`
	StartsLocal     *string `json:"startsLocal"`
	DurationMinutes *int    `json:"durationMinutes"`
	TZID            *string `json:"tzid"`
	Location        *string `json:"location"`
	RecurrenceRule  *string `json:"recurrenceRule"`
	RRuleUntil      *string `json:"rruleUntil"`
}

func (a *App) specFrom(c *gin.Context, body eventBody, base *models.Event, creating bool) (events.Spec, bool) {
	spec := events.Spec{TZID: a.churchTimezone()}
	if base != nil {
		spec.StartsLocal = base.StartsLocal
		spec.DurationMinutes = base.DurationMinutes
		spec.TZID = base.TZID
		spec.RRule = base.RRule
		if base.RRuleUntil != nil {
			spec.Until = *base.RRuleUntil
		}
	}
	if body.StartsLocal != nil {
		spec.StartsLocal = strings.TrimSpace(*body.StartsLocal)
	}
	if body.DurationMinutes != nil {
		spec.DurationMinutes = *body.DurationMinutes
	}
	if body.TZID != nil && strings.TrimSpace(*body.TZID) != "" {
		spec.TZID = strings.TrimSpace(*body.TZID)
	}
	if body.RecurrenceRule != nil {
		spec.RRule = strings.TrimSpace(*body.RecurrenceRule)
	}
	if body.RRuleUntil != nil {
		spec.Until = strings.TrimSpace(*body.RRuleUntil)
	}
	if creating && (body.StartsLocal == nil || body.DurationMinutes == nil) {
		jsonError(c, http.StatusBadRequest, "startsLocal and durationMinutes are required")
		return spec, false
	}
	if len(spec.RRule) > maxRecurrenceLen {
		jsonError(c, http.StatusBadRequest, "recurrence rule is too long")
		return spec, false
	}
	if _, err := events.Expand(spec, time.Now().AddDate(0, 1, 0)); err != nil {
		jsonError(c, http.StatusBadRequest, err.Error())
		return spec, false
	}
	return spec, true
}

func applySpec(ev *models.Event, spec events.Spec, body eventBody) {
	ev.StartsLocal = spec.StartsLocal
	ev.DurationMinutes = spec.DurationMinutes
	ev.TZID = spec.TZID
	ev.RRule = spec.RRule
	if spec.Until == "" {
		ev.RRuleUntil = nil
	} else {
		u := spec.Until
		ev.RRuleUntil = &u
	}
	if body.Title != nil {
		ev.Title = strings.TrimSpace(*body.Title)
	}
	if body.Description != nil {
		ev.Description = strings.TrimSpace(*body.Description)
	}
	if body.Location != nil {
		ev.Location = strings.TrimSpace(*body.Location)
	}
}

func textOK(c *gin.Context, title, description, location string) bool {
	if len(title) > maxGroupNameLen {
		jsonError(c, http.StatusBadRequest, "title is too long")
		return false
	}
	if len(description) > maxDescriptionLen {
		jsonError(c, http.StatusBadRequest, "description is too long")
		return false
	}
	if len(location) > maxLocationLen {
		jsonError(c, http.StatusBadRequest, "location is too long")
		return false
	}
	return true
}

func parseDateWindow(c *gin.Context) (from, to string, ok bool) {
	now := time.Now().UTC()
	from = c.Query("from")
	to = c.Query("to")
	if from == "" {
		from = now.Format("2006-01-02")
	}
	if to == "" {
		to = now.AddDate(0, 0, 90).Format("2006-01-02")
	}
	fd, err1 := time.Parse("2006-01-02", from)
	td, err2 := time.Parse("2006-01-02", to)
	if err1 != nil || err2 != nil {
		jsonError(c, http.StatusBadRequest, "from and to must be YYYY-MM-DD")
		return "", "", false
	}
	if td.Before(fd) {
		jsonError(c, http.StatusBadRequest, "to must be on or after from")
		return "", "", false
	}
	if td.Sub(fd) > maxEventWindow {
		jsonError(c, http.StatusBadRequest, "date range must be 366 days or less")
		return "", "", false
	}
	return from, to, true
}

func (a *App) apiListEvents(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	var g models.Group
	if err := a.DB.First(&g, id).Error; err != nil || !a.canSeeGroup(apiUser(c), &g) {
		jsonError(c, http.StatusNotFound, "group not found")
		return
	}
	from, to, ok := parseDateWindow(c)
	if !ok {
		return
	}
	c.JSON(http.StatusOK, gin.H{"events": a.buildOccurrences([]uint{g.ID}, from, to, nil, apiUser(c))})
}

func (a *App) apiCreateEvent(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	g, _, ok := a.loadManagedGroupID(c, uint(id))
	if !ok {
		return
	}
	var body eventBody
	if err := c.ShouldBindJSON(&body); err != nil {
		jsonError(c, http.StatusBadRequest, "startsLocal and durationMinutes are required")
		return
	}
	spec, ok := a.specFrom(c, body, nil, true)
	if !ok {
		return
	}
	gid := g.ID
	ev := models.Event{
		// The group's visibility is copied once; later group changes only
		// tighten it (DEC-48).
		GroupID: &gid, Visibility: g.Visibility,
		CreatedBy: apiUser(c).ID,
	}
	applySpec(&ev, spec, body)
	if !textOK(c, ev.Title, ev.Description, ev.Location) {
		return
	}
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&ev).Error; err != nil {
			return err
		}
		return events.Ensure(tx, &ev, time.Now().UTC())
	})
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not create event")
		return
	}
	c.JSON(http.StatusCreated, gin.H{"event": toEventJSON(&ev)})
}

func (a *App) loadManagedEvent(c *gin.Context) (*models.Event, bool) {
	ev, f, ok := a.loadVisibleEvent(c)
	if !ok {
		return nil, false
	}
	if !policy.CanManageEvent(asPerson(apiUser(c)), f.view, f.rel) {
		jsonError(c, http.StatusForbidden, "you can't change this event")
		return nil, false
	}
	return ev, true
}

func (a *App) apiUpdateEvent(c *gin.Context) {
	ev, ok := a.loadManagedEvent(c)
	if !ok {
		return
	}
	var body eventBody
	if err := c.ShouldBindJSON(&body); err != nil {
		jsonError(c, http.StatusBadRequest, "invalid body")
		return
	}
	spec, ok := a.specFrom(c, body, ev, false)
	if !ok {
		return
	}
	title, description, location := ev.Title, ev.Description, ev.Location
	if body.Title != nil {
		title = strings.TrimSpace(*body.Title)
	}
	if body.Description != nil {
		description = strings.TrimSpace(*body.Description)
	}
	if body.Location != nil {
		location = strings.TrimSpace(*body.Location)
	}
	if !textOK(c, title, description, location) {
		return
	}
	now := time.Now().UTC()
	start, _, err := events.ParseWall(spec.StartsLocal, spec.TZID)
	if err != nil {
		jsonError(c, http.StatusBadRequest, err.Error())
		return
	}
	whens, err := events.Expand(spec, events.WindowEnd(start, now))
	if err != nil {
		jsonError(c, http.StatusBadRequest, err.Error())
		return
	}
	var occs []models.EventOccurrence
	if err := a.DB.Where("event_id = ?", ev.ID).Find(&occs).Error; err != nil {
		jsonError(c, http.StatusInternalServerError, "could not update event")
		return
	}
	existing := make([]events.Existing, len(occs))
	for i, o := range occs {
		existing[i] = events.Existing{
			ID: o.ID, OriginalDate: o.OriginalDate, StartsAt: o.StartsAt, EndsAt: o.EndsAt,
			TimeFrozen: o.TimeFrozen(), Keep: o.Customized(),
		}
	}
	actions := events.ApplySeries(existing, whens, now)
	err = a.DB.Transaction(func(tx *gorm.DB) error {
		var locked models.Event
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&locked, ev.ID).Error; err != nil {
			return err
		}
		applySpec(&locked, spec, body)
		if err := tx.Save(&locked).Error; err != nil {
			return err
		}
		*ev = locked
		var cancelled []uint
		for _, act := range actions {
			switch act.Kind {
			case "move":
				if err := tx.Model(&models.EventOccurrence{}).Where("id = ?", act.ID).
					Updates(map[string]any{"starts_at": act.When.StartsAt, "ends_at": act.When.EndsAt}).Error; err != nil {
					return err
				}
			case "cancel":
				if err := tx.Model(&models.EventOccurrence{}).Where("id = ?", act.ID).
					Update("status", models.OccurrenceCancelled).Error; err != nil {
					return err
				}
				cancelled = append(cancelled, act.ID)
			case "add":
				row := models.EventOccurrence{
					EventID: ev.ID, OriginalDate: act.When.OriginalDate,
					StartsAt: act.When.StartsAt, EndsAt: act.When.EndsAt,
					Status: models.OccurrenceScheduled,
				}
				if err := tx.Create(&row).Error; err != nil {
					return err
				}
			}
		}
		return notifyOccurrencesCancelled(tx, apiUser(c).ID, cancelled)
	})
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not update event")
		return
	}
	c.JSON(http.StatusOK, gin.H{"event": toEventJSON(ev)})
}

func (a *App) apiDeleteEvent(c *gin.Context) {
	ev, ok := a.loadManagedEvent(c)
	if !ok {
		return
	}
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		return deleteEventRows(tx, []uint{ev.ID})
	})
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not delete event")
		return
	}
	c.JSON(http.StatusOK, gin.H{})
}

// deleteEventRows removes an event's registrations, occurrences, and hosts,
// then soft-deletes the event. tx is the caller's transaction.
func deleteEventRows(tx *gorm.DB, eventIDs []uint) error {
	if len(eventIDs) == 0 {
		return nil
	}
	var occIDs []uint
	if err := tx.Model(&models.EventOccurrence{}).Where("event_id IN ?", eventIDs).Pluck("id", &occIDs).Error; err != nil {
		return err
	}
	if len(occIDs) > 0 {
		if err := tx.Where("occurrence_id IN ?", occIDs).Delete(&models.Registration{}).Error; err != nil {
			return err
		}
	}
	if err := tx.Where("event_id IN ?", eventIDs).Delete(&models.EventOccurrence{}).Error; err != nil {
		return err
	}
	if err := tx.Where("event_id IN ?", eventIDs).Delete(&models.EventHost{}).Error; err != nil {
		return err
	}
	return tx.Where("id IN ?", eventIDs).Delete(&models.Event{}).Error
}

func (a *App) apiCancelOccurrence(c *gin.Context) {
	ev, ok := a.loadManagedEvent(c)
	if !ok {
		return
	}
	var body struct {
		Date string `json:"date"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		jsonError(c, http.StatusBadRequest, "date is required (YYYY-MM-DD)")
		return
	}
	if _, err := time.Parse("2006-01-02", body.Date); err != nil {
		jsonError(c, http.StatusBadRequest, "date must be YYYY-MM-DD")
		return
	}
	var occ models.EventOccurrence
	if err := a.DB.Where("event_id = ? AND original_date = ?", ev.ID, body.Date).First(&occ).Error; err != nil {
		jsonError(c, http.StatusNotFound, "that date is not on this event")
		return
	}
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&models.EventOccurrence{}).Where("id = ? AND status <> ?", occ.ID, models.OccurrenceCancelled).
			Update("status", models.OccurrenceCancelled)
		if res.Error != nil || res.RowsAffected == 0 {
			return res.Error // already cancelled: nobody is told twice
		}
		return notifyOccurrencesCancelled(tx, apiUser(c).ID, []uint{occ.ID})
	})
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not cancel")
		return
	}
	c.JSON(http.StatusOK, gin.H{})
}

func (a *App) apiUpdateOccurrence(c *gin.Context) {
	ev, ok := a.loadManagedEvent(c)
	if !ok {
		return
	}
	date := c.Param("date")
	if _, err := time.Parse("2006-01-02", date); err != nil {
		jsonError(c, http.StatusBadRequest, "date must be YYYY-MM-DD")
		return
	}
	var o models.EventOccurrence
	if err := a.DB.Where("event_id = ? AND original_date = ?", ev.ID, date).First(&o).Error; err != nil {
		jsonError(c, http.StatusNotFound, "that date is not on this event")
		return
	}
	var body struct {
		StartsLocal     *string `json:"startsLocal"`
		DurationMinutes *int    `json:"durationMinutes"`
		Location        *string `json:"location"`
		Title           *string `json:"title"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		jsonError(c, http.StatusBadRequest, "invalid body")
		return
	}
	dur := ev.DurationMinutes
	if o.DurationOverride != nil {
		dur = *o.DurationOverride
	}
	if body.DurationMinutes != nil {
		if *body.DurationMinutes < 1 || *body.DurationMinutes > events.MaxDurationMinutes {
			jsonError(c, http.StatusBadRequest, "duration must be between 1 minute and 7 days")
			return
		}
		dur = *body.DurationMinutes
		o.DurationOverride = body.DurationMinutes
	}
	start := o.StartsAt
	if body.StartsLocal != nil {
		t, _, err := events.ParseWall(strings.TrimSpace(*body.StartsLocal), ev.TZID)
		if err != nil {
			jsonError(c, http.StatusBadRequest, err.Error())
			return
		}
		start = t.UTC()
		o.StartsAtOverride = &start
	}
	if body.Location != nil {
		loc := strings.TrimSpace(*body.Location)
		if len(loc) > maxLocationLen {
			jsonError(c, http.StatusBadRequest, "location is too long")
			return
		}
		o.LocationOverride = &loc
	}
	if body.Title != nil {
		title := strings.TrimSpace(*body.Title)
		if len(title) > maxGroupNameLen {
			jsonError(c, http.StatusBadRequest, "title is too long")
			return
		}
		o.TitleOverride = &title
	}
	o.StartsAt = start
	o.EndsAt = start.Add(time.Duration(dur) * time.Minute)
	if err := a.DB.Save(&o).Error; err != nil {
		jsonError(c, http.StatusInternalServerError, "could not update this date")
		return
	}
	groupLoc := ""
	if ev.GroupID != nil {
		var g models.Group
		if err := a.DB.First(&g, *ev.GroupID).Error; err == nil {
			groupLoc = g.Location // group-ok: loadManagedEvent, only managers reach here
		}
	}
	inst := occurrenceToJSON(ev, &o, groupLoc)
	inst.Visibility = ev.Visibility
	inst.LeaderOpened = ev.LeaderOpened
	inst.ChurchWide = ev.GroupID == nil
	c.JSON(http.StatusOK, gin.H{"occurrence": inst})
}

func (a *App) apiRegister(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	var ev models.Event
	if err := a.DB.First(&ev, id).Error; err != nil {
		jsonError(c, http.StatusNotFound, "event not found")
		return
	}
	f := a.factsFor(apiUser(c), &ev)
	gp := policy.Group{}
	if f.group != nil {
		gp = asGroup(f.group)
	}
	if !policy.CanRegisterEvent(asPerson(apiUser(c)), f.view, gp, f.rel) {
		jsonError(c, http.StatusNotFound, "event not found")
		return
	}
	var body struct {
		Status string `json:"status"`
		Date   string `json:"date"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		jsonError(c, http.StatusBadRequest, "status and date are required")
		return
	}
	switch body.Status {
	case models.RegGoing, models.RegMaybe, models.RegDeclined:
	default:
		jsonError(c, http.StatusBadRequest, "status must be going, maybe, or declined")
		return
	}
	var o models.EventOccurrence
	if err := a.DB.Where("event_id = ? AND original_date = ? AND status = ?",
		ev.ID, body.Date, models.OccurrenceScheduled).First(&o).Error; err != nil {
		jsonError(c, http.StatusNotFound, "that date is not on this event")
		return
	}
	u := apiUser(c)
	r := models.Registration{UserID: u.ID, OccurrenceID: o.ID}
	a.DB.Where("user_id = ? AND occurrence_id = ?", u.ID, o.ID).
		Attrs(models.Registration{Status: body.Status}).
		FirstOrCreate(&r)
	r.Status = body.Status
	if err := a.DB.Save(&r).Error; err != nil {
		jsonError(c, http.StatusInternalServerError, "could not save registration")
		return
	}
	c.JSON(http.StatusOK, gin.H{"registration": gin.H{
		"userId": r.UserID, "eventId": ev.ID, "occurrenceId": o.ID,
		"date": body.Date, "status": r.Status,
	}})
}
