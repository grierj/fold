// Package content cleans and converts what people write in the composer
// (M3.4, PLAN 7.3). HTML from the editor is sanitized with bluemonday before
// it is stored; the plain-text part of an email is made from it.
package content

import (
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/microcosm-cc/bluemonday"
	"golang.org/x/net/html"
)

// ButtonClass marks a link the editor's button block made.
const ButtonClass = "fold-button"

var policy = func() *bluemonday.Policy {
	p := bluemonday.NewPolicy()
	p.AllowElements("p", "br", "strong", "b", "em", "i", "u", "s", "h2", "h3", "ul", "ol", "li", "blockquote", "hr")
	// Relative addresses are for the composer's pictures alone: a link may
	// only start with http, https or mailto, and a picture's address is
	// matched whole (images.go).
	p.AllowRelativeURLs(true)
	p.AllowAttrs("href").Matching(linkHrefRe).OnElements("a")
	p.AllowElements("img")
	p.AllowAttrs("src").Matching(imageSrcRe).OnElements("img")
	p.AllowAttrs("alt").Matching(imageAltRe).OnElements("img")
	p.AllowAttrs("width").Matching(imageWidthRe).OnElements("img")
	p.AllowAttrs("class").Matching(regexp.MustCompile(`^` + ButtonClass + `$`)).OnElements("a")
	p.AllowURLSchemes("https", "http", "mailto")
	p.RequireParseableURLs(true)
	p.RequireNoReferrerOnLinks(true)
	p.AddTargetBlankToFullyQualifiedLinks(true)
	return p
}()

// SanitizeHTML keeps only the composer's formatting: paragraphs, bold,
// italic, underline, strike, two heading sizes, lists, quotes, rules, links
// (http, https, mailto), button links and the composer's own uploaded pictures
// (each with a description; any other image is removed). Scripts, styles, event handlers,
// javascript: links and everything else are removed.
// Merge fields survive, including as a link's address.
func SanitizeHTML(s string) string {
	return strings.TrimSpace(dropBadImages(restoreLinkFields(policy.Sanitize(protectLinkFields(s)))))
}

var blankLines = regexp.MustCompile(`\n{3,}`)

// PlainText is the plain-text part of an email made from sanitized HTML:
// paragraphs and headings as blank-line separated blocks, list items with
// "- ", each link's address after its text, and each picture as
// "[Image: its description]".
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
			case "img":
				alt := strings.Join(strings.Fields(attrValue(z, hasAttr, "alt")), " ")
				block()
				if alt == "" {
					b.WriteString("[Image]")
				} else {
					b.WriteString("[Image: " + alt + "]")
				}
				block()
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

// Pictures in a message (DEC-118, Grier Nov 14 2026): only the ones the
// composer's own upload made, as a same-origin address under
// /uploads/messages/<message id>/, each with words that describe it. No count
// or size limit beyond the upload's 5MB a file. Email links to them by
// address (the sender adds the site's address); nothing is attached.

// MaxAltLen is the longest description a picture may carry.
const MaxAltLen = 300

const imageSrcPattern = `/uploads/messages/([1-9][0-9]{0,18})/[0-9a-f]{32}\.(?:jpg|png|gif)`

var (
	imageSrcRe   = regexp.MustCompile(`^` + imageSrcPattern + `$`)
	imageWidthRe = regexp.MustCompile(`^[1-9][0-9]{0,3}$`)
	// Any one visible character: a blank description is no description.
	imageAltRe = regexp.MustCompile(`(?s)\S`)
	// Only these addresses may be a link's; a relative address (allowed for
	// pictures) never is.
	linkHrefRe = regexp.MustCompile(`(?i)^\s*(https?|mailto):`)
)

// ImageMessageID is the message a picture's address belongs to, and false when
// src is not the address of one of the composer's uploads.
func ImageMessageID(src string) (uint64, bool) {
	m := imageSrcRe.FindStringSubmatch(src)
	if m == nil {
		return 0, false
	}
	id, err := strconv.ParseUint(m[1], 10, 64)
	return id, err == nil
}

// ImageSources lists the address of every picture in sanitized HTML, in order.
func ImageSources(s string) []string {
	var out []string
	z := html.NewTokenizer(strings.NewReader(s))
	for {
		switch z.Next() {
		case html.ErrorToken:
			return out
		case html.StartTagToken, html.SelfClosingTagToken:
			if name, hasAttr := z.TagName(); string(name) == "img" {
				if src := attrValue(z, hasAttr, "src"); src != "" {
					out = append(out, src)
				}
			}
		}
	}
}

// attrValue reads the value of the named attribute of the tag z just
// returned; hasAttr is TagName's second result.
func attrValue(z *html.Tokenizer, hasAttr bool, want string) string {
	val := ""
	for hasAttr {
		var k, v []byte
		k, v, hasAttr = z.TagAttr()
		if string(k) == want {
			val = string(v)
		}
	}
	return val
}

// dropBadImages removes each picture the policy left without an address of an
// upload or without words that describe it. The policy drops the attributes;
// an <img> that lost them would still be an element, so it goes here.
func dropBadImages(s string) string {
	if !strings.Contains(s, "<img") {
		return s
	}
	z := html.NewTokenizer(strings.NewReader(s))
	var b strings.Builder
	for {
		tt := z.Next()
		if tt == html.ErrorToken {
			return b.String()
		}
		if tt == html.StartTagToken || tt == html.SelfClosingTagToken {
			raw := string(z.Raw())
			name, hasAttr := z.TagName()
			if string(name) == "img" {
				src, alt := "", ""
				for hasAttr {
					var k, v []byte
					k, v, hasAttr = z.TagAttr()
					switch string(k) {
					case "src":
						src = string(v)
					case "alt":
						alt = string(v)
					}
				}
				if !imageSrcRe.MatchString(src) || strings.TrimSpace(alt) == "" || utf8.RuneCountInString(alt) > MaxAltLen {
					continue
				}
			}
			b.WriteString(raw)
			continue
		}
		b.Write(z.Raw())
	}
}
