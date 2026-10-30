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

func TestApplySeriesRestoresSeriesCancelledDates(t *testing.T) {
	now := at("2026-10-10", "12:00")
	existing := []Existing{
		// Cancelled by an earlier series edit; the new rule has it again.
		{ID: 1, OriginalDate: "2026-10-14", StartsAt: at("2026-10-14", "19:00"), EndsAt: at("2026-10-14", "20:00"), SeriesCancelled: true},
		// Cancelled by a person (or before the source was tracked): stays.
		{ID: 2, OriginalDate: "2026-10-21", StartsAt: at("2026-10-21", "19:00"), EndsAt: at("2026-10-21", "20:00"), Keep: true},
		// Series-cancelled and also carries another override: restored with
		// its own values, clock not moved.
		{ID: 3, OriginalDate: "2026-10-28", StartsAt: at("2026-10-28", "18:00"), EndsAt: at("2026-10-28", "19:00"), SeriesCancelled: true, TimeFrozen: true, Keep: true},
		{ID: 4, OriginalDate: "2026-11-04", StartsAt: at("2026-11-04", "19:00"), EndsAt: at("2026-11-04", "20:00"), SeriesCancelled: true, Keep: true}, // title override only
		// Past: untouched even though the rule has it.
		{ID: 5, OriginalDate: "2026-10-07", StartsAt: at("2026-10-07", "19:00"), EndsAt: at("2026-10-07", "20:00"), SeriesCancelled: true},
		// Series-cancelled and still not in the rule: left alone, not cancelled again.
		{ID: 6, OriginalDate: "2026-11-11", StartsAt: at("2026-11-11", "19:00"), EndsAt: at("2026-11-11", "20:00"), SeriesCancelled: true},
	}
	var next []When
	for _, d := range []string{"2026-10-07", "2026-10-14", "2026-10-21", "2026-10-28", "2026-11-04"} {
		next = append(next, When{OriginalDate: d, StartsAt: at(d, "20:00"), EndsAt: at(d, "21:00")})
	}
	got := ApplySeries(existing, next, now)
	if len(got) != 3 {
		t.Fatalf("actions: %+v", got)
	}
	byID := map[uint]Action{}
	for _, a := range got {
		if a.Kind != "restore" {
			t.Fatalf("unexpected %+v", a)
		}
		byID[a.ID] = a
	}
	if a, ok := byID[1]; !ok || !a.Retime || a.When.StartsAt.Hour() != 20 {
		t.Errorf("plain series-cancelled date: %+v", a)
	}
	if a, ok := byID[3]; !ok || a.Retime {
		t.Errorf("clock-frozen date must keep its clock: %+v", a)
	}
	if a, ok := byID[4]; !ok || a.Retime {
		t.Errorf("overridden date must keep its row as is: %+v", a)
	}
	for _, id := range []uint{2, 5, 6} {
		if _, ok := byID[id]; ok {
			t.Errorf("date %d must not be touched", id)
		}
	}
}
