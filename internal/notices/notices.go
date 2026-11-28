// Package notices is the bell. A notice stores a kind and ids, written in the
// same transaction as the change (DEC-42); its words are written when it is
// read, through the same visibility rules as any page (P12), so a notice can
// never carry an address or a phone number.
package notices

import (
	"context"
	"database/sql"
	"errors"
	"slices"
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
	MemberAdded      = "member.added"      // to the person a leader added (P6)
	MadeLeader       = "member.leader"     // to the person made a leader
	Invited          = "member.invited"    // to the person invited; actor: who invited; group, if any
	EventOpened      = "event.opened"      // to a group's other leaders and the admins; actor; event, and date if one date
	HostNamed        = "event.host"        // to the person made a host; actor; event

	// To the people signed up to a date (Going, Maybe or waiting), about
	// that date; event and date. Where one change touches many dates, each
	// person gets one notice naming the first of theirs.
	Promoted      = "answer.promoted" // moved up off the waitlist (DEC-84)
	NoRoom        = "answer.no_room"  // turned back on, and a date said Going to is full (DEC-47)
	DateCancelled = "date.cancelled"  // one date cancelled by hand
	EventDeleted  = "event.deleted"   // the event deleted (DEC-175)
	DatesDropped  = "dates.dropped"   // a series edit no longer makes the dates (DEC-91)
	DatesMoved    = "dates.moved"     // a series edit moved the dates to other days (R40)
	DatesRestored = "dates.restored"  // dates back on, answers as they were (DEC-91)
	DatesRetimed  = "dates.retimed"   // a series edit moved the dates to new times the same day; answers stay
	// To a person those running an event took off it; actor (R44).
	AnswerRemoved    = "answer.removed"     // off one date
	AnswerRemovedAll = "answer.removed_all" // off every date still to come; date: the first
)

// JobMail emails one notice to its person, for the kinds that are required
// mail (Mails). Its ref is the notice id.
const JobMail = "mail.notice"

// MailsPerDay is how many emails of one kind a person gets in a day.
const MailsPerDay = 10

// UrgentPerDay is how many urgent event emails, of every kind together, a
// person gets in a day, so nobody running an event can fill an inbox by
// cancelling and restoring. Past it the bell still tells them.
const UrgentPerDay = 5

// urgent are the event kinds that are emailed (R36).
var urgent = []string{Promoted, DateCancelled, EventDeleted, DatesDropped, DatesMoved}

// Mails reports whether a notice is also emailed. Until email choices
// arrive in slice 3 only required mail goes out: account requests, and of
// event notices only the urgent ones (R36): moving up off a waitlist, and a
// date the person said Going or Maybe to going away.
func Mails(kind, answer string) bool {
	switch kind {
	case AccountRequested, Promoted:
		return true
	case DateCancelled, EventDeleted, DatesDropped, DatesMoved:
		return policy.Urgent(answer)
	}
	return false
}

// New is one notice to add inside a command.
type New struct {
	PersonID  int64 // who it is for
	Kind      string
	ActorID   int64 // 0 when nobody in particular
	SubjectID int64 // the person it is about, 0 if none
	GroupID   int64 // 0 if none
	EventID   int64 // 0 if none
	DateID    int64 // 0 if none
	// Answer is the person's answer to the date, for R36's mail rule. It is
	// not stored.
	Answer string
}

func nullID(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}

// Add writes a notice, and queues its email when it is mailed.
func Add(tx *write.Tx, n New) error { return AddMany(tx, []New{n}) }

// batch is how many notices one INSERT writes.
const batch = 200

// AddMany writes notices in a few statements, however many people a change
// tells (docs/HAZARDS.md section 7), and queues one email job for each that
// is mailed. Each job sends one email after the commit.
func AddMany(tx *write.Tx, ns []New) error {
	for len(ns) > 0 {
		chunk := ns[:min(batch, len(ns))]
		ns = ns[len(chunk):]
		args := make([]any, 0, len(chunk)*8)
		mail := map[[2]any]bool{}
		for _, n := range chunk {
			args = append(args, n.PersonID, n.Kind, nullID(n.ActorID), nullID(n.SubjectID), nullID(n.GroupID), nullID(n.EventID), nullID(n.DateID), tx.Now.Unix())
			if Mails(n.Kind, n.Answer) {
				mail[[2]any{n.PersonID, n.Kind}] = true
			}
		}
		rows, err := tx.Query(`INSERT INTO notices (person_id, kind, actor_id, subject_id, group_id, event_id, date_id, created_at) VALUES `+
			strings.TrimSuffix(strings.Repeat("(?, ?, ?, ?, ?, ?, ?, ?), ", len(chunk)), ", ")+` RETURNING id, person_id, kind`, args...)
		if err != nil {
			return err
		}
		type sent struct {
			id, person int64
			kind       string
		}
		var queue []sent
		for rows.Next() {
			var x sent
			if err := rows.Scan(&x.id, &x.person, &x.kind); err != nil {
				rows.Close()
				return err
			}
			if mail[[2]any{x.person, x.kind}] {
				queue = append(queue, x)
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, x := range queue {
			// Each person gets at most a few emails of one kind a day; past
			// that the bell still counts them. A flood of requests cannot
			// flood an inbox.
			var today int
			if err := tx.QueryRow(`SELECT count(*) FROM notices WHERE person_id = ? AND kind = ? AND created_at > ? AND id <= ?`,
				x.person, x.kind, tx.Now.Add(-24*time.Hour).Unix(), x.id).Scan(&today); err != nil {
				return err
			}
			if today > MailsPerDay {
				continue
			}
			if slices.Contains(urgent, x.kind) {
				// Counted from the jobs already queued, inside the one write,
				// so the cap is spent as each email is queued.
				var sent int
				if err := tx.QueryRow(`SELECT count(*) FROM jobs j JOIN notices n ON n.id = j.ref
					WHERE j.kind = ? AND j.created_at > ? AND n.person_id = ? AND n.kind IN (`+strings.TrimSuffix(strings.Repeat("?, ", len(urgent)), ", ")+`)`,
					append([]any{JobMail, tx.Now.Add(-24 * time.Hour).Unix(), x.person}, kinds(urgent)...)...).Scan(&sent); err != nil {
					return err
				}
				if sent >= UrgentPerDay {
					continue
				}
			}
			if err := tx.Enqueue(write.NewJob{Kind: JobMail, Ref: x.id}); err != nil {
				return err
			}
		}
	}
	return nil
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
	event, date               sql.NullInt64
	at                        int64
	read                      sql.NullInt64
}

// Reader is who is reading: what wording and visibility need.
type Reader struct {
	ID    int64
	Admin bool
	Now   time.Time // for whether an invitation is still open
}

// List is a person's notices, newest first, worded for them. A notice whose
// subject they may no longer see is left out.
func List(ctx context.Context, q db.Querier, r Reader, limit int) ([]Notice, error) {
	rows, err := q.QueryContext(ctx, `SELECT id, person_id, kind, actor_id, subject_id, group_id, event_id, date_id, created_at, read_at
		FROM notices WHERE person_id = ? ORDER BY created_at DESC, id DESC LIMIT ?`, r.ID, limit)
	if err != nil {
		return nil, err
	}
	var raw []row
	for rows.Next() {
		var x row
		if err := rows.Scan(&x.id, &x.personID, &x.kind, &x.actorID, &x.subjectID, &x.group, &x.event, &x.date, &x.at, &x.read); err != nil {
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

// Unread is the bell's one plain count (DEC-44): exactly the unread
// notices the reader's list would show, worded through the same rules.
func Unread(ctx context.Context, q db.Querier, r Reader) (int, error) {
	list, err := List(ctx, q, r, 200)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, x := range list {
		if !x.Read {
			n++
		}
	}
	return n, nil
}

// ErrGone is a notice that is missing, not the reader's, or no longer worded
// for them.
var ErrGone = errors.New("notices: gone")

// One reads a single notice for its person.
func One(ctx context.Context, q db.Querier, r Reader, id int64) (Notice, error) {
	var x row
	err := q.QueryRowContext(ctx, `SELECT id, person_id, kind, actor_id, subject_id, group_id, event_id, date_id, created_at, read_at
		FROM notices WHERE id = ?`, id).Scan(&x.id, &x.personID, &x.kind, &x.actorID, &x.subjectID, &x.group, &x.event, &x.date, &x.at, &x.read)
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
func ForMail(ctx context.Context, q db.Querier, id int64, now time.Time) (Notice, string, error) {
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
	n, err := One(ctx, q, Reader{ID: personID, Admin: admin, Now: now}, id)
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
	case MemberAdded, MadeLeader, Invited:
		return wordMember(ctx, q, r, x, n)
	case EventOpened, HostNamed:
		return wordEvent(ctx, q, r, x, n)
	case Promoted, NoRoom, DateCancelled, EventDeleted, DatesDropped, DatesMoved, DatesRestored, DatesRetimed, AnswerRemoved, AnswerRemovedAll:
		return wordSignedUp(ctx, q, r, x, n)
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

// audiences words who an opened event or date now lets in.
var audiences = map[string]string{policy.Public: "everyone", policy.Church: "everyone signed in at the church"}

// wordEvent words the event notices, while the event is not deleted. An
// opening shows to an admin, or to someone who still leads the event's
// group, with the reason given (DEC-28); a host's notice only while they
// are still its host.
func wordEvent(ctx context.Context, q db.Querier, r Reader, x row, n Notice) (Notice, bool, error) {
	if !x.event.Valid {
		return n, false, nil
	}
	var uid, title, status, visibility, reason string
	var groupID sql.NullInt64
	err := q.QueryRowContext(ctx, `SELECT uid, title, status, visibility, open_reason, group_id FROM events WHERE id = ?`, x.event.Int64).
		Scan(&uid, &title, &status, &visibility, &reason, &groupID)
	if errors.Is(err, sql.ErrNoRows) {
		return n, false, nil
	}
	if err != nil || status != policy.EventActive {
		return n, false, err
	}
	who := "Someone"
	if x.actorID.Valid {
		q.QueryRowContext(ctx, `SELECT name FROM people WHERE id = ?`, x.actorID.Int64).Scan(&who)
	}
	quoted := "\u201c" + title + "\u201d"
	n.Href = "/events/" + uid
	if x.kind == HostNamed {
		var hosts bool
		q.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM event_hosts WHERE event_id = ? AND person_id = ?)`, x.event.Int64, r.ID).Scan(&hosts)
		if !hosts {
			return n, false, nil
		}
		n.Text, n.Subject = who+" made you a host of "+quoted+".", "You're hosting an event"
		return n, true, nil
	}
	var leads bool
	var groupStatus, groupVis string
	if groupID.Valid {
		q.QueryRowContext(ctx, `SELECT status, visibility, EXISTS (SELECT 1 FROM active_memberships WHERE group_id = g.id AND person_id = ? AND role = 'leader')
			FROM groups g WHERE g.id = ?`, r.ID, groupID.Int64).Scan(&groupStatus, &groupVis, &leads)
	}
	if !groupID.Valid || groupStatus == policy.GroupDeleted || (!leads && !r.Admin) {
		return n, false, nil
	}
	what := quoted
	if x.date.Valid {
		var duid, dvis string
		err := q.QueryRowContext(ctx, `SELECT uid, coalesce(visibility, ''), open_reason FROM dates WHERE id = ?`, x.date.Int64).Scan(&duid, &dvis, &reason)
		if err != nil {
			return n, false, nil
		}
		visibility = policy.Widest(visibility, dvis)
		what, n.Href = "a date of "+quoted, "/dates/"+duid
	}
	if !policy.MoreOpen(visibility, groupVis) {
		return n, false, nil // the opening was undone; the audit row keeps it
	}
	to := audiences[visibility]
	n.Text, n.Subject, n.Note = who+" opened "+what+" to "+to+".", "An event was opened wider", reason
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

// wordMember words the notices to a person about their own membership,
// through the same visibility rule as the group's page: it shows only
// while they may see the group. An invitation notice points at the open
// invitation, and goes once it is answered.
func wordMember(ctx context.Context, q db.Querier, r Reader, x row, n Notice) (Notice, bool, error) {
	who := "Someone"
	if x.actorID.Valid {
		q.QueryRowContext(ctx, `SELECT name FROM people WHERE id = ?`, x.actorID.Int64).Scan(&who)
	}
	if !x.group.Valid {
		return n, false, nil
	}
	var uid, name, status, visibility, role string
	err := q.QueryRowContext(ctx, `SELECT g.uid, g.name, g.status, g.visibility,
		coalesce((SELECT am.role FROM active_memberships am WHERE am.group_id = g.id AND am.person_id = ?), '')
		FROM groups g WHERE g.id = ?`, r.ID, x.group.Int64).Scan(&uid, &name, &status, &visibility, &role)
	if errors.Is(err, sql.ErrNoRows) {
		return n, false, nil
	}
	if err != nil {
		return n, false, err
	}
	quoted := "\u201c" + name + "\u201d"
	rel := policy.Rel{Member: role != "", Leader: role == "leader"}
	if x.kind == Invited {
		// The invitation shows only while its inviter may still invite into
		// the group: active, and an admin or an active leader of it. This is
		// accounts.inviterMay in SQL (the packages cannot import each other);
		// a test holds the two together.
		var inv string
		err := q.QueryRowContext(ctx, `SELECT i.uid FROM invitations i JOIN people p ON p.id = i.inviter_id
			WHERE i.group_id = ? AND i.person_id = ? AND i.state = 'open' AND i.expires_at > ? AND p.status = 'active'
			AND (p.is_admin = 1 OR EXISTS (SELECT 1 FROM active_memberships am WHERE am.group_id = i.group_id AND am.person_id = p.id AND am.role = 'leader'))
			ORDER BY i.id DESC LIMIT 1`, x.group.Int64, r.ID, r.Now.Unix()).Scan(&inv)
		if errors.Is(err, sql.ErrNoRows) || status != policy.GroupActive {
			return n, false, nil
		}
		if err != nil {
			return n, false, err
		}
		n.Text, n.Href, n.Subject = who+" invited you to join "+quoted+".", "/invitations/"+inv, "An invitation"
		return n, true, nil
	}
	if !rel.Member || !policy.CanSeeGroup(&policy.Person{ID: r.ID, Admin: r.Admin}, policy.Group{Status: status, Visibility: visibility}, rel) {
		return n, false, nil
	}
	n.Href = "/groups/" + uid
	switch x.kind {
	case MemberAdded:
		n.Text, n.Subject = who+" added you to "+quoted+". If it isn't for you, you can leave it from its page.", "You were added to a group"
	case MadeLeader:
		n.Text, n.Subject = who+" made you a leader of "+quoted+".", "You're a group leader"
	}
	return n, true, nil
}

func kinds(list []string) []any {
	out := make([]any, len(list))
	for i, k := range list {
		out[i] = k
	}
	return out
}
