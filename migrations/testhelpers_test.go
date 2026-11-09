package migrations_test

// Scaffolding shared by the tests of one migration: a fresh database at a
// version, a row-snapshot diff, an exact SQLSTATE check, a way down and up,
// a held table lock and the previous-writer comparison. A migration's own
// file keeps only its fixture and its cases.

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"fold/internal/testdb"
	"fold/migrations"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/pressly/goose/v3"
)

func newProvider(t *testing.T, db *sql.DB) *goose.Provider {
	t.Helper()
	p, err := migrations.NewProvider(db)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// freshAt returns a new empty database name, its connection and a provider,
// migrated up to version when version is above 0.
func freshAt(t *testing.T, version int64) (string, *sql.DB, *goose.Provider) {
	t.Helper()
	name := testdb.Fresh(t, false)
	db := openSQL(t, name)
	p := newProvider(t, db)
	if version > 0 {
		upTo(t, p, version)
	}
	return name, db, p
}

func upTo(t *testing.T, p *goose.Provider, version int64) {
	t.Helper()
	if _, err := p.UpTo(context.Background(), version); err != nil {
		t.Fatalf("up to %d: %v", version, err)
	}
}

// downTo rolls back to version and checks goose agrees where it stopped.
func downTo(t *testing.T, p *goose.Provider, version int64) {
	t.Helper()
	ctx := context.Background()
	if _, err := p.DownTo(ctx, version); err != nil {
		t.Fatalf("down to %d: %v", version, err)
	}
	if got, err := p.GetDBVersion(ctx); err != nil || got != version {
		t.Fatalf("version after the down = %d, %v, want %d", got, err, version)
	}
}

// timedUpTo applies the migration up to version and reports how long it took.
// A limit above zero is the context deadline.
func timedUpTo(p *goose.Provider, version int64, limit time.Duration) (time.Duration, error) {
	ctx := context.Background()
	if limit > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, limit)
		defer cancel()
	}
	start := time.Now()
	_, err := p.UpTo(ctx, version)
	return time.Since(start), err
}

// holdLock opens another session on the database and keeps a transaction that
// took lockSQL open until the test ends or the caller rolls it back.
func holdLock(t *testing.T, name, lockSQL string) *sql.Tx {
	t.Helper()
	tx, err := openSQL(t, name).BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tx.Rollback() })
	if _, err := tx.Exec(lockSQL); err != nil {
		t.Fatal(err)
	}
	return tx
}

// requireBlocked is the control for a held lock: a third session cannot read
// the table, and gives up with lock_not_available.
func requireBlocked(t *testing.T, name, table string) {
	t.Helper()
	ctx := context.Background()
	probe, err := openSQL(t, name).Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer probe.Close()
	if _, err := probe.ExecContext(ctx, `SET lock_timeout = '200ms'`); err != nil {
		t.Fatal(err)
	}
	if _, err := probe.ExecContext(ctx, `SELECT count(*) FROM `+table); pgErrorCode(err) != "55P03" {
		t.Fatalf("a read of %s under the held lock: %v (code %q), want lock_not_available 55P03", table, err, pgErrorCode(err))
	}
}

// wantPgError checks SQLSTATE and, when msg is not empty, the message.
func wantPgError(t *testing.T, label string, err error, code, msg string) {
	t.Helper()
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Errorf("%s: %v, want %s %q", label, err, code, msg)
		return
	}
	if pgErr.Code != code || (msg != "" && pgErr.Message != msg) {
		t.Errorf("%s: %s %q, want %s %q", label, pgErr.Code, pgErr.Message, code, msg)
	}
}

// snapshotRows runs a query of (id, document) pairs, usually a table's rows as
// jsonb text, and returns the documents by id.
func snapshotRows[K comparable](t *testing.T, db *sql.DB, q string) map[K]string {
	t.Helper()
	rs, err := db.Query(q)
	if err != nil {
		t.Fatal(err)
	}
	defer rs.Close()
	out := map[K]string{}
	for rs.Next() {
		var id K
		var doc string
		if err := rs.Scan(&id, &doc); err != nil {
			t.Fatal(err)
		}
		out[id] = doc
	}
	if err := rs.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// sameRows reports every id whose row differs between two snapshots. skip
// names ids to leave out of the comparison.
func sameRows[K comparable](t *testing.T, label string, got, want map[K]string, skip ...K) {
	t.Helper()
	skipped := map[K]bool{}
	for _, id := range skip {
		skipped[id] = true
	}
	if len(got) != len(want) {
		t.Errorf("%s: %d rows, want %d", label, len(got), len(want))
	}
	for id, doc := range want {
		if skipped[id] {
			continue
		}
		if got[id] != doc {
			t.Errorf("%s: row %v is %s, want %s", label, id, got[id], doc)
		}
	}
}

// queryLines runs a query of one text column and joins the rows with newlines.
func queryLines(t *testing.T, db *sql.DB, q string) string {
	t.Helper()
	rs, err := db.Query(q)
	if err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	defer rs.Close()
	var out []string
	for rs.Next() {
		var s string
		if err := rs.Scan(&s); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	if err := rs.Err(); err != nil {
		t.Fatal(err)
	}
	return strings.Join(out, "\n")
}

// procSource is the stored body of one function, named as regprocedure takes it
// ("public.reject_consent_change()").
func procSource(t *testing.T, db *sql.DB, signature string) string {
	t.Helper()
	return queryLines(t, db, `SELECT prosrc FROM pg_proc WHERE oid = '`+signature+`'::regprocedure`)
}

// collapseSQL collapses runs of whitespace to one space outside string
// literals, trims both ends and keeps case.
func collapseSQL(s string) string {
	var b strings.Builder
	inQuote, pendingSpace := false, false
	for _, r := range s {
		if !inQuote && (r == ' ' || r == '\n' || r == '\t' || r == '\r') {
			pendingSpace = b.Len() > 0
			continue
		}
		if pendingSpace {
			b.WriteByte(' ')
			pendingSpace = false
		}
		if r == '\'' {
			inQuote = !inQuote
		}
		b.WriteRune(r)
	}
	return b.String()
}

// bodyFidelity proves a replaced function body is the previous body with
// `added` in front of the statement that begins with anchor, byte for byte: the
// new source from its anchor on equals the previous source from its anchor on,
// and the text before it, whitespace collapsed outside string literals, equals
// added. With keepHead the previous text in front of the anchor must also
// survive unchanged at the start of the new source, and only what follows it is
// compared with added; without it the whole text in front of the anchor is.
func bodyFidelity(t *testing.T, label, previous, current, anchor, added string, keepHead bool) {
	t.Helper()
	i, j := strings.Index(previous, anchor), strings.Index(current, anchor)
	if i < 0 {
		t.Errorf("%s: the previous source does not contain %q", label, anchor)
		return
	}
	if j < 0 {
		t.Errorf("%s: the new source does not contain the previous body's statement %q", label, anchor)
		return
	}
	if current[j:] != previous[i:] {
		t.Errorf("%s: the new source from the previous statement is not the previous body byte for byte:\n%q\nwant\n%q", label, current[j:], previous[i:])
	}
	head := ""
	if keepHead {
		head = previous[:i]
		if !strings.HasPrefix(current, head) {
			t.Errorf("%s: the preexisting body in front of the added text changed", label)
			return
		}
	}
	if got := collapseSQL(current[len(head):j]); got != added {
		t.Errorf("%s: the added text is\n%s\nwant\n%s", label, got, added)
	}
}

// samePreviousWriter plays one previous-writer script at two versions and
// requires the same outcomes and the same final rows. run reports what each
// statement did and the rows it left.
func samePreviousWriter(t *testing.T, before, after int64, run func(*testing.T, int64) ([]string, map[int]string)) {
	t.Helper()
	previousOut, previousRows := run(t, before)
	currentOut, currentRows := run(t, after)
	if p, c := strings.Join(previousOut, "\n"), strings.Join(currentOut, "\n"); p != c {
		t.Errorf("the previous writer's outcomes differ at version %d:\n%s\nwant (version %d)\n%s", after, c, before, p)
	}
	sameRows(t, "rows after the previous writer's script", currentRows, previousRows)
}
