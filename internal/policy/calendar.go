package policy

import "time"

// CalendarScope is the product-level filter applied after date visibility has
// been decided. It never grants access to an event, group, series, or date.
type CalendarScope string

const (
	CalendarScopeMine CalendarScope = "mine"
	CalendarScopeAll  CalendarScope = "all"
)

// CalendarFacts are the already-batched facts for one occurrence. Authorized
// must come from effective occurrence visibility or an explicit exact-date
// grant; the remaining fields only decide calendar/feed inclusion and styling.
type CalendarFacts struct {
	Authorized    bool
	GroupMember   bool
	ChurchWide    bool
	Host          bool
	OwnStatus     string
	HouseholdDate bool
	ExactDate     bool
	PublicDate    bool
	StartsAt      time.Time
	// SignedOut: a visitor with no account has no own scope, so no date reads
	// as theirs (no "Not answered" on church-wide dates, DEC-99).
	SignedOut bool
}

type CalendarDecision struct {
	Include  bool
	OwnScope bool
}

// DecideCalendar applies DEC-73/77 before callers truncate or render rows.
// Own responses are deliberately separate from household registrations.
func DecideCalendar(scope CalendarScope, facts CalendarFacts, now time.Time) CalendarDecision {
	if !facts.Authorized {
		return CalendarDecision{}
	}
	answered := facts.OwnStatus != ""
	own := !facts.SignedOut && (facts.GroupMember || facts.ChurchWide || facts.Host || answered || facts.HouseholdDate || facts.ExactDate)
	decision := CalendarDecision{OwnScope: own}

	// Can't-go dates remain discoverable in Everything but disappear from
	// Just mine and from personal feeds (which apply their own final omission).
	if scope == CalendarScopeMine && facts.OwnStatus == "declined" {
		return decision
	}
	if scope == CalendarScopeMine && !own {
		return decision
	}

	// Answers always survive the discovery window (subject to authorization).
	// Membership/hosting and household/exact-date ties are standing scope and
	// are likewise not discovery rows.
	if answered || facts.GroupMember || facts.Host || facts.HouseholdDate || facts.ExactDate {
		decision.Include = true
		return decision
	}

	// Unanswered church-wide dates, and public discovery dates in Everything,
	// are useful only for the next 30 days. The boundary is inclusive.
	if facts.ChurchWide || (scope == CalendarScopeAll && facts.PublicDate) {
		decision.Include = !facts.StartsAt.Before(now) && !facts.StartsAt.After(now.AddDate(0, 0, 30))
		return decision
	}

	decision.Include = true
	return decision
}
