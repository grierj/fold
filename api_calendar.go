package main

import (
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"fold/internal/events"
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
	// MyStatus is the viewer's own answer for this date: going, maybe or
	// waitlisted. Anything else (including "can't go") reads as null.
	MyStatus *string `json:"myStatus"`
	// GroupDeleted marks a deleted group's event, sent only to an admin
	// who asked for deleted events (DEC-68).
	GroupDeleted bool `json:"groupDeleted,omitempty"`
}

// How many occurrences one calendar response carries. The answer says so
// when there were more (truncated), so nothing is silently dropped.
const maxCalendarOccurrences = 500

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
	tzName := strings.TrimSpace(c.Query("tz"))
	if tzName == "" {
		tzName = a.churchTimezone()
	}
	zone, err := events.LoadZone(tzName)
	if err != nil {
		jsonError(c, http.StatusBadRequest, "tz must be an IANA name, for example America/Denver")
		return
	}
	fd, _ := time.Parse("2006-01-02", from)
	td, _ := time.Parse("2006-01-02", to)
	windowStart := time.Date(fd.Year(), fd.Month(), fd.Day(), 0, 0, 0, 0, zone)
	windowEnd := time.Date(td.Year(), td.Month(), td.Day()+1, 0, 0, 0, 0, zone)

	// A group filter names one group the viewer may see, as on
	// /groups/:id/events; anything else is a 404.
	var onlyGroup *uint
	if raw := strings.TrimSpace(c.Query("group")); raw != "" {
		id, _ := strconv.Atoi(raw)
		var grp models.Group
		if err := a.DB.First(&grp, id).Error; err != nil || !a.canSeeGroup(me, &grp) {
			jsonError(c, http.StatusNotFound, "group not found")
			return
		}
		onlyGroup = &grp.ID
	}

	// Only events with a date in the window can add anything (#75).
	q := a.DB.Where("id IN (SELECT event_id FROM event_occurrences WHERE starts_at >= ? AND starts_at < ?)",
		windowStart.UTC(), windowEnd.UTC())
	if onlyGroup != nil {
		q = q.Where("group_id = ?", *onlyGroup)
	}
	// remote=1 keeps only events that offer remote attendance (M1.8
	// slice 2). Filtering on the event row here means a hidden event is
	// dropped by visibleEvents exactly as without the filter.
	if c.Query("remote") == "1" {
		q = q.Where("remote_available = ?", true)
	}
	if c.Query("childcare") == "1" {
		q = q.Where("childcare_available = ?", true)
	}
	// deleted=1 lists deleted groups' events too, for admins only (DEC-68).
	vs, err := a.eventCandidates(me, q, c.Query("deleted") == "1", true)
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not load the calendar")
		return
	}
	out := []calendarOccurrenceJSON{}
	byID, groups, rels, hosted, visibleIDs := vs.ByID, vs.Groups, vs.Rels, vs.Hosted, vs.IDs
	now := time.Now().UTC()
	scoped, err := a.scopedOccurrenceReads(me, a.DB.Where("starts_at >= ? AND starts_at < ? AND event_id IN (?)",
		windowStart.UTC(), windowEnd.UTC(), q.Model(&models.Event{}).Select("id")), now)
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not load the calendar")
		return
	}
	var occs []models.EventOccurrence
	if len(visibleIDs) > 0 {
		a.DB.Where("event_id IN ? AND status = ? AND starts_at >= ? AND starts_at < ?",
			visibleIDs, models.OccurrenceScheduled, windowStart.UTC(), windowEnd.UTC()).Order("starts_at").Find(&occs)
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

	// The viewer's own answers, for myStatus and the per-occurrence
	// "Just mine" rule.
	mineStatus := map[uint]string{}
	householdDates := map[uint]bool{}
	if me != nil && len(occs) > 0 {
		occIDs := make([]uint, len(occs))
		for i := range occs {
			occIDs[i] = occs[i].ID
		}
		var rows []models.Registration
		a.DB.Where("user_id = ? AND occurrence_id IN ? AND status IN ?", me.ID, occIDs,
			[]string{models.RegGoing, models.RegMaybe, models.RegWaitlisted}).Find(&rows)
		for _, r := range rows {
			mineStatus[r.OccurrenceID] = r.Status
		}
		var householdRows []models.Registration
		a.DB.Joins(listedRegistrations()).Where("registrations.registered_by = ? AND registrations.occurrence_id IN ? AND registrations.status IN ?", me.ID, occIDs, []string{models.RegGoing, models.RegMaybe, models.RegWaitlisted}).Find(&householdRows)
		for _, r := range householdRows {
			householdDates[r.OccurrenceID] = true
		}
	}

	// "Just mine" (signed in only; ignored otherwise): my groups' events,
	// events I host, and the specific dates I answered going, maybe or
	// waitlisted — not the whole series.
	justMine := me != nil && c.Query("mine") == "1"

	truncated := false
	for i := range occs {
		occ := &occs[i]
		ev := byID[occ.EventID]
		if !vs.canSeeDate(me, ev, occ) {
			read := scoped[occ.ID]
			if read == nil {
				continue
			}
			date := presentOccurrenceDate(&read.Event, occ, "", false, false, false, read.Registration, read, now)
			if c.Query("remote") == "1" && !date.RemoteAvailable {
				continue
			}
			if len(out) >= maxCalendarOccurrences {
				truncated = true
				break
			}
			status := read.Registration.Status
			out = append(out, calendarOccurrenceJSON{EventID: date.EventID, Date: date.Date, DateURL: date.DateURL,
				StartsAt: date.StartsAt, EndsAt: date.EndsAt, Title: date.Title, Location: date.Location,
				ChurchWide:      read.Event.GroupID == nil,
				RemoteAvailable: date.RemoteAvailable, ChildcareAvailable: read.Event.ChildcareAvailable, MyStatus: &status})
			continue
		}
		grp, rel := groupAndRel(ev, groups, rels)
		status, answered := mineStatus[occ.ID]
		if justMine && !rel.Member && !hosted[ev.ID] && !answered && !householdDates[occ.ID] {
			continue
		}
		shown := a.groupShownTo(me, grp, rel)
		groupLoc := ""
		if shown != nil {
			groupLoc = shown.Location
		}
		full := occurrenceToJSON(ev, occ, groupLoc)
		remoteAvailable := full.RemoteAvailable && (full.RemoteCapacity == nil || *full.RemoteCapacity > 0)
		if c.Query("remote") == "1" && !remoteAvailable {
			continue
		}
		if len(out) >= maxCalendarOccurrences {
			truncated = true
			break
		}
		row := calendarOccurrenceJSON{
			EventID:            ev.ID,
			Date:               occ.OriginalDate,
			StartsAt:           full.StartsAt,
			EndsAt:             full.EndsAt,
			Title:              full.Title,
			Location:           full.Location,
			ChurchWide:         ev.GroupID == nil,
			RemoteAvailable:    remoteAvailable,
			ChildcareAvailable: ev.ChildcareAvailable,
		}
		if !vs.canSeeSeries(me, ev) {
			row.DateURL = occurrenceDatePath(ev.ID, occ.OriginalDate)
		}
		if shown != nil {
			gid := shown.ID
			row.GroupID = &gid
			row.GroupName = shown.Name
			row.GroupDeleted = shown.DeletedAt.Valid
		} else if ev.GroupID != nil {
			// An opened event of a group this viewer can't see names
			// nobody: it is hosted by the church (DEC-17).
			row.HostedBy = a.siteName()
		}
		if answered {
			s := status
			row.MyStatus = &s
		}
		out = append(out, row)
	}
	c.JSON(http.StatusOK, gin.H{"occurrences": out, "truncated": truncated})
}
