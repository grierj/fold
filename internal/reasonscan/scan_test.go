package reasonscan

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/rand"
	"reflect"
	"sort"
	"testing"
)

const target = int64(7)

func qualifies(r Row) bool { return r.HasReason && r.RequesterID == target }

// stmt is what the fake source knows about one statement. Returned is what the
// server sent, known only when the statement completed (the command tag);
// Observed is what a proxy would have counted, which is all there is when a
// stream fails: a failed stream never delivers a tag, so its total is unknown
// and is never inferred from Observed or Consumed.
type stmt struct {
	After         *int64
	Limit         int
	Returned      int
	ReturnedKnown bool
	Observed      int
	Consumed      int
	Err           error
}

type source struct {
	rows []Row // committed rows, ascending by ID
	log  []stmt

	failQuery    map[int]error // batch index (query call number, 0-based) -> error returned by the query
	failDrain    map[int]int   // call number -> cut the stream after this many rows were sent (a drain failure after the page is full)
	failScanAt   map[int]int   // call number -> the Nth row (1-based) fails to scan
	failCloseOK  map[int]error // call number -> Close fails although the stream completed
	postCloseErr map[int]error // call number -> Err reports failure after a successful Close
	calls        int
}

func newSource(rows []Row) *source {
	s := &source{rows: append([]Row(nil), rows...)}
	sort.Slice(s.rows, func(i, j int) bool { return s.rows[i].ID < s.rows[j].ID })
	return s
}

func (s *source) set(id int64, f func(*Row)) {
	for i := range s.rows {
		if s.rows[i].ID == id {
			f(&s.rows[i])
			return
		}
	}
	panic(fmt.Sprintf("no row %d", id))
}

func (s *source) insert(r Row) {
	s.rows = append(s.rows, r)
	sort.Slice(s.rows, func(i, j int) bool { return s.rows[i].ID < s.rows[j].ID })
}

func (s *source) query(after *int64, limit int) (Rows, error) {
	call := s.calls
	s.calls++
	if err := s.failQuery[call]; err != nil {
		s.log = append(s.log, stmt{After: copyID(after), Limit: limit, Err: err})
		return nil, err
	}
	var data []Row
	for _, r := range s.rows {
		if after != nil && r.ID <= *after {
			continue
		}
		if len(data) == limit {
			break
		}
		data = append(data, r)
	}
	s.log = append(s.log, stmt{After: copyID(after), Limit: limit})
	return &fakeRows{s: s, idx: len(s.log) - 1, call: call, data: data, pos: -1}, nil
}

func copyID(p *int64) *int64 {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

type fakeRows struct {
	s      *source
	idx    int
	call   int
	data   []Row
	pos    int
	closed bool
}

func (r *fakeRows) Next() bool {
	if r.pos+1 >= len(r.data) {
		return false
	}
	r.pos++
	r.s.log[r.idx].Consumed++
	return true
}

func (r *fakeRows) Scan() (Row, error) {
	if n := r.s.failScanAt[r.call]; n > 0 && r.pos+1 == n {
		return Row{}, errors.New("scan failed")
	}
	return r.data[r.pos], nil
}

func (r *fakeRows) Err() error {
	if r.closed {
		return r.s.postCloseErr[r.call]
	}
	return nil
}

// Close drains: like pgx, it reads every row the server still sends. A cut
// stream fails here and leaves the total unknown.
func (r *fakeRows) Close() error {
	if r.closed {
		return nil
	}
	r.closed = true
	st := &r.s.log[r.idx]
	if cut, ok := r.s.failDrain[r.call]; ok {
		st.Observed = cut
		st.Err = errors.New("stream cut while draining")
		return st.Err
	}
	st.Returned, st.ReturnedKnown, st.Observed = len(r.data), true, len(r.data)
	if err := r.s.failCloseOK[r.call]; err != nil {
		st.Err = err
		return err
	}
	return nil
}

func params(unit, first, cap int) Params {
	return Params{UnitRows: unit, PageRequests: cap, FirstBatch: first, Growth: 2}
}

// reference is the single-statement scan written from the rules, over one
// snapshot: read up to unitRows rows after the cursor, stop right after the
// pageRequests-th qualifying request (next = its id), else next = the last id
// read when the whole unit was read, else nil.
func reference(rows []Row, after *int64, unitRows, pageRequests int) Unit {
	var u Unit
	read := 0
	var last int64
	for _, r := range rows {
		if after != nil && r.ID <= *after {
			continue
		}
		if read == unitRows {
			break
		}
		read++
		last = r.ID
		if !qualifies(r) {
			continue
		}
		u.Requests = append(u.Requests, r.ID)
		if len(u.Requests) == pageRequests {
			n := r.ID
			u.Next = &n
			return u
		}
	}
	if read >= unitRows {
		n := last
		u.Next = &n
	}
	return u
}

func sameUnit(a, b Unit) bool {
	if len(a.Requests) == 0 && len(b.Requests) == 0 {
		return ptrEq(a.Next, b.Next)
	}
	return reflect.DeepEqual(a.Requests, b.Requests) && ptrEq(a.Next, b.Next)
}

func ptrEq(a, b *int64) bool { return (a == nil && b == nil) || (a != nil && b != nil && *a == *b) }

func fmtUnit(u Unit) string {
	n := "nil"
	if u.Next != nil {
		n = fmt.Sprint(*u.Next)
	}
	return fmt.Sprintf("%v next=%s", u.Requests, n)
}

// mkRows makes rows with the given ids; every row whose index is chosen by
// want is a qualifying request of the target.
func mkRows(ids []int64, want func(i int) bool) []Row {
	out := make([]Row, len(ids))
	for i, id := range ids {
		out[i] = Row{ID: id, RequesterID: 99, SubjectUserID: 98}
		if want(i) {
			out[i].RequesterID, out[i].HasReason = target, true
		}
	}
	return out
}

func seq(n int, start, step int64) []int64 {
	out := make([]int64, n)
	for i := range out {
		out[i] = start + int64(i)*step
	}
	return out
}

func run(s *source, after *int64, p Params, h Hooks) (Unit, error) {
	return Scan(s.query, qualifies, after, p, h)
}

func TestBatchSchedule(t *testing.T) {
	cases := []struct {
		first, statements, last int
	}{
		{128, 9, 17360},
		{256, 8, 17488},
		{512, 7, 17744},
	}
	for _, c := range cases {
		b := Batches(params(50000, c.first, 100))
		sum := 0
		for _, n := range b {
			sum += n
		}
		if len(b) != c.statements || sum != 50000 || b[len(b)-1] != c.last {
			t.Errorf("first %d: %d statements, sum %d, last %d; want %d, 50000, %d (%v)", c.first, len(b), sum, b[len(b)-1], c.statements, c.last, b)
		}
		for i := 1; i < len(b)-1; i++ {
			if b[i] != b[i-1]*2 {
				t.Errorf("first %d: batch %d is %d, want double %d", c.first, i, b[i], b[i-1])
			}
		}
	}
	if b := Batches(params(5, 256, 100)); !reflect.DeepEqual(b, []int{5}) {
		t.Errorf("a unit smaller than the first batch is one statement of the unit: %v", b)
	}
	if b := Batches(params(256, 256, 100)); !reflect.DeepEqual(b, []int{256}) {
		t.Errorf("a unit equal to the first batch: %v", b)
	}
	if _, err := run(newSource(nil), nil, Params{}, Hooks{}); err == nil {
		t.Error("invalid bounds were accepted")
	}
}

// Signed ids at both ends of int64, units, first batches and caps small enough
// to cross every boundary, from no cursor and from every row id: the batched
// scan returns what the single-statement rules return for the same rows.
func TestScanMatchesTheSingleStatementRules(t *testing.T) {
	rng := rand.New(rand.NewSource(294))
	pool := []int64{math.MinInt64, math.MinInt64 + 1, -1000, -2, -1, 0, 1, 2, 3, 7, 100, 4000, math.MaxInt64 - 1, math.MaxInt64}
	for round := 0; round < 40; round++ {
		n := rng.Intn(36)
		set := map[int64]bool{}
		for len(set) < n {
			if rng.Intn(3) == 0 {
				set[pool[rng.Intn(len(pool))]] = true
			} else {
				set[rng.Int63n(2000)-1000] = true
			}
		}
		ids := make([]int64, 0, len(set))
		for id := range set {
			ids = append(ids, id)
		}
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
		density := []float64{0, 0.05, 0.5, 1}[rng.Intn(4)]
		rows := mkRows(ids, func(int) bool { return rng.Float64() < density })
		cursors := []*int64{nil}
		for i := range ids {
			cursors = append(cursors, &ids[i])
		}
		for _, unit := range []int{3, 7, 20, 1000} {
			for _, first := range []int{1, 2, 5, 256} {
				for _, cap := range []int{1, 3, 100} {
					for _, after := range cursors {
						want := reference(rows, after, unit, cap)
						got, err := run(newSource(rows), after, params(unit, first, cap), Hooks{})
						if err != nil {
							t.Fatal(err)
						}
						single, err := run(newSource(rows), after, params(unit, unit, cap), Hooks{})
						if err != nil {
							t.Fatal(err)
						}
						if !sameUnit(got, want) || !sameUnit(single, want) {
							c := "nil"
							if after != nil {
								c = fmt.Sprint(*after)
							}
							t.Fatalf("round %d unit %d first %d cap %d after %s:\n batched %s\n single  %s\n rules   %s\n ids %v", round, unit, first, cap, c, fmtUnit(got), fmtUnit(single), fmtUnit(want), ids)
						}
					}
				}
			}
		}
	}
}

// The 100th qualifying request is the cursor, wherever it falls against a batch
// boundary: the last row of a batch, the first of the next, inside one, and at
// the signed ends. Walking the pages returns every qualifying request once, in
// order.
func TestHundredthRequestBoundary(t *testing.T) {
	ids := append([]int64{math.MinInt64, math.MinInt64 + 5}, seq(246, -300, 3)...)
	ids = append(ids, math.MaxInt64-1, math.MaxInt64)
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	rows := mkRows(ids, func(int) bool { return true }) // 250 qualifying
	for _, first := range []int{1, 99, 100, 101, 256} {
		s := newSource(rows)
		var after *int64
		var got []int64
		pages := 0
		for {
			u, err := run(s, after, params(50000, first, 100), Hooks{})
			if err != nil {
				t.Fatal(err)
			}
			pages++
			if len(u.Requests) > 100 {
				t.Fatalf("first %d: a page of %d requests", first, len(u.Requests))
			}
			got = append(got, u.Requests...)
			if pages == 1 && (len(u.Requests) != 100 || u.Next == nil || *u.Next != ids[99]) {
				t.Fatalf("first %d: first page %s, want 100 requests ending at the 100th id %d", first, fmtUnit(u), ids[99])
			}
			if u.Next == nil {
				break
			}
			after = u.Next
			if pages > 10 {
				t.Fatalf("first %d: the walk does not end", first)
			}
		}
		if !reflect.DeepEqual(got, ids) {
			t.Fatalf("first %d: walk returned %d ids, want %d, in order and once", first, len(got), len(ids))
		}
		// 250 requests: pages of 100, 100, 50; the last request is MaxInt64, so the
		// page that ends the walk is the one that finds fewer than 100 with the
		// table exhausted (next nil) and no empty page follows.
		if pages != 3 {
			t.Errorf("first %d: %d pages, want 3", first, pages)
		}
	}
	// A cursor of MaxInt64 reads nothing and ends the walk.
	max := int64(math.MaxInt64)
	u, err := run(newSource(rows), &max, params(50000, 256, 100), Hooks{})
	if err != nil || len(u.Requests) != 0 || u.Next != nil {
		t.Errorf("after MaxInt64: %s %v", fmtUnit(u), err)
	}
}

func TestEmptyCapAndUnit(t *testing.T) {
	// An empty table is one statement and no cursor.
	s := newSource(nil)
	u, err := run(s, nil, params(50000, 256, 100), Hooks{})
	if err != nil || len(u.Requests) != 0 || u.Next != nil || len(s.log) != 1 {
		t.Errorf("empty table: %s, %d statements, %v", fmtUnit(u), len(s.log), err)
	}

	// Exactly the unit with nothing to find: the cursor is the last id read and
	// the next page is empty, as with the single statement; one row fewer and the
	// table has ended.
	for _, c := range []struct {
		rows     int
		wantNext bool
		stmts    int
	}{{600, true, 2}, {599, false, 2}, {255, false, 1}} {
		s := newSource(mkRows(seq(c.rows, 10, 1), func(int) bool { return false }))
		u, err := run(s, nil, params(600, 256, 100), Hooks{})
		if err != nil || (u.Next != nil) != c.wantNext || len(s.log) != c.stmts {
			t.Errorf("%d rows, unit 600: %s, %d statements, %v; want next=%v with %d statements", c.rows, fmtUnit(u), len(s.log), err, c.wantNext, c.stmts)
		}
		if c.wantNext {
			if *u.Next != 10+int64(c.rows)-1 {
				t.Errorf("cursor %d, want the last id %d", *u.Next, 10+int64(c.rows)-1)
			}
			u2, err := run(s, u.Next, params(600, 256, 100), Hooks{})
			if err != nil || len(u2.Requests) != 0 || u2.Next != nil {
				t.Errorf("the page after an exactly full unit: %s %v", fmtUnit(u2), err)
			}
		}
	}

	// The table ends exactly on a batch boundary: one more statement, returning
	// nothing, and no cursor.
	s = newSource(mkRows(seq(256, 1, 1), func(int) bool { return false }))
	u, err = run(s, nil, params(50000, 256, 100), Hooks{})
	if err != nil || u.Next != nil || len(s.log) != 2 || s.log[1].Returned != 0 || !s.log[1].ReturnedKnown {
		t.Errorf("table ending on a batch boundary: %s %+v %v", fmtUnit(u), s.log, err)
	}

	// A cap of one request.
	s = newSource(mkRows(seq(10, 1, 1), func(i int) bool { return i >= 3 }))
	u, err = run(s, nil, params(50000, 2, 1), Hooks{})
	if err != nil || len(u.Requests) != 1 || u.Requests[0] != 4 || u.Next == nil || *u.Next != 4 {
		t.Errorf("cap 1: %s %v", fmtUnit(u), err)
	}
}

func TestDensePageDrainsLessThanOneBatch(t *testing.T) {
	s := newSource(mkRows(seq(1000, 1, 1), func(int) bool { return true }))
	var events []BatchEvent
	u, err := run(s, nil, params(50000, 256, 100), Hooks{After: func(e BatchEvent) { events = append(events, e) }})
	if err != nil || len(u.Requests) != 100 || u.Next == nil || *u.Next != 100 {
		t.Fatalf("dense page: %s %v", fmtUnit(u), err)
	}
	if len(s.log) != 1 {
		t.Fatalf("%d statements for a dense page, want 1", len(s.log))
	}
	st := s.log[0]
	if !st.ReturnedKnown || st.Returned != 256 || st.Consumed != 100 || st.Returned-st.Consumed != 156 || st.Limit != 256 {
		t.Errorf("returned %d (known %v), consumed %d, limit %d; want 256, 100 and 156 drained", st.Returned, st.ReturnedKnown, st.Consumed, st.Limit)
	}
	if len(events) != 1 || !events[0].Stopped || events[0].Short || events[0].Consumed != 100 {
		t.Errorf("events %+v", events)
	}
	// The next page starts after the 100th request and reads the batch again.
	u, err = run(s, u.Next, params(50000, 256, 100), Hooks{})
	if err != nil || len(u.Requests) != 100 || *u.Next != 200 || len(s.log) != 2 || s.log[1].Returned != 256 {
		t.Errorf("second dense page: %s %v %+v", fmtUnit(u), err, s.log)
	}
	// The same page through the single statement drains the whole unit.
	s = newSource(mkRows(seq(60000, 1, 1), func(int) bool { return true }))
	if _, err := run(s, nil, params(50000, 50000, 100), Hooks{}); err != nil || s.log[0].Returned != 50000 || s.log[0].Consumed != 100 {
		t.Errorf("single statement: %+v %v", s.log[0], err)
	}
}

// A scan that finds nothing reads the whole unit, and a request with the
// largest number of busy retries (reasonBusyTries = 3 attempts) is three of
// them: 8 statements and 50,000 rows each at the pinned 256, 24 and 150,000
// per request. Today the same request is 3 statements and 150,000 rows.
func TestThreeAttemptsBound(t *testing.T) {
	for _, c := range []struct{ first, perAttempt int }{{128, 9}, {256, 8}, {512, 7}} {
		s := newSource(mkRows(seq(60000, 1, 1), func(int) bool { return false }))
		for attempt := 1; attempt <= 3; attempt++ {
			u, err := run(s, nil, params(50000, c.first, 100), Hooks{})
			if err != nil || u.Next == nil || *u.Next != 50000 {
				t.Fatalf("first %d attempt %d: %s %v", c.first, attempt, fmtUnit(u), err)
			}
		}
		rows, limits := 0, 0
		for _, st := range s.log {
			rows += st.Returned
			limits += st.Limit
			if !st.ReturnedKnown {
				t.Fatalf("a completed statement without a count: %+v", st)
			}
		}
		if len(s.log) != 3*c.perAttempt || rows != 150000 || limits != 150000 {
			t.Errorf("first %d: %d statements, %d rows, limits %d; want %d, 150000, 150000", c.first, len(s.log), rows, limits, 3*c.perAttempt)
		}
	}
	// Rows read for an early stop: S_k, the smallest prefix sum of the schedule at
	// or above the position R of the actual 100th match, and below 2R + first.
	for _, r := range []int{100, 256, 257, 768, 769, 1100, 10100, 32512, 32513} {
		s := newSource(mkRows(seq(60000, 1, 1), func(i int) bool { return i+1 >= r-99 && i+1 <= r })) // exactly 100 matches, the last at row r
		u, err := run(s, nil, params(50000, 256, 100), Hooks{})
		if err != nil || u.Next == nil || *u.Next != int64(r) {
			t.Fatalf("R=%d: %s %v", r, fmtUnit(u), err)
		}
		total := 0
		for _, st := range s.log {
			total += st.Returned
		}
		if total < r || (total < 50000 && total >= 2*r+256) {
			t.Errorf("R=%d: %d rows read, want at least R and under 2R+256 = %d", r, total, 2*r+256)
		}
	}
}

// The documented differences from one snapshot, as they happen: a request
// blanked before its batch is read stops being the 100th (the page ends one
// request later); a request committed below the cursor of a batch already read
// is never returned by the walk; one committed above it can join this page. The
// deletion's locked re-read, not these pages, decides what is deleted.
func TestChangesBetweenBatches(t *testing.T) {
	ids := seq(12, 10, 10) // 10..120, all qualifying
	mk := func() *source { return newSource(mkRows(ids, func(int) bool { return true })) }
	p := params(50, 2, 4) // batches 2, 4, 8, ...; the 4th request is the original 100th

	// What one snapshot would return.
	want := reference(mk().rows, nil, 50, 4)
	if fmtUnit(want) != "[10 20 30 40] next=40" {
		t.Fatalf("reference: %s", fmtUnit(want))
	}

	// (1) The original 100th (40) is blanked after batch 0 and before batch 1.
	s := mk()
	u, err := run(s, nil, p, Hooks{Before: func(i int) {
		if i == 1 {
			s.set(40, func(r *Row) { r.HasReason = false })
		}
	}})
	if err != nil || fmtUnit(u) != "[10 20 30 50] next=50" {
		t.Fatalf("blank of the original 100th: %s %v, want [10 20 30 50] next=50 (one snapshot: [10 20 30 40] next=40, then 40 is dropped at rehydration)", fmtUnit(u), err)
	}
	// Walking on from the cursor returns the rest once; nothing qualifying is lost.
	rest, err := run(s, u.Next, p, Hooks{})
	if err != nil || fmtUnit(rest) != "[60 70 80 90] next=90" {
		t.Fatalf("after the blank: %s %v", fmtUnit(rest), err)
	}

	// (2) An earlier match in a later batch is blanked: the page ends one later.
	s = mk()
	u, err = run(s, nil, p, Hooks{Before: func(i int) {
		if i == 1 {
			s.set(30, func(r *Row) { r.HasReason = false })
		}
	}})
	if err != nil || fmtUnit(u) != "[10 20 40 50] next=50" {
		t.Errorf("blank of an earlier match in a later batch: %s %v", fmtUnit(u), err)
	}

	// (3) A blank in a batch already read changes nothing for the scan (the
	// rehydration read drops it, as with one snapshot).
	s = mk()
	u, err = run(s, nil, p, Hooks{Before: func(i int) {
		if i == 1 {
			s.set(10, func(r *Row) { r.HasReason = false })
		}
	}})
	if err != nil || !sameUnit(u, want) {
		t.Errorf("blank in a batch already read: %s %v, want %s", fmtUnit(u), err, fmtUnit(want))
	}

	// (4) A request committed above the cursor of the batch just read, inside the
	// range of the next one, joins this page and shifts the cursor earlier.
	s = mk()
	u, err = run(s, nil, p, Hooks{Before: func(i int) {
		if i == 1 {
			s.insert(Row{ID: 35, RequesterID: target, HasReason: true})
		}
	}})
	if err != nil || fmtUnit(u) != "[10 20 30 35] next=35" {
		t.Errorf("late insert above the cursor: %s %v", fmtUnit(u), err)
	}
	// ... and the walk from that cursor returns 40 next: nothing is returned twice.
	rest, err = run(s, u.Next, p, Hooks{})
	if err != nil || fmtUnit(rest) != "[40 50 60 70] next=70" {
		t.Errorf("walk after a late insert: %s %v", fmtUnit(rest), err)
	}

	// (5) A request committed below the cursor of a batch already read is never
	// returned: this page has passed it, and no later page starts below its cursor.
	s = mk()
	late := Row{ID: 15, RequesterID: target, HasReason: true}
	u, err = run(s, nil, p, Hooks{Before: func(i int) {
		if i == 1 {
			s.insert(late)
		}
	}})
	if err != nil || fmtUnit(u) != "[10 20 30 40] next=40" {
		t.Fatalf("late commit below the cursor: %s %v", fmtUnit(u), err)
	}
	seen := map[int64]bool{}
	for _, id := range u.Requests {
		seen[id] = true
	}
	after := u.Next
	for pages := 0; after != nil && pages < 10; pages++ {
		page, err := run(s, after, p, Hooks{})
		if err != nil {
			t.Fatal(err)
		}
		for _, id := range page.Requests {
			if seen[id] {
				t.Fatalf("request %d returned twice", id)
			}
			seen[id] = true
		}
		after = page.Next
	}
	if seen[late.ID] {
		t.Error("a request committed below the returned cursor was returned")
	}
	for _, id := range ids {
		if !seen[id] {
			t.Errorf("request %d that existed throughout was never returned", id)
		}
	}
}

func TestFailuresNeverMakeAPageAndRetryAtTheSameCursor(t *testing.T) {
	rows := mkRows(seq(300, 1, 1), func(int) bool { return true })
	p := params(50000, 2, 100)
	want := reference(rows, nil, 50000, 100)

	// A later batch fails: no unit, the error is returned, the retry at the same
	// cursor returns the page.
	s := newSource(rows)
	s.failQuery = map[int]error{2: errors.New("injected statement failure")}
	var events []BatchEvent
	if u, err := run(s, nil, p, Hooks{After: func(e BatchEvent) { events = append(events, e) }}); err == nil || len(u.Requests) != 0 || u.Next != nil {
		t.Fatalf("a failed later batch returned %s %v", fmtUnit(u), err)
	}
	if len(events) != 3 || events[2].Err == nil || events[2].Index != 2 {
		t.Errorf("events %+v", events)
	}
	if u, err := run(s, nil, p, Hooks{}); err != nil || !sameUnit(u, want) {
		t.Errorf("retry: %s %v, want %s", fmtUnit(u), err, fmtUnit(want))
	}

	// The context ends: the error is the context's and is not an empty page.
	s = newSource(rows)
	s.failQuery = map[int]error{1: context.Canceled}
	if u, err := run(s, nil, p, Hooks{}); !errors.Is(err, context.Canceled) || len(u.Requests) != 0 {
		t.Errorf("cancel: %s %v", fmtUnit(u), err)
	}

	// A row fails to scan.
	s = newSource(rows)
	s.failScanAt = map[int]int{1: 3}
	if u, err := run(s, nil, p, Hooks{}); err == nil || len(u.Requests) != 0 {
		t.Errorf("scan failure: %s %v", fmtUnit(u), err)
	}

	// The stream is cut while draining, after the page was full: batches are 2, 4, 8,
	// 16, 32, 64 rows, the 100th request is row 100, so it is in the sixth batch (rows
	// 63 to 126, 38 consumed). The error is returned, no page is made, and the
	// statement total is unknown, not zero and not the rows observed.
	s = newSource(rows)
	s.failDrain = map[int]int{5: 40}
	u, err := run(s, nil, p, Hooks{})
	if err == nil || len(u.Requests) != 0 || u.Next != nil {
		t.Fatalf("a cut drain made a page: %s %v", fmtUnit(u), err)
	}
	st := s.log[len(s.log)-1]
	if st.ReturnedKnown || st.Returned != 0 || st.Observed != 40 || st.Consumed != 38 {
		t.Errorf("failed stream: %+v; want total unknown (not zero, not inferred), 40 frames observed, 38 consumed", st)
	}
	s.failDrain = nil
	if u, err := run(s, nil, p, Hooks{}); err != nil || !sameUnit(u, want) {
		t.Errorf("retry after a cut drain: %s %v", fmtUnit(u), err)
	}

	// Close fails on a stream that completed (a deferred error).
	s = newSource(rows)
	s.failCloseOK = map[int]error{0: errors.New("deferred error")}
	if u, err := run(s, nil, p, Hooks{}); err == nil || len(u.Requests) != 0 {
		t.Errorf("close failure: %s %v", fmtUnit(u), err)
	}

	// A context cancellation may close database/sql rows asynchronously. A
	// later Close can return nil even though Err still reports the failure.
	s = newSource(rows)
	s.postCloseErr = map[int]error{5: context.Canceled}
	if u, err := run(s, nil, p, Hooks{}); !errors.Is(err, context.Canceled) || len(u.Requests) != 0 || u.Next != nil {
		t.Errorf("post-close cancellation made a page: %s %v", fmtUnit(u), err)
	}
}

// walk reads a whole table page by page the way the live route does and returns
// the pages, the statements and the rows the server sent.
func walk(rows []Row, p Params) (pages, statements, sent int) {
	s := newSource(rows)
	var after *int64
	for {
		u, err := run(s, after, p, Hooks{})
		if err != nil {
			panic(err)
		}
		pages++
		if u.Next == nil {
			break
		}
		after = u.Next
	}
	for _, st := range s.log {
		statements++
		sent += st.Returned
	}
	return
}

// The 100k pilot shapes at the pinned 256: the figures in the design's section 9.1
// (as corrected by review) come from these walks.
func TestWalkModels(t *testing.T) {
	prod := params(50000, 256, 100)
	// 100k filler rows (even ids) and 100k scenario rows of the target (odd ids)
	// interleaved one to one: the discovery shape.
	spread := make([]Row, 0, 200000)
	for id := int64(1); id <= 200000; id++ {
		r := Row{ID: id, RequesterID: 99}
		if id%2 == 1 {
			r.RequesterID, r.HasReason = target, true
		}
		spread = append(spread, r)
	}
	pages, stmts, sent := walk(spread, prod)
	t.Logf("spread 100k/100k: %d pages, %d statements, %d rows sent", pages, stmts, sent)
	if pages != 1001 || stmts != 1001 || sent != 255946 {
		t.Errorf("spread: %d pages, %d statements, %d rows; want 1001, 1001, 255946", pages, stmts, sent)
	}
	// The request-size shape: 100k filler rows, then 100k scenario rows of the
	// target after them (the tail layout).
	tail := make([]Row, 0, 200000)
	for id := int64(1); id <= 100000; id++ {
		tail = append(tail, Row{ID: id, RequesterID: 99})
	}
	for id := int64(100001); id <= 200000; id++ {
		tail = append(tail, Row{ID: id, RequesterID: target, HasReason: true})
	}
	pages, stmts, sent = walk(tail, prod)
	if pages != 1003 || stmts != 1017 || sent != 355788 {
		t.Errorf("tail: %d pages, %d statements, %d rows; want 1003, 1017, 355788", pages, stmts, sent)
	}
	// An empty account over a table of 1,000,001 rows (21 units of 50,000) is 21
	// statements today and 8 per full unit with batches: 20 x 8 + 1.
	empty := make([]Row, 1000001)
	for i := range empty {
		empty[i] = Row{ID: int64(i + 1), RequesterID: 99}
	}
	pages, stmts, sent = walk(empty, prod)
	if pages != 21 || stmts != 161 || sent != 1000001 {
		t.Errorf("empty: %d pages, %d statements, %d rows; want 21, 161, 1000001", pages, stmts, sent)
	}
}
