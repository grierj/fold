package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"net/http"
	"strings"

	"fold/internal/mail"
	"fold/internal/notify"
	"fold/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// Unsubscribe links (M3.3, PLAN 7.4, RFC 8058). Every optional email carries
// a signed link naming its person and the kind of email it is. The link
// itself is the authority: the one-click POST and the email settings page
// need no sign-in. Links don't expire, so an old email's button still works;
// they change with SESSION_SECRET. Turning email on also needs a current
// signed-in session or a v2 link bound to the locked account's current address.
// A token whose identity doesn't verify answers 404 and changes nothing.

// unsubscribeVersion is the first byte of every token, so the format can change.
const unsubscribeVersion = 2

// unsubscribeKey and unsubscribeAddressKey are nil only when SESSION_SECRET is
// empty, which Config refuses. parseUnsubscribeToken then refuses every link, so
// a token minted without a key never verifies.
func (a *App) unsubscribeKey() []byte {
	key, _ := deriveKey(a.Config.SessionSecret, "unsubscribe:v1")
	return key
}

func (a *App) unsubscribeAddressKey() []byte {
	key, _ := deriveKey(a.Config.SessionSecret, "unsubscribe-address:v1")
	return key
}

// unsubscribeToken signs the identity and scope, then separately binds the
// destination address. The identity alone still authorizes viewing and opt-out.
func (a *App) unsubscribeToken(userID uint, scope, to string) string {
	body := []byte{unsubscribeVersion}
	body = binary.AppendUvarint(body, uint64(userID))
	body = append(body, scope...)
	mac := hmac.New(sha256.New, a.unsubscribeKey())
	mac.Write(body)
	raw := append(append([]byte(nil), body...), mac.Sum(nil)[:16]...)
	link := unsubscribeLink{body: body, addressKey: a.unsubscribeAddressKey()}
	return base64.RawURLEncoding.EncodeToString(append(raw, link.addressMAC(to)...))
}

var errBadUnsubscribe = errors.New("unsubscribe link not valid")

type unsubscribeLink struct {
	UserID        uint
	Scope         string
	body, addrMAC []byte
	// The parser supplies this derived key for boundTo; it is never serialized.
	addressKey []byte
}

func (l *unsubscribeLink) addressMAC(email string) []byte {
	mac := hmac.New(sha256.New, l.addressKey)
	mac.Write(l.body)
	mac.Write([]byte{0})
	mac.Write([]byte(mail.AddressHash(email)))
	return mac.Sum(nil)[:16]
}

func (l *unsubscribeLink) boundTo(email string) bool {
	return l != nil && len(l.addrMAC) == 16 && hmac.Equal(l.addrMAC, l.addressMAC(email))
}

// parseUnsubscribeToken checks the identity signature first. V1 links retain
// their view/opt-out authority; only a v2 address MAC can authorize turn-on.
func (a *App) parseUnsubscribeToken(token string) (unsubscribeLink, error) {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(raw) < 1+1+16 || len(a.Config.SessionSecret) == 0 {
		return unsubscribeLink{}, errBadUnsubscribe
	}
	tail := 16
	switch raw[0] {
	case 1:
	case unsubscribeVersion:
		tail = 32
	default:
		return unsubscribeLink{}, errBadUnsubscribe
	}
	if len(raw) < 1+1+tail {
		return unsubscribeLink{}, errBadUnsubscribe
	}
	body, sum := raw[:len(raw)-tail], raw[len(raw)-tail:len(raw)-tail+16]
	mac := hmac.New(sha256.New, a.unsubscribeKey())
	mac.Write(body)
	if !hmac.Equal(sum, mac.Sum(nil)[:16]) {
		return unsubscribeLink{}, errBadUnsubscribe
	}
	id, n := binary.Uvarint(body[1:])
	if n <= 0 || id == 0 {
		return unsubscribeLink{}, errBadUnsubscribe
	}
	scope := string(body[1+n:])
	if !validUnsubscribeScope(scope) {
		return unsubscribeLink{}, errBadUnsubscribe
	}
	link := unsubscribeLink{UserID: uint(id), Scope: scope, body: body, addressKey: a.unsubscribeAddressKey()}
	if tail == 32 {
		link.addrMAC = raw[len(raw)-16:]
	}
	return link, nil
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
func (a *App) unsubscribeUser(token string) (*models.User, *unsubscribeLink, bool) {
	link, err := a.parseUnsubscribeToken(token)
	if err != nil {
		return nil, nil, false
	}
	var u models.User
	if a.DB.First(&u, link.UserID).Error != nil {
		return nil, nil, false
	}
	return &u, &link, true
}

// oneClickUnsubscribe is RFC 8058's List-Unsubscribe-Post target: a POST
// with no cookie and no CSRF token (it is in csrfExemptRoutes), authorized
// by the signed token alone. It turns off the kind of email the link came
// from (DEC-114), at once.
func (a *App) oneClickUnsubscribe(c *gin.Context) {
	u, link, ok := a.unsubscribeUser(c.Param("token"))
	if !ok {
		c.String(http.StatusNotFound, "This unsubscribe link isn't valid.")
		return
	}
	if err := a.DB.Transaction(func(tx *gorm.DB) error {
		_, err := setEmailConsent(tx, u.ID, link.Scope, models.ConsentOptedOut, models.ConsentSourceUnsubscribe, c.ClientIP(), c.Request.UserAgent(), nil, link)
		return err
	}); err != nil {
		if errors.Is(err, errConsentSubjectGone) {
			c.String(http.StatusNotFound, "This unsubscribe link isn't valid.")
			return
		}
		c.String(http.StatusInternalServerError, "We couldn't unsubscribe you. Please try again.")
		return
	}
	c.String(http.StatusOK, "You're unsubscribed.")
}

// setEmailConsent appends a consent row for (user, email, scope) when the
// latest one says otherwise, and reports whether it did. Consent history is
// append-only (5.3); the person's row is locked so two quick changes can't
// interleave. A signed-in writer passes its session (see setConsent); a
// signed-link writer passes the parsed link. Exactly one is required. An
// affirmative turn-on clears only the locked holder's retained address choice
// and appends evidence even if the account was already opted in.
func setEmailConsent(tx *gorm.DB, userID uint, scope, status, source, ip, userAgent string, session *models.User, link *unsubscribeLink) (bool, error) {
	if (session == nil) == (link == nil) {
		return false, errors.New("email consent requires exactly one session or link")
	}
	var turnOn func(*gorm.DB, *models.User) (bool, error)
	if status == models.ConsentOptedIn {
		turnOn = clearOptOutOnTurnOn(scope, session, link)
	}
	// No row means opted in (the default for members, 7.4).
	return setConsent(tx, models.Consent{UserID: userID, Channel: models.ConsentEmail, Scope: scope, Status: status,
		Source: source, IP: ip, UserAgent: userAgent}, models.ConsentOptedIn, session, turnOn)
}

// errConsentSubjectGone: the person a consent write names was erased (or
// soft-deleted) before the write took their row lock. Nothing is written.
var errConsentSubjectGone = errors.New("consent: the person's account is gone")

// setConsent appends row when the latest (user, channel, scope) record
// says otherwise, and reports whether it did; none is the status in force
// with no record. Consent history is append-only (5.3); the person's row is
// locked so two quick changes can't interleave. Under that lock a missing or
// soft-deleted person returns errConsentSubjectGone and writes nothing, so an
// erasure that committed first is never followed by a stale consent row. A
// signed-in writer passes its session: unless the locked row is current for it
// (sessionCurrent), errSecuritySession is returned and nothing is written. A
// signed link passes nil. Only after both checks
// may turnOn authorize and clear a retained choice; its force result requires
// an appended row even when the latest account status already matches.
func setConsent(tx *gorm.DB, row models.Consent, none string, session *models.User, turnOn func(tx *gorm.DB, fresh *models.User) (force bool, err error)) (bool, error) {
	var fresh models.User
	if err := tx.Raw("SELECT id, email, status, password_hash, session_version, logout_version, deleted_at FROM users WHERE id = ? FOR NO KEY UPDATE", row.UserID).Scan(&fresh).Error; err != nil {
		return false, err
	}
	if fresh.ID == 0 || fresh.DeletedAt.Valid {
		return false, errConsentSubjectGone
	}
	if session != nil && !sessionCurrent(&fresh, session) {
		return false, errSecuritySession
	}
	force := false
	if turnOn != nil {
		var err error
		force, err = turnOn(tx, &fresh)
		if err != nil {
			return false, err
		}
	}
	var latest models.Consent
	err := tx.Where("user_id = ? AND channel = ? AND scope = ?", row.UserID, row.Channel, row.Scope).
		Order("at DESC, id DESC").First(&latest).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return false, err
	}
	current := none
	if err == nil {
		current = latest.Status
	}
	if current == row.Status && !force {
		return false, nil
	}
	row.UserAgent = clipRunes(row.UserAgent, 500)
	return true, tx.Create(&row).Error
}

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
