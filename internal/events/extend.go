package events

import (
	"context"
	"errors"
	"fmt"
	"time"

	"fold/models"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// LockID is the session advisory lock the daily occurrences.extend job takes.
// It is not the migration lock, so a migrate and an extend can overlap.
const LockID = 731002511

// insertBatch keeps one insert under the Postgres parameter limit.
const insertBatch = 500

// AfterEnsure runs in the transaction that added an event's new dates
// (their original dates), so the app can book the series room for them
// (9.2) without this package knowing about rooms.
type AfterEnsure func(tx *gorm.DB, ev *models.Event, added []string) error

// Extend inserts any missing occurrences through the rolling horizon.
// Rows that already exist, including cancelled dates and per-date overrides,
// are left alone. Safe to run twice. after, if not nil, sees each event's
// new dates in the same transaction.
func Extend(db *gorm.DB, now time.Time, after AfterEnsure) error {
	// A one-time event has all its dates from creation, and a series whose end
	// date has passed cannot make new ones, so neither is loaded.
	var evs []models.Event
	if err := db.Where("rrule <> '' AND (rrule_until IS NULL OR rrule_until >= ?)", now.AddDate(0, 0, -1).Format("2006-01-02")).
		Order("id").Find(&evs).Error; err != nil {
		return err
	}
	// One bad event must not stop the rest from being extended.
	var errs []error
	for i := range evs {
		ev := &evs[i]
		err := db.Transaction(func(tx *gorm.DB) error {
			// Re-read under the event's lock: an edit that committed since the
			// list was read wins, and none runs while this adds its dates.
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(ev, ev.ID).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return nil // deleted since
				}
				return err
			}
			added, err := ensure(tx, ev, now)
			if err != nil || after == nil || len(added) == 0 {
				return err
			}
			return after(tx, ev, added)
		})
		if err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Ensure generates occurrences for one event through the horizon.
func Ensure(db *gorm.DB, ev *models.Event, now time.Time) error {
	_, err := ensure(db, ev, now)
	return err
}

// ensure is Ensure, returning the original dates it added.
func ensure(db *gorm.DB, ev *models.Event, now time.Time) ([]string, error) {
	until := ""
	if ev.RRuleUntil != nil {
		until = *ev.RRuleUntil
	}
	start, _, err := ParseWall(ev.StartsLocal, ev.TZID)
	if err != nil {
		return nil, fmt.Errorf("event %d: %w", ev.ID, err)
	}
	whens, err := Expand(Spec{
		StartsLocal: ev.StartsLocal, DurationMinutes: ev.DurationMinutes,
		TZID: ev.TZID, RRule: ev.RRule, Until: until,
	}, WindowEnd(start, now))
	if err != nil {
		return nil, fmt.Errorf("event %d: %w", ev.ID, err)
	}
	var dates []string
	if err := db.Model(&models.EventOccurrence{}).Where("event_id = ?", ev.ID).
		Pluck("original_date", &dates).Error; err != nil {
		return nil, err
	}
	have := make(map[string]bool, len(dates))
	for _, d := range dates {
		have[d] = true
	}
	rows := make([]models.EventOccurrence, 0)
	for _, w := range whens {
		if have[w.OriginalDate] {
			continue
		}
		rows = append(rows, models.EventOccurrence{
			EventID: ev.ID, OriginalDate: w.OriginalDate,
			StartsAt: w.StartsAt, EndsAt: w.EndsAt,
			Status: models.OccurrenceScheduled,
		})
	}
	if len(rows) == 0 {
		return nil, nil
	}
	added := make([]string, len(rows))
	for i, r := range rows {
		added[i] = r.OriginalDate
	}
	return added, db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "event_id"}, {Name: "original_date"}},
		DoNothing: true,
	}).CreateInBatches(&rows, insertBatch).Error
}

// ExtendLocked runs Extend while holding LockID. A second caller, or a
// second app process, skips the work instead of waiting. The lock is taken
// on its own connection so it covers every query Extend makes on the pool.
func ExtendLocked(ctx context.Context, db *gorm.DB, now time.Time, after AfterEnsure) error {
	sqlDB, err := db.DB()
	if err != nil {
		return err
	}
	conn, err := sqlDB.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	var got bool
	if err := conn.QueryRowContext(ctx, "SELECT pg_try_advisory_lock($1)", LockID).Scan(&got); err != nil {
		return err
	}
	if !got {
		return nil
	}
	// The request context may be cancelled by now; the unlock must still run.
	defer conn.ExecContext(context.Background(), "SELECT pg_advisory_unlock($1)", LockID)
	return Extend(db, now, after)
}
