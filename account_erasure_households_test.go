package main

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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
		"api_admin.go:decideGuardianLink":                          true,
		"api_admin_household.go:apiAdminAddHouseholdPerson":        true,
		"api_household_conversion.go:issueHouseholdConversion":     true,
		"api_household_conversion.go:apiCancelHouseholdConversion": true,
		"api_household_conversion.go:acceptHouseholdConversion":    true,
		"api_household_conversion.go:approveHouseholdConversion":   true,
		"api_profile.go:apiUpdateMyHousehold":                      true, // N-11 family
		"api_profile.go:apiAddHouseholdPerson":                     true,
		"api_profile.go:apiUpdateHouseholdPerson":                  true,
		"api_profile.go:apiDeleteHouseholdPerson":                  true, // reconcileHouseholdPerson
		"api_registrations.go:apiRegister":                         true,
		"api_registrations.go:addHouseholdRegistration":            true,
		"api_teen_conversion.go:lockConversionParticipants":        true,
		"api_teen_conversion.go:lockTeenLifecycleUsers":            true,
	}
	got := map[string]bool{}
	var calls = map[string][]token.Pos{}
	fset := token.NewFileSet()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
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
// ascending.
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

// Several households selected together: the lowest and the highest through PERS
// alone, the middle one by the seat (and by PERS too). The held one is the
// highest, so the erasure takes the other two first and must give them back.
func TestHouseholdNoWaitSeveralHouseholdsAscending(t *testing.T) {
	s := newHLSceneSeat(t, models.RoleMember, false)
	low := s.hid
	mid := hlHousehold(t, s, "Middle")
	high := hlHousehold(t, s, "High")
	erasureMustCreate(t, s.app.DB, &models.HouseholdAccount{HouseholdID: mid, UserID: s.victim.ID, Relation: models.HouseholdRelationAdult})
	hlPersOnly(t, s, low, "A1")
	hlPersOnly(t, s, low, "A2")
	hlPersOnly(t, s, mid, "B")
	hlPersOnly(t, s, high, "C")
	hlBusyOnce(t, s, high, []uint{low, mid})
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
