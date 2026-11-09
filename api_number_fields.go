package main

import (
	"bytes"
	"encoding/json"
)

// Bounds for the whole-number-or-null fields of event and group bodies. Error
// copy states them too (api_error_values.go); TestNumberFieldBoundsMatchErrorCopy
// keeps the two together.
const (
	maxSeatLimit         = 100000 // capacity, remote and childcare capacity, member limit
	maxGuestsAllowed     = 5
	maxChildcareCount    = 20
	maxChildcareAgesNote = 200
)

// numberField is a body field that is a whole number from min to max, or null to
// clear it. Each field has one rule; a handler adds only its own error.
type numberField struct{ min, max int }

var (
	seatLimitField = numberField{1, maxSeatLimit}
	dateSeatsField = numberField{0, maxSeatLimit} // 0 is read by each caller: it closes a date's pool, and is refused for an event
	guestsField    = numberField{0, maxGuestsAllowed}
)

// parse reads raw. present says whether the field was in the body: an absent
// field is not set; null is set with a nil number; a number out of range or not
// whole is not ok.
func (f numberField) parse(raw json.RawMessage, present bool) (n *int, set, ok bool) {
	if !present {
		return nil, false, true
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, true, true
	}
	var v int
	if json.Unmarshal(raw, &v) != nil || v < f.min || v > f.max {
		return nil, false, false
	}
	return &v, true, true
}

func parseCapacity(raw *json.RawMessage) (cap *int, set bool, err error) {
	if raw == nil {
		return nil, false, nil
	}
	n, set, ok := seatLimitField.parse(*raw, true)
	if !ok {
		return nil, false, errInvalidCapacity
	}
	return n, set, nil
}

// parseRemoteCapacity permits 0 only for a per-date override, to close
// remote seats on that date. For an event, uncheck remote attendance.
func parseRemoteCapacity(raw json.RawMessage, allowZero bool) (cap *int, set bool, err error) {
	n, set, ok := dateSeatsField.parse(raw, len(raw) != 0)
	if !ok {
		if allowZero {
			return nil, false, errInvalidRemoteCapacityOverride
		}
		return nil, false, errInvalidRemoteCapacity
	}
	if n != nil && *n == 0 && !allowZero {
		return nil, false, errRemoteCapacityZero
	}
	return n, set, nil
}

func parseGuestsAllowed(raw *json.RawMessage) (n int, set bool, err error) {
	if raw == nil {
		return 0, false, nil
	}
	v, set, ok := guestsField.parse(*raw, true)
	if !ok {
		return 0, false, errInvalidGuests
	}
	if v == nil {
		return 0, set, nil
	}
	return *v, set, nil
}

// parseChildcareCapacity: a value RawMessage distinguishes an omitted capacity
// from explicit null. 0 is refused: uncheck childcare instead.
func parseChildcareCapacity(raw json.RawMessage) (*int, bool, error) {
	n, set, ok := dateSeatsField.parse(raw, len(raw) != 0)
	if !ok {
		return nil, false, errInvalidChildcareCapacity
	}
	if n != nil && *n == 0 {
		return nil, false, errChildcareCapacityZero
	}
	return n, set, nil
}

func parseMemberLimit(raw json.RawMessage) (limit *int, set bool, msg string) {
	n, set, ok := seatLimitField.parse(raw, raw != nil)
	if !ok {
		return nil, false, "member limit must be a positive whole number, or empty for no limit"
	}
	return n, set, ""
}
