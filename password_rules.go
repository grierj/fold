package main

import (
	"fmt"
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

var (
	errPasswordTooShort = apiErr(http.StatusBadRequest, "password_too_short", fmt.Sprintf("password must be at least %d characters", minPasswordLength))
	errPasswordTooLong  = apiErr(http.StatusBadRequest, "password_too_long", fmt.Sprintf("password must be at most %d bytes", maxPasswordBytes))
)

func passwordRuleError(pw string) error {
	switch passwordLength(pw) {
	case passwordTooShort:
		return errPasswordTooShort
	case passwordTooLong:
		return errPasswordTooLong
	}
	return nil
}

// checkNewPassword answers 400 and returns false when pw breaks the length
// rules. Every handler that sets a password calls it before hashing, so a
// too-long password is a clear 400 instead of a bcrypt 500.
func checkNewPassword(c *gin.Context, pw string) bool {
	if err := passwordRuleError(pw); err != nil {
		fail(c, err)
		return false
	}
	return true
}
