package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	foldmail "fold/internal/mail"
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

func (a *App) guardianEmailSignature(nonce string, authority guardianEmailAuthority) string {
	mac := hmac.New(sha256.New, a.Config.SessionSecret)
	mac.Write([]byte("fold:teen-consent:v1\n" + nonce + "\n"))
	b, _ := json.Marshal(authority)
	mac.Write(b)
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func latestAccountConsentID(tx *gorm.DB, teenID uint) (uint, error) {
	var row models.TeenConsent
	res := tx.Where("teen_user_id = ? AND scope = ?", teenID, models.TeenConsentAccount).Order("at DESC, id DESC").Limit(1).Find(&row)
	return row.ID, res.Error
}

// Issuing another email for one link replaces that link only. A guardian
// with two teens can use both emails independently.
func (a *App) createGuardianEmailToken(tx *gorm.DB, guardian, teen *models.User, linkID uint) (string, error) {
	if len(a.Config.SessionSecret) == 0 {
		return "", fmt.Errorf("consent email signing key is unavailable")
	}
	consentID, err := latestAccountConsentID(tx, teen.ID)
	if err != nil {
		return "", err
	}
	expires := time.Now().UTC().Add(tokenTTL).Truncate(time.Second)
	payload := guardianEmailAuthority{GuardianID: guardian.ID, GuardianEmail: normalizeEmail(guardian.Email), SessionVersion: guardian.SessionVersion,
		LinkID: linkID, TeenID: teen.ID, Scope: models.TeenConsentAccount, Status: models.ConsentGranted, ConsentID: consentID, ExpiresUnix: expires.Unix()}
	nonce, err := newRawToken()
	if err != nil {
		return "", err
	}
	raw := nonce + "." + a.guardianEmailSignature(nonce, payload)
	if err := tx.Where("kind = ? AND user_id = ? AND used_at IS NULL AND payload->>'linkId' = ?", models.TokenTeenConsent, guardian.ID, fmt.Sprint(linkID)).Delete(&models.AccountToken{}).Error; err != nil {
		return "", err
	}
	row := models.AccountToken{Kind: models.TokenTeenConsent, UserID: guardian.ID, TokenHash: hashToken(raw), Payload: jsonString(payload), ExpiresAt: expires}
	return raw, tx.Create(&row).Error
}

func (a *App) guardianEmailToken(tx *gorm.DB, raw string) (*models.AccountToken, guardianEmailAuthority, error) {
	var payload guardianEmailAuthority
	parts := strings.Split(raw, ".")
	if len(parts) != 2 || len(parts[0]) != 43 || len(parts[1]) != 43 || len(a.Config.SessionSecret) == 0 {
		return nil, payload, errTokenInvalid
	}
	var row models.AccountToken
	res := tx.Where("kind = ? AND token_hash = ? AND used_at IS NULL AND expires_at > now()", models.TokenTeenConsent, hashToken(raw)).Limit(1).Find(&row)
	if res.Error != nil || res.RowsAffected == 0 {
		return nil, payload, errTokenInvalid
	}
	if json.Unmarshal([]byte(row.Payload), &payload) != nil || payload.GuardianID != row.UserID ||
		payload.Scope != models.TeenConsentAccount || payload.Status != models.ConsentGranted || payload.ExpiresUnix != row.ExpiresAt.Unix() ||
		!hmac.Equal([]byte(parts[1]), []byte(a.guardianEmailSignature(parts[0], payload))) {
		return nil, payload, errTokenInvalid
	}
	return &row, payload, nil
}

func (a *App) guardianEmailActors(tx *gorm.DB, payload guardianEmailAuthority) (*models.User, *models.User, error) {
	guardian, teen, _, err := lockedTeenGuardian(tx, payload.GuardianID, payload.TeenID, payload.LinkID)
	if err != nil {
		return nil, nil, errTokenInvalid
	}
	consentID, err := latestAccountConsentID(tx, teen.ID)
	if err != nil || consentID != payload.ConsentID || guardian.SessionVersion != payload.SessionVersion || normalizeEmail(guardian.Email) != payload.GuardianEmail {
		return nil, nil, errTokenInvalid
	}
	return guardian, teen, nil
}

func (a *App) apiPreviewGuardianEmail(c *gin.Context) {
	var preview guardianEmailPreviewJSON
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		row, payload, err := a.guardianEmailToken(tx, c.Query("token"))
		if err != nil {
			return err
		}
		_, teen, err := a.guardianEmailActors(tx, payload)
		if err != nil {
			return err
		}
		preview = guardianEmailPreviewJSON{TeenName: teen.PublicName(), Scope: payload.Scope, Status: payload.Status, ExpiresAt: row.ExpiresAt}
		return nil
	})
	if err != nil {
		jsonError(c, http.StatusBadRequest, "that link is invalid or has expired")
		return
	}
	c.JSON(http.StatusOK, preview)
}

func (a *App) apiUseGuardianEmail(c *gin.Context) {
	var body struct {
		Token string `json:"token"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		jsonError(c, http.StatusBadRequest, "that link is invalid or has expired")
		return
	}
	var mailed []uint
	err := a.DB.Transaction(func(tx *gorm.DB) error {
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
		c.Set("apiUser", guardian)
		mailed, err = a.changeTeenConsent(tx, c, teen, guardian, teenConsentChange{Scope: payload.Scope, Status: payload.Status, ConfirmedGuardian: true})
		return err
	})
	if err != nil {
		jsonError(c, http.StatusBadRequest, "that link is invalid or has expired")
		return
	}
	a.deliverNotificationEmails(mailed)
	c.JSON(http.StatusOK, gin.H{})
}

// This is transactional safeguarding mail. It names nobody, and GET only
// previews the signed action so email scanners cannot give consent.
func (a *App) sendGuardianConsentEmail(linkID uint) error {
	var link models.GuardianLink
	if err := a.DB.First(&link, linkID).Error; err != nil {
		return err
	}
	if link.TeenUserID == nil {
		return nil
	}
	var raw, email string
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		guardian, teen, _, err := lockedTeenGuardian(tx, link.GuardianUserID, *link.TeenUserID, link.ID)
		if err != nil {
			return err
		}
		email = guardian.Email
		raw, err = a.createGuardianEmailToken(tx, guardian, teen, link.ID)
		return err
	})
	if err != nil {
		return err
	}
	if a.Mailer == nil {
		return fmt.Errorf("consent email sender is unavailable")
	}
	return a.Mailer.Send(context.Background(), foldmail.Message{To: email, Subject: "Review teen account consent in Fold",
		Text: "An admin confirmed your guardian link. Review the teen account and choose whether to give account consent:\n\n" + a.emailBaseURL() + "/guardian-consent?token=" + url.QueryEscape(raw) + "\n\nThis link expires in 24 hours and can be used once. Opening it does not give consent. You can also manage consent from your Fold profile."})
}

func (a *App) apiResendGuardianConsentEmail(c *gin.Context) {
	link, err := ownTeenLink(a.DB, c)
	if err != nil || link.Status != models.GuardianConfirmed {
		jsonError(c, http.StatusForbidden, "forbidden")
		return
	}
	me := apiUser(c)
	allowed, err := a.targetAllow(c.Request.Context(), "guardian-email:addr:"+normalizeEmail(me.Email))
	if err != nil || !allowed {
		log.Printf("guardian consent email limited: actor=%d err=%v", me.ID, err)
	} else if err := a.sendGuardianConsentEmail(link.ID); err != nil {
		log.Printf("guardian consent email: link=%d err=%v", link.ID, err)
	}
	c.JSON(http.StatusOK, gin.H{"message": "Check your email for a consent link. It expires in 24 hours."})
}
