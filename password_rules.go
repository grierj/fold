package main

import (
	"net/http"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
)

// minPasswordLength is the minimum for local-account passwords, enforced at
// setup, admin creation, password change, and admin password reset. The setup
// template states the requirement next to the field: keep them in sync.
const minPasswordLength = 10

// maxPasswordBytes is the longest password bcrypt accepts: it refuses more
// than 72 bytes (ErrPasswordTooLong) rather than truncating. The limit is in
// bytes, not characters, so accents and emoji use it up faster. The setup
// template and the SPA state it next to every new-password field; keep them
// in sync. Sign-in is unchanged: it never hashes a new password.
const maxPasswordBytes = 72

// Password length verdicts. The minimum counts characters (runes), matching
// the copy beside every field; the maximum counts bytes (bcrypt's limit).
const (
	passwordLengthOK = iota
	passwordTooShort
	passwordTooLong
)

// passwordLength is the one place the length rules are decided. Setup shows
// its own wording but takes its verdict from here. It never trims or logs.
func passwordLength(pw string) int {
	switch {
	case utf8.RuneCountInString(pw) < minPasswordLength:
		return passwordTooShort
	case len(pw) > maxPasswordBytes:
		return passwordTooLong
	}
	return passwordLengthOK
}

// The messages passwordRuleError sends. api_errors.go keys its error codes on
// them, so they are written out here; TestPasswordMessagesStateTheLimits keeps
// the numbers in them equal to the limits above.
const (
	errPasswordTooShort = "password must be at least 10 characters"
	errPasswordTooLong  = "password must be at most 72 bytes"
)

// passwordRuleError returns the message for a new password that breaks the
// length rules, or "" when it is fine.
func passwordRuleError(pw string) string {
	switch passwordLength(pw) {
	case passwordTooShort:
		return errPasswordTooShort
	case passwordTooLong:
		return errPasswordTooLong
	}
	return ""
}

// checkNewPassword answers 400 and returns false when pw breaks the length
// rules. Every handler that sets a password calls it before hashing, so a
// too-long password is a clear 400 instead of a bcrypt 500.
func checkNewPassword(c *gin.Context, pw string) bool {
	if msg := passwordRuleError(pw); msg != "" {
		jsonError(c, http.StatusBadRequest, msg)
		return false
	}
	return true
}
