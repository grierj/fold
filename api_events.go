package main

import (
	"encoding/json"
	"errors"
	"log"
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

// How far a list request may look. Stored occurrences go out 18 months.
const (
	maxEventWindow  = 366 * 24 * time.Hour
	settingTimezone = "timezone"
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
	c.JSON(http.StatusOK, gin.H{"name": a.siteName(), "timezone": a.churchTimezone(), "selfSignup": a.selfSignupMode(), "roomsEnabled": a.roomsEnabled()})
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

// occurrencesForEventsErr lists scheduled dates of these events and attaches
// attendee names only where policy allows. A caller who cannot see an
// event's group gets "Hosted by <church>" and no group id or name. A failed
// read is an error to answer with a 500, never an empty list.
func (a *App) occurrencesForEventsErr(eventIDs []uint, from, to string, startAfter *time.Time, me *models.User) ([]occurrenceJSON, error) {
	out, _, err := a.occurrencesForEventsAfterErr(eventIDs, from, to, startAfter, me, 0, nil)
	return out, err
}

// occurrencesForEventsAfterErr is occurrencesForEventsErr with a response cap
// (applied after date visibility, before attendee enrichment; zero keeps the
// full list) and a group-page cursor.
func (a *App) occurrencesForEventsAfterErr(eventIDs []uint, from, to string, startAfter *time.Time, me *models.User, limit int, cursor *groupUpcomingCursor) ([]occurrenceJSON, bool, error) {
	out := []occurrenceJSON{}
	if len(eventIDs) == 0 {
		return out, false, nil
	}
	// The one place event dates are listed: drop every event this viewer
	// can't see, so no caller has to remember to (hardening review).
	vs, err := a.dateCandidates(me, a.DB.Where("id IN ?", eventIDs))
	if err != nil {
		return out, false, err
	}
	if len(vs.IDs) == 0 {
		return out, false, nil
	}
	evs, byID, groups, rels, hosted := vs.Events, vs.ByID, vs.Groups, vs.Rels, vs.Hosted
	eventIDs = vs.IDs

	q := a.DB.Where("event_id IN ? AND status = ? AND original_date >= ? AND original_date <= ?",
		eventIDs, models.OccurrenceScheduled, from, to)
	if startAfter != nil {
		q = q.Where("starts_at >= ?", *startAfter)
	}
	var occs []models.EventOccurrence
	if err := q.Order("starts_at, id").Find(&occs).Error; err != nil {
		return out, false, err
	}

	truncated := false
	kept := make([]models.EventOccurrence, 0, len(occs))
	for _, o := range occs {
		if !vs.canSeeDate(me, byID[o.EventID], &o) {
			continue
		}
		if cursor != nil && (o.StartsAt.Before(cursor.StartsAt) || (o.StartsAt.Equal(cursor.StartsAt) && o.ID <= cursor.ID)) {
			continue
		}
		if limit > 0 && len(kept) >= limit {
			truncated = true
			break
		}
		kept = append(kept, o)
	}
	if len(kept) == 0 {
		return out, false, nil
	}

	ids := make([]uint, len(kept))
	for i := range kept {
		ids[i] = kept[i].ID
		kept[i].Event = *byID[kept[i].EventID]
	}
	type countRow struct {
		OccurrenceID uint
		Status       string
		N            int64
	}
	var counts []countRow
	if err := a.DB.Model(&models.Registration{}).Joins(listedRegistrations()).
		Select("registrations.occurrence_id, registrations.status, count(*) as n").
		Where("registrations.occurrence_id IN ? AND registrations.status NOT IN ?",
			ids, []string{models.RegCancelled, models.RegRemoved}).
		Group("registrations.occurrence_id, registrations.status").
		Scan(&counts).Error; err != nil {
		return out, false, err
	}
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
		if err := a.DB.Where("user_id = ? AND occurrence_id IN ? AND status <> ?", me.ID, ids, models.RegCancelled).Find(&rows).Error; err != nil {
			return out, false, err
		}
		for _, r := range rows {
			mine[r.OccurrenceID] = r
		}
	}
	// Seats used per occurrence, in one query per pool (M1.5, M1.8
	// slice 2). The counts above count rows; these count seats.
	// A failed seat or childcare total degrades to zero rather than hiding
	// the dates (TestOccurrenceCountErrorsKeepDates); it is logged.
	usedMap, err := seatsUsedMap(a.DB, ids)
	if err != nil {
		log.Printf("list event dates: seats: %v", err)
		usedMap = map[uint]int{}
	}
	remoteUsedMap, err := seatsUsedMapForMode(a.DB, ids, models.AttendanceRemote)
	if err != nil {
		log.Printf("list event dates: remote seats: %v", err)
		remoteUsedMap = map[uint]int{}
	}

	showChildcare := map[uint]bool{}
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
		showChildcare[ev.ID] = policy.CanSeeChildcare(p, viewOf(ev, hosted[ev.ID]), rel)
		showNames[ev.ID] = policy.CanSeeAttendeeNames(p, viewOf(ev, hosted[ev.ID]), gp, rel)
		showDeclined[ev.ID] = policy.CanManageEvent(p, viewOf(ev, hosted[ev.ID]), rel)
	}
	// Managers need totals; everyone else only needs to know if a
	// limited childcare pool is full. Skip dates that need neither.
	childcareIDs := []uint{}
	for _, o := range kept {
		ev := byID[o.EventID]
		if ev.ChildcareAvailable && (showChildcare[ev.ID] || ev.ChildcareCapacity != nil) {
			childcareIDs = append(childcareIDs, o.ID)
		}
	}
	childcareUsed, err := childcareUsedMap(a.DB, childcareIDs)
	if err != nil {
		log.Printf("list event dates: childcare: %v", err)
		childcareUsed = map[uint]int{}
	}
	// One set of household facts and one church-local clock per request.
	facts := a.requestFacts()
	names, removed := a.attendeeNames(kept, showNames, showChildcare, showDeclined, me, facts)
	guardians := a.confirmedContactGuardians(me, facts.users)
	for _, o := range kept {
		presentAttendeeContacts(names[o.ID], me, facts.users, guardians, showDeclined[o.EventID])
		presentAttendeeContacts(removed[o.ID], me, facts.users, guardians, showDeclined[o.EventID])
	}
	ownHousehold := a.myHouseholdAnswers(ids, me, facts)
	// Only a household account can have registered someone by name.
	householdVideo := map[uint]bool{}
	if me != nil {
		if _, inHousehold := facts.accounts[me.ID]; inHousehold {
			householdVideo, err = householdGoingMap(a.DB, ids, me, true)
			if err != nil {
				return out, false, err
			}
		}
	}

	siteName := ""
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
		inst.MyHouseholdRegistrations = ownHousehold[o.ID]
		if inst.MyHouseholdRegistrations == nil {
			inst.MyHouseholdRegistrations = []myHouseholdRegistrationJSON{}
		}
		if showDeclined[ev.ID] {
			inst.RemovedRegistrations = removed[o.ID]
		}
		inst.Visibility = policy.EffectiveVisibility(ev.Visibility, o.VisibilityOverride)
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
		inst.SpotsLeft = spotsLeftFor(inst.Capacity, used)
		remoteUsed := remoteUsedMap[o.ID]
		inst.ChildcareFull = ev.ChildcareAvailable && ev.ChildcareCapacity != nil && childcareUsed[o.ID] >= *ev.ChildcareCapacity
		if showChildcare[ev.ID] && ev.ChildcareAvailable {
			n := childcareUsed[o.ID]
			inst.ChildcareUsed = &n
			inst.ChildcareSpotsLeft = spotsLeftFor(ev.ChildcareCapacity, n)
		}
		inst.RemoteSeatsUsed = remoteUsed
		inst.RemoteSpotsLeft = spotsLeftFor(inst.RemoteCapacity, remoteUsed)
		if r, ok := mine[o.ID]; ok {
			s := r.Status
			inst.MyRegistration = &s
			gc := r.GuestCount
			inst.MyGuestCount = &gc
			inst.MyChildcareCount = &r.ChildcareCount
			inst.MyChildcareAgesNote = &r.ChildcareAgesNote
			m := attendanceModeOf(r.AttendanceMode)
			inst.MyAttendanceMode = &m
			if r.Status == models.RegWaitlisted {
				if pos, perr := waitlistPosition(a.DB, o.ID, r.ID); perr == nil {
					inst.MyWaitlistPosition = &pos
				}
			}
		}
		// The video link is per date: going on this date (or running the
		// event) reveals a registered_only link (policy.CanSeeVideoLink).
		applyVideoReveal(&inst, ev,
			mine[o.ID].Status == models.RegGoing || householdVideo[o.ID], showDeclined[ev.ID])
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
		if siteName == "" && (ev.GroupID == nil || a.groupShownTo(me, g, rel) == nil) {
			siteName = a.siteName()
		}
		a.presentGroup(&inst, ev, g, me, rel, siteName)
		if !vs.canSeeSeries(me, ev) {
			inst.hideSeries = true
			inst.DateURL = occurrenceDatePath(ev.ID, o.OriginalDate)
			inst.RecurrenceRule = ""
			inst.RRuleUntil = ""
			inst.SeriesStartsLocal = ""
			inst.SeriesTitle = ""
			inst.SeriesLocation = ""
			inst.SeriesDurationMinutes = 0
			inst.SeriesVisibility = ""
			// Like the exact-date page: a hidden parent contributes no
			// description or childcare details (PLAN 8.2). The video link
			// already follows the date page's reveal rule above.
			inst.Description = ""
			inst.ChildcareNotes = ""
			inst.ChildcareAges = ""
			inst.ChildcareCapacity = nil
		}
		out = append(out, inst)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].StartsAt.Before(out[j].StartsAt) })
	return out, truncated, nil
}

// occurrenceToJSON resolves a date's overrides without exposing viewer-specific fields.
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
		VisibilityOverride: o.VisibilityOverride, SeriesVisibility: ev.Visibility,
		StartsAt: start.UTC(), EndsAt: end.UTC(),
		StartsLocal: events.FormatWall(start, loc), EndsLocal: events.FormatWall(end, loc),
		TZID: ev.TZID, DurationMinutes: dur, Location: location, Title: title,
		Description:    ev.Description,
		RecurrenceRule: ev.RRule, RRuleUntil: until, SeriesStartsLocal: ev.StartsLocal,
		Overridden:  o.Customized(),
		SeriesTitle: ev.Title, SeriesLocation: ev.Location, SeriesDurationMinutes: ev.DurationMinutes,
		Registrations:      map[string]int64{"going": 0, "maybe": 0, "waitlisted": 0},
		Capacity:           events.EffectiveCapacity(ev.Capacity, o.CapacityOverride),
		WaitlistEnabled:    ev.WaitlistEnabled,
		GuestsAllowed:      ev.GuestsAllowed,
		ChildcareAvailable: ev.ChildcareAvailable, ChildcareNotes: ev.ChildcareNotes,
		ChildcareCapacity: ev.ChildcareCapacity, ChildcareAges: ev.ChildcareAges,
		RemoteAvailable: ev.RemoteAvailable,
		RemoteCapacity:  events.EffectiveRemoteCapacity(ev.RemoteCapacity, o.RemoteCapacityOverride),
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
		GuestsAllowed:      ev.GuestsAllowed,
		ChildcareAvailable: ev.ChildcareAvailable, ChildcareNotes: ev.ChildcareNotes,
		ChildcareCapacity: ev.ChildcareCapacity, ChildcareAges: ev.ChildcareAges,
		RemoteAvailable: ev.RemoteAvailable, RemoteCapacity: ev.RemoteCapacity,
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
	return a.groupSummaries([]models.Group{*g})[0]
}

// groupSummaries is served only after the caller selects visible groups. Each
// related kind is loaded once, regardless of the number of groups on the page.
func (a *App) groupSummaries(groups []models.Group) []groupSummaryJSON {
	out := make([]groupSummaryJSON, 0, len(groups))
	if len(groups) == 0 {
		return out
	}
	ids, leaderIDs := make([]uint, 0, len(groups)), make([]uint, 0, len(groups))
	for _, g := range groups {
		ids, leaderIDs = append(ids, g.ID), append(leaderIDs, g.LeaderID)
	}
	var counts []struct {
		GroupID uint
		Count   int64
	}
	a.DB.Model(&models.Membership{}).Select("memberships.group_id, count(*) AS count").Joins(listedUsers("memberships")).
		Where("memberships.group_id IN ? AND memberships.status = ?", ids, "active").Group("memberships.group_id").Scan(&counts)
	members := make(map[uint]int64, len(counts))
	for _, row := range counts {
		members[row.GroupID] = row.Count
	}
	var leaders []models.User
	a.DB.Where("id IN ?", leaderIDs).Find(&leaders)
	names := make(map[uint]string, len(leaders))
	for _, leader := range leaders {
		names[leader.ID] = leader.PublicName()
	}
	var dates []struct {
		GroupID  uint
		StartsAt time.Time
	}
	a.DB.Table("event_occurrences").Select("events.group_id, min(event_occurrences.starts_at) AS starts_at").
		Joins("JOIN events ON events.id = event_occurrences.event_id AND events.deleted_at IS NULL").
		Where("events.group_id IN ? AND event_occurrences.status = ? AND event_occurrences.starts_at >= ?", ids, models.OccurrenceScheduled, time.Now().UTC()).
		Group("events.group_id").Scan(&dates)
	next := make(map[uint]*time.Time, len(dates))
	for _, row := range dates {
		if !row.StartsAt.IsZero() {
			start := row.StartsAt.UTC()
			next[row.GroupID] = &start
		}
	}
	for _, g := range groups {
		out = append(out, groupSummaryJSON{
			ID: g.ID, Name: g.Name, Description: g.Description, // group-ok: callers have selected visible groups
			Tags: splitTags(g.Tags), ImageURL: g.ImageURL,
			CoverFocalX: g.CoverFocalX, CoverFocalY: g.CoverFocalY,
			Location: g.Location, LeaderName: names[g.LeaderID], MemberCount: members[g.ID], // group-ok: as above
			NextEventAt: next[g.ID],
			Status:      g.Status, LeaderID: g.LeaderID, Visibility: g.Visibility,
			DeletionRequested: g.DeletionRequested, MemberLimit: g.MemberLimit,
		})
	}
	return out
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
	// Remote attendance (M1.8 slice 2). RemoteCapacity is raw JSON like
	// Capacity: null clears to unlimited; a set limit must be positive.
	RemoteAvailable    *bool           `json:"remoteAvailable"`
	RemoteCapacity     json.RawMessage `json:"remoteCapacity"`
	ChildcareAvailable *bool           `json:"childcareAvailable"`
	ChildcareNotes     *string         `json:"childcareNotes"`
	ChildcareCapacity  json.RawMessage `json:"childcareCapacity"`
	ChildcareAges      *string         `json:"childcareAges"`
	// Video link (5.8, M1.8); see parseVideoEdits in api_video.go.
	VideoProvider *string `json:"videoProvider"`
	VideoURL      *string `json:"videoUrl"`
	VideoReveal   *string `json:"videoReveal"`
}

// specFrom builds the series rule from the body over base (nil on create),
// checks it against the window Ensure will really generate, and returns
// those dates. A rule that passes here cannot fail later as a 500 or leave
// the daily extender stuck.
func (a *App) specFrom(c *gin.Context, body eventBody, base *models.Event, creating bool) (events.Spec, []events.When, bool) {
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
		return spec, nil, false
	}
	if len(spec.RRule) > maxRecurrenceLen {
		jsonError(c, http.StatusBadRequest, "recurrence rule is too long")
		return spec, nil, false
	}
	// Check against the window Ensure and Extend will really generate, so a
	// rule that passes here cannot fail later as a 500 or a stuck extender.
	now := time.Now().UTC()
	window := now.AddDate(0, 1, 0)
	if start, _, err := events.ParseWall(spec.StartsLocal, spec.TZID); err == nil {
		window = events.WindowEnd(start, now)
	}
	whens, err := events.Expand(spec, window)
	if err != nil {
		specError(c, err)
		return spec, nil, false
	}
	return spec, whens, true
}

// specError answers a rejected series rule. A rule that makes too many dates
// has its own code so the page can say what to change.
func specError(c *gin.Context, err error) {
	if errors.Is(err, events.ErrTooManyDates) {
		jsonErrorCode(c, http.StatusBadRequest, "recurrence_too_many", err.Error())
		return
	}
	if errors.Is(err, events.ErrTooFrequent) {
		jsonErrorCode(c, http.StatusBadRequest, "recurrence_too_frequent", err.Error())
		return
	}
	jsonError(c, http.StatusBadRequest, err.Error())
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
	// Remote attendance (M1.8 slice 2).
	if body.RemoteAvailable != nil {
		ev.RemoteAvailable = *body.RemoteAvailable
	}
	if ed.remoteCapSet {
		ev.RemoteCapacity = ed.remoteCap
	}
	if body.ChildcareAvailable != nil {
		ev.ChildcareAvailable = *body.ChildcareAvailable
	}
	if body.ChildcareNotes != nil {
		ev.ChildcareNotes = strings.TrimSpace(*body.ChildcareNotes)
	}
	if body.ChildcareAges != nil {
		ev.ChildcareAges = strings.TrimSpace(*body.ChildcareAges)
	}
	if ed.childcareCapSet {
		ev.ChildcareCapacity = ed.childcareCap
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

type groupEventsJSON struct {
	Events     []occurrenceJSON `json:"events"`
	Truncated  bool             `json:"truncated"`
	HasMore    bool             `json:"hasMore"`
	NextCursor string           `json:"nextCursor"`
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
	limit, cursor, ok := parseGroupUpcomingPage(c, g.ID)
	if !ok {
		return
	}
	var eventIDs []uint
	a.DB.Model(&models.Event{}).Where("group_id = ?", g.ID).Pluck("id", &eventIDs)
	listed, hasMore, err := a.occurrencesForEventsAfterErr(eventIDs, from, to, nil, apiUser(c), limit, cursor)
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not load events")
		return
	}
	c.JSON(http.StatusOK, groupEventsJSON{
		Events: listed, Truncated: hasMore, HasMore: hasMore,
		NextCursor: nextGroupUpcomingCursor(g.ID, listed, hasMore),
	})
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

	spec, _, ok := a.specFrom(c, body, nil, true)
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
		if err := audit.Write(tx, audit.Entry{Actor: &me.ID, Action: audit.EventCreated, TargetType: "event", TargetID: ev.ID,
			Meta: map[string]any{"groupId": g.ID, "churchWide": false, "visibility": ev.Visibility}, IP: c.ClientIP()}); err != nil {
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

// errAnswered means the handler already wrote the response (a validation
// failure inside a transaction); the transaction rolls back and nothing more
// is sent.
var errAnswered = errors.New("response already sent")

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
	ed, ok := parseCapacityEdits(c, body)
	if !ok {
		return
	}
	me := apiUser(c)
	now := time.Now().UTC()
	var emailed []uint
	var locked models.Event
	// Everything that depends on the stored row is read from the locked row
	// inside the transaction, so a concurrent edit is never overwritten with
	// values built from a stale copy. Validation failures answer the request
	// themselves and return errAnswered to roll back (nothing is written yet).
	err := a.contactMutation(contactChange{EventID: ev.ID}, func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&locked, ev.ID).Error; err != nil {
			return err
		}
		if f := a.factsForIn(tx, me, &locked); !policy.CanManageEvent(asPerson(me), f.view, f.rel) {
			jsonError(c, http.StatusForbidden, "you can't change this event")
			return errAnswered
		}
		spec, whens, ok := a.specFrom(c, body, &locked, false)
		if !ok {
			return errAnswered
		}
		title, description, location := locked.Title, locked.Description, locked.Location
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
			return errAnswered
		}
		vid, ok := parseVideoEdits(c, body, &locked)
		if !ok {
			return errAnswered
		}
		var occs []models.EventOccurrence
		if err := tx.Where("event_id = ?", ev.ID).Find(&occs).Error; err != nil {
			return err
		}
		existing := make([]events.Existing, len(occs))
		for i, o := range occs {
			existing[i] = events.Existing{
				ID: o.ID, OriginalDate: o.OriginalDate, StartsAt: o.StartsAt, EndsAt: o.EndsAt,
				TimeFrozen: o.TimeFrozen(), Keep: o.Customized(),
				SeriesCancelled: o.SeriesCancelled(),
			}
		}
		actions := events.ApplySeries(existing, whens, now)
		oldCap, oldPlace := locked.Capacity, locked.Location
		oldRemoteCap := locked.RemoteCapacity
		oldChildcareCap, oldChildcareAvailable := locked.ChildcareCapacity, locked.ChildcareAvailable
		applySpec(&locked, spec, body, ed, vid)
		if err := tx.Save(&locked).Error; err != nil {
			return err
		}
		var cancelled, moved, restored []uint
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
					Updates(map[string]any{"status": models.OccurrenceCancelled, "cancel_source": models.CancelSourceSeries}).Error; err != nil {
					return err
				}
				cancelled = append(cancelled, act.ID)
			case "restore":
				// The rule includes this date again, so the cancellation the
				// series made is undone. Registrations were never touched by the
				// cancellation, so there is nothing to re-create.
				set := map[string]any{"status": models.OccurrenceScheduled, "cancel_source": ""}
				if act.Retime {
					set["starts_at"], set["ends_at"] = act.When.StartsAt, act.When.EndsAt
				}
				if err := tx.Model(&models.EventOccurrence{}).Where("id = ?", act.ID).Updates(set).Error; err != nil {
					return err
				}
				restored = append(restored, act.ID)
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
		ids, err = restoreSeriesDates(tx, me.ID, c.ClientIP(), &locked, restored)
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
		// A restored date already got its new time and place in the "back on" notice.
		for _, occID := range restored {
			movedSet[occID] = true
		}
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
		// Future scheduled dates are loaded once, after the series edit has moved or
		// cancelled some, for both fan-outs below.
		var futureOccs []models.EventOccurrence
		futureLoaded := false
		future := func() ([]models.EventOccurrence, error) {
			if !futureLoaded {
				if err := tx.Where("event_id = ? AND status = ? AND starts_at > ?", ev.ID,
					models.OccurrenceScheduled, now).Find(&futureOccs).Error; err != nil {
					return nil, err
				}
				futureLoaded = true
			}
			return futureOccs, nil
		}
		// event.changed, place variant: the event place changed, so every
		// future scheduled date without its own place changed place with it.
		if placeChanged {
			occs, err := future()
			if err != nil {
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
		// Capacity change: promote per future occurrence where an attendance
		// pool or childcare limit changed (old vs new). Promotion checks
		// the remaining room in every pool; nobody is removed (Decision 3).
		capChanged := ed.capSet && !intPtrEqual(oldCap, ed.cap)
		remoteCapChanged := ed.remoteCapSet && !intPtrEqual(oldRemoteCap, ed.remoteCap)
		childcareChanged := !intPtrEqual(oldChildcareCap, locked.ChildcareCapacity) ||
			oldChildcareAvailable != locked.ChildcareAvailable
		if capChanged || remoteCapChanged || childcareChanged {
			// The new series values, for dates a change doesn't cover.
			newCap, newRemoteCap := oldCap, oldRemoteCap
			if ed.capSet {
				newCap = ed.cap
			}
			if ed.remoteCapSet {
				newRemoteCap = ed.remoteCap
			}
			occs, err := future()
			if err != nil {
				return err
			}
			for i := range occs {
				sameIn := intPtrEqual(
					events.EffectiveCapacity(oldCap, occs[i].CapacityOverride),
					events.EffectiveCapacity(newCap, occs[i].CapacityOverride))
				sameRemote := intPtrEqual(
					events.EffectiveRemoteCapacity(oldRemoteCap, occs[i].RemoteCapacityOverride),
					events.EffectiveRemoteCapacity(newRemoteCap, occs[i].RemoteCapacityOverride))
				if sameIn && sameRemote && !childcareChanged {
					continue
				}
				lockedOcc, err := lockOccurrence(tx, occs[i].ID)
				if err != nil {
					return err
				}
				ids, err := a.promoteLocked(tx, &locked, lockedOcc, now, &me.ID)
				if err != nil {
					return err
				}
				emailed = append(emailed, ids...)
			}
		}
		return nil
	})
	if err != nil {
		switch {
		case errors.Is(err, errAnswered):
		case errors.Is(err, gorm.ErrRecordNotFound):
			jsonError(c, http.StatusNotFound, "event not found")
		default:
			jsonError(c, http.StatusInternalServerError, "could not update event")
		}
		return
	}
	a.deliverNotificationEmails(emailed)
	ev = &locked
	j := toEventJSON(ev)
	revealVideoToManager(&j, ev) // the locked row was re-checked: only managers reach here
	c.JSON(http.StatusOK, gin.H{"event": j})
}

func (a *App) apiDeleteEvent(c *gin.Context) {
	ev, ok := a.loadManagedEvent(c)
	if !ok {
		return
	}
	err := a.contactMutation(contactChange{EventID: ev.ID}, func(tx *gorm.DB) error {
		meta := map[string]any{"title": ev.Title, "churchWide": ev.GroupID == nil}
		if ev.GroupID != nil {
			meta["groupId"] = *ev.GroupID
		}
		if err := audit.Write(tx, audit.Entry{Actor: &apiUser(c).ID, Action: audit.EventDeleted, TargetType: "event", TargetID: ev.ID,
			Meta: meta, IP: c.ClientIP()}); err != nil {
			return err
		}
		return deleteEventRows(tx, []uint{ev.ID})
	})
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not delete event")
		return
	}
	c.JSON(http.StatusOK, gin.H{})
}

// deleteEventRows removes an event's registrations, guest signups, occurrences, and hosts,
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
		// Unverified guest signups point at occurrences with no cascade.
		if err := tx.Where("occurrence_id IN ?", occIDs).Delete(&models.GuestSignup{}).Error; err != nil {
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
	err := a.contactMutation(contactChange{EventID: ev.ID}, func(tx *gorm.DB) error {
		res := tx.Model(&models.EventOccurrence{}).Where("id = ? AND status <> ?", occ.ID, models.OccurrenceCancelled).
			Updates(map[string]any{"status": models.OccurrenceCancelled, "cancel_source": models.CancelSourceManual})
		if res.Error != nil || res.RowsAffected == 0 {
			if res.Error == nil {
				// Already cancelled: nobody is told twice. A person asking for
				// this date to be cancelled makes it theirs, so a later series
				// edit will not bring it back.
				res.Error = tx.Model(&models.EventOccurrence{}).
					Where("id = ? AND status = ? AND cancel_source = ?", occ.ID, models.OccurrenceCancelled, models.CancelSourceSeries).
					Update("cancel_source", models.CancelSourceManual).Error
			}
			return res.Error
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
		// RemoteCapacityOverride mirrors CapacityOverride for the
		// remote pool (M1.8 slice 2); null uses the event's cap.
		RemoteCapacityOverride json.RawMessage `json:"remoteCapacityOverride"`
		Visibility             json.RawMessage `json:"visibility"`
		Reason                 string          `json:"reason"`
		Confirm                bool            `json:"confirm"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		jsonError(c, http.StatusBadRequest, "invalid body")
		return
	}
	visibility, visibilitySet, visErr := parseDateVisibility(body.Visibility)
	if visErr != nil || len(body.Reason) > 500 {
		jsonError(c, 400, "that visibility isn't allowed on this event")
		return
	}
	newOverride, overrideSet, capMsg := parseCapacity(body.CapacityOverride)
	if capMsg != "" {
		jsonError(c, http.StatusBadRequest, capMsg)
		return
	}
	newRemoteOverride, remoteOverrideSet, capMsg := parseRemoteCapacity(body.RemoteCapacityOverride, true)
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
	var impact visibilityImpactJSON
	err := a.contactMutation(contactChange{EventID: ev.ID}, func(tx *gorm.DB) error {
		var current models.Event
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&current, ev.ID).Error; err != nil {
			return err
		}
		ev = &current
		f := a.factsForIn(tx, me, ev)
		if !policy.CanManageEvent(asPerson(me), f.view, f.rel) {
			return errOccurrenceNotFound
		}
		var o models.EventOccurrence
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("event_id = ? AND original_date = ?", ev.ID, date).First(&o).Error; err != nil {
			return errOccurrenceNotFound
		}
		if visibilitySet {
			next := policy.EffectiveVisibility(ev.Visibility, visibility)
			if !policy.CanSetEventVisibility(asPerson(me), f.view, f.rel, next) {
				return errDateVisibility
			}
			var err error
			impact, err = dateImpactOf(tx, ev, &o, next)
			if err != nil {
				return err
			}
			if !body.Confirm && impactHasLosers(impact) {
				return errNeedDecision
			}

		}
		oldStart, oldEnd := o.StartsAt, o.EndsAt
		oldPlace := ev.Location
		if o.LocationOverride != nil {
			oldPlace = *o.LocationOverride
		}
		oldOverride := o.CapacityOverride
		oldRemoteOverride := o.RemoteCapacityOverride
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
		if remoteOverrideSet {
			o.RemoteCapacityOverride = newRemoteOverride
		}
		o.StartsAt = start
		o.EndsAt = start.Add(time.Duration(dur) * time.Minute)
		if err := tx.Save(&o).Error; err != nil {
			return err
		}
		// Cancellation and promotion use the final title, time and seat pools
		// even when this request edits visibility and capacity together.
		if visibilitySet {
			ids, err := a.changeDateVisibility(c, tx, ev, &o, visibility, body.Reason, false)
			if err != nil {
				return err
			}
			emailed = append(emailed, ids...)
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
		// one promotes nobody and removes nobody (Decision 3). Either
		// pool's override counts.
		capMoved := overrideSet && !intPtrEqual(oldOverride, newOverride) &&
			!intPtrEqual(events.EffectiveCapacity(ev.Capacity, oldOverride),
				events.EffectiveCapacity(ev.Capacity, newOverride))
		remoteCapMoved := remoteOverrideSet && !intPtrEqual(oldRemoteOverride, newRemoteOverride) &&
			!intPtrEqual(events.EffectiveRemoteCapacity(ev.RemoteCapacity, oldRemoteOverride),
				events.EffectiveRemoteCapacity(ev.RemoteCapacity, newRemoteOverride))
		if capMoved || remoteCapMoved {
			ids, err := a.promoteLocked(tx, ev, &o, time.Now().UTC(), &me.ID)
			if err != nil {
				return err
			}
			emailed = append(emailed, ids...)
		}
		inst = occurrenceToJSON(ev, &o, groupLoc)
		applyVideoReveal(&inst, ev, false, true) // managers see the link
		inst.Visibility = policy.EffectiveVisibility(ev.Visibility, o.VisibilityOverride)
		inst.LeaderOpened = ev.LeaderOpened
		inst.ChurchWide = ev.GroupID == nil
		used, err := seatsUsed(tx, o.ID)
		if err != nil {
			return err
		}
		inst.SeatsUsed = used
		inst.SpotsLeft = spotsLeftFor(inst.Capacity, used)
		remoteUsed, err := remoteSeatsUsed(tx, o.ID)
		if err != nil {
			return err
		}
		inst.RemoteSeatsUsed = remoteUsed
		inst.RemoteSpotsLeft = spotsLeftFor(inst.RemoteCapacity, remoteUsed)
		if ev.ChildcareAvailable {
			n, err := childcareUsedFor(tx, o.ID, 0)
			if err != nil {
				return err
			}
			inst.ChildcareUsed = &n
			inst.ChildcareSpotsLeft = spotsLeftFor(ev.ChildcareCapacity, n)
		}
		var mineReg models.Registration
		if tx.Where("user_id = ? AND occurrence_id = ? AND status <> ?",
			me.ID, o.ID, models.RegCancelled).First(&mineReg).Error == nil {
			s := mineReg.Status
			inst.MyRegistration = &s
			gc := mineReg.GuestCount
			inst.MyGuestCount = &gc
			inst.MyChildcareCount = &mineReg.ChildcareCount
			inst.MyChildcareAgesNote = &mineReg.ChildcareAgesNote
			m := attendanceModeOf(mineReg.AttendanceMode)
			inst.MyAttendanceMode = &m
			if mineReg.Status == models.RegWaitlisted {
				if pos, perr := waitlistPosition(tx, o.ID, mineReg.ID); perr == nil {
					inst.MyWaitlistPosition = &pos
				}
			}
		}
		return nil
	})
	switch {
	case errors.Is(err, errNeedDecision):
		c.JSON(409, gin.H{"needsDecision": true, "impact": impact})
		return
	case errors.Is(err, errDateVisibility):
		jsonError(c, 400, "that visibility isn't allowed on this event")
		return
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

// restoreSeriesDates records dates a series edit just brought back and tells
// the people signed up for them. It runs in the series edit's transaction,
// after the rows are scheduled again, so the audit row, the notices and the
// change commit together. Nothing here touches registrations.
func restoreSeriesDates(tx *gorm.DB, actor uint, ip string, ev *models.Event, occIDs []uint) ([]uint, error) {
	if len(occIDs) == 0 {
		return nil, nil
	}
	var dates []string
	if err := tx.Model(&models.EventOccurrence{}).Where("id IN ?", occIDs).Order("original_date").Pluck("original_date", &dates).Error; err != nil {
		return nil, err
	}
	if err := audit.Write(tx, audit.Entry{Actor: &actor, Action: audit.EventDatesRestored, TargetType: "event", TargetID: ev.ID,
		Meta: map[string]any{"dates": dates, "occurrenceIds": occIDs}, IP: ip}); err != nil {
		return nil, err
	}
	return notifyOccurrencesRestored(tx, actor, occIDs)
}
