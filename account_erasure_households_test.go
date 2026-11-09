package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"fold/internal/guardkit"

	"fold/internal/audit"
	"fold/internal/notify"
	"fold/internal/testdb"
	"fold/internal/txguard"
	"fold/models"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/stdlib"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Household locks of an account erasure (M7.4 R4-3b-1, inventory 9.6
// requirement 5, signed in 15.5 as option (a)): the erasure takes every
// household it touches with NOWAIT and runs the whole transaction again, a
// bounded number of times, when one is busy. The route-level tests that need
// the proxy (exhaustion, the bounded attempt, a commit after a retry, a
// rollback that is not acknowledged) are in api_admin_test.go next to the
// R4-CTX delete tests.

// setHouseholdRetry sets the retry window and the two backoffs for one test and
// puts them back. A test that sets them stays serial.
func setHouseholdRetry(t *testing.T, window time.Duration, backoff [householdAttempts - 1]time.Duration) {
	t.Helper()
	oldWindow, oldBackoff := deleteRetryWindow, householdRetryBackoff
	deleteRetryWindow, householdRetryBackoff = window, backoff
	t.Cleanup(func() { deleteRetryWindow, householdRetryBackoff = oldWindow, oldBackoff })
}

func busyErr() error { return &pgconn.PgError{Code: "55P03", Message: "could not obtain lock on row"} }

// runHouseholdRetry is the loop apiAdminDeleteUser owns, with a fake attempt.
func runHouseholdRetry(ctx context.Context, attempt func(n int) error) (attempts int, err error) {
	retry := newHouseholdRetry(ctx)
	for {
		attempts++
		err = attempt(attempts)
		var again bool
		if again, err = retry.after(err); !again {
			return attempts, err
		}
	}
}

func TestHouseholdRetryOnlyRepeatsAConfirmedBusy(t *testing.T) {
	setHouseholdRetry(t, 2*time.Second, [2]time.Duration{time.Millisecond, time.Millisecond})
	other := errors.New("some other failure")
	cases := []struct {
		name     string
		attempt  func(n int) error
		want     int
		wantBusy bool
		check    func(t *testing.T, err error)
	}{
		{name: "success first time", attempt: func(int) error { return nil }, want: 1},
		{name: "a bare 55P03 repeats until it succeeds", attempt: func(n int) error {
			if n < 3 {
				return busyErr()
			}
			return nil
		}, want: 3},
		{name: "a bare 55P03 that never clears stops at three", attempt: func(int) error { return busyErr() }, want: householdAttempts, wantBusy: true},
		{name: "a 55P03 whose rollback was not acknowledged runs once", attempt: func(int) error {
			return &contactRollbackError{Cause: busyErr(), Rollback: errors.New("connection closed")}
		}, want: 1, wantBusy: true, check: func(t *testing.T, err error) {
			if !errors.Is(err, errContactRollback) || rollbackConfirmed(err) {
				t.Errorf("err = %v, want the unacknowledged rollback kept", err)
			}
		}},
		{name: "an unconfirmed commit never runs again", attempt: func(int) error {
			return &contactCommitError{Cause: busyErr()}
		}, want: 1, check: func(t *testing.T, err error) {
			if classifyContactErr(err) != outcomeUnknown || isHouseholdBusy(err) {
				t.Errorf("err = %v, want an unknown outcome that is not taken for a busy household", err)
			}
		}},
		{name: "any other error runs once", attempt: func(int) error { return other }, want: 1, check: func(t *testing.T, err error) {
			if !errors.Is(err, other) {
				t.Errorf("err = %v", err)
			}
		}},
		{name: "a deadlock is not retried", attempt: func(int) error { return &pgconn.PgError{Code: "40P01"} }, want: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			attempts, err := runHouseholdRetry(context.Background(), tc.attempt)
			if attempts != tc.want {
				t.Errorf("%d attempts, want %d", attempts, tc.want)
			}
			if got := isHouseholdBusy(err); got != tc.wantBusy {
				t.Errorf("busy = %v (err %v), want %v", got, err, tc.wantBusy)
			}
			if tc.check != nil {
				tc.check(t, err)
			}
		})
	}
}

func TestHouseholdRetryStopsWithTheWindowAndTheContext(t *testing.T) {
	t.Run("a new attempt starts only inside the window", func(t *testing.T) {
		setHouseholdRetry(t, 40*time.Millisecond, [2]time.Duration{30 * time.Millisecond, 30 * time.Millisecond})
		attempts, err := runHouseholdRetry(context.Background(), func(int) error { return busyErr() })
		if attempts != 2 || !isHouseholdBusy(err) {
			t.Errorf("%d attempts, err %v; want the window to end the loop after two attempts with the busy answer", attempts, err)
		}
	})
	t.Run("the context ends the wait and answers timed out", func(t *testing.T) {
		setHouseholdRetry(t, 2*time.Second, [2]time.Duration{time.Hour, time.Hour})
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
		defer cancel()
		start := time.Now()
		attempts, err := runHouseholdRetry(ctx, func(int) error { return busyErr() })
		if attempts != 1 || classifyContactErr(err) != outcomeTimedOut {
			t.Errorf("%d attempts, err %v, class %v; want one attempt and a timed-out answer", attempts, err, classifyContactErr(err))
		}
		if time.Since(start) > 5*time.Second {
			t.Error("the backoff did not end with the context")
		}
	})
	t.Run("a context that ended while the attempt ran starts nothing more", func(t *testing.T) {
		setHouseholdRetry(t, 2*time.Second, [2]time.Duration{time.Millisecond, time.Millisecond})
		ctx, cancel := context.WithCancel(context.Background())
		attempts, err := runHouseholdRetry(ctx, func(int) error {
			cancel()
			return busyErr()
		})
		if attempts != 1 || classifyContactErr(err) != outcomeTimedOut {
			t.Errorf("%d attempts, class %v, want one attempt and timed out", attempts, classifyContactErr(err))
		}
	})
}

// TestErasureHouseholdLockCensus pins the lock census of the erasure (written in
// the comment of account_erasure_households.go): the only functions that call
// lockHousehold are the household writers and conversion paths listed here,
// none of them reachable from the deletion callback's helpers, and the callback
// takes its households once, NOWAIT, between the identity sets and the first
// write. A new caller fails here until a person has classified it.
func TestErasureHouseholdLockCensus(t *testing.T) {
	want := map[string]bool{
		"age_rollover.go:rolloverAges":                             true, // flagHouseholdChild13 (N-05)
		"api_admin_guardian_links.go:decideGuardianLink":           true,
		"api_admin_household.go:apiAdminAddHouseholdPerson":        true,
		"api_household_conversion.go:issueHouseholdConversion":     true,
		"api_household_conversion.go:apiCancelHouseholdConversion": true,
		"api_household_conversion.go:approveHouseholdConversion":   true,
		"api_households.go:apiUpdateMyHousehold":                   true, // N-11 family
		"api_households.go:apiAddHouseholdPerson":                  true,
		"api_households.go:apiUpdateHouseholdPerson":               true,
		"api_households.go:apiDeleteHouseholdPerson":               true, // reconcileHouseholdPerson
		"api_registrations.go:apiRegister":                         true,
		"api_registrations.go:addHouseholdRegistration":            true,
		"api_teen_conversion.go:lockConversionParticipants":        true,
		"api_teen_conversion.go:lockTeenLifecycleUsers":            true,
	}
	got := map[string]bool{}
	var calls = map[string][]token.Pos{}
	parsed := guardkit.Production(t).Parse(parser.SkipObjectResolution)
	for _, problem := range parsed.Problems {
		t.Error(problem)
	}
	for _, pf := range parsed.Files {
		name, f := pf.Path, pf.AST
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				if id, ok := call.Fun.(*ast.Ident); ok {
					switch id.Name {
					case "lockHousehold":
						got[name+":"+fd.Name.Name] = true
					case "captureErasureSetsIn", "lockErasureHouseholdsIn", "eraseAccountCredentialsIn":
						if fd.Name.Name == "apiAdminDeleteUser" {
							calls[id.Name] = append(calls[id.Name], call.Pos())
						}
					}
				}
				return true
			})
		}
		// A households row lock written as SQL belongs to the erasure helper alone.
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			text := strings.ToUpper(lit.Value)
			if strings.Contains(text, "FROM HOUSEHOLDS") && strings.Contains(text, "FOR UPDATE") && name != "account_erasure_households.go" {
				t.Errorf("%s locks a households row in SQL; every household lock goes through lockHousehold or lockErasureHouseholdsIn", name)
			}
			return true
		})
	}
	for k := range got {
		if !want[k] {
			t.Errorf("%s calls lockHousehold and is not in the census; classify it (does it hold the row while it notifies or locks a user?) and add it", k)
		}
	}
	for k := range want {
		if !got[k] {
			t.Errorf("%s no longer calls lockHousehold; update the census", k)
		}
	}
	if len(calls["lockErasureHouseholdsIn"]) != 1 || len(calls["captureErasureSetsIn"]) != 1 || len(calls["eraseAccountCredentialsIn"]) != 1 {
		t.Fatalf("apiAdminDeleteUser calls: %v, want one each of captureErasureSetsIn, lockErasureHouseholdsIn, eraseAccountCredentialsIn", calls)
	}
	captured, locked, first := calls["captureErasureSetsIn"][0], calls["lockErasureHouseholdsIn"][0], calls["eraseAccountCredentialsIn"][0]
	if !(captured < locked && locked < first) {
		t.Error("lockErasureHouseholdsIn must run after captureErasureSetsIn and before the first write")
	}
}

// ---------------------------------------------------------------------------
// The scene: a household H held by a member (the writer) and by the person the
// erasure removes (the victim, who holds a seat in H). Every household writer
// that notifies the victim while it holds H, in both orders, with the real
// router, the real session cookie and the real DELETE.
// ---------------------------------------------------------------------------

type hlScene struct {
	coreErasureScene
	victim *models.User
	holder *models.User
	hc     *apiClient // the household holder, signed in
	hid    uint
	// proxyMark is how many proxy events existed once the admin was signed in
	// through the proxy (newHLRig): sign-in runs its own transactions, which
	// are not the deletion's.
	proxyMark int
	// states is the SQLSTATE log of the proxied handle (newHLRig only).
	states *erasureTestStateLog
}

func newHLScene(t *testing.T, victimRole string) *hlScene {
	t.Helper()
	return newHLSceneSeat(t, victimRole, true)
}

// newHLSceneSeat is newHLScene with or without a household_accounts seat for
// the victim in H. Without one, the erasure can reach H only through PERS.
func newHLSceneSeat(t *testing.T, victimRole string, seat bool) *hlScene {
	t.Helper()
	s := &hlScene{coreErasureScene: newCoreErasureScene(t)}
	s.ac.holdNotices = true
	s.victim = newTestUser(t, s.app, "hl-victim", erasurePW, victimRole)
	s.holder = newTestUser(t, s.app, "hl-holder", erasurePW, models.RoleMember)
	s.hc = loginAPI(t, s.app, "hl-holder", erasurePW)
	s.hc.holdNotices = true
	apiDo(t, s.hc, "PUT", "/api/v1/me/household", map[string]any{"name": "Holders", "address": "1 Oak St"}, http.StatusOK)
	s.hid = getMyHousehold(t, s.hc).ID
	if seat {
		erasureMustCreate(t, s.app.DB, &models.HouseholdAccount{HouseholdID: s.hid, UserID: s.victim.ID, Relation: models.HouseholdRelationAdult})
	}
	return s
}

func (s *hlScene) erase() <-chan httpResult {
	return bgDo(s.ac, "DELETE", "/api/v1/admin/users/"+itoa(s.victim.ID), nil)
}

func (s *hlScene) victimDeleted(t *testing.T) bool {
	t.Helper()
	return countRows(t, s.app.DB, "users", "id = ? AND deleted_at IS NOT NULL", s.victim.ID) == 1
}

// hlSnapshot is every table the erasure or a household writer could touch, as
// one string.
func hlSnapshot(t *testing.T, db *gorm.DB) string {
	t.Helper()
	var parts []string
	for _, table := range []string{"users", "audit_logs", "notifications", "jobs", "households", "household_accounts",
		"household_people", "guardian_links", "registrations", "account_requests", "memberships"} {
		parts = append(parts, table+": "+dumpRows(t, db, table, "id", "true"))
	}
	return strings.Join(parts, "\n")
}

// holdHousehold is a test transaction that holds the household row, as a
// writer that has not committed does.
func holdHousehold(t *testing.T, db *gorm.DB, hid uint) *gorm.DB {
	t.Helper()
	tx, _ := beginOwnedTx(t, db)
	if err := tx.Exec("SELECT id FROM households WHERE id = ? FOR UPDATE", hid).Error; err != nil {
		t.Fatal(err)
	}
	return tx
}

// gateQueue is a test transaction that asks for the contact gate while another
// transaction holds it. Postgres grants an advisory lock to its waiters in the
// order they asked, so once the queue waits (waitContactWaiters) it gets the
// gate the instant the holder's transaction ends and before any attempt that
// asks later: the holder's ROLLBACK has then really run on the server, and the
// next attempt cannot go further until release.
type gateQueue struct {
	got  chan error
	stop chan struct{}
	once sync.Once
}

func queueForGate(t *testing.T, app *App) *gateQueue {
	t.Helper()
	tx, _ := beginOwnedTx(t, app.DB)
	q := &gateQueue{got: make(chan error, 1), stop: make(chan struct{})}
	go func() {
		err := lockContacts(tx)
		q.got <- err
		if err != nil {
			return
		}
		<-q.stop
		tx.Rollback()
	}()
	t.Cleanup(q.release)
	return q
}

// acquired returns once the queue holds the gate.
func (q *gateQueue) acquired(t *testing.T) {
	t.Helper()
	select {
	case err := <-q.got:
		if err != nil {
			t.Fatalf("the gate queue: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the gate queue never got the gate")
	}
}

func (q *gateQueue) release() { q.once.Do(func() { close(q.stop) }) }

// householdLocked reports whether another transaction holds the household row
// FOR UPDATE. The probe takes the row for an instant when it is free.
func householdLocked(t *testing.T, db *gorm.DB, id uint) bool {
	t.Helper()
	locked := false
	if err := db.Transaction(func(tx *gorm.DB) error {
		var got []uint
		if err := tx.Raw("SELECT id FROM households WHERE id = ? FOR UPDATE SKIP LOCKED", id).Scan(&got).Error; err != nil {
			return err
		}
		locked = len(got) == 0
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return locked
}

// tracedProxyDB opens database name through the proxy with the SQLSTATE tracer
// of erasureTestSQLStates, so a test sees the code the server really answered
// (55P03) on the handle the route uses, not the proxy's own log of what it
// forwarded.
func tracedProxyDB(t *testing.T, p *testdb.Proxy, name string) (*gorm.DB, *erasureTestStateLog) {
	t.Helper()
	base, err := testdb.URL(name)
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	u.Host = p.Addr()
	q := u.Query()
	q.Set("sslmode", "disable")
	u.RawQuery = q.Encode()
	cfg, err := pgx.ParseConfig(u.String())
	if err != nil {
		t.Fatal(err)
	}
	log := &erasureTestStateLog{t: t}
	cfg.Tracer = erasureTestTracer{log: log}
	sqlDB := stdlib.OpenDB(*cfg)
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Use(txguard.Plugin{}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db, log
}

// hlHousehold seeds an empty household.
func hlHousehold(t *testing.T, s *hlScene, name string) uint {
	t.Helper()
	h := models.Household{Name: name, CreatedBy: s.holder.ID}
	erasureMustCreate(t, s.app.DB, &h)
	return h.ID
}

// hlPersOnly seeds a soft-deleted household person who is the victim (PERS: the
// converted_user_id link), in household hid.
func hlPersOnly(t *testing.T, s *hlScene, hid uint, first string) uint {
	t.Helper()
	p := models.HouseholdPerson{HouseholdID: hid, FirstName: first, Kind: models.HouseholdAdult, CreatedBy: s.holder.ID,
		ConvertedUserID: &s.victim.ID, DeletedAt: gorm.DeletedAt{Time: time.Now().UTC(), Valid: true}}
	erasureMustCreate(t, s.app.DB, &p)
	return p.ID
}

func awaitStates(t *testing.T, states *erasureTestStateLog, code string, n int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for states.Count(code) < n {
		if time.Now().After(deadline) {
			t.Fatalf("SQLSTATE %s recorded %d times, want at least %d", code, states.Count(code), n)
		}
		time.Sleep(time.Millisecond)
	}
}

func firstAuditID(t *testing.T, db *gorm.DB, action string) uint {
	t.Helper()
	var id uint
	if err := db.Raw("SELECT coalesce(min(id), 0) FROM audit_logs WHERE action = ?", action).Scan(&id).Error; err != nil {
		t.Fatal(err)
	}
	return id
}

// hlWriter is one household writer that notifies or locks the victim while it
// holds H.
type hlWriter struct {
	name       string
	victimRole string
	audit      string // the audit action the writer commits, for ordering
	status     int    // the writer's answer
	prepare    func(t *testing.T, s *hlScene)
	start      func(s *hlScene) <-chan httpResult
	// pause is the writer's first write after it holds H and before it notifies.
	pause func(s *hlScene) erasureTestPoint
	// atErasure, when set, runs on a writer-first schedule while the erasure's
	// second attempt is stopped before it deletes the victim's notices and the
	// writer has committed: what the writer left for the victim is still there.
	atErasure func(t *testing.T, s *hlScene)
	// check runs after both finished; writerWon is true when the writer's
	// transaction committed first (and the erasure retried).
	check func(t *testing.T, s *hlScene, writerWon bool)
}

func runHouseholdWriterOrders(t *testing.T, w hlWriter) {
	t.Helper()
	t.Run("writer first", func(t *testing.T) {
		s := newHLScene(t, w.victimRole)
		if w.prepare != nil {
			w.prepare(t, s)
		}
		states := erasureTestSQLStates(t, s.app)
		// A wide backoff, so the writer, released after the first 55P03, is done
		// before the second attempt starts.
		setHouseholdRetry(t, 2*time.Second, [2]time.Duration{400 * time.Millisecond, 400 * time.Millisecond})
		var erasureAt <-chan struct{}
		var releaseErasure func()
		if w.atErasure != nil {
			// The first attempt is refused on H before it gets this far, so the
			// stop is the second attempt's, after the writer let go of H.
			erasureAt, releaseErasure = erasureTestPause(t, s.app.DB, erasureTestBeforeNotificationsDelete(s.victim.ID))
		}
		reached, release := erasureTestPause(t, s.app.DB, w.pause(s))
		res := w.start(s)
		awaitDBSignal(t, reached)
		del := s.erase()
		awaitStates(t, states, "55P03", 1)
		release()
		if w.atErasure != nil {
			awaitDBSignal(t, erasureAt)
			w.atErasure(t, s)
			releaseErasure()
		}
		if got := awaitResult(t, res); got.code != w.status {
			t.Fatalf("%s: %d %s, want %d", w.name, got.code, got.body, w.status)
		}
		if got := awaitResult(t, del); got.code != http.StatusOK {
			t.Fatalf("erasure: %d %s, want 200 after a retry", got.code, got.body)
		}
		if n := states.Count("55P03"); n < 1 || n >= householdAttempts {
			t.Errorf("%d busy answers, want one or two and then a commit", n)
		}
		if n := states.Count("40P01"); n != 0 {
			t.Errorf("%d deadlocks recorded", n)
		}
		if !s.victimDeleted(t) {
			t.Fatal("the erasure did not commit")
		}
		if w.audit != "" {
			writer, erased := firstAuditID(t, s.app.DB, w.audit), firstAuditID(t, s.app.DB, audit.UserDeleted)
			if writer == 0 || erased == 0 || writer > erased {
				t.Errorf("%s audit id %d, user.deleted id %d: the later attempt must commit after the writer", w.audit, writer, erased)
			}
		}
		if n := rowsFor(t, s.app.DB, "notifications", s.victim.ID); n != 0 {
			t.Errorf("%d notices left for the erased person", n)
		}
		w.check(t, s, true)
	})
	t.Run("erasure first", func(t *testing.T) {
		s := newHLScene(t, w.victimRole)
		if w.prepare != nil {
			w.prepare(t, s)
		}
		states := erasureTestSQLStates(t, s.app)
		var erasurePID atomic.Int64
		p := erasureTestBeforeNotificationsDelete(s.victim.ID)
		p.PID = &erasurePID
		reached, release := erasureTestPause(t, s.app.DB, p)
		del := s.erase()
		awaitDBSignal(t, reached)
		res := w.start(s)
		waiter := waiterOn(t, s.app.DB, int(erasurePID.Load()))
		if q := statementOf(t, s.app.DB, waiter); !strings.Contains(q, `"households"`) {
			t.Fatalf("the writer waits in %q, want its household lock", q)
		}
		release()
		if got := awaitResult(t, del); got.code != http.StatusOK {
			t.Fatalf("erasure: %d %s, want 200", got.code, got.body)
		}
		if got := awaitResult(t, res); got.code != w.status {
			t.Fatalf("%s: %d %s, want %d", w.name, got.code, got.body, w.status)
		}
		if n := states.Count("55P03"); n != 0 {
			t.Errorf("%d busy answers, want none: the erasure held the household first", n)
		}
		if n := states.Count("40P01"); n != 0 {
			t.Errorf("%d deadlocks recorded", n)
		}
		if !s.victimDeleted(t) {
			t.Fatal("the erasure did not commit")
		}
		if n := rowsFor(t, s.app.DB, "notifications", s.victim.ID); n != 0 {
			t.Errorf("%d notices left for the erased person", n)
		}
		w.check(t, s, false)
	})
}

var hlFuture = time.Now().UTC().AddDate(0, 0, 30).Truncate(24 * time.Hour).Add(18 * time.Hour)

// hlEvent seeds an event with one date, capacity set when seats > 0.
func hlEvent(t *testing.T, s *hlScene, seats int) models.EventOccurrence {
	t.Helper()
	g := newTestGroup(t, s.app, s.admin, "Household Lock Club", models.GroupActive)
	ev, occ := seedEvent(t, s.app, g, s.admin.ID, hlFuture)
	if seats > 0 {
		if err := s.app.DB.Model(&models.Event{}).Where("id = ?", ev.ID).Update("capacity", seats).Error; err != nil {
			t.Fatal(err)
		}
	}
	return occ
}

func hlPersonPath(pid uint) string { return "/api/v1/me/household/people/" + itoa(pid) }

// (a) The erased admin is an N-11 recipient of a person add in H (the
// guardian-link task goes to every admin).
func TestHouseholdNoWaitAdminRecipientOfPersonAdd(t *testing.T) {
	runHouseholdWriterOrders(t, hlWriter{
		name: "POST person", victimRole: models.RoleAdmin, status: http.StatusCreated, audit: audit.HouseholdPersonAdded,
		start: func(s *hlScene) <-chan httpResult {
			return bgDo(s.hc, "POST", "/api/v1/me/household/people", map[string]any{"firstName": "Kit", "kind": "child", "birthDate": "2020-05-01"})
		},
		pause: func(*hlScene) erasureTestPoint {
			return erasureTestPoint{Processor: "create", Step: "gorm:create", Table: "household_people"}
		},
		check: func(t *testing.T, s *hlScene, _ bool) {
			if n := countRows(t, s.app.DB, "household_people", "household_id = ? AND first_name = 'Kit'", s.hid); n != 1 {
				t.Errorf("%d people named Kit in H, want 1", n)
			}
			for _, admin := range []*models.User{s.admin, s.second} {
				if n := countOf(t, s.app, admin.ID, notify.HouseholdLinkPending); n != 1 {
					t.Errorf("%s has %d link tasks, want 1", admin.Name, n)
				}
			}
		},
	})
}

// (b) The erased user is another holder of H, and the holder's removal of a
// person notifies the holder who signed that person up (notifyRegistrationRemoved).
func TestHouseholdNoWaitHolderNotifiedByPersonRemoval(t *testing.T) {
	var pid uint
	var occ models.EventOccurrence
	runHouseholdWriterOrders(t, hlWriter{
		name: "DELETE person", victimRole: models.RoleMember, status: http.StatusOK, audit: audit.HouseholdPersonRemoved,
		prepare: func(t *testing.T, s *hlScene) {
			pid = addMyPerson(t, s.hc, map[string]any{"firstName": "Ada", "kind": "adult"}).ID
			occ = hlEvent(t, s, 0)
			erasureMustCreate(t, s.app.DB, &models.Registration{OccurrenceID: occ.ID, HouseholdPersonID: &pid, RegisteredBy: &s.victim.ID,
				Status: models.RegGoing, Source: models.RegSourceWeb, AttendanceMode: models.AttendanceInPerson})
		},
		start: func(s *hlScene) <-chan httpResult { return bgDo(s.hc, "DELETE", hlPersonPath(pid), nil) },
		pause: func(*hlScene) erasureTestPoint {
			return erasureTestPoint{Processor: "update", Step: "gorm:update", Table: "registrations"}
		},
		check: func(t *testing.T, s *hlScene, _ bool) {
			if n := countRows(t, s.app.DB, "registrations", "household_person_id = ? AND status = ?", pid, models.RegCancelled); n != 1 {
				t.Errorf("%d cancelled registrations for the removed person, want 1", n)
			}
			if n := countRows(t, s.app.DB, "household_people", "id = ? AND deleted_at IS NOT NULL", pid); n != 1 {
				t.Errorf("the person was not removed (%d)", n)
			}
		},
	})
}

// (c) A guardian is told by the age rollover (flagHouseholdChild13, N-05) while
// the rollover holds the child's household.
func TestHouseholdNoWaitGuardianNotifiedByAgeRollover(t *testing.T) {
	runHouseholdWriterOrders(t, hlWriter{
		name: "age rollover", victimRole: models.RoleMember, status: http.StatusOK, audit: audit.HouseholdChildTurned13,
		prepare: func(t *testing.T, s *hlScene) {
			birth := time.Now().UTC().AddDate(-14, 0, 0)
			child := models.HouseholdPerson{HouseholdID: s.hid, FirstName: "Kit", Kind: models.HouseholdChild, BirthDate: &birth, CreatedBy: s.holder.ID}
			erasureMustCreate(t, s.app.DB, &child)
			erasureMustCreate(t, s.app.DB, &models.GuardianLink{GuardianUserID: s.victim.ID, PersonID: &child.ID, Status: models.GuardianConfirmed})
		},
		start: func(s *hlScene) <-chan httpResult {
			ch := make(chan httpResult, 1)
			go func() {
				n, err := s.app.rolloverAges(time.Now().UTC())
				code := http.StatusOK
				if err != nil {
					code = http.StatusInternalServerError
				}
				ch <- httpResult{code: code, body: fmt.Sprintf("%d %v", n, err)}
			}()
			return ch
		},
		pause: func(*hlScene) erasureTestPoint {
			return erasureTestPoint{Processor: "create", Step: "gorm:create", Table: "audit_logs"}
		},
		check: func(t *testing.T, s *hlScene, writerWon bool) {
			want := int64(0)
			if writerWon {
				want = 1
			}
			if n := auditCount(t, s.app, audit.HouseholdChildTurned13); n != want {
				t.Errorf("%d household.child_turned_13 rows, want %d", n, want)
			}
		},
	})
}

// (d) The erased person is waitlisted and the holder's removal of a person
// frees the only seat: promoteLocked promotes the victim and notifies them
// (notifyRegistrationPromoted: notify.Send takes the victim's user row FOR KEY
// SHARE) while it holds H. The writer is stopped at the promotion's own
// registrations update, so the schedule is only valid if the promotion really
// happens: a writer that never promotes the victim leaves the barrier unreached
// and the test fails there.
//
// Writer first: the erasure holds the victim's row and is refused on H (55P03),
// the writer then promotes and commits, and the second attempt finds the
// promotion, the audit row and the notice for the victim before it erases them.
// Erasure first: the victim is deleted when the writer reaches the waitlist, so
// nobody is promoted and no promotion notice is written.
func TestHouseholdNoWaitPromotionRecipient(t *testing.T) {
	var pid, victimReg uint
	var occ models.EventOccurrence
	promotionAudits := func(t *testing.T, s *hlScene) int64 {
		return countRows(t, s.app.DB, "audit_logs", "action = ? AND meta_json->>'registrationId' = ? AND meta_json->>'newStatus' = ?",
			audit.RegistrationChanged, itoa(victimReg), models.RegGoing)
	}
	runHouseholdWriterOrders(t, hlWriter{
		name: "DELETE person (promotes)", victimRole: models.RoleMember, status: http.StatusOK, audit: audit.HouseholdPersonRemoved,
		prepare: func(t *testing.T, s *hlScene) {
			pid = addMyPerson(t, s.hc, map[string]any{"firstName": "Ada", "kind": "adult"}).ID
			occ = hlEvent(t, s, 1)
			erasureMustCreate(t, s.app.DB, &models.Registration{OccurrenceID: occ.ID, HouseholdPersonID: &pid, RegisteredBy: &s.holder.ID,
				Status: models.RegGoing, Source: models.RegSourceWeb, AttendanceMode: models.AttendanceInPerson})
			waitlisted := time.Now().UTC().Add(-time.Hour)
			reg := models.Registration{OccurrenceID: occ.ID, UserID: &s.victim.ID, WaitlistedAt: &waitlisted,
				Status: models.RegWaitlisted, Source: models.RegSourceWeb, AttendanceMode: models.AttendanceInPerson}
			erasureMustCreate(t, s.app.DB, &reg)
			victimReg = reg.ID
		},
		start: func(s *hlScene) <-chan httpResult { return bgDo(s.hc, "DELETE", hlPersonPath(pid), nil) },
		pause: func(*hlScene) erasureTestPoint {
			return erasureTestPoint{Processor: "update", Step: "gorm:update", Table: "registrations",
				Assign: map[string]any{"status": models.RegGoing}}
		},
		atErasure: func(t *testing.T, s *hlScene) {
			if n := countRows(t, s.app.DB, "registrations", "id = ? AND status = ? AND promoted_at IS NOT NULL", victimReg, models.RegGoing); n != 1 {
				t.Errorf("the victim's registration is not promoted before the second attempt erases it (%d rows going)", n)
			}
			if n := promotionAudits(t, s); n != 1 {
				t.Errorf("%d promotion audit rows for the victim's registration, want 1", n)
			}
			if n := countOf(t, s.app, s.victim.ID, notify.RegistrationPromoted); n != 1 {
				t.Errorf("the victim holds %d registration.promoted notices, want 1: the recipient path ran under H", n)
			}
		},
		check: func(t *testing.T, s *hlScene, writerWon bool) {
			if n := countRows(t, s.app.DB, "registrations", "household_person_id = ? AND status = ?", pid, models.RegCancelled); n != 1 {
				t.Errorf("%d cancelled registrations for the removed person, want 1", n)
			}
			want := int64(0)
			if writerWon {
				want = 1
			}
			if n := promotionAudits(t, s); n != want {
				t.Errorf("%d promotion audit rows for the victim's registration, want %d", n, want)
			}
			if !writerWon {
				if n := countRows(t, s.app.DB, "registrations", "id = ? AND (status = ? OR promoted_at IS NOT NULL)", victimReg, models.RegGoing); n != 0 {
					t.Errorf("the erased person was promoted (%d rows)", n)
				}
			}
		},
	})
}

// ---------------------------------------------------------------------------
// Households selected through PERS (R4-3b-1; lockErasureHouseholdsIn).
// ---------------------------------------------------------------------------

// The households the erasure locks are the union of the account's seat and the
// households of PERS, read unscoped (a soft-deleted person counts), each once,
// ascending. This runs lockErasureHouseholdsIn directly (not through DELETE), so
// it can hold more PERS people than validatePersIn allows a real erasure.
func TestErasureHouseholdLockSelection(t *testing.T) {
	s := newHLSceneSeat(t, models.RoleMember, false)
	low := s.hid
	mid := hlHousehold(t, s, "Middle")
	high := hlHousehold(t, s, "High")
	unrelated := hlHousehold(t, s, "Unrelated")
	erasureMustCreate(t, s.app.DB, &models.HouseholdAccount{HouseholdID: mid, UserID: s.victim.ID, Relation: models.HouseholdRelationAdult})
	// Two people in the lowest household, one in the seat's household, one in
	// the highest: four PERS people, three households, the seat's among them.
	hlPersOnly(t, s, low, "A1")
	hlPersOnly(t, s, low, "A2")
	hlPersOnly(t, s, mid, "B")
	hlPersOnly(t, s, high, "C")
	// A person who is someone else (another converted user) selects nothing.
	other := newTestUser(t, s.app, "hl-other", erasurePW, models.RoleMember)
	erasureMustCreate(t, s.app.DB, &models.HouseholdPerson{HouseholdID: unrelated, FirstName: "Z", Kind: models.HouseholdAdult,
		CreatedBy: s.holder.ID, ConvertedUserID: &other.ID})
	var ids []uint
	err := s.app.DB.Transaction(func(tx *gorm.DB) error {
		subject := captureErasureSubject(s.victim)
		if err := captureErasureSetsIn(tx, &subject); err != nil {
			return err
		}
		if len(subject.People) != 4 {
			t.Errorf("PERS = %v, want the four soft-deleted people", subject.People)
		}
		var err error
		ids, err = lockErasureHouseholdsIn(tx, subject)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 3 || ids[0] != low || ids[1] != mid || ids[2] != high {
		t.Errorf("locked households %v, want each of %v once, ascending, and not %d", ids, []uint{low, mid, high}, unrelated)
	}
}

// hlBusyOnce erases the victim while the test holds household held. earlier are
// the households the erasure must take first, ascending. The erasure is stopped
// before each NOWAIT it sends, and at every stop the households before it are
// held by the erasure and the ones after it are not (ascending acquisition).
// The last request, for held, is refused with a real 55P03; the test has
// queued for the contact gate, so it gets the gate when the erasure's ROLLBACK
// has run on the server, and the second attempt cannot begin its work until
// the test lets it. In that window nothing the first attempt did is left:
// every earlier household is free again and the database is unchanged. Then
// the second attempt commits.
func hlBusyOnce(t *testing.T, s *hlScene, held uint, earlier []uint) {
	t.Helper()
	// The stops below take real time; the window must not end the loop.
	setHouseholdRetry(t, time.Minute, [2]time.Duration{25 * time.Millisecond, 100 * time.Millisecond})
	states := erasureTestSQLStates(t, s.app)
	order := append(append([]uint(nil), earlier...), held)
	hold := holdHousehold(t, s.app.DB, held)
	before := hlSnapshot(t, s.app.DB)
	reached := make([]<-chan struct{}, len(order))
	release := make([]func(), len(order))
	for i, id := range order {
		reached[i], release[i] = erasureTestPause(t, s.app.DB, erasureTestPoint{Processor: "row", Step: "gorm:row",
			SQLContains: "FOR UPDATE NOWAIT", VarAt: map[int]any{0: id}})
	}
	del := s.erase()
	last := len(order) - 1
	for i := range order {
		awaitDBSignal(t, reached[i])
		for j, id := range earlier {
			if got, want := householdLocked(t, s.app.DB, id), j < i; got != want {
				t.Fatalf("at the request for household %d, household %d is locked = %v, want %v: households are taken in ascending order", order[i], id, got, want)
			}
		}
		if i == last {
			break
		}
		release[i]()
	}
	q := queueForGate(t, s.app)
	waitContactWaiters(t, s.app, 1)
	release[last]()
	awaitStates(t, states, "55P03", 1)
	q.acquired(t)
	for _, id := range earlier {
		if householdLocked(t, s.app.DB, id) {
			t.Errorf("household %d is still locked after the refused attempt rolled back", id)
		}
	}
	if s.victimDeleted(t) {
		t.Error("the refused attempt deleted the victim")
	}
	if after := hlSnapshot(t, s.app.DB); after != before {
		t.Error("the refused attempt changed the database: its whole transaction must roll back")
	}
	if err := hold.Rollback().Error; err != nil {
		t.Fatal(err)
	}
	q.release()
	if got := awaitResult(t, del); got.code != http.StatusOK {
		t.Fatalf("erasure: %d %s, want 200 on the second attempt", got.code, got.body)
	}
	if !s.victimDeleted(t) {
		t.Fatal("the second attempt did not commit")
	}
	if n := states.Count("55P03"); n != 1 {
		t.Errorf("%d busy answers, want exactly the first attempt's", n)
	}
	if n := states.Count("40P01"); n != 0 {
		t.Errorf("%d deadlocks recorded", n)
	}
}

// A household reached only through PERS: the victim has no seat anywhere, and
// the only link is a soft-deleted person converted to them.
func TestHouseholdNoWaitPersOnlySoftDeletedPerson(t *testing.T) {
	s := newHLSceneSeat(t, models.RoleMember, false)
	hlPersOnly(t, s, s.hid, "Old")
	if n := countRows(t, s.app.DB, "household_accounts", "user_id = ?", s.victim.ID); n != 0 {
		t.Fatalf("the victim has %d seats, want none", n)
	}
	hlBusyOnce(t, s, s.hid, nil)
}

// Two households selected together through the real route: the lowest by the
// victim's seat and the highest by the one PERS person (validatePersIn allows
// one individual, so a real DELETE has at most one PERS household beside the
// seats). The held one is the highest, so the erasure takes the lower one first
// and must give it back. Larger household sets are covered at the helper level
// by TestErasureHouseholdLockSelection.
func TestHouseholdNoWaitSeveralHouseholdsAscending(t *testing.T) {
	s := newHLSceneSeat(t, models.RoleMember, false)
	low := s.hid
	high := hlHousehold(t, s, "High")
	erasureMustCreate(t, s.app.DB, &models.HouseholdAccount{HouseholdID: low, UserID: s.victim.ID, Relation: models.HouseholdRelationAdult})
	hlPersOnly(t, s, high, "C")
	hlBusyOnce(t, s, high, []uint{low})
}

// A fixture the real route used to accept is not one individual: four people
// converted to the victim. The erasure stops at validatePersIn, before it
// sends any NOWAIT, and changes nothing.
func TestHouseholdSeveralPersPeopleStopBeforeAnyLock(t *testing.T) {
	s := newHLSceneSeat(t, models.RoleMember, false)
	mid := hlHousehold(t, s, "Middle")
	high := hlHousehold(t, s, "High")
	erasureMustCreate(t, s.app.DB, &models.HouseholdAccount{HouseholdID: mid, UserID: s.victim.ID, Relation: models.HouseholdRelationAdult})
	hlPersOnly(t, s, s.hid, "A1")
	hlPersOnly(t, s, s.hid, "A2")
	hlPersOnly(t, s, mid, "B")
	hlPersOnly(t, s, high, "C")
	states := erasureTestSQLStates(t, s.app)
	before := hlSnapshot(t, s.app.DB)
	if r := deleteAccount(s.ac, s.victim.ID, nil); r.code != http.StatusInternalServerError {
		t.Fatalf("delete: %d %s, want the existing 500", r.code, r.body)
	}
	if after := hlSnapshot(t, s.app.DB); after != before {
		t.Error("the aborted erasure changed users, audit, notices, jobs or household rows")
	}
	if s.victimDeleted(t) {
		t.Error("the victim was deleted")
	}
	if n := states.Count("55P03"); n != 0 {
		t.Errorf("%d busy answers, want none: no household lock was sent", n)
	}
}

// A D4-class cycle goes through an occurrence lock, not a household: a writer
// holds the occurrence and asks for the erased person's row (the recipient
// lock) while the erasure holds that row and waits for the occurrence
// (promoteOnDisable). NOWAIT on households does not close it. It is recorded,
// not fixed: Postgres aborts one side with exactly one 40P01 and that side
// rolls back whole; nothing retries it.
func TestHouseholdDeadlockThroughAnOccurrenceIsRecordedNotRetried(t *testing.T) {
	s := newHLScene(t, models.RoleMember)
	occ := hlEvent(t, s, 0)
	erasureMustCreate(t, s.app.DB, &models.Registration{OccurrenceID: occ.ID, UserID: &s.victim.ID,
		Status: models.RegGoing, Source: models.RegSourceWeb, AttendanceMode: models.AttendanceInPerson})
	states := erasureTestSQLStates(t, s.app)
	db := s.app.DB

	// The writer: holds the occurrence and a sentinel row of its own.
	writer, writerPID := beginOwnedTx(t, db)
	if err := writer.Exec("SELECT id FROM event_occurrences WHERE id = ? FOR UPDATE", occ.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := writer.Create(&models.Setting{Key: "hl-sentinel", Value: "x"}).Error; err != nil {
		t.Fatal(err)
	}
	del := s.erase()
	erasurePID := waitForLockedUsersQuery(t, s.app, "%event_occurrences%FOR UPDATE%")
	waitDBBlocked(t, db, erasurePID, writerPID)
	var queryStart time.Time
	if err := db.Raw("SELECT query_start FROM pg_stat_activity WHERE pid = ?", erasurePID).Scan(&queryStart).Error; err != nil {
		t.Fatal(err)
	}
	// The erasure's one deadlock check finds no cycle: the writer is not waiting yet.
	erasureTestWaitPastDeadlockCheck(t, s.app, queryStart)
	writerErr := writer.Exec("SELECT id FROM users WHERE id = ? FOR KEY SHARE", s.victim.ID).Error
	if pgCode(writerErr) != "40P01" {
		t.Fatalf("the later waiter's error = %v, want SQLSTATE 40P01", writerErr)
	}
	if err := writer.Rollback().Error; err != nil {
		t.Fatal(err)
	}
	if got := awaitResult(t, del); got.code != http.StatusOK {
		t.Fatalf("erasure: %d %s; the erasure was meant to win", got.code, got.body)
	}
	if n := states.Count("40P01"); n != 1 {
		t.Errorf("%d deadlocks recorded, want exactly the one", n)
	}
	if n := states.Count("55P03"); n != 0 {
		t.Errorf("%d busy answers, want none: no household was held", n)
	}
	if n := countRows(t, db, "settings", "key = ?", "hl-sentinel"); n != 0 {
		t.Errorf("the loser left %d rows: its whole transaction must roll back", n)
	}
	if !s.victimDeleted(t) {
		t.Error("the erasure did not commit")
	}
}

// ---------------------------------------------------------------------------
// The household step (M7.4 R4-3b-2): PERS integrity, the seat, PERS
// anonymization, a staged person's registrations, guardian links, teen-consent
// tokens, sole-subject households and the erasure report. These tests seed rows
// directly and use the real DELETE; the barrier and lock-order tests above and
// below it use the proxied handle.
// ---------------------------------------------------------------------------

// hrReport is the "erasure" object of a 200 answer.
func hrReport(t *testing.T, r httpResult) erasureReportJSON {
	t.Helper()
	if r.code != http.StatusOK {
		t.Fatalf("delete: %d %s, want 200", r.code, r.body)
	}
	var out struct {
		Erasure *erasureReportJSON `json:"erasure"`
	}
	if err := json.Unmarshal([]byte(r.body), &out); err != nil || out.Erasure == nil {
		t.Fatalf("delete answered %q, want {\"erasure\": {...}}", r.body)
	}
	return *out.Erasure
}

func hrErase(t *testing.T, s coreErasureScene, id uint) erasureReportJSON {
	t.Helper()
	s.ac.holdNotices = true
	return hrReport(t, deleteAccount(s.ac, id, nil))
}

func hrHouseholdOf(t *testing.T, s coreErasureScene, name, address string) uint {
	t.Helper()
	h := models.Household{Name: name, Address: address, CreatedBy: s.admin.ID}
	erasureMustCreate(t, s.app.DB, &h)
	return h.ID
}

func hrSeat(t *testing.T, s coreErasureScene, hid, uid uint, relation string) {
	t.Helper()
	erasureMustCreate(t, s.app.DB, &models.HouseholdAccount{HouseholdID: hid, UserID: uid, Relation: relation})
}

// hrPerson seeds a household person; converted links it to a user as a
// completed conversion would.
func hrPerson(t *testing.T, s coreErasureScene, hid uint, first, kind string, converted *uint, deleted bool) uint {
	t.Helper()
	born := time.Date(2012, 5, 6, 0, 0, 0, 0, time.UTC)
	p := models.HouseholdPerson{HouseholdID: hid, FirstName: first, LastName: "Family", Kind: kind, BirthDate: &born,
		CareNotes: "allergic to " + first, PhotoConsent: true, CreatedBy: s.admin.ID, ConvertedUserID: converted}
	if deleted {
		p.DeletedAt = gorm.DeletedAt{Time: time.Now().UTC(), Valid: true}
	}
	erasureMustCreate(t, s.app.DB, &p)
	return p.ID
}

func hrHouseholdRow(t *testing.T, db *gorm.DB, id uint) models.Household {
	t.Helper()
	var h models.Household
	if err := db.First(&h, id).Error; err != nil {
		t.Fatal(err)
	}
	return h
}

func hrPersonRow(t *testing.T, db *gorm.DB, id uint) models.HouseholdPerson {
	t.Helper()
	var p models.HouseholdPerson
	if err := db.Unscoped().First(&p, id).Error; err != nil {
		t.Fatal(err)
	}
	return p
}

// hrAssertAnonymized: the person is this individual's scrubbed row, with the
// converted_user_id link exactly as it was.
func hrAssertAnonymized(t *testing.T, db *gorm.DB, id uint, converted *uint) {
	t.Helper()
	p := hrPersonRow(t, db, id)
	if p.FirstName != "" || p.LastName != "" || p.BirthDate != nil || p.CareNotes != "" || p.PhotoConsent || !p.DeletedAt.Valid {
		t.Errorf("person %d was not anonymized and soft-deleted: %+v", id, p)
	}
	if (p.ConvertedUserID == nil) != (converted == nil) || (converted != nil && *p.ConvertedUserID != *converted) {
		t.Errorf("person %d: converted_user_id = %v, want %v unchanged", id, p.ConvertedUserID, converted)
	}
}

// T16 fixtures (a) to (g): the sole-subject predicate through the real DELETE.
func TestErasureHouseholdSoleSubjectFixtures(t *testing.T) {
	type fixture struct {
		name   string
		seed   func(t *testing.T, s coreErasureScene, u *models.User, hid uint) (check func(t *testing.T))
		scrub  bool
		people int // PERS rows the erasure anonymizes
	}
	fixtures := []fixture{
		{name: "a one-holder household with no people", scrub: true, seed: func(t *testing.T, s coreErasureScene, u *models.User, hid uint) func(*testing.T) { return nil }},
		{name: "b holder plus the same individual's duplicate only", scrub: true, people: 1,
			seed: func(t *testing.T, s coreErasureScene, u *models.User, hid uint) func(*testing.T) {
				id := hrPerson(t, s, hid, "Dup", models.HouseholdAdult, &u.ID, true)
				return func(t *testing.T) { hrAssertAnonymized(t, s.app.DB, id, &u.ID) }
			}},
		{name: "c holder plus another adult's seat",
			seed: func(t *testing.T, s coreErasureScene, u *models.User, hid uint) func(*testing.T) {
				v := newTestUser(t, s.app, "hrf-other-adult", erasurePW, models.RoleMember)
				hrSeat(t, s, hid, v.ID, models.HouseholdRelationAdult)
				return nil
			}},
		{name: "d holder plus a surviving child",
			seed: func(t *testing.T, s coreErasureScene, u *models.User, hid uint) func(*testing.T) {
				id := hrPerson(t, s, hid, "Kid", models.HouseholdChild, nil, false)
				before := dumpRows(t, s.app.DB, "household_people", "id", "id = ?", id)
				return func(t *testing.T) {
					if got := dumpRows(t, s.app.DB, "household_people", "id", "id = ?", id); got != before {
						t.Errorf("the surviving child changed:\n%s\n%s", before, got)
					}
				}
			}},
		{name: "e holder plus a soft-deleted other person",
			seed: func(t *testing.T, s coreErasureScene, u *models.User, hid uint) func(*testing.T) {
				hrPerson(t, s, hid, "Gone", models.HouseholdAdult, nil, true)
				return nil
			}},
		{name: "f holder plus a legacy tombstone seat",
			seed: func(t *testing.T, s coreErasureScene, u *models.User, hid uint) func(*testing.T) {
				old := newTestUser(t, s.app, "hrf-tombstone", erasurePW, models.RoleMember)
				hrSeat(t, s, hid, old.ID, models.HouseholdRelationAdult)
				if err := s.app.DB.Delete(old).Error; err != nil {
					t.Fatal(err)
				}
				return nil
			}},
	}
	for _, f := range fixtures {
		t.Run(f.name, func(t *testing.T) {
			s := newCoreErasureScene(t)
			u := newTestUser(t, s.app, "hrf-victim", erasurePW, models.RoleMember)
			hid := hrHouseholdOf(t, s, "Smith household", "1 Oak St")
			hrSeat(t, s, hid, u.ID, models.HouseholdRelationAdult)
			check := f.seed(t, s, u, hid)
			rep := hrErase(t, s, u.ID)
			h := hrHouseholdRow(t, s.app.DB, hid)
			if f.scrub {
				if h.Name != "Removed household" || h.Address != "" || rep.HouseholdsScrubbed != 1 || rep.HouseholdsKept != 0 {
					t.Errorf("sole-subject household: %+v report %+v, want scrubbed", h, rep)
				}
			} else if h.Name != "Smith household" || h.Address != "1 Oak St" || rep.HouseholdsKept != 1 || rep.HouseholdsScrubbed != 0 {
				t.Errorf("shared household: %+v report %+v, want kept unchanged", h, rep)
			}
			if rep.PeopleAnonymized != f.people {
				t.Errorf("report anonymized %d people, want %d", rep.PeopleAnonymized, f.people)
			}
			if n := countRows(t, s.app.DB, "household_accounts", "user_id = ?", u.ID); n != 0 {
				t.Errorf("the erased account still has %d seats", n)
			}
			if check != nil {
				check(t)
			}
		})
	}
}

// O-9 on the admin path: H with seats U and V and P in PERS(U). Either order
// ends with H scrubbed by the second erasure and P anonymized; a living
// holder's seat always keeps H.
func TestErasureHouseholdTwoSeatsEitherOrder(t *testing.T) {
	// What H and P look like at the end of each order, without ids and times.
	finals := map[string]string{}
	for _, order := range []string{"u then v", "v then u"} {
		t.Run(order, func(t *testing.T) {
			s := newCoreErasureScene(t)
			u := newTestUser(t, s.app, "hr2-u", erasurePW, models.RoleMember)
			v := newTestUser(t, s.app, "hr2-v", erasurePW, models.RoleMember)
			hid := hrHouseholdOf(t, s, "Pair household", "2 Elm St")
			hrSeat(t, s, hid, u.ID, models.HouseholdRelationAdult)
			hrSeat(t, s, hid, v.ID, models.HouseholdRelationAdult)
			p := hrPerson(t, s, hid, "Pee", models.HouseholdAdult, &u.ID, false)
			first, second := u, v
			if order == "v then u" {
				first, second = v, u
			}
			rep := hrErase(t, s, first.ID)
			if h := hrHouseholdRow(t, s.app.DB, hid); h.Name != "Pair household" || h.Address != "2 Elm St" || rep.HouseholdsKept != 1 {
				t.Fatalf("after the first erasure: %+v report %+v, want kept (the other seat is alive)", h, rep)
			}
			if first == v {
				if pr := hrPersonRow(t, s.app.DB, p); pr.FirstName != "Pee" || pr.DeletedAt.Valid {
					t.Errorf("the other individual's person changed: %+v", pr)
				}
			} else {
				hrAssertAnonymized(t, s.app.DB, p, &u.ID)
			}
			rep = hrErase(t, s, second.ID)
			if h := hrHouseholdRow(t, s.app.DB, hid); h.Name != "Removed household" || h.Address != "" || rep.HouseholdsScrubbed != 1 || rep.HouseholdsKept != 0 {
				t.Errorf("after the second erasure: %+v report %+v, want scrubbed", h, rep)
			}
			hrAssertAnonymized(t, s.app.DB, p, &u.ID)
			if n := countRows(t, s.app.DB, "household_accounts", "household_id = ?", hid); n != 0 {
				t.Errorf("%d seats remain", n)
			}
			h, pr := hrHouseholdRow(t, s.app.DB, hid), hrPersonRow(t, s.app.DB, p)
			finals[order] = fmt.Sprintf("household %q %q; person %q %q born %v notes %q photo %v deleted %v converted %v",
				h.Name, h.Address, pr.FirstName, pr.LastName, pr.BirthDate, pr.CareNotes, pr.PhotoConsent, pr.DeletedAt.Valid, pr.ConvertedUserID != nil)
		})
	}
	if finals["u then v"] == "" || finals["u then v"] != finals["v then u"] {
		t.Errorf("the two orders end differently:\n%s\n%s", finals["u then v"], finals["v then u"])
	}
}

// Two erased subjects who each own a person of H leave it shared: retained and
// counted. householdErasedOwnerIn names the owner for the one-owner case.
func TestErasureHouseholdTwoErasedSubjectsStayShared(t *testing.T) {
	s := newCoreErasureScene(t)
	u := newTestUser(t, s.app, "hr3-u", erasurePW, models.RoleMember)
	v := newTestUser(t, s.app, "hr3-v", erasurePW, models.RoleMember)
	hid := hrHouseholdOf(t, s, "Shared household", "3 Ash St")
	hrSeat(t, s, hid, u.ID, models.HouseholdRelationAdult)
	hrSeat(t, s, hid, v.ID, models.HouseholdRelationAdult)
	p := hrPerson(t, s, hid, "Pee", models.HouseholdAdult, &u.ID, false)
	q := hrPerson(t, s, hid, "Que", models.HouseholdAdult, &v.ID, false)
	hrErase(t, s, u.ID)
	rep := hrErase(t, s, v.ID)
	if h := hrHouseholdRow(t, s.app.DB, hid); h.Name != "Shared household" || h.Address != "3 Ash St" || rep.HouseholdsKept != 1 || rep.HouseholdsScrubbed != 0 {
		t.Errorf("%+v report %+v, want retained and counted", h, rep)
	}
	hrAssertAnonymized(t, s.app.DB, p, &u.ID)
	hrAssertAnonymized(t, s.app.DB, q, &v.ID)
}

// An already scrubbed household is counted and not rewritten (idempotent).
func TestErasureHouseholdAlreadyScrubbedIsNotRewritten(t *testing.T) {
	s := newCoreErasureScene(t)
	u := newTestUser(t, s.app, "hr4-u", erasurePW, models.RoleMember)
	hid := hrHouseholdOf(t, s, "Removed household", "")
	hrSeat(t, s, hid, u.ID, models.HouseholdRelationAdult)
	before := dumpRows(t, s.app.DB, "households", "id", "id = ?", hid)
	rep := hrErase(t, s, u.ID)
	if got := dumpRows(t, s.app.DB, "households", "id", "id = ?", hid); got != before {
		t.Errorf("the scrubbed household was rewritten:\n%s\n%s", before, got)
	}
	if rep.HouseholdsScrubbed != 1 || rep.HouseholdsKept != 0 {
		t.Errorf("report %+v, want the household counted as scrubbed", rep)
	}
}

// A completed adult conversion: the duplicate is anonymized, converted_user_id
// stays, and a sibling in the same household is untouched.
func TestErasureHouseholdCompletedConversion(t *testing.T) {
	s := newCoreErasureScene(t)
	u := newTestUser(t, s.app, "hr5-u", erasurePW, models.RoleMember)
	parent := newTestUser(t, s.app, "hr5-parent", erasurePW, models.RoleMember)
	hid := hrHouseholdOf(t, s, "Conv household", "5 Fir St")
	hrSeat(t, s, hid, parent.ID, models.HouseholdRelationGuardian)
	hrSeat(t, s, hid, u.ID, models.HouseholdRelationAdult)
	dup := hrPerson(t, s, hid, "Dup", models.HouseholdAdult, &u.ID, true)
	sib := hrPerson(t, s, hid, "Sib", models.HouseholdChild, nil, false)
	before := dumpRows(t, s.app.DB, "household_people", "id", "id = ?", sib)
	rep := hrErase(t, s, u.ID)
	hrAssertAnonymized(t, s.app.DB, dup, &u.ID)
	if got := dumpRows(t, s.app.DB, "household_people", "id", "id = ?", sib); got != before {
		t.Errorf("the sibling changed:\n%s\n%s", before, got)
	}
	if rep.PeopleAnonymized != 1 || rep.HouseholdsKept != 1 {
		t.Errorf("report %+v", rep)
	}
}

// stagedFixture is a teen erased after approval but before the conversion
// completed: a pending_consent account, the approved request, the person in a
// parent's household, a sibling, registrations, guardian links and consent
// tokens.
type stagedFixture struct {
	s       coreErasureScene
	teen    *models.User
	parent  *models.User
	hid     uint
	person  uint
	sibling uint
	occ     models.EventOccurrence
	// regs of the staged person and of the sibling
	personReg, siblingReg uint
	// guardian links
	personLink, teenLink, siblingLink, rejectedLink, revokedLink uint
	// tokens
	unusedToken, usedToken, otherTeenToken uint
	request                                uint
}

// newStagedFixture seeds the staged teen; withConsent adds the account consent
// an admin granted before any guardian confirmed.
func newStagedFixture(t *testing.T, withConsent bool) *stagedFixture {
	t.Helper()
	f := &stagedFixture{s: newCoreErasureScene(t)}
	s, db := f.s, f.s.app.DB
	s.ac.holdNotices = true
	f.teen = newTestUser(t, s.app, "hrs-teen", erasurePW, models.RoleMember)
	f.parent = newTestUser(t, s.app, "hrs-parent", erasurePW, models.RoleMember)
	if err := db.Model(f.teen).Updates(map[string]any{"account_type": models.AccountTeen, "status": models.UserPendingConsent}).Error; err != nil {
		t.Fatal(err)
	}
	f.hid = hrHouseholdOf(t, s, "Parents household", "6 Pine St")
	hrSeat(t, s, f.hid, f.parent.ID, models.HouseholdRelationGuardian)
	f.person = hrPerson(t, s, f.hid, "Teena", models.HouseholdTeen, nil, false)
	f.sibling = hrPerson(t, s, f.hid, "Sib", models.HouseholdChild, nil, false)
	inv := erasureInvitation(t, db, s.admin.ID, "hrs-invite@x.test", true, &f.person)
	f.request = erasureRequest(t, db, "hrs-invite@x.test", models.RequestHouseholdConversion, models.RequestApproved, &f.teen.ID, &f.person, &inv)
	g := newTestGroup(t, s.app, s.admin, "Staged Club", models.GroupActive)
	_, f.occ = seedEvent(t, s.app, g, s.admin.ID, hlFuture)
	reg := func(person uint, token string) uint {
		r := models.Registration{OccurrenceID: f.occ.ID, HouseholdPersonID: &person, RegisteredBy: &f.parent.ID,
			Status: models.RegGoing, Source: models.RegSourceWeb, AttendanceMode: models.AttendanceInPerson,
			ManageTokenHash: token, RemovalNote: "note " + token}
		erasureMustCreate(t, db, &r)
		return r.ID
	}
	f.personReg = reg(f.person, "hrs-person-token")
	f.siblingReg = reg(f.sibling, "hrs-sibling-token")
	link := func(l models.GuardianLink) uint {
		l.GuardianUserID = f.parent.ID
		erasureMustCreate(t, db, &l)
		return l.ID
	}
	person, sibling := f.person, f.sibling
	f.personLink = link(models.GuardianLink{PersonID: &person, Status: models.GuardianPending})
	f.teenLink = link(models.GuardianLink{TeenUserID: &f.teen.ID, Status: models.GuardianConfirmed, CheckNote: "met the family"})
	f.siblingLink = link(models.GuardianLink{PersonID: &sibling, Status: models.GuardianConfirmed})
	f.rejectedLink = link(models.GuardianLink{PersonID: &person, Status: models.GuardianRejected, CheckNote: "name did not match"})
	f.revokedLink = link(models.GuardianLink{PersonID: &person, Status: models.GuardianRevoked})
	erasureMustCreate(t, db, &models.Notification{UserID: s.admin.ID, Kind: notify.HouseholdLinkPending, Category: notify.CatAdmin,
		Waiting: true, SubjectType: "guardian_link", SubjectID: &f.personLink, Title: "Confirm a guardian", Link: "/admin"})
	if withConsent {
		erasureMustCreate(t, db, &models.TeenConsent{TeenUserID: f.teen.ID, Scope: models.TeenConsentAccount, Status: models.ConsentGranted,
			GrantedBy: s.admin.ID, GranterRole: models.ConsentAdmin, EvidenceText: "checked in person"})
	}
	used := time.Now().Add(-time.Hour)
	token := func(hash, payload string, usedAt *time.Time) uint {
		tok := models.AccountToken{Kind: models.TokenTeenConsent, UserID: f.parent.ID, TokenHash: hashToken(hash), Payload: payload,
			ExpiresAt: time.Now().Add(time.Hour), UsedAt: usedAt}
		erasureMustCreate(t, db, &tok)
		return tok.ID
	}
	f.unusedToken = token("hrs-unused", fmt.Sprintf(`{"teenId":%d,"linkId":%d}`, f.teen.ID, f.teenLink), nil)
	f.usedToken = token("hrs-used", fmt.Sprintf(`{"teenId":%d,"linkId":%d}`, f.teen.ID, f.teenLink), &used)
	f.otherTeenToken = token("hrs-other", fmt.Sprintf(`{"teenId":%d}`, f.teen.ID+1000), nil)
	return f
}

// T15 staged teen: the person is the erased individual, the sibling and
// everything O-4 keeps are untouched, registrations are cancelled without
// gaining a user_id, and nothing can complete the pair afterwards.
func TestErasureHouseholdStagedTeen(t *testing.T) {
	f := newStagedFixture(t, true)
	db := f.s.app.DB
	keep := func() string {
		return dumpRows(t, db, "household_people", "id", "id = ?", f.sibling) +
			dumpRows(t, db, "registrations", "id", "id = ?", f.siblingReg) +
			dumpRows(t, db, "guardian_links", "id", "id IN ?", []uint{f.siblingLink, f.rejectedLink, f.revokedLink}) +
			dumpRows(t, db, "teen_consents", "id", "teen_user_id = ?", f.teen.ID) +
			dumpRows(t, db, "account_tokens", "id", "id IN ?", []uint{f.usedToken, f.otherTeenToken}) +
			dumpRows(t, db, "audit_logs", "id", "action IN ? ", []string{audit.TeenConsentGranted})
	}
	before := keep()
	rep := hrReport(t, deleteAccount(f.s.ac, f.teen.ID, nil))
	hrAssertAnonymized(t, db, f.person, nil)
	if got := keep(); got != before {
		t.Errorf("rows that must stay changed:\n%s\n%s", before, got)
	}
	var reg models.Registration
	if err := db.First(&reg, f.personReg).Error; err != nil {
		t.Fatal(err)
	}
	if reg.Status != models.RegCancelled || reg.UserID != nil || reg.HouseholdPersonID == nil || *reg.HouseholdPersonID != f.person ||
		reg.RegisteredBy == nil || *reg.RegisteredBy != f.parent.ID || reg.ManageTokenHash != "" || reg.RemovalNote != "" {
		t.Errorf("the staged person's registration = %+v, want cancelled, no user_id, holder kept, token and note cleared", reg)
	}
	if n := countRows(t, db, "audit_logs", "action = ? AND target_type = 'guardian_link'", audit.GuardianLinkRevoked); n != 2 {
		t.Errorf("%d guardian_link.revoked rows, want one per revoked link (2)", n)
	}
	for _, id := range []uint{f.personLink, f.teenLink} {
		if n := countRows(t, db, "guardian_links", "id = ? AND status = ?", id, models.GuardianRevoked); n != 1 {
			t.Errorf("link %d is not revoked", id)
		}
		if n := countRows(t, db, "audit_logs", "action = ? AND target_id = ? AND meta_json->>'reason' = 'account_deleted'", audit.GuardianLinkRevoked, id); n != 1 {
			t.Errorf("link %d has %d revoke audit rows with reason account_deleted, want 1", id, n)
		}
	}
	if n := countRows(t, db, "notifications", "kind = ? AND subject_id = ? AND resolved_at IS NOT NULL", notify.HouseholdLinkPending, f.personLink); n != 1 {
		t.Error("the pending guardian task was not resolved")
	}
	if n := countRows(t, db, "account_tokens", "id = ?", f.unusedToken); n != 0 {
		t.Error("the guardian's unused consent link for the erased teen survived")
	}
	if n := countRows(t, db, "account_tokens", "id IN ?", []uint{f.usedToken, f.otherTeenToken}); n != 2 {
		t.Errorf("%d of the used and the other teen's tokens remain, want 2", n)
	}
	if rep.PeopleAnonymized != 1 || rep.GuardianLinksRevoked != 2 || rep.HouseholdsKept != 1 || rep.HouseholdsScrubbed != 0 {
		t.Errorf("report %+v", rep)
	}
	if h := hrHouseholdRow(t, db, f.hid); h.Name != "Parents household" || h.Address != "6 Pine St" {
		t.Errorf("the parents' household changed: %+v", h)
	}

	// Non-revival: completion, consent, enablement and the age rollover.
	var teen models.User
	if err := db.Unscoped().First(&teen, f.teen.ID).Error; err != nil {
		t.Fatal(err)
	}
	err := db.Transaction(func(tx *gorm.DB) error {
		return f.s.app.completeTeenConversionIfReady(tx, &teen, time.Now(), "")
	})
	if err != nil {
		t.Errorf("completeTeenConversionIfReady on the erased pair: %v, want nil (neither an error nor a revival)", err)
	}
	err = db.Transaction(func(tx *gorm.DB) error {
		err := f.s.app.changeTeenConsent(tx, nil, &teen, f.parent, teenConsentChange{Scope: models.TeenConsentAccount, Status: models.ConsentGranted, ConfirmedGuardian: true})
		return err
	})
	if !errors.Is(err, errConsentTeen) {
		t.Errorf("changeTeenConsent on the erased teen = %v, want errConsentTeen", err)
	}
	// The tombstone is old enough to roll over, and the rollover leaves it alone
	// (it reads live accounts only; completeTeenConversionIfReady answers nil for
	// a deleted account even if it were reached).
	if err := db.Unscoped().Model(&models.User{}).Where("id = ?", f.teen.ID).
		Update("birth_date", time.Now().UTC().AddDate(-19, 0, 0)).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.app.rolloverAges(time.Now()); err != nil {
		t.Errorf("age rollover: %v", err)
	}
	if n := countRows(t, db, "audit_logs", "action = ? AND target_id = ?", audit.UserTurned18, f.teen.ID); n != 0 {
		t.Errorf("the age rollover wrote %d user.turned_18 rows for the erased account", n)
	}
	if n := countRows(t, db, "users", "id = ? AND account_type = ? AND status = ?", f.teen.ID, models.AccountTeen, models.UserDisabled); n != 1 {
		t.Error("the age rollover changed the erased account")
	}
	// Admin enable, a later consent and a guardian confirmation are refused for
	// the erased account.
	for _, req := range []struct {
		method, path string
		body         any
	}{
		{"POST", "/api/v1/admin/guardian-links/" + itoa(f.teenLink) + "/confirm", map[string]any{"checkNote": "too late"}},
		{"PUT", "/api/v1/admin/users/" + itoa(f.teen.ID), map[string]any{"status": models.UserActive}},
		{"POST", "/api/v1/admin/users/" + itoa(f.teen.ID) + "/teen-consents", map[string]any{"scope": models.TeenConsentAccount, "status": models.ConsentGranted, "evidenceText": "too late"}},
	} {
		if w := f.s.ac.do(req.method, req.path, req.body, true); w.Code < 400 {
			t.Errorf("%s %s answered %d for an erased account, want an error", req.method, req.path, w.Code)
		}
	}
	if n := countRows(t, db, "household_people", "id = ? AND converted_user_id IS NULL", f.person); n != 1 {
		t.Error("the staged person was converted after the erasure")
	}
	if n := countRows(t, db, "registrations", "user_id = ?", f.teen.ID); n != 0 {
		t.Error("a registration moved to the erased account")
	}
	if n := countRows(t, db, "users", "id = ? AND deleted_at IS NOT NULL AND status = ?", f.teen.ID, models.UserDisabled); n != 1 {
		t.Error("the erased account was revived")
	}
}

// A staged teen whose guardian has not confirmed (no consent yet) is erased the
// same way: the person is scrubbed and the household is kept.
func TestErasureHouseholdStagedTeenBeforeConsent(t *testing.T) {
	f := newStagedFixture(t, false)
	db := f.s.app.DB
	rep := hrReport(t, deleteAccount(f.s.ac, f.teen.ID, nil))
	hrAssertAnonymized(t, db, f.person, nil)
	if rep.PeopleAnonymized != 1 || rep.HouseholdsKept != 1 {
		t.Errorf("report %+v", rep)
	}
	if n := countRows(t, db, "teen_consents", "teen_user_id = ?", f.teen.ID); n != 0 {
		t.Errorf("%d consent rows appeared", n)
	}
	if n := countRows(t, db, "registrations", "id = ? AND status = ? AND user_id IS NULL", f.personReg, models.RegCancelled); n != 1 {
		t.Error("the staged registration was not cancelled")
	}
}

// A guardian is erased: the links she holds end, her sibling guardian's stay.
func TestErasureHouseholdGuardianLinksOfAnErasedGuardian(t *testing.T) {
	s := newCoreErasureScene(t)
	db := s.app.DB
	g := newTestUser(t, s.app, "hrg-guardian", erasurePW, models.RoleMember)
	other := newTestUser(t, s.app, "hrg-other", erasurePW, models.RoleMember)
	hid := hrHouseholdOf(t, s, "Guardian household", "7 Cedar St")
	hrSeat(t, s, hid, g.ID, models.HouseholdRelationGuardian)
	hrSeat(t, s, hid, other.ID, models.HouseholdRelationGuardian)
	kid := hrPerson(t, s, hid, "Kid", models.HouseholdChild, nil, false)
	mine := models.GuardianLink{GuardianUserID: g.ID, PersonID: &kid, Status: models.GuardianConfirmed, CheckNote: "kept note"}
	theirs := models.GuardianLink{GuardianUserID: other.ID, PersonID: &kid, Status: models.GuardianConfirmed}
	erasureMustCreate(t, db, &mine)
	erasureMustCreate(t, db, &theirs)
	before := dumpRows(t, db, "guardian_links", "id", "id = ?", theirs.ID)
	rep := hrErase(t, s, g.ID)
	if n := countRows(t, db, "guardian_links", "id = ? AND status = ? AND check_note = 'kept note'", mine.ID, models.GuardianRevoked); n != 1 {
		t.Error("the erased guardian's link was not revoked with its check note kept")
	}
	if got := dumpRows(t, db, "guardian_links", "id", "id = ?", theirs.ID); got != before {
		t.Errorf("another guardian's link changed:\n%s\n%s", before, got)
	}
	if rep.GuardianLinksRevoked != 1 || rep.HouseholdsKept != 1 {
		t.Errorf("report %+v", rep)
	}
}

// hrIntegritySnapshot is hlSnapshot plus the invitation and consent rows an
// erasure would also scrub, so an aborted erasure proves it rolled back whole.
func hrIntegritySnapshot(t *testing.T, db *gorm.DB) string {
	t.Helper()
	return hlSnapshot(t, db) + "\ninvitations: " + dumpRows(t, db, "invitations", "id", "true") +
		"\nteen_consents: " + dumpRows(t, db, "teen_consents", "id", "true")
}

// T15 integrity: a contradictory identity aborts the whole erasure before its
// first write, with the existing 500.
func TestErasurePersIntegrityAborts(t *testing.T) {
	cases := []struct {
		name string
		seed func(t *testing.T, s coreErasureScene, u *models.User, hid uint)
	}{
		{"completed and completed", func(t *testing.T, s coreErasureScene, u *models.User, hid uint) {
			hrPerson(t, s, hid, "One", models.HouseholdAdult, &u.ID, true)
			hrPerson(t, s, hid, "Two", models.HouseholdAdult, &u.ID, true)
		}},
		{"completed and a different staged person", func(t *testing.T, s coreErasureScene, u *models.User, hid uint) {
			hrPerson(t, s, hid, "One", models.HouseholdAdult, &u.ID, true)
			two := hrPerson(t, s, hid, "Two", models.HouseholdTeen, nil, false)
			erasureRequest(t, s.app.DB, "hrp-two@x.test", models.RequestHouseholdConversion, models.RequestApproved, &u.ID, &two, nil)
		}},
		{"staged and staged", func(t *testing.T, s coreErasureScene, u *models.User, hid uint) {
			// The partial unique index on approved_user_id stops a second
			// approved request for one user: assert it exists, drop it in this
			// isolated database and put it back in cleanup.
			const index = "idx_account_requests_approved_conversion_user"
			if n := countRows(t, s.app.DB, "pg_indexes", "indexname = ?", index); n != 1 {
				t.Fatalf("%s is missing: the baseline guarantee this case relies on is gone", index)
			}
			if err := s.app.DB.Exec("DROP INDEX " + index).Error; err != nil {
				t.Fatal(err)
			}
			var twoRequest uint
			t.Cleanup(func() {
				// Two approved rows for one user cannot sit under the index: remove
				// the second before it comes back.
				if twoRequest != 0 {
					if err := s.app.DB.Exec("DELETE FROM account_requests WHERE id = ?", twoRequest).Error; err != nil {
						t.Errorf("remove the second approved request: %v", err)
					}
				}
				if err := s.app.DB.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS ` + index + ` ON public.account_requests USING btree (approved_user_id)
					WHERE ((source = 'household_conversion'::text) AND (status = 'approved'::text) AND (approved_user_id IS NOT NULL))`).Error; err != nil {
					t.Errorf("recreate %s: %v", index, err)
				}
			})
			one := hrPerson(t, s, hid, "One", models.HouseholdTeen, nil, false)
			two := hrPerson(t, s, hid, "Two", models.HouseholdTeen, nil, false)
			erasureRequest(t, s.app.DB, "hrp-one@x.test", models.RequestHouseholdConversion, models.RequestApproved, &u.ID, &one, nil)
			twoRequest = erasureRequest(t, s.app.DB, "hrp-two@x.test", models.RequestHouseholdConversion, models.RequestApproved, &u.ID, &two, nil)
		}},
		{"completed person claimed by another account's approved conversion", func(t *testing.T, s coreErasureScene, u *models.User, hid uint) {
			// P is converted to U while an approved household_conversion request
			// names P for V. U has no request of its own, so only a check from the
			// person's side sees it; the partial unique indexes allow the row.
			v := newTestUser(t, s.app, "hrp-claimant", erasurePW, models.RoleMember)
			p := hrPerson(t, s, hid, "One", models.HouseholdAdult, &u.ID, true)
			erasureRequest(t, s.app.DB, "hrp-claim@x.test", models.RequestHouseholdConversion, models.RequestApproved, &v.ID, &p, nil)
			erasureInvitation(t, s.app.DB, s.admin.ID, "hrp-claim-inv@x.test", false, &p)
			erasureMustCreate(t, s.app.DB, &models.GuardianLink{GuardianUserID: v.ID, PersonID: &p, Status: models.GuardianPending})
		}},
		{"staged person in another household than its invitation", func(t *testing.T, s coreErasureScene, u *models.User, hid uint) {
			elsewhere := hrHouseholdOf(t, s, "Elsewhere", "9 Far St")
			one := hrPerson(t, s, hid, "One", models.HouseholdTeen, nil, false)
			other := hrPerson(t, s, elsewhere, "Other", models.HouseholdTeen, nil, false)
			inv := erasureInvitation(t, s.app.DB, s.admin.ID, "hrp-inv@x.test", true, &other)
			erasureRequest(t, s.app.DB, "hrp-inv@x.test", models.RequestHouseholdConversion, models.RequestApproved, &u.ID, &one, &inv)
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := newCoreErasureScene(t)
			s.ac.holdNotices = true
			u := newTestUser(t, s.app, "hrp-victim", erasurePW, models.RoleMember)
			hid := hrHouseholdOf(t, s, "Integrity household", "8 Birch St")
			hrSeat(t, s, hid, u.ID, models.HouseholdRelationAdult)
			c.seed(t, s, u, hid)
			before := hrIntegritySnapshot(t, s.app.DB)
			r := deleteAccount(s.ac, u.ID, nil)
			if r.code != http.StatusInternalServerError {
				t.Fatalf("delete: %d %s, want the existing 500", r.code, r.body)
			}
			if after := hrIntegritySnapshot(t, s.app.DB); after != before {
				t.Error("the aborted erasure changed users, audit, notices, jobs, invitations, consents or household rows")
			}
			if n := countRows(t, s.app.DB, "users", "id = ? AND deleted_at IS NULL", u.ID); n != 1 {
				t.Error("the user was deleted")
			}
		})
	}
}

// validatePersIn reads both arms and accepts one clean individual, completed or
// staged, and the same person on both arms.
func TestValidatePersAcceptsOneIndividual(t *testing.T) {
	s := newCoreErasureScene(t)
	u := newTestUser(t, s.app, "hrv-victim", erasurePW, models.RoleMember)
	hid := hrHouseholdOf(t, s, "Valid household", "")
	if err := validatePersIn(s.app.DB, erasureSubject{ID: u.ID}); err != nil {
		t.Errorf("no person: %v", err)
	}
	p := hrPerson(t, s, hid, "One", models.HouseholdAdult, &u.ID, true)
	erasureRequest(t, s.app.DB, "hrv@x.test", models.RequestHouseholdConversion, models.RequestApproved, &u.ID, &p, nil)
	if err := validatePersIn(s.app.DB, erasureSubject{ID: u.ID}); err != nil {
		t.Errorf("the same person on both arms: %v", err)
	}
	two := hrPerson(t, s, hid, "Two", models.HouseholdAdult, &u.ID, true)
	if err := validatePersIn(s.app.DB, erasureSubject{ID: u.ID}); !errors.Is(err, errErasureIntegrity) {
		t.Errorf("two completed people: %v, want errErasureIntegrity", err)
	}
	_ = two
}

// validatePersIn reads the person's side too: a completed person that another
// account's approved request names is a contradiction even when the erased
// account has no request, and the same person on U's own request is not.
func TestValidatePersRejectsAnotherAccountsApprovedRequest(t *testing.T) {
	s := newCoreErasureScene(t)
	u := newTestUser(t, s.app, "hrv-owner", erasurePW, models.RoleMember)
	v := newTestUser(t, s.app, "hrv-claimant", erasurePW, models.RoleMember)
	hid := hrHouseholdOf(t, s, "Claim household", "")
	p := hrPerson(t, s, hid, "One", models.HouseholdAdult, &u.ID, true)
	req := erasureRequest(t, s.app.DB, "hrv-claim@x.test", models.RequestHouseholdConversion, models.RequestApproved, &v.ID, &p, nil)
	if err := validatePersIn(s.app.DB, erasureSubject{ID: u.ID}); !errors.Is(err, errErasureIntegrity) {
		t.Errorf("completed for U, claimed by V: %v, want errErasureIntegrity", err)
	}
	// V's request is not approved any more: nothing contradicts the link.
	if err := s.app.DB.Exec("UPDATE account_requests SET status = ? WHERE id = ?", models.RequestRejected, req).Error; err != nil {
		t.Fatal(err)
	}
	if err := validatePersIn(s.app.DB, erasureSubject{ID: u.ID}); err != nil {
		t.Errorf("a rejected request by V: %v", err)
	}
}

// Lock-upgrade trace (R4-3b-LOCK-UPGRADE, 9.6): an erasure with a staged
// person locks the person's occurrences ascending under the household
// (reconcileHouseholdPerson) and then promoteOnDisable's own ascending set. A
// writer that holds the later occurrence and then asks for the erased user's row
// closes a D4-class cycle (writer-first schedule, the one this test runs); the
// database breaks it with exactly one 40P01, the loser rolls back whole, and no
// household was waited for, so nothing retries it. The other schedules (a writer
// holding the household, the erasure first, and the gated multi-occurrence
// writer of api_series_registrations.go) are the two tests after this one.
func TestHouseholdStagedErasureMeetsOccurrenceWriters(t *testing.T) {
	f := newStagedFixture(t, true)
	s, db := f.s, f.s.app.DB
	g := newTestGroup(t, s.app, s.admin, "Second Staged Club", models.GroupActive)
	_, later := seedEvent(t, s.app, g, s.admin.ID, hlFuture.Add(48*time.Hour))
	p := f.person
	erasureMustCreate(t, db, &models.Registration{OccurrenceID: later.ID, HouseholdPersonID: &p, RegisteredBy: &f.parent.ID,
		Status: models.RegGoing, Source: models.RegSourceWeb, AttendanceMode: models.AttendanceInPerson})
	states := erasureTestSQLStates(t, s.app)
	// The writer's session is on the traced handle too: the deadlock is raised
	// to the later waiter, which is the writer, so only a traced writer records it.
	db = s.app.DB

	// The writer holds the later occurrence, then asks for the erased user's row.
	writer, writerPID := beginOwnedTx(t, db)
	if err := writer.Exec("SELECT id FROM event_occurrences WHERE id = ? FOR UPDATE", later.ID).Error; err != nil {
		t.Fatal(err)
	}
	del := bgDo(s.ac, "DELETE", "/api/v1/admin/users/"+itoa(f.teen.ID), nil)
	erasurePID := waitForLockedUsersQuery(t, s.app, "%event_occurrences%FOR UPDATE%")
	waitDBBlocked(t, db, erasurePID, writerPID)
	var queryStart time.Time
	if err := db.Raw("SELECT query_start FROM pg_stat_activity WHERE pid = ?", erasurePID).Scan(&queryStart).Error; err != nil {
		t.Fatal(err)
	}
	erasureTestWaitPastDeadlockCheck(t, s.app, queryStart)
	writerErr := writer.Exec("SELECT id FROM users WHERE id = ? FOR KEY SHARE", f.teen.ID).Error
	if pgCode(writerErr) != "40P01" {
		t.Fatalf("the later waiter's error = %v, want SQLSTATE 40P01", writerErr)
	}
	if err := writer.Rollback().Error; err != nil {
		t.Fatal(err)
	}
	if got := awaitResult(t, del); got.code != http.StatusOK {
		t.Fatalf("erasure: %d %s; the erasure was meant to win", got.code, got.body)
	}
	if n := states.Count("40P01"); n != 1 {
		t.Errorf("%d deadlocks recorded, want exactly the one", n)
	}
	if n := states.Count("55P03"); n != 0 {
		t.Errorf("%d busy answers, want none: no household was held", n)
	}
	for _, id := range []uint{f.personReg} {
		if n := countRows(t, db, "registrations", "id = ? AND status = ? AND user_id IS NULL", id, models.RegCancelled); n != 1 {
			t.Errorf("registration %d was not cancelled", id)
		}
	}
	if n := countRows(t, db, "registrations", "household_person_id = ? AND status = ?", f.person, models.RegCancelled); n != 2 {
		t.Errorf("%d staged registrations cancelled, want both dates", n)
	}
}

// ---------------------------------------------------------------------------
// The 9.6 barriers with the household step in place (R4-3b-2): rows 1, 3, 4 and
// 5 and first-use creation, erasure first and writer first, assert what the
// erasure leaves in the household (closing R4-3a's limit, which only proved
// that neither side deadlocked); rows 6 and 7 refuse an erased holder, inviter
// or actor; fixtures (g) and (h) of T16.
// ---------------------------------------------------------------------------

// hbRoute is the hwRoute of that name. The person-add routes add a child, so a
// pending guardian link and an admin waiting task come with the person.
func hbRoute(name string) hwRoute {
	for _, r := range hwRoutes {
		if r.name != name {
			continue
		}
		if name == "POST person" || name == "first use POST person" {
			r.body = map[string]any{"firstName": "Bea", "kind": "child", "birthDate": "2020-05-01"}
		}
		return r
	}
	panic("no household route named " + name)
}

// hbScene is the member's scene for one route: a household named "Writers" with
// no other person, plus Ada for the two routes that act on a person. A
// first-use route starts with nothing.
func hbScene(t *testing.T, r hwRoute) hwScene {
	t.Helper()
	s := newHWScene(t, false)
	if r.firstUse {
		return s
	}
	apiDo(t, s.c, "PUT", "/api/v1/me/household", map[string]any{"name": "Writers", "address": "1 Oak St"}, http.StatusOK)
	s.hid = getMyHousehold(t, s.c).ID
	if r.name == "PUT person" || r.name == "DELETE person" {
		s.pid = addMyPerson(t, s.c, map[string]any{"firstName": "Ada", "kind": "adult"}).ID
	}
	return s
}

// hbFirstWrite is the writer's first write once it holds the user and the
// household. A first-use request is two transactions: ensureHousehold makes the
// household and the seat in the first and commits, then the route's own write
// takes the user lock again in the second. The writer-first barrier stops at
// that second write, so the writer holds the user lock for the part of the
// request that writes the submitted text; the gap between the two is
// TestHouseholdBarriersFirstUseGapRefusesTheSecondStep.
func hbFirstWrite(r hwRoute, s hwScene) erasureTestPoint {
	if r.firstWrite != nil {
		return r.firstWrite(s)
	}
	if r.name == "first use POST person" {
		return erasureTestPoint{Processor: "create", Step: "gorm:create", Table: "household_people"}
	}
	return erasureTestPoint{Processor: "update", Step: "gorm:update", Table: "households",
		Assign: map[string]any{"name": "Fresh"}}
}

// hbAuditByActor counts, per household audit action, the rows the account wrote.
func hbAuditByActor(t *testing.T, db *gorm.DB, uid uint) map[string]int64 {
	t.Helper()
	got := map[string]int64{}
	for _, action := range []string{audit.HouseholdCreated, audit.HouseholdUpdated, audit.HouseholdPersonAdded, audit.HouseholdPersonUpdated, audit.HouseholdPersonRemoved} {
		got[action] = countRows(t, db, "audit_logs", "action = ? AND actor_id = ?", action, uid)
	}
	return got
}

// hbHouseholdOf is the member's household: the seeded one, or the first-use one.
func hbHouseholdOf(t *testing.T, s hwScene) models.Household {
	t.Helper()
	var h models.Household
	if err := s.app.DB.Where("created_by = ?", s.u.ID).First(&h).Error; err != nil {
		t.Fatalf("the member's household: %v", err)
	}
	return h
}

func hbErase(s hwScene) <-chan httpResult {
	return bgDo(s.ac, "DELETE", "/api/v1/admin/users/"+itoa(s.u.ID), nil)
}

// hbWant is what a route leaves after the erasure.
type hbWant struct {
	// writerFirst runs after the writer committed and the erasure then ran.
	writerFirst func(t *testing.T, s hwScene, rep erasureReportJSON)
	// erasureFirst runs after the erasure committed and the writer was refused.
	erasureFirst func(t *testing.T, s hwScene, rep erasureReportJSON)
}

func hbScrubbed(t *testing.T, h models.Household) {
	t.Helper()
	if h.Name != "Removed household" || h.Address != "" {
		t.Errorf("household %q at %q, want the scrubbed values", h.Name, h.Address)
	}
}

// hbNoSubmittedText: nothing the writer submitted is left in a household, person
// or audit column.
func hbNoSubmittedText(t *testing.T, db *gorm.DB, texts ...string) {
	t.Helper()
	for _, text := range texts {
		if n := countRows(t, db, "households", "name = ? OR address = ?", text, text); n != 0 {
			t.Errorf("%d households still carry %q", n, text)
		}
		if n := countRows(t, db, "audit_logs", "meta_json::text LIKE ?", "%"+text+"%"); n != 0 {
			t.Errorf("%d audit rows carry %q", n, text)
		}
	}
}

var hbWants = map[string]hbWant{
	"PUT household": {
		writerFirst: func(t *testing.T, s hwScene, rep erasureReportJSON) {
			hbScrubbed(t, hrHouseholdRow(t, s.app.DB, s.hid))
			if rep.HouseholdsScrubbed != 1 || rep.HouseholdsKept != 0 {
				t.Errorf("report %+v, want the household scrubbed", rep)
			}
			hbNoSubmittedText(t, s.app.DB, "Renamed", "2 Elm St")
			if n := auditCount(t, s.app, audit.HouseholdUpdated); n != 2 {
				t.Errorf("%d household.updated rows, want the setup's and the writer's", n)
			}
		},
		erasureFirst: func(t *testing.T, s hwScene, rep erasureReportJSON) {
			hbScrubbed(t, hrHouseholdRow(t, s.app.DB, s.hid))
			if rep.HouseholdsScrubbed != 1 {
				t.Errorf("report %+v, want the household scrubbed", rep)
			}
			hbNoSubmittedText(t, s.app.DB, "Renamed", "2 Elm St")
			if n := auditCount(t, s.app, audit.HouseholdUpdated); n != 1 {
				t.Errorf("%d household.updated rows, want only the setup's", n)
			}
		},
	},
	"POST person": {
		// Bea is a child nobody erased, so the household is shared and stays; the
		// pending guardian link the writer made for U ends with U.
		writerFirst: func(t *testing.T, s hwScene, rep erasureReportJSON) {
			if h := hrHouseholdRow(t, s.app.DB, s.hid); h.Name != "Writers" || h.Address != "1 Oak St" || rep.HouseholdsKept != 1 || rep.HouseholdsScrubbed != 0 {
				t.Errorf("household %+v report %+v, want it retained and counted", h, rep)
			}
			if n := countRows(t, s.app.DB, "household_people", "household_id = ? AND first_name = 'Bea' AND deleted_at IS NULL", s.hid); n != 1 {
				t.Errorf("%d people named Bea, want the writer's one untouched", n)
			}
			if n := countRows(t, s.app.DB, "guardian_links", "guardian_user_id = ? AND status = ?", s.u.ID, models.GuardianRevoked); n != 1 || rep.GuardianLinksRevoked != 1 {
				t.Errorf("%d revoked links, report %+v, want the new pending link revoked", n, rep)
			}
			if n := countRows(t, s.app.DB, "guardian_links", "guardian_user_id = ? AND status <> ?", s.u.ID, models.GuardianRevoked); n != 0 {
				t.Errorf("%d live links remain for the erased guardian", n)
			}
			if n := countRows(t, s.app.DB, "notifications", "kind = ? AND resolved_at IS NULL AND waiting", notify.HouseholdLinkPending); n != 0 {
				t.Errorf("%d guardian tasks are still waiting", n)
			}
			if n := countRows(t, s.app.DB, "audit_logs", "action = ? AND meta_json->>'reason' = 'account_deleted'", audit.GuardianLinkRevoked); n != 1 {
				t.Errorf("%d revoke audit rows with reason account_deleted, want 1", n)
			}
		},
		erasureFirst: func(t *testing.T, s hwScene, rep erasureReportJSON) {
			hbScrubbed(t, hrHouseholdRow(t, s.app.DB, s.hid))
			if n := countRows(t, s.app.DB, "household_people", "household_id = ?", s.hid); n != 0 {
				t.Errorf("%d people in the scrubbed household, want none", n)
			}
			if n := countRows(t, s.app.DB, "guardian_links", "guardian_user_id = ?", s.u.ID); n != 0 {
				t.Errorf("%d guardian links for the erased account, want none", n)
			}
			if n := countRows(t, s.app.DB, "notifications", "kind = ?", notify.HouseholdLinkPending); n != 0 {
				t.Errorf("%d guardian tasks, want none", n)
			}
		},
	},
	"PUT person": {
		writerFirst: func(t *testing.T, s hwScene, rep erasureReportJSON) {
			if h := hrHouseholdRow(t, s.app.DB, s.hid); h.Name != "Writers" || rep.HouseholdsKept != 1 {
				t.Errorf("household %+v report %+v, want it retained and counted (Ada is not the erased person)", h, rep)
			}
			if n := countRows(t, s.app.DB, "household_people", "id = ? AND first_name = 'Changed'", s.pid); n != 1 {
				t.Error("the writer's committed change to Ada is gone")
			}
		},
		erasureFirst: func(t *testing.T, s hwScene, rep erasureReportJSON) {
			if h := hrHouseholdRow(t, s.app.DB, s.hid); h.Name != "Writers" || rep.HouseholdsKept != 1 {
				t.Errorf("household %+v report %+v, want it retained and counted", h, rep)
			}
			if n := countRows(t, s.app.DB, "household_people", "id = ? AND first_name = 'Ada'", s.pid); n != 1 {
				t.Error("a refused writer changed Ada")
			}
		},
	},
	"DELETE person": {
		writerFirst: func(t *testing.T, s hwScene, rep erasureReportJSON) {
			if h := hrHouseholdRow(t, s.app.DB, s.hid); h.Name != "Writers" || rep.HouseholdsKept != 1 {
				t.Errorf("household %+v report %+v, want it retained and counted (a soft-deleted other person)", h, rep)
			}
			if n := countRows(t, s.app.DB, "household_people", "id = ? AND deleted_at IS NOT NULL", s.pid); n != 1 {
				t.Error("the writer's committed removal of Ada is gone")
			}
			if n := auditCount(t, s.app, audit.HouseholdPersonRemoved); n != 1 {
				t.Errorf("%d household.person_removed rows, want the writer's one", n)
			}
		},
		erasureFirst: func(t *testing.T, s hwScene, rep erasureReportJSON) {
			if n := countRows(t, s.app.DB, "household_people", "id = ? AND deleted_at IS NULL", s.pid); n != 1 {
				t.Error("a refused writer removed Ada")
			}
			if n := auditCount(t, s.app, audit.HouseholdPersonRemoved); n != 0 {
				t.Errorf("%d household.person_removed rows after a refused writer", n)
			}
			if rep.HouseholdsKept != 1 {
				t.Errorf("report %+v, want the household retained", rep)
			}
		},
	},
	"first use PUT household": {
		// The writer made the household and the seat; the erasure then removes
		// the seat and scrubs the household (no one else is in it).
		writerFirst: func(t *testing.T, s hwScene, rep erasureReportJSON) {
			hbScrubbed(t, hbHouseholdOf(t, s))
			if rep.HouseholdsScrubbed != 1 {
				t.Errorf("report %+v, want the household scrubbed", rep)
			}
			hbNoSubmittedText(t, s.app.DB, "Fresh")
			if n := countRows(t, s.app.DB, "households", "created_by = ?", s.u.ID); n != 1 {
				t.Errorf("%d households made by the member, want 1", n)
			}
		},
		erasureFirst: func(t *testing.T, s hwScene, rep erasureReportJSON) {
			if n := countRows(t, s.app.DB, "households", "created_by = ?", s.u.ID); n != 0 {
				t.Errorf("%d households made for the erased account, want none", n)
			}
			if rep.HouseholdsKept != 0 || rep.HouseholdsScrubbed != 0 {
				t.Errorf("report %+v, want no household touched", rep)
			}
			if n := auditCount(t, s.app, audit.HouseholdCreated); n != 0 {
				t.Errorf("%d household.created rows, want none", n)
			}
		},
	},
	"first use POST person": {
		// Bea survives the erasure, so the household the writer made is shared and
		// stays, without the erased account's seat.
		writerFirst: func(t *testing.T, s hwScene, rep erasureReportJSON) {
			if h := hbHouseholdOf(t, s); h.Name == "Removed household" || rep.HouseholdsKept != 1 {
				t.Errorf("household %+v report %+v, want it retained and counted", h, rep)
			}
			if n := countRows(t, s.app.DB, "household_people", "first_name = 'Bea' AND deleted_at IS NULL"); n != 1 {
				t.Errorf("%d people named Bea, want the writer's one", n)
			}
			if rep.GuardianLinksRevoked != 1 {
				t.Errorf("report %+v, want the new guardian link revoked", rep)
			}
		},
		erasureFirst: func(t *testing.T, s hwScene, rep erasureReportJSON) {
			if n := countRows(t, s.app.DB, "households", "created_by = ?", s.u.ID); n != 0 {
				t.Errorf("%d households made for the erased account, want none", n)
			}
			if n := countRows(t, s.app.DB, "household_people", "true"); n != 0 {
				t.Errorf("%d household people, want none", n)
			}
			if n := countRows(t, s.app.DB, "guardian_links", "guardian_user_id = ?", s.u.ID); n != 0 {
				t.Errorf("%d guardian links, want none", n)
			}
		},
	},
}

// TestHouseholdBarriersWriterFirstThenErasureScrubs: the writer holds the user
// row and the household and is stopped before its first write; the real DELETE
// waits at the user row; the writer commits; the erasure reads the committed
// rows and scrubs or retains by T16. Neither side reports 40P01 or 55P03 (the
// erasure waited at the user row, not on a household), and the erasure's answer
// carries the report.
func TestHouseholdBarriersWriterFirstThenErasureScrubs(t *testing.T) {
	for _, name := range []string{"PUT household", "POST person", "PUT person", "DELETE person", "first use PUT household", "first use POST person"} {
		t.Run(name, func(t *testing.T) {
			r := hbRoute(name)
			s := hbScene(t, r)
			states := erasureTestSQLStates(t, s.app)
			var writerPID atomic.Int64
			p := hbFirstWrite(r, s)
			p.PID = &writerPID
			reached, release := erasureTestPause(t, s.app.DB, p)
			res := r.send(s.c, s)
			awaitDBSignal(t, reached)
			del := hbErase(s)
			waiter := waitForRowLockWaiter(t, s.app)
			waitDBBlocked(t, s.app.DB, waiter, int(writerPID.Load()))
			release()
			if got := awaitResult(t, res); got.code != r.status {
				t.Fatalf("%s: %d %s, want %d", r.name, got.code, got.body, r.status)
			}
			rep := hrReport(t, awaitResult(t, del))
			if n := states.Count("40P01"); n != 0 {
				t.Errorf("%d deadlocks recorded", n)
			}
			if n := states.Count("55P03"); n != 0 {
				t.Errorf("%d busy answers recorded: the erasure waited at the user row, never on a household", n)
			}
			if n := countRows(t, s.app.DB, "users", "id = ? AND deleted_at IS NOT NULL", s.u.ID); n != 1 {
				t.Fatal("the erasure did not commit")
			}
			if n := countRows(t, s.app.DB, "household_accounts", "user_id = ?", s.u.ID); n != 0 {
				t.Errorf("the erased account still has %d seats", n)
			}
			hbWants[name].writerFirst(t, s, rep)
		})
	}
}

// TestHouseholdBarriersFirstUseGapRefusesTheSecondStep: a first-use request is
// two transactions. The erasure waits behind the first (the household and the
// seat), commits after it, and the route's own write then finds the account
// erased: the usual 401, nothing the request submitted is stored, and the
// household the first step made is a sole-subject one the erasure scrubbed.
func TestHouseholdBarriersFirstUseGapRefusesTheSecondStep(t *testing.T) {
	for _, name := range []string{"first use PUT household", "first use POST person"} {
		t.Run(name, func(t *testing.T) {
			r := hbRoute(name)
			s := hbScene(t, r)
			states := erasureTestSQLStates(t, s.app)
			var writerPID atomic.Int64
			reached, release := erasureTestPause(t, s.app.DB, erasureTestPoint{Processor: "create", Step: "gorm:create",
				Table: "households", PID: &writerPID})
			res := r.send(s.c, s)
			awaitDBSignal(t, reached)
			del := hbErase(s)
			waiter := waitForRowLockWaiter(t, s.app)
			waitDBBlocked(t, s.app.DB, waiter, int(writerPID.Load()))
			release()
			rep := hrReport(t, awaitResult(t, del))
			got := awaitResult(t, res)
			if got.code != http.StatusUnauthorized || !strings.Contains(got.body, `"unauthorized"`) {
				t.Fatalf("%s: %d %s, want 401 unauthorized for the second step", r.name, got.code, got.body)
			}
			if n := states.Count("40P01") + states.Count("55P03"); n != 0 {
				t.Errorf("%d deadlocks or busy answers recorded", n)
			}
			hbScrubbed(t, hbHouseholdOf(t, s))
			if rep.HouseholdsScrubbed != 1 || rep.HouseholdsKept != 0 {
				t.Errorf("report %+v, want the household scrubbed", rep)
			}
			hbNoSubmittedText(t, s.app.DB, "Fresh", "Bea")
			if n := countRows(t, s.app.DB, "household_people", "true"); n != 0 {
				t.Errorf("%d household people, want none", n)
			}
			if n := countRows(t, s.app.DB, "household_accounts", "user_id = ?", s.u.ID); n != 0 {
				t.Errorf("the erased account has %d seats", n)
			}
			if n := countRows(t, s.app.DB, "guardian_links", "guardian_user_id = ?", s.u.ID); n != 0 {
				t.Errorf("%d guardian links, want none", n)
			}
			got2 := hbAuditByActor(t, s.app.DB, s.u.ID)
			if got2[audit.HouseholdCreated] != 1 || got2[audit.HouseholdUpdated] != 0 || got2[audit.HouseholdPersonAdded] != 0 {
				t.Errorf("audit rows by the account %v, want only the first step's household.created", got2)
			}
		})
	}
}

// TestHouseholdBarriersErasureFirstRefusesTheWriter: the real DELETE has done
// the whole household step and stops at its last statement, still holding the
// user row; the preloaded request (middleware loaded the account before) blocks
// on that row; the erasure commits; the writer is refused with the usual 401 and
// writes nothing: no household, seat, person, link, notice or audit row for U.
func TestHouseholdBarriersErasureFirstRefusesTheWriter(t *testing.T) {
	for _, name := range []string{"PUT household", "POST person", "PUT person", "DELETE person", "first use PUT household", "first use POST person"} {
		t.Run(name, func(t *testing.T) {
			r := hbRoute(name)
			s := hbScene(t, r)
			states := erasureTestSQLStates(t, s.app)
			var erasurePID atomic.Int64
			reached, release := erasureTestPause(t, s.app.DB, erasureTestPoint{Processor: "raw", Step: "gorm:raw",
				SQLContains: "DELETE FROM login_throttles", PID: &erasurePID})
			// The scene's own setup wrote household rows as this account; the
			// refused writer adds none to them.
			setup := hbAuditByActor(t, s.app.DB, s.u.ID)
			del := hbErase(s)
			awaitDBSignal(t, reached)
			res := r.send(s.c, s)
			waiters := hwWaiters(t, s.app, 1)
			waitDBBlocked(t, s.app.DB, waiters[0], int(erasurePID.Load()))
			release()
			rep := hrReport(t, awaitResult(t, del))
			got := awaitResult(t, res)
			if got.code != http.StatusUnauthorized || !strings.Contains(got.body, `"unauthorized"`) {
				t.Fatalf("%s: %d %s, want 401 unauthorized", r.name, got.code, got.body)
			}
			if n := states.Count("40P01") + states.Count("55P03"); n != 0 {
				t.Errorf("%d deadlocks or busy answers recorded", n)
			}
			for action, n := range hbAuditByActor(t, s.app.DB, s.u.ID) {
				if n != setup[action] {
					t.Errorf("%d %s rows by the erased account, %d before the writer's request", n, action, setup[action])
				}
			}
			if n := countRows(t, s.app.DB, "household_accounts", "user_id = ?", s.u.ID); n != 0 {
				t.Errorf("the erased account has %d seats", n)
			}
			if n := countRows(t, s.app.DB, "notifications", "user_id = ?", s.u.ID); n != 0 {
				t.Errorf("%d notices for the erased account", n)
			}
			hbWants[name].erasureFirst(t, s, rep)
		})
	}
}

// T16 fixture (g): a staged teen erased in a parent's household is shared. The
// household keeps its name and address, the teen's person is anonymized, and
// the report counts the household as kept.
func TestErasureHouseholdFixtureGStagedTeenInAParentsHousehold(t *testing.T) {
	f := newStagedFixture(t, true)
	before := hrHouseholdRow(t, f.s.app.DB, f.hid)
	rep := hrReport(t, deleteAccount(f.s.ac, f.teen.ID, nil))
	after := hrHouseholdRow(t, f.s.app.DB, f.hid)
	if after.Name != before.Name || after.Address != before.Address || rep.HouseholdsKept != 1 || rep.HouseholdsScrubbed != 0 {
		t.Errorf("household %+v -> %+v, report %+v, want it retained and counted", before, after, rep)
	}
	hrAssertAnonymized(t, f.s.app.DB, f.person, nil)
}

// hlChangedWhileBusy erases the victim while a test transaction holds household
// hid, the way a writer that has not committed does. The transaction adds what
// mutate gives it, the refused first attempt rolls back, the transaction
// commits, and the second attempt reads the committed rows under the lock.
func hlChangedWhileBusy(t *testing.T, s *hlScene, hid uint, mutate func(tx *gorm.DB)) erasureReportJSON {
	t.Helper()
	setHouseholdRetry(t, time.Minute, [2]time.Duration{25 * time.Millisecond, 100 * time.Millisecond})
	states := erasureTestSQLStates(t, s.app)
	hold := holdHousehold(t, s.app.DB, hid)
	if mutate != nil {
		mutate(hold)
	}
	reached, release := erasureTestPause(t, s.app.DB, erasureTestPoint{Processor: "row", Step: "gorm:row",
		SQLContains: "FOR UPDATE NOWAIT", VarAt: map[int]any{0: hid}})
	del := s.erase()
	awaitDBSignal(t, reached)
	q := queueForGate(t, s.app)
	waitContactWaiters(t, s.app, 1)
	release()
	awaitStates(t, states, "55P03", 1)
	q.acquired(t)
	if err := hold.Commit().Error; err != nil {
		t.Fatal(err)
	}
	q.release()
	rep := hrReport(t, awaitResult(t, del))
	if n := states.Count("55P03"); n != 1 {
		t.Errorf("%d busy answers, want exactly the first attempt's", n)
	}
	if n := states.Count("40P01"); n != 0 {
		t.Errorf("%d deadlocks recorded", n)
	}
	if !s.victimDeleted(t) {
		t.Fatal("the second attempt did not commit")
	}
	return rep
}

// T16 fixture (h): another request adds a person or a seat to the household
// while the erasure is refused on it. The sole-subject test is read after the
// lock is finally taken, so what the other request committed keeps the
// household; with nothing added it is scrubbed.
func TestErasureHouseholdFixtureHPredicateIsReadAfterTheLock(t *testing.T) {
	newSolo := func(t *testing.T) (*hlScene, uint) {
		s := newHLSceneSeat(t, models.RoleMember, false)
		hid := hlHousehold(t, s, "Solo household")
		erasureMustCreate(t, s.app.DB, &models.HouseholdAccount{HouseholdID: hid, UserID: s.victim.ID, Relation: models.HouseholdRelationAdult})
		return s, hid
	}
	t.Run("nothing added: scrubbed", func(t *testing.T) {
		s, hid := newSolo(t)
		rep := hlChangedWhileBusy(t, s, hid, nil)
		hbScrubbed(t, hrHouseholdRow(t, s.app.DB, hid))
		if rep.HouseholdsScrubbed != 1 || rep.HouseholdsKept != 0 {
			t.Errorf("report %+v", rep)
		}
	})
	t.Run("a person added while the erasure waited", func(t *testing.T) {
		s, hid := newSolo(t)
		var added uint
		rep := hlChangedWhileBusy(t, s, hid, func(tx *gorm.DB) {
			p := models.HouseholdPerson{HouseholdID: hid, FirstName: "Latecomer", Kind: models.HouseholdAdult, CreatedBy: s.holder.ID}
			if err := tx.Create(&p).Error; err != nil {
				t.Fatal(err)
			}
			added = p.ID
		})
		if h := hrHouseholdRow(t, s.app.DB, hid); h.Name != "Solo household" || rep.HouseholdsKept != 1 || rep.HouseholdsScrubbed != 0 {
			t.Errorf("household %+v report %+v, want it retained and counted", h, rep)
		}
		if n := countRows(t, s.app.DB, "household_people", "id = ? AND first_name = 'Latecomer' AND deleted_at IS NULL", added); n != 1 {
			t.Error("the person the other request added was touched")
		}
	})
	t.Run("a seat added while the erasure waited", func(t *testing.T) {
		s, hid := newSolo(t)
		v := newTestUser(t, s.app, "hl-late-seat", erasurePW, models.RoleMember)
		rep := hlChangedWhileBusy(t, s, hid, func(tx *gorm.DB) {
			if err := tx.Create(&models.HouseholdAccount{HouseholdID: hid, UserID: v.ID, Relation: models.HouseholdRelationAdult}).Error; err != nil {
				t.Fatal(err)
			}
		})
		if h := hrHouseholdRow(t, s.app.DB, hid); h.Name != "Solo household" || rep.HouseholdsKept != 1 || rep.HouseholdsScrubbed != 0 {
			t.Errorf("household %+v report %+v, want it retained and counted", h, rep)
		}
		if n := countRows(t, s.app.DB, "household_accounts", "household_id = ? AND user_id = ?", hid, v.ID); n != 1 {
			t.Error("the seat the other request added was removed")
		}
	})
}

// Rows 6 and 7 of 9.6 are assertions only: an admin or conversion writer refuses
// a soft-deleted holder, inviter or actor and writes nothing.
func TestHouseholdAdminWritersRefuseAnErasedHolderOrActor(t *testing.T) {
	s := newCoreErasureScene(t)
	s.ac.holdNotices = true
	holder := newTestUser(t, s.app, "hr6-holder", erasurePW, models.RoleMember)
	apiDo(t, s.ac, "POST", "/api/v1/admin/users/"+itoa(holder.ID)+"/household", map[string]any{"name": "Admin made"}, http.StatusCreated)
	s.erase(t, holder.ID)
	before := hlSnapshot(t, s.app.DB)
	for _, req := range []struct {
		name, path string
		body       any
	}{
		{"create household", "/api/v1/admin/users/" + itoa(holder.ID) + "/household", map[string]any{"name": "Again"}},
		{"add person", "/api/v1/admin/users/" + itoa(holder.ID) + "/household/people", map[string]any{"firstName": "Late", "kind": "adult"}},
	} {
		w := s.ac.do("POST", req.path, req.body, true)
		if w.Code != http.StatusNotFound && w.Code != http.StatusConflict {
			t.Errorf("%s for an erased holder: %d %s, want a refusal", req.name, w.Code, w.Body.String())
		}
	}
	if after := hlSnapshot(t, s.app.DB); after != before {
		t.Error("a refused admin writer changed users, audit, notices, jobs or household rows")
	}

	// An erased admin's old session is the actor.
	live := newTestUser(t, s.app, "hr6-live", erasurePW, models.RoleMember)
	other := loginAPI(t, s.app, "erase-second", erasurePW)
	s.erase(t, s.second.ID)
	before = hlSnapshot(t, s.app.DB)
	w := other.do("POST", "/api/v1/admin/users/"+itoa(live.ID)+"/household", map[string]any{"name": "By a ghost"}, true)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("an erased admin's session answered %d %s, want 401", w.Code, w.Body.String())
	}
	if after := hlSnapshot(t, s.app.DB); after != before {
		t.Error("an erased actor's request changed rows")
	}
}

func TestHouseholdConversionWritersRefuseAnErasedInviterOrActor(t *testing.T) {
	// An invitation the holder issued; the holder is erased before it is used.
	t.Run("inviter and holder erased before the invitation is used", func(t *testing.T) {
		s := newCoreErasureScene(t)
		s.ac.holdNotices = true
		holder, c, person, _ := householdFamily(t, s.app, s.ac, "hr7-parent")
		token := inviteHouseholdPerson(t, s.app, c, person.ID, "hr7-invitee@example.com")
		s.erase(t, holder.ID)
		anon := newAPIClient(t, s.app)
		anon.fetchCSRF()
		w := anon.do("POST", "/api/v1/invitations/accept", map[string]any{"ageBand": "adult", "token": token, "password": "password1234"}, true)
		if w.Code != http.StatusBadRequest && w.Code != http.StatusConflict {
			t.Errorf("accepting an erased inviter's invitation: %d %s, want a refusal", w.Code, w.Body.String())
		}
		if n := countRows(t, s.app.DB, "users", "email = ?", "hr7-invitee@example.com"); n != 0 {
			t.Errorf("%d accounts were made from the erased inviter's invitation", n)
		}
		if n := countRows(t, s.app.DB, "household_people", "id = ? AND converted_user_id IS NOT NULL", person.ID); n != 0 {
			t.Error("the person was converted")
		}
	})
	// A request already past the middleware when the actor was erased.
	t.Run("actor erased while the request waited", func(t *testing.T) {
		s := newHWScene(t, true)
		before := hwState(t, s.app.DB) + dumpRows(t, s.app.DB, "invitations", "id", "true") + dumpRows(t, s.app.DB, "jobs", "id", "true")
		blocker, commit := hwHoldUser(t, s.app, s.u.ID, "session_version = session_version + 1, deleted_at = now()")
		res := bgDo(s.c, "POST", conversionPath(s.pid), map[string]any{"email": "hr7-actor@example.com"})
		waitDBBlocked(t, s.app.DB, hwWaiters(t, s.app, 1)[0], blocker)
		commit()
		got := awaitResult(t, res)
		if got.code < 400 || got.code >= 500 {
			t.Fatalf("issue conversion by an erased actor: %d %s, want a refusal", got.code, got.body)
		}
		if after := hwState(t, s.app.DB) + dumpRows(t, s.app.DB, "invitations", "id", "true") + dumpRows(t, s.app.DB, "jobs", "id", "true"); after != before {
			t.Error("a refused conversion changed household, invitation, job, audit or notice rows")
		}
	})
}

// ---------------------------------------------------------------------------
// R4-3b-LOCK-UPGRADE (9.6): the erasure of an account with a staged person
// against occurrence writers, both orders, with SQLSTATE evidence.
//
// Trace (read from source, then run below). The erasure takes the contact gate,
// the user rows, then each household NOWAIT. The household step locks the staged
// person's occurrences ascending inside reconcileHouseholdPerson, under the
// household, and promoteOnDisable locks its own ascending set later. Writers:
//
//   - A household writer holds the household first and then occurrences
//     ascending (apiDeleteHouseholdPerson, the household registration route).
//     Writer first, the erasure is refused on the household (55P03), rolls back
//     and runs again after the writer committed; erasure first, the writer waits
//     on the household and holds nothing the erasure wants. Neither order has a
//     cycle (TestHouseholdStagedErasureAgainstAHouseholdWriter).
//   - A gated writer (apiRegisterSeries, the multi-occurrence writer) takes the
//     contact gate before its actor, event and occurrence rows, and the erasure
//     takes the same gate first, so the two never hold locks at the same time:
//     whichever comes second waits on the gate holding nothing
//     (TestHouseholdStagedErasureAgainstASeriesWriter).
//   - A writer that holds an occurrence without the gate or the household and then
//     asks for the erased person's row (a promotion notice) closes a D4-class cycle
//     with the erasure's ascending occurrence run; it is the one cycle left, the
//     database breaks it with one 40P01, the loser rolls back whole and nothing
//     retries it (TestHouseholdStagedErasureMeetsOccurrenceWriters).
// ---------------------------------------------------------------------------

// hlStagedSecondDate gives the scene's victim a staged person in H with a going
// registration on each of two dates (two groups, ascending occurrence ids),
// registered by the living holder. It returns the person and the two dates.
func hlStagedSecondDate(t *testing.T, s *hlScene) (person uint, first, second models.EventOccurrence) {
	t.Helper()
	first = hlEvent(t, s, 0)
	g := newTestGroup(t, s.app, s.admin, "Household Lock Club Two", models.GroupActive)
	_, second = seedEvent(t, s.app, g, s.admin.ID, hlFuture.Add(48*time.Hour))
	p := models.HouseholdPerson{HouseholdID: s.hid, FirstName: "Staged", LastName: "Person", Kind: models.HouseholdAdult, CreatedBy: s.holder.ID}
	erasureMustCreate(t, s.app.DB, &p)
	erasureRequest(t, s.app.DB, "hl-staged@x.test", models.RequestHouseholdConversion, models.RequestApproved, &s.victim.ID, &p.ID, nil)
	for _, occ := range []models.EventOccurrence{first, second} {
		erasureMustCreate(t, s.app.DB, &models.Registration{OccurrenceID: occ.ID, HouseholdPersonID: &p.ID, RegisteredBy: &s.holder.ID,
			Status: models.RegGoing, Source: models.RegSourceWeb, AttendanceMode: models.AttendanceInPerson,
			ManageTokenHash: "hl-staged-token-" + itoa(occ.ID), RemovalNote: "note"})
	}
	return p.ID, first, second
}

func hlAssertStagedGone(t *testing.T, s *hlScene, person uint) {
	t.Helper()
	hrAssertAnonymized(t, s.app.DB, person, nil)
	if n := countRows(t, s.app.DB, "registrations", "household_person_id = ? AND status = ? AND user_id IS NULL AND manage_token_hash = '' AND removal_note = '' AND registered_by = ?",
		person, models.RegCancelled, s.holder.ID); n != 2 {
		t.Errorf("%d of the staged person's two registrations are cancelled with their token and note cleared and the holder kept, want 2", n)
	}
}

// A household writer against the erasure of an account with a staged person on
// two dates, both orders (the harness of R4-3b-1): the writer cancels Ada's
// registration on the first date under the household.
func TestHouseholdStagedErasureAgainstAHouseholdWriter(t *testing.T) {
	var ada, staged uint
	runHouseholdWriterOrders(t, hlWriter{
		name: "DELETE person", victimRole: models.RoleMember, status: http.StatusOK, audit: audit.HouseholdPersonRemoved,
		prepare: func(t *testing.T, s *hlScene) {
			ada = addMyPerson(t, s.hc, map[string]any{"firstName": "Ada", "kind": "adult"}).ID
			var first models.EventOccurrence
			staged, first, _ = hlStagedSecondDate(t, s)
			erasureMustCreate(t, s.app.DB, &models.Registration{OccurrenceID: first.ID, HouseholdPersonID: &ada, RegisteredBy: &s.holder.ID,
				Status: models.RegGoing, Source: models.RegSourceWeb, AttendanceMode: models.AttendanceInPerson})
		},
		start: func(s *hlScene) <-chan httpResult { return bgDo(s.hc, "DELETE", hlPersonPath(ada), nil) },
		pause: func(*hlScene) erasureTestPoint {
			return erasureTestPoint{Processor: "update", Step: "gorm:update", Table: "registrations"}
		},
		check: func(t *testing.T, s *hlScene, _ bool) {
			hlAssertStagedGone(t, s, staged)
			if n := countRows(t, s.app.DB, "household_people", "id = ? AND deleted_at IS NOT NULL", ada); n != 1 {
				t.Error("the writer's removal of Ada is gone")
			}
			if n := countRows(t, s.app.DB, "registrations", "household_person_id = ? AND status = ?", ada, models.RegCancelled); n != 1 {
				t.Errorf("%d cancelled registrations for Ada, want 1", n)
			}
			if h := hrHouseholdRow(t, s.app.DB, s.hid); h.Name != "Holders" {
				t.Errorf("household %+v, want it retained (the holder's seat and Ada remain)", h)
			}
		},
	})
}

// The multi-occurrence writer (apiRegisterSeries) is gated: it and the erasure
// serialize on the contact gate, so neither order can close a cycle.
func TestHouseholdStagedErasureAgainstASeriesWriter(t *testing.T) {
	type seriesScene struct {
		*hlScene
		staged uint
		event  models.Event
		dates  []models.EventOccurrence
	}
	newScene := func(t *testing.T) seriesScene {
		s := newHLScene(t, models.RoleMember)
		g := newTestGroup(t, s.app, s.admin, "Series Lock Club", models.GroupActive)
		addMember(t, s.app, s.holder, g, models.MembershipMember)
		ev, occs := seedSeriesEvent(t, s.app, g, s.admin.ID, nil, false, 2)
		p := models.HouseholdPerson{HouseholdID: s.hid, FirstName: "Staged", LastName: "Person", Kind: models.HouseholdAdult, CreatedBy: s.holder.ID}
		erasureMustCreate(t, s.app.DB, &p)
		erasureRequest(t, s.app.DB, "hl-series-staged@x.test", models.RequestHouseholdConversion, models.RequestApproved, &s.victim.ID, &p.ID, nil)
		for _, occ := range occs {
			erasureMustCreate(t, s.app.DB, &models.Registration{OccurrenceID: occ.ID, HouseholdPersonID: &p.ID, RegisteredBy: &s.holder.ID,
				Status: models.RegGoing, Source: models.RegSourceWeb, AttendanceMode: models.AttendanceInPerson,
				ManageTokenHash: "hl-series-token-" + itoa(occ.ID), RemovalNote: "note"})
		}
		return seriesScene{hlScene: s, staged: p.ID, event: ev, dates: occs}
	}
	start := func(s seriesScene) <-chan httpResult {
		return bgDo(s.hc, "POST", fmt.Sprintf("/api/v1/events/%d/series-registrations", s.event.ID),
			map[string]any{"scope": seriesScopeAllFuture, "status": models.RegMaybe})
	}
	finish := func(t *testing.T, s seriesScene, states *erasureTestStateLog, res, del <-chan httpResult) {
		t.Helper()
		if got := awaitResult(t, res); got.code != http.StatusOK {
			t.Fatalf("series answer: %d %s, want 200", got.code, got.body)
		}
		if got := awaitResult(t, del); got.code != http.StatusOK {
			t.Fatalf("erasure: %d %s, want 200", got.code, got.body)
		}
		if n := states.Count("40P01") + states.Count("55P03"); n != 0 {
			t.Errorf("%d deadlocks or busy answers recorded: the gate serializes the two", n)
		}
		if !s.victimDeleted(t) {
			t.Fatal("the erasure did not commit")
		}
		hlAssertStagedGone(t, s.hlScene, s.staged)
		if n := countRows(t, s.app.DB, "registrations", "user_id = ? AND occurrence_id IN ? AND status = ?", s.holder.ID,
			[]uint{s.dates[0].ID, s.dates[1].ID}, models.RegMaybe); n != 2 {
			t.Errorf("%d of the writer's answers on the two dates, want 2", n)
		}
	}
	t.Run("writer first", func(t *testing.T) {
		s := newScene(t)
		states := erasureTestSQLStates(t, s.app)
		reached, release := erasureTestPause(t, s.app.DB, erasureTestPoint{Processor: "create", Step: "gorm:create", Table: "registrations"})
		res := start(s)
		awaitDBSignal(t, reached) // the writer holds the gate and both dates
		del := s.erase()
		waitContactWaiters(t, s.app, 1)
		release()
		finish(t, s, states, res, del)
	})
	t.Run("erasure first", func(t *testing.T) {
		s := newScene(t)
		states := erasureTestSQLStates(t, s.app)
		reached, release := erasureTestPause(t, s.app.DB, erasureTestPoint{Processor: "raw", Step: "gorm:raw", SQLContains: "DELETE FROM login_throttles"})
		del := s.erase()
		awaitDBSignal(t, reached) // the household step has run; the erasure holds the gate and both dates
		res := start(s)
		waitContactWaiters(t, s.app, 1)
		release()
		finish(t, s, states, res, del)
	})
}
