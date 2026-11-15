package mail

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"slices"
	"strings"

	"fold/internal/providers"
	"fold/models"

	"gorm.io/gorm"
)

// ErrSuppressed is a send refused because the address is suppressed
// (DEC-112). Reason is the stored reason, for the delivery record.
type ErrSuppressed struct{ Reason string }

func (e *ErrSuppressed) Error() string {
	if e.Reason == models.SuppressComplaint {
		return "mail: this address marked our email as spam"
	}
	return "mail: email to this address bounces"
}

// Suppressing wraps an adapter so nothing reaches a suppressed address
// (DEC-112): a hard bounce or manual suppression stops all mail, and a spam
// complaint stops optional and broadcast mail while account mail still goes.
// Every send pays one indexed read.
type Suppressing struct {
	inner Sender
	db    *gorm.DB
}

func WithSuppressions(db *gorm.DB, inner Sender) *Suppressing {
	return &Suppressing{inner: inner, db: db}
}

func (s *Suppressing) Capabilities() providers.EmailCaps { return s.inner.Capabilities() }

func (s *Suppressing) SendEmail(ctx context.Context, m Message) (string, error) {
	// One read per send (OpenSuppressionReasons with a single address).
	reasons, err := OpenSuppressionReasons(s.db.WithContext(ctx), []string{m.To})
	if err != nil {
		return "", err
	}
	if err := Blocked(reasons[NormalizeAddress(m.To)], m); err != nil {
		return "", err
	}
	return s.inner.SendEmail(ctx, m)
}

func IsSuppressed(err error) bool {
	var s *ErrSuppressed
	return errors.As(err, &s)
}

// Unwrap is the adapter under a suppression check, or s itself.
func Unwrap(s Sender) Sender {
	if w, ok := s.(*Suppressing); ok {
		return w.inner
	}
	return s
}

// Blocked is the suppression rule (DEC-112) for a message to an address
// with these open reasons: a hard bounce or manual suppression refuses
// everything, a complaint refuses optional and broadcast mail. A caller that
// already read the reasons (a notification batch) sends through Unwrap
// after asking this.
func Blocked(reasons []string, m Message) error {
	r := StrongestReason(reasons)
	if r == "" || (r == models.SuppressComplaint && !m.Optional && m.Stream != providers.StreamBroadcast) {
		return nil
	}
	return &ErrSuppressed{Reason: r}
}

// StrongestReason is the one reason that stands for an address with these
// open reasons wherever Fold shows or applies one: the composer's count, the
// refusal a send reports and the admin list. What stops the most mail wins
// (DEC-112): a hard bounce, then a manual suppression (both stop all mail),
// then a spam complaint (optional and broadcast mail only). "" when none.
func StrongestReason(reasons []string) string {
	best, bestRank := "", len(suppressionRank)
	for _, r := range reasons {
		rank := slices.Index(suppressionRank, r)
		if rank < 0 {
			// A reason this build doesn't know stops all mail, as a manual one does.
			rank = slices.Index(suppressionRank, models.SuppressManual)
		}
		if rank < bestRank {
			best, bestRank = r, rank
		}
	}
	return best
}

var suppressionRank = []string{models.SuppressHardBounce, models.SuppressManual, models.SuppressComplaint}

// NormalizeAddress is the one way Fold turns an email address into the form
// it hashes and matches: surrounding whitespace trimmed, then
// lower-cased. It is idempotent and total.
func NormalizeAddress(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// StoredAddressHash is the lowercase hex SHA-256 of the exact bytes of a
// string, with no normalizing. Total: an empty string hashes its own empty
// bytes. The hash is unkeyed and guessable, a pseudonymous matching value and
// not encryption.
func StoredAddressHash(stored string) string {
	sum := sha256.Sum256([]byte(stored))
	return hex.EncodeToString(sum[:])
}

// AddressHash is the value Fold stores and looks up in place of an address
// (PLAN 10.1): StoredAddressHash of NormalizeAddress(s). Callers that look up
// by hash drop empty normalized inputs first; an empty input still hashes.
// That SQL (migration 00008's backfill) and Go agree on these bytes is proven
// for UTF8 databases by the suppression matching-basis test.
func AddressHash(s string) string { return StoredAddressHash(NormalizeAddress(s)) }

// openSuppressionsSQL reads the open email suppressions for a set of address
// hashes. ORDER BY id fixes the order reasons are returned in.
const openSuppressionsSQL = `SELECT address_hash, reason FROM suppressions
	WHERE channel = 'email' AND cleared_at IS NULL AND address_hash IN ? ORDER BY id`

// suppressionLookupChunk is how many addresses one query carries, well under
// the 65,535 bind limit.
const suppressionLookupChunk = 500

// OpenSuppressionReasons maps each address to its open email suppression
// reasons, in ascending id order. Each input is normalized with
// NormalizeAddress; empty ones are dropped and duplicates read once. The key
// is the normalized address, and an address with no open row is left out.
// Empty input runs no query.
//
// A suppression is stored as the hash of the normalized address and nothing
// else, so this is the one way to ask about an address. The hash is computed
// in Go from the caller's address and never comes from request input.
func OpenSuppressionReasons(db *gorm.DB, addresses []string) (map[string][]string, error) {
	seen := make(map[string]bool, len(addresses))
	norm := make([]string, 0, len(addresses))
	for _, a := range addresses {
		n := NormalizeAddress(a)
		if n == "" || seen[n] {
			continue
		}
		seen[n] = true
		norm = append(norm, n)
	}
	out := map[string][]string{}
	for start := 0; start < len(norm); start += suppressionLookupChunk {
		chunk := norm[start:min(start+suppressionLookupChunk, len(norm))]
		byHash := make(map[string]string, len(chunk))
		hashes := make([]string, 0, len(chunk))
		for _, n := range chunk {
			h := StoredAddressHash(n)
			byHash[h] = n
			hashes = append(hashes, h)
		}
		var rows []struct {
			AddressHash string
			Reason      string
		}
		if err := db.Raw(openSuppressionsSQL, hashes).Scan(&rows).Error; err != nil {
			return nil, err
		}
		for _, r := range rows {
			if n, ok := byHash[r.AddressHash]; ok {
				out[n] = append(out[n], r.Reason)
			}
		}
	}
	return out, nil
}
