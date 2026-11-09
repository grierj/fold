package main

import (
	"time"

	"fold/models"
)

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// formatWhen is how emails and notices write an event time, in the
// reader's zone with its abbreviation: "Sat, Oct 17 · 8:00 PM MDT".
func formatWhen(t time.Time, loc *time.Location) string {
	return t.In(loc).Format("Mon, Jan 2 · 3:04 PM MST")
}

func visWord(v string) string {
	switch v {
	case models.VisibilityPublic:
		return "public"
	case models.VisibilityPrivate:
		return "private"
	case models.VisibilityInviteOnly:
		return "invite-only"
	default:
		return "closed"
	}
}

// ---------------------------------------------------------------------------
// Email delivery (5.12, M1.2). notify.deliver uses these recipient facts and
// rendering helpers. Sensitive kinds use the link-only RenderEmailSafe
// variant, which names nobody.
// ---------------------------------------------------------------------------

func clipRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}
