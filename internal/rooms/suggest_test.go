package rooms

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"fold/internal/testdb"
	"fold/models"

	"gorm.io/gorm"
)

func TestMain(m *testing.M) { os.Exit(testdb.Main(m)) }

// fixture builds rooms, groups, events and confirmed bookings straight in
// the database, so each 9.3 case says only what it needs.
type fixture struct {
	t    testing.TB
	db   *gorm.DB
	user uint
	now  time.Time
	seq  int
}

func newFixture(t testing.TB) *fixture {
	t.Helper()
	db := testdb.Shared(t)
	u := models.User{Email: "admin@example.com", Name: "Admin", Role: models.RoleAdmin, Status: models.UserActive}
	if err := db.Create(&u).Error; err != nil {
		t.Fatal(err)
	}
	return &fixture{t: t, db: db, user: u.ID, now: time.Now().UTC().Truncate(time.Minute)}
}

func (f *fixture) room(name string, restricted bool, capacity *int) uint {
	f.t.Helper()
	r := models.Room{Name: name, Active: true, Restricted: restricted, Capacity: capacity}
	if err := f.db.Create(&r).Error; err != nil {
		f.t.Fatal(err)
	}
	return r.ID
}

func (f *fixture) group(name, visibility, tags string) uint {
	f.t.Helper()
	g := models.Group{Name: name, LeaderID: f.user, Status: "active", Visibility: visibility, Tags: tags}
	if err := f.db.Create(&g).Error; err != nil {
		f.t.Fatal(err)
	}
	return g.ID
}

// event makes an event of group (0 for church-wide) with its own stored
// visibility.
func (f *fixture) event(group uint, visibility string) uint {
	f.t.Helper()
	ev := models.Event{Visibility: visibility, StartsLocal: f.now.Format("2006-01-02T15:04"), DurationMinutes: 60, TZID: "UTC", CreatedBy: f.user}
	if group != 0 {
		ev.GroupID = &group
	}
	if err := f.db.Create(&ev).Error; err != nil {
		f.t.Fatal(err)
	}
	return ev.ID
}

// use books room for a new date of event, days from now (negative is the
// past), with an optional per-date visibility.
func (f *fixture) use(room, event uint, days int, override string) uint {
	f.t.Helper()
	f.seq++
	start := f.now.AddDate(0, 0, days).Add(time.Duration(f.seq) * time.Minute)
	// An event's original dates are unique; each use gets its own.
	occ := models.EventOccurrence{EventID: event, OriginalDate: time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, f.seq).Format("2006-01-02"),
		StartsAt: start, EndsAt: start.Add(time.Hour), Status: "scheduled"}
	if override != "" {
		occ.VisibilityOverride = &override
	}
	if err := f.db.Create(&occ).Error; err != nil {
		f.t.Fatal(err)
	}
	if err := f.db.Exec(`INSERT INTO room_bookings (occurrence_id, room_id, starts_at, ends_at, setup_minutes, teardown_minutes, padded_range, status, allow_overlap, overlap_reason)
		VALUES (?, ?, ?, ?, 0, 0, tstzrange(?, ?, '[)'), 'confirmed', true, 'test')`, occ.ID, room, occ.StartsAt, occ.EndsAt, occ.StartsAt, occ.EndsAt).Error; err != nil {
		f.t.Fatal(err)
	}
	return occ.ID
}

func (f *fixture) subject(event, group uint, visibility, tags string) Subject {
	s := Subject{EventID: event, Visibility: visibility, Tags: strings.Split(tags, ","), Now: f.now}
	if group != 0 {
		s.GroupID = &group
	}
	return s
}

func (f *fixture) suggest(s Subject) Result {
	f.t.Helper()
	res, err := Suggest(f.db, s)
	if err != nil {
		f.t.Fatal(err)
	}
	return res
}

func find(res Result, room uint) *Suggestion {
	for i := range res.Suggestions {
		if res.Suggestions[i].RoomID == room {
			return &res.Suggestions[i]
		}
	}
	return nil
}

func ptr(n int) *int { return &n }

// The 13 cases of PLAN 9.3, plus explicitly opened and tightened dates and
// a church-wide event.
func TestSuggestionCases(t *testing.T) {
	t.Run("1 a room used only by an invite-only group is not suggested for a public event", func(t *testing.T) {
		f := newFixture(t)
		r := f.room("R", false, nil)
		x := f.group("X", inviteOnly, "")
		f.use(r, f.event(x, inviteOnly), -7, "")
		other := f.group("Other", public, "")
		if res := f.suggest(f.subject(0, other, public, "")); find(res, r) != nil {
			t.Fatalf("suggested: %+v", res)
		}
	})
	t.Run("2 nor for a private event an admin creates", func(t *testing.T) {
		f := newFixture(t)
		r := f.room("R", false, nil)
		x := f.group("X", inviteOnly, "")
		f.use(r, f.event(x, inviteOnly), -7, "")
		s := f.subject(0, 0, private, "")
		s.IncludeRestricted = true
		if res := f.suggest(s); find(res, r) != nil {
			t.Fatalf("suggested: %+v", res)
		}
	})
	t.Run("3 but it is for a new invite-only event of the same group", func(t *testing.T) {
		f := newFixture(t)
		r := f.room("R", false, nil)
		x := f.group("X", inviteOnly, "")
		f.use(r, f.event(x, inviteOnly), -7, "")
		res := f.suggest(f.subject(f.event(x, inviteOnly), x, inviteOnly, ""))
		if s := find(res, r); s == nil || s.Uses != 1 || s.Reason != "Used by this group once" {
			t.Fatalf("not suggested: %+v", res)
		}
	})
	t.Run("4 and not for another invite-only group", func(t *testing.T) {
		f := newFixture(t)
		r := f.room("R", false, nil)
		x := f.group("X", inviteOnly, "")
		f.use(r, f.event(x, inviteOnly), -7, "")
		y := f.group("Y", inviteOnly, "")
		if res := f.suggest(f.subject(f.event(y, inviteOnly), y, inviteOnly, "")); find(res, r) != nil {
			t.Fatalf("suggested: %+v", res)
		}
	})
	t.Run("5 only counting usages are counted", func(t *testing.T) {
		f := newFixture(t)
		r := f.room("R", false, nil)
		p := f.group("P", private, "")
		f.use(r, f.event(p, private), -3, "")
		x := f.group("X", inviteOnly, "")
		f.use(r, f.event(x, inviteOnly), -2, "")
		res := f.suggest(f.subject(0, f.group("New", public, ""), public, ""))
		if s := find(res, r); s == nil || s.Uses != 1 || s.Reason != "Used once" {
			t.Fatalf("want 1 usage: %+v", res)
		}
	})
	t.Run("6 widening the group never widens its events", func(t *testing.T) {
		f := newFixture(t)
		r := f.room("R", false, nil)
		x := f.group("X", inviteOnly, "")
		f.use(r, f.event(x, inviteOnly), -7, "")
		f.db.Model(&models.Group{}).Where("id = ?", x).Update("visibility", private)
		if res := f.suggest(f.subject(0, f.group("New", public, ""), public, "")); find(res, r) != nil {
			t.Fatalf("suggested: %+v", res)
		}
	})
	t.Run("7 a type filter that matches nothing falls back, labelled", func(t *testing.T) {
		f := newFixture(t)
		r := f.room("R", false, nil)
		f.use(r, f.event(f.group("Choir", public, "music"), public), -7, "")
		res := f.suggest(f.subject(0, f.group("Youth", public, "youth, games"), public, "youth, games"))
		if !res.OtherKinds || find(res, r) == nil {
			t.Fatalf("want the labelled fallback: %+v", res)
		}
		// With a match, only matching rooms are listed.
		m := f.room("M", false, nil)
		f.use(m, f.event(f.group("Teens", public, "Youth"), public), -7, "")
		res = f.suggest(f.subject(0, f.group("Youth 2", public, "youth"), public, "youth"))
		if res.OtherKinds || find(res, r) != nil || find(res, m) == nil || find(res, m).Reason != "Used by similar groups once" {
			t.Fatalf("want only the same-kind room: %+v", res)
		}
	})
	t.Run("8 a restricted room is suggested to admins only", func(t *testing.T) {
		f := newFixture(t)
		r := f.room("Nursery", true, nil)
		f.use(r, f.event(f.group("Kids", public, ""), public), -7, "")
		s := f.subject(0, f.group("New", public, ""), public, "")
		if find(f.suggest(s), r) != nil {
			t.Fatal("a leader shouldn't be offered a restricted room")
		}
		s.IncludeRestricted = true
		if find(f.suggest(s), r) == nil {
			t.Fatal("an admin should be offered it")
		}
	})
	t.Run("9 a usage older than 12 months doesn't count", func(t *testing.T) {
		f := newFixture(t)
		r := f.room("R", false, nil)
		f.use(r, f.event(f.group("Old", public, ""), public), -370, "")
		if res := f.suggest(f.subject(0, f.group("New", public, ""), public, "")); find(res, r) != nil {
			t.Fatalf("suggested: %+v", res)
		}
	})
	t.Run("10 a room with a conflict is listed last, marked busy", func(t *testing.T) {
		f := newFixture(t)
		busy, free := f.room("Busy", false, nil), f.room("Free", false, nil)
		g := f.group("G", public, "")
		ev := f.event(g, public)
		for i := 0; i < 3; i++ {
			f.use(busy, ev, -7*(i+1), "")
		}
		f.use(free, f.event(f.group("Other", public, ""), public), -7, "")
		taken := f.use(busy, f.event(f.group("Third", public, ""), public), 2, "")
		var occ models.EventOccurrence
		f.db.First(&occ, taken)
		s := f.subject(ev, g, public, "")
		s.Windows = []Window{{Start: occ.StartsAt.Add(30 * time.Minute), End: occ.EndsAt.Add(30 * time.Minute)}, {Start: occ.StartsAt.AddDate(0, 0, 7), End: occ.EndsAt.AddDate(0, 0, 7)}}
		res := f.suggest(s)
		if len(res.Suggestions) != 2 || res.Suggestions[0].RoomID != free || res.Suggestions[1].RoomID != busy ||
			res.Suggestions[1].BusyDates != 1 || res.Dates != 2 {
			t.Fatalf("want free first, busy last on 1 of 2 dates: %+v", res)
		}
	})
	t.Run("11 an opened event of an invite-only group counts at its own level", func(t *testing.T) {
		f := newFixture(t)
		r := f.room("R", false, nil)
		x := f.group("X", inviteOnly, "")
		f.use(r, f.event(x, inviteOnly), -9, "")
		opened := f.event(x, public)
		f.use(r, opened, -8, "")
		f.use(r, opened, -1, "")
		res := f.suggest(f.subject(0, f.group("New", public, ""), public, ""))
		if s := find(res, r); s == nil || s.Uses != 2 {
			t.Fatalf("want the 2 public usages: %+v", res)
		}
	})
	t.Run("12 a church-wide event skips same-group ranking and the type filter", func(t *testing.T) {
		f := newFixture(t)
		a, b := f.room("A", false, nil), f.room("B", false, nil)
		f.use(a, f.event(f.group("Choir", public, "music"), public), -20, "")
		f.use(b, f.event(0, public), -5, "")
		res := f.suggest(f.subject(f.event(0, public), 0, public, ""))
		if res.OtherKinds || len(res.Suggestions) != 2 || res.Suggestions[0].RoomID != b {
			t.Fatalf("want both, most recent first: %+v", res)
		}
	})
	t.Run("13 an invite-only group's event made private counts for a private event", func(t *testing.T) {
		f := newFixture(t)
		r := f.room("R", false, nil)
		x := f.group("X", inviteOnly, "")
		f.use(r, f.event(x, inviteOnly), -9, "")
		f.use(r, f.event(x, private), -8, "")
		res := f.suggest(f.subject(0, f.group("New", private, ""), private, ""))
		if s := find(res, r); s == nil || s.Uses != 1 {
			t.Fatalf("want the private usage only: %+v", res)
		}
	})
	t.Run("an explicitly opened date counts at its level; the rest of its series doesn't", func(t *testing.T) {
		f := newFixture(t)
		r := f.room("R", false, nil)
		x := f.group("X", inviteOnly, "")
		ev := f.event(x, inviteOnly)
		f.use(r, ev, -9, "")
		f.use(r, ev, -8, public)
		res := f.suggest(f.subject(0, f.group("New", public, ""), public, ""))
		if s := find(res, r); s == nil || s.Uses != 1 {
			t.Fatalf("want the opened date only: %+v", res)
		}
	})
	t.Run("a public event's date made invite-only doesn't count", func(t *testing.T) {
		f := newFixture(t)
		r := f.room("R", false, nil)
		f.use(r, f.event(f.group("G", public, ""), public), -8, inviteOnly)
		if res := f.suggest(f.subject(0, f.group("New", public, ""), public, "")); find(res, r) != nil {
			t.Fatalf("suggested: %+v", res)
		}
	})
	t.Run("an event's own earlier dates always count, first", func(t *testing.T) {
		f := newFixture(t)
		mine, other := f.room("Mine", false, nil), f.room("Other", false, nil)
		x := f.group("X", inviteOnly, "")
		ev := f.event(x, inviteOnly)
		f.use(mine, ev, -30, "")
		for i := 0; i < 4; i++ {
			f.use(other, f.event(f.group(fmt.Sprintf("P%d", i), public, ""), public), -2, "")
		}
		// Even asked as a public event (its next date opened), its own
		// invite-only dates count.
		res := f.suggest(f.subject(ev, x, public, ""))
		if len(res.Suggestions) != 2 || res.Suggestions[0].RoomID != mine || res.Suggestions[0].Reason != "Used by this event once" {
			t.Fatalf("want the event's own room first: %+v", res)
		}
	})
}

// Rule 4's order: series, group, kind, recency, total, then capacity.
func TestSuggestionRanking(t *testing.T) {
	f := newFixture(t)
	g := f.group("G", public, "study")
	ev := f.event(g, public)
	series, group, kind := f.room("Series", false, nil), f.room("Group", false, nil), f.room("Kind", false, nil)
	recent, small, fits, big := f.room("Recent", false, nil), f.room("Small", false, ptr(10)), f.room("Fits", false, ptr(30)), f.room("Big", false, ptr(200))
	f.use(series, ev, -100, "")
	f.use(group, f.event(g, public), -90, "")
	f.use(group, f.event(g, public), -80, "")
	f.use(kind, f.event(f.group("Study 2", public, "study"), public), -1, "")
	f.use(kind, f.event(f.group("Study 3", public, "Study, men"), public), -2, "")
	// The rest share a kind once, at the same moment, to reach the later
	// tiebreaks: recency, then capacity closest to and at least 25.
	sameKind := f.event(f.group("Study 4", public, "study"), public)
	at := f.now.AddDate(0, 0, -50)
	for _, r := range []uint{small, fits, big} {
		occ := models.EventOccurrence{EventID: sameKind, OriginalDate: fmt.Sprintf("2020-01-%02d", r%28+1), StartsAt: at, EndsAt: at.Add(time.Hour), Status: "scheduled"}
		f.db.Create(&occ)
		f.db.Exec(`INSERT INTO room_bookings (occurrence_id, room_id, starts_at, ends_at, padded_range, status, allow_overlap, overlap_reason) VALUES (?, ?, ?, ?, tstzrange(?, ?, '[)'), 'confirmed', true, 'test')`,
			occ.ID, r, at, at.Add(time.Hour), at, at.Add(time.Hour))
	}
	f.use(recent, sameKind, -10, "")
	s := f.subject(ev, g, public, "study")
	s.Capacity = ptr(25)
	res := f.suggest(s)
	got := []uint{}
	for _, x := range res.Suggestions {
		got = append(got, x.RoomID)
	}
	want := []uint{series, group, kind, recent, fits}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("order %v, want %v (top 5 of 7): %+v", got, want, res)
	}
	if res.Suggestions[1].Reason != "Used by this group 2 times" || res.Suggestions[2].Reason != "Used by similar groups 2 times" {
		t.Fatalf("reasons: %+v", res.Suggestions)
	}
}

// Deleted events, deleted groups' events, cancelled dates, unconfirmed
// bookings and inactive rooms never make a suggestion.
func TestSuggestionsIgnoreWhatIsGone(t *testing.T) {
	f := newFixture(t)
	gone, inactive, cancelled, requested := f.room("Gone", false, nil), f.room("Inactive", false, nil), f.room("Cancelled", false, nil), f.room("Requested", false, nil)
	deletedEvent := f.event(f.group("A", public, ""), public)
	f.use(gone, deletedEvent, -3, "")
	f.db.Delete(&models.Event{}, deletedEvent)
	dg := f.group("B", public, "")
	f.use(gone, f.event(dg, public), -3, "")
	f.db.Delete(&models.Group{}, dg)
	f.use(inactive, f.event(f.group("C", public, ""), public), -3, "")
	f.db.Model(&models.Room{}).Where("id = ?", inactive).Update("active", false)
	occ := f.use(cancelled, f.event(f.group("D", public, ""), public), -3, "")
	f.db.Model(&models.EventOccurrence{}).Where("id = ?", occ).Update("status", "cancelled")
	occ = f.use(requested, f.event(f.group("E", public, ""), public), -3, "")
	f.db.Exec("UPDATE room_bookings SET status = 'requested' WHERE occurrence_id = ?", occ)
	if res := f.suggest(f.subject(0, f.group("New", public, ""), public, "")); len(res.Suggestions) != 0 {
		t.Fatalf("want nothing: %+v", res)
	}
}

// 50 rooms and 20,000 bookings answer in under 100 ms (M2.3a).
func TestSuggestionsAreFast(t *testing.T) {
	if testing.Short() {
		t.Skip("seeds 20,000 bookings")
	}
	f := newFixture(t)
	s := seedLoad(t, f)
	best := time.Hour
	for i := 0; i < 5; i++ {
		start := time.Now()
		res := f.suggest(s)
		if d := time.Since(start); d < best {
			best = d
		}
		if len(res.Suggestions) != MaxSuggestions {
			t.Fatalf("want %d suggestions: %+v", MaxSuggestions, res)
		}
	}
	// The target is 100 ms (M2.3a). Shared CI runners are slower and noisy,
	// so there the test only catches a plan gone badly wrong.
	limit := 100 * time.Millisecond
	if os.Getenv("CI") != "" {
		limit = 300 * time.Millisecond
	}
	if best > limit {
		t.Fatalf("suggestions took %v, want under %v", best, limit)
	}
}

func BenchmarkSuggest(b *testing.B) {
	f := newFixture(b)
	s := seedLoad(b, f)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := Suggest(f.db, s); err != nil {
			b.Fatal(err)
		}
	}
}

// seedLoad: 50 rooms, 40 groups with tags, 400 events and 20,000 confirmed
// bookings over the last year and the next six months, in SQL.
func seedLoad(t testing.TB, f *fixture) Subject {
	t.Helper()
	exec := func(q string, args ...any) {
		if err := f.db.Exec(q, args...).Error; err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO rooms (name) SELECT 'Room ' || n FROM generate_series(1, 50) n`)
	vis, kinds := []string{public, private, inviteOnly}, []string{"study", "youth", "music", "men"}
	var groups []models.Group
	for n := 1; n <= 40; n++ {
		groups = append(groups, models.Group{Name: fmt.Sprintf("Group %d", n), LeaderID: f.user, Status: "active", Visibility: vis[n%3], Tags: kinds[n%4]})
	}
	if err := f.db.Create(&groups).Error; err != nil {
		t.Fatal(err)
	}
	var evs []models.Event
	for i := range groups {
		for j := 0; j < 10; j++ {
			evs = append(evs, models.Event{GroupID: &groups[i].ID, Title: "Event", Visibility: groups[i].Visibility,
				StartsLocal: "2026-01-01T19:00", DurationMinutes: 60, TZID: "UTC", CreatedBy: f.user})
		}
	}
	if err := f.db.Create(&evs).Error; err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO event_occurrences (event_id, original_date, starts_at, ends_at, status, created_at, updated_at)
		SELECT e.id, to_char(date '2000-01-01' + n, 'YYYY-MM-DD'),
			now() - interval '365 days' + (n * 547 / 50) * interval '1 day' + (e.id % 24) * interval '1 hour',
			now() - interval '365 days' + (n * 547 / 50) * interval '1 day' + (e.id % 24) * interval '1 hour' + interval '1 hour', 'scheduled', now(), now()
		FROM events e CROSS JOIN generate_series(1, 50) n`)
	exec(`INSERT INTO room_bookings (occurrence_id, room_id, starts_at, ends_at, padded_range, status, allow_overlap, overlap_reason)
		SELECT o.id, (SELECT min(id) FROM rooms) + (o.id % 50), o.starts_at, o.ends_at, tstzrange(o.starts_at, o.ends_at, '[)'), 'confirmed', true, 'test'
		FROM event_occurrences o`)
	var n int64
	f.db.Raw("SELECT count(*) FROM room_bookings").Scan(&n)
	f.db.Exec("ANALYZE")
	if n != 20000 {
		t.Fatalf("seeded %d bookings", n)
	}
	var g models.Group
	f.db.Where("visibility = ? AND tags = ?", "invite_only", "study").First(&g)
	var ev models.Event
	f.db.Where("group_id = ?", g.ID).First(&ev)
	s := f.subject(ev.ID, g.ID, inviteOnly, "study")
	for i := 0; i < MaxBusyDates; i++ {
		start := f.now.AddDate(0, 0, 7*(i+1))
		s.Windows = append(s.Windows, Window{Start: start, End: start.Add(time.Hour)})
	}
	return s
}

// Busy uses ordinary bookings with their real padding, checks only the
// soonest 8 dates whatever order they arrive in, and never counts the
// date's own booking.
func TestSuggestionBusyCheck(t *testing.T) {
	f := newFixture(t)
	hall := f.room("Hall", false, nil)
	f.db.Model(&models.Room{}).Where("id = ?", hall).Update("default_teardown_minutes", 30)
	g := f.group("G", public, "")
	f.use(hall, f.event(g, public), -7, "")
	// Another event's ordinary booking, 19:00-20:00 in 3 days with 30
	// minutes of setup: it holds [18:30, 20:00).
	other := f.event(f.group("Other", public, ""), public)
	start := time.Date(f.now.Year(), f.now.Month(), f.now.Day(), 19, 0, 0, 0, time.UTC).AddDate(0, 0, 3)
	occ := models.EventOccurrence{EventID: other, OriginalDate: start.Format("2006-01-02"), StartsAt: start, EndsAt: start.Add(time.Hour), Status: "scheduled"}
	if err := f.db.Create(&occ).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Exec(`INSERT INTO room_bookings (occurrence_id, room_id, starts_at, ends_at, setup_minutes, teardown_minutes, padded_range, status)
		VALUES (?, ?, ?, ?, 30, 0, tstzrange(?, ?, '[)'), 'confirmed')`, occ.ID, hall, start, start.Add(time.Hour), start.Add(-30*time.Minute), start.Add(time.Hour)).Error; err != nil {
		t.Fatal(err)
	}
	s := f.subject(f.event(g, public), g, public, "")
	busyAt := func(w Window) int {
		s.Windows = []Window{w}
		return f.suggest(s).Suggestions[0].BusyDates
	}
	// 17:45-18:00 ends before the booking's padding, but the hall's own
	// 30 minutes of teardown reach 18:30: touching, not overlapping.
	if busyAt(Window{Start: start.Add(-75 * time.Minute), End: start.Add(-60 * time.Minute)}) != 0 {
		t.Fatal("touching padded ranges aren't a conflict")
	}
	if busyAt(Window{Start: start.Add(-70 * time.Minute), End: start.Add(-50 * time.Minute)}) != 1 {
		t.Fatal("the hall's teardown overlaps the booking's setup: busy")
	}
	if busyAt(Window{Start: start, End: start.Add(time.Hour), OccurrenceID: occ.ID}) != 0 {
		t.Fatal("a date's own booking isn't a conflict")
	}
	// Ten weekly dates, latest first: the soonest 8 are checked, and the
	// busy one is among them.
	s.Windows = nil
	for i := 9; i >= 0; i-- {
		d := start.AddDate(0, 0, 7*i)
		s.Windows = append(s.Windows, Window{Start: d, End: d.Add(time.Hour)})
	}
	if res := f.suggest(s); res.Dates != MaxBusyDates || res.Suggestions[0].BusyDates != 1 {
		t.Fatalf("want 1 of the soonest 8 busy: %+v", res)
	}
}
