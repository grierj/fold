package push

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/url"
	"time"
)

// RFC 8292 VAPID: the application server identifies itself to the push
// service with an ES256 JWT and its public key.

// tokenLifetime is how long a VAPID token is valid. RFC 8292 caps it at 24
// hours; push services reject tokens near that limit when clocks drift.
const tokenLifetime = 12 * time.Hour

// GenerateKey makes a new VAPID key pair (P-256).
func GenerateKey() (*ecdsa.PrivateKey, error) {
	return ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
}

// PublicKey is the key in the form browsers take as applicationServerKey:
// the uncompressed point, base64url without padding.
func PublicKey(key *ecdsa.PrivateKey) (string, error) {
	b, err := key.PublicKey.Bytes()
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// MarshalPrivateKey is the raw 32-byte private scalar, for storing
// encrypted. ParsePrivateKey reads it back.
func MarshalPrivateKey(key *ecdsa.PrivateKey) ([]byte, error) { return key.Bytes() }

// ParsePrivateKey reads a key MarshalPrivateKey wrote.
func ParsePrivateKey(raw []byte) (*ecdsa.PrivateKey, error) {
	return ecdsa.ParseRawPrivateKey(elliptic.P256(), raw)
}

// audience is the origin of a push endpoint (the JWT's aud claim).
func audience(endpoint string) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", errors.New("push: the endpoint isn't a URL")
	}
	return u.Scheme + "://" + u.Host, nil
}

// vapidHeader is the Authorization header for endpoint, signed by key.
func vapidHeader(key *ecdsa.PrivateKey, endpoint, subject string, now time.Time) (string, error) {
	aud, err := audience(endpoint)
	if err != nil {
		return "", err
	}
	token, err := signToken(key, aud, subject, now.Add(tokenLifetime))
	if err != nil {
		return "", err
	}
	pub, err := PublicKey(key)
	if err != nil {
		return "", err
	}
	return "vapid t=" + token + ", k=" + pub, nil
}

// signToken is the compact ES256 JWT {aud, exp, sub}.
func signToken(key *ecdsa.PrivateKey, aud, sub string, exp time.Time) (string, error) {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"typ":"JWT","alg":"ES256"}`))
	claims, err := json.Marshal(struct {
		Aud string `json:"aud"`
		Exp int64  `json:"exp"`
		Sub string `json:"sub,omitempty"`
	}{aud, exp.Unix(), sub})
	if err != nil {
		return "", err
	}
	signing := header + "." + base64.RawURLEncoding.EncodeToString(claims)
	digest := sha256.Sum256([]byte(signing))
	r, s, err := ecdsa.Sign(rand.Reader, key, digest[:])
	if err != nil {
		return "", err
	}
	// JWS ES256 is r and s as two 32-byte big-endian numbers (RFC 7518 3.4).
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return signing + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}
