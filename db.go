package main

import (
	"context"
	"log"
	"os"

	"fold/internal/dbopen"

	"gorm.io/gorm"
)

// openDatabase connects to Postgres and applies pending migrations under
// the migration advisory lock.
func openDatabase(ctx context.Context, url string) (*gorm.DB, error) {
	db, err := dbopen.Open(url, dbopen.Logger(log.New(os.Stdout, "\r\n", log.LstdFlags)))
	if err != nil {
		return nil, err
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, err
	}
	if err := dbopen.Migrate(ctx, sqlDB); err != nil {
		return nil, err
	}
	return db, nil
}
