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
	"fold/internal/rooms"
	"fold/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ---------------------------------------------------------------------------
// Rooms (M2.1a, 9.1). Optional by design: while the rooms_enabled setting
// is off, every room endpoint answers 404 and no room UI shows. Admins add,
// edit and deactivate rooms; rooms are never deleted, so bookings keep
// their history.
// ---------------------------------------------------------------------------

// Room field limits, matching the CHECKs in migration 00045.
const (
	maxRoomName     = 100
	maxRoomBuilding = 100
	maxRoomFeatures = 500
	maxRoomNotes    = 2000
	maxRoomCapacity = 100000
)

func (a *App) roomsEnabled() bool {
	return boolSettingIn(a.DB, settingRoomsEnabled, false)
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
// changed, or the refusal for a field that is out of bounds.
func (b roomBody) apply(r *models.Room) ([]string, error) {
	var changed []string
	text := func(field string, v *string, max int, dst *string, tooLong error) error {
		if v == nil {
			return nil
		}
		s := strings.TrimSpace(*v)
		if utf8.RuneCountInString(s) > max {
			return tooLong
		}
		if s != *dst {
			*dst = s
			changed = append(changed, field)
		}
		return nil
	}
	for _, check := range []func() error{
		func() error { return text("name", b.Name, maxRoomName, &r.Name, errNameTooLong) },
		func() error {
			return text("building", b.Building, maxRoomBuilding, &r.Building, errBuildingTooLong)
		},
		func() error {
			return text("features", b.Features, maxRoomFeatures, &r.Features, errFeaturesTooLong)
		},
		func() error { return text("notes", b.Notes, maxRoomNotes, &r.Notes, errNotesTooLong) },
	} {
		if err := check(); err != nil {
			return nil, err
		}
	}
	if strings.TrimSpace(r.Name) == "" {
		return nil, errRoomNameRequired
	}
	if len(b.Capacity) > 0 {
		capacity, ok := parseOptionalPositive(b.Capacity, maxRoomCapacity)
		if !ok {
			return nil, errInvalidRoomCapacity
		}
		if !equalIntPtr(capacity, r.Capacity) {
			r.Capacity = capacity
			changed = append(changed, "capacity")
		}
	}
	minutes := func(field string, v *int, dst *int) error {
		if v == nil {
			return nil
		}
		if *v < 0 || *v > rooms.MaxPaddingMinutes {
			return errInvalidSetupMinutes
		}
		if *v != *dst {
			*dst = *v
			changed = append(changed, field)
		}
		return nil
	}
	if err := minutes("defaultSetupMinutes", b.DefaultSetupMinutes, &r.DefaultSetupMinutes); err != nil {
		return nil, err
	}
	if err := minutes("defaultTeardownMinutes", b.DefaultTeardownMinutes, &r.DefaultTeardownMinutes); err != nil {
		return nil, err
	}
	flag := func(field string, v *bool, dst *bool) {
		if v != nil && *v != *dst {
			*dst = *v
			changed = append(changed, field)
		}
	}
	flag("active", b.Active, &r.Active)
	flag("restricted", b.Restricted, &r.Restricted)
	return changed, nil
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
	return pgConstraint(err, pgUniqueViolation, "idx_rooms_building_name")
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
	c.JSON(http.StatusOK, adminRoomsJSON{Rooms: out, NeedApproval: roomApprovalOn(a.DB)})
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
	if _, err := body.apply(&room); err != nil {
		fail(c, err)
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
		fail(c, errRoomNameTaken)
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
	errNotFound := errors.New("not found")
	err = a.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&room, id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errNotFound
			}
			return err
		}
		var applyErr error
		changed, applyErr = body.apply(&room)
		if applyErr != nil {
			return applyErr
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
	case isAPIError(err):
		fail(c, err)
	case roomNameTaken(err):
		fail(c, errRoomNameTaken)
	case err != nil:
		jsonError(c, http.StatusInternalServerError, "could not save the room")
	default:
		c.JSON(http.StatusOK, gin.H{"room": toRoomJSON(&room)})
	}
}

// apiSetRoomsEnabled turns rooms on or off (9.1). Turning them off hides
// every room screen and endpoint; nothing is deleted.
func (a *App) apiSetRoomsEnabled(c *gin.Context) {
	if enabled, ok := a.setRoomSwitch(c, settingRoomsEnabled, audit.RoomsEnabledChanged); ok {
		c.JSON(http.StatusOK, gin.H{"roomsEnabled": enabled})
	}
}

// setRoomSwitch is the body of both rooms switches: read {enabled}, store it
// in the setting and audit it in one transaction. It answers the errors
// itself and reports ok only after a saved change.
func (a *App) setRoomSwitch(c *gin.Context, key, action string) (enabled, ok bool) {
	var body struct {
		Enabled *bool `json:"enabled"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.Enabled == nil {
		jsonError(c, http.StatusBadRequest, "invalid request")
		return false, false
	}
	me := apiUser(c)
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		if err := setSettingIn(tx, key, strconv.FormatBool(*body.Enabled)); err != nil {
			return err
		}
		return audit.Write(tx, audit.Entry{Actor: &me.ID, Action: action,
			TargetType: "setting", Meta: map[string]any{"enabled": *body.Enabled}, IP: c.ClientIP()})
	})
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not save the setting")
		return false, false
	}
	return *body.Enabled, true
}
