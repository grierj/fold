// Package keys turns the server's one secret into a separate key per purpose,
// and seals small secrets (mail passwords, a link waiting to be emailed) so
// they are never stored in the clear.
//
// The secret comes from SECRET_KEY in the church's .env file, outside the
// data volume, so a copy of the data directory or a backup does not carry the
// key that opens it. SECRET_KEY_PREVIOUS lets the secret be rotated: sealed
// values are opened with either and resealed with the current one on start
// (docs/OPERATIONS.md, "Rotating the secret key").
package keys

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
)

// Purpose names one use of a key. A new purpose gets its own name; a key is
// never shared between two.
type Purpose string

const (
	CSRF     Purpose = "csrf/v1"
	Settings Purpose = "settings/v1"
	Jobs     Purpose = "jobs/v1"
	Setup    Purpose = "setup/v1"
)

// MinSecretBytes is the shortest secret accepted: 32 random bytes, written as
// 64 hex characters (openssl rand -hex 32).
const MinSecretBytes = 32

type Keys struct {
	current  []byte
	previous []byte // nil unless rotating
}

// ErrNoOpen is a sealed value neither key opens.
var ErrNoOpen = errors.New("keys: the sealed value does not open with this secret")

// Parse reads a hex secret, and an optional previous one.
func Parse(currentHex, previousHex string) (*Keys, error) {
	cur, err := decode(currentHex)
	if err != nil {
		return nil, fmt.Errorf("SECRET_KEY: %w", err)
	}
	k := &Keys{current: cur}
	if previousHex != "" {
		prev, err := decode(previousHex)
		if err != nil {
			return nil, fmt.Errorf("SECRET_KEY_PREVIOUS: %w", err)
		}
		k.previous = prev
	}
	return k, nil
}

// FromBytes is for tests and dev.
func FromBytes(secret []byte) *Keys { return &Keys{current: secret} }

func decode(s string) ([]byte, error) {
	b, err := hex.DecodeString(s)
	if err != nil {
		return nil, errors.New("must be hex (make one with: openssl rand -hex 32)")
	}
	if len(b) < MinSecretBytes {
		return nil, fmt.Errorf("must be at least %d bytes (%d hex characters)", MinSecretBytes, 2*MinSecretBytes)
	}
	return b, nil
}

func derive(secret []byte, p Purpose) []byte {
	k, err := hkdf.Key(sha256.New, secret, nil, string(p), 32)
	if err != nil {
		panic(err) // only for an impossible length
	}
	return k
}

// MAC is HMAC-SHA256 under the purpose key.
func (k *Keys) MAC(p Purpose, msg []byte) []byte {
	m := hmac.New(sha256.New, derive(k.current, p))
	m.Write(msg)
	return m.Sum(nil)
}

// CheckMAC accepts a MAC made with the current key or, while rotating, the
// previous one.
func (k *Keys) CheckMAC(p Purpose, msg, mac []byte) bool {
	if hmac.Equal(mac, k.MAC(p, msg)) {
		return true
	}
	if k.previous == nil {
		return false
	}
	m := hmac.New(sha256.New, derive(k.previous, p))
	m.Write(msg)
	return hmac.Equal(mac, m.Sum(nil))
}

const sealVersion = 1

// Seal encrypts plaintext with AES-256-GCM under the purpose key. The purpose
// is also bound as associated data.
func (k *Keys) Seal(p Purpose, plaintext []byte) []byte {
	aead := newAEAD(derive(k.current, p))
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		panic(err)
	}
	out := append([]byte{sealVersion}, nonce...)
	return aead.Seal(out, nonce, plaintext, []byte(p))
}

// Open reverses Seal. It reports whether the previous key was needed, so the
// caller can reseal.
func (k *Keys) Open(p Purpose, sealed []byte) (plain []byte, usedPrevious bool, err error) {
	if out, err := open(derive(k.current, p), p, sealed); err == nil {
		return out, false, nil
	}
	if k.previous != nil {
		if out, err := open(derive(k.previous, p), p, sealed); err == nil {
			return out, true, nil
		}
	}
	return nil, false, ErrNoOpen
}

func open(key []byte, p Purpose, sealed []byte) ([]byte, error) {
	aead := newAEAD(key)
	if len(sealed) < 1+aead.NonceSize() || sealed[0] != sealVersion {
		return nil, ErrNoOpen
	}
	nonce := sealed[1 : 1+aead.NonceSize()]
	return aead.Open(nil, nonce, sealed[1+aead.NonceSize():], []byte(p))
}

func newAEAD(key []byte) cipher.AEAD {
	block, err := aes.NewCipher(key)
	if err != nil {
		panic(err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		panic(err)
	}
	return aead
}

// Rotating reports whether a previous secret is set.
func (k *Keys) Rotating() bool { return k.previous != nil }
