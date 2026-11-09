package main

// One validator per profile field, shared by every door that accepts it
// (sign-up, invitation accept, admin edits, the profile, households). Each
// trims first and counts characters (runes), never bytes, so a name in any
// script gets the same room, and each answers with a message built from its
// limit. The web mirrors the limits as maxLength on the inputs.

import (
	"strings"
	"unicode/utf8"
)

const (
	maxNameChars    = 100
	maxPhoneChars   = 30
	maxAddressChars = 500
)

// cleanText trims s and reports whether it fits in limit characters.
func cleanText(s string, limit int) (string, bool) {
	s = strings.TrimSpace(s)
	return s, utf8.RuneCountInString(s) <= limit
}

// requiredName is a person's name where one must be given.
func requiredName(s string) (string, error) {
	name, ok := cleanText(s, maxNameChars)
	if name == "" || !ok {
		return "", errNameRequired100
	}
	return name, nil
}

// optionalName is a person's name that may be empty.
func optionalName(s string) (string, error) {
	name, ok := cleanText(s, maxNameChars)
	if !ok {
		return "", errNameTooLong100
	}
	return name, nil
}

func displayName(s string) (string, error) {
	name, ok := cleanText(s, maxNameChars)
	if !ok {
		return "", errDisplayNameTooLong
	}
	return name, nil
}

func phoneNumber(s string) (string, error) {
	phone, ok := cleanText(s, maxPhoneChars)
	if !ok {
		return "", errPhoneTooLong
	}
	return phone, nil
}

func postalAddress(s string) (string, error) {
	addr, ok := cleanText(s, maxAddressChars)
	if !ok {
		return "", errAddressTooLong
	}
	return addr, nil
}
