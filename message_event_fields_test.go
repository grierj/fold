package main

import (
	"encoding/json"
	"testing"
	"time"

	"fold/internal/content"
	"fold/models"
)

// A real send, a composer preview and a reminder's subject read one event
// (here with a date title and a zone that is not UTC) and must give the same
// title, link and clock.
func TestEventMergeValuesAgreeAcrossSendPreviewAndReminder(t *testing.T) {
	f := newComposeFixture(t)
	const tz = "America/Los_Angeles"
	f.app.DB.Model(&models.Event{}).Where("id = ?", f.groupEvent.ID).Update("tzid", tz)
	f.app.DB.Model(&models.EventOccurrence{}).Where("id = ?", f.groupOcc.ID).Update("title_override", "Special night")
	if err := f.app.DB.Create(&models.Registration{OccurrenceID: f.groupOcc.ID, UserID: &f.leaderUser.ID, Status: models.RegGoing}).Error; err != nil {
		t.Fatal(err)
	}
	au := audienceJSON{Type: "occurrence", OccurrenceID: f.groupOcc.ID}
	raw, _ := json.Marshal(au)
	m := models.Message{Audience: string(raw)}

	sent, err := f.app.rowValues(f.app.DB, models.MessageRecipient{UserID: f.leaderUser.ID}, map[uint]*models.User{f.leaderUser.ID: f.leaderUser}, au, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	preview, err := f.app.sampleValues(&m, f.leaderUser)
	if err != nil {
		t.Fatal(err)
	}
	reminder := messageReminderJSON{EventID: f.groupEvent.ID, EventTitle: "Special night", StartsAt: f.groupOcc.StartsAt, TZID: tz}
	subject := reminderSubject("{{event_title}}|{{event_time}}", reminder)

	for name, vals := range map[string]map[string]string{"send": sent[f.leaderUser.ID], "preview": preview} {
		if vals[content.FieldEventTitle] != "Special night" {
			t.Errorf("%s title = %q", name, vals[content.FieldEventTitle])
		}
		if vals[content.FieldEventLink] != f.app.emailLink("/events/"+itoa(f.groupEvent.ID)) {
			t.Errorf("%s link = %q", name, vals[content.FieldEventLink])
		}
		if want := formatWhen(f.groupOcc.StartsAt, eventZone(tz)); vals[content.FieldEventTime] != want {
			t.Errorf("%s time = %q, want %q", name, vals[content.FieldEventTime], want)
		}
		if got := vals[content.FieldEventTitle] + "|" + vals[content.FieldEventTime]; got != subject {
			t.Errorf("%s gave %q, the reminder subject %q", name, got, subject)
		}
	}
}
