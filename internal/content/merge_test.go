package content

import (
	"strings"
	"testing"
)

func TestMergeFields(t *testing.T) {
	s := `<p>Hi {{ first_name }}, see <a href="{{event_link}}">{{event_title}}</a> {{Event_Time}} {{nope}}</p>`
	clean := SanitizeHTML(s)
	if !strings.Contains(clean, `href="{{event_link}}"`) {
		t.Fatalf("a merge-field link survives sanitizing: %s", clean)
	}
	if got := strings.Join(FieldsIn(clean), ","); got != "first_name,event_link,event_title,event_time,nope" {
		t.Fatalf("fields: %s", got)
	}
	vals := map[string]string{FieldFirstName: `Sam <b>`, FieldEventLink: "https://fold.example.org/events/3", FieldEventTitle: "Picnic & games", FieldEventTime: "Sat"}
	html := Merge(clean, vals, true)
	if strings.Contains(html, "<b>") || !strings.Contains(html, "Sam &lt;b&gt;") || !strings.Contains(html, `href="https://fold.example.org/events/3"`) || !strings.Contains(html, "Picnic &amp; games") {
		t.Fatalf("html merge escapes values: %s", html)
	}
	if got := Merge("Hi {{first_name}}", vals, false); got != "Hi Sam <b>" {
		t.Fatalf("text merge: %q", got)
	}
	if !IsMergeField("manage_link") || IsMergeField("nope") || IsEventField(FieldFirstName) || !IsEventField(FieldEventTime) {
		t.Fatal("field lists")
	}
	if strings.Contains(PlainText(clean), "merge-field.invalid") {
		t.Fatal("the stand-in address never shows")
	}
	// A link field in any case keeps its link.
	if got := SanitizeHTML(`<a href="{{ Manage_Link }}">x</a>`); !strings.Contains(got, `href="{{manage_link}}"`) {
		t.Fatalf("mixed-case link field: %s", got)
	}
	// Misspelled fields are found, so saving can refuse them.
	if got := strings.Join(FieldsIn("{{first name}} {{first-name}} {{field2}} {x}"), ","); got != "first name,first-name,field2" {
		t.Fatalf("misspelled fields: %s", got)
	}
}

func TestRenderEmail(t *testing.T) {
	body := SanitizeHTML(`<h2>News</h2><p>Hello <a class="fold-button" href="https://example.org/x">Sign up</a></p>`)
	out, err := RenderEmail(EmailPage{Church: "First <Church>", Subject: "News & notes", Body: body,
		Footer: "You're getting message notices from First Church.", SettingsURL: "https://fold.example.org/email-settings/tok",
		Address: []string{"1 Main St", "Springfield"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"<title>News &amp; notes</title>", "First &lt;Church&gt;", `max-width:600px`,
		`<h2 style="margin:24px 0 12px 0;`, `background-color:#1d4ed8;color:#ffffff;`, `href="https://example.org/x"`,
		`href="https://fold.example.org/email-settings/tok"`, "visit your email settings", "<br>1 Main St<br>Springfield",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	// Whatever the body holds, it was re-rendered, so it can't break out.
	out, _ = RenderEmail(EmailPage{Body: `<p>x</p></td></tr></table><script>alert(1)</script>`})
	if strings.Contains(out, "<script>") {
		t.Fatalf("raw markup got through: %s", out)
	}
}
