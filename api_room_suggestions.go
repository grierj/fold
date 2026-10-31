package main

import (
	"log"
	"strings"
	"time"

	"fold/internal/policy"
	"fold/internal/rooms"
	"fold/models"
)

// Room suggestions (M2.3b, 9.3, DEC-110) ride along with each room picker's
// own list: the one-date picker, the series picker and the create forms'
// room check. They are a shortcut, never a gate: every room in the list
// stays pickable, and suggestions only reorder it.

// roomSuggestionJSON is one suggested room: its id, how many counting
// usages its reason names, the reason, and how many of the checked dates
// it is taken on. Never another event's title, group or date (Rule 5).
type roomSuggestionJSON struct {
	RoomID    uint   `json:"roomId"`
	Uses      int    `json:"uses"`
	Reason    string `json:"reason"`
	BusyDates int    `json:"busyDates"`
}

type roomSuggestionsJSON struct {
	Rooms []roomSuggestionJSON `json:"rooms"`
	// OtherKinds: nothing matched the group's kind, so these are labelled
	// "Used by other kinds of events" (Rule 3).
	OtherKinds bool `json:"otherKinds"`
	// Dates is how many dates the busy counts cover (at most 8).
	Dates int `json:"dates"`
}

// suggestFor is who is choosing and for what. EventID is 0 on a create
// form; Visibility is the date's effective visibility, the series' own, or
// the one picked on the create form.
type suggestFor struct {
	person     *policy.Person
	eventID    uint
	groupID    *uint
	visibility string
	capacity   *int
	windows    []rooms.Window
	// tags are the group's, when the caller already loaded it; else read.
	tags *string
}

// roomSuggestions runs the engine for a picker. A failed read leaves the
// picker without suggestions rather than failing it: the full list still
// works. Only rooms the picker offers are kept.
func (a *App) roomSuggestions(s suggestFor, offered map[uint]bool) roomSuggestionsJSON {
	out := roomSuggestionsJSON{Rooms: []roomSuggestionJSON{}}
	subject := rooms.Subject{
		EventID: s.eventID, GroupID: s.groupID, Visibility: s.visibility, Capacity: s.capacity,
		IncludeRestricted: policy.CanBookRoom(s.person, true, true), Windows: s.windows, Now: time.Now().UTC(),
	}
	if s.tags != nil {
		subject.Tags = strings.Split(*s.tags, ",")
	} else if s.groupID != nil {
		var g models.Group
		if err := a.DB.Select("tags").First(&g, *s.groupID).Error; err == nil {
			subject.Tags = strings.Split(g.Tags, ",")
		}
	}
	res, err := rooms.Suggest(a.DB, subject)
	if err != nil {
		log.Printf("room suggestions: %v", err)
		return out
	}
	out.OtherKinds, out.Dates = res.OtherKinds, res.Dates
	for _, r := range res.Suggestions {
		if offered[r.RoomID] {
			out.Rooms = append(out.Rooms, roomSuggestionJSON{RoomID: r.RoomID, Uses: r.Uses, Reason: r.Reason, BusyDates: r.BusyDates})
		}
	}
	return out
}

// windowsOf turns a picker's dates into the engine's, first 8 only.
func windowsOf(occs []models.EventOccurrence) []rooms.Window {
	out := []rooms.Window{}
	for i := range occs {
		if len(out) == rooms.MaxBusyDates {
			break
		}
		out = append(out, rooms.Window{Start: occs[i].StartsAt, End: occs[i].EndsAt, OccurrenceID: occs[i].ID})
	}
	return out
}

// offeredRooms is the set of rooms a series or create-form picker lists.
func offeredRooms(list []seriesRoomJSON) map[uint]bool {
	out := make(map[uint]bool, len(list))
	for _, r := range list {
		out[r.ID] = true
	}
	return out
}

// createVisibility is V_E on a create form: the visibility picked there,
// else what the event would get (the group's, or public church-wide). An
// empty or unknown value falls back the same way; the create itself refuses
// a bad one, so this only ever guesses what the form will send.
func createVisibility(picked *string, g *models.Group) string {
	if picked != nil && policy.ValidVisibility(strings.TrimSpace(*picked)) {
		return strings.TrimSpace(*picked)
	}
	if g != nil {
		return g.Visibility
	}
	return models.VisibilityPublic
}

// groupTags is a loaded group's tags, or nil to read them by id.
func groupTags(g *models.Group) *string {
	if g == nil {
		return nil
	}
	return &g.Tags
}
