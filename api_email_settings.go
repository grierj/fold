package main

import (
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"

	"fold/internal/audit"
	"fold/internal/notify"
	"fold/models"

	"github.com/gin-gonic/gin"
	"github.com/gin-gonic/gin/binding"
	"gorm.io/gorm"
)

// Email settings (M3.3b, DEC-115): a switch per optional kind of email plus
// "Turn off all optional email", on the signed-out page an email's link
// opens (/email-settings/:token) and on Me → Notifications. Account and
// safety email can't be turned off. Every change appends a consent row.

// emailKindJSON is one optional kind of email and whether it is off.
type emailKindJSON struct {
	Category string `json:"category"`
	Off      bool   `json:"off"`
}

type emailSettingsJSON struct {
	// Kinds are the optional kinds this person can get, in display order;
	// admin notices only for admins.
	Kinds []emailKindJSON `json:"kinds"`
	// OptionalEmailOff: every kind listed is off.
	OptionalEmailOff bool `json:"optionalEmailOff"`
}

// emailKindsFor are the optional categories a person's settings list.
func emailKindsFor(u *models.User) []string {
	out := []string{}
	for _, c := range notify.OptionalEmailCategories() {
		if c == notify.CatAdmin && !asPerson(u).Admin {
			continue
		}
		out = append(out, c)
	}
	return out
}

func (a *App) emailSettingsFor(db *gorm.DB, u *models.User) (emailSettingsJSON, error) {
	out := emailSettingsJSON{Kinds: []emailKindJSON{}, OptionalEmailOff: true}
	optOuts, err := emailOptOuts(db, []uint{u.ID})
	if err != nil {
		return out, err
	}
	for _, c := range emailKindsFor(u) {
		off := optOuts[u.ID][categoryScope(c)]
		out.Kinds = append(out.Kinds, emailKindJSON{Category: c, Off: off})
		out.OptionalEmailOff = out.OptionalEmailOff && off
	}
	return out, nil
}

// emailSettingsChange is one switch: a kind (category) or, with no
// category, every kind listed.
type emailSettingsChange struct {
	Category *string `json:"category"`
	Off      *bool   `json:"off"`
	// OptionalEmailOff is the older Me field (DEC-97), the same as no category.
	OptionalEmailOff *bool `json:"optionalEmailOff"`
}

// applyEmailSettings validates and writes one change; false means a 400 was sent.
func (a *App) applyEmailSettings(c *gin.Context, u *models.User, source string) bool {
	var body emailSettingsChange
	// BodyWith: Me → Notifications reads the body first for its channel.
	if err := c.ShouldBindBodyWith(&body, binding.JSON); err != nil {
		jsonError(c, http.StatusBadRequest, "invalid request")
		return false
	}
	off := body.Off
	if off == nil {
		off = body.OptionalEmailOff
	}
	if off == nil {
		jsonError(c, http.StatusBadRequest, "invalid request")
		return false
	}
	// "Turn off all optional email" writes every optional kind, even one the
	// person doesn't get now, so it survives becoming an admin (DEC-97).
	kinds := notify.OptionalEmailCategories()
	if body.Category != nil {
		found := false
		for _, k := range emailKindsFor(u) {
			found = found || k == *body.Category
		}
		if !found {
			jsonError(c, http.StatusBadRequest, "that kind of email can't be changed here")
			return false
		}
		kinds = []string{*body.Category}
	}
	status := models.ConsentOptedIn
	if *off {
		status = models.ConsentOptedOut
	}
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		for _, k := range kinds {
			if _, err := setEmailConsent(tx, u.ID, categoryScope(k), status, source, c.ClientIP(), c.Request.UserAgent()); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not save email settings")
		return false
	}
	return true
}

// maskEmail shows enough of an address to recognize it: "s•••@example.com".
func maskEmail(email string) string {
	local, domain, ok := strings.Cut(email, "@")
	if !ok || local == "" {
		return "•••"
	}
	r, _ := utf8.DecodeRuneInString(local)
	return string(r) + "•••@" + domain
}

// apiTokenEmailSettings is the signed-out page's read: the masked address,
// the kind the link came from, and the switches. 404 for a bad link.
func (a *App) apiTokenEmailSettings(c *gin.Context) {
	u, scope, ok := a.unsubscribeUser(c.Param("token"))
	if !ok {
		jsonError(c, http.StatusNotFound, "not found")
		return
	}
	s, err := a.emailSettingsFor(a.DB, u)
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not load email settings")
		return
	}
	c.JSON(http.StatusOK, gin.H{"emailSettings": s, "email": maskEmail(u.Email),
		"fromCategory": strings.TrimPrefix(scope, "category:")})
}

// apiTokenUpdateEmailSettings changes one switch from the signed-out page.
// The link is the authority; the request still carries the CSRF token like
// every other form.
func (a *App) apiTokenUpdateEmailSettings(c *gin.Context) {
	u, _, ok := a.unsubscribeUser(c.Param("token"))
	if !ok {
		jsonError(c, http.StatusNotFound, "not found")
		return
	}
	if !a.applyEmailSettings(c, u, models.ConsentSourceUnsubscribe) {
		return
	}
	s, err := a.emailSettingsFor(a.DB, u)
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not load email settings")
		return
	}
	c.JSON(http.StatusOK, gin.H{"emailSettings": s})
}

// maxChurchAddress bounds the footer address (a few lines).
const maxChurchAddress = 300

func (a *App) apiAdminGetChurchAddress(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"address": a.churchAddress()})
}

// apiAdminSetChurchAddress saves the church's postal address for email
// footers (DEC-116), audited. Empty clears it.
func (a *App) apiAdminSetChurchAddress(c *gin.Context) {
	var body struct {
		Address *string `json:"address"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.Address == nil {
		jsonError(c, http.StatusBadRequest, "invalid request")
		return
	}
	lines := []string{}
	for _, l := range strings.Split(strings.ReplaceAll(*body.Address, "\r", ""), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	addr := strings.Join(lines, "\n")
	for _, r := range addr {
		// Plain characters only: no NUL (Postgres refuses it) or other control,
		// line-separator or text-direction characters in an email footer.
		if r != '\n' && (unicode.IsControl(r) || unicode.Is(unicode.Bidi_Control, r) || r == '\u2028' || r == '\u2029') {
			jsonError(c, http.StatusBadRequest, errChurchAddressCharacters)
			return
		}
	}
	if utf8.RuneCountInString(addr) > maxChurchAddress || len(lines) > 6 {
		jsonError(c, http.StatusBadRequest, errChurchAddressLong)
		return
	}
	if err := a.saveSetting(c, settingChurchAddress, addr, audit.ChurchAddressChanged, map[string]any{"set": addr != ""}); err != nil {
		jsonError(c, http.StatusInternalServerError, "could not save the address")
		return
	}
	c.JSON(http.StatusOK, gin.H{"address": addr})
}

const errChurchAddressLong = "the address must be at most 300 characters on 6 lines"

const errChurchAddressCharacters = "the address has a character that can't go in an email"

// churchAddress is the church's postal address for footers, or "".
func (a *App) churchAddress() string {
	var s models.Setting
	if a.DB.Where("key = ?", settingChurchAddress).First(&s).Error != nil {
		return ""
	}
	return strings.TrimSpace(s.Value)
}
