package main

import "unicode/utf8"

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

// check returns the field's refusal when s is longer than its limit.
func (f textField) check(s string) error {
	if utf8.RuneCountInString(s) > f.max {
		return f.tooLong
	}
	return nil
}

// validateGroupText checks every text field of a group.
func validateGroupText(name, description, location, tags string) error {
	return firstErr(groupNameField.check(name), descriptionField.check(description),
		locationField.check(location), tagsField.check(tags))
}

// validateEventText checks every text field of an event.
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

// strOrEmpty is the string a patch field holds, "" when the field was not sent.
func strOrEmpty(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
