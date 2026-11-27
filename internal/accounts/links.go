// Package accounts holds the commands and reads for setup, sign-in and
// sessions.
package accounts

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"time"

	"fold/internal/db"
	"fold/internal/policy"
	"fold/internal/write"
)

const (
	// LinkLife is how long an emailed link works (R8).
	LinkLife = 24 * time.Hour
	// SessionLife is how long a session lasts without a visit (R8).
	SessionLife = 7 * 24 * time.Hour
	// RenewEvery is how often a visit renews a session. Renewing writes a row
	// and an audit row, so it happens at most hourly; a session therefore
	// lasts a week from the last visit, give or take an hour.
	RenewEvery = time.Hour

	// JobLinkMail emails a link. Its ref is the link id and its secret the
	// token, erased when the job ends.
	JobLinkMail = "mail.link"
)

// NewToken is 32 random bytes, URL-safe.
func NewToken() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b[:])
}

// Hash is what the database stores for a token: links and sessions are
// stored hashed, so a copy of the database signs nobody in.
func Hash(token string) []byte {
	h := sha256.Sum256([]byte(token))
	return h[:]
}

// ValidTokenShape refuses anything that cannot be a token before any lookup.
func ValidTokenShape(s string) bool {
	if len(s) != 43 {
		return false
	}
	_, err := base64.RawURLEncoding.DecodeString(s)
	return err == nil
}

type linkRow struct {
	ID       int64
	PersonID int64
	Email    string // "" if the person row is gone
	facts    policy.Link
}

func loadLink(ctx context.Context, q db.Querier, token string) (linkRow, error) {
	var l linkRow
	var expires int64
	var used, cancelled sql.NullInt64
	var email sql.NullString
	err := q.QueryRowContext(ctx, `SELECT l.id, l.person_id, l.purpose, l.expires_at, l.used_at, l.cancelled_at, p.email
		FROM links l LEFT JOIN people p ON p.id = l.person_id WHERE l.token_hash = ?`,
		Hash(token)).Scan(&l.ID, &l.PersonID, &l.facts.Purpose, &expires, &used, &cancelled, &email)
	if errors.Is(err, sql.ErrNoRows) {
		return linkRow{}, nil
	}
	if err != nil {
		return linkRow{}, err
	}
	l.facts.Found = true
	l.Email = email.String
	l.facts.Used, l.facts.Cancelled = used.Valid, cancelled.Valid
	l.facts.ExpiresAt = time.Unix(expires, 0)
	return l, nil
}

// LinkInfo is what the page behind an emailed link needs before acting: the
// purpose, whether the link can still be used, and the address it was sent
// to, which the same-device check compares (R26).
type LinkInfo struct {
	Purpose string
	Usable  bool
	Email   string
}

// LookUpLink reads a link without using it.
func LookUpLink(ctx context.Context, q db.Querier, token string, now time.Time) (LinkInfo, error) {
	if !ValidTokenShape(token) {
		return LinkInfo{}, nil
	}
	l, err := loadLink(ctx, q, token)
	if err != nil || !l.facts.Found {
		return LinkInfo{}, err
	}
	return LinkInfo{Purpose: l.facts.Purpose, Usable: policy.LinkUsable(l.facts, l.facts.Purpose, now), Email: l.Email}, nil
}

// issueLink cancels the person's earlier links of the same purpose (asking for
// a new one cancels the old, R8), creates a new one and queues its email.
func issueLink(tx *write.Tx, personID int64, purpose string) (int64, error) {
	rows, err := tx.Query(`SELECT id FROM links WHERE person_id = ? AND purpose = ? AND used_at IS NULL AND cancelled_at IS NULL`, personID, purpose)
	if err != nil {
		return 0, err
	}
	var old []int64
	for rows.Next() {
		var id int64
		rows.Scan(&id)
		old = append(old, id)
	}
	rows.Close()
	for _, id := range old {
		if _, err := tx.Exec(`UPDATE links SET cancelled_at = ? WHERE id = ?`, tx.Now.Unix(), id); err != nil {
			return 0, err
		}
		if err := tx.CancelJobs(JobLinkMail, id); err != nil {
			return 0, err
		}
	}
	token := NewToken()
	res, err := tx.Exec(`INSERT INTO links (purpose, person_id, token_hash, created_at, expires_at) VALUES (?, ?, ?, ?, ?)`,
		purpose, personID, Hash(token), tx.Now.Unix(), tx.Now.Add(LinkLife).Unix())
	if err != nil {
		return 0, err
	}
	id, _ := res.LastInsertId()
	if err := tx.Enqueue(write.NewJob{Kind: JobLinkMail, Ref: id, Secret: []byte(token)}); err != nil {
		return 0, err
	}
	tx.Detail("purpose", purpose)
	if len(old) > 0 {
		tx.Detail("replaced", "1")
	}
	return id, nil
}

// useLink marks a link used, inside the write that acts on it.
func useLink(tx *write.Tx, id int64) error {
	_, err := tx.Exec(`UPDATE links SET used_at = ? WHERE id = ? AND used_at IS NULL`, tx.Now.Unix(), id)
	return err
}

// startSession creates a session and returns its token, for the cookie.
func startSession(tx *write.Tx, personID int64) (string, error) {
	token := NewToken()
	_, err := tx.Exec(`INSERT INTO sessions (token_hash, person_id, created_at, renewed_at, expires_at) VALUES (?, ?, ?, ?, ?)`,
		Hash(token), personID, tx.Now.Unix(), tx.Now.Unix(), tx.Now.Add(SessionLife).Unix())
	return token, err
}
