package main

// erasureReportJSON is what DELETE /api/v1/admin/users/:id answers under
// "erasure" (M7.4 R4-3b-2 and R4-4a-V): integer counts of what the household
// and audit steps kept or scrubbed, never a name, an id or an address.
// householdsKept comes first because it is the one an admin acts on: a household
// the erasure left as it was, because other people (or an ambiguous record)
// still share it.
//
// web/src/types.ts ErasureReport mirrors it; TestWebTypesMatchGoJSON checks
// the pair.
type erasureReportJSON struct {
	// HouseholdsKept: households the erasure locked and left unchanged.
	HouseholdsKept int `json:"householdsKept"`
	// HouseholdsScrubbed: households now named "Removed household" with no
	// address, counting one that an earlier erasure had already scrubbed.
	HouseholdsScrubbed int `json:"householdsScrubbed"`
	// PeopleAnonymized: household people rows that were this individual.
	PeopleAnonymized int `json:"peopleAnonymized"`
	// GuardianLinksRevoked: pending or confirmed guardian links the erasure ended.
	GuardianLinksRevoked int `json:"guardianLinksRevoked"`
	// ActivityEntriesKept: subject-linked RETAIN_* rows with a nonempty IP,
	// plus distinct conversion rows retained by discovery through this subject's
	// approved requests (including already-empty IPs). This is a row count,
	// not proof of IP ownership or a complete census of personal-IP residue.
	ActivityEntriesKept int64 `json:"activityEntriesKept"`
	// ReasonsKept and ReasonsRemoved: the contact-request reasons the admin
	// decided in this deletion (T07B-2b). Counts only, zero when the account had
	// none.
	ReasonsKept    int `json:"reasonsKept"`
	ReasonsRemoved int `json:"reasonsRemoved"`
}

func newErasureReport(h householdErasureCounts, a auditErasureCounts) erasureReportJSON {
	kept := a.Conversion.Retained
	for _, class := range []auditIPClass{auditIPRetainEssential, auditIPRetainUnattributed} {
		for _, count := range a.KeptByClassAndAction[class] {
			kept += count
		}
	}
	// MalformedConversionLinksRetained is already a subset of RETAIN_*.
	// Conversion rows have a separate class, so these totals do not overlap.
	return erasureReportJSON{
		HouseholdsKept: h.HouseholdsKept, HouseholdsScrubbed: h.HouseholdsScrubbed,
		PeopleAnonymized: h.PeopleAnonymized, GuardianLinksRevoked: h.GuardianLinksRevoked,
		ActivityEntriesKept: kept,
	}
}
