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
)

// The calendar (M1.6, DEC-67) lists every occurrence the viewer may see
// over a date range. No existing endpoint does that: /events is the home
// list of open events only, and /groups/:id/events is one group. The
// response is a slim row per occurrence — never attendees, counts, seats
// or declined data — so DEC-64 can't leak in bulk.
type calendarOccurrenceJSON struct {
	EventID            uint      `json:"eventId"`
	Date               string    `json:"date"`
	DateURL            string    `json:"dateUrl,omitempty"`
	StartsAt           time.Time `json:"startsAt"`
	EndsAt             time.Time `json:"endsAt"`
	Title              string    `json:"title"`
	Location           string    `json:"location"`
	GroupID            *uint     `json:"groupId,omitempty"`
	GroupName          string    `json:"groupName,omitempty"`
	ChurchWide         bool      `json:"churchWide"`
	RemoteAvailable    bool      `json:"remoteAvailable"`
	ChildcareAvailable bool      `json:"childcareAvailable"`
	HostedBy           string    `json:"hostedBy,omitempty"`
	// MyStatus is only the viewer's own answer. Household answers never
	// populate it. Declined is serialized so Everything can label Can't go.
	MyStatus *string `json:"myStatus"`
	// OwnScope drives RSVP-aware styling without granting any access.
	OwnScope bool `json:"ownScope"`
	// GroupDeleted marks a deleted group's event, sent only to an admin
	// who asked for deleted events (DEC-68).
	GroupDeleted bool `json:"groupDeleted,omitempty"`
}

const maxCalendarOccurrences = 500

func (a *App) calendarTime() time.Time { return a.now().UTC() }

func (a *App) apiCalendar(c *gin.Context) {
	me := apiUser(c)
	from, to, ok := parseDateWindow(c)
	if !ok {
		return
	}
	// The from/to dates are the viewer's calendar days (DEC-57): the
	// window on starts_at runs from their local midnight to the next
	// midnight after `to`, so an event near midnight in its own zone
	// lands on the viewer's day even at a week or month edge.
	zone := churchZoneOrUTC(a.DB)
	if tzName := strings.TrimSpace(c.Query("tz")); tzName != "" {
		var err error
		if zone, err = events.LoadZone(tzName); err != nil {
			jsonError(c, http.StatusBadRequest, "tz must be an IANA name, for example America/Denver")
			return
		}
	}
	fd, _ := models.ParseCalendarDate(from)
	td, _ := models.ParseCalendarDate(to)
	windowStart := time.Date(fd.Year(), fd.Month(), fd.Day(), 0, 0, 0, 0, zone)
	windowEnd := time.Date(td.Year(), td.Month(), td.Day()+1, 0, 0, 0, 0, zone)

	// A group filter names one group the viewer may see, as on
	// /groups/:id/events; anything else is a 404.
	var onlyGroup *uint
	if raw := strings.TrimSpace(c.Query("group")); raw != "" {
		id, _ := strconv.Atoi(raw)
		grp, _, ok := a.loadGroupFor(c, uint(id))
		if !ok {
			return
		}
		onlyGroup = &grp.ID
	}

	q := a.DB.Where("id IN (SELECT event_id FROM event_occurrences WHERE starts_at >= ? AND starts_at < ?)",
		windowStart.UTC(), windowEnd.UTC())
	if onlyGroup != nil {
		q = q.Where("group_id = ?", *onlyGroup)
	}
	// Filter candidates before the date cap; ordinary visibility policy still
	// applies below, including to private church-wide events.
	if c.Query("church") == "1" {
		q = q.Where("group_id IS NULL")
	}
	// remote=1 keeps only events that offer remote attendance (M1.8
	// slice 2). Filtering on the event row here means a hidden event is
	// dropped by eventCandidates exactly as without the filter.
	if c.Query("remote") == "1" {
		q = q.Where("remote_available = ?", true)
	}
	if c.Query("childcare") == "1" {
		q = q.Where("childcare_available = ?", true)
	}
	// deleted=1 lists deleted groups' events too, for admins only (DEC-68).
	candidates := a.eventCandidates
	if c.Query("deleted") == "1" {
		candidates = a.deletedEventCandidates
	}
	vs, err := candidates(me, q)
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not load the calendar")
		return
	}
	out := []calendarOccurrenceJSON{}
	byID, hosted, visibleIDs := vs.ByID, vs.Hosted, vs.IDs
	now := a.calendarTime()
	scoped, err := a.scopedOccurrenceReads(me, a.DB.Where("starts_at >= ? AND starts_at < ? AND event_id IN (?)",
		windowStart.UTC(), windowEnd.UTC(), q.Model(&models.Event{}).Select("id")), now)
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not load the calendar")
		return
	}
	var occs []models.EventOccurrence
	// Chunked so a huge event list stays under the bind-parameter limit; the
	// merged rows are sorted below. A failed read is a 500, not an empty month.
	if err := eachIDChunk(visibleIDs, func(chunk []uint) error {
		var part []models.EventOccurrence
		if err := a.DB.Where("event_id IN ? AND status = ? AND starts_at >= ? AND starts_at < ?",
			chunk, models.OccurrenceScheduled, windowStart.UTC(), windowEnd.UTC()).Order("starts_at").Find(&part).Error; err != nil {
			return err
		}
		occs = append(occs, part...)
		return nil
	}); err != nil {
		jsonError(c, http.StatusInternalServerError, "could not load the calendar")
		return
	}
	for _, read := range scoped {
		if byID[read.Event.ID] == nil {
			occs = append(occs, read.Occurrence)
		}
	}
	sort.Slice(occs, func(i, j int) bool {
		if occs[i].StartsAt.Equal(occs[j].StartsAt) {
			return occs[i].ID < occs[j].ID
		}
		return occs[i].StartsAt.Before(occs[j].StartsAt)
	})

	occIDs := visibleOccurrenceIDs(vs, me, occs, scoped)
	scopeFacts, err := a.loadCalendarScopeBatch(me, occIDs)
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not load the calendar")
		return
	}
	justMine := me != nil && c.Query("mine") == "1"
	scope := policy.CalendarScopeAll
	if justMine {
		scope = policy.CalendarScopeMine
	}

	truncated := false
	siteName := &siteNameOnce{a: a}
	for i := range occs {
		occ := &occs[i]
		date, ok := a.projectDate(me, vs, occ, scoped[occ.ID])
		if !ok {
			continue
		}
		status := scopeFacts.ownStatus[occ.ID]
		decision := calendarScopeDecision(scope, me != nil, date.Event, occ, date.Rel, hosted[date.Event.ID], scopeFacts.household[occ.ID], scoped[occ.ID] != nil, status, now)
		if !decision.Include {
			continue
		}
		if c.Query("remote") == "1" && !date.RemoteOffered() {
			continue
		}
		if len(out) >= maxCalendarOccurrences {
			truncated = true
			break
		}
		out = append(out, date.calendarRow(siteName, status, decision.OwnScope))
	}
	c.JSON(http.StatusOK, calendarResponseJSON{Occurrences: out, Truncated: truncated})
}
