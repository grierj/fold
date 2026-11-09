package main

import (
	"encoding/hex"
	"testing"
)

// Existing tokens, sealed rows and signed links stay valid only while each
// purpose derives the same bytes. Never edit these expectations to make a
// change pass; add a new purpose instead.
func TestDeriveKeyBytesAreFrozen(t *testing.T) {
	secret := []byte("frozen-test-secret-32-bytes-long!!")
	for purpose, want := range map[string]string{
		"departure-preview:v3":     "96d92c1e033a2fd0dc5d2691524b86cc6ddb9d2ff1a925a13f06eff45048ab0d",
		"people-selection:v1":      "bc41943d060c79f895f0cc4213c60d9de1d326908ea3df1af6f3cf7124da067c",
		"vapid:v1":                 "952968a58ee9e5b48bb61ddd58a30ee3faa74008524d384f156630cb86b6dfa4",
		"unsubscribe:v1":           "7e3fc3466d564b6f4dc053bb5621e5e29ffc908f1a3578b09fa8d125f63763a4",
		"unsubscribe-address:v1":   "edbc14136a34f0e5a45f6a027eb22ac29999511ffefd8f18bfddb1b5975da79e",
		"erasure-reason-review:v1": "d1a950cfe3505d4403b175118aab46b181eeef6fb3d85e1f2e7589e0a5b20155",
		"guardian-email:v1":        "96bdc17c782503ba03fcd1fa3f892bad8ffeb621a6f54753b49c9c249eafab7c",
	} {
		got, err := deriveKey(secret, purpose)
		if err != nil {
			t.Fatal(err)
		}
		if h := hex.EncodeToString(got); h != want {
			t.Errorf("%s: %s", purpose, h)
		}
	}
	if _, err := deriveKey(nil, "vapid:v1"); err == nil {
		t.Error("an empty secret derived a key")
	}
}
