package main

import (
	"encoding/json"
	"testing"
)

func TestParseChildcareCapacity(t *testing.T) {
	for _, tt := range []struct {
		raw string
		set bool
		cap *int
		bad bool
	}{
		{"", false, nil, false}, {"null", true, nil, false}, {"1", true, intPtr(1), false}, {"100000", true, intPtr(100000), false},
		{"0", false, nil, true}, {"-1", false, nil, true}, {"1.5", false, nil, true}, {`"1"`, false, nil, true}, {"100001", false, nil, true},
	} {
		cap, set, msg := parseChildcareCapacity(json.RawMessage(tt.raw))
		if !intPtrEqual(cap, tt.cap) || set != tt.set || (msg != "") != tt.bad {
			t.Errorf("%q: cap=%v set=%v msg=%q", tt.raw, cap, set, msg)
		}
	}
}
