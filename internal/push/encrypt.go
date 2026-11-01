package push

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
)

// RFC 8291 message encryption: one aes128gcm record (RFC 8188) keyed by an
// ECDH agreement between a fresh sender key and the browser's p256dh key,
// mixed with the browser's 16-byte auth secret.

// recordSize is the rs field of the aes128gcm header. One record holds
// the whole message: MaxPlaintext plus the delimiter and tag fit in it.
const recordSize = 4096

// errBadKeys: the subscription's p256dh or auth isn't a valid key. The
// message names neither value.
var errBadKeys = errors.New("push: the device's keys are invalid")

// decodeKey reads a base64url value with or without padding, as browsers
// and libraries send both.
func decodeKey(s string) ([]byte, error) {
	if b, err := base64.RawURLEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	return base64.URLEncoding.DecodeString(s)
}

// Encrypt encrypts plaintext for the subscription's keys with a fresh
// sender key and salt, returning the request body.
func Encrypt(plaintext []byte, p256dh, auth string) ([]byte, error) {
	sender, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	return encrypt(plaintext, p256dh, auth, sender, salt)
}

// encrypt is Encrypt with the sender key and salt given, so the RFC 8291
// Appendix A example can be reproduced byte for byte.
func encrypt(plaintext []byte, p256dh, auth string, sender *ecdh.PrivateKey, salt []byte) ([]byte, error) {
	uaRaw, err := decodeKey(p256dh)
	if err != nil {
		return nil, errBadKeys
	}
	authSecret, err := decodeKey(auth)
	if err != nil || len(authSecret) != 16 {
		return nil, errBadKeys
	}
	uaPublic, err := ecdh.P256().NewPublicKey(uaRaw)
	if err != nil {
		return nil, errBadKeys
	}
	if len(salt) != 16 {
		return nil, errors.New("push: salt must be 16 bytes")
	}
	if len(plaintext)+1+16 > recordSize {
		return nil, ErrTooLarge
	}
	shared, err := sender.ECDH(uaPublic)
	if err != nil {
		return nil, errBadKeys
	}
	asPublic := sender.PublicKey().Bytes()
	cek, nonce, err := deriveKeys(shared, authSecret, salt, uaPublic.Bytes(), asPublic)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(cek)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	// The header: salt, record size, key id length, then the sender's
	// public key as the key id (RFC 8291 section 4).
	out := make([]byte, 0, 16+4+1+len(asPublic)+len(plaintext)+1+gcm.Overhead())
	out = append(out, salt...)
	out = binary.BigEndian.AppendUint32(out, recordSize)
	out = append(out, byte(len(asPublic)))
	out = append(out, asPublic...)
	// A single, last record: the plaintext, then the 0x02 delimiter and no
	// padding (RFC 8188 section 2).
	record := append(append(make([]byte, 0, len(plaintext)+1), plaintext...), 0x02)
	return gcm.Seal(out, nonce, record, nil), nil
}

// deriveKeys runs the two HKDF steps of RFC 8291 section 3.4 and the
// RFC 8188 content key and nonce derivation.
func deriveKeys(shared, authSecret, salt, uaPublic, asPublic []byte) (cek, nonce []byte, err error) {
	keyInfo := append(append([]byte("WebPush: info\x00"), uaPublic...), asPublic...)
	ikm, err := hkdf.Key(sha256.New, shared, authSecret, string(keyInfo), 32)
	if err != nil {
		return nil, nil, err
	}
	prk, err := hkdf.Extract(sha256.New, ikm, salt)
	if err != nil {
		return nil, nil, err
	}
	if cek, err = hkdf.Expand(sha256.New, prk, "Content-Encoding: aes128gcm\x00", 16); err != nil {
		return nil, nil, err
	}
	nonce, err = hkdf.Expand(sha256.New, prk, "Content-Encoding: nonce\x00", 12)
	return cek, nonce, err
}
