package main

import (
	"net/http"
	"strings"
	"testing"

	"fold/internal/audit"
	"fold/models"
)

// Rooms are invisible until an admin turns them on (9.1, M2.1a); then only
// admins manage them, names are unique per building, and nothing is deleted.
func TestRoomCatalog(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
	newTestUser(t, app, "member", "password1234", models.RoleMember)
	admin := loginAPI(t, app, "admin", "password1234")
	member := loginAPI(t, app, "member", "password1234")

	// Off: every room route is 404, for anyone, and /site says so.
	for _, c := range []*apiClient{admin, member, newAPIClient(t, app)} {
		if w := c.do("GET", "/api/v1/admin/rooms", nil, false); w.Code != http.StatusNotFound {
			t.Fatalf("rooms off: GET %d", w.Code)
		}
	}
	if w := admin.do("POST", "/api/v1/admin/rooms", map[string]any{"name": "Hall"}, true); w.Code != http.StatusNotFound {
		t.Fatalf("rooms off: POST %d", w.Code)
	}
	if !strings.Contains(newAPIClient(t, app).do("GET", "/api/v1/site", nil, false).Body.String(), `"roomsEnabled":false`) {
		t.Fatal("site should say rooms are off")
	}

	// Only an admin turns rooms on.
	if w := member.do("PUT", "/api/v1/admin/settings/rooms", map[string]any{"enabled": true}, true); w.Code != http.StatusForbidden {
		t.Fatalf("member turning rooms on: %d", w.Code)
	}
	if w := admin.do("PUT", "/api/v1/admin/settings/rooms", map[string]any{}, true); w.Code != http.StatusBadRequest {
		t.Fatalf("missing enabled: %d", w.Code)
	}
	if w := admin.do("PUT", "/api/v1/admin/settings/rooms", map[string]any{"enabled": true}, true); w.Code != http.StatusOK {
		t.Fatalf("turn on: %d %s", w.Code, w.Body.String())
	}
	if contactCount(t, app, &models.AuditLog{}, "action = ?", audit.RoomsEnabledChanged) != 1 {
		t.Fatal("turning rooms on should be audited")
	}
	if !strings.Contains(newAPIClient(t, app).do("GET", "/api/v1/site", nil, false).Body.String(), `"roomsEnabled":true`) {
		t.Fatal("site should say rooms are on")
	}

	// On: members are refused, admins manage.
	if w := member.do("GET", "/api/v1/admin/rooms", nil, false); w.Code != http.StatusForbidden {
		t.Fatalf("member list: %d", w.Code)
	}
	var created struct {
		Room roomJSON `json:"room"`
	}
	w := admin.do("POST", "/api/v1/admin/rooms", map[string]any{
		"name": "  Fellowship Hall ", "building": "Main", "capacity": 80, "features": "kitchen, projector",
		"notes": "Key at the office", "defaultSetupMinutes": 30, "defaultTeardownMinutes": 15,
	}, true)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	decodeJSON(t, w, &created)
	hall := created.Room
	if hall.Name != "Fellowship Hall" || hall.Capacity == nil || *hall.Capacity != 80 || !hall.Active || hall.DefaultSetupMinutes != 30 || hall.DefaultTeardownMinutes != 15 {
		t.Fatalf("created room: %+v", hall)
	}
	for _, tc := range []struct {
		body map[string]any
		want int
	}{
		{map[string]any{"name": "fellowship hall", "building": "main"}, http.StatusConflict}, // same name, any case
		{map[string]any{"name": "Fellowship Hall", "building": "Annex"}, http.StatusCreated}, // another building is fine
		{map[string]any{"name": "   "}, http.StatusBadRequest},                               // a name is required
		{map[string]any{"name": "Nursery", "capacity": 0}, http.StatusBadRequest},
		{map[string]any{"name": "Nursery", "defaultSetupMinutes": 241}, http.StatusBadRequest},
	} {
		w := admin.do("POST", "/api/v1/admin/rooms", tc.body, true)
		if w.Code != tc.want {
			t.Fatalf("POST %v: %d %s, want %d", tc.body, w.Code, w.Body.String(), tc.want)
		}
		if tc.want == http.StatusConflict && !strings.Contains(w.Body.String(), `"code":"room_name_taken"`) {
			t.Fatalf("duplicate name code: %s", w.Body.String())
		}
	}

	// A new room is always active, whatever the body says.
	var fresh struct {
		Room roomJSON `json:"room"`
	}
	decodeJSON(t, admin.do("POST", "/api/v1/admin/rooms", map[string]any{"name": "Chapel", "active": false}, true), &fresh)
	if !fresh.Room.Active {
		t.Fatalf("a new room should be active: %+v", fresh.Room)
	}
	// Renaming into a taken name is the same conflict.
	if w := admin.do("PUT", "/api/v1/admin/rooms/"+itoa(fresh.Room.ID), map[string]any{"name": "Fellowship Hall", "building": "Main"}, true); w.Code != http.StatusConflict {
		t.Fatalf("rename into a taken name: %d %s", w.Code, w.Body.String())
	}

	// Edit keeps absent fields, clears capacity with null, and audits what changed.
	id := itoa(hall.ID)
	var updated struct {
		Room roomJSON `json:"room"`
	}
	w = admin.do("PUT", "/api/v1/admin/rooms/"+id, map[string]any{"capacity": nil, "restricted": true}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("edit: %d %s", w.Code, w.Body.String())
	}
	decodeJSON(t, w, &updated)
	if updated.Room.Capacity != nil || !updated.Room.Restricted || updated.Room.Notes != "Key at the office" || updated.Room.DefaultSetupMinutes != 30 {
		t.Fatalf("edited room: %+v", updated.Room)
	}
	if contactCount(t, app, &models.AuditLog{}, "action = ? AND target_id = ? AND meta_json->'changed' ? 'restricted'", audit.RoomUpdated, hall.ID) != 1 {
		t.Fatal("the edit should be audited with its changed fields")
	}

	// Deactivating keeps the room; there is no delete route.
	if w := admin.do("PUT", "/api/v1/admin/rooms/"+id, map[string]any{"active": false}, true); w.Code != http.StatusOK {
		t.Fatalf("deactivate: %d", w.Code)
	}
	if w := admin.do("DELETE", "/api/v1/admin/rooms/"+id, nil, true); w.Code != http.StatusNotFound && w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("delete should not exist: %d", w.Code)
	}
	var list struct {
		Rooms []roomJSON `json:"rooms"`
	}
	decodeJSON(t, admin.do("GET", "/api/v1/admin/rooms", nil, false), &list)
	if len(list.Rooms) != 3 || list.Rooms[len(list.Rooms)-1].ID != hall.ID || list.Rooms[len(list.Rooms)-1].Active {
		t.Fatalf("list should keep the inactive room, last: %+v", list.Rooms)
	}
	for _, missing := range []string{"999999", "18446744073709551615", "abc"} {
		if w := admin.do("PUT", "/api/v1/admin/rooms/"+missing, map[string]any{"active": true}, true); w.Code != http.StatusNotFound {
			t.Fatalf("unknown room %s: %d", missing, w.Code)
		}
	}

	// Off again: hidden again, nothing deleted.
	admin.do("PUT", "/api/v1/admin/settings/rooms", map[string]any{"enabled": false}, true)
	if w := admin.do("GET", "/api/v1/admin/rooms", nil, false); w.Code != http.StatusNotFound {
		t.Fatalf("rooms off again: %d", w.Code)
	}
	if contactCount(t, app, &models.Room{}, "1=1") != 3 {
		t.Fatal("turning rooms off must not delete rooms")
	}
}
