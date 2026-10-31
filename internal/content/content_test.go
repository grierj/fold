package content

import (
	"strings"
	"testing"
)

// M3.4 acceptance: scripts, on* attributes and javascript: links are gone.
func TestSanitizeHTML(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{`<p>Hi <strong>all</strong></p><script>alert(1)</script>`, `<p>Hi <strong>all</strong></p>`},
		{`<p onclick="steal()">Tap</p>`, `<p>Tap</p>`},
		{`<a href="javascript:alert(1)">x</a>`, `x`},
		{`<a href="JaVaScRiPt:alert(1)">x</a>`, `x`},
		{`<img src=x onerror="alert(1)">`, ``},
		{`<p style="color:red">x</p>`, `<p>x</p>`},
		{`<iframe src="https://evil"></iframe><p>ok</p>`, `<p>ok</p>`},
		{`<a href="https://church.org/sign-up" class="fold-button">Sign up</a>`, `<a href="https://church.org/sign-up" class="fold-button" rel="noreferrer noopener" target="_blank">Sign up</a>`},
		{`<a href="https://x.org" class="evil">x</a>`, `<a href="https://x.org" rel="noreferrer noopener" target="_blank">x</a>`},
		{`<a href="mailto:office@church.org">Write</a>`, `<a href="mailto:office@church.org" rel="noreferrer">Write</a>`},
		{`<h1>Big</h1><h2>Smaller</h2>`, `Big<h2>Smaller</h2>`},
	} {
		if got := SanitizeHTML(tc.in); got != tc.want {
			t.Errorf("SanitizeHTML(%q)\n got %q\nwant %q", tc.in, got, tc.want)
		}
	}
}

func TestPlainText(t *testing.T) {
	in := SanitizeHTML(`<h2>Picnic</h2><p>Bring a <strong>dish</strong> and a <a href="https://church.org/picnic">friend</a>.</p>` +
		`<ul><li>Chairs</li><li>Sun hats</li></ul><hr><p>Questions? <a href="mailto:o@c.org">Write us</a><br>See you!</p>`)
	got := PlainText(in)
	want := "Picnic\n\nBring a dish and a friend (https://church.org/picnic).\n\n- Chairs\n- Sun hats\n\n---\n\nQuestions? Write us\nSee you!"
	if got != want {
		t.Fatalf("PlainText:\n%q\nwant\n%q", got, want)
	}
	if strings.Contains(PlainText(`<p>a &amp; b</p>`), "&amp;") {
		t.Fatal("entities should be decoded")
	}
}
