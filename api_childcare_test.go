package main

import (
	"encoding/json"
	"fold/models"
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

func TestEffectiveNamedChildcare(t *testing.T) {
	personID := uint(1)
	for _, tt := range []struct {
		name string
		off  bool
		r    models.Registration
		want int
	}{
		{"unnamed party", false, models.Registration{ChildcareCount: 2}, 2},
		{"remote account drop in remains", false, models.Registration{AttendanceMode: models.AttendanceRemote, ChildcareCount: 2}, 2},
		{"named explicit", false, models.Registration{HouseholdPersonID: &personID, ChildcareRequested: true, AttendanceMode: models.AttendanceInPerson}, 1},
		{"named default off", false, models.Registration{HouseholdPersonID: &personID}, 0},
		{"remote named never", false, models.Registration{HouseholdPersonID: &personID, ChildcareRequested: true, AttendanceMode: models.AttendanceRemote}, 0},
		{"event switched off", true, models.Registration{HouseholdPersonID: &personID, ChildcareRequested: true}, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := effectiveChildcare(&models.Event{ChildcareAvailable: !tt.off}, tt.r); got != tt.want {
				t.Fatalf("got %d want %d", got, tt.want)
			}
		})
	}
}
