package app_test

import (
	"io/fs"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"fold/web"
)

// docs/DECISIONS.md P18: every control is a partial. A raw button, input,
// select or textarea written anywhere but web/templates/partials is refused.
// This is a source scan because nothing in html/template can hold the rule.
func TestNoRawControlsOutsidePartials(t *testing.T) {
	raw := regexp.MustCompile(`<(button|input|select|textarea)\b`)
	err := fs.WalkDir(web.Templates, "templates", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || strings.HasPrefix(p, "templates/partials/") {
			return err
		}
		b, _ := fs.ReadFile(web.Templates, p)
		if m := raw.FindString(string(b)); m != "" {
			t.Errorf("%s writes a raw %s>; use a partial from templates/partials", p, m)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestUserTextIsEscaped(t *testing.T) {
	s := newSite(t)
	b := s.browser()
	b.unlock()
	form := setupForm("pat@cloverdale.example")
	form.Set("church_name", `<script>alert(1)</script>`)
	form.Set("name", `"><img src=x onerror=alert(1)>`)
	form.Set("email", "not-an-address")
	r := b.post("/setup", "/setup", form)
	if strings.Contains(r.body, "<script>alert") || strings.Contains(r.body, "<img src=x") {
		t.Fatal("setup form echoed markup unescaped")
	}
	if !strings.Contains(r.body, "&lt;script&gt;") {
		t.Fatal("the typed name was not kept")
	}
	_ = url.Values{}
}

func TestGalleryShowsEveryPartial(t *testing.T) {
	s := newSite(t)
	admin := s.finishSetup()
	r := admin.get("/admin/gallery")
	for _, want := range []string{"btn-primary", "btn-danger-outline", "btn-busy", "aria-invalid", "choice-row", `class="chip"`,
		"segment-bar", "popover", "<dialog", "<select", `class="empty"`, "notice-danger", "status-line", "error-summary", "hx-post"} {
		if !strings.Contains(r.body, want) {
			t.Errorf("gallery lacks %s", want)
		}
	}
}

// A setup code pasted with a trailing newline or spaces still matches.
func TestSetupCodeIgnoresWhitespace(t *testing.T) {
	s := newSite(t)
	b := s.browser()
	r := b.post("/setup", "/setup/code", url.Values{"code": {"  " + strings.ToLower(s.app.SetupCode()) + "\r\n"}})
	if r.status != 303 {
		t.Fatalf("a code with whitespace was refused: %d", r.status)
	}
}

// Pages say what powers them at the foot, under the product's one name
// (docs/DECISIONS.md R25).
func TestFooterSaysPoweredByFold(t *testing.T) {
	s := newSite(t)
	r := s.browser().get("/setup")
	if !strings.Contains(r.body, "Powered by Fold") || strings.Contains(r.body, "Fellowship Facilitator") {
		t.Fatal("the footer does not say Powered by Fold")
	}
}
