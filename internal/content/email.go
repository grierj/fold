package content

import (
	"bytes"
	_ "embed"
	"html/template"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// The announcement email (M3.4b, PLAN 7.3): the sanitized body in a simple,
// mobile-safe layout (one 600px column of tables, inline styles, since email
// clients ignore stylesheets) with the church's name above it and, below,
// why the reader gets it, a link to their email settings and the church's
// postal address. Email can't use the app's theme tokens, so the colors are
// written out here, in the same neutral grays.

//go:embed email.html
var emailHTML string

var emailTemplate = template.Must(template.New("email").Parse(emailHTML))

// EmailPage is what one email shows around its body.
type EmailPage struct {
	Church  string
	Subject string
	// Body is HTML with merge fields filled in; it is sanitized again here,
	// so nothing but the composer's formatting can reach an inbox.
	Body string
	// Footer says why the reader gets this: "You're getting message notices
	// from First Church."
	Footer      string
	SettingsURL string
	Address     []string
}

// RenderEmail makes the HTML part of an email.
func RenderEmail(p EmailPage) (string, error) {
	var b bytes.Buffer
	err := emailTemplate.Execute(&b, struct {
		EmailPage
		Body template.HTML
	}{p, template.HTML(emailStyles(SanitizeHTML(p.Body)))})
	return b.String(), err
}

// Inline styles for each element the sanitizer keeps.
var elementStyles = map[atom.Atom]string{
	atom.P:          "margin:0 0 16px 0;",
	atom.H2:         "margin:24px 0 12px 0;font-size:22px;line-height:28px;font-weight:bold;",
	atom.H3:         "margin:20px 0 8px 0;font-size:18px;line-height:24px;font-weight:bold;",
	atom.Ul:         "margin:0 0 16px 0;padding-left:24px;",
	atom.Ol:         "margin:0 0 16px 0;padding-left:24px;",
	atom.Li:         "margin:0 0 4px 0;",
	atom.Blockquote: "margin:0 0 16px 0;padding-left:12px;border-left:3px solid #d4d4d8;color:#52525b;",
	atom.Hr:         "border:0;border-top:1px solid #e4e4e7;margin:24px 0;",
	atom.A:          "color:#1d4ed8;text-decoration:underline;",
}

const buttonStyle = "display:inline-block;padding:12px 20px;background-color:#1d4ed8;color:#ffffff;text-decoration:none;border-radius:6px;font-weight:bold;"

// emailStyles adds the inline styles to sanitized HTML. It parses and
// re-renders it, so whatever it returns is well-formed and escaped.
func emailStyles(s string) string {
	ctx := &html.Node{Type: html.ElementNode, Data: "div", DataAtom: atom.Div}
	nodes, err := html.ParseFragment(strings.NewReader(s), ctx)
	if err != nil {
		return template.HTMLEscapeString(PlainText(s))
	}
	var b strings.Builder
	for _, n := range nodes {
		styleTree(n)
		if err := html.Render(&b, n); err != nil {
			return template.HTMLEscapeString(PlainText(s))
		}
	}
	return b.String()
}

func styleTree(n *html.Node) {
	if n.Type == html.ElementNode {
		style := elementStyles[n.DataAtom]
		if n.DataAtom == atom.A && hasClass(n, ButtonClass) {
			style = buttonStyle
		}
		if style != "" {
			n.Attr = append(n.Attr, html.Attribute{Key: "style", Val: style})
		}
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		styleTree(c)
	}
}

func hasClass(n *html.Node, class string) bool {
	for _, a := range n.Attr {
		if a.Key == "class" && a.Val == class {
			return true
		}
	}
	return false
}
