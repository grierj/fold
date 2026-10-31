package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"net/http"
	"strings"

	"fold/internal/notify"
	"fold/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// Unsubscribe links (M3.3, PLAN 7.4, RFC 8058). Every optional email carries
// a signed link naming its person and the kind of email it is. The link
// itself is the authority: the one-click POST and the email settings page
// need no sign-in. Links don't expire, so an old email's button still works;
// they change with SESSION_SECRET. A token that doesn't verify answers 404
// and changes nothing.

// unsubscribeVersion is the first byte of every token, so the format can change.
const unsubscribeVersion = 1

func (a *App) unsubscribeKey() []byte {
	key := sha256.Sum256(append([]byte("fold:unsubscribe:v1:"), a.Config.SessionSecret...))
	return key[:]
}

// unsubscribeToken signs (user, scope): version, user id, scope, then a
// 16-byte HMAC, all base64url.
func (a *App) unsubscribeToken(userID uint, scope string) string {
	body := []byte{unsubscribeVersion}
	body = binary.AppendUvarint(body, uint64(userID))
	body = append(body, scope...)
	mac := hmac.New(sha256.New, a.unsubscribeKey())
	mac.Write(body)
	return base64.RawURLEncoding.EncodeToString(append(body, mac.Sum(nil)[:16]...))
}

var errBadUnsubscribe = errors.New("unsubscribe link not valid")

// parseUnsubscribeToken checks the signature and returns the user id and scope.
func (a *App) parseUnsubscribeToken(token string) (uint, string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(raw) < 1+1+16 || raw[0] != unsubscribeVersion || len(a.Config.SessionSecret) == 0 {
		return 0, "", errBadUnsubscribe
	}
	body, sum := raw[:len(raw)-16], raw[len(raw)-16:]
	mac := hmac.New(sha256.New, a.unsubscribeKey())
	mac.Write(body)
	if !hmac.Equal(sum, mac.Sum(nil)[:16]) {
		return 0, "", errBadUnsubscribe
	}
	id, n := binary.Uvarint(body[1:])
	if n <= 0 || id == 0 {
		return 0, "", errBadUnsubscribe
	}
	scope := string(body[1+n:])
	if !validUnsubscribeScope(scope) {
		return 0, "", errBadUnsubscribe
	}
	return uint(id), scope, nil
}

// validUnsubscribeScope: an optional notification category, the only kind
// of optional email so far (announcement scopes arrive with M3.4).
func validUnsubscribeScope(scope string) bool {
	cat, ok := strings.CutPrefix(scope, "category:")
	if !ok {
		return false
	}
	for _, c := range notify.OptionalEmailCategories() {
		if c == cat {
			return true
		}
	}
	return false
}

// unsubscribeUser loads the person a token names; a deleted account's link
// stops working.
func (a *App) unsubscribeUser(token string) (*models.User, string, bool) {
	id, scope, err := a.parseUnsubscribeToken(token)
	if err != nil {
		return nil, "", false
	}
	var u models.User
	if a.DB.First(&u, id).Error != nil {
		return nil, "", false
	}
	return &u, scope, true
}

// oneClickUnsubscribe is RFC 8058's List-Unsubscribe-Post target: a POST
// with no cookie and no CSRF token (it is in csrfExemptRoutes), authorized
// by the signed token alone. It turns off the kind of email the link came
// from (DEC-114), at once.
func (a *App) oneClickUnsubscribe(c *gin.Context) {
	u, scope, ok := a.unsubscribeUser(c.Param("token"))
	if !ok {
		c.String(http.StatusNotFound, "This unsubscribe link isn't valid.")
		return
	}
	if err := a.DB.Transaction(func(tx *gorm.DB) error {
		_, err := setEmailConsent(tx, u.ID, scope, models.ConsentOptedOut, models.ConsentSourceUnsubscribe, c.ClientIP(), c.Request.UserAgent())
		return err
	}); err != nil {
		c.String(http.StatusInternalServerError, "We couldn't unsubscribe you. Please try again.")
		return
	}
	c.String(http.StatusOK, "You're unsubscribed.")
}

// setEmailConsent appends a consent row for (user, email, scope) when the
// latest one says otherwise, and reports whether it did. Consent history is
// append-only (5.3); the person's row is locked so two quick changes can't
// interleave.
func setEmailConsent(tx *gorm.DB, userID uint, scope, status, source, ip, userAgent string) (bool, error) {
	if err := tx.Exec("SELECT 1 FROM users WHERE id = ? FOR NO KEY UPDATE", userID).Error; err != nil {
		return false, err
	}
	var latest models.Consent
	err := tx.Where("user_id = ? AND channel = ? AND scope = ?", userID, models.ConsentEmail, scope).
		Order("at DESC, id DESC").First(&latest).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return false, err
	}
	// No row means opted in (the default for members, 7.4).
	current := models.ConsentOptedIn
	if err == nil {
		current = latest.Status
	}
	if current == status {
		return false, nil
	}
	return true, tx.Create(&models.Consent{UserID: userID, Channel: models.ConsentEmail, Scope: scope, Status: status,
		Source: source, IP: ip, UserAgent: clipRunes(userAgent, 500)}).Error
}

// settingChurchAddress is the church's postal address for email footers
// (PLAN 10.5, DEC-116); empty until an admin sets it.
const settingChurchAddress = "church_address"

// footerFacts are the church's name and address for one batch of email.
type footerFacts struct{ site, address string }

// optionalEmailFooter ends an optional email (PLAN 10.5): what it is, how to
// stop that kind or change email settings, and the church's address.
func (a *App) optionalEmailFooter(f footerFacts, category, token string) string {
	footer := "\n\n--\n" + notify.YoureGetting(category, f.site) +
		". To stop them or change which emails you get, visit " + a.emailBaseURL() + "/email-settings/" + token
	if addr := strings.TrimSpace(f.address); addr != "" {
		for _, line := range strings.Split(strings.ReplaceAll(addr, "\r", ""), "\n") {
			if line = strings.TrimSpace(line); line != "" {
				footer += "\n" + line
			}
		}
	}
	return footer
}

// oneClickURL is the List-Unsubscribe link for token, or "" where mail
// clients would ignore it: they honor one-click links over https only
// (RFC 8058), and dev runs on http.
func (a *App) oneClickURL(token string) string {
	if base := a.emailBaseURL(); strings.HasPrefix(base, "https://") || a.Config.IsDev() {
		return base + "/u/unsubscribe/" + token
	}
	return ""
}
