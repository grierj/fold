package main

import (
	"context"
	"database/sql"
	"fmt"

	"fold/migrations"

	_ "github.com/jackc/pgx/v5/stdlib"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// openDatabase connects to Postgres and applies pending migrations under
// the migration advisory lock. GORM never changes the schema here: the
// goose files are the source of truth (C1).
func openDatabase(ctx context.Context, url string) (*gorm.DB, error) {
	db, err := gorm.Open(postgres.Open(url), &gorm.Config{})
	if err != nil {
		return nil, fmt.Errorf("connect to database: %w", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, err
	}
	if err := migrateUp(ctx, sqlDB); err != nil {
		return nil, err
	}
	return db, nil
}

func migrateUp(ctx context.Context, db *sql.DB) error {
	p, err := migrations.NewProvider(db)
	if err != nil {
		return err
	}
	if _, err := p.Up(ctx); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	return nil
}
