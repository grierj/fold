package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"fold/internal/mail"
	"fold/internal/notify"
	"fold/internal/providers"
	"fold/models"

	"gorm.io/gorm"
)

// Reader and turn-on tests can seed retained choices without erasing an account.
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
	if _, err := setEmailConsent(app.DB, u.ID, "category:groups", models.ConsentOptedOut, models.ConsentSourceWebForm, "", "", u, nil); err != nil {
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
	decodeJSON(t, anon.do("GET", "/api/v1/email-settings/"+app.unsubscribeToken(u.ID, "category:groups", u.Email), nil, false), &link)
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
func (s *blockingChoiceSender) SendEmail(ctx context.Context, m mail.Message) (string, error) {
	s.mu.Lock()
	s.messages = append(s.messages, m)
	first := len(s.messages) == 1
	s.mu.Unlock()
	if first {
		close(s.entered)
		select {
		case <-s.release:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	if s.next != nil {
		return s.next.SendEmail(ctx, m)
	}
	return "", nil
}

func (s *blockingChoiceSender) Capabilities() providers.EmailCaps {
	return providers.EmailCaps{Name: "blocking-choice"}
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

func TestEmailTurnOnRecordsAnAffirmationOverAnExistingOptIn(t *testing.T) {
	app := testApp(t)
	u := newTestUser(t, app, "holder", "password1234", models.RoleMember)
	consentTestSeed(t, app, u, models.ConsentEmail, "category:events", models.ConsentOptedIn)
	seedEmailAddressOff(t, app, u.Email, "events")
	c := loginAPI(t, app, "holder", "password1234")
	c.ip, c.ua = "203.0.113.23", "Affirmative holder"
	wantEmailSettingsStatus(t, c.do("PUT", "/api/v1/me/notification-settings", map[string]any{"category": "events", "off": false}, true), http.StatusOK)
	rows := consentTestRowsOf(t, app, u.ID)
	if len(rows) != 2 || rows[1].Status != models.ConsentOptedIn || rows[1].IP != c.ip || rows[1].UserAgent != c.ua {
		t.Fatalf("retained choice cleared without new evidence: %+v", rows)
	}
	wantRetainedEmailChoice(t, app, u.Email, "events", false)
}

func TestEmailWebhooksLeaveChoicesUntouched(t *testing.T) {
	app := testApp(t)
	u := newTestUser(t, app, "holder", "password1234", models.RoleMember)
	seedAllEmailAddressOff(t, app, u.Email)
	consentTestSeed(t, app, u, models.ConsentEmail, "category:events", models.ConsentOptedOut)
	before, records := consentTestRows(t, app, u.ID), retainedEmailChoices(t, app)
	for _, kind := range []providers.EventKind{providers.EventBounced, providers.EventComplained, providers.EventDelivered} {
		err := app.applyEmailEvent(context.Background(), providers.Event{
			Provider: "postmark", Kind: kind, Address: u.Email, MessageID: string(kind),
			Permanent: kind != providers.EventDelivered, At: time.Now().Add(-5 * time.Minute),
		})
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(before, consentTestRows(t, app, u.ID)) || !reflect.DeepEqual(records, retainedEmailChoices(t, app)) {
			t.Fatalf("%s report changed email choices", kind)
		}
	}
}

type emailOptOutStatement struct {
	SQL  string
	Vars []any
}

// recordEmailOptOutStatements observes both Exec and Raw/Scan, including a
// forbidden read on the tombstone path, without interpolating bind values.
func recordEmailOptOutStatements(t *testing.T, db *gorm.DB) func() []emailOptOutStatement {
	t.Helper()
	var mu sync.Mutex
	var statements []emailOptOutStatement
	capture := func(tx *gorm.DB) {
		if strings.Contains(tx.Statement.SQL.String(), "email_address_optouts") {
			mu.Lock()
			statements = append(statements, emailOptOutStatement{SQL: tx.Statement.SQL.String(), Vars: append([]any(nil), tx.Statement.Vars...)})
			mu.Unlock()
		}
	}
	name := fmt.Sprintf("test:email_optout_statements:%d", queryCounterSeq.Add(1))
	if err := db.Callback().Raw().After("gorm:raw").Register(name, capture); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Raw().Remove(name) })
	if err := db.Callback().Row().After("gorm:row").Register(name, capture); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Row().Remove(name) })
	return func() []emailOptOutStatement {
		mu.Lock()
		defer mu.Unlock()
		return append([]emailOptOutStatement(nil), statements...)
	}
}

func TestErasureExportsOnlyInForceEmailOptOuts(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(fmt.Sprintf("existing=%v", existing), func(t *testing.T) {
			s := newCoreErasureScene(t)
			s.ac.holdNotices = true
			u := newTestUser(t, s.app, "export-choice@example.com", erasurePW, models.RoleMember)
			at := time.Now().UTC().Add(-48 * time.Hour).Truncate(time.Microsecond)
			groupsAt, adminAt := at.Add(time.Hour), at.Add(2*time.Hour)
			for _, row := range []models.Consent{
				{Channel: models.ConsentEmail, Scope: "category:groups", Status: models.ConsentOptedIn, At: groupsAt},
				{Channel: models.ConsentEmail, Scope: "category:groups", Status: models.ConsentOptedOut, At: groupsAt},
				// A larger id with an older time cannot undo the newer choice.
				{Channel: models.ConsentEmail, Scope: "category:groups", Status: models.ConsentOptedIn, At: at},
				{Channel: models.ConsentEmail, Scope: "category:events", Status: models.ConsentOptedOut, At: at},
				{Channel: models.ConsentEmail, Scope: "category:events", Status: models.ConsentOptedIn, At: at},
				{Channel: models.ConsentEmail, Scope: "category:admin", Status: models.ConsentOptedOut, At: adminAt},
				{Channel: models.ConsentPush, Scope: "category:messages", Status: models.ConsentOptedOut, At: at},
				{Channel: models.ConsentEmail, Scope: "all", Status: models.ConsentOptedOut, At: at},
				{Channel: models.ConsentEmail, Scope: "group:1", Status: models.ConsentOptedOut, At: at},
			} {
				row.UserID, row.Source, row.IP = u.ID, models.ConsentSourceWebForm, "203.0.113.30"
				erasureMustCreate(t, s.app.DB, &row)
			}
			other := newTestUser(t, s.app, "other-export-choice@example.com", erasurePW, models.RoleMember)
			erasureMustCreate(t, s.app.DB, &models.Consent{UserID: other.ID, Channel: models.ConsentEmail, Scope: "category:messages", Status: models.ConsentOptedOut, Source: models.ConsentSourceWebForm, At: at})
			if existing {
				groupsAt = at.Add(-24 * time.Hour)
				erasureMustCreate(t, s.app.DB, &models.EmailAddressOptOut{AddressHash: mail.AddressHash(u.Email), Scope: "category:groups", OptedOutAt: groupsAt})
			}
			statements := recordEmailOptOutStatements(t, s.app.DB)
			s.erase(t, u.ID)
			recorded := statements()
			if len(recorded) != 1 || !strings.HasPrefix(recorded[0].SQL, "INSERT INTO email_address_optouts") {
				t.Fatalf("erasure did not issue exactly one export: %+v", recorded)
			}
			foundHash := false
			for _, bind := range recorded[0].Vars {
				if str, ok := bind.(string); ok {
					if strings.Contains(str, "@") {
						t.Fatal("export bound a plaintext address")
					}
					foundHash = foundHash || str == mail.AddressHash(u.Email)
				}
			}
			if !foundHash || strings.Contains(recorded[0].SQL, u.Email) {
				t.Fatal("export did not bind only the held address's Go hash")
			}
			want := []models.EmailAddressOptOut{
				{AddressHash: mail.AddressHash(u.Email), Scope: "category:admin", OptedOutAt: adminAt},
				{AddressHash: mail.AddressHash(u.Email), Scope: "category:groups", OptedOutAt: groupsAt},
			}
			before := retainedEmailChoices(t, s.app)
			if len(before) != len(want) {
				t.Fatalf("exported choices: got %+v, want %+v", before, want)
			}
			for i, got := range before {
				if got.AddressHash != want[i].AddressHash || got.Scope != want[i].Scope || !got.OptedOutAt.Equal(want[i].OptedOutAt) {
					t.Fatalf("exported choice or original time differs: got %+v, want %+v", got, want[i])
				}
			}
			wantEmailSettingsStatus(t, s.ac.do("DELETE", "/api/v1/admin/users/"+itoa(u.ID), nil, true), http.StatusNotFound)
			tombstoneRun(t, s.app, u.ID, 1)
			if got := retainedEmailChoices(t, s.app); !reflect.DeepEqual(got, before) {
				t.Fatalf("repeated erasure changed retained choices: %+v", got)
			}
		})
	}
}

func TestErasureOptOutExportRequiresHeldNonemptyAddress(t *testing.T) {
	for _, tc := range []struct {
		name    string
		address heldAddress
	}{
		{"unavailable", heldAddress{value: "poison@example.com"}},
		{"empty", heldAddress{ok: true}},
		{"whitespace", heldAddress{value: " \t\r\n", ok: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newCoreErasureScene(t)
			u := newTestUser(t, s.app, "unavailable-choice@example.com", erasurePW, models.RoleMember)
			erasureSeedConsentEvidence(t, s.app.DB, u.ID, "unavailable")
			tombstoneLegacy(t, s.app.DB, u.ID)
			seen := recordEmailOptOutStatements(t, s.app.DB)
			if err := s.app.DB.Transaction(func(tx *gorm.DB) error {
				var id uint
				if err := tx.Raw("SELECT id FROM users WHERE id = ? FOR UPDATE", u.ID).Scan(&id).Error; err != nil {
					return err
				}
				return eraseConsentEvidenceIn(tx, erasureSubject{ID: u.ID, Address: tc.address})
			}); err != nil {
				t.Fatal(err)
			}
			if statements := seen(); len(statements) != 0 {
				t.Fatalf("unavailable or blank held address issued opt-out SQL: %+v", statements)
			}
			if rows := retainedEmailChoices(t, s.app); len(rows) != 0 {
				t.Fatalf("unavailable or blank held address exported records: %+v", rows)
			}
		})
	}
}

func TestReusedAddressKeepsErasedChoicesUntilHolderTurnsOn(t *testing.T) {
	f := newComposeFixture(t)
	f.admin.holdNotices, f.leader.holdNotices = true, true
	const address, moved = "reused-choice@example.com", "moved-reused-choice@example.com"
	x := newTestUser(t, f.app, address, erasurePW, models.RoleMember)
	xc := loginAPI(t, f.app, address, erasurePW)
	xc.holdNotices = true
	oldTokens := []string{legacyUnsubscribeToken(f.app, x.ID, "category:events"), f.app.unsubscribeToken(x.ID, "category:events", address)}
	for _, category := range []string{notify.CatEvents, notify.CatGroups} {
		wantEmailSettingsStatus(t, xc.do("PUT", "/api/v1/me/notification-settings", map[string]any{"category": category, "off": true}, true), http.StatusOK)
	}
	wantEmailSettingsStatus(t, f.admin.do("DELETE", "/api/v1/admin/users/"+itoa(x.ID), nil, true), http.StatusOK)
	y := newTestUser(t, f.app, address, erasurePW, models.RoleMember)
	addMember(t, f.app, y, f.group, models.MembershipMember)
	yc := loginAPI(t, f.app, address, erasurePW)
	yc.holdNotices = true
	wantSettings := func(client *apiClient, eventsOff, groupsOff bool) {
		t.Helper()
		w := client.do("GET", "/api/v1/me/notification-settings", nil, false)
		wantEmailSettingsStatus(t, w, http.StatusOK)
		var got struct {
			Settings notificationSettingsJSON `json:"settings"`
		}
		decodeJSON(t, w, &got)
		for category, want := range map[string]bool{notify.CatEvents: eventsOff, notify.CatGroups: groupsOff, notify.CatMessages: false} {
			if off, found := kindOff(emailSettingsJSON{Kinds: got.Settings.Kinds}, category); !found || off != want {
				t.Fatalf("%s setting: found=%v off=%v, want %v", category, found, off, want)
			}
		}
	}
	wantSettings(yc, true, true)
	if len(consentTestRowsOf(t, f.app, y.ID)) != 0 {
		t.Fatal("creating the replacement invented an account choice")
	}
	for _, kind := range []string{notify.EventChanged, notify.GroupApproved} {
		id := sendNotice(t, f.app, kind, y)
		if err := f.app.deliverOneNotification(context.Background(), id); err != nil {
			t.Fatal(err)
		}
		if got := emailDeliveryOf(t, f.app, id); got.Status != models.DeliverySuppressed || got.Reason != "optional email turned off" {
			t.Fatalf("replacement's optional notice sent: %+v", got)
		}
	}
	erasureMustCreate(t, f.app.DB, &models.Registration{OccurrenceID: f.groupOcc.ID, UserID: &y.ID, Status: models.RegGoing})
	reminderFor(t, f.app, f.groupOcc, 1440)
	var reminder models.MessageRecipient
	if err := f.app.DB.Joins("JOIN messages m ON m.id = message_recipients.message_id").Where("m.category = ? AND message_recipients.user_id = ?", models.MessageCategoryReminder, y.ID).First(&reminder).Error; err != nil {
		t.Fatal(err)
	}
	if reminder.Status != models.RecipientSuppressed || reminder.Reason != skipEventsOff || mailCount(t, f.app, address) != 0 {
		t.Fatalf("replacement received optional email: %+v", reminder)
	}
	required := sendNotice(t, f.app, notify.OccurrenceChanged, y)
	if err := f.app.deliverOneNotification(context.Background(), required); err != nil {
		t.Fatal(err)
	}
	if got := emailDeliveryOf(t, f.app, required); got.Status != models.DeliverySent || mailCount(t, f.app, address) != 1 {
		t.Fatalf("required notice blocked: %+v", got)
	}
	queueResetMail(t, f.app, y)
	runResetMail(t, f.app)
	if mailCount(t, f.app, address) != 2 {
		t.Fatal("replacement's account mail blocked")
	}
	wantEmailSettingsStatus(t, yc.do("PUT", "/api/v1/me/notification-settings", map[string]any{"category": "events", "off": false}, true), http.StatusOK)
	wantRetainedEmailChoice(t, f.app, address, "events", false)
	wantRetainedEmailChoice(t, f.app, address, "groups", true)
	rows := consentTestRowsOf(t, f.app, y.ID)
	if len(rows) != 1 || rows[0].Scope != "category:events" || rows[0].Status != models.ConsentOptedIn {
		t.Fatalf("holder's turn-on was not recorded: %+v", rows)
	}
	eventNotice := sendNotice(t, f.app, notify.EventChanged, y)
	if err := f.app.deliverOneNotification(context.Background(), eventNotice); err != nil {
		t.Fatal(err)
	}
	if got := emailDeliveryOf(t, f.app, eventNotice); got.Status != models.DeliverySent || mailCount(t, f.app, address) != 3 {
		t.Fatalf("affirmative holder's event email blocked: %+v", got)
	}
	kept := retainedEmailChoices(t, f.app)
	for _, next := range []string{moved, address} {
		wantEmailSettingsStatus(t, f.admin.do("PUT", "/api/v1/admin/users/"+itoa(y.ID), map[string]string{"email": next}, true), http.StatusOK)
		if !reflect.DeepEqual(kept, retainedEmailChoices(t, f.app)) {
			t.Fatal("address change moved or cleared a retained choice")
		}
		yc = loginAPI(t, f.app, next, erasurePW)
		yc.holdNotices = true
		wantSettings(yc, false, next == address)
	}
	before := consentTestRows(t, f.app, y.ID)
	anon := newAPIClient(t, f.app)
	anon.holdNotices = true
	anon.fetchCSRF()
	for _, token := range oldTokens {
		path := "/api/v1/email-settings/" + token
		wantEmailSettingsStatus(t, anon.do("GET", path, nil, false), http.StatusNotFound)
		for _, off := range []bool{false, true} {
			wantEmailSettingsStatus(t, anon.do("PUT", path, map[string]any{"off": off}, true), http.StatusNotFound)
		}
		wantEmailSettingsStatus(t, postOneClick(f.app, "/u/unsubscribe/"+token, "Old mail"), http.StatusNotFound)
	}
	if !reflect.DeepEqual(before, consentTestRows(t, f.app, y.ID)) || !reflect.DeepEqual(kept, retainedEmailChoices(t, f.app)) {
		t.Fatal("erased holder's old links changed the replacement's choices")
	}
}
