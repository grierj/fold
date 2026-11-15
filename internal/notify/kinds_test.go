package notify

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"fold/internal/testdb"
)

// The promotion email carries the video link (M1.8); the bell never does.
func TestPromotionEmailCarriesVideoLink(t *testing.T) {
	k, ok := Lookup(RegistrationPromoted)
	if !ok {
		t.Fatal("registration.promoted is not registered")
	}
	link := "https://meet.example.org/room-1"
	v := Vars{Event: "Supper", When: "Tue 7pm", VideoURL: link}
	_, bell := k.Render(v)
	if strings.Contains(bell, link) {
		t.Errorf("the bell shows the video link: %q", bell)
	}
	_, email := k.RenderForEmail(v)
	if !strings.Contains(email, "Join by video: "+link) {
		t.Errorf("the email has no video link: %q", email)
	}
	v.VideoURL = ""
	_, email = k.RenderForEmail(v)
	if strings.Contains(email, "Join by video") {
		t.Errorf("no link set, but the email mentions one: %q", email)
	}
}

// kindKeyShape is what a kind key looks like: lower-case words joined by dots.
var kindKeyShape = regexp.MustCompile(`^[a-z0-9_]+(\.[a-z0-9_]+)+$`)

// Every string constant in kinds.go whose value looks like a kind key
// ("thing.event") must be registered, so a forgotten register call fails
// here instead of at Send time (#79).
func TestEveryKindConstIsRegistered(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "kinds.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := 0
	for _, d := range f.Decls {
		g, ok := d.(*ast.GenDecl)
		if !ok || g.Tok != token.CONST {
			continue
		}
		for _, s := range g.Specs {
			vs := s.(*ast.ValueSpec)
			for i, v := range vs.Values {
				lit, ok := v.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				key, _ := strconv.Unquote(lit.Value)
				if !kindKeyShape.MatchString(key) {
					continue
				}
				found++
				if _, ok := registry[key]; !ok {
					t.Errorf("kind %s (%q) is declared but never registered", vs.Names[i].Name, key)
				}
			}
		}
	}
	if found != len(registry) {
		t.Errorf("kinds.go declares %d kind keys, registry has %d", found, len(registry))
	}
}

func TestHouseholdRegistrationWordsNameTheAttendee(t *testing.T) {
	for _, key := range []string{RegistrationPromoted, RegistrationRemoved, RegistrationNotRestored} {
		kind, ok := Lookup(key)
		if !ok {
			t.Fatal(key)
		}
		title, body := kind.Render(Vars{Person: "Kit", Event: "Family night"})
		if !strings.Contains(title+body, "Kit") {
			t.Fatalf("%s did not identify household attendee: %s %s", key, title, body)
		}
		ordinaryTitle, ordinaryBody := kind.Render(Vars{Event: "Family night"})
		if strings.Contains(ordinaryTitle+ordinaryBody, "Kit") {
			t.Fatal("household wording reached user notice")
		}
	}
}

func TestTeenGuardianTaskExplainsConsent(t *testing.T) {
	kind, ok := Lookup(HouseholdLinkPending)
	if !ok {
		t.Fatal("missing guardian notification")
	}
	title, body := kind.Render(Vars{Person: "Taylor", Change: "teen_account", SubjectKind: SubjectTeen})
	if !strings.Contains(title, "Taylor") || !strings.Contains(body, "does not let the teen sign in") {
		t.Fatalf("account-targeted task: %s %s", title, body)
	}
	title, body = kind.RenderForEmail(Vars{Person: "Taylor", Actor: "Pat", Change: "teen_account", SubjectKind: SubjectTeen})
	if strings.Contains(title+body, "Taylor") || strings.Contains(title+body, "Pat") {
		t.Fatal("minor name leaked to email")
	}
	kind, ok = Lookup(HouseholdConverted)
	if !ok || len(kind.Actions) != 0 || kind.Waiting {
		t.Fatal("conversion notice must not be an approval task")
	}
}

func TestAddRequestNoticesAreSeparateSafeTasks(t *testing.T) {
	for _, key := range []string{AddSubjectReview, AddAdminReview, AddUpdated} {
		k, ok := Lookup(key)
		if !ok || k.BatchWindow != 0 || len(k.Actions) != 0 || !k.AllowEmail || k.Waiting != (key != AddUpdated) {
			t.Fatalf("unsafe add kind: %s %+v", key, k)
		}
		v := Vars{Person: "Private teen", Actor: "Private requester", Group: "Secret group", Event: "Secret event", Note: "private@example.com", SubjectKind: SubjectTeen}
		title, body := k.Render(v)
		emailTitle, emailBody := k.RenderForEmail(v)
		for _, secret := range []string{v.Person, v.Actor, v.Group, v.Event, v.Note} {
			if strings.Contains(title+body+emailTitle+emailBody, secret) {
				t.Fatalf("%s leaked %s", key, secret)
			}
		}
	}
}

func TestOccurrenceUpdateNoticeIsSafeAndNavigationOnly(t *testing.T) {
	k, ok := Lookup(OccurrenceUpdated)
	if !ok || k.BatchWindow != 0 || len(k.Actions) != 0 || k.Waiting || !k.AllowEmail {
		t.Fatalf("unsafe occurrence update: %+v", k)
	}
	vars := Vars{Person: "Private teen", Actor: "Hidden host", Group: "Hidden organizer", Event: "Secret series", Note: "private@example.com", SubjectKind: SubjectTeen}
	title, body := k.Render(vars)
	emailTitle, emailBody := k.RenderForEmail(vars)
	for _, secret := range []string{vars.Person, vars.Actor, vars.Group, vars.Event, vars.Note} {
		if strings.Contains(title+body+emailTitle+emailBody, secret) {
			t.Fatalf("notice leaked %s", secret)
		}
	}
}

func TestOccurrenceAddedAndChangedAreNamesFreeNavigationOnly(t *testing.T) {
	for _, key := range []string{OccurrenceAdded, OccurrenceChanged} {
		k, ok := Lookup(key)
		if !ok || k.BatchWindow != 0 || k.Waiting || len(k.Actions) != 0 || !k.AllowEmail {
			t.Fatal("unsafe kind", key)
		}
		v := Vars{Person: "Private teen", Actor: "Private leader", Group: "Hidden group", Event: "Secret series", Note: "private@example.com", SubjectKind: SubjectTeen}
		title, body := k.Render(v)
		et, eb := k.RenderForEmail(v)
		for _, secret := range []string{v.Person, v.Actor, v.Group, v.Event, v.Note} {
			if strings.Contains(title+body+et+eb, secret) {
				t.Fatal("notice leaked", key, secret)
			}
		}
	}
}

// Footers name the kind and the church; messages from leaders are "at"
// the church, so the sentence doesn't say "from" twice (DEC-119).
func TestYoureGetting(t *testing.T) {
	for cat, want := range map[string]string{
		CatGroups:   "You're getting group updates from First Church",
		CatEvents:   "You're getting event updates from First Church",
		CatAdmin:    "You're getting admin notices from First Church",
		CatMessages: "You're getting messages from leaders at First Church",
	} {
		if got := YoureGetting(cat, "First Church"); got != want {
			t.Errorf("%s: %q, want %q", cat, got, want)
		}
	}
}

// A guardian's copy shows the bell title and body, never the email text, and
// never a leader's note. This renders every optional email kind the way Send
// does, with a sentinel in every field a recipient-only capability or note
// can come from, and holds the copy to what the teen's own bell may say.
func TestCopyTextCarriesNoRecipientOnlyContent(t *testing.T) {
	const (
		note   = "SENTINEL-NOTE"
		manage = "https://x.example/guest/manage/SENTINEL-MANAGE"
		video  = "https://x.example/SENTINEL-VIDEO"
	)
	v := Vars{Actor: "Pat", Group: "Youth", Event: "Youth night", When: "Fri 7pm", Place: "12 Elm St", Change: "time", Note: note, ManageURL: manage, VideoURL: video}
	// Control arms: the sentinels do reach the dangerous kinds' other texts,
	// so the checks below can fail.
	promoted, _ := Lookup(RegistrationPromoted)
	if _, email := promoted.RenderForEmail(v); !strings.Contains(email, "SENTINEL-MANAGE") || !strings.Contains(email, "SENTINEL-VIDEO") {
		t.Fatalf("the promotion email no longer carries its links: %q", email)
	}
	removed, _ := Lookup(RegistrationRemoved)
	if _, bell := removed.Render(v); !strings.Contains(bell, note) {
		t.Fatalf("the removal bell no longer carries the note, so the title-only flag is untested: %q", bell)
	}
	for _, k := range Kinds() {
		if !k.AllowEmail || k.Required {
			continue
		}
		title, body := k.Render(v)
		ct, cb := k.CopyText(title, body)
		if ct != title {
			t.Errorf("%s: copy title %q, want the bell title %q", k.Key, ct, title)
		}
		for _, bad := range []string{"SENTINEL-MANAGE", "SENTINEL-VIDEO", "guest/manage"} {
			if strings.Contains(ct+cb, bad) {
				t.Errorf("%s: copy carries %q: %q", k.Key, bad, ct+cb)
			}
		}
		// A body that can carry a note is shown to nobody but the person it is
		// for: its copy has the title alone.
		if strings.Contains(body, note) && (!k.CopyTitleOnly || cb != "") {
			t.Errorf("%s: the body carries a note but the copy shows %q", k.Key, cb)
		}
		if strings.Contains(ct+cb, note) {
			t.Errorf("%s: copy carries the note: %q", k.Key, ct+cb)
		}
	}
	// Positive control: a copy of a plain kind keeps the place the teen sees.
	cancelled, _ := Lookup(OccurrenceCancelled)
	title, body := cancelled.Render(v)
	if _, cb := cancelled.CopyText(title, body); !strings.Contains(cb, "12 Elm St") {
		t.Errorf("a date's copy lost its place: %q", cb)
	}
}

// Only the kind written for someone who has just lost access to its subject
// is copied without the teen's access to that subject (Grier, 2026-11-13).
func TestOnlyRegistrationCancelledIsCopiedWithoutSubjectAccess(t *testing.T) {
	for _, k := range Kinds() {
		if k.CopyWithoutSubjectAccess != (k.Key == RegistrationCancelled) {
			t.Errorf("%s: CopyWithoutSubjectAccess = %v", k.Key, k.CopyWithoutSubjectAccess)
		}
	}
	k, _ := Lookup(RegistrationCancelled)
	if !k.AllowEmail || k.Required {
		t.Errorf("registration.cancelled must be optional email: %+v", k)
	}
	title, body := k.Render(Vars{Event: "Retreat", Group: "Hidden group", Place: "Home", When: "Fri"})
	for _, bad := range []string{"Hidden group", "Home", "Fri"} {
		if strings.Contains(title+body, bad) {
			t.Errorf("registration.cancelled names %q: %q", bad, title+body)
		}
	}
}

// A lost-access copy is the kind's own text for the plain title stored in the
// notice's data: the title survives any separator it holds, and a row with no
// usable title, like any other kind, has no copy.
func TestLostAccessCopyText(t *testing.T) {
	k, _ := Lookup(RegistrationCancelled)
	_, wantBody := k.Render(Vars{Event: "x"})
	for _, title := range []string{"Retreat", "Carols · 2031-12-24 · Part 2", "Games ·", "· 2031-12-24", `Quote "this" & <that>`} {
		data := `{"eventId":4,"eventTitle":` + jsonString(title) + `}`
		got, body, ok := k.LostAccessCopyText(data)
		if !ok || got != "Registration cancelled: "+title || body != wantBody {
			t.Errorf("%q: got %q %q %v", title, got, body, ok)
		}
	}
	for _, data := range []string{"", "{}", "null", "[]", "not json", `{"eventTitle":""}`, `{"eventTitle":" \t"}`, `{"eventTitle":3}`, `{"eventTitle":null}`, `{"eventtitle":"Retreat"}`} {
		if title, body, ok := k.LostAccessCopyText(data); ok || title != "" || body != "" {
			t.Errorf("data %q gave a copy: %q %q", data, title, body)
		}
	}
	// Only the lost-access kind has one: another kind's copy is its stored text.
	for _, other := range Kinds() {
		if other.CopyWithoutSubjectAccess {
			continue
		}
		if _, _, ok := other.LostAccessCopyText(`{"eventTitle":"Retreat"}`); ok {
			t.Errorf("%s has a lost-access copy", other.Key)
		}
	}
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// A person's Teen or Child badge is a bell badge in words, never title text,
// and never reaches an email or a lock screen (DEC-150).
func TestSubjectKindBadgeStaysOutOfText(t *testing.T) {
	for _, tc := range []struct {
		name string
		kind string
		vars Vars
	}{
		{"teen account request", AccountRequested, Vars{Person: "Tess", Change: "Self-signup", SubjectKind: SubjectTeen}},
		{"child household link", HouseholdLinkPending, Vars{Person: "Kai", Actor: "Pat", SubjectKind: SubjectChild}},
		{"teen household link", HouseholdLinkPending, Vars{Person: "Tess", Actor: "Pat", SubjectKind: SubjectTeen}},
	} {
		k, ok := Lookup(tc.kind)
		if !ok {
			t.Fatalf("%s: kind missing", tc.name)
		}
		plain := tc.vars
		plain.SubjectKind = ""
		title, body := k.Render(tc.vars)
		plainTitle, plainBody := k.Render(plain)
		if title != plainTitle || body != plainBody {
			t.Errorf("%s: the badge changed the text: %q %q vs %q %q", tc.name, title, body, plainTitle, plainBody)
		}
		for _, s := range []string{"(teen)", "(child)", "Teen", "Child"} {
			if strings.Contains(title+body, s) {
				t.Errorf("%s: text repeats the badge %q: %q %q", tc.name, s, title, body)
			}
		}
		et, eb := k.RenderForEmail(tc.vars)
		pt, pb, _ := k.RenderForPush(tc.vars)
		if text := et + eb + pt + pb; strings.Contains(text, tc.vars.Person) || strings.Contains(text, "(teen)") || strings.Contains(text, "(child)") {
			t.Errorf("%s: email or push names the person or carries the badge: %q", tc.name, text)
		}
	}
}

func TestSubjectKindIsStoredAndOnlyTeenOrChild(t *testing.T) {
	register(Kind{Key: "test.subject", Category: CatGroups, Render: func(v Vars) (string, string) { return "A person " + v.Person, "" }})
	defer delete(registry, "test.subject")
	db := testdb.Shared(t)
	uid := user(t, db, "subject-reader")
	for _, tc := range []struct {
		name, kind, want string
	}{
		{"teen", SubjectTeen, SubjectTeen},
		{"child", SubjectChild, SubjectChild},
		{"adult", "adult", ""},
		{"none", "", ""},
		{"made up", "admin", ""},
	} {
		if _, err := Send(db, Msg{Kind: "test.subject", To: []uint{uid}, Link: "/x", Data: map[string]any{"groupId": 4},
			Vars: Vars{Person: tc.name, SubjectKind: tc.kind}}); err != nil {
			t.Fatal(err)
		}
	}
	got := rows(t, db, uid)
	if len(got) != 5 {
		t.Fatalf("rows: %d", len(got))
	}
	for i, want := range []string{SubjectTeen, SubjectChild, "", "", ""} {
		if kind := SubjectKindOf(got[i].Data); kind != want {
			t.Errorf("row %d stores %q, want %q (data %s)", i, kind, want, got[i].Data)
		}
		if !strings.Contains(got[i].Data, `"groupId"`) {
			t.Errorf("row %d lost the writer's data: %s", i, got[i].Data)
		}
	}
	for _, bad := range []string{"", "{}", "not json", `{"subjectKind":"adult"}`, `{"subjectKind":7}`, `{"subjectKind":"teen"}`} {
		want := ""
		if bad == `{"subjectKind":"teen"}` {
			want = SubjectTeen
		}
		if kind := SubjectKindOf(bad); kind != want {
			t.Errorf("SubjectKindOf(%q) = %q, want %q", bad, kind, want)
		}
	}
}

// A merged row keeps the badge only when every notice folded into it is about
// the same kind of person; any mix, adult included, drops it.
func TestBatchedMergeKeepsTheBadgeOnlyWhenEveryRowAgrees(t *testing.T) {
	register(Kind{Key: "test.merge.subject", Category: CatGroups, BatchWindow: time.Hour,
		Render: func(v Vars) (string, string) { return "People " + itoa(v.Count), "" }})
	defer delete(registry, "test.merge.subject")
	db := testdb.Shared(t)
	for _, tc := range []struct {
		name  string
		kinds []string
		want  string
	}{
		{"all teens", []string{SubjectTeen, SubjectTeen, SubjectTeen}, SubjectTeen},
		{"all children", []string{SubjectChild, SubjectChild}, SubjectChild},
		{"teen then child", []string{SubjectTeen, SubjectChild}, ""},
		{"teen then adult", []string{SubjectTeen, ""}, ""},
		{"adult then teen", []string{"", SubjectTeen}, ""},
		{"mixed, then the first kind again", []string{SubjectTeen, SubjectChild, SubjectTeen}, ""},
	} {
		uid := user(t, db, "merge-"+strings.ReplaceAll(tc.name, " ", "-"))
		for _, kind := range tc.kinds {
			if _, err := Send(db, Msg{Kind: "test.merge.subject", To: []uint{uid}, Link: "/x", GroupKey: "people:1",
				Data: map[string]any{"groupId": 1}, Vars: Vars{SubjectKind: kind}}); err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
		}
		got := rows(t, db, uid)
		if len(got) != 1 || got[0].BatchCount != len(tc.kinds) {
			t.Fatalf("%s: not one merged row: %+v", tc.name, got)
		}
		if kind := SubjectKindOf(got[0].Data); kind != tc.want {
			t.Errorf("%s: merged row badge %q, want %q (data %s)", tc.name, kind, tc.want, got[0].Data)
		}
		if !strings.Contains(got[0].Data, `"groupId"`) {
			t.Errorf("%s: merge lost the row's data: %s", tc.name, got[0].Data)
		}
	}
}
