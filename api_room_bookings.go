package main

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"fold/internal/audit"
	"fold/internal/notify"
	"fold/internal/policy"
	"fold/models"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ---------------------------------------------------------------------------
// Room bookings (M2.1b, 9.2). A booking holds one room for one date, padded
// by the room's setup and teardown minutes (DEC-101). Confirmed bookings of
// one room never overlap: the database refuses it (room_bookings_no_overlap),
// so two leaders booking at once can't both win. An admin may double-book
// with a reason, which is audited. Moving a date moves its booking; a move
// into a conflict keeps the booking as requested (holding nothing) and tells
// the people running the event.
// ---------------------------------------------------------------------------

const maxOverlapReason = 500

// paddedRangeSQL is the one expression for a booking's held time; the
// table's CHECK compares padded_range against the same formula.
const paddedRangeSQL = `tstzrange(?::timestamptz - make_interval(mins => ?::int), ?::timestamptz + make_interval(mins => ?::int), '[)')`

// heldByOverlap reports an admin's double-booking holding a room for the
// given padded time. Double-bookings sit outside the exclusion constraint,
// so booking and moving check for them here: they count as busy for
// everyone else (9.2).
func heldByOverlap(tx *gorm.DB, roomID, occurrenceID uint, start, end time.Time, setup, teardown int) (bool, error) {
	var held bool
	// The status is a literal so the planner can use idx_room_bookings_overlap.
	err := tx.Raw(`SELECT EXISTS (SELECT 1 FROM room_bookings WHERE room_id = ? AND status = 'confirmed' AND allow_overlap
		AND occurrence_id <> ? AND padded_range && `+paddedRangeSQL+`)`,
		roomID, occurrenceID, start, setup, end, teardown).Scan(&held).Error
	return held, err
}

// lockRoom serializes booking writes per room, so the double-booking check
// above can't race another booking of the same room. Callers lock the
// occurrence first and the room second, so the order never inverts.
func lockRoom(tx *gorm.DB, roomID uint) (*models.Room, error) {
	var r models.Room
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&r, roomID).Error; err != nil {
		return nil, err
	}
	return &r, nil
}

// bookResult is what placeBooking wrote.
type bookResult int

const (
	bookedFree      bookResult = iota // confirmed: the room was free
	bookedOverlap                     // confirmed over a conflict, with an admin's reason
	bookedRequested                   // the room was busy: requested, holding nothing
	bookedPending                     // the room was free: requested, waiting on an admin's approval
	notBooked                         // the room was busy and nothing was written
)

// placement says how placeBooking books.
type placement struct {
	by *uint
	// reason double-books a busy room; the caller checks it is an admin's.
	reason string
	// keepRequested keeps a busy date as requested (holding nothing)
	// instead of writing nothing.
	keepRequested bool
	// requestID, when set, makes a free date wait on that approval request
	// (M2.1d) instead of confirming.
	requestID *uint
}

// placeBooking books room for a date that has no booking. The caller holds
// the date's lock, then the room's.
func placeBooking(tx *gorm.DB, occ *models.EventOccurrence, room *models.Room, setup, teardown int, p placement) (bookResult, error) {
	by, reason, keepRequested := p.by, p.reason, p.keepRequested
	insert := func(status string, overlap bool, why string) error {
		return tx.Exec(`INSERT INTO room_bookings (occurrence_id, room_id, starts_at, ends_at, setup_minutes, teardown_minutes,
			padded_range, status, allow_overlap, overlap_reason, created_by) VALUES (?, ?, ?, ?, ?, ?, `+paddedRangeSQL+`, ?, ?, ?, ?)`,
			occ.ID, room.ID, occ.StartsAt, occ.EndsAt, setup, teardown,
			occ.StartsAt, setup, occ.EndsAt, teardown,
			status, overlap, why, by).Error
	}
	held, err := heldByOverlap(tx, room.ID, occ.ID, occ.StartsAt, occ.EndsAt, setup, teardown)
	if err != nil {
		return 0, err
	}
	if !held {
		free, err := trySavepoint(tx, fmt.Sprintf("book%d", occ.ID), func() error {
			return insert(models.BookingConfirmed, false, "")
		})
		if err != nil {
			return 0, err
		}
		if free && p.requestID != nil {
			// The insert proved the room free; the booking then waits for an
			// admin, holding nothing until it is confirmed.
			return bookedPending, tx.Exec(`UPDATE room_bookings SET status = ?, request_id = ? WHERE occurrence_id = ?`,
				models.BookingRequested, *p.requestID, occ.ID).Error
		}
		if free {
			return bookedFree, nil
		}
	}
	switch {
	case reason != "":
		return bookedOverlap, insert(models.BookingConfirmed, true, reason)
	case keepRequested:
		return bookedRequested, insert(models.BookingRequested, false, "")
	}
	return notBooked, nil
}

// trySavepoint runs write under a savepoint. ok is false when the exclusion
// constraint refused it; the transaction is then as it was before. The
// savepoint is released either way: a whole series books hundreds of dates
// in one transaction, and open subtransactions slow every session.
func trySavepoint(tx *gorm.DB, name string, write func() error) (ok bool, err error) {
	if err := tx.SavePoint(name).Error; err != nil {
		return false, err
	}
	werr := write()
	if werr != nil && !isOverlap(werr) {
		return false, werr
	}
	if werr != nil {
		if err := tx.RollbackTo(name).Error; err != nil {
			return false, err
		}
	}
	return werr == nil, tx.Exec("RELEASE SAVEPOINT " + name).Error
}

// isOverlap reports the exclusion constraint refusing a booking.
func isOverlap(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23P01" && pgErr.ConstraintName == "room_bookings_no_overlap"
}

// roomPickerJSON is one bookable room as the picker for a date sees it.
type roomPickerJSON struct {
	ID         uint   `json:"id"`
	Name       string `json:"name"`
	Building   string `json:"building"`
	Capacity   *int   `json:"capacity"`
	Features   string `json:"features"`
	Restricted bool   `json:"restricted"`
	// Busy: another confirmed booking holds the room for this date's padded
	// time. Who holds it is never said.
	Busy bool `json:"busy"`
	// TooSmall: the date's seat cap or going count is above the room's
	// capacity. A warning, never a block (9.2).
	TooSmall bool `json:"tooSmall"`
}

// roomBookingJSON is a date's booking as its managers see it.
type roomBookingJSON struct {
	RoomID          uint   `json:"roomId"`
	RoomName        string `json:"roomName"`
	Building        string `json:"building"`
	Status          string `json:"status"`
	SetupMinutes    int    `json:"setupMinutes"`
	TeardownMinutes int    `json:"teardownMinutes"`
	DoubleBooked    bool   `json:"doubleBooked"`
	TooSmall        bool   `json:"tooSmall"`
	// Pending: requested, waiting on an admin's approval (M2.1d). A
	// requested booking that isn't pending lost its room.
	Pending bool `json:"pending"`
}

// occurrenceRoomJSON is the room a date's people see as its place.
type occurrenceRoomJSON struct {
	ID       uint   `json:"id"`
	Name     string `json:"name"`
	Building string `json:"building"`
}

// managedOccurrence loads the event the caller runs and its scheduled date.
func (a *App) managedOccurrence(c *gin.Context) (*models.Event, *models.EventOccurrence, bool) {
	ev, ok := a.loadManagedEvent(c)
	if !ok {
		return nil, nil, false
	}
	date := c.Param("date")
	if _, err := time.Parse("2006-01-02", date); err != nil {
		jsonError(c, http.StatusNotFound, "that date is not on this event")
		return nil, nil, false
	}
	var occ models.EventOccurrence
	if err := a.DB.Where("event_id = ? AND original_date = ? AND status = ?", ev.ID, date, models.OccurrenceScheduled).First(&occ).Error; err != nil {
		jsonError(c, http.StatusNotFound, "that date is not on this event")
		return nil, nil, false
	}
	return ev, &occ, true
}

// seatsWanted is what a room must hold for a date: the larger of its seat
// cap and the seats already going.
func seatsWanted(db *gorm.DB, ev *models.Event, occ *models.EventOccurrence) int {
	want := 0
	if cap := effectiveCapacity(ev, occ); cap != nil {
		want = *cap
	}
	// The same in-person count capacity uses: listed accounts only, remote
	// attendees aside. A failed count leaves just the cap; it's a warning.
	going, _ := seatsUsed(db, occ.ID)
	if going > want {
		want = going
	}
	return want
}

func effectiveCapacity(ev *models.Event, occ *models.EventOccurrence) *int {
	if occ.CapacityOverride != nil {
		return occ.CapacityOverride
	}
	return ev.Capacity
}

func tooSmall(capacity *int, want int) bool { return capacity != nil && want > *capacity }

// apiDateRooms lists the rooms the caller may book for a date, marking busy
// and too-small ones, with the date's current booking.
func (a *App) apiDateRooms(c *gin.Context) {
	ev, occ, ok := a.managedOccurrence(c)
	if !ok {
		return
	}
	me := asPerson(apiUser(c))
	var rooms []models.Room
	if err := a.DB.Where("active").Order("lower(building), lower(name)").Find(&rooms).Error; err != nil {
		jsonError(c, http.StatusInternalServerError, "could not list rooms")
		return
	}
	var busy []uint
	if err := a.DB.Raw(`SELECT DISTINCT r.id FROM rooms r JOIN room_bookings b ON b.room_id = r.id
		WHERE b.status = ? AND b.occurrence_id <> ?
		AND b.padded_range && tstzrange(?::timestamptz - make_interval(mins => r.default_setup_minutes::int),
			?::timestamptz + make_interval(mins => r.default_teardown_minutes::int), '[)')`,
		models.BookingConfirmed, occ.ID, occ.StartsAt, occ.EndsAt).Scan(&busy).Error; err != nil {
		jsonError(c, http.StatusInternalServerError, "could not list rooms")
		return
	}
	isBusy := make(map[uint]bool, len(busy))
	for _, id := range busy {
		isBusy[id] = true
	}
	want := seatsWanted(a.DB, ev, occ)
	out := []roomPickerJSON{}
	for _, r := range rooms {
		if !policy.CanBookRoom(me, true, r.Restricted) {
			continue
		}
		out = append(out, roomPickerJSON{ID: r.ID, Name: r.Name, Building: r.Building, Capacity: r.Capacity,
			Features: r.Features, Restricted: r.Restricted, Busy: isBusy[r.ID], TooSmall: tooSmall(r.Capacity, want)})
	}
	booking, err := a.dateBooking(a.DB, occ.ID, want)
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not list rooms")
		return
	}
	c.JSON(http.StatusOK, gin.H{"rooms": out, "booking": booking})
}

// dateBooking is a date's booking for its managers, or nil.
func (a *App) dateBooking(db *gorm.DB, occurrenceID uint, want int) (*roomBookingJSON, error) {
	var b models.RoomBooking
	err := db.Where("occurrence_id = ?", occurrenceID).First(&b).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var r models.Room
	if err := db.First(&r, b.RoomID).Error; err != nil {
		return nil, err
	}
	pending := false
	if b.RequestID != nil && b.Status == models.BookingRequested {
		if err := db.Raw("SELECT EXISTS (SELECT 1 FROM room_requests WHERE id = ? AND status = ?)", *b.RequestID, models.RoomRequestPending).Scan(&pending).Error; err != nil {
			return nil, err
		}
	}
	return &roomBookingJSON{RoomID: r.ID, RoomName: r.Name, Building: r.Building, Status: b.Status,
		SetupMinutes: b.SetupMinutes, TeardownMinutes: b.TeardownMinutes, DoubleBooked: b.AllowOverlap,
		TooSmall: tooSmall(r.Capacity, want), Pending: pending}, nil
}

// apiSetDateRoom books, changes or releases a date's room. roomId null
// releases it. Padding comes from the room; an admin may override it for
// this booking (DEC-101). A conflict is 409 room_busy unless an admin gives
// an overlap reason, which double-books (audited).
func (a *App) apiSetDateRoom(c *gin.Context) {
	ev, occ, ok := a.managedOccurrence(c)
	if !ok {
		return
	}
	var body struct {
		RoomID          *uint  `json:"roomId"`
		OverlapReason   string `json:"overlapReason"`
		SetupMinutes    *int   `json:"setupMinutes"`
		TeardownMinutes *int   `json:"teardownMinutes"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		jsonError(c, http.StatusBadRequest, "invalid request")
		return
	}
	user := apiUser(c)
	me := asPerson(user)
	reason := strings.TrimSpace(body.OverlapReason)
	if utf8.RuneCountInString(reason) > maxOverlapReason {
		jsonError(c, http.StatusBadRequest, "the reason must be 500 characters or fewer")
		return
	}
	if (body.SetupMinutes != nil || body.TeardownMinutes != nil) && !policy.CanAdjustBookingPadding(me) {
		jsonError(c, http.StatusForbidden, "only an admin can change a booking's setup and cleanup time")
		return
	}
	for _, m := range []*int{body.SetupMinutes, body.TeardownMinutes} {
		if m != nil && (*m < 0 || *m > maxRoomPadding) {
			jsonError(c, http.StatusBadRequest, "setup and cleanup minutes must be from 0 to 240")
			return
		}
	}
	errRoomGone, errRestricted, errBusy := errors.New("room gone"), errors.New("restricted"), errors.New("busy")
	errDateGone, errAdminBooking := errors.New("date gone"), errors.New("admin booking")
	var emailed []uint
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		// Book against the date as it is under the lock: a reschedule or a
		// cancel that committed since it was read must win.
		locked, err := lockOccurrence(tx, occ.ID)
		if err != nil {
			return err
		}
		if locked.Status != models.OccurrenceScheduled {
			return errDateGone
		}
		occ = locked
		var old models.RoomBooking
		hadOld := tx.Where("occurrence_id = ?", occ.ID).First(&old).Error == nil
		if hadOld {
			// A booking only an admin could make (a restricted room, or a
			// double-booking) is only an admin's to change or release.
			if !policy.CanDoubleBookRoom(me) {
				var oldRoom models.Room
				if err := tx.First(&oldRoom, old.RoomID).Error; err != nil {
					return err
				}
				if oldRoom.Restricted || old.AllowOverlap {
					return errAdminBooking
				}
			}
			if err := tx.Delete(&old).Error; err != nil {
				return err
			}
			// The old booking may have been a request's last date.
			if err := closeEmptyRoomRequests(tx); err != nil {
				return err
			}
		}
		if body.RoomID == nil {
			if !hadOld {
				return nil
			}
			return audit.Write(tx, audit.Entry{Actor: &user.ID, Action: audit.RoomBookingReleased, TargetType: "occurrence", TargetID: occ.ID,
				Meta: map[string]any{"roomId": old.RoomID, "eventId": ev.ID, "date": occ.OriginalDate}, IP: c.ClientIP()})
		}
		lockedRoom, err := lockRoom(tx, *body.RoomID)
		if errors.Is(err, gorm.ErrRecordNotFound) || (err == nil && !lockedRoom.Active) {
			return errRoomGone
		}
		if err != nil {
			return err
		}
		room := *lockedRoom
		if !policy.CanBookRoom(me, true, room.Restricted) {
			return errRestricted
		}
		// The room's usual padding, unless this is the same room again: then
		// an admin's override on the booking is kept (DEC-101).
		setup, teardown := room.DefaultSetupMinutes, room.DefaultTeardownMinutes
		if hadOld && old.RoomID == room.ID {
			setup, teardown = old.SetupMinutes, old.TeardownMinutes
		}
		if body.SetupMinutes != nil {
			setup = *body.SetupMinutes
		}
		if body.TeardownMinutes != nil {
			teardown = *body.TeardownMinutes
		}
		if !policy.CanDoubleBookRoom(me) {
			reason = ""
		}
		// With approval on, a leader's or host's booking is a request of one
		// date for an admin (DEC-102, DEC-106).
		var req *models.RoomRequest
		var requestID *uint
		if policy.BookingNeedsApproval(me, roomApprovalOn(tx)) {
			if req, err = openRoomRequest(tx, ev.ID, room.ID, user.ID, false); err != nil {
				return err
			}
			requestID = &req.ID
		}
		result, err := placeBooking(tx, occ, &room, setup, teardown, placement{by: &user.ID, reason: reason, requestID: requestID})
		if err != nil {
			return err
		}
		if result == notBooked {
			return errBusy
		}
		if result == bookedPending {
			ids, err := notifyRoomRequested(tx, req, ev, &room, 1, user.ID)
			emailed = append(emailed, ids...)
			if err != nil {
				return err
			}
		}
		action := audit.RoomBookingBooked
		meta := map[string]any{"roomId": room.ID, "eventId": ev.ID, "date": occ.OriginalDate, "setupMinutes": setup, "teardownMinutes": teardown}
		if req != nil {
			meta["requestId"] = req.ID
		}
		if result == bookedOverlap {
			action = audit.RoomBookingOverlap
			meta["reason"] = reason
		}
		if hadOld {
			meta["previousRoomId"] = old.RoomID
		}
		return audit.Write(tx, audit.Entry{Actor: &user.ID, Action: action, TargetType: "occurrence", TargetID: occ.ID, Meta: meta, IP: c.ClientIP()})
	})
	switch {
	case errors.Is(err, errDateGone):
		jsonError(c, http.StatusNotFound, "that date is not on this event")
		return
	case errors.Is(err, errAdminBooking):
		jsonError(c, http.StatusForbidden, "only an admin can change this booking")
		return
	case errors.Is(err, errRoomGone):
		jsonError(c, http.StatusNotFound, "room not found")
		return
	case errors.Is(err, errRestricted):
		jsonError(c, http.StatusForbidden, "only an admin can book this room")
		return
	case errors.Is(err, errBusy):
		jsonError(c, http.StatusConflict, "that room is already booked then")
		return
	case err != nil:
		jsonError(c, http.StatusInternalServerError, "could not save the room")
		return
	}
	a.deliverNotificationEmails(emailed)
	booking, err := a.dateBooking(a.DB, occ.ID, seatsWanted(a.DB, ev, occ))
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not load the room")
		return
	}
	c.JSON(http.StatusOK, gin.H{"booking": booking})
}

// releaseRoomBookings frees the rooms of dates that were cancelled, in the
// cancelling transaction (9.2).
func releaseRoomBookings(tx *gorm.DB, occurrenceIDs []uint) error {
	if len(occurrenceIDs) == 0 {
		return nil
	}
	if err := tx.Where("occurrence_id IN ?", occurrenceIDs).Delete(&models.RoomBooking{}).Error; err != nil {
		return err
	}
	return closeEmptyRoomRequests(tx)
}

// moveRoomBookings moves the bookings of dates whose times changed, in the
// same transaction (5.3, 9.2). A confirmed booking that now conflicts stays
// as requested, holding nothing; the dates that lost their room are
// returned for settleDateRooms to announce.
func moveRoomBookings(tx *gorm.DB, occurrenceIDs []uint) ([]string, error) {
	if len(occurrenceIDs) == 0 {
		return nil, nil
	}
	var bookings []models.RoomBooking
	// Room order, so two edits moving dates between the same rooms take the
	// room locks in the same order and can't deadlock.
	if err := tx.Where("occurrence_id IN ?", occurrenceIDs).Order("room_id, id").Find(&bookings).Error; err != nil {
		return nil, err
	}
	var lost []string
	for _, b := range bookings {
		var occ models.EventOccurrence
		if err := tx.First(&occ, b.OccurrenceID).Error; err != nil {
			return nil, err
		}
		if occ.StartsAt.Equal(b.StartsAt) && occ.EndsAt.Equal(b.EndsAt) {
			continue
		}
		move := func(status string) error {
			return tx.Exec(`UPDATE room_bookings SET starts_at = ?, ends_at = ?, padded_range = `+paddedRangeSQL+`, status = ?, updated_at = now() WHERE id = ?`,
				occ.StartsAt, occ.EndsAt, occ.StartsAt, b.SetupMinutes, occ.EndsAt, b.TeardownMinutes, status, b.ID).Error
		}
		if _, err := lockRoom(tx, b.RoomID); err != nil {
			return nil, err
		}
		// A confirmed booking keeps its room only if neither a confirmed
		// booking (the constraint) nor an admin's double-booking holds it
		// at the new time. An admin's own double-booking moves as it is:
		// the admin chose to share that room.
		held := false
		if b.Status == models.BookingConfirmed && !b.AllowOverlap {
			var err error
			if held, err = heldByOverlap(tx, b.RoomID, occ.ID, occ.StartsAt, occ.EndsAt, b.SetupMinutes, b.TeardownMinutes); err != nil {
				return nil, err
			}
		}
		kept := false
		if !held {
			var err error
			if kept, err = trySavepoint(tx, fmt.Sprintf("move%d", b.ID), func() error { return move(b.Status) }); err != nil {
				return nil, err
			}
		}
		if !kept {
			if err := move(models.BookingRequested); err != nil {
				return nil, err
			}
			lost = append(lost, occ.OriginalDate)
		}
	}
	return lost, nil
}

// notifyRoomLost tells the event's leaders (or a church-wide event's hosts)
// which dates lost their room, in one notice (9.2). The person who moved
// the date is told too (no Actor): the move saved, and a lost room is easy
// to miss on a page that no longer shows it.
func notifyRoomLost(tx *gorm.DB, ev *models.Event, dates []string) ([]uint, error) {
	var to []uint
	if ev.GroupID != nil {
		to = leaderIDs(tx, *ev.GroupID)
	} else {
		to = hostIDs(tx, ev.ID)
	}
	change := "1 date"
	if len(dates) > 1 {
		change = fmt.Sprintf("%d dates", len(dates))
	}
	return notify.Send(tx, notify.Msg{
		Kind: notify.RoomLost, To: to,
		SubjectType: "event", SubjectID: ev.ID, Link: fmt.Sprintf("/events/%d", ev.ID),
		Vars: notify.Vars{Event: ev.Title, Change: change}, // group-ok: the event's own leaders and hosts
		Data: map[string]any{"eventId": ev.ID, "dates": dates},
	})
}

// placeLine is a date's place (PLAN 14.5 "Where rooms show"), as
// web/src/lib/place.ts writes it: the room and its building lead, and a
// typed location follows as a detail.
func placeLine(room *occurrenceRoomJSON, location string) string {
	if room == nil {
		return location
	}
	line := room.Name
	if room.Building != "" {
		line += " · " + room.Building
	}
	if typed := strings.TrimSpace(location); typed != "" {
		line += " · " + typed
	}
	return line
}

// occurrenceRooms maps dates to their confirmed booking's room, for showing
// the room as the place (9.2). Empty while rooms are off. One read: the
// rooms switch is checked inside the same query, so a list of dates pays
// exactly one read for rooms whether they are on or off. A page shows its
// dates without rooms if the read fails; feeds use roomsFor and fail.
func (a *App) occurrenceRooms(db *gorm.DB, occurrenceIDs []uint) map[uint]*occurrenceRoomJSON {
	if len(occurrenceIDs) == 0 {
		return map[uint]*occurrenceRoomJSON{}
	}
	out, _ := roomsFor(db, occurrenceIDs)
	return out
}

// roomsChunk keeps one rooms lookup under the Postgres parameter limit.
const roomsChunk = 10000

// roomsFor is occurrenceRooms, reporting a failed read. A feed's dates come
// in chunks of 10,000, so a list of dates under that pays one read.
func roomsFor(db *gorm.DB, occurrenceIDs []uint) (map[uint]*occurrenceRoomJSON, error) {
	out := map[uint]*occurrenceRoomJSON{}
	for len(occurrenceIDs) > 0 {
		n := min(len(occurrenceIDs), roomsChunk)
		if err := roomsChunkInto(db, occurrenceIDs[:n], out); err != nil {
			return out, err
		}
		occurrenceIDs = occurrenceIDs[n:]
	}
	return out, nil
}

func roomsChunkInto(db *gorm.DB, occurrenceIDs []uint, out map[uint]*occurrenceRoomJSON) error {
	var rows []struct {
		OccurrenceID uint
		ID           uint
		Name         string
		Building     string
	}
	err := db.Raw(`SELECT b.occurrence_id, r.id, r.name, r.building FROM room_bookings b JOIN rooms r ON r.id = b.room_id
		WHERE b.occurrence_id IN ? AND b.status = ?
		AND EXISTS (SELECT 1 FROM settings WHERE key = ? AND value = 'true')`, occurrenceIDs, models.BookingConfirmed, settingRoomsEnabled).Scan(&rows).Error
	for _, r := range rows {
		out[r.OccurrenceID] = &occurrenceRoomJSON{ID: r.ID, Name: r.Name, Building: r.Building}
	}
	return err
}
