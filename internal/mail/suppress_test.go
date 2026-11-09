package mail

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"sync/atomic"
	"testing"
	"time"

	"fold/internal/providers"
	"fold/internal/testdb"
	"fold/models"

	"gorm.io/gorm"
)

// A hard bounce or a manual suppression stops all mail; a complaint stops
// optional and broadcast mail but not account mail; a cleared one stops
// nothing (DEC-112).
func TestSuppressionsStopMail(t *testing.T) {
	db := testdb.Shared(t)
	s := WithSuppressions(db, NewDevOutbox(db, "Fold <noreply@example.com>"))
	now := time.Now()
	for _, row := range []models.Suppression{
		{Channel: "email", Address: "gone@example.com", Reason: models.SuppressHardBounce},
		{Channel: "email", Address: "manual@example.com", Reason: models.SuppressManual},
		{Channel: "email", Address: "spam@example.com", Reason: models.SuppressComplaint},
		{Channel: "email", Address: "cleared@example.com", Reason: models.SuppressHardBounce, ClearedAt: &now},
	} {
		if err := db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	for _, tc := range []struct {
		to       string
		optional bool
		stream   providers.Stream
		sends    bool
	}{
		{"Gone@Example.com ", false, "", false},
		{"manual@example.com", false, "", false},
		{"spam@example.com", false, "", true},
		{"spam@example.com", true, "", false},
		{"spam@example.com", false, providers.StreamBroadcast, false},
		{"cleared@example.com", true, "", true},
		{"fine@example.com", true, providers.StreamBroadcast, true},
	} {
		_, err := s.SendEmail(ctx, providers.EmailMessage{To: tc.to, Subject: "s", Text: "t", Optional: tc.optional, Stream: tc.stream})
		if tc.sends && err != nil || !tc.sends && !IsSuppressed(err) {
			t.Errorf("%s optional=%v stream=%q: %v", tc.to, tc.optional, tc.stream, err)
		}
	}
	if Unwrap(s) != s.inner || Unwrap(s.inner) != s.inner {
		t.Fatal("Unwrap")
	}
}

var addressCorpus = []string{
	"", " ", "pat@example.com", " Pat@Example.COM ", "PAT@EXAMPLE.COM",
	"\tpat@example.com\n", " pat@example.com ", "pat@example.com ",
	"ÅSA@EXAMPLE.COM", "åsa@example.com", " Åsa@Example.com\r\n",
}

func TestNormalizeAddress(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{" Pat@Example.COM ", "pat@example.com"},
		{"\tpat@example.com\n", "pat@example.com"},
		{" pat@example.com ", "pat@example.com"},
		{"ÅSA@EXAMPLE.COM", "åsa@example.com"},
		{"", ""},
		{"   ", ""},
	} {
		if got := NormalizeAddress(tc.in); got != tc.want {
			t.Errorf("NormalizeAddress(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	for _, x := range addressCorpus {
		once := NormalizeAddress(x)
		if twice := NormalizeAddress(once); twice != once {
			t.Errorf("NormalizeAddress is not idempotent for %q: %q then %q", x, once, twice)
		}
	}
}

func TestAddressHash(t *testing.T) {
	// printf %s pat@example.com | sha256sum, computed outside this code.
	const patHash = "fe9733fcc96501276776fc927df08a395835461a2c5b12c65ebab3454877e227"
	// printf %s '' | sha256sum
	const emptyHash = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

	if got := AddressHash(" Pat@Example.COM "); got != patHash {
		t.Errorf("AddressHash(' Pat@Example.COM ') = %s, want %s", got, patHash)
	}
	if got := AddressHash("pat@example.com"); got != patHash {
		t.Errorf("AddressHash(pat@example.com) = %s, want %s", got, patHash)
	}
	for _, in := range []string{"\tpat@example.com\n", " pat@example.com ", "PAT@EXAMPLE.COM"} {
		if got := AddressHash(in); got != patHash {
			t.Errorf("AddressHash(%q) = %s, want %s", in, got, patHash)
		}
	}
	if AddressHash("ÅSA@EXAMPLE.COM") != AddressHash("åsa@example.com") {
		t.Error("ÅSA@EXAMPLE.COM and åsa@example.com should hash the same")
	}
	if StoredAddressHash("pat@example.com ") == AddressHash("pat@example.com") {
		t.Error("StoredAddressHash must not normalize: a stored trailing U+00A0 is its own value")
	}
	if got := StoredAddressHash(""); got != emptyHash {
		t.Errorf("StoredAddressHash('') = %s, want %s", got, emptyHash)
	}
	hexRE := regexp.MustCompile(`^[0-9a-f]{64}$`)
	for _, x := range addressCorpus {
		if h := AddressHash(x); !hexRE.MatchString(h) {
			t.Errorf("AddressHash(%q) = %q, not 64 lowercase hex", x, h)
		}
		if h := StoredAddressHash(x); !hexRE.MatchString(h) {
			t.Errorf("StoredAddressHash(%q) = %q, not 64 lowercase hex", x, h)
		}
	}
}

// hashedRow is a suppression written the way the R4-S binary writes it, with
// the address hash beside the address.
func hashedRow(address, reason string) models.Suppression {
	h := StoredAddressHash(address)
	return models.Suppression{Channel: "email", Address: address, AddressHash: &h, Reason: reason}
}

// A hashed row and a legacy NULL-hash row both block (the hash arm and the
// address arm), a cleared row blocks nothing, and a complaint still lets
// required mail through (DEC-112). Because the address arm requires a NULL
// hash, finding the hashed row proves the hash arm is used.
func TestSuppressingMatchesHashedAndLegacyRows(t *testing.T) {
	db := testdb.Shared(t)
	s := WithSuppressions(db, NewDevOutbox(db, "Fold <noreply@example.com>"))
	now := time.Now()
	hashedCleared := hashedRow("cleared-hashed@example.com", models.SuppressHardBounce)
	hashedCleared.ClearedAt = &now
	for _, row := range []models.Suppression{
		hashedRow("hashed@example.com", models.SuppressHardBounce),
		{Channel: "email", Address: "legacy@example.com", Reason: models.SuppressHardBounce},
		hashedRow("spam-hashed@example.com", models.SuppressComplaint),
		{Channel: "email", Address: "spam-legacy@example.com", Reason: models.SuppressComplaint},
		hashedCleared,
	} {
		if err := db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	for _, tc := range []struct {
		to       string
		optional bool
		sends    bool
	}{
		{" Hashed@Example.com ", false, false},
		{"hashed@example.com", true, false},
		{"LEGACY@example.com", false, false},
		{"spam-hashed@example.com", false, true},
		{"spam-hashed@example.com", true, false},
		{"spam-legacy@example.com", false, true},
		{"spam-legacy@example.com", true, false},
		{"cleared-hashed@example.com", false, true},
	} {
		_, err := s.SendEmail(ctx, providers.EmailMessage{To: tc.to, Subject: "s", Text: "t", Optional: tc.optional})
		if tc.sends && err != nil || !tc.sends && !IsSuppressed(err) {
			t.Errorf("%q optional=%v: %v", tc.to, tc.optional, err)
		}
	}
}

// countingAdapter records the sends that get past the suppression check.
type countingAdapter struct{ sent []string }

func (c *countingAdapter) Capabilities() providers.EmailCaps { return providers.EmailCaps{} }
func (c *countingAdapter) SendEmail(_ context.Context, m Message) (string, error) {
	c.sent = append(c.sent, m.To)
	return "id", nil
}

// With a complaint and a hard bounce both open, in either insert order and
// whether the rows are hashed or legacy NULL-hash rows: required mail is
// refused with hard_bounce, and optional mail follows the older row (this
// order was unspecified before R4-S). Nothing reaches the inner sender when
// refused, and a cleared companion row changes nothing.
func TestSuppressingBothReasonsInEitherOrder(t *testing.T) {
	db := testdb.Shared(t)
	inner := &countingAdapter{}
	s := WithSuppressions(db, inner)
	now := time.Now()
	type pair struct {
		address       string
		hashed        bool
		first, second string
	}
	for _, p := range []pair{
		{"hashed-complaint-first@example.com", true, models.SuppressComplaint, models.SuppressHardBounce},
		{"hashed-bounce-first@example.com", true, models.SuppressHardBounce, models.SuppressComplaint},
		{"legacy-complaint-first@example.com", false, models.SuppressComplaint, models.SuppressHardBounce},
		{"legacy-bounce-first@example.com", false, models.SuppressHardBounce, models.SuppressComplaint},
	} {
		for _, reason := range []string{p.first, p.second} {
			row := models.Suppression{Channel: "email", Address: p.address, Reason: reason}
			if p.hashed {
				row = hashedRow(p.address, reason)
			}
			if err := db.Create(&row).Error; err != nil {
				t.Fatal(err)
			}
		}
		// A cleared row beside the open pair changes nothing.
		cleared := models.Suppression{Channel: "email", Address: p.address, Reason: models.SuppressManual, ClearedAt: &now}
		if err := db.Create(&cleared).Error; err != nil {
			t.Fatal(err)
		}
		for _, optional := range []bool{false, true} {
			want := models.SuppressHardBounce
			if optional {
				want = p.first
			}
			_, err := s.SendEmail(context.Background(), providers.EmailMessage{To: " " + p.address + " ", Subject: "s", Text: "t", Optional: optional})
			var sup *ErrSuppressed
			if !errors.As(err, &sup) || sup.Reason != want {
				t.Errorf("%s optional=%v: %v, want reason %s", p.address, optional, err, want)
			}
		}
	}
	if len(inner.sent) != 0 {
		t.Fatalf("refused mail reached the sender: %v", inner.sent)
	}
	// A complaint alone still lets required mail through, hashed or legacy.
	for _, row := range []models.Suppression{
		hashedRow("complaint-hashed@example.com", models.SuppressComplaint),
		{Channel: "email", Address: "complaint-legacy@example.com", Reason: models.SuppressComplaint},
	} {
		if err := db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
		if _, err := s.SendEmail(context.Background(), providers.EmailMessage{To: row.Address, Subject: "s", Text: "t"}); err != nil {
			t.Errorf("%s: required mail refused: %v", row.Address, err)
		}
	}
	if len(inner.sent) != 2 {
		t.Fatalf("required mail past a complaint made %d sends, want 2", len(inner.sent))
	}
}

// readCounter counts the reads db runs from now on.
func readCounter(t *testing.T, db *gorm.DB) *atomic.Int64 {
	t.Helper()
	var n atomic.Int64
	count := func(*gorm.DB) { n.Add(1) }
	name := fmt.Sprintf("test:suppression_reads:%s", t.Name())
	if err := db.Callback().Query().Before("gorm:query").Register(name, count); err != nil {
		t.Fatal(err)
	}
	if err := db.Callback().Row().Before("gorm:row").Register(name, count); err != nil {
		t.Fatal(err)
	}
	return &n
}

func TestOpenSuppressionReasons(t *testing.T) {
	// A Fresh database, so the read counter's callbacks never reach the
	// shared one.
	db, err := testdb.Open(testdb.Fresh(t, true))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	now := time.Now()
	hashedCleared := hashedRow("cleared@example.com", models.SuppressHardBounce)
	hashedCleared.ClearedAt = &now
	// Inserted in id order: each pair's older row is first.
	for _, row := range []models.Suppression{
		hashedRow("both@example.com", models.SuppressComplaint),
		hashedRow("both@example.com", models.SuppressHardBounce),
		{Channel: "email", Address: "legacy-both@example.com", Reason: models.SuppressHardBounce},
		{Channel: "email", Address: "legacy-both@example.com", Reason: models.SuppressComplaint},
		hashedRow("one@example.com", models.SuppressManual),
		{Channel: "email", Address: "legacy@example.com", Reason: models.SuppressHardBounce},
		hashedCleared,
		hashedRow("åsa@example.com", models.SuppressHardBounce),
		hashedRow("padded@example.com\u00a0", models.SuppressHardBounce), // a stored U+00A0 is its own value, no input normalizes to it
	} {
		if err := db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	reads := readCounter(t, db)

	got, err := OpenSuppressionReasons(db, []string{
		" Both@Example.COM", "both@example.com", "", "   ", "LEGACY-BOTH@example.com", "One@example.com",
		"\tlegacy@example.com\n", "cleared@example.com", "ÅSA@example.com", "absent@example.com",
		"padded@example.com", "padded@example.com\u00a0",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{
		"both@example.com":        {models.SuppressComplaint, models.SuppressHardBounce},
		"legacy-both@example.com": {models.SuppressHardBounce, models.SuppressComplaint},
		"one@example.com":         {models.SuppressManual},
		"legacy@example.com":      {models.SuppressHardBounce},
		"åsa@example.com":         {models.SuppressHardBounce},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("OpenSuppressionReasons = %v, want %v", got, want)
	}
	if n := reads.Load(); n != 1 {
		t.Fatalf("reads = %d, want 1", n)
	}

	reads.Store(0)
	for _, in := range [][]string{nil, {}, {"", "  ", "\t"}} {
		out, err := OpenSuppressionReasons(db, in)
		if err != nil || len(out) != 0 {
			t.Fatalf("input %q: %v, %v", in, out, err)
		}
	}
	if n := reads.Load(); n != 0 {
		t.Fatalf("empty input ran %d queries, want none", n)
	}

	// More than one chunk: 1,204 distinct addresses make three reads (500, 500
	// and 204). Hashed and legacy rows sit in every chunk, at its first and
	// last slots (indexes 0 and 499, 500 and 999, 1000 and 1203), so a lookup
	// that dropped or repeated the results of any one read fails on the
	// complete map.
	for _, row := range []models.Suppression{
		{Channel: "email", Address: "chunk499@example.com", Reason: models.SuppressHardBounce},
		hashedRow("chunk999@example.com", models.SuppressManual),
	} {
		if err := db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	placed := map[int]string{
		0:    " BOTH@example.com ",      // hashed, chunk 1
		499:  "chunk499@example.com",    // legacy, chunk 1
		500:  "legacy-both@example.com", // legacy, chunk 2
		999:  "chunk999@example.com",    // hashed, chunk 2
		1000: "one@example.com",         // hashed, chunk 3
		1203: "legacy@example.com",      // legacy, chunk 3
	}
	many := make([]string, 1204)
	for i := range many {
		if a, ok := placed[i]; ok {
			many[i] = a
		} else {
			many[i] = fmt.Sprintf("u%d@example.com", i)
		}
	}
	reads.Store(0)
	got, err = OpenSuppressionReasons(db, many)
	if err != nil {
		t.Fatal(err)
	}
	if n := reads.Load(); n != 3 {
		t.Fatalf("1,204 distinct inputs made %d reads, want 3", n)
	}
	wantMany := map[string][]string{
		"both@example.com":        {models.SuppressComplaint, models.SuppressHardBounce},
		"chunk499@example.com":    {models.SuppressHardBounce},
		"legacy-both@example.com": {models.SuppressHardBounce, models.SuppressComplaint},
		"chunk999@example.com":    {models.SuppressManual},
		"one@example.com":         {models.SuppressManual},
		"legacy@example.com":      {models.SuppressHardBounce},
	}
	if !reflect.DeepEqual(got, wantMany) {
		t.Fatalf("chunked lookup = %v, want %v", got, wantMany)
	}
}
