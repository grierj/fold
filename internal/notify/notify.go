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
	"strconv"
	"strings"
	"time"

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

// Msg is one notification to write, for every recipient in To.
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
}

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

// Send writes (or merges) one row per recipient, skipping the actor.
func Send(tx *gorm.DB, m Msg) error {
	k, ok := Lookup(m.Kind)
	if !ok {
		return fmt.Errorf("notify: unknown kind %q", m.Kind)
	}
	if !ValidLink(m.Link) {
		return ErrBadLink
	}
	data := "{}"
	if len(m.Data) > 0 {
		b, err := json.Marshal(m.Data)
		if err != nil {
			return err
		}
		data = string(b)
	}
	actions := "[]"
	if len(k.Actions) > 0 {
		b, _ := json.Marshal(k.Actions)
		actions = string(b)
	}
	now := time.Now().UTC()
	seen := map[uint]bool{}
	for _, uid := range m.To {
		if uid == 0 || seen[uid] || (m.Actor != nil && *m.Actor == uid) {
			continue
		}
		seen[uid] = true
		if k.BatchWindow > 0 && m.GroupKey != "" {
			merged, err := merge(tx, k, m, uid, now)
			if err != nil {
				return err
			}
			if merged {
				continue
			}
		}
		title, body := k.Render(withCount(m.Vars, 1))
		row := models.Notification{
			UserID: uid, Kind: k.Key, Category: k.Category, Waiting: k.Waiting,
			ActorID: m.Actor, SubjectType: m.SubjectType, Title: title, Body: body,
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
		if err := q.Create(&row).Error; err != nil {
			return err
		}
	}
	return nil
}

func withCount(v Vars, n int) Vars { v.Count = n; return v }

// merge folds m into an open unread row with the same group key inside the
// kind's window, re-rendering its text with the new count (6.5).
func merge(tx *gorm.DB, k Kind, m Msg, uid uint, now time.Time) (bool, error) {
	var open models.Notification
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("user_id = ? AND group_key = ? AND kind = ? AND read_at IS NULL AND archived_at IS NULL AND updated_at > ?",
			uid, m.GroupKey, k.Key, now.Add(-k.BatchWindow)).
		Order("updated_at DESC").First(&open).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	n := open.BatchCount + 1
	title, body := k.Render(withCount(m.Vars, n))
	return true, tx.Model(&open).Updates(map[string]any{
		"batch_count": n, "title": title, "body": body, "updated_at": now, "seen_at": nil,
	}).Error
}

// Resolve closes every open copy of a task (all admins' rows about one
// subject) in the caller's transaction. It returns how many rows it closed;
// zero means someone else got there first, or there was no task.
func Resolve(tx *gorm.DB, kind string, subjectID uint, by *uint, resolution string) (int64, error) {
	res := tx.Model(&models.Notification{}).
		Where("kind = ? AND subject_id = ? AND waiting AND resolved_at IS NULL", kind, subjectID).
		Updates(map[string]any{"resolved_at": time.Now().UTC(), "resolved_by_id": by, "resolution": resolution})
	return res.RowsAffected, res.Error
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

// ---------------------------------------------------------------------------
// Reading and state changes. Every query is scoped to the user's own rows.
// ---------------------------------------------------------------------------

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
// after the cursor before. ok is false for an unknown filter or cursor.
func Feed(db *gorm.DB, userID uint, filter, before string, limit int) (FeedPage, bool) {
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
		return page, false
	}
	if filter == "" || filter == FilterAll || filter == FilterUnread {
		own().Where(sqlLive + " AND " + sqlTask).Count(&page.WaitingTotal)
		own().Where(sqlLive + " AND " + sqlTask).Order("created_at, id").Limit(5).Find(&page.Waiting)
	}
	if before != "" {
		at, id, ok := parseCursor(before)
		if !ok {
			return page, false
		}
		q = q.Where("(updated_at, id) < (?, ?)", at, id)
	}
	q.Order("updated_at DESC, id DESC").Limit(limit + 1).Find(&page.Items)
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		last := page.Items[limit-1]
		page.Next = fmt.Sprintf("%d.%d", last.UpdatedAt.UnixMicro(), last.ID)
	}
	return page, true
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

// Get loads one of the user's own rows (false for anyone else's).
func Get(db *gorm.DB, userID, id uint) (models.Notification, bool) {
	var n models.Notification
	err := db.Where("id = ? AND user_id = ?", id, userID).First(&n).Error
	return n, err == nil
}

// SetRead marks one of the user's rows read (or unread again).
func SetRead(db *gorm.DB, userID, id uint, read bool) bool {
	return setTime(db, userID, id, "read_at", read)
}

// SetArchived archives one of the user's rows, or restores it (Undo).
func SetArchived(db *gorm.DB, userID, id uint, archived bool) bool {
	return setTime(db, userID, id, "archived_at", archived)
}

func setTime(db *gorm.DB, userID, id uint, col string, on bool) bool {
	if _, ok := Get(db, userID, id); !ok {
		return false
	}
	var v any
	if on {
		v = gorm.Expr("coalesce(" + col + ", now())")
	}
	db.Model(&models.Notification{}).Where("id = ? AND user_id = ?", id, userID).Update(col, v)
	return true
}

// ReadAll marks every ordinary item read. Open tasks stay counted until
// they are resolved (6.13 #2).
func ReadAll(db *gorm.DB, userID uint) error {
	return db.Model(&models.Notification{}).
		Where("user_id = ? AND "+sqlUnread, userID).Update("read_at", gorm.Expr("now()")).Error
}

// ArchiveRead archives every read item that isn't an open task.
func ArchiveRead(db *gorm.DB, userID uint) error {
	return db.Model(&models.Notification{}).
		Where("user_id = ? AND archived_at IS NULL AND read_at IS NOT NULL AND NOT ("+sqlTask+")", userID).
		Update("archived_at", gorm.Expr("now()")).Error
}

// Seen stamps seen_at on the user's rows the panel showed (kept for the
// M1.2 "email if not seen" escalation; the badge doesn't use it).
func Seen(db *gorm.DB, userID uint, ids []uint) error {
	if len(ids) == 0 {
		return nil
	}
	return db.Model(&models.Notification{}).
		Where("user_id = ? AND id IN ? AND seen_at IS NULL", userID, ids).Update("seen_at", gorm.Expr("now()")).Error
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
