// Package txguard lets code that holds Postgres row locks in a transaction find
// out whether a later rollback to a savepoint took them away.
//
// A row lock taken after a SAVEPOINT is released when that savepoint is rolled
// back to (a PostgreSQL property), while the transaction goes on. Code that
// carries proof of such a lock for a while (notify.Scope) needs to know. Postgres
// does not report it and the lock leaves no trace to query, so the only way to
// know without a query per use is to watch the statements this process sends:
// the Plugin sees transaction control that GORM sends and tells each open Span
// if it may have released its locks.
//
// What it covers: transaction-control SQL sent through GORM's Exec, Raw,
// SavePoint or RollbackTo, including a nested Transaction that fails, on a
// handle derived from the transaction. Generated create, update, delete and
// query SQL is not a transaction-control path. SQL sent around GORM, straight
// to database/sql or from inside a database function, is outside the watch.
// A statement it cannot read that may end the transaction or roll back its
// locks breaks the Span, so a surprise ends in refusal.
//
// Which savepoints exist. A rollback to a name the Span saw made inside the
// watch keeps the locks; a rollback to any other name may reach a savepoint from
// before the watch and release them. So a name is only trusted once PostgreSQL
// has made it. The statement is read before it runs (a release, rollback or
// commit takes effect whether or not it succeeds, so it is assumed to have),
// but a SAVEPOINT is added to the Span only after GORM's Exec or Raw has
// executed it without error (a callback after gorm:raw). A dry-run handle
// sends nothing and is ignored. A failed SAVEPOINT (a bad statement, an aborted
// transaction) made nothing, so the name stays untrusted and a later ROLLBACK TO
// it breaks the Span. A SAVEPOINT sent through Rows, Row or a query cannot be
// confirmed there (GORM may defer the error to Scan), so it breaks the Span.
package txguard

import (
	"errors"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"

	"gorm.io/gorm"
)

const Name = "txguard"

// ErrNotInstalled: the database handle was opened without Plugin, so rollbacks
// are not watched and a Span would prove nothing.
var ErrNotInstalled = errors.New("txguard: this database handle was opened without txguard.Plugin")

var ErrNoTransaction = errors.New("txguard: not in a transaction")

// Plugin installs the statement watcher. Pass it to db.Use on every gorm.DB
// that code calling notify.WithRecipients may use.
type Plugin struct{}

func (Plugin) Name() string { return Name }

func (Plugin) Initialize(db *gorm.DB) error {
	cb := db.Callback()
	if err := cb.Raw().Before("gorm:raw").Register(Name+":raw", observe); err != nil {
		return err
	}
	if err := cb.Raw().After("gorm:raw").Register(Name+":raw-result", confirmRaw); err != nil {
		return err
	}
	if err := cb.Row().Before("gorm:row").Register(Name+":row", observeUnconfirmed); err != nil {
		return err
	}
	return cb.Query().Before("gorm:query").Register(Name+":query", observeUnconfirmed)
}

func Installed(db *gorm.DB) bool {
	if db == nil || db.Config == nil {
		return false
	}
	_, ok := db.Plugins[Name]
	return ok
}

// Span watches one transaction from Begin until End.
type Span struct {
	key    gorm.ConnPool
	mu     sync.Mutex
	stack  []string // savepoints made since Begin and not yet released or undone
	broken atomic.Bool
	ended  atomic.Bool
}

// active counts the spans that have begun and not ended, so a statement on any
// connection costs one atomic load, not the registry lock, when none is open.
var active atomic.Int64

var registry = struct {
	mu    sync.RWMutex
	spans map[gorm.ConnPool][]*Span
}{spans: map[gorm.ConnPool][]*Span{}}

// poolKey is the identity of the transaction a handle runs in. GORM swaps a
// prepared-statement wrapper for its transaction around savepoint statements,
// so both forms name the same key.
func poolKey(p gorm.ConnPool) (gorm.ConnPool, bool) {
	if p == nil {
		return nil, false
	}
	if w, ok := p.(*gorm.PreparedStmtTX); ok && w != nil && w.Tx != nil {
		p = w.Tx
	}
	if !reflect.TypeOf(p).Comparable() {
		return nil, false
	}
	return p, true
}

// Begin starts watching tx's transaction. tx must be a transaction handle
// (ErrNoTransaction otherwise) from a database opened with Plugin
// (ErrNotInstalled otherwise). Call End when the watch is over.
func Begin(tx *gorm.DB) (*Span, error) {
	if !Installed(tx) {
		return nil, ErrNotInstalled
	}
	if tx.Statement == nil {
		return nil, ErrNoTransaction
	}
	if c, ok := tx.Statement.ConnPool.(gorm.TxCommitter); !ok || c == nil || reflect.ValueOf(c).IsNil() {
		return nil, ErrNoTransaction
	}
	key, ok := poolKey(tx.Statement.ConnPool)
	if !ok {
		return nil, ErrNoTransaction
	}
	s := &Span{key: key}
	active.Add(1)
	registry.mu.Lock()
	registry.spans[key] = append(registry.spans[key], s)
	registry.mu.Unlock()
	return s, nil
}

// End stops the watch. It is safe to call on a nil Span and more than once.
func (s *Span) End() {
	if s == nil || !s.ended.CompareAndSwap(false, true) {
		return
	}
	active.Add(-1)
	registry.mu.Lock()
	defer registry.mu.Unlock()
	list := registry.spans[s.key]
	for i, other := range list {
		if other == s {
			list = append(list[:i:i], list[i+1:]...)
			break
		}
	}
	if len(list) == 0 {
		delete(registry.spans, s.key)
	} else {
		registry.spans[s.key] = list
	}
}

// Broken reports whether, since Begin, the transaction may have ended or a
// rollback may have released locks taken at Begin. It is false on a nil Span,
// and once true it stays true.
func (s *Span) Broken() bool { return s != nil && s.broken.Load() }

func observe(db *gorm.DB) {
	if active.Load() == 0 || db == nil || db.Statement == nil || db.DryRun {
		return
	}
	key, ok := poolKey(db.Statement.ConnPool)
	if !ok {
		return
	}
	registry.mu.RLock()
	list := registry.spans[key]
	if len(list) == 0 {
		registry.mu.RUnlock()
		return
	}
	spans := append([]*Span(nil), list...)
	registry.mu.RUnlock()
	sql := db.Statement.SQL.String()
	if sql == "" {
		return
	}
	for _, s := range spans {
		s.observe(sql)
	}
}

// confirmRaw runs after gorm:raw. A SAVEPOINT that GORM executed without error
// exists now, so the spans trust its name. One that did not run (an error
// before or during execution, or a dry run) is never added. The callbacks run
// in order on the one goroutine that owns the transaction, so no other
// statement falls between the observer before gorm:raw and this one.
func confirmRaw(db *gorm.DB) {
	if active.Load() == 0 || db == nil || db.DryRun || db.Error != nil || db.Statement == nil {
		return
	}
	name, ok := savepointName(db.Statement.SQL.String())
	if !ok {
		return
	}
	key, ok := poolKey(db.Statement.ConnPool)
	if !ok {
		return
	}
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	for _, s := range registry.spans[key] {
		s.created(name)
	}
}

// Row and query callbacks can defer database errors until Scan, after the
// callback returns, so a SAVEPOINT sent there cannot be confirmed. It breaks
// the spans rather than being trusted.
func observeUnconfirmed(db *gorm.DB) {
	if db != nil && !db.DryRun && db.Statement != nil && savepointWord.MatchString(db.Statement.SQL.String()) {
		if _, ok := savepointName(db.Statement.SQL.String()); ok {
			breakSpans(db)
		}
	}
	observe(db)
}

func breakSpans(db *gorm.DB) {
	key, ok := poolKey(db.Statement.ConnPool)
	if !ok {
		return
	}
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	for _, s := range registry.spans[key] {
		s.broken.Store(true)
	}
}

var rollbackWord = regexp.MustCompile(`(?i)\b(rollback|abort)\b`)
var commitWord = regexp.MustCompile(`(?i)\b(commit|prepare)\b`)
var endWord = regexp.MustCompile(`(?i)\bend\b`)
var savepointWord = regexp.MustCompile(`(?i)\b(savepoint|release)\b`)

// observe updates the span for one statement. Savepoints are a stack: a
// rollback to a name found in it is a rollback inside the watch, and the name
// stays (Postgres keeps the savepoint it rolled back to); one not found is a
// rollback to something older, so the locks may be gone.
func (s *Span) observe(sql string) {
	trimmed := strings.TrimSpace(sql)
	withoutTerminator := strings.TrimRight(trimmed, "; \t\r\n")
	// Check for a second statement before parsing a savepoint name. Without
	// this, SAVEPOINT a;COMMIT would look like a two-field SAVEPOINT.
	if strings.Contains(withoutTerminator, ";") &&
		(rollbackWord.MatchString(sql) || commitWord.MatchString(sql) || endWord.MatchString(sql) || savepointWord.MatchString(sql)) {
		s.broken.Store(true)
		return
	}
	f := strings.Fields(withoutTerminator)
	if len(f) == 0 {
		return
	}
	switch strings.ToLower(f[0]) {
	case "savepoint":
		if len(f) == 2 {
			// Not recorded here: it is added by created once it has run.
			return
		}
	case "release":
		rest := f[1:]
		if len(rest) > 0 && strings.EqualFold(rest[0], "savepoint") {
			rest = rest[1:]
		}
		if len(rest) == 1 {
			// Releasing keeps every lock. It drops the name and the ones made
			// after it, so a later rollback to the same name reaches an older one.
			s.mu.Lock()
			if i := lastIndex(s.stack, nameOf(rest[0])); i >= 0 {
				s.stack = s.stack[:i]
			} else {
				// The released name may predate this span. In PostgreSQL that
				// also releases every tracked descendant, but keeps their locks.
				s.stack = nil
			}
			s.mu.Unlock()
			return
		}
	case "commit", "end", "prepare":
		// These may finish the server-side transaction without closing the
		// database/sql handle, releasing all the recipient locks.
		s.broken.Store(true)
		return
	case "rollback", "abort":
		rest := f[1:]
		if len(rest) > 0 && (strings.EqualFold(rest[0], "work") || strings.EqualFold(rest[0], "transaction")) {
			rest = rest[1:]
		}
		if len(rest) > 1 && strings.EqualFold(rest[0], "to") {
			rest = rest[1:]
			if strings.EqualFold(rest[0], "savepoint") {
				rest = rest[1:]
			}
			if len(rest) == 1 {
				name := nameOf(rest[0])
				s.mu.Lock()
				i := lastIndex(s.stack, name)
				if i >= 0 {
					s.stack = s.stack[:i+1]
				}
				s.mu.Unlock()
				if i < 0 {
					s.broken.Store(true)
				}
				return
			}
		}
		// A whole-transaction rollback, with or without AND CHAIN, or a form
		// not read above.
		s.broken.Store(true)
		return
	}
	// Refuse unparsed transaction control, including a leading comment or a
	// second statement. END inside an ordinary CASE expression is not a
	// transaction end, so only treat it as control in those ambiguous forms.
	endAt := endWord.FindStringIndex(trimmed)
	ambiguousEnd := endAt != nil && (endAt[0] == 0 || strings.HasPrefix(trimmed, "--") || strings.HasPrefix(trimmed, "/*"))
	if rollbackWord.MatchString(sql) || commitWord.MatchString(sql) || savepointWord.MatchString(sql) || ambiguousEnd {
		s.broken.Store(true)
	}
}

// savepointName is the name of a plain, single-statement "SAVEPOINT name".
// Anything else (a comment, a second statement, extra words) is not read, and
// observe refuses it, so it never adds a trusted name.
func savepointName(sql string) (string, bool) {
	t := strings.TrimRight(strings.TrimSpace(sql), "; \t\r\n")
	if strings.Contains(t, ";") {
		return "", false
	}
	f := strings.Fields(t)
	if len(f) == 2 && strings.EqualFold(f[0], "savepoint") {
		return nameOf(f[1]), true
	}
	return "", false
}

func (s *Span) created(name string) {
	s.mu.Lock()
	s.stack = append(s.stack, name)
	s.mu.Unlock()
}

// nameOf is an identifier as Postgres folds it: unquoted names ignore case, a
// quoted one is kept as written (quotes included, so it never equals an
// unquoted one; a mismatch ends in refusal).
func nameOf(tok string) string {
	if strings.Contains(tok, `"`) {
		return tok
	}
	return strings.ToLower(tok)
}

func lastIndex(stack []string, name string) int {
	for i := len(stack) - 1; i >= 0; i-- {
		if stack[i] == name {
			return i
		}
	}
	return -1
}
