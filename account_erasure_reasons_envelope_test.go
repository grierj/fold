package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"runtime"
	"strconv"
	"strings"
	"testing"
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
