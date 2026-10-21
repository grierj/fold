package main

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"fold/models"
)

// M1.5 acceptance gaps found in the Oct 20 audit: these pin behavior the
// earlier tests covered only for guests or only in the notify package.

// A promoted member gets a "You're in" bell item and email, and the
// email's one-click "I can't come" link cancels the seat.
func TestPromotedMemberGetsYoureInWithCantComeLink(t *testing.T) {
	app := testApp(t)
	admin, gid := openGroup(t, app, "PromoMail")
	eventID, date := seedCappedEvent(t, app, admin, gid, intPtr(1), true)
	var g models.Group
	app.DB.Where("id = ?", gid).First(&g)
	_, c1 := mkUser(t, app, &g, "pm1")
	u2, c2 := mkUser(t, app, &g, "pm2")
	if code, _ := registerAs(c1, eventID, date, "going", nil); code != http.StatusOK {
		t.Fatal("pm1 going")
	}
	if code, _ := registerAs(c2, eventID, date, "going", nil); code != http.StatusOK {
		t.Fatal("pm2 waitlisted")
	}
	var occ models.EventOccurrence
	app.DB.Where("event_id = ?", eventID).First(&occ)
	if code, _ := registerAs(c1, eventID, date, "declined", nil); code != http.StatusOK {
		t.Fatal("pm1 cancel")
	}
	n := findKind(feed(t, c2, ""), "registration.promoted")
	if n == nil || !strings.HasPrefix(n.Title, "You're in") {
		t.Fatalf("bell: %+v", n)
	}
	if strings.Contains(n.Body, "/guest/manage/") {
		t.Fatal("bell item shows the manage token")
	}
	m := latestEmail(t, app, testEmail("pm2"))
	if !strings.Contains(m.Subject, "You're in") || !strings.Contains(m.Body, "Can't come") {
		t.Fatalf("promotion email: %q %q", m.Subject, m.Body)
	}
	tok := manageTokenInEmail(t, m)
	anon := anonClient(t, app, "10.4.0.9")
	apiDo(t, anon, "POST", "/api/v1/guest-registrations/manage/"+tok+"/cancel", nil, http.StatusOK)
	if got := regStatus(t, app, u2.ID, occ.ID); got != models.RegCancelled {
		t.Fatalf("after I can't come: %q, want cancelled", got)
	}
}

// A disabled account's going row is kept but is not named on the event
// page, not counted, and frees its seat (DEC-38).
func TestDisabledRegistrationHiddenFromEventPage(t *testing.T) {
	app := testApp(t)
	admin, gid := openGroup(t, app, "HideDisabled")
	eventID, date := seedCappedEvent(t, app, admin, gid, intPtr(1), false)
	var g models.Group
	app.DB.Where("id = ?", gid).First(&g)
	u1, c1 := mkUser(t, app, &g, "hd1")
	_, c2 := mkUser(t, app, &g, "hd2")
	if code, _ := registerAs(c1, eventID, date, "going", nil); code != http.StatusOK {
		t.Fatal("hd1 going")
	}
	apiDo(t, admin, "PUT", "/api/v1/admin/users/"+itoa(u1.ID), map[string]any{"status": "disabled"}, http.StatusOK)
	page, _ := getEvent(t, admin, eventID, http.StatusOK)
	o := page.Occurrences[0]
	for _, a := range o.Attendees {
		if a.UserID == u1.ID {
			t.Fatalf("disabled account listed: %+v", o.Attendees)
		}
	}
	if o.Registrations["going"] != 0 {
		t.Fatalf("disabled registration counted: %+v", o.Registrations)
	}
	// Its seat is free: a capacity-1 event without a waitlist takes hd2.
	if code, body := registerAs(c2, eventID, date, "going", nil); code != http.StatusOK || !strings.Contains(body, `"going"`) {
		t.Fatalf("hd2 into freed seat: %d %s", code, body)
	}
}

// After a re-enable drops the registration, a leader can still add the
// person over capacity by explicit choice (DEC-47).
func TestDroppedOnReenableCanBeAddedOverCapacity(t *testing.T) {
	app := testApp(t)
	admin, gid := openGroup(t, app, "DropAdd")
	eventID, date := seedCappedEvent(t, app, admin, gid, intPtr(1), false)
	var g models.Group
	app.DB.Where("id = ?", gid).First(&g)
	u1, c1 := mkUser(t, app, &g, "da1")
	_, c2 := mkUser(t, app, &g, "da2")
	registerAs(c1, eventID, date, "going", nil)
	apiDo(t, admin, "PUT", "/api/v1/admin/users/"+itoa(u1.ID), map[string]any{"status": "disabled"}, http.StatusOK)
	if code, _ := registerAs(c2, eventID, date, "going", nil); code != http.StatusOK {
		t.Fatal("da2 going")
	}
	apiDo(t, admin, "PUT", "/api/v1/admin/users/"+itoa(u1.ID), map[string]any{"status": "active"}, http.StatusOK)
	var occ models.EventOccurrence
	app.DB.Where("event_id = ?", eventID).First(&occ)
	var n int64
	app.DB.Model(&models.Registration{}).Where("user_id = ? AND occurrence_id = ?", u1.ID, occ.ID).Count(&n)
	if n != 0 {
		t.Fatal("registration not dropped")
	}
	apiDo(t, admin, "POST", fmt.Sprintf("/api/v1/events/%d/registrations/add", eventID),
		map[string]any{"date": date, "userId": u1.ID, "overCapacity": true}, http.StatusOK)
	if got := regStatus(t, app, u1.ID, occ.ID); got != models.RegGoing {
		t.Fatalf("added back: %q", got)
	}
}

// Time and place changes email each going or waitlisted person too, not
// only the bell.
func TestOccurrenceChangeEmailsGoingAndWaitlisted(t *testing.T) {
	app := testApp(t)
	admin, gid := openGroup(t, app, "ChangeMail")
	eventID, date := seedCappedEvent(t, app, admin, gid, intPtr(1), true)
	var g models.Group
	app.DB.Where("id = ?", gid).First(&g)
	_, c1 := mkUser(t, app, &g, "cm1")
	_, c2 := mkUser(t, app, &g, "cm2")
	_, c3 := mkUser(t, app, &g, "cm3")
	registerAs(c1, eventID, date, "going", nil)
	registerAs(c2, eventID, date, "going", nil) // waitlisted
	registerAs(c3, eventID, date, "maybe", nil)
	apiDo(t, admin, "PUT", fmt.Sprintf("/api/v1/events/%d/occurrences/%s", eventID, date),
		map[string]any{"location": "Annex"}, http.StatusOK)
	for _, tc := range []struct {
		name string
		want bool
	}{{"cm1", true}, {"cm2", true}, {"cm3", false}} {
		m, err := devOutbox(t, app).LatestTo(testEmail(tc.name))
		got := err == nil && strings.HasPrefix(m.Subject, "Updated:")
		if got != tc.want {
			t.Fatalf("%s change email = %v, want %v (%+v)", tc.name, got, tc.want, m)
		}
	}
}

// Event items expire at the occurrence end, through the real API path,
// and then show under Archived only.
func TestEventItemsArchiveAfterOccurrenceEnds(t *testing.T) {
	app := testApp(t)
	admin, gid := openGroup(t, app, "Expire")
	eventID, date := seedCappedEvent(t, app, admin, gid, intPtr(5), true)
	var g models.Group
	app.DB.Where("id = ?", gid).First(&g)
	u, c := mkUser(t, app, &g, "ex1")
	registerAs(c, eventID, date, "going", nil)
	apiDo(t, admin, "PUT", fmt.Sprintf("/api/v1/events/%d/occurrences/%s", eventID, date),
		map[string]any{"location": "Annex"}, http.StatusOK)
	var occ models.EventOccurrence
	app.DB.Where("event_id = ?", eventID).First(&occ)
	var row models.Notification
	if err := app.DB.Where("user_id = ? AND kind = ?", u.ID, "event.changed").First(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.ExpiresAt == nil || !row.ExpiresAt.Equal(occ.EndsAt) {
		t.Fatalf("expires_at = %v, want occurrence end %v", row.ExpiresAt, occ.EndsAt)
	}
	if findKind(feed(t, c, ""), "event.changed") == nil {
		t.Fatal("not in the inbox before the end")
	}
	// Move the clock past the end by moving the expiry into the past.
	past := time.Now().Add(-time.Minute)
	app.DB.Model(&models.Notification{}).Where("id = ?", row.ID).Update("expires_at", past)
	if findKind(feed(t, c, ""), "event.changed") != nil {
		t.Fatal("still in the inbox after the end")
	}
	if findKind(feed(t, c, "archived"), "event.changed") == nil {
		t.Fatal("not under Archived after the end")
	}
	var kept int64
	app.DB.Model(&models.Notification{}).Where("id = ?", row.ID).Count(&kept)
	if kept != 1 {
		t.Fatal("expired item was deleted, want archived")
	}
}
