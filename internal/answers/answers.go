// Package answers is the one place answers to dates change (docs/HAZARDS.md
// section 7). Every write names its move from policy's table and the dates
// it touches, inside the caller's command; seat counting and moving the
// waitlist up (milestone 2b) belong here. It imports neither events nor
// groups, so both can call it in the same write.
package answers

import (
	"context"
	"errors"
	"strings"
	"time"

	"fold/internal/db"
	"fold/internal/policy"
	"fold/internal/write"
)

// ErrNotAnAnswer is a state a person may not choose.
var ErrNotAnAnswer = errors.New("answers: not an answer")

func placeholders(n int) string { return strings.TrimSuffix(strings.Repeat("?, ", n), ", ") }

func quoted(states []string) string {
	q := make([]string, len(states))
	for i, s := range states {
		q[i] = "'" + s + "'"
	}
	return strings.Join(q, ", ")
}

// Set records a person's own answer on dates, in one statement (MoveAnswer).
// The caller has asked policy.CanAnswer for each date. It returns how many
// answers it wrote.
func Set(tx *write.Tx, personID int64, dateIDs []int64, to string) (int, error) {
	if !policy.IsAnswer(to) {
		return 0, ErrNotAnAnswer
	}
	if len(dateIDs) == 0 {
		return 0, nil
	}
	args := []any{personID, to, tx.Now.Unix(), tx.Now.Unix()}
	for _, id := range dateIDs {
		args = append(args, id)
	}
	// Only states the move starts from are overwritten; "" is no row yet.
	res, err := tx.Exec(`INSERT INTO answers (date_id, person_id, status, created_at, updated_at)
		SELECT d.id, ?, ?, ?, ? FROM dates d WHERE d.id IN (`+placeholders(len(dateIDs))+`)
		ON CONFLICT (date_id, person_id) DO UPDATE SET status = excluded.status, updated_at = excluded.updated_at
		WHERE answers.status IN (`+quoted(policy.MoveAnswer.From())+`)`, args...)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// Withdraw takes back a person's standing answers on a group's dates that
// have not started, when they leave the group or are removed from it
// (MoveLeaveGroup, DEC-74). Past answers are history and stay.
func Withdraw(tx *write.Tx, groupID, personID int64) (int, error) {
	res, err := tx.Exec(`UPDATE answers SET status = 'cancelled', updated_at = ?
		WHERE person_id = ? AND status IN (`+quoted(policy.MoveLeaveGroup.From())+`)
		AND date_id IN (SELECT d.id FROM dates d JOIN events e ON e.id = d.event_id WHERE e.group_id = ? AND d.starts_at > ?)`,
		tx.Now.Unix(), personID, groupID, tx.Now.Unix())
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// Counts are a date's answers that count: active accounts' own answers.
type Counts struct{ Going, Maybe, Cant int }

// CountsFor counts a date's answers. Callers show Cant only to those
// policy.CanSeeCantGo allows; withCant false leaves it unread.
func CountsFor(ctx context.Context, q db.Querier, dateID int64, withCant bool) (Counts, error) {
	var c Counts
	err := q.QueryRowContext(ctx, `SELECT
		coalesce(sum(status = 'going'), 0), coalesce(sum(status = 'maybe'), 0), coalesce(sum(status = 'cant' AND ?), 0)
		FROM counted_answers WHERE date_id = ?`, withCant, dateID).Scan(&c.Going, &c.Maybe, &c.Cant)
	return c, err
}

// Names are the public names of the people with one answer on a date, by
// name. Never their addresses.
func Names(ctx context.Context, q db.Querier, dateID int64, status string) ([]string, error) {
	rows, err := q.QueryContext(ctx, `SELECT p.name FROM counted_answers a JOIN people p ON p.id = a.person_id
		WHERE a.date_id = ? AND a.status = ? ORDER BY p.name COLLATE NOCASE`, dateID, status)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// Mine is a person's own answer on a date, or "" when they have none.
func Mine(ctx context.Context, q db.Querier, dateID, personID int64) (string, error) {
	var s string
	err := q.QueryRowContext(ctx, `SELECT coalesce((SELECT status FROM answers WHERE date_id = ? AND person_id = ?), '')`, dateID, personID).Scan(&s)
	return s, err
}

// StandingOnGroup counts a person's standing answers on a group's dates that
// have not started: what leaving it would take back.
func StandingOnGroup(ctx context.Context, q db.Querier, groupID, personID int64, now time.Time) (int, error) {
	var n int
	err := q.QueryRowContext(ctx, `SELECT count(*) FROM answers a JOIN dates d ON d.id = a.date_id JOIN events e ON e.id = d.event_id
		WHERE a.person_id = ? AND a.status IN (`+quoted(policy.MoveLeaveGroup.From())+`) AND e.group_id = ? AND d.starts_at > ?`,
		personID, groupID, now.Unix()).Scan(&n)
	return n, err
}

// GoingOrMaybe reports whether a person said Going or Maybe to any date of
// an event that has not ended: what shows them its video link on the event
// page (R41).
func GoingOrMaybe(ctx context.Context, q db.Querier, eventID, personID int64, now time.Time) (bool, error) {
	var yes bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM answers a JOIN dates d ON d.id = a.date_id
		WHERE d.event_id = ? AND a.person_id = ? AND a.status IN ('going', 'maybe') AND d.status = 'scheduled' AND d.ends_at > ?)`,
		eventID, personID, now.Unix()).Scan(&yes)
	return yes, err
}
