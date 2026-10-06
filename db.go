package main

import (
	"log"
	"os"
	"path/filepath"

	"church-groups/models"

	"github.com/glebarez/sqlite"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// openDatabase connects to Postgres when DATABASE_URL is set, otherwise to
// SQLite (development). It runs schema migrations and returns the handle.
func openDatabase(cfg Config) *gorm.DB {
	var db *gorm.DB
	var err error

	if cfg.DatabaseURL != "" {
		log.Println("Using Postgres")
		db, err = gorm.Open(postgres.Open(cfg.DatabaseURL), &gorm.Config{})
	} else {
		log.Printf("Using SQLite at %s (development only)", cfg.SQLitePath)
		if dir := filepath.Dir(cfg.SQLitePath); dir != "" {
			if mkErr := os.MkdirAll(dir, 0o755); mkErr != nil {
				log.Fatalf("Failed to create SQLite directory: %v", mkErr)
			}
		}
		db, err = gorm.Open(sqlite.Open(cfg.SQLitePath), &gorm.Config{})
	}
	if err != nil {
		log.Fatalf("Failed to connect to database: %v", err)
	}

	if err := db.AutoMigrate(
		&models.User{},
		&models.Group{},
		&models.Meeting{},
		&models.MeetingException{},
		&models.Membership{},
		&models.RSVP{},
		&models.Setting{},
	); err != nil {
		log.Fatalf("Failed to migrate database: %v", err)
	}
	return db
}
