package main

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"fold/internal/audit"
	"fold/internal/events"
	"fold/internal/policy"
	"fold/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Room feeds (M2.2b, 8.2): an admin-issued secret link per room that
// exports its confirmed bookings, so the church office or a calendar on
// the door can follow the room. Only admins make or turn off the link
// (section 6). The feed shows what an admin would see, except that a date
// whose own visibility is invite-only is titled "Reserved" with nothing
// that says whose it is.

// roomFeedTitle names a booking the feed doesn't describe.
const roomFeedTitle = "Reserved"

type roomFeedJSON struct {
	HasToken  bool       `json:"hasToken"`
	CreatedAt *time.Time `json:"createdAt"`
	// URL is the raw link, only in the response that made it.
	URL string `json:"url,omitempty"`
}

func (a *App) roomFeedURL(roomID uint, raw string) string {
	return a.emailBaseURL() + "/cal/rooms/" + strconv.FormatUint(uint64(roomID), 10) + "/" + raw + ".ics"
}

// adminRoom loads the room named in the path, or answers 404.
func (a *App) adminRoom(c *gin.Context) (*models.Room, bool) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 63)
	if err != nil {
		jsonError(c, http.StatusNotFound, "room not found")
		return nil, false
	}
	var room models.Room
	if err := a.DB.First(&room, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			jsonError(c, http.StatusNotFound, "room not found")
		} else {
			jsonError(c, http.StatusInternalServerError, "could not load the room")
		}
		return nil, false
	}
	return &room, true
}

func activeRoomFeed(db *gorm.DB, roomID uint) (*models.RoomFeedToken, error) {
	var tok models.RoomFeedToken
	err := db.Where("room_id = ? AND revoked_at IS NULL", roomID).First(&tok).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &tok, err
}

func roomFeedOf(tok *models.RoomFeedToken) roomFeedJSON {
	if tok == nil {
		return roomFeedJSON{}
	}
	at := tok.CreatedAt
	return roomFeedJSON{HasToken: true, CreatedAt: &at}
}

func (a *App) apiAdminGetRoomFeed(c *gin.Context) {
	room, ok := a.adminRoom(c)
	if !ok {
		return
	}
	tok, err := activeRoomFeed(a.DB, room.ID)
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not load the room's calendar link")
		return
	}
	c.JSON(http.StatusOK, gin.H{"feed": roomFeedOf(tok)})
}

// apiAdminCreateRoomFeed makes the room's link, or a new one, which turns
// the old one off. The raw token is in this response only.
func (a *App) apiAdminCreateRoomFeed(c *gin.Context) {
	room, ok := a.adminRoom(c)
	if !ok {
		return
	}
	if !room.Active {
		jsonError(c, http.StatusNotFound, "room not found")
		return
	}
	raw, err := newRawToken()
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not make the calendar link")
		return
	}
	me := apiUser(c)
	tok := models.RoomFeedToken{RoomID: room.ID, TokenHash: hashToken(raw), CreatedBy: &me.ID}
	err = a.DB.Transaction(func(tx *gorm.DB) error {
		// The room row serializes two admins making a link at once.
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&models.Room{}, room.ID).Error; err != nil {
			return err
		}
		res := tx.Model(&models.RoomFeedToken{}).Where("room_id = ? AND revoked_at IS NULL", room.ID).Update("revoked_at", time.Now().UTC())
		if res.Error != nil {
			return res.Error
		}
		if err := tx.Create(&tok).Error; err != nil {
			return err
		}
		return audit.Write(tx, audit.Entry{Actor: &me.ID, Action: audit.RoomFeedCreated, TargetType: "room", TargetID: room.ID,
			Meta: map[string]any{"replaced": res.RowsAffected > 0}, IP: c.ClientIP()})
	})
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not make the calendar link")
		return
	}
	out := roomFeedOf(&tok)
	out.URL = a.roomFeedURL(room.ID, raw)
	c.JSON(http.StatusOK, gin.H{"feed": out})
}

func (a *App) apiAdminRevokeRoomFeed(c *gin.Context) {
	room, ok := a.adminRoom(c)
	if !ok {
		return
	}
	me := apiUser(c)
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&models.RoomFeedToken{}).Where("room_id = ? AND revoked_at IS NULL", room.ID).Update("revoked_at", time.Now().UTC())
		if res.Error != nil || res.RowsAffected == 0 {
			return res.Error
		}
		return audit.Write(tx, audit.Entry{Actor: &me.ID, Action: audit.RoomFeedRevoked, TargetType: "room", TargetID: room.ID, IP: c.ClientIP()})
	})
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not turn off the calendar link")
		return
	}
	c.JSON(http.StatusOK, gin.H{"feed": roomFeedJSON{}})
}

// feedRoom serves /cal/rooms/<roomId>/<token>.ics. Every failure that
// isn't the server's is the same plain 404: rooms off, a wrong, revoked or
// other room's token, or a room that was deactivated.
func (a *App) feedRoom(c *gin.Context) {
	param := c.Param("token")
	roomID, err := strconv.ParseUint(c.Param("roomId"), 10, 63)
	if err != nil || !strings.HasSuffix(param, ".ics") || !a.roomsEnabled() {
		feedNotFound(c)
		return
	}
	var tok models.RoomFeedToken
	if err := a.DB.Where("token_hash = ? AND revoked_at IS NULL", hashToken(strings.TrimSuffix(param, ".ics"))).First(&tok).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			feedNotFound(c)
		} else {
			feedUnavailable(c, err)
		}
		return
	}
	var room models.Room
	if err := a.DB.First(&room, tok.RoomID).Error; err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		feedUnavailable(c, err)
		return
	}
	if uint64(tok.RoomID) != roomID || room.ID == 0 || !room.Active {
		feedNotFound(c)
		return
	}
	feed, err := a.buildRoomFeed(&room, time.Now().UTC())
	if err != nil {
		feedUnavailable(c, err)
		return
	}
	serveFeed(c, events.RenderFeed(feed))
}

// buildRoomFeed lists the room's confirmed bookings whose date starts in
// the feed window. Times are the event's, without setup and teardown.
func (a *App) buildRoomFeed(room *models.Room, now time.Time) (events.Feed, error) {
	windowStart, windowEnd := feedWindow(now)
	roomLine := placeLine(&occurrenceRoomJSON{ID: room.ID, Name: room.Name, Building: room.Building}, "")
	host := a.feedHost()
	feed := events.Feed{Name: roomLine, Host: host, WindowStart: windowStart, WindowEnd: windowEnd, Singles: []events.FeedSingle{}}
	tz := a.churchTimezone()

	var bookings []models.RoomBooking
	if err := a.DB.Joins("JOIN event_occurrences o ON o.id = room_bookings.occurrence_id").
		Where("room_bookings.room_id = ? AND room_bookings.status = ? AND o.status = ? AND o.starts_at >= ? AND o.starts_at < ?",
			room.ID, models.BookingConfirmed, models.OccurrenceScheduled, windowStart, windowEnd).
		Order("room_bookings.starts_at, room_bookings.id").Find(&bookings).Error; err != nil {
		return events.Feed{}, err
	}
	if len(bookings) == 0 {
		return feed, nil
	}
	occIDs := make([]uint, 0, len(bookings))
	for _, b := range bookings {
		occIDs = append(occIDs, b.OccurrenceID)
	}
	occByID := map[uint]*models.EventOccurrence{}
	evByID := map[uint]*models.Event{}
	groups := map[uint]*models.Group{}
	if err := eachIDChunk(occIDs, func(chunk []uint) error {
		var occs []models.EventOccurrence
		if err := a.DB.Where("id IN ?", chunk).Find(&occs).Error; err != nil {
			return err
		}
		evIDs, gIDs := []uint{}, []uint{}
		for i := range occs {
			occByID[occs[i].ID] = &occs[i]
			if evByID[occs[i].EventID] == nil {
				evIDs = append(evIDs, occs[i].EventID)
			}
		}
		var evs []models.Event
		if err := a.DB.Where("id IN ?", evIDs).Find(&evs).Error; err != nil {
			return err
		}
		for i := range evs {
			evByID[evs[i].ID] = &evs[i]
			if g := evs[i].GroupID; g != nil && groups[*g] == nil {
				gIDs = append(gIDs, *g)
			}
		}
		// Soft-deleted groups aren't loaded, so their dates are Reserved.
		var gs []models.Group
		if err := a.DB.Where("id IN ?", gIDs).Find(&gs).Error; err != nil {
			return err
		}
		for i := range gs {
			groups[gs[i].ID] = &gs[i]
		}
		return nil
	}); err != nil {
		return events.Feed{}, err
	}

	for _, b := range bookings {
		occ := occByID[b.OccurrenceID]
		if occ == nil {
			continue
		}
		ev := evByID[occ.EventID]
		single := events.FeedSingle{
			UID:   "room-" + strconv.FormatUint(uint64(room.ID), 10) + "-booking-" + strconv.FormatUint(uint64(b.ID), 10) + "@" + host,
			Title: roomFeedTitle, Location: roomLine, TZID: tz,
			StartsAt: b.StartsAt, EndsAt: b.EndsAt, UpdatedAt: b.UpdatedAt, Status: "CONFIRMED",
		}
		if ev != nil && roomFeedDescribes(ev, occ, groups) {
			grp := (*models.Group)(nil)
			if ev.GroupID != nil {
				grp = groups[*ev.GroupID]
			}
			groupLoc := ""
			if grp != nil && grp.Visibility != models.VisibilityInviteOnly {
				groupLoc = grp.Location // group-ok: the group is not invite-only, so its place isn't hidden
			}
			eff := occurrenceToJSON(ev, occ, groupLoc)
			single.Title = eff.Title
			single.Location = placeLine(&occurrenceRoomJSON{ID: room.ID, Name: room.Name, Building: room.Building}, eff.Location)
			single.URL = a.emailBaseURL() + occurrenceDatePath(ev.ID, occ.OriginalDate)
			single.UpdatedAt = laterTime(laterTime(ev.UpdatedAt, occ.UpdatedAt), b.UpdatedAt)
			// A hidden series contributes no description to an opened date.
			if ev.Visibility != models.VisibilityInviteOnly {
				single.Description = ev.Description
			}
		}
		feed.Singles = append(feed.Singles, single)
	}
	return feed, nil
}

// roomFeedDescribes: the room feed names a booking's event when its date
// isn't invite-only (policy.EffectiveVisibility) and its group, if any, is
// still there. Everything else is Reserved (8.2).
func roomFeedDescribes(ev *models.Event, occ *models.EventOccurrence, groups map[uint]*models.Group) bool {
	if ev.GroupID != nil && groups[*ev.GroupID] == nil {
		return false
	}
	return policy.EffectiveVisibility(ev.Visibility, occ.VisibilityOverride) != policy.InviteOnly
}
