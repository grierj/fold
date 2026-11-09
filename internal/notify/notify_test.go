package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"fold/internal/testdb"
	"fold/models"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestMain(m *testing.M) { os.Exit(testdb.Main(m)) }

func user(t *testing.T, db *gorm.DB, name string) uint {
	t.Helper()
	u := models.User{Email: name + "@example.com", Name: name, Role: models.RoleMember, Status: models.UserActive}
	if err := db.Create(&u).Error; err != nil {
		t.Fatal(err)
	}
	return u.ID
}

func rows(t *testing.T, db *gorm.DB, uid uint) []models.Notification {
	t.Helper()
	var out []models.Notification
	db.Where("user_id = ?", uid).Order("id").Find(&out)
	return out
}

// Every kind, rendered with teen and hidden-adult fixtures, says no email
// address or phone number (DEC-31, research 6.3a). Callers only pass
// names, so any contact detail would have to come from a kind's template.
func TestEveryKindRendersNoContactDetails(t *testing.T) {
	phone := regexp.MustCompile(`\+?\d[\d\s().-]{6,}\d`)
	fixtures := []Vars{
		{Actor: "Teen Tess", Person: "Kid Kai", Group: "Youth Group", Event: "Lock-in", When: "Fri, Dec 4 · 7:00 PM", Place: "Fellowship hall", Change: "added", Visibility: "public", GroupVis: "private", Note: "For visitors.", Count: 3},
		{Actor: "Hidden Hal", Person: "Hidden Hal", Change: "removed_from_group"},
		{},
	}
	if len(Kinds()) == 0 {
		t.Fatalf("registry has %d kinds", len(Kinds()))
	}
	for _, k := range Kinds() {
		for _, v := range fixtures {
			title, body := k.Render(v)
			text := title + "\n" + body
			if title == "" || strings.Contains(text, "@") || phone.MatchString(text) {
				t.Errorf("%s rendered %q", k.Key, text)
			}
		}
		if k.Waiting && k.BatchWindow > 0 {
			t.Errorf("%s: waiting kinds are never batched", k.Key)
		}
		if k.AllowPush && k.BatchWindow > 0 {
			t.Errorf("%s: a pushed kind isn't batched (a merge doesn't push again)", k.Key)
		}
		if len(k.Actions) > 2 {
			t.Errorf("%s: at most two inline actions", k.Key)
		}
	}
}

// A lock screen is public (DEC-124, research 2.4). Every kind's push text,
// rendered with teen, household and hidden-adult fixtures, carries no
// contact detail, teen badge, person, note, place, signed or video link;
// safety, consent and contact kinds say only the safe line; and a kind that
// doesn't push renders nothing.
func TestEveryKindsPushTextIsLockScreenSafe(t *testing.T) {
	phone := regexp.MustCompile(`\+?\d[\d\s().-]{6,}\d`)
	fixtures := []Vars{
		// A teen, a household child, contact details in the free text.
		{Actor: "Teen Tess", Person: "Kid Kai", Group: "Youth Group", Event: "Lock-in", When: "Fri, Dec 4 · 7:00 PM",
			Place: "14 Elm Street", Change: "time", Visibility: "public", GroupVis: "private",
			Note: "Call tess@example.com or 555-123-4567", Count: 3, Teen: true,
			ManageURL: "https://church.example/guest/manage/secret-token", VideoURL: "https://meet.example/room-9"},
		// A hidden adult: the caller left the group and place out.
		{Actor: "Hidden Hal", Person: "Hidden Hal", Event: "Supper", When: "Tue 7pm", Note: "hal@example.com", Change: "place"},
		{},
	}
	never := []string{"Teen Tess", "Kid Kai", "Hidden Hal", "14 Elm Street", "secret-token", "meet.example", "Call tess", "hal@example.com", "(teen)", "teen"}
	pushed := 0
	for _, k := range Kinds() {
		for _, v := range fixtures {
			title, body, ok := k.RenderForPush(v)
			if !k.AllowPush {
				if ok || title != "" || body != "" {
					t.Errorf("%s doesn't push but rendered %q %q", k.Key, title, body)
				}
				continue
			}
			text := title + "\n" + body
			if !ok || title == "" {
				t.Errorf("%s pushes but rendered no title", k.Key)
			}
			if strings.Contains(text, "@") || phone.MatchString(text) || strings.Contains(text, "http") {
				t.Errorf("%s push text %q has a contact detail or link", k.Key, text)
			}
			for _, s := range never {
				if strings.Contains(text, s) {
					t.Errorf("%s push text %q has %q", k.Key, text, s)
				}
			}
			if len(title) > 80 || len(body) > 120 {
				t.Errorf("%s push text is too long for a lock screen: %q", k.Key, text)
			}
			if k.Category == CatSafety && (title != SafePushTitle || body != SafePushBody) {
				t.Errorf("%s is a safety kind but pushes %q", k.Key, text)
			}
		}
		if k.AllowPush {
			pushed++
		}
		// An event's push never names its group, even when the caller let
		// the bell name it: the event's own title stands for it.
		if k.Category == CatEvents && k.AllowPush {
			title, body, _ := k.RenderForPush(Vars{Group: "Secret Circle", GroupVis: "invite only", Event: "Supper", When: "Tue 7pm", Change: "time"})
			if strings.Contains(title+body, "Secret Circle") {
				t.Errorf("%s push text names the group: %q %q", k.Key, title, body)
			}
		}
		// No push shows a place, even a changed one: a small group often
		// meets in a home and a lock screen is public (DEC-124, DEC-136).
		// Every Change a kind reads, with the place set each time.
		if k.AllowPush {
			for _, change := range []string{"", "time", "place", "time and place", "details", "added", "removed", "leader", "member",
				"approved", "not approved", "child", "teen", "teen_account", "1 date", "3 dates"} {
				title, body, _ := k.RenderForPush(Vars{Event: "Supper", When: "Tue 7pm", Place: "412 Elm Street", Change: change})
				for _, bad := range []string{"412 Elm Street", "412", "Elm"} {
					if strings.Contains(title+body, bad) {
						t.Errorf("%s push text (change %q) shows the place (%q): %q %q", k.Key, change, bad, title, body)
					}
				}
			}
		}
	}
	if pushed == 0 {
		t.Fatal("no kind pushes; the test checks nothing")
	}
	// The person whose role changed hears it in their own words; a group
	// they can't see is never named (the caller leaves it out).
	k, _ := Lookup(YourRole)
	for change, want := range map[string]string{"leader": "You're now a leader of Youth", "member": "You're no longer a leader of Youth"} {
		if title, _, ok := k.RenderForPush(Vars{Group: "Youth", Change: change, Person: "Hidden Hal", Teen: true}); !ok || title != want {
			t.Errorf("your role %s: %q", change, title)
		}
	}
	if title, _, _ := k.RenderForPush(Vars{Change: "leader"}); title != "You're now a leader of a group" {
		t.Errorf("your role, hidden group: %q", title)
	}
	// A row about someone in the household names nobody.
	for _, key := range []string{RegistrationPromoted, RegistrationRemoved} {
		k, _ := Lookup(key)
		if title, body, _ := k.RenderForPush(Vars{Person: "Sam", Event: "Supper"}); title != SafePushTitle || body != SafePushBody {
			t.Errorf("%s about a household person pushes %q %q", key, title, body)
		}
	}
}

// Send stores the push text on the row, where push.deliver reads it; a
// kind that doesn't push stores none.
func TestSendStoresPushText(t *testing.T) {
	db := testdb.Shared(t)
	uid := user(t, db, "recipient")
	ids, err := Send(db, Msg{Kind: OccurrenceCancelled, To: []uint{uid}, Link: "/events/1",
		Vars: Vars{Event: "Supper", When: "Tue 7pm", Place: "14 Elm Street"}})
	if err != nil || len(ids) != 1 {
		t.Fatalf("send: %v %v", ids, err)
	}
	var n models.Notification
	db.First(&n, ids[0])
	if n.PushTitle != "Cancelled: Supper" || n.PushBody != "Tue 7pm" {
		t.Fatalf("push text %q %q", n.PushTitle, n.PushBody)
	}
	ids, err = Send(db, Msg{Kind: MembershipLeft, To: []uint{uid}, Link: "/groups/1", Vars: Vars{Person: "Pat", Group: "Youth"}})
	if err != nil || len(ids) != 1 {
		t.Fatalf("send: %v %v", ids, err)
	}
	var quiet models.Notification
	db.First(&quiet, ids[0])
	if quiet.ID == 0 || quiet.PushTitle != "" || quiet.PushBody != "" {
		t.Fatalf("a kind that doesn't push stored %q %q", quiet.PushTitle, quiet.PushBody)
	}
}

func TestDepartureKindsRenderSummariesWithoutPrivateContext(t *testing.T) {
	departure, ok := Lookup(MembershipDeparture)
	if !ok || departure.Category != CatGroups || !departure.AllowEmail || len(departure.Actions) != 0 {
		t.Fatalf("departure kind: %+v ok=%v", departure, ok)
	}
	vars := Vars{Person: "Private Person", Group: "Hidden Group", KeptCount: 2, EndedCount: 3}
	title, body := departure.Render(vars)
	if title != "Your group membership ended" || !strings.Contains(body, "2 upcoming dates were kept and 3 ended") ||
		strings.Contains(title+body, vars.Person) || strings.Contains(title+body, vars.Group) {
		t.Fatalf("unsafe departure summary: %q %q", title, body)
	}
	emailTitle, emailBody := departure.RenderEmailSafe(vars)
	if strings.Contains(emailTitle+emailBody, vars.Person) || strings.Contains(emailTitle+emailBody, vars.Group) || !strings.Contains(emailBody, "2 upcoming dates were kept and 3 ended") {
		t.Fatalf("unsafe departure email: %q %q", emailTitle, emailBody)
	}

	left, ok := Lookup(MembershipLeft)
	if !ok || left.Category != CatGroups || !left.AllowEmail || len(left.Actions) != 0 {
		t.Fatalf("left kind: %+v ok=%v", left, ok)
	}
	leftTitle, _ := left.Render(vars)
	if leftTitle != "Private Person left Hidden Group" {
		t.Fatalf("leader summary title=%q", leftTitle)
	}
}

func TestOnlyApproveGroupIsAllowListed(t *testing.T) {
	// The v1 allow-list (5.12, DEC-44): approve a new group, and approve
	// a clean adult account request. Nothing else changes state inline.
	allowed := map[string]bool{GroupPending: true, AccountRequested: true}
	for _, k := range Kinds() {
		for _, a := range k.Actions {
			if !allowed[k.Key] || a.Key != ActionApproveGroup {
				t.Errorf("unexpected inline action %s on %s", a.Key, k.Key)
			}
		}
	}
	if !Allows(GroupPending, ActionApproveGroup) || !Allows(AccountRequested, ActionApproveAccount) ||
		Allows(GroupPending, "reject") || Allows(LeadershipChanged, ActionApproveGroup) || Allows("nope", ActionApproveGroup) {
		t.Fatal("Allows")
	}
}

func TestValidLink(t *testing.T) {
	for _, ok := range []string{"/", "/groups/3", "/admin?tab=approvals", "/groups/3#role-change-4"} {
		if !ValidLink(ok) {
			t.Errorf("%q refused", ok)
		}
	}
	for _, bad := range []string{"", "groups/3", "//evil.example", "https://evil.example", "javascript:alert(1)", "/\\evil", "/a\nb", "/x:y"} {
		if ValidLink(bad) {
			t.Errorf("%q accepted", bad)
		}
	}
	db := testdb.Shared(t)
	uid := user(t, db, "pat")
	if _, err := Send(db, Msg{Kind: MemberNotRestored, To: []uint{uid}, Link: "https://evil.example"}); err != ErrBadLink {
		t.Fatalf("bad link: %v", err)
	}
	// The table refuses it too, whatever writes the row.
	if err := db.Create(&models.Notification{UserID: uid, Kind: "x", Category: CatGroups, Title: "x", Link: "//evil"}).Error; err == nil {
		t.Fatal("table accepted //evil")
	}
}

func TestDedupeAndActorSkipped(t *testing.T) {
	db := testdb.Shared(t)
	a, b := user(t, db, "a"), user(t, db, "b")
	m := Msg{Kind: GroupPending, To: []uint{a, b, b}, Actor: &a, SubjectType: "group", SubjectID: 9, Link: "/admin", DedupeKey: "group.pending:9", Vars: Vars{Actor: "a", Group: "G"}}
	for i := 0; i < 2; i++ {
		if _, err := Send(db, m); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(rows(t, db, b)); n != 1 {
		t.Fatalf("b has %d rows", n)
	}
	if n := len(rows(t, db, a)); n != 0 {
		t.Fatalf("the actor was notified: %d", n)
	}
}

func TestBatching(t *testing.T) {
	register(Kind{Key: "test.joined", Category: CatGroups, BatchWindow: time.Hour,
		Render: func(v Vars) (string, string) {
			if v.Count > 1 {
				return v.Actor + " and " + itoa(v.Count-1) + " others joined " + v.Group, ""
			}
			return v.Actor + " joined " + v.Group, ""
		}})
	defer delete(registry, "test.joined")
	db := testdb.Shared(t)
	uid := user(t, db, "lead")
	send := func(who string) {
		t.Helper()
		ids, err := Send(db, Msg{Kind: "test.joined", To: []uint{uid}, Link: "/groups/1", GroupKey: "joined:1", Vars: Vars{Actor: who, Group: "Youth"}})
		if err != nil {
			t.Fatal(err)
		}
		if len(ids) != 1 {
			t.Fatalf("send returned %d ids", len(ids))
		}
	}
	send("Pat")
	send("Sam")
	send("Lee")
	got := rows(t, db, uid)
	if len(got) != 1 || got[0].BatchCount != 3 || got[0].Title != "Lee and 2 others joined Youth" {
		t.Fatalf("batched: %+v", got)
	}
	// Once read, the next one starts a new row.
	if _, err := SetRead(db, uid, got[0].ID, true); err != nil {
		t.Fatal(err)
	}
	send("Kim")
	if got = rows(t, db, uid); len(got) != 2 || got[1].Title != "Kim joined Youth" {
		t.Fatalf("after read: %+v", got)
	}
}

func itoa(n int) string { return strconv.Itoa(n) }

func TestStatesBadgeAndReadAll(t *testing.T) {
	db := testdb.Shared(t)
	uid, other := user(t, db, "admin"), user(t, db, "other")
	must := func(ids []uint, err error) []uint {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return ids
	}
	ids := must(Send(db, Msg{Kind: GroupPending, To: []uint{uid}, SubjectID: 5, Link: "/admin", Vars: Vars{Group: "G"}}))
	if len(ids) != 1 {
		t.Fatalf("send returned %d ids", len(ids))
	}
	must(Send(db, Msg{Kind: LeadershipChanged, To: []uint{uid}, SubjectID: 5, Link: "/groups/5"}))
	past := time.Now().Add(-time.Hour)
	must(Send(db, Msg{Kind: OccurrenceCancelled, To: []uint{uid}, SubjectID: 7, Link: "/events/1", ExpiresAt: &past}))
	unread, waiting, err := Badge(db, uid)
	if err != nil || unread != 1 || waiting != 1 {
		t.Fatalf("badge %d %d %v", unread, waiting, err)
	}
	if err := ReadAll(db, uid); err != nil {
		t.Fatal(err)
	}
	if unread, waiting, _ = Badge(db, uid); unread != 0 || waiting != 1 {
		t.Fatalf("after read-all: %d %d", unread, waiting)
	}
	page, _, _ := Feed(db, uid, FilterAll, "", 20)
	if len(page.Waiting) != 1 || len(page.Items) != 1 || page.WaitingTotal != 1 {
		t.Fatalf("feed: %+v", page)
	}
	// The expired cancellation shows under Archived only.
	arch, _, _ := Feed(db, uid, FilterArchived, "", 20)
	if len(arch.Items) != 1 || arch.Items[0].Kind != OccurrenceCancelled {
		t.Fatalf("archived: %+v", arch)
	}
	if n, err := Resolve(db, GroupPending, 5, &other, "approved"); err != nil || n != 1 {
		t.Fatalf("resolve %d %v", n, err)
	}
	if n, _ := Resolve(db, GroupPending, 5, &uid, "approved"); n != 0 {
		t.Fatal("resolved twice")
	}
	if _, waiting, _ = Badge(db, uid); waiting != 0 {
		t.Fatalf("waiting after resolve: %d", waiting)
	}
	// Own rows only.
	id := page.Items[0].ID
	if found, _ := SetArchived(db, other, id, true); found {
		t.Fatal("another user changed my row")
	}
	if found, _ := SetRead(db, other, id, false); found {
		t.Fatal("another user changed my row")
	}
	if _, ok := Get(db, other, id); ok {
		t.Fatal("another user read my row")
	}
	for _, on := range []bool{true, false} {
		if found, err := SetArchived(db, uid, id, on); !found || err != nil {
			t.Fatal("archive and undo", err)
		}
	}
	if _, ok, _ := Feed(db, uid, "bogus", "", 20); ok {
		t.Fatal("bogus filter accepted")
	}
}

func TestFeedCursor(t *testing.T) {
	db := testdb.Shared(t)
	uid := user(t, db, "pat")
	for i := 0; i < 5; i++ {
		if _, err := Send(db, Msg{Kind: LeadershipChanged, To: []uint{uid}, Link: "/"}); err != nil {
			t.Fatal(err)
		}
	}
	seen := map[uint]bool{}
	cursor := ""
	for pages := 0; pages < 5; pages++ {
		p, ok, err := Feed(db, uid, FilterAll, cursor, 2)
		if !ok || err != nil {
			t.Fatal("cursor refused")
		}
		for _, n := range p.Items {
			if seen[n.ID] {
				t.Fatalf("repeated %d", n.ID)
			}
			seen[n.ID] = true
		}
		if cursor = p.Next; cursor == "" {
			break
		}
	}
	if len(seen) != 5 {
		t.Fatalf("paged %d of 5", len(seen))
	}
}

func TestPrune(t *testing.T) {
	db := testdb.Shared(t)
	uid := user(t, db, "pat")
	now := time.Now().UTC()
	day := 24 * time.Hour
	mk := func(title string, age time.Duration, read, waiting bool) {
		t.Helper()
		n := models.Notification{UserID: uid, Kind: LeadershipChanged, Category: CatAdmin, Title: title, Link: "/",
			Waiting: waiting, CreatedAt: now.Add(-age), UpdatedAt: now.Add(-age)}
		if read {
			at := now.Add(-age)
			n.ReadAt = &at
		}
		if err := db.Create(&n).Error; err != nil {
			t.Fatal(err)
		}
	}
	mk("read old", 91*day, true, false)
	mk("read young", 89*day, true, false)
	mk("unread 100d", 100*day, false, false)
	mk("unread old", 181*day, false, false)
	mk("open task", 400*day, false, true)
	n, err := Prune(db, now)
	if err != nil || n != 2 {
		t.Fatalf("pruned %d %v", n, err)
	}
	var left []string
	db.Model(&models.Notification{}).Order("id").Pluck("title", &left)
	if strings.Join(left, ",") != "read young,unread 100d,open task" {
		t.Fatalf("left %v", left)
	}
}

// M1.2 (5.12): a sensitive kind's email names nobody and only links into
// Fold. No contact-request, consent, or minor kind exists yet (M1.10/M1.11),
// so a synthetic kind proves the RenderEmailSafe mechanism; the delivery
// path uses RenderForEmail, which prefers the safe variant.
func TestRenderEmailSafeNamesNobody(t *testing.T) {
	register(Kind{
		Key:        "test.contact_request",
		Category:   CatAdmin,
		AllowEmail: true,
		Render: func(v Vars) (string, string) {
			return v.Actor + " requested " + v.Person + "'s contact details",
				"Sign in to review it."
		},
		RenderEmailSafe: func(v Vars) (string, string) {
			return "You have a contact request in Fold",
				"Sign in to review it."
		},
	})
	defer delete(registry, "test.contact_request")

	k, ok := Lookup("test.contact_request")
	if !ok || !k.AllowEmail {
		t.Fatal("synthetic kind not registered")
	}
	v := Vars{Actor: "Pat Lee", Person: "Sam Kim"}
	title, body := k.RenderForEmail(v)
	text := title + "\n" + body
	for _, name := range []string{"Pat", "Lee", "Sam", "Kim"} {
		if strings.Contains(text, name) {
			t.Fatalf("email names someone: %q", text)
		}
	}
	// And Send stores the safe rendering on the row, which is what the
	// delivery path emails.
	db := testdb.Shared(t)
	uid := user(t, db, "recipient")
	ids, err := Send(db, Msg{Kind: "test.contact_request", To: []uint{uid},
		SubjectID: 1, Link: "/admin", Vars: v})
	if err != nil || len(ids) != 1 {
		t.Fatalf("send: %v %d", err, len(ids))
	}
	var n models.Notification
	if err := db.First(&n, ids[0]).Error; err != nil {
		t.Fatalf("row: %v", err)
	}
	if n.EmailTitle != title || n.EmailBody != body {
		t.Fatalf("row stores %q %q, want safe rendering", n.EmailTitle, n.EmailBody)
	}
	if strings.Contains(n.EmailTitle+n.EmailBody, "Pat") {
		t.Fatalf("stored email rendering names someone")
	}
}

func TestContactKindsNavigationOnlySafeEmail(t *testing.T) {
	for _, key := range []string{ContactSubjectReview, ContactAdminReview, ContactUpdated} {
		k, ok := Lookup(key)
		if !ok {
			t.Fatalf("missing %s", key)
		}
		if len(k.Actions) != 0 || k.BatchWindow != 0 || !k.AllowEmail || k.RenderEmailSafe == nil {
			t.Fatalf("unsafe contact kind %#v", k)
		}
		if k.Waiting != (key != ContactUpdated) {
			t.Fatalf("wrong task state %s", key)
		}
		title, body := k.RenderForEmail(Vars{Actor: "Private requester", Person: "Private subject", Group: "Hidden group", Event: "Hidden event", Note: "private reason"})
		if strings.Contains(title+body, "Private") || strings.Contains(title+body, "Hidden") || strings.Contains(title+body, "private reason") {
			t.Fatalf("sensitive email %s: %s %s", key, title, body)
		}
	}
}

// updated_at orders the feed and drives retention, so opening the bell,
// reading, archiving or resolving must not move it.
func TestStateChangesLeaveUpdatedAtAlone(t *testing.T) {
	db := testdb.Shared(t)
	uid, other := user(t, db, "stamp"), user(t, db, "stamp-other")
	old := time.Now().UTC().Add(-48 * time.Hour).Truncate(time.Microsecond)
	task, err := Send(db, Msg{Kind: GroupPending, To: []uint{uid}, SubjectID: 905, Link: "/admin", Vars: Vars{Group: "G"}})
	if err != nil || len(task) != 1 {
		t.Fatalf("send task: %v %v", task, err)
	}
	plain, err := Send(db, Msg{Kind: LeadershipChanged, To: []uint{uid}, SubjectID: 905, Link: "/groups/5"})
	if err != nil || len(plain) != 1 {
		t.Fatalf("send plain: %v %v", plain, err)
	}
	ids := []uint{task[0], plain[0]}
	if err := db.Model(&models.Notification{}).Where("id IN ?", ids).UpdateColumn("updated_at", old).Error; err != nil {
		t.Fatal(err)
	}
	unchanged := func(step string) {
		t.Helper()
		var got []models.Notification
		if err := db.Where("id IN ?", ids).Find(&got).Error; err != nil || len(got) != 2 {
			t.Fatalf("%s: load %v %v", step, got, err)
		}
		for _, n := range got {
			if !n.UpdatedAt.Equal(old) {
				t.Fatalf("%s moved updated_at of %d to %v", step, n.ID, n.UpdatedAt)
			}
		}
	}
	if found, err := SetRead(db, uid, plain[0], true); !found || err != nil {
		t.Fatal("read", err)
	}
	unchanged("read")
	if err := ReadAll(db, uid); err != nil {
		t.Fatal(err)
	}
	unchanged("read-all")
	if found, err := SetArchived(db, uid, plain[0], true); !found || err != nil {
		t.Fatal("archive", err)
	}
	unchanged("archive")
	if err := ArchiveRead(db, uid); err != nil {
		t.Fatal(err)
	}
	unchanged("archive-read")
	if n, err := Resolve(db, GroupPending, 905, &other, "approved"); err != nil || n != 1 {
		t.Fatalf("resolve %d %v", n, err)
	}
	unchanged("resolve")
}

// Delivery intent and the job either commit with the bell or roll back with it.
func TestSendWritesDeliveryIntentInTransaction(t *testing.T) {
	register(Kind{Key: "test.outbox.batch", Category: CatGroups, AllowEmail: true, BatchWindow: time.Hour,
		Render: func(v Vars) (string, string) { return v.Actor + " " + itoa(v.Count), "" }})
	defer delete(registry, "test.outbox.batch")
	for _, tc := range []struct {
		name, kind                        string
		rollback, repeat, merge, bellOnly bool
		want                              int64
	}{
		{name: "commit", kind: GroupPending, want: 1},
		{name: "rollback", kind: GroupPending, rollback: true},
		{name: "neither channel", kind: PushDevicesOff},
		{name: "dedupe", kind: GroupPending, repeat: true, want: 1},
		{name: "merge", kind: "test.outbox.batch", merge: true, want: 1},
		{name: "bell only", kind: GroupPending, bellOnly: true, want: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := testdb.Shared(t)
			uid := user(t, db, "recipient")
			m := Msg{Kind: tc.kind, To: []uint{uid}, Link: "/admin", BellOnly: tc.bellOnly, Vars: Vars{Actor: "Pat"}}
			if tc.repeat {
				m.DedupeKey = "once"
			}
			if tc.merge {
				m.GroupKey = "joined"
			}
			var ids []uint
			rollback := errors.New("rollback")
			err := db.Transaction(func(tx *gorm.DB) error {
				var err error
				ids, err = Send(tx, m)
				if err != nil {
					return err
				}
				if len(ids) != 1 {
					t.Fatalf("first send: %v", ids)
				}
				if tc.rollback {
					return rollback
				}
				return nil
			})
			if !errors.Is(err, rollback) && err != nil {
				t.Fatal(err)
			}
			if tc.repeat || tc.merge {
				if err := db.Transaction(func(tx *gorm.DB) error {
					second, err := Send(tx, m)
					if err != nil {
						return err
					}
					if tc.repeat && len(second) != 0 {
						t.Fatalf("dedupe IDs: %v", second)
					}
					if tc.merge && (len(second) != 1 || second[0] != ids[0]) {
						t.Fatalf("merge IDs: %v", second)
					}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			}
			var deliveries []models.NotificationDelivery
			if err := db.Find(&deliveries).Error; err != nil {
				t.Fatal(err)
			}
			if int64(len(deliveries)) != tc.want {
				t.Fatalf("deliveries: %+v, want %d", deliveries, tc.want)
			}
			if tc.want == 1 {
				d := deliveries[0]
				status, reason := models.DeliveryQueued, ""
				if tc.bellOnly {
					status, reason = models.DeliverySuppressed, "bell only"
				}
				if d.NotificationID != ids[0] || d.UserID != uid || d.Channel != models.DeliveryEmail || d.Status != status || d.Reason != reason {
					t.Fatalf("delivery: %+v", d)
				}
			}
			var queued []models.Job
			if err := db.Find(&queued).Error; err != nil {
				t.Fatal(err)
			}
			wantJobs := tc.want
			if tc.bellOnly {
				wantJobs = 0
			}
			if int64(len(queued)) != wantJobs {
				t.Fatalf("jobs: %+v, want %d", queued, wantJobs)
			}
			if wantJobs == 1 {
				j := queued[0]
				var payload struct {
					NotificationID uint `json:"notificationId"`
				}
				if err := json.Unmarshal([]byte(j.Payload), &payload); err != nil || payload.NotificationID != ids[0] {
					t.Fatalf("payload %s: %v", j.Payload, err)
				}
				if j.Kind != DeliverKind || j.UniqueKey == nil || *j.UniqueKey != DeliverKey(ids[0]) || j.Status != models.JobQueued || j.MaxAttempts != 8 || j.RunAt.After(time.Now()) {
					t.Fatalf("job: %+v", j)
				}
			}
		})
	}
}

func TestMergeWaitsForAnInFlightSend(t *testing.T) {
	register(Kind{Key: "test.outbox.lock", Category: CatGroups, AllowEmail: true, BatchWindow: time.Hour,
		Render: func(v Vars) (string, string) { return v.Actor + " " + itoa(v.Count), "" }})
	defer delete(registry, "test.outbox.lock")
	db := testdb.Shared(t)
	uid := user(t, db, "recipient")
	m := Msg{Kind: "test.outbox.lock", To: []uint{uid}, Link: "/groups/1", GroupKey: "joined", Vars: Vars{Actor: "Pat"}}
	ids, err := Send(db, m)
	if err != nil {
		t.Fatal(err)
	}
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	defer tx.Rollback()
	var held uint
	if err := tx.Raw("SELECT id FROM notification_deliveries WHERE notification_id = ? AND channel = 'email' FOR UPDATE", ids[0]).Scan(&held).Error; err != nil {
		t.Fatal(err)
	}
	if held == 0 {
		t.Fatal("no delivery to hold")
	}
	m.Vars.Actor = "Sam"
	err = db.Transaction(func(merging *gorm.DB) error {
		if err := merging.Exec("SET LOCAL lock_timeout = '200ms'").Error; err != nil {
			return err
		}
		_, err := Send(merging, m)
		return err
	})
	if err == nil || !strings.Contains(err.Error(), "lock timeout") {
		t.Fatalf("merge while sending: %v", err)
	}
	if err := tx.Commit().Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Transaction(func(merging *gorm.DB) error { _, err := Send(merging, m); return err }); err != nil {
		t.Fatal(err)
	}
	var n models.Notification
	if err := db.First(&n, ids[0]).Error; err != nil {
		t.Fatal(err)
	}
	if n.EmailTitle != "Sam 2" || n.BatchCount != 2 {
		t.Fatalf("merged: %+v", n)
	}
	var count int64
	if err := db.Model(&models.Job{}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("merge added a job: %d", count)
	}
}

// The channel ledger and jobs inserts stay batched as the admin list grows.
func TestSendBatchesDeliveryIntent(t *testing.T) {
	db := testdb.Shared(t)
	var recipients []uint
	for i := 0; i < 50; i++ {
		recipients = append(recipients, user(t, db, fmt.Sprintf("admin%d", i)))
	}
	writes := 0
	const callback = "test:delivery_writes"
	if err := db.Callback().Raw().Before("gorm:raw").Register(callback, func(tx *gorm.DB) {
		sql := tx.Statement.SQL.String()
		if strings.HasPrefix(sql, "INSERT INTO notification_deliveries ") || strings.HasPrefix(sql, "INSERT INTO jobs ") {
			writes++
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer db.Callback().Raw().Remove(callback)
	if err := db.Transaction(func(tx *gorm.DB) error {
		_, err := Send(tx, Msg{Kind: GroupPending, To: recipients, Link: "/admin"})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if writes != 2 {
		t.Fatalf("50-admin delivery fan-out used %d statements, want 2", writes)
	}
	for _, table := range []any{&models.NotificationDelivery{}, &models.Job{}} {
		var count int64
		if err := db.Model(table).Count(&count).Error; err != nil {
			t.Fatal(err)
		}
		if count != 50 {
			t.Fatalf("%T rows: %d", table, count)
		}
	}
}

// ---------------------------------------------------------------------------
// Erasure barriers (M7.4, R4-N). Every wait is a backend observed blocking on a
// named other backend in pg_blocking_pids, never a sleep. The "erasure" is the
// test-owned statement sequence that matters to notices in apiAdminDeleteUser:
// the user row FOR UPDATE, the notification delete, the soft delete. No contact
// gate is taken anywhere in this package: these are the plain-transaction cases.
// ---------------------------------------------------------------------------

// txWithPID begins a transaction and reports its backend, so a test can show
// which session waits on which. It rolls back at cleanup if the test did not.
func txWithPID(t *testing.T, db *gorm.DB) (*gorm.DB, int) {
	t.Helper()
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	t.Cleanup(func() { tx.Rollback() })
	var pid int
	if err := tx.Raw("SELECT pg_backend_pid()").Scan(&pid).Error; err != nil {
		t.Fatal(err)
	}
	return tx, pid
}

// waitBlocked returns once the waiter backend is blocked by the blocker backend.
func waitBlocked(t *testing.T, db *gorm.DB, waiter, blocker int) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		var blocked bool
		if err := db.Raw("SELECT ? = ANY(pg_blocking_pids(?))", blocker, waiter).Scan(&blocked).Error; err != nil {
			t.Fatal(err)
		}
		if blocked {
			return
		}
		select {
		case <-tick.C:
		case <-deadline.C:
			t.Fatalf("session %d never waited on session %d", waiter, blocker)
		}
	}
}

// async runs fn on its own goroutine; await collects its error.
func async(fn func() error) <-chan error {
	done := make(chan error, 1)
	go func() { done <- fn() }()
	return done
}

func await(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(10 * time.Second):
		t.Fatal("a database participant never finished")
		return nil
	}
}

func commit(t *testing.T, tx *gorm.DB) {
	t.Helper()
	if err := tx.Commit().Error; err != nil {
		t.Fatal(err)
	}
}

// eraseUser is what an erasure does to a person's notices: it takes the user
// row FOR UPDATE first, deletes their notifications (which cascades their
// deliveries), then soft-deletes the user.
func eraseUser(tx *gorm.DB, uid uint) error {
	if err := tx.Exec("SELECT id FROM users WHERE id = ? FOR UPDATE", uid).Error; err != nil {
		return err
	}
	if err := tx.Exec("DELETE FROM notifications WHERE user_id = ?", uid).Error; err != nil {
		return err
	}
	return tx.Exec("UPDATE users SET deleted_at = now() WHERE id = ?", uid).Error
}

func countWhere(t *testing.T, db *gorm.DB, table, where string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := db.Table(table).Where(where, args...).Count(&n).Error; err != nil {
		t.Fatal(err)
	}
	return n
}

func intentCounts(t *testing.T, db *gorm.DB) (deliveries, queuedJobs int64) {
	t.Helper()
	return countWhere(t, db, "notification_deliveries", "1 = 1"), countWhere(t, db, "jobs", "1 = 1")
}

// A recipient who is living or merely disabled gets the notice; a deleted,
// missing, zero, duplicate or acting recipient does not, and the ids come back
// in the order the recipients were named.
func TestSendRecipientLiveness(t *testing.T) {
	db := testdb.Shared(t)
	living, disabled, deleted := user(t, db, "living"), user(t, db, "disabled"), user(t, db, "deleted")
	actor, dup := user(t, db, "actor"), user(t, db, "dup")
	if err := db.Model(&models.User{}).Where("id = ?", disabled).Update("status", models.UserDisabled).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Delete(&models.User{}, deleted).Error; err != nil {
		t.Fatal(err)
	}
	missing := uint(987654)
	var ids []uint
	if err := db.Transaction(func(tx *gorm.DB) error {
		var err error
		ids, err = Send(tx, Msg{Kind: GroupPending, To: []uint{living, dup, dup, 0, deleted, disabled, actor, living, missing},
			Actor: &actor, Link: "/admin", Vars: Vars{Actor: "Pat"}})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var got []models.Notification
	if err := db.Where("id IN ?", ids).Find(&got).Error; err != nil {
		t.Fatal(err)
	}
	owner := map[uint]uint{}
	for _, n := range got {
		owner[n.ID] = n.UserID
	}
	var order []uint
	for _, id := range ids {
		order = append(order, owner[id])
	}
	if fmt.Sprint(order) != fmt.Sprint([]uint{living, dup, disabled}) {
		t.Fatalf("notified %v, want living, dup, disabled in that order (%v)", order, []uint{living, dup, disabled})
	}
	if n := countWhere(t, db, "notifications", "user_id IN ?", []uint{deleted, actor, missing}); n != 0 {
		t.Fatalf("%d notices for a deleted, acting or missing recipient", n)
	}
	if n := countWhere(t, db, "notifications", "1 = 1"); n != 3 {
		t.Fatalf("%d notices, want 3", n)
	}
	if deliveries, queued := intentCounts(t, db); deliveries != 3 || queued != 3 {
		t.Fatalf("delivery intent: %d deliveries, %d jobs, want 3 and 3", deliveries, queued)
	}
}

// The lock is one bulk statement for the recipients, one more per 500, and
// none per Send when a fan-out shares a Scope. Sorting happens before
// batching, so the statements together still lock in ascending id order.
func TestRecipientLocksAreBulk(t *testing.T) {
	db := testdb.Shared(t)
	users := make([]models.User, recipientLockBatch+1)
	for i := range users {
		users[i] = models.User{Email: fmt.Sprintf("bulk%d@example.com", i), Name: fmt.Sprintf("Bulk %d", i), Role: models.RoleMember, Status: models.UserActive}
	}
	if err := db.CreateInBatches(&users, 100).Error; err != nil {
		t.Fatal(err)
	}
	var all []uint
	for _, u := range users {
		all = append(all, u.ID)
	}
	var locks atomic.Int64
	const callback = "test:count_recipient_locks"
	if err := db.Callback().Row().Before("gorm:row").Register(callback, func(tx *gorm.DB) {
		sql := tx.Statement.SQL.String()
		if strings.Contains(sql, "FROM users WHERE id IN") && strings.Contains(sql, "FOR KEY SHARE") {
			locks.Add(1)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Callback().Row().Remove(callback) })
	for _, tc := range []struct {
		name string
		ids  []uint
		want int64
	}{{"nobody", nil, 0}, {"three people", all[:3], 1}, {"one more than a batch", all, 2}} {
		locks.Store(0)
		live := -1
		if err := db.Transaction(func(tx *gorm.DB) error {
			return WithRecipients(tx, append([]uint{0}, tc.ids...), func(sc *Scope) error {
				live = len(sc.s.live)
				return nil
			})
		}); err != nil {
			t.Fatal(err)
		}
		if locks.Load() != tc.want || live != len(tc.ids) {
			t.Fatalf("%s: %d lock statements for %d locked, want %d for %d", tc.name, locks.Load(), live, tc.want, len(tc.ids))
		}
	}
	// A fan-out of one Send per person shares one scope: the lock statement runs
	// once, whatever the number of Sends.
	locks.Store(0)
	if err := db.Transaction(func(tx *gorm.DB) error {
		return WithRecipients(tx, all[:3], func(sc *Scope) error {
			if locks.Load() != 1 {
				t.Fatalf("WithRecipients ran %d lock statements, want 1", locks.Load())
			}
			for _, id := range all[:3] {
				ids, err := Send(tx, Msg{Kind: GroupPending, To: []uint{id}, Link: "/admin", Scope: sc})
				if err != nil || len(ids) != 1 {
					t.Fatalf("send with a shared scope: %v %v", ids, err)
				}
			}
			if locks.Load() != 1 {
				t.Fatalf("three Sends with a scope ran %d more lock statements", locks.Load()-1)
			}
			return nil
		})
	}); err != nil {
		t.Fatal(err)
	}
}

// A Scope is proof that WithRecipients locked these people in this transaction.
// One that was not made by it (zero, or built by hand even with every private
// field filled in), one used after its WithRecipients call returned or after its
// transaction ended, one from another transaction or used outside any, and one
// that does not name a recipient of the Send are all refused with ErrBadScope
// before anything is written. Handles derived from the same transaction (a
// session, a nested savepoint) share its identity and are accepted.
func TestRecipientScopeRefusesForgedStaleAndForeignUse(t *testing.T) {
	db := testdb.Shared(t)
	a, b := user(t, db, "a"), user(t, db, "b")
	msg := func(sc *Scope, to ...uint) Msg { return Msg{Kind: GroupPending, To: to, Link: "/admin", Scope: sc} }
	refused := func(t *testing.T, h *gorm.DB, m Msg) {
		t.Helper()
		if ids, err := Send(h, m); !errors.Is(err, ErrBadScope) || len(ids) != 0 {
			t.Fatalf("want ErrBadScope and no notices, got %v %v", ids, err)
		}
	}
	none := func(t *testing.T) {
		t.Helper()
		if n := countWhere(t, db, "notifications", "1 = 1"); n != 0 {
			t.Fatalf("%d notices were written by a refused Send", n)
		}
	}
	t.Run("forged or zero", func(t *testing.T) {
		tx, _ := txWithPID(t, db)
		refused(t, tx, msg(&Scope{}, a))
		// The shape of a real scope, filled in by hand: closed, then open but
		// bound to no transaction. Neither is a scope WithRecipients made.
		hand := &scopeState{asked: map[uint]bool{a: true}, live: map[uint]bool{a: true}}
		refused(t, tx, msg(&Scope{s: hand}, a))
		hand.open.Store(true)
		refused(t, tx, msg(&Scope{s: hand}, a))
		tx.Rollback()
		none(t)
	})
	t.Run("used after its call returned, then after its transaction ended", func(t *testing.T) {
		// This subtest commits one valid send to exercise the scope after its
		// transaction ends. Keep that notice and its job out of later cases.
		t.Cleanup(func() {
			if err := db.Exec("DELETE FROM notifications WHERE user_id = ?", a).Error; err != nil {
				t.Errorf("clear committed notice: %v", err)
			}
			if err := db.Exec("DELETE FROM jobs WHERE kind = ?", DeliverKind).Error; err != nil {
				t.Errorf("clear committed delivery job: %v", err)
			}
		})
		tx, _ := txWithPID(t, db)
		var kept *Scope
		if err := WithRecipients(tx, []uint{a}, func(sc *Scope) error {
			kept = sc
			ids, err := Send(tx, msg(sc, a))
			if err != nil || len(ids) != 1 {
				t.Fatalf("inside the call: %v %v", ids, err)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		refused(t, tx, msg(kept, a)) // the transaction is still open; the scope is not
		commit(t, tx)
		refused(t, db, msg(kept, a))
		later, _ := txWithPID(t, db)
		refused(t, later, msg(kept, a))
		later.Rollback()
		if n := countWhere(t, db, "notifications", "user_id = ?", a); n != 1 {
			t.Fatalf("%d notices, want only the one sent inside the call", n)
		}
	})
	t.Run("another transaction, or none", func(t *testing.T) {
		first, _ := txWithPID(t, db)
		second, _ := txWithPID(t, db)
		if err := WithRecipients(first, []uint{a}, func(sc *Scope) error {
			refused(t, second, msg(sc, a))
			refused(t, db, msg(sc, a))
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		first.Rollback()
		second.Rollback()
		none(t)
	})
	t.Run("a recipient the scope did not lock", func(t *testing.T) {
		tx, _ := txWithPID(t, db)
		if err := WithRecipients(tx, []uint{a}, func(sc *Scope) error {
			refused(t, tx, msg(sc, a, b))
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		tx.Rollback()
		none(t)
	})
	t.Run("protection needs a transaction", func(t *testing.T) {
		err := WithRecipients(db, []uint{a}, func(*Scope) error {
			t.Fatal("the callback ran without a transaction")
			return nil
		})
		if !errors.Is(err, ErrNoTransaction) {
			t.Fatalf("want ErrNoTransaction, got %v", err)
		}
	})
	t.Run("handles of the same transaction are accepted", func(t *testing.T) {
		tx, _ := txWithPID(t, db)
		if err := WithRecipients(tx, []uint{a, b}, func(sc *Scope) error {
			if ids, err := Send(tx.Session(&gorm.Session{NewDB: true}), msg(sc, a)); err != nil || len(ids) != 1 {
				t.Fatalf("session handle: %v %v", ids, err)
			}
			return tx.Transaction(func(inner *gorm.DB) error {
				ids, err := Send(inner, msg(sc, b))
				if err != nil || len(ids) != 1 {
					t.Fatalf("savepoint handle: %v %v", ids, err)
				}
				return nil
			})
		}); err != nil {
			t.Fatal(err)
		}
		commit(t, tx)
		if n := countWhere(t, db, "notifications", "1 = 1"); n != 2 {
			t.Fatalf("%d notices, want 2", n)
		}
	})
}

// Rolling back a savepoint that predates the recipient lock releases that lock
// while the transaction and the scope go on. The scope watches the statements
// GORM sends (txguard) and, once such a rollback happened, refuses every later
// Send that carries it, without a query. A rollback inside the call, to a
// savepoint the call made, keeps the locks and the scope.
func TestRecipientScopeAfterSavepointRollback(t *testing.T) {
	msg := func(sc *Scope, to ...uint) Msg { return Msg{Kind: GroupPending, To: to, Link: "/admin", Scope: sc} }

	// The real hazard: the lock is gone, a separate erasure commits, and the
	// scope must not let Send write a notice for the erased account.
	t.Run("a rollback to an older savepoint, then an erasure, then a Send", func(t *testing.T) {
		db := testdb.Shared(t)
		uid, other := user(t, db, "savepoint"), user(t, db, "other")
		tx, _ := txWithPID(t, db)
		if err := tx.SavePoint("before_recipients").Error; err != nil {
			t.Fatal(err)
		}
		refused := false
		err := WithRecipients(tx, []uint{uid, other}, func(scope *Scope) error {
			if err := tx.RollbackTo("before_recipients").Error; err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := db.WithContext(ctx).Transaction(func(eraser *gorm.DB) error {
				return eraseUser(eraser, uid)
			}); err != nil {
				return err
			}
			ids, err := Send(tx, msg(scope, uid))
			if !errors.Is(err, ErrBadScope) || len(ids) != 0 {
				return fmt.Errorf("want ErrBadScope and no notice after erasure, got %v %v", ids, err)
			}
			// The scope as a whole is spent, not just the erased recipient.
			if ids, err := Send(tx, msg(scope, other)); !errors.Is(err, ErrBadScope) || len(ids) != 0 {
				return fmt.Errorf("want the whole scope refused, got %v %v", ids, err)
			}
			refused = true
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if !refused {
			t.Fatal("the callback did not reach the refusals")
		}
		// A Send with no scope locks for itself, and finds the erased account gone.
		if ids, err := Send(tx, msg(nil, uid)); err != nil || len(ids) != 0 {
			t.Fatalf("an unscoped send to an erased account: %v %v", ids, err)
		}
		commit(t, tx)
		if n := countWhere(t, db, "notifications", "user_id = ?", uid); n != 0 {
			t.Fatalf("%d notices remain for erased user", n)
		}
	})

	// Every way GORM can send that rollback, and text it cannot read.
	rollbacks := []struct {
		name string
		do   func(tx *gorm.DB) error
	}{
		{"RollbackTo", func(tx *gorm.DB) error { return tx.RollbackTo("older").Error }},
		{"RollbackTo on a fresh session handle", func(tx *gorm.DB) error {
			return tx.Session(&gorm.Session{NewDB: true}).RollbackTo("older").Error
		}},
		{"Exec with the SAVEPOINT word", func(tx *gorm.DB) error { return tx.Exec("ROLLBACK TO SAVEPOINT older").Error }},
		{"Exec without it, lower case", func(tx *gorm.DB) error { return tx.Exec("rollback to older").Error }},
		{"Exec with WORK", func(tx *gorm.DB) error { return tx.Exec("ROLLBACK WORK TO older;").Error }},
		{"Exec after a comment", func(tx *gorm.DB) error { return tx.Exec("/* x */ ROLLBACK TO older").Error }},
		{"Exec as the second statement", func(tx *gorm.DB) error { return tx.Exec("SELECT 1; ROLLBACK TO older").Error }},
		{"Raw", func(tx *gorm.DB) error { return tx.Raw("ROLLBACK TO older").Row().Err() }},
	}
	for _, rb := range rollbacks {
		t.Run("refused after "+rb.name, func(t *testing.T) {
			db := testdb.Shared(t)
			uid := user(t, db, "variant")
			tx, _ := txWithPID(t, db)
			if err := tx.SavePoint("older").Error; err != nil {
				t.Fatal(err)
			}
			err := WithRecipients(tx, []uint{uid}, func(scope *Scope) error {
				// The statement may fail in Postgres after the watch saw it
				// (a text the database refuses); the scope is spent either way.
				_ = rb.do(tx)
				if ids, err := Send(tx, msg(scope, uid)); !errors.Is(err, ErrBadScope) || len(ids) != 0 {
					return fmt.Errorf("want ErrBadScope and nothing written, got %v %v", ids, err)
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			tx.Rollback()
			if n := countWhere(t, db, "notifications", "1 = 1"); n != 0 {
				t.Fatalf("%d notices written under a spent scope", n)
			}
		})
	}

	t.Run("savepoints made inside the call keep the locks and the scope", func(t *testing.T) {
		db := testdb.Shared(t)
		uid := user(t, db, "inside")
		tx, _ := txWithPID(t, db)
		if err := tx.SavePoint("older").Error; err != nil {
			t.Fatal(err)
		}
		err := WithRecipients(tx, []uint{uid}, func(scope *Scope) error {
			send := func() error {
				ids, err := Send(tx, msg(scope, uid))
				if err != nil || len(ids) != 1 {
					return fmt.Errorf("send under the scope: %v %v", ids, err)
				}
				return nil
			}
			if err := tx.SavePoint("inside").Error; err != nil {
				return err
			}
			if err := send(); err != nil { // undone by the next line
				return err
			}
			if err := tx.RollbackTo("inside").Error; err != nil {
				return err
			}
			if err := send(); err != nil { // undone by the next line
				return err
			}
			if err := tx.RollbackTo("inside").Error; err != nil { // the name survives a rollback to it
				return err
			}
			if err := send(); err != nil { // kept (1)
				return err
			}
			if err := tx.Exec("RELEASE SAVEPOINT inside").Error; err != nil {
				return err
			}
			// A nested transaction that fails rolls back to its own savepoint.
			failed := tx.Transaction(func(inner *gorm.DB) error {
				if ids, err := Send(inner, msg(scope, uid)); err != nil || len(ids) != 1 {
					return fmt.Errorf("send in the nested transaction: %v %v", ids, err)
				}
				return errors.New("the nested work failed") // its notice is undone
			})
			if failed == nil || failed.Error() != "the nested work failed" {
				return fmt.Errorf("want the nested failure back, got %v", failed)
			}
			return send() // kept (2)
		})
		if err != nil {
			t.Fatal(err)
		}
		probe, _ := txWithPID(t, db)
		if err := probe.Exec("SELECT id FROM users WHERE id = ? FOR UPDATE NOWAIT", uid).Error; err == nil ||
			!strings.Contains(err.Error(), "could not obtain lock") {
			t.Fatalf("the recipient is not locked by the open transaction: %v", err)
		}
		probe.Rollback()
		commit(t, tx)
		if n := countWhere(t, db, "notifications", "user_id = ?", uid); n != 2 {
			t.Fatalf("%d notices, want the 2 sent outside the undone savepoints", n)
		}
	})

	t.Run("a rollback after the call, or before a later call, does not spend a new scope", func(t *testing.T) {
		db := testdb.Shared(t)
		uid := user(t, db, "later")
		tx, _ := txWithPID(t, db)
		if err := tx.SavePoint("older").Error; err != nil {
			t.Fatal(err)
		}
		if err := WithRecipients(tx, []uint{uid}, func(*Scope) error { return nil }); err != nil {
			t.Fatal(err)
		}
		if err := tx.RollbackTo("older").Error; err != nil {
			t.Fatal(err)
		}
		if err := WithRecipients(tx, []uint{uid}, func(scope *Scope) error {
			ids, err := Send(tx, msg(scope, uid))
			if err != nil || len(ids) != 1 {
				return fmt.Errorf("a new scope after the rollback: %v %v", ids, err)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		tx.Rollback()
	})

	// The watch is a GORM plugin; a handle opened without it cannot prove a lock.
	t.Run("a database handle without the plugin gets no scope", func(t *testing.T) {
		url, err := testdb.URL(testdb.Fresh(t, false))
		if err != nil {
			t.Fatal(err)
		}
		bare, err := gorm.Open(postgres.Open(url), &gorm.Config{Logger: logger.Discard})
		if err != nil {
			t.Fatal(err)
		}
		if sqlDB, err := bare.DB(); err == nil {
			t.Cleanup(func() { sqlDB.Close() })
		}
		tx := bare.Begin()
		if tx.Error != nil {
			t.Fatal(tx.Error)
		}
		defer tx.Rollback()
		err = WithRecipients(tx, []uint{1}, func(*Scope) error {
			t.Fatal("the callback ran without the rollback guard")
			return nil
		})
		if !errors.Is(err, ErrNoRollbackGuard) {
			t.Fatalf("want ErrNoRollbackGuard, got %v", err)
		}
	})
}

// Releasing an older savepoint also releases any savepoints made after it.
// Reusing their names cannot make an older rollback safe for a recipient scope.
func TestRecipientScopeAfterAncestorRelease(t *testing.T) {
	for _, tc := range []struct {
		name    string
		before  []string
		release string
	}{
		{"release older ancestor", []string{"outer_point", "middle_point"}, "RELEASE SAVEPOINT middle_point"},
		{"release with comment", []string{"outer_point"}, "RELEASE /* comment */ SAVEPOINT outer_point"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := testdb.Shared(t)
			uid := user(t, db, "ancestor")
			tx, _ := txWithPID(t, db)
			for _, name := range tc.before {
				if err := tx.SavePoint(name).Error; err != nil {
					t.Fatal(err)
				}
			}
			err := WithRecipients(tx, []uint{uid}, func(scope *Scope) error {
				if err := tx.SavePoint("outer_point").Error; err != nil {
					return err
				}
				if err := tx.Exec(tc.release).Error; err != nil {
					return err
				}
				if err := tx.RollbackTo("outer_point").Error; err != nil {
					return err
				}
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if err := db.WithContext(ctx).Transaction(func(eraser *gorm.DB) error {
					return eraseUser(eraser, uid)
				}); err != nil {
					return err
				}
				ids, err := Send(tx, Msg{Kind: GroupPending, To: []uint{uid}, Link: "/admin", Scope: scope})
				if !errors.Is(err, ErrBadScope) || len(ids) != 0 {
					return fmt.Errorf("want ErrBadScope and no notice after ancestor rollback and erasure, got %v %v", ids, err)
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			commit(t, tx)
			if n := countWhere(t, db, "notifications", "user_id = ?", uid); n != 0 {
				t.Fatalf("%d notices remain for erased user", n)
			}
		})
	}
}

// A raw COMMIT ends the database transaction even though database/sql's
// transaction handle is still open. It must also invalidate recipient scopes.
func TestRecipientScopeAfterSQLCommit(t *testing.T) {
	for _, statement := range []string{"COMMIT", "SAVEPOINT inner_point;COMMIT", "SAVEPOINT inner_point;END"} {
		t.Run(statement, func(t *testing.T) {
			db := testdb.Shared(t)
			uid := user(t, db, "sqlcommit")
			tx, _ := txWithPID(t, db)
			err := WithRecipients(tx, []uint{uid}, func(scope *Scope) error {
				if err := tx.Exec(statement).Error; err != nil {
					return err
				}
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if err := db.WithContext(ctx).Transaction(func(eraser *gorm.DB) error {
					return eraseUser(eraser, uid)
				}); err != nil {
					return err
				}
				ids, err := Send(tx, Msg{Kind: GroupPending, To: []uint{uid}, Link: "/admin", Scope: scope})
				if !errors.Is(err, ErrBadScope) || len(ids) != 0 {
					return fmt.Errorf("want ErrBadScope and no notice after SQL transaction end and erasure, got %v %v", ids, err)
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if n := countWhere(t, db, "notifications", "user_id = ?", uid); n != 0 {
				t.Fatalf("%d notices remain for erased user", n)
			}
		})
	}
}

// A SAVEPOINT that PostgreSQL never made must not stand in for the older one of
// the same name. Each case names a pre-scope savepoint, takes a SAVEPOINT of
// the same name that does not run (a dry run, or a statement refused by an
// aborted transaction or a syntax error), then rolls back to that name, which
// reaches the older savepoint and releases the recipient lock. A separate
// transaction then commits the erasure, and the scope must refuse the Send.
func TestRecipientScopeIgnoresSavepointsThatNeverRan(t *testing.T) {
	cases := []struct {
		name string
		do   func(tx *gorm.DB) error // the savepoint that does not run
	}{
		{"dry run Exec", func(tx *gorm.DB) error {
			return tx.Session(&gorm.Session{DryRun: true}).Exec("SAVEPOINT older").Error
		}},
		{"dry run SavePoint", func(tx *gorm.DB) error {
			return tx.Session(&gorm.Session{DryRun: true}).SavePoint("older").Error
		}},
		{"aborted transaction, Exec", func(tx *gorm.DB) error {
			if err := tx.Exec("SELECT 1/0").Error; err == nil {
				return errors.New("division by zero did not fail")
			}
			if err := tx.Exec("SAVEPOINT older").Error; err == nil {
				return errors.New("a savepoint in an aborted transaction did not fail")
			}
			return nil
		}},
		// The PostgreSQL dialect's SavePoint drops the Exec error and returns
		// nil, so GORM itself reports this failed savepoint as success.
		{"aborted transaction, SavePoint", func(tx *gorm.DB) error {
			if err := tx.Exec("SELECT 1/0").Error; err == nil {
				return errors.New("division by zero did not fail")
			}
			_ = tx.SavePoint("older")
			return nil
		}},
		{"syntax error", func(tx *gorm.DB) error {
			if err := tx.Exec("SAVEPOINT older (").Error; err == nil {
				return errors.New("a malformed savepoint did not fail")
			}
			return nil
		}},
		{"error already on the handle", func(tx *gorm.DB) error {
			bad := tx.Session(&gorm.Session{}).Where("id = ?", 1)
			bad.AddError(errors.New("earlier failure"))
			_ = bad.Exec("SAVEPOINT older").Error
			return nil
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := testdb.Shared(t)
			uid, other := user(t, db, "phantom"), user(t, db, "phantom-other")
			tx, _ := txWithPID(t, db)
			if err := tx.SavePoint("older").Error; err != nil {
				t.Fatal(err)
			}
			refused := false
			err := WithRecipients(tx, []uint{uid, other}, func(scope *Scope) error {
				if err := tc.do(tx); err != nil {
					return err
				}
				if err := tx.RollbackTo("older").Error; err != nil {
					return err
				}
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if err := db.WithContext(ctx).Transaction(func(eraser *gorm.DB) error {
					return eraseUser(eraser, uid)
				}); err != nil {
					return fmt.Errorf("the recipient lock should be gone: %w", err)
				}
				for _, to := range []uint{uid, other} {
					ids, err := Send(tx, Msg{Kind: GroupPending, To: []uint{to}, Link: "/admin", Scope: scope})
					if !errors.Is(err, ErrBadScope) || len(ids) != 0 {
						return fmt.Errorf("want ErrBadScope and no notice for %d, got %v %v", to, ids, err)
					}
				}
				refused = true
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if !refused {
				t.Fatal("the callback did not reach the refusals")
			}
			commit(t, tx)
			if n := countWhere(t, db, "notifications", "1 = 1"); n != 0 {
				t.Fatalf("%d notices written under a spent scope", n)
			}
			if n := countWhere(t, db, "users", "id = ? AND deleted_at IS NOT NULL", uid); n != 1 {
				t.Fatalf("the erasure did not commit (%d erased rows)", n)
			}
		})
	}

	// A SAVEPOINT sent through Row or Rows may have its error deferred, so it is
	// not trusted either, even though this one did run and a rollback to it
	// would have been harmless.
	t.Run("a savepoint sent through Row is not trusted", func(t *testing.T) {
		db := testdb.Shared(t)
		uid := user(t, db, "rowsave")
		tx, _ := txWithPID(t, db)
		err := WithRecipients(tx, []uint{uid}, func(scope *Scope) error {
			if err := tx.Raw("SAVEPOINT inner_point").Row().Err(); err != nil {
				return err
			}
			if err := tx.RollbackTo("inner_point").Error; err != nil {
				return err
			}
			ids, err := Send(tx, Msg{Kind: GroupPending, To: []uint{uid}, Link: "/admin", Scope: scope})
			if !errors.Is(err, ErrBadScope) || len(ids) != 0 {
				return fmt.Errorf("want ErrBadScope, got %v %v", ids, err)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		tx.Rollback()
	})

	// A name that did run keeps the scope, beside a dry run and a failed one.
	t.Run("a savepoint that ran keeps the scope beside ones that did not", func(t *testing.T) {
		db := testdb.Shared(t)
		uid := user(t, db, "ranafter")
		tx, _ := txWithPID(t, db)
		err := WithRecipients(tx, []uint{uid}, func(scope *Scope) error {
			if err := tx.SavePoint("real_point").Error; err != nil {
				return err
			}
			_ = tx.Session(&gorm.Session{DryRun: true}).Exec("SAVEPOINT dry_point").Error
			if err := tx.Exec("SELECT 1/0").Error; err == nil { // aborts the transaction
				return errors.New("division by zero did not fail")
			}
			if err := tx.Exec("SAVEPOINT failed_point").Error; err == nil {
				return errors.New("a savepoint in an aborted transaction did not fail")
			}
			if err := tx.RollbackTo("real_point").Error; err != nil { // recovers it
				return err
			}
			if err := tx.Exec("SELECT 1").Error; err != nil {
				return fmt.Errorf("the transaction did not recover: %w", err)
			}
			ids, err := Send(tx, Msg{Kind: GroupPending, To: []uint{uid}, Link: "/admin", Scope: scope})
			if err != nil || len(ids) != 1 {
				return fmt.Errorf("a confirmed savepoint lost the scope: %v %v", ids, err)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		tx.Rollback()
	})
}

// With a real scope the recipients behave as with no scope: living and disabled
// people are told, a deleted one, the actor, a zero id and a repeat are not, and
// the people it locked stay locked until the transaction ends.
func TestRecipientScopeKeepsRecipientRules(t *testing.T) {
	db := testdb.Shared(t)
	living, disabled, deleted, actor := user(t, db, "living"), user(t, db, "disabled"), user(t, db, "deleted"), user(t, db, "actor")
	if err := db.Model(&models.User{}).Where("id = ?", disabled).Update("status", models.UserDisabled).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Delete(&models.User{}, deleted).Error; err != nil {
		t.Fatal(err)
	}
	to := []uint{living, disabled, deleted, actor, 0, living}
	tx, _ := txWithPID(t, db)
	var ids []uint
	if err := WithRecipients(tx, to, func(sc *Scope) error {
		var err error
		ids, err = Send(tx, Msg{Kind: GroupPending, To: to, Actor: &actor, Link: "/admin", Scope: sc})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 || countWhere(t, db, "notifications", "user_id IN ?", []uint{deleted, actor}) != 0 {
		t.Fatalf("notified %v; the deleted and acting recipients must get nothing", ids)
	}
	probe, _ := txWithPID(t, db)
	for _, uid := range []uint{living, disabled} {
		err := probe.Exec("SELECT id FROM users WHERE id = ? FOR UPDATE NOWAIT", uid).Error
		if err == nil || !strings.Contains(err.Error(), "could not obtain lock") {
			t.Fatalf("user %d is not locked by the open transaction: %v", uid, err)
		}
		probe.Rollback()
		probe, _ = txWithPID(t, db)
	}
	probe.Rollback()
	tx.Rollback()
}

// The exported surface stays narrow: nothing outside this package can insert
// delivery intent without protection, take a bare recipient lock whose result a
// caller could carry to another transaction, or build the old public set.
func TestRecipientProtectionExportsStayNarrow(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "notify.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	banned := map[string]bool{"QueueProtected": true, "LockRecipients": true, "Held": true}
	for _, d := range f.Decls {
		switch d := d.(type) {
		case *ast.FuncDecl:
			if d.Recv == nil && banned[d.Name.Name] {
				t.Errorf("notify.%s is exported again", d.Name.Name)
			}
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				if ts, ok := spec.(*ast.TypeSpec); ok && banned[ts.Name.Name] {
					t.Errorf("notify.%s is exported again", ts.Name.Name)
				}
			}
		}
	}
}

// A fan-out names its recipients high to low; the lock is taken low to high.
// A session holding the low user's row waits for nothing but that row, and
// the high user is still free to lock while it does. Locks outlive Send until
// the caller's transaction ends, and a rollback releases them.
func TestSendLocksRecipientsInAscendingOrder(t *testing.T) {
	db := testdb.Shared(t)
	lo, hi := user(t, db, "lo"), user(t, db, "hi")
	blocker, bPID := txWithPID(t, db)
	if err := blocker.Exec("SELECT id FROM users WHERE id = ? FOR UPDATE", lo).Error; err != nil {
		t.Fatal(err)
	}
	sender, sPID := txWithPID(t, db)
	sent := async(func() error {
		_, err := Send(sender, Msg{Kind: GroupPending, To: []uint{hi, lo}, Link: "/admin"})
		return err
	})
	waitBlocked(t, db, sPID, bPID)
	probe, _ := txWithPID(t, db)
	if err := probe.Exec("SELECT id FROM users WHERE id = ? FOR UPDATE NOWAIT", hi).Error; err != nil {
		t.Fatalf("the sender locked the higher user before the lower one: %v", err)
	}
	probe.Rollback()
	blocker.Rollback()
	if err := await(t, sent); err != nil {
		t.Fatal(err)
	}
	for _, uid := range []uint{lo, hi} {
		probe, _ := txWithPID(t, db)
		err := probe.Exec("SELECT id FROM users WHERE id = ? FOR UPDATE NOWAIT", uid).Error
		if err == nil || !strings.Contains(err.Error(), "could not obtain lock") {
			t.Fatalf("user %d is not locked by the open sender: %v", uid, err)
		}
		probe.Rollback()
	}
	// A rollback after Send (a failure further on in the caller) frees them.
	sender.Rollback()
	probe, _ = txWithPID(t, db)
	for _, uid := range []uint{lo, hi} {
		if err := probe.Exec("SELECT id FROM users WHERE id = ? FOR UPDATE NOWAIT", uid).Error; err != nil {
			t.Fatalf("user %d stayed locked after rollback: %v", uid, err)
		}
	}
	probe.Rollback()
	if n := countWhere(t, db, "notifications", "1 = 1"); n != 0 {
		t.Fatalf("%d notices survived the rollback", n)
	}
}

// A send can wait for a lower recipient while a later send reaches a higher
// recipient. Its timestamp and batch cutoff must be sampled after that wait.
func TestSendUsesTimeAfterRecipientLock(t *testing.T) {
	register(Kind{Key: "test.lock-time", Category: CatGroups, AllowEmail: true, BatchWindow: time.Hour,
		Render: func(v Vars) (string, string) { return v.Actor, "" }})
	defer delete(registry, "test.lock-time")

	for _, tc := range []struct {
		name     string
		boundary bool
	}{
		{name: "newer merge stays monotonic"},
		{name: "window is evaluated after waiting", boundary: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := testdb.Shared(t)
			lo, hi := user(t, db, "lo"), user(t, db, "hi")
			msg := Msg{Kind: "test.lock-time", To: []uint{hi}, Link: "/groups/1", GroupKey: "joined", Vars: Vars{Actor: "first"}}
			var firstID uint
			if err := db.Transaction(func(tx *gorm.DB) error {
				ids, err := Send(tx, msg)
				if err == nil && len(ids) == 1 {
					firstID = ids[0]
				}
				return err
			}); err != nil || firstID == 0 {
				t.Fatalf("initial notice: id=%d err=%v", firstID, err)
			}

			blocker, blockerPID := txWithPID(t, db)
			if err := blocker.Exec("SELECT id FROM users WHERE id = ? FOR UPDATE", lo).Error; err != nil {
				t.Fatal(err)
			}
			sender, senderPID := txWithPID(t, db)
			delayed := msg
			delayed.To = []uint{hi, lo}
			delayed.Vars.Actor = "delayed"
			done := async(func() error {
				_, err := Send(sender, delayed)
				return err
			})
			waitBlocked(t, db, senderPID, blockerPID)

			var newer time.Time
			if tc.boundary {
				// At this instant the old notice is exactly one window old.
				// The delayed send will resume only after this update commits.
				newer = time.Now().UTC().Truncate(time.Microsecond)
				if err := db.Model(&models.Notification{}).Where("id = ?", firstID).
					UpdateColumn("updated_at", newer.Add(-time.Hour)).Error; err != nil {
					t.Fatal(err)
				}
			} else {
				later := msg
				later.Vars.Actor = "later"
				if err := db.Transaction(func(tx *gorm.DB) error {
					_, err := Send(tx, later)
					return err
				}); err != nil {
					t.Fatal(err)
				}
				var row models.Notification
				if err := db.First(&row, firstID).Error; err != nil {
					t.Fatal(err)
				}
				newer = row.UpdatedAt
			}

			blocker.Rollback()
			if err := await(t, done); err != nil {
				t.Fatal(err)
			}
			commit(t, sender)
			got := rows(t, db, hi)
			if tc.boundary {
				if len(got) != 2 {
					t.Fatalf("got %d notices after the batch window elapsed, want 2", len(got))
				}
			} else {
				if len(got) != 1 || got[0].UpdatedAt.Before(newer) {
					t.Fatalf("delayed merge moved timestamp backwards: before=%s after=%v", newer, got)
				}
			}
		})
	}
}

// Sender-first and deletion-first, for an insert, a merge into an open notice
// and a dedupe hit, with plain transactions and no contact gate. Sender first:
// the erasure waits for the sender, then deletes what it wrote. Deletion first:
// the sender waits for the erasure, rechecks deleted_at and writes nothing, so
// nothing is resurrected for the tombstone.
func TestSendAndErasureSerialize(t *testing.T) {
	register(Kind{Key: "test.barrier", Category: CatGroups, AllowEmail: true, BatchWindow: time.Hour,
		Render: func(v Vars) (string, string) { return v.Actor + " " + itoa(v.Count), "" }})
	defer delete(registry, "test.barrier")
	for _, mode := range []string{"insert", "merge", "dedupe"} {
		for _, senderFirst := range []bool{true, false} {
			name := mode + "/deletion first"
			if senderFirst {
				name = mode + "/sender first"
			}
			t.Run(name, func(t *testing.T) {
				db := testdb.Shared(t)
				uid := user(t, db, "recipient")
				m := Msg{Kind: "test.barrier", To: []uint{uid}, Link: "/groups/1", Vars: Vars{Actor: "Pat"}}
				wantIDs := 1
				switch mode {
				case "merge":
					m.GroupKey = "joined"
				case "dedupe":
					m.DedupeKey = "once"
					wantIDs = 0
				}
				if mode != "insert" {
					if ids, err := Send(db, m); err != nil || len(ids) != 1 {
						t.Fatalf("first notice: %v %v", ids, err)
					}
				}
				_, jobsBefore := intentCounts(t, db)
				if senderFirst {
					sender, sPID := txWithPID(t, db)
					ids, err := Send(sender, m)
					if err != nil || len(ids) != wantIDs {
						t.Fatalf("send: %v %v", ids, err)
					}
					eraser, ePID := txWithPID(t, db)
					erased := async(func() error { return eraseUser(eraser, uid) })
					waitBlocked(t, db, ePID, sPID)
					commit(t, sender)
					if err := await(t, erased); err != nil {
						t.Fatal(err)
					}
					commit(t, eraser)
				} else {
					eraser, ePID := txWithPID(t, db)
					if err := eraseUser(eraser, uid); err != nil {
						t.Fatal(err)
					}
					sender, sPID := txWithPID(t, db)
					var ids []uint
					sent := async(func() error {
						var err error
						ids, err = Send(sender, m)
						return err
					})
					waitBlocked(t, db, sPID, ePID)
					commit(t, eraser)
					if err := await(t, sent); err != nil {
						t.Fatal(err)
					}
					commit(t, sender)
					if len(ids) != 0 {
						t.Fatalf("a notice was written for the erased person: %v", ids)
					}
					if _, jobsAfter := intentCounts(t, db); jobsAfter != jobsBefore {
						t.Fatalf("jobs went from %d to %d", jobsBefore, jobsAfter)
					}
				}
				if n := countWhere(t, db, "notifications", "user_id = ?", uid); n != 0 {
					t.Fatalf("%d notices left for the erased person", n)
				}
				if deliveries, _ := intentCounts(t, db); deliveries != 0 {
					t.Fatalf("%d delivery rows left for the erased person", deliveries)
				}
			})
		}
	}
}

// lostIntent sends a notice and then removes its delivery intent, as if the
// transaction that should have queued it never had, and returns the row a
// repair pass would have selected.
func lostIntent(t *testing.T, db *gorm.DB, uid uint) models.Notification {
	t.Helper()
	ids, err := Send(db, Msg{Kind: GroupPending, To: []uint{uid}, Link: "/admin", Vars: Vars{Actor: "Pat"}})
	if err != nil || len(ids) != 1 {
		t.Fatalf("send: %v %v", ids, err)
	}
	if err := db.Exec("DELETE FROM notification_deliveries WHERE notification_id = ?", ids[0]).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("DELETE FROM jobs").Error; err != nil {
		t.Fatal(err)
	}
	var n models.Notification
	if err := db.First(&n, ids[0]).Error; err != nil {
		t.Fatal(err)
	}
	return n
}

// queueRepair queues one notice through the repair entry point, as a delivery
// or as a bell-only notice.
func queueRepair(tx *gorm.DB, bellOnly bool, n models.Notification) error {
	if bellOnly {
		return QueueDelivery(tx, nil, []models.Notification{n})
	}
	return QueueDelivery(tx, []models.Notification{n}, nil)
}

// QueueDelivery, the repair entry point, against an erasure: in either order,
// and with the contact gate absent. The row it was given was selected earlier.
func TestQueueDeliveryRepairAndErasureSerialize(t *testing.T) {
	t.Run("repair first", func(t *testing.T) {
		db := testdb.Shared(t)
		uid := user(t, db, "recipient")
		n := lostIntent(t, db, uid)
		repair, rPID := txWithPID(t, db)
		if err := QueueDelivery(repair, []models.Notification{n}, nil); err != nil {
			t.Fatal(err)
		}
		eraser, ePID := txWithPID(t, db)
		erased := async(func() error { return eraseUser(eraser, uid) })
		waitBlocked(t, db, ePID, rPID)
		commit(t, repair)
		if err := await(t, erased); err != nil {
			t.Fatal(err)
		}
		commit(t, eraser)
		if deliveries, _ := intentCounts(t, db); deliveries != 0 || countWhere(t, db, "notifications", "user_id = ?", uid) != 0 {
			t.Fatalf("the repair's delivery row outlived the erasure: %d", deliveries)
		}
	})
	t.Run("erasure first", func(t *testing.T) {
		db := testdb.Shared(t)
		uid := user(t, db, "recipient")
		n := lostIntent(t, db, uid)
		eraser, ePID := txWithPID(t, db)
		if err := eraseUser(eraser, uid); err != nil {
			t.Fatal(err)
		}
		repair, rPID := txWithPID(t, db)
		repaired := async(func() error { return QueueDelivery(repair, []models.Notification{n}, nil) })
		waitBlocked(t, db, rPID, ePID)
		commit(t, eraser)
		if err := await(t, repaired); err != nil {
			t.Fatalf("a stale repair must skip, not fail: %v", err)
		}
		commit(t, repair)
		if deliveries, queued := intentCounts(t, db); deliveries != 0 || queued != 0 {
			t.Fatalf("repair recreated work for an erased person: %d deliveries, %d jobs", deliveries, queued)
		}
	})
}

// Rows selected earlier are checked again: a notice deleted since (retention
// prune, or an erasure) and a recipient deleted since queue nothing and do not
// raise the foreign-key error they used to; a row naming the wrong owner is
// dropped; a live row still queues, bell-only or not.
func TestQueueDeliveryRevalidatesStaleRows(t *testing.T) {
	type setup func(t *testing.T, db *gorm.DB, n *models.Notification, other uint)
	for _, tc := range []struct {
		name         string
		stale        setup
		wantDelivery int64
	}{
		{"live", func(*testing.T, *gorm.DB, *models.Notification, uint) {}, 1},
		{"notice deleted, recipient alive", func(t *testing.T, db *gorm.DB, n *models.Notification, _ uint) {
			if err := db.Exec("DELETE FROM notifications WHERE id = ?", n.ID).Error; err != nil {
				t.Fatal(err)
			}
		}, 0},
		{"recipient deleted, notice kept", func(t *testing.T, db *gorm.DB, n *models.Notification, _ uint) {
			if err := db.Delete(&models.User{}, n.UserID).Error; err != nil {
				t.Fatal(err)
			}
		}, 0},
		{"row names the wrong owner", func(_ *testing.T, _ *gorm.DB, n *models.Notification, other uint) { n.UserID = other }, 0},
	} {
		for _, bellOnly := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/bellOnly=%v", tc.name, bellOnly), func(t *testing.T) {
				db := testdb.Shared(t)
				uid, other := user(t, db, "recipient"), user(t, db, "other")
				n := lostIntent(t, db, uid)
				tc.stale(t, db, &n, other)
				if err := db.Transaction(func(tx *gorm.DB) error { return queueRepair(tx, bellOnly, n) }); err != nil {
					t.Fatalf("a stale row must be skipped quietly: %v", err)
				}
				deliveries, queued := intentCounts(t, db)
				wantJobs := tc.wantDelivery
				if bellOnly {
					wantJobs = 0
				}
				if deliveries != tc.wantDelivery || queued != wantJobs {
					t.Fatalf("%d deliveries, %d jobs, want %d and %d", deliveries, queued, tc.wantDelivery, wantJobs)
				}
				if bellOnly && deliveries == 1 {
					var d models.NotificationDelivery
					if err := db.First(&d).Error; err != nil || d.Status != models.DeliverySuppressed || d.Reason != "bell only" {
						t.Fatalf("bell-only delivery: %+v %v", d, err)
					}
				}
			})
		}
	}
}

// Repair locks the recipient before the notice, as merge and the erasure do,
// and takes a fan-out's recipients in ascending order whatever order the rows
// arrived in. A merge already holding the notice FOR UPDATE makes the repair
// wait with the user's row already held.
func TestProtectLocksUsersBeforeNoticesInAscendingOrder(t *testing.T) {
	t.Run("an open notice lock", func(t *testing.T) {
		db := testdb.Shared(t)
		uid := user(t, db, "recipient")
		n := lostIntent(t, db, uid)
		merging, mPID := txWithPID(t, db)
		if err := merging.Exec("SELECT id FROM notifications WHERE id = ? FOR UPDATE", n.ID).Error; err != nil {
			t.Fatal(err)
		}
		repair, rPID := txWithPID(t, db)
		repaired := async(func() error { return QueueDelivery(repair, []models.Notification{n}, nil) })
		waitBlocked(t, db, rPID, mPID)
		probe, _ := txWithPID(t, db)
		err := probe.Exec("SELECT id FROM users WHERE id = ? FOR UPDATE NOWAIT", uid).Error
		probe.Rollback()
		if err == nil || !strings.Contains(err.Error(), "could not obtain lock") {
			t.Fatalf("the repair waited on the notice without holding the user: %v", err)
		}
		merging.Rollback()
		if err := await(t, repaired); err != nil {
			t.Fatal(err)
		}
		commit(t, repair)
		if deliveries, queued := intentCounts(t, db); deliveries != 1 || queued != 1 {
			t.Fatalf("%d deliveries, %d jobs, want 1 and 1", deliveries, queued)
		}
	})
	t.Run("rows in descending order", func(t *testing.T) {
		db := testdb.Shared(t)
		lo, hi := user(t, db, "lo"), user(t, db, "hi")
		nLo, nHi := lostIntent(t, db, lo), lostIntent(t, db, hi)
		blocker, bPID := txWithPID(t, db)
		if err := blocker.Exec("SELECT id FROM users WHERE id = ? FOR UPDATE", lo).Error; err != nil {
			t.Fatal(err)
		}
		repair, rPID := txWithPID(t, db)
		repaired := async(func() error { return QueueDelivery(repair, []models.Notification{nHi, nLo}, nil) })
		waitBlocked(t, db, rPID, bPID)
		probe, _ := txWithPID(t, db)
		err := probe.Exec("SELECT id FROM users WHERE id = ? FOR UPDATE NOWAIT", hi).Error
		probe.Rollback()
		if err != nil {
			t.Fatalf("the repair locked the higher user first: %v", err)
		}
		blocker.Rollback()
		if err := await(t, repaired); err != nil {
			t.Fatal(err)
		}
		commit(t, repair)
		if deliveries, queued := intentCounts(t, db); deliveries != 2 || queued != 2 {
			t.Fatalf("%d deliveries, %d jobs, want 2 and 2", deliveries, queued)
		}
	})
}

// users.email is a unique key, so changing it conflicts with the key-share
// lock a sender holds. Either order waits and finishes: no deadlock, the notice
// is kept, and the new address stands.
func TestSendAndEmailChangeWaitForEachOther(t *testing.T) {
	change := func(tx *gorm.DB, uid uint) error {
		return tx.Exec("UPDATE users SET email = ? WHERE id = ?", "changed@example.com", uid).Error
	}
	for _, senderFirst := range []bool{true, false} {
		t.Run(fmt.Sprintf("senderFirst=%v", senderFirst), func(t *testing.T) {
			db := testdb.Shared(t)
			uid := user(t, db, "recipient")
			sender, sPID := txWithPID(t, db)
			changer, cPID := txWithPID(t, db)
			var done <-chan error
			if senderFirst {
				if ids, err := Send(sender, Msg{Kind: GroupPending, To: []uint{uid}, Link: "/admin"}); err != nil || len(ids) != 1 {
					t.Fatalf("send: %v %v", ids, err)
				}
				done = async(func() error { return change(changer, uid) })
				waitBlocked(t, db, cPID, sPID)
				commit(t, sender)
				if err := await(t, done); err != nil {
					t.Fatal(err)
				}
				commit(t, changer)
			} else {
				if err := change(changer, uid); err != nil {
					t.Fatal(err)
				}
				done = async(func() error {
					ids, err := Send(sender, Msg{Kind: GroupPending, To: []uint{uid}, Link: "/admin"})
					if err == nil && len(ids) != 1 {
						err = fmt.Errorf("send wrote %v", ids)
					}
					return err
				})
				waitBlocked(t, db, sPID, cPID)
				commit(t, changer)
				if err := await(t, done); err != nil {
					t.Fatal(err)
				}
				commit(t, sender)
			}
			var u models.User
			if err := db.First(&u, uid).Error; err != nil || u.Email != "changed@example.com" {
				t.Fatalf("email: %+v %v", u, err)
			}
			if n := countWhere(t, db, "notifications", "user_id = ?", uid); n != 1 {
				t.Fatalf("%d notices, want 1", n)
			}
		})
	}
}

// Send, WithRecipients, Protect and QueueDelivery take row locks only: no advisory lock, so no
// contact gate, is acquired by the helper layer (the gate stays with the
// callers that already take it).
func TestNoticeHelpersTakeNoAdvisoryLock(t *testing.T) {
	db := testdb.Shared(t)
	uid := user(t, db, "recipient")
	n := lostIntent(t, db, uid)
	tx, _ := txWithPID(t, db)
	if _, err := Send(tx, Msg{Kind: GroupPending, To: []uint{uid}, Link: "/admin", DedupeKey: "a"}); err != nil {
		t.Fatal(err)
	}
	if err := WithRecipients(tx, []uint{uid}, func(sc *Scope) error {
		_, err := Send(tx, Msg{Kind: GroupPending, To: []uint{uid}, Link: "/admin", DedupeKey: "b", Scope: sc})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := QueueDelivery(tx, []models.Notification{n}, nil); err != nil {
		t.Fatal(err)
	}
	var advisory int64
	if err := tx.Raw("SELECT count(*) FROM pg_locks WHERE locktype = 'advisory' AND pid = pg_backend_pid()").Scan(&advisory).Error; err != nil {
		t.Fatal(err)
	}
	if advisory != 0 {
		t.Fatalf("the helpers hold %d advisory locks", advisory)
	}
}
