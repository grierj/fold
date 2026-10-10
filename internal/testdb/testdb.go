// Package testdb gives tests real Postgres databases (DEC-1, DEC-24).
//
// DATABASE_URL names a server and a database the tests may connect to with
// rights to create databases. The helper migrates a template database once
// per migration set, then copies it (CREATE DATABASE ... TEMPLATE) for each
// test package or throwaway test. Copies are dropped when they are done.
package testdb

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"

	"fold/migrations"
	"fold/models"

	_ "github.com/jackc/pgx/v5/stdlib"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const templateLock = 7316142 // advisory lock id serializing template builds

var (
	sharedOnce sync.Once
	sharedDB   *gorm.DB
	sharedName string
	sharedErr  error
)

// Main runs a test package and drops its shared database afterwards. Use it
// from TestMain in packages that call Shared.
func Main(m *testing.M) int {
	code := m.Run()
	if sharedName != "" {
		if sqlDB, err := sharedDB.DB(); err == nil {
			sqlDB.Close()
		}
		if err := dropDB(sharedName); err != nil {
			fmt.Fprintf(os.Stderr, "testdb: drop %s: %v\n", sharedName, err)
		}
	}
	return code
}

// Shared returns this package's migrated database, emptied of rows. Tests
// that use it must not call t.Parallel.
func Shared(t testing.TB) *gorm.DB {
	t.Helper()
	sharedOnce.Do(func() {
		sharedName, sharedErr = createDB(true)
		if sharedErr == nil {
			sharedDB, sharedErr = Open(sharedName)
		}
	})
	if sharedErr != nil {
		t.Fatalf("testdb: %v", sharedErr)
	}
	if err := truncate(sharedDB); err != nil {
		t.Fatalf("testdb: truncate: %v", err)
	}
	return sharedDB
}

// Fresh creates a throwaway database for one test (migrated, or empty when
// migrated is false), returns its name, and drops it when the test ends.
func Fresh(t testing.TB, migrated bool) string {
	t.Helper()
	name, err := createDB(migrated)
	if err != nil {
		t.Fatalf("testdb: %v", err)
	}
	t.Cleanup(func() {
		if err := dropDB(name); err != nil {
			t.Errorf("testdb: drop %s: %v", name, err)
		}
	})
	return name
}

// URL returns DATABASE_URL pointed at database name.
func URL(name string) (string, error) {
	base := os.Getenv("DATABASE_URL")
	if base == "" {
		return "", fmt.Errorf("DATABASE_URL is not set; run `make test`, or start Postgres and set it")
	}
	u, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("parse DATABASE_URL: %w", err)
	}
	u.Path = "/" + name
	return u.String(), nil
}

// Open connects GORM to database name.
func Open(name string) (*gorm.DB, error) {
	dsn, err := URL(name)
	if err != nil {
		return nil, err
	}
	return gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Discard})
}

// OpenSQL opens a database/sql handle to database name.
func OpenSQL(name string) (*sql.DB, error) {
	dsn, err := URL(name)
	if err != nil {
		return nil, err
	}
	return sql.Open("pgx", dsn)
}

func admin() (*sql.DB, error) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		return nil, fmt.Errorf("DATABASE_URL is not set; run `make test`, or start Postgres and set it")
	}
	return sql.Open("pgx", dsn)
}

func createDB(migrated bool) (string, error) {
	db, err := admin()
	if err != nil {
		return "", err
	}
	defer db.Close()
	name := "fold_test_" + randomHex()
	if !migrated {
		_, err := db.Exec("CREATE DATABASE " + name)
		return name, err
	}
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "SELECT pg_advisory_lock($1)", templateLock); err != nil {
		return "", err
	}
	defer conn.ExecContext(ctx, "SELECT pg_advisory_unlock($1)", templateLock)
	tmpl, err := ensureTemplate(ctx, conn)
	if err != nil {
		return "", err
	}
	_, err = conn.ExecContext(ctx, fmt.Sprintf("CREATE DATABASE %s TEMPLATE %s", name, tmpl))
	return name, err
}

// ensureTemplate builds the template for the current migration files if it
// is missing, and drops templates left by older migration sets.
func ensureTemplate(ctx context.Context, conn *sql.Conn) (string, error) {
	tmpl := "fold_tmpl_" + migrationsHash()
	var exists bool
	if err := conn.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)", tmpl).Scan(&exists); err != nil {
		return "", err
	}
	if exists {
		return tmpl, nil
	}
	rows, err := conn.QueryContext(ctx, "SELECT datname FROM pg_database WHERE datname LIKE 'fold\\_tmpl\\_%'")
	if err != nil {
		return "", err
	}
	var stale []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			rows.Close()
			return "", err
		}
		stale = append(stale, n)
	}
	rows.Close()
	for _, n := range stale {
		if _, err := conn.ExecContext(ctx, "DROP DATABASE IF EXISTS "+n); err != nil {
			return "", err
		}
	}
	building := tmpl + "_build"
	if _, err := conn.ExecContext(ctx, "DROP DATABASE IF EXISTS "+building); err != nil {
		return "", err
	}
	if _, err := conn.ExecContext(ctx, "CREATE DATABASE "+building); err != nil {
		return "", err
	}
	if err := migrate(building); err != nil {
		return "", err
	}
	// Rename only after a successful migration, so a failed build is never reused.
	if _, err := conn.ExecContext(ctx, fmt.Sprintf("ALTER DATABASE %s RENAME TO %s", building, tmpl)); err != nil {
		return "", err
	}
	return tmpl, nil
}

func migrate(name string) error {
	db, err := OpenSQL(name)
	if err != nil {
		return err
	}
	defer db.Close()
	p, err := migrations.NewProvider(db)
	if err != nil {
		return err
	}
	_, err = p.Up(context.Background())
	return err
}

func dropDB(name string) error {
	db, err := admin()
	if err != nil {
		return err
	}
	defer db.Close()
	_, err = db.Exec("DROP DATABASE IF EXISTS " + name + " WITH (FORCE)")
	return err
}

func truncate(db *gorm.DB) error {
	tables := make([]string, 0, len(models.All()))
	for _, m := range models.All() {
		stmt := &gorm.Statement{DB: db}
		if err := stmt.Parse(m); err != nil {
			return err
		}
		tables = append(tables, stmt.Schema.Table)
	}
	return db.Exec("TRUNCATE TABLE " + strings.Join(tables, ", ") + " RESTART IDENTITY CASCADE").Error
}

func migrationsHash() string {
	h := sha256.New()
	_ = fs.WalkDir(migrations.Files, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := fs.ReadFile(migrations.Files, path)
		if err != nil {
			return err
		}
		h.Write([]byte(path))
		h.Write(b)
		return nil
	})
	return hex.EncodeToString(h.Sum(nil))[:12]
}

func randomHex() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
