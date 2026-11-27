package mail

import (
	"bytes"
	_ "embed"
	"html/template"
	"strings"
)

// The email layout: one 600px column of tables with inline styles, because
// mail clients ignore stylesheets. Every value passes through html/template,
// so a church or person name cannot add markup.

//go:embed layout.html
var layoutHTML string

var layout = template.Must(template.New("email").Parse(layoutHTML))

// Email is one message's words, written when it is sent.
type Email struct {
	Church  string
	Subject string
	Before  []string // paragraphs above the button
	Button  *Button
	After   []string // quieter paragraphs below it
	Footer  string
}

type Button struct {
	Label string
	URL   string
}

// Render makes the text and HTML parts.
func (e Email) Render() (text, html string, err error) {
	var t strings.Builder
	t.WriteString(e.Church + "\n\n")
	for _, p := range e.Before {
		t.WriteString(p + "\n\n")
	}
	if e.Button != nil {
		t.WriteString(e.Button.Label + ":\n" + e.Button.URL + "\n\n")
	}
	for _, p := range e.After {
		t.WriteString(p + "\n\n")
	}
	t.WriteString("--\n" + e.Footer + "\n")
	var h bytes.Buffer
	if err := layout.Execute(&h, e); err != nil {
		return "", "", err
	}
	return t.String(), h.String(), nil
}

// Message is the email ready to send to one address.
func (e Email) Message(to string) (Message, error) {
	text, html, err := e.Render()
	if err != nil {
		return Message{}, err
	}
	return Message{To: to, Subject: e.Subject, Text: text, HTML: html, FromName: e.Church}, nil
}
