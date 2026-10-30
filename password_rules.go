package main

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// maxPasswordBytes is the longest password bcrypt accepts: it refuses more
// than 72 bytes (ErrPasswordTooLong) rather than truncating. The limit is in
// bytes, not characters, so accents and emoji use it up faster. The setup
// template and the SPA state it next to every new-password field; keep them
// in sync. Sign-in is unchanged: it never hashes a new password.
const maxPasswordBytes = 72

// passwordRuleError returns the message for a new password that breaks the
// length rules, or "" when it is fine. It never trims or logs the password.
func passwordRuleError(pw string) string {
	switch {
	case len(pw) < minPasswordLength:
		return "password must be at least 10 characters"
	case len(pw) > maxPasswordBytes:
		return "password must be at most 72 bytes"
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
