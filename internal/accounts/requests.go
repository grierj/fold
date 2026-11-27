package accounts

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"fold/internal/db"
	"fold/internal/notices"
	"fold/internal/people"
	"fold/internal/policy"
	"fold/internal/write"
)

// RequestAccount is the public "ask for an account" form, run from the queue
// so the answer says nothing about the address (docs/HAZARDS.md section 3).
// A new address becomes a person waiting for an admin, every admin gets a
// notice and an email, and the person is told their request is waiting. An
// address with an active account is told so by email instead; that email is
// the only hint, and only its owner reads it. Anything else changes nothing.
type RequestAccount struct {
	Name  string
	Email string
}

func (RequestAccount) Action() string { return "account.request" }

func (c RequestAccount) Apply(tx *write.Tx) error {
	p, err := people.ByEmail(tx.Context(), tx, c.Email)
	switch {
	case err == nil && p.Status == people.StatusActive:
		tx.Subject(p.UID)
		tx.Detail("outcome", "exists")
		return tx.Enqueue(write.NewJob{Kind: JobAccountExists, Ref: p.ID})
	case err == nil:
		return write.ErrNothing // waiting, disabled, or the first admin mid-setup
	case !errors.Is(err, people.ErrNotFound):
		return err
	}
	uid := people.NewUID()
	res, err := tx.Exec(`INSERT INTO people (uid, name, email, status, created_at, updated_at) VALUES (?, ?, ?, 'pending', ?, ?)`,
		uid, c.Name, c.Email, tx.Now.Unix(), tx.Now.Unix())
	if err != nil {
		return err
	}
	id, _ := res.LastInsertId()
	tx.Subject(uid)
	tx.Detail("outcome", "waiting")
	if err := notices.ToAdmins(tx, notices.New{Kind: notices.AccountRequested, SubjectID: id}); err != nil {
		return err
	}
	return tx.Enqueue(write.NewJob{Kind: JobRequestReceived, Ref: id})
}

// DecideAccount is an admin approving or declining a waiting request. An
// approved person is sent a sign-in link; a declined one is deleted, with
// nothing about them kept (R27).
type DecideAccount struct {
	PersonUID string
	Approve   bool
}

func (c DecideAccount) Action() string {
	if c.Approve {
		return "account.approve"
	}
	return "account.decline"
}

func (c DecideAccount) Apply(tx *write.Tx) error {
	if !policy.CanDecideAccount(tx.Actor.Person.Policy()) {
		return write.ErrNotAllowed
	}
	var id int64
	err := tx.QueryRow(`SELECT id FROM people WHERE uid = ? AND status = 'pending'`, c.PersonUID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return write.ErrNotAllowed // already decided, or never asked
	}
	if err != nil {
		return err
	}
	tx.Subject(c.PersonUID)
	if c.Approve {
		if _, err := tx.Exec(`UPDATE people SET status = 'active', updated_at = ?, version = version + 1 WHERE id = ?`, tx.Now.Unix(), id); err != nil {
			return err
		}
		_, err := issueLinkAs(tx, id, "signin", JobApprovedMail)
		return err
	}
	// A waiting person cannot sign in, so has no session or link; the deletes
	// say so rather than assume it. Their notices go with them (CASCADE).
	for _, q := range []string{`DELETE FROM sessions WHERE person_id = ?`, `DELETE FROM links WHERE person_id = ?`, `DELETE FROM people WHERE id = ?`} {
		if _, err := tx.Exec(q, id); err != nil {
			return err
		}
	}
	return nil
}

// Request is one waiting account request, for the admin page.
type Request struct {
	UID   string
	Name  string
	Email string
	At    time.Time
}

// Waiting lists the requests waiting for an admin, oldest first.
func Waiting(ctx context.Context, q db.Querier) ([]Request, error) {
	rows, err := q.QueryContext(ctx, `SELECT uid, name, email, created_at FROM people WHERE status = 'pending' ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Request
	for rows.Next() {
		var r Request
		var at int64
		if err := rows.Scan(&r.UID, &r.Name, &r.Email, &at); err != nil {
			return nil, err
		}
		r.At = time.Unix(at, 0)
		out = append(out, r)
	}
	return out, rows.Err()
}
