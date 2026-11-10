package main

import (
	"errors"
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"
)

// The length rules for group and event text. Each field has one limit, counted
// in characters, and one refusal; every create and edit path asks these.
type textField struct {
	max     int
	tooLong *apiError
	// singleLine refuses every control character and the line and paragraph
	// separators, for a name or a phone number. Every field refuses NUL, the one
	// character Postgres cannot store. The multi-line fields allow the rest on
	// purpose: they may already hold pasted tabs, and saving a record again must
	// not start refusing them.
	singleLine bool
}

// errTextCharacters answers a value with a character that can't be saved. It
// is the same refusal for every address on the public doors (DEC-52), because
// it is decided before any account is looked up.
var errTextCharacters = apiErr(http.StatusBadRequest, "text_characters", "the text has a character that can't be saved")

var (
	groupNameField   = textField{max: 200, tooLong: errNameTooLong}
	eventTitleField  = textField{max: 200, tooLong: errTitleTooLong}
	descriptionField = textField{max: 5000, tooLong: errDescriptionTooLong}
	locationField    = textField{max: 300, tooLong: errLocationTooLong}
	tagsField        = textField{max: 500, tooLong: errTagsTooLong}
)

func (f textField) check(s string) error {
	if utf8.RuneCountInString(s) > f.max {
		return f.tooLong
	}
	for _, r := range s {
		if r == 0 || (f.singleLine && (unicode.IsControl(r) || r == '\u2028' || r == '\u2029')) {
			return errTextCharacters
		}
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
	personNameField  = textField{max: maxNameChars, tooLong: errNameTooLong100, singleLine: true}
	displayNameField = textField{max: maxNameChars, tooLong: errDisplayNameTooLong, singleLine: true}
	phoneField       = textField{max: maxPhoneChars, tooLong: errPhoneTooLong, singleLine: true}
	addressField     = textField{max: maxAddressChars, tooLong: errAddressTooLong}
	careNotesField   = textField{max: maxCareNotesChars, tooLong: errCareNotesTooLong}
)

func (f textField) clean(s string) (string, error) {
	s = strings.TrimSpace(s)
	return s, f.check(s)
}

func requiredName(s string) (string, error) {
	name, err := personNameField.clean(s)
	if errors.Is(err, errTextCharacters) {
		return "", err
	}
	if name == "" || err != nil {
		return "", errNameRequired100
	}
	return name, nil
}

func optionalName(s string) (string, error) { return personNameField.clean(s) }

func displayName(s string) (string, error) { return displayNameField.clean(s) }

func phoneNumber(s string) (string, error) { return phoneField.clean(s) }

func postalAddress(s string) (string, error) { return addressField.clean(s) }
