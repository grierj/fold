package main

// Tests for the deleted-accounts review list (T07B-3a, slice S1): the legacy
// filter and its marker selector, request-atomic scanning against a reference
// query, and the read routes (discovery and probe) with their authorization.

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"net/http"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"fold/internal/audit"
	"fold/internal/policy"
	"fold/internal/reasonscan"
	"fold/internal/testdb"
	"fold/models"

	"gorm.io/gorm"
)

type legacyScene struct {
	reasonScene
	// users are members of the church, none deleted yet.
	users []*models.User
}

func newLegacyScene(t *testing.T, n int) legacyScene {
	t.Helper()
	s := legacyScene{reasonScene: newReasonScene(t)}
	for i := 0; i < n; i++ {
		s.users = append(s.users, newTestUser(t, s.app, fmt.Sprintf("legacy-%d", i), erasurePW, models.RoleMember))
	}
	return s
}

func (s legacyScene) bury(t *testing.T, users ...*models.User) {
	t.Helper()
	for _, u := range users {
		if err := s.app.DB.Exec("UPDATE users SET deleted_at = now() WHERE id = ?", u.ID).Error; err != nil {
			t.Fatal(err)
		}
	}
}

// ask makes a terminal contact request with a reason between two people.
func (s legacyScene) ask(t *testing.T, requester, subject *models.User, n int) models.ContactRequest {
	t.Helper()
	return erasureMakeRequest(t, s.app.DB, policy.RequestContact, policy.ContactWithdrawn, requester.ID, subject.ID, 1, 0, 0, n, "")
}

// askAdd makes a terminal group_add request with a reason.
func (s legacyScene) askAdd(t *testing.T, requester, subject *models.User, n int) models.ContactRequest {
	t.Helper()
	return erasureMakeRequest(t, s.app.DB, policy.RequestGroupAdd, policy.ContactWithdrawn, requester.ID, subject.ID, 1, 0, 0, n, "")
}

// setID gives a request an exact id (signed boundary ids).
func (s legacyScene) setID(t *testing.T, from uint, to int64) int64 {
	t.Helper()
	if err := s.app.DB.Exec("UPDATE contact_requests SET id = ? WHERE id = ?", to, from).Error; err != nil {
		t.Fatal(err)
	}
	return to
}

// mark writes a decision marker for (request, party) the way a decision does:
// the purpose's own action and target type, reviewVersion 1 and the party as
// erasedUserId, both JSON numbers.
func legacyMark(t *testing.T, db *gorm.DB, request int64, party uint, purpose string, kept bool) {
	t.Helper()
	k := requestKindOf(purpose)
	action := k.reasonRemoved
	if kept {
		action = k.reasonKept
	}
	legacyMarkRaw(t, db, action, k.target, request, fmt.Sprintf(`{"erasedUserId": %d, "reviewVersion": 1, "source": "legacy_review"}`, party))
}

func legacyMarkRaw(t *testing.T, db *gorm.DB, action, targetType string, request int64, meta string) {
	t.Helper()
	if err := db.Exec("INSERT INTO audit_logs (actor_id, action, target_type, target_id, meta_json) VALUES (NULL, ?, ?, ?, ?::jsonb)",
		action, targetType, request, meta).Error; err != nil {
		t.Fatal(err)
	}
}

// legacyReference is the rule written once more, as one SQL query that does not
// use requestKindOf or legacyMarkersIn: every (request, party) pair whose reason
// is not blank, whose party is a soft-deleted account and which has no marker of
// the purpose's own kind for that party (R3-C08), ordered as a page emits them:
// by request id, requester first.
func legacyReference(t *testing.T, db *gorm.DB) []reasonscan.Item {
	t.Helper()
	var rows []struct {
		ID    int64
		Party int64
	}
	err := db.Raw(`SELECT cr.id AS id, p.party AS party
FROM contact_requests cr
CROSS JOIN LATERAL (VALUES (cr.requester_id, 0), (cr.subject_user_id, 1)) AS p(party, ord)
JOIN users u ON u.id = p.party AND u.deleted_at IS NOT NULL
WHERE cr.reason <> ''
AND NOT EXISTS (
	SELECT 1 FROM audit_logs a
	WHERE a.target_id = cr.id
	AND ((cr.purpose = 'contact' AND a.target_type = 'contact_request' AND a.action IN (?, ?))
	  OR (cr.purpose <> 'contact' AND a.target_type = 'add_request' AND a.action IN (?, ?)))
	AND jsonb_typeof(a.meta_json->'reviewVersion') = 'number' AND (a.meta_json->>'reviewVersion')::numeric = 1 AND a.meta_json->>'reviewVersion' = '1'
	AND jsonb_typeof(a.meta_json->'erasedUserId') = 'number' AND a.meta_json->>'erasedUserId' = p.party::text)
ORDER BY cr.id, p.ord`, audit.ContactReasonKept, audit.ContactReasonRemoved, audit.AddReasonKept, audit.AddReasonRemoved).Scan(&rows).Error
	if err != nil {
		t.Fatal(err)
	}
	out := make([]reasonscan.Item, len(rows))
	for i, r := range rows {
		out[i] = reasonscan.Item{Request: r.ID, Party: r.Party}
	}
	return out
}

// legacyWalk reads every page from the start with scanLegacyUnit in one
// transaction per page, party 0 (discovery) or a tombstone's own, and returns
// the pairs in the order the pages emitted them with the number of pages.
func legacyWalk(t *testing.T, db *gorm.DB, party int64, pageRequests int) ([]reasonscan.Item, int) {
	t.Helper()
	var all []reasonscan.Item
	var after *int64
	for pages := 1; pages < 100000; pages++ {
		var unit legacyUnit
		if err := db.Transaction(func(tx *gorm.DB) error {
			var err error
			unit, err = scanLegacyUnit(tx, party, after, pageRequests)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if unit.Requests > pageRequests {
			t.Fatalf("a page of %d requests, the cap is %d", unit.Requests, pageRequests)
		}
		all = append(all, unit.Items...)
		if unit.Next == nil {
			return all, pages
		}
		after = unit.Next
	}
	t.Fatal("the walk never ended")
	return nil, 0
}

// scanLegacyOracle is the one-statement scan with the legacy rule decided from a
// reference set: it reads up to reasonUnitRows rows in one statement and
// stops right after the pageRequests-th qualifying request, like
// scanReasonItems for the live rule. party 0 is discovery.
func scanLegacyOracle(tx *gorm.DB, want map[int64][]int64, party int64, after *int64, pageRequests int) (legacyUnit, error) {
	var unit legacyUnit
	var rows *sql.Rows
	var err error
	if after == nil {
		rows, err = tx.Raw("SELECT id FROM contact_requests ORDER BY id LIMIT ?", reasonUnitRows).Rows()
	} else {
		rows, err = tx.Raw("SELECT id FROM contact_requests WHERE id > ?::bigint ORDER BY id LIMIT ?", *after, reasonUnitRows).Rows()
	}
	if err != nil {
		return unit, err
	}
	defer rows.Close()
	read := 0
	var last int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return unit, err
		}
		read++
		last = id
		var items []reasonscan.Item
		for _, p := range want[id] {
			if party == 0 || p == party {
				items = append(items, reasonscan.Item{Request: id, Party: p})
			}
		}
		if len(items) == 0 {
			continue
		}
		unit.Items = append(unit.Items, items...)
		unit.Requests++
		if unit.Requests == pageRequests {
			next := id
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

func legacyKeyed(items []reasonscan.Item) map[int64][]int64 {
	m := map[int64][]int64{}
	for _, it := range items {
		m[it.Request] = append(m[it.Request], it.Party)
	}
	return m
}

func itemsString(items []reasonscan.Item) string {
	var b strings.Builder
	for _, it := range items {
		fmt.Fprintf(&b, "%d/%d ", it.Request, it.Party)
	}
	return b.String()
}

// ---------------------------------------------------------------------------
// Qualification and scanning (R4-C01)
// ---------------------------------------------------------------------------

// TestLegacyScanNoSkip: the request-atomic rule on real rows. A two-party
// request is whole on a page, as the 100th request (101 items) or the 101st (the
// next page), in both id orders of its parties and both orders of their
// deletion, wherever its row falls against a batch boundary; a two-party request
// with one party already decided is one item; a tombstone's items span three
// pages and each appears once.
func TestLegacyScanNoSkip(t *testing.T) {
	type order struct {
		name      string
		reqIsLow  bool
		deleteLow bool
	}
	orders := []order{{"requester id low, low deleted first", true, true}, {"requester id low, high deleted first", true, false},
		{"requester id high, low deleted first", false, true}, {"requester id high, high deleted first", false, false}}
	for _, o := range orders {
		for _, pos := range []int{100, 101} {
			for _, first := range []int{7, 100, 256} {
				t.Run(fmt.Sprintf("%s/the %dth request/first batch %d", o.name, pos, first), func(t *testing.T) {
					s := newLegacyScene(t, 3)
					setScanBounds(t, 50000, first, 100)
					low, high, live := s.users[0], s.users[1], s.users[2]
					if o.deleteLow {
						s.bury(t, low)
						s.bury(t, high)
					} else {
						s.bury(t, high)
						s.bury(t, low)
					}
					req, sub := low, high
					if !o.reqIsLow {
						req, sub = high, low
					}
					for i := 0; i < pos-1; i++ {
						s.ask(t, low, live, i)
					}
					two := s.ask(t, req, sub, 1000)
					s.ask(t, low, live, 1001) // one more after, so the page does not end with the table
					page1, pages := legacyWalk(t, s.app.DB, 0, 100)
					if !reflect.DeepEqual(page1, legacyReference(t, s.app.DB)) {
						t.Fatalf("the walk is not the reference:\n walk %s\n ref  %s", itemsString(page1), itemsString(legacyReference(t, s.app.DB)))
					}
					if pages != 2 {
						t.Fatalf("%d pages, want 2", pages)
					}
					// The first page itself: 100 requests, the two-party one whole or not at all.
					var unit legacyUnit
					if err := s.app.DB.Transaction(func(tx *gorm.DB) error {
						var err error
						unit, err = scanLegacyUnit(tx, 0, nil, 100)
						return err
					}); err != nil {
						t.Fatal(err)
					}
					if unit.Requests != 100 {
						t.Fatalf("first page has %d requests, want 100", unit.Requests)
					}
					in := 0
					for _, it := range unit.Items {
						if it.Request == int64(two.ID) {
							in++
						}
					}
					want := 0
					if pos == 100 {
						want = 2
					}
					if in != want {
						t.Fatalf("the two-party request has %d items on page 1, want %d", in, want)
					}
					wantItems := 100
					if pos == 100 {
						wantItems = 101
					}
					if len(unit.Items) != wantItems {
						t.Fatalf("page 1 has %d items, want %d", len(unit.Items), wantItems)
					}
				})
			}
		}
	}

	t.Run("a two-party request with one party decided is one item", func(t *testing.T) {
		s := newLegacyScene(t, 2)
		s.bury(t, s.users[0], s.users[1])
		r := s.ask(t, s.users[0], s.users[1], 1)
		legacyMark(t, s.app.DB, int64(r.ID), s.users[0].ID, policy.RequestContact, true)
		items, _ := legacyWalk(t, s.app.DB, 0, 100)
		if len(items) != 1 || items[0] != (reasonscan.Item{Request: int64(r.ID), Party: int64(s.users[1].ID)}) {
			t.Fatalf("items %s, want only the undecided party", itemsString(items))
		}
		// The counterpart's own marker is not the party's: still listed after the first Keep.
		legacyMark(t, s.app.DB, int64(r.ID), s.users[1].ID, policy.RequestContact, false)
		if items, _ := legacyWalk(t, s.app.DB, 0, 100); len(items) != 0 {
			t.Fatalf("a request decided for both parties still lists %s", itemsString(items))
		}
	})

	t.Run("an account's items aggregate across three pages", func(t *testing.T) {
		s := newLegacyScene(t, 3)
		setScanBounds(t, 50000, 3, 4)
		gone, live, other := s.users[0], s.users[1], s.users[2]
		s.bury(t, gone)
		var ids []string
		for i := 0; i < 10; i++ {
			// Items of the same account, mixed with requests that are not its.
			ids = append(ids, strconv.FormatUint(uint64(s.ask(t, gone, live, i).ID), 10))
			s.ask(t, live, other, 100+i)
		}
		var seen []string
		var after *string
		pages := 0
		perAccount := map[string]int{}
		for i := 0; i < 20; i++ {
			path := "/api/v1/admin/erasure-reasons/legacy"
			if after != nil {
				path += "?after=" + *after
			}
			w := s.ac.do("GET", path, nil, false)
			if w.Code != http.StatusOK {
				t.Fatalf("discovery: %d %s", w.Code, w.Body.String())
			}
			var page legacyReasonDiscoveryJSON
			if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
				t.Fatal(err)
			}
			pages++
			for _, a := range page.Accounts {
				perAccount[a.UserID] += len(a.Items)
				for _, it := range a.Items {
					seen = append(seen, it.RequestID)
				}
			}
			if page.Next == nil {
				break
			}
			after = page.Next
		}
		if !reflect.DeepEqual(seen, ids) {
			t.Fatalf("items over pages %v, want %v (each once, in order)", seen, ids)
		}
		if pages != 3 {
			t.Fatalf("%d pages, want 3 (10 items, 4 requests a page)", pages)
		}
		if got := perAccount[strconv.FormatUint(uint64(gone.ID), 10)]; got != 10 || len(perAccount) != 1 {
			t.Fatalf("per account %v, want 10 items for the one account", perAccount)
		}
	})

	t.Run("a tombstone's own scan holds only its party", func(t *testing.T) {
		s := newLegacyScene(t, 3)
		a, b := s.users[0], s.users[1]
		s.bury(t, a, b)
		for i := 0; i < 5; i++ {
			s.ask(t, a, b, i)
		}
		s.ask(t, s.users[2], a, 50)
		itemsA, _ := legacyWalk(t, s.app.DB, int64(a.ID), 3)
		itemsB, _ := legacyWalk(t, s.app.DB, int64(b.ID), 3)
		if len(itemsA) != 6 || len(itemsB) != 5 {
			t.Fatalf("a has %d, b has %d, want 6 and 5", len(itemsA), len(itemsB))
		}
		for _, it := range itemsA {
			if it.Party != int64(a.ID) {
				t.Fatalf("a fixed party page holds %v", it)
			}
		}
	})
}

// TestLegacyScanRandomUnionEqualsReference: a seeded fixture of 1,000 requests
// with mixed party states, purposes, blank reasons, decided pairs, decoy markers
// and signed boundary ids. Every page walk, for several batch and page shapes and
// for discovery and each fixed party, is the reference query exactly, with no
// duplicate (request, party), and the batched scan equals the one-statement
// oracle from every cursor.
func TestLegacyScanRandomUnionEqualsReference(t *testing.T) {
	for _, seed := range []int64{301, 4242} {
		t.Run(fmt.Sprintf("seed %d", seed), func(t *testing.T) {
			rng := rand.New(rand.NewSource(seed))
			s := newLegacyScene(t, 6)
			// users 0-2 deleted, 3-5 live.
			s.bury(t, s.users[:3]...)
			const n = 1000
			for i := 0; i < n; i++ {
				req, sub := s.users[rng.Intn(6)], s.users[rng.Intn(6)]
				for sub.ID == req.ID {
					sub = s.users[rng.Intn(6)]
				}
				if rng.Intn(4) == 0 {
					s.askAdd(t, req, sub, i)
				} else {
					s.ask(t, req, sub, i)
				}
			}
			// Exact signed ids for the contact requests (a terminal add request is
			// immutable, its id included, so those keep the sequence's small ids): park
			// the contact ids out of the way, then assign a unique id from a pool that
			// holds the int64 ends, either side of zero, and avoids the add ids.
			var addIDs []int64
			if err := s.app.DB.Raw("SELECT id FROM contact_requests WHERE purpose <> 'contact'").Scan(&addIDs).Error; err != nil {
				t.Fatal(err)
			}
			taken := map[int64]bool{}
			for _, id := range addIDs {
				taken[id] = true
			}
			if err := s.app.DB.Exec("UPDATE contact_requests SET id = id + 100000000 WHERE purpose = 'contact'").Error; err != nil {
				t.Fatal(err)
			}
			var parked []int64
			if err := s.app.DB.Raw("SELECT id FROM contact_requests WHERE purpose = 'contact' ORDER BY id").Scan(&parked).Error; err != nil {
				t.Fatal(err)
			}
			pool := map[int64]bool{}
			for _, id := range []int64{math.MinInt64, -1, 0, math.MaxInt64, math.MinInt64 + 1, math.MaxInt64 - 1} {
				pool[id] = true
			}
			for len(pool) < len(parked) {
				if id := rng.Int63n(4000) - 2000; !taken[id] {
					pool[id] = true
				}
			}
			ids := make([]int64, 0, len(pool))
			for id := range pool {
				ids = append(ids, id)
			}
			sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
			rng.Shuffle(len(ids), func(i, j int) { ids[i], ids[j] = ids[j], ids[i] })
			for i, from := range parked {
				if err := s.app.DB.Exec("UPDATE contact_requests SET id = ? WHERE id = ?", ids[i], from).Error; err != nil {
					t.Fatal(err)
				}
			}
			ids = append(ids, addIDs...)
			sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
			// States: blank reasons, decided pairs, decoy markers.
			var reqs []struct {
				ID                 int64
				RequesterID        int64
				SubjectUserID      int64
				Purpose            string
				RequesterIsDeleted bool
				SubjectIsDeleted   bool
			}
			if err := s.app.DB.Raw(`SELECT cr.id, cr.requester_id, cr.subject_user_id, cr.purpose, (r.deleted_at IS NOT NULL) AS requester_is_deleted, (u.deleted_at IS NOT NULL) AS subject_is_deleted
				FROM contact_requests cr JOIN users r ON r.id = cr.requester_id JOIN users u ON u.id = cr.subject_user_id ORDER BY cr.id`).Scan(&reqs).Error; err != nil {
				t.Fatal(err)
			}
			for _, r := range reqs {
				blankable := r.Purpose == policy.RequestContact || r.RequesterIsDeleted || r.SubjectIsDeleted
				switch rng.Intn(10) {
				case 0:
					if !blankable {
						break
					}
					if err := s.app.DB.Exec("UPDATE contact_requests SET reason = '' WHERE id = ?", r.ID).Error; err != nil {
						t.Fatal(err)
					}
					continue
				}
				kind := requestKindOf(r.Purpose)
				for _, p := range []struct {
					id      int64
					deleted bool
				}{{r.RequesterID, r.RequesterIsDeleted}, {r.SubjectUserID, r.SubjectIsDeleted}} {
					switch rng.Intn(8) {
					case 0, 1:
						legacyMark(t, s.app.DB, r.ID, uint(p.id), r.Purpose, rng.Intn(2) == 0)
					case 2: // decoys that must not count
						other := "contact_request"
						if kind.target == other {
							other = "add_request"
						}
						meta := []string{
							fmt.Sprintf(`{"erasedUserId": "%d", "reviewVersion": 1}`, p.id),
							fmt.Sprintf(`{"erasedUserId": %d, "reviewVersion": 2}`, p.id),
							fmt.Sprintf(`{"erasedUserId": %d.0, "reviewVersion": 1}`, p.id),
							fmt.Sprintf(`{"erasedUserId": %d, "reviewVersion": "1"}`, p.id),
							fmt.Sprintf(`{"erasedUserId": %d}`, p.id),
							fmt.Sprintf(`{"erasedUserId": %d, "reviewVersion": 1}`, p.id+1),
						}[rng.Intn(6)]
						if rng.Intn(2) == 0 {
							legacyMarkRaw(t, s.app.DB, kind.reasonKept, kind.target, r.ID, meta)
						} else {
							legacyMarkRaw(t, s.app.DB, kind.reasonKept, other, r.ID,
								fmt.Sprintf(`{"erasedUserId": %d, "reviewVersion": 1}`, p.id))
						}
					}
				}
			}
			ref := legacyReference(t, s.app.DB)
			if len(ref) < 100 {
				t.Fatalf("the fixture is too sparse: %d pairs", len(ref))
			}
			t.Logf("seed %d: %d reference pairs over %d requests", seed, len(ref), len(ids))
			seenPair := map[reasonscan.Item]bool{}
			for _, it := range ref {
				if seenPair[it] {
					t.Fatalf("the reference lists %v twice", it)
				}
				seenPair[it] = true
			}
			want := legacyKeyed(ref)
			cursors := []*int64{nil}
			for i := 0; i < len(ids); i += 37 {
				c := ids[i]
				cursors = append(cursors, &c)
			}
			for _, shape := range []struct{ unit, first, page int }{{50000, 256, 100}, {50000, 5, 7}, {200, 64, 100}, {50000, 1, 1}, {97, 13, 3}} {
				setScanBounds(t, shape.unit, shape.first, shape.page)
				got, pages := legacyWalk(t, s.app.DB, 0, shape.page)
				if shape.unit == 50000 {
					if !reflect.DeepEqual(got, ref) {
						t.Fatalf("shape %+v: %d pages, walk differs from the reference:\n walk %.600s\n ref  %.600s", shape, pages, itemsString(got), itemsString(ref))
					}
				}
				seen := map[reasonscan.Item]bool{}
				for _, it := range got {
					if seen[it] {
						t.Fatalf("shape %+v: %v listed twice", shape, it)
					}
					seen[it] = true
					if !seenPair[it] {
						t.Fatalf("shape %+v: %v is not in the reference", shape, it)
					}
				}
				// Batched equals the one-statement oracle, from every sampled cursor,
				// for discovery and for a fixed party.
				for _, party := range []int64{0, int64(s.users[0].ID), int64(s.users[4].ID)} {
					for _, after := range cursors {
						err := s.app.DB.Transaction(func(tx *gorm.DB) error {
							batched, err := scanLegacyUnit(tx, party, after, shape.page)
							if err != nil {
								return err
							}
							oracle, err := scanLegacyOracle(tx, want, party, after, shape.page)
							if err != nil {
								return err
							}
							if !reflect.DeepEqual(batched.Items, oracle.Items) || batched.Requests != oracle.Requests ||
								(batched.Next == nil) != (oracle.Next == nil) || (batched.Next != nil && *batched.Next != *oracle.Next) {
								t.Errorf("shape %+v party %d: batched %s next %v requests %d, oracle %s next %v requests %d", shape, party,
									itemsString(batched.Items), batched.Next, batched.Requests, itemsString(oracle.Items), oracle.Next, oracle.Requests)
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

// ---------------------------------------------------------------------------
// The marker selector (R3-C08, F7)
// ---------------------------------------------------------------------------

// TestLegacyMarkerSelector: only a decision of the request's own kind for that
// party, with reviewVersion and erasedUserId as JSON numbers and reviewVersion
// exactly the integer 1, makes a pair reviewed. Each wrong value leaves it listed.
// The test helper reasonMarkers and the production selector agree on every row.
func TestLegacyMarkerSelector(t *testing.T) {
	contact, add := requestKindOf(policy.RequestContact), requestKindOf(policy.RequestGroupAdd)
	type row struct {
		name       string
		purpose    string
		action     func() string
		targetType func() string
		meta       func(party uint) string
		counts     bool
	}
	num := func(party uint, version string) string {
		return fmt.Sprintf(`{"erasedUserId": %d, "reviewVersion": %s}`, party, version)
	}
	cases := []row{
		{"contact kept, number 1", policy.RequestContact, func() string { return contact.reasonKept }, func() string { return contact.target }, func(p uint) string { return num(p, "1") }, true},
		{"contact removed, number 1", policy.RequestContact, func() string { return contact.reasonRemoved }, func() string { return contact.target }, func(p uint) string { return num(p, "1") }, true},
		{"add kept, number 1", policy.RequestGroupAdd, func() string { return add.reasonKept }, func() string { return add.target }, func(p uint) string { return num(p, "1") }, true},
		{"add removed, number 1", policy.RequestGroupAdd, func() string { return add.reasonRemoved }, func() string { return add.target }, func(p uint) string { return num(p, "1") }, true},
		{"reviewVersion string '1'", policy.RequestContact, func() string { return contact.reasonKept }, func() string { return contact.target }, func(p uint) string { return num(p, `"1"`) }, false},
		{"reviewVersion 1.0", policy.RequestContact, func() string { return contact.reasonKept }, func() string { return contact.target }, func(p uint) string { return num(p, "1.0") }, false},
		{"reviewVersion null", policy.RequestContact, func() string { return contact.reasonKept }, func() string { return contact.target }, func(p uint) string { return num(p, "null") }, false},
		{"reviewVersion missing", policy.RequestContact, func() string { return contact.reasonKept }, func() string { return contact.target }, func(p uint) string { return fmt.Sprintf(`{"erasedUserId": %d}`, p) }, false},
		{"reviewVersion 2", policy.RequestContact, func() string { return contact.reasonKept }, func() string { return contact.target }, func(p uint) string { return num(p, "2") }, false},
		{"reviewVersion true", policy.RequestContact, func() string { return contact.reasonKept }, func() string { return contact.target }, func(p uint) string { return num(p, "true") }, false},
		{"erasedUserId string", policy.RequestContact, func() string { return contact.reasonKept }, func() string { return contact.target }, func(p uint) string { return fmt.Sprintf(`{"erasedUserId": "%d", "reviewVersion": 1}`, p) }, false},
		{"erasedUserId 5.0", policy.RequestContact, func() string { return contact.reasonKept }, func() string { return contact.target }, func(p uint) string { return fmt.Sprintf(`{"erasedUserId": %d.0, "reviewVersion": 1}`, p) }, false},
		{"erasedUserId null", policy.RequestContact, func() string { return contact.reasonKept }, func() string { return contact.target }, func(uint) string { return `{"erasedUserId": null, "reviewVersion": 1}` }, false},
		{"erasedUserId missing", policy.RequestContact, func() string { return contact.reasonKept }, func() string { return contact.target }, func(uint) string { return `{"reviewVersion": 1}` }, false},
		{"another party", policy.RequestContact, func() string { return contact.reasonKept }, func() string { return contact.target }, func(p uint) string { return num(p+1, "1") }, false},
		{"add action on a contact request", policy.RequestContact, func() string { return add.reasonKept }, func() string { return contact.target }, func(p uint) string { return num(p, "1") }, false},
		{"contact action on an add request", policy.RequestGroupAdd, func() string { return contact.reasonKept }, func() string { return add.target }, func(p uint) string { return num(p, "1") }, false},
		{"add target type on a contact request", policy.RequestContact, func() string { return contact.reasonKept }, func() string { return add.target }, func(p uint) string { return num(p, "1") }, false},
		{"contact target type on an add request", policy.RequestGroupAdd, func() string { return add.reasonKept }, func() string { return contact.target }, func(p uint) string { return num(p, "1") }, false},
		{"an unrelated action", policy.RequestContact, func() string { return audit.ContactDecision }, func() string { return contact.target }, func(p uint) string { return num(p, "1") }, false},
		{"the view audit action", policy.RequestContact, func() string { return audit.UserErasureReasonsViewed }, func() string { return contact.target }, func(p uint) string { return num(p, "1") }, false},
	}
	s := newLegacyScene(t, 2)
	gone, other := s.users[0], s.users[1]
	s.bury(t, gone)
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var r models.ContactRequest
			switch c.purpose {
			case policy.RequestContact:
				r = s.ask(t, gone, other, 1)
			case policy.RequestGroupAdd:
				r = s.askAdd(t, gone, other, 1)
			}
			legacyMarkRaw(t, s.app.DB, c.action(), c.targetType(), int64(r.ID), c.meta(gone.ID))
			kind := requestKindOf(r.Purpose)
			var got map[legacyPair]bool
			if err := s.app.DB.Transaction(func(tx *gorm.DB) error {
				var err error
				got, err = legacyMarkersIn(tx, []legacyCandidate{{legacyPair{int64(r.ID), int64(gone.ID)}, kind}})
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if got[legacyPair{int64(r.ID), int64(gone.ID)}] != c.counts {
				t.Fatalf("selector says reviewed=%v, want %v", !c.counts, c.counts)
			}
			// The test helper (R3-C08's reference form) says the same.
			helper := reasonMarkers(t, s.app.DB, int64(r.ID), gone.ID, kind.reasonKept, kind.target) +
				reasonMarkers(t, s.app.DB, int64(r.ID), gone.ID, kind.reasonRemoved, kind.target)
			if (helper > 0) != c.counts {
				t.Fatalf("helper reasonMarkers counted %d, want reviewed=%v", helper, c.counts)
			}
			// And the page lists the pair exactly when the selector says unreviewed.
			items, _ := legacyWalk(t, s.app.DB, 0, 100)
			listed := false
			for _, it := range items {
				listed = listed || (it.Request == int64(r.ID) && it.Party == int64(gone.ID))
			}
			if listed == c.counts {
				t.Fatalf("listed=%v with a marker that counts=%v", listed, c.counts)
			}
		})
	}

	if requestKindOf(policy.RequestOccurrenceAdd) != add || requestKindOf(policy.RequestGroupAdd) != add || requestKindOf(policy.RequestContact) != contact || requestKindOf("mystery") != nil {
		t.Fatal("the marker vocabulary of each purpose is not the request kind registry's")
	}

	t.Run("a mixed batch matches each candidate to its own kind", func(t *testing.T) {
		s := newLegacyScene(t, 2)
		s.bury(t, s.users[0])
		c := s.ask(t, s.users[0], s.users[1], 1)
		a := s.askAdd(t, s.users[0], s.users[1], 2)
		// The contact request is decided with the add vocabulary and the add request
		// with the contact vocabulary: each is the wrong kind for its own request, so
		// both stay listed; the right kinds decide them.
		legacyMarkRaw(t, s.app.DB, add.reasonKept, add.target, int64(c.ID), num(s.users[0].ID, "1"))
		legacyMarkRaw(t, s.app.DB, contact.reasonKept, contact.target, int64(a.ID), num(s.users[0].ID, "1"))
		items, _ := legacyWalk(t, s.app.DB, 0, 100)
		if len(items) != 2 {
			t.Fatalf("wrong-kind markers decided %d of 2 requests: %s", 2-len(items), itemsString(items))
		}
		legacyMark(t, s.app.DB, int64(c.ID), s.users[0].ID, policy.RequestContact, true)
		legacyMark(t, s.app.DB, int64(a.ID), s.users[0].ID, policy.RequestGroupAdd, false)
		if items, _ := legacyWalk(t, s.app.DB, 0, 100); len(items) != 0 {
			t.Fatalf("right-kind markers left %s listed", itemsString(items))
		}
	})

	t.Run("an unknown purpose is an error and not a qualifying request", func(t *testing.T) {
		s := newLegacyScene(t, 2)
		s.bury(t, s.users[0])
		r := s.ask(t, s.users[0], s.users[1], 1)
		err := s.app.DB.Transaction(func(tx *gorm.DB) error {
			if err := tx.Exec("ALTER TABLE contact_requests DISABLE TRIGGER USER").Error; err != nil {
				return err
			}
			if err := tx.Exec("ALTER TABLE contact_requests DROP CONSTRAINT contact_requests_purpose_scope").Error; err != nil {
				return err
			}
			if err := tx.Exec("UPDATE contact_requests SET purpose = 'mystery' WHERE id = ?", r.ID).Error; err != nil {
				return err
			}
			_, err := scanLegacyUnit(tx, 0, nil, 100)
			if err == nil {
				t.Error("an unknown purpose was scanned without an error")
			}
			return fmt.Errorf("rollback: %w", err)
		})
		if err == nil {
			t.Fatal("expected the rollback error")
		}
	})
}

// ---------------------------------------------------------------------------
// Batched lookups, not per-row lookups (M1, M2)
// ---------------------------------------------------------------------------

// A batch of reason-bearing rows costs one facts statement and one marker
// statement, whatever its size, and a request with no reason costs none; the
// scan statements are the live schedule's.
func TestLegacyLookupsAreBatchedPerBatch(t *testing.T) {
	s := newLegacyScene(t, 3)
	s.bury(t, s.users[0])
	for i := 0; i < 40; i++ {
		s.ask(t, s.users[0], s.users[1], i)
	}
	s.ask(t, s.users[1], s.users[2], 99) // no deleted party: no marker candidate
	if err := s.app.DB.Exec("UPDATE contact_requests SET reason = '' WHERE id IN (SELECT id FROM contact_requests ORDER BY id LIMIT 5)").Error; err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var facts, markers, scans int
	name := fmt.Sprintf("test:legacy_lookups:%d", queryCounterSeq.Add(1))
	capture := func(tx *gorm.DB) {
		q := tx.Statement.SQL.String()
		mu.Lock()
		defer mu.Unlock()
		switch {
		case strings.Contains(q, "has_reason"):
			scans++
		case strings.Contains(q, "LEFT JOIN users req"):
			facts++
		case strings.Contains(q, "unnest("):
			markers++
		}
	}
	if err := s.app.DB.Callback().Raw().After("gorm:raw").Register(name, capture); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.app.DB.Callback().Raw().Remove(name) })
	if err := s.app.DB.Callback().Row().After("gorm:row").Register(name, capture); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.app.DB.Callback().Row().Remove(name) })

	setScanBounds(t, 50000, 256, 100)
	items, _ := legacyWalk(t, s.app.DB, 0, 100)
	if len(items) != 35 {
		t.Fatalf("%d items, want 35", len(items))
	}
	if scans != 1 || facts != 1 || markers != 1 {
		t.Fatalf("one 41-row batch took %d scan, %d facts, %d marker statements, want 1 each", scans, facts, markers)
	}

	// Many batches: per attempt at most 8 scan statements and 16 lookups.
	scans, facts, markers = 0, 0, 0
	setScanBounds(t, 50000, 4, 100)
	legacyWalk(t, s.app.DB, 0, 100)
	if scans > 8 || facts+markers > 16 || facts > scans || markers > scans {
		t.Fatalf("%d scans, %d facts, %d markers: over the bound of 8 scans and 16 lookups per attempt", scans, facts, markers)
	}
	if facts == 0 || markers == 0 {
		t.Fatalf("no lookups ran (%d, %d)", facts, markers)
	}
}

// ---------------------------------------------------------------------------
// The routes: discovery and probe
// ---------------------------------------------------------------------------

func (s legacyScene) discover(path string) (int, string) {
	w := s.ac.do("GET", path, nil, false)
	return w.Code, w.Body.String()
}

const legacyDiscoveryPath = "/api/v1/admin/erasure-reasons/legacy"
const legacyProbePath = "/api/v1/admin/erasure-reasons/legacy/probe"

func TestLegacyDiscoveryShapeAndSignedIDs(t *testing.T) {
	s := newLegacyScene(t, 3)
	gone, live := s.users[0], s.users[1]
	s.bury(t, gone)
	ids := []int64{math.MinInt64, -1, 0, math.MaxInt64}
	for i, want := range ids {
		r := s.ask(t, gone, live, i)
		s.setID(t, r.ID, want)
	}
	code, body := s.discover(legacyDiscoveryPath)
	if code != http.StatusOK {
		t.Fatalf("discovery: %d %s", code, body)
	}
	var page legacyReasonDiscoveryJSON
	if err := json.Unmarshal([]byte(body), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Accounts) != 1 || page.Accounts[0].UserID != strconv.FormatUint(uint64(gone.ID), 10) || page.Accounts[0].Name != "Deleted account" {
		t.Fatalf("accounts %+v, want one 'Deleted account' with the tombstone's decimal id", page.Accounts)
	}
	var got []string
	for _, it := range page.Accounts[0].Items {
		got = append(got, it.RequestID)
	}
	want := []string{"-9223372036854775808", "-1", "0", "9223372036854775807"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("request ids %v, want exact signed decimals %v", got, want)
	}
	if page.Next != nil {
		t.Fatalf("next %v, want null at the end of the table", *page.Next)
	}
	// Nothing but ids reaches the answer: no reason text, no counterpart, no hash, no ref.
	for _, leak := range []string{"identifying reason", live.Name, live.Email, "reviewHash", `"ref"`, `"reason"`} {
		if strings.Contains(body, leak) {
			t.Errorf("discovery leaks %q: %s", leak, body)
		}
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(body), &raw); err != nil || len(raw) != 2 {
		t.Fatalf("top-level keys %v (%v), want exactly accounts and next", raw, err)
	}
	if n := countAudit(t, s.app.DB, audit.UserErasureReasonsViewed); n != 0 {
		t.Fatalf("discovery wrote %d view audits, want none", n)
	}

	// The cursor chain over the signed ids: each page continues after the last
	// request it returned, however the ids sort around zero.
	setScanBounds(t, 50000, 256, 1)
	var chain []string
	var after *string
	for i := 0; i < 10; i++ {
		path := legacyDiscoveryPath
		if after != nil {
			path += "?after=" + *after
		}
		code, body := s.discover(path)
		if code != http.StatusOK {
			t.Fatalf("page %d: %d %s", i, code, body)
		}
		var p legacyReasonDiscoveryJSON
		if err := json.Unmarshal([]byte(body), &p); err != nil {
			t.Fatal(err)
		}
		for _, a := range p.Accounts {
			for _, it := range a.Items {
				chain = append(chain, it.RequestID)
			}
		}
		if p.Next == nil {
			break
		}
		after = p.Next
	}
	if !reflect.DeepEqual(chain, want) {
		t.Fatalf("one request a page gave %v, want %v", chain, want)
	}
	// Probe: found, and an empty first unit is found=false.
	code, body = s.discover(legacyProbePath)
	if code != http.StatusOK || body != `{"found":true}` {
		t.Fatalf("probe: %d %s, want found true", code, body)
	}
	if err := s.app.DB.Exec("UPDATE contact_requests SET reason = ''").Error; err != nil {
		t.Fatal(err)
	}
	code, body = s.discover(legacyProbePath)
	if code != http.StatusOK || body != `{"found":false}` {
		t.Fatalf("probe with nothing to review: %d %s, want found false", code, body)
	}
	code, body = s.discover(legacyDiscoveryPath)
	if code != http.StatusOK || body != `{"accounts":[],"next":null}` {
		t.Fatalf("discovery with nothing to review: %d %s, want empty accounts and a null next", code, body)
	}
}

func TestLegacyDiscoveryRefusesABadCursor(t *testing.T) {
	s := newLegacyScene(t, 2)
	s.bury(t, s.users[0])
	s.ask(t, s.users[0], s.users[1], 1)
	for _, bad := range []string{"", "+1", "01", "-0", " 1", "1.5", "abc", "9223372036854775808", "-9223372036854775809", "0x1"} {
		code, body := s.discover(legacyDiscoveryPath + "?after=" + strings.ReplaceAll(bad, " ", "%20"))
		if code != http.StatusBadRequest {
			t.Errorf("after=%q: %d %s, want 400", bad, code, body)
		}
	}
	for _, good := range []string{"0", "-1", "9223372036854775807", "-9223372036854775808"} {
		if code, body := s.discover(legacyDiscoveryPath + "?after=" + good); code != http.StatusOK {
			t.Errorf("after=%s: %d %s, want 200", good, code, body)
		}
	}
}

// What a refused actor gets, on an empty unit and on one with items: the early
// check refuses before any statement over contact_requests is sent, the seal
// decides the same rule on the locked row, and the answer names no count.
func TestLegacyReadAuthorization(t *testing.T) {
	paths := map[string]string{"discovery": legacyDiscoveryPath, "probe": legacyProbePath}
	for _, withItems := range []bool{false, true} {
		for name, path := range paths {
			unit := "an empty unit"
			if withItems {
				unit = "a unit with items"
			}
			setup := func(t *testing.T) (legacyScene, func() []reasonStatement) {
				s := newLegacyScene(t, 2)
				if withItems {
					s.bury(t, s.users[0])
					s.ask(t, s.users[0], s.users[1], 1)
				}
				return s, recordContactRequestStatements(t, s.app.DB)
			}
			leaky := func(body string) bool {
				return strings.Contains(body, "accounts") || strings.Contains(body, "found") || strings.Contains(body, "next")
			}

			t.Run(name+"/an admin known to be 17 on "+unit, func(t *testing.T) {
				s, statements := setup(t)
				if err := s.app.DB.Exec("UPDATE users SET birth_date = ? WHERE id = ?", time.Now().AddDate(-17, 0, 0), s.admin.ID).Error; err != nil {
					t.Fatal(err)
				}
				code, body := s.discover(path)
				if code != http.StatusForbidden || codeOf2(t, body) != "reason_review_not_allowed" || leaky(body) {
					t.Fatalf("%d %s, want 403 reason_review_not_allowed and no answer", code, body)
				}
				if n := len(statements()); n != 0 {
					t.Fatalf("%d statements over contact_requests before the refusal, want none: a refused admin reads nothing, even an empty unit", n)
				}
			})
			t.Run(name+"/a teen on "+unit, func(t *testing.T) {
				s, statements := setup(t)
				if err := s.app.DB.Exec("UPDATE users SET account_type = ?, birth_date = ? WHERE id = ?", models.AccountTeen, time.Now().AddDate(-15, 0, 0), s.admin.ID).Error; err != nil {
					t.Fatal(err)
				}
				code, body := s.discover(path)
				if code != http.StatusForbidden || codeOf2(t, body) != "reason_review_not_allowed" || leaky(body) {
					t.Fatalf("%d %s, want 403 reason_review_not_allowed", code, body)
				}
				if n := len(statements()); n != 0 {
					t.Fatalf("%d statements over contact_requests for a teen, want none", n)
				}
			})
			t.Run(name+"/an inactive admin on "+unit, func(t *testing.T) {
				s, statements := setup(t)
				if err := s.app.DB.Exec("UPDATE users SET status = 'disabled' WHERE id = ?", s.admin.ID).Error; err != nil {
					t.Fatal(err)
				}
				code, body := s.discover(path)
				if code != http.StatusUnauthorized || leaky(body) {
					t.Fatalf("%d %s, want 401 and no answer", code, body)
				}
				if n := len(statements()); n != 0 {
					t.Fatalf("%d statements over contact_requests for a signed-out session, want none", n)
				}
			})
			t.Run(name+"/a non-admin on "+unit, func(t *testing.T) {
				s, statements := setup(t)
				newTestUser(t, s.app, "legacy-member", erasurePW, models.RoleMember)
				mc := loginAPI(t, s.app, "legacy-member", erasurePW)
				w := mc.do("GET", path, nil, false)
				if w.Code != http.StatusForbidden || codeOf2(t, w.Body.String()) == "reason_review_not_allowed" || leaky(w.Body.String()) {
					t.Fatalf("%d %s, want a plain 403", w.Code, w.Body.String())
				}
				if n := len(statements()); n != 0 {
					t.Fatalf("%d statements for a non-admin, want none", n)
				}
			})
			t.Run(name+"/a signed-out client on "+unit, func(t *testing.T) {
				s, _ := setup(t)
				w := newAPIClient(t, s.app).do("GET", path, nil, false)
				if w.Code != http.StatusUnauthorized {
					t.Fatalf("%d %s, want 401", w.Code, w.Body.String())
				}
			})
			t.Run(name+"/the seal decides the rule again on "+unit, func(t *testing.T) {
				s, _ := setup(t)
				// The admin is an adult at the early read and a known 17-year-old when the
				// seal locks the row: refused with the review answer, and not the empty or
				// the nonempty page (the live page's empty unit passes no predicate; this
				// one does).
				s.app.hooks.readBeforeSeal = func(*gorm.DB) {
					if err := s.app.DB.Exec("UPDATE users SET birth_date = ? WHERE id = ?", time.Now().AddDate(-17, 0, 0), s.admin.ID).Error; err != nil {
						t.Error(err)
					}
				}
				code, body := s.discover(path)
				if code != http.StatusForbidden || codeOf2(t, body) != "reason_review_not_allowed" || leaky(body) {
					t.Fatalf("%d %s, want 403 reason_review_not_allowed from the seal", code, body)
				}
			})
			t.Run(name+"/a stale session is 401 at the seal on "+unit, func(t *testing.T) {
				s, _ := setup(t)
				s.app.hooks.readBeforeSeal = func(*gorm.DB) {
					if err := s.app.DB.Exec("UPDATE users SET session_version = session_version + 1 WHERE id = ?", s.admin.ID).Error; err != nil {
						t.Error(err)
					}
				}
				code, body := s.discover(path)
				if code != http.StatusUnauthorized || leaky(body) {
					t.Fatalf("%d %s, want 401", code, body)
				}
			})
			t.Run(name+"/a password cleared before the seal is 401 on "+unit, func(t *testing.T) {
				s, _ := setup(t)
				s.app.hooks.readBeforeSeal = func(*gorm.DB) {
					if err := s.app.DB.Exec("UPDATE users SET password_hash = NULL WHERE id = ?", s.admin.ID).Error; err != nil {
						t.Error(err)
					}
				}
				code, body := s.discover(path)
				if code != http.StatusUnauthorized || leaky(body) {
					t.Fatalf("%d %s, want 401", code, body)
				}
			})
		}
	}
}

func codeOf2(t *testing.T, body string) string {
	t.Helper()
	var e struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal([]byte(body), &e)
	return e.Code
}

// A busy actor row (another connection holds it FOR NO KEY UPDATE) is retried
// under the page's bounds and then answers 503 reason_review_busy, never an
// empty list: three attempts, each ending in the seal.
func TestLegacyReadSealBusy(t *testing.T) {
	old := busyRetryWindow
	busyRetryWindow = 30 * time.Second
	t.Cleanup(func() { busyRetryWindow = old })
	for name, path := range map[string]string{"discovery": legacyDiscoveryPath, "probe": legacyProbePath} {
		t.Run(name, func(t *testing.T) {
			s := newLegacyScene(t, 2)
			s.bury(t, s.users[0])
			s.ask(t, s.users[0], s.users[1], 1)
			var attempts atomic.Int64
			s.app.hooks.readBeforeSeal = func(*gorm.DB) { attempts.Add(1) }
			hold, _ := beginOwnedTx(t, s.app.DB)
			if err := hold.Exec("SELECT id FROM users WHERE id = ? FOR NO KEY UPDATE", s.admin.ID).Error; err != nil {
				t.Fatal(err)
			}
			code, body := s.discover(path)
			if code != http.StatusServiceUnavailable || codeOf2(t, body) != "reason_review_busy" || strings.Contains(body, "accounts") || strings.Contains(body, "found") {
				t.Fatalf("under the hold: %d %s, want 503 reason_review_busy", code, body)
			}
			if n := attempts.Load(); n != 3 {
				t.Fatalf("%d attempts, want 3", n)
			}
			hold.Rollback()
			attempts.Store(0)
			if code, body := s.discover(path); code != http.StatusOK {
				t.Fatalf("after the release: %d %s", code, body)
			}
			if n := attempts.Load(); n != 1 {
				t.Fatalf("%d attempts after the release, want 1", n)
			}
		})
	}
}

// A busy seal whose ROLLBACK is never acknowledged ends the read with busy
// and starts no second attempt: the server may still hold the locks. The
// proxy drops the attempt's ROLLBACK, so reasonPageTx keeps the
// contactRollbackError and every legacy read answers 503 reason_review_busy
// after exactly one seal. The shared helper also serves the live page, so it
// is covered here too; the acknowledged-rollback retry stays covered by
// TestLegacyReadSealBusy.
func TestLegacyReadBusySealRollbackNotAcknowledged(t *testing.T) {
	s := newLegacyScene(t, 2)
	s.bury(t, s.users[0])
	s.ask(t, s.users[0], s.users[1], 1)
	s.request(t, s.other, 1)
	// The seal finds the admin row busy on a direct connection, which never
	// goes through the proxy and cannot claim the fault.
	hold, _ := beginOwnedTx(t, s.app.DB)
	if err := hold.Exec("SELECT id FROM users WHERE id = ? FOR NO KEY UPDATE", s.admin.ID).Error; err != nil {
		t.Fatal(err)
	}
	rig := newCtxRigFor(t, s.app)
	var name string
	if err := s.app.DB.Raw("SELECT current_database()").Scan(&name).Error; err != nil {
		t.Fatal(err)
	}
	proxyDB, states := tracedProxyDB(t, rig.proxy, name)
	rig.papp.DB = proxyDB
	pc := loginAPI(t, rig.papp, "erase-admin", erasurePW)
	var attempts atomic.Int64
	rig.papp.hooks.readBeforeSeal = func(*gorm.DB) { attempts.Add(1) }
	shape := func(mark int) string {
		var out strings.Builder
		for _, e := range rig.proxy.Events()[mark:] {
			switch {
			case e.Kind == testdb.KindRollback:
				out.WriteString("R")
			case e.Kind == testdb.KindCommit:
				out.WriteString("C")
			case e.Kind == testdb.KindStatement && strings.HasPrefix(strings.ToLower(strings.TrimSpace(e.SQL)), "begin"):
				out.WriteString("B")
			}
		}
		return out.String()
	}
	call := func(method, path string, body any) (int, string) {
		attempts.Store(0)
		mark := len(rig.proxy.Events())
		refusals := states.Count("55P03")
		// The seal's statement claims the fault for this attempt's connection;
		// its ROLLBACK is then dropped, so the acknowledgment is lost.
		rig.proxy.Arm(testdb.Faults{Match: "FOR SHARE NOWAIT", Rollback: testdb.RollbackCloseUnforwarded})
		w := pc.do(method, path, body, method != "GET")
		if n := attempts.Load(); n != 1 {
			t.Fatalf("%s %s: %d seal attempts, want 1: nothing may begin after an unacknowledged rollback", method, path, n)
		}
		if n := states.Count("55P03") - refusals; n != 1 {
			t.Fatalf("%s %s: %d lock refusals, want the one busy seal", method, path, n)
		}
		if got := shape(mark); got != "BR" {
			t.Fatalf("%s %s: transactions %q, want one BEGIN and its lost ROLLBACK", method, path, got)
		}
		return w.Code, w.Body.String()
	}
	livePath := "/api/v1/admin/users/" + itoa(s.target.ID) + "/erasure-reasons/page"
	for _, r := range []struct {
		method, path string
		body         any
		leak         string
	}{
		{"GET", legacyDiscoveryPath, nil, "accounts"},
		{"GET", legacyProbePath, nil, "found"},
		{"POST", livePath, map[string]any{"after": nil}, "items"},
	} {
		code, body := call(r.method, r.path, r.body)
		if code != http.StatusServiceUnavailable || codeOf2(t, body) != "reason_review_busy" || strings.Contains(body, r.leak) {
			t.Fatalf("%s %s: %d %s, want 503 reason_review_busy and no answer", r.method, r.path, code, body)
		}
	}
	// Once the holder lets go the same reads work: the refused attempts stored
	// nothing and broke nothing.
	if err := hold.Rollback().Error; err != nil {
		t.Fatal(err)
	}
	w := pc.do("GET", legacyDiscoveryPath, nil, false)
	if w.Code != http.StatusOK {
		t.Fatalf("after the release: %d %s, want 200", w.Code, w.Body.String())
	}
}

// A failed lookup is a failed page (503), not a page without the request.
func TestLegacyReadFailureIsNeverAnEmptyList(t *testing.T) {
	s := newLegacyScene(t, 2)
	s.bury(t, s.users[0])
	s.ask(t, s.users[0], s.users[1], 1)
	name := fmt.Sprintf("test:legacy_lookup_fail:%d", queryCounterSeq.Add(1))
	fail := func(tx *gorm.DB) {
		if strings.Contains(tx.Statement.SQL.String(), "unnest(") {
			_ = tx.AddError(fmt.Errorf("injected marker lookup failure"))
		}
	}
	if err := s.app.DB.Callback().Raw().Before("gorm:raw").Register(name, fail); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.app.DB.Callback().Raw().Remove(name) })
	if err := s.app.DB.Callback().Row().Before("gorm:row").Register(name, fail); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.app.DB.Callback().Row().Remove(name) })
	code, body := s.discover(legacyDiscoveryPath)
	if code != http.StatusServiceUnavailable || codeOf2(t, body) != "reason_review_busy" {
		t.Fatalf("a failed marker lookup: %d %s, want 503 reason_review_busy", code, body)
	}
}
