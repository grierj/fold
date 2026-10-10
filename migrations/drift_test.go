package migrations_test

import (
	"context"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Drift runs GORM AutoMigrate for real against db, which must be a
// throwaway database that goose has just migrated, and returns every CREATE
// or ALTER statement it issued. An empty result means the models match the
// migrations. GORM's DryRun can't do this: its migrator has to query the
// database (HasTable, ColumnTypes) to decide what to change.
func Drift(db *gorm.DB, models ...any) ([]string, error) {
	rec := &recorder{Interface: logger.Discard}
	if err := db.Session(&gorm.Session{Logger: rec}).AutoMigrate(models...); err != nil {
		return nil, err
	}
	return rec.changes, nil
}

// recorder is a GORM logger that keeps every schema-changing statement.
type recorder struct {
	logger.Interface
	mu      sync.Mutex
	changes []string
}

func (r *recorder) LogMode(logger.LogLevel) logger.Interface { return r }

func (r *recorder) Trace(_ context.Context, _ time.Time, fc func() (string, int64), _ error) {
	sql, _ := fc()
	verb, _, _ := strings.Cut(strings.TrimSpace(sql), " ")
	switch strings.ToUpper(verb) {
	case "CREATE", "ALTER":
		r.mu.Lock()
		r.changes = append(r.changes, sql)
		r.mu.Unlock()
	}
}
