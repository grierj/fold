package main

import (
	"bytes"
	"encoding/json"
)

// Group member limits (DEC-39). A group may cap its active memberships
// (a class in a room); nil means unlimited. Self-join is refused when full,
// and there is no waitlist. Leaders and admins go over only by explicit
// choice (overLimit), which is audited. A re-enabled account whose group is
// full is left out of it and told to ask a leader (DEC-47); a leader can
// then use the audited over-limit add.

// parseMemberLimit reads memberLimit from a body: null clears it, a
// positive whole number sets it. raw is nil when the field is absent.
func parseMemberLimit(raw json.RawMessage) (limit *int, set bool, msg string) {
	if raw == nil {
		return nil, false, ""
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, true, ""
	}
	var n int
	if json.Unmarshal(raw, &n) != nil || n < 1 || n > 100000 {
		return nil, false, "member limit must be a positive whole number, or empty for no limit"
	}
	return &n, true, ""
}
