package main

import (
	"strings"
	"unicode/utf8"
)

// The length rules for group and event text. Each field has one limit, counted
// in characters, and one refusal; every create and edit path asks these.
type textField struct {
	max     int
	tooLong *apiError
}

var (
	groupNameField   = textField{200, errNameTooLong}
	eventTitleField  = textField{200, errTitleTooLong}
	descriptionField = textField{5000, errDescriptionTooLong}
	locationField    = textField{300, errLocationTooLong}
	tagsField        = textField{500, errTagsTooLong}
)

func (f textField) check(s string) error {
	if utf8.RuneCountInString(s) > f.max {
		return f.tooLong
	}
	return nil
}

func validateGroupText(name, description, location, tags string) error {
	return firstErr(groupNameField.check(name), descriptionField.check(description),
		locationField.check(location), tagsField.check(tags))
}

func validateEventText(title, description, location string) error {
	return firstErr(eventTitleField.check(title), descriptionField.check(description),
		locationField.check(location))
}

func firstErr(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

func strOrEmpty(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// Person fields, shared by every door that takes them (sign-up, invitation
// accept, admin edits, the profile, households). They trim first and count
// characters, like the group and event fields above, so a name in any script
// gets the same room. The web mirrors the limits as maxLength on the inputs.
const (
	maxNameChars      = 100
	maxPhoneChars     = 30
	maxAddressChars   = 500
	maxCareNotesChars = 5000
)

var (
	personNameField  = textField{maxNameChars, errNameTooLong100}
	displayNameField = textField{maxNameChars, errDisplayNameTooLong}
	phoneField       = textField{maxPhoneChars, errPhoneTooLong}
	addressField     = textField{maxAddressChars, errAddressTooLong}
	careNotesField   = textField{maxCareNotesChars, errCareNotesTooLong}
)

func (f textField) clean(s string) (string, error) {
	s = strings.TrimSpace(s)
	return s, f.check(s)
}

func requiredName(s string) (string, error) {
	name, err := personNameField.clean(s)
	if name == "" || err != nil {
		return "", errNameRequired100
	}
	return name, nil
}

func optionalName(s string) (string, error) { return personNameField.clean(s) }

func displayName(s string) (string, error) { return displayNameField.clean(s) }

func phoneNumber(s string) (string, error) { return phoneField.clean(s) }

func postalAddress(s string) (string, error) { return addressField.clean(s) }
