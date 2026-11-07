package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
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
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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

func TestReviewHashVectors(t *testing.T) {
	const secret = "fold-test-secret"
	key := reasonHashKey([]byte(secret))
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
	c := reviewHashOf(reasonHashKey([]byte("another secret")), 1, 2, hashFields(map[string]any{"reason": ""}))
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

func TestParseReasonReview(t *testing.T) {
	h := strings.Repeat("ab", 32)
	item := func(id, hash, choice string) reasonChoiceBody {
		return reasonChoiceBody{RequestID: id, ReviewHash: hash, Choice: choice}
	}
	if m, ok := parseReasonReview(nil); !ok || m != nil {
		t.Errorf("a missing review must parse to nil, got %v %v", m, ok)
	}
	m, ok := parseReasonReview(&reasonReviewBody{Items: []reasonChoiceBody{item("-5", h, "remove"), item("9223372036854775807", h, "keep")}})
	if !ok || len(m) != 2 || !m[-5].remove || m[9223372036854775807].remove {
		t.Errorf("a good review parsed to %v %v", m, ok)
	}
	for name, items := range map[string][]reasonChoiceBody{
		"duplicate":        {item("1", h, "keep"), item("1", h, "remove")},
		"non-canonical id": {item("01", h, "keep")},
		"plus id":          {item("+1", h, "keep")},
		"out of range id":  {item("9223372036854775808", h, "keep")},
		"short hash":       {item("1", h[:62], "keep")},
		"upper-case hash":  {item("1", strings.ToUpper(h), "keep")},
		"no choice":        {item("1", h, "")},
		"another choice":   {item("1", h, "blank")},
	} {
		if _, ok := parseReasonReview(&reasonReviewBody{Items: items}); ok {
			t.Errorf("%s was accepted", name)
		}
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
	if err := scanReasonRows(rows, reasonHashKey([]byte("k")), 1, 2, func(rr reasonRow) error { seen = append(seen, rr); return nil }); err != nil {
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

func reviewOf(items []reasonItemJSON, choice func(reasonItemJSON) string) map[string]any {
	var out []map[string]string
	for _, it := range items {
		out = append(out, map[string]string{"requestId": it.RequestID, "reviewHash": it.ReviewHash, "choice": choice(it)})
	}
	return map[string]any{"items": out}
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
	if code, page, _ := pageOf(t, s.ac, s.target.ID, nil); code != http.StatusOK || len(page.Items) != 1 {
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
	if res := deleteAccount(s.ac, s.target.ID, map[string]any{"reasonReview": map[string]any{"items": []any{}}}); res.code != http.StatusForbidden {
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
		unit, err = scanReasonItems(tx, liveReasonFilter(int64(s.target.ID)), nil)
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
			r := reviewOf(p.items, keepAll)
			r["items"] = append(r["items"].([]map[string]string), map[string]string{"requestId": "999999", "reviewHash": strings.Repeat("0", 64), "choice": "keep"})
			return r
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
			rows, err := p.s.app.DB.Raw("SELECT "+reasonSelect+" FROM contact_requests WHERE reason <> '' AND (requester_id = ? OR subject_user_id = ?) ORDER BY id", p.s.target.ID, p.s.target.ID).Rows()
			if err != nil {
				t.Fatal(err)
			}
			var items []reasonItemJSON
			if err := scanReasonRows(rows, reasonHashKey(p.s.app.Config.SessionSecret), int64(p.s.admin.ID), int64(p.s.other.ID), func(r reasonRow) error {
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
// the first page).
func unitCursor(t *testing.T, st reasonStatement) *int64 {
	t.Helper()
	if len(st.Vars) != 3 {
		t.Fatalf("a scanner unit binds %d values, want after, after and the limit: %v", len(st.Vars), st.Vars)
	}
	switch v := st.Vars[0].(type) {
	case nil:
		return nil
	case *int64:
		return v
	case int64:
		return &v
	default:
		t.Fatalf("cursor bound as %T", st.Vars[0])
		return nil
	}
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
	fset := token.NewFileSet()
	writeRe := regexp.MustCompile(`(?i)\b(insert into|update|delete from)\s+(public\.)?contact_requests\b`)
	lockRe := regexp.MustCompile(`(?i)\bfor (no key update|update|share|key share)\b`)
	readAuditRe := regexp.MustCompile(`(?i)\bfrom\s+(public\.)?audit_logs\b|join\s+audit_logs\b`)
	paths := make([]string, 0, len(sources))
	for p := range sources {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, path := range paths {
		if path == "internal/audit/audit.go" {
			continue // the writers; they read nothing
		}
		file, err := parser.ParseFile(fset, path, sources[path], 0)
		if err != nil {
			continue
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
					flat := strings.Join(strings.Fields(lit), " ")
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
	sources := map[string]string{}
	if err := walkProductionGo(".", func(path string, src []byte) error {
		sources[path] = string(src)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
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
