package events

import (
	"reflect"
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

func ids(es []WaitEntry) []uint {
	out := make([]uint, len(es))
	for i, e := range es {
		out[i] = e.ID
	}
	return out
}

func TestEffectiveRemoteCapacity(t *testing.T) {
	if got := EffectiveRemoteCapacity(intp(10), intp(0)); got == nil || *got != 0 {
		t.Fatalf("override 0 is a real cap: got %v, want 0", got)
	}
	if got := EffectiveRemoteCapacity(intp(10), nil); got == nil || *got != 10 {
		t.Fatalf("fallback: got %v, want 10", got)
	}
	if got := EffectiveRemoteCapacity(nil, nil); got != nil {
		t.Fatalf("null stays unlimited: got %v", got)
	}
}

func TestFits(t *testing.T) {
	if !Fits(nil, 100, 5) {
		t.Fatal("unlimited pool always fits")
	}
	if !Fits(intp(10), 8, 2) {
		t.Fatal("exactly filling the cap fits")
	}
	if Fits(intp(10), 8, 3) {
		t.Fatal("over the cap does not fit")
	}
	if Fits(intp(0), 0, 1) {
		t.Fatal("a 0 cap fits nobody")
	}
}

func poolEntries(specs ...struct {
	mode  string
	seats int
}) []WaitEntry {
	out := make([]WaitEntry, len(specs))
	for i, s := range specs {
		out[i] = WaitEntry{ID: uint(i + 1), Seats: s.seats, Mode: s.mode}
	}
	return out
}

func TestPickPromotionsByPoolFillsSeparately(t *testing.T) {
	pools := map[string]Pool{
		"in_person": {Capacity: intp(2), Used: 1}, // 1 free
		"remote":    {Capacity: intp(3), Used: 1}, // 2 free
	}
	wl := poolEntries(
		struct {
			mode  string
			seats int
		}{"in_person", 1},
		struct {
			mode  string
			seats int
		}{"remote", 1},
		struct {
			mode  string
			seats int
		}{"in_person", 1}, // in-person now full, left waiting
		struct {
			mode  string
			seats int
		}{"remote", 1},
		struct {
			mode  string
			seats int
		}{"remote", 1}, // remote now full
	)
	got := ids(PickPromotionsByPool(wl, pools, Pool{}))
	want := []uint{1, 2, 4}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestPickPromotionsByPoolSkipsTooBigWithoutBlockingOtherPool(t *testing.T) {
	pools := map[string]Pool{
		"in_person": {Capacity: intp(5), Used: 3}, // 2 free
		"remote":    {Capacity: intp(2), Used: 0}, // 2 free
	}
	wl := poolEntries(
		struct {
			mode  string
			seats int
		}{"in_person", 4}, // too big for its pool, skipped
		struct {
			mode  string
			seats int
		}{"remote", 2}, // still promotes from the other pool
		struct {
			mode  string
			seats int
		}{"in_person", 2}, // fits after the skipped party
	)
	got := ids(PickPromotionsByPool(wl, pools, Pool{}))
	if len(got) != 2 || got[0] != 2 || got[1] != 3 {
		t.Fatalf("got %v, want [2 3]", got)
	}
}

func TestPickPromotionsByPoolUnlimitedAndZero(t *testing.T) {
	pools := map[string]Pool{
		"in_person": {Capacity: nil, Used: 99},    // unlimited
		"remote":    {Capacity: intp(0), Used: 0}, // no seats at all
	}
	wl := poolEntries(
		struct {
			mode  string
			seats int
		}{"remote", 1},
		struct {
			mode  string
			seats int
		}{"in_person", 6},
		struct {
			mode  string
			seats int
		}{"in_person", 1},
	)
	got := ids(PickPromotionsByPool(wl, pools, Pool{}))
	if len(got) != 2 || got[0] != 2 || got[1] != 3 {
		t.Fatalf("got %v, want [2 3]", got)
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

func TestPickPromotionsByPoolChildcare(t *testing.T) {
	for _, tt := range []struct {
		name      string
		waitlist  []WaitEntry
		pools     map[string]Pool
		childcare Pool
		want      []uint
	}{
		{
			name: "full childcare skips parent without consuming seat",
			waitlist: []WaitEntry{
				{ID: 1, Seats: 1, Mode: "in_person", Childcare: 2},
				{ID: 2, Seats: 1, Mode: "in_person"},
			},
			pools:     map[string]Pool{"in_person": {Capacity: intp(2), Used: 1}},
			childcare: Pool{Capacity: intp(2), Used: 2}, want: []uint{2},
		},
		{
			name: "earlier picks consume childcare across attendance pools",
			waitlist: []WaitEntry{
				{ID: 1, Seats: 1, Mode: "remote", Childcare: 1},
				{ID: 2, Seats: 1, Mode: "in_person", Childcare: 2},
				{ID: 3, Seats: 1, Mode: "in_person", Childcare: 1},
				{ID: 4, Seats: 1, Mode: "remote"},
			},
			childcare: Pool{Capacity: intp(3), Used: 1}, want: []uint{1, 3, 4},
		},
		{
			name: "childcare fits but attendance does not",
			waitlist: []WaitEntry{
				{ID: 1, Seats: 2, Mode: "in_person", Childcare: 2},
				{ID: 2, Seats: 1, Mode: "remote", Childcare: 2},
			},
			pools:     map[string]Pool{"in_person": {Capacity: intp(2), Used: 1}},
			childcare: Pool{Capacity: intp(2)}, want: []uint{2},
		},
		{
			name:      "free childcare cannot bypass full attendance pool",
			waitlist:  []WaitEntry{{ID: 1, Seats: 1, Mode: "in_person", Childcare: 2}},
			pools:     map[string]Pool{"in_person": {Capacity: intp(1), Used: 1}},
			childcare: Pool{Capacity: intp(2)}, want: []uint{},
		},
		{
			name:      "unlimited childcare preserves every request",
			waitlist:  []WaitEntry{{ID: 1, Seats: 1, Childcare: 20}, {ID: 2, Seats: 1, Childcare: 20}},
			childcare: Pool{Used: 100}, want: []uint{1, 2},
		},
		{
			name:      "overfull childcare still permits people needing none",
			waitlist:  []WaitEntry{{ID: 1, Seats: 1, Childcare: 1}, {ID: 2, Seats: 1}},
			childcare: Pool{Capacity: intp(1), Used: 2}, want: []uint{2},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			before := append([]WaitEntry(nil), tt.waitlist...)
			if got := ids(PickPromotionsByPool(tt.waitlist, tt.pools, tt.childcare)); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
			if !reflect.DeepEqual(tt.waitlist, before) {
				t.Fatal("promotion changed the waitlist entries")
			}
		})
	}
}
