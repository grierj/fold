package main

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"fold/internal/mail"
	"fold/internal/notify"
	"fold/models"

	"gorm.io/gorm"
)

// No production writer exists in 2a. Fixtures insert retained choices directly.
func seedEmailAddressOff(t *testing.T, app *App, address string, categories ...string) {
	t.Helper()
	for _, cat := range categories {
		if err := app.DB.Create(&models.EmailAddressOptOut{AddressHash: mail.AddressHash(address), Scope: categoryScope(cat), OptedOutAt: time.Now().Add(-time.Hour)}).Error; err != nil {
			t.Fatal(err)
		}
	}
}

func seedAllEmailAddressOff(t *testing.T, app *App, address string) {
	t.Helper()
	seedEmailAddressOff(t, app, address, notify.OptionalEmailCategories()...)
}

func TestReadEmailOffCombinesAccountAndDestination(t *testing.T) {
	app := testApp(t)
	u := newTestUser(t, app, "choice", "password1234", models.RoleMember)
	at := time.Now().Add(-time.Hour)
	for _, row := range []models.Consent{
		{UserID: u.ID, Channel: models.ConsentEmail, Scope: "category:events", Status: models.ConsentOptedOut, Source: models.ConsentSourceWebForm, At: at},
		{UserID: u.ID, Channel: models.ConsentEmail, Scope: "category:events", Status: models.ConsentOptedIn, Source: models.ConsentSourceWebForm, At: at},
		{UserID: u.ID, Channel: models.ConsentEmail, Scope: "category:messages", Status: models.ConsentOptedOut, Source: models.ConsentSourceWebForm, At: at},
		{UserID: u.ID, Channel: models.ConsentPush, Scope: "category:admin", Status: models.ConsentOptedOut, Source: models.ConsentSourceWebForm, At: at},
	} {
		if err := app.DB.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	seedEmailAddressOff(t, app, " OLD@EXAMPLE.COM ", notify.CatGroups, notify.CatEvents)
	dests := []emailDest{{UserID: u.ID, Address: " old@example.com "}, {UserID: u.ID, Address: "current@example.com"}}
	q := countDBReads(t, app.DB)
	off, err := readEmailOff(app.DB, append(dests, dests...))
	q.Stop()
	if err != nil {
		t.Fatal(err)
	}
	if q.Reads() != 1 {
		t.Fatalf("%d statements, want one", q.Reads())
	}
	if !off.Off(u.ID, "OLD@example.com", "category:groups") || !off.Off(u.ID, "old@example.com", "category:events") || !off.Off(u.ID, "current@example.com", "category:messages") || off.Off(u.ID, "current@example.com", "category:events") || off.Off(u.ID, "current@example.com", "category:admin") {
		t.Fatalf("effective choices: %+v", off)
	}
	// An earlier opted-in account row never overrides a retained record.
	// The address arm also works without an account id, and vice versa.
	for _, tc := range []struct {
		dest  emailDest
		scope string
		want  bool
	}{
		{emailDest{Address: "old@example.com"}, "category:events", true},
		{emailDest{UserID: u.ID}, "category:messages", true},
		{emailDest{}, "category:events", false},
	} {
		got, err := readEmailOff(app.DB, []emailDest{tc.dest})
		if err != nil || got.Off(tc.dest.UserID, tc.dest.Address, tc.scope) != tc.want {
			t.Fatalf("%+v: %+v, %v", tc, got, err)
		}
	}
	q = countDBReads(t, app.DB)
	_, err = readEmailOff(app.DB, []emailDest{{}, {}})
	q.Stop()
	if err != nil || q.Reads() != 0 {
		t.Fatalf("empty read: %d, %v", q.Reads(), err)
	}
}

func TestReadEmailOffChunksDestinations(t *testing.T) {
	app := testApp(t)
	var dests []emailDest
	for i := range 1201 {
		d := emailDest{UserID: uint(i + 1), Address: fmt.Sprintf("chunk%d@example.com", i)}
		dests = append(dests, d, d)
		seedEmailAddressOff(t, app, d.Address, notify.CatEvents)
	}
	q := countDBReads(t, app.DB)
	off, err := readEmailOff(app.DB, dests)
	q.Stop()
	if err != nil || q.Reads() != 3 {
		t.Fatalf("chunked read: %d, %v", q.Reads(), err)
	}
	for _, d := range dests {
		if !off.Off(d.UserID, d.Address, "category:events") {
			t.Fatalf("lost destination %+v", d)
		}
	}
}

func TestEmailSettingsHideChoiceSource(t *testing.T) {
	app := testApp(t)
	u := newTestUser(t, app, "settings", "password1234", models.RoleMember)
	seedEmailAddressOff(t, app, u.Email, notify.CatGroups)
	retained, err := app.emailSettingsFor(app.DB, u)
	if err != nil {
		t.Fatal(err)
	}
	if err := app.DB.Where("address_hash = ?", mail.AddressHash(u.Email)).Delete(&models.EmailAddressOptOut{}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := setEmailConsent(app.DB, u.ID, "category:groups", models.ConsentOptedOut, models.ConsentSourceWebForm, "", "", nil); err != nil {
		t.Fatal(err)
	}
	account, err := app.emailSettingsFor(app.DB, u)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(account)
	b, _ := json.Marshal(retained)
	if string(a) != string(b) {
		t.Fatalf("choice source disclosed: %s vs %s", a, b)
	}
	seedEmailAddressOff(t, app, u.Email, notify.CatEvents, notify.CatMessages)
	c := loginAPI(t, app, "settings", "password1234")
	var me struct {
		Settings notificationSettingsJSON `json:"settings"`
	}
	decodeJSON(t, c.do("GET", "/api/v1/me/notification-settings", nil, false), &me)
	anon := newAPIClient(t, app)
	var link tokenSettings
	decodeJSON(t, anon.do("GET", "/api/v1/email-settings/"+app.unsubscribeToken(u.ID, "category:groups"), nil, false), &link)
	if !me.Settings.OptionalEmailOff || !link.EmailSettings.OptionalEmailOff || !reflect.DeepEqual(me.Settings.Kinds, link.EmailSettings.Kinds) {
		t.Fatalf("settings disagree: %+v vs %+v", me, link)
	}
}

func TestAddressOptOutsFilterAudienceSnapshotCopiesAndReminders(t *testing.T) {
	f := newComposeFixture(t)
	parent := newTestUser(t, f.app, "choice-parent", "password1234", models.RoleMember)
	if err := f.app.DB.Create(&models.Setting{Key: settingGuardianCopies, Value: "true"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.app.DB.Create(&models.GuardianLink{GuardianUserID: parent.ID, TeenUserID: &f.teen.ID, Status: models.GuardianConfirmed}).Error; err != nil {
		t.Fatal(err)
	}
	seedEmailAddressOff(t, f.app, f.teen.Email, notify.CatMessages, notify.CatEvents)
	seedEmailAddressOff(t, f.app, parent.Email, notify.CatMessages)
	// Preview and snapshot use identical skip words for either source.
	var preview struct {
		Summary audienceSummaryJSON `json:"summary"`
	}
	decodeJSON(t, f.leader.do("POST", "/api/v1/messages/audience-preview", map[string]any{"audience": groupAudience(f)}, true), &preview)
	if preview.Summary.Skipped[skipOptedOut] != 2 {
		t.Fatalf("preview: %+v", preview.Summary)
	}
	// Keep a second teen eligible, so their guardian's own retained choice is
	// exercised independently of the first teen's skipped direct row.
	copyTeen := newTestUser(t, f.app, "choice-copy-teen", "password1234", models.RoleMember)
	if err := f.app.DB.Model(copyTeen).Update("account_type", models.AccountTeen).Error; err != nil {
		t.Fatal(err)
	}
	addMember(t, f.app, copyTeen, f.group, models.MembershipMember)
	if err := f.app.DB.Create(&models.GuardianLink{GuardianUserID: parent.ID, TeenUserID: &copyTeen.ID, Status: models.GuardianConfirmed}).Error; err != nil {
		t.Fatal(err)
	}
	facts, err := recipientFacts(f.app.DB, []uint{f.teen.ID}, notify.CatMessages)
	if err != nil || len(facts) != 1 || facts[0].Skip != skipOptedOut {
		t.Fatalf("facts %+v: %v", facts, err)
	}
	id := f.scheduleNow(t, f.leader, "Retained choice", "<p>Hi</p>", groupAudience(f))
	if err := f.app.prepareMessageOnce(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	rows := recipientsOf(t, f.app, id)
	for _, key := range []string{f.teen.Email, "copy:" + parent.Email} {
		if r := rows[key]; r.Status != models.RecipientSuppressed || r.Reason != skipOptedOut {
			t.Fatalf("snapshot %s: %+v", key, r)
		}
	}
	if err := f.app.DB.Create(&models.Registration{OccurrenceID: f.groupOcc.ID, UserID: &f.teen.ID, Status: models.RegGoing}).Error; err != nil {
		t.Fatal(err)
	}
	reminderFor(t, f.app, f.groupOcc, 1440)
	var r models.MessageRecipient
	if err := f.app.DB.Joins("JOIN messages m ON m.id = message_recipients.message_id").Where("m.category = ? AND message_recipients.user_id = ?", models.MessageCategoryReminder, f.teen.ID).First(&r).Error; err != nil {
		t.Fatal(err)
	}
	if r.Status != models.RecipientSuppressed || r.Reason != skipEventsOff {
		t.Fatalf("reminder: %+v", r)
	}
}

// blockingChoiceSender pauses the first provider call, outside its mutex, so
// tests can commit changes between rows or after the composer's transaction.
type blockingChoiceSender struct {
	mu       sync.Mutex
	messages []mail.Message
	entered  chan struct{}
	release  chan struct{}
	next     mail.Sender
}

func newBlockingChoiceSender(t *testing.T, next mail.Sender) *blockingChoiceSender {
	t.Helper()
	s := &blockingChoiceSender{entered: make(chan struct{}), release: make(chan struct{}), next: next}
	t.Cleanup(s.resume)
	return s
}

func (s *blockingChoiceSender) resume() {
	s.mu.Lock()
	defer s.mu.Unlock()
	select {
	case <-s.release:
	default:
		close(s.release)
	}
}
func (s *blockingChoiceSender) sent() []mail.Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]mail.Message(nil), s.messages...)
}
func (s *blockingChoiceSender) Send(ctx context.Context, m mail.Message) error {
	s.mu.Lock()
	s.messages = append(s.messages, m)
	first := len(s.messages) == 1
	s.mu.Unlock()
	if first {
		close(s.entered)
		select {
		case <-s.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if s.next != nil {
		return s.next.Send(ctx, m)
	}
	return nil
}

func TestRequiredAccountMailIgnoresRetainedOptOuts(t *testing.T) {
	t.Run("password reset", func(t *testing.T) {
		app := testApp(t)
		u := newTestUser(t, app, "reset-choice", "password1234", models.RoleMember)
		seedAllEmailAddressOff(t, app, u.Email)
		queueResetMail(t, app, u)
		runResetMail(t, app)
		if mailCount(t, app, u.Email) != 1 {
			t.Fatal("retained choice blocked password reset")
		}
	})
	t.Run("invitation", func(t *testing.T) {
		app := testApp(t)
		f := queueHeldInvitationMail(t, app, false, true)
		seedAllEmailAddressOff(t, app, f.inv.Email)
		runResetMail(t, app)
		if mailCount(t, app, f.inv.Email) != 1 {
			t.Fatal("retained choice blocked invitation")
		}
	})
	t.Run("guest verification", func(t *testing.T) {
		app := testApp(t)
		f := queueHeldGuestVerify(t, app, false)
		seedAllEmailAddressOff(t, app, f.email)
		runResetMail(t, app)
		if mailCount(t, app, f.email) != 1 {
			t.Fatal("retained choice blocked guest verification")
		}
	})
}

// Keep the compiler checking this fixture against the real mail interface.
var _ mail.Sender = (*blockingChoiceSender)(nil)

func TestReadEmailOffBindsHashesOnly(t *testing.T) {
	app := testApp(t)
	u := newTestUser(t, app, "bound-choice", "password1234", models.RoleMember)
	seedEmailAddressOff(t, app, u.Email, notify.CatEvents)
	name := fmt.Sprintf("test:email_choice_binds:%d", queryCounterSeq.Add(1))
	var binds []any
	var statement string
	if err := app.DB.Callback().Row().Before("gorm:row").Register(name, func(tx *gorm.DB) {
		if strings.Contains(tx.Statement.SQL.String(), "email_address_optouts") {
			statement = tx.Statement.SQL.String()
			binds = append([]any(nil), tx.Statement.Vars...)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.DB.Callback().Row().Remove(name) })
	if _, err := readEmailOff(app.DB, []emailDest{{UserID: u.ID, Address: u.Email}}); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, bind := range binds {
		if s, ok := bind.(string); ok {
			if strings.Contains(s, u.Email) {
				t.Fatal("choice read bound a plaintext address")
			}
			found = found || s == mail.AddressHash(u.Email)
		}
	}
	if !found || strings.Contains(statement, u.Email) {
		t.Fatalf("choice read did not bind its Go hash: %q, %v", statement, binds)
	}
}
