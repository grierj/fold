package events

import (
	"context"
	"fmt"
	"time"

	"fold/models"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// LockID is the session advisory lock the daily occurrence ticker takes.
// It is not the migration lock, so a migrate and an extend can overlap.
const LockID = 731002511

// Extend inserts any missing occurrences through the rolling horizon.
// Rows that already exist, including cancelled dates and per-date overrides,
// are left alone. Safe to run twice.
func Extend(db *gorm.DB, now time.Time) error {
	var evs []models.Event
	if err := db.Find(&evs).Error; err != nil {
		return err
	}
	for i := range evs {
		if err := Ensure(db, &evs[i], now); err != nil {
			return err
		}
	}
	return nil
}

// Ensure generates occurrences for one event through the horizon.
func Ensure(db *gorm.DB, ev *models.Event, now time.Time) error {
	until := ""
	if ev.RRuleUntil != nil {
		until = *ev.RRuleUntil
	}
	start, _, err := ParseWall(ev.StartsLocal, ev.TZID)
	if err != nil {
		return fmt.Errorf("event %d: %w", ev.ID, err)
	}
	whens, err := Expand(Spec{
		StartsLocal: ev.StartsLocal, DurationMinutes: ev.DurationMinutes,
		TZID: ev.TZID, RRule: ev.RRule, Until: until,
	}, WindowEnd(start, now))
	if err != nil {
		return fmt.Errorf("event %d: %w", ev.ID, err)
	}
	var dates []string
	if err := db.Model(&models.EventOccurrence{}).Where("event_id = ?", ev.ID).
		Pluck("original_date", &dates).Error; err != nil {
		return err
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
		return nil
	}
	return db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "event_id"}, {Name: "original_date"}},
		DoNothing: true,
	}).Create(&rows).Error
}

// ExtendLocked runs Extend while holding LockID. A second caller, or a
// second app process, skips the work instead of waiting. The lock is taken
// on its own connection so it covers every query Extend makes on the pool.
func ExtendLocked(ctx context.Context, db *gorm.DB, now time.Time) error {
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
	defer conn.ExecContext(ctx, "SELECT pg_advisory_unlock($1)", LockID)
	return Extend(db, now)
}
