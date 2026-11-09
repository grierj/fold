package mail

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
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

// WithSuppressions wraps inner with the suppression check.
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

// IsSuppressed reports whether err is a refused send to a suppressed address.
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
	for _, r := range reasons {
		if r != models.SuppressComplaint || m.Optional || m.Stream == providers.StreamBroadcast {
			return &ErrSuppressed{Reason: r}
		}
	}
	return nil
}

// NormalizeAddress is the one way Fold turns an email address into the form
// it matches against stored addresses: surrounding whitespace trimmed, then
// lower-cased. It is idempotent and total.
func NormalizeAddress(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// StoredAddressHash is the lowercase hex SHA-256 of the exact bytes of an
// address as stored, with no normalizing. It is the value the address_hash
// CHECK on suppressions ties to the stored address on a UTF8 database.
// Total: an empty string hashes its own empty bytes. The hash is unkeyed and
// guessable, a pseudonymous matching value and not encryption.
func StoredAddressHash(stored string) string {
	sum := sha256.Sum256([]byte(stored))
	return hex.EncodeToString(sum[:])
}

// AddressHash is the hash a lookup uses for an address a person or caller
// supplied: StoredAddressHash of NormalizeAddress(s). Callers that look up by
// hash drop empty normalized inputs first; an empty input still hashes. That
// SQL and Go agree on these bytes is proven for UTF8 databases by the
// suppression matching-basis test (R4-S2).
func AddressHash(s string) string { return StoredAddressHash(NormalizeAddress(s)) }

// openSuppressionsSQL reads the open email suppressions for a set of
// normalized addresses and their hashes (R4-S3). A row with a hash matches by
// hash; a legacy row whose hash is still NULL (written by the previous
// release or by out-of-band SQL after migration 00002) matches by its stored
// address. ORDER BY id fixes the order reasons are returned in, which was
// unspecified before R4-S.
const openSuppressionsSQL = `SELECT address, address_hash, reason FROM suppressions
	WHERE channel = 'email' AND cleared_at IS NULL
	AND (address_hash IN ? OR (address_hash IS NULL AND address IN ?)) ORDER BY id`

// suppressionLookupChunk is how many addresses one query carries: two bind
// lists of this size stay well under the 65,535 bind limit.
const suppressionLookupChunk = 500

// OpenSuppressionReasons maps each address to its open email suppression
// reasons, in ascending id order. Each input is normalized with
// NormalizeAddress; empty ones are dropped and duplicates read once. The key
// is the normalized address, and an address with no open row is left out.
// Empty input runs no query.
//
// Both arms match when the normalized input's bytes equal the stored
// address's bytes: the hash arm through the SHA-256 the address_hash CHECK
// ties to the stored bytes, the address arm through SQL equality. On a UTF8
// database with a deterministic libc default collation (proven by the matching
// basis test) those are the rows text equality matched before the hash
// existed; elsewhere only ASCII addresses are claimed (inventory 7.4). The
// hash is computed in Go from the caller's address and never comes from
// request input.
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
			Address     string
			AddressHash *string
			Reason      string
		}
		if err := db.Raw(openSuppressionsSQL, hashes, chunk).Scan(&rows).Error; err != nil {
			return nil, err
		}
		for _, r := range rows {
			key := r.Address
			if r.AddressHash != nil {
				n, ok := byHash[*r.AddressHash]
				if !ok {
					continue
				}
				key = n
			}
			out[key] = append(out[key], r.Reason)
		}
	}
	return out, nil
}
