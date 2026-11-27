// Package people reads the people table, where every name and email lives
// (AGENTS.md, structural choice 3).
package people

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base32"
	"errors"
	"strings"
	"time"

	"fold/internal/db"
	"fold/internal/policy"
)

type Status string

const (
	// StatusSetup is the first admin while setup waits for the test email.
	// They cannot sign in.
	StatusSetup Status = "setup"
	// StatusPending is an account request waiting for an admin. It cannot
	// sign in.
	StatusPending  Status = "pending"
	StatusActive   Status = "active"
	StatusDisabled Status = "disabled"
)

type Person struct {
	ID            int64
	UID           string
	Name          string
	Email         string
	Status        Status
	IsAdmin       bool
	Zone          string
	ContactHidden bool // P5: leaders of their groups do not see the address
	Version       int64
}

// Policy is the person in policy's terms.
func (p Person) Policy() *policy.Person {
	return &policy.Person{ID: p.ID, Admin: p.IsAdmin, Inactive: p.Status != StatusActive}
}

const columns = `p.id, p.uid, p.name, p.email, p.status, p.is_admin, p.zone, p.contact_hidden, p.version`

func scan(row interface{ Scan(...any) error }) (Person, error) {
	var p Person
	err := row.Scan(&p.ID, &p.UID, &p.Name, &p.Email, &p.Status, &p.IsAdmin, &p.Zone, &p.ContactHidden, &p.Version)
	return p, err
}

// ErrNotFound is a person who does not exist or may not be loaded.
var ErrNotFound = errors.New("people: not found")

func notFound(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

// Session is a signed-in visit: the person and the session row.
type Session struct {
	ID        int64
	Person    Person
	RenewedAt time.Time
	ExpiresAt time.Time
}

// BySession loads the person behind a live session. An ended or expired
// session, or a person who is no longer active, is ErrNotFound.
func BySession(ctx context.Context, q db.Querier, tokenHash []byte, now time.Time) (Session, error) {
	row := q.QueryRowContext(ctx, `SELECT s.id, s.renewed_at, s.expires_at, `+columns+`
		FROM sessions s JOIN people p ON p.id = s.person_id
		WHERE s.token_hash = ? AND s.ended_at IS NULL AND s.expires_at > ? AND p.status = 'active'`,
		tokenHash, now.Unix())
	var s Session
	var renewed, expires int64
	err := row.Scan(&s.ID, &renewed, &expires, &s.Person.ID, &s.Person.UID, &s.Person.Name, &s.Person.Email,
		&s.Person.Status, &s.Person.IsAdmin, &s.Person.Zone, &s.Person.ContactHidden, &s.Person.Version)
	if err != nil {
		return Session{}, notFound(err)
	}
	s.RenewedAt, s.ExpiresAt = time.Unix(renewed, 0), time.Unix(expires, 0)
	return s, nil
}

func ByID(ctx context.Context, q db.Querier, id int64) (Person, error) {
	p, err := scan(q.QueryRowContext(ctx, `SELECT `+columns+` FROM people p WHERE p.id = ?`, id))
	return p, notFound(err)
}

// ByEmail looks up an address after NormalizeEmail.
func ByEmail(ctx context.Context, q db.Querier, email string) (Person, error) {
	p, err := scan(q.QueryRowContext(ctx, `SELECT `+columns+` FROM people p WHERE p.email = ?`, email))
	return p, notFound(err)
}

// NormalizeEmail lower-cases and trims an address. SQLite's NOCASE covers
// ASCII only, so every address is stored and compared in this form.
func NormalizeEmail(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// NewUID is a person's random identifier: 128 bits, so it is never reused.
func NewUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b[:]))
}
