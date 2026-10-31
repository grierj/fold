package main

import (
	"net/http"
	"strconv"

	"fold/internal/mail"

	"github.com/gin-gonic/gin"
)

// ---------------------------------------------------------------------------
// Dev tooling (M1.2)
// ---------------------------------------------------------------------------

// devOutbox renders what the devoutbox sender stored (5.6). It is
// unreachable unless APP_ENV=dev: anything else gets a 404, so no email
// contents ever leak in production.
func (a *App) devOutbox(c *gin.Context) {
	if !a.Config.IsDev() {
		c.Status(http.StatusNotFound)
		return
	}
	outbox, ok := mail.Unwrap(a.Mailer).(*mail.DevOutbox)
	if !ok {
		c.String(http.StatusInternalServerError, "the devoutbox sender is not configured")
		return
	}
	msgs, err := outbox.List(100)
	if err != nil {
		c.String(http.StatusInternalServerError, "could not read the outbox")
		return
	}
	c.HTML(http.StatusOK, "outbox.html", gin.H{"Messages": msgs})
}

// emailPreviewCSP is the policy for a page that shows an email's HTML (the
// dev outbox and the composer's preview): email needs inline styles, and
// nothing in it may run, load anything but images, or reach the app.
const emailPreviewCSP = "default-src 'none'; style-src 'unsafe-inline'; img-src https: data:; base-uri 'none'; form-action 'none'; " +
	"frame-ancestors 'self'; sandbox allow-popups allow-popups-to-escape-sandbox"

// serveEmailHTML answers with an email's HTML under emailPreviewCSP.
func serveEmailHTML(c *gin.Context, html string) {
	c.Header("Content-Security-Policy", emailPreviewCSP)
	c.Header("Cache-Control", "no-store")
	c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(html))
}

// devOutboxHTML shows one stored email's HTML part (M3.4b); dev only.
func (a *App) devOutboxHTML(c *gin.Context) {
	if !a.Config.IsDev() {
		c.Status(http.StatusNotFound)
		return
	}
	outbox, ok := mail.Unwrap(a.Mailer).(*mail.DevOutbox)
	id, err := strconv.ParseUint(c.Param("id"), 10, 63)
	if !ok || err != nil {
		c.Status(http.StatusNotFound)
		return
	}
	html, err := outbox.HTMLOf(uint(id))
	if err != nil || html == "" {
		c.Status(http.StatusNotFound)
		return
	}
	serveEmailHTML(c, html)
}
