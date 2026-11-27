package backup_test

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"

	"fold/internal/audit"
	"fold/internal/backup"
	"fold/internal/db"
	"fold/internal/keys"
	"fold/internal/settings"
	"fold/internal/testenv"
	"fold/internal/write"
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

func TestRestoreTheOldestOfAFullSet(t *testing.T) {
	env := testenv.New(t)
	ctx := context.Background()
	var oldest backup.Meta
	for i := range backup.Keep {
		m, err := backup.Take(ctx, env.Exec, env.Dir)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			oldest = m
		}
		env.Clock.Advance(time.Hour)
	}
	if _, err := backup.Restore(ctx, env.Exec, env.Dir, oldest.Name); err != nil {
		t.Fatalf("restoring the oldest backup: %v", err)
	}
	if list, _ := backup.List(env.Dir); len(list) != backup.Keep {
		t.Fatalf("%d backups kept", len(list))
	}
}

func TestOfflineRestoreNeverOpensTheLiveDatabase(t *testing.T) {
	for name, damage := range map[string]func(path string){
		"garbage": func(p string) { os.WriteFile(p, []byte("not a database at all, not even close"), 0o600) },
		"newer schema": func(p string) {
			w, _ := db.OpenWriter(p)
			w.Exec("PRAGMA user_version = 99")
			w.Close()
		},
	} {
		t.Run(name, func(t *testing.T) {
			env := testenv.New(t)
			ctx := context.Background()
			m, err := backup.Take(ctx, env.Exec, env.Dir)
			if err != nil {
				t.Fatal(err)
			}
			env.Writer.Close()
			env.Reader.Close()
			removeDB(env.Path)
			damage(env.Path)
			if _, err := backup.RestoreOffline(ctx, env.Dir, env.Path, m.Name, env.Clock, env.Keys); err != nil {
				t.Fatal(err)
			}
			w, err := db.OpenWriter(env.Path)
			if err != nil {
				t.Fatal(err)
			}
			defer w.Close()
			rep, err := audit.Verify(ctx, w)
			if err != nil || !rep.Intact() || rep.Last.Seq != m.Checkpoint.Seq+1 {
				t.Fatalf("after offline restore: %+v %v", rep, err)
			}
			if v, _ := db.Version(ctx, w); v != db.Latest() {
				t.Fatalf("schema %d", v)
			}
		})
	}
}

func TestBeforeMigrateCopyIsARestorableBackup(t *testing.T) {
	env := testenv.New(t)
	ctx := context.Background()
	m, err := backup.BeforeMigrate(ctx, env.Writer, env.Dir, env.Clock.Now(), 1)
	if err != nil {
		t.Fatal(err)
	}
	list, _ := backup.List(env.Dir)
	if len(list) != 1 || list[0].Note == "" || !backup.ValidName(m.Name) {
		t.Fatalf("list %+v", list)
	}
	env.Clock.Advance(time.Second)
	if _, err := backup.Restore(ctx, env.Exec, env.Dir, m.Name); err != nil {
		t.Fatal(err)
	}
}

// A backup whose mail settings were sealed with a key the server no longer
// has is refused, and the live database is left as it was. With the old key
// as the previous one it restores and is resealed with the current key.
func TestRestoreRefusesSettingsItCannotOpen(t *testing.T) {
	env := testenv.New(t)
	ctx := context.Background()
	oldHex, newHex := strings.Repeat("ab", 32), strings.Repeat("cd", 32)
	oldKeys, _ := keys.Parse(oldHex, "")
	before := write.New(env.Writer, env.Clock, oldKeys)
	if err := before.Run(ctx, write.System(), sealToken{}); err != nil {
		t.Fatal(err)
	}
	m, err := backup.Take(ctx, before, env.Dir)
	if err != nil {
		t.Fatal(err)
	}
	env.Clock.Advance(time.Minute)

	newKeys, _ := keys.Parse(newHex, "")
	after := write.New(env.Writer, env.Clock, newKeys)
	tip, _ := after.Checkpoint(ctx)
	if _, err := backup.Restore(ctx, after, env.Dir, m.Name); err == nil || !strings.Contains(err.Error(), "SECRET_KEY_PREVIOUS") {
		t.Fatalf("restore with the wrong key: %v", err)
	}
	if now, _ := after.Checkpoint(ctx); now.Seq != tip.Seq+1 { // only the safety copy's row
		t.Fatalf("the live log moved from %d to %d", tip.Seq, now.Seq)
	}

	rotating, _ := keys.Parse(newHex, oldHex)
	if _, err := backup.Restore(ctx, write.New(env.Writer, env.Clock, rotating), env.Dir, m.Name); err != nil {
		t.Fatal(err)
	}
	var s settings.Settings
	env.Reader.Read(ctx, func(q *sql.Tx) error { var err error; s, err = settings.Load(ctx, q); return err })
	if got, err := s.Open(newKeys, settings.PostmarkToken); err != nil || got != "pm-token" {
		t.Fatalf("not resealed with the current key: %q %v", got, err)
	}
}

type sealToken struct{}

func (sealToken) Action() string { return "settings.save" }
func (sealToken) Apply(tx *write.Tx) error {
	return settings.SaveSealed(tx, settings.PostmarkToken, "pm-token")
}
