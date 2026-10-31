package main

import (
	"errors"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"fold/internal/audit"
	"fold/internal/events"
	"fold/internal/policy"
	"fold/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ---------------------------------------------------------------------------
// Series rooms (M2.1c, 9.2). "Book for the whole series" stores the room on
// the event (default_room_id) and books every scheduled future date. A date
// the room is busy on keeps a requested booking, holding nothing, and shows
// on the event page as needing a room (DEC-103). Dates the daily extender or
// a series edit adds later are booked in the series room the same way.
// ---------------------------------------------------------------------------

var (
	errRoomGone   = errors.New("room gone")
	errRestricted = errors.New("restricted")
)

// seriesRoomJSON is one bookable room as the series picker sees it.
type seriesRoomJSON struct {
	ID         uint   `json:"id"`
	Name       string `json:"name"`
	Building   string `json:"building"`
	Capacity   *int   `json:"capacity"`
	Features   string `json:"features"`
	Restricted bool   `json:"restricted"`
	// BusyDates: how many of the dates another confirmed booking holds the
	// room on. Who holds it is never said.
	BusyDates int `json:"busyDates"`
	// TooSmall: the event's seat cap is above the room's capacity. A
	// warning, never a block (9.2).
	TooSmall bool `json:"tooSmall"`
}

// needsRoomJSON is a date whose booking holds nothing and is the leader's
// to re-pick: it lost its room (status requested) when it moved or the room
// was busy, or an admin didn't approve it (status rejected, M2.1d). Dates
// waiting on an admin are not listed (DEC-105).
type needsRoomJSON struct {
	Date     string `json:"date"`
	RoomName string `json:"roomName"`
	Status   string `json:"status"`
}

// seriesBookingJSON says what booking a series did.
type seriesBookingJSON struct {
	DefaultRoom *occurrenceRoomJSON `json:"defaultRoom"`
	Booked      int                 `json:"booked"`
	// Lost: dates the room was busy on, kept as requested.
	Lost []string `json:"lost"`
	// Kept: dates that keep a room picked for them alone.
	Kept int `json:"kept"`
	// Pending: dates waiting on an admin's approval (M2.1d).
	Pending int `json:"pending"`
}

// timeWindow is one date's unpadded time.
type timeWindow struct{ start, end time.Time }

// busyDates counts, per room, how many of the dates another confirmed
// booking holds the room on, padded by the room's own setup and teardown
// (DEC-101). One read fetches the confirmed bookings near the dates, then a
// sweep: a room's bookings sorted by start, with the latest end so far, make
// each date one binary search. Bookings of eventID's own dates don't count.
func busyDates(db *gorm.DB, rooms []models.Room, windows []timeWindow, eventID uint) (map[uint]int, error) {
	out := map[uint]int{}
	if len(windows) == 0 || len(rooms) == 0 {
		return out, nil
	}
	first, last := windows[0].start, windows[0].end
	for _, w := range windows {
		if w.start.Before(first) {
			first = w.start
		}
		if w.end.After(last) {
			last = w.end
		}
	}
	// Both sides' padding is at most maxRoomPadding, so nothing further out
	// can overlap.
	slack := 2 * time.Duration(maxRoomPadding) * time.Minute
	var rows []struct {
		RoomID uint
		Lo, Hi time.Time
	}
	if err := db.Raw(`SELECT b.room_id, lower(b.padded_range) AS lo, upper(b.padded_range) AS hi FROM room_bookings b
		WHERE b.status = ? AND b.starts_at < ? AND b.ends_at > ?
		AND NOT EXISTS (SELECT 1 FROM event_occurrences o WHERE o.id = b.occurrence_id AND o.event_id = ?)
		ORDER BY b.room_id, lower(b.padded_range)`,
		models.BookingConfirmed, last.Add(slack), first.Add(-slack), eventID).Scan(&rows).Error; err != nil {
		return nil, err
	}
	type held struct {
		starts []time.Time
		latest []time.Time // latest[i]: the latest end among bookings 0..i
	}
	byRoom := map[uint]*held{}
	for _, r := range rows {
		h := byRoom[r.RoomID]
		if h == nil {
			h = &held{}
			byRoom[r.RoomID] = h
		}
		latest := r.Hi
		if n := len(h.latest); n > 0 && h.latest[n-1].After(latest) {
			latest = h.latest[n-1]
		}
		h.starts = append(h.starts, r.Lo)
		h.latest = append(h.latest, latest)
	}
	for _, room := range rooms {
		h := byRoom[room.ID]
		if h == nil {
			continue
		}
		setup := time.Duration(room.DefaultSetupMinutes) * time.Minute
		teardown := time.Duration(room.DefaultTeardownMinutes) * time.Minute
		for _, w := range windows {
			from, to := w.start.Add(-setup), w.end.Add(teardown)
			// Bookings starting before this date's padded end overlap it if
			// any of them ends after its padded start (ranges are [lo, hi)).
			n := sort.Search(len(h.starts), func(i int) bool { return !h.starts[i].Before(to) })
			if n > 0 && h.latest[n-1].After(from) {
				out[room.ID]++
			}
		}
	}
	return out, nil
}

// roomChoices lists the active rooms p may book for the given dates, with
// how many of them each room is busy on. Bookings of eventID's own dates
// don't count: re-picking a series' room never finds it busy with itself.
func roomChoices(db *gorm.DB, p *policy.Person, windows []timeWindow, eventID uint, capacity *int) ([]seriesRoomJSON, error) {
	var rooms []models.Room
	if err := db.Where("active").Order("lower(building), lower(name)").Find(&rooms).Error; err != nil {
		return nil, err
	}
	busy, err := busyDates(db, rooms, windows, eventID)
	if err != nil {
		return nil, err
	}
	want := 0
	if capacity != nil {
		want = *capacity
	}
	out := []seriesRoomJSON{}
	for _, r := range rooms {
		if !policy.CanBookRoom(p, true, r.Restricted) {
			continue
		}
		out = append(out, seriesRoomJSON{ID: r.ID, Name: r.Name, Building: r.Building, Capacity: r.Capacity,
			Features: r.Features, Restricted: r.Restricted, BusyDates: busy[r.ID], TooSmall: tooSmall(r.Capacity, want)})
	}
	return out, nil
}

// apiSeriesRooms lists the rooms for a whole series, with busy counts over
// its scheduled future dates, the series room, and the dates needing a room.
func (a *App) apiSeriesRooms(c *gin.Context) {
	ev, ok := a.loadManagedEvent(c)
	if !ok {
		return
	}
	var occs []models.EventOccurrence
	if err := a.DB.Where("event_id = ? AND status = ? AND starts_at > ?", ev.ID, models.OccurrenceScheduled, time.Now()).
		Order("starts_at").Find(&occs).Error; err != nil {
		jsonError(c, http.StatusInternalServerError, "could not list rooms")
		return
	}
	windows := make([]timeWindow, len(occs))
	for i, o := range occs {
		windows[i] = timeWindow{o.StartsAt, o.EndsAt}
	}
	rooms, err := roomChoices(a.DB, asPerson(apiUser(c)), windows, ev.ID, ev.Capacity)
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not list rooms")
		return
	}
	var held []struct {
		Date, RoomName, Status string
		Waiting                bool
	}
	if err := a.DB.Raw(`SELECT o.original_date AS date, r.name AS room_name, b.status,
			EXISTS (SELECT 1 FROM room_requests q WHERE q.id = b.request_id AND q.status = ?) AS waiting
		FROM room_bookings b JOIN event_occurrences o ON o.id = b.occurrence_id JOIN rooms r ON r.id = b.room_id
		WHERE o.event_id = ? AND o.status = ? AND o.starts_at > ? AND b.status IN ?
		ORDER BY o.starts_at`, models.RoomRequestPending, ev.ID, models.OccurrenceScheduled, time.Now(),
		[]string{models.BookingRequested, models.BookingRejected}).Scan(&held).Error; err != nil {
		jsonError(c, http.StatusInternalServerError, "could not list rooms")
		return
	}
	needs, pending := []needsRoomJSON{}, 0
	for _, h := range held {
		if h.Waiting {
			pending++
		} else {
			needs = append(needs, needsRoomJSON{Date: h.Date, RoomName: h.RoomName, Status: h.Status})
		}
	}
	c.JSON(http.StatusOK, gin.H{"rooms": rooms, "dates": len(occs), "defaultRoom": a.seriesRoom(ev), "needsRoom": needs, "pending": pending})
}

// seriesRoom is the event's series room, or nil.
func (a *App) seriesRoom(ev *models.Event) *occurrenceRoomJSON {
	if ev.DefaultRoomID == nil {
		return nil
	}
	var r models.Room
	if a.DB.First(&r, *ev.DefaultRoomID).Error != nil {
		return nil
	}
	return &occurrenceRoomJSON{ID: r.ID, Name: r.Name, Building: r.Building}
}

// apiSetSeriesRoom books a room for the whole series, or (roomId null)
// stops booking one. An admin's reason double-books busy dates instead of
// leaving them needing a room.
func (a *App) apiSetSeriesRoom(c *gin.Context) {
	ev, ok := a.loadManagedEvent(c)
	if !ok {
		return
	}
	var body struct {
		RoomID        *uint  `json:"roomId"`
		OverlapReason string `json:"overlapReason"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		jsonError(c, http.StatusBadRequest, "invalid request")
		return
	}
	reason := strings.TrimSpace(body.OverlapReason)
	if utf8.RuneCountInString(reason) > maxOverlapReason {
		jsonError(c, http.StatusBadRequest, "the reason must be 500 characters or fewer")
		return
	}
	user := apiUser(c)
	var out seriesBookingJSON
	var emailed []uint
	errNotManager := errors.New("not a manager")
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		var locked models.Event
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&locked, ev.ID).Error; err != nil {
			return err
		}
		if f := a.factsForIn(tx, user, &locked); !policy.CanManageEvent(asPerson(user), f.view, f.rel) {
			return errNotManager
		}
		var err error
		var ids []uint
		out, ids, err = bookWholeSeries(tx, &locked, body.RoomID, user, reason, c.ClientIP())
		emailed = ids
		return err
	})
	switch {
	case errors.Is(err, errNotManager):
		jsonError(c, http.StatusForbidden, "you can't change this event")
	case errors.Is(err, errRoomGone):
		jsonError(c, http.StatusNotFound, "room not found")
	case errors.Is(err, errRestricted):
		jsonError(c, http.StatusForbidden, "only an admin can book this room")
	case err != nil:
		jsonError(c, http.StatusInternalServerError, "could not save the room")
	default:
		a.deliverNotificationEmails(emailed)
		c.JSON(http.StatusOK, out)
	}
}

// bookWholeSeries sets ev's series room (nil stops it) and books it on every
// scheduled future date, under ev's lock. A date keeps a room picked for it
// alone, and a booking only an admin could make stays unless an admin is
// booking. Bookings in the old series room, and requested ones, are
// replaced. A busy date keeps a requested booking (DEC-103) unless an
// admin's reason double-books it.
//
// With approval on (DEC-102), a leader's or host's free dates wait on one
// request for the whole series (DEC-106); busy ones are lost as above. The
// notification ids to email (the admins' task) are returned.
func bookWholeSeries(tx *gorm.DB, ev *models.Event, roomID *uint, user *models.User, reason, ip string) (seriesBookingJSON, []uint, error) {
	out := seriesBookingJSON{Lost: []string{}}
	me := asPerson(user)
	if !policy.CanDoubleBookRoom(me) {
		reason = ""
	}
	var occs []models.EventOccurrence
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("event_id = ? AND status = ? AND starts_at > ?", ev.ID, models.OccurrenceScheduled, time.Now()).
		Order("id").Find(&occs).Error; err != nil {
		return out, nil, err
	}
	// Dates first, then the room: the same lock order as one date's booking.
	var room *models.Room
	if roomID != nil {
		r, err := lockRoom(tx, *roomID)
		if errors.Is(err, gorm.ErrRecordNotFound) || (err == nil && !r.Active) {
			return out, nil, errRoomGone
		}
		if err != nil {
			return out, nil, err
		}
		if !policy.CanBookRoom(me, true, r.Restricted) {
			return out, nil, errRestricted
		}
		room = r
		out.DefaultRoom = &occurrenceRoomJSON{ID: r.ID, Name: r.Name, Building: r.Building}
	}
	var req *models.RoomRequest
	var requestID *uint
	if room != nil && policy.BookingNeedsApproval(me, roomApprovalOn(tx)) {
		var err error
		if req, err = openRoomRequest(tx, ev.ID, room.ID, user.ID, true); err != nil {
			return out, nil, err
		}
		requestID = &req.ID
	}
	ids := make([]uint, len(occs))
	for i, o := range occs {
		ids[i] = o.ID
	}
	var current []struct {
		models.RoomBooking
		Restricted bool
	}
	if len(ids) > 0 {
		if err := tx.Raw(`SELECT b.*, r.restricted FROM room_bookings b JOIN rooms r ON r.id = b.room_id
			WHERE b.occurrence_id IN ?`, ids).Scan(&current).Error; err != nil {
			return out, nil, err
		}
	}
	byDate := make(map[uint]int, len(current))
	for i, b := range current {
		byDate[b.OccurrenceID] = i
	}
	oldRoom := ev.DefaultRoomID
	inOld := func(b models.RoomBooking) bool { return oldRoom != nil && b.RoomID == *oldRoom }
	released := 0
	for i := range occs {
		occ := &occs[i]
		if j, has := byDate[occ.ID]; has {
			b := current[j]
			switch {
			case room != nil && b.RoomID == room.ID && b.Status == models.BookingConfirmed:
				continue // already there, with any padding an admin set
			case (b.Restricted || b.AllowOverlap) && !policy.CanDoubleBookRoom(me):
				out.Kept++
				continue
			case room == nil && !inOld(b.RoomBooking):
				continue // not the series room's booking: stopping leaves it
			case room != nil && b.Status == models.BookingConfirmed && !inOld(b.RoomBooking):
				out.Kept++
				continue
			}
			if err := tx.Delete(&models.RoomBooking{}, b.ID).Error; err != nil {
				return out, nil, err
			}
			released++
		}
		if room == nil {
			continue
		}
		result, err := placeBooking(tx, occ, room, room.DefaultSetupMinutes, room.DefaultTeardownMinutes,
			placement{by: &user.ID, reason: reason, keepRequested: true, requestID: requestID})
		if err != nil {
			return out, nil, err
		}
		switch result {
		case bookedRequested:
			out.Lost = append(out.Lost, occ.OriginalDate)
		case bookedPending:
			out.Pending++
		default:
			out.Booked++
		}
	}
	var emailed []uint
	if req != nil && out.Pending == 0 {
		if err := tx.Delete(req).Error; err != nil {
			return out, nil, err
		}
		req = nil
	}
	if req != nil {
		var err error
		if emailed, err = notifyRoomRequested(tx, req, ev, room, out.Pending, user.ID); err != nil {
			return out, nil, err
		}
	}
	// Replaced bookings may have been other requests' last dates.
	if err := closeEmptyRoomRequests(tx); err != nil {
		return out, nil, err
	}
	if err := tx.Model(&models.Event{}).Where("id = ?", ev.ID).Update("default_room_id", roomID).Error; err != nil {
		return out, nil, err
	}
	ev.DefaultRoomID = roomID
	meta := map[string]any{"eventId": ev.ID, "roomId": roomID, "previousRoomId": oldRoom,
		"booked": out.Booked, "lost": out.Lost, "kept": out.Kept, "released": released, "pending": out.Pending}
	if reason != "" {
		meta["reason"] = reason
	}
	if req != nil {
		meta["requestId"] = req.ID
	}
	return out, emailed, audit.Write(tx, audit.Entry{Actor: &user.ID, Action: audit.RoomBookingSeries, TargetType: "event", TargetID: ev.ID,
		Meta: meta, IP: ip})
}

// bookSeriesRoom books the series room for dates that were just added (by
// the daily extender or a series edit) or brought back (DEC-91) and have no
// booking. A busy date keeps a requested booking; its date is returned.
// A series room that was since deactivated books nothing.
func bookSeriesRoom(tx *gorm.DB, ev *models.Event, occurrenceIDs []uint, by *uint) ([]string, error) {
	if ev.DefaultRoomID == nil || len(occurrenceIDs) == 0 {
		return nil, nil
	}
	var occs []models.EventOccurrence
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id IN ? AND status = ? AND starts_at > now() AND NOT EXISTS (SELECT 1 FROM room_bookings b WHERE b.occurrence_id = event_occurrences.id)",
			occurrenceIDs, models.OccurrenceScheduled).Order("id").Find(&occs).Error; err != nil {
		return nil, err
	}
	if len(occs) == 0 {
		return nil, nil
	}
	room, err := lockRoom(tx, *ev.DefaultRoomID)
	if err != nil || !room.Active {
		return nil, err
	}
	// A series room still waiting on approval takes new dates into its request.
	var requestID *uint
	var open []models.RoomRequest
	if err := tx.Where("event_id = ? AND room_id = ? AND series AND status = ?", ev.ID, room.ID, models.RoomRequestPending).
		Order("id DESC").Limit(1).Find(&open).Error; err != nil {
		return nil, err
	}
	if len(open) == 1 {
		requestID = &open[0].ID
	}
	var lost []string
	for i := range occs {
		result, err := placeBooking(tx, &occs[i], room, room.DefaultSetupMinutes, room.DefaultTeardownMinutes,
			placement{by: by, keepRequested: true, requestID: requestID})
		if err != nil {
			return nil, err
		}
		if result == bookedRequested {
			lost = append(lost, occs[i].OriginalDate)
		}
	}
	return lost, nil
}

// settleDateRooms brings an event's bookings in line after its dates
// changed: moved dates take their bookings along, added or restored dates
// get the series room, and the leaders or hosts hear once about every date
// that ended up without its room. Notification ids to email are returned.
func settleDateRooms(tx *gorm.DB, ev *models.Event, moved, added []uint, by *uint) ([]uint, error) {
	lost, err := moveRoomBookings(tx, moved)
	if err != nil {
		return nil, err
	}
	more, err := bookSeriesRoom(tx, ev, added, by)
	if err != nil {
		return nil, err
	}
	lost = append(lost, more...)
	if len(lost) == 0 {
		return nil, nil
	}
	return notifyRoomLost(tx, ev, lost)
}

// afterExtend books the series room for dates the daily extender adds,
// collecting notification ids to email once the run is over. While rooms
// are off it books nothing and tells no one. A failure here is logged and
// undone on its own, so it never stops the event's new dates being added.
func (a *App) afterExtend(emailed *[]uint) events.AfterEnsure {
	return func(tx *gorm.DB, ev *models.Event, added []string) error {
		if ev.DefaultRoomID == nil || !a.roomsEnabled() {
			return nil
		}
		if err := tx.SavePoint("rooms").Error; err != nil {
			return err
		}
		sent, err := func() ([]uint, error) {
			var ids []uint
			if err := tx.Model(&models.EventOccurrence{}).Where("event_id = ? AND original_date IN ?", ev.ID, added).
				Pluck("id", &ids).Error; err != nil {
				return nil, err
			}
			return settleDateRooms(tx, ev, nil, ids, nil)
		}()
		if err != nil {
			log.Printf("occurrences: event %d: series room: %v", ev.ID, err)
			return tx.RollbackTo("rooms").Error
		}
		*emailed = append(*emailed, sent...)
		return nil
	}
}

// apiCheckRooms lists the rooms for an event being created, with busy
// counts over the dates it would have (the create form's Room field).
// groupId names the group creating it; without one, the caller creates a
// church-wide event, which only admins do.
func (a *App) apiCheckRooms(c *gin.Context) {
	var body struct {
		eventBody
		GroupID *uint `json:"groupId"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		jsonError(c, http.StatusBadRequest, "startsLocal and durationMinutes are required")
		return
	}
	me := apiUser(c)
	if body.GroupID != nil {
		if _, _, ok := a.loadManagedGroupID(c, *body.GroupID); !ok {
			return
		}
	} else if !policy.IsAdmin(asPerson(me)) {
		jsonError(c, http.StatusForbidden, "admin required")
		return
	}
	_, whens, ok := a.specFrom(c, body.eventBody, nil, true)
	if !ok {
		return
	}
	now := time.Now()
	windows := []timeWindow{}
	for _, w := range whens {
		if w.StartsAt.After(now) {
			windows = append(windows, timeWindow{w.StartsAt, w.EndsAt})
		}
	}
	ed, ok := parseCapacityEdits(c, body.eventBody)
	if !ok {
		return
	}
	rooms, err := roomChoices(a.DB, asPerson(me), windows, 0, ed.cap)
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not list rooms")
		return
	}
	c.JSON(http.StatusOK, gin.H{"rooms": rooms, "dates": len(windows)})
}

// bookCreatedSeries books the room picked on a create form for every date
// of the new event, in the creating transaction. Notification ids to email
// (an approval task) are returned.
func (a *App) bookCreatedSeries(tx *gorm.DB, ev *models.Event, roomID *uint, user *models.User, ip string) ([]uint, error) {
	if roomID == nil {
		return nil, nil
	}
	if !a.roomsEnabled() {
		return nil, errRoomGone
	}
	_, emailed, err := bookWholeSeries(tx, ev, roomID, user, "", ip)
	return emailed, err
}

// createRoomFailed answers a create whose room couldn't be booked.
func createRoomFailed(c *gin.Context, err error) bool {
	switch {
	case errors.Is(err, errRoomGone):
		jsonError(c, http.StatusNotFound, "room not found")
	case errors.Is(err, errRestricted):
		jsonError(c, http.StatusForbidden, "only an admin can book this room")
	default:
		return false
	}
	return true
}
