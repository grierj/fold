// Package content cleans and converts what people write in the composer
// (M3.4, PLAN 7.3). HTML from the editor is sanitized with bluemonday before
// it is stored; the plain-text part of an email is made from it.
package content

import (
	"regexp"
	"strings"

	"github.com/microcosm-cc/bluemonday"
	"golang.org/x/net/html"
)

// ButtonClass marks a link the editor's button block made.
const ButtonClass = "fold-button"

var policy = func() *bluemonday.Policy {
	p := bluemonday.NewPolicy()
	p.AllowElements("p", "br", "strong", "b", "em", "i", "u", "s", "h2", "h3", "ul", "ol", "li", "blockquote", "hr")
	p.AllowAttrs("href").OnElements("a")
	p.AllowAttrs("class").Matching(regexp.MustCompile(`^` + ButtonClass + `$`)).OnElements("a")
	p.AllowURLSchemes("https", "http", "mailto")
	p.RequireParseableURLs(true)
	p.RequireNoReferrerOnLinks(true)
	p.AddTargetBlankToFullyQualifiedLinks(true)
	return p
}()

// SanitizeHTML keeps only the composer's formatting: paragraphs, bold,
// italic, underline, strike, two heading sizes, lists, quotes, rules, links
// (http, https, mailto) and button links. Scripts, styles, event handlers,
// javascript: links and everything else are removed.
// Merge fields survive, including as a link's address.
func SanitizeHTML(s string) string {
	return strings.TrimSpace(restoreLinkFields(policy.Sanitize(protectLinkFields(s))))
}

var blankLines = regexp.MustCompile(`\n{3,}`)

// PlainText is the plain-text part of an email made from sanitized HTML:
// paragraphs and headings as blank-line separated blocks, list items with
// "- ", and each link's address after its text.
func PlainText(s string) string {
	z := html.NewTokenizer(strings.NewReader(s))
	var b strings.Builder
	var href string
	block := func() {
		t := strings.TrimRight(b.String(), " ")
		b.Reset()
		b.WriteString(t)
		if t != "" && !strings.HasSuffix(t, "\n\n") {
			b.WriteString("\n\n")
		}
	}
	for {
		switch z.Next() {
		case html.ErrorToken:
			out := strings.TrimSpace(b.String())
			return blankLines.ReplaceAllString(out, "\n\n")
		case html.TextToken:
			raw := string(z.Text())
			words := strings.Join(strings.Fields(raw), " ")
			cur := b.String()
			// Keep one space where the source had any, but never at a line's start.
			if raw != "" && strings.TrimLeft(raw, " \t\n\r") != raw && cur != "" && !strings.HasSuffix(cur, " ") && !strings.HasSuffix(cur, "\n") {
				b.WriteString(" ")
			}
			b.WriteString(words)
			if words != "" && strings.TrimRight(raw, " \t\n\r") != raw {
				b.WriteString(" ")
			}
		case html.StartTagToken, html.SelfClosingTagToken:
			name, hasAttr := z.TagName()
			switch string(name) {
			case "p", "h2", "h3", "ul", "ol", "blockquote":
				block()
			case "li":
				t := strings.TrimRight(b.String(), " ")
				b.Reset()
				b.WriteString(t)
				if t != "" && !strings.HasSuffix(t, "\n") {
					b.WriteString("\n")
				}
				b.WriteString("- ")
			case "br":
				b.WriteString("\n")
			case "hr":
				block()
				b.WriteString("---\n\n")
			case "a":
				href = ""
				for hasAttr {
					var k, v []byte
					k, v, hasAttr = z.TagAttr()
					if string(k) == "href" {
						href = string(v)
					}
				}
			}
		case html.EndTagToken:
			name, _ := z.TagName()
			switch string(name) {
			case "p", "h2", "h3", "ul", "ol", "blockquote":
				block()
			case "a":
				if href != "" && !strings.HasPrefix(href, "mailto:") {
					b.WriteString(" (" + href + ")")
				}
				href = ""
			}
		}
	}
}
