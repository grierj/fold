package events

import (
	"testing"
	"time"
)

func intp(n int) *int { return &n }

func TestEffectiveCapacityPicksOverride(t *testing.T) {
	if got := EffectiveCapacity(intp(10), intp(4)); got == nil || *got != 4 {
		t.Fatalf("override: got %v, want 4", got)
	}
	if got := EffectiveCapacity(intp(10), nil); got == nil || *got != 10 {
		t.Fatalf("fallback: got %v, want 10", got)
	}
	if got := EffectiveCapacity(nil, nil); got != nil {
		t.Fatalf("null stays unlimited: got %v", got)
	}
}

func entries(seats ...int) []WaitEntry {
	out := make([]WaitEntry, len(seats))
	for i, s := range seats {
		out[i] = WaitEntry{ID: uint(i + 1), Seats: s}
	}
	return out
}

func ids(es []WaitEntry) []uint {
	out := make([]uint, len(es))
	for i, e := range es {
		out[i] = e.ID
	}
	return out
}

func TestPickPromotionsInLineOrder(t *testing.T) {
	got := ids(PickPromotions(10, 7, entries(1, 1, 1), false))
	if len(got) != 3 || got[0] != 1 || got[1] != 2 || got[2] != 3 {
		t.Fatalf("got %v, want [1 2 3]", got)
	}
}

func TestPickPromotionsSkipsPartyTooBig(t *testing.T) {
	// 3 free seats: the 5-seat party is skipped, the 2-seat party takes 2.
	got := ids(PickPromotions(10, 7, entries(5, 2, 1), false))
	if len(got) != 2 || got[0] != 2 || got[1] != 3 {
		t.Fatalf("got %v, want [2 3]", got)
	}
}

func TestPickPromotionsStopsAtExactSeatCount(t *testing.T) {
	got := ids(PickPromotions(10, 8, entries(1, 1, 1), false))
	if len(got) != 2 {
		t.Fatalf("got %v, want exactly 2 picks", got)
	}
	// 0 free seats: nobody.
	if got := PickPromotions(10, 10, entries(1), false); len(got) != 0 {
		t.Fatalf("full: got %v, want none", ids(got))
	}
}

func TestPickPromotionsNeverAboveCapacity(t *testing.T) {
	// A party larger than the whole capacity never promotes, even with
	// every seat free.
	if got := PickPromotions(4, 0, entries(5), false); len(got) != 0 {
		t.Fatalf("oversize party: got %v, want none", ids(got))
	}
}

func TestPickPromotionsCutoffPicksNobody(t *testing.T) {
	if got := PickPromotions(10, 0, entries(1, 1), true); len(got) != 0 {
		t.Fatalf("cutoff: got %v, want none", ids(got))
	}
}

func TestInCutoff(t *testing.T) {
	start := time.Date(2026, 11, 1, 19, 0, 0, 0, time.UTC)
	if !InCutoff(start.Add(-2*time.Hour), start) {
		t.Fatal("exactly 2h before: want cutoff")
	}
	if !InCutoff(start.Add(-time.Hour), start) {
		t.Fatal("1h before: want cutoff")
	}
	if InCutoff(start.Add(-2*time.Hour-time.Second), start) {
		t.Fatal("2h1s before: want no cutoff")
	}
	if InCutoff(start.Add(-3*time.Hour), start) {
		t.Fatal("3h before: want no cutoff")
	}
}
