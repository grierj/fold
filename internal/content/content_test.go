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

const okImage = "/uploads/messages/12/0123456789abcdef0123456789abcdef.png"

// Pictures (DEC-118): only an address the composer's upload makes, with a
// description, survives; every other image is gone, element and all.
func TestSanitizeHTMLPictures(t *testing.T) {
	for name, tc := range map[string]struct{ in, want string }{
		"allowed":        {`<img src="` + okImage + `" alt="Our picnic">`, `<img src="` + okImage + `" alt="Our picnic">`},
		"jpg and gif":    {`<img src="/uploads/messages/3/ffffffffffffffffffffffffffffffff.jpg" alt="a"><img src="/uploads/messages/3/00000000000000000000000000000000.gif" alt="b">`, `<img src="/uploads/messages/3/ffffffffffffffffffffffffffffffff.jpg" alt="a"><img src="/uploads/messages/3/00000000000000000000000000000000.gif" alt="b">`},
		"width":          {`<img src="` + okImage + `" alt="a" width="300">`, `<img src="` + okImage + `" alt="a" width="300">`},
		"bad width":      {`<img src="` + okImage + `" alt="a" width="100%">`, `<img src="` + okImage + `" alt="a">`},
		"style and size": {`<img src="` + okImage + `" alt="a" style="width:9px" height="9" onerror="x()" class="big">`, `<img src="` + okImage + `" alt="a">`},
		"outside":        {`<img src="https://evil.example/x.png" alt="x">`, ``},
		"tracking":       {`<img src="https://evil.example/pixel.gif?u=1" alt="x"><p>hi</p>`, `<p>hi</p>`},
		"data uri":       {`<img src="data:image/png;base64,iVBORw0KGgo=" alt="x">`, ``},
		"javascript":     {`<img src="javascript:alert(1)" alt="x">`, ``},
		"protocol rel":   {`<img src="//evil.example/uploads/messages/12/0123456789abcdef0123456789abcdef.png" alt="x">`, ``},
		"traversal":      {`<img src="/uploads/messages/12/../../avatars/1/a.png" alt="x">`, ``},
		"traversal 2":    {`<img src="/uploads/messages/12/..%2F..%2Favatars/1/0123456789abcdef0123456789abcdef.png" alt="x">`, ``},
		"other folder":   {`<img src="/uploads/avatars/12/0123456789abcdef0123456789abcdef.png" alt="x">`, ``},
		"cover":          {`<img src="/uploads/covers/12/0123456789abcdef0123456789abcdef.png" alt="x">`, ``},
		"no id":          {`<img src="/uploads/messages//0123456789abcdef0123456789abcdef.png" alt="x">`, ``},
		"zero id":        {`<img src="/uploads/messages/0/0123456789abcdef0123456789abcdef.png" alt="x">`, ``},
		"short name":     {`<img src="/uploads/messages/12/abc.png" alt="x">`, ``},
		"upper hex":      {`<img src="/uploads/messages/12/0123456789ABCDEF0123456789abcdef.png" alt="x">`, ``},
		"webp":           {`<img src="/uploads/messages/12/0123456789abcdef0123456789abcdef.webp" alt="x">`, ``},
		"query":          {`<img src="` + okImage + `?x=1" alt="x">`, ``},
		"fragment":       {`<img src="` + okImage + `#x" alt="x">`, ``},
		"trailing space": {`<img src="` + okImage + ` " alt="x">`, ``},
		"no src":         {`<img alt="x">`, ``},
		"srcset":         {`<img srcset="https://evil.example/a.png 1x" alt="x">`, ``},
		"no alt":         {`<img src="` + okImage + `">`, ``},
		"blank alt":      {`<img src="` + okImage + `" alt="">`, ``},
		"spaces alt":     {`<img src="` + okImage + `" alt="   ">`, ``},
		"tab alt":        {`<img src="` + okImage + `" alt="&#9;&#10;">`, ``},
		"long alt":       {`<img src="` + okImage + `" alt="` + strings.Repeat("a", MaxAltLen+1) + `">`, ``},
		"longest alt":    {`<img src="` + okImage + `" alt="` + strings.Repeat("a", MaxAltLen) + `">`, `<img src="` + okImage + `" alt="` + strings.Repeat("a", MaxAltLen) + `">`},
		"inside text":    {`<p>See <img src="` + okImage + `" alt="the map"> here</p>`, `<p>See <img src="` + okImage + `" alt="the map"> here</p>`},
		"svg":            {`<svg onload="x()"><image href="` + okImage + `"></svg>`, ``},
		"picture":        {`<picture><source srcset="https://evil.example/a.png"><img src="https://evil.example/a.png" alt="x"></picture>`, ``},
		"link href rel":  {`<a href="/uploads/messages/12/0123456789abcdef0123456789abcdef.png">x</a>`, `x`},
		"link no scheme": {`<a href="church.org">x</a>`, `x`},
		"link proto rel": {`<a href="//evil.example">x</a>`, `x`},
	} {
		if got := SanitizeHTML(tc.in); got != tc.want {
			t.Errorf("%s: SanitizeHTML(%q)\n got %q\nwant %q", name, tc.in, got, tc.want)
		}
	}
}

func TestSanitizeHTMLPicturesAreStable(t *testing.T) {
	in := `<p>Hi</p><img src="` + okImage + `" alt="Our &amp; their picnic"><p>Bye</p>`
	once := SanitizeHTML(in)
	if twice := SanitizeHTML(once); twice != once {
		t.Fatalf("not stable:\n%q\n%q", once, twice)
	}
	if !strings.Contains(once, `alt="Our &amp; their picnic"`) {
		t.Fatalf("alt lost: %q", once)
	}
}

func TestImageSourcesAndMessageID(t *testing.T) {
	body := SanitizeHTML(`<img src="` + okImage + `" alt="a"><p>x</p><img src="/uploads/messages/7/ffffffffffffffffffffffffffffffff.gif" alt="b">`)
	got := ImageSources(body)
	if len(got) != 2 || got[0] != okImage {
		t.Fatalf("sources = %v", got)
	}
	for src, want := range map[string]uint64{okImage: 12, got[1]: 7} {
		if id, ok := ImageMessageID(src); !ok || id != want {
			t.Errorf("ImageMessageID(%q) = %d %v", src, id, ok)
		}
	}
	for _, bad := range []string{"", "https://x/y.png", "/uploads/messages/0/0123456789abcdef0123456789abcdef.png", "/uploads/messages/99999999999999999999/0123456789abcdef0123456789abcdef.png"} {
		if _, ok := ImageMessageID(bad); ok {
			t.Errorf("ImageMessageID(%q) accepted", bad)
		}
	}
	if len(ImageSources(`<p>no pictures</p>`)) != 0 {
		t.Fatal("found a picture in plain text")
	}
}

func TestPlainTextPictures(t *testing.T) {
	in := SanitizeHTML(`<p>Look:</p><img src="` + okImage + `" alt="  Our   picnic  table "><p>Bring food.</p>`)
	if got, want := PlainText(in), "Look:\n\n[Image: Our picnic table]\n\nBring food."; got != want {
		t.Fatalf("PlainText:\n%q\nwant\n%q", got, want)
	}
	if got := PlainText(`<img src="` + okImage + `">`); got != "[Image]" {
		t.Fatalf("no alt: %q", got)
	}
}

// An email links to a picture by its full address on the church's site, with
// room to shrink on a phone; it never attaches it.
func TestRenderEmailPictures(t *testing.T) {
	body := `<p>Hi</p><img src="` + okImage + `" alt="Our picnic" width="300"><img src="https://evil.example/pixel.gif" alt="x">`
	out, err := RenderEmail(EmailPage{Church: "First Church", Subject: "News", Body: body, BaseURL: "https://church.example/"})
	if err != nil {
		t.Fatal(err)
	}
	want := `<img src="https://church.example` + okImage + `" alt="Our picnic" width="300" style="max-width:100%;height:auto;`
	if !strings.Contains(out, want) {
		t.Fatalf("picture not absolute with its style:\n%s", out)
	}
	if strings.Contains(out, "evil.example") {
		t.Fatalf("an outside picture got into the email:\n%s", out)
	}
	if strings.Count(out, "<img") != 1 {
		t.Fatalf("pictures = %d", strings.Count(out, "<img"))
	}
	out, _ = RenderEmail(EmailPage{Body: `<img src="` + okImage + `">`, BaseURL: "https://church.example"})
	if strings.Contains(out, "<img") {
		t.Fatalf("a picture with no description was sent:\n%s", out)
	}
}
