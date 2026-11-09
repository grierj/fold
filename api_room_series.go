package main

import (
	"errors"
	"log"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"fold/internal/audit"
	"fold/internal/eventdates"
	"fold/internal/policy"
	"fold/internal/rooms"
	"fold/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Series rooms (M2.1c, 9.2). "Book for the whole series" stores the room on
// the event (default_room_id) and books every scheduled future date. A date
// the room is busy on keeps a requested booking, holding nothing, and shows
// on the event page as needing a room (DEC-103). Dates the daily extender or
// a series edit adds later are booked in the series room the same way.

var (
	errRoomGone   = apiErr(http.StatusNotFound, "", "room not found")
	errRestricted = errRoomRestricted
)

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

// roomChoices lists the active rooms p may book for the given dates, with
// how many of them each room is busy on. Bookings of eventID's own dates
// don't count: re-picking a series' room never finds it busy with itself.
func roomChoices(db *gorm.DB, p *policy.Person, windows []rooms.Window, eventID uint, capacity *int) ([]seriesRoomJSON, error) {
	list, busy, err := activeRoomsBusy(db, windows, eventID)
	if err != nil {
		return nil, err
	}
	want := 0
	if capacity != nil {
		want = *capacity
	}
	out := []seriesRoomJSON{}
	for _, r := range list {
		if !policy.CanBookRoom(p, true, r.Restricted) {
			continue
		}
		out = append(out, seriesRoomJSON{ID: r.ID, Name: r.Name, Building: r.Building, Capacity: r.Capacity,
			Features: r.Features, Restricted: r.Restricted, BusyDates: busy[r.ID], TooSmall: tooSmall(r.Capacity, want)})
	}
	return out, nil
}

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
	windows := make([]rooms.Window, len(occs))
	for i, o := range occs {
		windows[i] = rooms.Window{Start: o.StartsAt, End: o.EndsAt}
	}
	choices, err := roomChoices(a.DB, asPerson(apiUser(c)), windows, ev.ID, ev.Capacity)
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not list rooms")
		return
	}
	var held []struct {
		Date, RoomName, Status string
		Waiting                bool
	}
	if err := a.DB.Raw(`SELECT to_char(o.original_date, 'YYYY-MM-DD') AS date, r.name AS room_name, b.status,
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
	suggestions := a.roomSuggestions(suggestFor{person: asPerson(apiUser(c)), eventID: ev.ID, groupID: ev.GroupID,
		visibility: ev.Visibility, capacity: ev.Capacity, windows: windowsOf(occs)}, offeredRooms(choices))
	c.JSON(http.StatusOK, seriesRoomsJSON{Rooms: choices, Dates: len(occs), DefaultRoom: a.seriesRoom(ev), NeedsRoom: needs, Pending: pending, Suggestions: suggestions})
}

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
		fail(c, errOverlapReasonTooLong)
		return
	}
	user := apiUser(c)
	var out seriesBookingJSON
	errNotManager := errors.New("not a manager")
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		var locked models.Event
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&locked, ev.ID).Error; err != nil {
			return err
		}
		f, err := eventFactsIn(tx, user, &locked)
		if err != nil {
			return err
		}
		if !f.canManage(user) {
			return errNotManager
		}
		out, err = bookWholeSeries(tx, &locked, body.RoomID, user, reason, c.ClientIP())
		return err
	})
	switch {
	case errors.Is(err, errNotManager):
		fail(c, errCannotChangeEvent)
	case errors.Is(err, errRoomGone):
		jsonError(c, http.StatusNotFound, "room not found")
	case errors.Is(err, errRestricted):
		fail(c, errRoomRestricted)
	case err != nil:
		jsonError(c, http.StatusInternalServerError, "could not save the room")
	default:
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
// request for the whole series (DEC-106); busy ones are lost as above.
func bookWholeSeries(tx *gorm.DB, ev *models.Event, roomID *uint, user *models.User, reason, ip string) (seriesBookingJSON, error) {
	out := seriesBookingJSON{Lost: []string{}}
	me := asPerson(user)
	if !policy.CanDoubleBookRoom(me) {
		reason = ""
	}
	var occs []models.EventOccurrence
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("event_id = ? AND status = ? AND starts_at > ?", ev.ID, models.OccurrenceScheduled, time.Now()).
		Order("id").Find(&occs).Error; err != nil {
		return out, err
	}
	// Dates first, then the room: the same lock order as one date's booking.
	var room *models.Room
	if roomID != nil {
		r, err := lockRoom(tx, *roomID)
		if errors.Is(err, gorm.ErrRecordNotFound) || (err == nil && !r.Active) {
			return out, errRoomGone
		}
		if err != nil {
			return out, err
		}
		if !policy.CanBookRoom(me, true, r.Restricted) {
			return out, errRestricted
		}
		room = r
		out.DefaultRoom = &occurrenceRoomJSON{ID: r.ID, Name: r.Name, Building: r.Building}
	}
	var req *models.RoomRequest
	var requestID *uint
	// Re-booking the series room an admin already approved (to retry lost
	// dates) needs no new approval.
	approved := ev.DefaultRoomID != nil && room != nil && *ev.DefaultRoomID == room.ID && !ev.DefaultRoomPending
	needsApproval := room != nil && !approved && policy.BookingNeedsApproval(me, roomApprovalOn(tx))
	if needsApproval {
		var err error
		if req, err = openRoomRequest(tx, ev.ID, room.ID, &user.ID, true); err != nil {
			return out, err
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
			return out, err
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
				return out, err
			}
			released++
		}
		if room == nil {
			continue
		}
		result, err := placeBooking(tx, occ, room, room.DefaultSetupMinutes, room.DefaultTeardownMinutes,
			placement{by: &user.ID, reason: reason, keepRequested: true, requestID: requestID})
		if err != nil {
			return out, err
		}
		switch result {
		case bookedRequested:
			out.Lost = append(out.Lost, models.FormatCalendarDate(occ.OriginalDate))
		case bookedPending:
			out.Pending++
		default:
			out.Booked++
		}
	}
	if req != nil && out.Pending == 0 {
		if err := tx.Delete(req).Error; err != nil {
			return out, err
		}
		req = nil
	}
	if req != nil {
		var err error
		if err = notifyRoomRequested(tx, req, ev, room, out.Pending, &user.ID); err != nil {
			return out, err
		}
	}
	// Replaced bookings may have been other requests' last dates.
	if err := closeEmptyRoomRequests(tx); err != nil {
		return out, err
	}
	// Until an admin approves it, the series room's new dates keep going to
	// an admin, even if this request is withdrawn or had nothing to wait on.
	if err := tx.Model(&models.Event{}).Where("id = ?", ev.ID).
		Updates(map[string]any{"default_room_id": roomID, "default_room_pending": needsApproval}).Error; err != nil {
		return out, err
	}
	ev.DefaultRoomID, ev.DefaultRoomPending = roomID, needsApproval
	meta := map[string]any{"eventId": ev.ID, "roomId": roomID, "previousRoomId": oldRoom,
		"booked": out.Booked, "lost": out.Lost, "kept": out.Kept, "released": released, "pending": out.Pending}
	if reason != "" {
		meta["reason"] = reason
	}
	if req != nil {
		meta["requestId"] = req.ID
	}
	return out, audit.Write(tx, audit.Entry{Actor: &user.ID, Action: audit.RoomBookingSeries, TargetType: "event", TargetID: ev.ID,
		Meta: meta, IP: ip})
}

// bookSeriesRoom books the series room for dates that were just added (by
// the daily extender or a series edit) or brought back (DEC-91) and have no
// booking. A busy date keeps a requested booking; its date is returned.
// A series room that was since deactivated books nothing.
//
// A series room not yet approved (default_room_pending) books new dates as
// a request while approval is on: into the waiting series request, or a
// new one if that was withdrawn.
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
	var req *models.RoomRequest
	opened := false
	if ev.DefaultRoomPending && roomApprovalOn(tx) {
		var open []models.RoomRequest
		if err := tx.Where("event_id = ? AND room_id = ? AND series AND status = ?", ev.ID, room.ID, models.RoomRequestPending).
			Limit(1).Find(&open).Error; err != nil {
			return nil, err
		}
		if len(open) == 1 {
			req = &open[0]
		} else {
			// Asked for by whoever asked for the series room last.
			var last []models.RoomRequest
			if err := tx.Where("event_id = ? AND room_id = ? AND series", ev.ID, room.ID).Order("id DESC").Limit(1).Find(&last).Error; err != nil {
				return nil, err
			}
			var asker *uint
			if len(last) == 1 {
				asker = last[0].RequestedBy
			}
			if req, err = openRoomRequest(tx, ev.ID, room.ID, asker, true); err != nil {
				return nil, err
			}
			opened = true
		}
	}
	var requestID *uint
	if req != nil {
		requestID = &req.ID
	}
	var lost []string
	waiting := 0
	for i := range occs {
		result, err := placeBooking(tx, &occs[i], room, room.DefaultSetupMinutes, room.DefaultTeardownMinutes,
			placement{by: by, keepRequested: true, requestID: requestID})
		if err != nil {
			return nil, err
		}
		switch result {
		case bookedRequested:
			lost = append(lost, models.FormatCalendarDate(occs[i].OriginalDate))
		case bookedPending:
			waiting++
		}
	}
	if !opened {
		return lost, nil
	}
	if waiting == 0 {
		return lost, tx.Delete(req).Error
	}
	return lost, notifyRoomRequested(tx, req, ev, room, waiting, req.RequestedBy)
}

// settleDateRooms brings an event's bookings in line after its dates
// changed: moved dates take their bookings along, added or restored dates
// get the series room, and the leaders or hosts hear once about every date
// that ended up without its room.
func settleDateRooms(tx *gorm.DB, ev *models.Event, moved, added []uint, by *uint) error {
	lost, err := moveRoomBookings(tx, moved)
	if err != nil {
		return err
	}
	more, err := bookSeriesRoom(tx, ev, added, by)
	if err != nil {
		return err
	}
	lost = append(lost, more...)
	if len(lost) == 0 {
		return nil
	}
	return notifyRoomLost(tx, ev, lost)
}

// afterExtend books the series room for dates the daily extender adds;
// its notices queue their own delivery in the same transaction. While rooms
// are off it books nothing and tells no one. A failure here is logged and
// undone on its own, so it never stops the event's new dates being added.
func (a *App) afterExtend() eventdates.AfterEnsure {
	return func(tx *gorm.DB, ev *models.Event, added []string) error {
		if ev.DefaultRoomID == nil || !a.roomsEnabled() {
			return nil
		}
		if err := tx.SavePoint("rooms").Error; err != nil {
			return err
		}
		err := func() error {
			var ids []uint
			if err := tx.Model(&models.EventOccurrence{}).Where("event_id = ? AND original_date IN ?", ev.ID, added).
				Pluck("id", &ids).Error; err != nil {
				return err
			}
			return settleDateRooms(tx, ev, nil, ids, nil)
		}()
		if err != nil {
			log.Printf("occurrences: event %d: series room: %v", ev.ID, err)
			return tx.RollbackTo("rooms").Error
		}
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
		fail(c, errStartRequired)
		return
	}
	me := apiUser(c)
	var group *models.Group
	if body.GroupID != nil {
		g, _, ok := a.loadManagedGroupID(c, *body.GroupID)
		if !ok {
			return
		}
		group = g
	} else if !policy.IsAdmin(asPerson(me)) {
		jsonError(c, http.StatusForbidden, "admin required")
		return
	}
	_, whens, err := a.specFrom(body.eventBody, nil, true)
	if err != nil {
		fail(c, err)
		return
	}
	now := time.Now()
	upcoming := []rooms.Window{}
	for _, w := range whens {
		if w.StartsAt.After(now) {
			upcoming = append(upcoming, rooms.Window{Start: w.StartsAt, End: w.EndsAt})
		}
	}
	ed, err := parseCapacityEdits(body.eventBody)
	if err != nil {
		fail(c, err)
		return
	}
	list, err := roomChoices(a.DB, asPerson(me), upcoming, 0, ed.cap)
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not list rooms")
		return
	}
	// A new event has no dates of its own yet; Rule 1 uses the visibility
	// picked on the form (DEC-110).
	suggestions := a.roomSuggestions(suggestFor{person: asPerson(me), groupID: body.GroupID,
		visibility: createVisibility(body.Visibility, group), capacity: ed.cap, windows: upcoming, tags: groupTags(group)}, offeredRooms(list))
	c.JSON(http.StatusOK, roomsCheckJSON{Rooms: list, Dates: len(upcoming), Suggestions: suggestions})
}

// bookCreatedSeries books the room picked on a create form for every date
// of the new event, in the creating transaction.
func (a *App) bookCreatedSeries(tx *gorm.DB, ev *models.Event, roomID *uint, user *models.User, ip string) error {
	if roomID == nil {
		return nil
	}
	if !a.roomsEnabled() {
		return errRoomGone
	}
	_, err := bookWholeSeries(tx, ev, roomID, user, "", ip)
	return err
}
