package app

import (
	"crypto/rand"
	"encoding/base64"
	"net/http"

	"fold/internal/accounts"
	"fold/internal/keys"
)

// CSRF tokens are signed and stateless, so a stranger's visit writes nothing
// to the database. Signed out, the token is a MAC of a random cookie value.
// Signed in, it is a MAC of the session token's hash, so it is bound to the
// session and changes at every sign-in. Every POST must carry it.

const csrfField = "csrf"

func (a *App) ensureCSRFCookie(w http.ResponseWriter, r *http.Request) {
	if len(a.cookie(r, "csrf")) == 43 {
		return
	}
	var b [32]byte
	rand.Read(b[:])
	v := base64.RawURLEncoding.EncodeToString(b[:])
	a.setCookie(w, "csrf", v, 0)
	// Make the new value visible to this request's own render.
	r.AddCookie(&http.Cookie{Name: a.cookieName("csrf"), Value: v})
}

// csrfToken is the token this request's forms carry.
func (a *App) csrfToken(r *http.Request) string {
	var msg []byte
	if t := a.sessionToken(r); t != "" {
		msg = append([]byte("session:"), accounts.Hash(t)...)
	} else {
		nonce := a.cookie(r, "csrf")
		if nonce == "" {
			return ""
		}
		msg = []byte("visitor:" + nonce)
	}
	return base64.RawURLEncoding.EncodeToString(a.keys.MAC(keys.CSRF, msg))
}

func (a *App) checkCSRF(r *http.Request) bool {
	want := a.csrfToken(r)
	if want == "" {
		return false
	}
	return constantEqual(r.PostFormValue(csrfField), want)
}
