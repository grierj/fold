package notify

import (
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"fold/internal/testdb"
	"fold/models"

	"gorm.io/gorm"
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
	if err := Seen(db, uid, ids); err != nil {
		t.Fatal(err)
	}
	unchanged("seen")
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
