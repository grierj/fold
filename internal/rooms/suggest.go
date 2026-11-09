// Package rooms suggests rooms for an event (PLAN 9.3, M2.3). Suggestions
// are a shortcut, never a gate: every bookable room stays pickable by hand,
// and nothing here decides who may book what (that is internal/policy).
package rooms

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"fold/internal/grouptags"
	"fold/internal/policy"
	"fold/models"

	"gorm.io/gorm"
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

// countingUsagePredicate is the one test for which usages count toward
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
func countingUsagePredicate(s Subject) (string, []any) {
	since := s.Now.AddDate(0, -usageLookback, 0)
	counts := []string{effectiveVisibility + " IN ('" + policy.Public + "', '" + policy.Private + "')"}
	args := []any{since}
	if s.Visibility == policy.InviteOnly && s.GroupID != nil {
		counts = append(counts, "e.group_id = ?")
		args = append(args, *s.GroupID)
	}
	if s.EventID != 0 {
		counts = append(counts, "e.id = ?")
		args = append(args, s.EventID)
	}
	sql := "b.status = '" + models.BookingConfirmed + "' AND o.status = '" + models.OccurrenceScheduled + "' AND b.starts_at >= ?" +
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

// Suggest ranks rooms for s (9.3 Rules 2 to 4): two reads, the counting
// usages per candidate room and, for those rooms, their conflicts.
func Suggest(db *gorm.DB, s Subject) (Result, error) {
	tags := grouptags.Normalize(s.Tags)
	// "The next 8" dates: soonest first, whatever order they came in.
	windows := append([]Window(nil), s.Windows...)
	sort.Slice(windows, func(i, j int) bool { return windows[i].Start.Before(windows[j].Start) })
	if len(windows) > MaxBusyDates {
		windows = windows[:MaxBusyDates]
	}
	res := Result{Suggestions: []Suggestion{}, Dates: len(windows)}

	pred, args := countingUsagePredicate(s)
	group := uint(0)
	if s.GroupID != nil {
		group = *s.GroupID
	}
	// Rule 3's "shares a tag" compares tags the way the Home filter does,
	// once per group rather than once per booking. No tags match nothing.
	selectArgs := []any{grouptags.Array(tags), s.EventID, group}
	q := `WITH kind AS (SELECT id FROM groups WHERE deleted_at IS NULL
			AND ` + grouptags.MatchAnySQL + `)
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

// markBusy sets each candidate's busyDates (Rule 2).
func markBusy(db *gorm.DB, usages []roomUsage, windows []Window) error {
	ids := make([]uint, len(usages))
	for i, u := range usages {
		ids[i] = u.RoomID
	}
	busy, err := BusyCounts(db, ids, windows, 0)
	if err != nil {
		return err
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
