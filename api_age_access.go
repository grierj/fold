package main

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"fold/internal/accounts"
	"fold/models"
	"github.com/gin-gonic/gin"
)

// classifyAccountInput is the age boundary shared by account creation doors.
func (a *App) classifyAccountInput(c *gin.Context, band, birthDate string) (string, *time.Time, bool) {
	birth, err := accounts.ParseBirthDate(strings.TrimSpace(birthDate))
	if err != nil {
		fail(c, errInvalidBirthDate)
		return "", nil, false
	}
	today, err := churchClockIn(a.DB, time.Now().UTC())
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not check the birth date")
		return "", nil, false
	}
	kind, err := accounts.Classify(strings.TrimSpace(band), birth, today)
	if errors.Is(err, accounts.ErrUnder13) {
		fail(c, errUnder13)
		return "", nil, false
	}
	if err != nil {
		fail(c, errAgeMismatch)
		return "", nil, false
	}
	if kind == models.AccountAdult {
		birth = nil
	}
	return kind, birth, true
}
