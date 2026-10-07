package main

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"html/template"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/sessions"
)

// This file implements CSRF protection via the synchronizer token pattern:
// a random token is stored server-side in the (HMAC-signed) session cookie
// and must be submitted with every state-changing request, either as the
// "csrf_token" form field or the "X-CSRF-Token" header. Unlike header-based
// checks, this works identically over plain HTTP (local dev) and HTTPS
// (behind a TLS-terminating proxy).

const csrfFormField = "csrf_token"
const csrfHeader = "X-CSRF-Token"
const csrfSessionKey = "csrfToken"

// csrfToken returns the session's CSRF token, generating and persisting one
// if needed. The caller must save the session afterwards when a new token
// was created (csrfMiddleware does this).
func csrfToken(s *sessions.Session) (string, bool) {
	if t, ok := s.Values[csrfSessionKey].(string); ok && t != "" {
		return t, false
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", false
	}
	t := base64.RawURLEncoding.EncodeToString(b)
	s.Values[csrfSessionKey] = t
	return t, true
}

// csrfField renders the hidden form field carrying the token.
func csrfField(token string) template.HTML {
	return template.HTML(fmt.Sprintf(
		`<input type="hidden" name="%s" value="%s">`,
		csrfFormField, template.HTMLEscapeString(token),
	))
}

func (a *App) csrfMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		session, err := a.Store.Get(c.Request, sessionName)
		if err != nil {
			a.csrfFail(c, http.StatusInternalServerError, "Session error")
			return
		}

		token, created := csrfToken(session)
		if token == "" {
			a.csrfFail(c, http.StatusInternalServerError, "Failed to generate CSRF token")
			return
		}
		if created {
			// Persist the new token so the cookie is set before any POST.
			if err := session.Save(c.Request, c.Writer); err != nil {
				a.csrfFail(c, http.StatusInternalServerError, "Session error")
				return
			}
		}
		c.Set("csrfToken", token)

		if !isSafeMethod(c.Request.Method) {
			submitted := c.PostForm(csrfFormField)
			if submitted == "" {
				submitted = c.GetHeader(csrfHeader)
			}
			if subtle.ConstantTimeCompare([]byte(submitted), []byte(token)) != 1 {
				a.csrfFail(c, http.StatusForbidden, "CSRF token invalid")
				return
			}
		}
		c.Next()
	}
}

// csrfFail writes JSON for the API and plain text for the setup form.
func (a *App) csrfFail(c *gin.Context, code int, msg string) {
	if strings.HasPrefix(c.Request.URL.Path, "/api/") {
		jsonError(c, code, msg)
	} else {
		c.String(code, msg)
	}
	c.Abort()
}

func isSafeMethod(m string) bool {
	switch m {
	case http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodTrace:
		return true
	}
	return false
}
