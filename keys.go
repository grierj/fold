package main

import (
	"crypto/sha256"
	"errors"
)

// errNoSessionSecret is why no key can be derived. Config refuses an empty
// SESSION_SECRET outside tests, so this guards a misbuilt App, not a live one.
var errNoSessionSecret = errors.New("SESSION_SECRET is empty, so no key can be derived from it")

// deriveKey is the 32-byte key for one purpose, SHA-256 over
// "fold:<purpose>:" and the session secret. A purpose names its use and its
// version ("vapid:v1"), so no two uses share a key and a format change picks a
// new purpose. The derivation is part of what keeps old tokens and sealed rows
// valid: never change an existing purpose's bytes. An empty secret is an error;
// a key hashed from the prefix alone would be public.
func deriveKey(secret []byte, purpose string) ([]byte, error) {
	if len(secret) == 0 {
		return nil, errNoSessionSecret
	}
	k := sha256.Sum256(append([]byte("fold:"+purpose+":"), secret...))
	return k[:], nil
}
