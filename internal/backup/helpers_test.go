package backup_test

import (
	"database/sql"
	"path/filepath"

	"fold/internal/backup"
	"fold/internal/db"
	"fold/internal/testenv"
)

func openBackup(env *testenv.Env, name string) (*sql.DB, error) {
	return db.OpenWriter(filepath.Join(backup.Dir(env.Dir), name+".db"))
}
