package main

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
)

// The bounds are named once in api_number_fields.go; the error copy quotes them.
func TestNumberFieldBoundsMatchErrorCopy(t *testing.T) {
	for _, tt := range []struct {
		err  *apiError
		want []int
	}{
		{errInvalidChildcareCapacity, []int{1, maxSeatLimit}},
		{errInvalidRemoteCapacity, []int{1, maxSeatLimit}},
		{errInvalidRemoteCapacityOverride, []int{0, maxSeatLimit}},
		{errInvalidGuests, []int{0, maxGuestsAllowed}},
		{errInvalidChildcareCount, []int{0, maxChildcareCount}},
	} {
		for _, n := range tt.want {
			if !strings.Contains(tt.err.Message, strconv.Itoa(n)) {
				t.Errorf("%s: copy %q does not state the bound %d", tt.err.Code, tt.err.Message, n)
			}
		}
	}
}

func TestMemberLimitAndCapacityShareOneRule(t *testing.T) {
	for _, tt := range []struct {
		raw     string
		want    *int
		set, ok bool
	}{
		{"", nil, false, true}, {"null", nil, true, true}, {"1", intPtr(1), true, true}, {"100000", intPtr(100000), true, true},
		{"0", nil, false, false}, {"100001", nil, false, false}, {`"3"`, nil, false, false},
	} {
		raw := json.RawMessage(tt.raw)
		var rawPtr *json.RawMessage
		if tt.raw != "" {
			rawPtr = &raw
		}
		limit, set, msg := parseMemberLimit(rawPtrOrNil(rawPtr))
		capacity, capSet, err := parseCapacity(rawPtr)
		if !intPtrEqual(limit, tt.want) || set != tt.set || (msg == "") != tt.ok || !intPtrEqual(capacity, tt.want) || capSet != tt.set || (err == nil) != tt.ok {
			t.Errorf("%q: member limit (%v %t %q), capacity (%v %t %v)", tt.raw, limit, set, msg, capacity, capSet, err)
		}
	}
}

func rawPtrOrNil(p *json.RawMessage) json.RawMessage {
	if p == nil {
		return nil
	}
	return *p
}
