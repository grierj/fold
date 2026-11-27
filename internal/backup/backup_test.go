package backup_test

import (
	"context"
	"testing"

	"fold/internal/backup"
	"fold/internal/testenv"
)

func TestBackupsInOneSecondKeepBoth(t *testing.T) {
	env := testenv.New(t)
	ctx := context.Background()
	a, err := backup.Take(ctx, env.Exec, env.Dir)
	if err != nil {
		t.Fatal(err)
	}
	b, err := backup.Take(ctx, env.Exec, env.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if a.Name == b.Name || !backup.ValidName(b.Name) {
		t.Fatalf("names %q and %q", a.Name, b.Name)
	}
	// Restoring the first in the same second keeps it intact.
	m, err := backup.Restore(ctx, env.Exec, env.Dir, a.Name)
	if err != nil {
		t.Fatal(err)
	}
	if m.Checkpoint != a.Checkpoint {
		t.Fatalf("the restored backup's checkpoint changed: %+v vs %+v", m.Checkpoint, a.Checkpoint)
	}
	if list, _ := backup.List(env.Dir); len(list) != 3 {
		t.Fatalf("%d backups", len(list))
	}
}

func TestNamesCannotLeaveTheFolder(t *testing.T) {
	for _, n := range []string{"../../etc/passwd", "20261125-100000/../x", "", "20261125-100000.db"} {
		if backup.ValidName(n) {
			t.Errorf("ValidName(%q)", n)
		}
	}
}

func TestRestoreRefusesABrokenChain(t *testing.T) {
	env := testenv.New(t)
	ctx := context.Background()
	backup.Take(ctx, env.Exec, env.Dir) // leaves one row in the live log
	m, err := backup.Take(ctx, env.Exec, env.Dir)
	if err != nil || m.Checkpoint.Seq != 1 {
		t.Fatal(err, m.Checkpoint)
	}
	// Tamper with the backup file's log.
	w, err := openBackup(env, m.Name)
	if err != nil {
		t.Fatal(err)
	}
	w.Exec(`DROP TRIGGER audit_log_no_update`)
	w.Exec(`UPDATE audit_log SET details = '{"name":"x"}'`)
	w.Close()
	if _, err := backup.Restore(ctx, env.Exec, env.Dir, m.Name); err == nil {
		t.Fatal("restored a backup whose log was changed")
	}
}
