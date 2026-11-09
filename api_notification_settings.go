package main

import (
	"net/http"

	"fold/models"

	"github.com/gin-gonic/gin"
	"github.com/gin-gonic/gin/binding"
)

// notificationSettingsJSON is the Me > Notifications payload: the email
// switches (DEC-97, DEC-115) and the push section (M3.13b, DEC-122).
type notificationSettingsJSON struct {
	OptionalEmailOff bool `json:"optionalEmailOff"`
	// Kinds are the optional kinds with their email switches (DEC-115).
	Kinds []emailKindJSON `json:"kinds"`
	// Push is the person's devices and Push switches. Only signed in: the
	// signed-out email settings page never shows it.
	Push pushSettingsJSON `json:"push"`
}

func (a *App) notificationSettingsFor(u *models.User) (notificationSettingsJSON, error) {
	s, err := a.emailSettingsFor(a.DB, u)
	if err != nil {
		return notificationSettingsJSON{}, err
	}
	p, err := a.pushSettingsFor(a.DB, u)
	if err != nil {
		return notificationSettingsJSON{}, err
	}
	return notificationSettingsJSON{OptionalEmailOff: s.OptionalEmailOff, Kinds: s.Kinds, Push: p}, nil
}

func (a *App) apiGetNotificationSettings(c *gin.Context) {
	s, err := a.notificationSettingsFor(apiUser(c))
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not load notification settings")
		return
	}
	c.JSON(http.StatusOK, gin.H{"settings": s})
}

// apiUpdateNotificationSettings changes one switch. Email (DEC-115): a kind
// ({category, off}) or every optional kind ({optionalEmailOff}, DEC-97).
// Push ({channel: "push", category, off}, DEC-122): one optional category.
// Required kinds (account, safety, requests waiting on you) are never
// affected. The append-only consents rows, with their IP and user agent,
// are the record of the change (5.3), so no AuditLog row is added.
func (a *App) apiUpdateNotificationSettings(c *gin.Context) {
	me := apiUser(c)
	var which struct {
		Channel  string  `json:"channel"`
		Category *string `json:"category"`
		Off      *bool   `json:"off"`
	}
	if err := c.ShouldBindBodyWith(&which, binding.JSON); err != nil {
		jsonError(c, http.StatusBadRequest, "invalid request")
		return
	}
	switch which.Channel {
	case "", models.ConsentEmail:
		if !a.applyEmailSettings(c, me, models.ConsentSourceWebForm, me, nil) {
			return
		}
	case models.ConsentPush:
		if which.Category == nil {
			jsonError(c, http.StatusBadRequest, "invalid request")
			return
		}
		if !a.applyPushSettings(c, me, *which.Category, which.Off) {
			return
		}
	default:
		jsonError(c, http.StatusBadRequest, "invalid request")
		return
	}
	s, err := a.notificationSettingsFor(me)
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not load notification settings")
		return
	}
	c.JSON(http.StatusOK, gin.H{"settings": s})
}
