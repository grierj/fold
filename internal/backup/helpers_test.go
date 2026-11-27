package backup_test

import (
	"database/sql"
	"os"
	"path/filepath"

	"fold/internal/backup"
	"fold/internal/db"
	"fold/internal/testenv"
)

func openBackup(env *testenv.Env, name string) (*sql.DB, error) {
	return db.OpenWriter(filepath.Join(backup.Dir(env.Dir), name+".db"))
}

func removeDB(p string) {
	for _, s := range []string{"", "-wal", "-shm"} {
		os.Remove(p + s)
	}
}
