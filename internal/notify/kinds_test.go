package notify

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"strings"
	"testing"
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
				if !strings.Contains(key, ".") {
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
	title, body := kind.Render(Vars{Person: "Taylor", Change: "teen_account", Teen: true})
	if !strings.Contains(title, "Taylor") || !strings.Contains(body, "does not let the teen sign in") {
		t.Fatalf("account-targeted task: %s %s", title, body)
	}
	title, body = kind.RenderForEmail(Vars{Person: "Taylor", Actor: "Pat", Change: "teen_account", Teen: true})
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
		v := Vars{Person: "Private teen", Actor: "Private requester", Group: "Secret group", Event: "Secret event", Note: "private@example.com", Teen: true}
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
	vars := Vars{Person: "Private teen", Actor: "Hidden host", Group: "Hidden organizer", Event: "Secret series", Note: "private@example.com", Teen: true}
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
		v := Vars{Person: "Private teen", Actor: "Private leader", Group: "Hidden group", Event: "Secret series", Note: "private@example.com", Teen: true}
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
