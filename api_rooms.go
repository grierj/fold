package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"fold/internal/audit"
	"fold/internal/policy"
	"fold/models"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ---------------------------------------------------------------------------
// Rooms (M2.1a, 9.1). Optional by design: while the rooms_enabled setting
// is off, every room endpoint answers 404 and no room UI shows. Admins add,
// edit and deactivate rooms; rooms are never deleted, so bookings keep
// their history.
// ---------------------------------------------------------------------------

const settingRoomsEnabled = "rooms_enabled"

// Room field limits, matching the CHECKs in migration 00045.
const (
	maxRoomName     = 100
	maxRoomBuilding = 100
	maxRoomFeatures = 500
	maxRoomNotes    = 2000
	maxRoomCapacity = 100000
	maxRoomPadding  = 240
)

func (a *App) roomsEnabled() bool {
	var s models.Setting
	return a.DB.Where("key = ?", settingRoomsEnabled).First(&s).Error == nil && s.Value == "true"
}

// roomsOn hides every room route while rooms are off, before auth, so a
// church that doesn't use rooms shows no trace of them.
func (a *App) roomsOn() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !a.roomsEnabled() {
			jsonError(c, http.StatusNotFound, "not found")
			c.Abort()
			return
		}
		c.Next()
	}
}

// roomManager lets through only people who may manage rooms (section 6).
func (a *App) roomManager() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !policy.CanManageRooms(asPerson(apiUser(c))) {
			jsonError(c, http.StatusForbidden, "admin required")
			c.Abort()
			return
		}
		c.Next()
	}
}

type roomJSON struct {
	ID                     uint   `json:"id"`
	Name                   string `json:"name"`
	Building               string `json:"building"`
	Capacity               *int   `json:"capacity"`
	Features               string `json:"features"`
	Notes                  string `json:"notes"`
	Active                 bool   `json:"active"`
	Restricted             bool   `json:"restricted"`
	DefaultSetupMinutes    int    `json:"defaultSetupMinutes"`
	DefaultTeardownMinutes int    `json:"defaultTeardownMinutes"`
}

func toRoomJSON(r *models.Room) roomJSON {
	return roomJSON{ID: r.ID, Name: r.Name, Building: r.Building, Capacity: r.Capacity, Features: r.Features,
		Notes: r.Notes, Active: r.Active, Restricted: r.Restricted,
		DefaultSetupMinutes: r.DefaultSetupMinutes, DefaultTeardownMinutes: r.DefaultTeardownMinutes}
}

// roomBody is the create and edit body. On edit, an absent field keeps its
// value; capacity is raw so null (no limit) differs from absent.
type roomBody struct {
	Name                   *string         `json:"name"`
	Building               *string         `json:"building"`
	Capacity               json.RawMessage `json:"capacity"`
	Features               *string         `json:"features"`
	Notes                  *string         `json:"notes"`
	Active                 *bool           `json:"active"`
	Restricted             *bool           `json:"restricted"`
	DefaultSetupMinutes    *int            `json:"defaultSetupMinutes"`
	DefaultTeardownMinutes *int            `json:"defaultTeardownMinutes"`
}

// apply copies the body onto r and returns the names of the fields that
// changed, or an error message for a field that is out of bounds.
func (b roomBody) apply(r *models.Room) ([]string, string) {
	var changed []string
	text := func(field string, v *string, max int, dst *string, tooLong string) string {
		if v == nil {
			return ""
		}
		s := strings.TrimSpace(*v)
		if utf8.RuneCountInString(s) > max {
			return tooLong
		}
		if s != *dst {
			*dst = s
			changed = append(changed, field)
		}
		return ""
	}
	for _, check := range []func() string{
		func() string { return text("name", b.Name, maxRoomName, &r.Name, "name is too long") },
		func() string {
			return text("building", b.Building, maxRoomBuilding, &r.Building, "building is too long")
		},
		func() string {
			return text("features", b.Features, maxRoomFeatures, &r.Features, "features is too long")
		},
		func() string { return text("notes", b.Notes, maxRoomNotes, &r.Notes, "notes is too long") },
	} {
		if msg := check(); msg != "" {
			return nil, msg
		}
	}
	if strings.TrimSpace(r.Name) == "" {
		return nil, "room name is required"
	}
	if len(b.Capacity) > 0 {
		capacity, ok := parseOptionalPositive(b.Capacity, maxRoomCapacity)
		if !ok {
			return nil, "room capacity must be a whole number from 1 to 100000"
		}
		if !equalIntPtr(capacity, r.Capacity) {
			r.Capacity = capacity
			changed = append(changed, "capacity")
		}
	}
	minutes := func(field string, v *int, dst *int) string {
		if v == nil {
			return ""
		}
		if *v < 0 || *v > maxRoomPadding {
			return "setup and cleanup minutes must be from 0 to 240"
		}
		if *v != *dst {
			*dst = *v
			changed = append(changed, field)
		}
		return ""
	}
	if msg := minutes("defaultSetupMinutes", b.DefaultSetupMinutes, &r.DefaultSetupMinutes); msg != "" {
		return nil, msg
	}
	if msg := minutes("defaultTeardownMinutes", b.DefaultTeardownMinutes, &r.DefaultTeardownMinutes); msg != "" {
		return nil, msg
	}
	flag := func(field string, v *bool, dst *bool) {
		if v != nil && *v != *dst {
			*dst = *v
			changed = append(changed, field)
		}
	}
	flag("active", b.Active, &r.Active)
	flag("restricted", b.Restricted, &r.Restricted)
	return changed, ""
}

// parseOptionalPositive reads null as "no limit" and a whole number from 1
// to max as itself.
func parseOptionalPositive(raw json.RawMessage, max int) (*int, bool) {
	if strings.TrimSpace(string(raw)) == "null" {
		return nil, true
	}
	var n int
	if err := json.Unmarshal(raw, &n); err != nil || n < 1 || n > max {
		return nil, false
	}
	return &n, true
}

func equalIntPtr(a, b *int) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// roomNameTaken reports the one-name-per-building index.
func roomNameTaken(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "idx_rooms_building_name"
}

func (a *App) apiAdminListRooms(c *gin.Context) {
	var rooms []models.Room
	if err := a.DB.Order("active DESC, lower(building), lower(name)").Find(&rooms).Error; err != nil {
		jsonError(c, http.StatusInternalServerError, "could not list rooms")
		return
	}
	out := make([]roomJSON, 0, len(rooms))
	for i := range rooms {
		out = append(out, toRoomJSON(&rooms[i]))
	}
	c.JSON(http.StatusOK, gin.H{"rooms": out})
}

func (a *App) apiAdminCreateRoom(c *gin.Context) {
	var body roomBody
	if err := c.ShouldBindJSON(&body); err != nil {
		jsonError(c, http.StatusBadRequest, "invalid request")
		return
	}
	// A new room is always active; deactivating is an edit.
	body.Active = nil
	room := models.Room{Active: true}
	if _, msg := body.apply(&room); msg != "" {
		jsonError(c, http.StatusBadRequest, msg)
		return
	}
	me := apiUser(c)
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&room).Error; err != nil {
			return err
		}
		return audit.Write(tx, audit.Entry{Actor: &me.ID, Action: audit.RoomCreated, TargetType: "room", TargetID: room.ID,
			Meta: map[string]any{"name": room.Name, "building": room.Building}, IP: c.ClientIP()})
	})
	if roomNameTaken(err) {
		jsonError(c, http.StatusConflict, "a room with that name already exists in that building")
		return
	}
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not add the room")
		return
	}
	c.JSON(http.StatusCreated, gin.H{"room": toRoomJSON(&room)})
}

func (a *App) apiAdminUpdateRoom(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 63)
	if err != nil || id == 0 {
		jsonError(c, http.StatusNotFound, "room not found")
		return
	}
	var body roomBody
	if err := c.ShouldBindJSON(&body); err != nil {
		jsonError(c, http.StatusBadRequest, "invalid request")
		return
	}
	me := apiUser(c)
	var room models.Room
	var changed []string
	errNotFound, errInvalid := errors.New("not found"), errors.New("invalid")
	var invalid string
	err = a.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&room, id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errNotFound
			}
			return err
		}
		var msg string
		changed, msg = body.apply(&room)
		if msg != "" {
			invalid = msg
			return errInvalid
		}
		if len(changed) == 0 {
			return nil
		}
		if err := tx.Save(&room).Error; err != nil {
			return err
		}
		return audit.Write(tx, audit.Entry{Actor: &me.ID, Action: audit.RoomUpdated, TargetType: "room", TargetID: room.ID,
			Meta: map[string]any{"changed": changed}, IP: c.ClientIP()})
	})
	switch {
	case errors.Is(err, errNotFound):
		jsonError(c, http.StatusNotFound, "room not found")
	case errors.Is(err, errInvalid):
		jsonError(c, http.StatusBadRequest, invalid)
	case roomNameTaken(err):
		jsonError(c, http.StatusConflict, "a room with that name already exists in that building")
	case err != nil:
		jsonError(c, http.StatusInternalServerError, "could not save the room")
	default:
		c.JSON(http.StatusOK, gin.H{"room": toRoomJSON(&room)})
	}
}

// apiSetRoomsEnabled turns rooms on or off (9.1). Turning them off hides
// every room screen and endpoint; nothing is deleted.
func (a *App) apiSetRoomsEnabled(c *gin.Context) {
	var body struct {
		Enabled *bool `json:"enabled"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.Enabled == nil {
		jsonError(c, http.StatusBadRequest, "invalid request")
		return
	}
	me := apiUser(c)
	value := strconv.FormatBool(*body.Enabled)
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where(models.Setting{Key: settingRoomsEnabled}).
			Assign(models.Setting{Value: value}).
			FirstOrCreate(&models.Setting{}).Error; err != nil {
			return err
		}
		return audit.Write(tx, audit.Entry{Actor: &me.ID, Action: audit.RoomsEnabledChanged,
			TargetType: "setting", Meta: map[string]any{"enabled": *body.Enabled}, IP: c.ClientIP()})
	})
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not save the setting")
		return
	}
	c.JSON(http.StatusOK, gin.H{"roomsEnabled": *body.Enabled})
}
