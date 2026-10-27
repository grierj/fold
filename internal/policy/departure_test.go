package policy

import "testing"

func TestDecideDepartureDate(t *testing.T) {
	current := DepartureDateFacts{Scheduled: true, NotEnded: true, RegistrationCurrent: true, SubjectEligible: true}
	for _, tt := range []struct {
		name string
		edit func(*DepartureDateFacts)
		want DepartureDateDecision
	}{
		{"underway private defaults kept", func(f *DepartureDateFacts) { f.Underway = true }, DepartureDateDecision{true, true, true, DepartureKeepUnderway}},
		{"cutoff private defaults kept", func(f *DepartureDateFacts) { f.InsideCutoff = true }, DepartureDateDecision{true, true, true, DepartureKeepCutoff}},
		{"public future is offered unchecked", func(f *DepartureDateFacts) { f.Public = true }, DepartureDateDecision{true, true, false, DepartureKeepPublic}},
		{"underway wins over public", func(f *DepartureDateFacts) { f.Underway, f.InsideCutoff, f.Public = true, true, true }, DepartureDateDecision{true, true, true, DepartureKeepUnderway}},
		{"private future is affected but not keepable", func(*DepartureDateFacts) {}, DepartureDateDecision{Affected: true}},
		{"ineligible subject is affected but cannot keep underway", func(f *DepartureDateFacts) { f.SubjectEligible, f.Underway = false, true }, DepartureDateDecision{Affected: true}},
		{"ineligible subject is affected but cannot keep cutoff", func(f *DepartureDateFacts) { f.SubjectEligible, f.InsideCutoff = false, true }, DepartureDateDecision{Affected: true}},
		{"ineligible subject is affected but cannot keep public", func(f *DepartureDateFacts) { f.SubjectEligible, f.Public = false, true }, DepartureDateDecision{Affected: true}},
		{"ended is historical", func(f *DepartureDateFacts) { f.NotEnded = false }, DepartureDateDecision{}},
		{"cancelled date is not affected", func(f *DepartureDateFacts) { f.Scheduled = false }, DepartureDateDecision{}},
		{"declined removed and cancelled registrations are not current", func(f *DepartureDateFacts) { f.RegistrationCurrent = false }, DepartureDateDecision{}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			facts := current
			tt.edit(&facts)
			if got := DecideDepartureDate(facts); got != tt.want {
				t.Fatalf("got %+v want %+v", got, tt.want)
			}
		})
	}
}
