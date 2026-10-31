package main

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"fold/internal/audit"
	"fold/internal/notify"
	"fold/internal/policy"
	"fold/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ---------------------------------------------------------------------------
// Room booking approval (M2.1d, DEC-102, DEC-105, DEC-106). With the
// room_bookings_need_approval setting on, a leader's or host's booking is a
// request: its free dates are requested bookings (holding nothing) that
// point at one room_requests row, and every admin gets one task. An admin
// confirms it (a reason double-books dates that became busy) or rejects it.
// Dates that lost their room are not requests: they stay the leader's.
// ---------------------------------------------------------------------------

const settingRoomApproval = "room_bookings_need_approval"

// roomsRequestsLink is the admin Rooms tab, where the requests list is.
const roomRequestsLink = "/admin?tab=rooms"

// roomApprovalOn reads the setting in the caller's transaction.
func roomApprovalOn(db *gorm.DB) bool {
	var s models.Setting
	return db.Where("key = ?", settingRoomApproval).First(&s).Error == nil && s.Value == "true"
}

// apiSetRoomApproval turns "Room bookings need an admin's approval" on or
// off (DEC-102). Turning it off leaves open requests for admins to decide.
func (a *App) apiSetRoomApproval(c *gin.Context) {
	var body struct {
		Enabled *bool `json:"enabled"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.Enabled == nil {
		jsonError(c, http.StatusBadRequest, "invalid request")
		return
	}
	me := apiUser(c)
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where(models.Setting{Key: settingRoomApproval}).
			Assign(models.Setting{Value: strconv.FormatBool(*body.Enabled)}).
			FirstOrCreate(&models.Setting{}).Error; err != nil {
			return err
		}
		return audit.Write(tx, audit.Entry{Actor: &me.ID, Action: audit.RoomApprovalChanged,
			TargetType: "setting", Meta: map[string]any{"enabled": *body.Enabled}, IP: c.ClientIP()})
	})
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not save the setting")
		return
	}
	c.JSON(http.StatusOK, gin.H{"needApproval": *body.Enabled})
}

// openRoomRequest starts a request; the caller places its dates.
func openRoomRequest(tx *gorm.DB, eventID, roomID uint, by *uint, series bool) (*models.RoomRequest, error) {
	req := models.RoomRequest{EventID: eventID, RoomID: roomID, Series: series, RequestedBy: by, Status: models.RoomRequestPending}
	return &req, tx.Create(&req).Error
}

// notifyRoomRequested gives every admin the request as a task.
func notifyRoomRequested(tx *gorm.DB, req *models.RoomRequest, ev *models.Event, room *models.Room, dates int, actor *uint) ([]uint, error) {
	change := room.Name + " for 1 date"
	if dates != 1 {
		change = fmt.Sprintf("%s for %d dates", room.Name, dates)
	}
	return notify.Send(tx, notify.Msg{
		Kind: notify.RoomRequested, To: adminIDs(tx), Actor: actor,
		SubjectType: "room_request", SubjectID: req.ID, Link: roomRequestsLink,
		Vars:      notify.Vars{Actor: askerName(tx, actor), Event: ev.Title, Change: change}, // group-ok: admin tasks go to admins
		Data:      map[string]any{"requestId": req.ID, "eventId": ev.ID},
		DedupeKey: fmt.Sprintf("room.requested:%d", req.ID),
	})
}

func askerName(tx *gorm.DB, id *uint) string {
	if id == nil {
		return ""
	}
	return nameIn(tx, *id)
}

// closeEmptyRoomRequests withdraws pending requests none of whose dates
// still wait (re-picked, released or cancelled), and closes their tasks.
func closeEmptyRoomRequests(tx *gorm.DB) error {
	var ids []uint
	// SKIP LOCKED: a request an admin is deciding right now is locked by
	// that decision, which settles it anyway; waiting on it here could
	// deadlock against the decision waiting on this caller's dates.
	if err := tx.Raw(`UPDATE room_requests SET status = ?, updated_at = now() WHERE id IN (
			SELECT q.id FROM room_requests q WHERE q.status = ?
			AND NOT EXISTS (SELECT 1 FROM room_bookings b WHERE b.request_id = q.id AND b.status = ?)
			FOR UPDATE SKIP LOCKED) RETURNING id`,
		models.RoomRequestWithdrawn, models.RoomRequestPending, models.BookingRequested).Scan(&ids).Error; err != nil {
		return err
	}
	for _, id := range ids {
		if _, err := notify.Resolve(tx, notify.RoomRequested, id, nil, models.RoomRequestWithdrawn); err != nil {
			return err
		}
	}
	return nil
}

// roomRequestJSON is one open request in the admin list.
type roomRequestJSON struct {
	ID          uint   `json:"id"`
	EventID     uint   `json:"eventId"`
	EventTitle  string `json:"eventTitle"`
	GroupName   string `json:"groupName"`
	RoomID      uint   `json:"roomId"`
	RoomName    string `json:"roomName"`
	Building    string `json:"building"`
	Series      bool   `json:"series"`
	RequestedBy string `json:"requestedBy"`
	// Dates waiting, and how many of them another booking now holds the
	// room on (confirming those needs a double-book reason).
	Dates     int    `json:"dates"`
	BusyDates int    `json:"busyDates"`
	FirstDate string `json:"firstDate"`
	// FirstStartsAt: when the first waiting date starts (UTC).
	FirstStartsAt time.Time `json:"firstStartsAt"`
	CreatedAt     time.Time `json:"createdAt"`
}

// apiAdminRoomRequests lists the open requests, oldest first.
func (a *App) apiAdminRoomRequests(c *gin.Context) {
	var rows []struct {
		ID, EventID, RoomID                       uint
		EventTitle, GroupName, RoomName, Building string
		Series                                    bool
		RequestedByID                             *uint
		Dates, BusyDates                          int
		FirstDate                                 string
		FirstStartsAt                             time.Time
		CreatedAt                                 time.Time
	}
	if err := a.DB.Raw(`SELECT q.id, q.event_id, e.title AS event_title, coalesce(g.name, '') AS group_name,
			q.room_id, r.name AS room_name, r.building, q.series, q.requested_by AS requested_by_id, q.created_at,
			count(*) AS dates, min(o.original_date) AS first_date, min(o.starts_at) AS first_starts_at,
			count(*) FILTER (WHERE EXISTS (SELECT 1 FROM room_bookings c WHERE c.room_id = b.room_id AND c.status = 'confirmed'
				AND c.starts_at < b.ends_at + interval '480 minutes' AND c.starts_at > b.starts_at - interval '8 days' -- an event lasts at most 7 days (events.MaxDurationMinutes), padding 4 hours
				AND c.occurrence_id <> b.occurrence_id AND c.padded_range && b.padded_range)) AS busy_dates
		FROM room_requests q
		JOIN events e ON e.id = q.event_id
		LEFT JOIN groups g ON g.id = e.group_id
		JOIN rooms r ON r.id = q.room_id
		JOIN room_bookings b ON b.request_id = q.id AND b.status = ?
		JOIN event_occurrences o ON o.id = b.occurrence_id
		WHERE q.status = ?
		GROUP BY q.id, e.title, g.name, r.name, r.building
		ORDER BY q.created_at, q.id`, models.BookingRequested, models.RoomRequestPending).Scan(&rows).Error; err != nil {
		jsonError(c, http.StatusInternalServerError, "could not list room requests")
		return
	}
	out := make([]roomRequestJSON, 0, len(rows))
	for _, r := range rows {
		out = append(out, roomRequestJSON{ID: r.ID, EventID: r.EventID, EventTitle: r.EventTitle,
			GroupName: r.GroupName, // group-ok: admins see every group
			RoomID:    r.RoomID, RoomName: r.RoomName, Building: r.Building, Series: r.Series,
			RequestedBy: a.actorName(r.RequestedByID), Dates: r.Dates, BusyDates: r.BusyDates,
			FirstDate: r.FirstDate, FirstStartsAt: r.FirstStartsAt, CreatedAt: r.CreatedAt})
	}
	c.JSON(http.StatusOK, gin.H{"requests": out})
}

var errRequestDecided = errors.New("request decided")

// lockRequest loads a pending request and its event under their locks,
// event first: the same order as every other booking write.
func lockRequest(tx *gorm.DB, id uint) (*models.RoomRequest, *models.Event, error) {
	var peek models.RoomRequest
	if err := tx.First(&peek, id).Error; err != nil {
		return nil, nil, err
	}
	var ev models.Event
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&ev, peek.EventID).Error; err != nil {
		return nil, nil, err
	}
	var req models.RoomRequest
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&req, id).Error; err != nil {
		return nil, nil, err
	}
	if req.Status != models.RoomRequestPending {
		return nil, nil, errRequestDecided
	}
	return &req, &ev, nil
}

// requestBookings locks a request's waiting dates (dates, then room) and
// returns their bookings in date-id order.
func requestBookings(tx *gorm.DB, req *models.RoomRequest) ([]models.RoomBooking, map[uint]models.EventOccurrence, error) {
	var occs []models.EventOccurrence
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id IN (SELECT occurrence_id FROM room_bookings WHERE request_id = ? AND status = ?)", req.ID, models.BookingRequested).
		Order("id").Find(&occs).Error; err != nil {
		return nil, nil, err
	}
	byID := make(map[uint]models.EventOccurrence, len(occs))
	for _, o := range occs {
		byID[o.ID] = o
	}
	if _, err := lockRoom(tx, req.RoomID); err != nil {
		return nil, nil, err
	}
	var bookings []models.RoomBooking
	err := tx.Where("request_id = ? AND status = ?", req.ID, models.BookingRequested).Order("occurrence_id").Find(&bookings).Error
	return bookings, byID, err
}

func requestID(c *gin.Context) (uint, bool) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 63)
	if err != nil || id == 0 {
		jsonError(c, http.StatusNotFound, "room request not found")
		return 0, false
	}
	return uint(id), true
}

// apiConfirmRoomRequest confirms a request's dates. A date another booking
// now holds the room on is double-booked with the admin's reason, or, with
// no reason, goes back to its leaders as a date that lost its room.
func (a *App) apiConfirmRoomRequest(c *gin.Context) {
	id, ok := requestID(c)
	if !ok {
		return
	}
	var body struct {
		OverlapReason string `json:"overlapReason"`
	}
	// The body is optional.
	if err := c.ShouldBindJSON(&body); err != nil && !errors.Is(err, io.EOF) {
		jsonError(c, http.StatusBadRequest, "invalid request")
		return
	}
	reason := strings.TrimSpace(body.OverlapReason)
	if utf8.RuneCountInString(reason) > maxOverlapReason {
		jsonError(c, http.StatusBadRequest, "the reason must be 500 characters or fewer")
		return
	}
	me := apiUser(c)
	if !policy.CanDecideRoomRequest(asPerson(me)) {
		jsonError(c, http.StatusForbidden, "admin required")
		return
	}
	var confirmed, overlapped int
	lost := []string{}
	var emailed []uint
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		req, ev, err := lockRequest(tx, id)
		if err != nil {
			return err
		}
		bookings, occs, err := requestBookings(tx, req)
		if err != nil {
			return err
		}
		for _, b := range bookings {
			occ := occs[b.OccurrenceID]
			held, err := heldByOverlap(tx, b.RoomID, b.OccurrenceID, occ.StartsAt, occ.EndsAt, b.SetupMinutes, b.TeardownMinutes)
			if err != nil {
				return err
			}
			free := false
			if !held {
				if free, err = trySavepoint(tx, fmt.Sprintf("confirm%d", b.ID), func() error {
					return tx.Exec(`UPDATE room_bookings SET status = ?, updated_at = now() WHERE id = ?`, models.BookingConfirmed, b.ID).Error
				}); err != nil {
					return err
				}
			}
			switch {
			case free:
				confirmed++
			case reason != "":
				if err := tx.Exec(`UPDATE room_bookings SET status = ?, allow_overlap = true, overlap_reason = ?, updated_at = now() WHERE id = ?`,
					models.BookingConfirmed, reason, b.ID).Error; err != nil {
					return err
				}
				overlapped++
			default:
				// Busy and no reason: the date lost its room, the leader's to re-pick.
				if err := tx.Exec(`UPDATE room_bookings SET request_id = NULL, updated_at = now() WHERE id = ?`, b.ID).Error; err != nil {
					return err
				}
				lost = append(lost, occ.OriginalDate)
			}
		}
		if req.Series && ev.DefaultRoomID != nil && *ev.DefaultRoomID == req.RoomID {
			// The series room is approved: its new dates confirm directly.
			if err := tx.Model(ev).Update("default_room_pending", false).Error; err != nil {
				return err
			}
		}
		now := time.Now().UTC()
		if err := tx.Model(req).Updates(map[string]any{"status": models.RoomRequestConfirmed, "decided_by": me.ID, "decided_at": now, "updated_at": now}).Error; err != nil {
			return err
		}
		meta := map[string]any{"eventId": ev.ID, "roomId": req.RoomID, "confirmed": confirmed, "overlapped": overlapped, "lost": lost}
		if reason != "" {
			meta["reason"] = reason
		}
		if err := audit.Write(tx, audit.Entry{Actor: &me.ID, Action: audit.RoomRequestConfirmed, TargetType: "room_request", TargetID: req.ID,
			Meta: meta, IP: c.ClientIP()}); err != nil {
			return err
		}
		if _, err := notify.Resolve(tx, notify.RoomRequested, req.ID, &me.ID, "confirmed"); err != nil {
			return err
		}
		var room models.Room
		if err := tx.First(&room, req.RoomID).Error; err != nil {
			return err
		}
		note := fmt.Sprintf("%s is booked for %s.", room.Name, dateCount(confirmed+overlapped))
		if len(lost) > 0 {
			note += fmt.Sprintf(" %s busy now: pick another room for %s on the event page.", isAre(len(lost)), themIt(len(lost)))
		}
		emailed, err = notifyRoomDecided(tx, ev, me.ID, "approved", note)
		return err
	})
	if a.requestFailed(c, id, err) {
		return
	}
	a.deliverNotificationEmails(emailed)
	c.JSON(http.StatusOK, gin.H{"confirmed": confirmed, "overlapped": overlapped, "lost": lost})
}

// apiRejectRoomRequest declines a request: its dates hold nothing, show as
// not approved, and a series request stops being the series room.
func (a *App) apiRejectRoomRequest(c *gin.Context) {
	id, ok := requestID(c)
	if !ok {
		return
	}
	var body struct {
		Reason string `json:"reason"`
	}
	// The body is optional.
	if err := c.ShouldBindJSON(&body); err != nil && !errors.Is(err, io.EOF) {
		jsonError(c, http.StatusBadRequest, "invalid request")
		return
	}
	reason := strings.TrimSpace(body.Reason)
	if utf8.RuneCountInString(reason) > maxOverlapReason {
		jsonError(c, http.StatusBadRequest, "the reason must be 500 characters or fewer")
		return
	}
	me := apiUser(c)
	if !policy.CanDecideRoomRequest(asPerson(me)) {
		jsonError(c, http.StatusForbidden, "admin required")
		return
	}
	var emailed []uint
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		req, ev, err := lockRequest(tx, id)
		if err != nil {
			return err
		}
		if _, _, err := requestBookings(tx, req); err != nil {
			return err
		}
		res := tx.Exec(`UPDATE room_bookings SET status = ?, updated_at = now() WHERE request_id = ? AND status = ?`,
			models.BookingRejected, req.ID, models.BookingRequested)
		if res.Error != nil {
			return res.Error
		}
		if req.Series && ev.DefaultRoomID != nil && *ev.DefaultRoomID == req.RoomID {
			if err := tx.Model(ev).Updates(map[string]any{"default_room_id": nil, "default_room_pending": false}).Error; err != nil {
				return err
			}
		}
		now := time.Now().UTC()
		if err := tx.Model(req).Updates(map[string]any{"status": models.RoomRequestRejected, "decided_by": me.ID, "decided_at": now,
			"reject_reason": reason, "updated_at": now}).Error; err != nil {
			return err
		}
		if err := audit.Write(tx, audit.Entry{Actor: &me.ID, Action: audit.RoomRequestRejected, TargetType: "room_request", TargetID: req.ID,
			Meta: map[string]any{"eventId": ev.ID, "roomId": req.RoomID, "dates": res.RowsAffected, "reason": reason}, IP: c.ClientIP()}); err != nil {
			return err
		}
		if _, err := notify.Resolve(tx, notify.RoomRequested, req.ID, &me.ID, "rejected"); err != nil {
			return err
		}
		var room models.Room
		if err := tx.First(&room, req.RoomID).Error; err != nil {
			return err
		}
		note := room.Name + " wasn't approved. Pick another room on the event page."
		if reason != "" {
			note = room.Name + " wasn't approved: " + reason + " Pick another room on the event page."
		}
		emailed, err = notifyRoomDecided(tx, ev, me.ID, "not approved", note)
		return err
	})
	if a.requestFailed(c, id, err) {
		return
	}
	a.deliverNotificationEmails(emailed)
	c.JSON(http.StatusOK, gin.H{})
}

// requestFailed answers a confirm or reject that didn't go through.
func (a *App) requestFailed(c *gin.Context, id uint, err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, gorm.ErrRecordNotFound):
		jsonError(c, http.StatusNotFound, "room request not found")
	case errors.Is(err, errRequestDecided):
		a.alreadyHandled(c, notify.RoomRequested, id)
	default:
		jsonError(c, http.StatusInternalServerError, "could not save the room request")
	}
	return true
}

// notifyRoomDecided tells the event's leaders (or hosts) what an admin
// decided; the admin is the actor and isn't told.
func notifyRoomDecided(tx *gorm.DB, ev *models.Event, actor uint, change, note string) ([]uint, error) {
	var to []uint
	if ev.GroupID != nil {
		to = leaderIDs(tx, *ev.GroupID)
	} else {
		to = hostIDs(tx, ev.ID)
	}
	return notify.Send(tx, notify.Msg{
		Kind: notify.RoomRequestDecided, To: to, Actor: &actor,
		SubjectType: "event", SubjectID: ev.ID, Link: fmt.Sprintf("/events/%d", ev.ID),
		Vars: notify.Vars{Event: ev.Title, Change: change, Note: note}, // group-ok: the event's own leaders and hosts
		Data: map[string]any{"eventId": ev.ID},
	})
}

func dateCount(n int) string {
	if n == 1 {
		return "1 date"
	}
	return fmt.Sprintf("%d dates", n)
}

func isAre(n int) string {
	if n == 1 {
		return "1 date is"
	}
	return fmt.Sprintf("%d dates are", n)
}

func themIt(n int) string {
	if n == 1 {
		return "it"
	}
	return "them"
}
