package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"fold/internal/audit"
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
	if _, err := events.LoadZone(s.Value); err != nil {
		return "UTC"
	}
	return s.Value
}

func (a *App) apiSite(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"name": a.siteName(), "timezone": a.churchTimezone(), "selfSignup": a.selfSignupMode()})
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
	if _, err := events.LoadZone(tz); err != nil {
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
	// The one place event dates are listed: drop every event this viewer
	// can't see, so no caller has to remember to (hardening review).
	vs, err := a.visibleEvents(me, a.DB.Where("id IN ?", eventIDs))
	if err != nil || len(vs.IDs) == 0 {
		return out
	}
	evs, byID, groups, rels, hosted := vs.Events, vs.ByID, vs.Groups, vs.Rels, vs.Hosted
	eventIDs = vs.IDs

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
		Where("registrations.occurrence_id IN ? AND registrations.status NOT IN ?",
			ids, []string{models.RegCancelled, models.RegRemoved}).
		Group("registrations.occurrence_id, registrations.status").
		Scan(&counts)
	countOf := map[uint]map[string]int64{}
	for _, r := range counts {
		if countOf[r.OccurrenceID] == nil {
			countOf[r.OccurrenceID] = map[string]int64{}
		}
		countOf[r.OccurrenceID][r.Status] = r.N
	}
	// The viewer's own registration: myRegistration may read waitlisted
	// or removed, so only cancelled rows are left out. The row id feeds
	// myWaitlistPosition.
	mine := map[uint]models.Registration{}
	if me != nil {
		var rows []models.Registration
		a.DB.Where("user_id = ? AND occurrence_id IN ? AND status <> ?", me.ID, ids, models.RegCancelled).Find(&rows)
		for _, r := range rows {
			mine[r.OccurrenceID] = r
		}
	}
	// Seats used per occurrence, in one query (M1.5). The counts above
	// count rows; this counts seats.
	usedMap, err := seatsUsedMap(a.DB, ids)
	if err != nil {
		usedMap = map[uint]int{}
	}

	showNames := map[uint]bool{}
	// Who said "can't go" (count and names) is only for the people who run
	// the event: admins, the group's leaders, a church-wide event's hosts
	// (CanManageEvent, DEC-64). Everyone else gets no declined data at all.
	showDeclined := map[uint]bool{}
	p := asPerson(me)
	for i := range evs {
		ev := &evs[i]
		g, rel := groupAndRel(ev, groups, rels)
		gp := policy.Group{}
		if g != nil {
			gp = asGroup(g)
		}
		showNames[ev.ID] = policy.CanSeeAttendeeNames(p, viewOf(ev, hosted[ev.ID]), gp, rel)
		showDeclined[ev.ID] = policy.CanManageEvent(p, viewOf(ev, hosted[ev.ID]), rel)
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
		inst.Registrations = map[string]int64{"going": 0, "maybe": 0, "declined": 0, "waitlisted": 0}
		for s, n := range countOf[o.ID] {
			inst.Registrations[s] = n
		}
		if !showDeclined[ev.ID] {
			delete(inst.Registrations, models.RegDeclined)
		}
		used := usedMap[o.ID]
		inst.SeatsUsed = used
		if inst.Capacity != nil {
			left := *inst.Capacity - used
			if left < 0 {
				left = 0
			}
			inst.SpotsLeft = &left
		}
		if r, ok := mine[o.ID]; ok {
			s := r.Status
			inst.MyRegistration = &s
			gc := r.GuestCount
			inst.MyGuestCount = &gc
			if r.Status == models.RegWaitlisted {
				if pos, perr := waitlistPosition(a.DB, o.ID, r.ID); perr == nil {
					inst.MyWaitlistPosition = &pos
				}
			}
		}
		// The video link is per date: going on this date (or running the
		// event) reveals a registered_only link (policy.CanSeeVideoLink).
		applyVideoReveal(&inst, ev,
			mine[o.ID].Status == models.RegGoing, showDeclined[ev.ID])
		inst.AttendeesVisible = showNames[ev.ID]
		if showNames[ev.ID] {
			inst.Attendees = names[o.ID]
			if !showDeclined[ev.ID] {
				kept := inst.Attendees[:0:0]
				for _, at := range inst.Attendees {
					if at.Status != models.RegDeclined {
						kept = append(kept, at)
					}
				}
				inst.Attendees = kept
			}
		}
		a.presentGroup(&inst, ev, g, me, rel)
		out = append(out, inst)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartsAt.Before(out[j].StartsAt) })
	return out
}

// attendeeNames loads listed registrants for the occurrences whose events
// allow names. Disabled accounts stay out via listedUsers, and removed
// registrations are never listed. Waitlisted names follow the same
// CanSeeAttendeeNames rule as going names; they come back in promotion
// (line) order so the UI can render the waitlist section.
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
		GuestCount   int
		WaitlistedAt *time.Time
	}
	var rows []row
	a.DB.Model(&models.Registration{}).Joins(listedUsers("registrations")).
		Select("registrations.occurrence_id, registrations.user_id, users.name, users.display_name, registrations.status, registrations.guest_count, registrations.waitlisted_at").
		Where("registrations.occurrence_id IN ? AND registrations.status NOT IN ?",
			ids, []string{models.RegCancelled, models.RegRemoved}).
		Order("users.display_name, users.name, users.id").
		Scan(&rows)
	byOcc := map[uint][]row{}
	for _, r := range rows {
		byOcc[r.OccurrenceID] = append(byOcc[r.OccurrenceID], r)
	}
	for occID, rs := range byOcc {
		var rest, wait []row
		for _, r := range rs {
			if r.Status == models.RegWaitlisted {
				wait = append(wait, r)
			} else {
				rest = append(rest, r)
			}
		}
		sort.SliceStable(wait, func(i, j int) bool {
			ai, aj := wait[i].WaitlistedAt, wait[j].WaitlistedAt
			if ai != nil && aj != nil && !ai.Equal(*aj) {
				return ai.Before(*aj)
			}
			if (ai == nil) != (aj == nil) {
				return aj == nil
			}
			return wait[i].UserID < wait[j].UserID
		})
		for _, r := range append(rest, wait...) {
			name := r.DisplayName
			if name == "" {
				name = r.Name
			}
			if name == "" {
				name = "Member"
			}
			out[occID] = append(out[occID], attendeeJSON{
				UserID: r.UserID, Name: name, Status: r.Status, GuestCount: r.GuestCount,
			})
		}
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
	inst := occurrenceJSON{
		ID: o.ID, EventID: ev.ID, Date: o.OriginalDate,
		StartsAt: start.UTC(), EndsAt: end.UTC(),
		StartsLocal: events.FormatWall(start, loc), EndsLocal: events.FormatWall(end, loc),
		TZID: ev.TZID, DurationMinutes: dur, Location: location, Title: title,
		Description:    ev.Description,
		RecurrenceRule: ev.RRule, RRuleUntil: until, SeriesStartsLocal: ev.StartsLocal,
		Overridden:  o.TimeFrozen() || o.TitleOverride != nil || o.LocationOverride != nil,
		SeriesTitle: ev.Title, SeriesLocation: ev.Location, SeriesDurationMinutes: ev.DurationMinutes,
		Registrations:   map[string]int64{"going": 0, "maybe": 0, "waitlisted": 0},
		Capacity:        events.EffectiveCapacity(ev.Capacity, o.CapacityOverride),
		WaitlistEnabled: ev.WaitlistEnabled,
		GuestsAllowed:   ev.GuestsAllowed,
	}
	// Fail closed: only an "always" link shows from here. Callers that
	// know the viewer refine it with applyVideoReveal (api_video.go).
	applyVideoReveal(&inst, ev, false, false)
	return inst
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
	j := eventJSON{
		ID: ev.ID, GroupID: ev.GroupID, Title: ev.Title, Description: ev.Description,
		StartsLocal: ev.StartsLocal, DurationMinutes: ev.DurationMinutes, TZID: ev.TZID,
		StartsAt: start.UTC(), EndsAt: end.UTC(),
		Location: ev.Location, RecurrenceRule: ev.RRule, RRuleUntil: until,
		Visibility: ev.Visibility, LeaderOpened: ev.LeaderOpened,
		ChurchWide: ev.GroupID == nil,
		Capacity:   ev.Capacity, WaitlistEnabled: ev.WaitlistEnabled,
		GuestsAllowed: ev.GuestsAllowed,
		VideoProvider: videoProviderOf(ev), VideoReveal: videoRevealOf(ev),
	}
	// Fail closed like occurrenceToJSON; presentEvent refines for the
	// event's managers, and create/update responses go to a manager.
	if link := videoLinkOf(ev); link != "" {
		if policy.CanSeeVideoLink(j.VideoReveal, false, false) {
			j.VideoURL = link
		} else {
			j.VideoHidden = true
		}
	}
	return j
}

func (a *App) groupSummary(g *models.Group) groupSummaryJSON {
	members := memberCount(a.DB, g.ID)
	var leader models.User
	leaderName := ""
	if err := a.DB.First(&leader, g.LeaderID).Error; err == nil {
		leaderName = leader.PublicName()
	}
	var next *time.Time
	var row struct {
		StartsAt time.Time `gorm:"column:starts_at"`
	}
	err := a.DB.Table("event_occurrences").
		Select("event_occurrences.starts_at").
		Joins("JOIN events ON events.id = event_occurrences.event_id AND events.deleted_at IS NULL").
		Where("events.group_id = ? AND event_occurrences.status = ? AND event_occurrences.starts_at >= ?",
			g.ID, models.OccurrenceScheduled, time.Now().UTC()).
		Order("event_occurrences.starts_at").Limit(1).Scan(&row).Error
	if err == nil && !row.StartsAt.IsZero() {
		t := row.StartsAt.UTC()
		next = &t
	}
	return groupSummaryJSON{
		ID: g.ID, Name: g.Name, Description: g.Description, // group-ok: groupSummary is served only after CanSeeGroup
		Tags: splitTags(g.Tags), ImageURL: g.ImageURL,
		CoverFocalX: g.CoverFocalX, CoverFocalY: g.CoverFocalY,
		Location: g.Location, LeaderName: leaderName, MemberCount: members, // group-ok: as above
		NextEventAt: next,
		Status:      g.Status, LeaderID: g.LeaderID, Visibility: g.Visibility,
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
	// Create only: who can see the new event, defaulting to the group's
	// value (DEC-51). Wider than the group is a leader-opened override.
	Visibility       *string `json:"visibility"`
	VisibilityReason *string `json:"visibilityReason"`
	// Capacity, waitlist, and guests (M1.5). Capacity is raw JSON so null
	// (clear to unlimited) is distinguishable from absent (leave it).
	// guestsAllowed is a number 0-5; null clears to 0.
	Capacity        *json.RawMessage `json:"capacity"`
	WaitlistEnabled *bool            `json:"waitlistEnabled"`
	GuestsAllowed   *json.RawMessage `json:"guestsAllowed"`
	// Video link (5.8, M1.8); see parseVideoEdits in api_video.go.
	VideoProvider *string `json:"videoProvider"`
	VideoURL      *string `json:"videoUrl"`
	VideoReveal   *string `json:"videoReveal"`
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

func applySpec(ev *models.Event, spec events.Spec, body eventBody, ed capacityEdits, vid videoEdits) {
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
	// Capacity, waitlist, and guests (M1.5). A set capacity of nil clears
	// to unlimited; a set guestsAllowed of null clears to 0.
	if ed.capSet {
		ev.Capacity = ed.cap
	}
	if body.WaitlistEnabled != nil {
		ev.WaitlistEnabled = *body.WaitlistEnabled
	}
	if ed.guestsSet {
		ev.GuestsAllowed = ed.guests
	}
	// Video (M1.8): vid holds the effective, validated values.
	ev.VideoProvider = vid.provider
	ev.VideoURL = vid.url
	ev.VideoReveal = vid.reveal
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
	g, rel, ok := a.loadManagedGroupID(c, uint(id))
	if !ok {
		return
	}
	var body eventBody
	if err := c.ShouldBindJSON(&body); err != nil {
		jsonError(c, http.StatusBadRequest, "startsLocal and durationMinutes are required")
		return
	}
	ed, ok := parseCapacityEdits(c, body)
	if !ok {
		return
	}
	vid, ok := parseVideoEdits(c, body, nil)
	if !ok {
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
	applySpec(&ev, spec, body, ed, vid)
	if !textOK(c, ev.Title, ev.Description, ev.Location) {
		return
	}
	me := apiUser(c)
	reason := ""
	if body.VisibilityReason != nil {
		reason = strings.TrimSpace(*body.VisibilityReason)
	}
	if len(reason) > 500 {
		jsonError(c, http.StatusBadRequest, "reason is too long")
		return
	}
	if body.Visibility != nil && strings.TrimSpace(*body.Visibility) != "" {
		vis := strings.TrimSpace(*body.Visibility)
		if !policy.CanSetEventVisibility(asPerson(me), viewOf(&ev, false), rel, vis) {
			jsonError(c, http.StatusBadRequest, "that visibility isn't allowed on this event")
			return
		}
		ev.Visibility = vis
		ev.LeaderOpened = policy.MoreOpen(vis, g.Visibility)
	}
	var emailed []uint
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&ev).Error; err != nil {
			return err
		}
		if ev.Visibility != g.Visibility {
			action := audit.VisibilityChanged
			if ev.LeaderOpened {
				action = audit.VisibilityOverride
			}
			meta := map[string]any{"oldVisibility": g.Visibility, "newVisibility": ev.Visibility,
				"oldLeaderOpened": false, "leaderOpened": ev.LeaderOpened, "groupVisibility": g.Visibility, "atCreation": true}
			if reason != "" {
				meta["reason"] = reason
			}
			if err := audit.Write(tx, audit.Entry{Actor: &me.ID, Action: action, TargetType: "event", TargetID: ev.ID,
				Meta: meta, IP: c.ClientIP()}); err != nil {
				return err
			}
			if ev.LeaderOpened {
				ids, err := notifyVisibilityOverride(tx, me.ID, &ev, g, ev.Visibility, reason)
				emailed = append(emailed, ids...)
				if err != nil {
					return err
				}
			}
		}
		return events.Ensure(tx, &ev, time.Now().UTC())
	})
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not create event")
		return
	}
	a.deliverNotificationEmails(emailed)
	j := toEventJSON(&ev)
	revealVideoToManager(&j, &ev) // the creator leads the group or is an admin
	c.JSON(http.StatusCreated, gin.H{"event": j})
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
	ed, ok := parseCapacityEdits(c, body)
	if !ok {
		return
	}
	vid, ok := parseVideoEdits(c, body, ev)
	if !ok {
		return
	}
	me := apiUser(c)
	var emailed []uint
	err = a.DB.Transaction(func(tx *gorm.DB) error {
		var locked models.Event
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&locked, ev.ID).Error; err != nil {
			return err
		}
		oldCap, oldPlace := locked.Capacity, locked.Location
		applySpec(&locked, spec, body, ed, vid)
		if err := tx.Save(&locked).Error; err != nil {
			return err
		}
		*ev = locked
		var cancelled, moved []uint
		for _, act := range actions {
			switch act.Kind {
			case "move":
				if err := tx.Model(&models.EventOccurrence{}).Where("id = ?", act.ID).
					Updates(map[string]any{"starts_at": act.When.StartsAt, "ends_at": act.When.EndsAt}).Error; err != nil {
					return err
				}
				moved = append(moved, act.ID)
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
		ids, err := notifyOccurrencesCancelled(tx, me.ID, cancelled)
		emailed = append(emailed, ids...)
		if err != nil {
			return err
		}
		// event.changed: one item per occurrence per going or waitlisted
		// person. A date that both moved and took the series place change
		// gets the combined "time and place" variant, not two items. The
		// move actions already skip frozen and customized dates (TimeFrozen
		// semantics), so the time fan-out mails exactly the dates that moved.
		placeChanged := body.Location != nil && strings.TrimSpace(*body.Location) != oldPlace
		movedSet := map[uint]bool{}
		movedByID := map[uint]*models.EventOccurrence{}
		if len(moved) > 0 {
			var movedRows []models.EventOccurrence
			tx.Where("id IN ?", moved).Find(&movedRows)
			for i := range movedRows {
				movedByID[movedRows[i].ID] = &movedRows[i]
			}
		}
		for _, occID := range moved {
			movedSet[occID] = true
			o := movedByID[occID]
			if o == nil {
				continue
			}
			variant := "time"
			if placeChanged && o.LocationOverride == nil {
				variant = "time and place"
			}
			ids, err := notifyEventChanged(tx, &locked, o, variant, me.ID)
			emailed = append(emailed, ids...)
			if err != nil {
				return err
			}
		}
		// event.changed, place variant: the event place changed, so every
		// future scheduled date without its own place changed place with it.
		if placeChanged {
			var occs []models.EventOccurrence
			if err := tx.Where("event_id = ? AND status = ? AND starts_at > ?", ev.ID,
				models.OccurrenceScheduled, now).Find(&occs).Error; err != nil {
				return err
			}
			for i := range occs {
				if occs[i].LocationOverride != nil || movedSet[occs[i].ID] {
					// That date keeps its own place, or already got the
					// combined notice above.
					continue
				}
				ids, err := notifyEventChanged(tx, &locked, &occs[i], "place", me.ID)
				emailed = append(emailed, ids...)
				if err != nil {
					return err
				}
			}
		}
		// Capacity change: promote per future occurrence where the
		// effective capacity changed (old vs new). Lowering promotes
		// nobody new; the fits check inside promotion makes that a
		// no-op, and nobody is ever removed (Decision 3).
		if ed.capSet && !intPtrEqual(oldCap, ed.cap) {
			var occs []models.EventOccurrence
			if err := tx.Where("event_id = ? AND status = ? AND starts_at > ?", ev.ID,
				models.OccurrenceScheduled, now).Find(&occs).Error; err != nil {
				return err
			}
			for i := range occs {
				oldEff := events.EffectiveCapacity(oldCap, occs[i].CapacityOverride)
				newEff := events.EffectiveCapacity(ed.cap, occs[i].CapacityOverride)
				if intPtrEqual(oldEff, newEff) {
					continue
				}
				lockedOcc, err := lockOccurrence(tx, occs[i].ID)
				if err != nil {
					return err
				}
				ids, err := a.promoteLocked(tx, &locked, lockedOcc, now, &me.ID)
				emailed = append(emailed, ids...)
				if err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not update event")
		return
	}
	a.deliverNotificationEmails(emailed)
	j := toEventJSON(ev)
	revealVideoToManager(&j, ev) // loadManagedEvent: only managers reach here
	c.JSON(http.StatusOK, gin.H{"event": j})
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
	var emailed []uint
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&models.EventOccurrence{}).Where("id = ? AND status <> ?", occ.ID, models.OccurrenceCancelled).
			Update("status", models.OccurrenceCancelled)
		if res.Error != nil || res.RowsAffected == 0 {
			return res.Error // already cancelled: nobody is told twice
		}
		ids, err := notifyOccurrencesCancelled(tx, apiUser(c).ID, []uint{occ.ID})
		emailed = append(emailed, ids...)
		return err
	})
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not cancel")
		return
	}
	a.deliverNotificationEmails(emailed)
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
	var body struct {
		StartsLocal      *string          `json:"startsLocal"`
		DurationMinutes  *int             `json:"durationMinutes"`
		Location         *string          `json:"location"`
		Title            *string          `json:"title"`
		CapacityOverride *json.RawMessage `json:"capacityOverride"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		jsonError(c, http.StatusBadRequest, "invalid body")
		return
	}
	newOverride, overrideSet, capMsg := parseCapacity(body.CapacityOverride)
	if capMsg != "" {
		jsonError(c, http.StatusBadRequest, capMsg)
		return
	}
	// Validate everything that doesn't need the row before the lock.
	if body.DurationMinutes != nil &&
		(*body.DurationMinutes < 1 || *body.DurationMinutes > events.MaxDurationMinutes) {
		jsonError(c, http.StatusBadRequest, "duration must be between 1 minute and 7 days")
		return
	}
	var wallStart *time.Time
	if body.StartsLocal != nil {
		t, _, err := events.ParseWall(strings.TrimSpace(*body.StartsLocal), ev.TZID)
		if err != nil {
			jsonError(c, http.StatusBadRequest, err.Error())
			return
		}
		u := t.UTC()
		wallStart = &u
	}
	if body.Location != nil && len(strings.TrimSpace(*body.Location)) > maxLocationLen {
		jsonError(c, http.StatusBadRequest, "location is too long")
		return
	}
	if body.Title != nil && len(strings.TrimSpace(*body.Title)) > maxGroupNameLen {
		jsonError(c, http.StatusBadRequest, "title is too long")
		return
	}
	me := apiUser(c)
	groupLoc := ""
	if ev.GroupID != nil {
		var g models.Group
		if err := a.DB.First(&g, *ev.GroupID).Error; err == nil {
			groupLoc = g.Location // group-ok: loadManagedEvent, only managers reach here
		}
	}
	var emailed []uint
	var inst occurrenceJSON
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		var o models.EventOccurrence
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("event_id = ? AND original_date = ?", ev.ID, date).First(&o).Error; err != nil {
			return errOccurrenceNotFound
		}
		oldStart, oldEnd := o.StartsAt, o.EndsAt
		oldPlace := ev.Location
		if o.LocationOverride != nil {
			oldPlace = *o.LocationOverride
		}
		oldOverride := o.CapacityOverride
		dur := ev.DurationMinutes
		if o.DurationOverride != nil {
			dur = *o.DurationOverride
		}
		if body.DurationMinutes != nil {
			dur = *body.DurationMinutes
			o.DurationOverride = body.DurationMinutes
		}
		start := o.StartsAt
		if wallStart != nil {
			start = *wallStart
			o.StartsAtOverride = wallStart
		}
		newPlace := oldPlace
		if body.Location != nil {
			loc := strings.TrimSpace(*body.Location)
			o.LocationOverride = &loc
			newPlace = loc
		}
		if body.Title != nil {
			title := strings.TrimSpace(*body.Title)
			o.TitleOverride = &title
		}
		if overrideSet {
			o.CapacityOverride = newOverride
		}
		o.StartsAt = start
		o.EndsAt = start.Add(time.Duration(dur) * time.Minute)
		if err := tx.Save(&o).Error; err != nil {
			return err
		}
		// event.changed fans out when the date's time or place actually
		// changed: one item per going or waitlisted person, with the
		// combined variant when both changed.
		timeChanged := !o.StartsAt.Equal(oldStart) || !o.EndsAt.Equal(oldEnd)
		placeChanged := newPlace != oldPlace
		if timeChanged || placeChanged {
			variant := "time"
			if placeChanged {
				variant = "place"
			}
			if timeChanged && placeChanged {
				variant = "time and place"
			}
			ids, err := notifyEventChanged(tx, ev, &o, variant, me.ID)
			emailed = append(emailed, ids...)
			if err != nil {
				return err
			}
		}
		// A raised per-date cap promotes into the new seats; a lowered
		// one promotes nobody and removes nobody (Decision 3).
		if overrideSet && !intPtrEqual(oldOverride, newOverride) {
			oldEff := events.EffectiveCapacity(ev.Capacity, oldOverride)
			newEff := events.EffectiveCapacity(ev.Capacity, newOverride)
			if !intPtrEqual(oldEff, newEff) {
				ids, err := a.promoteLocked(tx, ev, &o, time.Now().UTC(), &me.ID)
				emailed = append(emailed, ids...)
				if err != nil {
					return err
				}
			}
		}
		inst = occurrenceToJSON(ev, &o, groupLoc)
		applyVideoReveal(&inst, ev, false, true) // managers see the link
		inst.Visibility = ev.Visibility
		inst.LeaderOpened = ev.LeaderOpened
		inst.ChurchWide = ev.GroupID == nil
		used, err := seatsUsed(tx, o.ID)
		if err != nil {
			return err
		}
		inst.SeatsUsed = used
		if inst.Capacity != nil {
			left := *inst.Capacity - used
			if left < 0 {
				left = 0
			}
			inst.SpotsLeft = &left
		}
		var mineReg models.Registration
		if tx.Where("user_id = ? AND occurrence_id = ? AND status <> ?",
			me.ID, o.ID, models.RegCancelled).First(&mineReg).Error == nil {
			s := mineReg.Status
			inst.MyRegistration = &s
			gc := mineReg.GuestCount
			inst.MyGuestCount = &gc
			if mineReg.Status == models.RegWaitlisted {
				if pos, perr := waitlistPosition(tx, o.ID, mineReg.ID); perr == nil {
					inst.MyWaitlistPosition = &pos
				}
			}
		}
		return nil
	})
	switch {
	case errors.Is(err, errOccurrenceNotFound):
		jsonError(c, http.StatusNotFound, "that date is not on this event")
		return
	case err != nil:
		jsonError(c, http.StatusInternalServerError, "could not update this date")
		return
	}
	a.deliverNotificationEmails(emailed)
	c.JSON(http.StatusOK, gin.H{"occurrence": inst})
}
