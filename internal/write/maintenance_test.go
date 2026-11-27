package write_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"fold/internal/testenv"
	"fold/internal/write"
)

func setting(t *testing.T, env *testenv.Env, v string) {
	t.Helper()
	err := env.Exec.Run(context.Background(), write.System(), fn{"settings.save", func(tx *write.Tx) error {
		_, err := tx.Exec(`INSERT INTO settings (key, value, updated_at) VALUES ('k', ?, 0)
			ON CONFLICT (key) DO UPDATE SET value = excluded.value`, v)
		return err
	}})
	if err != nil {
		t.Fatal(err)
	}
}

// The spike the restore design rests on: the backup interface replaces the
// live WAL database while the read pool is open and one reader is in the
// middle of a transaction.
func TestReplaceWithUnderOpenReaders(t *testing.T) {
	env := testenv.New(t)
	ctx := context.Background()
	setting(t, env, "old")
	copyPath := filepath.Join(env.Dir, "copy.db")
	cp, err := env.Exec.VacuumInto(ctx, copyPath)
	if err != nil {
		t.Fatal(err)
	}
	if cp.Seq != 1 {
		t.Fatalf("checkpoint %+v", cp)
	}
	setting(t, env, "new")

	inTx := make(chan string)
	release := make(chan struct{})
	finished := make(chan error)
	go func() {
		finished <- env.Reader.Read(ctx, func(q *sql.Tx) error {
			var v string
			q.QueryRow(`SELECT value FROM settings WHERE key = 'k'`).Scan(&v)
			inTx <- v
			<-release
			return nil
		})
	}()
	if v := <-inTx; v != "new" {
		t.Fatalf("reader saw %q", v)
	}
	done := make(chan error)
	go func() { done <- env.Exec.ReplaceWith(ctx, copyPath) }()
	close(release)
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	var v string
	env.Reader.Read(ctx, func(q *sql.Tx) error {
		return q.QueryRow(`SELECT value FROM settings WHERE key = 'k'`).Scan(&v)
	})
	if v != "old" {
		t.Fatalf("after restore a reader sees %q, want old", v)
	}
	if rep, _ := env.Exec.Verify(ctx); !rep.Intact() || rep.Last.Seq != 1 {
		t.Fatalf("chain after restore %+v", rep)
	}
	// The executor still writes.
	setting(t, env, "after")
	if rep, _ := env.Exec.Verify(ctx); !rep.Intact() || rep.Last.Seq != 2 {
		t.Fatalf("chain after a later write %+v", rep)
	}
}
