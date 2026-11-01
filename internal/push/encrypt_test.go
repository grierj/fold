package push

import (
	"bytes"
	"crypto/ecdh"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

// The RFC 8291 Appendix A example, copied from
// https://www.rfc-editor.org/rfc/rfc8291.txt (section 5 and Appendix A).
const (
	rfcPlaintext = "V2hlbiBJIGdyb3cgdXAsIEkgd2FudCB0byBiZSBhIHdhdGVybWVsb24"
	rfcASPublic  = "BP4z9KsN6nGRTbVYI_c7VJSPQTBtkgcy27mlmlMoZIIgDll6e3vCYLocInmYWAmS6TlzAC8wEqKK6PBru3jl7A8"
	rfcASPrivate = "yfWPiYE-n46HLnH0KqZOF1fJJU3MYrct3AELtAQ-oRw"
	rfcUAPublic  = "BCVxsr7N_eNgVRqvHtD0zTZsEc6-VV-JvLexhqUzORcxaOzi6-AYWXvTBHm4bjyPjs7Vd8pZGH6SRpkNtoIAiw4"
	rfcUAPrivate = "q1dXpw3UpT5VOmu_cf_v6ih07Aems3njxI-JWgLcM94"
	rfcSalt      = "DGv6ra1nlYgDCS1FRnbzlw"
	rfcAuth      = "BTBZMqHH6r4Tts7J_aSIgg"
	rfcCEK       = "oIhVW04MRdy2XN9CiKLxTg"
	rfcNonce     = "4h_95klXJ5E_qnoN"
	rfcBody      = "DGv6ra1nlYgDCS1FRnbzlwAAEABBBP4z9KsN6nGRTbVYI_c7VJSPQTBtkgcy27ml" +
		"mlMoZIIgDll6e3vCYLocInmYWAmS6TlzAC8wEqKK6PBru3jl7A_yl95bQpu6cVPT" +
		"pK4Mqgkf1CXztLVBSt2Ks3oZwbuwXPXLWyouBWLVWGNWQexSgSxsj_Qulcy4a-fN"
)

func b64(t *testing.T, s string) []byte {
	t.Helper()
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		t.Fatalf("decode %q: %v", s, err)
	}
	return b
}

// TestRFC8291AppendixA reproduces the RFC's example byte for byte: the
// intermediate CEK and nonce, and the whole request body.
func TestRFC8291AppendixA(t *testing.T) {
	sender, err := ecdh.P256().NewPrivateKey(b64(t, rfcASPrivate))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(sender.PublicKey().Bytes(), b64(t, rfcASPublic)) {
		t.Fatal("as_private does not give as_public")
	}
	ua, err := ecdh.P256().NewPrivateKey(b64(t, rfcUAPrivate))
	if err != nil {
		t.Fatal(err)
	}
	shared, err := sender.ECDH(ua.PublicKey())
	if err != nil {
		t.Fatal(err)
	}
	if got := base64.RawURLEncoding.EncodeToString(shared); got != "kyrL1jIIOHEzg3sM2ZWRHDRB62YACZhhSlknJ672kSs" {
		t.Fatalf("ecdh_secret %s", got)
	}
	cek, nonce, err := deriveKeys(shared, b64(t, rfcAuth), b64(t, rfcSalt), b64(t, rfcUAPublic), b64(t, rfcASPublic))
	if err != nil {
		t.Fatal(err)
	}
	if got := base64.RawURLEncoding.EncodeToString(cek); got != rfcCEK {
		t.Errorf("CEK %s, want %s", got, rfcCEK)
	}
	if got := base64.RawURLEncoding.EncodeToString(nonce); got != rfcNonce {
		t.Errorf("NONCE %s, want %s", got, rfcNonce)
	}
	body, err := encrypt(b64(t, rfcPlaintext), rfcUAPublic, rfcAuth, sender, b64(t, rfcSalt))
	if err != nil {
		t.Fatal(err)
	}
	if got := base64.RawURLEncoding.EncodeToString(body); got != rfcBody {
		t.Fatalf("body\n got %s\nwant %s", got, rfcBody)
	}
	// And the receiver's side, which the fake push service uses, reads it.
	plain, err := decrypt(body, ua, b64(t, rfcAuth))
	if err != nil || string(plain) != "When I grow up, I want to be a watermelon" {
		t.Fatalf("decrypt %q %v", plain, err)
	}
}

func TestEncryptRefusesBadKeys(t *testing.T) {
	for name, keys := range map[string][2]string{
		"p256dh not base64": {"!!", rfcAuth},
		"p256dh not a key":  {strings.Repeat("A", 87), rfcAuth},
		"auth too short":    {rfcUAPublic, "AAAA"},
	} {
		if _, err := Encrypt([]byte("hi"), keys[0], keys[1]); !errors.Is(err, errBadKeys) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// Padded base64url, which some browsers send, works.
	if _, err := Encrypt([]byte("hi"), rfcUAPublic+"=", rfcAuth+"=="); err != nil {
		t.Errorf("padded keys: %v", err)
	}
}

// A device's keys are checked when it subscribes, with the same rules
// Encrypt applies; the error names neither value.
func TestValidKeys(t *testing.T) {
	if err := ValidKeys(rfcUAPublic, rfcAuth); err != nil {
		t.Fatalf("the RFC's keys: %v", err)
	}
	padded := base64.URLEncoding.EncodeToString(b64(t, rfcUAPublic))
	if err := ValidKeys(padded, rfcAuth+"=="); err != nil {
		t.Fatalf("padded keys: %v", err)
	}
	for _, c := range [][2]string{
		{"", rfcAuth},
		{rfcUAPublic, ""},
		{"not base64!", rfcAuth},
		{rfcUAPublic[:40], rfcAuth},        // not a point
		{rfcUAPublic, rfcAuth[:10]},        // short secret
		{rfcUAPublic, rfcUAPublic},         // a key, not a secret
		{strings.Repeat("A", 87), rfcAuth}, // 65 bytes, not on the curve
	} {
		err := ValidKeys(c[0], c[1])
		if err == nil {
			t.Errorf("ValidKeys(%q, %q) accepted", c[0], c[1])
			continue
		}
		if (c[0] != "" && strings.Contains(err.Error(), c[0])) || (c[1] != "" && strings.Contains(err.Error(), c[1])) {
			t.Errorf("the error repeats a key: %v", err)
		}
	}
}
