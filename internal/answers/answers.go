// Package answers is the one place answers to dates change (docs/HAZARDS.md
// section 7). Every write names its move from policy's table and the dates
// it touches, inside the caller's command. Seats are counted here, and
// Settle is the one place anyone moves up the waitlist. It imports neither
// events nor groups, so both can call it in the same write.
package answers

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"fold/internal/db"
	"fold/internal/notices"
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

func ids(list []int64) []any {
	out := make([]any, len(list))
	for i, id := range list {
		out[i] = id
	}
	return out
}

func scanIDs(tx *write.Tx, query string, args ...any) ([]int64, error) {
	rows, err := tx.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// Result is what Set wrote.
type Result struct {
	Written int // answers changed
	Waiting int // of them, dates that were full, so the Going waits
}

// Set records a person's own answer on dates (MoveAnswer). A Going on a
// date with no seat left waits instead, keeping an earlier place in line; a
// Going that holds a seat keeps it whatever the limit became. Moving off
// Going frees the seat, and Settle moves the next person up. The caller has
// asked policy.CanAnswer for each date.
func Set(tx *write.Tx, personID int64, dateIDs []int64, to string) (Result, error) {
	if !policy.IsAnswer(to) {
		return Result{}, ErrNotAnAnswer
	}
	if len(dateIDs) == 0 {
		return Result{}, nil
	}
	rows, err := tx.Query(`SELECT d.id, coalesce(e.seats, 0), coalesce(a.status, ''),
		(SELECT count(*) FROM counted_answers c WHERE c.date_id = d.id AND c.status = 'going' AND c.person_id <> ?)
		FROM dates d JOIN events e ON e.id = d.event_id LEFT JOIN answers a ON a.date_id = d.id AND a.person_id = ?
		WHERE d.id IN (`+placeholders(len(dateIDs))+`)`, append([]any{personID, personID}, ids(dateIDs)...)...)
	if err != nil {
		return Result{}, err
	}
	by := map[string][]int64{}
	var freed []int64
	for rows.Next() {
		var id int64
		var seats, others int
		var cur string
		if err := rows.Scan(&id, &seats, &cur, &others); err != nil {
			rows.Close()
			return Result{}, err
		}
		next := to
		if to == policy.AnswerGoing && cur != policy.AnswerGoing && !policy.HasSeat(seats, others) {
			next = policy.AnswerWaiting
		}
		if !policy.MoveAnswer.Allows(cur, next) {
			continue
		}
		by[next] = append(by[next], id)
		if cur == policy.AnswerGoing && next != policy.AnswerGoing {
			freed = append(freed, id)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return Result{}, err
	}
	var r Result
	for next, list := range by {
		since := any(nil)
		if next == policy.AnswerWaiting {
			since = tx.Now.Unix()
		}
		res, err := tx.Exec(`INSERT INTO answers (date_id, person_id, status, waiting_since, created_at, updated_at)
			SELECT d.id, ?, ?, ?, ?, ? FROM dates d WHERE d.id IN (`+placeholders(len(list))+`)
			ON CONFLICT (date_id, person_id) DO UPDATE SET status = excluded.status, updated_at = excluded.updated_at,
			waiting_since = CASE WHEN answers.status = 'waiting' AND excluded.status = 'waiting' THEN answers.waiting_since ELSE excluded.waiting_since END
			WHERE answers.status IN (`+quoted(policy.MoveAnswer.From())+`)`,
			append([]any{personID, next, since, tx.Now.Unix(), tx.Now.Unix()}, ids(list)...)...)
		if err != nil {
			return Result{}, err
		}
		n, _ := res.RowsAffected()
		r.Written += int(n)
		if next == policy.AnswerWaiting {
			r.Waiting += int(n)
		}
	}
	return r, Settle(tx, freed)
}

// Settle moves the waitlist up on each date: while the date has a free seat
// and someone is waiting, the earliest waiting person goes (DEC-84). Every
// change that frees a seat or adds room calls it in the same write, once
// per command, with every date it touched; on a date with no free seat it
// moves nobody. Nobody moves up on a date that is cancelled or in a deleted
// event, or within policy.Cutoff of its start (R45), and nobody who can no
// longer see the date moves up. Each person moved up is told
// once, by bell and email (R36), naming their first date; the command's
// audit row names them and counts the answers moved.
func Settle(tx *write.Tx, dateIDs []int64) error {
	type first struct {
		eventID, dateID int64
		starts          int64
	}
	told := map[int64]first{}
	var order []int64
	moved := 0
	for _, dateID := range dateIDs {
		var seats, going int
		var starts, eventID int64
		var active, cancelled bool
		if err := tx.QueryRow(`SELECT coalesce(e.seats, 0), d.starts_at, e.id, e.status = 'active', d.status = 'cancelled',
			(SELECT count(*) FROM counted_answers c WHERE c.date_id = d.id AND c.status = 'going')
			FROM dates d JOIN events e ON e.id = d.event_id WHERE d.id = ?`, dateID).Scan(&seats, &starts, &eventID, &active, &cancelled, &going); err != nil {
			return err
		}
		if !policy.CanPromote(active, cancelled, time.Unix(starts, 0), tx.Now) {
			continue
		}
		e, err := eventOf(tx.Context(), tx, eventID)
		if err != nil {
			return err
		}
		waiting, err := signed(tx.Context(), tx, e, []string{policy.AnswerWaiting}, `d.id = ?`, "a.waiting_since, a.id", dateID)
		if err != nil {
			return err
		}
		// Someone kept on the list who can no longer see the date (R3)
		// keeps their place, and the seat goes to someone who can come.
		var queue []int64
		for _, w := range waiting {
			if w.Sees(e, e.Visibility, w.DateVis) {
				queue = append(queue, w.PersonID)
			}
		}
		up := policy.Promote(seats, going, queue)
		if len(up) == 0 {
			continue
		}
		res, err := tx.Exec(`UPDATE answers SET status = 'going', waiting_since = NULL, updated_at = ?
			WHERE date_id = ? AND status IN (`+quoted(policy.MovePromote.From())+`) AND person_id IN (`+placeholders(len(up))+`)`,
			append([]any{tx.Now.Unix(), dateID}, ids(up)...)...)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		moved += int(n)
		for _, p := range up {
			f, seen := told[p]
			if !seen {
				order = append(order, p)
			}
			if !seen || starts < f.starts {
				told[p] = first{eventID, dateID, starts}
			}
		}
	}
	if moved == 0 {
		return nil
	}
	var ns []notices.New
	for _, p := range order {
		f := told[p]
		ns = append(ns, notices.New{PersonID: p, Kind: notices.Promoted, EventID: f.eventID, DateID: f.dateID, Answer: policy.AnswerGoing})
		var uid string
		if err := tx.QueryRow(`SELECT uid FROM people WHERE id = ?`, p).Scan(&uid); err != nil {
			return err
		}
		tx.Subject(uid)
	}
	tx.Detail("promoted", strconv.Itoa(moved))
	return notices.AddMany(tx, ns)
}

// goingOn lists the dates among a query's that a person holds a seat on.
func goingOn(tx *write.Tx, personID int64, where string, args ...any) ([]int64, error) {
	return scanIDs(tx, `SELECT a.date_id FROM answers a JOIN dates d ON d.id = a.date_id
		WHERE a.person_id = ? AND a.status = 'going' AND `+where, append([]any{personID}, args...)...)
}

// Withdraw takes back a person's answers on a group's dates that have not
// started, when they leave the group or are removed from it
// (MoveLeaveGroup, DEC-74). Past answers are history and stay. It also takes
// back answers on dates cancelled for now, so bringing one back later does
// not bring back someone who left. Each seat it frees moves the waitlist up.
// It returns how many dates still on it took them off: the same count the
// leave confirmation states (StandingOnGroup), whatever else it tidied.
func Withdraw(tx *write.Tx, groupID, personID int64) (int, error) {
	n, err := StandingOnGroup(tx.Context(), tx, groupID, personID, tx.Now)
	if err != nil {
		return 0, err
	}
	const ofGroup = `d.starts_at > ? AND d.event_id IN (SELECT id FROM events WHERE group_id = ?)`
	freed, err := goingOn(tx, personID, ofGroup, tx.Now.Unix(), groupID)
	if err != nil {
		return 0, err
	}
	if _, err = tx.Exec(`UPDATE answers SET status = 'cancelled', waiting_since = NULL, updated_at = ?
		WHERE person_id = ? AND status IN (`+quoted(policy.MoveLeaveGroup.From())+`)
		AND date_id IN (SELECT d.id FROM dates d WHERE `+ofGroup+`)`,
		tx.Now.Unix(), personID, tx.Now.Unix(), groupID); err != nil {
		return 0, err
	}
	return n, Settle(tx, freed)
}

// Remove takes a person off dates that have not begun, as those running the
// event choose (MoveRemove, DEC-84): their answer is taken back, and the
// next waiting person moves up only where that frees a seat. The caller has
// asked policy.CanRemoveAnswer. It returns how many answers it took back,
// and the first of their dates it took them off.
func Remove(tx *write.Tx, personID int64, dateIDs []int64) (int, int64, error) {
	if len(dateIDs) == 0 {
		return 0, 0, nil
	}
	in := `d.starts_at > ? AND d.id IN (` + placeholders(len(dateIDs)) + `)`
	args := append([]any{tx.Now.Unix()}, ids(dateIDs)...)
	var first int64
	if err := tx.QueryRow(`SELECT coalesce((SELECT d.id FROM answers a JOIN dates d ON d.id = a.date_id
		WHERE a.person_id = ? AND a.status IN (`+quoted(policy.MoveRemove.From())+`) AND `+in+` ORDER BY d.starts_at, d.id LIMIT 1), 0)`,
		append([]any{personID}, args...)...).Scan(&first); err != nil {
		return 0, 0, err
	}
	freed, err := goingOn(tx, personID, in, args...)
	if err != nil {
		return 0, 0, err
	}
	res, err := tx.Exec(`UPDATE answers SET status = 'cancelled', waiting_since = NULL, updated_at = ?
		WHERE person_id = ? AND status IN (`+quoted(policy.MoveRemove.From())+`) AND date_id IN (SELECT d.id FROM dates d WHERE `+in+`)`,
		append([]any{tx.Now.Unix(), personID}, args...)...)
	if err != nil {
		return 0, 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), first, Settle(tx, freed)
}

// GuestCancel takes back a guest's own Going or place in line on one date
// (MoveGuestCancel, DEC-55); a seat it frees moves the next person up. The
// caller has asked policy.GuestCanCancel. It reports whether it changed
// anything.
func GuestCancel(tx *write.Tx, personID, dateID int64) (bool, error) {
	var was string
	if err := tx.QueryRow(`SELECT coalesce((SELECT status FROM answers WHERE person_id = ? AND date_id = ?), '')`, personID, dateID).Scan(&was); err != nil {
		return false, err
	}
	if !policy.MoveGuestCancel.Allows(was, policy.AnswerCancelled) {
		return false, nil
	}
	if _, err := tx.Exec(`UPDATE answers SET status = 'cancelled', waiting_since = NULL, updated_at = ? WHERE person_id = ? AND date_id = ?`,
		tx.Now.Unix(), personID, dateID); err != nil {
		return false, err
	}
	if was != policy.AnswerGoing {
		return true, nil
	}
	return true, Settle(tx, []int64{dateID})
}

// Disabled moves the waitlist up on every date still to come that a
// person, whose account was just turned off, held a seat on: their answers
// stay, hidden and not counted (DEC-38). Call it after the account's
// status changes.
func Disabled(tx *write.Tx, personID int64) error {
	freed, err := goingOn(tx, personID, `d.starts_at > ?`, tx.Now.Unix())
	if err != nil {
		return err
	}
	return Settle(tx, freed)
}

// Restored brings a person's answers back when their account is turned on
// again (DEC-38, DEC-47). Each comes back as it was where there is room: a
// Going on a date still to come that is now full is dropped (MoveNoRoom),
// and the person is told once to ask a leader. A place on a waitlist comes
// back where it was, and moves up if a seat is free. Call it after the
// account's status changes. It returns how many answers it dropped.
func Restored(tx *write.Tx, personID int64) (int, error) {
	rows, err := tx.Query(`SELECT d.id, d.event_id, coalesce(e.seats, 0),
		(SELECT count(*) FROM counted_answers c WHERE c.date_id = d.id AND c.status = 'going' AND c.person_id <> ?)
		FROM answers a JOIN dates d ON d.id = a.date_id JOIN events e ON e.id = d.event_id
		WHERE a.person_id = ? AND a.status = 'going' AND d.starts_at > ? AND d.status = 'scheduled' AND e.status = 'active'
		ORDER BY d.starts_at, d.id`, personID, personID, tx.Now.Unix())
	if err != nil {
		return 0, err
	}
	var full []int64
	var firstEvent int64
	for rows.Next() {
		var dateID, eventID int64
		var seats, others int
		if err := rows.Scan(&dateID, &eventID, &seats, &others); err != nil {
			rows.Close()
			return 0, err
		}
		if !policy.HasSeat(seats, others) {
			if len(full) == 0 {
				firstEvent = eventID
			}
			full = append(full, dateID)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if len(full) > 0 {
		if _, err := tx.Exec(`UPDATE answers SET status = 'cancelled', updated_at = ? WHERE person_id = ? AND status IN (`+quoted(policy.MoveNoRoom.From())+`)
			AND date_id IN (`+placeholders(len(full))+`)`, append([]any{tx.Now.Unix(), personID}, ids(full)...)...); err != nil {
			return 0, err
		}
		tx.Detail("dropped", strconv.Itoa(len(full)))
		if err := notices.Add(tx, notices.New{PersonID: personID, Kind: notices.NoRoom, EventID: firstEvent, DateID: full[0]}); err != nil {
			return 0, err
		}
	}
	waiting, err := scanIDs(tx, `SELECT a.date_id FROM answers a JOIN dates d ON d.id = a.date_id
		WHERE a.person_id = ? AND a.status = 'waiting' AND d.starts_at > ?`, personID, tx.Now.Unix())
	if err != nil {
		return 0, err
	}
	return len(full), Settle(tx, waiting)
}

// Counts are a date's answers that count: active accounts' own answers, and
// their places on the waitlist.
type Counts struct{ Going, Maybe, Cant, Waiting int }

// CountsFor counts a date's answers. Callers show Cant only to those
// policy.CanSeeCantGo allows; withCant false leaves it unread.
func CountsFor(ctx context.Context, q db.Querier, dateID int64, withCant bool) (Counts, error) {
	var c Counts
	err := q.QueryRowContext(ctx, `SELECT
		coalesce(sum(status = 'going'), 0), coalesce(sum(status = 'maybe'), 0), coalesce(sum(status = 'cant' AND ?), 0),
		coalesce(sum(status = 'waiting'), 0)
		FROM counted_answers WHERE date_id = ?`, withCant, dateID).Scan(&c.Going, &c.Maybe, &c.Cant, &c.Waiting)
	return c, err
}

// Named is one person with an answer: their public name, never their
// address, and their id for the people running the event.
type Named struct{ UID, Name string }

// Names are the people with one answer on a date, by name; the waitlist is
// in its order.
func Names(ctx context.Context, q db.Querier, dateID int64, status string) ([]Named, error) {
	order := "p.name COLLATE NOCASE"
	if status == policy.AnswerWaiting {
		order = "a.waiting_since, a.id"
	}
	rows, err := q.QueryContext(ctx, `SELECT p.uid, p.name FROM counted_answers a JOIN people p ON p.id = a.person_id
		WHERE a.date_id = ? AND a.status = ? ORDER BY `+order, dateID, status)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Named
	for rows.Next() {
		var n Named
		if err := rows.Scan(&n.UID, &n.Name); err != nil {
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

// Place is a person's number on a date's waitlist, from 1, or 0 when they
// are not waiting.
func Place(ctx context.Context, q db.Querier, dateID, personID int64) (int, error) {
	var n int
	err := q.QueryRowContext(ctx, `SELECT coalesce((SELECT count(*) FROM counted_answers o, counted_answers me
		WHERE me.date_id = ?1 AND me.person_id = ?2 AND me.status = 'waiting' AND o.date_id = ?1 AND o.status = 'waiting'
		AND (o.waiting_since < me.waiting_since OR (o.waiting_since = me.waiting_since AND o.id <= me.id))), 0)`, dateID, personID).Scan(&n)
	return n, err
}

// StandingOnGroup counts a person's answers on a group's dates that are on
// and have not started, in events not deleted: the dates leaving takes them
// off, as the leave confirmation states it and the audit row records it
// (docs/HAZARDS.md section 7). A member of an active group sees all of
// these; nobody else is offered the leave confirmation.
func StandingOnGroup(ctx context.Context, q db.Querier, groupID, personID int64, now time.Time) (int, error) {
	var n int
	err := q.QueryRowContext(ctx, `SELECT count(*) FROM answers a JOIN dates d ON d.id = a.date_id JOIN events e ON e.id = d.event_id
		WHERE a.person_id = ? AND a.status IN (`+quoted(policy.MoveLeaveGroup.From())+`) AND e.group_id = ? AND d.starts_at > ?
		AND d.status = 'scheduled' AND e.status = 'active'`,
		personID, groupID, now.Unix()).Scan(&n)
	return n, err
}

// GoingOrMaybe reports whether a person said Going or Maybe to any date of
// an event that has not ended: what shows them its video link on the event
// page (R41). A place on the waitlist is not a seat.
func GoingOrMaybe(ctx context.Context, q db.Querier, eventID, personID int64, now time.Time) (bool, error) {
	var yes bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM answers a JOIN dates d ON d.id = a.date_id
		WHERE d.event_id = ? AND a.person_id = ? AND a.status IN ('going', 'maybe') AND d.status = 'scheduled' AND d.ends_at > ?)`,
		eventID, personID, now.Unix()).Scan(&yes)
	return yes, err
}
