package main

// Database-backed tests for the batched live-page scanner
// (account_erasure_reasons_scan.go). The scanner's own rules (signed ids, the
// 100th request, empty/cap/unit, bounds, drain and failure accounting, change
// between batches) are tested without a database in internal/reasonscan; these
// tests check the wiring against Postgres, the HTTP route, the actor seal and
// the deletion.

import (
	"fmt"
	"math"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"fold/internal/audit"
	"fold/internal/policy"
	"fold/internal/reasonscan"
	"fold/internal/testdb"
	"fold/models"

	"gorm.io/gorm"
)

// setScanBounds shortens the scanner's bounds for one test and restores them,
// and the hooks, when it ends. Tests that call it stay serial.
func setScanBounds(t *testing.T, unit, first, pageRequests int) {
	t.Helper()
	pu, pf, pp, ph := reasonUnitRows, reasonScanBatchFirst, reasonPageRequests, reasonScanHooks
	reasonUnitRows, reasonScanBatchFirst, reasonPageRequests = unit, first, pageRequests
	t.Cleanup(func() {
		reasonUnitRows, reasonScanBatchFirst, reasonPageRequests, reasonScanHooks = pu, pf, pp, ph
	})
}

// scannerStatements are the recorded statements that are scanner batches.
func scannerStatements(all []reasonStatement) []reasonStatement {
	var out []reasonStatement
	for _, st := range all {
		if strings.HasPrefix(st.SQL, reasonUnitPrefix) {
			out = append(out, st)
		}
	}
	return out
}

// limitOf is the LIMIT a recorded scanner statement carries (its last bind).
func limitOf(t *testing.T, st reasonStatement) int {
	t.Helper()
	n, err := strconv.Atoi(fmt.Sprint(st.Vars[len(st.Vars)-1]))
	if err != nil {
		t.Fatalf("limit bind %v: %v", st.Vars, err)
	}
	return n
}

// The batched scan returns what the single statement returns, on real rows with
// signed boundary ids, from no cursor and from every id, under the driver's
// automatic plan cache and a forced generic plan.
func TestBatchedScanMatchesTheSingleStatementOnPostgres(t *testing.T) {
	s := newReasonScene(t)
	boundary := []int64{math.MinInt64, -1, 0, 7, math.MaxInt64}
	for i, id := range boundary {
		r := s.contactRequest(t, 60+i)
		if err := s.app.DB.Exec("UPDATE contact_requests SET id = ? WHERE id = ?", id, r.ID).Error; err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 9; i++ {
		s.contactRequest(t, 100+i)
		s.fillerRequest(t)
	}
	var all []int64
	if err := s.app.DB.Raw("SELECT id FROM contact_requests ORDER BY id").Scan(&all).Error; err != nil {
		t.Fatal(err)
	}
	cursors := []*int64{nil}
	for i := range all {
		cursors = append(cursors, &all[i])
	}
	same := func(a, b reasonUnit) bool {
		if (a.Next == nil) != (b.Next == nil) || (a.Next != nil && *a.Next != *b.Next) {
			return false
		}
		return len(a.Requests) == len(b.Requests) && (len(a.Requests) == 0 || reflect.DeepEqual(a.Requests, b.Requests))
	}
	for _, mode := range []string{"auto", "force_generic_plan"} {
		t.Run(mode, func(t *testing.T) {
			for _, unit := range []int{3, 7, 50} {
				for _, first := range []int{1, 2, 5} {
					for _, pageCap := range []int{1, 3, 100} {
						setScanBounds(t, unit, first, pageCap)
						err := s.app.DB.Transaction(func(tx *gorm.DB) error {
							if err := tx.Exec("SET LOCAL plan_cache_mode = " + mode).Error; err != nil {
								return err
							}
							for _, after := range cursors {
								filter := liveReasonFilter(int64(s.target.ID))
								want, err := scanReasonItems(tx, filter, after, pageCap)
								if err != nil {
									return err
								}
								got, err := scanReasonItemsBatched(tx, filter, after, pageCap)
								if err != nil {
									return err
								}
								if !same(got, want) {
									c := "nil"
									if after != nil {
										c = strconv.FormatInt(*after, 10)
									}
									t.Errorf("unit %d first %d cap %d after %s: batched %+v, single %+v", unit, first, pageCap, c, got, want)
								}
							}
							return nil
						})
						if err != nil {
							t.Fatal(err)
						}
					}
				}
			}
		})
	}
}

// Plans under the DEFAULT planner settings (no enable_* switch) for the limits
// the pinned schedule sends (256, 512, the last clipped batch 17488), with the
// driver's automatic plan cache, a forced custom plan and a forced generic plan,
// on 30,000 rows. Each case is one prepared statement executed repeatedly on one
// connection; the prepared statement's custom and generic plan counters are
// recorded, and nothing asserts that a switch to the generic plan happens after
// the seventh execution (PostgreSQL decides that from costs). A case that picks a
// sequential or bitmap scan or a Sort at default settings fails: the design
// counts that as a failure of the batch schedule.
func TestBatchedScanPlansAtDefaultPlannerSettings(t *testing.T) {
	s := newReasonScene(t)
	bulkRequests(t, s.app.DB, s.fillerRequest(t).ID, 30000)
	if err := s.app.DB.Exec("ANALYZE contact_requests").Error; err != nil {
		t.Fatal(err)
	}
	var lo, hi int64
	if err := s.app.DB.Raw("SELECT min(id), max(id) FROM contact_requests").Row().Scan(&lo, &hi); err != nil {
		t.Fatal(err)
	}
	dollar := func(sql string) string {
		n := 0
		for strings.Contains(sql, "?") {
			n++
			sql = strings.Replace(sql, "?", "$"+strconv.Itoa(n), 1)
		}
		return sql
	}
	for _, mode := range []string{"auto", "force_custom_plan", "force_generic_plan"} {
		for _, limit := range []int{256, 512, 17488} {
			t.Run(fmt.Sprintf("%s/%d", mode, limit), func(t *testing.T) {
				var plans []string
				var counters string
				err := s.app.DB.Transaction(func(tx *gorm.DB) error {
					if err := tx.Exec("SET LOCAL plan_cache_mode = " + mode).Error; err != nil {
						return err
					}
					if err := tx.Exec("PREPARE reason_batch_plan (bigint, integer) AS " + dollar(reasonScanContinuationSQL)).Error; err != nil {
						return err
					}
					defer tx.Exec("DEALLOCATE reason_batch_plan")
					for i, cursor := range []int64{lo, lo, lo + (hi-lo)/2, hi - 1500, lo, lo + (hi-lo)/3, lo, lo, hi - 3000, lo} {
						p := explainLines(t, tx, fmt.Sprintf("EXECUTE reason_batch_plan(%d, %d)", cursor, limit))
						if i < 2 || i == 9 {
							plans = append(plans, p)
						}
					}
					return tx.Raw("SELECT string_agg(name || ': custom ' || custom_plans || ', generic ' || generic_plans, '; ') FROM pg_prepared_statements WHERE name = 'reason_batch_plan'").Scan(&counters).Error
				})
				if err != nil {
					t.Fatal(err)
				}
				t.Logf("prepared counters after 10 executions: %s", counters)
				for _, p := range plans {
					if !strings.Contains(p, "Index Scan using") || strings.Contains(p, "Seq Scan") || strings.Contains(p, "Bitmap") || strings.Contains(p, "Sort") ||
						strings.Contains(p, "Rows Removed by Filter") || !strings.Contains(p, "Index Cond: (id >") {
						t.Errorf("limit %d, %s, default planner settings:\n%s", limit, mode, p)
					}
					if rows := planInt(t, planIndex, p, 1); rows > limit {
						t.Errorf("the index scan returned %d rows for a batch of %d:\n%s", rows, limit, p)
					}
				}
			})
		}
	}
}

// A page that finds nothing reads the whole unit; with the actor row held, the
// page makes the busy route's three attempts. The scanner statements of the one
// request are the schedule three times: at most 3 x (statements of the schedule)
// and 3 x the unit in LIMITs, with each attempt's LIMITs equal to the schedule.
// The page time budget is one context for the whole request, retries included.
func TestReasonPageStatementBoundAcrossBusyAttempts(t *testing.T) {
	setScanBounds(t, 1000, 2, 100)
	s := newReasonScene(t)
	bulkRequests(t, s.app.DB, s.fillerRequest(t).ID, 1100) // the target has no reasons: nothing ends a scan early
	if err := s.app.DB.Exec("ANALYZE contact_requests").Error; err != nil {
		t.Fatal(err)
	}
	var seals int
	s.app.readBeforeSeal = func(*gorm.DB) { seals++ }
	hold, _ := beginOwnedTx(t, s.app.DB)
	if err := hold.Exec("SELECT id FROM users WHERE id = ? FOR NO KEY UPDATE", s.admin.ID).Error; err != nil {
		t.Fatal(err)
	}
	statements := recordContactRequestStatements(t, s.app.DB)
	code, _, body := pageOf(t, s.ac, s.target.ID, nil)
	hold.Rollback()
	if code != http.StatusServiceUnavailable || !strings.Contains(body, `"code":"reason_review_busy"`) || strings.Contains(body, `"items"`) {
		t.Fatalf("a page against a held actor row: %d %s", code, body)
	}
	schedule := reasonscan.Batches(reasonscan.Params{UnitRows: 1000, PageRequests: 100, FirstBatch: 2, Growth: 2})
	scans := scannerStatements(statements())
	if seals != reasonBusyTries || len(scans) != reasonBusyTries*len(schedule) {
		t.Fatalf("%d attempts reached the seal, %d scanner statements; want %d attempts of %d statements", seals, len(scans), reasonBusyTries, len(schedule))
	}
	total := 0
	for i, st := range scans {
		if got, want := limitOf(t, st), schedule[i%len(schedule)]; got != want {
			t.Errorf("statement %d has LIMIT %d, want %d (the schedule %v)", i, got, want, schedule)
		}
		total += limitOf(t, st)
	}
	if total != reasonBusyTries*1000 {
		t.Errorf("LIMITs sum to %d, want %d (three units)", total, reasonBusyTries*1000)
	}
	if n := countAudit(t, s.app.DB, audit.UserErasureReasonsViewed); n != 0 {
		t.Errorf("%d view audits committed by busy attempts", n)
	}
}

// Walks the live pages of the scene's target and returns the items.
func walkTarget(t *testing.T, s reasonScene) []reasonItemJSON {
	t.Helper()
	return allPages(t, s.ac, s.target.ID)
}

func idsOf(items []reasonItemJSON) []string {
	var out []string
	for _, it := range items {
		out = append(out, it.RequestID)
	}
	return out
}

// Changes committed while a page is read, and what the deletion does with the
// walk that resulted. The pages differ from a one-snapshot read in exactly the
// ways the design lists; the deletion decides on its own locked re-read.
func TestReasonPageChangesBetweenBatchesAndTheDeletion(t *testing.T) {
	keep := func(reasonItemJSON) string { return "keep" }
	// Six reasons of the target with a filler between each; the page cap is 4 and
	// the first batch 2 (batches of 2, 4, 8 rows). A change committed before batch 1
	// is read lands before the 4th request, which is in a later batch.
	build := func(t *testing.T) (reasonScene, []models.ContactRequest) {
		setScanBounds(t, 200, 2, 4)
		s := newReasonScene(t)
		var mine []models.ContactRequest
		for i := 0; i < 6; i++ {
			mine = append(mine, s.contactRequest(t, i+1))
			s.fillerRequest(t)
		}
		return s, mine
	}

	t.Run("the original 4th request is blanked before its batch is read", func(t *testing.T) {
		s, mine := build(t)
		reasonScanHooks = reasonscan.Hooks{Before: func(i int) {
			if i == 1 {
				if err := s.app.DB.Exec("UPDATE contact_requests SET reason = '' WHERE id = ?", mine[3].ID).Error; err != nil {
					t.Error(err)
				}
			}
		}}
		code, first, body := pageOf(t, s.ac, s.target.ID, nil)
		if code != http.StatusOK {
			t.Fatalf("page: %d %s", code, body)
		}
		// One snapshot would have ended the page at request 4 and then dropped it
		// (blank) at the items read: three items and the cursor at request 4. Here
		// the 4th request is the next one.
		want := []string{itoa(mine[0].ID), itoa(mine[1].ID), itoa(mine[2].ID), itoa(mine[4].ID)}
		if !reflect.DeepEqual(idsOf(first.Items), want) || first.Next == nil || *first.Next != itoa(mine[4].ID) {
			t.Fatalf("page %v next %v, want %v ending at request %d", idsOf(first.Items), first.Next, want, mine[4].ID)
		}
		reasonScanHooks = reasonscan.Hooks{}
		_, second, _ := pageOf(t, s.ac, s.target.ID, first.Next)
		walk := append(append([]reasonItemJSON{}, first.Items...), second.Items...)
		if !reflect.DeepEqual(idsOf(walk), []string{itoa(mine[0].ID), itoa(mine[1].ID), itoa(mine[2].ID), itoa(mine[4].ID), itoa(mine[5].ID)}) {
			t.Fatalf("walk %v", idsOf(walk))
		}
		// The blanked request is in neither the reviewed set nor the current set,
		// so the deletion of the walk succeeds.
		res := deleteAccount(s.ac, s.target.ID, map[string]any{"reasonReview": reviewOf(walk, keep)})
		if res.code != http.StatusOK {
			t.Fatalf("delete of a walk that skipped a blanked request: %d %s", res.code, res.body)
		}
	})

	t.Run("a request committed below the returned cursor is never shown and the deletion refuses", func(t *testing.T) {
		s, _ := build(t)
		var late models.ContactRequest
		reasonScanHooks = reasonscan.Hooks{Before: func(i int) {
			if i == 1 {
				late = s.contactRequest(t, 77)
				// An id below every row already read.
				if err := s.app.DB.Exec("UPDATE contact_requests SET id = -5 WHERE id = ?", late.ID).Error; err != nil {
					t.Error(err)
				}
			}
		}}
		_, first, _ := pageOf(t, s.ac, s.target.ID, nil)
		reasonScanHooks = reasonscan.Hooks{}
		walk := append([]reasonItemJSON{}, first.Items...)
		for after := first.Next; after != nil; {
			_, page, _ := pageOf(t, s.ac, s.target.ID, after)
			walk = append(walk, page.Items...)
			after = page.Next
		}
		for _, id := range idsOf(walk) {
			if id == "-5" {
				t.Fatal("the walk showed a request committed below its cursor")
			}
		}
		if len(walk) != 6 {
			t.Fatalf("walk of %d items %v, want the six that existed throughout", len(walk), idsOf(walk))
		}
		// The current set has seven: the reviewed set differs, so nothing is deleted.
		res := deleteAccount(s.ac, s.target.ID, map[string]any{"reasonReview": reviewOf(walk, keep)})
		if res.code != http.StatusConflict || !strings.Contains(res.body, `"code":"reason_review_changed"`) {
			t.Fatalf("delete with a reviewed set missing a current request: %d %s, want 409 reason_review_changed", res.code, res.body)
		}
		var u models.User
		if err := s.app.DB.Unscoped().First(&u, s.target.ID).Error; err != nil || u.DeletedAt.Valid {
			t.Errorf("the account was deleted by a refusal: %v", err)
		}
		if n := countAudit(t, s.app.DB, decisionActions...); n != 0 {
			t.Errorf("%d decision audits after a refusal", n)
		}
		// A new walk shows it, and that review deletes.
		fresh := walkTarget(t, s)
		if len(fresh) != 7 {
			t.Fatalf("a new walk shows %d items, want 7", len(fresh))
		}
		if res := deleteAccount(s.ac, s.target.ID, map[string]any{"reasonReview": reviewOf(fresh, keep)}); res.code != http.StatusOK {
			t.Fatalf("delete after a new walk: %d %s", res.code, res.body)
		}
	})

	t.Run("a request committed above the cursor can join the page and the walk stays whole", func(t *testing.T) {
		setScanBounds(t, 200, 2, 100)
		s := newReasonScene(t)
		for i := 0; i < 4; i++ {
			s.contactRequest(t, i+1)
			s.fillerRequest(t)
		}
		var added models.ContactRequest
		reasonScanHooks = reasonscan.Hooks{Before: func(i int) {
			if i == 1 {
				added = s.contactRequest(t, 88)
			}
		}}
		code, page, body := pageOf(t, s.ac, s.target.ID, nil)
		reasonScanHooks = reasonscan.Hooks{}
		if code != http.StatusOK || len(page.Items) != 5 || page.Items[4].RequestID != itoa(added.ID) {
			t.Fatalf("page %d %s, want the four plus the one committed during the read", code, body)
		}
		if res := deleteAccount(s.ac, s.target.ID, map[string]any{"reasonReview": reviewOf(page.Items, keep)}); res.code != http.StatusOK {
			t.Fatalf("delete: %d %s", res.code, res.body)
		}
	})
}

// A batch after the first fails: the page answers 503 reason_review_busy with no
// items, no review marker and no committed view audit, and the same cursor read
// again returns the full page.
func TestReasonPageLaterBatchFailureAndRetry(t *testing.T) {
	setScanBounds(t, 40, 2, 100)
	s := newReasonScene(t)
	for i := 0; i < 3; i++ {
		s.contactRequest(t, i+1)
		s.fillerRequest(t)
	}
	want := targetIDs(t, s.app.DB, s.target.ID)
	var batches int
	var mu sync.Mutex
	failed := false
	reasonScanHooks = reasonscan.Hooks{Before: func(i int) {
		mu.Lock()
		defer mu.Unlock()
		batches++
	}}
	name := fmt.Sprintf("test:fail_third_batch:%d", queryCounterSeq.Add(1))
	var seen int
	if err := s.app.DB.Callback().Row().Before("gorm:row").Register(name, func(tx *gorm.DB) {
		if !strings.HasPrefix(strings.Join(strings.Fields(tx.Statement.SQL.String()), " "), reasonUnitPrefix) {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		seen++
		if seen == 3 && !failed {
			failed = true
			tx.AddError(fmt.Errorf("injected batch failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.app.DB.Callback().Row().Remove(name) }()

	code, _, body := pageOf(t, s.ac, s.target.ID, nil)
	if code != http.StatusServiceUnavailable || !strings.Contains(body, `"code":"reason_review_busy"`) || strings.Contains(body, `"items"`) || strings.Contains(body, "reviewRequired") || strings.Contains(body, "identifying reason") {
		t.Fatalf("a failed later batch: %d %s, want 503 reason_review_busy and nothing else", code, body)
	}
	if n := countAudit(t, s.app.DB, audit.UserErasureReasonsViewed); n != 0 {
		t.Errorf("%d view audits after a failed batch", n)
	}
	code, page, body := pageOf(t, s.ac, s.target.ID, nil)
	if code != http.StatusOK {
		t.Fatalf("retry: %d %s", code, body)
	}
	var got []int64
	for _, it := range page.Items {
		id, _ := parseRequestID(it.RequestID)
		got = append(got, id)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("retry returned %v, want %v", got, want)
	}
	if n := countAudit(t, s.app.DB, audit.UserErasureReasonsViewed); n != 1 {
		t.Errorf("%d view audits after the retry, want 1", n)
	}
}

// Early refusals scan nothing; the one refusal that comes after the scan (a
// known-underage admin) is unchanged and leaks nothing; a seal that fails after the
// scan rolls the page back. The permission sequence is not moved.
func TestReasonPageRefusalSequenceAndSeal(t *testing.T) {
	s := newReasonScene(t)
	reason := s.contactRequest(t, 1)
	member := newTestUser(t, s.app, "page-member", erasurePW, models.RoleMember)
	_ = member
	statements := recordContactRequestStatements(t, s.app.DB)
	scans := func() int { return len(scannerStatements(statements())) }

	// A signed-in member is refused by the route before the handler.
	mc := loginAPI(t, s.app, "page-member", erasurePW)
	if code, _, body := pageOf(t, mc, s.target.ID, nil); code != http.StatusForbidden || strings.Contains(body, reason.Reason) {
		t.Errorf("a member: %d %s", code, body)
	}
	if scans() != 0 {
		t.Errorf("%d scanner statements for a member", scans())
	}
	// An admin paging themself is refused by the account rules before the scan.
	if code, _, body := pageOf(t, s.ac, s.admin.ID, nil); code != http.StatusBadRequest || strings.Contains(body, reason.Reason) {
		t.Errorf("paging yourself: %d %s", code, body)
	}
	if scans() != 0 {
		t.Errorf("%d scanner statements for a refused account rule", scans())
	}

	// A known-underage admin: the unit is scanned (the refusal needs a nonempty
	// unit), then refused with no reason text, no count and no committed audit.
	birth := time.Now().AddDate(-15, 0, 0)
	if err := s.app.DB.Model(&models.User{}).Where("id = ?", s.admin.ID).Update("birth_date", birth).Error; err != nil {
		t.Fatal(err)
	}
	code, _, body := pageOf(t, s.ac, s.target.ID, nil)
	if code != http.StatusForbidden || strings.Contains(body, reason.Reason) || strings.Contains(body, "items") || strings.Contains(body, "next") || strings.Contains(body, "count") {
		t.Errorf("a known-underage admin: %d %s", code, body)
	}
	if scans() == 0 {
		t.Error("the underage refusal did not scan: the permission check has moved before the scan, which this change must not do")
	}
	if n := countAudit(t, s.app.DB, audit.UserErasureReasonsViewed); n != 0 {
		t.Errorf("%d committed view audits after a refusal", n)
	}
	if err := s.app.DB.Model(&models.User{}).Where("id = ?", s.admin.ID).Update("birth_date", nil).Error; err != nil {
		t.Fatal(err)
	}

	// The seal fails after the scan and the view audit were written: the whole page
	// rolls back and nothing is committed.
	before := scans()
	s.app.readBeforeSeal = func(*gorm.DB) {
		if err := s.app.DB.Exec("UPDATE users SET session_version = session_version + 1 WHERE id = ?", s.admin.ID).Error; err != nil {
			t.Error(err)
		}
	}
	code, _, body = pageOf(t, s.ac, s.target.ID, nil)
	if code != http.StatusUnauthorized || strings.Contains(body, reason.Reason) {
		t.Errorf("a seal that fails: %d %s, want 401 and no text", code, body)
	}
	if scans() == before {
		t.Error("the sealed page did not scan before failing")
	}
	if n := countAudit(t, s.app.DB, audit.UserErasureReasonsViewed); n != 0 {
		t.Errorf("%d committed view audits after a failed seal", n)
	}
}

// One dense page through the fault proxy: the server's own command tag says how
// many rows the first batch sent (256), the scanner took 100, and 156 were drained.
// Then the same page with the stream cut while those rows are being drained: the
// page fails with 503 and no committed audit, the proxy saw 150 DataRow frames and no
// command tag, so the statement's total is unknown (it is not zero and not 150),
// and the same request made again succeeds.
func TestReasonPageDenseDrainAndCutStreamThroughTheProxy(t *testing.T) {
	setScanBounds(t, reasonUnitRows, reasonScanBatchFirst, reasonPageRequests)
	s := newProxiedAttemptScene(t)
	target, other := s.target("drain-target"), s.target("drain-other")
	first := erasureMakeRequest(t, s.direct, policy.RequestGroupAdd, policy.ContactWithdrawn, other.ID, target.ID, 1, 0, 0, 1, "")
	bulkRequests(t, s.direct, first.ID, 299) // 300 rows, all the target's: the 100th request is row 100 of a 256-row batch
	var mu sync.Mutex
	var events []reasonscan.BatchEvent
	reasonScanHooks = reasonscan.Hooks{After: func(e reasonscan.BatchEvent) {
		mu.Lock()
		events = append(events, e)
		mu.Unlock()
	}}
	proxy := s.rig.proxy
	initial := "FROM contact_requests ORDER BY id LIMIT"
	scanTags := func() []scanTag {
		var out []scanTag
		for _, tg := range proxy.Tags() {
			if strings.Contains(tg.SQL, initial) {
				out = append(out, scanTag{tg.Tag, tg.Rows, tg.Cut})
			}
		}
		return out
	}

	code, page, body := pageOf(t, s.c, target.ID, nil)
	if code != http.StatusOK || len(page.Items) != 100 {
		t.Fatalf("dense page: %d %s", code, body)
	}
	tags := scanTags()
	if len(tags) != 1 || tags[0].tag != "SELECT 256" || tags[0].rows != 256 || tags[0].cut {
		t.Fatalf("scanner statement results seen by the proxy: %+v, want one 'SELECT 256' with 256 DataRow frames", tags)
	}
	mu.Lock()
	if len(events) != 1 || events[0].Consumed != 100 || !events[0].Stopped || events[0].Limit != 256 || events[0].Err != nil {
		t.Fatalf("batch events %+v", events)
	}
	mu.Unlock()
	t.Logf("exact for a completed statement: the server sent 256 rows, the scanner took 100, 156 were drained")

	// The cut stream.
	events = nil
	viewsBefore := countAudit(t, s.direct, audit.UserErasureReasonsViewed)
	proxy.Arm(testdb.Faults{Match: initial, CutAfterDataRows: 150})
	code, _, body = pageOf(t, s.c, target.ID, nil)
	if code != http.StatusServiceUnavailable || !strings.Contains(body, `"code":"reason_review_busy"`) || strings.Contains(body, `"items"`) || strings.Contains(body, "identifying reason") {
		t.Fatalf("a stream cut during the drain: %d %s, want 503 reason_review_busy", code, body)
	}
	if n := countAudit(t, s.direct, audit.UserErasureReasonsViewed); n != viewsBefore {
		t.Errorf("a failed page committed %d view audits", n-viewsBefore)
	}
	tags = scanTags()
	if len(tags) != 2 || !tags[1].cut || tags[1].tag != "" || tags[1].rows != 150 {
		t.Fatalf("after the cut the proxy log is %+v; want the cut statement with no tag and 150 frames observed", tags)
	}
	mu.Lock()
	failedBatch := events[len(events)-1]
	mu.Unlock()
	if failedBatch.Err == nil || failedBatch.Consumed != 100 || !failedBatch.Stopped {
		t.Errorf("failed batch event %+v: want 100 consumed, the 100th request found, and the drain error", failedBatch)
	}
	t.Logf("a failed stream: 150 frames observed by the proxy, 100 consumed, no command tag: the total is unknown")

	// The retry returns the page.
	code, page, body = pageOf(t, s.c, target.ID, nil)
	if code != http.StatusOK || len(page.Items) != 100 {
		t.Fatalf("retry: %d %s", code, body)
	}
}

// scanTag is a scanner statement's result as the proxy saw it.
type scanTag struct {
	tag  string
	rows int
	cut  bool
}
