package events

import (
	"testing"
	"time"
)

func at(s string) time.Time {
	t, err := time.Parse(WallLayout, s)
	if err != nil {
		panic(err)
	}
	return t
}

func storedAt(id int64, day, hm string) Stored {
	return Stored{ID: id, Day: day, StartsAt: at(day + "T" + hm), EndsAt: at(day + "T" + hm).Add(time.Hour), StartsLocal: day + "T" + hm}
}

func date(day, hm string) Date {
	return Date{Day: day, StartsAt: at(day + "T" + hm), EndsAt: at(day + "T" + hm).Add(time.Hour), StartsLocal: day + "T" + hm}
}

// DEC-91: future dates move; dates the new repeat drops are cancelled; dates
// the series cancelled come back when it makes them again; dates already
// started are never touched. Carried from Fold's series_test.go.
func TestPlanSeries(t *testing.T) {
	now := at("2026-12-10T12:00")
	personCancelled := storedAt(5, "2026-12-29", "19:00")
	personCancelled.Cancelled = true
	seriesCancelled := storedAt(6, "2027-01-05", "19:00")
	seriesCancelled.Cancelled, seriesCancelled.BySeries = true, true
	stillDropped := storedAt(7, "2027-01-12", "19:00")
	stillDropped.Cancelled, stillDropped.BySeries = true, true
	pastCancelled := storedAt(8, "2026-11-24", "19:00")
	pastCancelled.Cancelled, pastCancelled.BySeries = true, true
	in := []Stored{
		storedAt(1, "2026-12-01", "19:00"), // past
		storedAt(2, "2026-12-08", "19:00"), // past
		storedAt(3, "2026-12-15", "19:00"), // moves to 20:00
		storedAt(4, "2026-12-22", "19:00"), // the new repeat drops it
		personCancelled,                    // stays cancelled
		seriesCancelled,                    // comes back
		stillDropped,                       // still not made: left alone
		pastCancelled,                      // past: left alone though made again
	}
	next := []Date{
		date("2026-11-24", "20:00"), date("2026-12-01", "20:00"), date("2026-12-08", "20:00"),
		date("2026-12-15", "20:00"), date("2026-12-29", "20:00"), date("2027-01-05", "20:00"),
		date("2027-01-19", "20:00"), // a new day
		date("2026-12-09", "20:00"), // a new day, but already past
	}
	p := PlanSeries(in, next, now)
	if len(p.Move) != 1 || p.Move[0].ID != 3 || p.Move[0].Date.StartsLocal != "2026-12-15T20:00" {
		t.Errorf("move: %+v", p.Move)
	}
	if len(p.Cancel) != 1 || p.Cancel[0] != 4 {
		t.Errorf("cancel: %+v", p.Cancel)
	}
	if len(p.Restore) != 1 || p.Restore[0].ID != 6 || p.Restore[0].Date.StartsLocal != "2027-01-05T20:00" {
		t.Errorf("restore: %+v", p.Restore)
	}
	if len(p.Add) != 1 || p.Add[0].Day != "2027-01-19" {
		t.Errorf("add: %+v", p.Add)
	}
}

// The same repeat plans nothing, and a date started an hour ago stays put.
func TestPlanSeriesSameRepeatIsNothing(t *testing.T) {
	now := at("2026-12-15T19:30")
	in := []Stored{storedAt(1, "2026-12-15", "19:00"), storedAt(2, "2026-12-22", "19:00")}
	p := PlanSeries(in, []Date{date("2026-12-15", "19:00"), date("2026-12-22", "19:00")}, now)
	if len(p.Move)+len(p.Cancel)+len(p.Restore)+len(p.Add) != 0 {
		t.Errorf("plan: %+v", p)
	}
	p = PlanSeries(in, []Date{date("2026-12-15", "18:00"), date("2026-12-22", "18:00")}, now)
	if len(p.Move) != 1 || p.Move[0].ID != 2 {
		t.Errorf("a started date moved: %+v", p.Move)
	}
}

// A date R34 moved counts as different from the same clock unmoved.
func TestPlanSeriesSeesTheMovedFlag(t *testing.T) {
	now := at("2027-03-01T00:00")
	s := storedAt(1, "2027-03-14", "03:30")
	d := date("2027-03-14", "03:30")
	d.Moved = true
	if p := PlanSeries([]Stored{s}, []Date{d}, now); len(p.Move) != 1 {
		t.Errorf("plan: %+v", p)
	}
}
