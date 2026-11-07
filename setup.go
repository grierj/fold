package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"fold/internal/events"
	"fold/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const setupSessionKey = "setupAuthorization"

// A separate lock serializes first-account creation across server processes.
const setupLockID int64 = 0x466f6c6453657475

var errSetupComplete = errors.New("setup already complete")

func newSetupToken() (string, error) {
	var token [32]byte
	if _, err := rand.Read(token[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(token[:]), nil
}

func setupTokenHash(token string) string {
	hash := sha256.Sum256([]byte(token))
	return hex.EncodeToString(hash[:])
}

func (a *App) setupAuthorized(c *gin.Context) bool {
	if a.Config.SetupToken == "" {
		return false
	}
	session, err := a.Store.Get(c.Request, sessionName)
	if err != nil {
		return false
	}
	stored, _ := session.Values[setupSessionKey].(string)
	return subtle.ConstantTimeCompare([]byte(stored), []byte(setupTokenHash(a.Config.SetupToken))) == 1
}

func (a *App) unlockSetup(c *gin.Context) {
	ready, err := a.setupComplete()
	if err != nil {
		c.String(http.StatusServiceUnavailable, "Setup isn't available right now. Try again in a moment.")
		return
	}
	if ready {
		c.Redirect(http.StatusSeeOther, "/")
		return
	}
	if a.Config.SetupToken == "" || subtle.ConstantTimeCompare([]byte(c.PostForm("setup_token")), []byte(a.Config.SetupToken)) != 1 {
		a.html(c, http.StatusForbidden, "setup-unlock.html", gin.H{"Error": "That setup code doesn't match. Check the code and try again."})
		return
	}
	session, err := a.Store.Get(c.Request, sessionName)
	if err == nil {
		session.Values[setupSessionKey] = setupTokenHash(a.Config.SetupToken)
		err = session.Save(c.Request, c.Writer)
	}
	if err != nil {
		c.String(http.StatusInternalServerError, "Setup isn't available right now. Try again in a moment.")
		return
	}
	c.Redirect(http.StatusSeeOther, "/setup")
}

func (a *App) createFirstAdmin(ctx context.Context, admin *models.User, in setupInput) error {
	return a.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SELECT pg_advisory_xact_lock(?)", setupLockID).Error; err != nil {
			return err
		}
		var count int64
		if err := tx.Model(&models.User{}).Count(&count).Error; err != nil {
			return err
		}
		if count != 0 {
			return errSetupComplete
		}
		if err := tx.Create(admin).Error; err != nil {
			return err
		}
		for _, setting := range []models.Setting{{Key: "site_name", Value: in.SiteName}, {Key: settingTimezone, Value: in.Timezone}} {
			if err := refuseRestoreFenceKey(setting.Key); err != nil {
				return err
			}
			if err := tx.Where(models.Setting{Key: setting.Key}).Assign(models.Setting{Value: setting.Value}).FirstOrCreate(&models.Setting{}).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// Setup preserves non-secret answers, but never either password. The field
// order is shared by the summary and native autofocus on the first error.
type setupInput struct {
	SiteName, Timezone, Name, Email string
	Password, Confirm               string
	Adult                           bool
}

type setupFieldError struct {
	ID      string
	Message string
}

func readSetupInput(c *gin.Context) setupInput {
	return setupInput{
		SiteName: strings.TrimSpace(c.PostForm("site_name")),
		Timezone: strings.TrimSpace(c.PostForm("timezone")),
		Name:     strings.TrimSpace(c.PostForm("name")),
		Email:    normalizeEmail(c.PostForm("email")),
		Password: c.PostForm("password"),
		Confirm:  c.PostForm("password_confirm"),
		Adult:    c.PostForm("age_band") == "adult",
	}
}

func (in setupInput) fieldErrors() map[string]string {
	out := map[string]string{}
	if in.SiteName == "" {
		out["site_name"] = "Enter your church name."
	}
	if in.Timezone == "" {
		out["timezone"] = "Choose your church's time zone."
	} else if _, err := events.LoadZone(in.Timezone); err != nil {
		out["timezone"] = "Choose a suggested time zone, for example America/Denver."
	}
	if in.Name == "" {
		out["name"] = "Enter your name."
	}
	if in.Email == "" {
		out["email"] = "Enter your email."
	} else if !validEmail(in.Email) {
		out["email"] = "Enter a valid email address."
	}
	if in.Password == "" {
		out["password"] = "Enter a password."
	} else if verdict := passwordLength(in.Password); verdict == passwordTooShort {
		out["password"] = fmt.Sprintf("Use at least %d characters.", minPasswordLength)
	} else if verdict == passwordTooLong {
		out["password"] = fmt.Sprintf("Use at most %d bytes (accents and emoji take more).", maxPasswordBytes)
	}
	if in.Confirm == "" {
		out["password_confirm"] = "Enter your password again."
	} else if in.Password != in.Confirm {
		out["password_confirm"] = "Enter the same password in both fields."
	}
	if !in.Adult {
		out["age_band"] = "Confirm that you are 18 or older. The first administrator must be an adult."
	}
	return out
}

func (in setupInput) pageData(fields map[string]string, message string) gin.H {
	var summary []setupFieldError
	first := ""
	for _, id := range []string{"site_name", "timezone", "name", "email", "password", "password_confirm", "age_band"} {
		if text := fields[id]; text != "" {
			if first == "" {
				first = id
			}
			summary = append(summary, setupFieldError{ID: id, Message: text})
		}
	}
	return gin.H{
		"Error": message, "FieldErrors": fields, "ErrorSummary": summary, "FirstError": first,
		"EnteredSiteName": in.SiteName, "EnteredTimezone": in.Timezone,
		"EnteredName": in.Name, "EnteredEmail": in.Email,
		"AdultConfirmed": in.Adult, "PasswordMinimum": minPasswordLength, "PasswordMaximum": maxPasswordBytes,
	}
}
