package main

import (
	"net/http"

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
