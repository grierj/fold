// Package dbopen is the one place that opens Postgres through GORM and applies
// the goose migrations, for the server, the CLI and the test database.
package dbopen

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"fold/internal/txguard"
	"fold/migrations"

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
	return logger.New(writer, logger.Config{
		SlowThreshold:             200 * time.Millisecond,
		LogLevel:                  logger.Warn,
		IgnoreRecordNotFoundError: false,
		Colorful:                  true,
		ParameterizedQueries:      true,
	})
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
