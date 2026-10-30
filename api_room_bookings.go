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
	var going int
	db.Raw(`SELECT COALESCE(SUM(1 + guest_count), 0) FROM registrations WHERE occurrence_id = ? AND status = ?`,
		occ.ID, models.RegGoing).Scan(&going)
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
	return &roomBookingJSON{RoomID: r.ID, RoomName: r.Name, Building: r.Building, Status: b.Status,
		SetupMinutes: b.SetupMinutes, TeardownMinutes: b.TeardownMinutes, DoubleBooked: b.AllowOverlap,
		TooSmall: tooSmall(r.Capacity, want)}, nil
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
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		if _, err := lockOccurrence(tx, occ.ID); err != nil {
			return err
		}
		var old models.RoomBooking
		hadOld := tx.Where("occurrence_id = ?", occ.ID).First(&old).Error == nil
		if hadOld {
			if err := tx.Delete(&old).Error; err != nil {
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
		var room models.Room
		if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).Where("id = ? AND active", *body.RoomID).First(&room).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errRoomGone
			}
			return err
		}
		if !policy.CanBookRoom(me, true, room.Restricted) {
			return errRestricted
		}
		setup, teardown := room.DefaultSetupMinutes, room.DefaultTeardownMinutes
		if body.SetupMinutes != nil {
			setup = *body.SetupMinutes
		}
		if body.TeardownMinutes != nil {
			teardown = *body.TeardownMinutes
		}
		insert := func(overlap bool, why string) error {
			return tx.Exec(`INSERT INTO room_bookings (occurrence_id, room_id, starts_at, ends_at, setup_minutes, teardown_minutes,
				padded_range, status, allow_overlap, overlap_reason, created_by) VALUES (?, ?, ?, ?, ?, ?, `+paddedRangeSQL+`, ?, ?, ?, ?)`,
				occ.ID, room.ID, occ.StartsAt, occ.EndsAt, setup, teardown,
				occ.StartsAt, setup, occ.EndsAt, teardown,
				models.BookingConfirmed, overlap, why, user.ID).Error
		}
		// An admin's double-booking sits outside the exclusion constraint,
		// so it is checked here too: no one books over it without a reason.
		var heldByOverlap bool
		if err := tx.Raw(`SELECT EXISTS (SELECT 1 FROM room_bookings WHERE room_id = ? AND status = ? AND allow_overlap
			AND occurrence_id <> ? AND padded_range && `+paddedRangeSQL+`)`,
			room.ID, models.BookingConfirmed, occ.ID, occ.StartsAt, setup, occ.EndsAt, teardown).Scan(&heldByOverlap).Error; err != nil {
			return err
		}
		tx.SavePoint("book")
		overlap := false
		err := errBusy
		if !heldByOverlap {
			err = insert(false, "")
		}
		if err != nil {
			if err != errBusy && !isOverlap(err) {
				return err
			}
			if err != errBusy {
				tx.RollbackTo("book")
			}
			if reason == "" || !policy.CanDoubleBookRoom(me) {
				return errBusy
			}
			if err := insert(true, reason); err != nil {
				return err
			}
			overlap = true
		}
		action := audit.RoomBookingBooked
		meta := map[string]any{"roomId": room.ID, "eventId": ev.ID, "date": occ.OriginalDate, "setupMinutes": setup, "teardownMinutes": teardown}
		if overlap {
			action = audit.RoomBookingOverlap
			meta["reason"] = reason
		}
		if hadOld {
			meta["previousRoomId"] = old.RoomID
		}
		return audit.Write(tx, audit.Entry{Actor: &user.ID, Action: action, TargetType: "occurrence", TargetID: occ.ID, Meta: meta, IP: c.ClientIP()})
	})
	switch {
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
	return tx.Where("occurrence_id IN ?", occurrenceIDs).Delete(&models.RoomBooking{}).Error
}

// moveRoomBookings moves the bookings of dates whose times changed, in the
// same transaction (5.3, 9.2). A confirmed booking that now conflicts stays
// as requested, holding nothing, and the event's leaders or hosts get one
// notice for all its lost dates. Notification ids to email are returned.
func moveRoomBookings(tx *gorm.DB, ev *models.Event, occurrenceIDs []uint, actor uint) ([]uint, error) {
	if len(occurrenceIDs) == 0 {
		return nil, nil
	}
	var bookings []models.RoomBooking
	if err := tx.Where("occurrence_id IN ?", occurrenceIDs).Order("id").Find(&bookings).Error; err != nil {
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
		savepoint := fmt.Sprintf("move%d", b.ID)
		tx.SavePoint(savepoint)
		if err := move(b.Status); err != nil {
			if !isOverlap(err) {
				return nil, err
			}
			tx.RollbackTo(savepoint)
			if err := move(models.BookingRequested); err != nil {
				return nil, err
			}
			lost = append(lost, occ.OriginalDate)
		}
	}
	if len(lost) == 0 {
		return nil, nil
	}
	return notifyRoomLost(tx, ev, lost, actor)
}

// notifyRoomLost tells the event's leaders (or a church-wide event's hosts)
// which dates lost their room, in one notice (9.2).
func notifyRoomLost(tx *gorm.DB, ev *models.Event, dates []string, actor uint) ([]uint, error) {
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
		Kind: notify.RoomLost, To: to, Actor: &actor,
		SubjectType: "event", SubjectID: ev.ID, Link: fmt.Sprintf("/events/%d", ev.ID),
		Vars: notify.Vars{Event: ev.Title, Change: change}, // group-ok: the event's own leaders and hosts
		Data: map[string]any{"eventId": ev.ID, "dates": dates},
	})
}

// occurrenceRooms maps dates to their confirmed booking's room, for showing
// the room as the place (9.2). Empty while rooms are off.
func (a *App) occurrenceRooms(db *gorm.DB, occurrenceIDs []uint) map[uint]*occurrenceRoomJSON {
	out := map[uint]*occurrenceRoomJSON{}
	if len(occurrenceIDs) == 0 || !a.roomsEnabled() {
		return out
	}
	var rows []struct {
		OccurrenceID uint
		ID           uint
		Name         string
		Building     string
	}
	db.Raw(`SELECT b.occurrence_id, r.id, r.name, r.building FROM room_bookings b JOIN rooms r ON r.id = b.room_id
		WHERE b.occurrence_id IN ? AND b.status = ?`, occurrenceIDs, models.BookingConfirmed).Scan(&rows)
	for _, r := range rows {
		out[r.OccurrenceID] = &occurrenceRoomJSON{ID: r.ID, Name: r.Name, Building: r.Building}
	}
	return out
}
