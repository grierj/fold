package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"fold/internal/audit"
	"fold/internal/eventdates"
	"fold/internal/events"
	"fold/internal/notify"
	"fold/internal/policy"
	"fold/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// How far a list request may look. Stored occurrences go out 18 months.
const (
	maxEventWindow = 366 * 24 * time.Hour
)

func (a *App) apiSite(c *gin.Context) {
	c.JSON(http.StatusOK, siteJSON{Name: a.siteName(), Timezone: churchZoneOrUTC(a.DB).String(), SelfSignup: a.selfSignupMode(), RoomsEnabled: a.roomsEnabled(), PushEnabled: a.pushEnabled()})
}

func (a *App) apiSetTimezone(c *gin.Context) {
	var body struct {
		Timezone string `json:"timezone"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		fail(c, errTimezoneRequired)
		return
	}
	tz := strings.TrimSpace(body.Timezone)
	if _, err := events.LoadZone(tz); err != nil {
		fail(c, errInvalidTimezone)
		return
	}
	if err := setSettingIn(a.DB, settingTimezone, tz); err != nil {
		jsonError(c, http.StatusInternalServerError, "could not save timezone")
		return
	}
	c.JSON(http.StatusOK, timezoneResponseJSON{Timezone: tz})
}

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
	// can't see, so no caller has to remember to.
	vs, err := a.dateCandidates(me, a.DB.Where("id IN ?", eventIDs))
	if err != nil {
		return out, false, err
	}
	if len(vs.IDs) == 0 {
		return out, false, nil
	}
	evs, byID := vs.Events, vs.ByID
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
	usedMap, err := seatsUsedMapForMode(a.DB, ids, models.AttendanceInPerson)
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
		f := vs.of(ev)
		showChildcare[ev.ID] = policy.CanSeeChildcare(p, f.view, f.rel)
		showNames[ev.ID] = policy.CanSeeAttendeeNames(p, f.view, asGroup(f.group), f.rel)
		showDeclined[ev.ID] = f.canManage(me)
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
	names, removed, err := a.attendeeNames(kept, showNames, showChildcare, showDeclined, me, facts)
	if err != nil {
		return out, false, err
	}
	guardians := a.confirmedContactGuardians(me, facts.users)
	for _, o := range kept {
		presentAttendeeContacts(names[o.ID], me, facts.users, guardians, showDeclined[o.EventID])
		presentAttendeeContacts(removed[o.ID], me, facts.users, guardians, showDeclined[o.EventID])
	}
	ownHousehold, err := a.myHouseholdAnswers(ids, me, facts)
	if err != nil {
		return out, false, err
	}
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

	siteName := &siteNameOnce{a: a}
	keptIDs := make([]uint, len(kept))
	for i := range kept {
		keptIDs[i] = kept[i].ID
	}
	rooms := a.occurrenceRooms(a.DB, keptIDs)
	for i := range kept {
		o := &kept[i]
		date, ok := a.projectDate(me, vs, o, nil)
		if !ok {
			continue
		}
		ev := date.Event
		inst := date.occurrenceJSON(siteName)
		inst.Room = rooms[o.ID]
		inst.MyHouseholdRegistrations = ownHousehold[o.ID]
		if inst.MyHouseholdRegistrations == nil {
			inst.MyHouseholdRegistrations = []myHouseholdRegistrationJSON{}
		}
		if showDeclined[ev.ID] {
			inst.RemovedRegistrations = removed[o.ID]
		}
		inst.Registrations = registrationCounts(countOf[o.ID], showDeclined[ev.ID])
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
			if err := inst.setMine(a.DB, &r); err != nil {
				return out, false, err
			}
		}
		// The video link is per date: going on this date (or running the
		// event) reveals a registered_only link (policy.CanSeeVideoLink).
		date.listVideo(&inst, mine[o.ID].Status == models.RegGoing || householdVideo[o.ID])
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
		out = append(out, inst)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].StartsAt.Before(out[j].StartsAt) })
	return out, truncated, nil
}

// changeVariant names what changed on a date for its event.changed notice:
// the time, the place, or both. At least one is true.
func changeVariant(timeChanged, placeChanged bool) string {
	switch {
	case timeChanged && placeChanged:
		return "time and place"
	case placeChanged:
		return "place"
	}
	return "time"
}

func (o *occurrenceJSON) setMine(db *gorm.DB, r *models.Registration) error {
	status, guests, childcare, note := r.Status, r.GuestCount, r.ChildcareCount, r.ChildcareAgesNote
	mode := attendanceModeOf(r.AttendanceMode)
	o.MyRegistration, o.MyGuestCount, o.MyChildcareCount, o.MyChildcareAgesNote, o.MyAttendanceMode =
		&status, &guests, &childcare, &note, &mode
	if r.Status == models.RegWaitlisted {
		pos, err := waitlistPosition(db, o.ID, r.ID)
		if err != nil {
			return err
		}
		o.MyWaitlistPosition = &pos
	}
	return nil
}

// registrationCounts is a date's row counts by answer. Going, maybe and
// waitlisted are always present; "can't go" (declined) only for the people who
// run the event (DEC-64).
func registrationCounts(counted map[string]int64, withDeclined bool) map[string]int64 {
	out := map[string]int64{models.RegGoing: 0, models.RegMaybe: 0, models.RegWaitlisted: 0}
	if withDeclined {
		out[models.RegDeclined] = 0
	}
	for status, n := range counted {
		if status != models.RegDeclined || withDeclined {
			out[status] = n
		}
	}
	return out
}

func effectiveOccurrenceTitle(ev *models.Event, o *models.EventOccurrence) string {
	if o.TitleOverride != nil {
		return *o.TitleOverride
	}
	return ev.Title
}

func effectiveOccurrenceDuration(ev *models.Event, o *models.EventOccurrence) int {
	if o.DurationOverride != nil {
		return *o.DurationOverride
	}
	return ev.DurationMinutes
}

// effectiveOccurrencePlace is the date's own place, else its event's, else
// groupPlace (events.EffectivePlace; "" when the viewer may not see the group).
func effectiveOccurrencePlace(ev *models.Event, o *models.EventOccurrence, groupPlace string) string {
	return events.EffectivePlace(ev.Location, o.LocationOverride, groupPlace)
}

func occurrenceToJSON(ev *models.Event, o *models.EventOccurrence, groupLoc string) occurrenceJSON {
	loc := eventZone(ev.TZID)
	dur := effectiveOccurrenceDuration(ev, o)
	start := o.StartsAt.In(loc)
	end := start.Add(time.Duration(dur) * time.Minute)
	title := effectiveOccurrenceTitle(ev, o)
	location := effectiveOccurrencePlace(ev, o, groupLoc)
	until := ""
	if ev.RRuleUntil != nil {
		until = models.FormatCalendarDate(*ev.RRuleUntil)
	}
	inst := occurrenceJSON{
		ID: o.ID, EventID: ev.ID, Date: models.FormatCalendarDate(o.OriginalDate),
		VisibilityOverride: o.VisibilityOverride, SeriesVisibility: ev.Visibility,
		StartsAt: start.UTC(), EndsAt: end.UTC(),
		StartsLocal: events.FormatWall(start, loc), EndsLocal: events.FormatWall(end, loc),
		TZID: ev.TZID, DurationMinutes: dur, Location: location, Title: title,
		Description:    ev.Description,
		RecurrenceRule: ev.RRule, RRuleUntil: until, SeriesStartsLocal: ev.StartsLocal,
		Overridden:  o.Customized(),
		SeriesTitle: ev.Title, SeriesLocation: ev.Location, SeriesDurationMinutes: ev.DurationMinutes,
		Registrations:        registrationCounts(nil, false),
		Capacity:             events.EffectiveCapacity(ev.Capacity, o.CapacityOverride),
		WaitlistEnabled:      ev.WaitlistEnabled,
		GuestsAllowed:        ev.GuestsAllowed,
		ReminderEmailMinutes: ev.ReminderEmailMinutes,
		ReminderPush:         ev.ReminderPush,
		ChildcareAvailable:   ev.ChildcareAvailable, ChildcareNotes: ev.ChildcareNotes,
		ChildcareCapacity: ev.ChildcareCapacity, ChildcareAges: ev.ChildcareAges,
		RemoteAvailable: ev.RemoteAvailable,
		RemoteCapacity:  events.EffectiveCapacity(ev.RemoteCapacity, o.RemoteCapacityOverride),
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
		until = models.FormatCalendarDate(*ev.RRuleUntil)
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
		ReminderEmailMinutes: ev.ReminderEmailMinutes,
		ReminderPush:         ev.ReminderPush,
		VideoProvider:        videoProviderOf(ev), VideoReveal: videoRevealOf(ev),
	}
	// Fail closed like occurrenceToJSON; presentEvent refines for the
	// event's managers, and create/update responses go to a manager.
	applyEventVideoReveal(&j, ev, false, false)
	return j
}

func (a *App) groupSummary(viewer *models.User, g *models.Group) (groupSummaryJSON, error) {
	out, err := a.groupSummaries(viewer, []models.Group{*g})
	if err != nil {
		return groupSummaryJSON{}, err
	}
	return out[0], nil
}

// groupSummaries is served only after the caller selects visible groups. Each
// related kind is loaded once, regardless of the number of groups on the page.
// NextEventAt is the viewer's first scheduled date at or after now that they
// may see, decided per date by visibleSet.canSeeDate (event visibility and the
// date's override), never by a SQL copy of the policy: a hidden date must not
// reveal its time through the group card or the directory order.
func (a *App) groupSummaries(viewer *models.User, groups []models.Group) ([]groupSummaryJSON, error) {
	out := make([]groupSummaryJSON, 0, len(groups))
	if len(groups) == 0 {
		return out, nil
	}
	ids, leaderIDs := make([]uint, 0, len(groups)), make([]uint, 0, len(groups))
	for _, g := range groups {
		ids, leaderIDs = append(ids, g.ID), append(leaderIDs, g.LeaderID)
	}
	var counts []struct {
		GroupID uint
		Count   int64
	}
	if err := a.DB.Model(&models.Membership{}).Select("memberships.group_id, count(*) AS count").Joins(listedUsers("memberships")).
		Where("memberships.group_id IN ? AND memberships.status = ?", ids, models.MembershipActive).Group("memberships.group_id").Scan(&counts).Error; err != nil {
		return nil, err
	}
	members := make(map[uint]int64, len(counts))
	for _, row := range counts {
		members[row.GroupID] = row.Count
	}
	var leaders []models.User
	if err := a.DB.Where("id IN ?", leaderIDs).Find(&leaders).Error; err != nil {
		return nil, err
	}
	names := make(map[uint]string, len(leaders))
	for _, leader := range leaders {
		names[leader.ID] = leader.PublicName()
	}
	next, err := nextVisibleDates(a.DB, viewer, ids)
	if err != nil {
		return nil, err
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
	return out, nil
}

// nextVisibleDates maps each group to the viewer's first upcoming scheduled
// date. Whether a date is visible depends only on its event, the viewer and the
// date's override, so one grouped minimum per (event, override) holds the
// first visible date without reading every occurrence. The number of
// statements does not grow with the number of groups. The parity test against
// full upcoming occurrences guards the synthetic occurrence used below.
func nextVisibleDates(db *gorm.DB, viewer *models.User, groupIDs []uint) (map[uint]*time.Time, error) {
	if db == nil || db.Statement == nil {
		return nil, errNextDatesSnapshot
	}
	var next map[uint]*time.Time
	load := func(tx *gorm.DB) error {
		var err error
		next, err = nextVisibleDatesIn(tx, viewer, groupIDs)
		return err
	}
	var err error
	if c, borrowed := db.Statement.ConnPool.(gorm.TxCommitter); borrowed {
		if c == nil || reflect.ValueOf(c).IsNil() {
			return nil, errNextDatesSnapshot
		}
		// GORM implements a nested Transaction with a savepoint and ignores its
		// isolation options. Never upgrade or end a transaction owned by a caller.
		var isolation string
		if err = db.Raw("SHOW transaction_isolation").Scan(&isolation).Error; err == nil {
			if isolation != "repeatable read" && isolation != "serializable" {
				err = errNextDatesSnapshot
			} else {
				err = load(db)
			}
		}
	} else {
		err = db.Transaction(load, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	}
	if err != nil {
		return nil, err
	}
	return next, nil
}

var errNextDatesSnapshot = errors.New("next visible dates requires a repeatable-read snapshot")

// Candidates, policy facts and all minimum chunks must share a snapshot: a
// tightening committed between them must not pair old authority with new dates.
func nextVisibleDatesIn(tx *gorm.DB, viewer *models.User, groupIDs []uint) (map[uint]*time.Time, error) {
	now := time.Now().UTC()
	vs, err := loadCandidates(tx, viewer, tx.Where(
		"group_id IN ? AND id IN (SELECT event_id FROM event_occurrences WHERE status = ? AND starts_at >= ?)",
		groupIDs, models.OccurrenceScheduled, now), false)
	if err != nil {
		return nil, err
	}
	next := map[uint]*time.Time{}
	if len(vs.IDs) == 0 {
		return next, nil
	}
	err = eachIDChunk(vs.IDs, func(ids []uint) error {
		var rows []struct {
			EventID            uint
			VisibilityOverride *string
			StartsAt           time.Time
		}
		if err := tx.Table("event_occurrences").
			Select("event_id, visibility_override, min(starts_at) AS starts_at").
			Where("event_id IN ? AND status = ? AND starts_at >= ?", ids, models.OccurrenceScheduled, now).
			Group("event_id, visibility_override").Scan(&rows).Error; err != nil {
			return err
		}
		for _, row := range rows {
			ev := vs.ByID[row.EventID]
			if ev == nil || ev.GroupID == nil || row.StartsAt.IsZero() {
				continue
			}
			occ := models.EventOccurrence{EventID: row.EventID, VisibilityOverride: row.VisibilityOverride, StartsAt: row.StartsAt}
			if !vs.canSeeDate(viewer, ev, &occ) {
				continue
			}
			start := row.StartsAt.UTC()
			if cur := next[*ev.GroupID]; cur == nil || start.Before(*cur) {
				next[*ev.GroupID] = &start
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return next, nil
}

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
	VideoProvider      *string         `json:"videoProvider"`
	VideoURL           *string         `json:"videoUrl"`
	VideoReveal        *string         `json:"videoReveal"`
	// ReminderEmailMinutes: when each date's email reminder goes (M3.6):
	// 0 off, 120, 1440, 2880 or 10080 minutes before.
	ReminderEmailMinutes *int `json:"reminderEmailMinutes"`
	// ReminderPush: each date's 2-hour push reminder (M3.13d3), on by
	// default.
	ReminderPush *bool `json:"reminderPush"`
	// Create only: the room for every date (M2.1c). A series edit changes
	// it through PUT /events/:id/room.
	RoomID *uint `json:"roomId"`
}

// specFrom builds the series rule from the body over base (nil on create),
// checks it against the window Ensure will really generate, and returns
// those dates. A rule that passes here cannot fail later as a 500 or leave
// the daily extender stuck.
func (a *App) specFrom(body eventBody, base *models.Event, creating bool) (events.Spec, []events.When, error) {
	spec := events.Spec{TZID: churchZoneOrUTC(a.DB).String()}
	if base != nil {
		spec.StartsLocal = base.StartsLocal
		spec.DurationMinutes = base.DurationMinutes
		spec.TZID = base.TZID
		spec.RRule = base.RRule
		if base.RRuleUntil != nil {
			spec.Until = models.FormatCalendarDate(*base.RRuleUntil)
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
	spec.StartsLocal = padWallHour(spec.StartsLocal)
	if creating && (body.StartsLocal == nil || body.DurationMinutes == nil) {
		return spec, nil, errStartRequired
	}
	if len(spec.RRule) > maxRecurrenceLen {
		return spec, nil, errRecurrenceRuleTooLong
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
		return spec, nil, specAnswer(err)
	}
	for _, when := range whens {
		if _, err := models.ParseCalendarDate(when.OriginalDate); err != nil {
			return spec, nil, specAnswer(err)
		}
	}
	return spec, whens, nil
}

// padWallHour makes 2026-10-20T9:00 into 2026-10-20T09:00. ParseWall accepts
// a one-digit hour, while the events_starts_local_check column rule wants the
// full YYYY-MM-DDTHH:MM, so such an edit is stored padded instead of failing
// at the insert. Anything else is left for ParseWall to judge.
func padWallHour(s string) string {
	if len(s) == len("2006-01-02T9:04") && s[10] == 'T' && s[12] == ':' {
		return s[:11] + "0" + s[11:]
	}
	return s
}

// specAnswer is the answer to a rejected series rule. A rule that makes too
// many dates has its own code so the page can say what to change.
func specAnswer(err error) error {
	for _, r := range specRefusals {
		if errors.Is(err, r.from) {
			return r.answer
		}
	}
	return apiErrf(http.StatusBadRequest, "", "%s", err.Error())
}

// specRefusals answer the series-rule errors internal/events owns, which
// cannot name an HTTP answer. Their wording is the events package's.
var specRefusals = []struct {
	from   error
	answer *apiError
}{
	{events.ErrTooManyDates, apiErr(http.StatusBadRequest, "recurrence_too_many", events.ErrTooManyDates.Error())},
	{events.ErrTooFrequent, apiErr(http.StatusBadRequest, "recurrence_too_frequent", events.ErrTooFrequent.Error())},
	{events.ErrBadStart, errInvalidStart},
	{events.ErrBadDuration, errInvalidDuration},
}

func applySpec(ev *models.Event, spec events.Spec, body eventBody, ed capacityEdits, vid videoEdits) {
	ev.StartsLocal = spec.StartsLocal
	ev.DurationMinutes = spec.DurationMinutes
	ev.TZID = spec.TZID
	ev.RRule = spec.RRule
	// specFrom already ran spec through events.Expand, which refuses a
	// date that is not a real YYYY-MM-DD, so the parse here cannot fail.
	ev.RRuleUntil = nil
	if spec.Until != "" {
		if u, err := models.ParseCalendarDate(spec.Until); err == nil {
			ev.RRuleUntil = &u
		}
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
	if ed.reminderSet {
		ev.ReminderEmailMinutes = ed.reminder
	}
	if body.ReminderPush != nil {
		ev.ReminderPush = *body.ReminderPush
	}
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
	ev.VideoProvider = vid.provider
	ev.VideoURL = vid.url
	ev.VideoReveal = vid.reveal
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
	fd, err1 := models.ParseCalendarDate(from)
	td, err2 := models.ParseCalendarDate(to)
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
	g, _, ok := a.loadGroupFor(c, uint(id))
	if !ok {
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
	if err := a.DB.Model(&models.Event{}).Where("group_id = ?", g.ID).Pluck("id", &eventIDs).Error; err != nil {
		jsonError(c, http.StatusInternalServerError, "could not load events")
		return
	}
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

// draftEvent validates an event body and applies it to ev (the caller's
// starting values: its group, visibility, creator), the part of creating an
// event that group and church-wide events share. The error is the answer.
func (a *App) draftEvent(body eventBody, ev models.Event) (models.Event, error) {
	ed, err := parseCapacityEdits(body)
	if err != nil {
		return ev, err
	}
	vid, err := parseVideoEdits(body, nil)
	if err != nil {
		return ev, err
	}
	spec, _, err := a.specFrom(body, nil, true)
	if err != nil {
		return ev, err
	}
	ev.ReminderEmailMinutes, ev.ReminderPush = defaultReminderMinutes, true
	applySpec(&ev, spec, body, ed, vid)
	return ev, validateEventText(ev.Title, ev.Description, ev.Location)
}

// createEventIn saves a drafted event in the caller's transaction: the row,
// its created audit entry (with meta), whatever the caller adds after the row
// (hosts, a visibility override), the dates, and the series room booking.
func (a *App) createEventIn(tx *gorm.DB, ev *models.Event, creator *models.User, ip string, roomID *uint, meta map[string]any, after func() error) error {
	if err := createEvent(tx, ev); err != nil {
		return err
	}
	if err := audit.Write(tx, audit.Entry{Actor: &creator.ID, Action: audit.EventCreated, TargetType: "event", TargetID: ev.ID, Meta: meta, IP: ip}); err != nil {
		return err
	}
	if after != nil {
		if err := after(); err != nil {
			return err
		}
	}
	if err := eventdates.Ensure(tx, ev, time.Now().UTC()); err != nil {
		return err
	}
	return a.bookCreatedSeries(tx, ev, roomID, creator, ip)
}

func (a *App) apiCreateEvent(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	g, _, ok := a.loadManagedGroupID(c, uint(id))
	if !ok {
		return
	}
	var body eventBody
	if err := c.ShouldBindJSON(&body); err != nil {
		fail(c, errStartRequired)
		return
	}
	me := apiUser(c)
	gid := g.ID
	ev, err := a.draftEvent(body, models.Event{GroupID: &gid, CreatedBy: me.ID})
	if err != nil {
		fail(c, err)
		return
	}
	reason := ""
	if body.VisibilityReason != nil {
		reason = strings.TrimSpace(*body.VisibilityReason)
	}
	if len(reason) > 500 {
		fail(c, errReasonTooLong)
		return
	}
	vis := ""
	if body.Visibility != nil {
		vis = strings.TrimSpace(*body.Visibility)
		if vis != "" && !policy.ValidVisibility(vis) {
			fail(c, errVisibilityNotAllowed)
			return
		}
	}
	errCreateGroupHidden := errors.New("group not found")
	errCreateGroupForbidden := errors.New("group event create forbidden")
	err = a.contactMutation(contactChange{GroupID: gid}, func(tx *gorm.DB) error {
		// The gate serializes group-first invitation/departure writers with
		// this actor-first order. No event is held before the group SHARE,
		// unlike the booking paths that need groupSchedulesIn's plain read.
		actor, err := lockActorIn(tx, me, actorNoKeyUpdate)
		if err != nil {
			return err
		}
		var locked models.Group
		if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).First(&locked, gid).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errCreateGroupHidden
			}
			return err
		}
		rel, err := groupRel(tx, actor.ID, gid)
		if err != nil {
			return err
		}
		if !policy.CanSeeGroup(asPerson(actor), asGroup(&locked), rel) {
			return errCreateGroupHidden
		}
		if !policy.CanManageGroup(asPerson(actor), rel) {
			return errCreateGroupForbidden
		}
		// DEC-147: a refusal writes no event, date, audit row or booking.
		if !policy.GroupCanSchedule(locked.Status) {
			return errGroupEventsNotApproved
		}
		ev.Visibility, ev.LeaderOpened = locked.Visibility, false
		if vis != "" {
			if !policy.CanSetEventVisibility(asPerson(actor), viewOf(&ev, false), rel, vis) {
				return errVisibilityNotAllowed
			}
			ev.Visibility = vis
			ev.LeaderOpened = policy.MoreOpen(vis, locked.Visibility)
		}
		meta := map[string]any{"groupId": gid, "churchWide": false, "visibility": ev.Visibility}
		return a.createEventIn(tx, &ev, actor, c.ClientIP(), body.RoomID, meta, func() error {
			if ev.Visibility == locked.Visibility {
				return nil
			}
			action := audit.VisibilityChanged
			if ev.LeaderOpened {
				action = audit.VisibilityOverride
			}
			meta := map[string]any{"oldVisibility": locked.Visibility, "newVisibility": ev.Visibility,
				"oldLeaderOpened": false, "leaderOpened": ev.LeaderOpened, "groupVisibility": locked.Visibility, "atCreation": true}
			if reason != "" {
				meta["reason"] = reason
			}
			if err := audit.Write(tx, audit.Entry{Actor: &actor.ID, Action: action, TargetType: "event", TargetID: ev.ID,
				Meta: meta, IP: c.ClientIP()}); err != nil {
				return err
			}
			if ev.LeaderOpened {
				return notifyVisibilityOverride(tx, actor.ID, &ev, &locked, ev.Visibility, reason)
			}
			return nil
		})
	})
	if errors.Is(err, errCreateGroupHidden) {
		jsonError(c, http.StatusNotFound, "group not found")
		return
	}
	if errors.Is(err, errCreateGroupForbidden) {
		jsonError(c, http.StatusForbidden, "forbidden")
		return
	}
	if err != nil {
		fail(c, err)
		return
	}
	j := toEventJSON(&ev)
	applyEventVideoReveal(&j, &ev, false, true) // the creator leads the group or is an admin
	c.JSON(http.StatusCreated, eventSeriesResponseJSON{Event: j})
}

func (a *App) loadManagedEvent(c *gin.Context) (*models.Event, bool) {
	ev, _, err := a.loadManagedEventFor(c)
	if err != nil {
		fail(c, err)
	}
	return ev, err == nil
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
	ed, err := parseCapacityEdits(body)
	if err != nil {
		fail(c, err)
		return
	}
	me := apiUser(c)
	now := a.now().UTC()
	var locked models.Event
	// Everything that depends on the stored row is read from the locked row
	// inside the transaction, so a concurrent edit is never overwritten with
	// values built from a stale copy. A validation failure is returned as its
	// answer and rolls back (nothing is written yet).
	err = a.contactMutation(contactChange{EventID: ev.ID}, func(tx *gorm.DB) error {
		actor, lockedEv, _, err := lockManagedEventIn(tx, me, ev.ID)
		if err != nil {
			return err
		}
		me = actor
		locked = *lockedEv
		spec, whens, err := a.specFrom(body, &locked, false)
		if err != nil {
			return err
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
		if err := validateEventText(title, description, location); err != nil {
			return err
		}
		vid, err := parseVideoEdits(body, &locked)
		if err != nil {
			return err
		}
		var occs []models.EventOccurrence
		if err := tx.Where("event_id = ?", ev.ID).Find(&occs).Error; err != nil {
			return err
		}
		existing := make([]events.Existing, len(occs))
		for i, o := range occs {
			existing[i] = events.Existing{
				ID: o.ID, OriginalDate: models.FormatCalendarDate(o.OriginalDate), StartsAt: o.StartsAt, EndsAt: o.EndsAt,
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
		var cancelled, moved, restored, added []uint
		for _, act := range actions {
			switch act.Kind {
			case events.ActionMove:
				if err := tx.Model(&models.EventOccurrence{}).Where("id = ?", act.ID).
					Updates(map[string]any{"starts_at": act.When.StartsAt, "ends_at": act.When.EndsAt}).Error; err != nil {
					return err
				}
				moved = append(moved, act.ID)
			case events.ActionCancel:
				if err := tx.Model(&models.EventOccurrence{}).Where("id = ?", act.ID).
					Updates(map[string]any{"status": models.OccurrenceCancelled, "cancel_source": models.CancelSourceSeries}).Error; err != nil {
					return err
				}
				cancelled = append(cancelled, act.ID)
			case events.ActionRestore:
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
			case events.ActionAdd:
				addDate, err := models.ParseCalendarDate(act.When.OriginalDate)
				if err != nil {
					return err
				}
				row := models.EventOccurrence{
					EventID: ev.ID, OriginalDate: addDate,
					StartsAt: act.When.StartsAt, EndsAt: act.When.EndsAt,
					Status: models.OccurrenceScheduled,
				}
				if err := tx.Create(&row).Error; err != nil {
					return err
				}
				added = append(added, row.ID)
			}
		}
		err = notifyOccurrences(tx, me.ID, cancelled, notify.OccurrenceCancelled)
		if err != nil {
			return err
		}
		// Cancelled dates free their rooms; moved dates take their bookings
		// along (9.2). Added dates, and restored ones (whose series cancel
		// freed their room), get the series room if there is one.
		if err := releaseRoomBookings(tx, cancelled); err != nil {
			return err
		}
		err = settleDateRooms(tx, &locked, moved, append(added, restored...), &me.ID)
		if err != nil {
			return err
		}
		err = restoreSeriesDates(tx, me.ID, c.ClientIP(), &locked, restored)
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
			if err := tx.Where("id IN ?", moved).Find(&movedRows).Error; err != nil {
				return err
			}
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
			err := notifyEventChanged(tx, &locked, o, changeVariant(true, placeChanged && o.LocationOverride == nil), &me.ID)
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
				err := notifyEventChanged(tx, &locked, &occs[i], "place", &me.ID)
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
					events.EffectiveCapacity(oldRemoteCap, occs[i].RemoteCapacityOverride),
					events.EffectiveCapacity(newRemoteCap, occs[i].RemoteCapacityOverride))
				if sameIn && sameRemote && !childcareChanged {
					continue
				}
				lockedOcc, err := lockOccurrence(tx, occs[i].ID)
				if err != nil {
					return err
				}
				err = a.promoteLocked(tx, &locked, lockedOcc, now, &me.ID)
				if err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		switch {
		case isAPIError(err):
			fail(c, err)
		case errors.Is(err, gorm.ErrRecordNotFound):
			jsonError(c, http.StatusNotFound, "event not found")
		default:
			jsonError(c, http.StatusInternalServerError, "could not update event")
		}
		return
	}
	ev = &locked
	j := toEventJSON(ev)
	applyEventVideoReveal(&j, ev, false, true) // the locked row was re-checked: only managers reach here
	c.JSON(http.StatusOK, eventSeriesResponseJSON{Event: j})
}

// lockManagedEventIn requires the contact gate, which serializes role and
// relationship revocations. A plain actor read keeps the event as the first
// row lock: locking the actor could cycle with ungated registration writers.
func lockManagedEventIn(tx *gorm.DB, session *models.User, eventID uint) (*models.User, *models.Event, eventFacts, error) {
	actor, err := lockActorIn(tx, session, actorRead)
	if err != nil {
		return nil, nil, eventFacts{}, err
	}
	var locked models.Event
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&locked, eventID).Error; err != nil {
		return nil, nil, eventFacts{}, err
	}
	f, err := eventFactsIn(tx, actor, &locked)
	if err != nil {
		return nil, nil, eventFacts{}, err
	}
	if !f.canManage(actor) {
		return nil, nil, eventFacts{}, errCannotChangeEvent
	}
	return actor, &locked, f, nil
}

// answerEventWrite answers a failed write on a locked event: its own answer,
// 404 when the event went away while the request waited, else a 500.
func answerEventWrite(c *gin.Context, err error, failure string) {
	switch {
	case isAPIError(err):
		fail(c, err)
	case errors.Is(err, gorm.ErrRecordNotFound):
		jsonError(c, http.StatusNotFound, "event not found")
	default:
		jsonError(c, http.StatusInternalServerError, failure)
	}
}

func (a *App) apiDeleteEvent(c *gin.Context) {
	ev, ok := a.loadManagedEvent(c)
	if !ok {
		return
	}
	err := a.contactMutation(contactChange{EventID: ev.ID}, func(tx *gorm.DB) error {
		actor, ev, _, err := lockManagedEventIn(tx, apiUser(c), ev.ID)
		if err != nil {
			return err
		}
		meta := map[string]any{"title": ev.Title, "churchWide": ev.GroupID == nil}
		if ev.GroupID != nil {
			meta["groupId"] = *ev.GroupID
		}
		if err := audit.Write(tx, audit.Entry{Actor: &actor.ID, Action: audit.EventDeleted, TargetType: "event", TargetID: ev.ID,
			Meta: meta, IP: c.ClientIP()}); err != nil {
			return err
		}
		return deleteEventRows(tx, []uint{ev.ID})
	})
	if err != nil {
		answerEventWrite(c, err, "could not delete event")
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
	// Lock the events before any child row goes: a concurrent host add
	// (FOR KEY SHARE on the event) either lands first and is deleted below, or
	// waits and finds the event gone. Find, not First, and Unscoped, because
	// deleteGroup passes ids that include already soft-deleted events whose
	// leftovers it still cleans, and absent ids must not raise an error.
	var locked []models.Event
	if err := tx.Unscoped().Clauses(clause.Locking{Strength: "UPDATE"}).Select("id").
		Where("id IN ?", eventIDs).Order("id").Find(&locked).Error; err != nil {
		return err
	}
	var occIDs []uint
	if err := tx.Model(&models.EventOccurrence{}).Where("event_id IN ?", eventIDs).Pluck("id", &occIDs).Error; err != nil {
		return err
	}
	if len(occIDs) > 0 {
		// Hard-deleting a registration needs its exact-date grant ended first,
		// with its snapshots: end any live grant as event_deleted, no notices
		// (the dates are about to disappear). Already-ended grants keep their
		// own reason.
		if err := endLiveAccessOnDates(tx, occIDs, policy.OccurrenceEventDeleted, time.Now().UTC()); err != nil {
			return err
		}
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
	if err := tx.Where("id IN ?", eventIDs).Delete(&models.Event{}).Error; err != nil {
		return err
	}
	// The dates' bookings went with them; withdraw requests left waiting.
	return closeEmptyRoomRequests(tx)
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
	if _, err := models.ParseCalendarDate(body.Date); err != nil {
		fail(c, errSeriesDateInvalid)
		return
	}
	var occ models.EventOccurrence
	if err := a.DB.Where("event_id = ? AND original_date = ?", ev.ID, body.Date).First(&occ).Error; err != nil {
		jsonError(c, http.StatusNotFound, "that date is not on this event")
		return
	}
	err := a.contactMutation(contactChange{EventID: ev.ID}, func(tx *gorm.DB) error {
		actor, _, _, err := lockManagedEventIn(tx, apiUser(c), ev.ID)
		if err != nil {
			return err
		}
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
		err = notifyOccurrences(tx, actor.ID, []uint{occ.ID}, notify.OccurrenceCancelled)
		if err != nil {
			return err
		}
		// A cancelled date frees its room (9.2).
		return releaseRoomBookings(tx, []uint{occ.ID})
	})
	if err != nil {
		answerEventWrite(c, err, "could not cancel")
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
	if _, err := models.ParseCalendarDate(date); err != nil {
		fail(c, errSeriesDateInvalid)
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
		fail(c, errVisibilityNotAllowed)
		return
	}
	newOverride, overrideSet, capErr := parseCapacity(body.CapacityOverride)
	if capErr != nil {
		fail(c, capErr)
		return
	}
	newRemoteOverride, remoteOverrideSet, capErr := parseRemoteCapacity(body.RemoteCapacityOverride, true)
	if capErr != nil {
		fail(c, capErr)
		return
	}
	// Validate everything that doesn't need the row before the lock.
	if body.DurationMinutes != nil && !events.ValidDuration(*body.DurationMinutes) {
		fail(c, errInvalidDuration)
		return
	}
	if err := validateEventText(strings.TrimSpace(strOrEmpty(body.Title)), "", strings.TrimSpace(strOrEmpty(body.Location))); err != nil {
		fail(c, err)
		return
	}
	me := apiUser(c)
	var inst occurrenceJSON
	var impact visibilityImpactJSON
	err := a.contactMutation(contactChange{EventID: ev.ID}, func(tx *gorm.DB) error {
		actor, err := lockActorIn(tx, me, actorRead)
		if err != nil {
			return err
		}
		me = actor
		var current models.Event
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&current, ev.ID).Error; err != nil {
			return err
		}
		ev = &current
		f, err := eventFactsIn(tx, me, ev)
		if err != nil {
			return err
		}
		if !f.canManage(me) {
			return errOccurrenceNotFound
		}
		// A series edit may have changed the zone while the body arrived.
		var wallStart *time.Time
		if body.StartsLocal != nil {
			t, _, err := events.ParseWall(strings.TrimSpace(*body.StartsLocal), ev.TZID)
			if err != nil {
				return specAnswer(err)
			}
			u := t.UTC()
			wallStart = &u
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
		dur := effectiveOccurrenceDuration(ev, &o)
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
			err := a.changeDateVisibility(tx, me, c.ClientIP(), ev, &o, f.group, visibility, body.Reason, false)
			if err != nil {
				return err
			}
		}
		// event.changed fans out when the date's time or place actually
		// changed: one item per going or waitlisted person, with the
		// combined variant when both changed.
		timeChanged := !o.StartsAt.Equal(oldStart) || !o.EndsAt.Equal(oldEnd)
		placeChanged := newPlace != oldPlace
		if timeChanged {
			// The date's room moves with it, or is lost to a conflict (9.2).
			err := settleDateRooms(tx, ev, []uint{o.ID}, nil, &me.ID)
			if err != nil {
				return err
			}
		}
		if timeChanged || placeChanged {
			err := notifyEventChanged(tx, ev, &o, changeVariant(timeChanged, placeChanged), &me.ID)
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
			!intPtrEqual(events.EffectiveCapacity(ev.RemoteCapacity, oldRemoteOverride),
				events.EffectiveCapacity(ev.RemoteCapacity, newRemoteOverride))
		if capMoved || remoteCapMoved {
			err := a.promoteLocked(tx, ev, &o, a.now().UTC(), &me.ID)
			if err != nil {
				return err
			}
		}
		groupLoc := ""
		if f.group != nil {
			groupLoc = f.group.Location // group-ok: managers only, re-checked under the lock
		}
		inst = occurrenceToJSON(ev, &o, groupLoc)
		inst.Room = a.occurrenceRooms(a.DB, []uint{o.ID})[o.ID]
		applyVideoReveal(&inst, ev, false, true) // managers see the link
		inst.Visibility = policy.EffectiveVisibility(ev.Visibility, o.VisibilityOverride)
		inst.LeaderOpened = ev.LeaderOpened
		inst.ChurchWide = ev.GroupID == nil
		used, err := seatsUsedForMode(tx, o.ID, models.AttendanceInPerson, 0)
		if err != nil {
			return err
		}
		inst.SeatsUsed = used
		inst.SpotsLeft = spotsLeftFor(inst.Capacity, used)
		remoteUsed, err := seatsUsedForMode(tx, o.ID, models.AttendanceRemote, 0)
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
		if err := tx.Where("user_id = ? AND occurrence_id = ? AND status <> ?",
			me.ID, o.ID, models.RegCancelled).Limit(1).Find(&mineReg).Error; err != nil {
			return err
		}
		if mineReg.ID != 0 {
			if err := inst.setMine(tx, &mineReg); err != nil {
				return err
			}
		}
		return nil
	})
	switch {
	case errors.Is(err, errNeedDecision):
		c.JSON(http.StatusConflict, gin.H{"needsDecision": true, "impact": impact})
		return
	case errors.Is(err, errDateVisibility):
		fail(c, errVisibilityNotAllowed)
		return
	case errors.Is(err, errOccurrenceNotFound):
		jsonError(c, http.StatusNotFound, "that date is not on this event")
		return
	case errors.Is(err, gorm.ErrRecordNotFound):
		jsonError(c, http.StatusNotFound, "event not found")
		return
	case isAPIError(err):
		fail(c, err)
		return
	case err != nil:
		jsonError(c, http.StatusInternalServerError, "could not update this date")
		return
	}
	c.JSON(http.StatusOK, occurrenceResponseJSON{Occurrence: inst})
}

// restoreSeriesDates records dates a series edit just brought back and tells
// the people signed up for them. It runs in the series edit's transaction,
// after the rows are scheduled again, so the audit row, the notices and the
// change commit together. Nothing here touches registrations.
func restoreSeriesDates(tx *gorm.DB, actor uint, ip string, ev *models.Event, occIDs []uint) error {
	if len(occIDs) == 0 {
		return nil
	}
	var dates []string
	if err := tx.Model(&models.EventOccurrence{}).Where("id IN ?", occIDs).Order("original_date").
		Pluck("to_char(original_date, 'YYYY-MM-DD')", &dates).Error; err != nil {
		return err
	}
	if err := audit.Write(tx, audit.Entry{Actor: &actor, Action: audit.EventDatesRestored, TargetType: "event", TargetID: ev.ID,
		Meta: map[string]any{"dates": dates, "occurrenceIds": occIDs}, IP: ip}); err != nil {
		return err
	}
	return notifyOccurrences(tx, actor, occIDs, notify.OccurrenceRestored)
}

// occurrenceJSON is one date of an event. startsAt/endsAt are UTC;
// startsLocal/endsLocal are the wall clock in tzid (C2). date is the series
// date and stays put when a per-date override moves the clock.
type occurrenceJSON struct {
	hideSeries            bool             `json:"-"`
	ID                    uint             `json:"id"`
	EventID               uint             `json:"eventId"`
	Date                  string           `json:"date"`
	DateURL               string           `json:"dateUrl,omitempty"`
	VisibilityOverride    *string          `json:"visibilityOverride,omitempty"`
	SeriesVisibility      string           `json:"seriesVisibility,omitempty" series:"omit"`
	GroupVisibility       string           `json:"groupVisibility,omitempty"`
	StartsAt              time.Time        `json:"startsAt"`
	EndsAt                time.Time        `json:"endsAt"`
	StartsLocal           string           `json:"startsLocal"`
	EndsLocal             string           `json:"endsLocal"`
	TZID                  string           `json:"tzid"`
	DurationMinutes       int              `json:"durationMinutes"`
	Location              string           `json:"location"`
	Title                 string           `json:"title"`
	Description           string           `json:"description" series:"blank"`
	RecurrenceRule        string           `json:"recurrenceRule" series:"omit"`
	RRuleUntil            string           `json:"rruleUntil,omitempty" series:"omit"`
	SeriesStartsLocal     string           `json:"seriesStartsLocal" series:"omit"`
	SeriesTitle           string           `json:"seriesTitle" series:"omit"`
	SeriesLocation        string           `json:"seriesLocation" series:"omit"`
	SeriesDurationMinutes int              `json:"seriesDurationMinutes" series:"omit"`
	Overridden            bool             `json:"overridden"`
	Registrations         map[string]int64 `json:"registrations"`
	MyRegistration        *string          `json:"myRegistration"`
	Visibility            string           `json:"visibility"`
	LeaderOpened          bool             `json:"leaderOpened"`
	ChurchWide            bool             `json:"churchWide"`
	GroupID               *uint            `json:"groupId,omitempty"`
	GroupName             string           `json:"groupName,omitempty"`
	HostedBy              string           `json:"hostedBy,omitempty"`
	// Room is the date's confirmed room, shown as its place (9.2, M2.1b).
	Room *occurrenceRoomJSON `json:"room,omitempty"`
	// AttendeesVisible is false when the caller gets a count only. An empty
	// allowed list is omitted; the bool is what says names were allowed.
	AttendeesVisible         bool                          `json:"attendeesVisible"`
	Attendees                []attendeeJSON                `json:"attendees,omitempty"`
	MyHouseholdRegistrations []myHouseholdRegistrationJSON `json:"myHouseholdRegistrations"`
	RemovedRegistrations     []attendeeJSON                `json:"removedRegistrations,omitempty"`
	// Capacity, waitlist, and guests (M1.5). Capacity is the effective
	// cap for this date (per-date override wins); null means unlimited.
	// SpotsLeft is null when unlimited and never below 0. SeatsUsed
	// counts seats (1 + guest_count per going party of a listed
	// account); the Registrations map counts rows.
	Capacity        *int `json:"capacity"`
	SeatsUsed       int  `json:"seatsUsed"`
	SpotsLeft       *int `json:"spotsLeft"`
	WaitlistEnabled bool `json:"waitlistEnabled"`
	GuestsAllowed   int  `json:"guestsAllowed"`
	// ReminderEmailMinutes is the series' reminder time (M3.6), for its
	// Edit series form; 0 is off. ReminderPush is its 2-hour push
	// reminder switch (M3.13d3).
	ReminderEmailMinutes int  `json:"reminderEmailMinutes"`
	ReminderPush         bool `json:"reminderPush"`
	// Remote attendance (M1.8 slice 2): a second seat pool. The remote
	// fields mirror the in-person ones; remoteCapacity is the effective
	// cap for this date (per-date override wins), null is unlimited.
	RemoteAvailable     bool    `json:"remoteAvailable"`
	ChildcareAvailable  bool    `json:"childcareAvailable"`
	ChildcareFull       bool    `json:"childcareFull"`
	ChildcareNotes      string  `json:"childcareNotes" series:"blank"`
	ChildcareCapacity   *int    `json:"childcareCapacity" series:"blank"`
	ChildcareAges       string  `json:"childcareAges" series:"blank"`
	RemoteCapacity      *int    `json:"remoteCapacity"`
	RemoteSeatsUsed     int     `json:"remoteSeatsUsed"`
	ChildcareUsed       *int    `json:"childcareUsed,omitempty"`
	ChildcareSpotsLeft  *int    `json:"childcareSpotsLeft,omitempty"`
	MyChildcareCount    *int    `json:"myChildcareCount,omitempty"`
	MyChildcareAgesNote *string `json:"myChildcareAgesNote,omitempty"`
	RemoteSpotsLeft     *int    `json:"remoteSpotsLeft"`
	// MyGuestCount and MyWaitlistPosition describe the signed-in
	// viewer's own registration; both are absent without one. Position
	// is set only when the viewer is waitlisted.
	MyGuestCount       *int `json:"myGuestCount,omitempty"`
	MyWaitlistPosition *int `json:"myWaitlistPosition,omitempty"`
	// MyAttendanceMode is the viewer's own pool (in_person or remote);
	// absent without a registration.
	MyAttendanceMode *string `json:"myAttendanceMode,omitempty"`
	// Video link (5.8, M1.8). VideoURL is present only when this viewer
	// may see it for this date (policy.CanSeeVideoLink); VideoHidden says
	// a link exists but is being withheld, so the UI can say "Register
	// to get the link".
	VideoProvider string `json:"videoProvider"`
	VideoURL      string `json:"videoUrl,omitempty"`
	VideoReveal   string `json:"videoReveal"`
	VideoHidden   bool   `json:"videoHidden,omitempty"`
}

// hideSeriesDetails withdraws the series from a viewer who may see only this
// date (an opened date of a hidden series): every field tagged series:"omit"
// or series:"blank" is cleared, and the "omit" ones are left out of the JSON
// altogether. dateURL is where that viewer reaches the date. The tags on
// occurrenceJSON are the one list of what belongs to the series.
func (o *occurrenceJSON) hideSeriesDetails(dateURL string) {
	o.hideSeries = true
	o.DateURL = dateURL
	v := reflect.ValueOf(o).Elem()
	for i := 0; i < v.NumField(); i++ {
		if v.Type().Field(i).Tag.Get("series") != "" {
			v.Field(i).SetZero()
		}
	}
}

// MarshalJSON preserves the established full-series wire contract for
// authorized readers, but does not serialize even empty series keys on an
// opened date of a hidden series.
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
	t := reflect.TypeOf(o)
	for i := 0; i < t.NumField(); i++ {
		if t.Field(i).Tag.Get("series") == "omit" {
			delete(fields, strings.Split(t.Field(i).Tag.Get("json"), ",")[0])
		}
	}
	return json.Marshal(fields)
}

type eventJSON struct {
	ID              uint       `json:"id"`
	GroupID         *uint      `json:"groupId,omitempty"`
	GroupName       string     `json:"groupName,omitempty"`
	GroupVisibility string     `json:"groupVisibility,omitempty"`
	HostedBy        string     `json:"hostedBy,omitempty"`
	Title           string     `json:"title"`
	Description     string     `json:"description"`
	StartsLocal     string     `json:"startsLocal"`
	DurationMinutes int        `json:"durationMinutes"`
	TZID            string     `json:"tzid"`
	StartsAt        time.Time  `json:"startsAt"`
	EndsAt          time.Time  `json:"endsAt"`
	Location        string     `json:"location"`
	RecurrenceRule  string     `json:"recurrenceRule"`
	RRuleUntil      string     `json:"rruleUntil,omitempty"`
	Visibility      string     `json:"visibility"`
	LeaderOpened    bool       `json:"leaderOpened"`
	ChurchWide      bool       `json:"churchWide"`
	Hosts           []hostJSON `json:"hosts,omitempty"`
	Capacity        *int       `json:"capacity"`
	WaitlistEnabled bool       `json:"waitlistEnabled"`
	GuestsAllowed   int        `json:"guestsAllowed"`
	// ReminderEmailMinutes: when each date's email reminder goes (M3.6,
	// DEC-133); 0 is off. ReminderPush: each date's 2-hour push reminder
	// (M3.13d3).
	ReminderEmailMinutes int  `json:"reminderEmailMinutes"`
	ReminderPush         bool `json:"reminderPush"`
	// Remote attendance (M1.8 slice 2). RemoteCapacity null means
	// unlimited remote seats.
	RemoteAvailable    bool   `json:"remoteAvailable"`
	ChildcareAvailable bool   `json:"childcareAvailable"`
	ChildcareNotes     string `json:"childcareNotes"`
	ChildcareCapacity  *int   `json:"childcareCapacity"`
	ChildcareAges      string `json:"childcareAges"`
	RemoteCapacity     *int   `json:"remoteCapacity"`
	// Video link (5.8, M1.8). At series level the link shows under
	// "always" and to the event's managers; a person going to one date
	// gets it on that date's occurrence row instead.
	VideoProvider string `json:"videoProvider"`
	VideoURL      string `json:"videoUrl,omitempty"`
	VideoReveal   string `json:"videoReveal"`
	VideoHidden   bool   `json:"videoHidden,omitempty"`
}

const maxRecurrenceLen = 300
