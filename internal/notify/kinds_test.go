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
