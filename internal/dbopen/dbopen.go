// Package dbopen is the one place that opens Postgres through GORM and applies
// the goose migrations, for the server, the CLI and the test database.
package dbopen

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"fold/internal/txguard"
	"fold/migrations"

	"github.com/jackc/pgx/v5/pgconn"
	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" database/sql driver
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Open connects GORM to url with the transaction guard installed (the one
// notify.WithRecipients needs to see a savepoint rollback release its locks).
// It changes no schema: the goose files are the source of truth (C1).
func Open(url string, log logger.Interface) (*gorm.DB, error) {
	db, err := gorm.Open(postgres.Open(url), &gorm.Config{Logger: log})
	if err != nil {
		return nil, fmt.Errorf("connect to database: %w", err)
	}
	if err := db.Use(txguard.Plugin{}); err != nil {
		return nil, fmt.Errorf("connect to database: %w", err)
	}
	return db, nil
}

// Logger keeps GORM's default diagnostics, but never interpolates bound
// values: even successful slow writes can contain private contact details and
// request reasons.
func Logger(writer logger.Writer) logger.Interface {
	base := logger.New(writer, logger.Config{
		SlowThreshold:             200 * time.Millisecond,
		LogLevel:                  logger.Warn,
		IgnoreRecordNotFoundError: false,
		Colorful:                  true,
		ParameterizedQueries:      true,
	})
	return &legacyBusyFilter{base: base}
}

// legacySaveBusyKey marks the context of one legacy reason save's fenced
// transactions. The save retries its two expected lock failures and answers
// a busy refusal without their SQLSTATE, so only there may the production
// logger drop them; the live deletion seals the same actor row with the same
// statement, and its diagnostics stay intact.
type legacySaveBusyKey struct{}

// WithLegacySaveBusyScope returns ctx as one legacy reason save's execution
// context. The save wraps its bounded context before its fenced
// transactions, and GORM carries that context into every statement's logger
// call, including through Begin.
func WithLegacySaveBusyScope(ctx context.Context) context.Context {
	return context.WithValue(ctx, legacySaveBusyKey{}, true)
}

// legacySaveBusyScope reports whether ctx is a legacy save's execution context.
func legacySaveBusyScope(ctx context.Context) bool {
	scoped, _ := ctx.Value(legacySaveBusyKey{}).(bool)
	return scoped
}

// legacyBusyFilter drops only the expected lock contention inside a legacy
// save's own transactions: the actor seal and the counterpart blanking
// UPDATE both fail with 55P03 while another transaction holds the row, and
// the route retries the save and answers a busy refusal without that
// SQLSTATE. The same failure anywhere else, and every other error, still
// reaches the base logger with redaction unchanged.
type legacyBusyFilter struct{ base logger.Interface }

func (l *legacyBusyFilter) LogMode(level logger.LogLevel) logger.Interface {
	return &legacyBusyFilter{base: l.base.LogMode(level)}
}

func (l *legacyBusyFilter) Info(ctx context.Context, msg string, data ...interface{}) {
	l.base.Info(ctx, msg, data...)
}

func (l *legacyBusyFilter) Warn(ctx context.Context, msg string, data ...interface{}) {
	l.base.Warn(ctx, msg, data...)
}

func (l *legacyBusyFilter) Error(ctx context.Context, msg string, data ...interface{}) {
	l.base.Error(ctx, msg, data...)
}

// Trace keeps every diagnostic except the two expected busy failures inside
// a legacy save's own transactions, which the route retries and answers
// without the SQLSTATE.
func (l *legacyBusyFilter) Trace(ctx context.Context, begin time.Time, fc func() (string, int64), err error) {
	if err != nil {
		sql, rows := fc()
		if legacySaveBusyScope(ctx) && isExpectedLegacyBusy(err, sql) {
			return
		}
		l.base.Trace(ctx, begin, func() (string, int64) { return sql, rows }, err)
		return
	}
	l.base.Trace(ctx, begin, fc, err)
}

func (l *legacyBusyFilter) ParamsFilter(ctx context.Context, sql string, params ...interface{}) (string, []interface{}) {
	if f, ok := l.base.(gorm.ParamsFilter); ok {
		return f.ParamsFilter(ctx, sql, params...)
	}
	return sql, params
}

// isExpectedLegacyBusy reports whether err is lock-not-available from one of
// the two statements the legacy save treats as a busy retry: the actor seal
// selecting the admin row FOR SHARE NOWAIT, or the counterpart blank failing
// in its trigger's NOWAIT share lock on the deleted party.
func isExpectedLegacyBusy(err error, sql string) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	if pgErr.Code != "55P03" {
		return false
	}
	if strings.Contains(sql, "FOR SHARE NOWAIT") && strings.Contains(sql, "FROM users") {
		return true
	}
	if strings.Contains(sql, "UPDATE contact_requests") && strings.Contains(sql, "SET reason = ''") {
		return true
	}
	return false
}

// Migrate applies pending migrations under the migration advisory lock, so two
// processes starting together never migrate at the same time.
func Migrate(ctx context.Context, db *sql.DB) error {
	p, err := migrations.NewProvider(db)
	if err != nil {
		return err
	}
	if _, err := p.Up(ctx); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	return nil
}
