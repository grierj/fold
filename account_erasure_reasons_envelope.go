package main

// The DELETE body's reason review envelope and its strict reading (M7.4 T07B-2
// scaling, F2, R4, R6).
//
// Go's encoding/json matches struct fields without regard to case and lets the
// last duplicate key win, so decoding the envelope into a struct would accept a
// second "count", a "Count" or two "reasonReview" keys and silently pick one.
// The readers here walk the tokens instead: keys are compared as decoded text
// (so a key written with a unicode escape equals its plain spelling), byte for
// byte, each at most once, and every value is checked by its token type.
//
// The new envelope (format "set-v1") is four strings:
//
//	format  "set-v1"
//	count   canonical decimal uint64, 1 or more
//	digest  64 lowercase hex: the set digest (reasonSetHasher)
//	choices standard base64 with padding of exactly ceil(count/4) bytes
//
// Item i (0-based, ascending signed request id) has the two bits
// 2*(i mod 4) and the next one of byte i/4, least significant first. Code 1 is
// keep, 2 is remove; 0 (unchosen) and 3 are refused, so the vector never
// stands in for a choice nobody made.
//
// The old envelope {"items":[{requestId, reviewHash, choice}...]} is read only
// to be told apart from the new one and checked for shape; its entries are
// never applied (reason_review_outdated).

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"strconv"
	"strings"
)

type reasonEnvelopeKind int

const (
	// reasonEnvelopeAbsent: no reasonReview key, or JSON null.
	reasonEnvelopeAbsent reasonEnvelopeKind = iota
	// reasonEnvelopeNew: a well-formed set-v1 envelope.
	reasonEnvelopeNew
	// reasonEnvelopeLegacy: a well-formed old items envelope, never applied.
	reasonEnvelopeLegacy
)

const (
	reasonCodeKeep   = 1
	reasonCodeRemove = 2

	reasonSetFormat = "set-v1"
)

type reasonEnvelope struct {
	kind   reasonEnvelopeKind
	count  uint64
	digest [sha256.Size]byte
	// choices is the decoded vector (new kind only).
	choices []byte
}

func (e reasonEnvelope) choiceAt(i int) byte {
	return (e.choices[i/4] >> (2 * uint(i%4))) & 3
}

// parseReasonEnvelope reads the raw reasonReview value. ok is false for
// anything malformed, and always when ambiguous (the body named the key twice
// or in another case). The value is already one JSON value (a json.RawMessage),
// so nothing can trail it.
func parseReasonEnvelope(raw json.RawMessage, ambiguous bool) (reasonEnvelope, bool) {
	if ambiguous {
		return reasonEnvelope{}, false
	}
	if len(raw) == 0 || string(bytes.TrimSpace(raw)) == "null" {
		return reasonEnvelope{kind: reasonEnvelopeAbsent}, true
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return reasonEnvelope{}, false
	}
	seen := map[string]bool{}
	strs := map[string]string{}
	for dec.More() {
		kt, err := dec.Token()
		key, isString := kt.(string)
		if err != nil || !isString || seen[key] {
			return reasonEnvelope{}, false
		}
		seen[key] = true
		switch key {
		case "items":
			if !readLegacyItems(dec) {
				return reasonEnvelope{}, false
			}
		case "format", "count", "digest", "choices":
			vt, err := dec.Token()
			v, isString := vt.(string)
			if err != nil || !isString {
				return reasonEnvelope{}, false
			}
			strs[key] = v
		default:
			return reasonEnvelope{}, false
		}
	}
	if t, err := dec.Token(); err != nil || t != json.Delim('}') {
		return reasonEnvelope{}, false
	}
	if _, err := dec.Token(); err != io.EOF {
		return reasonEnvelope{}, false
	}
	if len(seen) == 1 && seen["items"] {
		return reasonEnvelope{kind: reasonEnvelopeLegacy}, true
	}
	if len(seen) != 4 || seen["items"] {
		return reasonEnvelope{}, false
	}
	return buildNewEnvelope(strs["format"], strs["count"], strs["digest"], strs["choices"])
}

// readLegacyItems checks the old envelope's array: every entry exactly
// requestId, reviewHash and choice as strings, a canonical id, a 64-character
// lowercase hash, a choice of keep or remove, and no id twice. An empty array
// is allowed, as the old reader allowed it. It reads the array's tokens and
// leaves the decoder after the closing bracket.
func readLegacyItems(dec *json.Decoder) bool {
	if t, err := dec.Token(); err != nil || t != json.Delim('[') {
		return false
	}
	ids := map[int64]struct{}{}
	for dec.More() {
		if t, err := dec.Token(); err != nil || t != json.Delim('{') {
			return false
		}
		fields := map[string]string{}
		for dec.More() {
			kt, err := dec.Token()
			key, isString := kt.(string)
			if err != nil || !isString {
				return false
			}
			if key != "requestId" && key != "reviewHash" && key != "choice" {
				return false
			}
			if _, dup := fields[key]; dup {
				return false
			}
			vt, err := dec.Token()
			v, isString := vt.(string)
			if err != nil || !isString {
				return false
			}
			fields[key] = v
		}
		if t, err := dec.Token(); err != nil || t != json.Delim('}') {
			return false
		}
		if len(fields) != 3 {
			return false
		}
		id, ok := parseRequestID(fields["requestId"])
		if !ok || !validReviewHash(fields["reviewHash"]) || (fields["choice"] != "keep" && fields["choice"] != "remove") {
			return false
		}
		if _, dup := ids[id]; dup {
			return false
		}
		ids[id] = struct{}{}
	}
	t, err := dec.Token()
	return err == nil && t == json.Delim(']')
}

func buildNewEnvelope(format, count, digest, choices string) (reasonEnvelope, bool) {
	if format != reasonSetFormat {
		return reasonEnvelope{}, false
	}
	n, err := strconv.ParseUint(count, 10, 64)
	if err != nil || n == 0 || strconv.FormatUint(n, 10) != count {
		return reasonEnvelope{}, false
	}
	if !validReviewHash(digest) { // 64 lowercase hex, as a review hash is
		return reasonEnvelope{}, false
	}
	var env reasonEnvelope
	env.kind, env.count = reasonEnvelopeNew, n
	if _, err := hex.Decode(env.digest[:], []byte(digest)); err != nil {
		return reasonEnvelope{}, false
	}
	// The strict decoder plus a re-encode refuses non-canonical padding, a
	// newline, the URL-safe alphabet and trailing bits.
	dec, err := base64.StdEncoding.Strict().DecodeString(choices)
	if err != nil || base64.StdEncoding.EncodeToString(dec) != choices {
		return reasonEnvelope{}, false
	}
	want := n / 4
	if n%4 != 0 {
		want++
	}
	if uint64(len(dec)) != want {
		return reasonEnvelope{}, false
	}
	env.choices = dec
	for i := uint64(0); i < n; i++ {
		if c := env.choiceAt(int(i)); c != reasonCodeKeep && c != reasonCodeRemove {
			return reasonEnvelope{}, false
		}
	}
	// Padding slots of the last byte are zero.
	if n%4 != 0 && dec[len(dec)-1]>>(2*uint(n%4)) != 0 {
		return reasonEnvelope{}, false
	}
	return env, true
}

// topKeys says whether the two top-level keys the review depends on were named
// unambiguously.
type topKeys struct {
	// attemptAmbiguous: attemptId appeared twice or in another case. No single
	// id exists to claim, so the body is refused before the claim.
	attemptAmbiguous bool
	// reviewAmbiguous: reasonReview appeared twice or in another case; the
	// envelope step refuses it.
	reviewAmbiguous bool
}

// scanTopKeys walks the top-level keys of the first JSON value of raw. A value
// that is not an object has no keys. Case-insensitivity follows encoding/json
// (strings.EqualFold is the comparison its field matching is defined by).
func scanTopKeys(raw []byte) topKeys {
	var out topKeys
	dec := json.NewDecoder(bytes.NewReader(raw))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return out
	}
	var attempts, reviews int
	for dec.More() {
		kt, err := dec.Token()
		key, isString := kt.(string)
		if err != nil || !isString {
			return out
		}
		switch {
		case key == "attemptId":
			attempts++
		case strings.EqualFold(key, "attemptId"):
			out.attemptAmbiguous = true
		case key == "reasonReview":
			reviews++
		case strings.EqualFold(key, "reasonReview"):
			out.reviewAmbiguous = true
		}
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			return out
		}
	}
	out.attemptAmbiguous = out.attemptAmbiguous || attempts > 1
	out.reviewAmbiguous = out.reviewAmbiguous || reviews > 1
	return out
}
