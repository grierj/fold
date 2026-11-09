package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"fold/internal/guardkit"

	"fold/internal/audit"
	"fold/internal/policy"
	"fold/internal/testdb"
	"fold/models"

	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
)

// ---------------------------------------------------------------------------
// Pure tests: no database.
// ---------------------------------------------------------------------------

func TestParseRequestID(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want int64
		ok   bool
	}{
		{"0", 0, true},
		{"1", 1, true},
		{"-1", -1, true},
		{"9223372036854775807", 9223372036854775807, true},
		{"-9223372036854775808", -9223372036854775808, true},
		{"", 0, false},
		{"+1", 0, false},
		{"01", 0, false},
		{"-0", 0, false},
		{" 1", 0, false},
		{"1 ", 0, false},
		{"1.0", 0, false},
		{"0x1", 0, false},
		{"9223372036854775808", 0, false},
		{"-9223372036854775809", 0, false},
		{"abc", 0, false},
	} {
		got, ok := parseRequestID(tc.in)
		if ok != tc.ok || (ok && got != tc.want) {
			t.Errorf("parseRequestID(%q) = %d, %v; want %d, %v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

func TestInt64Array(t *testing.T) {
	if got := int64Array([]int64{-9223372036854775808, -1, 0, 9223372036854775807}); got != "{-9223372036854775808,-1,0,9223372036854775807}" {
		t.Errorf("int64Array = %s", got)
	}
	if got := int64Array(nil); got != "{}" {
		t.Errorf("int64Array(nil) = %s", got)
	}
}

func hashFields(set map[string]any) []sql.NullString {
	out := make([]sql.NullString, len(reasonColumns))
	for i := range out {
		out[i] = sql.NullString{String: "x", Valid: true}
	}
	for name, v := range set {
		if v == nil {
			out[reasonColIndex[name]] = sql.NullString{}
		} else {
			out[reasonColIndex[name]] = sql.NullString{String: v.(string), Valid: true}
		}
	}
	return out
}

// referenceHash re-derives a hash straight from the written contract, so the
// pinned vectors below do not only agree with the code that made them.
func referenceHash(secret string, viewer, party string, fields []sql.NullString) string {
	k := sha256.Sum256([]byte("fold:erasure-reason-review:v1:" + secret))
	mac := hmac.New(sha256.New, k[:])
	mac.Write([]byte{0x66, 0x6f, 0x6c, 0x64, 0x2d, 0x72, 0x65, 0x61, 0x73, 0x6f, 0x6e, 0x2d, 0x72, 0x65, 0x76, 0x69, 0x65, 0x77, 0x2d, 0x76, 0x31})
	put := func(s string, valid bool) {
		if !valid {
			mac.Write([]byte{0})
			return
		}
		var n [4]byte
		binary.BigEndian.PutUint32(n[:], uint32(len(s)))
		mac.Write([]byte{1})
		mac.Write(n[:])
		mac.Write([]byte(s))
	}
	put(viewer, true)
	put(party, true)
	for _, f := range fields {
		put(f.String, f.Valid)
	}
	return hex.EncodeToString(mac.Sum(nil))
}

// reviewHashOf is the hex form of reviewHashRaw, which the page shows.
func reviewHashOf(key []byte, viewerID, partyID int64, fields []sql.NullString) string {
	raw := reviewHashRaw(key, viewerID, partyID, fields)
	return hex.EncodeToString(raw[:])
}

func TestReviewHashVectors(t *testing.T) {
	const secret = "fold-test-secret"
	key := testReasonKey([]byte(secret))
	if len(reasonColumns) != 28 {
		t.Fatalf("the hash covers %d columns, want 28", len(reasonColumns))
	}
	for _, tc := range []struct {
		name          string
		viewer, party int64
		set           map[string]any
		want          string
	}{
		{"all fields set", 7, 9, nil, "62bc84efdabac06547cb53c63e8d7d18abee200eed8b9c371aee1022eb3aa9bc"},
		{"viewer and party swapped", 9, 7, nil, "c5c2a6561726abe849f90eeaeaa3bb971f906bc83d5480ee50c5234a14090b39"},
		{"NULL reason", 7, 9, map[string]any{"reason": nil}, "a10f5178c10361e97eb28b02920c09c1ac77cb292ea0d411ed73737ce04dc1bf"},
		{"empty reason", 7, 9, map[string]any{"reason": ""}, "527616b21bcdc6abd7d5aace919bc5bd05f6a67f4f4c69218abbad63b6c114a0"},
		{"multi-byte and combining characters", 7, 9, map[string]any{"reason": "café ☕ é"}, "d02750065bf4062e628be1ae4ac04aaaad6bcd25e83709a359bba9c769760105"},
		{"extreme signed ids", -1, 9223372036854775807, map[string]any{"id": "-9223372036854775808"}, "c7225ab506e16f5fd7b3f71bcfe6fa5a57e529ac5e01800f9772341d05928b04"},
	} {
		fields := hashFields(tc.set)
		got := reviewHashOf(key, tc.viewer, tc.party, fields)
		if got != tc.want {
			t.Errorf("%s: hash %s, want %s", tc.name, got, tc.want)
		}
		if ref := referenceHash(secret, strconv.FormatInt(tc.viewer, 10), strconv.FormatInt(tc.party, 10), fields); ref != got {
			t.Errorf("%s: the hash differs from the reference derivation: %s vs %s", tc.name, got, ref)
		}
		if !validReviewHash(got) {
			t.Errorf("%s: %q is not a valid review hash", tc.name, got)
		}
	}
	// NULL and the empty string are different values; a hash made with another
	// secret differs.
	a := reviewHashOf(key, 1, 2, hashFields(map[string]any{"reason": nil}))
	b := reviewHashOf(key, 1, 2, hashFields(map[string]any{"reason": ""}))
	c := reviewHashOf(testReasonKey([]byte("another secret")), 1, 2, hashFields(map[string]any{"reason": ""}))
	if a == b || b == c {
		t.Error("NULL, the empty string and another secret must all hash differently")
	}
}

func TestValidReviewHash(t *testing.T) {
	good := strings.Repeat("a1", 32)
	if !validReviewHash(good) {
		t.Fatal("a 64-character lowercase hex string was refused")
	}
	for _, bad := range []string{"", good[:63], good + "0", strings.ToUpper(good), strings.Repeat("g", 64), " " + good[1:]} {
		if validReviewHash(bad) {
			t.Errorf("%q was accepted", bad)
		}
	}
}

func TestReasonProjectionShape(t *testing.T) {
	seen := map[string]bool{}
	for _, c := range reasonColumns {
		if seen[c.name] {
			t.Errorf("column %s is listed twice", c.name)
		}
		seen[c.name] = true
		for _, ex := range reasonHashExcluded {
			if c.name == ex {
				t.Errorf("%s is excluded but also hashed", c.name)
			}
		}
	}
	if reasonColumns[0].name != "id" {
		t.Errorf("the first hashed column is %s, want id", reasonColumns[0].name)
	}
	proj := reasonProjection()
	if strings.Count(proj, ", ")+1 < len(reasonColumns) {
		t.Errorf("the projection has fewer parts than columns: %s", proj)
	}
	if strings.Contains(proj, "add_childcare_ages_note") {
		t.Error("the ages note must stay out of the hash")
	}
	if !strings.HasSuffix(reasonSelect, ", created_at") {
		t.Errorf("reasonSelect must end with created_at: %s", reasonSelect)
	}
}

func TestLiveReasonFilter(t *testing.T) {
	f := liveReasonFilter(5)
	for _, tc := range []struct {
		name string
		s    reasonSource
		want int
	}{
		{"requester with text", reasonSource{ID: 1, RequesterID: 5, SubjectUserID: 6, HasReason: true}, 1},
		{"subject with text", reasonSource{ID: 1, RequesterID: 6, SubjectUserID: 5, HasReason: true}, 1},
		{"blank reason", reasonSource{ID: 1, RequesterID: 5, SubjectUserID: 6}, 0},
		{"another account's request", reasonSource{ID: 1, RequesterID: 6, SubjectUserID: 7, HasReason: true}, 0},
	} {
		if got := f(tc.s); len(got) != tc.want || (tc.want == 1 && got[0] != 5) {
			t.Errorf("%s: parties %v, want %d", tc.name, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// Database tests.
// ---------------------------------------------------------------------------

// TestReviewHashCoversEveryColumn reads information_schema.columns and fails on
// any column that is neither hashed nor excluded, a hashed column that no
// longer exists, and any data_type the projection has no rule for.
func TestReviewHashCoversEveryColumn(t *testing.T) {
	app := testApp(t)
	var cols []struct {
		ColumnName string
		DataType   string
	}
	if err := app.DB.Raw("SELECT column_name, data_type FROM information_schema.columns WHERE table_schema = 'public' AND table_name = 'contact_requests' ORDER BY ordinal_position").Scan(&cols).Error; err != nil {
		t.Fatal(err)
	}
	hashed := map[string]string{}
	for _, c := range reasonColumns {
		hashed[c.name] = c.kind
	}
	excluded := map[string]bool{}
	for _, c := range reasonHashExcluded {
		excluded[c] = true
	}
	var inTable []string
	for _, c := range cols {
		inTable = append(inTable, c.ColumnName)
		kind, ok := hashed[c.ColumnName]
		switch {
		case excluded[c.ColumnName]:
		case !ok:
			t.Errorf("contact_requests.%s (%s) is neither hashed nor excluded from the review hash", c.ColumnName, c.DataType)
		case kind != c.DataType:
			t.Errorf("contact_requests.%s is %s in the table but %s in reasonColumns", c.ColumnName, c.DataType, kind)
		}
		if _, ok := map[string]bool{reasonBigint: true, reasonText: true, reasonBool: true, reasonTime: true}[c.DataType]; !ok && !excluded[c.ColumnName] {
			t.Errorf("contact_requests.%s has data_type %s, which the projection has no rule for", c.ColumnName, c.DataType)
		}
	}
	for name := range hashed {
		found := false
		for _, c := range inTable {
			found = found || c == name
		}
		if !found {
			t.Errorf("reasonColumns names %s, which is not a column", name)
		}
	}
	// Table order, so the projection and a future dump agree.
	var order []string
	for _, c := range inTable {
		if !excluded[c] {
			order = append(order, c)
		}
	}
	var listed []string
	for _, c := range reasonColumns {
		listed = append(listed, c.name)
	}
	if strings.Join(order, ",") != strings.Join(listed, ",") {
		t.Errorf("reasonColumns is not in table order:\n table %v\n list  %v", order, listed)
	}
}

// TestReasonProjectionAgainstPostgres: the SQL projection of each kind, with
// NULLs, both infinities, a pre-1970 instant, microsecond precision and an id
// above 2^53. The expected texts are what the contract says; the first run on
// PostgreSQL 16 confirms them.
func TestReasonProjectionAgainstPostgres(t *testing.T) {
	app := testApp(t)
	var got [5]sql.NullString
	row := app.DB.Raw(`SELECT
	 (9007199254740993::bigint)::text,
	 CASE true WHEN true THEN 't' WHEN false THEN 'f' END,
	 trunc(extract(epoch FROM timestamptz '1970-01-01 00:00:00.000001+00') * 1000000)::text,
	 trunc(extract(epoch FROM timestamptz '1969-12-31 23:59:59.999999+00') * 1000000)::text,
	 CASE WHEN timestamptz 'infinity' = 'infinity' THEN 'infinity' END`).Row()
	if err := row.Scan(&got[0], &got[1], &got[2], &got[3], &got[4]); err != nil {
		t.Fatal(err)
	}
	want := []string{"9007199254740993", "t", "1", "-1", "infinity"}
	for i, w := range want {
		if got[i].String != w {
			t.Errorf("projection %d = %q, want %q", i, got[i].String, w)
		}
	}
	// And through the real projection: a row's id and flags come back as text.
	scene := newCoreErasureScene(t)
	target := newTestUser(t, scene.app, "projection-target", erasurePW, models.RoleMember)
	r := erasureMakeRequest(t, scene.app.DB, policy.RequestGroupAdd, policy.ContactWithdrawn, scene.second.ID, target.ID, 1, 0, 0, 1, "")
	rows, err := scene.app.DB.Raw("SELECT "+reasonSelect+" FROM contact_requests WHERE id = ?", r.ID).Rows()
	if err != nil {
		t.Fatal(err)
	}
	var seen []reasonRow
	if err := scanReasonRows(rows, testReasonKey([]byte("k")), 1, 2, func(rr reasonRow) error { seen = append(seen, rr); return nil }); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 1 || seen[0].ID != int64(r.ID) || seen[0].Purpose != policy.RequestGroupAdd || seen[0].Reason != r.Reason || !validReviewHash(seen[0].Hash) {
		t.Errorf("scanned %+v for request %d", seen, r.ID)
	}
}

// reasonScene is a deletion scene with a target that other people's requests
// mention.
type reasonScene struct {
	coreErasureScene
	target *models.User
	other  *models.User
}

func newReasonScene(t *testing.T) reasonScene {
	t.Helper()
	s := reasonScene{coreErasureScene: newCoreErasureScene(t)}
	// The review tests send their own reviews, or none.
	s.ac.autoReview = false
	s.target = newTestUser(t, s.app, "reason-target", erasurePW, models.RoleMember)
	s.other = newTestUser(t, s.app, "reason-other", erasurePW, models.RoleMember)
	return s
}

// request makes a terminal group_add request in which the target is the
// subject (their asked-for add) and the requester is who.
func (s reasonScene) request(t *testing.T, who *models.User, n int) models.ContactRequest {
	t.Helper()
	return erasureMakeRequest(t, s.app.DB, policy.RequestGroupAdd, policy.ContactWithdrawn, who.ID, s.target.ID, 1, 0, 0, n, "")
}

// contactRequest makes a terminal contact request in which the target is the
// requester.
func (s reasonScene) contactRequest(t *testing.T, n int) models.ContactRequest {
	t.Helper()
	return erasureMakeRequest(t, s.app.DB, policy.RequestContact, policy.ContactWithdrawn, s.target.ID, s.other.ID, 1, 0, 0, n, "")
}

func pageOf(t *testing.T, c *apiClient, target uint, after *string) (int, reasonPageJSON, string) {
	t.Helper()
	w := c.do("POST", "/api/v1/admin/users/"+itoa(target)+"/erasure-reasons/page", map[string]any{"after": after}, true)
	var page reasonPageJSON
	if w.Code == http.StatusOK {
		if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
			t.Fatalf("page body: %v", err)
		}
	}
	return w.Code, page, w.Body.String()
}

// allPages walks the traversal from a null cursor and returns every item.
func allPages(t *testing.T, c *apiClient, target uint) []reasonItemJSON {
	t.Helper()
	var items []reasonItemJSON
	var after *string
	for i := 0; i < 1000; i++ {
		code, page, body := pageOf(t, c, target, after)
		if code != http.StatusOK {
			t.Fatalf("page: %d %s", code, body)
		}
		items = append(items, page.Items...)
		if page.Next == nil {
			return items
		}
		after = page.Next
	}
	t.Fatal("the traversal never ended")
	return nil
}

// entriesOf turns served items into review entries with the given choices.
func entriesOf(items []reasonItemJSON, choice func(reasonItemJSON) string) []reviewEntry {
	var out []reviewEntry
	for _, it := range items {
		id, ok := parseRequestID(it.RequestID)
		if !ok {
			panic("entriesOf: bad request id " + it.RequestID)
		}
		out = append(out, reviewEntry{ID: id, Hash: it.ReviewHash, Choice: choice(it)})
	}
	return out
}

// reviewOf is the set-v1 envelope for items with the given choices.
func reviewOf(items []reasonItemJSON, choice func(reasonItemJSON) string) map[string]any {
	return envelopeOf(entriesOf(items, choice))
}

func countAudit(t *testing.T, db *gorm.DB, actions ...string) int64 {
	t.Helper()
	var n int64
	if err := db.Model(&models.AuditLog{}).Where("action IN ?", actions).Count(&n).Error; err != nil {
		t.Fatal(err)
	}
	return n
}

var decisionActions = []string{audit.ContactReasonKept, audit.ContactReasonRemoved, audit.AddReasonKept, audit.AddReasonRemoved}

func reasonTextOf(t *testing.T, db *gorm.DB, id uint) string {
	t.Helper()
	var s string
	if err := db.Raw("SELECT reason FROM contact_requests WHERE id = ?", id).Scan(&s).Error; err != nil {
		t.Fatal(err)
	}
	return s
}

// The page: items with every field, one view audit per nonempty page, no audit
// and no reviewRequired for an empty unit, and the adult rule.
func TestReasonReviewPage(t *testing.T) {
	s := newReasonScene(t)
	r1 := s.request(t, s.other, 1)
	r2 := s.contactRequest(t, 2)
	// An unrelated request and a blank reason never appear.
	erasureMakeRequest(t, s.app.DB, policy.RequestGroupAdd, policy.ContactWithdrawn, s.other.ID, s.second.ID, 1, 0, 0, 3, "")
	// A terminal add request's reason cannot be edited while its parties live
	// (the add trigger), so the blank one is a contact request.
	blank := s.contactRequest(t, 4)
	if err := s.app.DB.Exec("UPDATE contact_requests SET reason = '' WHERE id = ?", blank.ID).Error; err != nil {
		t.Fatal(err)
	}

	code, page, body := pageOf(t, s.ac, s.target.ID, nil)
	if code != http.StatusOK || !page.ReviewRequired || page.Next != nil || len(page.Items) != 2 {
		t.Fatalf("first page: %d reviewRequired=%v next=%v items=%d %s", code, page.ReviewRequired, page.Next, len(page.Items), body)
	}
	byID := map[string]reasonItemJSON{}
	for _, it := range page.Items {
		byID[it.RequestID] = it
		if !validReviewHash(it.ReviewHash) || it.CreatedAt.IsZero() {
			t.Errorf("item %s has no hash or time: %+v", it.RequestID, it)
		}
	}
	a, b := byID[strconv.FormatUint(uint64(r1.ID), 10)], byID[strconv.FormatUint(uint64(r2.ID), 10)]
	if a.Role != "subject" || a.Purpose != policy.RequestGroupAdd || a.Status != policy.ContactWithdrawn || a.Reason != r1.Reason ||
		a.CounterpartName != s.other.PublicName() || a.CounterpartDeleted || a.CounterpartRef != "" || a.ReviewerIsParty {
		t.Errorf("subject item: %+v", a)
	}
	if b.Role != "requester" || b.Purpose != policy.RequestContact || b.Reason != r2.Reason || b.ReviewerIsParty {
		t.Errorf("requester item: %+v", b)
	}
	if n := countAudit(t, s.app.DB, audit.UserErasureReasonsViewed); n != 1 {
		t.Fatalf("%d view audit rows, want 1", n)
	}
	var view models.AuditLog
	if err := s.app.DB.Where("action = ?", audit.UserErasureReasonsViewed).First(&view).Error; err != nil {
		t.Fatal(err)
	}
	if view.ActorID == nil || *view.ActorID != s.admin.ID || view.TargetType != "user" || view.TargetID != s.target.ID || view.IP != "" ||
		view.MetaJSON != `{"count": 2, "source": "deletion"}` {
		t.Errorf("view row: %+v", view)
	}
	if strings.Contains(view.MetaJSON, r1.Reason) {
		t.Error("the view audit must not hold reason text")
	}

	// reviewerIsParty: the second admin is the requester of a request about the target.
	s.request(t, s.second, 5)
	_, page, _ = pageOf(t, s.ac, s.target.ID, nil)
	parties := func(items []reasonItemJSON) int {
		n := 0
		for _, it := range items {
			if it.ReviewerIsParty {
				n++
			}
		}
		return n
	}
	if len(page.Items) != 3 || parties(page.Items) != 0 {
		t.Errorf("the first admin sees %d items, party to %d, want 3 and 0", len(page.Items), parties(page.Items))
	}
	second := loginAPI(t, s.app, "erase-second", erasurePW)
	code, page, body = pageOf(t, second, s.target.ID, nil)
	if code != http.StatusOK || len(page.Items) != 3 || parties(page.Items) != 1 {
		t.Errorf("the second admin (requester of one request) sees %d items, party to %d: %d %s", len(page.Items), parties(page.Items), code, body)
	}

	// A target with no reasons: an empty unit, no reviewRequired, no new audit.
	clean := newTestUser(t, s.app, "reason-clean", erasurePW, models.RoleMember)
	before := countAudit(t, s.app.DB, audit.UserErasureReasonsViewed)
	code, page, body = pageOf(t, s.ac, clean.ID, nil)
	if code != http.StatusOK || len(page.Items) != 0 || page.Next != nil || strings.Contains(body, "reviewRequired") || !strings.Contains(body, `"items":[]`) {
		t.Errorf("empty traversal: %d %s", code, body)
	}
	if countAudit(t, s.app.DB, audit.UserErasureReasonsViewed) != before {
		t.Error("an empty unit wrote a view audit")
	}

	// Their own account and a bad cursor.
	if code, _, _ := pageOf(t, s.ac, s.admin.ID, nil); code != http.StatusBadRequest {
		t.Errorf("own account: %d, want 400", code)
	}
	bad := "01"
	if code, _, _ := pageOf(t, s.ac, s.target.ID, &bad); code != http.StatusBadRequest {
		t.Errorf("non-canonical cursor: %d, want 400", code)
	}
}

// The adult rule (R4-C05): an admin of unknown age reviews; one known to be
// under 18 gets 403 with no text, no count and no audit.
func TestReasonReviewAdultRule(t *testing.T) {
	s := newReasonScene(t)
	r := s.request(t, s.other, 1)
	code, first, _ := pageOf(t, s.ac, s.target.ID, nil)
	if code != http.StatusOK || len(first.Items) != 1 {
		t.Fatalf("an admin of unknown age: %d", code)
	}
	before := countAudit(t, s.app.DB, audit.UserErasureReasonsViewed)
	birth := time.Now().AddDate(-15, 0, 0)
	if err := s.app.DB.Model(&models.User{}).Where("id = ?", s.admin.ID).Update("birth_date", birth).Error; err != nil {
		t.Fatal(err)
	}
	code, _, body := pageOf(t, s.ac, s.target.ID, nil)
	if code != http.StatusForbidden || !strings.Contains(body, `"code":"reason_review_not_allowed"`) || !strings.Contains(body, `"reviewRequired":true`) || strings.Contains(body, r.Reason) {
		t.Fatalf("a known-underage admin: %d %s", code, body)
	}
	if countAudit(t, s.app.DB, audit.UserErasureReasonsViewed) != before {
		t.Error("a refused page wrote a view audit")
	}
	// And the deletion with a nonempty set refuses the same admin; an empty set
	// would not.
	if res := deleteAccount(s.ac, s.target.ID, map[string]any{"reasonReview": reviewOf(first.Items, func(reasonItemJSON) string { return "keep" })}); res.code != http.StatusForbidden {
		t.Errorf("delete by a known-underage admin: %d %s", res.code, res.body)
	}
	if reasonTextOf(t, s.app.DB, r.ID) == "" {
		t.Error("a refused deletion blanked a reason")
	}
}

// The traversal: every unit is bounded, a request is returned once, and the
// client concludes "no reasons" only from a walk that ends with none.
func TestReasonScanNoSkip(t *testing.T) {
	prevRows, prevReqs := reasonUnitRows, reasonPageRequests
	reasonUnitRows, reasonPageRequests = 7, 3
	defer func() { reasonUnitRows, reasonPageRequests = prevRows, prevReqs }()

	s := newReasonScene(t)
	// 20 unrelated blank-reason or other-account rows around the target's, so
	// several units pass with nothing in them.
	var mine []int64
	for i := 0; i < 8; i++ {
		for j := 0; j < 4; j++ {
			erasureMakeRequest(t, s.app.DB, policy.RequestGroupAdd, policy.ContactWithdrawn, s.other.ID, s.second.ID, 1, 0, 0, 100+i*4+j, "")
		}
		mine = append(mine, int64(s.request(t, s.other, i).ID))
	}
	items := allPages(t, s.ac, s.target.ID)
	var got []int64
	for _, it := range items {
		id, ok := parseRequestID(it.RequestID)
		if !ok {
			t.Fatalf("item id %q", it.RequestID)
		}
		got = append(got, id)
	}
	sort.Slice(got, func(i, j int) bool { return got[i] < got[j] })
	sort.Slice(mine, func(i, j int) bool { return mine[i] < mine[j] })
	if fmt.Sprint(got) != fmt.Sprint(mine) {
		t.Fatalf("the traversal returned %v, want exactly %v", got, mine)
	}
	// The same table for an account with no reasons walks to the end empty.
	clean := newTestUser(t, s.app, "scan-clean", erasurePW, models.RoleMember)
	if got := allPages(t, s.ac, clean.ID); len(got) != 0 {
		t.Errorf("an account with no reasons returned %d items", len(got))
	}
}

// Statement shapes: the first page has no lower bound but still reads at most
// reasonUnitRows rows, and a page route never scans the table unbounded.
func TestReasonScanIsBounded(t *testing.T) {
	prev := reasonUnitRows
	reasonUnitRows = 5
	defer func() { reasonUnitRows = prev }()
	s := newReasonScene(t)
	for i := 0; i < 12; i++ {
		erasureMakeRequest(t, s.app.DB, policy.RequestGroupAdd, policy.ContactWithdrawn, s.other.ID, s.second.ID, 1, 0, 0, i, "")
	}
	var unit reasonUnit
	err := s.app.DB.Transaction(func(tx *gorm.DB) error {
		var err error
		unit, err = scanReasonItems(tx, liveReasonFilter(int64(s.target.ID)), nil, reasonPageRequests)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(unit.Requests) != 0 || unit.Next == nil {
		t.Fatalf("a unit over unrelated rows: %+v", unit)
	}
	var ids []int64
	if err := s.app.DB.Raw("SELECT id FROM contact_requests ORDER BY id LIMIT 5").Scan(&ids).Error; err != nil {
		t.Fatal(err)
	}
	if *unit.Next != ids[len(ids)-1] {
		t.Errorf("next = %d, want the fifth id %d", *unit.Next, ids[len(ids)-1])
	}
}

// Missing, extra, changed, unchosen, duplicate, another admin's hash, the other
// party's hash and malformed entries each refuse with nothing changed and no
// decision audit; an empty current set deletes normally.
func TestReasonReviewDeleteExactSet(t *testing.T) {
	type setup struct {
		s     reasonScene
		items []reasonItemJSON
		reqs  []models.ContactRequest
	}
	prepare := func(t *testing.T) setup {
		s := newReasonScene(t)
		reqs := []models.ContactRequest{s.request(t, s.other, 1), s.request(t, s.second, 2), s.contactRequest(t, 3)}
		return setup{s: s, items: allPages(t, s.ac, s.target.ID), reqs: reqs}
	}
	keepAll := func(reasonItemJSON) string { return "keep" }
	cases := []struct {
		name string
		code int
		api  string
		body func(t *testing.T, p setup) any
	}{
		{"no review at all", http.StatusConflict, "needs_reason_review", func(t *testing.T, p setup) any { return nil }},
		{"a missing item", http.StatusConflict, "reason_review_changed", func(t *testing.T, p setup) any { return reviewOf(p.items[1:], keepAll) }},
		{"an extra item", http.StatusConflict, "reason_review_changed", func(t *testing.T, p setup) any {
			return envelopeOf(append(entriesOf(p.items, keepAll), reviewEntry{ID: 999999, Hash: strings.Repeat("0", 64), Choice: "keep"}))
		}},
		{"a changed row", http.StatusConflict, "reason_review_changed", func(t *testing.T, p setup) any {
			// A contact request: a terminal add request cannot be edited while
			// its parties live.
			if err := p.s.app.DB.Exec("UPDATE contact_requests SET reason = reason || '!' WHERE id = ?", p.reqs[2].ID).Error; err != nil {
				t.Fatal(err)
			}
			return reviewOf(p.items, keepAll)
		}},
		{"a hashed timestamp column differs", http.StatusConflict, "reason_review_changed", func(t *testing.T, p setup) any {
			// A contact request: terminal add requests cannot be edited while their
			// parties live.
			if err := p.s.app.DB.Exec("UPDATE contact_requests SET expires_at = expires_at + interval '1 day' WHERE id = ?", p.reqs[2].ID).Error; err != nil {
				t.Fatal(err)
			}
			return reviewOf(p.items, keepAll)
		}},
		{"another admin's hash", http.StatusConflict, "reason_review_changed", func(t *testing.T, p setup) any {
			second := loginAPI(t, p.s.app, "erase-second", erasurePW)
			return reviewOf(allPages(t, second, p.s.target.ID), keepAll)
		}},
		{"a hash for the other party", http.StatusConflict, "reason_review_changed", func(t *testing.T, p setup) any {
			// The same rows hashed for the counterpart: viewer and party differ.
			rows, err := p.s.app.DB.Raw("SELECT "+reasonSelect+" FROM contact_requests WHERE reason <> '' AND (requester_id = ? OR subject_user_id = ?) ORDER BY contact_requests.id", p.s.target.ID, p.s.target.ID).Rows()
			if err != nil {
				t.Fatal(err)
			}
			var items []reasonItemJSON
			if err := scanReasonRows(rows, testReasonKey(p.s.app.Config.SessionSecret), int64(p.s.admin.ID), int64(p.s.other.ID), func(r reasonRow) error {
				items = append(items, reasonItemJSON{RequestID: strconv.FormatInt(r.ID, 10), ReviewHash: r.Hash})
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			return reviewOf(items, keepAll)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := prepare(t)
			if len(p.items) != 3 {
				t.Fatalf("%d items, want 3", len(p.items))
			}
			var body any
			if b := tc.body(t, p); b != nil {
				body = map[string]any{"reasonReview": b}
			}
			res := deleteAccount(p.s.ac, p.s.target.ID, body)
			if res.code != tc.code || !strings.Contains(res.body, `"code":"`+tc.api+`"`) {
				t.Fatalf("%s: %d %s, want %d %s", tc.name, res.code, res.body, tc.code, tc.api)
			}
			if n := countAudit(t, p.s.app.DB, decisionActions...); n != 0 {
				t.Errorf("%d decision audits after a refusal", n)
			}
			var u models.User
			if err := p.s.app.DB.Unscoped().First(&u, p.s.target.ID).Error; err != nil || u.DeletedAt.Valid {
				t.Errorf("the account was deleted by a refusal: %v", err)
			}
			for _, r := range p.reqs {
				if reasonTextOf(t, p.s.app.DB, r.ID) == "" {
					t.Errorf("request %d was blanked by a refusal", r.ID)
				}
			}
		})
	}

	// Malformed entries are 400s that end the id; the account is untouched.
	t.Run("malformed entries", func(t *testing.T) {
		p := prepare(t)
		h := strings.Repeat("ab", 32)
		for name, items := range map[string][]map[string]string{
			"duplicate":    {{"requestId": p.items[0].RequestID, "reviewHash": h, "choice": "keep"}, {"requestId": p.items[0].RequestID, "reviewHash": h, "choice": "keep"}},
			"bad id":       {{"requestId": "+1", "reviewHash": h, "choice": "keep"}},
			"short hash":   {{"requestId": "1", "reviewHash": "ab", "choice": "keep"}},
			"unchosen":     {{"requestId": p.items[0].RequestID, "reviewHash": p.items[0].ReviewHash, "choice": ""}},
			"wrong choice": {{"requestId": p.items[0].RequestID, "reviewHash": p.items[0].ReviewHash, "choice": "blank"}},
		} {
			res := deleteAccount(p.s.ac, p.s.target.ID, map[string]any{"reasonReview": map[string]any{"items": items}})
			if res.code != http.StatusBadRequest {
				t.Errorf("%s: %d %s, want 400", name, res.code, res.body)
			}
		}
		if n := countAudit(t, p.s.app.DB, decisionActions...); n != 0 {
			t.Errorf("%d decision audits", n)
		}
	})

	// A stale body does nothing when the set is empty.
	t.Run("an empty current set deletes normally", func(t *testing.T) {
		s := newReasonScene(t)
		stale := reviewOf([]reasonItemJSON{{RequestID: "5", ReviewHash: strings.Repeat("c", 64)}}, func(reasonItemJSON) string { return "remove" })
		res := deleteAccount(s.ac, s.target.ID, map[string]any{"reasonReview": stale})
		if res.code != http.StatusOK {
			t.Fatalf("delete: %d %s", res.code, res.body)
		}
		if n := countAudit(t, s.app.DB, decisionActions...); n != 0 {
			t.Errorf("%d decision audits for an empty set", n)
		}
	})

	// The mixed deletion: removed rows blank, kept rows stay, one decision audit
	// per item with exact meta, the ages note still goes, the counts are reported.
	t.Run("a mixed deletion", func(t *testing.T) {
		p := prepare(t)
		extra := erasureMakeRequest(t, p.s.app.DB, policy.RequestGroupAdd, policy.ContactWithdrawn, p.s.target.ID, p.s.other.ID, 1, 0, 0, 9, "")
		// A date add request about the target with an ages note: the note goes
		// with the deletion (R4-T07A) whatever the admin chooses for the reason,
		// and the reason is a separate choice (kept here).
		eventID, occ, groupID := erasureEventFixture(t, p.s.coreErasureScene)
		ages := erasureMakeRequest(t, p.s.app.DB, policy.RequestOccurrenceAdd, policy.ContactDenied, p.s.other.ID, p.s.target.ID, groupID, eventID, occ.ID, 10, "ages 3 and 5")
		p.items = allPages(t, p.s.ac, p.s.target.ID)
		remove := map[string]bool{
			strconv.FormatUint(uint64(p.reqs[0].ID), 10): true,
			strconv.FormatUint(uint64(extra.ID), 10):     true,
		}
		res := deleteAccount(p.s.ac, p.s.target.ID, map[string]any{"reasonReview": reviewOf(p.items, func(it reasonItemJSON) string {
			if remove[it.RequestID] {
				return "remove"
			}
			return "keep"
		})})
		if res.code != http.StatusOK {
			t.Fatalf("delete: %d %s", res.code, res.body)
		}
		var out struct {
			Erasure erasureReportJSON `json:"erasure"`
		}
		if err := json.Unmarshal([]byte(res.body), &out); err != nil {
			t.Fatal(err)
		}
		if out.Erasure.ReasonsKept != 3 || out.Erasure.ReasonsRemoved != 2 {
			t.Errorf("report %+v, want 3 kept and 2 removed", out.Erasure)
		}
		var note string
		if err := p.s.app.DB.Raw("SELECT add_childcare_ages_note FROM contact_requests WHERE id = ?", ages.ID).Scan(&note).Error; err != nil || note != "" {
			t.Errorf("ages note = %q (%v), want blanked by the deletion", note, err)
		}
		if got := reasonTextOf(t, p.s.app.DB, ages.ID); got != ages.Reason {
			t.Errorf("the kept date-add reason = %q, want it kept as %q", got, ages.Reason)
		}
		if n := countAudit(t, p.s.app.DB, audit.AddReasonKept); n != 2 {
			t.Errorf("%d add.reason_kept rows, want 2 (the group add and the date add)", n)
		}
		for _, r := range append(append([]models.ContactRequest{}, p.reqs...), extra) {
			blanked := remove[strconv.FormatUint(uint64(r.ID), 10)]
			if got := reasonTextOf(t, p.s.app.DB, r.ID); blanked && got != "" || !blanked && got != r.Reason {
				t.Errorf("request %d reason = %q (removed: %v)", r.ID, got, blanked)
			}
		}
		var rows []models.AuditLog
		if err := p.s.app.DB.Where("action IN ?", decisionActions).Order("target_id").Find(&rows).Error; err != nil {
			t.Fatal(err)
		}
		if len(rows) != 5 {
			t.Fatalf("%d decision audits, want 5", len(rows))
		}
		for _, row := range rows {
			if row.ActorID == nil || *row.ActorID != p.s.admin.ID {
				t.Errorf("decision row %+v lacks the deciding admin", row)
			}
			var meta map[string]any
			if err := json.Unmarshal([]byte(row.MetaJSON), &meta); err != nil {
				t.Fatal(err)
			}
			keys := make([]string, 0, len(meta))
			for k := range meta {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			if strings.Join(keys, ",") != "erasedRole,erasedUserId,purpose,reviewVersion,reviewerIsParty,source,status" {
				t.Errorf("meta keys %v", keys)
			}
			if meta["erasedUserId"] != float64(p.s.target.ID) || meta["source"] != "deletion" || meta["reviewVersion"] != float64(1) {
				t.Errorf("meta %v", meta)
			}
			if strings.Contains(row.MetaJSON, "identifying reason") {
				t.Error("a decision row holds reason text")
			}
		}
	})
}

// Request ids are signed 64-bit end to end (R4-C04): explicit ids at both ends
// of the range and either side of zero are shown, decided, audited and blanked
// exactly.
func TestSignedRequestIDsEndToEnd(t *testing.T) {
	s := newReasonScene(t)
	ids := []int64{-9223372036854775808, -1, 0, 9223372036854775807}
	for i, id := range ids {
		r := s.contactRequest(t, 10+i)
		// Contact rows may take any id: the add-request trigger guards only add rows.
		if err := s.app.DB.Exec("UPDATE contact_requests SET id = ? WHERE id = ?", id, r.ID).Error; err != nil {
			t.Fatal(err)
		}
	}
	items := allPages(t, s.ac, s.target.ID)
	if len(items) != len(ids) {
		t.Fatalf("%d items, want %d", len(items), len(ids))
	}
	got := map[string]bool{}
	for _, it := range items {
		got[it.RequestID] = true
	}
	for _, id := range ids {
		if !got[strconv.FormatInt(id, 10)] {
			t.Errorf("id %d is missing from the pages: %v", id, got)
		}
	}
	// Remove the even positions, keep the others.
	res := deleteAccount(s.ac, s.target.ID, map[string]any{"reasonReview": reviewOf(items, func(it reasonItemJSON) string {
		if it.RequestID == "-9223372036854775808" || it.RequestID == "0" {
			return "remove"
		}
		return "keep"
	})})
	if res.code != http.StatusOK {
		t.Fatalf("delete: %d %s", res.code, res.body)
	}
	var removed, kept []string
	if err := s.app.DB.Raw("SELECT target_id::text FROM audit_logs WHERE action = ? ORDER BY audit_logs.target_id", audit.ContactReasonRemoved).Scan(&removed).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.app.DB.Raw("SELECT target_id::text FROM audit_logs WHERE action = ? ORDER BY audit_logs.target_id", audit.ContactReasonKept).Scan(&kept).Error; err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(removed) != "[-9223372036854775808 0]" || fmt.Sprint(kept) != "[-1 9223372036854775807]" {
		t.Errorf("removed %v kept %v", removed, kept)
	}
	var blank []string
	if err := s.app.DB.Raw("SELECT id::text FROM contact_requests WHERE reason = '' ORDER BY contact_requests.id").Scan(&blank).Error; err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(blank) != "[-9223372036854775808 0]" {
		t.Errorf("blanked rows %v", blank)
	}
}

// A page whose admin loses the right to act between the early read and the
// seal answers 403 with no text and commits no view audit; a busy actor row
// is retried three times and answers 503 reason_review_busy, never an empty
// page.
func TestReasonReviewSealOnPages(t *testing.T) {
	s := newReasonScene(t)
	s.request(t, s.other, 1)
	var seals atomic.Int64
	s.app.readBeforeSeal = func(*gorm.DB) { seals.Add(1) }

	hold, _ := beginOwnedTx(t, s.app.DB)
	if err := hold.Exec("SELECT id FROM users WHERE id = ? FOR NO KEY UPDATE", s.admin.ID).Error; err != nil {
		t.Fatal(err)
	}
	code, _, body := pageOf(t, s.ac, s.target.ID, nil)
	if code != http.StatusServiceUnavailable || !strings.Contains(body, `"code":"reason_review_busy"`) || strings.Contains(body, "identifying reason") {
		t.Fatalf("busy actor row: %d %s", code, body)
	}
	if seals.Load() != 3 {
		t.Errorf("%d attempts under the hold, want 3", seals.Load())
	}
	if n := countAudit(t, s.app.DB, audit.UserErasureReasonsViewed); n != 0 {
		t.Errorf("%d view audits committed by busy attempts", n)
	}
	hold.Rollback()

	// Demoted at the seam: another connection commits the demotion after the
	// early read. The second admin keeps the system valid.
	seals.Store(0)
	s.app.readBeforeSeal = func(*gorm.DB) {
		if seals.Add(1) == 1 {
			if err := s.app.DB.Exec("UPDATE users SET role = 'member' WHERE id = ?", s.admin.ID).Error; err != nil {
				t.Error(err)
			}
		}
	}
	code, _, body = pageOf(t, s.ac, s.target.ID, nil)
	if code != http.StatusForbidden {
		t.Fatalf("demoted at the seam: %d %s, want 403", code, body)
	}
	if strings.Contains(body, "identifying reason") {
		t.Error("a refused page carried reason text")
	}
	if n := countAudit(t, s.app.DB, audit.UserErasureReasonsViewed); n != 0 {
		t.Errorf("%d view audits after a refused seal", n)
	}
}

// Request ids and tombstone references are signed 64-bit values, so they travel
// as decimal strings (R4-C04): a Go field of another type, or a TypeScript
// field typed number, would silently lose precision above 2^53.
func TestReasonTypesUseStringIDs(t *testing.T) {
	typ := reflect.TypeOf(reasonItemJSON{})
	for _, name := range []string{"RequestID", "CounterpartRef"} {
		f, ok := typ.FieldByName(name)
		if !ok || f.Type.Kind() != reflect.String {
			t.Errorf("reasonItemJSON.%s must be a string, got %v", name, f.Type)
		}
	}
	src, err := os.ReadFile("web/src/types.ts")
	if err != nil {
		t.Fatal(err)
	}
	ts := string(src)
	for _, want := range []string{"  requestId: string;", "  counterpartRef?: string;", "  next: string | null;"} {
		if strings.Count(ts, want) < 1 {
			t.Errorf("web/src/types.ts lacks %q in ErasureReasonItem or ErasureReasonPage", want)
		}
	}
	// The pair check covers every field name; this covers the page's own cursor.
	next, _ := reflect.TypeOf(reasonPageJSON{}).FieldByName("Next")
	if next.Type.Kind() != reflect.Pointer || next.Type.Elem().Kind() != reflect.String {
		t.Errorf("reasonPageJSON.Next must be *string, got %v", next.Type)
	}
}

// ---------------------------------------------------------------------------
// Restore with a panel open (R3-C01, design TestReasonReviewRestoreWithOpenPanel).
// ---------------------------------------------------------------------------

// The review is stateless, so a restore cannot map a confirmation onto other
// choices. An admin has read the pages and minted an id; then the database is
// restored (a restart, pg_restore --clean while the process runs, or the
// data-only restore of the fence and the receipts). The answers:
//   - while the process runs, the first fenced route is 503 restore_detected;
//   - a restarted process answers the old id attempt_expired;
//   - a fresh id with the same body applies exactly the body's choices when the
//     rows are as the pages showed them, and is 409 reason_review_changed when
//     any hashed column differs, with nothing changed either way for the rows
//     the body does not name.
func TestReasonReviewRestoreWithOpenPanel(t *testing.T) {
	fenceTools(t)
	for _, v := range []struct {
		name                       string
		restart, dataOnly, changed bool
	}{
		{"restart, facts unchanged", true, false, false},
		{"restart, a hashed column differs", true, false, true},
		{"pg_restore --clean while running, facts unchanged", false, false, false},
		{"pg_restore --clean while running, a hashed column differs", false, false, true},
		{"data-only restore while running, facts unchanged", false, true, false},
		{"data-only restore while running, a hashed column differs", false, true, true},
	} {
		t.Run(v.name, func(t *testing.T) {
			s := newAttemptScene(t)
			db := s.app.DB
			target, other := s.target("panel-target"), s.target("panel-other")
			x, y, z := s.target("panel-x"), s.target("panel-y"), s.target("panel-z")
			mk := func(purpose string, n int) models.ContactRequest {
				return erasureMakeRequest(t, db, purpose, policy.ContactWithdrawn, other.ID, target.ID, 1, 0, 0, n, "")
			}
			kept, edited, removed := mk(policy.RequestGroupAdd, 1), mk(policy.RequestContact, 2), mk(policy.RequestGroupAdd, 3)
			// Neither party is an account this test deletes, so it needs no review.
			bystander := erasureMakeRequest(t, db, policy.RequestGroupAdd, policy.ContactWithdrawn, other.ID, z.ID, 1, 0, 0, 4, "")
			text := func(id uint) string { return reasonTextOf(t, db, id) }
			wantText := map[uint]string{kept.ID: kept.Reason, edited.ID: edited.Reason, removed.ID: removed.Reason, bystander.ID: bystander.Reason}

			// Two fenced commits around the dump, so a running process has
			// acknowledged one the restore loses.
			if w := s.del(s.mint(x.ID), x.ID); w.Code != http.StatusOK {
				t.Fatalf("delete X: %d %s", w.Code, w.Body.String())
			}
			dump := fenceDump(t, db)
			if w := s.del(s.mint(y.ID), y.ID); w.Code != http.StatusOK {
				t.Fatalf("delete Y: %d %s", w.Code, w.Body.String())
			}
			mutate := func() {
				// Only a contact request's reason can be edited while its parties live.
				if err := db.Exec("UPDATE contact_requests SET reason = reason || ' (edited)' WHERE id = ?", edited.ID).Error; err != nil {
					t.Fatal(err)
				}
			}
			// A full restore rewinds a change made after the dump, so it is made
			// before the pages are read; a data-only restore leaves contact
			// requests alone, so the change is made after.
			if v.changed && !v.dataOnly {
				mutate()
			}
			items := allPages(t, s.c, target.ID)
			if len(items) != 3 {
				t.Fatalf("%d items, want 3", len(items))
			}
			review := reviewOf(items, func(it reasonItemJSON) string {
				if it.RequestID == strconv.FormatUint(uint64(kept.ID), 10) {
					return "keep"
				}
				return "remove"
			})
			oldID := s.mint(target.ID)
			if v.changed && v.dataOnly {
				mutate()
			}
			path := "/api/v1/admin/users/" + itoa(target.ID)
			body := func(id string) map[string]any { return map[string]any{"attemptId": id, "reasonReview": review} }
			fenceRestore(t, db, dump, v.dataOnly, s.direct)

			if !v.restart {
				// The same process: the first fenced route finds the restore.
				w := s.c.do("DELETE", path, body(oldID), true)
				if w.Code != http.StatusServiceUnavailable || codeOf(t, w) != "restore_detected" {
					t.Fatalf("the open panel's delete after the restore: %d %s, want 503 restore_detected", w.Code, w.Body.String())
				}
				if n := s.restarts.Load(); n != 1 {
					t.Fatalf("the restart recorder fired %d times, want once", n)
				}
			}
			// The process starts again (for the running variants, the restart the
			// recorder asked for).
			fresh := &App{DB: s.direct, Store: s.app.Store, Config: s.app.Config, Uploads: s.app.Uploads, Mailer: s.app.Mailer}
			bootFence(t, fresh)
			sub := loginAPI(t, fresh, "admin", "pw")

			w := sub.do("DELETE", path, body(oldID), true)
			if w.Code != http.StatusConflict || codeOf(t, w) != "attempt_expired" {
				t.Fatalf("the old id on the restarted process: %d %s, want 409 attempt_expired", w.Code, w.Body.String())
			}
			if s.deleted(target.ID) || countAudit(t, db, decisionActions...) != 0 {
				t.Fatal("the old id changed something")
			}
			mint := sub.do("POST", "/api/v1/admin/operation-attempts", map[string]any{"kind": attemptKindAccountDelete, "subjectUserId": target.ID}, true)
			var minted attemptMintJSON
			decodeJSON(t, mint, &minted)
			if len(minted.AttemptID) != 80 {
				t.Fatalf("mint on the restarted process: %d %s", mint.Code, mint.Body.String())
			}
			w = sub.do("DELETE", path, body(minted.AttemptID), true)
			if v.changed {
				if w.Code != http.StatusConflict || codeOf(t, w) != "reason_review_changed" {
					t.Fatalf("a fresh id over a differing row: %d %s, want 409 reason_review_changed", w.Code, w.Body.String())
				}
				if s.deleted(target.ID) || countAudit(t, db, decisionActions...) != 0 {
					t.Error("a refused review changed something")
				}
				for id, want := range wantText {
					if id != edited.ID && text(id) != want {
						t.Errorf("request %d reason changed to %q by a refused review", id, text(id))
					}
				}
				return
			}
			if w.Code != http.StatusOK {
				t.Fatalf("a fresh id over unchanged rows: %d %s, want 200", w.Code, w.Body.String())
			}
			// Exactly the body's choices, on exactly the rows the pages showed.
			if text(kept.ID) != wantText[kept.ID] || text(edited.ID) != "" || text(removed.ID) != "" {
				t.Errorf("reasons after the delete: kept %q, edited %q, removed %q", text(kept.ID), text(edited.ID), text(removed.ID))
			}
			if text(bystander.ID) != wantText[bystander.ID] {
				t.Error("a choice reached a row the review does not name")
			}
			var decided []string
			if err := db.Raw("SELECT action || ':' || target_id::text FROM audit_logs WHERE action IN ? ORDER BY target_id", decisionActions).Scan(&decided).Error; err != nil {
				t.Fatal(err)
			}
			want := []string{
				audit.AddReasonKept + ":" + strconv.FormatUint(uint64(kept.ID), 10),
				audit.ContactReasonRemoved + ":" + strconv.FormatUint(uint64(edited.ID), 10),
				audit.AddReasonRemoved + ":" + strconv.FormatUint(uint64(removed.ID), 10),
			}
			if fmt.Sprint(decided) != fmt.Sprint(want) {
				t.Errorf("decision audits %v, want %v", decided, want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Readiness (R4-C06): the traversal is the scanner itself.
// ---------------------------------------------------------------------------

type reasonStatement struct {
	SQL  string
	Vars []any
}

// recordContactRequestStatements sees every statement over contact_requests
// that GORM sends on db (Exec, Raw/Scan and Rows), with its bind values.
func recordContactRequestStatements(t *testing.T, db *gorm.DB) func() []reasonStatement {
	t.Helper()
	var mu sync.Mutex
	var out []reasonStatement
	capture := func(tx *gorm.DB) {
		sqlText := strings.Join(strings.Fields(tx.Statement.SQL.String()), " ")
		if strings.Contains(sqlText, "contact_requests") {
			mu.Lock()
			out = append(out, reasonStatement{SQL: sqlText, Vars: append([]any(nil), tx.Statement.Vars...)})
			mu.Unlock()
		}
	}
	name := fmt.Sprintf("test:reason_statements:%d", queryCounterSeq.Add(1))
	if err := db.Callback().Raw().After("gorm:raw").Register(name, capture); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Raw().Remove(name) })
	if err := db.Callback().Row().After("gorm:row").Register(name, capture); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Row().Remove(name) })
	return func() []reasonStatement {
		mu.Lock()
		defer mu.Unlock()
		return append([]reasonStatement(nil), out...)
	}
}

const reasonUnitPrefix = "SELECT id, requester_id, subject_user_id, (reason <> '') AS has_reason FROM contact_requests"

// unitCursor is the lower bound a recorded scanner statement carries (nil for
// the first page). The two statements differ in shape (F5): the first binds only
// the limit and has no id predicate; a continuation binds the cursor, then the
// limit, and filters on id > the cursor. Neither binds a NULL.
func unitCursor(t *testing.T, st reasonStatement) *int64 {
	t.Helper()
	switch len(st.Vars) {
	case 1:
		if strings.Contains(st.SQL, "WHERE") {
			t.Fatalf("a first-page scanner unit binds only the limit but has a predicate: %s", st.SQL)
		}
		return nil
	case 2:
		if !strings.Contains(st.SQL, "WHERE id > $1::bigint") {
			t.Fatalf("a continuation unit binds a cursor but does not filter on it: %s", st.SQL)
		}
		switch v := st.Vars[0].(type) {
		case *int64:
			if v == nil {
				t.Fatal("a continuation unit bound a nil cursor")
			}
			return v
		case int64:
			return &v
		default:
			t.Fatalf("cursor bound as %T", st.Vars[0])
		}
	default:
		t.Fatalf("a scanner unit binds %d values, want the limit alone or the cursor and the limit: %v", len(st.Vars), st.Vars)
	}
	return nil
}

// An account with no reasons over a table larger than three units reaches next
// null in units, each under its budget, with no audit and no adult check; a
// unit failure retried at the same cursor never rereads earlier units; and no
// page route ever scans contact_requests unbounded.
func TestReasonReviewReadiness(t *testing.T) {
	prev := reasonUnitRows
	reasonUnitRows = 5
	defer func() { reasonUnitRows = prev }()
	s := newReasonScene(t)
	// 17 unrelated requests: four units of five rows with a fifth short one.
	for i := 0; i < 17; i++ {
		erasureMakeRequest(t, s.app.DB, policy.RequestGroupAdd, policy.ContactWithdrawn, s.other.ID, s.second.ID, 1, 0, 0, i, "")
	}
	clean := newTestUser(t, s.app, "ready-clean", erasurePW, models.RoleMember)
	// An admin known to be under 18 gets no refusal for a unit with nothing in it.
	birth := time.Now().AddDate(-15, 0, 0)
	if err := s.app.DB.Model(&models.User{}).Where("id = ?", s.admin.ID).Update("birth_date", birth).Error; err != nil {
		t.Fatal(err)
	}
	statements := recordContactRequestStatements(t, s.app.DB)

	var cursors []string
	var after *string
	for i := 0; ; i++ {
		if i > 20 {
			t.Fatal("the traversal never ended")
		}
		code, page, body := pageOf(t, s.ac, clean.ID, after)
		if code != http.StatusOK || len(page.Items) != 0 || page.ReviewRequired || strings.Contains(body, "reviewRequired") {
			t.Fatalf("unit %d: %d %s, want an empty page that says nothing about review", i, code, body)
		}
		if page.Next == nil {
			break
		}
		cursors = append(cursors, *page.Next)
		after = page.Next
	}
	if len(cursors) < 3 {
		t.Fatalf("%d cursors, want more than three units", len(cursors))
	}
	if n := countAudit(t, s.app.DB, audit.UserErasureReasonsViewed); n != 0 {
		t.Errorf("%d view audits for a traversal with nothing in it", n)
	}
	units, last := 0, int64(-1<<63)
	for _, st := range statements() {
		if !strings.HasPrefix(st.SQL, reasonUnitPrefix) {
			t.Errorf("a page route sent a statement other than the scanner unit for an account with no reasons: %s", st.SQL)
			continue
		}
		units++
		if !regexp.MustCompile(`ORDER BY id LIMIT \$\d+$`).MatchString(st.SQL) || fmt.Sprint(st.Vars[len(st.Vars)-1]) != strconv.Itoa(reasonUnitRows) {
			t.Errorf("unit %d is not bounded by the unit size: %s %v", units, st.SQL, st.Vars)
		}
		if c := unitCursor(t, st); c != nil {
			if *c < last {
				t.Errorf("unit %d rereads rows before the previous cursor: %d after %d", units, *c, last)
			}
			last = *c
		}
	}
	if units != len(cursors)+1 {
		t.Errorf("%d scanner statements for %d pages", units, len(cursors)+1)
	}
}

// Failures are never an empty answer, and a retry resumes at the cursor it
// holds: the failed unit is read twice, every other unit once.
func TestReasonReviewReadinessRetriesTheSameCursor(t *testing.T) {
	prev := reasonUnitRows
	reasonUnitRows = 4
	defer func() { reasonUnitRows = prev }()
	s := newReasonScene(t)
	for i := 0; i < 11; i++ {
		erasureMakeRequest(t, s.app.DB, policy.RequestGroupAdd, policy.ContactWithdrawn, s.other.ID, s.second.ID, 1, 0, 0, i, "")
	}
	mine := s.request(t, s.other, 99)
	statements := recordContactRequestStatements(t, s.app.DB)

	// Fail the second unit once with a statement error.
	var units atomic.Int64
	name := fmt.Sprintf("test:fail_second_unit:%d", queryCounterSeq.Add(1))
	failed := atomic.Bool{}
	if err := s.app.DB.Callback().Row().Before("gorm:row").Register(name, func(tx *gorm.DB) {
		if strings.HasPrefix(strings.Join(strings.Fields(tx.Statement.SQL.String()), " "), reasonUnitPrefix) && units.Add(1) == 2 && failed.CompareAndSwap(false, true) {
			tx.AddError(errors.New("injected unit failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.app.DB.Callback().Row().Remove(name) }()

	var after *string
	retries := 0
	var found []string
	for i := 0; i < 30; i++ {
		code, page, body := pageOf(t, s.ac, s.target.ID, after)
		if code == http.StatusServiceUnavailable {
			if !strings.Contains(body, `"code":"reason_review_busy"`) || strings.Contains(body, `"items"`) || strings.Contains(body, "reviewRequired") {
				t.Fatalf("a failed unit: %s, want 503 reason_review_busy and no items", body)
			}
			retries++
			continue // the same cursor
		}
		if code != http.StatusOK {
			t.Fatalf("page: %d %s", code, body)
		}
		for _, it := range page.Items {
			found = append(found, it.RequestID)
		}
		if page.Next == nil {
			break
		}
		after = page.Next
	}
	if retries != 1 || fmt.Sprint(found) != fmt.Sprintf("[%d]", mine.ID) {
		t.Fatalf("retries %d, found %v, want one retry and request %d", retries, found, mine.ID)
	}
	seen := map[string]int{}
	for _, st := range statements() {
		if !strings.HasPrefix(st.SQL, reasonUnitPrefix) {
			continue
		}
		key := "first"
		if c := unitCursor(t, st); c != nil {
			key = strconv.FormatInt(*c, 10)
		}
		seen[key]++
	}
	twice := 0
	for key, n := range seen {
		if n > 2 {
			t.Errorf("cursor %s was read %d times", key, n)
		}
		if n == 2 {
			twice++
		}
	}
	if twice != 1 {
		t.Errorf("%d cursors were read twice (%v), want only the failed unit's", twice, seen)
	}
}

// A page that runs out of time answers 503 reason_review_busy, never "none".
func TestReasonReviewPageTimeoutIsNeverEmpty(t *testing.T) {
	s := newReasonScene(t)
	s.request(t, s.other, 1)
	prev := reasonPageBudget
	reasonPageBudget = time.Nanosecond
	defer func() { reasonPageBudget = prev }()
	code, _, body := pageOf(t, s.ac, s.target.ID, nil)
	if code != http.StatusServiceUnavailable || !strings.Contains(body, `"code":"reason_review_busy"`) || strings.Contains(body, `"items"`) || strings.Contains(body, "identifying reason") {
		t.Fatalf("an expired page: %d %s, want 503 reason_review_busy", code, body)
	}
	if n := countAudit(t, s.app.DB, audit.UserErasureReasonsViewed); n != 0 {
		t.Errorf("%d view audits after a timeout", n)
	}
}

// A unit with items and an admin who may not review answers 403 with no text
// and no count; an unknown-age non-teen admin reviews; known-underage and teen
// admins are refused. Nothing in the refusal says how many reasons there are.
func TestReasonReviewRefusalLeaksNothing(t *testing.T) {
	cases := []struct {
		name  string
		set   string
		args  []any
		allow bool
	}{
		{"unknown age", "birth_date = NULL", nil, true},
		{"adult of 40", "birth_date = ?", []any{time.Now().AddDate(-40, 0, 0)}, true},
		{"known 17, not a teen", "birth_date = ?", []any{time.Now().AddDate(-17, -1, 0)}, false},
		{"teen account at 14", "account_type = 'teen', birth_date = ?", []any{time.Now().AddDate(-14, 0, 0)}, false},
		{"teen account at 16", "account_type = 'teen', birth_date = ?", []any{time.Now().AddDate(-16, 0, 0)}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newReasonScene(t)
			r := s.request(t, s.other, 1)
			s.contactRequest(t, 2)
			if err := s.app.DB.Exec("UPDATE users SET "+tc.set+" WHERE id = ?", append(tc.args, s.admin.ID)...).Error; err != nil {
				t.Fatal(err)
			}
			code, page, body := pageOf(t, s.ac, s.target.ID, nil)
			if tc.allow {
				if code != http.StatusOK || len(page.Items) != 2 {
					t.Fatalf("%s: %d %s, want the two reasons", tc.name, code, body)
				}
				return
			}
			var got map[string]any
			if err := json.Unmarshal([]byte(body), &got); err != nil {
				t.Fatal(err)
			}
			keys := make([]string, 0, len(got))
			for k := range got {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			if code != http.StatusForbidden || strings.Join(keys, ",") != "code,error,reviewRequired" || got["code"] != "reason_review_not_allowed" || got["reviewRequired"] != true {
				t.Fatalf("%s: %d %s, want 403 with only code, error and reviewRequired", tc.name, code, body)
			}
			if strings.Contains(body, r.Reason) || strings.Contains(body, "items") || strings.Contains(body, "next") || strings.Contains(body, "count") {
				t.Errorf("%s: the refusal leaks: %s", tc.name, body)
			}
			if n := countAudit(t, s.app.DB, audit.UserErasureReasonsViewed); n != 0 {
				t.Errorf("%s: %d view audits for a refused page", tc.name, n)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Concurrency with pg_locks barriers (run with -count=8).
// ---------------------------------------------------------------------------

// Two admins' full deletes of one target: both wait at the exclusive gate
// behind a held lock (pg_locks shows two ungranted advisory locks), then exactly
// one applies; the other finds the account gone before any write and settles
// not_applied with subjectDeleted. The blanked set and the decision audits come
// only from the applied attempt.
func TestReasonReviewTwoAdminsDeleteOneTarget(t *testing.T) {
	logs := captureServerLog(t)
	s := newAttemptScene(t)
	db := s.app.DB
	target, other := s.target("race-target"), s.target("race-other")
	second := newTestUser(t, s.app, "race-second", "pw", models.RoleAdmin)
	c2 := loginAPI(t, s.app, "race-second", "pw")
	r1 := erasureMakeRequest(t, db, policy.RequestGroupAdd, policy.ContactWithdrawn, other.ID, target.ID, 1, 0, 0, 1, "")
	erasureMakeRequest(t, db, policy.RequestContact, policy.ContactWithdrawn, target.ID, other.ID, 1, 0, 0, 2, "")

	path := "/api/v1/admin/users/" + itoa(target.ID)
	mint := func(c *apiClient) string {
		w := s.mintWith(c, attemptKindAccountDelete, target.ID)
		var out attemptMintJSON
		decodeJSON(t, w, &out)
		return out.AttemptID
	}
	type racer struct {
		c      *apiClient
		id     string
		viewer uint
		review map[string]any
	}
	racers := []*racer{{c: s.c, viewer: s.admin.ID}, {c: c2, viewer: second.ID}}
	for _, r := range racers {
		r.id = mint(r.c)
		r.review = reviewOf(allPages(t, r.c, target.ID), func(it reasonItemJSON) string {
			if it.RequestID == strconv.FormatUint(uint64(r1.ID), 10) {
				return "remove"
			}
			return "keep"
		})
	}
	release, _ := holdAdvisory(t, db, contactLockID)
	defer release()
	done := make([]chan *httptest.ResponseRecorder, len(racers))
	for i, r := range racers {
		i, r := i, r
		done[i] = make(chan *httptest.ResponseRecorder, 1)
		go func() {
			done[i] <- r.c.do("DELETE", path, map[string]any{"attemptId": r.id, "reasonReview": r.review}, true)
		}()
	}
	// Barrier: both deletions are waiting for the gate, and neither has written.
	deadline := time.Now().Add(10 * time.Second)
	for advisoryWaiters(t, db) != 2 {
		if time.Now().After(deadline) {
			t.Fatalf("%d backends wait for the gate, want both deletions", advisoryWaiters(t, db))
		}
		time.Sleep(5 * time.Millisecond)
	}
	if s.deleted(target.ID) || countAudit(t, db, decisionActions...) != 0 {
		t.Fatal("something was written while both deletions waited at the gate")
	}
	release()
	got := []*httptest.ResponseRecorder{awaitRec(t, done[0], "the first admin's delete"), awaitRec(t, done[1], "the second admin's delete")}
	winner := -1
	for i, w := range got {
		switch w.Code {
		case http.StatusOK:
			if winner >= 0 {
				t.Fatal("both deletions applied")
			}
			winner = i
		case http.StatusNotFound:
		default:
			t.Fatalf("racer %d: %d %s, want 200 or 404", i, w.Code, w.Body.String())
		}
	}
	if winner < 0 {
		t.Fatalf("neither deletion applied: %d %s / %d %s", got[0].Code, got[0].Body.String(), got[1].Code, got[1].Body.String())
	}
	loser := 1 - winner
	if !s.deleted(target.ID) {
		t.Fatal("the account is not deleted")
	}
	// The blanked set and the decisions are the winner's alone.
	var actors []uint
	if err := db.Raw("SELECT DISTINCT actor_id FROM audit_logs WHERE action IN ?", decisionActions).Scan(&actors).Error; err != nil {
		t.Fatal(err)
	}
	if len(actors) != 1 || actors[0] != racers[winner].viewer {
		t.Errorf("decision audits by %v, want only the applied attempt's admin %d", actors, racers[winner].viewer)
	}
	if n := countAudit(t, db, decisionActions...); n != 2 {
		t.Errorf("%d decision audits, want 2 (one per reason, from one attempt)", n)
	}
	if reasonTextOf(t, db, r1.ID) != "" {
		t.Error("the removed reason was not blanked")
	}
	// The loser's id settles not_applied, and says the account is already gone.
	sw := racers[loser].c.do("POST", "/api/v1/admin/operation-attempts/settle",
		map[string]any{"attemptId": racers[loser].id, "kind": attemptKindAccountDelete, "subjectUserId": target.ID}, true)
	var settled attemptSettleJSON
	decodeJSON(t, sw, &settled)
	wantSettle(t, settled, settleNotApplied, ptr(true))
	ws := racers[winner].c.do("POST", "/api/v1/admin/operation-attempts/settle",
		map[string]any{"attemptId": racers[winner].id, "kind": attemptKindAccountDelete, "subjectUserId": target.ID}, true)
	var winnerSettled attemptSettleJSON // a fresh value: json.Unmarshal keeps fields an answer omits
	decodeJSON(t, ws, &winnerSettled)
	wantSettle(t, winnerSettled, settleApplied, nil)
	for _, leak := range []string{"40P01", "deadlock detected"} {
		if strings.Contains(logs.String(), leak) {
			t.Errorf("the server log carries %q", leak)
		}
	}
}

// A page read concurrent with the deletion of the reading admin, both orders.
//
// Deleted first: the page is parked at the seam (idle in a transaction, waiting
// on nothing) while another admin deletes the reader; when it resumes, the seal
// finds the account gone and answers 401 with no text and no view audit.
//
// Deleting first: the deletion of the reader is parked before its seal holding
// the reader's row FOR UPDATE; the reader's page seals three times against it
// (NOWAIT, never waiting), answers 503 reason_review_busy and commits no view
// audit; no deadlock reaches the log.
func TestReasonReviewPageVersusDeletingTheReadingAdmin(t *testing.T) {
	setup := func(t *testing.T) (reasonScene, *apiClient, *models.User) {
		s := newReasonScene(t)
		s.request(t, s.other, 1)
		reader := newTestUser(t, s.app, "reading-admin", erasurePW, models.RoleAdmin)
		return s, loginAPI(t, s.app, "reading-admin", erasurePW), reader
	}
	t.Run("the reader is deleted while their page is parked", func(t *testing.T) {
		logs := captureServerLog(t)
		s, rc, reader := setup(t)
		park := newParkPoint(t)
		s.app.readBeforeSeal = park.seam()
		done := make(chan *httptest.ResponseRecorder, 1)
		go func() {
			done <- rc.do("POST", "/api/v1/admin/users/"+itoa(s.target.ID)+"/erasure-reasons/page", map[string]any{"after": nil}, true)
		}()
		pid := park.await(t)
		assertWaitsOnNothing(t, s.app.DB, pid, "the page")
		if w := s.ac.do("DELETE", "/api/v1/admin/users/"+itoa(reader.ID), nil, true); w.Code != http.StatusOK {
			t.Fatalf("deleting the reader: %d %s", w.Code, w.Body.String())
		}
		park.open()
		w := awaitRec(t, done, "the parked page")
		if w.Code != http.StatusUnauthorized || strings.Contains(w.Body.String(), "identifying reason") {
			t.Fatalf("the page of a deleted reader: %d %s, want 401 and no text", w.Code, w.Body.String())
		}
		if n := countAudit(t, s.app.DB, audit.UserErasureReasonsViewed); n != 0 {
			t.Errorf("%d view audits committed for a refused page", n)
		}
		if strings.Contains(logs.String(), "40P01") {
			t.Error("a deadlock reached the server log")
		}
	})
	t.Run("the reader's deletion is parked before its seal", func(t *testing.T) {
		logs := captureServerLog(t)
		s, rc, reader := setup(t)
		var seals atomic.Int64
		s.app.readBeforeSeal = func(*gorm.DB) { seals.Add(1) }
		park := newParkPoint(t)
		s.app.deleteBeforeSeal = park.seam()
		done := make(chan *httptest.ResponseRecorder, 1)
		go func() { done <- s.ac.do("DELETE", "/api/v1/admin/users/"+itoa(reader.ID), nil, true) }()
		pid := park.await(t)
		assertWaitsOnNothing(t, s.app.DB, pid, "the deletion")
		if probeNoWait(t, s.app.DB, reader.ID, "SHARE") {
			t.Fatal("the deletion should hold the reader's row")
		}
		code, _, body := pageOf(t, rc, s.target.ID, nil)
		if code != http.StatusServiceUnavailable || !strings.Contains(body, `"code":"reason_review_busy"`) || strings.Contains(body, "identifying reason") {
			t.Fatalf("the page while the reader is being deleted: %d %s, want 503 reason_review_busy", code, body)
		}
		if seals.Load() != 3 {
			t.Errorf("%d page attempts, want 3", seals.Load())
		}
		if n := countAudit(t, s.app.DB, audit.UserErasureReasonsViewed); n != 0 {
			t.Errorf("%d view audits committed by busy attempts", n)
		}
		park.open()
		if w := awaitRec(t, done, "the deletion"); w.Code != http.StatusOK {
			t.Fatalf("the deletion: %d %s", w.Code, w.Body.String())
		}
		if strings.Contains(logs.String(), "40P01") {
			t.Error("a deadlock reached the server log")
		}
	})
}

// The seal reads the same facts as the middleware (R6-C01): a password-bearing
// admin passes every caller with one sealed attempt, and a password_hash
// another connection sets NULL between the early read and the seal is refused
// 401 with nothing written, on the page and on the deletion.
func TestReasonReviewCurrentAdminFacts(t *testing.T) {
	nullHash := func(t *testing.T, s reasonScene) {
		if err := s.app.DB.Exec("UPDATE users SET password_hash = NULL WHERE id = ?", s.admin.ID).Error; err != nil {
			t.Error(err)
		}
	}
	t.Run("page", func(t *testing.T) {
		s := newReasonScene(t)
		s.request(t, s.other, 1)
		var seals atomic.Int64
		s.app.readBeforeSeal = func(*gorm.DB) { seals.Add(1) }
		if code, page, body := pageOf(t, s.ac, s.target.ID, nil); code != http.StatusOK || len(page.Items) != 1 || seals.Load() != 1 {
			t.Fatalf("a password-bearing admin: %d %s after %d seals, want 200 and one", code, body, seals.Load())
		}
		before := countAudit(t, s.app.DB, audit.UserErasureReasonsViewed)
		s.app.readBeforeSeal = func(*gorm.DB) { nullHash(t, s) }
		code, _, body := pageOf(t, s.ac, s.target.ID, nil)
		if code != http.StatusUnauthorized || strings.Contains(body, "identifying reason") {
			t.Fatalf("a hash set NULL at the seam: %d %s, want 401 and no text", code, body)
		}
		if countAudit(t, s.app.DB, audit.UserErasureReasonsViewed) != before {
			t.Error("a view audit was committed for a refused seal")
		}
	})
	t.Run("deletion", func(t *testing.T) {
		s := newReasonScene(t)
		r := s.request(t, s.other, 1)
		review := reviewOf(allPages(t, s.ac, s.target.ID), func(reasonItemJSON) string { return "remove" })
		s.app.sealAttempts = &atomic.Int64{}
		s.app.deleteBeforeSeal = func(*gorm.DB) { nullHash(t, s) }
		res := deleteAccount(s.ac, s.target.ID, map[string]any{"reasonReview": review})
		if res.code != http.StatusUnauthorized {
			t.Fatalf("a hash set NULL at the seam: %d %s, want 401", res.code, res.body)
		}
		var u models.User
		if err := s.app.DB.Unscoped().First(&u, s.target.ID).Error; err != nil || u.DeletedAt.Valid {
			t.Errorf("the account was deleted by a refused seal: %v", err)
		}
		if reasonTextOf(t, s.app.DB, r.ID) == "" || countAudit(t, s.app.DB, decisionActions...) != 0 {
			t.Error("a refused seal blanked a reason or left a decision")
		}
	})
	t.Run("deletion passes with one sealed attempt", func(t *testing.T) {
		s := newReasonScene(t)
		s.request(t, s.other, 1)
		review := reviewOf(allPages(t, s.ac, s.target.ID), func(reasonItemJSON) string { return "keep" })
		s.app.sealAttempts = &atomic.Int64{}
		if res := deleteAccount(s.ac, s.target.ID, map[string]any{"reasonReview": review}); res.code != http.StatusOK {
			t.Fatalf("delete: %d %s", res.code, res.body)
		}
		if n := s.app.sealAttempts.Load(); n != 1 {
			t.Errorf("%d sealed attempts, want 1", n)
		}
	})
}

// Blanking a reason makes the request trigger share-lock a soft-deleted party's
// users row NOWAIT (migration 00007). When another transaction holds that row
// the answer names this cause (409 delete_reason_busy), not a busy household;
// nothing is deleted, COMMIT was never sent, and once the row is free the same
// review applies.
func TestReasonBlankCounterpartyLockIsNotAHouseholdBusy(t *testing.T) {
	s := newReasonScene(t)
	// The counterparty is already a tombstone.
	if err := s.app.DB.Exec("UPDATE users SET deleted_at = now() WHERE id = ?", s.other.ID).Error; err != nil {
		t.Fatal(err)
	}
	r := s.request(t, s.other, 1)
	review := reviewOf(allPages(t, s.ac, s.target.ID), func(reasonItemJSON) string { return "remove" })
	hold, _ := beginOwnedTx(t, s.app.DB)
	if err := hold.Exec("SELECT id FROM users WHERE id = ? FOR NO KEY UPDATE", s.other.ID).Error; err != nil {
		t.Fatal(err)
	}
	released := false
	release := func() {
		if !released {
			released = true
			hold.Rollback()
		}
	}
	defer release()

	res := deleteAccount(s.ac, s.target.ID, map[string]any{"reasonReview": review})
	if res.code != http.StatusConflict || !strings.Contains(res.body, `"code":"delete_reason_busy"`) || strings.Contains(res.body, "delete_household_busy") {
		t.Fatalf("a held counterparty row: %d %s, want 409 delete_reason_busy", res.code, res.body)
	}
	for _, leak := range []string{"55P03", "NOWAIT"} {
		if strings.Contains(res.body, leak) {
			t.Errorf("the response carries %q: %s", leak, res.body)
		}
	}
	var u models.User
	if err := s.app.DB.Unscoped().First(&u, s.target.ID).Error; err != nil || u.DeletedAt.Valid {
		t.Errorf("the account was deleted by a refused attempt: %v", err)
	}
	if reasonTextOf(t, s.app.DB, r.ID) == "" || countAudit(t, s.app.DB, decisionActions...) != 0 {
		t.Error("a refused attempt blanked a reason or left a decision")
	}
	release()
	if res := deleteAccount(s.ac, s.target.ID, map[string]any{"reasonReview": review}); res.code != http.StatusOK {
		t.Fatalf("after the row is free: %d %s, want 200", res.code, res.body)
	}
	if reasonTextOf(t, s.app.DB, r.ID) != "" {
		t.Error("the reason was not blanked once the row was free")
	}
}

// errReasonCounterpartBusy keeps the 55P03 inside it, so the household retry
// rules (only a confirmed rollback retries) apply to it unchanged.
func TestReasonCounterpartBusyKeepsTheRetryRules(t *testing.T) {
	inner := &pgconn.PgError{Code: "55P03"}
	err := fmt.Errorf("%w: %w", errReasonCounterpartBusy, inner)
	if !isHouseholdBusy(err) || !errors.Is(err, errReasonCounterpartBusy) || !rollbackConfirmed(err) {
		t.Fatal("a wrapped 55P03 must still be found, and a plain wrapper is a confirmed rollback")
	}
	unacknowledged := &contactRollbackError{Cause: err, Rollback: errors.New("connection reset")}
	if rollbackConfirmed(unacknowledged) || !errors.Is(unacknowledged, errReasonCounterpartBusy) {
		t.Fatal("an unacknowledged rollback must not be retried, and must still name the cause")
	}
	retry := newHouseholdRetry(context.Background())
	if again, _ := retry.after(&contactCommitError{Cause: err}); again {
		t.Error("a COMMIT error was retried")
	}
	if again, _ := retry.after(unacknowledged); again {
		t.Error("an unacknowledged ROLLBACK was retried")
	}
}

// ---------------------------------------------------------------------------
// P-5 census: who locks, writes and reads what the reason review touches.
// ---------------------------------------------------------------------------

// reasonCensus lists, from the production source, by "file:function":
//   - rowLockers: statements that lock contact_requests rows (a locking clause
//     in a literal over the table);
//   - requestWriters: INSERT, UPDATE or DELETE literals over contact_requests,
//     and calls of Create, Save, Update* or Delete on a models.ContactRequest;
//   - auditReaders: functions that read audit_logs (a SELECT literal over the
//     table, or a models.AuditLog model passed to a query), the readers that
//     must never scan a signed decision target into a uint (R4-C04);
//   - gatedFenced: callers of a.contactMutationCtx, the fenced exclusive-gate
//     transactions.
//
// It reads syntax only. Like the other guards it does not see SQL built from
// non-literal pieces, SQL sent around GORM or a model reached through another
// function's result.
func reasonCensus(sources map[string]string) map[string][]string {
	out := map[string]map[string]bool{"rowLockers": {}, "requestWriters": {}, "auditReaders": {}, "gatedFenced": {}}
	writeRe := regexp.MustCompile(`(?i)\b(insert into|update|delete from)\s+(public\.)?contact_requests\b`)
	lockRe := regexp.MustCompile(`(?i)\bfor (no key update|update|share|key share)\b`)
	readAuditRe := regexp.MustCompile(`(?i)\bfrom\s+(public\.)?audit_logs\b|join\s+audit_logs\b`)
	// A file that does not parse is skipped here (guardkit reports it in Problems); the
	// real tree compiles, and a fixture that does not parse shows up as a missing site.
	for _, pf := range guardkit.Sources(sources).Parse(0).Files {
		path, file := pf.Path, pf.AST
		if path == "internal/audit/audit.go" {
			continue // the writers; they read nothing
		}
		for _, d := range file.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			site := path + ":" + fd.Name.Name
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				switch x := n.(type) {
				case *ast.BasicLit:
					if x.Kind != token.STRING {
						return true
					}
					lit, err := strconv.Unquote(x.Value)
					if err != nil {
						return true
					}
					flat := guardkit.Squash(lit)
					low := strings.ToLower(flat)
					if strings.Contains(low, "contact_requests") && lockRe.MatchString(low) && !strings.HasPrefix(low, "update") {
						out["rowLockers"][site] = true
					}
					if writeRe.MatchString(flat) {
						out["requestWriters"][site] = true
					}
					if readAuditRe.MatchString(low) && !strings.HasPrefix(low, "update") && !strings.HasPrefix(low, "insert") && !strings.HasPrefix(low, "delete") {
						out["auditReaders"][site] = true
					}
				case *ast.SelectorExpr:
					// Any use of the AuditLog model outside internal/audit.
					if id, ok := x.X.(*ast.Ident); ok && id.Name == "models" && x.Sel.Name == "AuditLog" {
						out["auditReaders"][site] = true
					}
				case *ast.CallExpr:
					sel, ok := x.Fun.(*ast.SelectorExpr)
					if !ok {
						return true
					}
					switch sel.Sel.Name {
					case "contactMutationCtx":
						out["gatedFenced"][site] = true
					case "Create", "Save", "Update", "Updates", "UpdateColumn", "UpdateColumns", "Delete":
						for _, arg := range x.Args {
							if mentionsModel(arg, "ContactRequest") {
								out["requestWriters"][site] = true
							}
						}
						if mentionsModel(sel.X, "ContactRequest") {
							out["requestWriters"][site] = true
						}
					}
				}
				return true
			})
			// A model query with a locking clause over models.ContactRequest locks its
			// rows too (clause.Locking{Strength: ...}); the heuristic is the two
			// names in one function.
			if mentionsModel(fd.Body, "ContactRequest") {
				locking := false
				ast.Inspect(fd.Body, func(n ast.Node) bool {
					if sel, ok := n.(*ast.SelectorExpr); ok && sel.Sel.Name == "Locking" {
						locking = true
					}
					return true
				})
				if locking {
					out["rowLockers"][site] = true
				}
			}
			// A table named in Table("audit_logs ...") is a read when no write
			// verb is applied to the same chain; the literal check above covers
			// raw SQL, so only the Table form needs this.
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				c, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				if sel, ok := c.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Table" && len(c.Args) > 0 {
					if lit, ok := c.Args[0].(*ast.BasicLit); ok && lit.Kind == token.STRING && strings.Contains(lit.Value, "audit_logs") {
						out["auditReaders"][site] = true
					}
				}
				return true
			})
		}
	}
	res := map[string][]string{}
	for k, set := range out {
		for site := range set {
			res[k] = append(res[k], site)
		}
		sort.Strings(res[k])
	}
	return res
}

// mentionsModel reports whether n contains models.<name> (a type, a composite
// literal or an address of one).
func mentionsModel(n ast.Node, name string) bool {
	found := false
	ast.Inspect(n, func(m ast.Node) bool {
		if sel, ok := m.(*ast.SelectorExpr); ok && sel.Sel.Name == name {
			if id, ok := sel.X.(*ast.Ident); ok && id.Name == "models" {
				found = true
			}
		}
		return true
	})
	return found
}

func TestReasonReviewCensus(t *testing.T) {
	sources := guardkit.Production(t)
	got := reasonCensus(sources)
	for _, kind := range []string{"rowLockers", "requestWriters", "auditReaders", "gatedFenced"} {
		want := reasonCensusPinned[kind]
		if fmt.Sprint(got[kind]) != fmt.Sprint(want) {
			t.Errorf("%s changed.\n got  %v\n want %v\nRevisit the P-5 census (inventory 9 and 15.9): a new contact_requests row locker or writer, a new audit_logs reader (it must not scan a signed decision target into a uint, R4-C04) or a new fenced gate caller needs its lock order and its place under the exclusive gate decided, then this pin updated.", kind, got[kind], want)
		}
	}
	// The pin sees a new site of each kind.
	for name, src := range map[string]struct{ kind, code string }{
		"a new row locker":    {"rowLockers", "package main\nfunc newLocker(tx *gorm.DB) { tx.Raw(\"SELECT id FROM contact_requests WHERE id = ? FOR UPDATE\", 1) }\n"},
		"a new request write": {"requestWriters", "package main\nfunc newWriter(tx *gorm.DB) { tx.Exec(\"UPDATE contact_requests SET status = 'x'\") }\n"},
		"a model write":       {"requestWriters", "package main\nfunc newModelWriter(tx *gorm.DB) { tx.Create(&models.ContactRequest{}) }\n"},
		"a new audit reader":  {"auditReaders", "package main\nfunc newReader(tx *gorm.DB) { tx.Raw(\"SELECT target_id FROM audit_logs\").Scan(&ids) }\n"},
		"a model audit read":  {"auditReaders", "package main\nfunc newModelReader(tx *gorm.DB) { tx.Model(&models.AuditLog{}).Count(&n) }\n"},
		"a new fenced caller": {"gatedFenced", "package main\nfunc (a *App) newFenced() { a.contactMutationCtx(ctx, change, fn) }\n"},
	} {
		with := map[string]string{"new_site.go": src.code}
		for k, v := range sources {
			with[k] = v
		}
		if fmt.Sprint(reasonCensus(with)[src.kind]) == fmt.Sprint(reasonCensusPinned[src.kind]) {
			t.Errorf("%s: the census did not see it", name)
		}
	}
}

// reasonCensusPinned is the P-5 census at the T07B-2b base (M7.4 inventory 9 and
// 15.9). Classification of each site, kept by review:
//
// rowLockers (all run under the exclusive contact gate or, for reasons, inside
// apiAdminDeleteUser's fenced callback): eraseContactRequestTextIn locks a
// subject's occurrence_add rows FOR UPDATE after the soft delete;
// planReasonReviewIn locks the target's reasoned rows FOR UPDATE in id order,
// after keepsAnAdminIn and before every household lock; the others are the
// lifecycle's own model queries (reconcileContacts and the contact and add
// request routes) under the same gate.
//
// requestWriters by literal or inline model: the ages-note blank and the reason
// blank, each guarded by TestContactRequestTextChangesStayInTheirHelpers. The
// creation and decision writers go through variables the guard binds; this
// census does not see them.
//
// auditReaders, each shown not to read the new decision rows (targets
// contact_request and add_request, actions contact.reason_* and add.reason_*)
// into a uint: captureErasureSetsIn, discoverAuditConversionsIn,
// eraseTombstoneOnce, flagHouseholdChild13, rolloverAges,
// previouslyRegisteredPerson, loadAccessHistory and appendRecoverableRegistrations
// filter to other actions or target types; conversionInviterDisabled,
// conversionSendAllowed, apiTestMessage, apiPushSubscribe and apiPushTest only
// count; eraseAuditDetailsIn updates and aggregates in SQL (the five new actions
// are classified in auditIPAttribution); contactHistoryQuery selects only the
// actions in contactHistoryTargets, which has no decision action.
//
// gatedFenced: the three callers of a.contactMutationCtx. The reason review adds
// no fourth: its consume step is a statement of apiAdminDeleteUser's callback and
// its pages are ungated reads that end with the actor seal.
var reasonCensusPinned = map[string][]string{
	"rowLockers": {
		"account_erasure.go:eraseContactRequestTextIn",
		"account_erasure_reasons.go:planReasonReviewIn",
		"api_contact.go:apiDecideContactRequest",
		"api_contact.go:apiWithdrawContactRequest",
		"api_contact.go:mutateAddRequest",
		"api_contact.go:useContactGrant",
		"api_registrations.go:apiAddRegistration",
		"contact_lifecycle.go:reconcileContacts",
	},
	"requestWriters": {
		"account_erasure.go:eraseContactRequestTextIn",
		"account_erasure_reasons.go:eraseContactRequestReasonsIn",
	},
	"auditReaders": {
		"account_erasure.go:captureErasureSetsIn",
		"account_erasure_audit.go:discoverAuditConversionsIn",
		"account_erasure_audit.go:eraseAuditDetailsIn",
		"account_erasure_tombstones.go:eraseTombstoneOnce",
		"age_rollover.go:flagHouseholdChild13",
		"age_rollover.go:rolloverAges",
		"api_contact_history.go:contactHistoryQuery",
		"api_household_conversion.go:conversionInviterDisabled",
		"api_household_conversion.go:conversionSendAllowed",
		"api_message_preview.go:apiTestMessage",
		"api_push.go:apiPushSubscribe",
		"api_push.go:apiPushTest",
		"api_registrations.go:previouslyRegisteredPerson",
		"registration_access.go:loadAccessHistory",
		"registration_subjects.go:appendRecoverableRegistrations",
	},
	"gatedFenced": {
		"account_erasure_tombstones.go:eraseTombstoneOnce",
		"admin_operation_attempts.go:apiAdminSettleOperationAttempt",
		"api_admin.go:apiAdminDeleteUser",
	},
}

// ---------------------------------------------------------------------------
// Independent review round (R1-R4).
// ---------------------------------------------------------------------------

// R1: the page equivalent of TestSettleBusyWindowClosesAfterBackoff. A busy
// actor row lets a page start a new transaction only while the busy window
// (measured from the request) is open, and the window is looked at again after
// each backoff, so the sleep cannot carry a third attempt past it. At least two
// attempts must have run so the post-backoff path was really reached; each
// refused attempt rolls back its view audit, so none is committed. Without the
// recheck after the backoff, attempt 3 begins at about 130 ms and its BEGIN lies
// outside the 100 ms window.
func TestReasonReviewBusyWindowClosesAfterBackoff(t *testing.T) {
	const window = 100 * time.Millisecond
	old := reasonBusyWindow
	reasonBusyWindow = window
	t.Cleanup(func() { reasonBusyWindow = old })
	s := newProxiedAttemptScene(t)
	target, other := s.target("window-target"), s.target("window-other")
	erasureMakeRequest(t, s.direct, policy.RequestGroupAdd, policy.ContactWithdrawn, other.ID, target.ID, 1, 0, 0, 1, "")
	var attempts atomic.Int64
	s.app.readBeforeSeal = func(*gorm.DB) { attempts.Add(1) }
	hold, _ := beginOwnedTx(t, s.direct)
	if err := hold.Exec("SELECT id FROM users WHERE id = ? FOR NO KEY UPDATE", s.admin.ID).Error; err != nil {
		t.Fatal(err)
	}
	mark := len(s.rig.proxy.Events())
	began := time.Now()
	code, _, body := pageOf(t, s.c, target.ID, nil)
	if code != http.StatusServiceUnavailable || !strings.Contains(body, `"code":"reason_review_busy"`) || strings.Contains(body, "identifying reason") {
		t.Fatalf("a page against a held actor row: %d %s, want 503 reason_review_busy and no text", code, body)
	}
	var starts []time.Duration
	for _, e := range s.rig.proxy.Events()[mark:] {
		if e.Kind == testdb.KindStatement && strings.Contains(e.SQL, "set_config('statement_timeout'") {
			starts = append(starts, e.Time.Sub(began))
		}
	}
	if len(starts) < 2 || int64(len(starts)) != attempts.Load() {
		t.Fatalf("the page began %d transactions (%d reached the seal) at %v; want at least 2 so the retry path ran, each sealed once", len(starts), attempts.Load(), starts)
	}
	for _, at := range starts {
		if at >= window {
			t.Fatalf("a transaction began %s after the request, outside the %s busy window (starts %v)", at, window, starts)
		}
	}
	if n := countAudit(t, s.direct, audit.UserErasureReasonsViewed); n != 0 {
		t.Errorf("%d view audits committed by refused attempts", n)
	}
}

// reasonAuditWant is one view or decision audit row, judged in full.
type reasonAuditWant struct {
	Action string
	Type   string
	ID     int64
	Actor  uint
	Meta   map[string]any
	HasIP  bool // decision rows carry the deciding admin's address; view rows none
}

// assertReasonAudits requires the review's audit rows (views and decisions) to
// be exactly want, in the order they were written.
func assertReasonAudits(t *testing.T, db *gorm.DB, want []reasonAuditWant) {
	t.Helper()
	var rows []struct {
		ID         uint
		ActorID    *uint
		Action     string
		TargetType string
		TargetID   string
		MetaJSON   string
		IP         string
	}
	if err := db.Raw("SELECT id, actor_id, action, target_type, target_id::text AS target_id, meta_json::text AS meta_json, ip FROM audit_logs WHERE action IN ? ORDER BY id",
		append([]string{audit.UserErasureReasonsViewed}, decisionActions...)).Scan(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != len(want) {
		var got []string
		for _, r := range rows {
			got = append(got, r.Action+":"+r.TargetType+":"+r.TargetID)
		}
		t.Fatalf("%d review audit rows %v, want %d", len(rows), got, len(want))
	}
	for i, w := range want {
		r := rows[i]
		if r.Action != w.Action || r.TargetType != w.Type || r.TargetID != strconv.FormatInt(w.ID, 10) {
			t.Errorf("audit %d is %s on %s %s, want %s on %s %d", i, r.Action, r.TargetType, r.TargetID, w.Action, w.Type, w.ID)
		}
		if r.ActorID == nil || *r.ActorID != w.Actor {
			t.Errorf("audit %d (%s) has actor %v, want %d", i, w.Action, r.ActorID, w.Actor)
		}
		if (r.IP != "") != w.HasIP {
			t.Errorf("audit %d (%s) has ip %q, want an address: %v", i, w.Action, r.IP, w.HasIP)
		}
		if got, wantMeta := erasureMetaOf(t, r.MetaJSON), erasureWire(t, w.Meta); !reflect.DeepEqual(got, wantMeta) {
			t.Errorf("audit %d (%s) has meta %v, want %v", i, w.Action, got, wantMeta)
		}
	}
}

func reasonViewWant(admin, target uint, count int) reasonAuditWant {
	return reasonAuditWant{Action: audit.UserErasureReasonsViewed, Type: "user", ID: int64(target), Actor: admin,
		Meta: map[string]any{"count": count, "source": "deletion"}}
}

func reasonKeptWant(action, targetType string, request int64, admin, erased uint, role, purpose, status string, party bool) reasonAuditWant {
	return reasonAuditWant{Action: action, Type: targetType, ID: request, Actor: admin, HasIP: true, Meta: map[string]any{
		"erasedUserId": erased, "erasedRole": role, "purpose": purpose, "status": status,
		"reviewerIsParty": party, "source": "deletion", "reviewVersion": 1}}
}

// reasonMarkers counts the marker rows for (request, party) the way the party-
// qualified selector reads them (R3-C08): the purpose-mapped action and target
// type, the request as target, and typed number metadata for reviewVersion 1
// and the party. Malformed metadata is never cast.
func reasonMarkers(t *testing.T, db *gorm.DB, request int64, party uint, action, targetType string) int64 {
	t.Helper()
	var n int64
	if err := db.Raw(`SELECT count(*) FROM audit_logs WHERE action = ? AND target_type = ? AND target_id = ?
		AND jsonb_typeof(meta_json->'reviewVersion') = 'number' AND meta_json->>'reviewVersion' = '1'
		AND jsonb_typeof(meta_json->'erasedUserId') = 'number' AND meta_json->>'erasedUserId' = ?`,
		action, targetType, request, strconv.FormatUint(uint64(party), 10)).Scan(&n).Error; err != nil {
		t.Fatal(err)
	}
	return n
}

// R2: signed decision targets survive the reviewer's own erasure (R4-4a) and its
// IP minimization. The reviewing admin is deleted after deciding requests with
// ids at both ends of the int64 range and either side of zero: the deletion
// succeeds (no signed target is scanned into a uint), the decision rows keep
// their exact targets and metadata, the admin's address is cleared from them,
// and the party-qualified marker still names the decided party for each request
// and the other (deleted) party for none, so that party's copy stays unreviewed.
func TestSignedRequestIDsSurviveReviewerErasure(t *testing.T) {
	s := newReasonScene(t)
	// The counterparty is already a tombstone, as in a legacy review.
	if err := s.app.DB.Exec("UPDATE users SET deleted_at = now() WHERE id = ?", s.other.ID).Error; err != nil {
		t.Fatal(err)
	}
	ids := []int64{-9223372036854775808, -1, 0, 9223372036854775807}
	for i, id := range ids {
		r := s.contactRequest(t, 20+i)
		if err := s.app.DB.Exec("UPDATE contact_requests SET id = ? WHERE id = ?", id, r.ID).Error; err != nil {
			t.Fatal(err)
		}
	}
	items := allPages(t, s.ac, s.target.ID)
	if len(items) != len(ids) {
		t.Fatalf("%d items, want %d", len(items), len(ids))
	}
	res := deleteAccount(s.ac, s.target.ID, map[string]any{"reasonReview": reviewOf(items, func(it reasonItemJSON) string {
		if it.RequestID == "-9223372036854775808" || it.RequestID == "0" {
			return "remove"
		}
		return "keep"
	})})
	if res.code != http.StatusOK {
		t.Fatalf("delete: %d %s", res.code, res.body)
	}
	action := func(id int64) string {
		if id == -9223372036854775808 || id == 0 {
			return audit.ContactReasonRemoved
		}
		return audit.ContactReasonKept
	}
	var decisions []struct {
		TargetID string
		MetaJSON string
		IP       string
	}
	snapshot := func() string {
		if err := s.app.DB.Raw("SELECT target_id::text AS target_id, meta_json::text AS meta_json, ip FROM audit_logs WHERE action IN ? ORDER BY audit_logs.id", decisionActions).Scan(&decisions).Error; err != nil {
			t.Fatal(err)
		}
		var b strings.Builder
		for _, d := range decisions {
			b.WriteString(d.TargetID + "|" + d.MetaJSON + "\n")
		}
		return b.String()
	}
	checkMarkers := func(when string) {
		for _, id := range ids {
			if got := reasonMarkers(t, s.app.DB, id, s.target.ID, action(id), "contact_request"); got != 1 {
				t.Errorf("%s: %d markers for request %d and the decided party, want 1", when, got, id)
			}
			for _, a := range append([]string{}, decisionActions...) {
				if got := reasonMarkers(t, s.app.DB, id, s.other.ID, a, "contact_request"); got != 0 {
					t.Errorf("%s: %d %s markers name the other party of request %d, want none (it stays unreviewed)", when, got, a, id)
				}
			}
		}
	}
	before := snapshot()
	if len(decisions) != len(ids) {
		t.Fatalf("%d decisions, want %d", len(decisions), len(ids))
	}
	for _, d := range decisions {
		if d.IP == "" {
			t.Fatal("a decision row has no address before the reviewer's erasure")
		}
	}
	checkMarkers("after the deletion")

	// The reviewer is erased by the other admin.
	second := loginAPI(t, s.app, "erase-second", erasurePW)
	if r := deleteAccount(second, s.admin.ID, nil); r.code != http.StatusOK {
		t.Fatalf("erasing the reviewing admin: %d %s", r.code, r.body)
	}
	if after := snapshot(); after != before {
		t.Errorf("the reviewer's erasure changed a decision's target or metadata:\n%s\n%s", before, after)
	}
	for _, d := range decisions {
		if d.IP != "" {
			t.Errorf("decision on target %s still has the address %q after the reviewer's erasure", d.TargetID, d.IP)
		}
	}
	checkMarkers("after the reviewer's erasure")
	var views int64
	if err := s.app.DB.Raw("SELECT count(*) FROM audit_logs WHERE action = ? AND ip <> ''", audit.UserErasureReasonsViewed).Scan(&views).Error; err != nil || views != 0 {
		t.Errorf("%d view rows carry an address (%v)", views, err)
	}
}

// R2: a pending request with a negative id is a known rollback of the deletion
// (reconcileContacts loads models.ContactRequest, whose id is a uint). This is
// the behaviour before the review existed and the review neither widens nor
// fixes it: nothing changes, no decision audit and no receipt are written, and
// the attempt id ends rolled back.
func TestNegativeIDPendingRowIsAKnownRollback(t *testing.T) {
	s := newAttemptScene(t)
	db := s.app.DB
	target, other := s.target("neg-target"), s.target("neg-other")
	pending := erasureMakeRequest(t, db, policy.RequestContact, policy.ContactPending, target.ID, other.ID, 1, 0, 0, 1, "")
	const negative = int64(-7)
	if err := db.Exec("UPDATE contact_requests SET id = ? WHERE id = ?", negative, pending.ID).Error; err != nil {
		t.Fatal(err)
	}
	items := allPages(t, s.c, target.ID)
	if len(items) != 1 || items[0].RequestID != "-7" {
		t.Fatalf("the review shows %+v, want the one signed request", items)
	}
	id := s.mint(target.ID)
	w := s.c.do("DELETE", "/api/v1/admin/users/"+itoa(target.ID),
		map[string]any{"attemptId": id, "reasonReview": reviewOf(items, func(reasonItemJSON) string { return "remove" })}, true)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("delete over a negative-id pending row: %d %s, want the ordinary 500", w.Code, w.Body.String())
	}
	if s.deleted(target.ID) {
		t.Error("the account was deleted")
	}
	var text, status string
	if err := db.Raw("SELECT reason, status FROM contact_requests WHERE id = ?", negative).Row().Scan(&text, &status); err != nil {
		t.Fatal(err)
	}
	if text != pending.Reason || status != policy.ContactPending {
		t.Errorf("the request is %q/%q after the rollback, want its reason and pending", text, status)
	}
	if n := countAudit(t, db, decisionActions...); n != 0 {
		t.Errorf("%d decision audits after a rolled-back deletion", n)
	}
	if n := s.receipts(); n != 0 {
		t.Errorf("%d receipts after a rolled-back deletion", n)
	}
	if st := s.state(id, target.ID); st != attemptRolledBack {
		t.Errorf("ledger = %v, want rolled back", st)
	}
}

// R3 helpers.

func agesRaceReasoned(text string) func(*models.ContactRequest) {
	return func(r *models.ContactRequest) { r.Reason = text }
}

// eraseReviewed is erase with the review the admin read earlier. A nil review
// sends none.
func (w *agesRaceWorld) eraseReviewed(c *apiClient, id uint, review any) <-chan httpResult {
	body := map[string]any{}
	if m, ok := review.(map[string]any); ok {
		body["reasonReview"] = m["reasonReview"]
	}
	return w.run.do(c, "DELETE", "/api/v1/admin/users/"+itoa(id), body)
}

func agesRaceChanged(t *testing.T, ch <-chan httpResult, what string) {
	t.Helper()
	r := awaitResult(t, ch)
	if r.code != http.StatusConflict || !strings.Contains(r.body, `"code":"reason_review_changed"`) {
		t.Fatalf("%s: %d %s, want 409 reason_review_changed", what, r.code, r.body)
	}
}

// R3, lifecycle first: the review is read before the race; a sweep holds the
// gate and has expired the pending request when the erasure, queued behind it,
// carries that review. The erasure answers 409 reason_review_changed and deletes
// nothing (the ages note stays); a review read afterwards succeeds, blanks the
// note, keeps the reason, and the audit holds exactly the two page views and the
// one decision.
func TestAgesNoteErasureRaceReasonedLifecycleFirst(t *testing.T) {
	w := newAgesRaceWorld(t)
	f, db := w.f, w.db
	u, leader := f.outsider, f.leader
	const text = "reasoned race: call before sunday"
	p := w.request(leader.ID, u.ID, policy.ContactPending, agesRaceNote, agesRaceExpired, agesRaceReasoned(text))
	path := "/api/v1/admin/users/" + itoa(u.ID)
	stale := f.ac.withKeptReasons(path, nil)
	if stale == nil {
		t.Fatal("the review pages returned no reason")
	}
	before := w.rows()[p.ID]

	pid, sweepDone, releaseSweep := w.holdSweep(time.Now().UTC())
	del := w.eraseReviewed(f.ac, u.ID, stale)
	agesRaceGateWaiter(t, db, pid, "the erasure")
	if countRows(t, db, "users", "id = ? AND deleted_at IS NOT NULL", u.ID) != 0 {
		t.Fatal("the erasure deleted the person while the sweep held the gate")
	}
	releaseSweep()
	select {
	case err := <-sweepDone:
		if err != nil {
			t.Fatalf("the sweep: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the sweep never finished")
	}
	agesRaceChanged(t, del, "the erasure carrying a review from before the sweep")
	mid := w.rows()[p.ID]
	if mid.Status != policy.ContactExpired || mid.Note != agesRaceNote || mid.Reason != text || before.Status != policy.ContactPending {
		t.Errorf("after the refused erasure the request is %q with note %q and reason %q (was %q), want expired, the note and the reason untouched",
			mid.Status, mid.Note, mid.Reason, before.Status)
	}
	if countRows(t, db, "users", "id = ? AND deleted_at IS NOT NULL", u.ID) != 0 {
		t.Fatal("a refused review deleted the person")
	}
	if n := countAudit(t, db, decisionActions...); n != 0 {
		t.Errorf("%d decision audits after a refused review", n)
	}

	fresh := f.ac.withKeptReasons(path, nil)
	agesRaceOK(t, w.eraseReviewed(f.ac, u.ID, fresh), "the erasure with a refreshed review")
	agesRaceScrubbed(t, "request", mid, w.rows()[p.ID])
	if countRows(t, db, "users", "id = ? AND deleted_at IS NOT NULL", u.ID) != 1 {
		t.Error("the refreshed review did not delete the person")
	}
	assertReasonAudits(t, db, []reasonAuditWant{
		reasonViewWant(f.admin.ID, u.ID, 1),
		reasonViewWant(f.admin.ID, u.ID, 1),
		reasonKeptWant(audit.AddReasonKept, "add_request", int64(p.ID), f.admin.ID, u.ID, "subject", policy.RequestOccurrenceAdd, policy.ContactExpired, false),
	})
	agesRaceStates(t, w.states, nil)
}

// R3, erasure first: two admins each read the review for one person of a pending
// request (the asker for the first, the asked person for the second) before the
// race. The asker's erasure holds the gate and ends the request; the second
// admin's erasure of the asked person, queued behind it, carries a review of a
// request that has since ended. It is refused 409 reason_review_changed and
// deletes nothing; a refreshed review succeeds, blanks the note and keeps the
// reason. The audit holds exactly the four page views and two decisions.
func TestAgesNoteErasureRaceReasonedErasureFirst(t *testing.T) {
	w := newAgesRaceWorld(t)
	f, db := w.f, w.db
	asker := newTestUser(t, w.app, "ages-asker-reasoned", erasurePW, models.RoleMember)
	subject := newTestUser(t, w.app, "ages-asked-reasoned", erasurePW, models.RoleMember)
	admin2 := newTestUser(t, w.app, "ages-admin-reasoned", erasurePW, models.RoleAdmin)
	ac2 := loginAPI(t, w.app, "ages-admin-reasoned", erasurePW)
	ac2.holdNotices = true
	const text = "reasoned race: the asked person's copy"
	r := w.request(asker.ID, subject.ID, policy.ContactPending, agesRaceNote, agesRaceReasoned(text))
	askerPath, subjectPath := "/api/v1/admin/users/"+itoa(asker.ID), "/api/v1/admin/users/"+itoa(subject.ID)
	askerReview := f.ac.withKeptReasons(askerPath, nil)
	staleReview := ac2.withKeptReasons(subjectPath, nil)
	if askerReview == nil || staleReview == nil {
		t.Fatal("the review pages returned no reason")
	}
	before := w.rows()[r.ID]

	var askerPID atomic.Int64
	pa := erasureTestBeforeNotificationsDelete(asker.ID)
	pa.PID = &askerPID
	askerReached, releaseAsker := w.run.pause(pa)
	first := w.eraseReviewed(f.ac, asker.ID, askerReview)
	awaitDBSignal(t, askerReached)
	requireContactGate(t, db, agesRacePID(&askerPID), true, "the asker's erasure")
	second := w.eraseReviewed(ac2, subject.ID, staleReview)
	agesRaceGateWaiter(t, db, agesRacePID(&askerPID), "the asked person's erasure")
	if countRows(t, db, "users", "id = ? AND deleted_at IS NOT NULL", subject.ID) != 0 {
		t.Fatal("the asked person was deleted while the asker's erasure held the gate")
	}
	if got := w.rows()[r.ID]; got.Whole != before.Whole {
		t.Fatal("the request changed before the first erasure released the gate")
	}
	releaseAsker()
	agesRaceOK(t, first, "the asker's erasure")
	agesRaceChanged(t, second, "the asked person's erasure carrying a review from before the asker's")
	mid := w.rows()[r.ID]
	if mid.Status != policy.ContactEnded || mid.Note != agesRaceNote || mid.Reason != text || before.Status != policy.ContactPending {
		t.Errorf("after the refused erasure the request is %q with note %q and reason %q (was %q), want ended by the asker's erasure, the note and reason untouched",
			mid.Status, mid.Note, mid.Reason, before.Status)
	}
	if countRows(t, db, "users", "id = ? AND deleted_at IS NOT NULL", subject.ID) != 0 {
		t.Fatal("a refused review deleted the asked person")
	}

	fresh := ac2.withKeptReasons(subjectPath, nil)
	agesRaceOK(t, w.eraseReviewed(ac2, subject.ID, fresh), "the asked person's erasure with a refreshed review")
	agesRaceScrubbed(t, "request", mid, w.rows()[r.ID])
	if countRows(t, db, "users", "id = ? AND deleted_at IS NOT NULL", subject.ID) != 1 {
		t.Error("the refreshed review did not delete the asked person")
	}
	assertReasonAudits(t, db, []reasonAuditWant{
		reasonViewWant(f.admin.ID, asker.ID, 1),
		reasonViewWant(admin2.ID, subject.ID, 1),
		reasonKeptWant(audit.AddReasonKept, "add_request", int64(r.ID), f.admin.ID, asker.ID, "requester", policy.RequestOccurrenceAdd, policy.ContactPending, false),
		reasonViewWant(admin2.ID, subject.ID, 1),
		reasonKeptWant(audit.AddReasonKept, "add_request", int64(r.ID), admin2.ID, subject.ID, "subject", policy.RequestOccurrenceAdd, policy.ContactEnded, false),
	})
	agesRaceStates(t, w.states, nil)
}

// R4: a logout between the pages and the DELETE refuses the delete, changes
// nothing and writes no decision.
func TestReasonReviewLogoutBetweenPageAndDelete(t *testing.T) {
	s := newReasonScene(t)
	r := s.request(t, s.other, 1)
	review := reviewOf(allPages(t, s.ac, s.target.ID), func(reasonItemJSON) string { return "remove" })
	if err := s.app.DB.Exec("UPDATE users SET logout_version = logout_version + 1 WHERE id = ?", s.admin.ID).Error; err != nil {
		t.Fatal(err)
	}
	res := deleteAccount(s.ac, s.target.ID, map[string]any{"reasonReview": review})
	if res.code != http.StatusUnauthorized {
		t.Fatalf("a delete after a logout: %d %s, want 401", res.code, res.body)
	}
	var u models.User
	if err := s.app.DB.Unscoped().First(&u, s.target.ID).Error; err != nil || u.DeletedAt.Valid {
		t.Errorf("the account was deleted after a logout: %v", err)
	}
	if reasonTextOf(t, s.app.DB, r.ID) == "" || countAudit(t, s.app.DB, decisionActions...) != 0 {
		t.Error("a refused delete blanked a reason or left a decision")
	}
}

// R4 (R4-C03): reviewerIsParty is true in the decision's metadata when the
// deciding admin is the requester of one request about the erased person and the
// subject of another, and false for a request they are not in.
func TestReasonReviewDecisionMetaReviewerIsParty(t *testing.T) {
	s := newReasonScene(t)
	asRequester := erasureMakeRequest(t, s.app.DB, policy.RequestGroupAdd, policy.ContactWithdrawn, s.admin.ID, s.target.ID, 1, 0, 0, 1, "")
	asSubject := erasureMakeRequest(t, s.app.DB, policy.RequestContact, policy.ContactWithdrawn, s.target.ID, s.admin.ID, 1, 0, 0, 2, "")
	noParty := s.request(t, s.other, 3)
	items := allPages(t, s.ac, s.target.ID)
	party := map[string]bool{}
	for _, it := range items {
		party[it.RequestID] = it.ReviewerIsParty
	}
	idOf := func(r models.ContactRequest) string { return strconv.FormatUint(uint64(r.ID), 10) }
	if !party[idOf(asRequester)] || !party[idOf(asSubject)] || party[idOf(noParty)] {
		t.Fatalf("page flags %v, want true for the two requests the admin is in and false for the other", party)
	}
	if res := deleteAccount(s.ac, s.target.ID, map[string]any{"reasonReview": reviewOf(items, func(reasonItemJSON) string { return "keep" })}); res.code != http.StatusOK {
		t.Fatalf("delete: %d %s", res.code, res.body)
	}
	assertReasonAudits(t, s.app.DB, []reasonAuditWant{
		reasonViewWant(s.admin.ID, s.target.ID, 3),
		reasonKeptWant(audit.AddReasonKept, "add_request", int64(asRequester.ID), s.admin.ID, s.target.ID, "subject", policy.RequestGroupAdd, policy.ContactWithdrawn, true),
		reasonKeptWant(audit.ContactReasonKept, "contact_request", int64(asSubject.ID), s.admin.ID, s.target.ID, "requester", policy.RequestContact, policy.ContactWithdrawn, true),
		reasonKeptWant(audit.AddReasonKept, "add_request", int64(noParty.ID), s.admin.ID, s.target.ID, "subject", policy.RequestGroupAdd, policy.ContactWithdrawn, false),
	})
}

// reasonReviewRepeatSet is the set the design requires to pass at -count=8
// (T07B-2b acceptance: the concurrency tests with pg_locks barriers, the seal
// schedules, the restore schedules and the signed-id and audit cases). The
// orchestration runs it with
//
//	go test -count=8 -run '^(<names joined by |>)$' .
//
// and TestReasonReviewRepeatSetExists keeps every name real, so a rename cannot
// silently shrink the repeat run. None of these changes a package variable
// without restoring it, and each makes its own scene, so they repeat safely.
var reasonReviewRepeatSet = []string{
	"TestReasonReviewTwoAdminsDeleteOneTarget",
	"TestReasonReviewPageVersusDeletingTheReadingAdmin",
	"TestReasonReviewSealOnPages",
	"TestReasonReviewCurrentAdminFacts",
	"TestReasonReviewBusyWindowClosesAfterBackoff",
	"TestReasonReviewRestoreWithOpenPanel",
	"TestReasonReviewDeleteExactSet",
	"TestReasonReviewReadiness",
	"TestReasonReviewReadinessRetriesTheSameCursor",
	"TestReasonReviewLogoutBetweenPageAndDelete",
	"TestReasonReviewDecisionMetaReviewerIsParty",
	"TestReasonBlankCounterpartyLockIsNotAHouseholdBusy",
	"TestReasonBlankBusyInALaterBatch",
	"TestReasonProbeIsBounded",
	"TestReasonDeleteBatchStatements",
	"TestReasonDeleteFaultsRollBack",
	"TestReasonEnvelopeAttemptOrdering",
	"TestReasonOrderingMatrix",
	"TestReasonDeleteBatching",
	"TestReasonOrderIsSigned",
	"TestReasonLegacyEnvelope",
	"TestReasonReviewDeleteStreaming",
	"TestSignedRequestIDsEndToEnd",
	"TestSignedRequestIDsSurviveReviewerErasure",
	"TestNegativeIDPendingRowIsAKnownRollback",
	"TestAgesNoteErasureRaceReasonedLifecycleFirst",
	"TestAgesNoteErasureRaceReasonedErasureFirst",
	"TestActorSealVsConversion",
	"TestActorSealBusy",
	"TestSettleBusyWindowClosesAfterBackoff",
}

func TestReasonReviewRepeatSetExists(t *testing.T) {
	have := map[string]bool{}
	files, err := filepath.Glob("*_test.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, f := range files {
		file, err := parser.ParseFile(fset, f, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range file.Decls {
			if fd, ok := d.(*ast.FuncDecl); ok && fd.Recv == nil && strings.HasPrefix(fd.Name.Name, "Test") {
				have[fd.Name.Name] = true
			}
		}
	}
	seen := map[string]bool{}
	for _, name := range reasonReviewRepeatSet {
		if !have[name] {
			t.Errorf("the repeat set names %s, which is not a test", name)
		}
		if seen[name] {
			t.Errorf("the repeat set names %s twice", name)
		}
		seen[name] = true
	}
	t.Logf("-run '^(%s)$'", strings.Join(reasonReviewRepeatSet, "|"))
}

// ---------------------------------------------------------------------------
// T07B-2 scaling: signed order, the set-v1 envelope, the streamed consume and
// the batched writes. Database tests.
// ---------------------------------------------------------------------------

// Every statement that lists reasons orders by the bigint id: pages, the cursor
// chain, the DELETE stream and the digest agree on ascending signed order.
func TestReasonOrderIsSigned(t *testing.T) {
	prev := reasonPageRequests
	reasonPageRequests = 4
	t.Cleanup(func() { reasonPageRequests = prev })
	s := newReasonScene(t)
	signed := []int64{-9223372036854775808, -10, -2, -1, 0, 2, 10, 9007199254740993, 9223372036854775807}
	shuffled := []int64{10, -1, 9223372036854775807, 0, -10, 9007199254740993, 2, -9223372036854775808, -2}
	for i, id := range shuffled {
		r := s.contactRequest(t, 30+i)
		if err := s.app.DB.Exec("UPDATE contact_requests SET id = ? WHERE id = ?", id, r.ID).Error; err != nil {
			t.Fatal(err)
		}
	}
	var items []reasonItemJSON
	var cursors []string
	var after *string
	for i := 0; i < 10; i++ {
		code, page, body := pageOf(t, s.ac, s.target.ID, after)
		if code != http.StatusOK {
			t.Fatalf("page: %d %s", code, body)
		}
		items = append(items, page.Items...)
		if page.Next == nil {
			break
		}
		cursors = append(cursors, *page.Next)
		after = page.Next
	}
	var got []int64
	for _, it := range items {
		id, _ := parseRequestID(it.RequestID)
		got = append(got, id)
	}
	if fmt.Sprint(got) != fmt.Sprint(signed) {
		t.Fatalf("page order %v, want signed %v", got, signed)
	}
	// 4 requests per page: the cursor is the fourth and the eighth id.
	if fmt.Sprint(cursors) != "[-1 9007199254740993]" {
		t.Errorf("cursor chain %v, want [-1 9007199254740993]", cursors)
	}
	// The old order, kept as a regression fixture: sorting the same ids as text
	// differs, so this test can fail.
	text := make([]string, len(signed))
	for i, id := range signed {
		text[i] = strconv.FormatInt(id, 10)
	}
	sort.Strings(text)
	if fmt.Sprint(text) == fmt.Sprint(signed) {
		t.Fatal("the text order equals the signed order: the fixture proves nothing")
	}
	// The DELETE streams the set in signed order and the digest follows it, so a
	// signed-order envelope is accepted; the decisions are audited in that order.
	if res := deleteAccount(s.ac, s.target.ID, map[string]any{"reasonReview": reviewOf(items, func(reasonItemJSON) string { return "keep" })}); res.code != http.StatusOK {
		t.Fatalf("delete: %d %s", res.code, res.body)
	}
	var audited []int64
	if err := s.app.DB.Raw("SELECT target_id FROM audit_logs WHERE action = ? ORDER BY id", audit.ContactReasonKept).Scan(&audited).Error; err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(audited) != fmt.Sprint(signed) {
		t.Errorf("decision audits in order %v, want %v", audited, signed)
	}
}

// The old items envelope: ignored when the account has no reasons, refused with
// reason_review_outdated (before the permission check, applying nothing) when it
// has some; a malformed one is always a 400.
func TestReasonLegacyEnvelope(t *testing.T) {
	legacyOf := func(items []reasonItemJSON) map[string]any {
		var out []map[string]string
		for _, it := range items {
			out = append(out, map[string]string{"requestId": it.RequestID, "reviewHash": it.ReviewHash, "choice": "remove"})
		}
		return map[string]any{"items": out}
	}
	t.Run("an empty current set ignores it", func(t *testing.T) {
		s := newReasonScene(t)
		res := deleteAccount(s.ac, s.target.ID, map[string]any{"reasonReview": legacyOf([]reasonItemJSON{{RequestID: "5", ReviewHash: strings.Repeat("c", 64)}})})
		if res.code != http.StatusOK {
			t.Fatalf("delete: %d %s", res.code, res.body)
		}
		if n := countAudit(t, s.app.DB, decisionActions...); n != 0 {
			t.Errorf("%d decision audits", n)
		}
		if !strings.Contains(res.body, `"reasonsKept":0`) || !strings.Contains(res.body, `"reasonsRemoved":0`) {
			t.Errorf("counts: %s", res.body)
		}
	})
	t.Run("a nonempty set is outdated, even when every hash matches", func(t *testing.T) {
		s := newReasonScene(t)
		r := s.request(t, s.other, 1)
		items := allPages(t, s.ac, s.target.ID)
		// Before the permission check: a known-underage admin still gets outdated.
		if err := s.app.DB.Model(&models.User{}).Where("id = ?", s.admin.ID).Update("birth_date", time.Now().AddDate(-15, 0, 0)).Error; err != nil {
			t.Fatal(err)
		}
		res := deleteAccount(s.ac, s.target.ID, map[string]any{"reasonReview": legacyOf(items)})
		if res.code != http.StatusConflict || !strings.Contains(res.body, `"code":"reason_review_outdated"`) {
			t.Fatalf("delete: %d %s, want 409 reason_review_outdated", res.code, res.body)
		}
		if reasonTextOf(t, s.app.DB, r.ID) == "" || countAudit(t, s.app.DB, decisionActions...) != 0 {
			t.Error("an outdated review blanked a reason or left a decision")
		}
		var u models.User
		if err := s.app.DB.Unscoped().First(&u, s.target.ID).Error; err != nil || u.DeletedAt.Valid {
			t.Errorf("the account was deleted by a refusal: %v", err)
		}
	})
	t.Run("no review with a nonempty set still needs one", func(t *testing.T) {
		s := newReasonScene(t)
		s.request(t, s.other, 1)
		res := deleteAccount(s.ac, s.target.ID, nil)
		if res.code != http.StatusConflict || !strings.Contains(res.body, `"code":"needs_reason_review"`) {
			t.Fatalf("delete: %d %s", res.code, res.body)
		}
	})
	t.Run("a malformed envelope is a 400 whatever the database holds", func(t *testing.T) {
		s := newReasonScene(t) // an empty set
		h := strings.Repeat("ab", 32)
		for name, raw := range map[string]string{
			"legacy duplicate id":    `{"reasonReview":{"items":[{"requestId":"1","reviewHash":"` + h + `","choice":"keep"},{"requestId":"1","reviewHash":"` + h + `","choice":"keep"}]}}`,
			"legacy case variant":    `{"reasonReview":{"Items":[]}}`,
			"legacy extra key":       `{"reasonReview":{"items":[],"x":"y"}}`,
			"items null":             `{"reasonReview":{"items":null}}`,
			"empty object":           `{"reasonReview":{}}`,
			"new, duplicate count":   `{"reasonReview":{"format":"set-v1","count":"1","count":"1","digest":"` + h + `","choices":"AQ=="}}`,
			"new, count 0":           `{"reasonReview":{"format":"set-v1","count":"0","digest":"` + h + `","choices":""}}`,
			"new, case variant":      `{"reasonReview":{"format":"set-v1","Count":"1","digest":"` + h + `","choices":"AQ=="}}`,
			"new, unchosen code":     `{"reasonReview":{"format":"set-v1","count":"1","digest":"` + h + `","choices":"AA=="}}`,
			"new, count 2^40":        `{"reasonReview":{"format":"set-v1","count":"1099511627776","digest":"` + h + `","choices":"AQ=="}}`,
			"reasonReview twice":     `{"reasonReview":{"items":[]},"reasonReview":null}`,
			"reasonReview in a case": `{"reasonreview":{"items":[]}}`,
		} {
			res := deleteAccount(s.ac, s.target.ID, json.RawMessage(raw))
			if res.code != http.StatusBadRequest || !strings.Contains(res.body, "invalid reason review") {
				t.Errorf("%s: %d %s, want 400 invalid reason review", name, res.code, res.body)
			}
		}
		var u models.User
		if err := s.app.DB.Unscoped().First(&u, s.target.ID).Error; err != nil || u.DeletedAt.Valid {
			t.Errorf("a malformed review deleted the account: %v", err)
		}
	})
	t.Run("an ambiguous attemptId is refused as a body that does not parse", func(t *testing.T) {
		s := newReasonScene(t)
		for _, raw := range []string{`{"attemptId":"a","attemptId":"b"}`, `{"attemptid":"a"}`} {
			res := deleteAccount(s.ac, s.target.ID, json.RawMessage(raw))
			if res.code != http.StatusBadRequest || !strings.Contains(res.body, "invalid body") {
				t.Errorf("%s: %d %s, want 400 invalid body", raw, res.code, res.body)
			}
		}
	})
}

// The set digest: a claim whose count is wrong, whose vector is longer than the
// set or whose digest is for another set is 409 reason_review_changed with
// nothing changed; a claim whose size alone is impossible is a 400.
func TestReasonReviewDeleteStreaming(t *testing.T) {
	s := newReasonScene(t)
	reqs := []models.ContactRequest{s.request(t, s.other, 1), s.request(t, s.second, 2), s.contactRequest(t, 3)}
	items := allPages(t, s.ac, s.target.ID)
	if len(items) != 3 {
		t.Fatalf("%d items, want 3", len(items))
	}
	keep := func(reasonItemJSON) string { return "keep" }
	refuse := func(name string, body map[string]any, code int, api string) {
		t.Helper()
		res := deleteAccount(s.ac, s.target.ID, map[string]any{"reasonReview": body})
		if res.code != code || !strings.Contains(res.body, api) {
			t.Errorf("%s: %d %s, want %d %s", name, res.code, res.body, code, api)
		}
	}
	// A claim of one fewer item, with a digest consistent with that claim.
	refuse("a claim of fewer items", reviewOf(items[:2], keep), http.StatusConflict, `"code":"reason_review_changed"`)
	// A huge, internally consistent vector against a small set: no scaling with the claim.
	huge := reviewOf(items, keep)
	huge["count"] = strconv.Itoa(400000)
	huge["choices"] = base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x55}, 100000))
	refuse("a huge consistent claim", huge, http.StatusConflict, `"code":"reason_review_changed"`)
	// A claim no vector can match: shape.
	shape := reviewOf(items, keep)
	shape["count"] = "1099511627776"
	refuse("a claim of 2^40", shape, http.StatusBadRequest, "invalid reason review")
	// The right claim with a flipped digest bit.
	flipped := reviewOf(items, keep)
	d := []byte(flipped["digest"].(string))
	if d[0] == 'a' {
		d[0] = 'b'
	} else {
		d[0] = 'a'
	}
	flipped["digest"] = string(d)
	refuse("a wrong digest", flipped, http.StatusConflict, `"code":"reason_review_changed"`)
	// A digest computed in the wrong order.
	rev := entriesOf(items, keep)
	rev[0], rev[2] = rev[2], rev[0]
	refuse("a digest made in descending order", envelopeInOrder(rev), http.StatusConflict, `"code":"reason_review_changed"`)
	for _, r := range reqs {
		if reasonTextOf(t, s.app.DB, r.ID) == "" {
			t.Errorf("request %d was blanked by a refusal", r.ID)
		}
	}
	if n := countAudit(t, s.app.DB, decisionActions...); n != 0 {
		t.Errorf("%d decision audits after refusals", n)
	}
	// An empty set ignores a well-formed new-form review made for other rows.
	t.Run("an empty set ignores a stale new-form review", func(t *testing.T) {
		e := newReasonScene(t)
		stale := reviewOf([]reasonItemJSON{{RequestID: "5", ReviewHash: strings.Repeat("c", 64)}}, func(reasonItemJSON) string { return "remove" })
		if res := deleteAccount(e.ac, e.target.ID, map[string]any{"reasonReview": stale}); res.code != http.StatusOK {
			t.Fatalf("delete: %d %s", res.code, res.body)
		}
		if n := countAudit(t, e.app.DB, decisionActions...); n != 0 {
			t.Errorf("%d decision audits for an empty set", n)
		}
	})
}

// The writes go in batches of reasonBlankBatch: boundaries at N and N+1, every
// decision audited once, the removes blanked and the kept left alone.
func TestReasonDeleteBatching(t *testing.T) {
	prev := reasonBlankBatch
	reasonBlankBatch = 2
	t.Cleanup(func() { reasonBlankBatch = prev })
	for _, n := range []int{1, 2, 3, 4, 5} {
		t.Run(fmt.Sprintf("%d reasons in batches of 2", n), func(t *testing.T) {
			s := newReasonScene(t)
			for i := 0; i < n; i++ {
				s.request(t, s.other, 40+i)
			}
			items := allPages(t, s.ac, s.target.ID)
			// Remove every item but the second.
			keepID := ""
			if n >= 2 {
				keepID = items[1].RequestID
			}
			res := deleteAccount(s.ac, s.target.ID, map[string]any{"reasonReview": reviewOf(items, func(it reasonItemJSON) string {
				if it.RequestID == keepID {
					return "keep"
				}
				return "remove"
			})})
			if res.code != http.StatusOK {
				t.Fatalf("delete: %d %s", res.code, res.body)
			}
			wantKept := 0
			if n >= 2 {
				wantKept = 1
			}
			if got := countAudit(t, s.app.DB, audit.AddReasonKept); got != int64(wantKept) {
				t.Errorf("%d kept audits, want %d", got, wantKept)
			}
			if got := countAudit(t, s.app.DB, audit.AddReasonRemoved); got != int64(n-wantKept) {
				t.Errorf("%d removed audits, want %d", got, n-wantKept)
			}
			var blank int64
			if err := s.app.DB.Raw("SELECT count(*) FROM contact_requests WHERE reason = ''").Scan(&blank).Error; err != nil {
				t.Fatal(err)
			}
			if blank != int64(n-wantKept) {
				t.Errorf("%d blanked rows, want %d", blank, n-wantKept)
			}
		})
	}
}

// A hold on a deleted party's users row that only a later batch touches still
// answers 409 delete_reason_busy (never a busy household), with nothing
// deleted and no decision audit; once the row is free the same review applies.
func TestReasonBlankBusyInALaterBatch(t *testing.T) {
	prev := reasonBlankBatch
	reasonBlankBatch = 2
	t.Cleanup(func() { reasonBlankBatch = prev })
	s := newReasonScene(t)
	// Batch one (ids 1 and 2) has a live counterpart; batch two (3 and 4) a tombstone.
	var reqs []models.ContactRequest
	reqs = append(reqs, s.request(t, s.other, 1), s.request(t, s.other, 2), s.request(t, s.second, 3), s.request(t, s.second, 4))
	if err := s.app.DB.Exec("UPDATE users SET deleted_at = now() WHERE id = ?", s.second.ID).Error; err != nil {
		t.Fatal(err)
	}
	review := reviewOf(allPages(t, s.ac, s.target.ID), func(reasonItemJSON) string { return "remove" })
	hold, _ := beginOwnedTx(t, s.app.DB)
	if err := hold.Exec("SELECT id FROM users WHERE id = ? FOR NO KEY UPDATE", s.second.ID).Error; err != nil {
		t.Fatal(err)
	}
	released := false
	release := func() {
		if !released {
			released = true
			hold.Rollback()
		}
	}
	defer release()
	res := deleteAccount(s.ac, s.target.ID, map[string]any{"reasonReview": review})
	if res.code != http.StatusConflict || !strings.Contains(res.body, `"code":"delete_reason_busy"`) || strings.Contains(res.body, "delete_household_busy") {
		t.Fatalf("a held counterparty row in a later batch: %d %s, want 409 delete_reason_busy", res.code, res.body)
	}
	for _, leak := range []string{"55P03", "NOWAIT"} {
		if strings.Contains(res.body, leak) {
			t.Errorf("the response carries %q: %s", leak, res.body)
		}
	}
	for _, r := range reqs {
		if reasonTextOf(t, s.app.DB, r.ID) == "" {
			t.Errorf("request %d was blanked by a refused attempt", r.ID)
		}
	}
	if countAudit(t, s.app.DB, decisionActions...) != 0 {
		t.Error("a refused attempt left a decision audit")
	}
	release()
	if res := deleteAccount(s.ac, s.target.ID, map[string]any{"reasonReview": review}); res.code != http.StatusOK {
		t.Fatalf("after the row is free: %d %s, want 200", res.code, res.body)
	}
	for _, r := range reqs {
		if reasonTextOf(t, s.app.DB, r.ID) != "" {
			t.Errorf("request %d was not blanked once the row was free", r.ID)
		}
	}
}

// The scanner's statements, planned with parameters unknown: the initial one is
// a Limit over an ordered Index Scan with no Sort and no id predicate, the
// continuation has an Index Cond on id and no Filter on it. Planner switches are
// off only to keep a near-empty table from choosing a sequential scan: the proof
// is that the primary key can serve the predicate as written.
func TestReasonScanSQLShape(t *testing.T) {
	s := newReasonScene(t)
	dollar := func(sql string) string {
		n := 0
		for strings.Contains(sql, "?") {
			n++
			sql = strings.Replace(sql, "?", "$"+strconv.Itoa(n), 1)
		}
		return sql
	}
	// plan prepares sql with the named parameter types and explains its generic
	// plan: EXPLAIN (GENERIC_PLAN) cannot be sent with a parameter list through
	// the driver (it counts the $n in the text and wants arguments), and
	// EXPLAIN EXECUTE under force_generic_plan shows the same plan with the
	// parameters unknown.
	plan := func(sql, types, args string) string {
		var lines []string
		err := s.app.DB.Transaction(func(tx *gorm.DB) error {
			for _, set := range []string{"enable_seqscan = off", "enable_bitmapscan = off", "plan_cache_mode = force_generic_plan"} {
				if err := tx.Exec("SET LOCAL " + set).Error; err != nil {
					return err
				}
			}
			if err := tx.Exec("PREPARE reason_scan_shape (" + types + ") AS " + dollar(sql)).Error; err != nil {
				return err
			}
			defer tx.Exec("DEALLOCATE reason_scan_shape")
			return tx.Raw("EXPLAIN EXECUTE reason_scan_shape(" + args + ")").Scan(&lines).Error
		})
		if err != nil {
			t.Fatalf("explain: %v", err)
		}
		return strings.Join(lines, "\n")
	}
	initial := plan(reasonScanInitialSQL, "integer", "100")
	// Either index supplies id order: contact_requests_scope also starts with id.
	orderedIndex := strings.Contains(initial, "contact_requests_pkey") || strings.Contains(initial, "contact_requests_scope")
	if !strings.Contains(initial, "Limit") || !orderedIndex || strings.Contains(initial, "Sort") || strings.Contains(initial, "Index Cond") {
		t.Errorf("initial plan:\n%s", initial)
	}
	cont := plan(reasonScanContinuationSQL, "bigint, integer", "0, 100")
	if !strings.Contains(cont, "Index Cond: (id > $1)") || strings.Contains(cont, "Sort") || strings.Contains(cont, "Filter:") {
		t.Errorf("continuation plan:\n%s", cont)
	}
	// The old statement could not be served that way.
	old := plan("SELECT id, requester_id, subject_user_id, (reason <> '') AS has_reason FROM contact_requests WHERE (?::bigint IS NULL OR id > ?::bigint) ORDER BY id LIMIT ?", "bigint, bigint, integer", "0, 0, 100")
	if strings.Contains(old, "Index Cond: (id > $") {
		t.Errorf("the NULL-or statement gained an Index Cond; this test no longer shows the difference:\n%s", old)
	}
}

// Many executions on one connection, then a forced generic plan: the units
// still walk the whole table in order with no skip, at signed boundary ids.
func TestReasonScanUnderAGenericPlan(t *testing.T) {
	prev := reasonUnitRows
	reasonUnitRows = 3
	t.Cleanup(func() { reasonUnitRows = prev })
	s := newReasonScene(t)
	ids := []int64{-9223372036854775808, -1, 0, 7, 9223372036854775807}
	for i, id := range ids {
		r := s.contactRequest(t, 60+i)
		if err := s.app.DB.Exec("UPDATE contact_requests SET id = ? WHERE id = ?", id, r.ID).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, mode := range []string{"auto", "force_generic_plan"} {
		t.Run(mode, func(t *testing.T) {
			var got []int64
			err := s.app.DB.Transaction(func(tx *gorm.DB) error {
				if err := tx.Exec("SET LOCAL plan_cache_mode = " + mode).Error; err != nil {
					return err
				}
				var after *int64
				for i := 0; i < 20; i++ {
					unit, err := scanReasonItems(tx, liveReasonFilter(int64(s.target.ID)), after, 100)
					if err != nil {
						return err
					}
					got = append(got, unit.Requests...)
					if unit.Next == nil {
						return nil
					}
					after = unit.Next
				}
				t.Fatal("the walk never ended")
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if fmt.Sprint(got) != fmt.Sprint(ids) {
				t.Errorf("walk found %v, want %v", got, ids)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// T07B-2 scaling, second pass: the bounds the first pass only stated. Database
// tests; fixtures here are bulk-made with bulkRequests so a table can hold
// thousands of rows.
// ---------------------------------------------------------------------------

// bulkRequests copies the request template into n new rows (new ids, every
// other column equal) in one statement.
func bulkRequests(t *testing.T, db *gorm.DB, template uint, n int) {
	t.Helper()
	var cols string
	if err := db.Raw(`SELECT string_agg(quote_ident(column_name), ', ' ORDER BY ordinal_position) FROM information_schema.columns
		WHERE table_schema = 'public' AND table_name = 'contact_requests' AND column_name <> 'id'`).Scan(&cols).Error; err != nil || cols == "" {
		t.Fatalf("contact_requests columns: %q %v", cols, err)
	}
	if err := db.Exec("INSERT INTO contact_requests ("+cols+") SELECT "+cols+" FROM (SELECT * FROM contact_requests WHERE id = ?) c, generate_series(1, ?)", template, n).Error; err != nil {
		t.Fatalf("bulk insert: %v", err)
	}
}

// fillerRequest makes a request that is none of the target's: other asks second,
// with a reason, so it is a reasoned row the scanner reads and drops.
func (s reasonScene) fillerRequest(t *testing.T) models.ContactRequest {
	t.Helper()
	return erasureMakeRequest(t, s.app.DB, policy.RequestContact, policy.ContactWithdrawn, s.other.ID, s.second.ID, 1, 0, 0, 999, "")
}

// targetIDs is the reference answer: the ids of every reasoned request the
// target is in, in ascending signed order, straight from SQL.
func targetIDs(t *testing.T, db *gorm.DB, target uint) []int64 {
	t.Helper()
	var ids []int64
	if err := db.Raw("SELECT id FROM contact_requests WHERE reason <> '' AND (requester_id = ? OR subject_user_id = ?) ORDER BY contact_requests.id", target, target).Scan(&ids).Error; err != nil {
		t.Fatal(err)
	}
	return ids
}

// rowHeld says whether another connection cannot take the row's lock right now.
// NOWAIT never waits, so this is a read of the row-lock table through the only
// interface that counts, not through granted pg_locks entries.
func rowHeld(t *testing.T, db *gorm.DB, id int64) bool {
	t.Helper()
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	defer tx.Rollback()
	err := tx.Exec("SELECT id FROM contact_requests WHERE id = ? FOR UPDATE NOWAIT", id).Error
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == "55P03" {
		return true
	}
	if err != nil {
		t.Fatalf("lock probe on %d: %v", id, err)
	}
	return false
}

// explainLines runs EXPLAIN (ANALYZE, BUFFERS) for sql (built with Sprintf from
// numbers only) in tx and returns the text plan.
func explainLines(t *testing.T, tx *gorm.DB, sql string) string {
	t.Helper()
	var lines []string
	if err := tx.Raw("EXPLAIN (ANALYZE, BUFFERS) " + sql).Scan(&lines).Error; err != nil {
		t.Fatalf("explain: %v", err)
	}
	return strings.Join(lines, "\n")
}

var (
	planRemoved = regexp.MustCompile(`Rows Removed by Filter: (\d+)`)
	planBuffers = regexp.MustCompile(`Buffers: shared hit=(\d+)(?: read=(\d+))?`)
	planIndex   = regexp.MustCompile(`Index Scan using \S+ on contact_requests.*actual time=[\d.]+\.\.[\d.]+ rows=(\d+) loops=`)
)

func planInt(t *testing.T, re *regexp.Regexp, plan string, group int) int {
	t.Helper()
	m := re.FindStringSubmatch(plan)
	if m == nil || m[group] == "" {
		return 0
	}
	n, err := strconv.Atoi(m[group])
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// ---- the investigation of the old envelope's answers -------------------------

// The old {items} envelope against the new server, in every combination of
// envelope, set and admin the route can meet. Order of refusals, which this
// pins (T07B-2 R6): malformed (400, whatever the set) / no reasons (everything
// is ignored and the account is deleted, whoever the admin is) / absent or old
// envelope with reasons (needs_reason_review or reason_review_outdated, both
// BEFORE the permission check, as needs_reason_review always was) / a set-v1
// envelope with reasons (permission first, then the comparison).
//
// What changed against 166da5d, and only this: a well-formed old envelope for
// an account that has reasons used to reach the permission check and the
// comparison (403 for an under-18 admin, 409 reason_review_changed for a stale
// one, and applied choices for a current one). It is now always 409
// reason_review_outdated and nothing in it is applied. A page reload then shows
// the person what is true: the page route still answers 403 for the same admin.
func TestReasonOrderingMatrix(t *testing.T) {
	type admin struct {
		name  string
		under bool
	}
	admins := []admin{{"an adult admin", false}, {"an admin known to be under 18", true}}
	legacy := func(items []reasonItemJSON) map[string]any {
		out := []map[string]string{} // an empty array, not null: a well-formed old envelope
		for _, it := range items {
			out = append(out, map[string]string{"requestId": it.RequestID, "reviewHash": it.ReviewHash, "choice": "keep"})
		}
		return map[string]any{"items": out}
	}
	stale := func(items []reasonItemJSON) map[string]any {
		return envelopeOf(append(entriesOf(items, func(reasonItemJSON) string { return "keep" }), reviewEntry{ID: 424242, Hash: strings.Repeat("0", 64), Choice: "keep"}))
	}
	type want struct {
		code int
		api  string // the code in the body, "" for a deletion
	}
	type envelope struct {
		name string
		body func(items []reasonItemJSON) any
		// empty and full are the answers for an account without and with reasons.
		empty, adultFull, underFull want
	}
	ok := want{http.StatusOK, ""}
	envelopes := []envelope{
		{"no review", func([]reasonItemJSON) any { return nil }, ok, want{409, "needs_reason_review"}, want{409, "needs_reason_review"}},
		{"the old items envelope", func(i []reasonItemJSON) any { return map[string]any{"reasonReview": legacy(i)} }, ok, want{409, "reason_review_outdated"}, want{409, "reason_review_outdated"}},
		{"a current set-v1 review", func(i []reasonItemJSON) any {
			if len(i) == 0 { // an account with none: any well-formed review is ignored
				return map[string]any{"reasonReview": envelopeOf([]reviewEntry{{ID: 5, Hash: strings.Repeat("c", 64), Choice: "remove"}})}
			}
			return map[string]any{"reasonReview": reviewOf(i, func(reasonItemJSON) string { return "keep" })}
		}, ok, ok, want{403, "reason_review_not_allowed"}},
		{"a stale set-v1 review", func(i []reasonItemJSON) any { return map[string]any{"reasonReview": stale(i)} }, ok, want{409, "reason_review_changed"}, want{403, "reason_review_not_allowed"}},
		{"a malformed review", func([]reasonItemJSON) any {
			return json.RawMessage(`{"reasonReview":{"items":[],"x":"y"}}`)
		}, want{400, "invalid reason review"}, want{400, "invalid reason review"}, want{400, "invalid reason review"}},
	}
	for _, a := range admins {
		for _, e := range envelopes {
			for _, full := range []bool{false, true} {
				name := fmt.Sprintf("%s, %s, %s", a.name, e.name, map[bool]string{false: "no reasons", true: "reasons"}[full])
				t.Run(name, func(t *testing.T) {
					s := newReasonScene(t)
					var r models.ContactRequest
					var items []reasonItemJSON
					if full {
						r = s.request(t, s.other, 1)
						items = allPages(t, s.ac, s.target.ID)
					}
					if a.under {
						if err := s.app.DB.Model(&models.User{}).Where("id = ?", s.admin.ID).Update("birth_date", time.Now().AddDate(-15, 0, 0)).Error; err != nil {
							t.Fatal(err)
						}
					}
					w := e.empty
					if full {
						w = e.adultFull
						if a.under {
							w = e.underFull
						}
					}
					res := deleteAccount(s.ac, s.target.ID, e.body(items))
					if res.code != w.code || (w.api != "" && !strings.Contains(res.body, w.api)) {
						t.Fatalf("%d %s, want %d %s", res.code, res.body, w.code, w.api)
					}
					var u models.User
					if err := s.app.DB.Unscoped().First(&u, s.target.ID).Error; err != nil {
						t.Fatal(err)
					}
					if deleted := u.DeletedAt.Valid; deleted != (w.code == http.StatusOK) {
						t.Errorf("deleted = %v after a %d", deleted, w.code)
					}
					if w.code != http.StatusOK && full && reasonTextOf(t, s.app.DB, r.ID) == "" {
						t.Error("a refusal blanked a reason")
					}
					if w.code != http.StatusOK && countAudit(t, s.app.DB, decisionActions...) != 0 {
						t.Error("a refusal wrote a decision audit")
					}
					if a.under && full && e.name == "the old items envelope" {
						// The person is not stuck: a reload shows the page's own refusal.
						if code, _, body := pageOf(t, s.ac, s.target.ID, nil); code != http.StatusForbidden || !strings.Contains(body, "reason_review_not_allowed") {
							t.Errorf("the page for the same admin: %d %s, want 403 reason_review_not_allowed", code, body)
						}
					}
				})
			}
		}
	}
}

// ---- the probe ---------------------------------------------------------------

// The probe takes no row lock, and the streamed consume locks exactly the
// account's reasoned rows, whole, and nothing else. Locks are read with NOWAIT
// from a third connection (never from granted pg_locks entries, which count
// tuple locks unreliably).
func TestReasonProbeIsBounded(t *testing.T) {
	s := newReasonScene(t)
	mine := []models.ContactRequest{s.request(t, s.other, 1), s.request(t, s.second, 2), s.contactRequest(t, 3)}
	others := []models.ContactRequest{s.fillerRequest(t)}
	// A blank reason of the target's own: not part of the set, so not locked.
	blank := s.contactRequest(t, 4)
	if err := s.app.DB.Exec("UPDATE contact_requests SET reason = '' WHERE id = ?", blank.ID).Error; err != nil {
		t.Fatal(err)
	}
	ids := func(rs ...models.ContactRequest) (out []int64) {
		for _, r := range rs {
			out = append(out, int64(r.ID))
		}
		return out
	}

	t.Run("the probe locks nothing, even parked inside its transaction", func(t *testing.T) {
		tx, _ := beginOwnedTx(t, s.app.DB)
		has, err := reasonSetExistsIn(tx, s.target.ID)
		if err != nil || !has {
			t.Fatalf("probe = %v, %v, want true", has, err)
		}
		for _, id := range ids(append(append(mine, others...), blank)...) {
			if rowHeld(t, s.app.DB, id) {
				t.Errorf("row %d is locked by the probe", id)
			}
		}
		tx.Rollback()
	})

	t.Run("the probe answers false for an account with none", func(t *testing.T) {
		has, err := reasonSetExistsIn(s.app.DB, s.second.ID+1000)
		if err != nil || has {
			t.Fatalf("probe = %v, %v, want false", has, err)
		}
	})

	t.Run("the consume locks the whole set while it streams, and only the set", func(t *testing.T) {
		items := allPages(t, s.ac, s.target.ID)
		if len(items) != 3 {
			t.Fatalf("%d items, want 3", len(items))
		}
		parked, release := make(chan struct{}), make(chan struct{})
		var once sync.Once
		prev := reasonRowHook
		reasonRowHook = func(seen uint64) {
			if seen == 2 {
				once.Do(func() { close(parked) })
				<-release
			}
		}
		t.Cleanup(func() { reasonRowHook = prev })
		done := make(chan httpResult, 1)
		go func() {
			done <- deleteAccount(s.ac, s.target.ID, map[string]any{"reasonReview": reviewOf(items, func(reasonItemJSON) string { return "keep" })})
		}()
		select {
		case <-parked:
		case <-time.After(20 * time.Second):
			close(release)
			t.Fatal("the consume never reached its second row")
		}
		var held, free []int64
		for _, id := range ids(mine...) {
			if rowHeld(t, s.app.DB, id) {
				held = append(held, id)
			}
		}
		for _, id := range ids(append(others, blank)...) {
			if !rowHeld(t, s.app.DB, id) {
				free = append(free, id)
			}
		}
		close(release)
		res := <-done
		if len(held) != len(mine) {
			t.Errorf("rows held while parked: %v of %v (the whole set is locked by design)", held, ids(mine...))
		}
		if len(free) != 2 {
			t.Errorf("rows outside the set that were free: %v, want both", free)
		}
		if res.code != http.StatusOK {
			t.Fatalf("delete: %d %s", res.code, res.body)
		}
	})
}

// planBlocks is the number of buffer blocks (hit plus read, shared plus local)
// the top node of an EXPLAIN (ANALYZE, BUFFERS) plan touched. Local buffers are
// what a temporary table uses.
func planBlocks(plan string) int {
	m := regexp.MustCompile(`Buffers: ([^\n]*)`).FindStringSubmatch(plan)
	if m == nil {
		return 0
	}
	total, kind := 0, ""
	for _, tok := range strings.FieldsFunc(m[1], func(r rune) bool { return r == ' ' || r == ',' }) {
		switch tok {
		case "shared", "local", "temp":
			kind = tok
			continue
		}
		if kind == "temp" {
			continue
		}
		if strings.HasPrefix(tok, "hit=") || strings.HasPrefix(tok, "read=") {
			n, err := strconv.Atoi(tok[strings.Index(tok, "=")+1:])
			if err == nil {
				total += n
			}
		}
	}
	return total
}

// What the probe costs when nothing matches, when the match is last and when it
// is first. LIMIT 1 bounds the answer and the locks, not the read: a table with
// no match is read whole (the 40 s deletion budget bounds that, not this
// statement). The early-stop assertion is made only where it is achievable: a
// serial heap scan whose physical order the fixture controls.
//
// The measured fixture is a private temporary table with contact_requests'
// columns, and the statement is reasonProbeSQL with the table name swapped. The
// shared table cannot give a deterministic block count: testdb empties tables
// with DELETE, so earlier tests leave dead tuples in its heap, a scan reads
// those pages ahead of a new row, and VACUUM FULL keeps them while any other
// session or prepared transaction still holds an older snapshot. In the suite
// that showed as a first-row match that removed 0 rows yet read 500 or 1000
// pages, a different number each run; alone, 1 page. A temporary table is new
// storage that only this session can see, so its pages are its own. The shared
// table is still measured for what it can prove (rows removed, which counts
// live tuples only); its block counts are logged with the facts that explain
// them, not asserted.
func TestReasonProbeScanCost(t *testing.T) {
	const filler = 20000
	s := newReasonScene(t)
	target := strconv.Itoa(int(s.target.ID))

	measure := func(t *testing.T, order string) (removed, blocks int) {
		var plan string
		err := s.app.DB.Transaction(func(tx *gorm.DB) error {
			if err := tx.Exec("CREATE TEMP TABLE probe_cost (LIKE contact_requests INCLUDING DEFAULTS) ON COMMIT DROP").Error; err != nil {
				return err
			}
			insert := func(from, to int, reason string, requester, subject string) error {
				return tx.Exec(`INSERT INTO probe_cost (id, requester_id, subject_user_id, context_type, context_id, fields, reason, needs_admin, admin_decision, expires_at)
					SELECT g, `+requester+`, `+subject+`, 'group', 1, 'email', ?, false, 'not_needed', now() FROM generate_series(?::bigint, ?::bigint) g`, reason, from, to).Error
			}
			mine, other := target, "99999999"
			rows := []func() error{
				func() error { return insert(1, filler, "filler reason", other, other+"+1") },
			}
			match := func() error { return insert(filler+1, filler+1, "the target's reason", mine, other) }
			switch order {
			case "absent":
			case "late":
				rows = append(rows, match)
			case "early":
				rows = append([]func() error{func() error { return insert(filler+1, filler+1, "the target's reason", mine, other) }}, rows...)
			}
			for _, f := range rows {
				if err := f(); err != nil {
					return err
				}
			}
			if err := tx.Exec("ANALYZE probe_cost").Error; err != nil {
				return err
			}
			// No indexes exist; synchronized scans could still start midway through
			// the heap and parallel workers could read ahead, either of which would
			// defeat the fixture's deliberate row order.
			for _, set := range []string{"synchronize_seqscans = off", "max_parallel_workers_per_gather = 0", "enable_bitmapscan = off"} {
				if err := tx.Exec("SET LOCAL " + set).Error; err != nil {
					return err
				}
			}
			probe := strings.NewReplacer("?", target).Replace(reasonProbeSQL)
			if strings.Count(probe, "contact_requests") != 1 {
				t.Fatalf("the probe statement names its table %d times: %s", strings.Count(probe, "contact_requests"), probe)
			}
			plan = explainLines(t, tx, strings.Replace(probe, "contact_requests", "probe_cost", 1))
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(plan, "Seq Scan on probe_cost") || !strings.Contains(plan, "Limit") || strings.Contains(plan, "Gather") {
			t.Fatalf("the controlled plan is not a limited, serial heap scan:\n%s", plan)
		}
		t.Logf("%s plan:\n%s", order, plan)
		return planInt(t, planRemoved, plan, 1), planBlocks(plan)
	}
	var absentRows, lateRows, earlyRows, absentBlocks, lateBlocks, earlyBlocks int
	t.Run("absent", func(t *testing.T) { absentRows, absentBlocks = measure(t, "absent") })
	t.Run("late", func(t *testing.T) { lateRows, lateBlocks = measure(t, "late") })
	t.Run("early", func(t *testing.T) { earlyRows, earlyBlocks = measure(t, "early") })
	t.Logf("probe over %d unrelated reasoned rows: no match removed %d rows (%d blocks); match last removed %d (%d); match first removed %d (%d)",
		filler, absentRows, absentBlocks, lateRows, lateBlocks, earlyRows, earlyBlocks)
	if absentRows < filler || lateRows < filler {
		t.Errorf("a probe with no match, or a late one, must read the table: removed %d and %d of %d", absentRows, lateRows, filler)
	}
	if earlyRows > filler/100 {
		t.Errorf("the early match still removed %d rows: the limit did not stop the heap scan", earlyRows)
	}
	if lateBlocks < 100 {
		t.Errorf("the full read touched only %d blocks: the fixture is too small to show an early stop", lateBlocks)
	}
	if earlyBlocks*10 > lateBlocks {
		t.Errorf("early-stop blocks %d are not far below the full read %d", earlyBlocks, lateBlocks)
	}

	// The shared table itself: rows removed are live-tuple counts and so are
	// deterministic; blocks and the facts behind them are logged.
	t.Run("contact_requests", func(t *testing.T) {
		shared := func(t *testing.T, build func(t *testing.T, s reasonScene)) (removed, blocks int, facts string) {
			sc := newReasonScene(t)
			if err := sc.app.DB.Exec("VACUUM FULL contact_requests").Error; err != nil {
				t.Fatal(err)
			}
			build(t, sc)
			if err := sc.app.DB.Exec("ANALYZE contact_requests").Error; err != nil {
				t.Fatal(err)
			}
			var plan string
			if err := sc.app.DB.Transaction(func(tx *gorm.DB) error {
				for _, set := range []string{"enable_indexscan", "enable_bitmapscan", "enable_indexonlyscan", "synchronize_seqscans"} {
					if err := tx.Exec("SET LOCAL " + set + " = off").Error; err != nil {
						return err
					}
				}
				plan = explainLines(t, tx, strings.NewReplacer("?", strconv.Itoa(int(sc.target.ID))).Replace(reasonProbeSQL))
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(plan, "Seq Scan on contact_requests") || !strings.Contains(plan, "Limit") || strings.Contains(plan, "Gather") {
				t.Fatalf("the controlled plan is not a limited, serial heap scan:\n%s", plan)
			}
			var pages, dead, holders, prepared, slots int64
			for q, dst := range map[string]*int64{
				"SELECT pg_relation_size('contact_requests') / 8192":                                               &pages,
				"SELECT coalesce(max(n_dead_tup), 0) FROM pg_stat_user_tables WHERE relname = 'contact_requests'":  &dead,
				"SELECT count(*) FROM pg_stat_activity WHERE backend_xmin IS NOT NULL AND pid <> pg_backend_pid()": &holders,
				"SELECT count(*) FROM pg_prepared_xacts":                                                           &prepared,
				"SELECT count(*) FROM pg_replication_slots":                                                        &slots,
			} {
				if err := sc.app.DB.Raw(q).Scan(dst).Error; err != nil {
					t.Fatal(err)
				}
			}
			facts = fmt.Sprintf("heap %d pages, %d dead tuples (stats), %d other sessions holding a snapshot, %d prepared transactions, %d replication slots", pages, dead, holders, prepared, slots)
			return planInt(t, planRemoved, plan, 1), planBlocks(plan), facts
		}
		fill := func(t *testing.T, sc reasonScene) { bulkRequests(t, sc.app.DB, sc.fillerRequest(t).ID, filler) }
		var rows [3]int
		var blocks [3]int
		var facts [3]string
		t.Run("absent", func(t *testing.T) { rows[0], blocks[0], facts[0] = shared(t, fill) })
		t.Run("late", func(t *testing.T) {
			rows[1], blocks[1], facts[1] = shared(t, func(t *testing.T, sc reasonScene) { fill(t, sc); sc.contactRequest(t, 1) })
		})
		t.Run("early", func(t *testing.T) {
			rows[2], blocks[2], facts[2] = shared(t, func(t *testing.T, sc reasonScene) { sc.contactRequest(t, 1); fill(t, sc) })
		})
		t.Logf("shared table: removed %v rows, touched %v blocks; %s | %s | %s", rows, blocks, facts[0], facts[1], facts[2])
		if rows[0] < filler || rows[1] < filler {
			t.Errorf("a probe with no match, or a late one, must read the table: removed %d and %d of %d", rows[0], rows[1], filler)
		}
		if rows[2] > filler/100 {
			t.Errorf("the early match still removed %d rows: the limit did not stop the heap scan", rows[2])
		}
	})
}

// ---- the consume is bounded by what the database holds, not by the claim ------

// A claim for millions of reasons against a set of three, in three parts so
// each cost is seen alone.
//
//  1. End to end: the route refuses it as changed and changes nothing. Its total
//     allocation includes the JSON read, decode and check of a multi-megabyte
//     body, which is unavoidable and linear in the body; the 32-bytes-per-claim
//     backstop here only catches a record per claimed reason. The tight bounds on
//     parsing alone are TestReasonEnvelopeParseMemoryIsLinearInTheBody.
//  2. The consume alone: planReasonReviewIn is called on an envelope built in
//     memory (so no JSON is parsed in the window) for a fixed actual set of three
//     rows, with claims from a million to 64 million. What it allocates must not
//     depend on the claim: the same, within a fixed allowance, at every claim.
//  3. TestReasonConsumePeakMemoryOnLargeSets measures peak memory on large actual
//     sets.
func TestReasonConsumeMemoryDoesNotScaleWithClaim(t *testing.T) {
	s := newReasonScene(t)
	s.request(t, s.other, 1)
	s.request(t, s.second, 2)
	s.contactRequest(t, 3)
	items := allPages(t, s.ac, s.target.ID)

	t.Run("end to end", func(t *testing.T) {
		claim := func(n int) json.RawMessage {
			env := reviewOf(items, func(reasonItemJSON) string { return "keep" })
			env["count"] = strconv.Itoa(n)
			env["choices"] = base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x55}, n/4))
			b, err := json.Marshal(map[string]any{"reasonReview": env})
			if err != nil {
				t.Fatal(err)
			}
			return b
		}
		measure := func(n int) uint64 {
			body := claim(n)
			var before, after runtime.MemStats
			runtime.GC()
			runtime.ReadMemStats(&before)
			res := deleteAccount(s.ac, s.target.ID, body)
			runtime.ReadMemStats(&after)
			if res.code != http.StatusConflict || !strings.Contains(res.body, `"code":"reason_review_changed"`) {
				t.Fatalf("claim of %d: %d %.200s, want 409 reason_review_changed", n, res.code, res.body)
			}
			return after.TotalAlloc - before.TotalAlloc
		}
		small, large := 4_000_000, 16_000_000 // the larger vector is 4 MB: about 5.3 MB of base64, under the 6 MiB body limit
		a, b := measure(small), measure(large)
		extra := int64(b) - int64(a)
		t.Logf("allocated %d bytes for a claim of %d and %d for %d: %d bytes more for %d more claimed reasons", a, small, b, large, extra, large-small)
		if allowance := int64(large-small) * 32; extra > allowance {
			t.Errorf("memory grew by %d bytes for %d more claimed reasons, over the %d-byte allowance (32 per claimed reason)", extra, large-small, allowance)
		}
		for _, r := range []string{"identifying reason 1", "identifying reason 2", "identifying reason 3"} {
			var n int64
			if err := s.app.DB.Raw("SELECT count(*) FROM contact_requests WHERE reason = ?", r).Scan(&n).Error; err != nil || n != 1 {
				t.Errorf("reason %q: %d rows, %v", r, n, err)
			}
		}
	})

	t.Run("the consume alone, for a fixed actual set", func(t *testing.T) {
		var actor, target models.User
		if err := s.app.DB.First(&actor, s.admin.ID).Error; err != nil {
			t.Fatal(err)
		}
		if err := s.app.DB.First(&target, s.target.ID).Error; err != nil {
			t.Fatal(err)
		}
		// One call's allocation, in a transaction that is rolled back. The claim's
		// choices vector exists before the window opens; the call must not copy it.
		consume := func(claim uint64, choices []byte) uint64 {
			var used uint64
			err := s.app.DB.Transaction(func(tx *gorm.DB) error {
				env := reasonEnvelope{kind: reasonEnvelopeNew, count: claim, choices: choices}
				var err error
				used = allocated(func() { _, _, err = s.app.planReasonReviewIn(tx, &actor, &target, env, "") })
				if !errors.Is(err, errReasonReviewChanged) {
					t.Fatalf("claim of %d: %v, want errReasonReviewChanged", claim, err)
				}
				return errors.New("roll back")
			})
			if err == nil || err.Error() != "roll back" {
				t.Fatal(err)
			}
			return used
		}
		// The least of several runs drops stray allocations from other goroutines.
		least := func(claim uint64) uint64 {
			choices := make([]byte, (claim+3)/4)
			min := ^uint64(0)
			for i := 0; i < 5; i++ {
				if u := consume(claim, choices); u < min {
					min = u
				}
			}
			return min
		}
		const allowance = 64 << 10 // the difference between any two claims, for the same three rows
		base := least(1 << 20)
		for _, claim := range []uint64{1 << 24, 1 << 26} {
			got := least(claim)
			t.Logf("consume of 3 actual reasons: %d bytes at a claim of %d, %d at %d", base, 1<<20, got, claim)
			if got > base+allowance {
				t.Errorf("claim %d allocated %d bytes against %d at a claim of %d: more than %d bytes apart, so it scales with the claim", claim, got, base, 1<<20, allowance)
			}
		}
		if base > 1<<20 {
			t.Errorf("reading three reasons allocated %d bytes: over 1 MiB", base)
		}
	})
}

// peakHeapGrowth runs f while a sampler reads the live heap every half
// millisecond, and returns the most it grew beyond the heap after a collection
// just before f. It is a sampled peak, so it can miss a spike shorter than the
// sampling interval; the bounds asserted from it have room for that.
func peakHeapGrowth(f func()) uint64 {
	runtime.GC()
	var base runtime.MemStats
	runtime.ReadMemStats(&base)
	stop, done := make(chan struct{}), make(chan uint64)
	go func() {
		var m runtime.MemStats
		var peak uint64
		tick := time.NewTicker(500 * time.Microsecond)
		defer tick.Stop()
		for {
			select {
			case <-stop:
				done <- peak
				return
			case <-tick.C:
				runtime.ReadMemStats(&m)
				peak = max(peak, m.HeapAlloc)
			}
		}
	}()
	f()
	var end runtime.MemStats
	runtime.ReadMemStats(&end)
	close(stop)
	peak := max(<-done, end.HeapAlloc)
	if peak < base.HeapAlloc {
		return 0
	}
	return peak - base.HeapAlloc
}

// Peak memory on large actual sets, apart from the claim: the consume's peak
// grows with the real rows it keeps (a small record each), and the write stage's
// peak does not grow with the set at all (it holds one batch). Measured at 20,000
// and at 60,000 real reasons, with the collector made eager so garbage does not
// stand in for retention. The bounds are per row and fixed, with room for the
// sampler, and far under what a hash, a text or a metadata map per row (a kilobyte
// or more each) would cost.
func TestReasonConsumePeakMemoryOnLargeSets(t *testing.T) {
	defer debug.SetGCPercent(debug.SetGCPercent(20))
	const first, second = 20_000, 60_000
	s := newReasonScene(t)
	tpl := s.contactRequest(t, 1)
	var actor, target models.User
	if err := s.app.DB.First(&actor, s.admin.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.app.DB.First(&target, s.target.ID).Error; err != nil {
		t.Fatal(err)
	}
	grow := func(to int) {
		var have int64
		if err := s.app.DB.Raw("SELECT count(*) FROM contact_requests WHERE reason <> '' AND requester_id = ?", s.target.ID).Scan(&have).Error; err != nil {
			t.Fatal(err)
		}
		bulkRequests(t, s.app.DB, tpl.ID, to-int(have))
	}
	type peaks struct{ consume, write uint64 }
	measure := func(n int) peaks {
		grow(n)
		var p peaks
		// The consume, with the claim equal to the set so every row is kept.
		env := reasonEnvelope{kind: reasonEnvelopeNew, count: uint64(n), choices: make([]byte, (n+3)/4)}
		err := s.app.DB.Transaction(func(tx *gorm.DB) error {
			var err error
			p.consume = peakHeapGrowth(func() { _, _, err = s.app.planReasonReviewIn(tx, &actor, &target, env, "") })
			if !errors.Is(err, errReasonReviewChanged) {
				t.Fatalf("consume of %d: %v, want errReasonReviewChanged", n, err)
			}
			return errors.New("roll back")
		})
		if err == nil || err.Error() != "roll back" {
			t.Fatal(err)
		}
		// The write stage, for decisions made before the window: half blanked, half kept.
		ids := targetIDs(t, s.app.DB, s.target.ID)
		if len(ids) != n {
			t.Fatalf("%d reasons, want %d", len(ids), n)
		}
		plan := reasonPlan{ActorID: actor.ID}
		for i, id := range ids {
			plan.Decisions = append(plan.Decisions, reasonDecision{RequestID: id, Remove: i%2 == 0, Role: "requester", Purpose: policy.RequestContact, Status: policy.ContactWithdrawn})
		}
		err = s.app.DB.Transaction(func(tx *gorm.DB) error {
			var err error
			p.write = peakHeapGrowth(func() { err = eraseContactRequestReasonsIn(tx, erasureSubject{ID: s.target.ID}, plan) })
			if err != nil {
				t.Fatalf("write of %d: %v", n, err)
			}
			return errors.New("roll back")
		})
		if err == nil || err.Error() != "roll back" {
			t.Fatal(err)
		}
		return p
	}
	a, b := measure(first), measure(second)
	t.Logf("peak heap growth: consume %d bytes at %d reasons, %d at %d; write %d at %d, %d at %d", a.consume, first, b.consume, second, a.write, first, b.write, second)
	if slope := (float64(b.consume) - float64(a.consume)) / float64(second-first); slope > 400 {
		t.Errorf("the consume's peak grows by %.0f bytes per real reason (400 allowed; a 72-byte record that doubles as it grows is about 216)", slope)
	}
	if b.consume > 400*second+8<<20 {
		t.Errorf("the consume's peak at %d reasons is %d bytes", second, b.consume)
	}
	if grew := int64(b.write) - int64(a.write); grew > 4<<20 {
		t.Errorf("the write stage's peak grew %d bytes from %d to %d reasons: it should hold one batch whatever the set", grew, first, second)
	}
	if b.write > 32<<20 {
		t.Errorf("the write stage's peak at %d reasons is %d bytes (32 MiB allowed)", second, b.write)
	}
}

// ---- batches -----------------------------------------------------------------

// newProxiedReasonScene is a reason scene whose routes reach the database
// through pgproxy, which logs every statement the server runs.
func newProxiedReasonScene(t *testing.T) (reasonScene, *ctxRig) {
	t.Helper()
	s := newReasonScene(t)
	rig := newCtxRigFor(t, s.app)
	bootFence(t, rig.papp)
	rig.papp.requestRestart = func() {}
	s.ac = loginAPI(t, rig.papp, "erase-admin", erasurePW)
	return s, rig
}

// With the batch shortened to two, the statements one deletion sends are, per
// batch in order: its audit INSERT and then, only when the batch removes
// something, its blanking UPDATE. Checked at N and N+1 around each boundary,
// from the proxy's statement log and not from the rows.
func TestReasonDeleteBatchStatements(t *testing.T) {
	prev := reasonBlankBatch
	reasonBlankBatch = 2
	t.Cleanup(func() { reasonBlankBatch = prev })
	// Keep ordinals 2 and 3 (the whole second batch of four) and remove the rest.
	keep := map[int]bool{2: true, 3: true}
	for _, n := range []int{1, 2, 3, 4, 5, 6} {
		t.Run(fmt.Sprintf("%d reasons", n), func(t *testing.T) {
			s, rig := newProxiedReasonScene(t)
			for i := 0; i < n; i++ {
				s.request(t, s.other, 70+i)
			}
			items := allPages(t, s.ac, s.target.ID)
			ordinal := map[string]int{}
			for i, it := range items {
				ordinal[it.RequestID] = i
			}
			// Only the deletion's own statements are compared.
			start := len(rig.proxy.Events())
			res := deleteAccount(s.ac, s.target.ID, map[string]any{"reasonReview": reviewOf(items, func(it reasonItemJSON) string {
				if keep[ordinal[it.RequestID]] {
					return "keep"
				}
				return "remove"
			})})
			if res.code != http.StatusOK {
				t.Fatalf("delete: %d %s", res.code, res.body)
			}
			var got, want []string
			for _, e := range rig.proxy.Events()[start:] {
				if e.Kind != testdb.KindStatement || e.Action != testdb.ActionForwarded {
					continue
				}
				switch {
				case strings.Contains(e.SQL, "jsonb_to_recordset"):
					got = append(got, "audit")
				case strings.Contains(e.SQL, "UPDATE contact_requests SET reason = ''"):
					got = append(got, "blank")
				}
			}
			for first := 0; first < n; first += 2 {
				want = append(want, "audit")
				for i := first; i < min(first+2, n); i++ {
					if !keep[i] {
						want = append(want, "blank")
						break
					}
				}
			}
			if fmt.Sprint(got) != fmt.Sprint(want) {
				t.Fatalf("statements %v, want %v", got, want)
			}
			var blank int64
			if err := s.app.DB.Raw("SELECT count(*) FROM contact_requests WHERE reason = ''").Scan(&blank).Error; err != nil {
				t.Fatal(err)
			}
			wantBlank := 0
			for i := 0; i < n; i++ {
				if !keep[i] {
					wantBlank++
				}
			}
			if blank != int64(wantBlank) {
				t.Errorf("%d blanked rows, want %d", blank, wantBlank)
			}
		})
	}
}

// injectRequestFault makes an UPDATE of the request's reason raise (mode
// "raise") or silently skip the row ("skip"), through a BEFORE UPDATE trigger
// that fires ahead of the migrations' own (triggers run in name order). It is
// removed when the test ends: testdb clears rows between tests, not triggers.
func injectRequestFault(t *testing.T, db *gorm.DB, id uint, mode string) {
	t.Helper()
	action := "RAISE EXCEPTION 'injected failure' USING ERRCODE = 'P0001'"
	if mode == "skip" {
		action = "RETURN NULL"
	}
	for _, sql := range []string{
		fmt.Sprintf(`CREATE FUNCTION aaa_reason_fault() RETURNS trigger LANGUAGE plpgsql AS $f$
			BEGIN IF OLD.id = %d AND NEW.reason = '' THEN %s; END IF; RETURN NEW; END $f$`, id, action),
		"CREATE TRIGGER aaa_reason_fault BEFORE UPDATE ON contact_requests FOR EACH ROW EXECUTE FUNCTION aaa_reason_fault()",
	} {
		if err := db.Exec(sql).Error; err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		db.Exec("DROP TRIGGER IF EXISTS aaa_reason_fault ON contact_requests")
		db.Exec("DROP FUNCTION IF EXISTS aaa_reason_fault()")
	})
}

// A failure in any batch, or a blanking that touches a different number of rows
// than the review named, rolls back the whole deletion: no decision audit from
// an earlier batch, no blanked row, the account still there, the attempt id
// rolled back, and nothing about it said to the admin but "could not delete".
func TestReasonDeleteFaultsRollBack(t *testing.T) {
	prev := reasonBlankBatch
	reasonBlankBatch = 2
	t.Cleanup(func() { reasonBlankBatch = prev })
	for _, tc := range []struct {
		name    string
		mode    string
		ordinal int // which reason (0-based, ascending id) the fault is on
	}{
		{"a statement error in the second batch", "raise", 3},
		{"a statement error in the last, partial batch", "raise", 4},
		{"a row skipped in the first batch", "skip", 1},
		{"a row skipped in the second batch", "skip", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newReasonScene(t)
			var reqs []models.ContactRequest
			for i := 0; i < 5; i++ {
				reqs = append(reqs, s.request(t, s.other, 80+i))
			}
			items := allPages(t, s.ac, s.target.ID)
			review := reviewOf(items, func(reasonItemJSON) string { return "remove" })
			injectRequestFault(t, s.app.DB, reqs[tc.ordinal].ID, tc.mode)
			id := mintAttemptFor(t, s.ac, s.target.ID)
			res := deleteAccount(s.ac, s.target.ID, map[string]any{"attemptId": id, "reasonReview": review})
			// A 500 never carries its cause (the route logs "could not delete user" and
			// answers the generic server_error); the cause is in the log and in the
			// rollback asserted below.
			if res.code != http.StatusInternalServerError || !strings.Contains(res.body, `"code":"server_error"`) {
				t.Fatalf("delete: %d %s, want 500 server_error", res.code, res.body)
			}
			for _, leak := range []string{"delete_reason_busy", "delete_household_busy", "delete_outcome_unknown", "delete_timed_out", "injected", "P0001", "blanked"} {
				if strings.Contains(res.body, leak) {
					t.Errorf("the response carries %q: %s", leak, res.body)
				}
			}
			var u models.User
			if err := s.app.DB.Unscoped().First(&u, s.target.ID).Error; err != nil || u.DeletedAt.Valid {
				t.Errorf("the account was deleted by a failed attempt: %v", err)
			}
			if n := countAudit(t, s.app.DB, decisionActions...); n != 0 {
				t.Errorf("%d decision audits survived the rollback", n)
			}
			for _, r := range reqs {
				if reasonTextOf(t, s.app.DB, r.ID) == "" {
					t.Errorf("request %d was blanked by a rolled-back deletion", r.ID)
				}
			}
			// The attempt ended rolled back: it is closed, and settling says not applied.
			if again := deleteAccount(s.ac, s.target.ID, map[string]any{"attemptId": id, "reasonReview": review}); again.code != http.StatusConflict || !strings.Contains(again.body, "attempt_closed") {
				t.Errorf("resubmitting the id: %d %s, want 409 attempt_closed", again.code, again.body)
			}
			if got := settleOf(t, s.ac, id, s.target.ID); got.Result != settleNotApplied {
				t.Errorf("settle = %+v, want not_applied", got)
			}
		})
	}
}

// mintAttemptFor mints an account-delete attempt id as c's admin.
func mintAttemptFor(t *testing.T, c *apiClient, subject uint) string {
	t.Helper()
	w := c.do("POST", "/api/v1/admin/operation-attempts", map[string]any{"kind": attemptKindAccountDelete, "subjectUserId": subject}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("mint: %d %s", w.Code, w.Body.String())
	}
	var out attemptMintJSON
	decodeJSON(t, w, &out)
	return out.AttemptID
}

func settleOf(t *testing.T, c *apiClient, id string, subject uint) attemptSettleJSON {
	t.Helper()
	w := c.do("POST", "/api/v1/admin/operation-attempts/settle", map[string]any{"attemptId": id, "kind": attemptKindAccountDelete, "subjectUserId": subject}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("settle: %d %s", w.Code, w.Body.String())
	}
	var out attemptSettleJSON
	decodeJSON(t, w, &out)
	return out
}

// ---- a spent budget ---------------------------------------------------------

// The deletion's budget running out answers 503 delete_timed_out, never busy and
// never "nothing was deleted", rolls everything back, and the same review
// applies afterwards. The three places it can run out here: between streamed
// rows, between write batches, and while the stream waits on a row another
// connection holds.
func TestReasonSpentBudget(t *testing.T) {
	const budget, reserve = 3 * time.Second, time.Second
	setup := func(t *testing.T, n int) (reasonScene, []models.ContactRequest, map[string]any) {
		s := newReasonScene(t)
		var reqs []models.ContactRequest
		for i := 0; i < n; i++ {
			reqs = append(reqs, s.request(t, s.other, 90+i))
		}
		items := allPages(t, s.ac, s.target.ID)
		return s, reqs, reviewOf(items, func(reasonItemJSON) string { return "remove" })
	}
	refused := func(t *testing.T, s reasonScene, reqs []models.ContactRequest, res httpResult) {
		t.Helper()
		if res.code != http.StatusServiceUnavailable || !strings.Contains(res.body, `"code":"delete_timed_out"`) {
			t.Fatalf("delete: %d %s, want 503 delete_timed_out", res.code, res.body)
		}
		for _, wrong := range []string{"delete_reason_busy", "delete_household_busy", "delete_outcome_unknown"} {
			if strings.Contains(res.body, wrong) {
				t.Errorf("the response says %q: %s", wrong, res.body)
			}
		}
		var u models.User
		if err := s.app.DB.Unscoped().First(&u, s.target.ID).Error; err != nil || u.DeletedAt.Valid {
			t.Errorf("the account was deleted: %v", err)
		}
		if countAudit(t, s.app.DB, decisionActions...) != 0 {
			t.Error("a timed-out deletion left a decision audit")
		}
		for _, r := range reqs {
			if reasonTextOf(t, s.app.DB, r.ID) == "" {
				t.Errorf("request %d was blanked", r.ID)
			}
		}
	}
	// spend sleeps until less than the reserve is left of the deletion's budget.
	spend := func(started time.Time) {
		time.Sleep(time.Until(started.Add(budget - reserve/2)))
	}

	t.Run("between streamed rows", func(t *testing.T) {
		setAdminDeleteBounds(t, budget, reserve)
		s, reqs, review := setup(t, 4)
		prevEvery, prevHook := reasonGuardEvery, reasonRowHook
		reasonGuardEvery = 1
		started := time.Now()
		reasonRowHook = func(seen uint64) {
			if seen == 2 {
				spend(started)
			}
		}
		t.Cleanup(func() { reasonGuardEvery, reasonRowHook = prevEvery, prevHook })
		refused(t, s, reqs, deleteAccount(s.ac, s.target.ID, map[string]any{"reasonReview": review}))
		reasonRowHook = nil
		if res := deleteAccount(s.ac, s.target.ID, map[string]any{"reasonReview": review}); res.code != http.StatusOK {
			t.Fatalf("the same review afterwards: %d %s", res.code, res.body)
		}
	})

	t.Run("between write batches", func(t *testing.T) {
		setAdminDeleteBounds(t, budget, reserve)
		prevBatch := reasonBlankBatch
		reasonBlankBatch = 2
		t.Cleanup(func() { reasonBlankBatch = prevBatch })
		s, reqs, review := setup(t, 5)
		prevHook := reasonBatchHook
		started := time.Now()
		reasonBatchHook = func(batch int) {
			if batch == 1 {
				spend(started)
			}
		}
		t.Cleanup(func() { reasonBatchHook = prevHook })
		refused(t, s, reqs, deleteAccount(s.ac, s.target.ID, map[string]any{"reasonReview": review}))
		reasonBatchHook = nil
		if res := deleteAccount(s.ac, s.target.ID, map[string]any{"reasonReview": review}); res.code != http.StatusOK {
			t.Fatalf("the same review afterwards: %d %s", res.code, res.body)
		}
	})

	t.Run("while the stream waits on a row another connection holds", func(t *testing.T) {
		setAdminDeleteBounds(t, 2*time.Second, 500*time.Millisecond)
		s, reqs, review := setup(t, 3)
		hold, _ := beginOwnedTx(t, s.app.DB)
		if err := hold.Exec("SELECT id FROM contact_requests WHERE id = ? FOR UPDATE", reqs[1].ID).Error; err != nil {
			t.Fatal(err)
		}
		released := false
		release := func() {
			if !released {
				released = true
				hold.Rollback()
			}
		}
		defer release()
		refused(t, s, reqs, deleteAccount(s.ac, s.target.ID, map[string]any{"reasonReview": review}))
		release()
		setAdminDeleteBounds(t, 40*time.Second, 2*time.Second)
		if res := deleteAccount(s.ac, s.target.ID, map[string]any{"reasonReview": review}); res.code != http.StatusOK {
			t.Fatalf("after the row is free: %d %s", res.code, res.body)
		}
	})
}

// ---- the envelope and the attempt ----------------------------------------------

// The envelope's refusals never come before the submission check and never
// leave an attempt id open: a body that does not parse, or names attemptId
// twice, records nothing (the id still works); every refusal after the claim
// ends it rolled back (closed, and settling says not applied); an id that is
// not this deletion's is refused as such before the envelope is looked at.
func TestReasonEnvelopeAttemptOrdering(t *testing.T) {
	bad := `{"items":[],"x":"y"}`
	body := func(id, review string) json.RawMessage {
		return json.RawMessage(fmt.Sprintf(`{"attemptId":%q,"reasonReview":%s}`, id, review))
	}
	// An account with no reasons, as its own scene.
	scene := func(t *testing.T) (reasonScene, *models.User) {
		s := newReasonScene(t)
		return s, newTestUser(t, s.app, "attempt-subject", erasurePW, models.RoleMember)
	}

	t.Run("an attemptId named twice records nothing", func(t *testing.T) {
		s, target := scene(t)
		id := mintAttemptFor(t, s.ac, target.ID)
		for _, raw := range []string{
			fmt.Sprintf(`{"attemptId":%q,"attemptId":%q}`, id, id),
			fmt.Sprintf(`{"attemptid":%q}`, id),
			fmt.Sprintf(`{"attemptId":%q,"ATTEMPTID":""}`, id),
		} {
			res := deleteAccount(s.ac, target.ID, json.RawMessage(raw))
			if res.code != http.StatusBadRequest || !strings.Contains(res.body, "invalid body") {
				t.Fatalf("%s: %d %s, want 400 invalid body", raw, res.code, res.body)
			}
		}
		// The id was never claimed: it still runs, once.
		if res := deleteAccount(s.ac, target.ID, map[string]any{"attemptId": id}); res.code != http.StatusOK {
			t.Fatalf("the unclaimed id afterwards: %d %s", res.code, res.body)
		}
	})

	t.Run("a body that does not parse records nothing", func(t *testing.T) {
		s, target := scene(t)
		id := mintAttemptFor(t, s.ac, target.ID)
		res := deleteAccount(s.ac, target.ID, rawJSON(fmt.Sprintf(`{"attemptId":%q,`, id)))
		if res.code != http.StatusBadRequest || !strings.Contains(res.body, "invalid body") {
			t.Fatalf("%d %s, want 400 invalid body", res.code, res.body)
		}
		if res := deleteAccount(s.ac, target.ID, map[string]any{"attemptId": id}); res.code != http.StatusOK {
			t.Fatalf("the id afterwards: %d %s", res.code, res.body)
		}
	})

	for i, raw := range []func(id string) json.RawMessage{
		func(id string) json.RawMessage {
			return json.RawMessage(fmt.Sprintf(`{"attemptId":%q,"reasonReview":null,"reasonReview":{"items":[]}}`, id))
		},
		func(id string) json.RawMessage {
			return json.RawMessage(fmt.Sprintf(`{"attemptId":%q,"reasonreview":{"items":[]}}`, id))
		},
		func(id string) json.RawMessage { return body(id, bad) },
	} {
		t.Run(fmt.Sprintf("a malformed or ambiguous review (%d) ends the id rolled back", i), func(t *testing.T) {
			s, target := scene(t)
			id := mintAttemptFor(t, s.ac, target.ID)
			res := deleteAccount(s.ac, target.ID, raw(id))
			if res.code != http.StatusBadRequest || !strings.Contains(res.body, "invalid reason review") {
				t.Fatalf("%s: %d %s, want 400 invalid reason review", raw(id), res.code, res.body)
			}
			if again := deleteAccount(s.ac, target.ID, map[string]any{"attemptId": id}); again.code != http.StatusConflict || !strings.Contains(again.body, "attempt_closed") {
				t.Errorf("resubmitting: %d %s, want 409 attempt_closed", again.code, again.body)
			}
			if got := settleOf(t, s.ac, id, target.ID); got.Result != settleNotApplied {
				t.Errorf("settle = %+v, want not_applied", got)
			}
			var u models.User
			if err := s.app.DB.Unscoped().First(&u, target.ID).Error; err != nil || u.DeletedAt.Valid {
				t.Errorf("the account was deleted by a malformed review: %v", err)
			}
		})
	}

	for _, tc := range []struct {
		name, api string
		review    func(items []reasonItemJSON) any
	}{
		{"outdated", "reason_review_outdated", func(i []reasonItemJSON) any {
			return map[string]any{"items": []map[string]string{{"requestId": i[0].RequestID, "reviewHash": i[0].ReviewHash, "choice": "keep"}}}
		}},
		{"needs a review", "needs_reason_review", func([]reasonItemJSON) any { return nil }},
		{"changed", "reason_review_changed", func([]reasonItemJSON) any {
			return envelopeOf([]reviewEntry{{ID: 1, Hash: strings.Repeat("1", 64), Choice: "keep"}})
		}},
	} {
		t.Run("a review refused after the claim ends the id: "+tc.name, func(t *testing.T) {
			sc := newReasonScene(t)
			sc.request(t, sc.other, 1)
			items := allPages(t, sc.ac, sc.target.ID)
			id := mintAttemptFor(t, sc.ac, sc.target.ID)
			b := map[string]any{"attemptId": id}
			if r := tc.review(items); r != nil {
				b["reasonReview"] = r
			}
			res := deleteAccount(sc.ac, sc.target.ID, b)
			if res.code != http.StatusConflict || !strings.Contains(res.body, tc.api) {
				t.Fatalf("%d %s, want 409 %s", res.code, res.body, tc.api)
			}
			if again := deleteAccount(sc.ac, sc.target.ID, b); again.code != http.StatusConflict || !strings.Contains(again.body, "attempt_closed") {
				t.Errorf("resubmitted: %d %s, want attempt_closed", again.code, again.body)
			}
			if got := settleOf(t, sc.ac, id, sc.target.ID); got.Result != settleNotApplied {
				t.Errorf("settle = %+v", got)
			}
		})
	}

	// Keys written with JSON escapes: the same keys as the plain spellings, so the
	// same outcomes. Before the claim, a collision or a case variant of attemptId is
	// a body that does not parse and records nothing; after it, a collision or a
	// case variant of reasonReview ends the id rolled back. (jsonEsc turns "#u"
	// into a real backslash-u; see account_erasure_reasons_envelope_test.go.)
	t.Run("an attemptId written with an escape is the attemptId, and is spent by the delete", func(t *testing.T) {
		s, target := scene(t)
		id := mintAttemptFor(t, s.ac, target.ID)
		res := deleteAccount(s.ac, target.ID, rawJSON(jsonEsc(fmt.Sprintf(`{"attempt#u0049d":%q}`, id))))
		if res.code != http.StatusOK {
			t.Fatalf("%d %s, want 200: the escaped key is attemptId", res.code, res.body)
		}
		var u models.User
		if err := s.app.DB.Unscoped().First(&u, target.ID).Error; err != nil || !u.DeletedAt.Valid {
			t.Errorf("the account was not deleted: %v", err)
		}
		if got := settleOf(t, s.ac, id, target.ID); got.Result != settleApplied {
			t.Errorf("settle = %+v, want applied (the id was claimed)", got)
		}
		if again := deleteAccount(s.ac, target.ID, map[string]any{"attemptId": id}); again.code != http.StatusConflict {
			t.Errorf("replay: %d %s, want 409", again.code, again.body)
		}
	})

	for name, key := range map[string]string{
		"plain, then escaped":   `"attemptId":%[1]q,"attempt#u0049d":%[1]q`,
		"escaped, then plain":   `"attempt#u0049d":%[1]q,"attemptId":%[1]q`,
		"escaped twice":         `"attempt#u0049d":%[1]q,"attempt#u0049d":%[1]q`,
		"escaped two ways":      `"attempt#u0049d":%[1]q,"#u0061ttemptId":%[1]q`,
		"an escaped capital":    `"#u0041ttemptId":%[1]q`,
		"an escaped lower-case": `"attempt#u0069d":%[1]q`,
	} {
		t.Run("an attemptId named ambiguously through an escape records nothing: "+name, func(t *testing.T) {
			s, target := scene(t)
			id := mintAttemptFor(t, s.ac, target.ID)
			res := deleteAccount(s.ac, target.ID, rawJSON(jsonEsc("{"+fmt.Sprintf(key, id)+"}")))
			if res.code != http.StatusBadRequest || !strings.Contains(res.body, "invalid body") {
				t.Fatalf("%d %s, want 400 invalid body (before the claim)", res.code, res.body)
			}
			// Never claimed: the id still runs, once, for its own account.
			if res := deleteAccount(s.ac, target.ID, map[string]any{"attemptId": id}); res.code != http.StatusOK {
				t.Fatalf("the unclaimed id afterwards: %d %s", res.code, res.body)
			}
		})
	}

	t.Run("a reasonReview written with an escape is the review", func(t *testing.T) {
		sc := newReasonScene(t)
		r := sc.request(t, sc.other, 1)
		items := allPages(t, sc.ac, sc.target.ID)
		review, err := json.Marshal(reviewOf(items, func(reasonItemJSON) string { return "remove" }))
		if err != nil {
			t.Fatal(err)
		}
		id := mintAttemptFor(t, sc.ac, sc.target.ID)
		res := deleteAccount(sc.ac, sc.target.ID, rawJSON(jsonEsc(fmt.Sprintf(`{"attemptId":%q,"reasonRev#u0069ew":%s}`, id, review))))
		if res.code != http.StatusOK {
			t.Fatalf("%d %s, want 200: the escaped key is reasonReview", res.code, res.body)
		}
		if reasonTextOf(t, sc.app.DB, r.ID) != "" {
			t.Error("the removed reason was not blanked: the escaped key was not read as the review")
		}
	})

	for name, tail := range map[string]string{
		"plain, then escaped": `"reasonReview":null,"reason#u0052eview":{"items":[]}`,
		"escaped twice":       `"reasonRev#u0069ew":null,"reasonRev#u0069ew":{"items":[]}`,
		"escaped, then plain": `"reasonRev#u0069ew":{"items":[]},"reasonReview":null`,
		"an escaped capital":  `"#u0052easonReview":{"items":[]}`,
	} {
		t.Run("a reasonReview named ambiguously through an escape ends the id rolled back: "+name, func(t *testing.T) {
			s, target := scene(t)
			id := mintAttemptFor(t, s.ac, target.ID)
			res := deleteAccount(s.ac, target.ID, rawJSON(jsonEsc(fmt.Sprintf(`{"attemptId":%q,`, id)+tail+"}")))
			if res.code != http.StatusBadRequest || !strings.Contains(res.body, "invalid reason review") {
				t.Fatalf("%d %s, want 400 invalid reason review (after the claim)", res.code, res.body)
			}
			if again := deleteAccount(s.ac, target.ID, map[string]any{"attemptId": id}); again.code != http.StatusConflict || !strings.Contains(again.body, "attempt_closed") {
				t.Errorf("resubmitting: %d %s, want 409 attempt_closed", again.code, again.body)
			}
			if got := settleOf(t, s.ac, id, target.ID); got.Result != settleNotApplied {
				t.Errorf("settle = %+v, want not_applied", got)
			}
			var u models.User
			if err := s.app.DB.Unscoped().First(&u, target.ID).Error; err != nil || u.DeletedAt.Valid {
				t.Errorf("the account was deleted: %v", err)
			}
		})
	}

	t.Run("an id for another account is refused first, before the envelope is read", func(t *testing.T) {
		s, target := scene(t)
		other := newTestUser(t, s.app, "attempt-other", erasurePW, models.RoleMember)
		id := mintAttemptFor(t, s.ac, other.ID)
		res := deleteAccount(s.ac, target.ID, body(id, bad))
		if res.code != http.StatusConflict || !strings.Contains(res.body, "attempt_expired") {
			t.Fatalf("%d %s, want 409 attempt_expired (the submission check comes first)", res.code, res.body)
		}
		// And it was not spent: it still runs for its own account.
		if res := deleteAccount(s.ac, other.ID, map[string]any{"attemptId": id}); res.code != http.StatusOK {
			t.Fatalf("the id for its own account: %d %s", res.code, res.body)
		}
	})
}

// ---- the scanner over wide, signed and sparse tables -----------------------------

// walkPages walks the live pages from a null cursor and returns, per page, the
// number of items, the ids in order, and the cursor each page gave.
func walkPages(t *testing.T, s reasonScene) (sizes []int, ids []int64, cursors []string) {
	t.Helper()
	var after *string
	for i := 0; i < 500; i++ {
		code, page, body := pageOf(t, s.ac, s.target.ID, after)
		if code != http.StatusOK {
			t.Fatalf("page: %d %s", code, body)
		}
		sizes = append(sizes, len(page.Items))
		for _, it := range page.Items {
			id, ok := parseRequestID(it.RequestID)
			if !ok {
				t.Fatalf("item id %q", it.RequestID)
			}
			ids = append(ids, id)
		}
		if page.Next == nil {
			return sizes, ids, cursors
		}
		cursors = append(cursors, *page.Next)
		after = page.Next
	}
	t.Fatal("the walk never ended")
	return
}

// 101 and 200 matches inside one 50,000-row unit, at the approved page cap of
// 100 requests: a page holds a full 100, the next page starts after the 100th,
// and a table that ends exactly on the cap still ends with an empty page.
// Signed boundary ids sit among the matches, and every walk equals the
// reference query: nothing skipped, nothing twice.
func TestReasonScanWideUnitsAreExactAtTheCap(t *testing.T) {
	if reasonPageRequests != 100 || reasonUnitRows != 50000 {
		t.Fatalf("the approved bounds changed: %d requests, %d rows", reasonPageRequests, reasonUnitRows)
	}
	build := func(t *testing.T, matches int, signed bool) reasonScene {
		s := newReasonScene(t)
		tpl := s.contactRequest(t, 1)
		bulkRequests(t, s.app.DB, tpl.ID, matches-1)
		if signed {
			old := targetIDs(t, s.app.DB, s.target.ID)
			for i, to := range map[int]int64{0: -9223372036854775808, 1: -1, 2: 0, matches - 1: 9223372036854775807} {
				if err := s.app.DB.Exec("UPDATE contact_requests SET id = ? WHERE id = ?", to, old[i]).Error; err != nil {
					t.Fatal(err)
				}
			}
		}
		return s
	}
	for _, tc := range []struct {
		matches int
		signed  bool
		sizes   []int
	}{
		{101, false, []int{100, 1}},
		{200, false, []int{100, 100, 0}},
		{101, true, []int{100, 1}},
		{200, true, []int{100, 100, 0}},
		{99, false, []int{99}},
		{100, false, []int{100, 0}},
	} {
		t.Run(fmt.Sprintf("%d matches, signed boundaries %v", tc.matches, tc.signed), func(t *testing.T) {
			s := build(t, tc.matches, tc.signed)
			want := targetIDs(t, s.app.DB, s.target.ID)
			if len(want) != tc.matches {
				t.Fatalf("the fixture holds %d reasons, want %d", len(want), tc.matches)
			}
			sizes, got, cursors := walkPages(t, s)
			if fmt.Sprint(sizes) != fmt.Sprint(tc.sizes) {
				t.Errorf("page sizes %v, want %v", sizes, tc.sizes)
			}
			if fmt.Sprint(got) != fmt.Sprint(want) {
				t.Errorf("the walk returned %d ids that differ from the reference (%d)", len(got), len(want))
			}
			for i, c := range cursors {
				// A page that reached the cap names its 100th request as the cursor.
				if i < len(tc.sizes) && tc.sizes[i] == 100 && c != strconv.FormatInt(want[(i+1)*100-1], 10) {
					t.Errorf("cursor %d = %s, want the 100th id of its page %d", i, c, want[(i+1)*100-1])
				}
			}
		})
	}
	t.Run("an empty table", func(t *testing.T) {
		s := newReasonScene(t)
		sizes, ids, cursors := walkPages(t, s)
		if fmt.Sprint(sizes) != "[0]" || len(ids) != 0 || len(cursors) != 0 {
			t.Errorf("empty table walked as %v %v %v", sizes, ids, cursors)
		}
	})
}

// buildSparse makes a table of about 4,900 rows with the target's seven reasons
// spread through it and the first and last at the signed extremes.
func buildSparse(t *testing.T, s reasonScene) {
	t.Helper()
	tpl := s.fillerRequest(t)
	for i := 0; i < 7; i++ {
		bulkRequests(t, s.app.DB, tpl.ID, 700)
		s.contactRequest(t, 100+i)
	}
	ids := targetIDs(t, s.app.DB, s.target.ID)
	for from, to := range map[int64]int64{ids[0]: -9223372036854775808, ids[len(ids)-1]: 9223372036854775807} {
		if err := s.app.DB.Exec("UPDATE contact_requests SET id = ? WHERE id = ?", to, from).Error; err != nil {
			t.Fatal(err)
		}
	}
}

// A table many units long with matches far apart: the walk takes exactly as
// many pages as there are units (a unit with no match is an empty page with a
// cursor, never "no reasons"), finds every match once in signed order, and the
// pages that end a traversal are the only ones without a cursor.
func TestReasonScanSparseTableSpansUnits(t *testing.T) {
	prev := reasonUnitRows
	reasonUnitRows = 1000
	t.Cleanup(func() { reasonUnitRows = prev })
	s := newReasonScene(t)
	buildSparse(t, s)
	var rows int
	if err := s.app.DB.Raw("SELECT count(*) FROM contact_requests").Scan(&rows).Error; err != nil {
		t.Fatal(err)
	}
	want := targetIDs(t, s.app.DB, s.target.ID)
	sizes, got, cursors := walkPages(t, s)
	if fmt.Sprint(got) != fmt.Sprint(want) || len(want) != 7 {
		t.Fatalf("walk found %v, want %v", got, want)
	}
	if units := (rows + reasonUnitRows - 1) / reasonUnitRows; len(sizes) != units {
		t.Errorf("%d pages for %d rows in units of %d, want %d", len(sizes), rows, reasonUnitRows, units)
	}
	if len(cursors) != len(sizes)-1 {
		t.Errorf("%d cursors for %d pages: only the last page may end the walk", len(cursors), len(sizes))
	}
	empty := 0
	for _, n := range sizes {
		if n == 0 {
			empty++
		}
	}
	t.Logf("%d rows, %d pages, %d of them empty with a cursor", rows, len(sizes), empty)
}

// The same sparse table walked through scanReasonItems on one connection, more
// than six times (so the driver's statement may switch to a generic plan), in
// the server's own choice of plan and with generic plans forced. The units
// found equal the reference either way. pg_prepared_statements is logged so
// the record shows which plans ran.
func TestReasonScanSparseTableUnderGenericPlans(t *testing.T) {
	prev := reasonUnitRows
	reasonUnitRows = 300
	t.Cleanup(func() { reasonUnitRows = prev })
	s := newReasonScene(t)
	buildSparse(t, s)
	want := targetIDs(t, s.app.DB, s.target.ID)
	for _, mode := range []string{"auto", "force_generic_plan", "force_custom_plan"} {
		t.Run(mode, func(t *testing.T) {
			var got []int64
			units := 0
			err := s.app.DB.Transaction(func(tx *gorm.DB) error {
				if err := tx.Exec("SET LOCAL plan_cache_mode = " + mode).Error; err != nil {
					return err
				}
				var after *int64
				for {
					unit, err := scanReasonItems(tx, liveReasonFilter(int64(s.target.ID)), after, reasonPageRequests)
					if err != nil {
						return err
					}
					units++
					got = append(got, unit.Requests...)
					if unit.Next == nil {
						break
					}
					after = unit.Next
				}
				var plans []struct {
					GenericPlans, CustomPlans int64
				}
				if err := tx.Raw("SELECT generic_plans, custom_plans FROM pg_prepared_statements WHERE statement LIKE '%contact_requests WHERE id > %'").Scan(&plans).Error; err != nil {
					return err
				}
				t.Logf("%s: %d units; continuation statement plans (generic, custom): %+v", mode, units, plans)
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if units <= 6 {
				t.Fatalf("only %d units: the statement never ran more than six times", units)
			}
			if fmt.Sprint(got) != fmt.Sprint(want) {
				t.Errorf("walk found %v, want %v", got, want)
			}
		})
	}
}

// Under a generic plan, a continuation reads the same few index entries whether
// its cursor is near the start of the table or near the end, and never filters
// on id: the buffers of a unit do not grow with the cursor, and its index scan
// returns at most the unit. The statement that carried a NULL-or condition is
// planned the same way and logged for comparison.
func TestReasonScanPlanDoesNotGrowWithCursor(t *testing.T) {
	s := newReasonScene(t)
	bulkRequests(t, s.app.DB, s.fillerRequest(t).ID, 30000)
	if err := s.app.DB.Exec("ANALYZE contact_requests").Error; err != nil {
		t.Fatal(err)
	}
	var lo, hi int64
	if err := s.app.DB.Raw("SELECT min(id), max(id) FROM contact_requests").Row().Scan(&lo, &hi); err != nil {
		t.Fatal(err)
	}
	const unit = 1000
	dollar := func(sql string) string {
		n := 0
		for strings.Contains(sql, "?") {
			n++
			sql = strings.Replace(sql, "?", "$"+strconv.Itoa(n), 1)
		}
		return sql
	}
	plan := func(sql string, params string, args string) string {
		var out string
		err := s.app.DB.Transaction(func(tx *gorm.DB) error {
			for _, set := range []string{"enable_seqscan = off", "enable_bitmapscan = off", "plan_cache_mode = force_generic_plan"} {
				if err := tx.Exec("SET LOCAL " + set).Error; err != nil {
					return err
				}
			}
			if err := tx.Exec("PREPARE reason_scan_plan (" + params + ") AS " + dollar(sql)).Error; err != nil {
				return err
			}
			defer tx.Exec("DEALLOCATE reason_scan_plan")
			out = explainLines(t, tx, "EXECUTE reason_scan_plan("+args+")")
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	var buffers []int
	for _, cursor := range []int64{lo, lo + (hi-lo)/2, hi - 1500} {
		p := plan(reasonScanContinuationSQL, "bigint, integer", fmt.Sprintf("%d, %d", cursor, unit))
		if strings.Contains(p, "Sort") || strings.Contains(p, "Rows Removed by Filter") || !strings.Contains(p, "Index Cond: (id > $1)") {
			t.Errorf("cursor %d: the plan filters or sorts:\n%s", cursor, p)
		}
		if rows := planInt(t, planIndex, p, 1); rows > unit || rows == 0 {
			t.Errorf("cursor %d: the index scan returned %d rows for a unit of %d", cursor, rows, unit)
		}
		buffers = append(buffers, planInt(t, planBuffers, p, 1)+planInt(t, planBuffers, p, 2))
	}
	t.Logf("buffers per unit at cursors near the start, middle and end: %v", buffers)
	for _, b := range buffers[1:] {
		if b > buffers[0]*2+30 {
			t.Errorf("buffers grew with the cursor: %v", buffers)
		}
	}
	t.Logf("the old NULL-or statement, generic plan, middle cursor:\n%s", plan("SELECT id, requester_id, subject_user_id, (reason <> '') AS has_reason FROM contact_requests WHERE (?::bigint IS NULL OR id > ?::bigint) ORDER BY id LIMIT ?", "bigint, bigint, integer", fmt.Sprintf("%d, %d, %d", lo+(hi-lo)/2, lo+(hi-lo)/2, unit)))
}

// testReasonKey is reasonHashKey for tests, which never pass an empty secret.
func testReasonKey(secret []byte) []byte {
	k, err := reasonHashKey(secret)
	if err != nil {
		panic(err)
	}
	return k
}
