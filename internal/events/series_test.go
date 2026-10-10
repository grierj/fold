package events

import (
	"testing"
	"time"
)

func at(date, hm string) time.Time {
	t, err := time.Parse(time.RFC3339, date+"T"+hm+":00Z")
	if err != nil {
		panic(err)
	}
	return t
}

func TestApplySeriesMovesKeepsAndCancels(t *testing.T) {
	now := at("2026-10-10", "12:00")
	existing := []Existing{
		{ID: 1, OriginalDate: "2026-10-07", StartsAt: at("2026-10-07", "19:00"), EndsAt: at("2026-10-07", "20:00")}, // past
		{ID: 2, OriginalDate: "2026-10-14", StartsAt: at("2026-10-14", "19:00"), EndsAt: at("2026-10-14", "20:00")},
		{ID: 3, OriginalDate: "2026-10-21", StartsAt: at("2026-10-21", "18:00"), EndsAt: at("2026-10-21", "19:00"), TimeFrozen: true, Keep: true},
		{ID: 4, OriginalDate: "2026-10-28", StartsAt: at("2026-10-28", "19:00"), EndsAt: at("2026-10-28", "20:00"), Keep: true}, // cancelled
		{ID: 5, OriginalDate: "2026-11-04", StartsAt: at("2026-11-04", "19:00"), EndsAt: at("2026-11-04", "20:00")},             // falls off the new rule
	}
	next := []When{
		{OriginalDate: "2026-10-07", StartsAt: at("2026-10-07", "20:00"), EndsAt: at("2026-10-07", "21:00")},
		{OriginalDate: "2026-10-14", StartsAt: at("2026-10-14", "20:00"), EndsAt: at("2026-10-14", "21:00")},
		{OriginalDate: "2026-10-21", StartsAt: at("2026-10-21", "20:00"), EndsAt: at("2026-10-21", "21:00")},
		{OriginalDate: "2026-10-28", StartsAt: at("2026-10-28", "20:00"), EndsAt: at("2026-10-28", "21:00")},
		{OriginalDate: "2026-11-11", StartsAt: at("2026-11-11", "20:00"), EndsAt: at("2026-11-11", "21:00")},
	}
	got := ApplySeries(existing, next, now)
	want := map[string]Action{
		"move:2":         {Kind: "move", ID: 2},
		"cancel:5":       {Kind: "cancel", ID: 5},
		"add:2026-11-11": {Kind: "add"},
	}
	if len(got) != len(want) {
		t.Fatalf("actions: %+v", got)
	}
	for _, a := range got {
		key := a.Kind
		switch a.Kind {
		case "move", "cancel":
			key += ":" + itoa(a.ID)
		case "add":
			key += ":" + a.When.OriginalDate
		}
		if _, ok := want[key]; !ok {
			t.Errorf("unexpected %s", key)
		}
		delete(want, key)
		if a.Kind == "move" && (a.When.StartsAt.Hour() != 20 || a.ID != 2) {
			t.Errorf("move: %+v", a)
		}
	}
	if len(want) != 0 {
		t.Fatalf("missing %+v", want)
	}
}

func itoa(n uint) string {
	if n == 0 {
		return "0"
	}
	var b [8]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
