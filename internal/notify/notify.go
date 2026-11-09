// Package notify stores Fold's in-app notifications (5.12, DEC-42). Fold
// builds them itself: no vendor, no extra service (DEC-43).
//
// Domain code calls Send inside the same transaction as its AuditLog row.
// The kind registry (kinds.go) decides the category, whether the row is a
// task waiting on someone, batching, inline actions, and the wording. Text
// is rendered once, when the row is written. Callers pass only names and
// places the recipient may see: the registry never looks anything up, so a
// hidden group's name, place or link can only reach a row if the caller put
// it there (PR #41, #43, #44 privacy rules).
package notify

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"fold/internal/jobs"
	"fold/internal/txguard"
	"fold/internal/userlock"
	"fold/models"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Retention (DEC-45): read or archived rows go after 90 days, unread ones
// after 180. AuditLog stays the record.
const (
	KeepRead   = 90 * 24 * time.Hour
	KeepUnread = 180 * 24 * time.Hour
)

// DeliverJob is the job that sends one stored notice by email and to the
// recipient's devices. The handler lives in the server; this package owns the
// type, its payload and its key. Eight attempts on jobs.Backoff span about an
// hour, the same as the message batch.
var DeliverJob = jobs.Type{Name: DeliverKind, MaxAttempts: 8}

const DeliverKind = "notify.deliver"

// DeliverPayload is notify.deliver's payload: the notice id and nothing else.
type DeliverPayload struct {
	NotificationID uint `json:"notificationId"`
}

// DeliverKeyPrefix is what a notice's delivery job key starts with; SQL that
// looks a job up by key appends the notice id to it.
const DeliverKeyPrefix = DeliverKind + ":"

func DeliverKey(id uint) string { return DeliverKeyPrefix + strconv.FormatUint(uint64(id), 10) }

func DeliverSpec(id uint, at time.Time) jobs.Spec {
	return DeliverJob.Spec(DeliverPayload{NotificationID: id}, at, DeliverKey(id))
}

type Msg struct {
	Kind        string
	To          []uint
	Actor       *uint // never notified about their own action
	SubjectType string
	SubjectID   uint
	Link        string // an internal path, starting with "/"
	Vars        Vars
	Data        map[string]any // small and non-sensitive
	DedupeKey   string         // optional: a second Send with it is a no-op
	GroupKey    string         // batching key for kinds with a BatchWindow
	ExpiresAt   *time.Time     // after this the row shows under Archived only
	// SuppressActions drops the kind's inline actions for this row
	// (DEC-44: a teen or flagged account request is Review-only). The
	// endpoint still checks eligibility itself; this only hides the
	// button.
	SuppressActions bool
	// BellOnly records the notice without emailing or pushing it.
	BellOnly bool
	// Scope is optional. A caller that fans out one Send per recipient calls
	// WithRecipients once for all of them and passes the scope it is given, so
	// each Send skips its own lock query. A Send checks the scope against its
	// own transaction and recipients and fails with ErrBadScope when it was not
	// made by WithRecipients, was made in another transaction, has been closed,
	// or does not name a recipient of this Send.
	Scope *Scope
}

// Scope is the proof that WithRecipients locked a set of recipients in one
// transaction. It can only be made by WithRecipients: everything it knows is
// private, so a caller can neither build one nor widen what it covers, and a
// zero or hand-built Scope covers nothing and is refused. It is valid only
// while the WithRecipients call that made it is running and only while its
// locks are still held: a copy kept after that, one used in another
// transaction, or one whose locks a savepoint rollback released, is refused.
type Scope struct{ s *scopeState }

type scopeState struct {
	pool  gorm.ConnPool // the transaction the locks belong to
	asked map[uint]bool // non-zero ids WithRecipients was asked to lock
	live  map[uint]bool // the asked ids that were not deleted when locked
	open  atomic.Bool
	span  *txguard.Span // sees a rollback past the locks (see WithRecipients)
}

// recipientLockBatch bounds one recipient lock statement. IDs are sorted
// before batching, so the whole set is still locked in ascending ID order.
const recipientLockBatch = userlock.Batch

// deliveryInsertBatch is the rows one multi-row INSERT of delivery intent
// carries; it bounds a statement's size, not a parameter limit (5 per row).
const deliveryInsertBatch = 500

// ErrBadScope: a Msg.Scope that is not valid for this Send (see Msg.Scope).
var ErrBadScope = errors.New("notify: recipient scope is not valid here")

// ErrNoRollbackGuard: the database handle was opened without txguard.Plugin, so a
// savepoint rollback that releases the recipient locks could not be noticed and
// a Scope would prove nothing.
var ErrNoRollbackGuard = errors.New("notify: the database handle does not watch savepoint rollbacks (open it with txguard.Plugin)")

// ErrNoTransaction: recipient protection needs a transaction, because the locks
// it takes would end with the single statement that took them.
var ErrNoTransaction = errors.New("notify: recipient protection needs a transaction")

// ErrBadLink: a link that isn't an internal path.
var ErrBadLink = errors.New("notify: link must be an internal path")

// ValidLink reports whether s is an internal path: it starts with one "/",
// and has no scheme, backslash or control characters.
func ValidLink(s string) bool {
	if !strings.HasPrefix(s, "/") || strings.HasPrefix(s, "//") || strings.ContainsAny(s, "\\\r\n\t") {
		return false
	}
	return !strings.Contains(strings.SplitN(s, "?", 2)[0], ":")
}

// transactionOf reports the connection a GORM handle is bound to when that is a
// transaction. Every handle derived from one transaction (Session, WithContext,
// Where, a nested Transaction's savepoint) shares it; another transaction, or
// the plain pool, has a different one.
func transactionOf(tx *gorm.DB) (gorm.ConnPool, bool) {
	if tx == nil || tx.Statement == nil {
		return nil, false
	}
	c, ok := tx.Statement.ConnPool.(gorm.TxCommitter)
	if !ok || c == nil || reflect.ValueOf(c).IsNil() {
		return nil, false
	}
	return tx.Statement.ConnPool, true
}

// WithRecipients locks the live users among ids in tx, runs fn with the Scope
// that proves it, and closes the Scope when fn returns. Pass the Scope as
// Msg.Scope to every Send of one fan-out: the lock is then one bulk statement
// for the whole fan-out (one per userlock.Batch ids) instead of one per Send. tx must be a
// transaction (ErrNoTransaction otherwise). The locks last until that
// transaction ends; the Scope itself is good only inside fn.
//
// The locks are FOR KEY SHARE on users not deleted, taken in ascending ID order
// (zero and repeated IDs are ignored). A concurrent erasure, which takes the
// user row FOR UPDATE, either waits for this transaction and then deletes the
// notices it wrote, or has committed first and the recheck of deleted_at here
// leaves the person out. A disabled user is still live: status plays no part.
//
// Savepoints. Rolling back to a savepoint made before the locks releases them (a
// PostgreSQL property), while the transaction and fn go on. The scope therefore
// watches the transaction (txguard): a rollback to a savepoint made inside fn is
// fine, and the scope stays good; a rollback to any older one, or one the watch
// cannot read, ends the scope. A savepoint counts as made inside fn only after
// GORM executed its SAVEPOINT without error (a dry run, or a refusal such as an
// aborted transaction, makes none, so a rollback to that name reaches an older
// savepoint and ends the scope too), and every later Send that carries it fails with
// ErrBadScope before writing. A Send with no scope locks for itself and is not
// affected. This costs the fan-out no query: the watch reads the statements GORM
// sends, not the database. The database handle must have been opened with
// txguard.Plugin (ErrNoRollbackGuard otherwise); SQL sent around GORM is outside
// the watch (see txguard).
func WithRecipients(tx *gorm.DB, ids []uint, fn func(*Scope) error) error {
	pool, ok := transactionOf(tx)
	if !ok {
		return ErrNoTransaction
	}
	span, err := txguard.Begin(tx)
	if err != nil {
		if errors.Is(err, txguard.ErrNotInstalled) {
			return ErrNoRollbackGuard
		}
		return ErrNoTransaction
	}
	// Watching starts before the lock statement, so no rollback can fall in a gap.
	defer span.End()
	st := &scopeState{pool: pool, span: span, asked: make(map[uint]bool, len(ids))}
	for _, id := range ids {
		if id != 0 {
			st.asked[id] = true
		}
	}
	live, err := lockRecipients(tx, ids)
	if err != nil {
		return err
	}
	st.live = live
	st.open.Store(true)
	defer st.open.Store(false)
	return fn(&Scope{s: st})
}

// check refuses a scope that was not made by WithRecipients, has been closed,
// belongs to another transaction, may have lost its locks to transaction control, or does
// not cover every id. It reads nothing from the database, so a fan-out pays nothing per recipient for it.
func (sc *Scope) check(tx *gorm.DB, ids []uint) error {
	if sc == nil || sc.s == nil {
		return fmt.Errorf("%w: it was not made by WithRecipients", ErrBadScope)
	}
	if !sc.s.open.Load() {
		return fmt.Errorf("%w: its WithRecipients call has returned", ErrBadScope)
	}
	if pool, ok := transactionOf(tx); !ok || pool != sc.s.pool {
		return fmt.Errorf("%w: it was made in a different transaction", ErrBadScope)
	}
	if sc.s.span.Broken() {
		return fmt.Errorf("%w: transaction control may have released its recipient locks", ErrBadScope)
	}
	for _, id := range ids {
		if !sc.s.asked[id] {
			return fmt.Errorf("%w: user %d was not among the recipients it locked", ErrBadScope, id)
		}
	}
	return nil
}

// lockRecipients is the statement behind WithRecipients and Send: FOR KEY SHARE
// on the non-deleted users among ids, ascending, in batches of userlock.Batch. It returns
// the ids it locked. Run on a plain handle, the locks end with the statement.
func lockRecipients(tx *gorm.DB, ids []uint) (map[uint]bool, error) {
	return userlock.KeyShareLive(tx, ids)
}

// Send writes (or merges) one row per recipient, skipping the actor. It
// returns the IDs of the rows written or merged for callers that need them.
// Delivery intent is written in tx too; delivery no longer needs those IDs.
// A dedupe hit writes nothing and returns no ID: the first send owns delivery.
//
// Before it merges or inserts anything, Send de-duplicates the recipients,
// drops zero IDs and the actor, and locks the rest (unless m.Scope already
// did: it is checked, not trusted). A deleted recipient gets no notice. The
// locks stay until tx ends, which covers the merge, the insert and the
// delivery intent.
func Send(tx *gorm.DB, m Msg) ([]uint, error) {
	k, ok := Lookup(m.Kind)
	if !ok {
		return nil, fmt.Errorf("notify: unknown kind %q", m.Kind)
	}
	if !ValidLink(m.Link) {
		return nil, ErrBadLink
	}
	data := "{}"
	if len(m.Data) > 0 {
		b, err := json.Marshal(m.Data)
		if err != nil {
			return nil, err
		}
		data = string(b)
	}
	actions := "[]"
	if len(k.Actions) > 0 && !m.SuppressActions {
		b, _ := json.Marshal(k.Actions)
		actions = string(b)
	}
	seen := map[uint]bool{}
	var order []uint
	for _, uid := range m.To {
		if uid == 0 || seen[uid] || (m.Actor != nil && *m.Actor == uid) {
			continue
		}
		seen[uid] = true
		order = append(order, uid)
	}
	var live map[uint]bool
	if m.Scope != nil {
		if err := m.Scope.check(tx, order); err != nil {
			return nil, err
		}
		live = m.Scope.s.live
	} else if len(order) > 0 {
		var err error
		if live, err = lockRecipients(tx, order); err != nil {
			return nil, err
		}
	}
	// Recipient locks can wait for an erasure or an email change. Sample the
	// batch window and write timestamp only after that wait has completed.
	now := time.Now().UTC()
	var ids []uint
	var created []models.Notification
	for _, uid := range order {
		if !live[uid] {
			continue
		}
		if k.BatchWindow > 0 && m.GroupKey != "" {
			merged, id, err := merge(tx, k, m, uid, now)
			if err != nil {
				return nil, err
			}
			if merged {
				ids = append(ids, id)
				continue
			}
		}
		first := withCount(m.Vars, max(m.Vars.Count, 1))
		title, body := k.Render(first)
		emailTitle, emailBody := k.RenderForEmail(first)
		pushTitle, pushBody, _ := k.RenderForPush(first)
		row := models.Notification{
			UserID: uid, Kind: k.Key, Category: k.Category, Waiting: k.Waiting,
			ActorID: m.Actor, SubjectType: m.SubjectType, Title: title, Body: body,
			EmailTitle: emailTitle, EmailBody: emailBody, PushTitle: pushTitle, PushBody: pushBody,
			Link: m.Link, Actions: actions, BatchCount: 1, Data: data,
			CreatedAt: now, UpdatedAt: now, ExpiresAt: m.ExpiresAt,
		}
		if m.SubjectID != 0 {
			id := m.SubjectID
			row.SubjectID = &id
		}
		if m.DedupeKey != "" {
			key := m.DedupeKey
			row.DedupeKey = &key
		}
		if m.GroupKey != "" {
			key := m.GroupKey
			row.GroupKey = &key
		}
		q := tx
		if row.DedupeKey != nil {
			q = tx.Clauses(clause.OnConflict{
				Columns:     []clause.Column{{Name: "user_id"}, {Name: "dedupe_key"}},
				TargetWhere: clause.Where{Exprs: []clause.Expression{clause.Expr{SQL: "dedupe_key IS NOT NULL"}}},
				DoNothing:   true,
			})
		}
		res := q.Create(&row)
		if res.Error != nil {
			return nil, res.Error
		}
		if res.RowsAffected > 0 {
			ids = append(ids, row.ID)
			created = append(created, row)
		}
	}
	// These rows were written a moment ago under the locks above, so their
	// owners are protected already.
	if err := queueProtected(tx, m.BellOnly, created...); err != nil {
		return nil, err
	}
	return ids, nil
}

// QueueDelivery is the repair entry point. It queues channel intent and jobs
// for notices selected earlier, possibly in another transaction: deliver rows
// get their email ledger row and a delivery job, bellOnly rows only the
// suppressed ledger row. Both groups are protected first by one Protect call
// (a single ascending acquisition), and only the rows that survive are queued:
// a notice deleted since, or one whose recipient is deleted, is skipped and
// nothing is recreated for it. Send does not use it; its rows are written under
// the locks it just took.
func QueueDelivery(tx *gorm.DB, deliver, bellOnly []models.Notification) error {
	all := make([]models.Notification, 0, len(deliver)+len(bellOnly))
	all = append(append(all, deliver...), bellOnly...)
	kept, err := Protect(tx, all)
	if err != nil {
		return err
	}
	alive := make(map[uint]bool, len(kept))
	for _, row := range kept {
		alive[row.ID] = true
	}
	survivors := func(rows []models.Notification) []models.Notification {
		var out []models.Notification
		for _, row := range rows {
			if alive[row.ID] {
				out = append(out, row)
			}
		}
		return out
	}
	if err := queueProtected(tx, false, survivors(deliver)...); err != nil {
		return err
	}
	return queueProtected(tx, true, survivors(bellOnly)...)
}

// Protect makes rows safe to queue in tx. In this order, it locks the owners
// that are not deleted (lockRecipients, ascending user ID), then locks the
// notifications that still exist in ascending notice ID order, and returns
// the rows, in their original order, whose notice exists and still belongs to
// a locked owner. Under READ COMMITTED both statements see everything that
// committed before them, so an erasure that finished first removes the rows
// and an erasure that starts later waits for this transaction. User locks come
// before notice locks, as in merge and the erasure delete.
func Protect(tx *gorm.DB, rows []models.Notification) ([]models.Notification, error) {
	if len(rows) == 0 {
		return nil, nil
	}
	owners := make([]uint, 0, len(rows))
	for _, row := range rows {
		owners = append(owners, row.UserID)
	}
	held, err := lockRecipients(tx, owners)
	if err != nil {
		return nil, err
	}
	var want []uint
	for _, row := range rows {
		if row.ID != 0 && held[row.UserID] {
			want = append(want, row.ID)
		}
	}
	slices.Sort(want)
	want = slices.Compact(want)
	owner := make(map[uint]uint, len(want))
	for len(want) > 0 {
		n := min(len(want), recipientLockBatch)
		var found []struct {
			ID     uint
			UserID uint
		}
		if err := tx.Raw(`SELECT id, user_id FROM notifications WHERE id IN ? ORDER BY id FOR KEY SHARE`,
			want[:n]).Scan(&found).Error; err != nil {
			return nil, err
		}
		for _, f := range found {
			owner[f.ID] = f.UserID
		}
		want = want[n:]
	}
	kept := make([]models.Notification, 0, len(rows))
	for _, row := range rows {
		if uid, ok := owner[row.ID]; ok && uid == row.UserID && held[uid] {
			kept = append(kept, row)
		}
	}
	return kept, nil
}

// queueProtected writes channel intent and jobs in the caller's transaction
// for rows that are already protected: written by Send in this transaction, or
// returned by Protect in it. It takes no locks of its own.
// A fan-out uses one delivery INSERT and one jobs INSERT per deliveryInsertBatch rows.
// Push has no queued ledger row: device outcomes own its aggregate record.
func queueProtected(tx *gorm.DB, bellOnly bool, rows ...models.Notification) error {
	now := time.Now().UTC()
	var values []string
	var args []any
	var specs []jobs.Spec
	for _, row := range rows {
		k, ok := Lookup(row.Kind)
		if !ok {
			return fmt.Errorf("notify: unknown kind %q", row.Kind)
		}
		if k.AllowEmail {
			status, reason := models.DeliveryQueued, ""
			if bellOnly {
				status, reason = models.DeliverySuppressed, "bell only"
			}
			values = append(values, "(?, ?, ?, ?, ?)")
			args = append(args, row.ID, models.DeliveryEmail, row.UserID, status, reason)
		}
		if !bellOnly && (k.AllowEmail || k.AllowPush) {
			specs = append(specs, DeliverSpec(row.ID, now))
		}
	}
	for len(values) > 0 {
		n := min(len(values), deliveryInsertBatch)
		if err := tx.Exec(`INSERT INTO notification_deliveries (notification_id, channel, user_id, status, reason) VALUES `+
			strings.Join(values[:n], ", ")+` ON CONFLICT (notification_id, channel) DO NOTHING`, args[:5*n]...).Error; err != nil {
			return err
		}
		values, args = values[n:], args[5*n:]
	}
	_, err := jobs.EnqueueMany(tx, specs)
	return err
}

func withCount(v Vars, n int) Vars { v.Count = n; return v }

// merge folds m into an open unread row with the same group key inside the
// kind's window, re-rendering its text with the new count (6.5). A merge
// keeps the original delivery intent and returns the existing row's ID.
// Send has locked uid (lockRecipients) before calling it: updating a row does
// not re-run the user foreign key, so without that lock a merge could not see
// an erasure. The order is user, then notification, then its delivery row.
func merge(tx *gorm.DB, k Kind, m Msg, uid uint, now time.Time) (bool, uint, error) {
	var open models.Notification
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("user_id = ? AND group_key = ? AND kind = ? AND read_at IS NULL AND archived_at IS NULL AND updated_at > ?",
			uid, m.GroupKey, k.Key, now.Add(-k.BatchWindow)).
		Order("updated_at DESC").First(&open).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, 0, nil
	}
	if err != nil {
		return false, 0, err
	}
	// The handler locks the delivery row, then reads the notification without
	// a lock. Merge locks the notification, then the delivery row. The read
	// cannot wait on our notification lock, so this order cannot deadlock.
	// An in-flight send finishes first; its sent ledger row stays terminal.
	var deliveryID uint
	if err := tx.Raw(`SELECT id FROM notification_deliveries
		WHERE notification_id = ? AND channel = 'email' FOR UPDATE`, open.ID).Scan(&deliveryID).Error; err != nil {
		return false, 0, err
	}
	n := open.BatchCount + 1
	title, body := k.Render(withCount(m.Vars, n))
	emailTitle, emailBody := k.RenderForEmail(withCount(m.Vars, n))
	pushTitle, pushBody, _ := k.RenderForPush(withCount(m.Vars, n))
	err = tx.Model(&open).Updates(map[string]any{
		"batch_count": n, "title": title, "body": body,
		"email_title": emailTitle, "email_body": emailBody,
		"push_title": pushTitle, "push_body": pushBody,
		"updated_at": now, "seen_at": nil,
	}).Error
	return true, open.ID, err
}

// Resolve closes every open copy of a task (all admins' rows about one
// subject) in the caller's transaction. It returns how many rows it closed;
// zero means someone else got there first, or there was no task.
func Resolve(tx *gorm.DB, kind string, subjectID uint, by *uint, resolution string) (int64, error) {
	res := tx.Model(&models.Notification{}).
		Where("kind = ? AND subject_id = ? AND "+sqlTask, kind, subjectID).
		UpdateColumns(map[string]any{"resolved_at": time.Now().UTC(), "resolved_by_id": by, "resolution": resolution})
	return res.RowsAffected, res.Error
}

// OpenTask reports whether a task of kind about subjectID is still waiting
// for someone, for any recipient.
func OpenTask(tx *gorm.DB, kind string, subjectID uint) (bool, error) {
	var n int64
	err := tx.Model(&models.Notification{}).Where("kind = ? AND subject_id = ? AND "+sqlTask, kind, subjectID).Limit(1).Count(&n).Error
	return n > 0, err
}

// LastResolution is the most recent resolution of a task, for the 409 an
// inline action gets when it loses a race.
func LastResolution(db *gorm.DB, kind string, subjectID uint) (by *uint, resolution string, ok bool) {
	var n models.Notification
	if err := db.Where("kind = ? AND subject_id = ? AND resolved_at IS NOT NULL", kind, subjectID).
		Order("resolved_at DESC").First(&n).Error; err != nil {
		return nil, "", false
	}
	return n.ResolvedByID, n.Resolution, true
}

// The SQL for each state, so the badge, the feed and read-all agree.
const (
	sqlLive    = "archived_at IS NULL AND (expires_at IS NULL OR expires_at > now())"
	sqlArchive = "(archived_at IS NOT NULL OR (expires_at IS NOT NULL AND expires_at <= now()))"
	sqlTask    = "waiting AND resolved_at IS NULL"
	sqlUnread  = "read_at IS NULL AND NOT (" + sqlTask + ")"
)

// Badge is the bell's count (DEC-44): unread ordinary items plus open tasks,
// with no double counting.
func Badge(db *gorm.DB, userID uint) (unread, waiting int64, err error) {
	var row struct{ Unread, Waiting int64 }
	err = db.Model(&models.Notification{}).
		Select("count(*) FILTER (WHERE "+sqlUnread+") AS unread, count(*) FILTER (WHERE "+sqlTask+") AS waiting").
		Where("user_id = ? AND "+sqlLive, userID).Scan(&row).Error
	return row.Unread, row.Waiting, err
}

// Filters for Feed.
const (
	FilterAll      = "all"
	FilterUnread   = "unread"
	FilterWaiting  = "waiting"
	FilterArchived = "archived"
)

// FeedPage is one page of the feed. Waiting holds the open tasks (oldest
// first, up to 5) on the all and unread filters; they are not repeated in
// Items. Next is the cursor for the following page, or "".
type FeedPage struct {
	Waiting      []models.Notification
	WaitingTotal int64
	Items        []models.Notification
	Next         string
}

// Feed returns the user's notifications for filter, newest activity first,
// after the cursor before. ok is false for an unknown filter or cursor; err
// is a failed read.
func Feed(db *gorm.DB, userID uint, filter, before string, limit int) (FeedPage, bool, error) {
	if limit <= 0 || limit > 50 {
		limit = 20
	}
	page := FeedPage{Waiting: []models.Notification{}, Items: []models.Notification{}}
	own := func() *gorm.DB { return db.Model(&models.Notification{}).Where("user_id = ?", userID) }
	q := own()
	switch filter {
	case "", FilterAll:
		q = q.Where(sqlLive + " AND NOT (" + sqlTask + ")")
	case FilterUnread:
		q = q.Where(sqlLive + " AND " + sqlUnread)
	case FilterWaiting:
		q = q.Where(sqlLive + " AND " + sqlTask)
	case FilterArchived:
		q = q.Where(sqlArchive)
	default:
		return page, false, nil
	}
	if filter == "" || filter == FilterAll || filter == FilterUnread {
		if err := own().Where(sqlLive + " AND " + sqlTask).Count(&page.WaitingTotal).Error; err != nil {
			return page, true, err
		}
		if err := own().Where(sqlLive + " AND " + sqlTask).Order("created_at, id").Limit(5).Find(&page.Waiting).Error; err != nil {
			return page, true, err
		}
	}
	if before != "" {
		at, id, ok := parseCursor(before)
		if !ok {
			return page, false, nil
		}
		q = q.Where("(updated_at, id) < (?, ?)", at, id)
	}
	if err := q.Order("updated_at DESC, id DESC").Limit(limit + 1).Find(&page.Items).Error; err != nil {
		return page, true, err
	}
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		last := page.Items[limit-1]
		page.Next = fmt.Sprintf("%d.%d", last.UpdatedAt.UnixMicro(), last.ID)
	}
	return page, true, nil
}

func parseCursor(s string) (time.Time, uint, bool) {
	a, b, ok := strings.Cut(s, ".")
	if !ok {
		return time.Time{}, 0, false
	}
	us, err1 := strconv.ParseInt(a, 10, 64)
	id, err2 := strconv.ParseUint(b, 10, 64)
	if err1 != nil || err2 != nil {
		return time.Time{}, 0, false
	}
	return time.UnixMicro(us).UTC(), uint(id), true
}

func Get(db *gorm.DB, userID, id uint) (models.Notification, bool) {
	var n models.Notification
	err := db.Where("id = ? AND user_id = ?", id, userID).First(&n).Error
	return n, err == nil
}

// SetRead marks one of the user's rows read (or unread again). found is false
// for a row that isn't the user's; err is a failed write.
func SetRead(db *gorm.DB, userID, id uint, read bool) (bool, error) {
	return setTime(db, userID, id, "read_at", read)
}

// SetArchived archives one of the user's rows, or restores it (Undo).
func SetArchived(db *gorm.DB, userID, id uint, archived bool) (bool, error) {
	return setTime(db, userID, id, "archived_at", archived)
}

// setTime flips a timestamp column. It uses UpdateColumn so updated_at, which
// orders the feed and drives retention, only moves when the content does.
func setTime(db *gorm.DB, userID, id uint, col string, on bool) (bool, error) {
	if _, ok := Get(db, userID, id); !ok {
		return false, nil
	}
	var v any
	if on {
		v = gorm.Expr("coalesce(" + col + ", now())")
	}
	err := db.Model(&models.Notification{}).Where("id = ? AND user_id = ?", id, userID).UpdateColumn(col, v).Error
	return err == nil, err
}

// ReadAll marks every ordinary item read. Open tasks stay counted until
// they are resolved (6.13 #2).
func ReadAll(db *gorm.DB, userID uint) error {
	return db.Model(&models.Notification{}).
		Where("user_id = ? AND "+sqlUnread, userID).UpdateColumn("read_at", gorm.Expr("now()")).Error
}

func ArchiveRead(db *gorm.DB, userID uint) error {
	return db.Model(&models.Notification{}).
		Where("user_id = ? AND archived_at IS NULL AND read_at IS NOT NULL AND NOT ("+sqlTask+")", userID).
		UpdateColumn("archived_at", gorm.Expr("now()")).Error
}

// Prune is the daily retention cleanup (DEC-45). Open tasks are kept
// however old, since they still need someone.
func Prune(db *gorm.DB, now time.Time) (int64, error) {
	res := db.Where("NOT ("+sqlTask+") AND ("+
		"((read_at IS NOT NULL OR archived_at IS NOT NULL OR (expires_at IS NOT NULL AND expires_at <= ?)) AND updated_at < ?)"+
		" OR updated_at < ?)", now, now.Add(-KeepRead), now.Add(-KeepUnread)).
		Delete(&models.Notification{})
	return res.RowsAffected, res.Error
}

// Settled reports whether n is past needing anyone: archived, expired, or a
// task someone resolved. Delivery that waits (a push held for quiet hours) or
// runs late (a backfill) drops a settled notice.
func Settled(n *models.Notification, now time.Time) bool {
	return n.ArchivedAt != nil || (n.ExpiresAt != nil && !n.ExpiresAt.After(now)) || (n.Waiting && n.ResolvedAt != nil)
}
