package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"fold/internal/jobs"
	"fold/internal/notify"
	"fold/models"

	"gorm.io/gorm"
)

// copyJobName is the job kind spelled out, so a rename in production code fails
// TestGuardianCopyKindIsRegistered rather than silently emptying every count.
const copyJobName = "notify.guardian_copy"

// copyFixture is a teen with two confirmed guardians, a youth group and one of
// its dates, with the church's guardian-copy switch on.
type copyFixture struct {
	app       *App
	teen      *models.User
	guardians []*models.User
	leader    *models.User
	group     *models.Group
	event     models.Event
	occ       models.EventOccurrence
}

func newCopyFixture(t *testing.T) *copyFixture {
	t.Helper()
	app := testApp(t)
	f := &copyFixture{app: app}
	f.leader = newTestUser(t, app, "leader", "password1234", models.RoleMember)
	f.teen = newTestUser(t, app, "sam", "password1234", models.RoleMember)
	if err := app.DB.Model(f.teen).Update("account_type", models.AccountTeen).Error; err != nil {
		t.Fatal(err)
	}
	f.group = newTestGroup(t, app, f.leader, "Youth night", models.GroupActive)
	f.event, f.occ = seedEvent(t, app, f.group, f.leader.ID, time.Now().UTC().Add(72*time.Hour))
	for _, name := range []string{"mom", "dad"} {
		f.guardians = append(f.guardians, f.guardian(t, name))
	}
	if err := app.DB.Create(&models.Setting{Key: settingGuardianCopies, Value: "true"}).Error; err != nil {
		t.Fatal(err)
	}
	return f
}

// guardian makes an adult with a password and a confirmed link to the teen.
func (f *copyFixture) guardian(t *testing.T, name string) *models.User {
	t.Helper()
	g := newTestUser(t, f.app, name, "password1234", models.RoleMember)
	if err := f.app.DB.Model(g).Update("account_type", models.AccountAdult).Error; err != nil {
		t.Fatal(err)
	}
	f.link(t, g, models.GuardianConfirmed)
	return g
}

func (f *copyFixture) link(t *testing.T, g *models.User, status string) {
	t.Helper()
	if err := f.app.DB.Create(&models.GuardianLink{GuardianUserID: g.ID, TeenUserID: &f.teen.ID, Status: status}).Error; err != nil {
		t.Fatal(err)
	}
}

// hideEvent makes the date private to people the teen is not among, so the
// teen can no longer see the event or its group.
func (f *copyFixture) hideEvent(t *testing.T) {
	t.Helper()
	if err := f.app.DB.Model(&models.Group{}).Where("id = ?", f.group.ID).Update("visibility", models.VisibilityInviteOnly).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.app.DB.Model(&models.Event{}).Where("id = ?", f.event.ID).Update("visibility", models.VisibilityInviteOnly).Error; err != nil {
		t.Fatal(err)
	}
}

// notice writes one stored notice for the teen. It queues notify.deliver and
// nothing else, as every writer does.
func (f *copyFixture) notice(t *testing.T, kind, subjectType string, subjectID uint, vars notify.Vars) uint {
	t.Helper()
	return f.noticeWithData(t, kind, subjectType, subjectID, vars, nil)
}

func (f *copyFixture) noticeWithData(t *testing.T, kind, subjectType string, subjectID uint, vars notify.Vars, data map[string]any) uint {
	t.Helper()
	ids, err := notify.Send(f.app.DB, notify.Msg{Kind: kind, To: []uint{f.teen.ID}, SubjectType: subjectType, SubjectID: subjectID,
		Link: "/events/" + itoa(f.event.ID) + "/private-link", Vars: vars, Data: data})
	if err != nil || len(ids) != 1 {
		t.Fatalf("send %s: %v %v", kind, ids, err)
	}
	return ids[0]
}

// deliver sends the teen's notice and then runs every job that follows it,
// copies included.
func (f *copyFixture) deliver(t *testing.T, id uint) {
	t.Helper()
	if err := f.app.deliverOneNotification(context.Background(), id); err != nil {
		t.Fatalf("deliver: %v", err)
	}
	drainNotices(t, f.app)
}

func copyJobs(t *testing.T, app *App) []models.Job {
	t.Helper()
	var rows []models.Job
	if err := app.DB.Where("kind = ?", copyJobName).Order("id").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	return rows
}

// copiesTo is every email to address whose subject marks it a copy.
func copiesTo(t *testing.T, app *App, address string) []string {
	t.Helper()
	ob := devOutbox(t, app)
	msgs, err := ob.List(200)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, m := range msgs {
		if m.To == address && strings.HasPrefix(m.Subject, "Copy: ") {
			out = append(out, m.Subject)
		}
	}
	return out
}

func (f *copyFixture) requireNoCopies(t *testing.T, why string) {
	t.Helper()
	for _, g := range f.guardians {
		if got := copiesTo(t, f.app, g.Email); len(got) != 0 {
			t.Fatalf("%s: %s got copies %v", why, g.Email, got)
		}
	}
}

// TestNoticeGuardianCopyPerKind pins what each copied kind shows a guardian.
// A copy is the teen's bell text under a marker line, with the guardian's own
// footer: the leader's note and the teen's manage and video links never ride
// along, and a title-only kind shows its title and nothing else.
func TestNoticeGuardianCopyPerKind(t *testing.T) {
	const (
		manage = "https://fold.example/guest/manage/SENTINEL-MANAGE"
		video  = "https://video.example/SENTINEL-VIDEO"
		note   = "SENTINEL-NOTE"
	)
	vars := notify.Vars{Event: "Youth night", When: "Fri 7pm", Place: "12 Elm St", Group: "Youth night", Change: "time", Note: note, ManageURL: manage, VideoURL: video}
	for _, tc := range []struct {
		name        string
		kind        string
		subject     string
		hidden      bool     // the teen has lost the event before the copy job runs
		wantTitle   string   // the copy's subject after "Copy: "
		wantInBody  []string // text the copy carries
		wantAbsent  []string // text no copy may carry
		wantNoCopy  bool
		teenGetsNew bool
	}{
		{name: "date cancelled", kind: notify.OccurrenceCancelled, subject: "occurrence",
			wantTitle: "Cancelled: Youth night", wantInBody: []string{"Fri 7pm", "12 Elm St"}, wantAbsent: []string{note, manage, video}},
		{name: "date back on", kind: notify.OccurrenceRestored, subject: "occurrence",
			wantTitle: "Back on: Youth night", wantInBody: []string{"Fri 7pm", "12 Elm St"}, wantAbsent: []string{note, manage, video}},
		{name: "date changed", kind: notify.EventChanged, subject: "occurrence",
			wantTitle: "Updated: Youth night", wantInBody: []string{"The time changed.", "12 Elm St"}, wantAbsent: []string{note, manage, video}},
		{name: "promoted keeps the teen's links out", kind: notify.RegistrationPromoted, subject: "occurrence",
			wantTitle: "You're in: Youth night", wantInBody: []string{"Fri 7pm"}, wantAbsent: []string{manage, video, "guest/manage", "Cancel your registration"}},
		{name: "removed shows the title alone", kind: notify.RegistrationRemoved, subject: "occurrence",
			wantTitle: "Registration removed: Youth night", wantAbsent: []string{note, "Note:", "A leader or host removed"}},
		{name: "not restored to a date", kind: notify.RegistrationNotRestored, subject: "occurrence",
			wantTitle: "You're not back in Youth night yet", wantInBody: []string{"The event was full"}, wantAbsent: []string{note}},
		{name: "not restored to a group", kind: notify.MemberNotRestored, subject: "group",
			wantTitle: "You're not back in Youth night yet", wantInBody: []string{"The group was full"}, wantAbsent: []string{note}},
		{name: "a date the teen lost is not copied", kind: notify.OccurrenceCancelled, subject: "occurrence", hidden: true, wantNoCopy: true,
			wantTitle: "Cancelled: Youth night"},
		{name: "a group the teen lost is not copied", kind: notify.MemberNotRestored, subject: "group", hidden: true, wantNoCopy: true,
			wantTitle: "You're not back in Youth night yet"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newCopyFixture(t)
			subjectID := f.occ.ID
			if tc.subject == "group" {
				subjectID = f.group.ID
			}
			id := f.notice(t, tc.kind, tc.subject, subjectID, vars)
			if tc.hidden {
				f.hideEvent(t)
			}
			f.deliver(t, id)
			// The teen's own email always goes out.
			if got := latestEmail(t, f.app, f.teen.Email); got.Subject != tc.wantTitle {
				t.Fatalf("teen's email subject %q, want %q", got.Subject, tc.wantTitle)
			}
			// Control: the teen's own promotion email does carry their links, so
			// their absence from the copy is not an empty fixture.
			if tc.kind == notify.RegistrationPromoted {
				if b := latestEmail(t, f.app, f.teen.Email).Body; !strings.Contains(b, manage) || !strings.Contains(b, video) {
					t.Fatalf("the teen's own email lacks its links: %q", b)
				}
			}
			if tc.wantNoCopy {
				f.requireNoCopies(t, tc.name)
				return
			}
			for _, g := range f.guardians {
				if got := copiesTo(t, f.app, g.Email); len(got) != 1 || got[0] != "Copy: "+tc.wantTitle {
					t.Fatalf("%s copies %v, want exactly one %q", g.Email, got, "Copy: "+tc.wantTitle)
				}
				m := latestEmail(t, f.app, g.Email)
				if !strings.Contains(m.Body, "Copy: sent to sam because your church sends guardian copies.") {
					t.Errorf("no copy marker naming the teen: %q", m.Body)
				}
				for _, want := range tc.wantInBody {
					if !strings.Contains(m.Body, want) {
						t.Errorf("copy lacks %q: %q", want, m.Body)
					}
				}
				for _, bad := range tc.wantAbsent {
					if strings.Contains(m.Body, bad) {
						t.Errorf("copy carries %q: %q", bad, m.Body)
					}
				}
				// No teen link, no teen unsubscribe; the guardian's own.
				if strings.Contains(m.Body, f.app.emailLink("/events/"+itoa(f.event.ID)+"/private-link")) {
					t.Errorf("copy carries the teen's notice link: %q", m.Body)
				}
				scope := categoryScope(notify.CatEvents)
				if tc.kind == notify.MemberNotRestored {
					scope = categoryScope(notify.CatGroups)
				}
				if teenTok := f.app.unsubscribeToken(f.teen.ID, scope, f.teen.Email); strings.Contains(m.Body, teenTok) {
					t.Errorf("copy carries the teen's unsubscribe token")
				}
				if own := f.app.unsubscribeToken(g.ID, scope, g.Email); !strings.Contains(m.Body, own) || m.ListUnsubscribe == "" || !strings.Contains(m.ListUnsubscribe, own) {
					t.Errorf("copy lacks the guardian's own unsubscribe: %q / %q", m.Body, m.ListUnsubscribe)
				}
			}
			if rows := copyJobs(t, f.app); len(rows) != 2 {
				t.Errorf("%d copy jobs, want one per guardian", len(rows))
			}
		})
	}
}

// TestNoticeGuardianCopyOffMakesNone: with the switch off nothing is queued or
// sent, and switching it on later does not backfill.
func TestNoticeGuardianCopyOffMakesNone(t *testing.T) {
	f := newCopyFixture(t)
	if err := f.app.DB.Model(&models.Setting{}).Where("key = ?", settingGuardianCopies).Update("value", "false").Error; err != nil {
		t.Fatal(err)
	}
	id := f.notice(t, notify.OccurrenceCancelled, "occurrence", f.occ.ID, notify.Vars{Event: "Youth night"})
	f.deliver(t, id)
	if n := len(copyJobs(t, f.app)); n != 0 {
		t.Fatalf("%d copy jobs with the switch off", n)
	}
	if err := f.app.DB.Model(&models.Setting{}).Where("key = ?", settingGuardianCopies).Update("value", "true").Error; err != nil {
		t.Fatal(err)
	}
	f.deliver(t, id)
	f.requireNoCopies(t, "switching on later")
	// Control: the next notice is copied.
	next := f.notice(t, notify.OccurrenceCancelled, "occurrence", f.occ.ID, notify.Vars{Event: "Youth night"})
	f.deliver(t, next)
	if n := len(copyJobs(t, f.app)); n != 2 {
		t.Fatalf("%d copy jobs after switching on, want 2", n)
	}
}

// TestNoticeGuardianCopyLostAccessNotice is the owner's decision (Grier,
// 2026-11-13) for registration.cancelled: its guardians get a copy even
// though the teen can no longer see the event, because that is the one notice
// written for a person who has just lost access. The copy names the event the
// teen's notice names and nothing more: no group, place, link or token.
func TestNoticeGuardianCopyLostAccessNotice(t *testing.T) {
	f := newCopyFixture(t)
	f.hideEvent(t)
	// The writer stores the plain title (notifyRegistrationCancelled); the
	// end-to-end test below drives the real writer, this one pins the rest.
	id := f.noticeWithData(t, notify.RegistrationCancelled, "event", f.event.ID, notify.Vars{Event: "Leaders' Retreat"}, map[string]any{notify.DataEventTitle: "Leaders' Retreat"})
	f.deliver(t, id)
	const title = "Registration cancelled: Leaders' Retreat"
	if got := latestEmail(t, f.app, f.teen.Email); got.Subject != title {
		t.Fatalf("teen's subject %q", got.Subject)
	}
	var stored models.Notification
	if err := f.app.DB.First(&stored, id).Error; err != nil {
		t.Fatal(err)
	}
	for _, g := range f.guardians {
		if got := copiesTo(t, f.app, g.Email); len(got) != 1 || got[0] != "Copy: "+title {
			t.Fatalf("%s copies %v", g.Email, got)
		}
		m := latestEmail(t, f.app, g.Email)
		for _, want := range []string{"Copy: sent to sam because your church sends guardian copies.", stored.Body} {
			if !strings.Contains(m.Body, want) {
				t.Errorf("copy lacks %q: %q", want, m.Body)
			}
		}
		// The copy's own footer links to the guardian's email settings; no
		// other link may appear.
		for _, bad := range []string{"Youth night", "private-link", "/events/", "/groups/"} {
			if strings.Contains(m.Body, bad) {
				t.Errorf("copy carries %q: %q", bad, m.Body)
			}
		}
	}
	// The exemption is for that notice alone and every other check still
	// applies: a revoked link gets nothing.
	if err := f.app.DB.Model(&models.GuardianLink{}).Where("guardian_user_id = ?", f.guardians[0].ID).Update("status", models.GuardianRevoked).Error; err != nil {
		t.Fatal(err)
	}
	id2 := f.noticeWithData(t, notify.RegistrationCancelled, "event", f.event.ID, notify.Vars{Event: "Second event"}, map[string]any{notify.DataEventTitle: "Second event"})
	f.deliver(t, id2)
	if got := copiesTo(t, f.app, f.guardians[0].Email); len(got) != 1 {
		t.Fatalf("a revoked guardian got another copy: %v", got)
	}
	if got := copiesTo(t, f.app, f.guardians[1].Email); len(got) != 2 {
		t.Fatalf("a confirmed guardian's copies %v, want 2", got)
	}
}

// TestNoticeGuardianCopyEligibleGuardiansOnly: only a confirmed link to an
// adult, active, signed-in-capable account gets a copy; the rest are quiet.
func TestNoticeGuardianCopyEligibleGuardiansOnly(t *testing.T) {
	f := newCopyFixture(t)
	// The two fixture guardians are the control arm. Corrupt or legacy links:
	teenGuardian := f.guardian(t, "teen-guardian")
	guestGuardian := f.guardian(t, "guest-guardian")
	bare := f.guardian(t, "passwordless")
	disabled := f.guardian(t, "disabled-adult")
	pending := newTestUser(t, f.app, "pending-guardian", "password1234", models.RoleMember)
	revoked := newTestUser(t, f.app, "revoked-guardian", "password1234", models.RoleMember)
	for u, updates := range map[*models.User]map[string]any{
		teenGuardian:  {"account_type": models.AccountTeen},
		guestGuardian: {"status": models.UserGuest},
		bare:          {"password_hash": nil},
		disabled:      {"status": models.UserDisabled},
	} {
		if err := f.app.DB.Model(u).Updates(updates).Error; err != nil {
			t.Fatal(err)
		}
	}
	f.link(t, pending, models.GuardianPending)
	f.link(t, revoked, models.GuardianRevoked)
	id := f.notice(t, notify.OccurrenceCancelled, "occurrence", f.occ.ID, notify.Vars{Event: "Youth night"})
	f.deliver(t, id)
	for _, g := range f.guardians {
		if got := copiesTo(t, f.app, g.Email); len(got) != 1 {
			t.Fatalf("eligible guardian %s got %v", g.Email, got)
		}
	}
	for _, g := range []*models.User{teenGuardian, guestGuardian, bare, disabled, pending, revoked} {
		if got := copiesTo(t, f.app, g.Email); len(got) != 0 {
			t.Errorf("ineligible guardian %s got %v", g.Email, got)
		}
	}
	if n := len(copyJobs(t, f.app)); n != 2 {
		t.Errorf("%d copy jobs, want one per eligible guardian", n)
	}
}

// TestNoticeGuardianCopyOnlyWithTheTeensSentEmail: a copy exists only if the
// teen's own email was accepted, and the jobs commit with that outcome.
func TestNoticeGuardianCopyOnlyWithTheTeensSentEmail(t *testing.T) {
	t.Run("a transient failure makes none until the retry sends", func(t *testing.T) {
		f := newCopyFixture(t)
		sender := &notifyFailSender{err: errors.New("provider down")}
		real := f.app.Mailer
		f.app.Mailer = sender
		id := f.notice(t, notify.OccurrenceCancelled, "occurrence", f.occ.ID, notify.Vars{Event: "Youth night"})
		if err := f.app.deliverOneNotification(context.Background(), id); err == nil {
			t.Fatal("a transient failure returned no error")
		}
		if n := len(copyJobs(t, f.app)); n != 0 {
			t.Fatalf("%d copy jobs after a failed teen send", n)
		}
		if d := emailDeliveryOf(t, f.app, id); d.Status != models.DeliveryQueued {
			t.Fatalf("delivery %+v", d)
		}
		f.app.Mailer = real
		f.deliver(t, id)
		if n := len(copyJobs(t, f.app)); n != 2 {
			t.Fatalf("%d copy jobs after the retry, want 2", n)
		}
		for _, g := range f.guardians {
			if got := copiesTo(t, f.app, g.Email); len(got) != 1 {
				t.Fatalf("%s got %v", g.Email, got)
			}
		}
	})
	t.Run("a suppressed teen address makes none", func(t *testing.T) {
		f := newCopyFixture(t)
		suppressDeliverAddress(t, f.app, f.teen)
		f.deliver(t, f.notice(t, notify.OccurrenceCancelled, "occurrence", f.occ.ID, notify.Vars{Event: "Youth night"}))
		if n := len(copyJobs(t, f.app)); n != 0 {
			t.Fatalf("%d copy jobs for a suppressed teen", n)
		}
		f.requireNoCopies(t, "suppressed teen")
	})
	t.Run("a teen with the category off makes none", func(t *testing.T) {
		f := newCopyFixture(t)
		if err := f.app.DB.Create(&models.Consent{UserID: f.teen.ID, Channel: models.ConsentEmail, Scope: categoryScope(notify.CatEvents),
			Status: models.ConsentOptedOut, Source: models.ConsentSourceWebForm}).Error; err != nil {
			t.Fatal(err)
		}
		f.deliver(t, f.notice(t, notify.OccurrenceCancelled, "occurrence", f.occ.ID, notify.Vars{Event: "Youth night"}))
		if n := len(copyJobs(t, f.app)); n != 0 {
			t.Fatalf("%d copy jobs for a teen who turned the category off", n)
		}
		f.requireNoCopies(t, "teen category off")
	})
	t.Run("a required notice is never copied", func(t *testing.T) {
		f := newCopyFixture(t)
		f.deliver(t, f.notice(t, notify.OccurrenceChanged, "occurrence", f.occ.ID, notify.Vars{Event: "Youth night"}))
		if n := len(copyJobs(t, f.app)); n != 0 {
			t.Fatalf("%d copy jobs for a safety notice", n)
		}
	})
	t.Run("an adult's notice is never copied", func(t *testing.T) {
		f := newCopyFixture(t)
		if err := f.app.DB.Model(f.teen).Update("account_type", models.AccountAdult).Error; err != nil {
			t.Fatal(err)
		}
		f.deliver(t, f.notice(t, notify.OccurrenceCancelled, "occurrence", f.occ.ID, notify.Vars{Event: "Youth night"}))
		if n := len(copyJobs(t, f.app)); n != 0 {
			t.Fatalf("%d copy jobs for an adult account", n)
		}
	})
}

// faultOn makes every statement on app.DB fail inside Postgres while it is on
// (so a claim transaction aborts, as a real failure would): one whose SQL
// contains target, or a model read of the table named target. A target
// "update:<table>" fails only a model update of that table, which is how the
// ledger write after a send is told from the claim's own reads. hits counts how
// often it was reached; off() stops it.
func faultOn(t *testing.T, app *App, target string) (hits *int, off func()) {
	t.Helper()
	table, updateOnly := strings.CutPrefix(target, "update:")
	if !updateOnly {
		table = target
	}
	name := fmt.Sprintf("test:copy_fault:%d", queryCounterSeq.Add(1))
	on, n := true, 0
	rewrite := func(tx *gorm.DB) {
		if !on {
			return
		}
		if tx.Statement.SQL.Len() == 0 && tx.Statement.Table == table {
			// A model read builds its SQL after this callback.
			n++
			tx.Statement.Table = "missing_copy_fault_table"
			return
		}
		if !updateOnly && strings.Contains(tx.Statement.SQL.String(), table) {
			n++
			tx.Statement.SQL.Reset()
			tx.Statement.SQL.WriteString("SELECT * FROM missing_copy_fault_table")
		}
	}
	remove := func() {
		_ = app.DB.Callback().Row().Remove(name)
		_ = app.DB.Callback().Query().Remove(name)
		_ = app.DB.Callback().Raw().Remove(name)
		_ = app.DB.Callback().Update().Remove(name)
	}
	registers := []func() error{
		func() error { return app.DB.Callback().Row().Before("gorm:row").Register(name, rewrite) },
		func() error { return app.DB.Callback().Query().Before("gorm:query").Register(name, rewrite) },
		func() error { return app.DB.Callback().Raw().Before("gorm:raw").Register(name, rewrite) },
		func() error { return app.DB.Callback().Update().Before("gorm:update").Register(name, rewrite) },
	}
	if updateOnly {
		registers = registers[3:]
	}
	for _, register := range registers {
		if err := register(); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(remove)
	return &n, func() { on = false }
}

// TestNoticeGuardianCopyFaultsBeforeDispatchSendNothing: a failed read or
// enqueue in the claim happens before the provider is called, so nothing is
// sent, the delivery stays queued, and a clean rerun mails the teen once and
// queues each guardian's copy once.
func TestNoticeGuardianCopyFaultsBeforeDispatchSendNothing(t *testing.T) {
	for _, tc := range []struct{ name, fragment string }{
		{"the switch read", "settings"},
		{"the guardian read", "guardian_links"},
		{"the job insert", `INSERT INTO jobs (kind, payload, run_at, status, max_attempts, unique_key)`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newCopyFixture(t)
			sender := &notifyFailSender{}
			real := f.app.Mailer
			f.app.Mailer = sender
			id := f.notice(t, notify.OccurrenceCancelled, "occurrence", f.occ.ID, notify.Vars{Event: "Youth night"})
			hits, off := faultOn(t, f.app, tc.fragment)
			// notify.Send queued its own delivery job before the fault; the
			// claim is what must fail.
			err := f.app.deliverOneNotification(context.Background(), id)
			if err == nil || *hits == 0 || sender.n() != 0 {
				t.Fatalf("fault not reached or mail sent: err=%v hits=%d calls=%d", err, *hits, sender.n())
			}
			if d := emailDeliveryOf(t, f.app, id); d.Status != models.DeliveryQueued || d.SentAt != nil {
				t.Fatalf("delivery %+v", d)
			}
			if n := len(copyJobs(t, f.app)); n != 0 {
				t.Fatalf("%d copy jobs after a rolled-back claim", n)
			}
			off()
			f.app.Mailer = real
			f.deliver(t, id)
			if got := mailCount(t, f.app, f.teen.Email); got != 1 {
				t.Fatalf("teen mailed %d times, want once", got)
			}
			for _, g := range f.guardians {
				if got := copiesTo(t, f.app, g.Email); len(got) != 1 {
					t.Fatalf("%s got %v", g.Email, got)
				}
			}
		})
	}
}

// TestNoticeGuardianCopyJobIsRegistered: the System page lists the kind with
// a label and the notice kind's eight attempts.
func TestGuardianCopyKindIsRegistered(t *testing.T) {
	app := testApp(t)
	var found *jobs.Kind
	for _, k := range app.notifyKinds() {
		if k.Name == copyJobName {
			k := k
			found = &k
		}
	}
	if found == nil || found.Label == "" || found.MaxAttempts != 8 {
		t.Fatalf("kind %+v", found)
	}
	got := app.jobKinds()[copyJobName]
	if got.Label == "" {
		t.Fatalf("System page kind %+v", got)
	}
}

// queued sends the teen's notice and stops before the copy jobs run, so a test
// can change the world in between.
func (f *copyFixture) queued(t *testing.T, kind, subjectType string, subjectID uint) uint {
	t.Helper()
	id := f.notice(t, kind, subjectType, subjectID, notify.Vars{Event: "Youth night", When: "Fri 7pm", Place: "12 Elm St", Group: "Youth night"})
	if err := f.app.deliverOneNotification(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if n := len(copyJobs(t, f.app)); n != 2 {
		t.Fatalf("%d copy jobs queued, want 2", n)
	}
	return id
}

// TestNoticeGuardianCopyStopsFailClosed: every fact is read again when the job
// runs, and a change committed before that read stops the copy, for the one
// guardian it concerns or for both. The first row is the control: with nothing
// changed both guardians get their copy.
func TestNoticeGuardianCopyStopsFailClosed(t *testing.T) {
	for _, tc := range []struct {
		name       string
		change     func(t *testing.T, f *copyFixture, id uint)
		mom, dad   bool // who still gets a copy
		everything bool // the notice's subject type is set to something unmapped
	}{
		{name: "nothing changed (control)", mom: true, dad: true},
		{name: "link revoked", mom: false, dad: true, change: func(t *testing.T, f *copyFixture, _ uint) {
			f.app.DB.Model(&models.GuardianLink{}).Where("guardian_user_id = ?", f.guardians[0].ID).Update("status", models.GuardianRevoked)
		}},
		{name: "guardian erased", mom: false, dad: true, change: func(t *testing.T, f *copyFixture, _ uint) {
			eraseNoticesNow(t, f.app.DB, f.guardians[0].ID)
		}},
		{name: "guardian disabled", mom: false, dad: true, change: func(t *testing.T, f *copyFixture, _ uint) {
			f.app.DB.Model(f.guardians[0]).Update("status", models.UserDisabled)
		}},
		{name: "guardian turned the category off", mom: false, dad: true, change: func(t *testing.T, f *copyFixture, _ uint) {
			optOut := models.Consent{UserID: f.guardians[0].ID, Channel: models.ConsentEmail, Scope: categoryScope(notify.CatEvents), Status: models.ConsentOptedOut, Source: models.ConsentSourceWebForm}
			if err := f.app.DB.Create(&optOut).Error; err != nil {
				t.Fatal(err)
			}
		}},
		{name: "guardian address suppressed", mom: false, dad: true, change: func(t *testing.T, f *copyFixture, _ uint) {
			suppressDeliverAddress(t, f.app, f.guardians[0])
		}},
		{name: "teen erased", change: func(t *testing.T, f *copyFixture, _ uint) { eraseNoticesNow(t, f.app.DB, f.teen.ID) }},
		{name: "teen disabled", change: func(t *testing.T, f *copyFixture, _ uint) {
			f.app.DB.Model(f.teen).Update("status", models.UserDisabled)
		}},
		{name: "teen turned 18", change: func(t *testing.T, f *copyFixture, _ uint) {
			f.app.DB.Model(f.teen).Update("account_type", models.AccountAdult)
		}},
		{name: "switch turned off", change: func(t *testing.T, f *copyFixture, _ uint) {
			f.app.DB.Model(&models.Setting{}).Where("key = ?", settingGuardianCopies).Update("value", "false")
		}},
		{name: "teen lost the date", change: func(t *testing.T, f *copyFixture, _ uint) { f.hideEvent(t) }},
		{name: "date deleted", change: func(t *testing.T, f *copyFixture, _ uint) {
			f.app.DB.Delete(&models.EventOccurrence{}, f.occ.ID)
		}},
		{name: "teen's delivery is not sent", change: func(t *testing.T, f *copyFixture, id uint) {
			f.app.DB.Model(&models.NotificationDelivery{}).Where("notification_id = ?", id).Update("status", models.DeliverySuppressed)
		}},
		{name: "notice about something unmapped", change: func(t *testing.T, f *copyFixture, id uint) {
			f.app.DB.Model(&models.Notification{}).Where("id = ?", id).Update("subject_type", "household")
		}},
		{name: "notice gone", change: func(t *testing.T, f *copyFixture, id uint) {
			f.app.DB.Delete(&models.Notification{}, id)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newCopyFixture(t)
			id := f.queued(t, notify.OccurrenceCancelled, "occurrence", f.occ.ID)
			if tc.change != nil {
				tc.change(t, f, id)
			}
			drainNotices(t, f.app)
			for i, want := range []bool{tc.mom, tc.dad} {
				g := f.guardians[i]
				got := copiesTo(t, f.app, g.Email)
				if want && len(got) != 1 || !want && len(got) != 0 {
					t.Errorf("%s: copies %v, want a copy: %v", g.Email, got, want)
				}
			}
			// Every job finished: a "no" is not a failure to retry.
			var open int64
			f.app.DB.Model(&models.Job{}).Where("kind = ? AND status <> ?", copyJobName, models.JobDone).Count(&open)
			if open != 0 {
				t.Errorf("%d copy jobs left open", open)
			}
		})
	}
}

// A failed read is an error, never a "no": the job is retried and sends once.
func TestNoticeGuardianCopyFailedReadRetries(t *testing.T) {
	for _, table := range []string{"settings", "guardian_links", "notification_deliveries", "notifications"} {
		t.Run(table, func(t *testing.T) {
			f := newCopyFixture(t)
			f.queued(t, notify.OccurrenceCancelled, "occurrence", f.occ.ID)
			var job models.Job
			if err := f.app.DB.Where("kind = ?", copyJobName).Order("id").First(&job).Error; err != nil {
				t.Fatal(err)
			}
			hits, off := faultOn(t, f.app, table)
			run := func() error {
				return f.app.guardianCopyKind().Run(context.Background(), json.RawMessage(job.Payload))
			}
			if err := run(); err == nil || *hits == 0 {
				t.Fatalf("failed read of %s gave err=%v (hits %d), want an error", table, err, *hits)
			}
			if got := mailCount(t, f.app, f.guardians[0].Email); got != 0 {
				t.Fatalf("mailed %d after a failed read", got)
			}
			off()
			if err := run(); err != nil {
				t.Fatal(err)
			}
			if got := mailCount(t, f.app, f.guardians[0].Email); got != 1 {
				t.Fatalf("%d emails after the retry, want the one copy", got)
			}
		})
	}
}

// TestNoticeGuardianCopyDirectGuardian: a guardian who is also sent the same
// notice (same kind, subject and dedupe key) gets it once, as their own. A
// notice with no dedupe key is a different notice, so they get their own and
// a marked copy.
func TestNoticeGuardianCopyDirectGuardian(t *testing.T) {
	f := newCopyFixture(t)
	mom, dad := f.guardians[0], f.guardians[1]
	send := func(kind, key string) []uint {
		msg := notify.Msg{Kind: kind, To: []uint{f.teen.ID, mom.ID}, SubjectType: "occurrence", SubjectID: f.occ.ID, Link: "/", Vars: notify.Vars{Event: "Youth night", When: "Fri 7pm", Change: "time"}}
		if key != "" {
			msg.DedupeKey = key
		}
		ids, err := notify.Send(f.app.DB, msg)
		if err != nil || len(ids) != 2 {
			t.Fatalf("send: %v %v", ids, err)
		}
		return ids
	}
	for _, id := range send(notify.OccurrenceCancelled, "cancel:1") {
		f.deliver(t, id)
	}
	if got := mailCount(t, f.app, mom.Email); got != 1 || len(copiesTo(t, f.app, mom.Email)) != 0 {
		t.Fatalf("mom got %d emails, %d of them copies; want her own notice alone", got, len(copiesTo(t, f.app, mom.Email)))
	}
	if got := copiesTo(t, f.app, dad.Email); len(got) != 1 {
		t.Fatalf("dad's copies %v", got)
	}
	// No dedupe key: two different notices.
	for _, id := range send(notify.EventChanged, "") {
		f.deliver(t, id)
	}
	if got := mailCount(t, f.app, mom.Email); got != 3 || len(copiesTo(t, f.app, mom.Email)) != 1 {
		t.Fatalf("mom got %d emails (%d copies); want her notice, the first one and a marked copy", got, len(copiesTo(t, f.app, mom.Email)))
	}
}

// TestNoticeGuardianCopySendsOnce: retries, a requeue by the hourly repair and
// a second drain add no second job and no second email.
func TestNoticeGuardianCopySendsOnce(t *testing.T) {
	f := newCopyFixture(t)
	id := f.notice(t, notify.OccurrenceCancelled, "occurrence", f.occ.ID, notify.Vars{Event: "Youth night"})
	f.deliver(t, id)
	f.deliver(t, id) // the delivery is terminal: a second run does nothing
	if err := f.app.reconcileNotifications(context.Background(), time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	drainNotices(t, f.app)
	if n := len(copyJobs(t, f.app)); n != 2 {
		t.Fatalf("%d copy jobs, want 2", n)
	}
	for _, g := range f.guardians {
		if got := copiesTo(t, f.app, g.Email); len(got) != 1 {
			t.Fatalf("%s copies %v", g.Email, got)
		}
	}
	if got := mailCount(t, f.app, f.teen.Email); got != 1 {
		t.Fatalf("teen mailed %d times", got)
	}
	// The unique key holds against a repeated enqueue.
	specs, err := guardianCopyJobs(f.app.DB, &models.Notification{ID: id}, mustKind(t, notify.OccurrenceCancelled), f.teen, time.Now())
	if err != nil || len(specs) != 2 {
		t.Fatalf("specs %d err %v", len(specs), err)
	}
	if added, err := jobs.EnqueueMany(f.app.DB, specs); err != nil || added != 0 {
		t.Fatalf("a repeated enqueue added %d (err %v)", added, err)
	}
}

func mustKind(t *testing.T, key string) notify.Kind {
	t.Helper()
	k, ok := notify.Lookup(key)
	if !ok {
		t.Fatalf("kind %s", key)
	}
	return k
}

// TestNoticeGuardianCopyJobsCommitOnlyWithTheSentStatus: nothing is written
// after the provider accepts the teen's mail except the sent status. When
// that write fails the claim rolls back whole, jobs included; the retry mails
// the teen again (delivery is at least once) and only then queues the copies.
func TestNoticeGuardianCopyJobsCommitOnlyWithTheSentStatus(t *testing.T) {
	f := newCopyFixture(t)
	sender := &notifyFailSender{}
	f.app.Mailer = sender
	id := f.notice(t, notify.OccurrenceCancelled, "occurrence", f.occ.ID, notify.Vars{Event: "Youth night"})
	hits, off := faultOn(t, f.app, "update:notification_deliveries")
	if err := f.app.deliverOneNotification(context.Background(), id); err == nil || *hits == 0 {
		t.Fatalf("the status write was not reached: err=%v hits=%d", err, *hits)
	}
	if sender.n() != 1 {
		t.Fatalf("provider calls %d, want the one that was accepted", sender.n())
	}
	if n := len(copyJobs(t, f.app)); n != 0 {
		t.Fatalf("%d copy jobs survived a rolled-back sent status", n)
	}
	off()
	if err := f.app.deliverOneNotification(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if sender.n() != 2 || len(copyJobs(t, f.app)) != 2 {
		t.Fatalf("after the retry: %d provider calls, %d copy jobs; want 2 and 2", sender.n(), len(copyJobs(t, f.app)))
	}
}

// TestNoticeGuardianCopyEventSubject covers the subject type no teen notice
// uses today: a kind about an event follows the teen's access to the event.
// A stored row stands in for it, since the registry's event-subject kinds are
// written to hosts and leaders.
func TestNoticeGuardianCopyEventSubject(t *testing.T) {
	for _, hidden := range []bool{false, true} {
		f := newCopyFixture(t)
		if hidden {
			f.hideEvent(t)
		}
		f.deliver(t, f.notice(t, notify.WaitlistCutoff, "event", f.event.ID, notify.Vars{Event: "Youth night", When: "Fri 7pm"}))
		for _, g := range f.guardians {
			if got := copiesTo(t, f.app, g.Email); (len(got) == 1) == hidden || len(got) > 1 {
				t.Errorf("hidden=%v: %s copies %v", hidden, g.Email, got)
			}
		}
	}
}

// TestNoticeGuardianCopyKindsAreClassified: a newly registered optional email
// kind fails here until someone decides whether a teen's guardian hears of it.
func TestNoticeGuardianCopyKindsAreClassified(t *testing.T) {
	const (
		copied  = "copied (its subject is a date, an event or a group the teen must still see)"
		exempt  = "copied without the teen's access (owner decision, 2026-11-13)"
		direct  = "already sent straight to the guardians"
		neverTo = "never written to a teen (a leader, host or admin notice)"
	)
	want := map[string]string{
		notify.OccurrenceCancelled:     copied,
		notify.OccurrenceRestored:      copied,
		notify.EventChanged:            copied,
		notify.RegistrationPromoted:    copied,
		notify.RegistrationRemoved:     copied,
		notify.RegistrationNotRestored: copied,
		notify.MemberNotRestored:       copied,
		notify.RegistrationCancelled:   exempt,
		notify.MembershipDeparture:     direct,
		notify.LeadershipChanged:       neverTo,
		notify.YourRole:                neverTo,
		notify.GroupApproved:           neverTo,
		notify.GroupRejected:           neverTo,
		notify.MembershipLeft:          neverTo,
		notify.VisibilityOverride:      neverTo,
		notify.RoomRequestDecided:      neverTo,
		notify.RoomLost:                neverTo,
		notify.PushKeysReset:           neverTo,
		notify.WaitlistCutoff:          neverTo,
	}
	seen := map[string]bool{}
	for _, k := range notify.Kinds() {
		if !k.AllowEmail || k.Required {
			continue
		}
		seen[k.Key] = true
		class, ok := want[k.Key]
		if !ok {
			t.Errorf("%s is an optional email kind with no copy decision: add it to this table", k.Key)
			continue
		}
		if k.CopyWithoutSubjectAccess != (class == exempt) {
			t.Errorf("%s: CopyWithoutSubjectAccess = %v but it is %s", k.Key, k.CopyWithoutSubjectAccess, class)
		}
	}
	for key := range want {
		if !seen[key] {
			t.Errorf("%s is classified here but is no longer an optional email kind", key)
		}
	}
}

// singleDateScene is what singleDateCancellation leaves: a teen with two
// confirmed guardians, cancelled off one date, and no mail delivered yet.
type singleDateScene struct {
	app       *App
	teen      *models.User
	guardians []*models.User
	day       string // the cancelled date's calendar day
	occ       models.EventOccurrence
	event     eventJSON
}

// singleDateCancellation drives the real path that writes registration.cancelled
// for one date: an admin takes a date that a leader had opened back to its
// invite-only group, and a teen who is not in that group loses the seat they held.
func singleDateCancellation(t *testing.T, eventTitle, dateTitle string) *singleDateScene {
	t.Helper()
	app, admin, _, ev, dates := dateVisibilityFixture(t, models.VisibilityInviteOnly)
	if eventTitle != "" {
		if err := app.DB.Model(&models.Event{}).Where("id = ?", ev.ID).Update("title", eventTitle).Error; err != nil {
			t.Fatal(err)
		}
	}
	s := &singleDateScene{app: app, day: models.FormatCalendarDate(dates[0].OriginalDate), occ: dates[0], event: ev}
	path := fmt.Sprintf("/api/v1/events/%d/occurrences/%s", ev.ID, s.day)
	if dateTitle != "" {
		apiDo(t, admin, "PUT", path, map[string]any{"title": dateTitle}, 200)
	}
	apiDo(t, admin, "PUT", path, map[string]any{"visibility": "public"}, 200)
	s.teen = newTestUser(t, app, "sam", "password1234", models.RoleMember)
	if err := app.DB.Model(s.teen).Update("account_type", models.AccountTeen).Error; err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"mom", "dad"} {
		g := newTestUser(t, app, name, "password1234", models.RoleMember)
		if err := app.DB.Model(g).Update("account_type", models.AccountAdult).Error; err != nil {
			t.Fatal(err)
		}
		if err := app.DB.Create(&models.GuardianLink{GuardianUserID: g.ID, TeenUserID: &s.teen.ID, Status: models.GuardianConfirmed}).Error; err != nil {
			t.Fatal(err)
		}
		s.guardians = append(s.guardians, g)
	}
	if err := app.DB.Create(&models.Setting{Key: settingGuardianCopies, Value: "true"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := app.DB.Create(&models.Registration{OccurrenceID: dates[0].ID, UserID: &s.teen.ID, Status: models.RegGoing}).Error; err != nil {
		t.Fatal(err)
	}
	apiDo(t, admin, "PUT", path, map[string]any{"visibility": nil, "confirm": true}, 200)
	return s
}

// The guardian's copy of a single date's cancellation names the event's title
// and no date (DEC-149), while the teen's own notice keeps the date joined to
// its title. The copy is built from the plain title the writer stored, so a
// title that holds any separator, or looks like a date, comes through whole;
// the title the date is known by (its own override) is the one named; and a
// rename after the cancellation changes neither notice.
func TestGuardianCopyOfSingleDateCancellationNamesNoDate(t *testing.T) {
	for _, tc := range []struct {
		name, eventTitle, dateTitle, shown string
		renameAfter                        bool
	}{
		{name: "plain title", shown: "Open evening"},
		{name: "title with separators", eventTitle: "Carols · 2031-12-24 · Part 2", shown: "Carols · 2031-12-24 · Part 2"},
		{name: "title ending in a separator", eventTitle: "Games ·", shown: "Games ·"},
		{name: "the date's own title", dateTitle: "Late · 2032-01-02 · Prayer", shown: "Late · 2032-01-02 · Prayer"},
		{name: "renamed before delivery", shown: "Open evening", renameAfter: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := singleDateCancellation(t, tc.eventTitle, tc.dateTitle)
			if tc.renameAfter {
				if err := s.app.DB.Model(&models.Event{}).Where("id = ?", s.event.ID).Update("title", "RENAMED LATER").Error; err != nil {
					t.Fatal(err)
				}
				if err := s.app.DB.Model(&models.EventOccurrence{}).Where("id = ?", s.occ.ID).Update("title_override", "OVERRIDDEN LATER").Error; err != nil {
					t.Fatal(err)
				}
			}
			teenTitle := "Registration cancelled: " + tc.shown + " · " + s.day
			var stored models.Notification
			if err := s.app.DB.Where("user_id = ? AND kind = ?", s.teen.ID, notify.RegistrationCancelled).First(&stored).Error; err != nil {
				t.Fatal(err)
			}
			// The teen's notice is unchanged: it names the date.
			if stored.Title != teenTitle || stored.EmailTitle != teenTitle {
				t.Fatalf("the teen's notice is %q / %q, want %q", stored.Title, stored.EmailTitle, teenTitle)
			}
			var data map[string]any
			if err := json.Unmarshal([]byte(stored.Data), &data); err != nil || data[notify.DataEventTitle] != tc.shown {
				t.Fatalf("stored data %q, want the plain title %q (%v)", stored.Data, tc.shown, err)
			}
			if got := latestEmail(t, s.app, s.teen.Email); got.Subject != teenTitle {
				t.Fatalf("the teen's email subject is %q, want %q", got.Subject, teenTitle)
			}
			copyTitle := "Registration cancelled: " + tc.shown
			for _, g := range s.guardians {
				if got := copiesTo(t, s.app, g.Email); len(got) != 1 || got[0] != "Copy: "+copyTitle {
					t.Fatalf("%s copies %v, want [Copy: %s]", g.Email, got, copyTitle)
				}
				m := latestEmail(t, s.app, g.Email)
				rest := strings.ReplaceAll(m.Body, copyTitle, "")
				for _, bad := range []string{s.day, "Hidden fellowship", "SECRET GROUP PLACE", "/events/", "/groups/", "RENAMED LATER", "OVERRIDDEN LATER"} {
					if strings.Contains(m.Subject+rest, bad) {
						t.Errorf("%s: the copy carries %q: %q / %q", g.Email, bad, m.Subject, m.Body)
					}
				}
				if !strings.Contains(m.Body, stored.Body) || !strings.Contains(m.Body, "Copy: sent to sam because your church sends guardian copies.") {
					t.Errorf("%s: the copy lacks the fixed sentence or the marker: %q", g.Email, m.Body)
				}
			}
		})
	}
}

// A registration.cancelled notice with no usable plain title in its data (a row
// written before the title was stored, or a damaged one) gets no guardian
// copy, and the teen still gets their own email. The stored title is never
// split to recover the event's name, since a title can contain any separator.
func TestNoticeGuardianCopyNeedsThePlainTitle(t *testing.T) {
	for _, tc := range []struct {
		name string
		data map[string]any
	}{
		{"no data, as an older row", nil},
		{"data without the title", map[string]any{"eventId": 1}},
		{"empty title", map[string]any{notify.DataEventTitle: ""}},
		{"blank title", map[string]any{notify.DataEventTitle: "   "}},
		{"title that is not text", map[string]any{notify.DataEventTitle: 7}},
		{"title that is null", map[string]any{notify.DataEventTitle: nil}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newCopyFixture(t)
			f.hideEvent(t)
			const stored = "Registration cancelled: Retreat · 2031-01-02"
			id := f.noticeWithData(t, notify.RegistrationCancelled, "event", f.event.ID, notify.Vars{Event: "Retreat · 2031-01-02"}, tc.data)
			f.deliver(t, id)
			if got := latestEmail(t, f.app, f.teen.Email); got.Subject != stored {
				t.Fatalf("the teen's subject is %q, want %q", got.Subject, stored)
			}
			f.requireNoCopies(t, tc.name)
			for _, j := range copyJobs(t, f.app) {
				if j.Status != models.JobDone {
					t.Errorf("copy job %d is %s: a copy with nothing to show finishes quietly", j.ID, j.Status)
				}
			}
			if len(copyJobs(t, f.app)) != len(f.guardians) {
				t.Fatalf("copy jobs %d, want one per guardian: the control arm lost its jobs", len(copyJobs(t, f.app)))
			}
		})
	}
}
