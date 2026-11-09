package testdb

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
)

// These tests prove every proxy mode against the real server. A mode is proved
// by what the server shows (a row, a backend in pg_stat_activity) together
// with what the proxy log says reached it.

type proxyRig struct {
	t      *testing.T
	name   string
	proxy  *Proxy
	direct *gorm.DB // straight to the server
	via    *gorm.DB // through the proxy, one connection
}

func newProxyRig(t *testing.T) *proxyRig {
	t.Helper()
	name := Fresh(t, false)
	direct, err := Open(name)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if sqlDB, err := direct.DB(); err == nil {
			sqlDB.Close()
		}
	})
	if err := direct.Exec("CREATE TABLE proxy_rows (id int PRIMARY KEY)").Error; err != nil {
		t.Fatal(err)
	}
	p := NewProxy(t)
	via, err := p.Open(name)
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := via.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	return &proxyRig{t: t, name: name, proxy: p, direct: direct, via: via}
}

func (r *proxyRig) rows() int64 {
	r.t.Helper()
	var n int64
	if err := r.direct.Raw("SELECT count(*) FROM proxy_rows").Scan(&n).Error; err != nil {
		r.t.Fatal(err)
	}
	return n
}

func (r *proxyRig) backendExists(pid uint32) bool {
	r.t.Helper()
	var n int64
	if err := r.direct.Raw("SELECT count(*) FROM pg_stat_activity WHERE pid = ?", pid).Scan(&n).Error; err != nil {
		r.t.Fatal(err)
	}
	return n > 0
}

func (r *proxyRig) waitFor(what string, within time.Duration, cond func() bool) {
	r.t.Helper()
	WaitUntil(r.t, what, within, cond)
}

// pidOf is the backend of the first logged statement containing sql.
func (r *proxyRig) pidOf(sql string) uint32 {
	r.t.Helper()
	var pid uint32
	r.waitFor("a logged statement "+sql, 5*time.Second, func() bool {
		for _, e := range r.proxy.Find(KindStatement, "") {
			if e.PID != 0 && contains(e.SQL, sql) {
				pid = e.PID
				return true
			}
		}
		return false
	})
	return pid
}

func contains(s, sub string) bool {
	return strings.Contains(strings.ToLower(s), strings.ToLower(sub))
}

// commitInTx inserts one row in a transaction and commits it on its own
// goroutine, so a test can look at the server while COMMIT is held.
func (r *proxyRig) commitInTx() <-chan error {
	tx := r.via.Begin()
	if tx.Error != nil {
		r.t.Fatal(tx.Error)
	}
	if err := tx.Exec("INSERT INTO proxy_rows VALUES (1)").Error; err != nil {
		r.t.Fatal(err)
	}
	ch := make(chan error, 1)
	go func() { ch <- tx.Commit().Error }()
	return ch
}

func TestProxyForwardsUnchanged(t *testing.T) {
	r := newProxyRig(t)
	if err := r.via.Transaction(func(tx *gorm.DB) error {
		return tx.Exec("INSERT INTO proxy_rows VALUES (1)").Error
	}); err != nil {
		t.Fatal(err)
	}
	if n := r.rows(); n != 1 {
		t.Fatalf("rows = %d, want 1", n)
	}
	var sawInsert, sawCommit bool
	var last time.Time
	for _, e := range r.proxy.Events() {
		if e.Time.Before(last) {
			t.Errorf("log out of order at %+v", e)
		}
		last = e.Time
		if e.Kind == KindStatement && contains(e.SQL, "INSERT INTO proxy_rows") && e.Action == ActionForwarded {
			sawInsert = true
		}
		if e.Kind == KindCommit && e.Action == ActionForwarded {
			sawCommit = true
		}
	}
	if !sawInsert || !sawCommit {
		t.Errorf("log lacks the insert (%v) or the commit (%v): %+v", sawInsert, sawCommit, r.proxy.Events())
	}
}

// A server error after DataRows has no CommandComplete. The next result on the
// same connection must not inherit the failed statement's observed frames.
func TestProxyCountsFailedPartialResults(t *testing.T) {
	r := newProxyRig(t)
	db, err := r.via.DB()
	if err != nil {
		t.Fatal(err)
	}
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	tx, err := conn.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := tx.QueryContext(context.Background(), "SELECT CASE WHEN x = 4 THEN 1 / (x - 4) ELSE x END FROM generate_series(1, 5) x")
	if err == nil {
		for rows.Next() {
			var n int
			if err = rows.Scan(&n); err != nil {
				break
			}
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
	}
	if err == nil {
		t.Fatal("expected a partial-result error")
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := conn.QueryRowContext(context.Background(), "SELECT 42").Scan(&n); err != nil || n != 42 {
		t.Fatalf("subsequent query: %d, %v", n, err)
	}
	var failed, rollback, next *TagEvent
	for _, tag := range r.proxy.Tags() {
		tag := tag
		if strings.Contains(tag.SQL, "generate_series") && tag.Failed {
			failed = &tag
		}
		if strings.EqualFold(strings.TrimSpace(tag.SQL), "rollback") {
			rollback = &tag
		}
		if strings.Contains(tag.SQL, "SELECT 42") && tag.Tag != "" {
			next = &tag
		}
	}
	if failed == nil || failed.Tag != "" || failed.Rows != 3 || failed.Forwarded != 3 {
		t.Fatalf("failed result: %+v", failed)
	}
	if rollback == nil || rollback.Rows != 0 || rollback.Forwarded != 0 {
		t.Fatalf("rollback result: %+v", rollback)
	}
	if next == nil || next.Rows != 1 || next.Forwarded != 1 || next.Conn != failed.Conn || rollback.Conn != failed.Conn {
		t.Fatalf("subsequent result: %+v", next)
	}
}

func TestProxyCommitModes(t *testing.T) {
	t.Run("a: reply held", func(t *testing.T) {
		r := newProxyRig(t)
		r.proxy.Arm(Faults{Match: "insert into proxy_rows", Commit: CommitHoldReply})
		done := r.commitInTx()
		r.waitFor("the row to commit on the server", 5*time.Second, func() bool { return r.rows() == 1 })
		select {
		case err := <-done:
			t.Fatalf("COMMIT returned (%v) while its reply was held", err)
		case <-time.After(300 * time.Millisecond):
		}
		if held := r.proxy.Find(KindCommit, ActionHeld); len(held) != 1 {
			t.Errorf("held commits = %d, want 1", len(held))
		}
		r.proxy.ReleaseReply()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("commit after release: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("COMMIT never returned after the release")
		}
	})
	t.Run("b: forwarded, client closed before the reply", func(t *testing.T) {
		r := newProxyRig(t)
		r.proxy.Arm(Faults{Match: "insert into proxy_rows", Commit: CommitForwardThenClose})
		err := <-r.commitInTx()
		if err == nil {
			t.Fatal("COMMIT answered although the client was cut before the reply")
		}
		r.waitFor("the row to commit on the server", 5*time.Second, func() bool { return r.rows() == 1 })
	})
	t.Run("c: closed without forwarding", func(t *testing.T) {
		r := newProxyRig(t)
		r.proxy.Arm(Faults{Match: "insert into proxy_rows", Commit: CommitCloseUnforwarded})
		done := r.commitInTx()
		pid := r.pidOf("insert into proxy_rows")
		err := <-done
		if err == nil {
			t.Fatal("COMMIT answered although it was never forwarded")
		}
		if d := r.proxy.Find(KindCommit, ActionDropped); len(d) != 1 {
			t.Errorf("dropped commits = %d, want 1", len(d))
		}
		if fwd := r.proxy.Find(KindCommit, ActionForwarded); len(fwd) != 0 {
			t.Errorf("a COMMIT was forwarded: %+v", fwd)
		}
		r.waitFor("the backend to end", 5*time.Second, func() bool { return !r.backendExists(pid) })
		if n := r.rows(); n != 0 {
			t.Errorf("rows = %d, want 0 (rolled back)", n)
		}
	})
}

func TestProxyRollbackModes(t *testing.T) {
	rollback := func(r *proxyRig) error {
		tx := r.via.Begin()
		if tx.Error != nil {
			t.Fatal(tx.Error)
		}
		if err := tx.Exec("INSERT INTO proxy_rows VALUES (1)").Error; err != nil {
			t.Fatal(err)
		}
		return tx.Rollback().Error
	}
	t.Run("g: closed without forwarding", func(t *testing.T) {
		r := newProxyRig(t)
		r.proxy.Arm(Faults{Match: "insert into proxy_rows", Rollback: RollbackCloseUnforwarded})
		if err := rollback(r); err == nil {
			t.Fatal("ROLLBACK answered although it was never forwarded")
		}
		if d := r.proxy.Find(KindRollback, ActionDropped); len(d) != 1 {
			t.Errorf("dropped rollbacks = %d, want 1", len(d))
		}
		if fwd := r.proxy.Find(KindRollback, ActionForwarded); len(fwd) != 0 {
			t.Errorf("a ROLLBACK was forwarded: %+v", fwd)
		}
	})
	t.Run("g: forwarded, client closed before the reply", func(t *testing.T) {
		r := newProxyRig(t)
		r.proxy.Arm(Faults{Match: "insert into proxy_rows", Rollback: RollbackForwardThenClose})
		if err := rollback(r); err == nil {
			t.Fatal("ROLLBACK answered although the client was cut before the reply")
		}
		if fwd := r.proxy.Find(KindRollback, ActionForwarded); len(fwd) != 1 {
			t.Errorf("forwarded rollbacks = %d, want 1", len(fwd))
		}
		if n := r.rows(); n != 0 {
			t.Errorf("rows = %d, want 0", n)
		}
	})
	t.Run("unarmed: forwarded and answered", func(t *testing.T) {
		r := newProxyRig(t)
		if err := rollback(r); err != nil {
			t.Fatalf("plain ROLLBACK: %v", err)
		}
	})
}

// sleeping runs pg_sleep(seconds) through the proxy under a short deadline, so
// the driver cancels and closes the connection the way it does for a context
// that ended.
func (r *proxyRig) sleeping(seconds string, within time.Duration) (uint32, <-chan error) {
	// Open the pooled connection first, so the short deadline covers the
	// statement and not a dial on a busy machine.
	if err := r.via.Exec("SELECT 1").Error; err != nil {
		r.t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), within)
	r.t.Cleanup(cancel)
	ch := make(chan error, 1)
	go func() { ch <- r.via.WithContext(ctx).Exec("SELECT pg_sleep(" + seconds + ")").Error }()
	pid := r.pidOf("pg_sleep(" + seconds + ")")
	return pid, ch
}

func (r *proxyRig) awaitExpiry(ch <-chan error) {
	r.t.Helper()
	select {
	case err := <-ch:
		if !errors.Is(err, context.DeadlineExceeded) {
			r.t.Fatalf("err = %v, want the context deadline", err)
		}
	case <-time.After(5 * time.Second):
		r.t.Fatal("the call did not return at its deadline")
	}
}

func TestProxyCancelModes(t *testing.T) {
	t.Run("forwarded: the statement is cancelled", func(t *testing.T) {
		r := newProxyRig(t)
		r.proxy.Arm(Faults{Match: "pg_sleep"})
		pid, ch := r.sleeping("30", 300*time.Millisecond)
		r.awaitExpiry(ch)
		r.waitFor("the backend to end", 5*time.Second, func() bool { return !r.backendExists(pid) })
		if fwd := r.proxy.Find(KindCancel, ActionForwarded); len(fwd) != 1 {
			t.Errorf("forwarded cancels = %d, want 1", len(fwd))
		}
		if fwd := r.proxy.Find(KindTerminate, ActionForwarded); len(fwd) != 1 {
			t.Errorf("forwarded Terminates = %d, want 1", len(fwd))
		}
	})
	t.Run("e: dropped, the statement runs on", func(t *testing.T) {
		r := newProxyRig(t)
		r.proxy.Arm(Faults{Match: "pg_sleep", Cancel: CancelDrop})
		pid, ch := r.sleeping("1.5", 300*time.Millisecond)
		r.awaitExpiry(ch)
		// The proxy logs the dropped cancel before the statement can end (it
		// still has about a second of pg_sleep left), so the backend must be
		// alive when that event is seen.
		r.waitFor("the cancel to be dropped", 5*time.Second, func() bool { return len(r.proxy.Find(KindCancel, ActionDropped)) == 1 })
		if !r.backendExists(pid) {
			t.Fatal("the backend ended although the cancel was dropped")
		}
		// The forwarded Terminate is read when the statement ends.
		r.waitFor("the backend to end after its statement", 5*time.Second, func() bool { return !r.backendExists(pid) })
		if d := r.proxy.Find(KindCancel, ActionDropped); len(d) != 1 {
			t.Errorf("dropped cancels = %d, want 1", len(d))
		}
		if fwd := r.proxy.Find(KindCancel, ActionForwarded); len(fwd) != 0 {
			t.Errorf("a cancel was forwarded: %+v", fwd)
		}
	})
	t.Run("d: held until released", func(t *testing.T) {
		r := newProxyRig(t)
		r.proxy.Arm(Faults{Match: "pg_sleep", Cancel: CancelHold})
		pid, ch := r.sleeping("30", 300*time.Millisecond)
		r.awaitExpiry(ch)
		r.waitFor("the cancel to be held", 5*time.Second, func() bool { return len(r.proxy.Find(KindCancel, ActionHeld)) == 1 })
		if !r.backendExists(pid) {
			t.Fatal("the backend ended while the cancel was held")
		}
		if h := r.proxy.Find(KindCancel, ActionHeld); len(h) != 1 {
			t.Fatalf("held cancels = %d, want 1", len(h))
		}
		if fwd := r.proxy.Find(KindTerminate, ActionForwarded); len(fwd) != 0 {
			t.Errorf("Terminate forwarded before the cancel completed: %+v", fwd)
		}
		r.proxy.ReleaseCancel()
		r.waitFor("the backend to end after the release", 5*time.Second, func() bool { return !r.backendExists(pid) })
		fwdCancel, fwdTerm := r.proxy.Find(KindCancel, ActionForwarded), r.proxy.Find(KindTerminate, ActionForwarded)
		if len(fwdCancel) != 1 || len(fwdTerm) != 1 {
			t.Fatalf("forwarded cancel %d, Terminate %d, want 1 and 1", len(fwdCancel), len(fwdTerm))
		}
		if fwdTerm[0].Time.Before(fwdCancel[0].Time) {
			t.Error("Terminate reached the server before the cancel")
		}
	})
}

func TestProxyCloseModes(t *testing.T) {
	t.Run("f: Terminate and FIN withheld", func(t *testing.T) {
		r := newProxyRig(t)
		r.proxy.Arm(Faults{Match: "pg_sleep", Close: CloseWithholdAll})
		pid, ch := r.sleeping("30", 300*time.Millisecond)
		r.awaitExpiry(ch)
		// The cancel ends the statement; with nothing forwarded after it the
		// backend stays connected and idle.
		r.waitFor("the cancel to land", 5*time.Second, func() bool {
			var state string
			r.direct.Raw("SELECT coalesce(state, '') FROM pg_stat_activity WHERE pid = ?", pid).Scan(&state)
			return state == "idle"
		})
		// Both withheld closes are logged before anything could be forwarded, so
		// once the proxy has recorded them the checks below are not racing it.
		r.waitFor("the Terminate to be held", 5*time.Second, func() bool { return len(r.proxy.Find(KindTerminate, ActionHeld)) == 1 })
		r.waitFor("the FIN to be held", 5*time.Second, func() bool { return len(r.proxy.Find(KindFIN, ActionHeld)) == 1 })
		if !r.backendExists(pid) {
			t.Fatal("the backend ended although Terminate and FIN were withheld")
		}
		if fwd := r.proxy.Find(KindTerminate, ActionForwarded); len(fwd) != 0 {
			t.Errorf("Terminate forwarded: %+v", fwd)
		}
		if fwd := r.proxy.Find(KindFIN, ActionForwarded); len(fwd) != 0 {
			t.Errorf("FIN forwarded: %+v", fwd)
		}
		r.proxy.ReleaseClose()
		r.waitFor("the backend to end after the release", 5*time.Second, func() bool { return !r.backendExists(pid) })
		// The proxy writes Terminate and only then logs the FIN, and the
		// backend can end in between: wait for the event itself.
		r.waitFor("the FIN to be forwarded", 5*time.Second, func() bool { return len(r.proxy.Find(KindFIN, ActionForwarded)) == 1 })
		if fwd := r.proxy.Find(KindTerminate, ActionForwarded); len(fwd) != 1 {
			t.Errorf("forwarded Terminates after the release = %d, want 1", len(fwd))
		}
		if fwd := r.proxy.Find(KindFIN, ActionForwarded); len(fwd) != 1 {
			t.Errorf("forwarded FINs after the release = %d, want 1", len(fwd))
		}
	})
	t.Run("f2: Terminate forwarded, FIN held", func(t *testing.T) {
		r := newProxyRig(t)
		r.proxy.Arm(Faults{Match: "pg_sleep", Close: CloseWithholdFIN})
		pid, ch := r.sleeping("30", 300*time.Millisecond)
		r.awaitExpiry(ch)
		r.waitFor("the backend to end on Terminate", 5*time.Second, func() bool { return !r.backendExists(pid) })
		// Terminate ends the backend before fromClient necessarily reads EOF.
		// Observe that separate event before checking that FIN stayed held.
		r.waitFor("the FIN to be held", 5*time.Second, func() bool { return len(r.proxy.Find(KindFIN, ActionHeld)) == 1 })
		if fwd := r.proxy.Find(KindTerminate, ActionForwarded); len(fwd) != 1 {
			t.Errorf("forwarded Terminates = %d, want 1", len(fwd))
		}
		if h := r.proxy.Find(KindFIN, ActionHeld); len(h) != 1 {
			t.Errorf("held FINs = %d, want 1", len(h))
		}
		if fwd := r.proxy.Find(KindFIN, ActionForwarded); len(fwd) != 0 {
			t.Errorf("FIN forwarded before release: %+v", fwd)
		}
		r.proxy.ReleaseClose()
		r.waitFor("the FIN to be forwarded", 5*time.Second, func() bool { return len(r.proxy.Find(KindFIN, ActionForwarded)) == 1 })
	})
}

func TestProxyArmClaimsOneConnection(t *testing.T) {
	r := newProxyRig(t)
	r.proxy.Arm(Faults{Match: "never matches this", Commit: CommitCloseUnforwarded})
	if err := r.via.Transaction(func(tx *gorm.DB) error {
		return tx.Exec("INSERT INTO proxy_rows VALUES (1)").Error
	}); err != nil {
		t.Fatalf("an unclaimed fault changed a transaction: %v", err)
	}
	if n := r.rows(); n != 1 {
		t.Errorf("rows = %d, want 1", n)
	}
}

// replyBeforeCut makes the proxy wait, after it sent a transaction-ending
// message and before it cuts the client, until the server's ReadyForQuery has
// come back and been dropped. That is the schedule in which a forward-then-
// close fault used to hand the client the reply it promises to withhold. The
// returned flag says the reply did reach the proxy first.
func (r *proxyRig) replyBeforeCut() *atomic.Bool {
	saw := &atomic.Bool{}
	r.proxy.setAfterForward(func(c *proxyConn) {
		for stop := time.Now().Add(5 * time.Second); time.Now().Before(stop); time.Sleep(time.Millisecond) {
			if c.suppressedReady.Load() > 0 {
				saw.Store(true)
				return
			}
		}
	})
	return saw
}

func TestProxyForwardThenCloseNeverAnswers(t *testing.T) {
	t.Run("COMMIT", func(t *testing.T) {
		r := newProxyRig(t)
		saw := r.replyBeforeCut()
		r.proxy.Arm(Faults{Match: "insert into proxy_rows", Commit: CommitForwardThenClose})
		if err := <-r.commitInTx(); err == nil {
			t.Fatal("COMMIT was answered although the reply arrived before the client was cut")
		}
		if !saw.Load() {
			t.Error("the server's reply never reached the proxy before the cut")
		}
		r.waitFor("the row to commit on the server", 5*time.Second, func() bool { return r.rows() == 1 })
	})
	t.Run("ROLLBACK", func(t *testing.T) {
		r := newProxyRig(t)
		saw := r.replyBeforeCut()
		r.proxy.Arm(Faults{Match: "insert into proxy_rows", Rollback: RollbackForwardThenClose})
		tx := r.via.Begin()
		if err := tx.Exec("INSERT INTO proxy_rows VALUES (1)").Error; err != nil {
			t.Fatal(err)
		}
		if err := tx.Rollback().Error; err == nil {
			t.Fatal("ROLLBACK was answered although the reply arrived before the client was cut")
		}
		if !saw.Load() {
			t.Error("the server's reply never reached the proxy before the cut")
		}
		if n := r.rows(); n != 0 {
			t.Errorf("rows = %d, want 0", n)
		}
	})
}

// rawConn is a pgconn connection through the proxy, whose Prepare and
// ExecPrepared speak the extended protocol, so a COMMIT or ROLLBACK can be
// prepared on its own and executed later, or executed again with no Parse.
func (r *proxyRig) rawConn() *pgconn.PgConn {
	r.t.Helper()
	base, err := URL(r.name)
	if err != nil {
		r.t.Fatal(err)
	}
	u, err := url.Parse(base)
	if err != nil {
		r.t.Fatal(err)
	}
	u.Host = r.proxy.Addr()
	q := u.Query()
	q.Set("sslmode", "disable")
	u.RawQuery = q.Encode()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pc, err := pgconn.Connect(ctx, u.String())
	if err != nil {
		r.t.Fatal(err)
	}
	r.t.Cleanup(func() { pc.Close(context.Background()) })
	return pc
}

func rawCtx(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func rawSimple(t *testing.T, pc *pgconn.PgConn, sql string) {
	t.Helper()
	if _, err := pc.Exec(rawCtx(t), sql).ReadAll(); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

func rawPrepare(t *testing.T, pc *pgconn.PgConn, name, sql string) {
	t.Helper()
	if _, err := pc.Prepare(rawCtx(t), name, sql, nil); err != nil {
		t.Fatalf("prepare %s: %v", sql, err)
	}
}

func rawExecPrepared(ctx context.Context, pc *pgconn.PgConn, name string) error {
	_, err := pc.ExecPrepared(ctx, name, nil, nil, nil).Close()
	return err
}

func TestProxyExtendedTransactionEnds(t *testing.T) {
	// A COMMIT or ROLLBACK that is prepared on its own (Parse, Describe, Sync)
	// and executed later (Bind, Execute, Sync) meets its fault at the Execute.
	prepared := func(t *testing.T, fault Faults, end string) (*proxyRig, *pgconn.PgConn, *atomic.Bool) {
		r := newProxyRig(t)
		saw := r.replyBeforeCut()
		fault.Match = "insert into proxy_rows"
		r.proxy.Arm(fault)
		pc := r.rawConn()
		rawSimple(t, pc, "BEGIN")
		rawPrepare(t, pc, "ins", "INSERT INTO proxy_rows VALUES (1)")
		if err := rawExecPrepared(rawCtx(t), pc, "ins"); err != nil {
			t.Fatal(err)
		}
		// Preparing the transaction end is not running it: it is answered
		// whatever is armed, and nothing is held or cut.
		rawPrepare(t, pc, "end", end)
		return r, pc, saw
	}
	t.Run("COMMIT reply held", func(t *testing.T) {
		r, pc, _ := prepared(t, Faults{Commit: CommitHoldReply}, "COMMIT")
		done := make(chan error, 1)
		go func() { done <- rawExecPrepared(rawCtx(t), pc, "end") }()
		r.waitFor("the row to commit on the server", 5*time.Second, func() bool { return r.rows() == 1 })
		select {
		case err := <-done:
			t.Fatalf("COMMIT returned (%v) while its reply was held", err)
		case <-time.After(300 * time.Millisecond):
		}
		if held := r.proxy.Find(KindCommit, ActionHeld); len(held) != 1 {
			t.Errorf("held commits = %d, want 1", len(held))
		}
		r.proxy.ReleaseReply()
		if err := <-done; err != nil {
			t.Errorf("COMMIT after the release: %v", err)
		}
	})
	t.Run("COMMIT forwarded, client cut before the reply", func(t *testing.T) {
		r, pc, saw := prepared(t, Faults{Commit: CommitForwardThenClose}, "COMMIT")
		if err := rawExecPrepared(rawCtx(t), pc, "end"); err == nil {
			t.Fatal("COMMIT was answered although the client was cut before the reply")
		}
		if !saw.Load() {
			t.Error("the server's reply never reached the proxy before the cut")
		}
		r.waitFor("the row to commit on the server", 5*time.Second, func() bool { return r.rows() == 1 })
		if fwd := r.proxy.Find(KindCommit, ActionForwarded); len(fwd) != 1 {
			t.Errorf("forwarded commits = %d, want 1", len(fwd))
		}
	})
	t.Run("COMMIT closed without forwarding", func(t *testing.T) {
		r, pc, _ := prepared(t, Faults{Commit: CommitCloseUnforwarded}, "COMMIT")
		if err := rawExecPrepared(rawCtx(t), pc, "end"); err == nil {
			t.Fatal("COMMIT was answered although it was never forwarded")
		}
		if d := r.proxy.Find(KindCommit, ActionDropped); len(d) != 1 {
			t.Errorf("dropped commits = %d, want 1", len(d))
		}
		if fwd := r.proxy.Find(KindCommit, ActionForwarded); len(fwd) != 0 {
			t.Errorf("a COMMIT was forwarded: %+v", fwd)
		}
		r.waitFor("the transaction to roll back", 5*time.Second, func() bool { return r.rows() == 0 && !r.hasOpenWriter() })
	})
	t.Run("ROLLBACK closed without forwarding", func(t *testing.T) {
		r, pc, _ := prepared(t, Faults{Rollback: RollbackCloseUnforwarded}, "ROLLBACK")
		if err := rawExecPrepared(rawCtx(t), pc, "end"); err == nil {
			t.Fatal("ROLLBACK was answered although it was never forwarded")
		}
		if d := r.proxy.Find(KindRollback, ActionDropped); len(d) != 1 {
			t.Errorf("dropped rollbacks = %d, want 1", len(d))
		}
		if fwd := r.proxy.Find(KindRollback, ActionForwarded); len(fwd) != 0 {
			t.Errorf("a ROLLBACK was forwarded: %+v", fwd)
		}
		if n := r.rows(); n != 0 {
			t.Errorf("rows = %d, want 0", n)
		}
	})
	t.Run("ROLLBACK forwarded, client cut before the reply", func(t *testing.T) {
		r, pc, saw := prepared(t, Faults{Rollback: RollbackForwardThenClose}, "ROLLBACK")
		if err := rawExecPrepared(rawCtx(t), pc, "end"); err == nil {
			t.Fatal("ROLLBACK was answered although the client was cut before the reply")
		}
		if !saw.Load() {
			t.Error("the server's reply never reached the proxy before the cut")
		}
		if fwd := r.proxy.Find(KindRollback, ActionForwarded); len(fwd) != 1 {
			t.Errorf("forwarded rollbacks = %d, want 1", len(fwd))
		}
		if n := r.rows(); n != 0 {
			t.Errorf("rows = %d, want 0", n)
		}
	})
	t.Run("a prepared COMMIT is run again with no Parse", func(t *testing.T) {
		r := newProxyRig(t)
		pc := r.rawConn()
		rawPrepare(t, pc, "ins1", "INSERT INTO proxy_rows VALUES (1)")
		rawPrepare(t, pc, "ins2", "INSERT INTO proxy_rows VALUES (2)")
		rawPrepare(t, pc, "end", "COMMIT")
		// Unarmed: forwarded, answered and logged.
		rawSimple(t, pc, "BEGIN")
		if err := rawExecPrepared(rawCtx(t), pc, "ins1"); err != nil {
			t.Fatal(err)
		}
		if err := rawExecPrepared(rawCtx(t), pc, "end"); err != nil {
			t.Fatalf("plain prepared COMMIT: %v", err)
		}
		if n := r.rows(); n != 1 {
			t.Fatalf("rows = %d, want 1", n)
		}
		// Armed now, on this connection (the claiming statement is a simple
		// Query). The second COMMIT is not parsed again.
		r.replyBeforeCut()
		r.proxy.Arm(Faults{Match: "select 'claim'", Commit: CommitForwardThenClose})
		rawSimple(t, pc, "SELECT 'claim'")
		rawSimple(t, pc, "BEGIN")
		if err := rawExecPrepared(rawCtx(t), pc, "ins2"); err != nil {
			t.Fatal(err)
		}
		if err := rawExecPrepared(rawCtx(t), pc, "end"); err == nil {
			t.Fatal("the cached COMMIT was answered although the client was cut before the reply")
		}
		r.waitFor("the second row to commit on the server", 5*time.Second, func() bool { return r.rows() == 2 })
		if fwd := r.proxy.Find(KindCommit, ActionForwarded); len(fwd) != 2 {
			t.Errorf("forwarded commits = %d, want 2 (every execution is logged)", len(fwd))
		}
		var inserts int
		for _, e := range r.proxy.Find(KindStatement, ActionForwarded) {
			if contains(e.SQL, "INSERT INTO proxy_rows") {
				inserts++
			}
		}
		if inserts != 2 {
			t.Errorf("logged inserts = %d, want 2", inserts)
		}
	})
	for _, end := range []string{"COMMIT", "ROLLBACK"} {
		t.Run("claim cached "+end, func(t *testing.T) {
			r := newProxyRig(t)
			pc := r.rawConn()
			rawPrepare(t, pc, "end", end)
			rawSimple(t, pc, "BEGIN")
			rawSimple(t, pc, "INSERT INTO proxy_rows VALUES (1)")
			fault := Faults{Match: end}
			if end == "COMMIT" {
				fault.Commit = CommitCloseUnforwarded
			} else {
				fault.Rollback = RollbackCloseUnforwarded
			}
			r.proxy.Arm(fault)
			if err := rawExecPrepared(rawCtx(t), pc, "end"); err == nil {
				t.Fatalf("cached %s was answered despite armed fault", end)
			}
			kind := KindCommit
			if end == "ROLLBACK" {
				kind = KindRollback
			}
			if d := r.proxy.Find(kind, ActionDropped); len(d) != 1 {
				t.Errorf("dropped %s events = %d, want 1 (claimed at Execute with no Parse or Query)", end, len(d))
			}
			if fwd := r.proxy.Find(kind, ActionForwarded); len(fwd) != 0 {
				t.Errorf("a %s was forwarded: %+v", end, fwd)
			}
			r.waitFor("the transaction to roll back", 5*time.Second, func() bool { return r.rows() == 0 && !r.hasOpenWriter() })
		})
	}
}

// hasOpenWriter reports whether any backend of the test database is still
// inside a transaction.
func (r *proxyRig) hasOpenWriter() bool {
	r.t.Helper()
	var n int64
	if err := r.direct.Raw("SELECT count(*) FROM pg_stat_activity WHERE datname = current_database() AND pid <> pg_backend_pid() AND state LIKE 'idle in transaction%'").Scan(&n).Error; err != nil {
		r.t.Fatal(err)
	}
	return n > 0
}
