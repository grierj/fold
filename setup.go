package main

import (
	"fmt"
	"strings"

	"fold/internal/events"

	"github.com/gin-gonic/gin"
)

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
	} else if len(in.Password) < minPasswordLength {
		out["password"] = fmt.Sprintf("Use at least %d characters.", minPasswordLength)
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
		"AdultConfirmed": in.Adult, "PasswordMinimum": minPasswordLength,
	}
}
