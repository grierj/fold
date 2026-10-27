package policy

// DepartureKeepReason explains why one current registration may be retained
// when its account leaves a group. The caller computes the clock facts with the
// shared event cutoff rule; this package decides only from those facts.
const (
	DepartureKeepUnderway = "underway"
	DepartureKeepCutoff   = "cutoff"
	DepartureKeepPublic   = "public"
)

// DepartureDateFacts are the pure facts needed for DEC-74's date choice.
// RegistrationCurrent means the subject account has a Going, Maybe, or
// Waitlisted registration. Declined, cancelled, removed, proxy, and household
// registrations are not current departure registrations. SubjectEligible means
// the account can currently use retained exact-date authority.
type DepartureDateFacts struct {
	Scheduled           bool
	NotEnded            bool
	RegistrationCurrent bool
	SubjectEligible     bool
	Underway            bool
	InsideCutoff        bool
	Public              bool
}

// DepartureDateDecision says whether a registration is affected by departure
// and, if so, whether the confirmation may retain it. Underway and cutoff dates
// default to kept; a chosen public date is offered unchecked.
type DepartureDateDecision struct {
	Affected    bool
	Keepable    bool
	DefaultKeep bool
	KeepReason  string
}

func DecideDepartureDate(f DepartureDateFacts) DepartureDateDecision {
	if !f.Scheduled || !f.NotEnded || !f.RegistrationCurrent {
		return DepartureDateDecision{}
	}
	out := DepartureDateDecision{Affected: true}
	if !f.SubjectEligible {
		return out
	}
	switch {
	case f.Underway:
		out.Keepable, out.DefaultKeep, out.KeepReason = true, true, DepartureKeepUnderway
	case f.InsideCutoff:
		out.Keepable, out.DefaultKeep, out.KeepReason = true, true, DepartureKeepCutoff
	case f.Public:
		out.Keepable, out.KeepReason = true, DepartureKeepPublic
	}
	return out
}
