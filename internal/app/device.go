package app

import (
	"encoding/base64"
	"net/http"
	"strconv"
	"strings"
	"time"

	"fold/internal/accounts"
	"fold/internal/keys"
)

// The same-device cookie (docs/DECISIONS.md R26). Asking for a sign-in link,
// or sending setup's test message, marks this browser for that address for
// as long as a link lasts. Opening the emailed link in a marked browser acts
// at once; anywhere else the link shows its button, so a mail scanner that
// opens it first cannot use it up. The cookie is a MAC of the address and
// the time, so it cannot be made outside the server or moved to another
// address, and it holds nothing personal in the clear.

func (a *App) deviceMAC(email string, at int64) string {
	msg := strconv.FormatInt(at, 10) + "|" + email
	return base64.RawURLEncoding.EncodeToString(a.keys.MAC(keys.Device, []byte(msg)))
}

// markDevice marks this browser as the one that asked for a link to email.
// It is set whether or not the address has an account, so it says nothing.
func (a *App) markDevice(w http.ResponseWriter, email string) {
	at := a.clock.Now().Unix()
	a.setCookie(w, "device", strconv.FormatInt(at, 10)+"."+a.deviceMAC(email, at), accounts.LinkLife)
}

// sameDevice reports whether this browser asked for a link to email within
// a link's lifetime.
func (a *App) sameDevice(r *http.Request, email string) bool {
	at, mac, ok := strings.Cut(a.cookie(r, "device"), ".")
	if !ok || email == "" {
		return false
	}
	t, err := strconv.ParseInt(at, 10, 64)
	if err != nil {
		return false
	}
	age := a.clock.Now().Sub(time.Unix(t, 0))
	if age < 0 || age > accounts.LinkLife {
		return false
	}
	return constantEqual(mac, a.deviceMAC(email, t))
}
