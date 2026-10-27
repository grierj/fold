package policy

import (
	"testing"
	"time"
)

func TestDecideCalendar(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	base := CalendarFacts{Authorized: true, PublicDate: true, StartsAt: now.AddDate(0, 0, 7)}
	tests := []struct {
		name  string
		scope CalendarScope
		facts CalendarFacts
		want  CalendarDecision
	}{
		{"authorization remains the gate", CalendarScopeMine, CalendarFacts{GroupMember: true}, CalendarDecision{}},
		{"outsider public unanswered absent from mine", CalendarScopeMine, base, CalendarDecision{}},
		{"outsider public going joins mine", CalendarScopeMine, withCalendar(base, func(f *CalendarFacts) { f.OwnStatus = "going" }), CalendarDecision{Include: true, OwnScope: true}},
		{"outsider public declined absent from mine", CalendarScopeMine, withCalendar(base, func(f *CalendarFacts) { f.OwnStatus = "declined" }), CalendarDecision{OwnScope: true}},
		{"outsider public declined stays in all", CalendarScopeAll, withCalendar(base, func(f *CalendarFacts) { f.OwnStatus = "declined" }), CalendarDecision{Include: true, OwnScope: true}},
		{"member date exempt from window", CalendarScopeMine, withCalendar(base, func(f *CalendarFacts) { f.GroupMember = true; f.StartsAt = now.AddDate(0, 0, 90) }), CalendarDecision{Include: true, OwnScope: true}},
		{"host date exempt from window", CalendarScopeMine, withCalendar(base, func(f *CalendarFacts) { f.Host = true; f.StartsAt = now.AddDate(0, 0, 90) }), CalendarDecision{Include: true, OwnScope: true}},
		{"household date is scope but not an own answer", CalendarScopeMine, withCalendar(base, func(f *CalendarFacts) { f.HouseholdDate = true }), CalendarDecision{Include: true, OwnScope: true}},
		{"exact date is scope", CalendarScopeMine, withCalendar(base, func(f *CalendarFacts) { f.ExactDate = true }), CalendarDecision{Include: true, OwnScope: true}},
		{"church wide inside mine window", CalendarScopeMine, withCalendar(base, func(f *CalendarFacts) { f.ChurchWide = true }), CalendarDecision{Include: true, OwnScope: true}},
		{"church wide beyond mine window", CalendarScopeMine, withCalendar(base, func(f *CalendarFacts) { f.ChurchWide = true; f.StartsAt = now.AddDate(0, 0, 31) }), CalendarDecision{OwnScope: true}},
		{"public inside all window", CalendarScopeAll, base, CalendarDecision{Include: true}},
		{"public at all boundary", CalendarScopeAll, withCalendar(base, func(f *CalendarFacts) { f.StartsAt = now.AddDate(0, 0, 30) }), CalendarDecision{Include: true}},
		{"public one instant past boundary", CalendarScopeAll, withCalendar(base, func(f *CalendarFacts) { f.StartsAt = now.AddDate(0, 0, 30).Add(time.Nanosecond) }), CalendarDecision{}},
		{"past unanswered public omitted", CalendarScopeAll, withCalendar(base, func(f *CalendarFacts) { f.StartsAt = now.Add(-time.Nanosecond) }), CalendarDecision{}},
		{"anonymous church-wide at all boundary", CalendarScopeAll, withCalendar(base, func(f *CalendarFacts) { f.PublicDate = false; f.ChurchWide = true; f.StartsAt = now.AddDate(0, 0, 30) }), CalendarDecision{Include: true, OwnScope: true}},
		{"anonymous church-wide past omitted", CalendarScopeAll, withCalendar(base, func(f *CalendarFacts) {
			f.PublicDate = false
			f.ChurchWide = true
			f.StartsAt = now.Add(-time.Nanosecond)
		}), CalendarDecision{OwnScope: true}},
		{"anonymous church-wide beyond boundary omitted", CalendarScopeAll, withCalendar(base, func(f *CalendarFacts) {
			f.PublicDate = false
			f.ChurchWide = true
			f.StartsAt = now.AddDate(0, 0, 30).Add(time.Nanosecond)
		}), CalendarDecision{OwnScope: true}},
		{"visible private date remains in all", CalendarScopeAll, withCalendar(base, func(f *CalendarFacts) { f.PublicDate = false; f.StartsAt = now.AddDate(0, 0, 90) }), CalendarDecision{Include: true}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := DecideCalendar(tc.scope, tc.facts, now); got != tc.want {
				t.Fatalf("got %+v want %+v", got, tc.want)
			}
		})
	}
}

func withCalendar(f CalendarFacts, change func(*CalendarFacts)) CalendarFacts {
	change(&f)
	return f
}
