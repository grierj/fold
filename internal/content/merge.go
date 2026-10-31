package content

import (
	"html"
	"regexp"
	"strings"
)

// Merge fields (PLAN 7.3): {{first_name}} and, in a message to one event's
// people, {{event_title}}, {{event_time}}, {{event_link}} and
// {{manage_link}}. They are filled in for each reader when the message is
// sent, and with the author's own values in a preview or test.
const (
	FieldFirstName  = "first_name"
	FieldEventTitle = "event_title"
	FieldEventTime  = "event_time"
	FieldEventLink  = "event_link"
	FieldManageLink = "manage_link"
)

// MergeFields lists every field, in the order the composer offers them.
var MergeFields = []string{FieldFirstName, FieldEventTitle, FieldEventTime, FieldEventLink, FieldManageLink}

// IsEventField: the field needs a message to one event's people.
func IsEventField(f string) bool { return f != FieldFirstName }

// mergeRe matches anything in double braces, so a misspelled field
// ("{{first name}}") is caught and refused instead of sent as written.
var mergeRe = regexp.MustCompile(`\{\{([^{}]*)\}\}`)

func fieldName(inner string) string { return strings.ToLower(strings.TrimSpace(inner)) }

// FieldsIn lists the distinct fields s uses, known or not, trimmed and
// lowercased.
func FieldsIn(s string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, m := range mergeRe.FindAllStringSubmatch(s, -1) {
		f := fieldName(m[1])
		if !seen[f] {
			seen[f] = true
			out = append(out, f)
		}
	}
	return out
}

// IsMergeField: f is one of MergeFields.
func IsMergeField(f string) bool {
	for _, k := range MergeFields {
		if k == f {
			return true
		}
	}
	return false
}

// Merge fills the fields in s from values; a field without a value becomes
// "". inHTML escapes each value for HTML text or an attribute.
func Merge(s string, values map[string]string, inHTML bool) string {
	return mergeRe.ReplaceAllStringFunc(s, func(m string) string {
		v := values[fieldName(mergeRe.FindStringSubmatch(m)[1])]
		if inHTML {
			return html.EscapeString(v)
		}
		return v
	})
}

// A link the editor made to {{event_link}} or {{manage_link}} isn't a URL
// yet, so the sanitizer would drop it. SanitizeHTML swaps each for a URL
// under this address first and back after; the server fills in the real
// link, always one of its own.
const mergeLinkBase = "https://merge-field.invalid/"

var linkFieldRe = regexp.MustCompile(`(?i)\{\{\s*(event_link|manage_link)\s*\}\}`)

// protectLinkFields swaps link fields, in any case, for their stand-in URL.
func protectLinkFields(s string) string {
	return linkFieldRe.ReplaceAllStringFunc(s, func(m string) string {
		return mergeLinkBase + fieldName(linkFieldRe.FindStringSubmatch(m)[1])
	})
}

func restoreLinkFields(s string) string {
	for _, f := range []string{FieldEventLink, FieldManageLink} {
		s = strings.ReplaceAll(s, mergeLinkBase+f, "{{"+f+"}}")
	}
	return s
}
