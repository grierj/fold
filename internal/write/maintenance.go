package write

import (
	"context"
	"errors"
	"fmt"

	"fold/internal/audit"

	"modernc.org/sqlite"
)

// VacuumInto writes a consistent copy of the database to path and returns
// the audit checkpoint the copy ends at. SQLite's VACUUM INTO reads one
// snapshot; copying the live file could tear (docs/HAZARDS.md section 9).
func (e *Executor) VacuumInto(ctx context.Context, path string) (audit.Checkpoint, error) {
	conn, err := e.w.Conn(ctx)
	if err != nil {
		return audit.Checkpoint{}, err
	}
	defer conn.Close()
	// Read the checkpoint and copy under one write lock, so no change lands
	// between the two.
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return audit.Checkpoint{}, err
	}
	cp, err := audit.CheckpointOf(ctx, conn)
	if err != nil {
		conn.ExecContext(ctx, "ROLLBACK")
		return audit.Checkpoint{}, err
	}
	if _, err := conn.ExecContext(ctx, "ROLLBACK"); err != nil {
		return audit.Checkpoint{}, err
	}
	// VACUUM cannot run inside a transaction. Nothing else writes on this
	// connection meanwhile: it is the only writer and we hold it.
	if _, err := conn.ExecContext(ctx, "VACUUM INTO ?", path); err != nil {
		return audit.Checkpoint{}, fmt.Errorf("write: copy database: %w", err)
	}
	return cp, nil
}

type restorer interface {
	NewRestore(srcURI string) (*sqlite.Backup, error)
}

// ReplaceWith copies a prepared database file over the live one in a single
// step, using SQLite's backup interface on the writer connection. The source
// must already be complete: integrity checked, migrated, sessions ended, links
// expired and its "restored" audit row written (package backup prepares it),
// so a crash leaves either the old database or the finished one.
func (e *Executor) ReplaceWith(ctx context.Context, preparedPath string) error {
	conn, err := e.w.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	return conn.Raw(func(dc any) error {
		r, ok := dc.(restorer)
		if !ok {
			return errors.New("write: the driver cannot restore")
		}
		b, err := r.NewRestore(preparedPath)
		if err != nil {
			return err
		}
		if _, err := b.Step(-1); err != nil {
			b.Finish()
			return fmt.Errorf("write: restore: %w", err)
		}
		return b.Finish()
	})
}

// Checkpoint is the live log's latest sequence and hash.
func (e *Executor) Checkpoint(ctx context.Context) (audit.Checkpoint, error) {
	return audit.CheckpointOf(ctx, e.w)
}

// Verify checks the live chain.
func (e *Executor) Verify(ctx context.Context) (audit.Report, error) { return audit.Verify(ctx, e.w) }

// IntegrityCheck runs SQLite's own check.
func (e *Executor) IntegrityCheck(ctx context.Context) error {
	var res string
	if err := e.w.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&res); err != nil {
		return err
	}
	if res != "ok" {
		return fmt.Errorf("write: integrity check: %s", res)
	}
	return nil
}
