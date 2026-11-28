package events

import (
	"errors"
	"strconv"

	"fold/internal/answers"
	"fold/internal/policy"
	"fold/internal/write"
)

// Answer is a person's Going, Maybe or Can't go to a date (DEC-63), or, on
// a series date, to it and the dates after it, or to every date still to
// come (DEC-71). Every date it writes is one the person may answer; dates
// added later are not answered (DEC-92). All of them go in one statement.
type Answer struct {
	DateUID string
	Status  string
	Scope   string
	// Confirmed: the person saw how many dates a Can't go for more than one
	// would answer (Fold's 5.4).
	Confirmed bool
	Dates     int // how many dates it answers
}

// ErrConfirmCantGo is a Can't go for several dates not yet confirmed.
// Nothing is written; Answer.Dates says how many.
var ErrConfirmCantGo = errors.New("events: confirm can't go for several dates")

func (*Answer) Action() string { return "answer.set" }

func (c *Answer) Apply(tx *write.Tx) error {
	if !policy.IsAnswer(c.Status) || !policy.ValidScope(c.Scope) {
		return write.ErrNotAllowed
	}
	d, e, err := DateByUID(tx.Context(), tx, c.DateUID)
	if errors.Is(err, ErrNotFound) {
		return write.ErrNotAllowed
	}
	if err != nil {
		return err
	}
	g, rel, err := groupOf(tx, e.GroupID)
	if err != nil {
		return err
	}
	p := actor(tx)
	host := false
	if e.ChurchWide() && p != nil {
		if host, err = IsHost(tx.Context(), tx, e.ID, p.ID); err != nil {
			return err
		}
	}
	ev := e.Policy(host)
	if !policy.CanSeeDate(p, ev, d.Visibility, g.Policy(), rel) || rel.Removed {
		return write.ErrNotAllowed
	}
	if !policy.CanAnswer(p, ev, d.Visibility, g.Policy(), rel, d.Cancelled, !d.EndsAt.After(tx.Now)) {
		return ErrDateGone
	}
	ids := []int64{d.ID}
	if c.Scope != policy.ScopeDate {
		if !policy.CanAnswerSeries(p, ev, g.Policy(), rel, e.Repeat != Once) {
			return write.ErrNotAllowed
		}
		// Only dates on now are answered. One cancelled now and brought back
		// later comes back unanswered, like a date added later (DEC-92).
		from := d.StartsAt
		if c.Scope == policy.ScopeAll {
			from = tx.Now
		}
		rows, err := tx.Query(`SELECT `+dateColumns+` FROM dates d WHERE d.event_id = ? AND d.status = 'scheduled'
			AND d.ends_at > ? AND d.starts_at >= ? AND d.id <> ? ORDER BY d.starts_at`, e.ID, tx.Now.Unix(), from.Unix(), d.ID)
		if err != nil {
			return err
		}
		for rows.Next() {
			x, err := scanDate(rows)
			if err != nil {
				rows.Close()
				return err
			}
			if policy.CanAnswer(p, ev, x.Visibility, g.Policy(), rel, false, false) {
				ids = append(ids, x.ID)
			}
		}
		rows.Close()
	}
	c.Dates = len(ids)
	if c.Status == policy.AnswerCant && len(ids) > 1 && !c.Confirmed {
		return ErrConfirmCantGo
	}
	n, err := answers.Set(tx, p.ID, ids, c.Status)
	if err != nil {
		return err
	}
	tx.Subject(d.UID)
	tx.Subject(e.UID)
	tx.Detail("answer", c.Status)
	tx.Detail("scope", c.Scope)
	tx.Detail("dates", strconv.Itoa(n))
	return nil
}
