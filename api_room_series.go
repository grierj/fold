package main

import (
	"errors"
	"net/http"
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

// needsRoomJSON is a date whose booking holds nothing: it lost its room
// when it moved, or the room was busy when the series was booked.
type needsRoomJSON struct {
	Date     string `json:"date"`
	RoomName string `json:"roomName"`
}

// seriesBookingJSON says what booking a series did.
type seriesBookingJSON struct {
	DefaultRoom *occurrenceRoomJSON `json:"defaultRoom"`
	Booked      int                 `json:"booked"`
	// Lost: dates the room was busy on, kept as requested.
	Lost []string `json:"lost"`
	// Kept: dates that keep a room picked for them alone.
	Kept int `json:"kept"`
}

// timeWindow is one date's unpadded time.
type timeWindow struct{ start, end time.Time }

// pgTimes is a Postgres array literal of times, for unnest.
func pgTimes(ts []time.Time) string {
	parts := make([]string, len(ts))
	for i, t := range ts {
		parts[i] = `"` + t.UTC().Format(time.RFC3339Nano) + `"`
	}
	return "{" + strings.Join(parts, ",") + "}"
}

// roomChoices lists the active rooms p may book for the given dates, with
// how many of them each room is busy on. Bookings of eventID's own dates
// don't count: re-picking a series' room never finds it busy with itself.
func roomChoices(db *gorm.DB, p *policy.Person, windows []timeWindow, eventID uint, capacity *int) ([]seriesRoomJSON, error) {
	var rooms []models.Room
	if err := db.Where("active").Order("lower(building), lower(name)").Find(&rooms).Error; err != nil {
		return nil, err
	}
	busy := map[uint]int{}
	if len(windows) > 0 {
		starts, ends := make([]time.Time, len(windows)), make([]time.Time, len(windows))
		for i, w := range windows {
			starts[i], ends[i] = w.start, w.end
		}
		var rows []struct {
			RoomID uint
			Dates  int
		}
		if err := db.Raw(`SELECT r.id AS room_id, count(DISTINCT w.n) AS dates
			FROM unnest(?::timestamptz[], ?::timestamptz[]) WITH ORDINALITY AS w(s, e, n)
			JOIN rooms r ON r.active
			JOIN room_bookings b ON b.room_id = r.id AND b.status = ?
				AND b.padded_range && tstzrange(w.s - make_interval(mins => r.default_setup_minutes::int),
					w.e + make_interval(mins => r.default_teardown_minutes::int), '[)')
			WHERE NOT EXISTS (SELECT 1 FROM event_occurrences o WHERE o.id = b.occurrence_id AND o.event_id = ?)
			GROUP BY r.id`, pgTimes(starts), pgTimes(ends), models.BookingConfirmed, eventID).Scan(&rows).Error; err != nil {
			return nil, err
		}
		for _, r := range rows {
			busy[r.RoomID] = r.Dates
		}
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
	needs := []needsRoomJSON{}
	if err := a.DB.Raw(`SELECT o.original_date AS date, r.name AS room_name FROM room_bookings b
		JOIN event_occurrences o ON o.id = b.occurrence_id JOIN rooms r ON r.id = b.room_id
		WHERE o.event_id = ? AND o.status = ? AND o.starts_at > ? AND b.status = ?
		ORDER BY o.starts_at`, ev.ID, models.OccurrenceScheduled, time.Now(), models.BookingRequested).Scan(&needs).Error; err != nil {
		jsonError(c, http.StatusInternalServerError, "could not list rooms")
		return
	}
	c.JSON(http.StatusOK, gin.H{"rooms": rooms, "dates": len(occs), "defaultRoom": a.seriesRoom(ev), "needsRoom": needs})
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
		out, err = bookWholeSeries(tx, &locked, body.RoomID, user, reason, c.ClientIP())
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
		c.JSON(http.StatusOK, out)
	}
}

// bookWholeSeries sets ev's series room (nil stops it) and books it on every
// scheduled future date, under ev's lock. A date keeps a room picked for it
// alone, and a booking only an admin could make stays unless an admin is
// booking. Bookings in the old series room, and requested ones, are
// replaced. A busy date keeps a requested booking (DEC-103) unless an
// admin's reason double-books it.
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
		result, err := placeBooking(tx, occ, room, room.DefaultSetupMinutes, room.DefaultTeardownMinutes, &user.ID, reason, true)
		if err != nil {
			return out, err
		}
		if result == bookedRequested {
			out.Lost = append(out.Lost, occ.OriginalDate)
		} else {
			out.Booked++
		}
	}
	if err := tx.Model(&models.Event{}).Where("id = ?", ev.ID).Update("default_room_id", roomID).Error; err != nil {
		return out, err
	}
	ev.DefaultRoomID = roomID
	meta := map[string]any{"eventId": ev.ID, "roomId": roomID, "previousRoomId": oldRoom,
		"booked": out.Booked, "lost": out.Lost, "kept": out.Kept, "released": released}
	if reason != "" {
		meta["reason"] = reason
	}
	return out, audit.Write(tx, audit.Entry{Actor: &user.ID, Action: audit.RoomBookingSeries, TargetType: "event", TargetID: ev.ID,
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
		Where("id IN ? AND status = ? AND NOT EXISTS (SELECT 1 FROM room_bookings b WHERE b.occurrence_id = event_occurrences.id)",
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
	var lost []string
	for i := range occs {
		result, err := placeBooking(tx, &occs[i], room, room.DefaultSetupMinutes, room.DefaultTeardownMinutes, by, "", true)
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
// are off it books nothing and tells no one.
func (a *App) afterExtend(emailed *[]uint) events.AfterEnsure {
	return func(tx *gorm.DB, ev *models.Event, added []string) error {
		if ev.DefaultRoomID == nil || !a.roomsEnabled() {
			return nil
		}
		var ids []uint
		if err := tx.Model(&models.EventOccurrence{}).Where("event_id = ? AND original_date IN ?", ev.ID, added).
			Pluck("id", &ids).Error; err != nil {
			return err
		}
		sent, err := settleDateRooms(tx, ev, nil, ids, nil)
		*emailed = append(*emailed, sent...)
		return err
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
