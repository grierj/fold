package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"fold/internal/content"
	"fold/internal/mail"
	"fold/internal/policy"
	"fold/internal/providers"
	"fold/models"

	"gorm.io/gorm"
)

// An announcement's event fields are filled when each email row or push goes
// out, by what its reader may see of the event or date then
// (readerEventValues, message_event_visibility.go). These tests seed a
// message that is still sending with queued rows (what preparation leaves),
// change the event or the reader, and run the real batch or push job, so the
// fake provider tells a current title from the one they were prepared with.

// deliverySender is the email provider: it records each message and can fail
// the first few sends or run a step after each one.
type deliverySender struct {
	mu    sync.Mutex
	msgs  []mail.Message
	fail  int
	after func(n int)
}

func (s *deliverySender) SendEmail(_ context.Context, m mail.Message) (string, error) {
	s.mu.Lock()
	s.msgs = append(s.msgs, m)
	n := len(s.msgs)
	s.mu.Unlock()
	if s.after != nil {
		s.after(n)
	}
	if n <= s.fail {
		return "", &providers.SendError{Provider: "fake", Code: 503, Message: "service unavailable"}
	}
	return "fake-" + itoa(uint(n)), nil
}

func (s *deliverySender) Capabilities() providers.EmailCaps { return providers.EmailCaps{Name: "fake"} }

// to is the last message sent to addr.
func (s *deliverySender) to(t *testing.T, addr string) mail.Message {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := len(s.msgs) - 1; i >= 0; i-- {
		if s.msgs[i].To == addr {
			return s.msgs[i]
		}
	}
	t.Fatalf("nothing was sent to %s (%d sent)", addr, len(s.msgs))
	return mail.Message{}
}

func (s *deliverySender) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.msgs)
}

// deliveryBodyText puts each event field on its own place in the plain text,
// so a field that is filled is told from one that is blank.
const deliveryBodyText = "{{event_title}} at {{event_time}}. {{event_link}} {{manage_link}}"

func toRow(u *models.User) models.MessageRecipient {
	return models.MessageRecipient{UserID: u.ID, Address: u.Email}
}

// seedSending is a message that is going out, by author, with a queued email
// row for each of rows, as preparation leaves them. It returns the message and
// its rows' ids.
func seedSending(t *testing.T, app *App, author *models.User, au audienceJSON, rows ...models.MessageRecipient) (uint, []uint) {
	t.Helper()
	raw, err := json.Marshal(au)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	m := models.Message{AuthorID: &author.ID, Status: models.MessageSending, Subject: readVisSubject,
		BodyHTML: content.SanitizeHTML(readVisBody), BodyText: deliveryBodyText, Audience: string(raw), ScheduledAt: &now}
	if err := app.DB.Create(&m).Error; err != nil {
		t.Fatal(err)
	}
	ids := []uint{}
	for i := range rows {
		rows[i].MessageID, rows[i].Channel, rows[i].Status = m.ID, models.DeliveryEmail, models.RecipientQueued
		if err := app.DB.Create(&rows[i]).Error; err != nil {
			t.Fatal(err)
		}
		ids = append(ids, rows[i].ID)
	}
	return m.ID, ids
}

func deliverRows(app *App, s *deliverySender, id uint, ids []uint) error {
	app.Mailer = s
	return app.deliverMessageBatchOnce(context.Background(), messageBatchJob{MessageID: id, RecipientIDs: ids})
}

// checkDelivered asserts what one email said: the subject and the plain-text
// line holding each event field exactly as w gives them (readWant: empty is
// "nothing"), and, for a blank, that none of forbid reached any part of it.
// copy is a guardian copy, whose subject and text carry the copy note.
func checkDelivered(t *testing.T, label string, msg mail.Message, first string, w readWant, copy bool, forbid ...string) {
	t.Helper()
	subject := w.title + " news for " + first
	body := msg.Text
	if copy {
		subject = "Copy: " + subject
		i := strings.Index(body, "\n\n")
		if i < 0 {
			t.Fatalf("%s: no copy note in %q", label, msg.Text)
		}
		body = body[i+2:]
	}
	if want := strings.TrimSpace(subject); msg.Subject != want {
		t.Errorf("%s: subject %q, want %q", label, msg.Subject, want)
	}
	if want := w.title + " at " + w.when + ". " + w.event + " " + w.manage; !strings.HasPrefix(body, want) {
		t.Errorf("%s: text %q, want it to start %q", label, body, want)
	}
	if w == (readWant{}) {
		for _, bad := range forbid {
			for part, s := range map[string]string{"subject": msg.Subject, "text": msg.Text, "html": msg.HTML} {
				if strings.Contains(s, bad) {
					t.Errorf("%s: %q reached the %s", label, bad, part)
				}
			}
		}
	}
}

// blankForbid is everything a reader who may see nothing of f's event must
// never be sent: the title it had, the one it is renamed to, the date's own
// title, the event's page (which every date link starts with) and the time.
func blankForbid(f readVisFixture) []string {
	return []string{"Vis night", "Renamed night", "Special night", f.eventURL(), f.when(f.occs[0])}
}

type liveCase struct {
	name     string
	vis      string
	member   bool
	change   func(t *testing.T, f readVisFixture)
	date, sr func(f readVisFixture) readWant
}

func liveSeen(f readVisFixture, title string) readWant {
	return readWant{title: title, when: f.when(f.occs[0]), event: f.eventURL(), manage: f.dateURL(f.occs[0])}
}

func seriesOnly(f readVisFixture) readWant {
	return readWant{title: "Vis night", event: f.eventURL(), manage: f.eventURL()}
}

func noEventFields(readVisFixture) readWant { return readWant{} }

// A change after preparation (a rename, a tightened event or date, a lost
// membership, a cancelled or deleted event) reaches the email: the reader
// gets what the read page would give them now, per field, never the event as
// it was when the row was queued.
func TestEmailDeliveryUsesTheEventAsTheReaderMaySeeItNow(t *testing.T) {
	cases := []liveCase{
		{"a rename", models.VisibilityPublic, false,
			func(t *testing.T, f readVisFixture) { f.rename(t, "Renamed night") },
			func(f readVisFixture) readWant { return liveSeen(f, "Renamed night") },
			func(f readVisFixture) readWant { return liveSeen(f, "Renamed night") }},
		{"an event tightened to invite only", models.VisibilityPublic, false,
			func(t *testing.T, f readVisFixture) {
				f.rename(t, "Renamed night")
				if err := f.app.DB.Model(&models.Event{}).Where("id = ?", f.ev.ID).Update("visibility", models.VisibilityInviteOnly).Error; err != nil {
					t.Fatal(err)
				}
			}, noEventFields, noEventFields},
		{"a member who left", models.VisibilityInviteOnly, true,
			func(t *testing.T, f readVisFixture) {
				f.setMembership(t, f.reader, models.MembershipRemoved)
				f.rename(t, "Renamed night")
			}, noEventFields, noEventFields},
		{"one date closed by its override", models.VisibilityPublic, false,
			func(t *testing.T, f readVisFixture) {
				f.setDate(t, f.occs[0], "visibility_override", models.VisibilityInviteOnly)
			},
			noEventFields, func(f readVisFixture) readWant { return seriesOnly(f) }},
		{"a cancelled date", models.VisibilityPublic, false,
			func(t *testing.T, f readVisFixture) {
				f.setDate(t, f.occs[0], "title_override", "Special night")
				f.setDate(t, f.occs[0], "status", models.OccurrenceCancelled)
			},
			func(f readVisFixture) readWant { return seriesOnly(f) }, func(f readVisFixture) readWant { return seriesOnly(f) }},
		{"a deleted event", models.VisibilityPublic, false,
			func(t *testing.T, f readVisFixture) {
				if err := f.app.DB.Exec("UPDATE events SET deleted_at = now() WHERE id = ?", f.ev.ID).Error; err != nil {
					t.Fatal(err)
				}
			}, noEventFields, noEventFields},
		{"a date with its own title", models.VisibilityPublic, false,
			func(t *testing.T, f readVisFixture) {
				f.setDate(t, f.occs[0], "title_override", "Special night")
				f.rename(t, "Renamed night")
			},
			func(f readVisFixture) readWant { return liveSeen(f, "Special night") },
			func(f readVisFixture) readWant { return liveSeen(f, "Renamed night") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newReadVisFixture(t, tc.vis)
			if tc.member {
				addMember(t, f.app, f.reader, &f.group, models.MembershipMember)
			}
			f.register(t, f.reader, f.occs[0])
			dateMsg, dateRows := seedSending(t, f.app, f.leaderU, f.occAudience(f.occs[0]), toRow(f.reader))
			seriesMsg, seriesRows := seedSending(t, f.app, f.leaderU, f.seriesAudience(), toRow(f.reader))
			tc.change(t, f)
			first := firstName(f.reader.PublicName())
			for _, run := range []struct {
				label string
				id    uint
				ids   []uint
				want  readWant
			}{
				{"a one-date audience", dateMsg, dateRows, tc.date(f)},
				{"a series audience", seriesMsg, seriesRows, tc.sr(f)},
			} {
				s := &deliverySender{}
				if err := deliverRows(f.app, s, run.id, run.ids); err != nil {
					t.Fatalf("%s: %v", run.label, err)
				}
				checkDelivered(t, run.label, s.to(t, f.reader.Email), first, run.want, false, blankForbid(f)...)
			}
		})
	}
}

// A transient email failure leaves the row queued; the retry rechecks, so a
// rename and a tightening made after the failed attempt are what it sees.
func TestEmailRetryRechecksEventFields(t *testing.T) {
	f := newReadVisFixture(t, models.VisibilityPublic)
	f.register(t, f.reader, f.occs[0])
	id, ids := seedSending(t, f.app, f.leaderU, f.seriesAudience(), toRow(f.reader))
	first := firstName(f.reader.PublicName())

	failing := &deliverySender{fail: 1}
	if err := deliverRows(f.app, failing, id, ids); err == nil {
		t.Fatal("a transient failure did not stop the batch")
	}
	checkDelivered(t, "the failed attempt", failing.to(t, f.reader.Email), first, liveSeen(f, "Vis night"), false)

	f.rename(t, "Renamed night")
	if err := f.app.DB.Model(&models.Event{}).Where("id = ?", f.ev.ID).Update("visibility", models.VisibilityInviteOnly).Error; err != nil {
		t.Fatal(err)
	}
	retry := &deliverySender{}
	if err := deliverRows(f.app, retry, id, ids); err != nil {
		t.Fatal(err)
	}
	checkDelivered(t, "the retry", retry.to(t, f.reader.Email), first, readWant{}, false, blankForbid(f)...)
}

// A change between two rows of one batch reaches the later row.
func TestEmailLaterRowSeesAChangeMadeAfterTheEarlierOne(t *testing.T) {
	f := newReadVisFixture(t, models.VisibilityPublic)
	id, ids := seedSending(t, f.app, f.leaderU, f.seriesAudience(), toRow(f.leaderU), toRow(f.reader))
	s := &deliverySender{after: func(n int) {
		if n == 1 {
			f.app.DB.Model(&models.Event{}).Where("id = ?", f.ev.ID).Update("title", "Renamed night")
		}
	}}
	if err := deliverRows(f.app, s, id, ids); err != nil {
		t.Fatal(err)
	}
	seen := func(title string) readWant { return readWant{title: title, event: f.eventURL(), manage: f.eventURL()} }
	checkDelivered(t, "the earlier row", s.to(t, f.leaderU.Email), firstName(f.leaderU.PublicName()), seen("Vis night"), false)
	checkDelivered(t, "the later row", s.to(t, f.reader.Email), firstName(f.reader.PublicName()), seen("Renamed night"), false)
}

// An administrator sees any event, but only while they are one: demoted after
// the batch began and before their own row renders, the later row gets no
// event fields of a group they are not in.
func TestEmailRowDoesNotUseAnAdministratorsCachedPrivilege(t *testing.T) {
	f := newReadVisFixture(t, models.VisibilityInviteOnly)
	if err := f.app.DB.Model(&models.User{}).Where("id = ?", f.reader.ID).Update("role", models.RoleAdmin).Error; err != nil {
		t.Fatal(err)
	}
	first := firstName(f.reader.PublicName())
	seen := readWant{title: "Vis night", event: f.eventURL(), manage: f.eventURL()}

	// Control: still an administrator, the reader sees the hidden event.
	cid, cids := seedSending(t, f.app, f.leaderU, f.seriesAudience(), toRow(f.reader))
	control := &deliverySender{}
	if err := deliverRows(f.app, control, cid, cids); err != nil {
		t.Fatal(err)
	}
	checkDelivered(t, "an administrator", control.to(t, f.reader.Email), first, seen, false)

	id, ids := seedSending(t, f.app, f.leaderU, f.seriesAudience(), toRow(f.leaderU), toRow(f.reader))
	s := &deliverySender{after: func(n int) {
		if n == 1 {
			f.app.DB.Model(&models.User{}).Where("id = ?", f.reader.ID).Update("role", models.RoleMember)
		}
	}}
	if err := deliverRows(f.app, s, id, ids); err != nil {
		t.Fatal(err)
	}
	checkDelivered(t, "the leader's row", s.to(t, f.leaderU.Email), firstName(f.leaderU.PublicName()), seen, false)
	checkDelivered(t, "the demoted administrator's row", s.to(t, f.reader.Email), first, readWant{}, false, blankForbid(f)...)
}

// A guardian copy carries the teen's own message with the teen's values, so
// its event fields follow what the teen may see now, not what the guardian,
// here an administrator, may.
func TestGuardianCopyEventFieldsFollowTheTeensAccess(t *testing.T) {
	f := newReadVisFixture(t, models.VisibilityInviteOnly)
	teen := f.reader
	addMember(t, f.app, teen, &f.group, models.MembershipMember)
	f.register(t, teen, f.occs[0])
	parent := newTestUser(t, f.app, "parent", "password1234", models.RoleAdmin)
	row := func() models.MessageRecipient {
		r := toRow(parent)
		r.CopyOfUserID = &teen.ID
		return r
	}
	first := firstName(teen.PublicName())

	id, ids := seedSending(t, f.app, f.leaderU, f.occAudience(f.occs[0]), row())
	s := &deliverySender{}
	if err := deliverRows(f.app, s, id, ids); err != nil {
		t.Fatal(err)
	}
	checkDelivered(t, "the teen can see the event", s.to(t, parent.Email), first, liveSeen(f, "Vis night"), true)

	f.setMembership(t, teen, models.MembershipRemoved)
	f.rename(t, "Renamed night")
	id, ids = seedSending(t, f.app, f.leaderU, f.occAudience(f.occs[0]), row())
	s = &deliverySender{}
	if err := deliverRows(f.app, s, id, ids); err != nil {
		t.Fatal(err)
	}
	checkDelivered(t, "the teen lost access", s.to(t, parent.Email), first, readWant{}, true, blankForbid(f)...)
}

// failReads makes every read of a table whose name starts with prefix fail
// until the test ends, through the one handle app.DB shares with every
// transaction and session: a lookup that fails inside the delivery's own
// transaction, which a cancelled context on a copy of the handle cannot reach.
func failReads(t *testing.T, db *gorm.DB, prefix string) {
	t.Helper()
	name := "test:fail-reads:" + prefix
	fail := func(tx *gorm.DB) {
		if strings.HasPrefix(tx.Statement.Table, prefix) {
			_ = tx.AddError(errors.New("lookup failed"))
		}
	}
	if err := db.Callback().Query().Before("gorm:query").Register(name, fail); err != nil {
		t.Fatal(err)
	}
	if err := db.Callback().Row().Before("gorm:row").Register(name, fail); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = db.Callback().Query().Remove(name)
		_ = db.Callback().Row().Remove(name)
	})
}

// A failed lookup fails the delivery: nothing is sent and the row stays
// queued for the retry, never read as "may not see" or "may see". Each table
// the field loader reads is failed in turn, for a one-date and a series
// audience.
func TestEmailLookupFailureFailsTheDelivery(t *testing.T) {
	for _, tc := range []struct {
		table  string
		series bool
	}{
		{"event_occurrences", false},
		{"events", false},
		{"events", true},
		{"registrations", false},
		{"registrations", true},
		{"memberships", false},
		{"memberships", true},
	} {
		name := tc.table + "/one date"
		if tc.series {
			name = tc.table + "/series"
		}
		t.Run(name, func(t *testing.T) {
			f := newReadVisFixture(t, models.VisibilityPublic)
			addMember(t, f.app, f.reader, &f.group, models.MembershipMember)
			f.register(t, f.reader, f.occs[0])
			au := f.occAudience(f.occs[0])
			if tc.series {
				au = f.seriesAudience()
			}
			id, ids := seedSending(t, f.app, f.leaderU, au, toRow(f.reader))
			failReads(t, f.app.DB, tc.table)
			s := &deliverySender{}
			if err := deliverRows(f.app, s, id, ids); err == nil {
				t.Fatal("a failed lookup was not an error")
			}
			if s.count() != 0 {
				t.Fatalf("%d emails went out after a failed lookup", s.count())
			}
			var row models.MessageRecipient
			if err := f.app.DB.First(&row, ids[0]).Error; err != nil || row.Status != models.RecipientQueued {
				t.Fatalf("the row after a failed lookup: %+v %v", row, err)
			}
		})
	}
}

// A one-connection pool cannot serve the event fields from a second
// connection while the claimed row's transaction holds the only one: the
// fields are read through that transaction, so the email is sent. A deadline
// stands in for the hang a regression would be.
func TestEmailEventFieldsNeedOnlyTheClaimedConnection(t *testing.T) {
	oneConnection := func(t *testing.T, app *App) {
		t.Helper()
		sqlDB, err := app.DB.DB()
		if err != nil {
			t.Fatal(err)
		}
		before := sqlDB.Stats().MaxOpenConnections
		sqlDB.SetMaxOpenConns(1)
		t.Cleanup(func() { sqlDB.SetMaxOpenConns(before) })
	}
	deliver := func(t *testing.T, app *App, s *deliverySender, id uint, ids []uint) {
		t.Helper()
		done := make(chan error, 1)
		go func() { done <- deliverRows(app, s, id, ids) }()
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(30 * time.Second):
			t.Fatal("delivery is waiting for a second connection")
		}
	}
	t.Run("a series the reader can see", func(t *testing.T) {
		f := newReadVisFixture(t, models.VisibilityPublic)
		f.register(t, f.reader, f.occs[0])
		dateID, dateIDs := seedSending(t, f.app, f.leaderU, f.occAudience(f.occs[0]), toRow(f.reader))
		seriesID, seriesIDs := seedSending(t, f.app, f.leaderU, f.seriesAudience(), toRow(f.reader))
		oneConnection(t, f.app)
		s := &deliverySender{}
		first := firstName(f.reader.PublicName())
		deliver(t, f.app, s, dateID, dateIDs)
		checkDelivered(t, "a one-date audience", s.to(t, f.reader.Email), first, liveSeen(f, "Vis night"), false)
		deliver(t, f.app, s, seriesID, seriesIDs)
		checkDelivered(t, "a series audience", s.to(t, f.reader.Email), first, liveSeen(f, "Vis night"), false)
	})
	t.Run("an exact-date invitation to a hidden series", func(t *testing.T) {
		f := newOccurrenceReadFixture(t)
		date := f.dates[1]
		dateID, dateIDs := seedSending(t, f.app, f.issuer, audienceJSON{Type: policy.AudienceOccurrence, OccurrenceID: date.ID}, toRow(f.recipient))
		seriesID, seriesIDs := seedSending(t, f.app, f.issuer, audienceJSON{Type: policy.AudienceEventSeries, EventID: f.event.ID}, toRow(f.recipient))
		oneConnection(t, f.app)
		s := &deliverySender{}
		deliver(t, f.app, s, dateID, dateIDs)
		if got := s.to(t, f.recipient.Email); !strings.Contains(got.Subject, "Invited date title") {
			t.Errorf("the invited date: %q", got.Subject)
		}
		deliver(t, f.app, s, seriesID, seriesIDs)
		if got := s.to(t, f.recipient.Email); strings.Contains(got.Subject+got.Text+got.HTML, "Invited date title") {
			t.Errorf("a series announcement borrowed the date's title: %q", got.Subject)
		}
	})
}

// A guest registrant is personalized as themselves but sees what a signed-out
// visitor sees (C5): once the event, or the one date, is no longer public,
// the reminder prepared while it was public goes out without the title, the
// time or either page, however the date was renamed. A member of the group
// beside them still sees it, so the same delivery tells the two apart.
func TestGuestReminderEmailSeesOnlyWhatAVisitorSees(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(t *testing.T, f composeFixture)
		title  string
	}{
		{"the event made private", func(t *testing.T, f composeFixture) {
			if err := f.app.DB.Model(&models.Event{}).Where("id = ?", f.groupEvent.ID).
				Updates(map[string]any{"title": "Renamed night", "visibility": models.VisibilityPrivate}).Error; err != nil {
				t.Fatal(err)
			}
		}, "Renamed night"},
		{"one date made private", func(t *testing.T, f composeFixture) {
			if err := f.app.DB.Model(&models.EventOccurrence{}).Where("id = ?", f.groupOcc.ID).
				Updates(map[string]any{"title_override": "Special night", "visibility_override": models.VisibilityPrivate}).Error; err != nil {
				t.Fatal(err)
			}
		}, "Special night"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newComposeFixture(t)
			f.admin.holdNotices, f.leader.holdNotices = true, true
			guest := newTestUser(t, f.app, "reminder-guest", "password1234", models.RoleMember)
			if err := f.app.DB.Model(guest).Update("status", models.UserGuest).Error; err != nil {
				t.Fatal(err)
			}
			for _, u := range []*models.User{guest, f.teen} {
				if err := f.app.DB.Create(&models.Registration{OccurrenceID: f.groupOcc.ID, UserID: &u.ID, Status: models.RegGoing}).Error; err != nil {
					t.Fatal(err)
				}
			}
			at := f.groupOcc.StartsAt.Add(-24 * time.Hour)
			if err := f.app.sendReminder(context.Background(), reminderJob{OccurrenceID: f.groupOcc.ID, StartsAt: f.groupOcc.StartsAt.Unix(), Minutes: 1440}, at); err != nil {
				t.Fatal(err)
			}
			var m models.Message
			if err := f.app.DB.Where("category = ? AND occurrence_id = ?", models.MessageCategoryReminder, f.groupOcc.ID).First(&m).Error; err != nil {
				t.Fatal(err)
			}
			var rows []models.MessageRecipient
			if err := f.app.DB.Where("message_id = ? AND channel = ? AND user_id IN ?", m.ID, models.DeliveryEmail, []uint{guest.ID, f.teen.ID}).Order("id").Find(&rows).Error; err != nil {
				t.Fatal(err)
			}
			if len(rows) != 2 || rows[0].Status != models.RecipientQueued || rows[1].Status != models.RecipientQueued {
				t.Fatalf("the prepared reminder rows: %+v", rows)
			}
			var ev models.Event
			if err := f.app.DB.First(&ev, f.groupEvent.ID).Error; err != nil {
				t.Fatal(err)
			}
			when := formatWhen(f.groupOcc.StartsAt, eventZone(ev.TZID))
			tc.change(t, f)
			s := &deliverySender{}
			if err := deliverRows(f.app, s, m.ID, []uint{rows[0].ID, rows[1].ID}); err != nil {
				t.Fatal(err)
			}
			if got := s.to(t, f.teen.Email); !strings.Contains(got.Subject, tc.title) {
				t.Errorf("a member of the group no longer sees the date: %q", got.Subject)
			}
			got := s.to(t, guest.Email)
			for _, bad := range []string{tc.title, "Renamed night", "Special night", "Book night", when, f.app.emailLink("/events/" + itoa(ev.ID))} {
				for part, text := range map[string]string{"subject": got.Subject, "text": got.Text, "html": got.HTML} {
					if strings.Contains(text, bad) {
						t.Errorf("%q reached the %s of a guest who may not see the date: %q", bad, part, text)
					}
				}
			}
		})
	}
}

// An exact-date invitation shows the invited date alone, and stops showing it
// when the invitation ends; a series announcement never borrows the date's
// title.
func TestEmailExactDateInvitation(t *testing.T) {
	f := newOccurrenceReadFixture(t)
	date := f.dates[1]
	first := firstName(f.recipient.PublicName())
	dateURL := f.app.emailLink(occurrenceDatePath(f.event.ID, models.FormatCalendarDate(date.OriginalDate)))
	when := formatWhen(date.StartsAt, eventZone("UTC"))
	seeOne := readWant{title: "Invited date title", when: when, manage: dateURL}
	forbid := []string{"Hidden series title", "Invited date title", when, f.app.emailLink("/events/" + itoa(f.event.ID))}
	run := func(label string, au audienceJSON, want readWant) {
		t.Helper()
		id, ids := seedSending(t, f.app, f.issuer, au, toRow(f.recipient))
		s := &deliverySender{}
		if err := deliverRows(f.app, s, id, ids); err != nil {
			t.Fatalf("%s: %v", label, err)
		}
		checkDelivered(t, label, s.to(t, f.recipient.Email), first, want, false, forbid...)
	}
	occ := audienceJSON{Type: policy.AudienceOccurrence, OccurrenceID: date.ID}
	series := audienceJSON{Type: policy.AudienceEventSeries, EventID: f.event.ID}
	run("the invited date", occ, seeOne)
	run("the series, by a titled date", series, readWant{})
	endSeededAccess(t, f.app.DB, f.access.RegistrationID, policy.OccurrenceDeclined, &f.recipient.ID, time.Now().UTC())
	run("the ended invitation", occ, readWant{})
}

// A reminder email, first or retried, is filled by the same rules: the
// registrant who lost the date, or whose date was renamed or cancelled, gets
// only what the read page would give them.
func TestReminderEmailRechecksEventFields(t *testing.T) {
	for _, retry := range []bool{false, true} {
		name := "first send"
		if retry {
			name = "Try again"
		}
		t.Run(name+"/access lost and renamed", func(t *testing.T) {
			f, id, rows, _ := prepareChoiceBatch(t, true, retry, false)
			var ev models.Event
			if err := f.app.DB.First(&ev, f.groupEvent.ID).Error; err != nil {
				t.Fatal(err)
			}
			when := formatWhen(f.groupOcc.StartsAt, eventZone(ev.TZID))
			if err := f.app.DB.Model(&models.Event{}).Where("id = ?", ev.ID).Updates(map[string]any{"title": "Renamed night", "visibility": models.VisibilityInviteOnly}).Error; err != nil {
				t.Fatal(err)
			}
			if err := f.app.DB.Model(&models.Membership{}).Where("group_id = ? AND user_id = ?", f.group.ID, f.teen.ID).Update("status", models.MembershipLeft).Error; err != nil {
				t.Fatal(err)
			}
			s := &deliverySender{}
			if err := deliverRows(f.app, s, id, []uint{rows[0].ID, rows[1].ID}); err != nil {
				t.Fatal(err)
			}
			if got := s.to(t, rows[0].Address); !strings.Contains(got.Subject, "Renamed night") || !strings.Contains(got.Subject, when) {
				t.Errorf("the leader still sees the date: %q", got.Subject)
			}
			lost := s.to(t, rows[1].Address)
			for _, bad := range []string{"Renamed night", "Book night", when, f.app.emailLink("/events/" + itoa(ev.ID))} {
				for part, text := range map[string]string{"subject": lost.Subject, "text": lost.Text, "html": lost.HTML} {
					if strings.Contains(text, bad) {
						t.Errorf("%q reached the %s of a registrant who lost the date: %q", bad, part, text)
					}
				}
			}
		})
		t.Run(name+"/cancelled", func(t *testing.T) {
			f, id, rows, _ := prepareChoiceBatch(t, true, retry, false)
			var ev models.Event
			if err := f.app.DB.First(&ev, f.groupEvent.ID).Error; err != nil {
				t.Fatal(err)
			}
			when := formatWhen(f.groupOcc.StartsAt, eventZone(ev.TZID))
			f.app.DB.Model(&models.Event{}).Where("id = ?", ev.ID).Update("title", "Renamed night")
			if err := f.app.DB.Model(&models.EventOccurrence{}).Where("id = ?", f.groupOcc.ID).Update("status", models.OccurrenceCancelled).Error; err != nil {
				t.Fatal(err)
			}
			s := &deliverySender{}
			if err := deliverRows(f.app, s, id, []uint{rows[0].ID, rows[1].ID}); err != nil {
				t.Fatal(err)
			}
			for _, row := range rows {
				got := s.to(t, row.Address)
				if !strings.Contains(got.Subject, "Renamed night") || strings.Contains(got.Subject+got.Text+got.HTML, when) {
					t.Errorf("a cancelled date gives the series title and no time: %q / %q", got.Subject, got.Text)
				}
			}
		})
	}
}

// A push held for quiet hours is filled when it goes, after the morning
// opens: a rename shows, and an event the person can no longer see leaves no
// title on the lock screen.
func TestAnnouncementPushRechecksEventFieldsAfterQuietHours(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(t *testing.T, f composeFixture)
		title  string
	}{
		{"renamed", func(t *testing.T, f composeFixture) {
			if err := f.app.DB.Model(&models.Event{}).Where("id = ?", f.groupEvent.ID).Update("title", "Renamed night").Error; err != nil {
				t.Fatal(err)
			}
		}, "Renamed night news"},
		{"tightened and left the group", func(t *testing.T, f composeFixture) {
			if err := f.app.DB.Model(&models.Event{}).Where("id = ?", f.groupEvent.ID).Updates(map[string]any{"title": "Renamed night", "visibility": models.VisibilityInviteOnly}).Error; err != nil {
				t.Fatal(err)
			}
			if err := f.app.DB.Model(&models.Membership{}).Where("group_id = ? AND user_id = ?", f.group.ID, f.teen.ID).Update("status", models.MembershipLeft).Error; err != nil {
				t.Fatal(err)
			}
		}, "news"},
		{"deleted", func(t *testing.T, f composeFixture) {
			if err := f.app.DB.Exec("UPDATE events SET deleted_at = now() WHERE id = ?", f.groupEvent.ID).Error; err != nil {
				t.Fatal(err)
			}
		}, "news"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newComposeFixture(t)
			fp := useFakePush(t, f.app)
			clock := quietHours(f.app)
			pushConsentRow(t, f.app, f.teen, "all", models.ConsentOptedIn)
			f.app.DB.Model(f.teen).Update("timezone", "America/Denver")
			phone := newPushDevice(t, f.app, f.teen, "phone")
			if err := f.app.DB.Create(&models.Registration{OccurrenceID: f.groupOcc.ID, UserID: &f.teen.ID, Status: models.RegGoing}).Error; err != nil {
				t.Fatal(err)
			}
			id := f.scheduleNow(t, f.leader, "{{event_title}} news", "<p>{{event_title}} at {{event_time}}</p>",
				map[string]any{"type": "occurrence", "occurrenceId": f.groupOcc.ID})
			runMessageJobs(t, f.app)
			if row := pushRowsOf(t, f.app, id)[f.teen.Email]; row.Status != models.RecipientQueued || fp.total() != 0 {
				t.Fatalf("the push should be waiting for the morning: %+v, pushes %v", row, fp.sent)
			}
			tc.change(t, f)
			clock.set(8, 0, 16)
			makeDue(f.app)
			drainJobs(t, f.app)
			if row := pushRowsOf(t, f.app, id)[f.teen.Email]; row.Status != models.RecipientSent || fp.count(phone.path) != 1 {
				t.Fatalf("in the morning: %+v, pushes %v", row, fp.sent)
			}
			note := pushPayload(t, fp, phone)
			if note.Title != tc.title {
				t.Errorf("lock-screen title %q, want %q", note.Title, tc.title)
			}
			if tc.title == "news" {
				for _, bad := range []string{"Book night", "Renamed night"} {
					if strings.Contains(note.Title+note.Body, bad) {
						t.Errorf("%q reached the lock screen: %q / %q", bad, note.Title, note.Body)
					}
				}
			}
		})
	}
}
