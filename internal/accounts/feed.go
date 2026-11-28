package accounts

import (
	"context"
	"database/sql"
	"errors"

	"fold/internal/db"
	"fold/internal/people"
	"fold/internal/policy"
	"fold/internal/write"
)

// A person's calendar feed: a standing link that does one thing, show their
// own calendar to a calendar app (AGENTS.md, "Links are secrets"). Only its
// hash is stored, so the link is shown once, when it is made.

// NewFeed turns the feed on, or replaces the link, which stops the old one
// at once. Token is set on success, for the page that shows it; it is never
// written anywhere else.
type NewFeed struct {
	Token    string
	Replaced bool // an earlier link stopped working
}

func (*NewFeed) Action() string { return "feed.new" }

func (c *NewFeed) Apply(tx *write.Tx) error {
	p := tx.Actor.Person
	if p == nil || !policy.CanHaveFeed(p.Policy()) {
		return write.ErrNotAllowed
	}
	var had bool
	if err := tx.QueryRow(`SELECT feed_hash IS NOT NULL FROM people WHERE id = ?`, p.ID).Scan(&had); err != nil {
		return err
	}
	token := NewToken()
	if _, err := tx.Exec(`UPDATE people SET feed_hash = ? WHERE id = ?`, Hash(token), p.ID); err != nil {
		return err
	}
	tx.Subject(p.UID)
	if had {
		tx.Detail("replaced", "yes")
	}
	c.Token, c.Replaced = token, had
	return nil
}

// EndFeed turns the feed off.
type EndFeed struct{}

func (EndFeed) Action() string { return "feed.off" }

func (EndFeed) Apply(tx *write.Tx) error {
	p := tx.Actor.Person
	if p == nil || !policy.CanHaveFeed(p.Policy()) {
		return write.ErrNotAllowed
	}
	if _, err := tx.Exec(`UPDATE people SET feed_hash = NULL WHERE id = ?`, p.ID); err != nil {
		return err
	}
	tx.Subject(p.UID)
	return nil
}

// FeedOwner is the person a feed token belongs to, if the token has the
// shape of one, matches, and its person may have a feed now. Every other
// case is people.ErrNotFound, alike.
func FeedOwner(ctx context.Context, q db.Querier, token string) (people.Person, error) {
	if !ValidTokenShape(token) {
		return people.Person{}, people.ErrNotFound
	}
	var id int64
	err := q.QueryRowContext(ctx, `SELECT id FROM people WHERE feed_hash = ?`, Hash(token)).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return people.Person{}, people.ErrNotFound
	}
	if err != nil {
		return people.Person{}, err
	}
	p, err := people.ByID(ctx, q, id)
	if err != nil {
		return people.Person{}, err
	}
	if !policy.CanHaveFeed(p.Policy()) {
		return people.Person{}, people.ErrNotFound
	}
	return p, nil
}
