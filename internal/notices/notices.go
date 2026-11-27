// Package notices is the bell. A notice stores a kind and ids, written in the
// same transaction as the change (DEC-42); its words are written when it is
// read, through the same visibility rules as any page (P12), so a notice can
// never carry an address or a phone number.
package notices

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"fold/internal/db"
	"fold/internal/policy"
	"fold/internal/write"
)

// Kinds. Each has a wording in word below.
const (
	AccountRequested = "account.requested" // to every admin; subject: the person asking
	GroupRequested   = "group.requested"   // to every admin; actor: who asked; group
	GroupApproved    = "group.approved"    // to the group's leaders
	GroupRejected    = "group.rejected"    // to the group's leaders
	GroupDeleted     = "group.deleted"     // to the group's members
)

// JobMail emails one notice to its person, for the kinds that are required
// mail (Mails). Its ref is the notice id.
const JobMail = "mail.notice"

// MailsPerDay is how many emails of one kind a person gets in a day.
const MailsPerDay = 10

// Mails reports whether a kind is also emailed. Until email choices arrive
// in slice 3 only required account mail goes out.
func Mails(kind string) bool { return kind == AccountRequested }

// New is one notice to add inside a command.
type New struct {
	PersonID  int64 // who it is for
	Kind      string
	ActorID   int64 // 0 when nobody in particular
	SubjectID int64 // the person it is about, 0 if none
	GroupID   int64 // 0 if none
}

func nullID(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}

// Add writes a notice, and queues its email when the kind is mailed.
func Add(tx *write.Tx, n New) error {
	res, err := tx.Exec(`INSERT INTO notices (person_id, kind, actor_id, subject_id, group_id, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		n.PersonID, n.Kind, nullID(n.ActorID), nullID(n.SubjectID), nullID(n.GroupID), tx.Now.Unix())
	if err != nil {
		return err
	}
	if !Mails(n.Kind) {
		return nil
	}
	// Each person gets at most a few emails of one kind a day; past that the
	// bell still counts them. A flood of requests cannot flood an inbox.
	var today int
	if err := tx.QueryRow(`SELECT count(*) FROM notices WHERE person_id = ? AND kind = ? AND created_at > ?`,
		n.PersonID, n.Kind, tx.Now.Add(-24*time.Hour).Unix()).Scan(&today); err != nil {
		return err
	}
	if today > MailsPerDay {
		return nil
	}
	id, _ := res.LastInsertId()
	return tx.Enqueue(write.NewJob{Kind: JobMail, Ref: id})
}

// ToAdmins adds a notice for every active admin.
func ToAdmins(tx *write.Tx, n New) error {
	rows, err := tx.Query(`SELECT id FROM people WHERE is_admin = 1 AND status = 'active'`)
	if err != nil {
		return err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		rows.Scan(&id)
		ids = append(ids, id)
	}
	rows.Close()
	for _, id := range ids {
		n.PersonID = id
		if err := Add(tx, n); err != nil {
			return err
		}
	}
	return nil
}

// Notice is one notice as a person reads it.
type Notice struct {
	ID      int64
	Kind    string
	Text    string // worded now, for this reader
	Href    string // where opening it goes
	Subject string // an email subject, for mailed kinds
	Note    string // an admin's note on a group decision, shown beneath (R31)
	At      time.Time
	Read    bool
}

type row struct {
	id, personID              int64
	kind                      string
	actorID, subjectID, group sql.NullInt64
	at                        int64
	read                      sql.NullInt64
}

// Reader is who is reading: what wording and visibility need.
type Reader struct {
	ID    int64
	Admin bool
}

// List is a person's notices, newest first, worded for them. A notice whose
// subject they may no longer see is left out.
func List(ctx context.Context, q db.Querier, r Reader, limit int) ([]Notice, error) {
	rows, err := q.QueryContext(ctx, `SELECT id, person_id, kind, actor_id, subject_id, group_id, created_at, read_at
		FROM notices WHERE person_id = ? ORDER BY created_at DESC, id DESC LIMIT ?`, r.ID, limit)
	if err != nil {
		return nil, err
	}
	var raw []row
	for rows.Next() {
		var x row
		if err := rows.Scan(&x.id, &x.personID, &x.kind, &x.actorID, &x.subjectID, &x.group, &x.at, &x.read); err != nil {
			rows.Close()
			return nil, err
		}
		raw = append(raw, x)
	}
	rows.Close()
	var out []Notice
	for _, x := range raw {
		n, ok, err := word(ctx, q, r, x)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, n)
		}
	}
	return out, nil
}

// adminOnly are the kinds only an admin reads. Someone who stops being an
// admin keeps the rows but no longer sees or counts them.
var adminOnly = []string{AccountRequested, GroupRequested}

// Unread is the bell's one plain count (DEC-44): the unread notices the
// reader would see in the list. A notice whose subject has since gone is
// deleted with it, so only the admin-only kinds need leaving out here.
func Unread(ctx context.Context, q db.Querier, r Reader) (int, error) {
	query := `SELECT count(*) FROM notices WHERE person_id = ? AND read_at IS NULL`
	args := []any{r.ID}
	if !r.Admin {
		for _, k := range adminOnly {
			query += ` AND kind <> ?`
			args = append(args, k)
		}
	}
	var n int
	err := q.QueryRowContext(ctx, query, args...).Scan(&n)
	return n, err
}

// ErrGone is a notice that is missing, not the reader's, or no longer worded
// for them.
var ErrGone = errors.New("notices: gone")

// One reads a single notice for its person.
func One(ctx context.Context, q db.Querier, r Reader, id int64) (Notice, error) {
	var x row
	err := q.QueryRowContext(ctx, `SELECT id, person_id, kind, actor_id, subject_id, group_id, created_at, read_at
		FROM notices WHERE id = ?`, id).Scan(&x.id, &x.personID, &x.kind, &x.actorID, &x.subjectID, &x.group, &x.at, &x.read)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && x.personID != r.ID) {
		return Notice{}, ErrGone
	}
	if err != nil {
		return Notice{}, err
	}
	n, ok, err := word(ctx, q, r, x)
	if err == nil && !ok {
		err = ErrGone
	}
	return n, err
}

// ForMail reads a notice for the email job: the notice, its person's
// address, and the reader to word it for.
func ForMail(ctx context.Context, q db.Querier, id int64) (Notice, string, error) {
	var personID int64
	var email, status string
	var admin bool
	err := q.QueryRowContext(ctx, `SELECT p.id, p.email, p.status, p.is_admin FROM notices n JOIN people p ON p.id = n.person_id WHERE n.id = ?`, id).
		Scan(&personID, &email, &status, &admin)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && status != "active") {
		return Notice{}, "", ErrGone
	}
	if err != nil {
		return Notice{}, "", err
	}
	n, err := One(ctx, q, Reader{ID: personID, Admin: admin}, id)
	return n, email, err
}

// word writes a notice's words for its reader, or reports it should not be
// shown. Names come from people at read time; nothing is stored.
func word(ctx context.Context, q db.Querier, r Reader, x row) (Notice, bool, error) {
	n := Notice{ID: x.id, Kind: x.kind, At: time.Unix(x.at, 0), Read: x.read.Valid}
	switch x.kind {
	case AccountRequested:
		if !r.Admin || !x.subjectID.Valid {
			return n, false, nil
		}
		var name, status string
		err := q.QueryRowContext(ctx, `SELECT name, status FROM people WHERE id = ?`, x.subjectID.Int64).Scan(&name, &status)
		if errors.Is(err, sql.ErrNoRows) {
			return n, false, nil // declined: nothing about them is kept (R27)
		}
		if err != nil {
			return n, false, err
		}
		n.Subject = "New account request"
		n.Href = "/admin/requests"
		if status == "pending" {
			n.Text = name + " asked for an account."
		} else {
			n.Text = name + "'s account request was approved."
			n.Href = "/"
		}
		return n, true, nil
	case GroupRequested, GroupApproved, GroupRejected, GroupDeleted:
		return wordGroup(ctx, q, r, x, n)
	}
	return n, false, nil
}

// wordGroup words the group notices. A leader's notice about their group's
// decision shows only while they may still see the group (they lead it, or
// they are an admin); the group request only to admins.
func wordGroup(ctx context.Context, q db.Querier, r Reader, x row, n Notice) (Notice, bool, error) {
	if !x.group.Valid {
		return n, false, nil
	}
	var uid, name, status, reason, note string
	err := q.QueryRowContext(ctx, `SELECT uid, name, status, decision_reason, decision_note FROM groups WHERE id = ?`, x.group.Int64).
		Scan(&uid, &name, &status, &reason, &note)
	if errors.Is(err, sql.ErrNoRows) {
		return n, false, nil
	}
	if err != nil {
		return n, false, err
	}
	quoted := "\u201c" + name + "\u201d"
	if x.kind == GroupDeleted {
		n.Text, n.Href, n.Subject = "The group "+quoted+" was deleted.", "/groups", "A group was deleted"
		return n, true, nil
	}
	if status == policy.GroupDeleted {
		return n, false, nil
	}
	if x.kind == GroupRequested {
		if !r.Admin {
			return n, false, nil
		}
		who := "Someone"
		if x.actorID.Valid {
			q.QueryRowContext(ctx, `SELECT name FROM people WHERE id = ?`, x.actorID.Int64).Scan(&who)
		}
		n.Subject = "A new group is waiting"
		if status == policy.GroupPending {
			n.Text, n.Href = who+" asked to start "+quoted+".", "/admin/groups"
		} else {
			n.Text, n.Href = who+"'s group "+quoted+" has been decided.", "/groups/"+uid
		}
		return n, true, nil
	}
	var leads bool
	q.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM active_memberships WHERE group_id = ? AND person_id = ? AND role = 'leader')`, x.group.Int64, r.ID).Scan(&leads)
	if !leads && !r.Admin {
		return n, false, nil
	}
	n.Href = "/groups/" + uid
	if x.kind == GroupApproved {
		n.Text, n.Subject = "Your group "+quoted+" was approved.", "Your group was approved"
	} else {
		n.Text, n.Subject = strings.TrimSpace("Your group "+quoted+" wasn't approved. "+policy.GroupReasons[reason]), "About your group"
	}
	n.Note = note
	return n, true, nil
}

// Open marks one of the actor's notices read. It is upkeep (R19).
type Open struct{ ID int64 }

func (Open) Action() string { return "notice.read" }
func (Open) Upkeep()        {}

func (c Open) Apply(tx *write.Tx) error {
	res, err := tx.Exec(`UPDATE notices SET read_at = ? WHERE id = ? AND person_id = ? AND read_at IS NULL`,
		tx.Now.Unix(), c.ID, tx.Actor.Person.ID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return write.ErrNothing
	}
	return nil
}

// ReadAll marks every one of the actor's notices read. It is upkeep.
type ReadAll struct{}

func (ReadAll) Action() string { return "notice.read_all" }
func (ReadAll) Upkeep()        {}

func (ReadAll) Apply(tx *write.Tx) error {
	res, err := tx.Exec(`UPDATE notices SET read_at = ? WHERE person_id = ? AND read_at IS NULL`, tx.Now.Unix(), tx.Actor.Person.ID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return write.ErrNothing
	}
	return nil
}

// JobCleanup deletes old notices daily: read ones after 90 days, unread ones
// after 180 (DEC-45).
const JobCleanup = "notices.cleanup"

type cleanup struct{}

func (cleanup) Action() string { return "notice.cleanup" }
func (cleanup) Upkeep()        {}

func (cleanup) Apply(tx *write.Tx) error {
	now := tx.Now
	res, err := tx.Exec(`DELETE FROM notices WHERE (read_at IS NOT NULL AND read_at < ?) OR (read_at IS NULL AND created_at < ?)`,
		now.Add(-90*24*time.Hour).Unix(), now.Add(-180*24*time.Hour).Unix())
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return write.ErrNothing
	}
	return nil
}

// Cleanup is the JobCleanup handler.
func Cleanup(e *write.Executor) write.Handler {
	return func(ctx context.Context, _ write.Job) error { return e.Run(ctx, write.System(), cleanup{}) }
}
