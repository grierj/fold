// Package dbopen is the one place that opens Postgres through GORM and applies
// the goose migrations, for the server, the CLI and the test database.
package dbopen

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
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
	return newLegacyBusyFilter(writer, logger.Config{
		SlowThreshold:             200 * time.Millisecond,
		LogLevel:                  logger.Warn,
		IgnoreRecordNotFoundError: false,
		Colorful:                  true,
		ParameterizedQueries:      true,
	})
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
// reaches the log with redaction unchanged.
//
// The filter formats diagnostics itself instead of delegating to GORM's
// logger: a delegating wrapper adds an external stack frame, so GORM's
// FileWithLineNum would report this package rather than the real query
// caller on every unrelated error and slow trace (T301-S4-R2). The level,
// slow-query, record-not-found and redaction behavior below mirrors the
// default logger at the pinned GORM version.
type legacyBusyFilter struct {
	writer                              logger.Writer
	config                              logger.Config
	infoStr, warnStr, errStr            string
	traceStr, traceErrStr, traceWarnStr string
}

func newLegacyBusyFilter(writer logger.Writer, config logger.Config) *legacyBusyFilter {
	l := &legacyBusyFilter{writer: writer, config: config}
	if config.Colorful {
		l.infoStr = logger.Green + "%s\n" + logger.Reset + logger.Green + "[info] " + logger.Reset
		l.warnStr = logger.BlueBold + "%s\n" + logger.Reset + logger.Magenta + "[warn] " + logger.Reset
		l.errStr = logger.Magenta + "%s\n" + logger.Reset + logger.Red + "[error] " + logger.Reset
		l.traceStr = logger.Green + "%s\n" + logger.Reset + logger.Yellow + "[%.3fms] " + logger.BlueBold + "[rows:%v]" + logger.Reset + " %s"
		l.traceWarnStr = logger.Green + "%s " + logger.Yellow + "%s\n" + logger.Reset + logger.RedBold + "[%.3fms] " + logger.Yellow + "[rows:%v]" + logger.Magenta + " %s" + logger.Reset
		l.traceErrStr = logger.RedBold + "%s " + logger.MagentaBold + "%s\n" + logger.Reset + logger.Yellow + "[%.3fms] " + logger.BlueBold + "[rows:%v]" + logger.Reset + " %s"
	} else {
		l.infoStr = "%s\n[info] "
		l.warnStr = "%s\n[warn] "
		l.errStr = "%s\n[error] "
		l.traceStr = "%s\n[%.3fms] [rows:%v] %s"
		l.traceWarnStr = "%s %s\n[%.3fms] [rows:%v] %s"
		l.traceErrStr = "%s %s\n[%.3fms] [rows:%v] %s"
	}
	return l
}

func (l *legacyBusyFilter) LogMode(level logger.LogLevel) logger.Interface {
	cp := *l
	cp.config.LogLevel = level
	return &cp
}

func (l *legacyBusyFilter) Info(ctx context.Context, msg string, data ...interface{}) {
	if l.config.LogLevel >= logger.Info {
		l.writer.Printf(l.infoStr+msg, append([]interface{}{callerLocation()}, data...)...)
	}
}

func (l *legacyBusyFilter) Warn(ctx context.Context, msg string, data ...interface{}) {
	if l.config.LogLevel >= logger.Warn {
		l.writer.Printf(l.warnStr+msg, append([]interface{}{callerLocation()}, data...)...)
	}
}

func (l *legacyBusyFilter) Error(ctx context.Context, msg string, data ...interface{}) {
	if l.config.LogLevel >= logger.Error {
		l.writer.Printf(l.errStr+msg, append([]interface{}{callerLocation()}, data...)...)
	}
}

// Trace keeps every diagnostic except the two expected busy failures inside
// a legacy save's own transactions, which the route retries and answers
// without the SQLSTATE.
func (l *legacyBusyFilter) Trace(ctx context.Context, begin time.Time, fc func() (string, int64), err error) {
	if l.config.LogLevel <= logger.Silent {
		return
	}
	elapsed := time.Since(begin)
	switch {
	case err != nil && l.config.LogLevel >= logger.Error && (!errors.Is(err, logger.ErrRecordNotFound) || !l.config.IgnoreRecordNotFoundError):
		sql, rows := fc()
		if legacySaveBusyScope(ctx) && isExpectedLegacyBusy(err, sql) {
			return
		}
		caller := callerLocation()
		if rows == -1 {
			l.writer.Printf(l.traceErrStr, caller, err, float64(elapsed.Nanoseconds())/1e6, "-", sql)
		} else {
			l.writer.Printf(l.traceErrStr, caller, err, float64(elapsed.Nanoseconds())/1e6, rows, sql)
		}
	case elapsed > l.config.SlowThreshold && l.config.SlowThreshold != 0 && l.config.LogLevel >= logger.Warn:
		sql, rows := fc()
		slowLog := fmt.Sprintf("SLOW SQL >= %v", l.config.SlowThreshold)
		caller := callerLocation()
		if rows == -1 {
			l.writer.Printf(l.traceWarnStr, caller, slowLog, float64(elapsed.Nanoseconds())/1e6, "-", sql)
		} else {
			l.writer.Printf(l.traceWarnStr, caller, slowLog, float64(elapsed.Nanoseconds())/1e6, rows, sql)
		}
	case l.config.LogLevel == logger.Info:
		sql, rows := fc()
		caller := callerLocation()
		if rows == -1 {
			l.writer.Printf(l.traceStr, caller, float64(elapsed.Nanoseconds())/1e6, "-", sql)
		} else {
			l.writer.Printf(l.traceStr, caller, float64(elapsed.Nanoseconds())/1e6, rows, sql)
		}
	}
}

func (l *legacyBusyFilter) ParamsFilter(ctx context.Context, sql string, params ...interface{}) (string, []interface{}) {
	if l.config.ParameterizedQueries {
		return sql, nil
	}
	return sql, params
}

// dbopenSourceFile is this file, whose frames the caller lookup skips along
// with GORM's internals.
var dbopenSourceFile string

func init() {
	_, file, _, _ := runtime.Caller(0)
	dbopenSourceFile = filepath.ToSlash(file)
}

// callerLocation reports the application file:line for a diagnostic. It walks
// out past this filter's own frames and GORM's internals the way GORM's
// logger does, so test files and application frames are kept while generated
// files are skipped.
func callerLocation() string {
	const depth = 13
	pcs := make([]uintptr, depth)
	// Skip Callers, callerLocation and the filter method that called it.
	n := runtime.Callers(3, pcs)
	frames := runtime.CallersFrames(pcs[:n])
	for i := 0; i < n; i++ {
		frame, _ := frames.Next()
		if frame.PC == 0 {
			break
		}
		file := filepath.ToSlash(frame.File)
		if file == dbopenSourceFile || strings.HasSuffix(file, ".gen.go") {
			continue
		}
		if strings.Contains(file, "gorm.io/gorm") && !strings.HasSuffix(file, "_test.go") {
			continue
		}
		return fmt.Sprintf("%s:%d", frame.File, frame.Line)
	}
	return ""
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
