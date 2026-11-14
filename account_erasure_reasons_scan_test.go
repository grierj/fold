package main

// Database-backed tests for the batched live-page scanner
// (account_erasure_reasons_scan.go). The scanner's own rules (signed ids, the
// 100th request, empty/cap/unit, bounds, drain and failure accounting, change
// between batches) are tested without a database in internal/reasonscan; these
// tests check the wiring against Postgres, the HTTP route, the actor seal and
// the deletion.

import (
	"database/sql"
	"fmt"
	"math"
	"net/http"
	"reflect"
	"regexp"
	"sort"
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
	// The explicit positive boundary must not collide with later generated IDs.
	if err := s.app.DB.Exec("SELECT setval(pg_get_serial_sequence('contact_requests', 'id'), 1000, true)").Error; err != nil {
		t.Fatal(err)
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
	s.app.hooks.readBeforeSeal = func(*gorm.DB) { seals++ }
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
	scans := scannerStatements(statements())
	if seals != busyRetryTries || len(scans)%busyRetryTries != 0 {
		t.Fatalf("%d attempts reached the seal, %d scanner statements; want %d equal attempts", seals, len(scans), busyRetryTries)
	}
	per := len(scans) / busyRetryTries
	total := 0
	for i, st := range scans {
		got := limitOf(t, st)
		if same := limitOf(t, scans[i%per]); got != same {
			t.Errorf("statement %d has LIMIT %d, but the same step of the first attempt had %d", i, got, same)
		}
		if i%per == 0 && got != 2 {
			t.Errorf("attempt %d starts with LIMIT %d, want the first batch (2)", i/per, got)
		}
		total += got
	}
	if total != busyRetryTries*1000 {
		t.Errorf("LIMITs sum to %d, want %d (three units)", total, busyRetryTries*1000)
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
	s.app.hooks.readBeforeSeal = func(*gorm.DB) {
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

// scanReasonItems reads one unit: at most reasonUnitRows rows by primary key,
// filtered in Go, stopping right after the pageRequests-th qualifying
// request, so a request's parties are returned together or not at all
// (R4-C01). next is that request's id; with fewer qualifying requests it is
// the last id read when the unit read its whole budget, else nil. No statement
// scans the table: the first page has no lower bound but still reads at most
// reasonUnitRows rows. It is the tests' oracle for scanReasonItemsBatched: one statement
// with LIMIT reasonUnitRows, the shape the live page used before batching.
func scanReasonItems(tx *gorm.DB, filter reasonFilter, after *int64, pageRequests int) (reasonUnit, error) {
	var unit reasonUnit
	var rows *sql.Rows
	var err error
	if after == nil {
		rows, err = tx.Raw(reasonScanInitialSQL, reasonUnitRows).Rows()
	} else {
		rows, err = tx.Raw(reasonScanContinuationSQL, *after, reasonUnitRows).Rows()
	}
	if err != nil {
		return unit, err
	}
	defer rows.Close()
	read := 0
	var last int64
	for rows.Next() {
		var s reasonSource
		if err := rows.Scan(&s.ID, &s.RequesterID, &s.SubjectUserID, &s.HasReason); err != nil {
			return unit, err
		}
		read++
		last = s.ID
		if !filter(s) {
			continue
		}
		unit.Requests = append(unit.Requests, s.ID)
		if len(unit.Requests) == pageRequests {
			next := s.ID
			unit.Next = &next
			return unit, rows.Err()
		}
	}
	if err := rows.Err(); err != nil {
		return unit, err
	}
	if read >= reasonUnitRows {
		unit.Next = &last
	}
	return unit, nil
}

// The marker lookup's fixture, in ids: add_request decisions on 1..12000
// (kept and removed alternating), contact_request decisions on 20001..23000,
// each for party 1000+id%40 with the application's seven meta keys; a viewed
// audit (right party and version, wrong action) on every tenth add request;
// and 6,000 audit rows of other kinds. Ids 12001..20000 and 23001.. have no
// audit row at all.
const (
	markerAddMax        = 12000
	markerContactFirst  = 20001
	markerContactLast   = 23000
	markerNoiseRows     = 6000
	markerBatch         = 2048
	markerPlanRepeats   = 10
	markerNoAddFirst    = 12001
	markerNoContactBase = 23001
)

func markerParty(id int64) int64 { return 1000 + id%40 }

// markerDecided is the fixture's truth, written from its construction and not
// from the selector: a pair is decided when its request has a decision of the
// candidate's own kind for that party.
func markerDecided(c legacyCandidate) bool {
	if c.kind == requestKindOf(policy.RequestContact) {
		return c.Request >= markerContactFirst && c.Request <= markerContactLast && c.Party == markerParty(c.Request)
	}
	return c.Request >= 1 && c.Request <= markerAddMax && c.Party == markerParty(c.Request)
}

func seedMarkerFixture(t *testing.T, db *gorm.DB) {
	t.Helper()
	add, contact := requestKindOf(policy.RequestGroupAdd), requestKindOf(policy.RequestContact)
	meta := func(purpose string) string {
		return fmt.Sprintf(`jsonb_build_object('erasedUserId', 1000 + i %% 40, 'erasedRole', 'requester', 'purpose', '%s', 'status', 'withdrawn',
			'reviewerIsParty', false, 'source', 'legacy_review', 'reviewVersion', 1)`, purpose)
	}
	stmts := []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO audit_logs (actor_id, action, target_type, target_id, meta_json, ip)
SELECT NULL, CASE WHEN i % 2 = 0 THEN ? ELSE ? END, ?, i, ` + meta("group_add") + `, '' FROM generate_series(1, ?::int) i`,
			[]any{add.reasonKept, add.reasonRemoved, add.target, markerAddMax}},
		{`INSERT INTO audit_logs (actor_id, action, target_type, target_id, meta_json, ip)
SELECT NULL, CASE WHEN i % 2 = 0 THEN ? ELSE ? END, ?, i, ` + meta("contact") + `, '' FROM generate_series(?::int, ?::int) i`,
			[]any{contact.reasonKept, contact.reasonRemoved, contact.target, markerContactFirst, markerContactLast}},
		{`INSERT INTO audit_logs (actor_id, action, target_type, target_id, meta_json, ip)
SELECT NULL, ?, ?, i, ` + meta("group_add") + `, '' FROM generate_series(10, ?::int, 10) i`,
			[]any{audit.UserErasureReasonsViewed, add.target, markerAddMax}},
		{`INSERT INTO audit_logs (actor_id, action, target_type, target_id, meta_json, ip)
SELECT NULL, CASE WHEN i % 3 = 0 THEN 'user.login' WHEN i % 3 = 1 THEN 'group.update' ELSE 'event.update' END,
       CASE WHEN i % 3 = 0 THEN 'user' WHEN i % 3 = 1 THEN 'group' ELSE 'event' END, i % 700 + 1,
       jsonb_build_object('note', 'x', 'n', i), '203.0.113.7' FROM generate_series(1, ?::int) i`,
			[]any{markerNoiseRows}},
	}
	for _, st := range stmts {
		if err := db.Exec(st.sql, st.args...).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Exec("ANALYZE audit_logs").Error; err != nil {
		t.Fatal(err)
	}
}

// markerBatches are two 2,048-candidate batches, every pair distinct: one
// where every pair is decided (the QC decided arm), and one in the production
// mix: both kinds, half decided, the rest with no audit row, another party's
// decision, or the other kind's decision.
func markerBatches() (decided, mixed []legacyCandidate) {
	add, contact := requestKindOf(policy.RequestGroupAdd), requestKindOf(policy.RequestContact)
	for i := int64(0); i < markerBatch; i++ {
		id := 1 + i*5
		decided = append(decided, legacyCandidate{legacyPair{id, markerParty(id)}, add})
	}
	for i := int64(0); i < markerBatch/8; i++ {
		a, c := 1+i*40, markerContactFirst+i*10
		mixed = append(mixed,
			legacyCandidate{legacyPair{a, markerParty(a)}, add},
			legacyCandidate{legacyPair{c + 1, markerParty(c + 1)}, contact},
			legacyCandidate{legacyPair{a + 2, markerParty(a + 2)}, add},
			legacyCandidate{legacyPair{c + 3, markerParty(c + 3)}, contact},
			legacyCandidate{legacyPair{markerNoAddFirst + i, 1000}, add},
			legacyCandidate{legacyPair{markerNoContactBase + i, 1000}, contact},
			legacyCandidate{legacyPair{a + 4, markerParty(a+4) + 1}, add},
			legacyCandidate{legacyPair{c + 5, markerParty(c+5) + 1}, contact},
		)
	}
	// The other kind's decision: a contact candidate on an add decision's id and
	// an add candidate on a contact decision's id decide nothing.
	for i := int64(0); i < markerBatch/16; i++ {
		a, c := 1+i*40+6, markerContactFirst+i*10+7
		mixed[i*8+4] = legacyCandidate{legacyPair{a, markerParty(a)}, contact}
		mixed[i*8+5] = legacyCandidate{legacyPair{c, markerParty(c)}, add}
	}
	return decided, mixed
}

func markerArgs(cands []legacyCandidate) string {
	reqs, parties := make([]int64, len(cands)), make([]int64, len(cands))
	targets, kept, removed := make([]string, len(cands)), make([]string, len(cands)), make([]string, len(cands))
	for i, c := range cands {
		reqs[i], parties[i] = c.Request, c.Party
		targets[i], kept[i], removed[i] = c.kind.target, c.kind.reasonKept, c.kind.reasonRemoved
	}
	return fmt.Sprintf("'%s'::bigint[], '%s'::bigint[], '%s'::text[], '%s'::text[], '%s'::text[]",
		pgBigintArray(reqs), pgBigintArray(parties), pgTextArray(targets), pgTextArray(kept), pgTextArray(removed))
}

var (
	planAuditNode = regexp.MustCompile(`(?m)^.*(?:Seq Scan|Index Scan|Index Only Scan|Bitmap Heap Scan|Bitmap Index Scan) .*audit_logs.*$`)
	planAuditLoop = regexp.MustCompile(`Index Scan using idx_audit_logs_target on audit_logs \S+\s+\(cost=[^)]*\) \(actual time=[\d.]+\.\.[\d.]+ rows=\d+ loops=(\d+)\)`)
)

// markerPlanProblem says what is wrong with one marker-lookup plan, or "" when
// audit_logs is reached only by one probe per candidate through
// idx_audit_logs_target on both its columns: no sequential or bitmap scan, no
// join that builds one side from audit_logs, no Materialize or Gather over it.
// The plan's own text is the report; nothing here is a time.
func markerPlanProblem(plan string, candidates int) string {
	for _, bad := range []string{"Seq Scan", "Bitmap", "Materialize", "Gather", "Hash Join", "Merge Join", "Hash Semi Join", "Merge Semi Join"} {
		if strings.Contains(plan, bad) {
			return "plan contains " + bad
		}
	}
	nodes := planAuditNode.FindAllString(plan, -1)
	if len(nodes) != 1 || !strings.Contains(nodes[0], "Index Scan using idx_audit_logs_target on audit_logs") {
		return fmt.Sprintf("audit_logs is reached by %d node(s), want one Index Scan using idx_audit_logs_target", len(nodes))
	}
	if !strings.Contains(plan, "Index Cond: ((target_type = ") || !strings.Contains(plan, "AND (target_id = ") {
		return "the index is not used on both target_type and target_id"
	}
	m := planAuditLoop.FindStringSubmatch(plan)
	if m == nil {
		return "no executed index scan on audit_logs"
	}
	if loops, _ := strconv.Atoi(m[1]); loops != candidates {
		return fmt.Sprintf("the index was probed %d times for %d candidates", loops, candidates)
	}
	return ""
}

// The marker lookup is planned one candidate at a time through
// idx_audit_logs_target whatever the plan cache decides: a custom plan, a
// generic plan, and the driver's automatic choice over ten executions of one
// prepared statement on one connection. Default planner settings, a table big
// enough that a sequential scan is the cheaper path for a batch of
// semi-joined candidates with no statistics on the jsonb predicates, and no
// timing: the statement is read, executed and counted, and its rows are the
// fixture's truth. The old EXISTS form turned into a Parallel Seq Scan with
// Materialize and a join filter that costs candidates x marker rows.
func TestLegacyMarkerLookupUsesTargetIndexInEveryPlanMode(t *testing.T) {
	s := newReasonScene(t)
	seedMarkerFixture(t, s.app.DB)
	decided, mixed := markerBatches()
	batches := []struct {
		name  string
		cands []legacyCandidate
	}{{"every pair decided", decided}, {"mixed, half decided", mixed}}
	for _, b := range batches {
		n := 0
		for _, c := range b.cands {
			if markerDecided(c) {
				n++
			}
		}
		if want := map[string]int{"every pair decided": markerBatch, "mixed, half decided": markerBatch / 2}[b.name]; n != want {
			t.Fatalf("%s: the fixture decides %d of %d, want %d", b.name, n, len(b.cands), want)
		}
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
		t.Run(mode, func(t *testing.T) {
			var counters string
			err := s.app.DB.Transaction(func(tx *gorm.DB) error {
				if err := tx.Exec("SET LOCAL plan_cache_mode = " + mode).Error; err != nil {
					return err
				}
				var settings string
				if err := tx.Raw("SELECT current_setting('plan_cache_mode') || ' ' || current_setting('enable_seqscan') || ' ' || current_setting('enable_bitmapscan') || ' ' || current_setting('enable_indexscan')").Scan(&settings).Error; err != nil {
					return err
				}
				if settings != mode+" on on on" {
					t.Fatalf("planner settings %q, want %s with every scan type on", settings, mode)
				}
				if err := tx.Exec("PREPARE marker_plan (bigint[], bigint[], text[], text[], text[]) AS " + dollar(legacyMarkersSQL)).Error; err != nil {
					return err
				}
				defer tx.Exec("DEALLOCATE marker_plan")
				for i := 0; i < markerPlanRepeats; i++ {
					b := batches[i%len(batches)]
					plan := explainLines(t, tx, "EXECUTE marker_plan("+markerArgs(b.cands)+")")
					if problem := markerPlanProblem(plan, len(b.cands)); problem != "" {
						t.Errorf("%s, execution %d, %s: %s\n%s", mode, i+1, b.name, problem, plan)
					}
					var got []legacyPair
					rows, err := tx.Raw("EXECUTE marker_plan(" + markerArgs(b.cands) + ")").Rows()
					if err != nil {
						return err
					}
					for rows.Next() {
						var p legacyPair
						if err := rows.Scan(&p.Request, &p.Party); err != nil {
							rows.Close()
							return err
						}
						got = append(got, p)
					}
					rows.Close()
					var want []legacyPair
					for _, c := range b.cands {
						if markerDecided(c) {
							want = append(want, c.legacyPair)
						}
					}
					sortPairs(got)
					sortPairs(want)
					if !reflect.DeepEqual(got, want) {
						t.Errorf("%s, execution %d, %s: %d pairs returned, want the fixture's %d", mode, i+1, b.name, len(got), len(want))
					}
				}
				return tx.Raw("SELECT string_agg(name || ': custom ' || custom_plans || ', generic ' || generic_plans, '; ') FROM pg_prepared_statements WHERE name = 'marker_plan'").Scan(&counters).Error
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("prepared counters after %d explained and %d executed runs: %s", markerPlanRepeats, markerPlanRepeats, counters)
		})
	}
}

func sortPairs(p []legacyPair) {
	sort.Slice(p, func(i, j int) bool {
		if p[i].Request != p[j].Request {
			return p[i].Request < p[j].Request
		}
		return p[i].Party < p[j].Party
	})
}

// legacyMarkersExistsSQL is the marker selector as it was written before the
// per-candidate probe (an EXISTS semi-join), kept as the differential oracle:
// the two forms answer every candidate the same way.
const legacyMarkersExistsSQL = `SELECT c.req, c.party
FROM unnest(?::bigint[], ?::bigint[], ?::text[], ?::text[], ?::text[]) AS c(req, party, target_type, kept, removed)
WHERE EXISTS (
	SELECT 1 FROM audit_logs a
	WHERE a.target_type = c.target_type AND a.target_id = c.req AND a.action IN (c.kept, c.removed)
	AND jsonb_typeof(a.meta_json->'reviewVersion') = 'number' AND a.meta_json->>'reviewVersion' = '1'
	AND jsonb_typeof(a.meta_json->'erasedUserId') = 'number' AND a.meta_json->>'erasedUserId' = c.party::text)`

// One mixed batch through legacyMarkersIn, against a hand-built answer: a
// marker decides exactly its own (request, party) of its own kind, however
// many other candidates, kinds, parties, duplicates and malformed markers share
// the batch, at the ends of the signed id range too. The one-candidate cases
// are TestLegacyMarkerSelector.
func TestLegacyMarkerLookupDecidesExactlyOwnPairs(t *testing.T) {
	s := newReasonScene(t)
	add, contact := requestKindOf(policy.RequestGroupAdd), requestKindOf(policy.RequestContact)
	meta := func(party int64, version string) string {
		return fmt.Sprintf(`{"erasedUserId": %d, "reviewVersion": %s, "source": "legacy_review"}`, party, version)
	}
	var cands []legacyCandidate
	want := map[legacyPair]bool{}
	next := int64(100)
	// one gives a candidate a fresh request id, optionally with markers written
	// as (kind, action, meta) rows on that id, and records whether it is decided.
	type mark struct {
		kind         *requestKind
		kept         bool
		target       string // empty: the kind's own
		actionKind   *requestKind
		party        int64
		version      string
		rawMeta      string
		unrelatedAct string
	}
	one := func(id, party int64, kind *requestKind, decided bool, marks ...mark) {
		t.Helper()
		for _, m := range marks {
			action := m.kind.reasonRemoved
			if m.kept {
				action = m.kind.reasonKept
			}
			if m.actionKind != nil {
				action = m.actionKind.reasonKept
			}
			if m.unrelatedAct != "" {
				action = m.unrelatedAct
			}
			target := m.kind.target
			if m.target != "" {
				target = m.target
			}
			body := m.rawMeta
			if body == "" {
				body = meta(m.party, m.version)
			}
			legacyMarkRaw(t, s.app.DB, action, target, id, body)
		}
		p := legacyPair{id, party}
		cands = append(cands, legacyCandidate{p, kind})
		if decided {
			want[p] = true
		}
	}
	fresh := func() int64 { next++; return next }
	const A, B = int64(7001), int64(7002)

	// Each purpose's own two actions decide; nothing else does.
	one(fresh(), A, contact, true, mark{kind: contact, kept: true, party: A, version: "1"})
	one(fresh(), A, contact, true, mark{kind: contact, party: A, version: "1"})
	one(fresh(), A, add, true, mark{kind: add, kept: true, party: A, version: "1"})
	one(fresh(), A, add, true, mark{kind: add, party: A, version: "1"})
	// Crossed kinds decide nothing: action, target type, or both of the other kind.
	one(fresh(), A, contact, false, mark{kind: add, kept: true, party: A, version: "1"})
	one(fresh(), A, add, false, mark{kind: contact, kept: true, party: A, version: "1"})
	one(fresh(), A, contact, false, mark{kind: contact, actionKind: add, party: A, version: "1"})
	one(fresh(), A, add, false, mark{kind: add, actionKind: contact, party: A, version: "1"})
	one(fresh(), A, contact, false, mark{kind: contact, kept: true, target: add.target, party: A, version: "1"})
	one(fresh(), A, add, false, mark{kind: add, kept: true, target: contact.target, party: A, version: "1"})
	// The right row with the wrong action: a view audit and an unrelated decision.
	one(fresh(), A, contact, false, mark{kind: contact, unrelatedAct: audit.UserErasureReasonsViewed, party: A, version: "1"})
	one(fresh(), A, contact, false, mark{kind: contact, unrelatedAct: audit.ContactDecision, party: A, version: "1"})
	// Malformed markers decide nothing and are never cast.
	for _, v := range []string{`"1"`, "1.0", "1.00", "null", "2", "true", "0", "-1", "1e1", "[1]", `{"v": 1}`} {
		one(fresh(), A, contact, false, mark{kind: contact, kept: true, party: A, version: v})
	}
	one(fresh(), A, contact, false, mark{kind: contact, kept: true, rawMeta: fmt.Sprintf(`{"erasedUserId": %d}`, A)})
	for _, who := range []string{`"7001"`, "7001.0", "7001.00", "null", "true", "[7001]", `{"id": 7001}`} {
		one(fresh(), A, contact, false, mark{kind: contact, kept: true, rawMeta: fmt.Sprintf(`{"erasedUserId": %s, "reviewVersion": 1}`, who)})
	}
	one(fresh(), A, contact, false, mark{kind: contact, kept: true, rawMeta: `{"reviewVersion": 1}`})
	one(fresh(), A, contact, false, mark{kind: contact, kept: true, rawMeta: `{}`})
	// No row at all.
	one(fresh(), A, contact, false)
	one(fresh(), A, add, false)
	// Another party's marker never decides this one, and each party of one
	// request is decided by its own marker.
	both := fresh()
	one(both, A, contact, true, mark{kind: contact, kept: true, party: A, version: "1"})
	one(both, B, contact, false)
	bothAdd := fresh()
	one(bothAdd, A, add, true, mark{kind: add, kept: true, party: A, version: "1"})
	one(bothAdd, B, add, true, mark{kind: add, party: B, version: "1"})
	one(fresh(), A, contact, false, mark{kind: contact, kept: true, party: B, version: "1"})
	// Two decisions for one pair are one answer; the same candidate twice is one answer.
	twice := fresh()
	one(twice, A, contact, true, mark{kind: contact, kept: true, party: A, version: "1"}, mark{kind: contact, party: A, version: "1"})
	cands = append(cands, cands[len(cands)-1])
	// A request that has a malformed marker and a valid one is decided by the valid one.
	one(fresh(), A, add, true, mark{kind: add, kept: true, party: A, version: `"1"`}, mark{kind: add, party: A, version: "1"})
	// The signed ends: a decision stored at these target ids and for these
	// parties decides exactly them.
	for _, id := range []int64{math.MinInt64, math.MinInt64 + 1, -1, 0, math.MaxInt64 - 1, math.MaxInt64} {
		for _, party := range []int64{math.MinInt64, -1, 0, A, math.MaxInt64} {
			one(id, party, contact, true, mark{kind: contact, kept: true, party: party, version: "1"})
		}
		// A party the stored markers do not name, and the neighbours of the
		// extreme parties, decide nothing.
		for _, party := range []int64{424242, math.MinInt64 + 1, 1, math.MaxInt64 - 1} {
			one(id, party, contact, false)
		}
	}
	one(math.MinInt64, 31337, add, false, mark{kind: contact, kept: true, party: 31337, version: "1"})

	run := func(sqlText string, in []legacyCandidate) map[legacyPair]bool {
		t.Helper()
		reqs, parties := make([]int64, len(in)), make([]int64, len(in))
		targets, kept, removed := make([]string, len(in)), make([]string, len(in)), make([]string, len(in))
		for i, c := range in {
			reqs[i], parties[i] = c.Request, c.Party
			targets[i], kept[i], removed[i] = c.kind.target, c.kind.reasonKept, c.kind.reasonRemoved
		}
		got, seen := map[legacyPair]bool{}, map[legacyPair]int{}
		var rows []struct{ Req, Party int64 }
		if err := s.app.DB.Raw(sqlText, pgBigintArray(reqs), pgBigintArray(parties), pgTextArray(targets), pgTextArray(kept), pgTextArray(removed)).Scan(&rows).Error; err != nil {
			t.Fatal(err)
		}
		for _, r := range rows {
			p := legacyPair{r.Req, r.Party}
			got[p] = true
			seen[p]++
		}
		// A candidate given twice is answered twice, never once per marker.
		asked := map[legacyPair]int{}
		for _, c := range in {
			asked[c.legacyPair]++
		}
		for p, n := range seen {
			if n != asked[p] {
				t.Errorf("pair %+v answered %d times for %d candidate rows", p, n, asked[p])
			}
		}
		return got
	}
	check := func(name string, got map[legacyPair]bool) {
		t.Helper()
		for p := range want {
			if !got[p] {
				t.Errorf("%s: %+v has a decision and is not returned", name, p)
			}
		}
		for p := range got {
			if !want[p] {
				t.Errorf("%s: %+v has no decision and is returned", name, p)
			}
		}
	}
	var got map[legacyPair]bool
	if err := s.app.DB.Transaction(func(tx *gorm.DB) error {
		var err error
		got, err = legacyMarkersIn(tx, cands)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	check("legacyMarkersIn", got)
	check("the statement itself, one row per candidate row", run(legacyMarkersSQL, cands))
	check("the EXISTS form", run(legacyMarkersExistsSQL, cands))
	if len(want) == 0 || len(want) == len(cands) {
		t.Fatalf("the batch decides %d of %d candidates: it must be mixed", len(want), len(cands))
	}

	t.Run("the 2,048-candidate batches of the plan test", func(t *testing.T) {
		seedMarkerFixture(t, s.app.DB)
		decided, mixed := markerBatches()
		for name, batch := range map[string][]legacyCandidate{"every pair decided": decided, "mixed": mixed} {
			wantSet := map[legacyPair]bool{}
			for _, c := range batch {
				if markerDecided(c) {
					wantSet[c.legacyPair] = true
				}
			}
			var got map[legacyPair]bool
			if err := s.app.DB.Transaction(func(tx *gorm.DB) error {
				var err error
				got, err = legacyMarkersIn(tx, batch)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, wantSet) {
				t.Errorf("%s: %d pairs returned, the fixture decides %d", name, len(got), len(wantSet))
			}
		}
	})
}
