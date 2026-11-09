package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"fold/internal/jobs"
	"fold/internal/mail"
	"fold/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// The random nonce is the email credential. The signature binds its stored
// authority, and only the hash of the complete credential is stored.
type guardianEmailAuthority struct {
	GuardianID     uint   `json:"guardianId"`
	GuardianEmail  string `json:"guardianEmail"`
	SessionVersion int    `json:"sessionVersion"`
	LinkID         uint   `json:"linkId"`
	TeenID         uint   `json:"teenId"`
	Scope          string `json:"scope"`
	Status         string `json:"status"`
	ConsentID      uint   `json:"consentId"`
	ExpiresUnix    int64  `json:"expiresUnix"`
}

type guardianEmailPreviewJSON struct {
	TeenName  string    `json:"teenName"`
	Scope     string    `json:"scope"`
	Status    string    `json:"status"`
	ExpiresAt time.Time `json:"expiresAt"`
}

// guardianEmailSignature signs the link under its own key derived from
// SESSION_SECRET, not the secret itself, which also signs session cookies.
func (a *App) guardianEmailSignature(nonce string, authority guardianEmailAuthority) (string, error) {
	key, err := deriveKey(a.Config.SessionSecret, "guardian-email:v1")
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte("fold:teen-consent:v1\n" + nonce + "\n"))
	b, _ := json.Marshal(authority)
	mac.Write(b)
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

// Issuing another email for one link replaces that link only. A guardian
// with two teens can use both emails independently.
func (a *App) createGuardianEmailToken(tx *gorm.DB, guardian, teen *models.User, linkID uint) (string, error) {
	consentID, err := latestAccountConsentID(tx, teen.ID)
	if err != nil {
		return "", err
	}
	expires := time.Now().UTC().Add(tokenTTL).Truncate(time.Second)
	payload := guardianEmailAuthority{GuardianID: guardian.ID, GuardianEmail: mail.NormalizeAddress(guardian.Email), SessionVersion: guardian.SessionVersion,
		LinkID: linkID, TeenID: teen.ID, Scope: models.TeenConsentAccount, Status: models.ConsentGranted, ConsentID: consentID, ExpiresUnix: expires.Unix()}
	nonce, err := newRawToken()
	if err != nil {
		return "", err
	}
	sig, err := a.guardianEmailSignature(nonce, payload)
	if err != nil {
		return "", err
	}
	raw := nonce + "." + sig
	_, err = storeAccountToken(tx, raw, models.TokenTeenConsent, guardian.ID, jsonString(payload), expires,
		"payload->>'linkId' = ?", fmt.Sprint(linkID))
	return raw, err
}

func (a *App) guardianEmailToken(tx *gorm.DB, raw string) (*models.AccountToken, guardianEmailAuthority, error) {
	var payload guardianEmailAuthority
	parts := strings.Split(raw, ".")
	if len(parts) != 2 || len(parts[0]) != rawTokenLen || len(parts[1]) != digestLen {
		return nil, payload, errTokenInvalid
	}
	row, err := findAccountToken(tx, models.TokenTeenConsent, raw)
	if err != nil {
		return nil, payload, errTokenInvalid
	}
	if json.Unmarshal([]byte(row.Payload), &payload) != nil || payload.GuardianID != row.UserID ||
		payload.Scope != models.TeenConsentAccount || payload.Status != models.ConsentGranted || payload.ExpiresUnix != row.ExpiresAt.Unix() {
		return nil, payload, errTokenInvalid
	}
	want, err := a.guardianEmailSignature(parts[0], payload)
	if err != nil || !hmac.Equal([]byte(parts[1]), []byte(want)) {
		return nil, payload, errTokenInvalid
	}
	return row, payload, nil
}

// guardianEmailActors decides a link's use under the gate and row locks.
func (a *App) guardianEmailActors(tx *gorm.DB, payload guardianEmailAuthority) (*models.User, *models.User, error) {
	guardian, teen, _, err := lockedTeenGuardian(tx, payload.GuardianID, payload.TeenID, payload.LinkID)
	if err != nil {
		return nil, nil, errTokenInvalid
	}
	return guardian, teen, guardianEmailStillBinds(tx, payload, guardian, teen)
}

// guardianEmailPreviewActors is the same decision for the public page that
// only shows who the link is for: a plain read, no gate and no locks, so
// anyone holding a link cannot queue behind (or hold up) every contact writer.
func (a *App) guardianEmailPreviewActors(tx *gorm.DB, payload guardianEmailAuthority) (*models.User, *models.User, error) {
	guardian, teen, _, err := readTeenGuardian(tx, payload.GuardianID, payload.TeenID, payload.LinkID)
	if err != nil {
		return nil, nil, errTokenInvalid
	}
	return guardian, teen, guardianEmailStillBinds(tx, payload, guardian, teen)
}

func guardianEmailStillBinds(tx *gorm.DB, payload guardianEmailAuthority, guardian, teen *models.User) error {
	consentID, err := latestAccountConsentID(tx, teen.ID)
	if err != nil || consentID != payload.ConsentID || guardian.SessionVersion != payload.SessionVersion || mail.NormalizeAddress(guardian.Email) != payload.GuardianEmail {
		return errTokenInvalid
	}
	return nil
}

func (a *App) apiPreviewGuardianEmail(c *gin.Context) {
	var preview guardianEmailPreviewJSON
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		row, payload, err := a.guardianEmailToken(tx, c.Query("token"))
		if err != nil {
			return err
		}
		_, teen, err := a.guardianEmailPreviewActors(tx, payload)
		if err != nil {
			return err
		}
		preview = guardianEmailPreviewJSON{TeenName: teen.PublicName(), Scope: payload.Scope, Status: payload.Status, ExpiresAt: row.ExpiresAt}
		return nil
	})
	if err != nil {
		fail(c, errLinkExpired)
		return
	}
	c.JSON(http.StatusOK, preview)
}

func (a *App) apiUseGuardianEmail(c *gin.Context) {
	var body struct {
		Token string `json:"token"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		fail(c, errLinkExpired)
		return
	}
	err := a.contactMutation(contactChange{}, func(tx *gorm.DB) error {
		_, payload, err := a.guardianEmailToken(tx, body.Token)
		if err != nil {
			return err
		}
		guardian, teen, err := a.guardianEmailActors(tx, payload)
		if err != nil {
			return err
		}
		// Reload/consume after obtaining user and link locks, matching issuance
		// order. A concurrent resend or use cannot redeem a replaced token.
		if _, err := consumeAccountToken(tx, models.TokenTeenConsent, body.Token); err != nil {
			return err
		}
		return a.changeTeenConsent(tx, c, teen, guardian, teenConsentChange{Scope: payload.Scope, Status: payload.Status, ConfirmedGuardian: true})
	})
	if err != nil {
		fail(c, errLinkExpired)
		return
	}
	c.JSON(http.StatusOK, gin.H{})
}

func (a *App) apiResendGuardianConsentEmail(c *gin.Context) {
	link, err := ownTeenLink(a.DB, c)
	if err != nil || link.Status != models.GuardianConfirmed {
		jsonError(c, http.StatusForbidden, "forbidden")
		return
	}
	me := apiUser(c)
	allowed, err := a.targetAllow(c.Request.Context(), guardianEmailAddrKey(me.Email))
	if err != nil {
		log.Printf("guardian consent email limit check: actor=%d err=%s", me.ID, jobs.CleanError(err))
		jsonError(c, http.StatusInternalServerError, "could not send the consent email")
		return
	}
	switch {
	case !allowed:
		// Over the limit answers the ordinary success (DEC-52); nothing is queued.
		log.Printf("guardian consent email limited: actor=%d", me.ID)
	default:
		// The answer says an email is on its way, so the job is durably queued
		// before it is given (R6-4).
		if _, err := guardianConsentMailType.Enqueue(a.DB, guardianConsentMailJob{GuardianLinkID: link.ID}, time.Now(), ""); err != nil {
			log.Printf("guardian consent email enqueue: link=%d err=%s", link.ID, jobs.CleanError(err))
			jsonError(c, http.StatusInternalServerError, "could not send the consent email")
			return
		}
	}
	c.JSON(http.StatusOK, noticeResponseJSON{Message: "Check your email for a consent link. It expires in " + tokenLifetime + "."})
}
