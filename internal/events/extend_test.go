package events

import (
	"database/sql"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"fold/internal/testdb"
	"fold/models"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestMain(m *testing.M) { os.Exit(testdb.Main(m)) }

// session is its own connection pool whose sessions run in one time zone and
// one DateStyle. A calendar date has no zone, so nothing a session sets may
// move it.
type session struct {
	label string
	zone  string
	// text is how this session's DateStyle spells March 1, 2026 when a date
	// is cast to text. It is why outward dates never use date::text.
	text string
	db   *gorm.DB
}

func openSessions(t *testing.T, shared *gorm.DB) []session {
	t.Helper()
	var name string
	if err := shared.Raw("SELECT current_database()").Scan(&name).Error; err != nil {
		t.Fatal(err)
	}
	base, err := testdb.URL(name)
	if err != nil {
		t.Fatal(err)
	}
	var out []session
	for _, s := range []session{
		{label: "Kiritimati, ISO", zone: "Pacific/Kiritimati", text: "2026-03-01"},
		{label: "Los Angeles, SQL DMY", zone: "America/Los_Angeles", text: "01/03/2026"},
		{label: "Kiritimati, German DMY", zone: "Pacific/Kiritimati", text: "01.03.2026"},
	} {
		u, err := url.Parse(base)
		if err != nil {
			t.Fatal(err)
		}
		q := u.Query()
		q.Set("timezone", s.zone)
		switch s.text {
		case "01/03/2026":
			q.Set("datestyle", "SQL, DMY")
		case "01.03.2026":
			q.Set("datestyle", "German, DMY")
		default:
			q.Set("datestyle", "ISO, MDY")
		}
		// pgx treats a literal '+' in a connection URL as '+', not a space.
		// GORM reads timezone from the raw DSN before URL decoding, so retain
		// the IANA name's slash (which is legal in a URL query).
		u.RawQuery = strings.NewReplacer("+", "%20", "%2F", "/").Replace(q.Encode())
		db, err := gorm.Open(postgres.Open(u.String()), &gorm.Config{Logger: logger.Discard})
		if err != nil {
			t.Fatal(err)
		}
		sqlDB, err := db.DB()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { sqlDB.Close() })
		// The setting must really be in force, or the test proves nothing.
		var zone, text string
		if err := db.Raw("SHOW TimeZone").Scan(&zone).Error; err != nil || zone != s.zone {
			t.Fatalf("%s: session time zone %q (%v), want %s", s.label, zone, err, s.zone)
		}
		if err := db.Raw("SELECT '2026-03-01'::date::text").Scan(&text).Error; err != nil || text != s.text {
			t.Fatalf("%s: a date casts to %q (%v), want %q", s.label, text, err, s.text)
		}
		s.db = db
		out = append(out, s)
	}
	return out
}

func testOwner(t *testing.T, db *gorm.DB) models.User {
	t.Helper()
	u := models.User{Email: "owner@example.com", Name: "Owner", Role: models.RoleAdmin, Status: models.UserActive}
	if err := db.Create(&u).Error; err != nil {
		t.Fatal(err)
	}
	return u
}

func calendarDay(t *testing.T, s string) time.Time {
	t.Helper()
	d, err := models.ParseCalendarDate(s)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// dateTexts are an event's original dates as to_char writes them.
func dateTexts(t *testing.T, db *gorm.DB, eventID uint) []string {
	t.Helper()
	var out []string
	if err := db.Model(&models.EventOccurrence{}).Where("event_id = ?", eventID).Order("original_date").
		Pluck("to_char(original_date, 'YYYY-MM-DD')", &out).Error; err != nil {
		t.Fatal(err)
	}
	return out
}

func weeklyFrom(first time.Time, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = first.AddDate(0, 0, 7*i).Format("2006-01-02")
	}
	return out
}

func count(t *testing.T, db *gorm.DB, query string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := db.Model(&models.EventOccurrence{}).Where(query, args...).Count(&n).Error; err != nil {
		t.Fatal(err)
	}
	return n
}

// A series that begins at 00:30 on Sunday, March 1 at UTC+14 starts on
// February 28 in UTC. Its dates are March 1, 8, 15 ..., written by one
// session and read, bound and extended again from sessions in other zones
// and date styles.
func TestExtendKeepsCalendarDatesInEverySessionZone(t *testing.T) {
	shared := testdb.Shared(t)
	owner := testOwner(t, shared)
	sessions := openSessions(t, shared)
	now := time.Date(2026, 2, 20, 12, 0, 0, 0, time.UTC)
	ev := models.Event{Title: "Island sunrise", Visibility: models.VisibilityPublic, StartsLocal: "2026-03-01T00:30",
		DurationMinutes: 60, TZID: "Pacific/Kiritimati", RRule: "FREQ=WEEKLY", CreatedBy: owner.ID}
	if err := shared.Create(&ev).Error; err != nil {
		t.Fatal(err)
	}

	var added []string
	if err := Extend(sessions[0].db, now, func(tx *gorm.DB, e *models.Event, dates []string) error {
		added = append(added, dates...)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	first := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	if len(added) < 70 {
		t.Fatalf("only %d dates added", len(added))
	}
	want := weeklyFrom(first, len(added))
	if strings.Join(added, ",") != strings.Join(want, ",") {
		t.Fatalf("AfterEnsure saw %v, want weekly from 2026-03-01", added[:3])
	}

	kiritimati, err := LoadZone("Pacific/Kiritimati")
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range sessions {
		// Read: model values and to_char agree with what Extend reported.
		var rows []models.EventOccurrence
		if err := s.db.Where("event_id = ?", ev.ID).Order("original_date").Find(&rows).Error; err != nil {
			t.Fatal(err)
		}
		if len(rows) != len(added) {
			t.Fatalf("%s: %d rows, want %d", s.label, len(rows), len(added))
		}
		for i, r := range rows {
			if got := models.FormatCalendarDate(r.OriginalDate); got != added[i] {
				t.Fatalf("%s: row %d is %s, want %s", s.label, i, got, added[i])
			}
		}
		if got := dateTexts(t, s.db, ev.ID); strings.Join(got, ",") != strings.Join(added, ",") {
			t.Fatalf("%s: to_char gave %v", s.label, got[:3])
		}
		// Bind: text and calendar values select the same single row, and a
		// range or list takes whole dates, not instants.
		if n := count(t, s.db, "event_id = ? AND original_date = ?", ev.ID, "2026-03-01"); n != 1 {
			t.Fatalf("%s: = text matched %d rows", s.label, n)
		}
		// 00:30 on March 1 in Kiritimati is February 28 in UTC; the column
		// takes the calendar date the value shows.
		if n := count(t, s.db, "event_id = ? AND original_date = ?", ev.ID, time.Date(2026, 3, 1, 0, 30, 0, 0, kiritimati)); n != 1 {
			t.Fatalf("%s: = time matched %d rows", s.label, n)
		}
		if n := count(t, s.db, "event_id = ? AND original_date = ?", ev.ID, time.Date(2026, 2, 28, 10, 30, 0, 0, time.UTC)); n != 0 {
			t.Fatalf("%s: the UTC date of that instant matched %d rows", s.label, n)
		}
		if n := count(t, s.db, "event_id = ? AND original_date >= ? AND original_date <= ?", ev.ID, "2026-03-01", "2026-03-29"); n != 5 {
			t.Fatalf("%s: range matched %d rows, want 5", s.label, n)
		}
		if n := count(t, s.db, "event_id = ? AND original_date IN ?", ev.ID, []string{"2026-03-01", "2026-03-08", "2026-03-09"}); n != 2 {
			t.Fatalf("%s: IN matched %d rows, want 2", s.label, n)
		}
		// Extend again from this session: nothing is missing, so nothing is
		// added and nothing is duplicated.
		again := 0
		if err := Extend(s.db, now, func(tx *gorm.DB, e *models.Event, dates []string) error {
			again += len(dates)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if again != 0 {
			t.Fatalf("%s: a second extension added %d dates", s.label, again)
		}
		if n := count(t, s.db, "event_id = ?", ev.ID); int(n) != len(added) {
			t.Fatalf("%s: %d rows after extending again, want %d", s.label, n, len(added))
		}
	}
}

// A model value written from a session in any zone is the calendar date it
// shows, whatever date the same instant has in UTC.
func TestOccurrenceDateWritesAreCalendarDates(t *testing.T) {
	shared := testdb.Shared(t)
	owner := testOwner(t, shared)
	sessions := openSessions(t, shared)
	for i, s := range sessions {
		loc, err := LoadZone(s.zone)
		if err != nil {
			t.Fatal(err)
		}
		ev := models.Event{Title: "One time", Visibility: models.VisibilityPublic, StartsLocal: "2026-03-01T09:00",
			DurationMinutes: 60, TZID: "UTC", CreatedBy: owner.ID}
		if err := s.db.Create(&ev).Error; err != nil {
			t.Fatal(err)
		}
		// 00:30 at UTC+14 or 23:30 at UTC-8, each on a different UTC date.
		hour, minute := 0, 30
		if s.zone == "America/Los_Angeles" {
			hour, minute = 23, 30
		}
		written := time.Date(2026, 3, 1+i, hour, minute, 0, 0, loc)
		want := time.Date(2026, 3, 1+i, 0, 0, 0, 0, time.UTC).Format("2006-01-02")
		occ := models.EventOccurrence{EventID: ev.ID, OriginalDate: written, StartsAt: written, EndsAt: written.Add(time.Hour), Status: models.OccurrenceScheduled}
		if err := s.db.Create(&occ).Error; err != nil {
			t.Fatal(err)
		}
		for _, r := range sessions {
			var got models.EventOccurrence
			if err := r.db.First(&got, occ.ID).Error; err != nil {
				t.Fatal(err)
			}
			if text := models.FormatCalendarDate(got.OriginalDate); text != want {
				t.Fatalf("written in %s, read in %s: %s, want %s", s.label, r.label, text, want)
			}
			if texts := dateTexts(t, r.db, ev.ID); len(texts) != 1 || texts[0] != want {
				t.Fatalf("written in %s, to_char in %s: %v, want %s", s.label, r.label, texts, want)
			}
		}
	}
}

// An end date is inclusive and optional; Extend skips a series whose end date
// is before yesterday and keeps one that ends yesterday; a leap day is a date.
func TestExtendEndDatesAreInclusiveAndOptional(t *testing.T) {
	db := testdb.Shared(t)
	owner := testOwner(t, db)
	now := time.Date(2026, 2, 20, 12, 0, 0, 0, time.UTC)
	series := func(title, start, rule, until string) models.Event {
		t.Helper()
		ev := models.Event{Title: title, Visibility: models.VisibilityPublic, StartsLocal: start,
			DurationMinutes: 60, TZID: "UTC", RRule: rule, CreatedBy: owner.ID}
		if until != "" {
			d := calendarDay(t, until)
			ev.RRuleUntil = &d
		}
		if err := db.Create(&ev).Error; err != nil {
			t.Fatal(err)
		}
		return ev
	}
	onTheDay := series("Ends on a date it has", "2026-03-01T19:00", "FREQ=WEEKLY", "2026-03-29")
	dayBefore := series("Ends the day before a date", "2026-03-01T19:00", "FREQ=WEEKLY", "2026-03-28")
	open := series("No end", "2026-03-01T19:00", "FREQ=WEEKLY", "")
	leap := series("Leap", "2028-02-27T10:00", "FREQ=DAILY", "2028-03-01")
	endedYesterday := series("Ended yesterday", "2026-02-01T19:00", "FREQ=WEEKLY", "2026-02-19")
	endedBefore := series("Ended before yesterday", "2026-02-01T19:00", "FREQ=WEEKLY", "2026-02-18")

	for pass := 0; pass < 2; pass++ { // the second pass must change nothing
		if err := Extend(db, now, nil); err != nil {
			t.Fatal(err)
		}
		for _, tc := range []struct {
			ev   models.Event
			want string
		}{
			{onTheDay, "2026-03-01,2026-03-08,2026-03-15,2026-03-22,2026-03-29"},
			{dayBefore, "2026-03-01,2026-03-08,2026-03-15,2026-03-22"},
			{leap, "2028-02-27,2028-02-28,2028-02-29,2028-03-01"},
			{endedYesterday, "2026-02-01,2026-02-08,2026-02-15"},
			{endedBefore, ""},
		} {
			if got := strings.Join(dateTexts(t, db, tc.ev.ID), ","); got != tc.want {
				t.Fatalf("pass %d, %s: %q, want %q", pass, tc.ev.Title, got, tc.want)
			}
		}
		if n := len(dateTexts(t, db, open.ID)); n < 70 {
			t.Fatalf("pass %d: the series with no end has %d dates", pass, n)
		}
	}

	// The end date comes back exactly, and no end comes back as NULL.
	for _, tc := range []struct {
		ev   models.Event
		want string
	}{{onTheDay, "2026-03-29"}, {leap, "2028-03-01"}, {open, ""}} {
		var got models.Event
		if err := db.First(&got, tc.ev.ID).Error; err != nil {
			t.Fatal(err)
		}
		text := ""
		if got.RRuleUntil != nil {
			text = models.FormatCalendarDate(*got.RRuleUntil)
		}
		if text != tc.want {
			t.Fatalf("%s: until %q, want %q", tc.ev.Title, text, tc.want)
		}
	}
}

// The database itself refuses what the columns cannot hold.
func TestDateAndWallClockColumnsRefuseBadValues(t *testing.T) {
	db := testdb.Shared(t)
	owner := testOwner(t, db)
	insert := func(startsLocal, tzid string) error {
		return db.Exec(`INSERT INTO events (title, description, starts_local, duration_minutes, tzid, rrule, location, visibility, created_by)
			VALUES ('E', '', ?, 60, ?, '', '', 'public', ?)`, startsLocal, tzid, owner.ID).Error
	}
	if err := insert("2026-03-01T09:00", "UTC"); err != nil {
		t.Fatalf("a good event: %v", err)
	}
	if err := insert("2026-03-01T09:00", strings.Repeat("x", 64)); err != nil {
		t.Fatalf("a 64-character zone name: %v", err)
	}
	for _, bad := range []string{"", "garbage", "2026-03-01 09:00", "2026-03-01T9:00", "2026-03-01T09:00:00", "2026-03-01"} {
		if err := insert(bad, "UTC"); err == nil {
			t.Errorf("starts_local %q was accepted", bad)
		}
	}
	for _, bad := range []string{"", strings.Repeat("x", 65)} {
		if err := insert("2026-03-01T09:00", bad); err == nil {
			t.Errorf("tzid of %d characters was accepted", len(bad))
		}
	}

	var ev models.Event
	if err := db.First(&ev).Error; err != nil {
		t.Fatal(err)
	}
	addDate := func(date string) error {
		return db.Exec(`INSERT INTO event_occurrences (event_id, original_date, starts_at, ends_at, status)
			VALUES (?, ?, now(), now() + interval '1 hour', 'scheduled')`, ev.ID, date).Error
	}
	for _, good := range []string{"2028-02-29", "2026-02-28"} {
		if err := addDate(good); err != nil {
			t.Fatalf("original_date %s: %v", good, err)
		}
	}
	for _, bad := range []string{"2027-02-29", "2026-02-30", "2026-04-31", "2026-13-01", "not-a-date", ""} {
		if err := addDate(bad); err == nil {
			t.Errorf("original_date %q was accepted", bad)
		}
	}
	// A date has its row once: the unique index compares real dates.
	if err := addDate("2028-02-29"); err == nil {
		t.Error("the same date was added twice")
	}
	var nullable sql.NullString
	if err := db.Raw("SELECT to_char(rrule_until, 'YYYY-MM-DD') FROM events WHERE id = ?", ev.ID).Row().Scan(&nullable); err != nil || nullable.Valid {
		t.Fatalf("a series with no end date has until %v (%v)", nullable, err)
	}
}
