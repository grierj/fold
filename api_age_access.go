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
		jsonError(c, http.StatusBadRequest, "birth date must be a real date (YYYY-MM-DD)")
		return "", nil, false
	}
	kind, err := accounts.Classify(strings.TrimSpace(band), birth, a.householdClock(time.Now().UTC()))
	if errors.Is(err, accounts.ErrUnder13) {
		jsonError(c, http.StatusBadRequest, "Fold accounts start at 13. A parent or guardian can add a younger child to their household and sign them up for events.")
		return "", nil, false
	}
	if err != nil {
		jsonError(c, http.StatusBadRequest, "pick 18 or older, or 13 to 17 with a birth date that matches")
		return "", nil, false
	}
	if kind == models.AccountAdult {
		birth = nil
	}
	return kind, birth, true
}
