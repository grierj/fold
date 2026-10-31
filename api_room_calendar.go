package main

import (
	"net/http"
	"strings"
	"time"

	"fold/internal/events"
	"fold/internal/policy"
	"fold/models"

	"github.com/gin-gonic/gin"
)

// The by-room view (M2.2a, 8.1): per active room, its confirmed bookings
// over one day or one week of the church's calendar. A booking whose date
// the viewer can't see is a "Reserved" block: its times and nothing else,
// so the room's use never says whose it is (section 6).

// roomSlotJSON is one booking in the by-room view. A reserved slot carries
// only startsAt, endsAt and reserved (TestRoomCalendarReservedKeys).
type roomSlotJSON struct {
	StartsAt time.Time `json:"startsAt"`
	EndsAt   time.Time `json:"endsAt"`
	Reserved bool      `json:"reserved,omitempty"`

	EventID    uint   `json:"eventId,omitempty"`
	Date       string `json:"date,omitempty"`
	DateURL    string `json:"dateUrl,omitempty"`
	Title      string `json:"title,omitempty"`
	GroupID    *uint  `json:"groupId,omitempty"`
	GroupName  string `json:"groupName,omitempty"`
	HostedBy   string `json:"hostedBy,omitempty"`
	ChurchWide bool   `json:"churchWide,omitempty"`
	// Waiting: a booking waiting for an admin's approval, which holds
	// nothing yet. Admins only (DEC-109).
	Waiting bool `json:"waiting,omitempty"`
}

type roomCalendarRoomJSON struct {
	ID         uint           `json:"id"`
	Name       string         `json:"name"`
	Building   string         `json:"building"`
	Capacity   *int           `json:"capacity"`
	Restricted bool           `json:"restricted"`
	Bookings   []roomSlotJSON `json:"bookings"`
}

type roomCalendarJSON struct {
	Timezone  string                 `json:"timezone"`
	From      string                 `json:"from"`
	To        string                 `json:"to"`
	Rooms     []roomCalendarRoomJSON `json:"rooms"`
	Truncated bool                   `json:"truncated"`
}

// How many bookings one by-room response carries; past it, truncated.
const maxRoomCalendarSlots = 2000

// roomCalendarWindow is the church-calendar days the view covers: one day,
// or the Sunday-to-Saturday week holding date (web/src/lib/calendar.ts).
func roomCalendarWindow(view, date string, zone *time.Location, now time.Time) (first, last time.Time, ok bool) {
	day := now.In(zone)
	if date != "" {
		d, err := time.Parse("2006-01-02", date)
		if err != nil {
			return time.Time{}, time.Time{}, false
		}
		day = d
	}
	first = time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, zone)
	switch view {
	case "", "day":
		return first, first, true
	case "week":
		first = first.AddDate(0, 0, -int(first.Weekday()))
		return first, first.AddDate(0, 0, 6), true
	}
	return time.Time{}, time.Time{}, false
}

func (a *App) apiRoomCalendar(c *gin.Context) {
	me := apiUser(c)
	p := asPerson(me)
	// Teens get the same 404 as rooms being off: nothing to find here.
	if !policy.CanSeeRoomCalendar(p) {
		jsonError(c, http.StatusNotFound, "not found")
		return
	}
	tzName := a.churchTimezone()
	zone, err := events.LoadZone(tzName)
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not load the room calendar")
		return
	}
	now := a.calendarTime()
	first, last, ok := roomCalendarWindow(strings.TrimSpace(c.Query("view")), strings.TrimSpace(c.Query("date")), zone, now)
	if !ok {
		jsonError(c, http.StatusBadRequest, "view must be day or week, and date YYYY-MM-DD")
		return
	}
	windowStart, windowEnd := first.UTC(), last.AddDate(0, 0, 1).UTC()

	var rooms []models.Room
	if err := a.DB.Where("active").Order("lower(building), lower(name), id").Find(&rooms).Error; err != nil {
		jsonError(c, http.StatusInternalServerError, "could not load the room calendar")
		return
	}
	out := roomCalendarJSON{Timezone: tzName, From: first.Format("2006-01-02"), To: last.Format("2006-01-02"), Rooms: []roomCalendarRoomJSON{}}
	index := map[uint]int{}
	roomIDs := []uint{}
	for _, r := range rooms {
		if !policy.CanListRoom(p, r.Restricted) {
			continue
		}
		index[r.ID] = len(out.Rooms)
		roomIDs = append(roomIDs, r.ID)
		out.Rooms = append(out.Rooms, roomCalendarRoomJSON{ID: r.ID, Name: r.Name, Building: r.Building,
			Capacity: r.Capacity, Restricted: r.Restricted, Bookings: []roomSlotJSON{}})
	}
	if len(roomIDs) == 0 {
		c.JSON(http.StatusOK, out)
		return
	}

	// Confirmed bookings hold the room; an admin also sees the requested
	// ones waiting on a pending approval request (DEC-109). A requested
	// booking with no pending request lost its room and holds nothing.
	type bookingRow struct {
		models.RoomBooking
		EventID      uint
		OriginalDate string
		Waiting      bool
	}
	var rows []bookingRow
	q := a.DB.Table("room_bookings b").
		Select(`b.*, o.event_id, o.original_date, (b.status = ?) AS waiting`, models.BookingRequested).
		Joins("JOIN event_occurrences o ON o.id = b.occurrence_id").
		Where("b.room_id IN ? AND b.starts_at < ? AND b.ends_at > ? AND o.status = ?",
			roomIDs, windowEnd, windowStart, models.OccurrenceScheduled)
	if policy.CanSeeWaitingBookings(p) {
		q = q.Where(`(b.status = ? OR (b.status = ? AND EXISTS (SELECT 1 FROM room_requests rr
			WHERE rr.id = b.request_id AND rr.status = ?)))`,
			models.BookingConfirmed, models.BookingRequested, models.RoomRequestPending)
	} else {
		q = q.Where("b.status = ?", models.BookingConfirmed)
	}
	if err := q.Order("b.starts_at, b.id").Limit(maxRoomCalendarSlots + 1).Scan(&rows).Error; err != nil {
		jsonError(c, http.StatusInternalServerError, "could not load the room calendar")
		return
	}
	if len(rows) > maxRoomCalendarSlots {
		rows, out.Truncated = rows[:maxRoomCalendarSlots], true
	}
	if len(rows) == 0 {
		c.JSON(http.StatusOK, out)
		return
	}

	// Each date goes through the ordinary date filter, then the viewer's
	// exact-date invitations; never the series alone (AGENTS.md).
	eventIDs, occIDs := []uint{}, []uint{}
	seen := map[uint]bool{}
	for _, r := range rows {
		occIDs = append(occIDs, r.OccurrenceID)
		if !seen[r.EventID] {
			seen[r.EventID] = true
			eventIDs = append(eventIDs, r.EventID)
		}
	}
	vs, err := a.dateCandidates(me, a.DB.Where("id IN ?", eventIDs))
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not load the room calendar")
		return
	}
	scoped, err := a.scopedOccurrenceReads(me, a.DB.Where("id IN ?", occIDs), now)
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not load the room calendar")
		return
	}
	var occs []models.EventOccurrence
	if err := a.DB.Where("id IN ?", occIDs).Find(&occs).Error; err != nil {
		jsonError(c, http.StatusInternalServerError, "could not load the room calendar")
		return
	}
	occByID := make(map[uint]*models.EventOccurrence, len(occs))
	for i := range occs {
		occByID[occs[i].ID] = &occs[i]
	}

	siteName := ""
	for _, r := range rows {
		slot := roomSlotJSON{StartsAt: r.StartsAt, EndsAt: r.EndsAt, Reserved: true}
		occ := occByID[r.OccurrenceID]
		ev := vs.ByID[r.EventID]
		switch {
		case occ == nil:
		case ev != nil && vs.canSeeDate(me, ev, occ):
			full := occurrenceToJSON(ev, occ, "")
			slot = roomSlotJSON{StartsAt: r.StartsAt, EndsAt: r.EndsAt, EventID: ev.ID, Date: occ.OriginalDate,
				Title: full.Title, ChurchWide: ev.GroupID == nil}
			if !vs.canSeeSeries(me, ev) {
				slot.DateURL = occurrenceDatePath(ev.ID, occ.OriginalDate)
			}
			grp, rel := groupAndRel(ev, vs.Groups, vs.Rels)
			if shown := a.groupShownTo(me, grp, rel); shown != nil {
				gid := shown.ID
				slot.GroupID = &gid
				slot.GroupName = shown.Name
			} else if ev.GroupID != nil {
				// An opened event of a group this viewer can't see names
				// nobody: the church hosts it (DEC-17).
				if siteName == "" {
					siteName = a.siteName()
				}
				slot.HostedBy = siteName
			}
		case scoped[occ.ID] != nil:
			// An exact-date invitation shows that date and its title, never
			// the series or its group.
			read := scoped[occ.ID]
			full := occurrenceToJSON(&read.Event, occ, "")
			slot = roomSlotJSON{StartsAt: r.StartsAt, EndsAt: r.EndsAt, EventID: read.Event.ID, Date: occ.OriginalDate,
				DateURL: occurrenceDatePath(read.Event.ID, occ.OriginalDate), Title: full.Title,
				ChurchWide: read.Event.GroupID == nil}
		}
		// Only an admin gets waiting rows. One for a date even an admin
		// can't open (a deleted group's) holds nothing and isn't shown.
		if r.Waiting && slot.Reserved {
			continue
		}
		slot.Waiting = r.Waiting
		room := &out.Rooms[index[r.RoomID]]
		room.Bookings = append(room.Bookings, slot)
	}
	c.JSON(http.StatusOK, out)
}
