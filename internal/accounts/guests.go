package accounts

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"fold/internal/answers"
	"fold/internal/db"
	"fold/internal/events"
	"fold/internal/groups"
	"fold/internal/keys"
	"fold/internal/notices"
	"fold/internal/people"
	"fold/internal/policy"
	"fold/internal/write"
)

// Guests answer a public date by emailed link (R15, R35, DEC-55, DEC-56).
// A stranger's form becomes a confirm link: the name and email wait sealed
// in its row, and no person or answer exists until it is opened. Opening
// it answers Going and sends a manage link for that one date.

const (
	// JobGuestConfirm emails a confirm link. Its ref is the link and its
	// secret the token.
	JobGuestConfirm = "mail.guest_confirm"
	// JobGuestGoing emails the manage link after a confirm. Its ref is the
	// manage link and its secret the token.
	JobGuestGoing = "mail.guest_going"
	// JobGuestCleanup deletes confirm links that expired or were cancelled.
	JobGuestCleanup = "guests.cleanup"

	// MaxGuestPending is how many confirm links one address may have
	// waiting at once; past it a request does nothing and answers as usual.
	MaxGuestPending = 3
)

// sealedGuest is what waits in a confirm link's row.
type sealedGuest struct {
	Name  string `json:"n"`
	Email string `json:"e"`
}

// guestDate loads a date with what deciding about guests needs, inside the
// write.
func guestDate(tx *write.Tx, dateUID string) (events.Row, events.Event, groups.Group, bool, error) {
	d, e, err := events.DateByUID(tx.Context(), tx, dateUID)
	if errors.Is(err, events.ErrNotFound) {
		return d, e, groups.Group{}, false, nil
	}
	if err != nil {
		return d, e, groups.Group{}, false, err
	}
	return dateFacts(tx.Context(), tx, d, e, tx.Now)
}

func dateFacts(ctx context.Context, q db.Querier, d events.Row, e events.Event, now time.Time) (events.Row, events.Event, groups.Group, bool, error) {
	var g groups.Group
	if !e.ChurchWide() {
		var err error
		if g, err = groups.ByID(ctx, q, e.GroupID); err != nil {
			return d, e, g, false, err
		}
	}
	return d, e, g, policy.GuestCanAnswer(e.Policy(false), d.Visibility, g.Policy(), d.Cancelled, !d.EndsAt.After(now)), nil
}

// RequestGuest is the guest form, run from the queue so the form answers
// at once and the same for every address (docs/HAZARDS.md section 3). It
// looks up no person at all: every address gets the same row and the same
// email. Asking again for the same date replaces the waiting link.
type RequestGuest struct {
	Name, Email, DateUID string
}

func (RequestGuest) Action() string { return "guest.request" }

func (c RequestGuest) Apply(tx *write.Tx) error {
	if facts, err := setupFacts(tx); err != nil || !facts.Done {
		if err != nil {
			return err
		}
		return write.ErrNothing
	}
	d, e, _, open, err := guestDate(tx, c.DateUID)
	if err != nil {
		return err
	}
	if !open {
		return write.ErrNothing
	}
	mac := tx.MAC(keys.GuestAddress, []byte(c.Email))
	rows, err := tx.Query(`SELECT id FROM links WHERE purpose = 'guest' AND address_mac = ? AND date_id = ?`, mac, d.ID)
	if err != nil {
		return err
	}
	var old []int64
	for rows.Next() {
		var id int64
		rows.Scan(&id)
		old = append(old, id)
	}
	rows.Close()
	for _, id := range old {
		if _, err := tx.Exec(`DELETE FROM links WHERE id = ?`, id); err != nil {
			return err
		}
		if err := tx.CancelJobs(JobGuestConfirm, id); err != nil {
			return err
		}
	}
	var waiting int
	if err := tx.QueryRow(`SELECT count(*) FROM links WHERE purpose = 'guest' AND address_mac = ? AND expires_at > ? AND cancelled_at IS NULL`,
		mac, tx.Now.Unix()).Scan(&waiting); err != nil {
		return err
	}
	if waiting >= MaxGuestPending {
		return write.ErrNothing
	}
	plain, err := json.Marshal(sealedGuest{Name: c.Name, Email: c.Email})
	if err != nil {
		return err
	}
	token := NewToken()
	res, err := tx.Exec(`INSERT INTO links (purpose, date_id, sealed, address_mac, token_hash, created_at, expires_at) VALUES ('guest', ?, ?, ?, ?, ?, ?)`,
		d.ID, tx.Seal(keys.Guest, plain), mac, Hash(token), tx.Now.Unix(), tx.Now.Add(LinkLife).Unix())
	if err != nil {
		return err
	}
	id, _ := res.LastInsertId()
	tx.Subject(d.UID)
	tx.Subject(e.UID)
	if len(old) > 0 {
		tx.Detail("replaced", "1")
	}
	return tx.Enqueue(write.NewJob{Kind: JobGuestConfirm, Ref: id, Secret: []byte(token)})
}

// openGuest opens a confirm link's sealed name and email.
func openGuest(k interface {
	Open(keys.Purpose, []byte) ([]byte, bool, error)
}, sealed []byte) (sealedGuest, error) {
	var g sealedGuest
	plain, _, err := k.Open(keys.Guest, sealed)
	if err != nil {
		return g, err
	}
	return g, json.Unmarshal(plain, &g)
}

// UseGuestLink opens a confirm link (R35): its row is deleted whatever
// happens next, and the date is asked about again. Outcome says what it
// did (policy.ConfirmGuest). Where it answers, the person is signed in in
// that browser, and a manage link for the date is emailed (DEC-55).
type UseGuestLink struct {
	Token        string
	Outcome      string // set on success
	DateUID      string // set on success
	SessionToken string // set when it answered
}

func (*UseGuestLink) Action() string { return "guest.confirm" }

func (c *UseGuestLink) Apply(tx *write.Tx) error {
	if !ValidTokenShape(c.Token) {
		return write.ErrNotAllowed
	}
	var id, dateID int64
	var sealed []byte
	var expires int64
	var cancelled sql.NullInt64
	err := tx.QueryRow(`SELECT id, date_id, sealed, expires_at, cancelled_at FROM links WHERE token_hash = ? AND purpose = 'guest'`, Hash(c.Token)).
		Scan(&id, &dateID, &sealed, &expires, &cancelled)
	if errors.Is(err, sql.ErrNoRows) {
		return write.ErrNotAllowed
	}
	if err != nil {
		return err
	}
	if !policy.LinkUsable(policy.Link{Found: true, Purpose: "guest", Cancelled: cancelled.Valid, ExpiresAt: time.Unix(expires, 0)}, "guest", tx.Now) {
		return write.ErrNotAllowed
	}
	who, err := openGuest(tx, sealed)
	if err != nil {
		return write.ErrNotAllowed // sealed before a key change: the cleanup deletes it
	}
	if _, err := tx.Exec(`DELETE FROM links WHERE id = ?`, id); err != nil {
		return err
	}
	var duid string
	if err := tx.QueryRow(`SELECT uid FROM dates WHERE id = ?`, dateID).Scan(&duid); err != nil {
		return err
	}
	d, e, g, open, err := guestDate(tx, duid)
	if err != nil {
		return err
	}
	c.DateUID = d.UID
	tx.Subject(d.UID)
	tx.Subject(e.UID)
	p, err := people.ByEmail(tx.Context(), tx, who.Email)
	if err != nil && !errors.Is(err, people.ErrNotFound) {
		return err
	}
	status, memberMay := "", false
	if err == nil {
		status = string(p.Status)
		if p.Status == people.StatusActive {
			rel, host := policy.Rel{}, false
			if !e.ChurchWide() {
				if rel, err = groups.RelOf(tx.Context(), tx, g.ID, p.ID); err != nil {
					return err
				}
			} else if host, err = events.IsHost(tx.Context(), tx, e.ID, p.ID); err != nil {
				return err
			}
			memberMay = policy.CanAnswer(p.Policy(), e.Policy(host), d.Visibility, g.Policy(), rel, d.Cancelled, !d.EndsAt.After(tx.Now))
		}
	}
	c.Outcome = policy.ConfirmGuest(open, status, memberMay)
	tx.Detail("outcome", c.Outcome)
	switch c.Outcome {
	case policy.ConfirmNew:
		uid := people.NewUID()
		res, err := tx.Exec(`INSERT INTO people (uid, name, email, status, created_at, updated_at) VALUES (?, ?, ?, 'guest', ?, ?)`,
			uid, who.Name, who.Email, tx.Now.Unix(), tx.Now.Unix())
		if err != nil {
			return err
		}
		pid, _ := res.LastInsertId()
		p = people.Person{ID: pid, UID: uid, Status: people.StatusGuest}
	case policy.ConfirmAsThem:
	default:
		return nil // the link is gone, and nothing else changed
	}
	tx.Subject(p.UID)
	if _, err := answers.Set(tx, p.ID, []int64{d.ID}, policy.AnswerGoing); err != nil {
		return err
	}
	linkID, token, err := notices.ManageLink(tx, p.ID, d.ID)
	if err != nil {
		return err
	}
	if err := tx.Enqueue(write.NewJob{Kind: JobGuestGoing, Ref: linkID, Secret: []byte(token)}); err != nil {
		return err
	}
	c.SessionToken, err = startSession(tx, p.ID)
	return err
}

// Manage is what a manage link's page shows, read without using it.
type Manage struct {
	Works    bool
	DateUID  string
	PersonID int64
	Answer   string
}

// LookUpManage reads a manage link (DEC-55). Anything but a working one
// comes back as the zero Manage, so every failure answers alike.
func LookUpManage(ctx context.Context, q db.Querier, token string, now time.Time) (Manage, error) {
	if !ValidTokenShape(token) {
		return Manage{}, nil
	}
	var m Manage
	var dateID int64
	var revoked sql.NullInt64
	var status string
	err := q.QueryRowContext(ctx, `SELECT l.person_id, l.date_id, l.cancelled_at, p.status, d.uid,
		coalesce((SELECT a.status FROM answers a WHERE a.person_id = l.person_id AND a.date_id = l.date_id), '')
		FROM links l JOIN people p ON p.id = l.person_id JOIN dates d ON d.id = l.date_id
		WHERE l.token_hash = ? AND l.purpose = 'manage'`, Hash(token)).Scan(&m.PersonID, &dateID, &revoked, &status, &m.DateUID, &m.Answer)
	if errors.Is(err, sql.ErrNoRows) {
		return Manage{}, nil
	}
	if err != nil {
		return Manage{}, err
	}
	d, e, err := events.DateByUID(ctx, q, m.DateUID)
	if err != nil {
		return Manage{}, err
	}
	var g groups.Group
	if !e.ChurchWide() {
		if g, err = groups.ByID(ctx, q, e.GroupID); err != nil {
			return Manage{}, err
		}
	}
	facts := policy.ManageLink{Found: true, Revoked: revoked.Valid, Ended: !d.EndsAt.After(now),
		Visible: policy.CanSeeDate(nil, e.Policy(false), d.Visibility, g.Policy(), policy.Rel{}),
		SignsIn: status == string(people.StatusActive) || status == string(people.StatusGuest), Answer: m.Answer}
	if !policy.ManageLinkWorks(facts) {
		return Manage{}, nil
	}
	m.Works = true
	return m, nil
}

// revokeManage cancels a person's manage links to a date.
func revokeManage(tx *write.Tx, personID, dateID int64) error {
	_, err := tx.Exec(`UPDATE links SET cancelled_at = ? WHERE purpose = 'manage' AND person_id = ? AND date_id = ? AND cancelled_at IS NULL`,
		tx.Now.Unix(), personID, dateID)
	return err
}

// CancelByManageLink is a manage link's Cancel (a POST with a CSRF token,
// never a GET): it takes back the answer, revokes the person's manage links
// to that date, and frees the seat. It signs nobody in.
type CancelByManageLink struct {
	Token   string
	DateUID string // set on success
}

func (*CancelByManageLink) Action() string { return "guest.cancel" }

func (c *CancelByManageLink) Apply(tx *write.Tx) error {
	m, err := LookUpManage(tx.Context(), tx, c.Token, tx.Now)
	if err != nil {
		return err
	}
	if !m.Works {
		return write.ErrNotAllowed
	}
	return cancelGuest(tx, m.PersonID, m.DateUID, &c.DateUID)
}

func cancelGuest(tx *write.Tx, personID int64, dateUID string, out *string) error {
	d, e, err := events.DateByUID(tx.Context(), tx, dateUID)
	if err != nil {
		return err
	}
	changed, err := answers.GuestCancel(tx, personID, d.ID)
	if err != nil {
		return err
	}
	if !changed {
		return write.ErrNotAllowed
	}
	if err := revokeManage(tx, personID, d.ID); err != nil {
		return err
	}
	var uid string
	if err := tx.QueryRow(`SELECT uid FROM people WHERE id = ?`, personID).Scan(&uid); err != nil {
		return err
	}
	*out = d.UID
	tx.Subject(d.UID)
	tx.Subject(e.UID)
	tx.Subject(uid)
	return nil
}

// GuestCancel is a signed-in guest's Cancel on a date page: their own
// answer, on a date a signed-out visitor may still see, that has not ended.
type GuestCancel struct {
	DateUID string
}

func (GuestCancel) Action() string { return "guest.cancel" }

func (c GuestCancel) Apply(tx *write.Tx) error {
	p := tx.Actor.Person
	if p == nil || !p.Guest() {
		return write.ErrNotAllowed
	}
	d, e, g, _, err := guestDate(tx, c.DateUID)
	if err != nil {
		return err
	}
	if d.ID == 0 || !policy.CanSeeDate(nil, e.Policy(false), d.Visibility, g.Policy(), policy.Rel{}) {
		return write.ErrNotAllowed
	}
	mine, err := answers.Mine(tx.Context(), tx, d.ID, p.ID)
	if err != nil {
		return err
	}
	if !policy.GuestCanCancel(mine, !d.EndsAt.After(tx.Now)) {
		return write.ErrNotAllowed
	}
	var out string
	return cancelGuest(tx, p.ID, d.UID, &out)
}

// cleanupGuests deletes confirm links that expired or were cancelled, with
// the sealed name and email in them (R35). It is upkeep (R19).
type cleanupGuests struct{}

func (cleanupGuests) Action() string { return "guest.cleanup" }
func (cleanupGuests) Upkeep()        {}

func (cleanupGuests) Apply(tx *write.Tx) error {
	res, err := tx.Exec(`DELETE FROM links WHERE purpose = 'guest' AND (expires_at <= ? OR cancelled_at IS NOT NULL)`, tx.Now.Unix())
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return write.ErrNothing
	}
	return nil
}

// CleanupGuests is the JobGuestCleanup handler.
func CleanupGuests(e *write.Executor) write.Handler {
	return func(ctx context.Context, _ write.Job) error { return e.Run(ctx, write.System(), cleanupGuests{}) }
}
