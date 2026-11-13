package main

// Tests for the deleted-accounts review list (T07B-3a, slice S1): the legacy
// filter and its marker selector, request-atomic scanning against a reference
// query, and the read routes (discovery and probe) with their authorization.

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"fold/internal/audit"
	"fold/internal/dbopen"
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
// after exactly one seal. The shared helper also serves the live page and an
// account's own page, so they are
// covered here too; the acknowledged-rollback retry stays covered by
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
		{"POST", legacyPagePath(s.users[0].ID), map[string]any{"after": nil}, "items"},
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

// ---------------------------------------------------------------------------
// A deleted account's own page (slice S2): POST legacy/:userId/page
// ---------------------------------------------------------------------------

func legacyPagePath(id uint) string {
	return "/api/v1/admin/erasure-reasons/legacy/" + itoa(id) + "/page"
}

// accountPage posts one page request as c and decodes a 200.
func accountPage(t *testing.T, c *apiClient, id uint, after *string) (int, reasonPageJSON, string) {
	t.Helper()
	w := c.do("POST", legacyPagePath(id), map[string]any{"after": after}, true)
	var page reasonPageJSON
	if w.Code == http.StatusOK {
		if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
			t.Fatalf("page body: %v", err)
		}
	}
	return w.Code, page, w.Body.String()
}

func requestIDs(items []reasonItemJSON) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.RequestID
	}
	return out
}

func viewAudits(t *testing.T, db *gorm.DB) []models.AuditLog {
	t.Helper()
	var rows []models.AuditLog
	if err := db.Where("action = ?", audit.UserErasureReasonsViewed).Order("id").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	return rows
}

// txStatement is one statement GORM sent, with the transaction it ran in.
type txStatement struct {
	pool any
	sql  string
}

// recordTxStatements sees every statement GORM sends on db, in order, with the
// connection pool it used: statements of one transaction share it.
func recordTxStatements(t *testing.T, db *gorm.DB) func() []txStatement {
	t.Helper()
	var mu sync.Mutex
	var out []txStatement
	capture := func(tx *gorm.DB) {
		mu.Lock()
		out = append(out, txStatement{pool: tx.Statement.ConnPool, sql: strings.Join(strings.Fields(tx.Statement.SQL.String()), " ")})
		mu.Unlock()
	}
	name := fmt.Sprintf("test:tx_statements:%d", queryCounterSeq.Add(1))
	for _, reg := range []func() error{
		func() error { return db.Callback().Raw().After("gorm:raw").Register(name, capture) },
		func() error { return db.Callback().Row().After("gorm:row").Register(name, capture) },
		func() error { return db.Callback().Query().After("gorm:query").Register(name, capture) },
		func() error { return db.Callback().Create().After("gorm:create").Register(name, capture) },
	} {
		if err := reg(); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		_ = db.Callback().Raw().Remove(name)
		_ = db.Callback().Row().Remove(name)
		_ = db.Callback().Query().Remove(name)
		_ = db.Callback().Create().Remove(name)
	})
	return func() []txStatement {
		mu.Lock()
		defer mu.Unlock()
		return append([]txStatement(nil), out...)
	}
}

// failStatements makes every statement whose SQL satisfies match fail, on the
// Raw, Row, Query and Create paths, until the test ends. A model query or insert
// has no SQL before GORM builds it, so those fail after they ran: the error is
// what the caller sees, and the transaction is rolled back with the row in it.
func failStatements(t *testing.T, db *gorm.DB, match func(sql string) bool) (stop func()) {
	t.Helper()
	name := fmt.Sprintf("test:fail_statements:%d", queryCounterSeq.Add(1))
	fail := func(tx *gorm.DB) {
		if match(tx.Statement.SQL.String()) {
			_ = tx.AddError(fmt.Errorf("injected statement failure"))
		}
	}
	for _, reg := range []func() error{
		func() error { return db.Callback().Raw().Before("gorm:raw").Register(name, fail) },
		func() error { return db.Callback().Row().Before("gorm:row").Register(name, fail) },
		func() error { return db.Callback().Query().After("gorm:query").Register(name, fail) },
		func() error { return db.Callback().Create().After("gorm:create").Register(name, fail) },
	} {
		if err := reg(); err != nil {
			t.Fatal(err)
		}
	}
	stop = func() {
		_ = db.Callback().Raw().Remove(name)
		_ = db.Callback().Row().Remove(name)
		_ = db.Callback().Query().Remove(name)
		_ = db.Callback().Create().Remove(name)
	}
	t.Cleanup(stop)
	return stop
}

// The page of an account deleted before reasons were reviewed: every field of
// an item, both party orders and the actor as either party, a live and a
// deleted counterpart, context labels, the per-viewer and per-party hash, the
// view audit (once per page that shows items, never for an empty one, never
// text) and the decided pair that leaves the page.
func TestLegacyAccountPage(t *testing.T) {
	s := newLegacyScene(t, 4)
	gone, live, gone2, bare := s.users[0], s.users[1], s.users[2], s.users[3]
	group := newTestGroup(t, s.app, live, "Legacy Group", models.GroupActive)
	// The requester side, the subject side, a deleted counterpart, the admin as
	// the subject and as the requester, and rows that never qualify: a blank
	// reason, someone else's request.
	r1 := erasureMakeRequest(t, s.app.DB, policy.RequestContact, policy.ContactWithdrawn, gone.ID, live.ID, group.ID, 0, 0, 1, "")
	r2 := erasureMakeRequest(t, s.app.DB, policy.RequestGroupAdd, policy.ContactWithdrawn, live.ID, gone.ID, 999999, 0, 0, 2, "")
	// s.ask files its requests in group 1, which is the group above here: these
	// name a group that does not exist, so no label can be shown.
	noGroup := func(requester, subject *models.User, n int) models.ContactRequest {
		return erasureMakeRequest(t, s.app.DB, policy.RequestContact, policy.ContactWithdrawn, requester.ID, subject.ID, 999999, 0, 0, n, "")
	}
	r3 := noGroup(gone, gone2, 3)
	r4 := noGroup(s.admin, gone, 4)
	r5 := noGroup(gone, s.admin, 5)
	blank := noGroup(gone, live, 6)
	if err := s.app.DB.Exec("UPDATE contact_requests SET reason = '' WHERE id = ?", blank.ID).Error; err != nil {
		t.Fatal(err)
	}
	noGroup(live, s.other, 7)
	s.bury(t, gone, gone2, bare)
	id := func(r models.ContactRequest) string { return itoa(r.ID) }
	goneRef, gone2Ref := itoa(gone.ID), itoa(gone2.ID)

	code, page, body := accountPage(t, s.ac, gone.ID, nil)
	want := []string{id(r1), id(r2), id(r3), id(r4), id(r5)}
	if code != http.StatusOK || !page.ReviewRequired || page.Next != nil || !reflect.DeepEqual(requestIDs(page.Items), want) {
		t.Fatalf("first page: %d reviewRequired=%v next=%v ids=%v, want ids %v: %s", code, page.ReviewRequired, page.Next, requestIDs(page.Items), want, body)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(body), &raw); err != nil || len(raw) != 3 || raw["reviewRequired"] == nil {
		t.Fatalf("top-level keys %v (%v), want items, next and reviewRequired", raw, err)
	}
	byID := map[string]reasonItemJSON{}
	for _, it := range page.Items {
		byID[it.RequestID] = it
		if !validReviewHash(it.ReviewHash) || it.CreatedAt.IsZero() || it.PartyRef != goneRef {
			t.Errorf("item %s: hash %q, createdAt %v, partyRef %q (want %s)", it.RequestID, it.ReviewHash, it.CreatedAt, it.PartyRef, goneRef)
		}
	}
	check := func(name string, got reasonItemJSON, r models.ContactRequest, role, purpose, counterpart string, counterpartDeleted bool, ref string, reviewerIsParty bool, label string) {
		t.Helper()
		if got.Role != role || got.Purpose != purpose || got.Status != policy.ContactWithdrawn || got.Reason != r.Reason ||
			got.CounterpartName != counterpart || got.CounterpartDeleted != counterpartDeleted || got.CounterpartRef != ref ||
			got.ReviewerIsParty != reviewerIsParty || got.ContextLabel != label {
			t.Errorf("%s: %+v", name, got)
		}
	}
	check("requester side, live counterpart, group shown", byID[id(r1)], r1, "requester", policy.RequestContact, live.PublicName(), false, "", false, "Legacy Group")
	check("subject side, context group gone", byID[id(r2)], r2, "subject", policy.RequestGroupAdd, live.PublicName(), false, "", false, "")
	check("deleted counterpart is never named", byID[id(r3)], r3, "requester", policy.RequestContact, "Deleted account", true, gone2Ref, false, "")
	check("the admin is the requester", byID[id(r4)], r4, "subject", policy.RequestContact, s.admin.PublicName(), false, "", true, "")
	check("the admin is the subject", byID[id(r5)], r5, "requester", policy.RequestContact, s.admin.PublicName(), false, "", true, "")
	for _, leak := range []string{gone.Name, gone.Email, gone2.Name, gone2.Email, blank.Reason, "identifying reason 7"} {
		if leak != "" && strings.Contains(body, leak) {
			t.Errorf("the page leaks %q", leak)
		}
	}

	// The hash binds the viewer and the party: recompute it from the production
	// reader, and see that another admin and another party get different ones.
	key, err := reasonHashKey(s.app.Config.SessionSecret)
	if err != nil {
		t.Fatal(err)
	}
	hashFor := func(viewer, party uint, request uint) string {
		var got string
		rows, err := s.app.DB.Raw("SELECT "+reasonSelect+" FROM contact_requests WHERE id = ?", request).Rows()
		if err != nil {
			t.Fatal(err)
		}
		if err := scanReasonRowsWith(rows, key, int64(viewer), int64(party), true, nil, func(r reasonRow) error { got = r.Hash; return nil }); err != nil {
			t.Fatal(err)
		}
		return got
	}
	if got := byID[id(r3)].ReviewHash; got != hashFor(s.admin.ID, gone.ID, r3.ID) {
		t.Errorf("the hash is not (admin, account)'s: %s", got)
	}
	second := loginAPI(t, s.app, "erase-second", erasurePW)
	_, secondPage, _ := accountPage(t, second, gone.ID, nil)
	parties := 0
	for _, it := range secondPage.Items {
		if it.ReviewerIsParty {
			parties++
		}
		if it.ReviewHash == byID[it.RequestID].ReviewHash {
			t.Errorf("another admin got the same hash for %s", it.RequestID)
		}
	}
	if len(secondPage.Items) != 5 || parties != 0 {
		t.Errorf("another admin sees %d items, party to %d, want 5 and 0", len(secondPage.Items), parties)
	}

	// The other tombstone's page for the same request: its own side, ref and hash.
	_, page2, _ := accountPage(t, s.ac, gone2.ID, nil)
	if len(page2.Items) != 1 || page2.Items[0].RequestID != id(r3) {
		t.Fatalf("the counterpart's page: %+v", page2.Items)
	}
	it := page2.Items[0]
	if it.Role != "subject" || it.PartyRef != gone2Ref || it.CounterpartName != "Deleted account" || !it.CounterpartDeleted || it.CounterpartRef != goneRef {
		t.Errorf("the counterpart's item: %+v", it)
	}
	if it.ReviewHash == byID[id(r3)].ReviewHash {
		t.Error("two parties of one request got the same hash")
	}

	// One view audit per nonempty page: the three above, never text.
	audits := viewAudits(t, s.app.DB)
	if len(audits) != 3 {
		t.Fatalf("%d view audits after three nonempty pages, want 3", len(audits))
	}
	first := audits[0]
	if first.ActorID == nil || *first.ActorID != s.admin.ID || first.TargetType != "user" || first.TargetID != gone.ID || first.IP != "" ||
		first.MetaJSON != `{"count": 5, "source": "legacy_review"}` {
		t.Errorf("view row: %+v", first)
	}
	if audits[1].ActorID == nil || *audits[1].ActorID != s.second.ID || audits[2].TargetID != gone2.ID {
		t.Errorf("view rows by viewer and target: %+v %+v", audits[1], audits[2])
	}
	for _, a := range audits {
		if strings.Contains(a.MetaJSON, "identifying") {
			t.Error("a view audit holds reason text")
		}
	}

	// An account with nothing: an empty unit, no reviewRequired, no audit.
	before := len(viewAudits(t, s.app.DB))
	code, page, body = accountPage(t, s.ac, bare.ID, nil)
	if code != http.StatusOK || body != `{"items":[],"next":null}` || page.ReviewRequired {
		t.Errorf("an empty account: %d %s, want exactly empty items and a null next", code, body)
	}
	if n := len(viewAudits(t, s.app.DB)); n != before {
		t.Errorf("an empty page wrote a view audit (%d, was %d)", n, before)
	}

	// A decided pair leaves its own account's page and no other: the deleted
	// counterpart still lists the request until it is decided itself.
	legacyMark(t, s.app.DB, int64(r3.ID), gone.ID, policy.RequestContact, true)
	_, page, _ = accountPage(t, s.ac, gone.ID, nil)
	if want := []string{id(r1), id(r2), id(r4), id(r5)}; !reflect.DeepEqual(requestIDs(page.Items), want) {
		t.Errorf("after deciding (r3, account): %v, want %v", requestIDs(page.Items), want)
	}
	if got := viewAudits(t, s.app.DB); got[len(got)-1].MetaJSON != `{"count": 4, "source": "legacy_review"}` {
		t.Errorf("the page's audit count is not what it showed: %s", got[len(got)-1].MetaJSON)
	}
	if _, page2, _ = accountPage(t, s.ac, gone2.ID, nil); len(page2.Items) != 1 {
		t.Errorf("the counterpart lost the request when only the other side was decided: %v", requestIDs(page2.Items))
	}
	legacyMark(t, s.app.DB, int64(r3.ID), gone2.ID, policy.RequestContact, false)
	if _, page2, _ = accountPage(t, s.ac, gone2.ID, nil); len(page2.Items) != 0 || page2.ReviewRequired {
		t.Errorf("a decided counterpart still lists the request: %+v", page2)
	}
}

// An id that is not a deleted account is 404 and reads no request; the actor is
// decided first, so an admin who may not review cannot tell a deleted account
// from a live one by the answer.
func TestLegacyAccountPageNotADeletedAccount(t *testing.T) {
	s := newLegacyScene(t, 3)
	gone, live := s.users[0], s.users[1]
	s.ask(t, gone, live, 1)
	s.bury(t, gone)
	statements := recordContactRequestStatements(t, s.app.DB)

	var maxID uint
	if err := s.app.DB.Raw("SELECT max(id) FROM users").Scan(&maxID).Error; err != nil {
		t.Fatal(err)
	}
	for name, id := range map[string]string{
		"a live account":      itoa(live.ID),
		"no such user":        itoa(maxID + 1000),
		"zero":                "0",
		"a leading zero":      "0" + itoa(gone.ID),
		"a plus sign":         "+" + itoa(gone.ID),
		"negative":            "-" + itoa(gone.ID),
		"not a number":        "abc",
		"a fraction":          itoa(gone.ID) + ".0",
		"beyond 63 bits":      "9223372036854775808",
		"beyond 64 bits":      "18446744073709551616",
		"a signed boundary":   "-9223372036854775808",
		"whitespace":          "%20" + itoa(gone.ID),
		"a hex number":        "0x" + itoa(gone.ID),
		"the user's own id":   itoa(s.admin.ID),
		"a max 63-bit number": "9223372036854775807",
	} {
		w := s.ac.do("POST", "/api/v1/admin/erasure-reasons/legacy/"+id+"/page", map[string]any{"after": nil}, true)
		if w.Code != http.StatusNotFound || strings.Contains(w.Body.String(), "identifying") || strings.Contains(w.Body.String(), "items") {
			t.Errorf("%s: %d %s, want 404 and nothing else", name, w.Code, w.Body.String())
		}
	}
	// Only the ids that reached the transaction (a well-formed one) read the
	// table's scanner: none of the above did, since none was a deleted account.
	if n := len(scannerStatements(statements())); n != 0 {
		t.Errorf("%d scanner statements for ids that are not deleted accounts, want none", n)
	}
	if n := len(viewAudits(t, s.app.DB)); n != 0 {
		t.Errorf("%d view audits for ids that are not deleted accounts", n)
	}
	// The deleted account itself works, so the 404s above were about the id.
	if code, page, body := accountPage(t, s.ac, gone.ID, nil); code != http.StatusOK || len(page.Items) != 1 {
		t.Errorf("the deleted account: %d %s", code, body)
	}
}

// Who may read an account's text: an active adult admin. Everyone else is
// refused before the account or any request is read (no statement over
// contact_requests, no audit row), with the answer discovery gives.
func TestLegacyAccountPageAuthorization(t *testing.T) {
	setup := func(t *testing.T) (legacyScene, func() []reasonStatement) {
		s := newLegacyScene(t, 2)
		s.ask(t, s.users[0], s.users[1], 1)
		s.bury(t, s.users[0])
		return s, recordContactRequestStatements(t, s.app.DB)
	}
	noText := func(t *testing.T, body string) {
		t.Helper()
		for _, leak := range []string{"identifying", "items", "next", "count", "reviewHash"} {
			if strings.Contains(body, leak) {
				t.Errorf("the refusal leaks %q: %s", leak, body)
			}
		}
	}
	for _, id := range []string{"a deleted account", "a live account", "no such user"} {
		t.Run("a known-17 admin, "+id, func(t *testing.T) {
			s, statements := setup(t)
			if err := s.app.DB.Exec("UPDATE users SET birth_date = ? WHERE id = ?", time.Now().AddDate(-17, 0, 0), s.admin.ID).Error; err != nil {
				t.Fatal(err)
			}
			target := map[string]uint{"a deleted account": s.users[0].ID, "a live account": s.users[1].ID, "no such user": 999999}[id]
			code, _, body := accountPage(t, s.ac, target, nil)
			if code != http.StatusForbidden || codeOf2(t, body) != "reason_review_not_allowed" {
				t.Fatalf("%d %s, want 403 reason_review_not_allowed whatever the id", code, body)
			}
			noText(t, body)
			if n := len(statements()); n != 0 {
				t.Errorf("%d statements over contact_requests before the refusal, want none", n)
			}
			if n := len(viewAudits(t, s.app.DB)); n != 0 {
				t.Errorf("%d view audits for a refused admin", n)
			}
		})
	}
	t.Run("a teen admin", func(t *testing.T) {
		s, statements := setup(t)
		if err := s.app.DB.Exec("UPDATE users SET account_type = ?, birth_date = ? WHERE id = ?", models.AccountTeen, time.Now().AddDate(-15, 0, 0), s.admin.ID).Error; err != nil {
			t.Fatal(err)
		}
		code, _, body := accountPage(t, s.ac, s.users[0].ID, nil)
		if code != http.StatusForbidden || codeOf2(t, body) != "reason_review_not_allowed" {
			t.Fatalf("%d %s, want 403 reason_review_not_allowed", code, body)
		}
		noText(t, body)
		if len(statements()) != 0 || len(viewAudits(t, s.app.DB)) != 0 {
			t.Error("a refused teen read requests or wrote an audit row")
		}
	})
	t.Run("an adult admin of unknown age", func(t *testing.T) {
		s, _ := setup(t)
		if err := s.app.DB.Exec("UPDATE users SET birth_date = NULL WHERE id = ?", s.admin.ID).Error; err != nil {
			t.Fatal(err)
		}
		if code, page, body := accountPage(t, s.ac, s.users[0].ID, nil); code != http.StatusOK || len(page.Items) != 1 {
			t.Fatalf("%d %s, want the page: an unknown age is not under 18", code, body)
		}
	})
	t.Run("a disabled admin is signed out", func(t *testing.T) {
		s, statements := setup(t)
		if err := s.app.DB.Exec("UPDATE users SET status = 'disabled' WHERE id = ?", s.admin.ID).Error; err != nil {
			t.Fatal(err)
		}
		code, _, body := accountPage(t, s.ac, s.users[0].ID, nil)
		if code != http.StatusUnauthorized {
			t.Fatalf("%d %s, want 401", code, body)
		}
		noText(t, body)
		if len(statements()) != 0 {
			t.Error("a signed-out admin read requests")
		}
	})
	t.Run("a non-admin", func(t *testing.T) {
		s, statements := setup(t)
		newTestUser(t, s.app, "legacy-member", erasurePW, models.RoleMember)
		mc := loginAPI(t, s.app, "legacy-member", erasurePW)
		code, _, body := accountPage(t, mc, s.users[0].ID, nil)
		if code != http.StatusForbidden || codeOf2(t, body) == "reason_review_not_allowed" {
			t.Fatalf("%d %s, want a plain 403", code, body)
		}
		noText(t, body)
		if len(statements()) != 0 || len(viewAudits(t, s.app.DB)) != 0 {
			t.Error("a non-admin read requests or wrote an audit row")
		}
	})
	t.Run("a signed-out client", func(t *testing.T) {
		s, _ := setup(t)
		// A client with no session has no csrf token either, so the token check
		// may answer first; either way nothing is read.
		w := newAPIClient(t, s.app).do("POST", legacyPagePath(s.users[0].ID), map[string]any{"after": nil}, false)
		if (w.Code != http.StatusUnauthorized && w.Code != http.StatusForbidden) || strings.Contains(w.Body.String(), "identifying") {
			t.Fatalf("%d %s, want 401 or 403", w.Code, w.Body.String())
		}
	})
	t.Run("a post without the csrf token", func(t *testing.T) {
		s, statements := setup(t)
		w := s.ac.do("POST", legacyPagePath(s.users[0].ID), map[string]any{"after": nil}, false)
		if w.Code != http.StatusForbidden || codeOf2(t, w.Body.String()) != "session_expired" {
			t.Fatalf("%d %s, want the csrf refusal", w.Code, w.Body.String())
		}
		noText(t, w.Body.String())
		if len(statements()) != 0 || len(viewAudits(t, s.app.DB)) != 0 {
			t.Error("a post without a token read requests or wrote an audit row")
		}
	})
	t.Run("a get is not a route", func(t *testing.T) {
		s, _ := setup(t)
		w := s.ac.do("GET", legacyPagePath(s.users[0].ID), nil, false)
		if w.Code == http.StatusOK || strings.Contains(w.Body.String(), "identifying") {
			t.Fatalf("%d %s: the account page must be a POST", w.Code, w.Body.String())
		}
	})
}

// The body and the cursor: signed ids in decimal strings, a bad one 400 before
// anything is read.
func TestLegacyAccountPageBodyAndCursor(t *testing.T) {
	s := newLegacyScene(t, 2)
	gone, live := s.users[0], s.users[1]
	s.ask(t, gone, live, 1)
	s.bury(t, gone)
	statements := recordContactRequestStatements(t, s.app.DB)
	for name, body := range map[string]any{
		"a number":           map[string]any{"after": 5},
		"an object":          map[string]any{"after": map[string]any{}},
		"not json":           rawJSON(`{"after":`),
		"no body":            nil,
		"a json array":       rawJSON(`[]`),
		"a non-canonical id": map[string]any{"after": "01"},
		"a plus sign":        map[string]any{"after": "+1"},
		"empty":              map[string]any{"after": ""},
		"a space":            map[string]any{"after": " 1"},
		"a fraction":         map[string]any{"after": "1.5"},
		"hex":                map[string]any{"after": "0x1"},
		"negative zero":      map[string]any{"after": "-0"},
		"beyond int64":       map[string]any{"after": "9223372036854775808"},
		"below int64":        map[string]any{"after": "-9223372036854775809"},
	} {
		w := s.ac.do("POST", legacyPagePath(gone.ID), body, true)
		if w.Code != http.StatusBadRequest || strings.Contains(w.Body.String(), "identifying") {
			t.Errorf("%s: %d %s, want 400", name, w.Code, w.Body.String())
		}
	}
	if len(statements()) != 0 || len(viewAudits(t, s.app.DB)) != 0 {
		t.Error("a bad body read requests or wrote a view audit")
	}
	// No "after" key and a null one are both the start.
	for name, body := range map[string]any{"missing": map[string]any{}, "null": map[string]any{"after": nil}} {
		w := s.ac.do("POST", legacyPagePath(gone.ID), body, true)
		if w.Code != http.StatusOK {
			t.Errorf("after %s: %d %s", name, w.Code, w.Body.String())
		}
	}
	for _, good := range []string{"0", "-1", "9223372036854775807", "-9223372036854775808"} {
		g := good
		if code, _, body := accountPage(t, s.ac, gone.ID, &g); code != http.StatusOK {
			t.Errorf("after %s: %d %s, want 200", good, code, body)
		}
	}
}

// Paging an account: the cursor boundaries over signed ids, the 100-item cap, a
// unit that spends its budget before finding anything, and the union of the
// pages against the reference query. Every page's ids are decimal strings.
func TestLegacyAccountPageCursorBoundaries(t *testing.T) {
	t.Run("signed boundary ids, one request a page", func(t *testing.T) {
		s := newLegacyScene(t, 3)
		gone, live := s.users[0], s.users[1]
		ids := []int64{math.MinInt64, -1, 0, math.MaxInt64}
		for i, want := range ids {
			r := s.ask(t, gone, live, i)
			s.setID(t, r.ID, want)
		}
		// Another account's request between them never enters this account's pages.
		s.ask(t, s.users[2], live, 9)
		s.bury(t, gone)
		setScanBounds(t, 50000, 256, 1)
		var chain []string
		var after *string
		for i := 0; i < 10; i++ {
			code, page, body := accountPage(t, s.ac, gone.ID, after)
			if code != http.StatusOK || len(page.Items) > 1 {
				t.Fatalf("page %d: %d %s", i, code, body)
			}
			chain = append(chain, requestIDs(page.Items)...)
			if page.Next == nil {
				break
			}
			if len(page.Items) == 1 && *page.Next != page.Items[0].RequestID {
				t.Fatalf("page %d: next %s is not the last request returned %s", i, *page.Next, page.Items[0].RequestID)
			}
			after = page.Next
		}
		if want := []string{"-9223372036854775808", "-1", "0", "9223372036854775807"}; !reflect.DeepEqual(chain, want) {
			t.Fatalf("the chain %v, want %v", chain, want)
		}
		if n := len(viewAudits(t, s.app.DB)); n != 4 {
			t.Errorf("%d view audits for four pages of one item, want 4", n)
		}
	})

	t.Run("the 100th and 101st request", func(t *testing.T) {
		s := newLegacyScene(t, 2)
		gone, live := s.users[0], s.users[1]
		var want []string
		for i := 0; i < 101; i++ {
			want = append(want, itoa(s.ask(t, gone, live, i).ID))
		}
		s.bury(t, gone)
		code, page, body := accountPage(t, s.ac, gone.ID, nil)
		if code != http.StatusOK || len(page.Items) != 100 || page.Next == nil || *page.Next != want[99] {
			t.Fatalf("first page: %d items=%d next=%v: %s", code, len(page.Items), page.Next, body)
		}
		code, last, body := accountPage(t, s.ac, gone.ID, page.Next)
		if code != http.StatusOK || len(last.Items) != 1 || last.Next != nil || !last.ReviewRequired {
			t.Fatalf("second page: %d items=%d next=%v: %s", code, len(last.Items), last.Next, body)
		}
		got := append(requestIDs(page.Items), requestIDs(last.Items)...)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("the union of both pages has the wrong ids or order")
		}
		// The audit counts what each page showed.
		audits := viewAudits(t, s.app.DB)
		if len(audits) != 2 || audits[0].MetaJSON != `{"count": 100, "source": "legacy_review"}` || audits[1].MetaJSON != `{"count": 1, "source": "legacy_review"}` {
			t.Errorf("view audits %+v", audits)
		}
	})

	t.Run("a spent unit with nothing to show leads on", func(t *testing.T) {
		s := newLegacyScene(t, 3)
		gone, live := s.users[0], s.users[1]
		// Two other people's requests, then this account's: a unit of two rows
		// finds nothing the first time and the cursor moves past them.
		s.ask(t, live, s.users[2], 1)
		s.ask(t, live, s.users[2], 2)
		mine := s.ask(t, gone, live, 3)
		s.bury(t, gone)
		setScanBounds(t, 2, 1, 100)
		before := len(viewAudits(t, s.app.DB))
		code, page, body := accountPage(t, s.ac, gone.ID, nil)
		if code != http.StatusOK || len(page.Items) != 0 || page.Next == nil || page.ReviewRequired {
			t.Fatalf("first unit: %d %s, want no items, reviewRequired omitted and a next", code, body)
		}
		if len(viewAudits(t, s.app.DB)) != before {
			t.Error("an empty unit wrote a view audit")
		}
		code, page, body = accountPage(t, s.ac, gone.ID, page.Next)
		if code != http.StatusOK || !reflect.DeepEqual(requestIDs(page.Items), []string{itoa(mine.ID)}) {
			t.Fatalf("second unit: %d %s, want the request", code, body)
		}
	})

	t.Run("pages equal the reference over a mix", func(t *testing.T) {
		s := newLegacyScene(t, 5)
		r := rand.New(rand.NewSource(301))
		for i := 0; i < 160; i++ {
			a, b := s.users[r.Intn(len(s.users))], s.users[r.Intn(len(s.users))]
			if a.ID == b.ID {
				continue
			}
			req := s.ask(t, a, b, i)
			if r.Intn(5) == 0 {
				if err := s.app.DB.Exec("UPDATE contact_requests SET reason = '' WHERE id = ?", req.ID).Error; err != nil {
					t.Fatal(err)
				}
			}
		}
		s.bury(t, s.users[0], s.users[1], s.users[2])
		for _, u := range s.users[:3] {
			for _, it := range legacyReference(t, s.app.DB) {
				if it.Party == int64(u.ID) && r.Intn(4) == 0 {
					var purpose string
					if err := s.app.DB.Raw("SELECT purpose FROM contact_requests WHERE id = ?", it.Request).Scan(&purpose).Error; err != nil {
						t.Fatal(err)
					}
					legacyMark(t, s.app.DB, it.Request, u.ID, purpose, r.Intn(2) == 0)
				}
			}
		}
		setScanBounds(t, 50000, 16, 7)
		ref := legacyReference(t, s.app.DB)
		for _, u := range s.users[:3] {
			var want []string
			for _, it := range ref {
				if it.Party == int64(u.ID) {
					want = append(want, strconv.FormatInt(it.Request, 10))
				}
			}
			var got []string
			var after *string
			for pages := 0; pages < 100; pages++ {
				code, page, body := accountPage(t, s.ac, u.ID, after)
				if code != http.StatusOK || len(page.Items) > 7 {
					t.Fatalf("account %d page %d: %d %d items %s", u.ID, pages, code, len(page.Items), body)
				}
				got = append(got, requestIDs(page.Items)...)
				if page.Next == nil {
					break
				}
				after = page.Next
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("account %d: pages gave %v, the reference %v", u.ID, got, want)
			}
		}
	})
}

// The page is one transaction that ends with the actor seal: the early actor
// read first, the account and the scan after it, the view audit before the
// seal, and nothing after the seal.
func TestLegacyAccountPageStatementOrder(t *testing.T) {
	s := newLegacyScene(t, 2)
	gone, live := s.users[0], s.users[1]
	s.ask(t, gone, live, 1)
	s.bury(t, gone)
	statements := recordTxStatements(t, s.app.DB)
	if code, page, body := accountPage(t, s.ac, gone.ID, nil); code != http.StatusOK || len(page.Items) != 1 {
		t.Fatalf("%d %s", code, body)
	}
	const sealSQL = "password_hash, session_version, logout_version FROM users WHERE id = $1 FOR SHARE NOWAIT"
	all := statements()
	var pool any
	for _, st := range all {
		if strings.Contains(st.sql, sealSQL) || strings.Contains(st.sql, strings.ReplaceAll(sealSQL, "$1", "?")) {
			if pool != nil {
				t.Fatal("two seal statements in one request")
			}
			pool = st.pool
		}
	}
	if pool == nil {
		t.Fatal("no seal statement")
	}
	var tx []string
	for _, st := range all {
		if st.pool == pool {
			tx = append(tx, st.sql)
		}
	}
	at := func(sub string) int {
		for i, sql := range tx {
			if strings.Contains(sql, sub) {
				return i
			}
		}
		return -1
	}
	actorRead, account, scan, items, auditAt, seal := at(`FROM "users"`), at("SELECT id, deleted_at FROM users WHERE id"), at(reasonUnitPrefix), at("AND reason <> ''"), at(`INSERT INTO "audit_logs"`), len(tx)-1
	if !strings.Contains(tx[seal], "FOR SHARE NOWAIT") {
		t.Fatalf("the transaction does not end with the seal: %v", tx)
	}
	if !(actorRead >= 0 && actorRead < account && account < scan && scan < items && items < auditAt && auditAt < seal) {
		t.Errorf("statement order: actor read %d, account %d, scan %d, items %d, audit %d, seal %d in %v", actorRead, account, scan, items, auditAt, seal, tx)
	}
}

// The seal decides the page: a stale session, a cleared password or an admin who
// stopped being allowed to review commits nothing, and its answer carries no
// item. The view audit is in the same transaction, so a refused page leaves none.
func TestLegacyAccountPageSealDecides(t *testing.T) {
	for name, tc := range map[string]struct {
		code   int
		reason string
		hook   func(s legacyScene) func(*gorm.DB)
	}{
		"a stale session": {http.StatusUnauthorized, "signed_out", func(s legacyScene) func(*gorm.DB) {
			return func(*gorm.DB) {
				if err := s.app.DB.Exec("UPDATE users SET session_version = session_version + 1 WHERE id = ?", s.admin.ID).Error; err != nil {
					t.Error(err)
				}
			}
		}},
		"a logout": {http.StatusUnauthorized, "signed_out", func(s legacyScene) func(*gorm.DB) {
			return func(*gorm.DB) {
				if err := s.app.DB.Exec("UPDATE users SET logout_version = logout_version + 1 WHERE id = ?", s.admin.ID).Error; err != nil {
					t.Error(err)
				}
			}
		}},
		"a cleared password": {http.StatusUnauthorized, "signed_out", func(s legacyScene) func(*gorm.DB) {
			return func(*gorm.DB) {
				if err := s.app.DB.Exec("UPDATE users SET password_hash = NULL WHERE id = ?", s.admin.ID).Error; err != nil {
					t.Error(err)
				}
			}
		}},
		"an admin who became known to be 17": {http.StatusForbidden, "reason_review_not_allowed", func(s legacyScene) func(*gorm.DB) {
			return func(*gorm.DB) {
				if err := s.app.DB.Exec("UPDATE users SET birth_date = ? WHERE id = ?", time.Now().AddDate(-17, 0, 0), s.admin.ID).Error; err != nil {
					t.Error(err)
				}
			}
		}},
		"an admin who became a member": {http.StatusUnauthorized, "signed_out", func(s legacyScene) func(*gorm.DB) {
			return func(*gorm.DB) {
				if err := s.app.DB.Exec("UPDATE users SET role = 'member', session_version = session_version + 1 WHERE id = ?", s.admin.ID).Error; err != nil {
					t.Error(err)
				}
			}
		}},
	} {
		t.Run(name, func(t *testing.T) {
			s := newLegacyScene(t, 2)
			s.ask(t, s.users[0], s.users[1], 1)
			s.bury(t, s.users[0])
			s.app.hooks.readBeforeSeal = tc.hook(s)
			code, _, body := accountPage(t, s.ac, s.users[0].ID, nil)
			if code != tc.code || codeOf2(t, body) != tc.reason {
				t.Fatalf("%d %s, want %d %q", code, body, tc.code, tc.reason)
			}
			for _, leak := range []string{"identifying", "items", "reviewHash"} {
				if strings.Contains(body, leak) {
					t.Errorf("the refusal leaks %q: %s", leak, body)
				}
			}
			if n := len(viewAudits(t, s.app.DB)); n != 0 {
				t.Errorf("%d view audits committed for a page the seal refused", n)
			}
		})
	}

	t.Run("an empty unit is decided by the seal too", func(t *testing.T) {
		s := newLegacyScene(t, 2)
		s.bury(t, s.users[0])
		s.app.hooks.readBeforeSeal = func(*gorm.DB) {
			if err := s.app.DB.Exec("UPDATE users SET birth_date = ? WHERE id = ?", time.Now().AddDate(-17, 0, 0), s.admin.ID).Error; err != nil {
				t.Error(err)
			}
		}
		code, _, body := accountPage(t, s.ac, s.users[0].ID, nil)
		if code != http.StatusForbidden || codeOf2(t, body) != "reason_review_not_allowed" || strings.Contains(body, "items") {
			t.Fatalf("%d %s, want 403 reason_review_not_allowed with no page", code, body)
		}
	})

	t.Run("the account is deleted account only until the read", func(t *testing.T) {
		// A live account that is deleted between requests is a different 404/200
		// each time: nothing is cached or remembered by the route.
		s := newLegacyScene(t, 2)
		s.ask(t, s.users[0], s.users[1], 1)
		if code, _, _ := accountPage(t, s.ac, s.users[0].ID, nil); code != http.StatusNotFound {
			t.Fatalf("a live account: %d, want 404", code)
		}
		s.bury(t, s.users[0])
		if code, page, body := accountPage(t, s.ac, s.users[0].ID, nil); code != http.StatusOK || len(page.Items) != 1 {
			t.Fatalf("after it was deleted: %d %s", code, body)
		}
	})
}

// A busy actor row is retried under the page's bounds and then 503 busy, with no
// item and no audit row; an unacknowledged rollback starts no second attempt
// (TestLegacyReadBusySealRollbackNotAcknowledged covers this route too).
func TestLegacyAccountPageSealBusy(t *testing.T) {
	old := busyRetryWindow
	busyRetryWindow = 30 * time.Second
	t.Cleanup(func() { busyRetryWindow = old })
	s := newLegacyScene(t, 2)
	s.ask(t, s.users[0], s.users[1], 1)
	s.bury(t, s.users[0])
	var attempts atomic.Int64
	s.app.hooks.readBeforeSeal = func(*gorm.DB) { attempts.Add(1) }
	hold, _ := beginOwnedTx(t, s.app.DB)
	if err := hold.Exec("SELECT id FROM users WHERE id = ? FOR NO KEY UPDATE", s.admin.ID).Error; err != nil {
		t.Fatal(err)
	}
	code, _, body := accountPage(t, s.ac, s.users[0].ID, nil)
	if code != http.StatusServiceUnavailable || codeOf2(t, body) != "reason_review_busy" || strings.Contains(body, "items") || strings.Contains(body, "identifying") {
		t.Fatalf("under the hold: %d %s, want 503 reason_review_busy", code, body)
	}
	if n := attempts.Load(); n != 3 {
		t.Fatalf("%d attempts, want 3", n)
	}
	if n := len(viewAudits(t, s.app.DB)); n != 0 {
		t.Fatalf("%d view audits after three rolled-back attempts, want none", n)
	}
	hold.Rollback()
	attempts.Store(0)
	code, page, body := accountPage(t, s.ac, s.users[0].ID, nil)
	if code != http.StatusOK || len(page.Items) != 1 || attempts.Load() != 1 {
		t.Fatalf("after the release: %d %s (%d attempts)", code, body, attempts.Load())
	}
	if n := len(viewAudits(t, s.app.DB)); n != 1 {
		t.Fatalf("%d view audits after one served page, want 1", n)
	}
}

// A failed statement is a failed page (503, retry the same cursor), never an
// empty one and never a half-written audit: the read of the account, of the
// scan's lookups, of the items, and the audit insert itself.
func TestLegacyAccountPageFailureIsNeverAnEmptyPage(t *testing.T) {
	for name, match := range map[string]func(string) bool{
		"the account read":    func(sql string) bool { return strings.Contains(sql, "SELECT id, deleted_at FROM users WHERE id") },
		"a scan lookup":       func(sql string) bool { return strings.Contains(sql, "unnest(") },
		"the facts lookup":    func(sql string) bool { return strings.Contains(sql, "LEFT JOIN users req") },
		"the item read":       func(sql string) bool { return strings.Contains(sql, "reason <> ''") && strings.Contains(sql, "ANY(") },
		"the view audit":      func(sql string) bool { return strings.Contains(sql, `"audit_logs"`) },
		"the context lookups": func(sql string) bool { return strings.Contains(sql, `FROM "groups"`) },
	} {
		t.Run(name, func(t *testing.T) {
			s := newLegacyScene(t, 2)
			group := newTestGroup(t, s.app, s.users[1], "Legacy Group", models.GroupActive)
			erasureMakeRequest(t, s.app.DB, policy.RequestContact, policy.ContactWithdrawn, s.users[0].ID, s.users[1].ID, group.ID, 0, 0, 1, "")
			s.bury(t, s.users[0])
			stop := failStatements(t, s.app.DB, match)
			code, _, body := accountPage(t, s.ac, s.users[0].ID, nil)
			stop()
			if code != http.StatusServiceUnavailable || codeOf2(t, body) != "reason_review_busy" {
				t.Fatalf("%d %s, want 503 reason_review_busy", code, body)
			}
			for _, leak := range []string{"items", "identifying", "next", "reviewRequired"} {
				if strings.Contains(body, leak) {
					t.Errorf("the failure leaks %q: %s", leak, body)
				}
			}
			if n := len(viewAudits(t, s.app.DB)); n != 0 {
				t.Errorf("%d view audits after a failed page, want none", n)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// The save (T-301 S4): PUT /admin/erasure-reasons/legacy/:userId.
//
// The rig is an attemptScene (admin "admin", the test attempt clock, mint,
// settle, ledger and receipts) whose kind is legacy_reason_save, with a deleted
// account's requests made straight in the database.
// ---------------------------------------------------------------------------

func legacySavePath(id uint) string { return "/api/v1/admin/erasure-reasons/legacy/" + itoa(id) }

type saveRig struct{ *attemptScene }

func newSaveRig(t *testing.T) *saveRig {
	t.Helper()
	s := newAttemptScene(t)
	s.kind = attemptKindLegacyReasonSave
	return &saveRig{s}
}

// newProxiedSaveRig serves the routes through pgproxy, like newProxiedAttemptScene.
func newProxiedSaveRig(t *testing.T) *saveRig {
	t.Helper()
	s := newProxiedAttemptScene(t)
	s.kind = attemptKindLegacyReasonSave
	return &saveRig{s}
}

func (r *saveRig) bury(users ...*models.User) {
	r.t.Helper()
	for _, u := range users {
		if err := r.direct.Exec("UPDATE users SET deleted_at = now() WHERE id = ?", u.ID).Error; err != nil {
			r.t.Fatal(err)
		}
	}
}

// ask makes a terminal contact request with a reason between two people.
func (r *saveRig) ask(requester, subject *models.User, n int) models.ContactRequest {
	r.t.Helper()
	return erasureMakeRequest(r.t, r.direct, policy.RequestContact, policy.ContactWithdrawn, requester.ID, subject.ID, 1, 0, 0, n, "")
}

// askAdd makes a terminal group_add request with a reason.
func (r *saveRig) askAdd(requester, subject *models.User, n int) models.ContactRequest {
	r.t.Helper()
	return erasureMakeRequest(r.t, r.direct, policy.RequestGroupAdd, policy.ContactWithdrawn, requester.ID, subject.ID, 1, 0, 0, n, "")
}

// itemsAs is the deleted account's whole listing for c: every page from the start.
func (r *saveRig) itemsAs(c *apiClient, account uint) []reasonItemJSON {
	r.t.Helper()
	var out []reasonItemJSON
	var after *string
	for {
		code, page, body := accountPage(r.t, c, account, after)
		if code != http.StatusOK {
			r.t.Fatalf("page: %d %s", code, body)
		}
		out = append(out, page.Items...)
		if page.Next == nil {
			return out
		}
		after = page.Next
	}
}

func (r *saveRig) items(account uint) []reasonItemJSON { return r.itemsAs(r.c, account) }

// choose pairs each item, in order, with its choice; "" leaves the item out.
func choose(items []reasonItemJSON, choices ...string) []map[string]string {
	out := []map[string]string{}
	for i, it := range items {
		if i < len(choices) && choices[i] != "" {
			out = append(out, map[string]string{"requestId": it.RequestID, "reviewHash": it.ReviewHash, "choice": choices[i]})
		}
	}
	return out
}

func (r *saveRig) putAs(c *apiClient, account uint, body any) *httptest.ResponseRecorder {
	return c.do("PUT", legacySavePath(account), body, true)
}

func (r *saveRig) send(account uint, id string, decisions any) *httptest.ResponseRecorder {
	return r.putAs(r.c, account, map[string]any{"attemptId": id, "decisions": decisions})
}

// save mints an id for the account and sends the decisions with it.
func (r *saveRig) save(account uint, decisions any) (string, *httptest.ResponseRecorder) {
	r.t.Helper()
	id := r.mint(account)
	return id, r.send(account, id, decisions)
}

type decisionRow struct {
	ID         int64
	ActorID    *int64
	Action     string
	TargetType string
	TargetID   int64
	Meta       map[string]any
	Raw        string
}

// decisionRows are the decision audit rows (kept and removed, both kinds), in
// the order they were written. Request ids are signed, so no uint model is used.
func (r *saveRig) decisionRows() []decisionRow {
	r.t.Helper()
	rows, err := r.direct.Raw(`SELECT id, actor_id, action, target_type, target_id, meta_json::text FROM audit_logs WHERE action IN (?, ?, ?, ?) ORDER BY id`,
		audit.ContactReasonKept, audit.ContactReasonRemoved, audit.AddReasonKept, audit.AddReasonRemoved).Rows()
	if err != nil {
		r.t.Fatal(err)
	}
	defer rows.Close()
	var out []decisionRow
	for rows.Next() {
		var d decisionRow
		if err := rows.Scan(&d.ID, &d.ActorID, &d.Action, &d.TargetType, &d.TargetID, &d.Raw); err != nil {
			r.t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(d.Raw), &d.Meta); err != nil {
			r.t.Fatal(err)
		}
		out = append(out, d)
	}
	return out
}

// decisionsFor are the rows that name this request and tombstone.
func (r *saveRig) decisionsFor(request int64, party uint) []decisionRow {
	var out []decisionRow
	for _, d := range r.decisionRows() {
		if d.TargetID == request && d.Meta["erasedUserId"] == float64(party) {
			out = append(out, d)
		}
	}
	return out
}

func (r *saveRig) reasonOf(request int64) string {
	r.t.Helper()
	var reason string
	if err := r.direct.Raw("SELECT reason FROM contact_requests WHERE id = ?", request).Scan(&reason).Error; err != nil {
		r.t.Fatal(err)
	}
	return reason
}

// written is everything a save can change, as one string: every request row,
// every audit row but the page views, the receipts and the restore counter.
func (r *saveRig) written() string {
	r.t.Helper()
	return dumpRows(r.t, r.direct, "contact_requests", "id", "true") + "\n" +
		dumpRows(r.t, r.direct, "audit_logs", "id", "action <> ?", audit.UserErasureReasonsViewed) + "\n" +
		fmt.Sprintf("receipts=%d counter=%d", r.receipts(), r.counter())
}

func wantSaveCode(t *testing.T, w *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if w.Code != status || codeOf2(t, w.Body.String()) != code {
		t.Fatalf("%d %s, want %d %q", w.Code, w.Body.String(), status, code)
	}
}

// wantUnchanged fails when a refused save changed anything or left the id in a
// state other than want.
func (r *saveRig) wantUnchanged(before string, id string, account uint, want attemptState) {
	r.t.Helper()
	if got := r.written(); got != before {
		r.t.Fatalf("a refused save wrote something:\nbefore: %s\nafter:  %s", before, got)
	}
	if got := r.state(id, account); got != want {
		r.t.Fatalf("the attempt id is in state %v, want %v", got, want)
	}
}

// The deletion's budget is the save's: 40 seconds, one value on one name.
func TestLegacySaveBudgetIsTheDeletionBudget(t *testing.T) {
	if legacySaveBudget != 40*time.Second || adminDeleteBudget != 40*time.Second {
		t.Fatalf("legacySaveBudget = %v, adminDeleteBudget = %v, want both 40s", legacySaveBudget, adminDeleteBudget)
	}
	if legacySaveMaxDecisions != reasonPageRequests {
		t.Fatalf("a save takes %d decisions and a page holds %d", legacySaveMaxDecisions, reasonPageRequests)
	}
}

// A save decides each named reason as it was told: kept ones stay, removed ones
// are blanked, every decision writes one signed audit row of exactly the
// documented shape, and the pair leaves the list. The id ends applied with one
// receipt, and a resend is refused.
func TestLegacySaveDecidesEachReason(t *testing.T) {
	r := newSaveRig(t)
	gone, live, other := r.target("gone"), r.target("live"), r.target("other")
	c1 := r.ask(gone, live, 1)         // the account as requester
	a1 := r.askAdd(live, gone, 2)      // the account as subject, an add request
	c2 := r.ask(r.admin, gone, 3)      // the admin is a party
	c3 := r.ask(gone, other, 4)        // kept
	untouched := r.ask(live, other, 5) // someone else's request
	r.bury(gone)
	items := r.items(gone.ID)
	if got, want := strings.Join(requestIDs(items), ","), fmt.Sprintf("%d,%d,%d,%d", c1.ID, a1.ID, c2.ID, c3.ID); got != want {
		t.Fatalf("listing = %s, want %s", got, want)
	}
	var seals atomic.Int64
	r.app.hooks.sealAttempts = &seals
	floor := r.app.restoreFenceFloor()
	counter := r.counter()
	id, w := r.save(gone.ID, choose(items, "keep", "remove", "remove", "keep"))
	if w.Code != http.StatusOK || strings.TrimSpace(w.Body.String()) != `{"kept":2,"removed":2}` {
		t.Fatalf("save: %d %s, want 200 {kept:2, removed:2}", w.Code, w.Body.String())
	}
	if got := seals.Load(); got != 1 {
		t.Fatalf("%d seals, want 1", got)
	}
	for _, c := range []struct {
		id     int64
		remove bool
	}{{int64(c1.ID), false}, {int64(a1.ID), true}, {int64(c2.ID), true}, {int64(c3.ID), false}} {
		if blank := r.reasonOf(c.id) == ""; blank != c.remove {
			t.Errorf("request %d: blank = %v, want %v", c.id, blank, c.remove)
		}
	}
	if r.reasonOf(int64(untouched.ID)) == "" {
		t.Error("someone else's request was blanked")
	}
	want := []struct {
		req             uint
		action, target  string
		role            string
		reviewerIsParty bool
	}{
		{c1.ID, audit.ContactReasonKept, "contact_request", "requester", false},
		{a1.ID, audit.AddReasonRemoved, "add_request", "subject", false},
		{c2.ID, audit.ContactReasonRemoved, "contact_request", "subject", true},
		{c3.ID, audit.ContactReasonKept, "contact_request", "requester", false},
	}
	rows := r.decisionRows()
	if len(rows) != len(want) {
		t.Fatalf("%d decision rows, want %d: %+v", len(rows), len(want), rows)
	}
	for i, wnt := range want {
		d := rows[i]
		expected := map[string]any{"erasedUserId": float64(gone.ID), "erasedRole": wnt.role, "purpose": "contact", "status": policy.ContactWithdrawn,
			"reviewerIsParty": wnt.reviewerIsParty, "source": reasonSourceLegacy, "reviewVersion": float64(1)}
		if wnt.target == "add_request" {
			expected["purpose"] = policy.RequestGroupAdd
		}
		if d.Action != wnt.action || d.TargetType != wnt.target || d.TargetID != int64(wnt.req) || d.ActorID == nil || *d.ActorID != int64(r.admin.ID) || !reflect.DeepEqual(d.Meta, expected) {
			t.Errorf("decision %d = %+v\nwant action %s on %s %d by %d with meta %v", i, d, wnt.action, wnt.target, wnt.req, r.admin.ID, expected)
		}
		for _, leak := range []string{"identifying", id, items[i].ReviewHash} {
			if strings.Contains(d.Raw, leak) {
				t.Errorf("decision %d's meta carries %q: %s", i, leak, d.Raw)
			}
		}
	}
	if r.state(id, gone.ID) != attemptApplied || r.receipts() != 1 || r.counter() != counter+1 || r.app.restoreFenceFloor() <= floor {
		t.Errorf("state %v, receipts %d, counter %d (was %d), floor %d (was %d): want applied, one receipt and the ticket raised", r.state(id, gone.ID), r.receipts(), r.counter(), counter, r.app.restoreFenceFloor(), floor)
	}
	if got := r.items(gone.ID); len(got) != 0 {
		t.Errorf("%d reasons are still listed after every one was decided", len(got))
	}
	if w := r.c.do("GET", legacyDiscoveryPath, nil, false); w.Code != http.StatusOK || strings.Contains(w.Body.String(), itoa(gone.ID)) {
		t.Errorf("discovery after the save: %d %s", w.Code, w.Body.String())
	}
	// The same id is used up: nothing runs twice.
	before := r.written()
	wantSaveCode(t, r.send(gone.ID, id, choose(items, "keep", "keep", "keep", "keep")), http.StatusConflict, "attempt_closed")
	if r.written() != before {
		t.Error("a resent id changed something")
	}
	wantSettle(t, r.settled(id, gone.ID), settleApplied, nil)
}

// A request between two deleted accounts is two decisions: the first Keep or
// Remove leaves the other account's side listed, a decided side cannot be
// decided again, and a Remove blanks the one reason for both.
func TestLegacySaveTwoTombstones(t *testing.T) {
	setup := func(t *testing.T) (*saveRig, *models.User, *models.User, models.ContactRequest) {
		r := newSaveRig(t)
		a, b := r.target("gone-a"), r.target("gone-b")
		req := r.ask(a, b, 1)
		r.bury(a, b)
		return r, a, b, req
	}
	t.Run("keep then keep", func(t *testing.T) {
		r, a, b, req := setup(t)
		itemsA, itemsB := r.items(a.ID), r.items(b.ID)
		if _, w := r.save(a.ID, choose(itemsA, "keep")); w.Code != http.StatusOK {
			t.Fatalf("keep for a: %d %s", w.Code, w.Body.String())
		}
		if got := r.items(a.ID); len(got) != 0 {
			t.Errorf("a's side is still listed: %v", requestIDs(got))
		}
		if got := r.items(b.ID); len(got) != 1 {
			t.Fatalf("b's side after a's keep: %v, want the request still listed", requestIDs(got))
		}
		// b's hash from before a's keep is still the row's hash: a keep changes no column.
		if _, w := r.save(b.ID, choose(itemsB, "keep")); w.Code != http.StatusOK {
			t.Fatalf("keep for b: %d %s", w.Code, w.Body.String())
		}
		if len(r.items(a.ID))+len(r.items(b.ID)) != 0 || r.reasonOf(int64(req.ID)) == "" {
			t.Error("two keeps must list nothing and blank nothing")
		}
		if n, m := len(r.decisionsFor(int64(req.ID), a.ID)), len(r.decisionsFor(int64(req.ID), b.ID)); n != 1 || m != 1 {
			t.Errorf("markers for a: %d, for b: %d, want one each with their own erasedUserId", n, m)
		}
	})
	t.Run("keep then keep again", func(t *testing.T) {
		r, a, _, req := setup(t)
		itemsA := r.items(a.ID)
		if _, w := r.save(a.ID, choose(itemsA, "keep")); w.Code != http.StatusOK {
			t.Fatal(w.Body.String())
		}
		before := r.written()
		id, w := r.save(a.ID, choose(itemsA, "keep"))
		wantSaveCode(t, w, http.StatusConflict, "reason_review_changed")
		r.wantUnchanged(before, id, a.ID, attemptRolledBack)
		if n := len(r.decisionsFor(int64(req.ID), a.ID)); n != 1 {
			t.Errorf("%d markers for a, want exactly the first", n)
		}
	})
	t.Run("keep then remove", func(t *testing.T) {
		r, a, b, req := setup(t)
		itemsA, itemsB := r.items(a.ID), r.items(b.ID)
		if _, w := r.save(a.ID, choose(itemsA, "keep")); w.Code != http.StatusOK {
			t.Fatal(w.Body.String())
		}
		if _, w := r.save(b.ID, choose(itemsB, "remove")); w.Code != http.StatusOK {
			t.Fatalf("remove for b: %d %s", w.Code, w.Body.String())
		}
		if r.reasonOf(int64(req.ID)) != "" {
			t.Error("the removed reason is still there")
		}
		rows := r.decisionRows()
		if len(rows) != 2 || rows[0].Action != audit.ContactReasonKept || rows[1].Action != audit.ContactReasonRemoved ||
			rows[0].Meta["erasedUserId"] != float64(a.ID) || rows[1].Meta["erasedUserId"] != float64(b.ID) {
			t.Errorf("decisions = %+v, want a kept then b removed", rows)
		}
	})
	t.Run("remove then keep", func(t *testing.T) {
		r, a, b, req := setup(t)
		itemsA, itemsB := r.items(a.ID), r.items(b.ID)
		if _, w := r.save(a.ID, choose(itemsA, "remove")); w.Code != http.StatusOK {
			t.Fatal(w.Body.String())
		}
		if got := r.items(b.ID); len(got) != 0 {
			t.Errorf("a blank reason is still listed for b: %v", requestIDs(got))
		}
		before := r.written()
		id, w := r.save(b.ID, choose(itemsB, "keep"))
		wantSaveCode(t, w, http.StatusConflict, "reason_review_changed")
		r.wantUnchanged(before, id, b.ID, attemptRolledBack)
		if n := len(r.decisionsFor(int64(req.ID), b.ID)); n != 0 {
			t.Errorf("%d decisions for b after the blank, want none", n)
		}
	})
	t.Run("a marker for the other account does not decide this one", func(t *testing.T) {
		r, a, b, req := setup(t)
		items := r.items(a.ID)
		legacyMark(t, r.direct, int64(req.ID), b.ID, policy.RequestContact, true)
		if _, w := r.save(a.ID, choose(items, "keep")); w.Code != http.StatusOK {
			t.Fatalf("a's own decision after b's marker: %d %s", w.Code, w.Body.String())
		}
	})
}

// A set that is not the one the admin was served is refused whole: nothing is
// written, no named request is touched and the id ends rolled back.
func TestLegacySaveRefusesAStaleSet(t *testing.T) {
	type env struct {
		r              *saveRig
		gone, live     *models.User
		gone2          *models.User
		items          []reasonItemJSON
		reqs           []models.ContactRequest
		otherAdminHash map[string]string
		wrongParty     map[string]string
	}
	setup := func(t *testing.T) env {
		r := newSaveRig(t)
		e := env{r: r, gone: r.target("gone"), live: r.target("live"), gone2: r.target("gone2")}
		e.reqs = []models.ContactRequest{r.ask(e.gone, e.live, 1), r.ask(e.live, e.gone, 2), r.ask(e.gone, e.live, 3)}
		both := r.ask(e.gone, e.gone2, 4) // for a hash made for another party
		r.bury(e.gone, e.gone2)
		e.items = r.items(e.gone.ID)
		// A second admin's hashes and the other tombstone's hash of the shared request.
		admin2 := newTestUser(t, r.app, "admin2", "pw", models.RoleAdmin)
		_ = admin2
		c2 := loginAPI(t, r.app, "admin2", "pw")
		e.otherAdminHash, e.wrongParty = map[string]string{}, map[string]string{}
		for _, it := range r.itemsAs(c2, e.gone.ID) {
			e.otherAdminHash[it.RequestID] = it.ReviewHash
		}
		for _, it := range r.items(e.gone2.ID) {
			e.wrongParty[it.RequestID] = it.ReviewHash
		}
		if e.wrongParty[strconv.FormatUint(uint64(both.ID), 10)] == "" {
			t.Fatal("the shared request is not in the other tombstone's listing")
		}
		e.items = append([]reasonItemJSON(nil), e.items...)
		return e
	}
	refuse := func(t *testing.T, e env, decisions any) {
		t.Helper()
		before := e.r.written()
		id, w := e.r.save(e.gone.ID, decisions)
		wantSaveCode(t, w, http.StatusConflict, "reason_review_changed")
		e.r.wantUnchanged(before, id, e.gone.ID, attemptRolledBack)
		// The refusal says nothing about which item changed or why.
		for _, leak := range []string{"identifying", "requestId", id} {
			if strings.Contains(w.Body.String(), leak) {
				t.Errorf("the refusal carries %q: %s", leak, w.Body.String())
			}
		}
	}
	// the three contact items of gone (the fourth, shared with gone2, is another case)
	own := func(e env) []reasonItemJSON {
		var out []reasonItemJSON
		for _, it := range e.items {
			if it.CounterpartDeleted {
				continue
			}
			out = append(out, it)
		}
		return out
	}
	t.Run("a reason blanked since it was served", func(t *testing.T) {
		e := setup(t)
		if err := e.r.direct.Exec("UPDATE contact_requests SET reason = '' WHERE id = ?", e.reqs[1].ID).Error; err != nil {
			t.Fatal(err)
		}
		refuse(t, e, choose(own(e), "keep", "remove", "keep"))
	})
	t.Run("a reason edited since it was served", func(t *testing.T) {
		e := setup(t)
		if err := e.r.direct.Exec("UPDATE contact_requests SET reason = 'edited' WHERE id = ?", e.reqs[2].ID).Error; err != nil {
			t.Fatal(err)
		}
		refuse(t, e, choose(own(e), "keep", "keep", "keep"))
	})
	t.Run("a marker added since it was served", func(t *testing.T) {
		e := setup(t)
		legacyMark(t, e.r.direct, int64(e.reqs[0].ID), e.gone.ID, policy.RequestContact, true)
		refuse(t, e, choose(own(e), "remove", "keep", "keep"))
	})
	t.Run("a hash made for another admin", func(t *testing.T) {
		e := setup(t)
		items := own(e)
		items[1].ReviewHash = e.otherAdminHash[items[1].RequestID]
		refuse(t, e, choose(items, "keep", "keep", "keep"))
	})
	t.Run("a hash made for another account's page", func(t *testing.T) {
		e := setup(t)
		var shared reasonItemJSON
		for _, it := range e.items {
			if it.CounterpartDeleted {
				shared = it
			}
		}
		shared.ReviewHash = e.wrongParty[shared.RequestID]
		refuse(t, e, choose([]reasonItemJSON{shared}, "keep"))
	})
	t.Run("a tampered hash", func(t *testing.T) {
		e := setup(t)
		items := own(e)
		h := []byte(items[2].ReviewHash)
		if h[0] == '0' {
			h[0] = '1'
		} else {
			h[0] = '0'
		}
		items[2].ReviewHash = string(h)
		refuse(t, e, choose(items, "keep", "keep", "keep"))
	})
	t.Run("a request that does not exist", func(t *testing.T) {
		e := setup(t)
		d := choose(own(e), "keep", "keep", "keep")
		d = append(d, map[string]string{"requestId": "987654321", "reviewHash": e.items[0].ReviewHash, "choice": "keep"})
		refuse(t, e, d)
	})
	t.Run("a request of someone else", func(t *testing.T) {
		e := setup(t)
		elsewhere := e.r.ask(e.live, e.r.target("third"), 9)
		d := choose(own(e), "keep", "keep", "keep")
		d = append(d, map[string]string{"requestId": strconv.FormatUint(uint64(elsewhere.ID), 10), "reviewHash": e.items[0].ReviewHash, "choice": "remove"})
		refuse(t, e, d)
		if e.r.reasonOf(int64(elsewhere.ID)) == "" {
			t.Error("someone else's request was blanked")
		}
	})
	t.Run("an unrelated marker does not make a set stale", func(t *testing.T) {
		e := setup(t)
		// A decision for the same request about another account, one of another
		// purpose's vocabulary, and one with the wrong version are not markers.
		legacyMark(t, e.r.direct, int64(e.reqs[0].ID), e.live.ID, policy.RequestContact, true)
		legacyMarkRaw(t, e.r.direct, audit.AddReasonKept, "add_request", int64(e.reqs[1].ID), fmt.Sprintf(`{"erasedUserId": %d, "reviewVersion": 1}`, e.gone.ID))
		legacyMarkRaw(t, e.r.direct, audit.ContactReasonKept, "contact_request", int64(e.reqs[2].ID), fmt.Sprintf(`{"erasedUserId": %d, "reviewVersion": 2}`, e.gone.ID))
		if _, w := e.r.save(e.gone.ID, choose(own(e), "keep", "remove", "keep")); w.Code != http.StatusOK {
			t.Fatalf("%d %s, want 200: none of those is a marker for this pair", w.Code, w.Body.String())
		}
	})
	t.Run("a subset is allowed and decides only what it names", func(t *testing.T) {
		e := setup(t)
		items := own(e)
		if _, w := e.r.save(e.gone.ID, choose(items, "", "remove", "")); w.Code != http.StatusOK {
			t.Fatalf("%d %s", w.Code, w.Body.String())
		}
		if e.r.reasonOf(int64(e.reqs[0].ID)) == "" || e.r.reasonOf(int64(e.reqs[1].ID)) != "" || e.r.reasonOf(int64(e.reqs[2].ID)) == "" {
			t.Error("a save decided something it did not name")
		}
		if got := e.r.items(e.gone.ID); len(got) != 3 { // two undecided contact requests and the shared one
			t.Errorf("%d reasons listed after a one-item save, want the other three", len(got))
		}
	})
}

// Everything wrong with a request that is not about the choices: before the
// claim nothing is recorded and the id stays usable; after it the id ends rolled
// back and cannot be sent again.
func TestLegacySaveValidation(t *testing.T) {
	r := newSaveRig(t)
	gone, live := r.target("gone"), r.target("live")
	r.ask(gone, live, 1)
	r.ask(live, gone, 2)
	r.bury(gone)
	items := r.items(gone.ID)
	if len(items) != 2 {
		t.Fatalf("%d items", len(items))
	}
	good := choose(items, "keep", "remove")

	t.Run("before the claim nothing is recorded", func(t *testing.T) {
		id := r.mint(gone.ID)
		quoted := func(v any) string { b, _ := json.Marshal(v); return string(b) }
		dec := quoted(good)
		idj := quoted(id)
		for name, tc := range map[string]struct {
			path   string
			body   any
			status int
		}{
			"a user id of 0":                   {legacySavePath(0), map[string]any{"attemptId": id, "decisions": good}, 404},
			"a negative user id":               {"/api/v1/admin/erasure-reasons/legacy/-1", map[string]any{"attemptId": id, "decisions": good}, 404},
			"a leading zero":                   {"/api/v1/admin/erasure-reasons/legacy/0" + itoa(gone.ID), map[string]any{"attemptId": id, "decisions": good}, 404},
			"a plus sign":                      {"/api/v1/admin/erasure-reasons/legacy/+" + itoa(gone.ID), map[string]any{"attemptId": id, "decisions": good}, 404},
			"a fraction":                       {"/api/v1/admin/erasure-reasons/legacy/" + itoa(gone.ID) + ".0", map[string]any{"attemptId": id, "decisions": good}, 404},
			"letters":                          {"/api/v1/admin/erasure-reasons/legacy/abc", map[string]any{"attemptId": id, "decisions": good}, 404},
			"a user id past 63 bits":           {"/api/v1/admin/erasure-reasons/legacy/9223372036854775808", map[string]any{"attemptId": id, "decisions": good}, 404},
			"an empty body":                    {legacySavePath(gone.ID), rawJSON(``), 400},
			"a body that is not json":          {legacySavePath(gone.ID), rawJSON(`{`), 400},
			"a body that is not an object":     {legacySavePath(gone.ID), rawJSON(`[]`), 400},
			"no attempt id":                    {legacySavePath(gone.ID), map[string]any{"decisions": good}, 400},
			"an empty attempt id":              {legacySavePath(gone.ID), map[string]any{"attemptId": "", "decisions": good}, 400},
			"an attempt id that is not a text": {legacySavePath(gone.ID), rawJSON(`{"attemptId": 5, "decisions": ` + dec + `}`), 400},
			"the attempt id twice":             {legacySavePath(gone.ID), rawJSON(`{"attemptId": ` + idj + `, "attemptId": ` + idj + `, "decisions": ` + dec + `}`), 400},
			"the attempt id in another case":   {legacySavePath(gone.ID), rawJSON(`{"AttemptID": ` + idj + `, "decisions": ` + dec + `}`), 400},
			"the decisions twice":              {legacySavePath(gone.ID), rawJSON(`{"attemptId": ` + idj + `, "decisions": ` + dec + `, "decisions": ` + dec + `}`), 400},
			"the decisions in another case":    {legacySavePath(gone.ID), rawJSON(`{"attemptId": ` + idj + `, "Decisions": ` + dec + `}`), 400},
			"a request id that is a number":    {legacySavePath(gone.ID), rawJSON(`{"attemptId": ` + idj + `, "decisions": [{"requestId": 5, "reviewHash": "x", "choice": "keep"}]}`), 400},
		} {
			before := r.written()
			w := r.c.do("PUT", tc.path, tc.body, true)
			if w.Code != tc.status {
				t.Errorf("%s: %d %s, want %d", name, w.Code, w.Body.String(), tc.status)
			}
			r.wantUnchanged(before, id, gone.ID, attemptAbsent)
		}
		// The id was never claimed, so it still works once.
		if w := r.send(gone.ID, id, good); w.Code != http.StatusOK {
			t.Fatalf("the unclaimed id: %d %s", w.Code, w.Body.String())
		}
	})
	// Fresh requests for the post-claim shapes.
	r2 := newSaveRig(t)
	gone2, live2 := r2.target("gone"), r2.target("live")
	r2.ask(gone2, live2, 1)
	r2.ask(live2, gone2, 2)
	r2.bury(gone2)
	items2 := r2.items(gone2.ID)
	one := items2[0]
	t.Run("after the claim the id ends rolled back", func(t *testing.T) {
		many := make([]map[string]string, 101)
		for i := range many {
			many[i] = map[string]string{"requestId": strconv.Itoa(i + 1), "reviewHash": one.ReviewHash, "choice": "keep"}
		}
		withID := func(id string) map[string]string {
			return map[string]string{"requestId": id, "reviewHash": one.ReviewHash, "choice": "keep"}
		}
		withHash := func(h string) map[string]string {
			return map[string]string{"requestId": one.RequestID, "reviewHash": h, "choice": "keep"}
		}
		withChoice := func(c string) map[string]string {
			return map[string]string{"requestId": one.RequestID, "reviewHash": one.ReviewHash, "choice": c}
		}
		for name, decisions := range map[string]any{
			"no decisions at all":                  nil,
			"an empty list":                        []map[string]string{},
			"101 decisions":                        many,
			"a request id with a leading zero":     []map[string]string{withID("0" + one.RequestID)},
			"a request id with a plus sign":        []map[string]string{withID("+" + one.RequestID)},
			"a negative zero":                      []map[string]string{withID("-0")},
			"a fractional request id":              []map[string]string{withID(one.RequestID + ".0")},
			"an empty request id":                  []map[string]string{withID("")},
			"a request id past 64 bits":            []map[string]string{withID("9223372036854775808")},
			"a request id with a space":            []map[string]string{withID(" " + one.RequestID)},
			"a hash in capitals":                   []map[string]string{withHash(strings.ToUpper(one.ReviewHash))},
			"a short hash":                         []map[string]string{withHash(one.ReviewHash[:63])},
			"a long hash":                          []map[string]string{withHash(one.ReviewHash + "0")},
			"a hash that is not hex":               []map[string]string{withHash("g" + one.ReviewHash[1:])},
			"no hash":                              []map[string]string{withHash("")},
			"no choice":                            []map[string]string{withChoice("")},
			"a choice in capitals":                 []map[string]string{withChoice("Keep")},
			"a choice that is not keep or remove":  []map[string]string{withChoice("delete")},
			"a choice of skip":                     []map[string]string{withChoice("skip")},
			"the same request twice":               []map[string]string{withChoice("keep"), withChoice("remove")},
			"one good decision and one bad choice": []map[string]string{withChoice("keep"), {"requestId": items2[1].RequestID, "reviewHash": items2[1].ReviewHash, "choice": "maybe"}},
		} {
			before := r2.written()
			id, w := r2.save(gone2.ID, decisions)
			wantSaveCode(t, w, http.StatusBadRequest, "invalid")
			r2.wantUnchanged(before, id, gone2.ID, attemptRolledBack)
			wantSaveCode(t, r2.send(gone2.ID, id, choose(items2, "keep", "keep")), http.StatusConflict, "attempt_closed")
			if t.Failed() {
				t.Fatalf("(%s)", name)
			}
		}
	})
	t.Run("an account that is not deleted, or not there, answers 404 after the claim", func(t *testing.T) {
		for name, account := range map[string]uint{"a live account": live2.ID, "no such account": 987654} {
			before := r2.written()
			id, w := r2.save(account, choose(items2, "keep", "keep"))
			wantSaveCode(t, w, http.StatusNotFound, "not_found")
			r2.wantUnchanged(before, id, account, attemptRolledBack)
			if t.Failed() {
				t.Fatalf("(%s)", name)
			}
		}
		for _, it := range r2.items(gone2.ID) {
			if r2.reasonOf(mustInt64(t, it.RequestID)) == "" {
				t.Error("a refused save blanked a reason")
			}
		}
	})
	t.Run("an id that does not belong to this save is expired and runs nothing", func(t *testing.T) {
		admin2 := newTestUser(t, r2.app, "admin2", "pw", models.RoleAdmin)
		_ = admin2
		c2 := loginAPI(t, r2.app, "admin2", "pw")
		other := r2.target("other")
		before := r2.written()
		wrongKind := r2.mintWith(r2.c, attemptKindAccountDelete, gone2.ID)
		var minted attemptMintJSON
		decodeJSON(t, wrongKind, &minted)
		for name, id := range map[string]string{
			"an id of the deletion kind": minted.AttemptID,
			"an id for another account":  r2.mint(other.ID),
			"an id another admin minted": func() string {
				var m attemptMintJSON
				decodeJSON(t, r2.mintWith(c2, attemptKindLegacyReasonSave, gone2.ID), &m)
				return m.AttemptID
			}(),
			"an id that is not hex":           strings.Repeat("z", 80),
			"an id that is too short":         "abcd",
			"an id with the wrong key":        strings.Repeat("0", 80),
			"an attempt id that is not valid": "not an id",
		} {
			w := r2.send(gone2.ID, id, choose(items2, "keep", "keep"))
			if w.Code != http.StatusConflict || codeOf2(t, w.Body.String()) != "attempt_expired" {
				t.Errorf("%s: %d %s, want 409 attempt_expired", name, w.Code, w.Body.String())
			}
		}
		if r2.written() != before {
			t.Error("an expired id changed something")
		}
	})
	t.Run("a restore the process saw ends the id rolled back", func(t *testing.T) {
		id := r2.mint(gone2.ID)
		before := r2.written()
		r2.app.noteRestoreDetected()
		w := r2.send(gone2.ID, id, choose(items2, "keep", "keep"))
		wantSaveCode(t, w, http.StatusServiceUnavailable, "restore_detected")
		r2.wantUnchanged(before, id, gone2.ID, attemptRolledBack)
	})
}

func mustInt64(t *testing.T, s string) int64 {
	t.Helper()
	v, ok := parseRequestID(s)
	if !ok {
		t.Fatalf("%q is not a request id", s)
	}
	return v
}

// Who may save: the same people who may read, decided on the early read and again
// at the seal. A refusal after the claim ends the id rolled back and writes nothing.
func TestLegacySaveAuthorization(t *testing.T) {
	setup := func(t *testing.T) (*saveRig, *models.User, []map[string]string) {
		r := newSaveRig(t)
		gone, live := r.target("gone"), r.target("live")
		r.ask(gone, live, 1)
		r.bury(gone)
		return r, gone, choose(r.items(gone.ID), "remove")
	}
	for name, tc := range map[string]struct {
		set    string
		args   []any
		status int
		code   string
	}{
		"a known-17 admin": {"birth_date = ?", []any{time.Now().AddDate(-17, 0, 0)}, http.StatusForbidden, "reason_review_not_allowed"},
		"a teen admin":     {"account_type = 'teen', birth_date = ?", []any{time.Now().AddDate(-15, 0, 0)}, http.StatusForbidden, "reason_review_not_allowed"},
	} {
		t.Run(name, func(t *testing.T) {
			r, gone, decisions := setup(t)
			if err := r.direct.Exec("UPDATE users SET "+tc.set+" WHERE id = ?", append(tc.args, r.admin.ID)...).Error; err != nil {
				t.Fatal(err)
			}
			before := r.written()
			id, w := r.save(gone.ID, decisions)
			wantSaveCode(t, w, tc.status, tc.code)
			r.wantUnchanged(before, id, gone.ID, attemptRolledBack)
			for _, leak := range []string{"identifying", "reviewHash"} {
				if strings.Contains(w.Body.String(), leak) {
					t.Errorf("the refusal carries %q", leak)
				}
			}
		})
	}
	t.Run("an adult admin of unknown age", func(t *testing.T) {
		r, gone, decisions := setup(t)
		if err := r.direct.Exec("UPDATE users SET birth_date = NULL WHERE id = ?", r.admin.ID).Error; err != nil {
			t.Fatal(err)
		}
		if _, w := r.save(gone.ID, decisions); w.Code != http.StatusOK {
			t.Fatalf("%d %s, want 200: an unknown age is not under 18", w.Code, w.Body.String())
		}
	})
	t.Run("a disabled admin is signed out before the claim", func(t *testing.T) {
		r, gone, decisions := setup(t)
		if err := r.direct.Exec("UPDATE users SET status = 'disabled' WHERE id = ?", r.admin.ID).Error; err != nil {
			t.Fatal(err)
		}
		before := r.written()
		w := r.putAs(r.c, gone.ID, map[string]any{"attemptId": strings.Repeat("0", 80), "decisions": decisions})
		wantSaveCode(t, w, http.StatusUnauthorized, "signed_out")
		if r.written() != before {
			t.Error("a signed-out request wrote something")
		}
	})
	t.Run("a non-admin", func(t *testing.T) {
		r, gone, decisions := setup(t)
		newTestUser(t, r.app, "legacy-member", "pw", models.RoleMember)
		mc := loginAPI(t, r.app, "legacy-member", "pw")
		before := r.written()
		w := r.putAs(mc, gone.ID, map[string]any{"attemptId": strings.Repeat("0", 80), "decisions": decisions})
		if w.Code != http.StatusForbidden || codeOf2(t, w.Body.String()) == "reason_review_not_allowed" {
			t.Fatalf("%d %s, want a plain 403", w.Code, w.Body.String())
		}
		if r.written() != before {
			t.Error("a non-admin wrote something")
		}
	})
	t.Run("a signed-out client", func(t *testing.T) {
		r, gone, decisions := setup(t)
		w := newAPIClient(t, r.app).do("PUT", legacySavePath(gone.ID), map[string]any{"attemptId": strings.Repeat("0", 80), "decisions": decisions}, false)
		if w.Code != http.StatusUnauthorized && w.Code != http.StatusForbidden {
			t.Fatalf("%d %s, want 401 or 403", w.Code, w.Body.String())
		}
	})
	t.Run("a put without the csrf token", func(t *testing.T) {
		r, gone, decisions := setup(t)
		id := r.mint(gone.ID)
		before := r.written()
		w := r.c.do("PUT", legacySavePath(gone.ID), map[string]any{"attemptId": id, "decisions": decisions}, false)
		wantSaveCode(t, w, http.StatusForbidden, "session_expired")
		r.wantUnchanged(before, id, gone.ID, attemptAbsent)
	})
	t.Run("only a put saves", func(t *testing.T) {
		r, gone, decisions := setup(t)
		id := r.mint(gone.ID)
		before := r.written()
		for _, method := range []string{"POST", "PATCH", "DELETE"} {
			w := r.c.do(method, legacySavePath(gone.ID), map[string]any{"attemptId": id, "decisions": decisions}, true)
			if w.Code == http.StatusOK {
				t.Errorf("%s answered 200", method)
			}
		}
		r.wantUnchanged(before, id, gone.ID, attemptAbsent)
	})
}

// The seal decides the actor on the locked row last: a session that went stale or
// an admin who stopped qualifying after the early read saves nothing.
func TestLegacySaveSealDecides(t *testing.T) {
	for name, tc := range map[string]struct {
		code   int
		reason string
		sql    string
		args   []any
	}{
		"a stale session":                    {http.StatusUnauthorized, "signed_out", "session_version = session_version + 1", nil},
		"a logout":                           {http.StatusUnauthorized, "signed_out", "logout_version = logout_version + 1", nil},
		"a cleared password":                 {http.StatusUnauthorized, "signed_out", "password_hash = NULL", nil},
		"an admin who became a member":       {http.StatusUnauthorized, "signed_out", "role = 'member', session_version = session_version + 1", nil},
		"an admin who became known to be 17": {http.StatusForbidden, "reason_review_not_allowed", "birth_date = ?", []any{time.Now().AddDate(-17, 0, 0)}},
	} {
		t.Run(name, func(t *testing.T) {
			r := newSaveRig(t)
			gone, live := r.target("gone"), r.target("live")
			r.ask(gone, live, 1)
			r.bury(gone)
			decisions := choose(r.items(gone.ID), "remove")
			var seals atomic.Int64
			r.app.hooks.sealAttempts = &seals
			r.app.hooks.legacySaveBeforeSeal = func(*gorm.DB) {
				if err := r.direct.Exec("UPDATE users SET "+tc.sql+" WHERE id = ?", append(tc.args, r.admin.ID)...).Error; err != nil {
					t.Error(err)
				}
			}
			// Everything the save did is inside the transaction the seal rolls back.
			reasons := r.reasonOf(mustInt64(t, decisions[0]["requestId"]))
			id := r.mint(gone.ID)
			receipts, counter := r.receipts(), r.counter()
			w := r.send(gone.ID, id, decisions)
			wantSaveCode(t, w, tc.code, tc.reason)
			if seals.Load() != 1 {
				t.Errorf("%d seals, want 1", seals.Load())
			}
			if got := r.reasonOf(mustInt64(t, decisions[0]["requestId"])); got != reasons {
				t.Errorf("the reason changed from %q to %q under a refused seal", reasons, got)
			}
			if n := len(r.decisionRows()); n != 0 || r.receipts() != receipts || r.counter() != counter {
				t.Errorf("%d decisions, receipts %d (was %d), counter %d (was %d): a refused seal must commit nothing", n, r.receipts(), receipts, r.counter(), counter)
			}
			if got := r.state(id, gone.ID); got != attemptRolledBack {
				t.Errorf("state %v, want rolled back", got)
			}
		})
	}
}

// A busy actor row, a busy other deleted account and a rollback nobody
// acknowledged each answer for themselves, retrying only what is safe to retry.
func TestLegacySaveBusy(t *testing.T) {
	old := busyRetryWindow
	busyRetryWindow = 30 * time.Second
	t.Cleanup(func() { busyRetryWindow = old })

	t.Run("the admin's own row is busy", func(t *testing.T) {
		r := newSaveRig(t)
		gone, live := r.target("gone"), r.target("live")
		r.ask(gone, live, 1)
		r.bury(gone)
		decisions := choose(r.items(gone.ID), "remove")
		var seals, retries atomic.Int64
		r.app.hooks.sealAttempts = &seals
		r.app.hooks.legacySaveBeforeRetry = func() { retries.Add(1) }
		hold, _ := beginOwnedTx(t, r.direct)
		if err := hold.Exec("SELECT id FROM users WHERE id = ? FOR NO KEY UPDATE", r.admin.ID).Error; err != nil {
			t.Fatal(err)
		}
		before := r.written()
		var appLogs, gormLogs bytes.Buffer
		previous := log.Writer()
		log.SetOutput(&appLogs)
		t.Cleanup(func() { log.SetOutput(previous) })
		r.app.DB = r.app.DB.Session(&gorm.Session{Logger: dbopen.Logger(log.New(&gormLogs, "\r\n", log.LstdFlags))})
		id, w := r.save(gone.ID, decisions)
		wantSaveCode(t, w, http.StatusConflict, "admin_account_busy")
		for name, out := range map[string]string{"application": appLogs.String(), "database": gormLogs.String()} {
			if strings.Contains(out, "55P03") || strings.Contains(strings.ToUpper(out), "NOWAIT") {
				t.Errorf("busy save exposed the database lock error in its %s log: %s", name, out)
			}
		}
		if seals.Load() != 3 || retries.Load() != 2 {
			t.Errorf("%d seals and %d retries, want 3 and 2", seals.Load(), retries.Load())
		}
		r.wantUnchanged(before, id, gone.ID, attemptRolledBack)
		if err := hold.Rollback().Error; err != nil {
			t.Fatal(err)
		}
		seals.Store(0)
		if _, w := r.save(gone.ID, decisions); w.Code != http.StatusOK || seals.Load() != 1 {
			t.Fatalf("after the release: %d %s with %d seals, want 200 and 1", w.Code, w.Body.String(), seals.Load())
		}
	})

	t.Run("the other deleted account is busy", func(t *testing.T) {
		r := newSaveRig(t)
		a, b := r.target("gone-a"), r.target("gone-b")
		// An add request: migration 00007's share lock on the deleted parties is
		// for non-contact rows only.
		req := r.askAdd(a, b, 1)
		r.bury(a, b)
		itemsA := r.items(a.ID)
		var seals, retries atomic.Int64
		r.app.hooks.sealAttempts = &seals
		r.app.hooks.legacySaveBeforeRetry = func() { retries.Add(1) }
		hold, _ := beginOwnedTx(t, r.direct)
		if err := hold.Exec("SELECT id FROM users WHERE id = ? FOR UPDATE", b.ID).Error; err != nil {
			t.Fatal(err)
		}
		before := r.written()
		var appLogs, gormLogs bytes.Buffer
		previous := log.Writer()
		log.SetOutput(&appLogs)
		t.Cleanup(func() { log.SetOutput(previous) })
		r.app.DB = r.app.DB.Session(&gorm.Session{Logger: dbopen.Logger(log.New(&gormLogs, "\r\n", log.LstdFlags))})
		id, w := r.save(a.ID, choose(itemsA, "remove"))
		wantSaveCode(t, w, http.StatusConflict, "legacy_save_busy")
		for name, out := range map[string]string{"application": appLogs.String(), "database": gormLogs.String()} {
			if strings.Contains(out, "55P03") || strings.Contains(strings.ToUpper(out), "NOWAIT") {
				t.Errorf("busy save exposed the database lock error in its %s log: %s", name, out)
			}
		}
		if strings.Contains(w.Body.String(), "delete_reason_busy") || strings.Contains(w.Body.String(), "deleted") {
			t.Errorf("the answer speaks of a deletion: %s", w.Body.String())
		}
		if retries.Load() != 2 || seals.Load() != 0 {
			t.Errorf("%d retries and %d seals, want 2 and none (the blank never got to the seal)", retries.Load(), seals.Load())
		}
		r.wantUnchanged(before, id, a.ID, attemptRolledBack)
		// A keep blanks nothing, so it never needs the other account's row.
		if _, w := r.save(a.ID, choose(itemsA, "keep")); w.Code != http.StatusOK {
			t.Fatalf("a keep while the other account is held: %d %s", w.Code, w.Body.String())
		}
		if err := hold.Rollback().Error; err != nil {
			t.Fatal(err)
		}
		if n := len(r.decisionsFor(int64(req.ID), a.ID)); n != 1 {
			t.Errorf("%d decisions for a, want the keep", n)
		}
	})

	t.Run("an unacknowledged rollback starts no second attempt", func(t *testing.T) {
		for name, tc := range map[string]struct {
			match string
			code  string
			hold  func(r *saveRig, a, b *models.User) *gorm.DB
		}{
			"at the seal": {"FOR SHARE NOWAIT", "admin_account_busy", func(r *saveRig, a, b *models.User) *gorm.DB {
				hold, _ := beginOwnedTx(t, r.direct)
				if err := hold.Exec("SELECT id FROM users WHERE id = ? FOR NO KEY UPDATE", r.admin.ID).Error; err != nil {
					t.Fatal(err)
				}
				return hold
			}},
			"at the blank": {"UPDATE contact_requests SET reason", "legacy_save_busy", func(r *saveRig, a, b *models.User) *gorm.DB {
				hold, _ := beginOwnedTx(t, r.direct)
				if err := hold.Exec("SELECT id FROM users WHERE id = ? FOR UPDATE", b.ID).Error; err != nil {
					t.Fatal(err)
				}
				return hold
			}},
		} {
			t.Run(name, func(t *testing.T) {
				r := newProxiedSaveRig(t)
				a, b := r.target("gone-a"), r.target("gone-b")
				r.askAdd(a, b, 1)
				r.bury(a, b)
				decisions := choose(r.items(a.ID), "remove")
				var seals, retries atomic.Int64
				r.app.hooks.sealAttempts = &seals
				r.app.hooks.legacySaveBeforeRetry = func() { retries.Add(1) }
				hold := tc.hold(r, a, b)
				before := r.written()
				id := r.mint(a.ID)
				mark := len(r.rig.proxy.Events())
				r.rig.proxy.Arm(testdb.Faults{Match: tc.match, Rollback: testdb.RollbackCloseUnforwarded})
				w := r.send(a.ID, id, decisions)
				wantSaveCode(t, w, http.StatusConflict, tc.code)
				begins := 0
				for _, e := range r.rig.proxy.Events()[mark:] {
					if e.Kind == testdb.KindStatement && strings.HasPrefix(strings.ToLower(strings.TrimSpace(e.SQL)), "begin") {
						begins++
					}
				}
				if begins != 1 || retries.Load() != 0 {
					t.Errorf("%d transactions and %d retries, want one and none after a rollback nobody acknowledged", begins, retries.Load())
				}
				if err := hold.Rollback().Error; err != nil {
					t.Fatal(err)
				}
				r.rig.awaitReleased(r.rig.pidOf(tc.match), 10*time.Second)
				r.wantUnchanged(before, id, a.ID, attemptRolledBack)
				wantSettle(t, r.settled(id, a.ID), settleNotApplied, nil)
			})
		}
	})
}

// The busy filter is scoped to a legacy save's own transactions: the same
// lock failure on the same statement is dropped only there. Anywhere else —
// the live deletion seals the same actor row with the same statement — the
// production logger keeps it, as it keeps every unrelated failure, with
// redaction unchanged.
func TestLegacyBusyLoggerScope(t *testing.T) {
	// prodDB equips the rig with the production-configured GORM logger
	// writing to logs. marked decides whether the handle carries the save's
	// scope, the way the route's bounded context does.
	prodDB := func(t *testing.T, r *saveRig, logs *bytes.Buffer, marked bool) *gorm.DB {
		t.Helper()
		db := r.app.DB.Session(&gorm.Session{Logger: dbopen.Logger(log.New(logs, "\r\n", log.LstdFlags))})
		if marked {
			return db.WithContext(dbopen.WithLegacySaveBusyScope(context.Background()))
		}
		return db
	}
	suppressed := func(t *testing.T, out string) {
		t.Helper()
		if strings.Contains(out, "55P03") || strings.Contains(strings.ToUpper(out), "NOWAIT") {
			t.Errorf("the database log hid a failure it does not own: %s", out)
		}
	}
	kept := func(t *testing.T, out, want string) {
		t.Helper()
		if !strings.Contains(out, want) {
			t.Errorf("the database log hid a failure it does not own, want %q in: %s", want, out)
		}
	}
	// busySeal runs the exact actor seal while another transaction holds the
	// admin row, and checks the failure really is lock-not-available: the log
	// assertions below mean nothing without it.
	busySeal := func(t *testing.T, db *gorm.DB, admin *models.User) {
		t.Helper()
		err := currentAdminIn(db, admin, nil)
		if !errors.Is(err, errActorBusy) || pgCode(err) != pgLockNotAvailable {
			t.Fatalf("seal = %v, want the actor busy on SQLSTATE 55P03", err)
		}
	}

	t.Run("actor seal outside the save still logged", func(t *testing.T) {
		r := newSaveRig(t)
		hold, _ := beginOwnedTx(t, r.direct)
		if err := hold.Exec("SELECT id FROM users WHERE id = ? FOR NO KEY UPDATE", r.admin.ID).Error; err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := hold.Rollback().Error; err != nil {
				t.Fatal(err)
			}
		}()
		t.Run("direct", func(t *testing.T) {
			var logs bytes.Buffer
			busySeal(t, prodDB(t, r, &logs, false), r.admin)
			kept(t, logs.String(), "55P03")
		})
		t.Run("in a transaction", func(t *testing.T) {
			var logs bytes.Buffer
			tx := prodDB(t, r, &logs, false).Begin()
			busySeal(t, tx, r.admin)
			if err := tx.Rollback().Error; err != nil {
				t.Fatal(err)
			}
			kept(t, logs.String(), "55P03")
		})
	})

	t.Run("actor seal inside the save suppressed", func(t *testing.T) {
		r := newSaveRig(t)
		hold, _ := beginOwnedTx(t, r.direct)
		if err := hold.Exec("SELECT id FROM users WHERE id = ? FOR NO KEY UPDATE", r.admin.ID).Error; err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := hold.Rollback().Error; err != nil {
				t.Fatal(err)
			}
		}()
		t.Run("direct", func(t *testing.T) {
			var logs bytes.Buffer
			busySeal(t, prodDB(t, r, &logs, true), r.admin)
			suppressed(t, logs.String())
		})
		t.Run("in a transaction", func(t *testing.T) {
			var logs bytes.Buffer
			tx := prodDB(t, r, &logs, true).Begin()
			busySeal(t, tx, r.admin)
			if err := tx.Rollback().Error; err != nil {
				t.Fatal(err)
			}
			suppressed(t, logs.String())
		})
	})

	// busyBlank runs the exact counterpart blank while another transaction
	// holds the other deleted party, and checks the failure really is
	// lock-not-available from the trigger's share lock.
	busyBlank := func(t *testing.T, db *gorm.DB, req models.ContactRequest, party uint) {
		t.Helper()
		err := db.Exec("UPDATE contact_requests SET reason = '' WHERE id = ANY(?::bigint[]) AND reason <> '' AND (requester_id = ? OR subject_user_id = ?)",
			pgBigintArray([]int64{int64(req.ID)}), party, party).Error
		if pgCode(err) != pgLockNotAvailable {
			t.Fatalf("blank = %v, want SQLSTATE 55P03", err)
		}
	}
	blankScene := func(t *testing.T) (*saveRig, models.ContactRequest, uint, *gorm.DB) {
		t.Helper()
		r := newSaveRig(t)
		a, b := r.target("gone-a"), r.target("gone-b")
		req := r.askAdd(a, b, 1)
		r.bury(a, b)
		hold, _ := beginOwnedTx(t, r.direct)
		if err := hold.Exec("SELECT id FROM users WHERE id = ? FOR UPDATE", b.ID).Error; err != nil {
			t.Fatal(err)
		}
		return r, req, a.ID, hold
	}

	t.Run("counterpart blank outside the save still logged", func(t *testing.T) {
		r, req, party, hold := blankScene(t)
		defer func() {
			if err := hold.Rollback().Error; err != nil {
				t.Fatal(err)
			}
		}()
		t.Run("direct", func(t *testing.T) {
			var logs bytes.Buffer
			busyBlank(t, prodDB(t, r, &logs, false), req, party)
			kept(t, logs.String(), "55P03")
		})
		t.Run("in a transaction", func(t *testing.T) {
			var logs bytes.Buffer
			tx := prodDB(t, r, &logs, false).Begin()
			busyBlank(t, tx, req, party)
			if err := tx.Rollback().Error; err != nil {
				t.Fatal(err)
			}
			kept(t, logs.String(), "55P03")
		})
	})

	t.Run("counterpart blank inside the save suppressed", func(t *testing.T) {
		r, req, party, hold := blankScene(t)
		defer func() {
			if err := hold.Rollback().Error; err != nil {
				t.Fatal(err)
			}
		}()
		t.Run("direct", func(t *testing.T) {
			var logs bytes.Buffer
			busyBlank(t, prodDB(t, r, &logs, true), req, party)
			suppressed(t, logs.String())
		})
		t.Run("in a transaction", func(t *testing.T) {
			var logs bytes.Buffer
			tx := prodDB(t, r, &logs, true).Begin()
			busyBlank(t, tx, req, party)
			if err := tx.Rollback().Error; err != nil {
				t.Fatal(err)
			}
			suppressed(t, logs.String())
		})
	})

	t.Run("a different lock still logged", func(t *testing.T) {
		r := newSaveRig(t)
		hold, _ := beginOwnedTx(t, r.direct)
		if err := hold.Exec("SELECT id FROM users WHERE id = ? FOR UPDATE", r.admin.ID).Error; err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := hold.Rollback().Error; err != nil {
				t.Fatal(err)
			}
		}()
		for _, marked := range []bool{false, true} {
			var logs bytes.Buffer
			err := prodDB(t, r, &logs, marked).Exec("SELECT id FROM users WHERE id = ? FOR UPDATE NOWAIT", r.admin.ID).Error
			if err == nil || pgCode(err) != pgLockNotAvailable {
				t.Fatalf("other NOWAIT lock = %v, want SQLSTATE 55P03", err)
			}
			kept(t, logs.String(), "55P03")
		}
	})

	t.Run("an unrelated failure still logged", func(t *testing.T) {
		r := newSaveRig(t)
		for _, marked := range []bool{false, true} {
			var logs bytes.Buffer
			err := prodDB(t, r, &logs, marked).Exec("SELECT * FROM legacy_busy_missing_table_xyz").Error
			if err == nil {
				t.Fatal("missing-table query succeeded")
			}
			kept(t, logs.String(), "legacy_busy_missing_table_xyz")
		}
	})
}

// A save that runs out of time or whose COMMIT is lost answers for itself: timed
// out says nothing was saved, unknown never does, and settle decides which it was.
func TestLegacySaveOutcomes(t *testing.T) {
	t.Run("the budget leaves no time to commit", func(t *testing.T) {
		old := legacySaveBudget
		legacySaveBudget = contactCommitReserve - 500*time.Millisecond
		t.Cleanup(func() { legacySaveBudget = old })
		r := newSaveRig(t)
		gone, live := r.target("gone"), r.target("live")
		r.ask(gone, live, 1)
		r.bury(gone)
		decisions := choose(r.items(gone.ID), "remove")
		before := r.written()
		id, w := r.save(gone.ID, decisions)
		wantSaveCode(t, w, http.StatusServiceUnavailable, "legacy_save_timed_out")
		r.wantUnchanged(before, id, gone.ID, attemptRolledBack)
		wantSettle(t, r.settled(id, gone.ID), settleNotApplied, nil)
	})
	t.Run("the budget runs out before the commit", func(t *testing.T) {
		old := legacySaveBudget
		legacySaveBudget = contactCommitReserve + time.Second
		t.Cleanup(func() { legacySaveBudget = old })
		r := newSaveRig(t)
		gone, live := r.target("gone"), r.target("live")
		r.ask(gone, live, 1)
		r.bury(gone)
		decisions := choose(r.items(gone.ID), "remove")
		r.app.hooks.legacySaveBeforeSeal = func(*gorm.DB) { time.Sleep(time.Second + 200*time.Millisecond) }
		before := r.written()
		id, w := r.save(gone.ID, decisions)
		wantSaveCode(t, w, http.StatusServiceUnavailable, "legacy_save_timed_out")
		if strings.Contains(w.Body.String(), "unknown") {
			t.Errorf("a rolled-back save answered as unknown: %s", w.Body.String())
		}
		r.wantUnchanged(before, id, gone.ID, attemptRolledBack)
	})
	for name, fault := range map[string]testdb.CommitFault{
		"the commit reached the server and its reply was lost": testdb.CommitForwardThenClose,
		"the commit never reached the server":                  testdb.CommitCloseUnforwarded,
	} {
		t.Run(name, func(t *testing.T) {
			r := newProxiedSaveRig(t)
			gone, live := r.target("gone"), r.target("live")
			req := r.ask(gone, live, 1)
			r.bury(gone)
			decisions := choose(r.items(gone.ID), "remove")
			id := r.mint(gone.ID)
			floor := r.app.restoreFenceFloor()
			r.rig.proxy.Arm(testdb.Faults{Match: "idle_in_transaction_session_timeout", Commit: fault})
			w := r.send(gone.ID, id, decisions)
			wantSaveCode(t, w, http.StatusInternalServerError, "legacy_save_outcome_unknown")
			if strings.Contains(strings.ToLower(w.Body.String()), "nothing was saved") {
				t.Errorf("an unknown outcome says nothing was saved: %s", w.Body.String())
			}
			if got := r.state(id, gone.ID); got != attemptUnknown {
				t.Fatalf("state %v, want unknown", got)
			}
			if r.app.restoreFenceFloor() != floor {
				t.Error("an unknown commit raised the restore floor")
			}
			if fault == testdb.CommitCloseUnforwarded {
				r.rig.awaitReleased(r.rig.pidOf("idle_in_transaction_session_timeout"), 10*time.Second)
				if r.reasonOf(int64(req.ID)) == "" || len(r.decisionRows()) != 0 {
					t.Error("a commit that never arrived saved something")
				}
				wantSettle(t, r.settled(id, gone.ID), settleHistoryUnavailable, nil)
				return
			}
			if r.reasonOf(int64(req.ID)) != "" || len(r.decisionRows()) != 1 || r.receipts() != 1 {
				t.Error("a commit the server received was not saved")
			}
			wantSettle(t, r.settled(id, gone.ID), settleApplied, nil)
			// Unknown or not, the id is spent.
			wantSaveCode(t, r.send(gone.ID, id, decisions), http.StatusConflict, "attempt_closed")
		})
	}
}

// An id applies at most once and settles to what happened: the lifecycle on
// the real route.
func TestLegacySaveAttemptLifecycle(t *testing.T) {
	setup := func(t *testing.T) (*saveRig, *models.User, []map[string]string) {
		r := newSaveRig(t)
		gone, live := r.target("gone"), r.target("live")
		r.ask(gone, live, 1)
		r.bury(gone)
		return r, gone, choose(r.items(gone.ID), "remove")
	}
	t.Run("settled before it was sent, an id never runs", func(t *testing.T) {
		r, gone, decisions := setup(t)
		id := r.mint(gone.ID)
		raw := r.rawSettle(id, gone.ID)
		if raw != `{"result":"not_applied"}` {
			t.Fatalf("settle = %s, want not_applied with no subjectDeleted for a tombstone", raw)
		}
		before := r.written()
		wantSaveCode(t, r.send(gone.ID, id, decisions), http.StatusConflict, "attempt_closed")
		r.wantUnchanged(before, id, gone.ID, attemptFenced)
	})
	t.Run("an id older than ten minutes is expired and fenced", func(t *testing.T) {
		r, gone, decisions := setup(t)
		id := r.mint(gone.ID)
		r.advance(11 * time.Minute)
		before := r.written()
		wantSaveCode(t, r.send(gone.ID, id, decisions), http.StatusConflict, "attempt_expired")
		if r.written() != before {
			t.Error("an expired id ran")
		}
		wantSettle(t, r.settled(id, gone.ID), settleNotApplied, nil)
	})
	t.Run("a refused set settles not applied", func(t *testing.T) {
		r, gone, decisions := setup(t)
		if err := r.direct.Exec("UPDATE contact_requests SET reason = 'edited'").Error; err != nil {
			t.Fatal(err)
		}
		id, w := r.save(gone.ID, decisions)
		wantSaveCode(t, w, http.StatusConflict, "reason_review_changed")
		if got := r.rawSettle(id, gone.ID); got != `{"result":"not_applied"}` {
			t.Fatalf("settle = %s", got)
		}
		wantSaveCode(t, r.send(gone.ID, id, decisions), http.StatusConflict, "attempt_closed")
	})
	t.Run("a saved id settles applied and resends are closed", func(t *testing.T) {
		r, gone, decisions := setup(t)
		id, w := r.save(gone.ID, decisions)
		if w.Code != http.StatusOK {
			t.Fatal(w.Body.String())
		}
		wantSettle(t, r.settled(id, gone.ID), settleApplied, nil)
		wantSaveCode(t, r.send(gone.ID, id, decisions), http.StatusConflict, "attempt_closed")
	})
	t.Run("two sends of one id at once run one", func(t *testing.T) {
		r, gone, decisions := setup(t)
		c2 := loginAPI(t, r.app, "admin", "pw")
		id := r.mint(gone.ID)
		results := make(chan *httptest.ResponseRecorder, 2)
		start := make(chan struct{})
		for _, c := range []*apiClient{r.c, c2} {
			go func(c *apiClient) {
				<-start
				results <- r.putAs(c, gone.ID, map[string]any{"attemptId": id, "decisions": decisions})
			}(c)
		}
		close(start)
		var ok, closed int
		for i := 0; i < 2; i++ {
			w := <-results
			switch {
			case w.Code == http.StatusOK:
				ok++
			case w.Code == http.StatusConflict && codeOf2(t, w.Body.String()) == "attempt_closed":
				closed++
			default:
				t.Errorf("%d %s", w.Code, w.Body.String())
			}
		}
		if ok != 1 || closed != 1 || len(r.decisionRows()) != 1 || r.receipts() != 1 {
			t.Errorf("%d ok, %d closed, %d decisions, %d receipts, want one of each kind and one decision", ok, closed, len(r.decisionRows()), r.receipts())
		}
	})
}

// Request ids are signed 64-bit and are never read into an unsigned field: the
// save decides exactly the named ids, at the edges of the range, and only those
// pairs leave the list.
func TestLegacySaveSignedRequestIDs(t *testing.T) {
	r := newSaveRig(t)
	gone, live := r.target("gone"), r.target("live")
	ids := []int64{math.MinInt64, -1, 0, 1, math.MaxInt64}
	for i := range ids {
		made := r.ask(gone, live, i+1)
		r.setID2(made.ID, ids[i])
	}
	r.bury(gone)
	items := r.items(gone.ID)
	if got, want := strings.Join(requestIDs(items), ","), "-9223372036854775808,-1,0,1,9223372036854775807"; got != want {
		t.Fatalf("listing = %s, want %s", got, want)
	}
	choices := []string{"remove", "keep", "remove", "", "keep"} // 1 is left undecided
	_, w := r.save(gone.ID, choose(items, choices...))
	if w.Code != http.StatusOK || strings.TrimSpace(w.Body.String()) != `{"kept":2,"removed":2}` {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	rows := r.decisionRows()
	if len(rows) != 4 {
		t.Fatalf("%d decisions, want 4", len(rows))
	}
	for i, wantID := range []int64{math.MinInt64, -1, 0, math.MaxInt64} {
		if rows[i].TargetID != wantID || rows[i].Meta["erasedUserId"] != float64(gone.ID) {
			t.Errorf("decision %d targets %d for %v, want %d for %d", i, rows[i].TargetID, rows[i].Meta["erasedUserId"], wantID, gone.ID)
		}
	}
	for _, c := range []struct {
		id    int64
		blank bool
	}{{math.MinInt64, true}, {-1, false}, {0, true}, {1, false}, {math.MaxInt64, false}} {
		if blank := r.reasonOf(c.id) == ""; blank != c.blank {
			t.Errorf("request %d: blank = %v, want %v", c.id, blank, c.blank)
		}
	}
	// Only the decided pairs left: the undecided one (id 1) is the whole listing.
	left := legacyReference(t, r.direct)
	if len(left) != 1 || left[0].Request != 1 || left[0].Party != int64(gone.ID) {
		t.Errorf("the reference listing after the save = %v, want only request 1", left)
	}
	if got := r.items(gone.ID); len(got) != 1 || got[0].RequestID != "1" {
		t.Errorf("the page after the save = %v, want only request 1", requestIDs(got))
	}
}

// setID2 gives a request an exact id.
func (r *saveRig) setID2(from uint, to int64) {
	r.t.Helper()
	if err := r.direct.Exec("UPDATE contact_requests SET id = ? WHERE id = ?", to, from).Error; err != nil {
		r.t.Fatal(err)
	}
}

// One save carries at most a page: 100 decisions are applied in one transaction,
// 101 are refused.
func TestLegacySaveTakesAPageOfDecisions(t *testing.T) {
	r := newSaveRig(t)
	gone, live := r.target("gone"), r.target("live")
	for i := 1; i <= 101; i++ {
		r.ask(gone, live, i)
	}
	r.bury(gone)
	all := r.items(gone.ID)
	if len(all) != 101 {
		t.Fatalf("%d items", len(all))
	}
	choices := make([]string, 101)
	for i := range choices {
		choices[i] = "keep"
		if i%3 == 0 {
			choices[i] = "remove"
		}
	}
	before := r.written()
	id, w := r.save(gone.ID, choose(all, choices...))
	wantSaveCode(t, w, http.StatusBadRequest, "invalid")
	r.wantUnchanged(before, id, gone.ID, attemptRolledBack)
	reasonBlankBatchWas := reasonBlankBatch
	reasonBlankBatch = 7 // several batches inside the one transaction
	t.Cleanup(func() { reasonBlankBatch = reasonBlankBatchWas })
	_, w = r.save(gone.ID, choose(all[:100], choices[:100]...))
	removed := 0
	for i := 0; i < 100; i++ {
		if i%3 == 0 {
			removed++
		}
	}
	if w.Code != http.StatusOK || strings.TrimSpace(w.Body.String()) != fmt.Sprintf(`{"kept":%d,"removed":%d}`, 100-removed, removed) {
		t.Fatalf("%d %s, want %d kept and %d removed", w.Code, w.Body.String(), 100-removed, removed)
	}
	if n := len(r.decisionRows()); n != 100 || len(r.items(gone.ID)) != 1 {
		t.Errorf("%d decisions and %d reasons left listed, want 100 and 1", n, len(r.items(gone.ID)))
	}
}

// A failure anywhere in the save leaves nothing behind: no audit row, no
// blanked reason, no receipt, no ticket, and an id that ended rolled back with
// an answer that names no cause.
func TestLegacySaveFailureLeavesNothing(t *testing.T) {
	fails := map[string]struct {
		match func(sql string) bool
		batch int
	}{
		"the request lock": {func(sql string) bool {
			return strings.Contains(sql, "FROM contact_requests WHERE id = ANY") && strings.Contains(sql, "FOR UPDATE")
		}, 0},
		"the marker lookup":        {func(sql string) bool { return strings.Contains(sql, "unnest(") }, 0},
		"the audit insert":         {func(sql string) bool { return strings.Contains(sql, "jsonb_to_recordset") }, 0},
		"the blank":                {func(sql string) bool { return strings.Contains(sql, "UPDATE contact_requests SET reason") }, 0},
		"the second blanked batch": {nil, 2},
	}
	for name, tc := range fails {
		t.Run(name, func(t *testing.T) {
			r := newSaveRig(t)
			gone, live := r.target("gone"), r.target("live")
			for i := 1; i <= 3; i++ {
				r.ask(gone, live, i)
			}
			r.bury(gone)
			decisions := choose(r.items(gone.ID), "remove", "remove", "remove")
			match := tc.match
			if tc.batch > 0 {
				old := reasonBlankBatch
				reasonBlankBatch = 1
				t.Cleanup(func() { reasonBlankBatch = old })
				var seen atomic.Int64
				match = func(sql string) bool {
					return strings.Contains(sql, "UPDATE contact_requests SET reason") && seen.Add(1) == int64(tc.batch)
				}
			}
			failStatements(t, r.app.DB, match)
			before := r.written()
			id, w := r.save(gone.ID, decisions)
			if w.Code != http.StatusInternalServerError || codeOf2(t, w.Body.String()) != "server_error" || strings.Contains(w.Body.String(), "injected") {
				t.Fatalf("%d %s, want a plain 500 with no cause", w.Code, w.Body.String())
			}
			r.wantUnchanged(before, id, gone.ID, attemptRolledBack)
			for _, it := range r.itemsAfterFailure(gone.ID) {
				if it == "" {
					t.Error("a reason was blanked by a failed save")
				}
			}
		})
	}
}

func (r *saveRig) itemsAfterFailure(account uint) []string {
	r.t.Helper()
	var out []string
	rows, err := r.direct.Raw("SELECT reason FROM contact_requests WHERE requester_id = ? OR subject_user_id = ? ORDER BY id", account, account).Rows()
	if err != nil {
		r.t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			r.t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}

// The save's statements run in the order the contract names: the ticket, the
// receipt, the plain actor read, the account, the requests ascending, the markers,
// the audit rows, the blank and, last, the seal.
func TestLegacySaveStatementOrder(t *testing.T) {
	r := newSaveRig(t)
	gone, live := r.target("gone"), r.target("live")
	r.ask(gone, live, 1)
	r.bury(gone)
	decisions := choose(r.items(gone.ID), "remove")
	id := r.mint(gone.ID)
	read := recordTxStatements(t, r.app.DB)
	if w := r.send(gone.ID, id, decisions); w.Code != http.StatusOK {
		t.Fatal(w.Body.String())
	}
	var tx []txStatement
	for _, st := range read() {
		if strings.Contains(st.sql, "admin_operation_attempts") {
			for _, s := range read() {
				if s.pool == st.pool {
					tx = append(tx, s)
				}
			}
			break
		}
	}
	if len(tx) == 0 {
		t.Fatal("no statements of the save's transaction")
	}
	index := func(substr string) int {
		for i, s := range tx {
			if strings.Contains(s.sql, substr) {
				return i
			}
		}
		t.Fatalf("no statement contains %q: %v", substr, tx)
		return -1
	}
	steps := []string{
		"pg_advisory_xact_lock", "UPDATE settings SET value", "INSERT INTO admin_operation_attempts", "FROM \"users\" WHERE \"users\".\"id\" = $1", "FROM users WHERE id = $1 FOR UPDATE",
		"FROM contact_requests WHERE id = ANY", "unnest(", "jsonb_to_recordset", "UPDATE contact_requests SET reason", "FOR SHARE NOWAIT",
	}
	last := -1
	for _, step := range steps {
		i := index(step)
		if i <= last {
			t.Errorf("%q is statement %d, after statement %d: the order must be %v", step, i, last, steps)
		}
		last = i
	}
	if seal := index("FOR SHARE NOWAIT"); seal != len(tx)-1 {
		t.Errorf("the seal is statement %d of %d: it must be the last", seal, len(tx))
	}
}

// A restored database brings back what its dump held: a tombstone decided
// before the dump stays decided, one decided after it is listed again with its
// reason, and a counterpart with no marker of its own is still listed.
func TestLegacySaveRestorePopulations(t *testing.T) {
	fenceTools(t)
	r := newSaveRig(t)
	decidedBefore, decidedAfter := r.target("decided-before"), r.target("decided-after")
	c, d := r.target("gone-c"), r.target("gone-d")
	reqBefore := r.ask(decidedBefore, r.target("live1"), 1)
	reqAfter := r.ask(decidedAfter, r.target("live2"), 2)
	reqBoth := r.ask(c, d, 3)
	r.bury(decidedBefore, decidedAfter, c, d)
	if _, w := r.save(decidedBefore.ID, choose(r.items(decidedBefore.ID), "remove")); w.Code != http.StatusOK {
		t.Fatal(w.Body.String())
	}
	if _, w := r.save(c.ID, choose(r.items(c.ID), "keep")); w.Code != http.StatusOK {
		t.Fatal(w.Body.String())
	}
	dump := fenceDump(t, r.app.DB)
	if _, w := r.save(decidedAfter.ID, choose(r.items(decidedAfter.ID), "remove")); w.Code != http.StatusOK {
		t.Fatal(w.Body.String())
	}
	if r.reasonOf(int64(reqAfter.ID)) != "" {
		t.Fatal("the removal after the dump did not blank")
	}
	fenceRestore(t, r.app.DB, dump, false, r.direct)
	fresh := &App{DB: r.direct, Store: r.app.Store, Config: r.app.Config, Uploads: r.app.Uploads, Mailer: r.app.Mailer}
	bootFence(t, fresh)
	fc := loginAPI(t, fresh, "admin", "pw")
	listed := func(account uint) []string { return requestIDs(r.itemsAs(fc, account)) }
	if got := listed(decidedBefore.ID); len(got) != 0 {
		t.Errorf("a tombstone decided before the dump is listed: %v", got)
	}
	if got := listed(decidedAfter.ID); len(got) != 1 || got[0] != itoa(reqAfter.ID) {
		t.Errorf("a tombstone decided after the dump = %v, want its request listed again", got)
	}
	if r.reasonOf(int64(reqAfter.ID)) == "" || r.reasonOf(int64(reqBefore.ID)) != "" {
		t.Error("the restored reasons are not the dump's: the later removal is back and the earlier one is not")
	}
	if got := listed(c.ID); len(got) != 0 {
		t.Errorf("the decided side of the shared request is listed: %v", got)
	}
	if got := listed(d.ID); len(got) != 1 || got[0] != itoa(reqBoth.ID) {
		t.Errorf("the counterpart with no marker of its own = %v, want the shared request still listed", got)
	}
}

// Nothing the save or its scanner reads goes through an unsigned request id
// (R4-C04): neither a model with a uint id nor a uint-typed id variable.
func TestLegacyCensusHasNoUnsignedRequestID(t *testing.T) {
	for _, file := range []string{"api_admin_reasons_legacy.go", "account_erasure_reasons_scan.go"} {
		b, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		src := string(b)
		for _, bad := range []string{"models.ContactRequest", "models.AuditLog", "[]uint{", "ids []uint", "requestID uint", "request uint"} {
			if strings.Contains(src, bad) {
				t.Errorf("%s names %q; request ids are int64 end to end", file, bad)
			}
		}
	}
}
