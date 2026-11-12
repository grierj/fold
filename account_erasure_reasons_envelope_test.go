package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The shared digest vectors: this suite and web/src/lib/reasonSetDigest.test.ts
// read the same file, so the two sides cannot drift. The digests were computed
// independently of production code and cross-checked with sha256sum over the
// preimage.
const reasonSetVectorsPath = "web/src/lib/reasonSetVectors.json"

type reasonSetVector struct {
	Name          string   `json:"name"`
	IDs           []string `json:"ids"`
	Hashes        []string `json:"hashes"`
	Choices       []int    `json:"choices"`
	ChoicesBase64 string   `json:"choicesBase64"`
	PreimageHex   string   `json:"preimageHex"`
	Digest        string   `json:"digest"`
}

func loadReasonSetVectors(t *testing.T) []reasonSetVector {
	t.Helper()
	b, err := os.ReadFile(reasonSetVectorsPath)
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Vectors []reasonSetVector `json:"vectors"`
	}
	if err := json.Unmarshal(b, &f); err != nil || len(f.Vectors) == 0 {
		t.Fatalf("vectors: %v (%d)", err, len(f.Vectors))
	}
	return f.Vectors
}

func TestReasonSetDigestVectors(t *testing.T) {
	for _, v := range loadReasonSetVectors(t) {
		t.Run(v.Name, func(t *testing.T) {
			n := len(v.IDs)
			if len(v.Hashes) != n || len(v.Choices) != n {
				t.Fatalf("vector %q is malformed", v.Name)
			}
			pre, err := hex.DecodeString(v.PreimageHex)
			if err != nil || len(pre) != 35+40*n {
				t.Fatalf("preimage is %d bytes, want 35 + 40n = %d (%v)", len(pre), 35+40*n, err)
			}
			if !bytes.HasPrefix(pre, []byte("fold:erasure-reason-set:v1\x00")) || len(reasonSetPrefix) != 26 {
				t.Error("the preimage must start with the 26-byte prefix and a zero byte")
			}
			h := newReasonSetHasher(uint64(n))
			var prev int64
			for i, s := range v.IDs {
				id, ok := parseRequestID(s)
				if !ok || (i > 0 && id <= prev) {
					t.Fatalf("id %q is not canonical or not ascending", s)
				}
				prev = id
				raw, err := hex.DecodeString(v.Hashes[i])
				if err != nil || len(raw) != 32 {
					t.Fatal("bad vector hash")
				}
				var a [32]byte
				copy(a[:], raw)
				h.add(id, &a)
			}
			sum := h.sum()
			if got := hex.EncodeToString(sum[:]); got != v.Digest {
				t.Errorf("digest %s, want %s", got, v.Digest)
			}
			// The same bytes through the envelope reader: every choice code.
			env, ok := buildNewEnvelope(reasonSetFormat, strconv.Itoa(n), v.Digest, v.ChoicesBase64)
			if !ok {
				t.Fatal("the vector's envelope was refused")
			}
			for i, want := range v.Choices {
				if got := int(env.choiceAt(i)); got != want {
					t.Errorf("choice %d is %d, want %d", i, got, want)
				}
			}
			// The test-side builder (testhelpers_test.go) writes the same envelope.
			var entries []reviewEntry
			for i, s := range v.IDs {
				id, _ := parseRequestID(s)
				c := "keep"
				if v.Choices[i] == 2 {
					c = "remove"
				}
				entries = append(entries, reviewEntry{ID: id, Hash: v.Hashes[i], Choice: c})
			}
			if m := envelopeOf(entries); m["digest"] != v.Digest || m["choices"] != v.ChoicesBase64 {
				t.Errorf("envelopeOf = %v", m)
			}
		})
	}
}

// generatedReasonVector is a large vector that the shared file describes
// instead of listing (see its generatedNote): web/src/lib/reasonSetDigest.test.ts
// builds the same review from the same fields. Item i has id FirstID + i*Step,
// 32 hash bytes from one linear congruential byte stream, and choice code 2
// when i mod 3 is 1, else 1.
type generatedReasonVector struct {
	Name          string `json:"name"`
	Count         int    `json:"count"`
	FirstID       string `json:"firstId"`
	Step          string `json:"step"`
	HashSeed      uint32 `json:"hashSeed"`
	Digest        string `json:"digest"`
	ChoicesSha256 string `json:"choicesSha256"`
}

func loadGeneratedReasonVectors(t *testing.T) []generatedReasonVector {
	t.Helper()
	b, err := os.ReadFile(reasonSetVectorsPath)
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Generated []generatedReasonVector `json:"generated"`
	}
	if err := json.Unmarshal(b, &f); err != nil || len(f.Generated) == 0 {
		t.Fatalf("generated vectors: %v (%d)", err, len(f.Generated))
	}
	return f.Generated
}

// The counts where a chunked or size-limited digest could differ from a whole
// one: a 1 MiB chunk holds 26,214 whole 40-byte records, and WebCrypto takes a
// preimage up to 16 MiB. Both are derived here from the wire format, so a
// vector file that drops a boundary fails.
func requiredGeneratedReasonCounts() []int {
	perChunk := (1 << 20) / 40
	lastWebCrypto := ((16 << 20) - 35) / 40
	return []int{perChunk - 1, perChunk, perChunk + 1, perChunk + 2, 2 * perChunk, 2*perChunk + 1, lastWebCrypto, lastWebCrypto + 1}
}

func TestReasonSetGeneratedVectors(t *testing.T) {
	vectors := loadGeneratedReasonVectors(t)
	have := map[int]bool{}
	for _, v := range vectors {
		have[v.Count] = true
	}
	for _, n := range requiredGeneratedReasonCounts() {
		if !have[n] {
			t.Errorf("the shared file has no generated vector with %d reasons", n)
		}
	}
	for _, v := range vectors {
		t.Run(v.Name, func(t *testing.T) {
			first, err1 := strconv.ParseInt(v.FirstID, 10, 64)
			step, err2 := strconv.ParseInt(v.Step, 10, 64)
			if err1 != nil || err2 != nil || step <= 0 || v.Count <= 0 {
				t.Fatalf("vector %q is malformed", v.Name)
			}
			if last := new(big.Int).Add(big.NewInt(first), new(big.Int).Mul(big.NewInt(step), big.NewInt(int64(v.Count-1)))); !last.IsInt64() {
				t.Fatalf("the last id of %q overflows int64", v.Name)
			}
			h := newReasonSetHasher(uint64(v.Count))
			packed := make([]byte, (v.Count+3)/4)
			x, id := v.HashSeed, first
			for i := 0; i < v.Count; i++ {
				var raw [sha256.Size]byte
				for b := range raw {
					x = x*1664525 + 1013904223
					raw[b] = byte(x >> 24)
				}
				h.add(id, &raw)
				code := byte(reasonCodeKeep)
				if i%3 == 1 {
					code = reasonCodeRemove
				}
				packed[i/4] |= code << (2 * uint(i%4))
				if i < v.Count-1 {
					id += step
				}
			}
			sum := h.sum()
			if got := hex.EncodeToString(sum[:]); got != v.Digest {
				t.Errorf("digest %s, want %s", got, v.Digest)
			}
			choices := base64.StdEncoding.EncodeToString(packed)
			cs := sha256.Sum256([]byte(choices))
			if got := hex.EncodeToString(cs[:]); got != v.ChoicesSha256 {
				t.Errorf("choices digest %s, want %s", got, v.ChoicesSha256)
			}
			env, ok := buildNewEnvelope(reasonSetFormat, strconv.Itoa(v.Count), v.Digest, choices)
			if !ok || env.count != uint64(v.Count) {
				t.Fatalf("the vector's envelope was refused (ok %v)", ok)
			}
			for i := 0; i < v.Count; i++ {
				want := byte(reasonCodeKeep)
				if i%3 == 1 {
					want = reasonCodeRemove
				}
				if got := env.choiceAt(i); got != want {
					t.Fatalf("choice %d is %d, want %d", i, got, want)
				}
			}
		})
	}
}

const (
	envDigest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	envHash   = "ab01ab01ab01ab01ab01ab01ab01ab01ab01ab01ab01ab01ab01ab01ab01ab01"
)

func envJSON(format, count, digest, choices string) string {
	return fmt.Sprintf(`{"format":%q,"count":%q,"digest":%q,"choices":%q}`, format, count, digest, choices)
}

func TestReasonEnvelopeStrict(t *testing.T) {
	good := envJSON("set-v1", "1", envDigest, "AQ==")
	// five items: 0x55 is four keeps, 0x01 one more in slot 0.
	five := envJSON("set-v1", "5", envDigest, "VQE=")
	b64 := func(b ...byte) string { return base64.StdEncoding.EncodeToString(b) }
	legacyItem := func(id, hash, choice string) string {
		return fmt.Sprintf(`{"requestId":%q,"reviewHash":%q,"choice":%q}`, id, hash, choice)
	}
	cases := []struct {
		name string
		raw  string
		kind reasonEnvelopeKind
		ok   bool
	}{
		{"valid new", good, reasonEnvelopeNew, true},
		{"valid new, five items", five, reasonEnvelopeNew, true},
		{"absent", ``, reasonEnvelopeAbsent, true},
		{"null", `null`, reasonEnvelopeAbsent, true},

		{"unknown key", `{"format":"set-v1","count":"1","digest":"` + envDigest + `","choices":"AQ==","extra":"x"}`, 0, false},
		{"missing key", `{"format":"set-v1","count":"1","digest":"` + envDigest + `"}`, 0, false},
		{"case variant Count", `{"format":"set-v1","Count":"1","digest":"` + envDigest + `","choices":"AQ=="}`, 0, false},
		{"case variant COUNT", `{"format":"set-v1","COUNT":"1","digest":"` + envDigest + `","choices":"AQ=="}`, 0, false},
		{"case variant Format", `{"Format":"set-v1","count":"1","digest":"` + envDigest + `","choices":"AQ=="}`, 0, false},
		{"case variant Digest", `{"format":"set-v1","count":"1","Digest":"` + envDigest + `","choices":"AQ=="}`, 0, false},
		{"case variant Choices", `{"format":"set-v1","count":"1","digest":"` + envDigest + `","Choices":"AQ=="}`, 0, false},
		{"case variant Items", `{"Items":[]}`, 0, false},
		{"duplicate count", `{"format":"set-v1","count":"1","count":"1","digest":"` + envDigest + `","choices":"AQ=="}`, 0, false},
		{"duplicate items", `{"items":[],"items":[]}`, 0, false},
		{"null count", `{"format":"set-v1","count":null,"digest":"` + envDigest + `","choices":"AQ=="}`, 0, false},
		{"number count", `{"format":"set-v1","count":1,"digest":"` + envDigest + `","choices":"AQ=="}`, 0, false},
		{"array digest", `{"format":"set-v1","count":"1","digest":["` + envDigest + `"],"choices":"AQ=="}`, 0, false},
		{"object choices", `{"format":"set-v1","count":"1","digest":"` + envDigest + `","choices":{}}`, 0, false},
		{"boolean format", `{"format":true,"count":"1","digest":"` + envDigest + `","choices":"AQ=="}`, 0, false},
		{"another format", envJSON("set-v2", "1", envDigest, "AQ=="), 0, false},
		{"mixed legacy and new", `{"items":[],"format":"set-v1","count":"1","digest":"` + envDigest + `","choices":"AQ=="}`, 0, false},
		{"empty object", `{}`, 0, false},
		{"items null", `{"items":null}`, 0, false},
		{"items an object", `{"items":{}}`, 0, false},
		{"an array", `[]`, 0, false},
		{"a string", `"set-v1"`, 0, false},
		{"a number", `7`, 0, false},
		{"trailing data after the object", good + ` x`, 0, false},

		{"count empty", envJSON("set-v1", "", envDigest, "AQ=="), 0, false},
		{"count 0", envJSON("set-v1", "0", envDigest, "AQ=="), 0, false},
		{"count 01", envJSON("set-v1", "01", envDigest, "AQ=="), 0, false},
		{"count +1", envJSON("set-v1", "+1", envDigest, "AQ=="), 0, false},
		{"count -1", envJSON("set-v1", "-1", envDigest, "AQ=="), 0, false},
		{"count with a space", envJSON("set-v1", " 1", envDigest, "AQ=="), 0, false},
		{"count 1e3", envJSON("set-v1", "1e3", envDigest, "AQ=="), 0, false},
		{"count 2^64", envJSON("set-v1", "18446744073709551616", envDigest, "AQ=="), 0, false},
		{"count 2^40 with one byte", envJSON("set-v1", "1099511627776", envDigest, "AQ=="), 0, false},
		{"count above 4 x length", envJSON("set-v1", "5", envDigest, "AQ=="), 0, false},
		{"count below the length", envJSON("set-v1", "1", envDigest, b64(1, 1)), 0, false},

		{"digest upper case", envJSON("set-v1", "1", strings.ToUpper(envDigest), "AQ=="), 0, false},
		{"digest short", envJSON("set-v1", "1", envDigest[:62], "AQ=="), 0, false},
		{"digest not hex", envJSON("set-v1", "1", strings.Repeat("g", 64), "AQ=="), 0, false},

		{"choices without padding", envJSON("set-v1", "1", envDigest, "AQ"), 0, false},
		{"choices with a newline", envJSON("set-v1", "1", envDigest, "AQ==\n"), 0, false},
		{"choices url-safe alphabet", envJSON("set-v1", "1", envDigest, "_w=="), 0, false},
		{"choices with trailing bits", envJSON("set-v1", "1", envDigest, "AR=="), 0, false},
		{"choices of the wrong length", envJSON("set-v1", "1", envDigest, "AQEB"), 0, false},
		{"choices empty", envJSON("set-v1", "1", envDigest, ""), 0, false},
		{"code 0 (unchosen)", envJSON("set-v1", "1", envDigest, b64(0)), 0, false},
		{"code 3", envJSON("set-v1", "1", envDigest, b64(3)), 0, false},
		{"code 0 in the second slot", envJSON("set-v1", "2", envDigest, b64(0x01)), 0, false},
		{"nonzero padding slot", envJSON("set-v1", "1", envDigest, b64(0x05)), 0, false},

		{"legacy empty items", `{"items":[]}`, reasonEnvelopeLegacy, true},
		{"legacy with an item", `{"items":[` + legacyItem("-5", envHash, "remove") + `]}`, reasonEnvelopeLegacy, true},
		{"legacy entries are not read", `{"items":[{"anything":1},"x",null]}`, reasonEnvelopeLegacy, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env, ok := parseReasonEnvelope(json.RawMessage(tc.raw), false)
			if ok != tc.ok || (ok && env.kind != tc.kind) {
				t.Fatalf("parse = kind %d ok %v, want kind %d ok %v", env.kind, ok, tc.kind, tc.ok)
			}
			// An ambiguous key refuses any value.
			if _, ok := parseReasonEnvelope(json.RawMessage(tc.raw), true); ok {
				t.Error("an ambiguous reasonReview key was accepted")
			}
		})
	}
	// The five-item envelope decodes to its codes.
	env, _ := parseReasonEnvelope(json.RawMessage(five), false)
	if env.count != 5 || env.choiceAt(0) != 1 || env.choiceAt(4) != 1 {
		t.Errorf("five-item envelope %+v", env)
	}
}

func TestReasonEnvelopeTopKeys(t *testing.T) {
	for _, tc := range []struct {
		name, raw       string
		attempt, review bool
	}{
		{"clean", `{"attemptId":"a","reasonReview":null,"successors":{}}`, false, false},
		{"empty", ``, false, false},
		{"not an object", `[1]`, false, false},
		{"attemptId twice", `{"attemptId":"a","attemptId":"b"}`, true, false},
		{"attemptId in another case", `{"attemptid":"a"}`, true, false},
		{"attemptId in another case beside the exact key", `{"attemptId":"a","ATTEMPTID":"b"}`, true, false},
		{"reasonReview twice", `{"reasonReview":null,"reasonReview":{}}`, false, true},
		{"reasonReview in another case", `{"reasonreview":{}}`, false, true},
		{"a nested copy of the key is not a top-level key", `{"successors":{"attemptId":"x","attemptId":"y"},"reasonReview":null}`, false, false},
		{"trailing data after the object", `{"attemptId":"a"} {"attemptId":"b"}`, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := scanTopKeys([]byte(tc.raw))
			if got.attemptAmbiguous != tc.attempt || got.reviewAmbiguous != tc.review {
				t.Errorf("scanTopKeys = %+v, want attempt %v review %v", got, tc.attempt, tc.review)
			}
		})
	}
}

// Whatever the reader accepts has exact, unique keys and re-encodes to the
// structure it read.
func FuzzReasonEnvelope(f *testing.F) {
	f.Add(envJSON("set-v1", "1", envDigest, "AQ=="))
	f.Add(`{"items":[]}`)
	f.Add(`{"format":"set-v1","count":"1","count":"1","digest":"x","choices":"AQ=="}`)
	f.Add(`{"count":"1"}`)
	f.Add(jsonEsc(`{"c#u006funt":"1"}`))
	f.Add(jsonEsc(`{"#u0069tems":[],"items":[]}`))
	f.Add(jsonEsc(`{"c#qount":"1"}`))
	// Boundary counts around the byte (4), the padding slots and the largest
	// counts a 64-bit count allows, each with a right-sized vector and then
	// with its padding bits, its code and its length spoiled.
	for _, n := range []int{1, 2, 3, 4, 5, 7, 8, 9, 15, 16, 17} {
		f.Add(envJSON("set-v1", strconv.Itoa(n), envDigest, base64.StdEncoding.EncodeToString(fuzzChoices(n, 0x55))))
		f.Add(envJSON("set-v1", strconv.Itoa(n), envDigest, base64.StdEncoding.EncodeToString(fuzzChoices(n, 0xaa))))
		f.Add(envJSON("set-v1", strconv.Itoa(n), envDigest, base64.StdEncoding.EncodeToString(append(fuzzChoices(n, 0x55), 0))))
		f.Add(envJSON("set-v1", strconv.Itoa(n), envDigest, base64.StdEncoding.EncodeToString(fuzzChoices(n, 0xff))))
	}
	for _, n := range []string{"9223372036854775807", "9223372036854775808", "18446744073709551615", "18446744073709551616", "4294967296", "4294967295"} {
		f.Add(envJSON("set-v1", n, envDigest, "AQ=="))
	}
	f.Add("  " + envJSON("set-v1", "1", envDigest, "AQ==") + "\n")
	f.Add(envJSON("set-v1", "1", envDigest, "AQ==") + envJSON("set-v1", "1", envDigest, "AQ=="))
	f.Add("\ufeff" + envJSON("set-v1", "1", envDigest, "AQ=="))
	f.Add(`{"items":[{"requestId":"-9223372036854775808","reviewHash":"` + envHash + `","choice":"keep"}]}`)
	f.Fuzz(func(t *testing.T, raw string) {
		// A nested key never makes a top-level key ambiguous: the wrapper names
		// each of its two keys once, whatever the value holds.
		if json.Valid([]byte(raw)) {
			wrapped := []byte(`{"attemptId":"a","reasonReview":` + raw + `}`)
			if got := scanTopKeys(wrapped); got.attemptAmbiguous || got.reviewAmbiguous {
				t.Fatalf("a nested value made a top-level key ambiguous: %q", raw)
			}
		}
		scanTopKeys([]byte(raw))
		env, ok := parseReasonEnvelope(json.RawMessage(raw), false)
		if again, ok2 := parseReasonEnvelope(json.RawMessage(raw), false); ok2 != ok || again.kind != env.kind || again.count != env.count {
			t.Fatalf("parsing %q twice gave different answers", raw)
		}
		if _, accepted := parseReasonEnvelope(json.RawMessage(raw), true); accepted {
			t.Fatalf("an ambiguous reasonReview key was accepted for %q", raw)
		}
		if !ok {
			return
		}
		if env.kind == reasonEnvelopeAbsent {
			return
		}
		dec := json.NewDecoder(strings.NewReader(raw))
		if _, err := dec.Token(); err != nil {
			t.Fatalf("accepted a value with no first token: %q", raw)
		}
		seen := map[string]bool{}
		for dec.More() {
			kt, err := dec.Token()
			key, isString := kt.(string)
			if err != nil || !isString || seen[key] {
				t.Fatalf("accepted %q with a bad or repeated key", raw)
			}
			seen[key] = true
			var skip json.RawMessage
			if err := dec.Decode(&skip); err != nil {
				t.Fatalf("accepted %q whose value does not decode", raw)
			}
		}
		switch env.kind {
		case reasonEnvelopeNew:
			if len(seen) != 4 || !seen["format"] || !seen["count"] || !seen["digest"] || !seen["choices"] {
				t.Fatalf("accepted a new envelope with keys %v", seen)
			}
			if env.count == 0 || uint64(len(env.choices)) != (env.count+3)/4 {
				t.Fatalf("accepted count %d with %d bytes", env.count, len(env.choices))
			}
			for i := uint64(0); i < env.count; i++ {
				if c := env.choiceAt(int(i)); c != 1 && c != 2 {
					t.Fatalf("accepted code %d at %d", c, i)
				}
			}
			// Canonical form: what was accepted, written out again in the one
			// spelling the reader prefers, is accepted and means the same.
			canon := envJSON(reasonSetFormat, strconv.FormatUint(env.count, 10), hex.EncodeToString(env.digest[:]), base64.StdEncoding.EncodeToString(env.choices))
			back, ok := parseReasonEnvelope(json.RawMessage(canon), false)
			if !ok || back.kind != reasonEnvelopeNew || back.count != env.count || back.digest != env.digest || !bytes.Equal(back.choices, env.choices) {
				t.Fatalf("accepted %q but its canonical form %q was not read back as the same envelope", raw, canon)
			}
		case reasonEnvelopeLegacy:
			if len(seen) != 1 || !seen["items"] {
				t.Fatalf("accepted a legacy envelope with keys %v", seen)
			}
		}
	})
}

// fuzzChoices is the right length of choices vector for n items with every byte
// set to fill: 0x55 is all keeps, 0xaa all removes, 0xff the unused code 3, and
// any of them leaves the padding slots of the last byte set when n is not a
// multiple of 4.
func fuzzChoices(n int, fill byte) []byte {
	return bytes.Repeat([]byte{fill}, (n+3)/4)
}

// Keys written with JSON escapes. encoding/json decodes a key before the reader
// compares it, so an escaped spelling is the same key as the plain one: allowed
// once, a collision twice, and a case variant when it decodes to one.
//
// The escapes are real backslash sequences in the text the reader sees. They
// are written here with the placeholders "#u" (a backslash and u) and "#q" (a
// backslash and a quote) and expanded by jsonEsc, so that no editor, generator
// or raw-string rule can quietly turn them back into plain letters;
// TestReasonEnvelopeEscapesAreReal checks that they stayed escapes.

// jsonEsc expands "#u" into a backslash and u, and "#q" into a backslash and a quote.
func jsonEsc(s string) string {
	bs := string(rune(92))
	return strings.ReplaceAll(strings.ReplaceAll(s, "#u", bs+"u"), "#q", bs+string(rune(34)))
}

// escapedEnvelopeCases are the envelope cases written with escapes.
func escapedEnvelopeCases() []struct {
	name string
	raw  string
	kind reasonEnvelopeKind
	ok   bool
} {
	d, h := envDigest, envHash
	body := func(format, count, digest, choices string) string {
		return `{` + format + `,` + count + `,` + digest + `,` + choices + `}`
	}
	f, c, g, ch := `"format":"set-v1"`, `"count":"1"`, `"digest":"`+d+`"`, `"choices":"AQ=="`
	return []struct {
		name string
		raw  string
		kind reasonEnvelopeKind
		ok   bool
	}{
		// allowed: an escaped key is that key, once
		{"count written with an escape", body(f, `"c#u006funt":"1"`, g, ch), reasonEnvelopeNew, true},
		{"count written with an escaped first letter", body(f, `"#u0063ount":"1"`, g, ch), reasonEnvelopeNew, true},
		{"all four keys escaped", body(`"for#u006dat":"set-v1"`, `"#u0063ount":"1"`, `"dig#u0065st":"`+d+`"`, `"choi#u0063es":"AQ=="`), reasonEnvelopeNew, true},
		{"an escape in a value is decoded: count", body(f, `"count":"#u0031"`, g, ch), reasonEnvelopeNew, true},
		{"an escape in a value is decoded: format", body(`"format":"set#u002dv1"`, c, g, ch), reasonEnvelopeNew, true},
		{"an escape in a value is decoded: digest", body(f, c, `"digest":"#u0061`+d[1:]+`"`, ch), reasonEnvelopeNew, true},
		{"the old envelope's key escaped", `{"#u0069tems":[]}`, reasonEnvelopeLegacy, true},
		{"the old envelope's entry keys escaped", `{"items":[{"request#u0049d":"1","review#u0048ash":"` + h + `","choice":"keep"}]}`, reasonEnvelopeLegacy, true},

		// refused: two spellings of one key are a duplicate
		{"count plain, then escaped", body(f, `"count":"1","c#u006funt":"1"`, g, ch), 0, false},
		{"count escaped, then plain", body(f, `"c#u006funt":"1","count":"1"`, g, ch), 0, false},
		{"count escaped two ways", body(f, `"c#u006funt":"1","co#u0075nt":"1"`, g, ch), 0, false},
		{"format plain, then escaped", body(`"format":"set-v1","for#u006dat":"set-v1"`, c, g, ch), 0, false},
		{"digest plain, then escaped", body(f, c, `"digest":"`+d+`","dig#u0065st":"`+d+`"`, ch), 0, false},
		{"choices plain, then escaped", body(f, c, g, `"choices":"AQ==","choi#u0063es":"AQ=="`), 0, false},
		{"items plain, then escaped", `{"items":[],"#u0069tems":[]}`, 0, false},

		// refused: an escape that spells another case
		{"Count by an escaped capital", body(f, `"#u0043ount":"1"`, g, ch), 0, false},
		{"Items by an escaped capital", `{"#u0049tems":[]}`, 0, false},

		// refused: an escaped key that is not one of the keys, or not valid JSON
		{"an escaped unknown key", body(f, c, g, ch+`,"ext#u0072a":"x"`), 0, false},
		{"an escaped quote in a key", body(f, `"c#qount":"1"`, g, ch), 0, false},
		{"a bad escape in a key", body(f, `"c#uZZZZount":"1"`, g, ch), 0, false},
		{"a lone surrogate as a value", body(f, `"count":"#ud800"`, g, ch), 0, false},
		{"a lone surrogate as a key", body(f, `"#ud800":"1"`, g, ch), 0, false},
	}
}

func TestReasonEnvelopeEscapedKeys(t *testing.T) {
	for _, tc := range escapedEnvelopeCases() {
		t.Run(tc.name, func(t *testing.T) {
			raw := jsonEsc(tc.raw)
			env, ok := parseReasonEnvelope(json.RawMessage(raw), false)
			if ok != tc.ok || (ok && env.kind != tc.kind) {
				t.Fatalf("parse = kind %d ok %v, want kind %d ok %v\n%s", env.kind, ok, tc.kind, tc.ok, raw)
			}
			if _, ok := parseReasonEnvelope(json.RawMessage(raw), true); ok {
				t.Error("an ambiguous reasonReview key was accepted")
			}
		})
	}
}

// The top-level keys the change depends on, written with escapes.
func TestReasonEnvelopeEscapedTopKeys(t *testing.T) {
	for _, tc := range []struct {
		name, raw       string
		attempt, review bool
	}{
		{"attemptId written with an escape, once", `{"attempt#u0049d":"a"}`, false, false},
		{"attemptId with an escaped first letter, once", `{"#u0061ttemptId":"a"}`, false, false},
		{"attemptId plain, then escaped", `{"attemptId":"a","attempt#u0049d":"b"}`, true, false},
		{"attemptId escaped, then plain", `{"attempt#u0049d":"a","attemptId":"b"}`, true, false},
		{"attemptId escaped twice", `{"attempt#u0049d":"a","attempt#u0049d":"b"}`, true, false},
		{"attemptId escaped two ways", `{"attempt#u0049d":"a","#u0061ttemptId":"b"}`, true, false},
		{"AttemptId by an escaped capital", `{"#u0041ttemptId":"a"}`, true, false},
		{"attemptId with an escaped final letter, once", `{"attemptI#u0064":"a"}`, false, false},
		{"attemptid by an escaped lower-case i", `{"attempt#u0069d":"a"}`, true, false},
		{"reasonReview written with an escape, once", `{"reasonRev#u0069ew":null}`, false, false},
		{"reasonReview plain, then escaped", `{"reasonReview":null,"reason#u0052eview":{}}`, false, true},
		{"reasonReview escaped twice", `{"reasonRev#u0069ew":null,"reasonRev#u0069ew":{}}`, false, true},
		{"ReasonReview by an escaped capital", `{"#u0052easonReview":{}}`, false, true},
		{"a nested escaped copy is not a top-level key", `{"successors":{"attempt#u0049d":"x","attemptId":"y"},"reasonReview":null}`, false, false},
		{"an escaped key and an unrelated key", `{"succ#u0065ssors":{},"attempt#u0049d":"a"}`, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := jsonEsc(tc.raw)
			got := scanTopKeys([]byte(raw))
			if got.attemptAmbiguous != tc.attempt || got.reviewAmbiguous != tc.review {
				t.Errorf("scanTopKeys = %+v, want attempt %v review %v\n%s", got, tc.attempt, tc.review, raw)
			}
		})
	}
}

// The cases above must contain real escapes and not their plain spellings: a
// backslash is in every raw text that was written with a placeholder, and the
// plain spelling of an escaped key is not what is there.
func TestReasonEnvelopeEscapesAreReal(t *testing.T) {
	bs := string(rune(92))
	if got := jsonEsc("a#u006fb#qc"); got != "a"+bs+"u006fb"+bs+string(rune(34))+"c" {
		t.Fatalf("jsonEsc = %q", got)
	}
	for _, tc := range escapedEnvelopeCases() {
		raw := jsonEsc(tc.raw)
		if !strings.Contains(raw, bs) {
			t.Errorf("%s: no backslash in %s", tc.name, raw)
		}
		if strings.Contains(tc.raw, bs) {
			t.Errorf("%s: the case was written with a bare backslash, which a generator may rewrite: use the placeholders", tc.name)
		}
	}
	// What an escape decodes to is exactly what a plain letter is.
	var plain, escaped map[string]string
	if err := json.Unmarshal([]byte(`{"count":"1"}`), &plain); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(jsonEsc(`{"c#u006funt":"1"}`)), &escaped); err != nil {
		t.Fatal(err)
	}
	if plain["count"] != "1" || escaped["count"] != "1" {
		t.Errorf("plain %v, escaped %v: the escape did not decode to the plain key", plain, escaped)
	}
}

// What parsing costs in memory, apart from the consume. Reading the review
// allocates in proportion to the body (the value is held, decoded and checked)
// and that is unavoidable; it is measured here alone, with a tight per-item
// slope, so the consume's own retention can be asserted claim-independent in
// the database test. Measured at the time of writing: about 2.3 bytes per
// claimed reason to parse the envelope and 1.4 to scan the top-level keys.

func allocated(f func()) uint64 {
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	f()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

func TestReasonEnvelopeParseMemoryIsLinearInTheBody(t *testing.T) {
	const small, large = 4_000_000, 16_000_000
	body := func(n int) (json.RawMessage, []byte) {
		raw := json.RawMessage(envJSON("set-v1", strconv.Itoa(n), envDigest, base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x55}, n/4))))
		return raw, append(append([]byte(`{"attemptId":"x","reasonReview":`), raw...), '}')
	}
	measure := func(n int) (parse, scan uint64) {
		raw, whole := body(n)
		parse = allocated(func() {
			if _, ok := parseReasonEnvelope(raw, false); !ok {
				t.Fatal("the envelope was refused")
			}
		})
		scan = allocated(func() { scanTopKeys(whole) })
		return parse, scan
	}
	ps, ss := measure(small)
	pl, sl := measure(large)
	slope := func(a, b uint64) float64 { return (float64(b) - float64(a)) / float64(large-small) }
	t.Logf("parse: %d bytes for %d reasons, %d for %d (%.2f bytes per extra reason); scanTopKeys: %d and %d (%.2f)",
		ps, small, pl, large, slope(ps, pl), ss, sl, slope(ss, sl))
	if got := slope(ps, pl); got > 3.5 {
		t.Errorf("parsing the envelope costs %.2f bytes per extra claimed reason, over the 3.5 allowed (2.3 measured)", got)
	}
	if got := slope(ss, sl); got > 2.2 {
		t.Errorf("scanning the top-level keys costs %.2f bytes per extra claimed reason, over the 2.2 allowed (1.4 measured)", got)
	}
	if pl > uint64(3.5*float64(large)) || sl > uint64(2.2*float64(large)) {
		t.Errorf("absolute cost over the allowance: parse %d, scan %d", pl, sl)
	}
}

// scripts/reason-review-hardening.sh: process ownership and cancellation. The
// script is copied into a scratch git repository and run with stub go, npm and
// node, whose steps misbehave on purpose: a command whose parent leaves before
// its TERM-ignoring child, commands that ignore TERM themselves, a supervisor
// killed from inside its group, a signal in the window between forking a step
// and registering it. Real processes and real signals; bystanders (in the
// test's own process group, and in the script's group beside it) must survive.

const hardeningScriptPath = "scripts/reason-review-hardening.sh"

// hardeningStubs are the stub tools, as shell. A step whose name is in
// STUB_STEP acts as STUB_SCENARIO says; every other step passes and prints what
// the script checks for.
var hardeningStubs = map[string]string{
	"go": `#!/bin/sh
echo "go $*" >>"$STUB_DIR/calls"
case "$1" in
env) echo sh ;;
version) echo "go version stub linux/amd64" ;;
build) while [ $# -gt 0 ]; do if [ "$1" = -o ]; then : >"$2"; fi; shift; done ;;
test) shift; exec "$STUB_BEHAVE" go "$@" ;;
esac
`,
	"npm": `#!/bin/sh
exec "$STUB_BEHAVE" npm "$@"
`,
	"node": `#!/bin/sh
case "$1" in --version) echo v0.0.0 ;; esac
exit 0
`,
	"behave.sh": `#!/bin/sh
kind=$1
shift
step=web
if [ "$kind" = go ]; then
	step=normal
	for a in "$@"; do
		case "$a" in -fuzz) step=fuzz ;; -race) step=race ;; esac
	done
fi
passes() {
	run=
	count=1
	while [ $# -gt 0 ]; do
		case "$1" in -run) run=$2; shift ;; -count=*) count=${1#-count=} ;; esac
		shift
	done
	names=$(printf %s "$run" | sed 's/^^(//; s/)\$$//' | tr '|' ' ')
	i=0
	while [ $i -lt "$count" ]; do
		for n in $names; do echo "--- PASS: $n (0.00s)"; done
		i=$((i + 1))
	done
}
idle() {
	i=0
	while [ $i -lt 600 ]; do sleep 0.2; i=$((i + 1)); done
}
stubborn() {
	sh -c 'trap "" TERM; echo $ >"$1"; i=0; while [ $i -lt 600 ]; do sleep 0.2; i=$((i+1)); done' sh "$STUB_DIR/grandchild.pid" &
}
if [ "$step" = "$STUB_STEP" ]; then
	echo $$ >"$STUB_DIR/root.pid"
	case "$STUB_SCENARIO" in
	stray) stubborn; passes "$@"; : >"$STUB_DIR/started"; exit 0 ;;
	termroot) stubborn; trap 'exit 0' TERM; : >"$STUB_DIR/started"; idle ;;
	deaf) trap '' TERM; stubborn; : >"$STUB_DIR/started"; idle ;;
	lostsup) stubborn; : >"$STUB_DIR/started"; kill -KILL $PPID; idle ;;
	killgroup) : >"$STUB_DIR/started"; kill -KILL 0 ;;
	esac
fi
case "$step" in
normal | race) passes "$@" ;;
web)
	n=0
	while [ $n -lt "$FOLD_REASON_RUNS" ]; do
		echo "digest seed $((FOLD_REASON_SEED_BASE + n)) ok" >>"$FOLD_REASON_EVIDENCE"
		echo "store seed $((FOLD_REASON_SEED_BASE + n)) ok" >>"$FOLD_REASON_EVIDENCE"
		n=$((n + 1))
	done
	;;
esac
`,
	"launch-hook.sh": `#!/bin/sh
: >"$STUB_DIR/inwindow"
i=0
while [ ! -e "$STUB_DIR/started" ] && [ $i -lt 50 ]; do sleep 0.1; i=$((i + 1)); done
sleep 1
`,
}

type hardeningRig struct {
	t        *testing.T
	repo     string
	stubs    string
	state    string
	evidence string
	path     string
}

// newHardeningRig builds the scratch repository and the stub tools. withNode
// false leaves node off the PATH altogether.
func newHardeningRig(t *testing.T, withNode bool) *hardeningRig {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("these tests identify processes by their /proc start time")
	}
	dir := t.TempDir()
	r := &hardeningRig{t: t, repo: filepath.Join(dir, "repo"), stubs: filepath.Join(dir, "stubs"), state: filepath.Join(dir, "state"), evidence: filepath.Join(dir, "evidence")}
	for _, d := range []string{filepath.Join(r.repo, "scripts"), filepath.Join(r.repo, "web", "node_modules"), r.stubs, r.state} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	script, err := os.ReadFile(hardeningScriptPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(r.repo, hardeningScriptPath), script, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range hardeningStubs {
		if name == "node" && !withNode {
			continue
		}
		if err := os.WriteFile(filepath.Join(r.stubs, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// The system tools the script needs, linked into one directory, so a test
	// can leave one out; nothing else of the machine's PATH is visible.
	tools := filepath.Join(dir, "tools")
	if err := os.MkdirAll(tools, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"bash", "env", "sh", "git", "sleep", "grep", "sed", "find", "comm", "sort", "cp", "mv", "mktemp", "tail", "uname", "date", "cat", "rm", "mkdir", "ls", "dirname", "tr", "xargs", "sha256sum", "cut", "awk", "head"} {
		p, err := exec.LookPath(name)
		if err != nil {
			t.Fatalf("the hardening script tests need %s on PATH: %v", name, err)
		}
		if err := os.Symlink(p, filepath.Join(tools, name)); err != nil {
			t.Fatal(err)
		}
	}
	r.path = r.stubs + string(os.PathListSeparator) + tools
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", r.repo, "-c", "user.name=test", "-c", "user.email=test@example.invalid", "-c", "commit.gpgsign=false"}, args...)...)
		cmd.Env = []string{"PATH=" + r.path, "HOME=" + dir}
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q")
	git("add", "-A")
	git("commit", "-q", "-m", "scratch")
	return r
}

type hardeningRun struct {
	cmd  *exec.Cmd
	done chan struct{}
	err  error
	out  string
	// groupID is a process that shares the script's own process group (not a
	// step's), when one was asked for: nothing the script does to a step may
	// touch it.
	groupID procID
}

// procID is a process's identity: its PID and the start time the kernel
// recorded for it, so a later process that was given the same number is not
// mistaken for it. The tests only observe processes by identity; the ones they
// start are ended through their os/exec handles, and the stub processes only by
// the script under test (they also give up on their own after a couple of
// minutes), never by a PID read back from a file.
type procID struct {
	pid   int
	start string
}

// procStat reads /proc/PID/stat as (state, start time).
func procStat(pid int) (state, start string, ok bool) {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return "", "", false
	}
	s := string(b)
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return "", "", false
	}
	f := strings.Fields(s[i+1:]) // state is field 3 of the file, start time field 22
	if len(f) < 20 {
		return "", "", false
	}
	return f[0], f[19], true
}

func captureProc(pid int) (procID, bool) {
	_, start, ok := procStat(pid)
	return procID{pid, start}, ok
}

// ended: the process this identity names is gone, replaced by another process
// with its number, or a zombie nobody has reaped (a container's PID 1 may not
// reap), which can no longer run.
func (p procID) ended() bool {
	state, start, ok := procStat(p.pid)
	return !ok || start != p.start || state == "Z" || state == "X"
}

// start runs the script in a process group of its own, so a signal to the
// test's group and one to the script's cannot be confused. extraEnv is
// STUB_STEP, STUB_SCENARIO and the like. When bystander is set, a process is
// put in the script's own group beside it.
func (r *hardeningRig) start(extraEnv []string, bystander bool, args ...string) *hardeningRun {
	r.t.Helper()
	script := filepath.Join(r.repo, hardeningScriptPath)
	argv := append([]string{script, "--evidence-dir", r.evidence, "--fuzz-time", "1s", "--race-count", "1", "--seeds", "2"}, args...)
	cmd := exec.Command("bash", argv...)
	cmd.Dir = r.repo
	cmd.Env = append([]string{"PATH=" + r.path, "HOME=" + r.state, "STUB_DIR=" + r.state, "STUB_BEHAVE=" + filepath.Join(r.stubs, "behave.sh")}, extraEnv...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// A file, not a pipe: a bystander that inherits a pipe would hold Wait open.
	outPath := filepath.Join(r.state, "out.log")
	outFile, err := os.Create(outPath)
	if err != nil {
		r.t.Fatal(err)
	}
	cmd.Stdout, cmd.Stderr = outFile, outFile
	run := &hardeningRun{cmd: cmd, done: make(chan struct{})}
	if err := cmd.Start(); err != nil {
		r.t.Fatal(err)
	}
	go func() {
		run.err = cmd.Wait()
		_ = outFile.Close()
		b, _ := os.ReadFile(outPath)
		run.out = string(b)
		close(run.done)
	}()
	if bystander {
		b := exec.Command("sleep", "300")
		b.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pgid: cmd.Process.Pid}
		if err := b.Start(); err != nil {
			r.t.Fatalf("starting a bystander in the script's process group: %v", err)
		}
		var ok bool
		if run.groupID, ok = captureProc(b.Process.Pid); !ok {
			r.t.Fatal("the bystander is not running")
		}
		r.t.Cleanup(func() { _ = b.Process.Kill(); _ = b.Wait() })
	}
	// Cleanup holds the script's own handle: if the test failed with the script
	// still running, ask it to stop, which ends the step it owns, and only then
	// kill it. No PID read from a file and no saved group number is signalled.
	r.t.Cleanup(func() {
		select {
		case <-run.done:
			return
		default:
		}
		_ = cmd.Process.Signal(syscall.SIGTERM)
		select {
		case <-run.done:
		case <-time.After(30 * time.Second):
			_ = cmd.Process.Kill()
			<-run.done
		}
	})
	return run
}

func (run *hardeningRun) exitCode(t *testing.T, within time.Duration) int {
	t.Helper()
	select {
	case <-run.done:
	case <-time.After(within):
		t.Fatalf("the script was still running after %v", within)
	}
	if run.err == nil {
		return 0
	}
	var ee *exec.ExitError
	if !errors.As(run.err, &ee) {
		t.Fatalf("waiting for the script: %v", run.err)
	}
	return ee.ExitCode()
}

func (r *hardeningRig) waitFor(name string, within time.Duration) {
	r.t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(filepath.Join(r.state, name)); err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	r.t.Fatalf("%s never appeared", name)
}

// proc waits for a stub to write its PID and captures its identity at once.
func (r *hardeningRig) proc(name string) procID {
	r.t.Helper()
	r.waitFor(name, 10*time.Second)
	for i := 0; i < 100; i++ { // the file may be created before the number is written
		if b, err := os.ReadFile(filepath.Join(r.state, name)); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && pid > 1 {
				if id, ok := captureProc(pid); ok {
					return id
				}
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	r.t.Fatalf("%s holds no running process", name)
	return procID{}
}

func (r *hardeningRig) expectEnded(what string, p procID) {
	r.t.Helper()
	for i := 0; i < 150; i++ {
		if p.ended() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	r.t.Errorf("%s (PID %d) is still running", what, p.pid)
}

func (r *hardeningRig) expectAlive(what string, p procID) {
	r.t.Helper()
	if p.ended() {
		r.t.Errorf("%s (PID %d) ended; it was not the step's to end", what, p.pid)
	}
}

// bystanderInTestGroup is a process in the test's own process group, which no
// part of the script may signal.
func bystanderInTestGroup(t *testing.T) procID {
	t.Helper()
	cmd := exec.Command("sleep", "300")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	id, ok := captureProc(cmd.Process.Pid)
	if !ok {
		t.Fatal("the bystander is not running")
	}
	return id
}

type hardeningEvidence struct {
	Result string `json:"result"`
	Steps  []struct {
		Name    string `json:"name"`
		Exit    int    `json:"exit"`
		Verdict string `json:"verdict"`
	} `json:"steps"`
}

func (r *hardeningRig) record() hardeningEvidence {
	r.t.Helper()
	b, err := os.ReadFile(filepath.Join(r.evidence, "evidence.json"))
	if err != nil {
		r.t.Fatal(err)
	}
	var ev hardeningEvidence
	if err := json.Unmarshal(b, &ev); err != nil {
		r.t.Fatalf("evidence.json is not JSON: %v\n%s", err, b)
	}
	return ev
}

// A step whose command leaves a TERM-ignoring child behind and exits first is
// the case a process-list snapshot loses: the child is reparented and no
// longer found from the root. The group still owns it, and a run that
// completes normally leaves nothing of any step behind.
func TestReasonHardeningStepLeftoversDoNotOutliveTheStep(t *testing.T) {
	r := newHardeningRig(t, true)
	bystander := bystanderInTestGroup(t)
	run := r.start([]string{"STUB_STEP=normal", "STUB_SCENARIO=stray"}, true)
	if code := run.exitCode(t, 60*time.Second); code != 0 {
		t.Fatalf("the script exited %d:\n%s", code, run.out)
	}
	r.expectEnded("the TERM-ignoring child the first step left behind", r.proc("grandchild.pid"))
	r.expectAlive("a bystander in the test's process group", bystander)
	r.expectAlive("a bystander in the script's own process group", run.groupID)
	ev := r.record()
	if ev.Result != "pass" || len(ev.Steps) != 4 {
		t.Fatalf("record: %+v", ev)
	}
	for _, s := range ev.Steps {
		if s.Exit != 0 || s.Verdict != "pass" {
			t.Errorf("step %+v", s)
		}
	}
	if b, err := os.ReadFile(filepath.Join(r.evidence, "active.pgid")); err != nil || strings.TrimSpace(string(b)) == "" {
		t.Errorf("active.pgid was not written: %v", err)
	}
}

// TERM or INT to the script's PID alone: the command leaves on TERM but its
// TERM-ignoring child does not, and the child is killed all the same; the
// record is incomplete; the exit status is the signal's; the script's own group
// and the caller's survive.
func TestReasonHardeningCancellationKillsWhatTheParentLeft(t *testing.T) {
	for _, tc := range []struct {
		name string
		sig  syscall.Signal
		code int
	}{{"TERM", syscall.SIGTERM, 143}, {"INT", syscall.SIGINT, 130}, {"HUP", syscall.SIGHUP, 129}} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.sig == syscall.SIGINT && signal.Ignored(syscall.SIGINT) {
				t.Skip("this test process started with SIGINT ignored, which its children inherit and no shell script can undo")
			}
			r := newHardeningRig(t, true)
			bystander := bystanderInTestGroup(t)
			run := r.start([]string{"STUB_STEP=normal", "STUB_SCENARIO=termroot"}, true)
			root, child := r.proc("root.pid"), r.proc("grandchild.pid")
			r.waitFor("started", 10*time.Second)
			if err := run.cmd.Process.Signal(tc.sig); err != nil { // the script alone, through its handle
				t.Fatal(err)
			}
			if code := run.exitCode(t, 30*time.Second); code != tc.code {
				t.Fatalf("exit %d, want %d:\n%s", code, tc.code, run.out)
			}
			r.expectEnded("the step's command", root)
			r.expectEnded("the TERM-ignoring child whose parent had already left", child)
			r.expectAlive("a bystander in the test's process group", bystander)
			r.expectAlive("a bystander in the script's own process group", run.groupID)
			if ev := r.record(); ev.Result != "incomplete" {
				t.Errorf("result %q, want incomplete", ev.Result)
			}
		})
	}
}

// A command and its child that both ignore TERM: the cleanup is still bounded,
// ends with KILL, and does not wait on them.
func TestReasonHardeningCancellationIsBoundedWhenTermIsIgnored(t *testing.T) {
	r := newHardeningRig(t, true)
	run := r.start([]string{"STUB_STEP=normal", "STUB_SCENARIO=deaf"}, false)
	root, child := r.proc("root.pid"), r.proc("grandchild.pid")
	r.waitFor("started", 10*time.Second)
	began := time.Now()
	if err := run.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if code := run.exitCode(t, 30*time.Second); code != 143 {
		t.Fatalf("exit %d, want 143:\n%s", code, run.out)
	}
	if took := time.Since(began); took > 20*time.Second {
		t.Errorf("cancellation took %v", took)
	}
	r.expectEnded("the TERM-ignoring command", root)
	r.expectEnded("the TERM-ignoring child", child)
	if ev := r.record(); ev.Result != "incomplete" {
		t.Errorf("result %q, want incomplete", ev.Result)
	}
}

// A signal in the window between forking the step and registering it is held,
// then acted on: the step does not keep running behind an exited script. The
// hook runs inside that window (FOLD_HARDENING_TEST_LAUNCH_HOOK) until the step
// is up and the signal has had time to arrive.
func TestReasonHardeningSignalDuringLaunchIsNotLost(t *testing.T) {
	r := newHardeningRig(t, true)
	run := r.start([]string{"STUB_STEP=normal", "STUB_SCENARIO=termroot", "FOLD_HARDENING_TEST_LAUNCH_HOOK=" + filepath.Join(r.stubs, "launch-hook.sh")}, false)
	r.waitFor("inwindow", 10*time.Second)
	r.waitFor("started", 10*time.Second)
	root, child := r.proc("root.pid"), r.proc("grandchild.pid")
	if err := run.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if code := run.exitCode(t, 30*time.Second); code != 143 {
		t.Fatalf("exit %d, want 143:\n%s", code, run.out)
	}
	r.expectEnded("the step started in the launch window", root)
	r.expectEnded("the step's TERM-ignoring child", child)
	if ev := r.record(); ev.Result != "incomplete" || len(ev.Steps) != 0 {
		t.Errorf("record %+v, want incomplete with no finished step", ev)
	}
}

// Losing only the supervisor, the process that runs the command, while the
// command and a TERM-ignoring descendant keep running: the holder that shares
// the job notices, the step fails (125) and its whole group is ended, within a
// bounded time, and the later steps still run. Bystanders in the test's group
// and in the script's own group are untouched.
func TestReasonHardeningLostSupervisorAloneLeavesNoSurvivors(t *testing.T) {
	r := newHardeningRig(t, true)
	bystander := bystanderInTestGroup(t)
	run := r.start([]string{"STUB_STEP=normal", "STUB_SCENARIO=lostsup"}, true)
	root, child := r.proc("root.pid"), r.proc("grandchild.pid")
	r.waitFor("started", 10*time.Second)
	if code := run.exitCode(t, 60*time.Second); code != 1 {
		t.Fatalf("exit %d, want 1:\n%s", code, run.out)
	}
	r.expectEnded("the command whose supervisor was killed", root)
	r.expectEnded("the TERM-ignoring descendant of that command", child)
	r.expectAlive("a bystander in the test's process group", bystander)
	r.expectAlive("a bystander in the script's own process group", run.groupID)
	ev := r.record()
	if ev.Result != "fail" || len(ev.Steps) != 4 || ev.Steps[0].Name != "normal" || ev.Steps[0].Exit != 125 || ev.Steps[0].Verdict != "fail" {
		t.Fatalf("record %+v", ev)
	}
	for _, s := range ev.Steps[1:] {
		if s.Verdict != "pass" {
			t.Errorf("a later step did not run to a pass: %+v", s)
		}
	}
	log, err := os.ReadFile(filepath.Join(r.evidence, "normal.log"))
	if err != nil || !strings.Contains(string(log), "supervisor ended without reporting a status") {
		t.Errorf("the step's log does not say the supervisor was lost: %v\n%s", err, log)
	}
}

// The whole job gone at once (here the command kills its entire group,
// supervisor and holder included) is a failed step, reported and recorded, not
// a hang or a pass; the later steps still run. Nothing is left to signal, and
// the script says so.
func TestReasonHardeningLostJobFailsTheStep(t *testing.T) {
	r := newHardeningRig(t, true)
	bystander := bystanderInTestGroup(t)
	run := r.start([]string{"STUB_STEP=normal", "STUB_SCENARIO=killgroup"}, false)
	if code := run.exitCode(t, 60*time.Second); code != 1 {
		t.Fatalf("exit %d, want 1:\n%s", code, run.out)
	}
	ev := r.record()
	if ev.Result != "fail" || len(ev.Steps) != 4 || ev.Steps[0].Name != "normal" || ev.Steps[0].Exit != 125 || ev.Steps[0].Verdict != "fail" {
		t.Errorf("record %+v", ev)
	}
	for _, s := range ev.Steps[1:] {
		if s.Verdict != "pass" {
			t.Errorf("a later step did not run to a pass: %+v", s)
		}
	}
	if !strings.Contains(run.out, "both ended") {
		t.Errorf("the script did not report that nothing stable was left to signal:\n%s", run.out)
	}
	r.expectAlive("a bystander in the test's process group", bystander)
}

// A missing tool is refused before anything runs, and nothing is written.
func TestReasonHardeningRefusesWhenAToolIsMissing(t *testing.T) {
	r := newHardeningRig(t, false)
	run := r.start(nil, false)
	if code := run.exitCode(t, 30*time.Second); code != 2 {
		t.Fatalf("exit %d, want 2:\n%s", code, run.out)
	}
	if !strings.Contains(run.out, "node is required") {
		t.Errorf("output does not name the missing tool:\n%s", run.out)
	}
	if _, err := os.Stat(r.evidence); err == nil {
		t.Error("an evidence directory was created for a run that never started")
	}
	if _, err := os.Stat(filepath.Join(r.state, "calls")); err == nil {
		t.Error("a tool ran before the dependencies were checked")
	}
}
