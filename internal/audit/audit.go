// Package audit is the sealed log (docs/DECISIONS.md R6, P17). Only the write
// executor appends to it, in the same transaction as the change. Nothing
// shows it on a page.
//
// Each row stores the SHA-256 of its own fields and of the row before it, so a
// row changed behind the log's back breaks the chain. The bytes hashed are
// fixed by Format; changing them means a new format number, never an edit.
package audit

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"time"

	"fold/internal/db"
)

// Format is the version of the hashed bytes.
const Format = 1

// Actor values that are not a person's identifier.
const (
	Anonymous = "anonymous"
	System    = "system"
)

// Entry is what a command records. It holds identifiers and short structured
// values only: the patterns below refuse names, addresses and free text, so
// nothing personal can land in a log that is never edited.
type Entry struct {
	Action   string
	Actor    string
	Subjects []string
	Details  map[string]string
}

var (
	actionPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)+$`)
	idPattern     = regexp.MustCompile(`^[a-z0-9]{1,40}$`)
	keyPattern    = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)
	valuePattern  = regexp.MustCompile(`^[A-Za-z0-9_.:+-]{0,64}$`)
)

const maxDetails = 16

func (e Entry) check() error {
	if !actionPattern.MatchString(e.Action) {
		return fmt.Errorf("audit: action %q is not a dotted name", e.Action)
	}
	if !idPattern.MatchString(e.Actor) {
		return fmt.Errorf("audit: actor %q is not an identifier", e.Actor)
	}
	for _, s := range e.Subjects {
		if !idPattern.MatchString(s) {
			return fmt.Errorf("audit: subject %q is not an identifier", s)
		}
	}
	if len(e.Details) > maxDetails {
		return errors.New("audit: too many details")
	}
	for k, v := range e.Details {
		if !keyPattern.MatchString(k) || !valuePattern.MatchString(v) {
			return fmt.Errorf("audit: detail %q is not a short structured value", k)
		}
	}
	return nil
}

// Row is one stored row.
type Row struct {
	Seq      int64
	At       int64
	Action   string
	Actor    string
	Subjects string
	Details  string
	Format   int
	Prev     []byte
	Hash     []byte
}

var genesis = make([]byte, sha256.Size)

// hashRow is the format 1 hash: every field written as a length and its
// bytes, in a fixed order, then the previous hash.
func hashRow(r Row) []byte {
	h := sha256.New()
	field := func(b []byte) {
		var n [binary.MaxVarintLen64]byte
		h.Write(n[:binary.PutUvarint(n[:], uint64(len(b)))])
		h.Write(b)
	}
	field([]byte("audit-format-1"))
	field([]byte(strconv.FormatInt(r.Seq, 10)))
	field([]byte(strconv.FormatInt(r.At, 10)))
	field([]byte(r.Action))
	field([]byte(r.Actor))
	field([]byte(r.Subjects))
	field([]byte(r.Details))
	field(r.Prev)
	return h.Sum(nil)
}

// Append adds e at the end of the chain. Call it only from the executor,
// inside the change's own transaction.
func Append(ctx context.Context, tx *sql.Tx, at time.Time, e Entry) (Row, error) {
	if err := e.check(); err != nil {
		return Row{}, err
	}
	if e.Subjects == nil {
		e.Subjects = []string{}
	}
	if e.Details == nil {
		e.Details = map[string]string{}
	}
	subjects, _ := json.Marshal(e.Subjects)
	details, _ := json.Marshal(e.Details) // map keys come out sorted
	last, err := Last(ctx, tx)
	if err != nil {
		return Row{}, err
	}
	r := Row{
		Seq: last.Seq + 1, At: at.Unix(), Action: e.Action, Actor: e.Actor,
		Subjects: string(subjects), Details: string(details), Format: Format, Prev: last.Hash,
	}
	r.Hash = hashRow(r)
	_, err = tx.ExecContext(ctx, `INSERT INTO audit_log (seq, at, action, actor, subjects, details, format, prev_hash, hash)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, r.Seq, r.At, r.Action, r.Actor, r.Subjects, r.Details, r.Format, r.Prev, r.Hash)
	if err != nil {
		return Row{}, fmt.Errorf("audit: append: %w", err)
	}
	return r, nil
}

// Checkpoint is the latest sequence number and hash. Written outside the
// server (into backups, into the admins' monthly email), it is what makes a
// rewrite of the chain show.
type Checkpoint struct {
	Seq  int64  `json:"seq"`
	Hash string `json:"hash"`
}

func (c Checkpoint) String() string { return fmt.Sprintf("%d:%s", c.Seq, c.Hash) }

// Last is the last row's sequence and hash; an empty log is 0 and the
// all-zero hash.
func Last(ctx context.Context, q db.Querier) (Row, error) {
	r := Row{Prev: genesis, Hash: genesis}
	err := q.QueryRowContext(ctx, `SELECT seq, hash FROM audit_log ORDER BY seq DESC LIMIT 1`).Scan(&r.Seq, &r.Hash)
	if errors.Is(err, sql.ErrNoRows) {
		return Row{Hash: genesis}, nil
	}
	return r, err
}

func CheckpointOf(ctx context.Context, q db.Querier) (Checkpoint, error) {
	r, err := Last(ctx, q)
	if err != nil {
		return Checkpoint{}, err
	}
	return Checkpoint{Seq: r.Seq, Hash: hex.EncodeToString(r.Hash)}, nil
}

// Report is the outcome of a chain check.
type Report struct {
	Rows     int64
	Last     Checkpoint
	BrokenAt int64 // 0 when the chain is intact
	Problem  string
}

func (r Report) Intact() bool { return r.BrokenAt == 0 }

// Verify walks the chain in sequence order and recomputes every hash.
func Verify(ctx context.Context, q db.Querier) (Report, error) {
	rows, err := q.QueryContext(ctx, `SELECT seq, at, action, actor, subjects, details, format, prev_hash, hash FROM audit_log ORDER BY seq`)
	if err != nil {
		return Report{}, err
	}
	defer rows.Close()
	rep := Report{Last: Checkpoint{Hash: hex.EncodeToString(genesis)}}
	prev := genesis
	var want int64 = 1
	for rows.Next() {
		var r Row
		if err := rows.Scan(&r.Seq, &r.At, &r.Action, &r.Actor, &r.Subjects, &r.Details, &r.Format, &r.Prev, &r.Hash); err != nil {
			return Report{}, err
		}
		rep.Rows++
		switch {
		case r.Seq != want:
			rep.BrokenAt, rep.Problem = want, fmt.Sprintf("row %d is missing", want)
		case r.Format != Format:
			rep.BrokenAt, rep.Problem = r.Seq, fmt.Sprintf("row %d has unknown format %d", r.Seq, r.Format)
		case string(r.Prev) != string(prev):
			rep.BrokenAt, rep.Problem = r.Seq, fmt.Sprintf("row %d does not follow row %d", r.Seq, r.Seq-1)
		case string(hashRow(r)) != string(r.Hash):
			rep.BrokenAt, rep.Problem = r.Seq, fmt.Sprintf("row %d was changed after it was written", r.Seq)
		}
		if rep.BrokenAt != 0 {
			return rep, rows.Err()
		}
		prev, want = r.Hash, want+1
		rep.Last = Checkpoint{Seq: r.Seq, Hash: hex.EncodeToString(r.Hash)}
	}
	return rep, rows.Err()
}

// Holds reports whether the log still has the row a checkpoint names, with
// the same hash. A checkpoint from an older backup or email that no longer
// matches means the chain was rewritten, unless a restore explains it (the
// "restored" row says from which backup).
func Holds(ctx context.Context, q db.Querier, c Checkpoint) (bool, error) {
	if c.Seq == 0 {
		return true, nil
	}
	var h []byte
	err := q.QueryRowContext(ctx, `SELECT hash FROM audit_log WHERE seq = ?`, c.Seq).Scan(&h)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil && hex.EncodeToString(h) == c.Hash, err
}
