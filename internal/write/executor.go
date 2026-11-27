// Package write is the one door for changes (AGENTS.md, "One door for
// writes"). It holds the only writer connection. Every change is a Command
// run by Executor.Run: begin the write, load the actor fresh, let the command
// ask policy and apply itself, append the audit row, commit.
//
// The other writes in this package are the executor's own machinery: job
// bookkeeping (a job's enqueue was audited by the command that queued it),
// and the maintenance operations Backup and Restore, which write their own
// audit rows.
package write

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"fold/internal/audit"
	"fold/internal/clock"
	"fold/internal/keys"
	"fold/internal/people"
)

var (
	// ErrNotAllowed is policy saying no. Handlers answer it exactly as they
	// answer something missing.
	ErrNotAllowed = errors.New("write: not allowed")
	// ErrSignedOut is an actor whose session ended, expired, or whose account
	// is no longer active, between the page and the write.
	ErrSignedOut = errors.New("write: the session is no longer valid")
	// ErrNothing is a command deciding there is nothing to change. The
	// transaction is rolled back, no audit row is written, and Run returns nil.
	ErrNothing = errors.New("write: nothing to change")
)

type actorKind int

const (
	anonymous actorKind = iota
	system
	bySession
)

// ActorRef names who is acting. The actor itself is loaded inside the write.
type ActorRef struct {
	kind        actorKind
	sessionHash []byte
}

func Anonymous() ActorRef { return ActorRef{kind: anonymous} }
func System() ActorRef    { return ActorRef{kind: system} }

// Session is a signed-in person, named by their session's token hash.
func Session(tokenHash []byte) ActorRef { return ActorRef{kind: bySession, sessionHash: tokenHash} }

// Actor is who is acting, as the database says right now.
type Actor struct {
	kind      actorKind
	Person    *people.Person // nil for anonymous and system
	SessionID int64
}

func (a Actor) IsSystem() bool    { return a.kind == system }
func (a Actor) IsAnonymous() bool { return a.kind == anonymous }

func (a Actor) auditID() string {
	switch a.kind {
	case system:
		return audit.System
	case bySession:
		return a.Person.UID
	default:
		return audit.Anonymous
	}
}

// Command is one named change. Apply asks policy with the facts it loads
// through tx, then makes the change. It must not touch the network.
type Command interface {
	// Action is the audit action, a dotted name such as "session.end".
	Action() string
	Apply(tx *Tx) error
}

// Executor runs commands on the single writer connection.
type Executor struct {
	w     *sql.DB
	clock clock.Clock
	keys  *keys.Keys
	wake  chan struct{}
}

func New(w *sql.DB, c clock.Clock, k *keys.Keys) *Executor {
	return &Executor{w: w, clock: c, keys: k, wake: make(chan struct{}, 1)}
}

func (e *Executor) Clock() clock.Clock { return e.clock }
func (e *Executor) Keys() *keys.Keys   { return e.keys }

// Tx is a command's view of the write: the transaction, the actor, the one
// clock reading for the whole change, and the audit row being built.
type Tx struct {
	ctx      context.Context
	tx       *sql.Tx
	Actor    Actor
	Now      time.Time
	keys     *keys.Keys
	subjects []string
	details  map[string]string
	queued   bool
}

func (t *Tx) Context() context.Context { return t.ctx }

func (t *Tx) Exec(query string, args ...any) (sql.Result, error) {
	return t.tx.ExecContext(t.ctx, query, args...)
}

func (t *Tx) QueryRow(query string, args ...any) *sql.Row {
	return t.tx.QueryRowContext(t.ctx, query, args...)
}

func (t *Tx) Query(query string, args ...any) (*sql.Rows, error) {
	return t.tx.QueryContext(t.ctx, query, args...)
}

// QueryContext and QueryRowContext make Tx a db.Querier, so read helpers work
// inside a write.
func (t *Tx) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return t.tx.QueryContext(ctx, query, args...)
}

func (t *Tx) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return t.tx.QueryRowContext(ctx, query, args...)
}

// Subject adds a person's (or thing's) random identifier to the audit row.
func (t *Tx) Subject(uid string) { t.subjects = append(t.subjects, uid) }

// Detail adds one short structured value to the audit row.
func (t *Tx) Detail(key, value string) {
	if t.details == nil {
		t.details = map[string]string{}
	}
	t.details[key] = value
}

// Seal encrypts a small secret with a purpose key.
func (t *Tx) Seal(p keys.Purpose, plain []byte) []byte { return t.keys.Seal(p, plain) }

// Open decrypts a sealed value.
func (t *Tx) Open(p keys.Purpose, sealed []byte) ([]byte, bool, error) { return t.keys.Open(p, sealed) }

// Run executes c as the actor ref names.
func (e *Executor) Run(ctx context.Context, as ActorRef, c Command) error {
	tx, err := e.w.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("write: begin %s: %w", c.Action(), err)
	}
	defer tx.Rollback()
	t := &Tx{ctx: ctx, tx: tx, Now: e.clock.Now(), keys: e.keys}
	if t.Actor, err = loadActor(ctx, tx, as, t.Now); err != nil {
		return err
	}
	if err := c.Apply(t); err != nil {
		if errors.Is(err, ErrNothing) {
			return nil
		}
		return err
	}
	if _, err := audit.Append(ctx, tx, t.Now, audit.Entry{
		Action: c.Action(), Actor: t.Actor.auditID(), Subjects: t.subjects, Details: t.details,
	}); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("write: commit %s: %w", c.Action(), err)
	}
	if t.queued {
		e.Wake()
	}
	return nil
}

func loadActor(ctx context.Context, tx *sql.Tx, as ActorRef, now time.Time) (Actor, error) {
	switch as.kind {
	case anonymous, system:
		return Actor{kind: as.kind}, nil
	}
	s, err := people.BySession(ctx, tx, as.sessionHash, now)
	if errors.Is(err, people.ErrNotFound) {
		return Actor{}, ErrSignedOut
	}
	if err != nil {
		return Actor{}, err
	}
	return Actor{kind: bySession, Person: &s.Person, SessionID: s.ID}, nil
}

// Wake tells the job runner new work may be due.
func (e *Executor) Wake() {
	select {
	case e.wake <- struct{}{}:
	default:
	}
}
