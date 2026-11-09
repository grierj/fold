package rooms

import (
	"testing"
	"time"

	"fold/internal/policy"
	"fold/models"
)

// book writes a booking the way placeBooking does, padded by PaddedRange.
func (f *fixture) book(room, event uint, start, end time.Time, setup, teardown int, status string) uint {
	f.t.Helper()
	f.seq++
	occ := models.EventOccurrence{EventID: event, OriginalDate: time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, f.seq),
		StartsAt: start, EndsAt: end, Status: models.OccurrenceScheduled}
	if err := f.db.Create(&occ).Error; err != nil {
		f.t.Fatal(err)
	}
	if err := f.db.Exec(`INSERT INTO room_bookings (occurrence_id, room_id, starts_at, ends_at, setup_minutes, teardown_minutes, padded_range, status)
		VALUES (?, ?, ?, ?, ?, ?, `+PaddedRange("?", "?", "?", "?")+`, ?)`,
		occ.ID, room, start, end, setup, teardown, start, setup, end, teardown, status).Error; err != nil {
		f.t.Fatalf("booking %v-%v: %v", start, end, err)
	}
	return occ.ID
}

// goHolds is the held rule in Go: the padded ranges are [start-setup,
// end+teardown) and overlap when each starts before the other ends.
func goHolds(bStart, bEnd time.Time, bSetup, bTeardown int, wStart, wEnd time.Time, wSetup, wTeardown int) bool {
	bLo, bHi := bStart.Add(-time.Duration(bSetup)*time.Minute), bEnd.Add(time.Duration(bTeardown)*time.Minute)
	wLo, wHi := wStart.Add(-time.Duration(wSetup)*time.Minute), wEnd.Add(time.Duration(wTeardown)*time.Minute)
	return bLo.Before(wHi) && wLo.Before(bHi)
}

// BusyCounts is the SQL form of the held rule. This pins it, edge by edge,
// against the same rule in Go: touching ranges, either side's padding, a
// range over a daylight-saving change, and the bookings that hold nothing.
func TestBusyCountsAgreeWithTheHeldRule(t *testing.T) {
	f := newFixture(t)
	denver, err := time.LoadLocation("America/Denver")
	if err != nil {
		t.Skip("no tz database")
	}
	const roomSetup, roomTeardown = 15, 30
	room := f.room("Hall", false, nil)
	f.db.Model(&models.Room{}).Where("id = ?", room).Updates(map[string]any{"default_setup_minutes": roomSetup, "default_teardown_minutes": roomTeardown})
	g := f.group("G", policy.Public, "")
	holder := f.event(g, policy.Public)
	// Held: 2026-03-08 (clocks go forward at 02:00 in Denver) 01:00-03:30
	// local, setup 20, teardown 10.
	bStart := time.Date(2026, 3, 8, 1, 0, 0, 0, denver)
	bEnd := time.Date(2026, 3, 8, 3, 30, 0, 0, denver)
	f.book(room, holder, bStart, bEnd, 20, 10, models.BookingConfirmed)
	// Bookings that hold nothing, right on top.
	f.book(room, holder, bStart, bEnd, 0, 0, models.BookingRequested)
	f.book(room, holder, bStart, bEnd, 0, 0, models.BookingRejected)

	probe := f.event(g, policy.Public)
	// Offsets chosen so the padded ranges end exactly at, one minute before
	// and one minute after touching.
	lo := bStart.Add(-20 * time.Minute) // the held range starts here
	hi := bEnd.Add(10 * time.Minute)    // and ends here
	for _, gap := range []int{-61, -60, -59, 0, 1, 45, 100} {
		for _, shift := range []time.Duration{0, time.Minute, -time.Minute, 24 * time.Hour} {
			// A date ending `gap` minutes before the held range starts, or
			// (positive gaps) starting that many minutes after it ends.
			for _, before := range []bool{true, false} {
				var ws, we time.Time
				if before {
					we = lo.Add(time.Duration(gap) * time.Minute)
					ws = we.Add(-time.Hour)
				} else {
					ws = hi.Add(time.Duration(gap) * time.Minute)
					we = ws.Add(time.Hour)
				}
				ws, we = ws.Add(shift), we.Add(shift)
				want := goHolds(bStart, bEnd, 20, 10, ws, we, roomSetup, roomTeardown)
				got, err := BusyCounts(f.db, []uint{room}, []Window{{Start: ws, End: we}}, 0)
				if err != nil {
					t.Fatal(err)
				}
				if (got[room] == 1) != want {
					t.Errorf("window %v-%v: SQL busy %d, Go rule %v", ws, we, got[room], want)
				}
			}
		}
	}

	// The window's own date never conflicts with itself; another event's
	// does; exceptEvent ignores the event's own dates.
	own := Window{Start: bStart, End: bEnd}
	if got, _ := BusyCounts(f.db, []uint{room}, []Window{own}, 0); got[room] != 1 {
		t.Fatalf("an unrelated window over the booking is busy: %v", got)
	}
	var occ uint
	f.db.Raw("SELECT occurrence_id FROM room_bookings WHERE room_id = ? AND status = ?", room, models.BookingConfirmed).Scan(&occ)
	own.OccurrenceID = occ
	if got, _ := BusyCounts(f.db, []uint{room}, []Window{own}, 0); got[room] != 0 {
		t.Fatalf("a date's own booking is not a conflict: %v", got)
	}
	own.OccurrenceID = 0
	if got, _ := BusyCounts(f.db, []uint{room}, []Window{own}, holder); got[room] != 0 {
		t.Fatalf("the excepted event's bookings are ignored: %v", got)
	}
	if got, _ := BusyCounts(f.db, []uint{room}, []Window{own}, probe); got[room] != 1 {
		t.Fatalf("another event's exception changes nothing: %v", got)
	}
	// Many windows: each is counted once, per room.
	many := []Window{own, {Start: bEnd.Add(24 * time.Hour), End: bEnd.Add(25 * time.Hour)}, {Start: bStart.Add(time.Minute), End: bStart.Add(2 * time.Minute)}}
	if got, _ := BusyCounts(f.db, []uint{room}, many, 0); got[room] != 2 {
		t.Fatalf("want 2 of 3 windows busy: %v", got)
	}
}

// PaddedRange is the formula the table's CHECK holds every row to: a row
// written with it is accepted, one written with a different range is not.
func TestPaddedRangeIsTheTablesFormula(t *testing.T) {
	f := newFixture(t)
	room := f.room("Hall", false, nil)
	ev := f.event(f.group("G", policy.Public, ""), policy.Public)
	start := f.now.Add(48 * time.Hour)
	f.book(room, ev, start, start.Add(time.Hour), MaxPaddingMinutes, MaxPaddingMinutes, models.BookingConfirmed)
	f.seq++
	occ := models.EventOccurrence{EventID: ev, OriginalDate: time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, f.seq), StartsAt: start, EndsAt: start.Add(time.Hour), Status: models.OccurrenceScheduled}
	f.db.Create(&occ)
	err := f.db.Exec(`INSERT INTO room_bookings (occurrence_id, room_id, starts_at, ends_at, setup_minutes, teardown_minutes, padded_range, status)
		VALUES (?, ?, ?, ?, 10, 10, tstzrange(?, ?, '[)'), 'requested')`, occ.ID, room, start, start.Add(time.Hour), start, start.Add(time.Hour)).Error
	if err == nil {
		t.Fatal("a padded_range that is not the formula must be refused")
	}
}

// bestOf is the fastest of n runs of fn.
func bestOf(n int, fn func()) time.Duration {
	best := time.Hour
	for i := 0; i < n; i++ {
		start := time.Now()
		fn()
		if d := time.Since(start); d < best {
			best = d
		}
	}
	return best
}

// scanCost is what one pass over every booking costs on this machine right now.
// The speed tests below are budgets in scans, so a loaded runner slows the
// budget as much as the code under test; a plan that probes the range index
// stays at a few dozen scans, one that scans the bookings per probe is hundreds.
func scanCost(f *fixture) time.Duration {
	return bestOf(5, func() {
		f.db.Exec(`SELECT count(*), max(upper(padded_range)) FROM room_bookings`)
	})
}

// A series of daily dates over every room answers well inside a request:
// the replaced in-memory sweep existed because a join once grew with every
// booking ever made (M2.1c); the range index keeps each probe the same size.
func TestBusyCountsStayFastForALongSeries(t *testing.T) {
	if testing.Short() {
		t.Skip("seeds 20,000 bookings")
	}
	f := newFixture(t)
	seedLoad(t, f)
	var ids []uint
	f.db.Raw("SELECT id FROM rooms ORDER BY id").Scan(&ids)
	var windows []Window
	for i := 0; i < 550; i++ { // 18 months of daily dates
		s := f.now.AddDate(0, 0, i)
		windows = append(windows, Window{Start: s, End: s.Add(time.Hour)})
	}
	var busy map[uint]int
	best := bestOf(3, func() {
		var err error
		if busy, err = BusyCounts(f.db, ids, windows, 1); err != nil {
			t.Fatal(err)
		}
	})
	scan := scanCost(f)
	t.Logf("550 dates x %d rooms: %v (%.0f scans), %d rooms busy somewhere", len(ids), best, float64(best)/float64(scan), len(busy))
	if limit := 200 * scan; best > limit {
		t.Fatalf("busy counts took %v, want under %v (200 scans of the bookings)", best, limit)
	}
}
