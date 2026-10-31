package main

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"testing"
	"time"

	"fold/models"
)

type pickerWithSuggestions struct {
	Rooms []struct {
		ID uint `json:"id"`
	} `json:"rooms"`
	Suggestions roomSuggestionsJSON `json:"suggestions"`
}

func pickerSuggestions(t *testing.T, body string) pickerWithSuggestions {
	t.Helper()
	var out pickerWithSuggestions
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	return out
}

func suggestionKeysOK(t *testing.T, body string) {
	t.Helper()
	var raw struct {
		Suggestions struct {
			Rooms []map[string]any `json:"rooms"`
		} `json:"suggestions"`
	}
	if err := json.Unmarshal([]byte(body), &raw); err != nil {
		t.Fatal(err)
	}
	for _, s := range raw.Suggestions.Rooms {
		keys := []string{}
		for k := range s {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		if strings.Join(keys, ",") != "busyDates,reason,roomId,uses" {
			t.Fatalf("a suggestion carries %v", keys)
		}
	}
}

// All three pickers suggest the rooms a group has used, with a reason and
// busy marks, and never name the other event (DEC-110, 9.3 Rule 5).
func TestRoomPickersSuggest(t *testing.T) {
	f := newBookingFixture(t)
	if code, body := f.book(t, f.leader, f.eventA, map[string]any{"roomId": f.hall.ID}); code != http.StatusOK {
		t.Fatalf("book A: %d %s", code, body)
	}
	// An admin uses the nursery for the group: only admins get it suggested.
	ev := createFeedEvent(t, f.leader, f.group, "Kids night", f.startOn.AddDate(0, 0, 1), "")
	if w := f.admin.do("PUT", "/api/v1/events/"+itoa(ev.ID)+"/dates/"+feedDate(f.startOn.AddDate(0, 0, 1))+"/room", map[string]any{"roomId": f.nursery.ID}, true); w.Code != http.StatusOK {
		t.Fatalf("admin books the nursery: %d %s", w.Code, w.Body.String())
	}

	// One date: B's picker suggests the hall, busy at B's time.
	w := f.leader.do("GET", f.path(f.eventB, "/rooms"), nil, false)
	body := w.Body.String()
	p := pickerSuggestions(t, body)
	if len(p.Suggestions.Rooms) != 1 || p.Suggestions.Rooms[0].RoomID != f.hall.ID || p.Suggestions.Rooms[0].Reason != "Used by this group once" ||
		p.Suggestions.Rooms[0].BusyDates != 1 || p.Suggestions.Dates != 1 {
		t.Fatalf("one-date suggestions: %s", body)
	}
	suggestionKeysOK(t, body)
	if strings.Contains(body, "Event A") || strings.Contains(body, "Kids night") {
		t.Fatalf("suggestions named another event: %s", body)
	}
	body = f.admin.do("GET", f.path(f.eventB, "/rooms"), nil, false).Body.String()
	if p := pickerSuggestions(t, body); len(p.Suggestions.Rooms) != 2 {
		t.Fatalf("an admin should also get the nursery: %s", body)
	}

	// The series picker.
	body = f.leader.do("GET", "/api/v1/events/"+itoa(f.eventB.ID)+"/rooms", nil, false).Body.String()
	if p := pickerSuggestions(t, body); len(p.Suggestions.Rooms) != 1 || p.Suggestions.Rooms[0].RoomID != f.hall.ID {
		t.Fatalf("series suggestions: %s", body)
	}
	suggestionKeysOK(t, body)

	// A create form in the same group.
	check := func(c *apiClient, extra map[string]any) pickerWithSuggestions {
		req := map[string]any{"startsLocal": f.startOn.AddDate(0, 0, 14).Format("2006-01-02T15:04"), "durationMinutes": 60, "tzid": "UTC"}
		for k, v := range extra {
			req[k] = v
		}
		w := c.do("POST", "/api/v1/rooms/check", req, true)
		if w.Code != http.StatusOK {
			t.Fatalf("rooms check: %d %s", w.Code, w.Body.String())
		}
		suggestionKeysOK(t, w.Body.String())
		return pickerSuggestions(t, w.Body.String())
	}
	if p := check(f.leader, map[string]any{"groupId": f.group}); len(p.Suggestions.Rooms) != 1 || p.Suggestions.Rooms[0].BusyDates != 0 {
		t.Fatalf("create form suggestions: %+v", p.Suggestions)
	}
}

// The create form's chosen visibility decides which usages count (Rule
// 1): an invite-only group's rooms are suggested to its own invite-only
// events, never to a public one elsewhere.
func TestCreateFormSuggestionsUseTheChosenVisibility(t *testing.T) {
	f := newBookingFixture(t)
	f.app.DB.Model(&models.Group{}).Where("id = ?", f.group).Update("visibility", models.VisibilityInviteOnly)
	f.app.DB.Model(&models.Event{}).Where("id = ?", f.eventA.ID).Update("visibility", models.VisibilityInviteOnly)
	if code, body := f.book(t, f.leader, f.eventA, map[string]any{"roomId": f.hall.ID}); code != http.StatusOK {
		t.Fatalf("book A: %d %s", code, body)
	}
	req := func(group uint, vis string) roomSuggestionsJSON {
		body := map[string]any{"startsLocal": f.startOn.AddDate(0, 0, 14).Format(time.DateOnly + "T15:04"), "durationMinutes": 60, "tzid": "UTC", "visibility": vis}
		if group != 0 {
			body["groupId"] = group
		}
		w := f.admin.do("POST", "/api/v1/rooms/check", body, true)
		var out pickerWithSuggestions
		decodeJSON(t, w, &out)
		return out.Suggestions
	}
	if s := req(f.group, models.VisibilityInviteOnly); len(s.Rooms) != 1 {
		t.Fatalf("the group's own invite-only event should get its room: %+v", s)
	}
	if s := req(f.group, models.VisibilityPublic); len(s.Rooms) != 0 {
		t.Fatalf("a public event must not learn where the invite-only group meets: %+v", s)
	}
	if s := req(0, models.VisibilityPublic); len(s.Rooms) != 0 {
		t.Fatalf("a church-wide event must not either: %+v", s)
	}
}
