package main

// erasureReportJSON is what DELETE /api/v1/admin/users/:id answers under
// "erasure" (M7.4 R4-3b-2, inventory T16 and 15.5): integer counts of what the
// household step did, never a name, an id or an address. householdsKept comes
// first because it is the one an admin acts on: a household the erasure left
// as it was, because other people (or an ambiguous record) still share it.
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
}

// newErasureReport turns the household step's counts into the response body.
func newErasureReport(c householdErasureCounts) erasureReportJSON {
	return erasureReportJSON(c)
}
