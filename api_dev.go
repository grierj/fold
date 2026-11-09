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
