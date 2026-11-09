package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"fold/internal/audit"
	"fold/internal/events"
	"fold/internal/jobs"
	"fold/internal/mail"
	"fold/internal/policy"
	"fold/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)


func (a *App) apiUpdateMe(c *gin.Context) {
	u := apiUser(c)
	var body struct {
		Name                   *string `json:"name"`
		DisplayName            *string `json:"displayName"`
		Email                  *string `json:"email"`
		CurrentPassword        string  `json:"currentPassword"`
		Phone                  *string `json:"phone"`
		Timezone               *string `json:"timezone"`
		HideContactFromLeaders *bool   `json:"hideContactFromLeaders"`
		AvatarFocal            *struct {
			X float64 `json:"x"`
			Y float64 `json:"y"`
		} `json:"avatarFocal"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		jsonError(c, http.StatusBadRequest, "invalid request body")
		return
	}
	updates := map[string]interface{}{}
	if body.Name != nil {
		name, err := optionalName(*body.Name)
		if err != nil {
			fail(c, err)
			return
		}
		updates["name"] = name
	}
	if body.DisplayName != nil {
		dn, err := displayName(*body.DisplayName)
		if err != nil {
			fail(c, err)
			return
		}
		updates["display_name"] = dn
	}
	// The email is the login. Changing it sends a verification link to the
	// new address and a notice to the old one; the change applies only after
	// verification (10.1, M1.2).
	var emailChange *string
	if body.Email != nil && mail.NormalizeAddress(*body.Email) != mail.NormalizeAddress(u.Email) {
		emailChange = body.Email
	}
	if body.Phone != nil {
		phone, err := phoneNumber(*body.Phone)
		if err != nil {
			fail(c, err)
			return
		}
		updates["phone"] = phone
	}
	if body.Timezone != nil {
		tz := strings.TrimSpace(*body.Timezone)
		if _, err := events.LoadZone(tz); err != nil {
			fail(c, errInvalidTimezone)
			return
		}
		updates["timezone"] = tz
	}
	if body.HideContactFromLeaders != nil && policy.CanEditOwnContactPrivacy(asPerson(u)) {
		updates["hide_contact_from_leaders"] = *body.HideContactFromLeaders
	}
	if body.AvatarFocal != nil {
		updates["avatar_focal_x"] = clampFocal(body.AvatarFocal.X)
		updates["avatar_focal_y"] = clampFocal(body.AvatarFocal.Y)
	}
	// An email change proves the current password before anything is saved,
	// so a wrong password leaves the other fields as they were.
	if emailChange != nil {
		if !validEmail(mail.NormalizeAddress(*emailChange)) {
			fail(c, errEnterValidEmail)
			return
		}
		if !a.reauthenticate(c, u, body.CurrentPassword) {
			return
		}
	}
	if len(updates) == 0 && emailChange == nil {
		c.JSON(http.StatusOK, profileUpdateJSON{User: a.toUserJSON(u)})
		return
	}
	updated := *u
	if len(updates) > 0 {
		err := a.DB.Transaction(func(tx *gorm.DB) error {
			var locked models.User
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&locked, u.ID).Error; err != nil {
				return err
			}
			// Recheck the account type under the lock. A teen's stored value
			// never changes through this profile preference.
			if !policy.CanEditOwnContactPrivacy(asPerson(&locked)) {
				delete(updates, "hide_contact_from_leaders")
			}
			oldHidden := locked.HideContactFromLeaders
			if err := tx.Model(&locked).Updates(updates).Error; err != nil {
				return err
			}
			if value, supplied := updates["hide_contact_from_leaders"]; supplied && value != oldHidden {
				if err := audit.Write(tx, audit.Entry{Actor: &u.ID, Action: audit.ContactPrivacyChanged,
					TargetType: "user", TargetID: u.ID, IP: c.ClientIP(),
					Meta: map[string]any{"hideContactFromLeaders": value}}); err != nil {
					return err
				}
			}
			return tx.First(&updated, u.ID).Error
		})
		if err != nil {
			jsonError(c, http.StatusInternalServerError, "could not save profile")
			return
		}
	}
	if emailChange != nil {
		a.apiRequestEmailChange(c, u, mail.NormalizeAddress(*emailChange))
		return
	}
	c.JSON(http.StatusOK, profileUpdateJSON{User: a.toUserJSON(&updated)})
}

// apiRequestEmailChange starts a verified email change (10.1, M1.2): a
// verification link goes to the new address, a notice to the old one. The
// change applies only after verification, and then ends every other
// session. The caller has already checked the new address and the current
// password.
func (a *App) apiRequestEmailChange(c *gin.Context, u *models.User, newEmail string) {
	// Validate the proof against locked credentials before any address lookup.
	// Quotas retain their independently committed transactions; token issuance
	// below rechecks this exact epoch, without nesting a second DB connection.
	var validated *models.User
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		var err error
		validated, err = lockCurrentCredentials(tx, u)
		return err
	})
	if answerStaleSession(c, err) {
		return
	}
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not start email change")
		return
	}
	u = validated
	// Per-user and per-target limits (#92): a member can't use the change
	// form to spam an address, and the target gets at most a few links.
	// Past the limit the answer is the same; nothing is sent.
	ctx := c.Request.Context()
	userOK, err := a.targetAllow(ctx, emailChangeUserKey(u.ID))
	addrOK := false
	if err == nil && userOK {
		// Account checks consume the actor budget too (#157). An exhausted
		// actor never looks up another address or spends its target budget.
		var taken int64
		if err := a.DB.Model(&models.User{}).Where("email = ? AND id <> ?", newEmail, u.ID).Count(&taken).Error; err != nil {
			jsonError(c, http.StatusInternalServerError, "could not start email change")
			return
		}
		if taken > 0 {
			fail(c, errEmailTaken)
			return
		}
		addrOK, err = a.targetAllow(ctx, emailChangeAddrKey(newEmail))
	}
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not start email change")
		return
	}
	if !userOK || !addrOK {
		// Answer as usual and log it (DEC-52): nothing is stored or sent.
		log.Printf("email change rate limited: user=%d", u.ID)
		c.JSON(http.StatusOK, profileUpdateJSON{User: a.toUserJSON(u), VerificationSent: true})
		return
	}
	oldEmail := u.Email
	var cancelRaw string
	err = a.DB.Transaction(func(tx *gorm.DB) error {
		if _, err := lockCurrentCredentials(tx, u); err != nil {
			return err
		}
		// The verification row is created first, so its deadline cannot pass
		// the cancellation token's. Its raw value is discarded: the queued job
		// rotates the hash and mails the only usable link.
		_, verifyID, err := issueAccountToken(tx, models.TokenEmailVerify, u.ID,
			map[string]any{"new_email": newEmail}, tokenTTL)
		if err != nil {
			return err
		}
		cancelRaw, _, err = issueAccountToken(tx, models.TokenEmailChangeCancel, u.ID, nil, tokenTTL)
		if err != nil {
			return err
		}
		if err := audit.Write(tx, audit.Entry{Actor: &u.ID, Action: audit.EmailChangeRequested,
			TargetType: "user", TargetID: u.ID,
			Meta: map[string]any{"oldEmail": oldEmail, "newEmail": newEmail}, IP: c.ClientIP()}); err != nil {
			return err
		}
		_, err = emailVerifyMailType.Enqueue(tx, accountTokenMailJob{AccountTokenID: verifyID}, time.Now(), "")
		return err
	})
	if answerStaleSession(c, err) {
		return
	}
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not start email change")
		return
	}
	// The old-address notice stays synchronous: once the change is verified
	// the cancellation capability is revoked and the old address survives only
	// in the audit row, so a delayed job could mint nothing or mail a stale
	// address. Failure is logged; the verification job is already durable.
	if err := a.sendEmailChangeNotice(oldEmail, newEmail, cancelRaw); err != nil {
		// Like every direct-mail log line: the user ID and a cleaned error, never
		// the address a provider refusal may echo back.
		log.Printf("email change notice for user %d: %s", u.ID, jobs.CleanError(err))
	}
	c.JSON(http.StatusOK, profileUpdateJSON{User: a.toUserJSON(u), VerificationSent: true})
}

// sendEmailChangeNotice tells the old address what's happening (10.1). It is
// synchronous, so its send budget leaves headroom under the HTTP write timeout
// for the transaction and response. Delivery jobs use a separate, longer bound.
func (a *App) sendEmailChangeNotice(to, newEmail, cancelRaw string) error {
	link := a.emailBaseURL() + "/cancel-email-change?token=" + cancelRaw
	ctx, cancel := context.WithTimeout(context.Background(), syncMailTimeout)
	defer cancel()
	return sendTracked(ctx, a.mailSender(), mail.Message{
		To:      to,
		Subject: "Your Fold sign-in email is changing",
		Text: fmt.Sprintf(`The sign-in email for your Fold account at %s is being changed to %s.

The change takes effect only after the new address is verified.

If this wasn't you, cancel the pending change and end every current sign-in here:
%s

Opening the link does not change anything until you confirm. After cancelling, reset your password and contact your church office. This link works once and expires in %s.`,
			a.siteName(), newEmail, link, tokenLifetime),
	}, nil).err
}

// apiVerifyEmailChange redeems an email verification token: the new address
// takes effect and every other session ends (10.1). The token is the
// credential, so this needs no login; the current session stays logged in
// when it belongs to the same user.
func (a *App) apiVerifyEmailChange(c *gin.Context) {
	var body struct {
		Token string `json:"token"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.Token == "" {
		jsonError(c, http.StatusBadRequest, "token is required")
		return
	}
	var fresh models.User
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		u, tok, err := lockAndConsumeAccountToken(tx, models.TokenEmailVerify, body.Token)
		if err != nil {
			return err
		}
		if !u.CanSignIn() {
			return errTokenInvalid // rolls the claim back
		}
		var payload struct {
			NewEmail string `json:"new_email"`
		}
		if err := json.Unmarshal([]byte(tok.Payload), &payload); err != nil || !validEmail(payload.NewEmail) {
			return errTokenInvalid
		}
		if tok.UserID != u.ID {
			return errTokenInvalid
		}
		var taken int64
		if err := tx.Model(&models.User{}).Where("email = ? AND id <> ?", payload.NewEmail, u.ID).Count(&taken).Error; err != nil {
			return err
		}
		if taken > 0 {
			return errEmailTaken
		}
		oldEmail := u.Email
		if err := endAllSessionsIn(tx, u, map[string]any{"email": payload.NewEmail}); err != nil {
			return err
		}
		if err := audit.Write(tx, audit.Entry{Actor: &u.ID, Action: audit.EmailChanged,
			TargetType: "user", TargetID: u.ID,
			Meta: map[string]any{"oldEmail": oldEmail, "newEmail": payload.NewEmail}, IP: c.ClientIP()}); err != nil {
			return err
		}
		// Capture this commit's epoch, never a later password reset's epoch.
		return tx.First(&fresh, u.ID).Error
	})
	switch {
	case errors.Is(err, errTokenInvalid):
		fail(c, errLinkExpired)
		return
	case errors.Is(err, errEmailTaken):
		fail(c, errEmailTaken)
		return
	case err != nil:
		jsonError(c, http.StatusInternalServerError, "could not verify email")
		return
	}
	// This browser stays logged in; every other session ends.
	if session, serr := a.Store.Get(c.Request, sessionName); serr == nil {
		if uid, ok := session.Values[sessionUserKey].(uint); ok && uid == fresh.ID {
			session.Values[sessionVersionKey] = fresh.SessionVersion
			_ = session.Save(c.Request, c.Writer)
		}
	}
	c.JSON(http.StatusOK, gin.H{"user": a.toUserJSON(&fresh)})
}
