package rooms

import (
	"time"

	"fold/models"

	"gorm.io/gorm"
)

// MaxPaddingMinutes is the most setup or teardown time one booking may
// add to either side (DEC-101); the room_bookings CHECKs say the same.
const MaxPaddingMinutes = 240

// PaddedRange is the one SQL expression for the time a booking holds: its
// date widened by setup before and teardown after, as a [) range. Each
// argument is a SQL expression (a column, or a ? placeholder), in the order
// it appears in the SQL text, so a ? list binds start, setup, end, teardown. The
// room_bookings table's padded_range CHECK compares the stored column
// against the same formula, and every "is the room busy" question compares
// against this range through HeldCondition.
func PaddedRange(start, setup, end, teardown string) string {
	return "tstzrange(" + start + "::timestamptz - make_interval(mins => " + setup + "::int), " +
		end + "::timestamptz + make_interval(mins => " + teardown + "::int), '[)')"
}

// HeldCondition is the one test for "another booking holds the room": a
// confirmed booking b of the given room, of a date other than exceptOcc,
// whose padded range overlaps want (a range expression, see PaddedRange).
// Requested and rejected bookings hold nothing. The status is a literal so
// the planner can use idx_room_bookings_confirmed_range. Every argument but b
// is a SQL expression, in the order the placeholders appear.
func HeldCondition(b, room, exceptOcc, want string) string {
	return b + ".room_id = " + room + " AND " + b + ".status = '" + models.BookingConfirmed + "'" +
		" AND " + b + ".occurrence_id <> " + exceptOcc + " AND " + b + ".padded_range && " + want
}

// BusyCounts counts, per room, the windows another confirmed booking holds
// the room on. Each window is padded by the room's own default setup and
// teardown (DEC-101), then probes the confirmed bookings' range index once
// (migration 00050). A window's own occurrence never conflicts with itself.
// A nonzero exceptEvent also ignores every booking of that event's dates, so
// re-picking a series' room never finds it busy with itself. Like the
// exclusion constraint, it reads booking rows alone: cancelling a date or
// deleting an event releases its booking, so none is left to count.
// A room with no busy window is absent from the map: read it as zero.
func BusyCounts(db *gorm.DB, roomIDs []uint, windows []Window, exceptEvent uint) (map[uint]int, error) {
	out := map[uint]int{}
	if len(windows) == 0 || len(roomIDs) == 0 {
		return out, nil
	}
	starts, ends, occs := make([]time.Time, len(windows)), make([]time.Time, len(windows)), make([]int64, len(windows))
	for i, w := range windows {
		starts[i], ends[i], occs[i] = w.Start.UTC(), w.End.UTC(), int64(w.OccurrenceID)
	}
	var rows []struct {
		RoomID uint
		Dates  int
	}
	want := PaddedRange("w.starts_at", "r.default_setup_minutes", "w.ends_at", "r.default_teardown_minutes")
	// Whose bookings are ignored besides the window's own date.
	except, args := "", []any{pgTimes(starts), pgTimes(ends), pgInts(occs), roomIDs}
	if exceptEvent != 0 {
		except = " AND NOT EXISTS (SELECT 1 FROM event_occurrences o WHERE o.id = b.occurrence_id AND o.event_id = ?)"
		args = append(args, exceptEvent)
	}
	err := db.Raw(`SELECT x.room_id, count(*) AS dates
		FROM (SELECT r.id AS room_id, w.occurrence_id, `+want+` AS want
			FROM rooms r
			CROSS JOIN unnest(?::timestamptz[], ?::timestamptz[], ?::bigint[]) AS w(starts_at, ends_at, occurrence_id)
			WHERE r.id IN ?) x
		CROSS JOIN LATERAL (SELECT 1 FROM room_bookings b WHERE `+HeldCondition("b", "x.room_id", "x.occurrence_id", "x.want")+except+`
			LIMIT 1) hit
		GROUP BY x.room_id`, args...).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		out[r.RoomID] = r.Dates
	}
	return out, nil
}
