// Package rooms suggests rooms for an event (PLAN 9.3, M2.3). Suggestions
// are a shortcut, never a gate: every bookable room stays pickable by hand,
// and nothing here decides who may book what (that is internal/policy).
package rooms

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"
)

// Visibility values, as stored on events and occurrences.
const (
	public     = "public"
	private    = "private"
	inviteOnly = "invite_only"
)

// MaxSuggestions is how many rooms a picker suggests (9.3 Rule 4).
const MaxSuggestions = 5

// MaxBusyDates is how many of a series' dates Rule 2 checks for conflicts.
const MaxBusyDates = 8

// usageLookback: a usage counts if it started within the last 12 months or
// is still to come (9.3 Definitions).
const usageLookback = 12

// Window is one date being booked. OccurrenceID, when set, is that date's
// own occurrence, whose current booking is never a conflict with itself.
type Window struct {
	Start, End   time.Time
	OccurrenceID uint
}

// Subject is the event E a room is being picked for.
type Subject struct {
	// EventID is E; 0 for an event not created yet (a create form), which
	// has no same-series usages.
	EventID uint
	// GroupID is G_E; nil for a church-wide event.
	GroupID *uint
	// Visibility is V_E: the effective visibility of the date being booked
	// (its override, else the event's), or the visibility chosen on a
	// create form. Never the group's value alone.
	Visibility string
	// Tags are T_E, the group's tags (DEC-100); empty for a church-wide event.
	Tags []string
	// Capacity is E's seat cap, for Rule 4's last tiebreak.
	Capacity *int
	// IncludeRestricted: the chooser may book restricted rooms (an admin).
	IncludeRestricted bool
	// Windows are the dates being booked; at most MaxBusyDates are checked.
	Windows []Window
	Now     time.Time
}

// Suggestion is one suggested room. It carries only the room and counts of
// counting usages, never another event's title, group or date (Rule 5).
type Suggestion struct {
	RoomID uint
	// Uses is the count the reason names.
	Uses   int
	Reason string
	// BusyDates is how many of the checked dates the room is taken on.
	BusyDates int
}

// Result is a picker's suggestions, best first.
type Result struct {
	Suggestions []Suggestion
	// OtherKinds: no candidate was used by a group sharing E's tags, so
	// these are all candidates, labelled "Used by other kinds of events"
	// (Rule 3).
	OtherKinds bool
	// Dates is how many dates Busy was checked across.
	Dates int
}

// effectiveVisibility is V_B, a usage's date visibility (M1.13, DEC-46/48):
// the occurrence's override, else its event's stored value.
const effectiveVisibility = "COALESCE(o.visibility_override, e.visibility)"

// CountingUsagePredicate is the one test for which usages count toward
// suggestions for s (9.3 Rule 1). It is a SQL condition over a confirmed
// room booking b, its occurrence o, its event e and the event's live group
// g (nil for a church-wide event), with its arguments.
//
//   - Public or private E: only usages whose date is public or private.
//   - Invite-only E: those, plus usages by events of E's own group.
//   - Usages by E itself always count.
//
// A usage started within the last 12 months or still to come; deleted
// events and events of deleted groups never count.
func CountingUsagePredicate(s Subject) (string, []any) {
	since := s.Now.AddDate(0, -usageLookback, 0)
	counts := []string{effectiveVisibility + " IN ('" + public + "', '" + private + "')"}
	args := []any{since}
	if s.Visibility == inviteOnly && s.GroupID != nil {
		counts = append(counts, "e.group_id = ?")
		args = append(args, *s.GroupID)
	}
	if s.EventID != 0 {
		counts = append(counts, "e.id = ?")
		args = append(args, s.EventID)
	}
	sql := "b.status = 'confirmed' AND o.status = 'scheduled' AND b.starts_at >= ?" +
		" AND e.deleted_at IS NULL AND (e.group_id IS NULL OR g.id IS NOT NULL)" +
		" AND (" + strings.Join(counts, " OR ") + ")"
	return sql, args
}

type roomUsage struct {
	RoomID     uint
	Capacity   *int
	Total      int
	SameSeries int
	SameGroup  int
	SameKind   int
	LastUsed   time.Time
	busyDates  int
}

// NormalizeTags lowercases and trims tags as the Home tag filter compares
// them, dropping empty ones.
func NormalizeTags(tags []string) []string {
	out := []string{}
	for _, t := range tags {
		if t = strings.ToLower(strings.TrimSpace(t)); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// Suggest ranks rooms for s (9.3 Rules 2 to 4): two reads, the counting
// usages per candidate room and, for those rooms, their conflicts.
func Suggest(db *gorm.DB, s Subject) (Result, error) {
	tags := NormalizeTags(s.Tags)
	// "The next 8" dates: soonest first, whatever order they came in.
	windows := append([]Window(nil), s.Windows...)
	sort.Slice(windows, func(i, j int) bool { return windows[i].Start.Before(windows[j].Start) })
	if len(windows) > MaxBusyDates {
		windows = windows[:MaxBusyDates]
	}
	res := Result{Suggestions: []Suggestion{}, Dates: len(windows)}

	pred, args := CountingUsagePredicate(s)
	group := uint(0)
	if s.GroupID != nil {
		group = *s.GroupID
	}
	// Rule 3's "shares a tag" compares tags the way the Home filter does,
	// once per group rather than once per booking. No tags match nothing.
	selectArgs := []any{"{" + strings.Join(quoteArray(tags), ",") + "}", s.EventID, group}
	q := `WITH kind AS (SELECT id FROM groups WHERE deleted_at IS NULL
			AND regexp_split_to_array(lower(tags), '\s*,\s*') && ?::text[])
		SELECT r.id AS room_id, r.capacity, count(*) AS total,
			count(*) FILTER (WHERE e.id = ?) AS same_series,
			count(*) FILTER (WHERE e.group_id = ?) AS same_group,
			count(*) FILTER (WHERE kind.id IS NOT NULL) AS same_kind,
			max(b.starts_at) AS last_used
		FROM room_bookings b
		JOIN rooms r ON r.id = b.room_id
		JOIN event_occurrences o ON o.id = b.occurrence_id
		JOIN events e ON e.id = o.event_id
		LEFT JOIN groups g ON g.id = e.group_id AND g.deleted_at IS NULL
		LEFT JOIN kind ON kind.id = e.group_id
		WHERE r.active AND (NOT r.restricted OR ?) AND ` + pred + `
		GROUP BY r.id, r.capacity`
	var usages []roomUsage
	if err := db.Raw(q, append(append(selectArgs, s.IncludeRestricted), args...)...).Scan(&usages).Error; err != nil {
		return res, err
	}
	if len(usages) == 0 {
		return res, nil
	}

	// Rule 3: with tags, only rooms some same-kind usage has used, unless
	// that leaves nothing. A church-wide event has no tags: no filter.
	if len(tags) > 0 {
		kept := usages[:0:0]
		for _, u := range usages {
			if u.SameKind > 0 {
				kept = append(kept, u)
			}
		}
		if len(kept) == 0 {
			res.OtherKinds = true
		} else {
			usages = kept
		}
	}

	if err := markBusy(db, usages, windows); err != nil {
		return res, err
	}
	sort.Slice(usages, func(i, j int) bool { return ranksBefore(&usages[i], &usages[j], s.Capacity) })
	if len(usages) > MaxSuggestions {
		usages = usages[:MaxSuggestions]
	}
	for _, u := range usages {
		uses, reason := reasonFor(u, s, res.OtherKinds)
		res.Suggestions = append(res.Suggestions, Suggestion{RoomID: u.RoomID, Uses: uses, Reason: reason, BusyDates: u.busyDates})
	}
	return res, nil
}

// quoteArray writes tags as Postgres array elements.
func quoteArray(tags []string) []string {
	out := make([]string, len(tags))
	for i, t := range tags {
		out[i] = `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(t) + `"`
	}
	return out
}

// markBusy counts, per candidate, the windows another confirmed booking
// holds it on, with the room's own setup and teardown padding (9.2). Like
// the exclusion constraint, it reads booking rows alone: cancelling a date
// or deleting an event releases its booking, so none is left to count.
func markBusy(db *gorm.DB, usages []roomUsage, windows []Window) error {
	if len(windows) == 0 {
		return nil
	}
	ids := make([]uint, len(usages))
	for i, u := range usages {
		ids[i] = u.RoomID
	}
	starts, ends, occs := make([]time.Time, len(windows)), make([]time.Time, len(windows)), make([]int64, len(windows))
	for i, w := range windows {
		starts[i], ends[i], occs[i] = w.Start.UTC(), w.End.UTC(), int64(w.OccurrenceID)
	}
	var rows []struct {
		RoomID uint
		Dates  int
	}
	// Each (room, date) pair is padded first, then probes the confirmed
	// bookings' range index (migration 00050) once.
	err := db.Raw(`SELECT x.room_id, count(*) AS dates
		FROM (SELECT r.id AS room_id, w.occurrence_id,
				tstzrange(w.starts_at - make_interval(mins => r.default_setup_minutes::int),
					w.ends_at + make_interval(mins => r.default_teardown_minutes::int), '[)') AS want
			FROM rooms r
			CROSS JOIN unnest(?::timestamptz[], ?::timestamptz[], ?::bigint[]) AS w(starts_at, ends_at, occurrence_id)
			WHERE r.id IN ?) x
		CROSS JOIN LATERAL (SELECT 1 FROM room_bookings b WHERE b.room_id = x.room_id AND b.status = 'confirmed'
			AND b.padded_range && x.want AND b.occurrence_id <> x.occurrence_id LIMIT 1) hit
		GROUP BY x.room_id`, pgTimes(starts), pgTimes(ends), pgInts(occs), ids).Scan(&rows).Error
	if err != nil {
		return err
	}
	busy := map[uint]int{}
	for _, r := range rows {
		busy[r.RoomID] = r.Dates
	}
	for i := range usages {
		usages[i].busyDates = busy[usages[i].RoomID]
	}
	return nil
}

func pgTimes(ts []time.Time) string {
	parts := make([]string, len(ts))
	for i, t := range ts {
		parts[i] = `"` + t.Format(time.RFC3339Nano) + `"`
	}
	return "{" + strings.Join(parts, ",") + "}"
}

func pgInts(ns []int64) string {
	parts := make([]string, len(ns))
	for i, n := range ns {
		parts[i] = fmt.Sprint(n)
	}
	return "{" + strings.Join(parts, ",") + "}"
}

// ranksBefore is Rule 4, with busy rooms last (Rule 2): same series, same
// group, same kind, most recent use, most use, then the capacity closest to
// (and at least) E's seat cap; the room id keeps the order stable.
func ranksBefore(a, b *roomUsage, capacity *int) bool {
	if (a.busyDates > 0) != (b.busyDates > 0) {
		return a.busyDates == 0
	}
	if a.SameSeries != b.SameSeries {
		return a.SameSeries > b.SameSeries
	}
	if a.SameGroup != b.SameGroup {
		return a.SameGroup > b.SameGroup
	}
	if a.SameKind != b.SameKind {
		return a.SameKind > b.SameKind
	}
	if !a.LastUsed.Equal(b.LastUsed) {
		return a.LastUsed.After(b.LastUsed)
	}
	if a.Total != b.Total {
		return a.Total > b.Total
	}
	if capacity != nil {
		fitsA, fitsB := a.Capacity != nil && *a.Capacity >= *capacity, b.Capacity != nil && *b.Capacity >= *capacity
		if fitsA != fitsB {
			return fitsA
		}
		if fitsA && *a.Capacity != *b.Capacity {
			return *a.Capacity < *b.Capacity
		}
	}
	return a.RoomID < b.RoomID
}

// reasonFor says why a room was suggested, by the strongest tie it has:
// "Used by this event 3 times", "Used by this group 6 times", "Used by
// similar groups 2 times", else "Used 4 times".
func reasonFor(u roomUsage, s Subject, otherKinds bool) (int, string) {
	switch {
	case s.EventID != 0 && u.SameSeries > 0:
		return u.SameSeries, "Used by this event " + times(u.SameSeries)
	case s.GroupID != nil && u.SameGroup > 0:
		return u.SameGroup, "Used by this group " + times(u.SameGroup)
	case !otherKinds && u.SameKind > 0:
		return u.SameKind, "Used by similar groups " + times(u.SameKind)
	}
	return u.Total, "Used " + times(u.Total)
}

func times(n int) string {
	if n == 1 {
		return "once"
	}
	return fmt.Sprintf("%d times", n)
}
