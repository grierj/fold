package main

import (
	"errors"
	"net/http"
	"strings"
	"unicode/utf8"

	"fold/internal/audit"
	"fold/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ---------------------------------------------------------------------------
// Matching old locations (M2.1e, 9.1). Before rooms, places were typed. An
// admin sees the typed places of upcoming dates that have no room, and maps
// one to a room: every free matching date is booked in it, and a series
// whose own place matches gets it as its series room, so later dates are
// booked too. Typed locations are never changed, so the place line shows
// the room and drops a typed place that only repeats it. Running it again
// books nothing twice: dates with a booking are skipped.
// ---------------------------------------------------------------------------

// effectiveLocationSQL is a date's place as the pages show it: the date's
// own (even an explicitly empty one), else the event's, else the group's.
const effectiveLocationSQL = `btrim(COALESCE(o.location_override, NULLIF(e.location, ''), g.location, ''))`

// unbookedDatesSQL is the scheduled upcoming dates with no booking at all.
const unbookedDatesSQL = `FROM event_occurrences o
	JOIN events e ON e.id = o.event_id AND e.deleted_at IS NULL
	LEFT JOIN groups g ON g.id = e.group_id
	WHERE o.status = 'scheduled' AND o.starts_at > now()
	AND NOT EXISTS (SELECT 1 FROM room_bookings b WHERE b.occurrence_id = o.id)`

// seriesMatchSQL picks live series with upcoming dates and no series room
// whose own place (the event's, else the group's) matches the ? location.
const seriesMatchSQL = `e.deleted_at IS NULL AND e.rrule <> '' AND e.default_room_id IS NULL
	AND lower(btrim(COALESCE(NULLIF(e.location, ''), (SELECT g.location FROM groups g WHERE g.id = e.group_id), ''))) = lower(btrim(?))
	AND EXISTS (SELECT 1 FROM event_occurrences u WHERE u.event_id = e.id AND u.status = 'scheduled' AND u.starts_at > now())`

// typedLocationJSON is one typed place and how many upcoming dates without
// a room use it. Spellings that differ only in case or outer spaces are one
// place, shown in its most common spelling.
type typedLocationJSON struct {
	Location string `json:"location"`
	Dates    int    `json:"dates"`
	Events   int    `json:"events"`
}

// matchBusyJSON is a matching date the room was taken on.
type matchBusyJSON struct {
	EventID    uint   `json:"eventId"`
	EventTitle string `json:"eventTitle"`
	Date       string `json:"date"`
}

// maxMatchBusy caps the busy dates a match lists; busyTotal counts them all.
const maxMatchBusy = 100

func (a *App) apiAdminTypedLocations(c *gin.Context) {
	out := []typedLocationJSON{}
	if err := a.DB.Raw(`SELECT mode() WITHIN GROUP (ORDER BY loc) AS location, count(*) AS dates, count(DISTINCT event_id) AS events
		FROM (SELECT o.event_id, ` + effectiveLocationSQL + ` AS loc ` + unbookedDatesSQL + `) d
		WHERE loc <> '' GROUP BY lower(loc) ORDER BY count(*) DESC, lower(loc) LIMIT 200`).Scan(&out).Error; err != nil {
		jsonError(c, http.StatusInternalServerError, "could not list locations")
		return
	}
	c.JSON(http.StatusOK, gin.H{"locations": out})
}

// apiAdminMatchLocation books a room for every free upcoming date whose
// typed place matches, and makes it the series room of series whose own
// place matches and that have none.
func (a *App) apiAdminMatchLocation(c *gin.Context) {
	var body struct {
		Location string `json:"location"`
		RoomID   uint   `json:"roomId"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		jsonError(c, http.StatusBadRequest, "invalid request")
		return
	}
	// Matched in SQL on both sides (lower(btrim())), so the database's own
	// case rules decide, as they did for the list.
	loc := strings.TrimSpace(body.Location)
	if loc == "" || utf8.RuneCountInString(loc) > 500 || body.RoomID == 0 {
		jsonError(c, http.StatusBadRequest, "pick a location and a room")
		return
	}
	me := apiUser(c)
	booked, series := 0, 0
	busy := []matchBusyJSON{}
	busyTotal := 0
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		// Events, then their dates, then the room: the lock order of every
		// other booking path. Every write to a date takes its event's lock
		// first, so the dates read under these locks stay as read.
		var candidates []uint
		if err := tx.Raw(`SELECT DISTINCT o.event_id `+unbookedDatesSQL+` AND lower(`+effectiveLocationSQL+`) = lower(btrim(?))
			UNION SELECT e.id FROM events e WHERE `+seriesMatchSQL+`
			ORDER BY 1`, loc, loc).Scan(&candidates).Error; err != nil {
			return err
		}
		if len(candidates) == 0 {
			return nil
		}
		var evs []models.Event
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id IN ?", candidates).Order("id").Find(&evs).Error; err != nil {
			return err
		}
		eventIDs := make([]uint, len(evs))
		titles := make(map[uint]string, len(evs))
		for i, e := range evs {
			eventIDs[i] = e.ID
			titles[e.ID] = e.Title
		}
		if len(eventIDs) == 0 {
			return nil
		}
		var occs []models.EventOccurrence
		if err := tx.Raw(`SELECT o.* `+unbookedDatesSQL+` AND lower(`+effectiveLocationSQL+`) = lower(btrim(?)) AND o.event_id IN ?
			ORDER BY o.id FOR UPDATE OF o`, loc, eventIDs).Scan(&occs).Error; err != nil {
			return err
		}
		room, err := lockRoom(tx, body.RoomID)
		if errors.Is(err, gorm.ErrRecordNotFound) || (err == nil && !room.Active) {
			return errRoomGone
		}
		if err != nil {
			return err
		}
		for i := range occs {
			result, err := placeBooking(tx, &occs[i], room, room.DefaultSetupMinutes, room.DefaultTeardownMinutes, placement{by: &me.ID})
			if err != nil {
				return err
			}
			if result == notBooked {
				busyTotal++
				if len(busy) < maxMatchBusy {
					busy = append(busy, matchBusyJSON{EventID: occs[i].EventID, EventTitle: titles[occs[i].EventID], Date: occs[i].OriginalDate})
				}
				continue
			}
			booked++
		}
		res := tx.Exec(`UPDATE events e SET default_room_id = ?, default_room_pending = false, updated_at = now()
			WHERE e.id IN ? AND `+seriesMatchSQL, room.ID, eventIDs, loc)
		if res.Error != nil {
			return res.Error
		}
		series = int(res.RowsAffected)
		return audit.Write(tx, audit.Entry{Actor: &me.ID, Action: audit.RoomLocationMatched, TargetType: "room", TargetID: room.ID,
			Meta: map[string]any{"location": loc, "booked": booked, "busy": busyTotal, "series": series}, IP: c.ClientIP()})
	})
	switch {
	case errors.Is(err, errRoomGone):
		jsonError(c, http.StatusNotFound, "room not found")
	case err != nil:
		jsonError(c, http.StatusInternalServerError, "could not book the room")
	default:
		c.JSON(http.StatusOK, gin.H{"booked": booked, "busy": busy, "busyTotal": busyTotal, "series": series})
	}
}
