package main

import (
	"fold/internal/policy"
	"time"

	"fold/internal/content"
	"fold/models"
)

// messageEvent is the event a message is about, as its merge fields read it:
// the title shown (a single-date audience's own title when the date has one),
// the event's zone, and the id its page link is built from. A real send, a
// composer preview and a reminder's subject all derive their event fields
// from one of these, so the three cannot disagree about a title, a link or
// the event's clock.
type messageEvent struct {
	ID    uint
	Title string
	TZID  string
}

// newMessageEvent reads ev for au. titleOverride is the date's own title; it
// applies only to a one-date audience.
func newMessageEvent(ev models.Event, au audienceJSON, titleOverride *string) messageEvent {
	f := messageEvent{ID: ev.ID, Title: ev.Title, TZID: ev.TZID}
	if au.Type == policy.AudienceOccurrence && titleOverride != nil && *titleOverride != "" {
		f.Title = *titleOverride
	}
	return f
}

// eventPath is the event's page.
func (f messageEvent) eventPath() string { return "/events/" + idString(f.ID) }

// timeText is a date's start on the event's clock, as a merge field and a
// reminder's subject show it.
func (f messageEvent) timeText(startsAt time.Time) string {
	return formatWhen(startsAt, eventZone(f.TZID))
}

// fields are the merge fields every message about the event can use: its
// title, its page as both the event link and (until a reader's own date is
// known) the manage link, and, with startsAt, the date's time.
func (f messageEvent) fields(link string, startsAt *time.Time) map[string]string {
	vals := map[string]string{content.FieldEventTitle: f.Title, content.FieldEventLink: link, content.FieldManageLink: link}
	if startsAt != nil {
		vals[content.FieldEventTime] = f.timeText(*startsAt)
	}
	return vals
}

// eventFields is fields with the church's link to the event's page.
func (a *App) eventFields(f messageEvent, startsAt *time.Time) map[string]string {
	return f.fields(a.emailLink(f.eventPath()), startsAt)
}
