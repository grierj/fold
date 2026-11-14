package main

// The QC-1 harness (deploy/digitalocean/qc1-measure.sh) measures the real
// application, and what it needs to know about it is written down once, in
// deploy/digitalocean/qc1/app-facts.json. The harness reads that file; it never
// reads application source. This test holds the file's "go" section equal to
// the code (values, and the statements the application actually sends), so a
// refactor that moves or rewrites any of it fails here, beside the code, with
// the command that regenerates the file. The "web" section is held by
// web/src/components/ReasonReviewSection.test.tsx.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"

	"fold/internal/images"
	"fold/internal/policy"
	"fold/models"

	"gorm.io/gorm"
)

const qc1FactsFile = "deploy/digitalocean/qc1/app-facts.json"

// qc1PlanMarkers are the substrings of the application's statements that the
// harness looks for in the server's auto_explain log, one per statement kind,
// in the form the log shows ($n placeholders).
var qc1PlanMarkers = map[string]string{
	"delete-set":           "reason <> '' AND (requester_id = $1 OR subject_user_id = $2) ORDER BY contact_requests.id FOR UPDATE",
	"scanner-initial":      "FROM contact_requests ORDER BY id LIMIT $1",
	"scanner-continuation": "FROM contact_requests WHERE id > $1::bigint ORDER BY id LIMIT $2",
	"page-items":           "WHERE id = ANY($1::bigint[]) AND reason <> '' AND (requester_id = $2 OR subject_user_id = $3) ORDER BY contact_requests.id",
	"audit-report":         "SELECT class, action, count(*) FROM (SELECT action, ",
	"blank-batch":          "UPDATE contact_requests SET reason = '' WHERE id = ANY($1::bigint[]) AND reason <> '' AND (requester_id = $2 OR subject_user_id = $3)",
}

// qc1LegacyPlanMarkers are the substrings the legacy-only plan row (item 11) finds in the log: the statement that reads a batch's
// facts and the marker lookup. They are placeholder-free parts of legacyFactsSQL and legacyMarkersSQL; the matrix's plan row never
// looks for them, so they are kept apart from qc1PlanMarkers and merged only into the facts file.
var qc1LegacyPlanMarkers = map[string]string{
	"legacy-facts":   "LEFT JOIN users sub ON sub.id = cr.subject_user_id",
	"legacy-markers": "AS c(req, party, target_type, kept, removed)",
}

type qc1GoFacts struct {
	ContactLockID         string `json:"contactLockID"`
	MaxUploadBytes        int    `json:"maxUploadBytes"`
	MaxRequestBytes       int    `json:"maxRequestBytes"`
	ReasonUnitRows        int    `json:"reasonUnitRows"`
	ReasonScanBatchFirst  int    `json:"reasonScanBatchFirst"`
	ReasonScanBatchGrowth int    `json:"reasonScanBatchGrowth"`
	ScannerSQL            struct {
		Initial      string `json:"initial"`
		Continuation string `json:"continuation"`
	} `json:"scannerSQL"`
	PlanMarkers map[string]string `json:"planMarkers"`
	AuditReport struct {
		Pieces                  []string `json:"pieces"`
		SubjectMention          string   `json:"subjectMention"`
		MalformedConversionLink string   `json:"malformedConversionLink"`
		Successor               string   `json:"successor"`
	} `json:"auditReport"`
}

type qc1FactsFileShape struct {
	About string          `json:"about"`
	Go    qc1GoFacts      `json:"go"`
	Web   json.RawMessage `json:"web"`
}

// qc1CodeFacts reads the facts from the code itself.
func qc1CodeFacts(t *testing.T) qc1GoFacts {
	t.Helper()
	var f qc1GoFacts
	f.ContactLockID = fmt.Sprintf("0x%x", contactLockID)
	f.MaxUploadBytes = images.MaxUploadBytes
	f.MaxRequestBytes = maxRequestBytes
	f.ReasonUnitRows = reasonUnitRows
	f.ReasonScanBatchFirst = reasonScanBatchFirst
	f.ReasonScanBatchGrowth = reasonScanBatchGrowth
	f.ScannerSQL.Initial = reasonScanInitialSQL
	f.ScannerSQL.Continuation = reasonScanContinuationSQL
	f.PlanMarkers = map[string]string{}
	for _, m := range []map[string]string{qc1PlanMarkers, qc1LegacyPlanMarkers} {
		for k, v := range m {
			f.PlanMarkers[k] = v
		}
	}
	// The audit statement is classification (generated from the rule table)
	// between four fixed pieces around the two named constants.
	const sentinel = "\x00CLASSIFICATION\x00"
	sqlText := auditRetentionReportSQL(sentinel)
	head, rest, ok := strings.Cut(sqlText, sentinel)
	if !ok {
		t.Fatal("the audit report statement does not carry its classification")
	}
	p1, rest, ok1 := strings.Cut(rest, auditSubjectMention)
	p2, p3, ok2 := strings.Cut(rest, auditMalformedConversionLink)
	if !ok1 || !ok2 {
		t.Fatal("the audit report statement does not carry the subject mention and the malformed-link term in that order")
	}
	f.AuditReport.Pieces = []string{head, p1, p2, p3}
	f.AuditReport.SubjectMention = auditSubjectMention
	f.AuditReport.MalformedConversionLink = auditMalformedConversionLink
	// The erasure sends conversion discovery's first statement directly after the report, on the same
	// connection; the harness finds it in the log to tie the logged report to its deletion.
	f.AuditReport.Successor = strings.Replace(auditConversionRequestsSQL, "?", "$1", 1)
	return f
}

func TestQC1AppFacts(t *testing.T) {
	raw, err := os.ReadFile(qc1FactsFile)
	if err != nil {
		t.Fatal(err)
	}
	var file qc1FactsFileShape
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatalf("%s: %v", qc1FactsFile, err)
	}
	got := qc1CodeFacts(t)
	if os.Getenv("UPDATE_QC1_FACTS") == "1" {
		file.Go = got
		var out bytes.Buffer
		enc := json.NewEncoder(&out)
		enc.SetEscapeHTML(false)
		enc.SetIndent("", "  ")
		if err := enc.Encode(file); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(qc1FactsFile, out.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	if !reflect.DeepEqual(file.Go, got) {
		want, _ := json.MarshalIndent(got, "", "  ")
		have, _ := json.MarshalIndent(file.Go, "", "  ")
		t.Fatalf("%s \"go\" no longer matches the code. The QC-1 harness measures against these values: if the change is intended, run\n\n\tUPDATE_QC1_FACTS=1 go test -run TestQC1AppFacts .\n\nand review the diff.\ncode:\n%s\nfile:\n%s", qc1FactsFile, want, have)
	}
}

// numberNamed numbers the @name arguments of text the way GORM sends them
// ($n, one per occurrence), starting at start; it returns the text and the
// next number.
func numberNamed(text string, start int) (string, int) {
	n := start
	out := regexp.MustCompile(`@[a-z]+`).ReplaceAllStringFunc(text, func(string) string {
		s := fmt.Sprintf("$%d", n)
		n++
		return s
	})
	return out, n
}

// The harness finds each statement kind in the server log by its marker. A
// deletion with reasons, and the pages that precede it, send every one of
// them, and the audit report is the facts file's statement with its named
// arguments numbered.
func TestQC1PlanMarkersAreWhatTheApplicationSends(t *testing.T) {
	// One row a unit, so the walk continues from a cursor and sends the continuation statement.
	prevUnit := reasonUnitRows
	reasonUnitRows = 1
	t.Cleanup(func() { reasonUnitRows = prevUnit })
	s := newReasonScene(t)
	var mu sync.Mutex
	var sent []string
	capture := func(tx *gorm.DB) {
		mu.Lock()
		sent = append(sent, tx.Statement.SQL.String())
		mu.Unlock()
	}
	name := fmt.Sprintf("test:qc1_facts:%d", queryCounterSeq.Add(1))
	if err := s.app.DB.Callback().Raw().After("gorm:raw").Register(name, capture); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.app.DB.Callback().Raw().Remove(name) })
	if err := s.app.DB.Callback().Row().After("gorm:row").Register(name, capture); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.app.DB.Callback().Row().Remove(name) })

	s.request(t, s.other, 70)
	s.request(t, s.other, 71)
	items := allPages(t, s.ac, s.target.ID)
	if len(items) != 2 {
		t.Fatalf("%d items, want 2", len(items))
	}
	res := deleteAccount(s.ac, s.target.ID, map[string]any{"reasonReview": reviewOf(items, func(reasonItemJSON) string { return "remove" })})
	if res.code != http.StatusOK {
		t.Fatalf("delete: %d %s", res.code, res.body)
	}
	var blanked int64
	s.app.DB.Model(&models.ContactRequest{}).Where("reason <> ''").Count(&blanked)
	if blanked != 0 {
		t.Fatalf("%d reasons survived a review that removed them", blanked)
	}

	mu.Lock()
	defer mu.Unlock()
	collapsed := make([]string, len(sent))
	for i, q := range sent {
		collapsed[i] = strings.Join(strings.Fields(q), " ")
	}
	for kind, marker := range qc1PlanMarkers {
		found := false
		for _, q := range collapsed {
			if strings.Contains(q, marker) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("no statement the application sent contains the %s marker %q", kind, marker)
		}
	}

	facts := qc1CodeFacts(t).AuditReport
	mention, next := numberNamed(facts.SubjectMention, 1)
	link, _ := numberNamed(facts.MalformedConversionLink, next)
	tail := facts.Pieces[1] + mention + facts.Pieces[2] + link + facts.Pieces[3]
	found := false
	for _, q := range sent {
		if strings.HasPrefix(q, facts.Pieces[0]) && strings.HasSuffix(q, tail) {
			found = true
		}
	}
	if !found {
		t.Errorf("the audit report the application sent is not the facts file's statement with its arguments numbered:\n%s", tail)
	}
	// The harness ties a logged report to its deletion by the statement that follows it on
	// the same connection: conversion discovery's first query, text for text the facts file's.
	// This test sees every connection's statements, so it proves the order, not the adjacency;
	// eraseAuditDetailsIn runs nothing between the two.
	reportAt, successorAt := -1, -1
	for i, q := range sent {
		switch {
		case reportAt < 0 && strings.HasPrefix(q, facts.Pieces[0]) && strings.HasSuffix(q, tail):
			reportAt = i
		case reportAt >= 0 && successorAt < 0 && q == facts.Successor:
			successorAt = i
		}
	}
	if reportAt < 0 || successorAt < 0 {
		t.Errorf("the application did not send the facts file's successor statement after the audit report (report at %d, successor at %d):\n%s", reportAt, successorAt, facts.Successor)
	}
}

// The harness loads its background rows with deploy/digitalocean/qc1/filler.sql
// and scenario.sql. Each names the columns it inserts into contact_requests:
// every one must exist, and every column a row cannot do without (NOT NULL,
// no default) must be among them, so a migration that adds one fails here and
// not hours into a measurement. The list itself is pinned (qc1LoaderColumns), as
// the grep it replaces pinned it, so a column dropped from both files fails
// here, and so does a new column of the table that nobody has decided about.
// qc1LoaderColumns are the columns filler.sql and scenario.sql insert, in order.
// qc1LoaderLeftToDefaults are the other columns of contact_requests: nullable or
// defaulted, and not part of the rows the measurements read (denied group_add
// requests). A migration that adds a column to the table fails
// TestQC1LoaderColumnsFitTheSchema until the new column is in one of the two
// lists (and, for the first, in both SQL files).
var (
	qc1LoaderColumns = []string{"id", "requester_id", "subject_user_id", "context_type", "context_id", "fields", "reason", "needs_admin",
		"subject_decision", "subject_decided_by", "subject_decided_at", "admin_decision", "status", "created_at", "expires_at", "completed_at", "purpose"}
	qc1LoaderLeftToDefaults = []string{"add_attendance_mode", "add_childcare_ages_note", "add_childcare_count", "add_guest_count", "add_over_capacity", "add_over_limit", "admin_decided_at", "admin_decided_by", "completed_target_id", "end_reason", "occurrence_id", "withdrawn_by_id"}
)

func TestQC1LoaderColumnsFitTheSchema(t *testing.T) {
	app := testApp(t)
	var existing []string
	if err := app.DB.Raw(`SELECT column_name FROM information_schema.columns WHERE table_schema = 'public' AND table_name = 'contact_requests'`).Scan(&existing).Error; err != nil {
		t.Fatal(err)
	}
	var required []string
	if err := app.DB.Raw(`SELECT column_name FROM information_schema.columns WHERE table_schema = 'public' AND table_name = 'contact_requests'
		AND is_nullable = 'NO' AND column_default IS NULL AND is_generated = 'NEVER' AND is_identity = 'NO'`).Scan(&required).Error; err != nil {
		t.Fatal(err)
	}
	insert := regexp.MustCompile(`(?s)INSERT INTO contact_requests \(([^)]*)\)`)
	for _, file := range []string{"filler.sql", "scenario.sql"} {
		raw, err := os.ReadFile("deploy/digitalocean/qc1/" + file)
		if err != nil {
			t.Fatal(err)
		}
		m := insert.FindSubmatch(raw)
		if m == nil {
			t.Fatalf("%s has no INSERT INTO contact_requests (...)", file)
		}
		listed := map[string]bool{}
		for _, c := range strings.Split(string(m[1]), ",") {
			listed[strings.TrimSpace(c)] = true
		}
		var order []string
		for _, c := range strings.Split(string(m[1]), ",") {
			order = append(order, strings.TrimSpace(c))
		}
		if !reflect.DeepEqual(order, qc1LoaderColumns) {
			t.Errorf("%s inserts %v, not the pinned %v. If the loaders should change, change qc1LoaderColumns and both files together", file, order, qc1LoaderColumns)
		}
		have := map[string]bool{}
		for _, c := range existing {
			have[c] = true
		}
		for c := range listed {
			if !have[c] {
				t.Errorf("%s inserts into %q, which contact_requests does not have", file, c)
			}
		}
		for _, c := range required {
			if !listed[c] {
				t.Errorf("%s does not give %q, which contact_requests requires (NOT NULL, no default)", file, c)
			}
		}
	}
	known := map[string]bool{}
	for _, c := range append(append([]string{}, qc1LoaderColumns...), qc1LoaderLeftToDefaults...) {
		known[c] = true
	}
	for _, c := range existing {
		if !known[c] {
			t.Errorf("contact_requests has a column %q the QC-1 loaders have not decided about: add it to qc1LoaderColumns (and to both INSERT lists in deploy/digitalocean/qc1) or to qc1LoaderLeftToDefaults", c)
		}
	}
	for _, c := range qc1LoaderLeftToDefaults {
		if !slices.Contains(existing, c) {
			t.Errorf("qc1LoaderLeftToDefaults names %q, which contact_requests no longer has", c)
		}
	}
}

// The legacy-only plan row looks for these two statements in the server log, so the application must send them: one discovery page over a
// deleted account's reason sends the facts statement and the marker lookup, and TestQC1AppFacts holds the facts file equal to the merged map.
func TestQC1LegacyPlanMarkersAreWhatTheApplicationSends(t *testing.T) {
	s := newLegacyScene(t, 2)
	gone, live := s.users[0], s.users[1]
	s.bury(t, gone)
	s.askAdd(t, gone, live, 1)
	var mu sync.Mutex
	var sent []string
	capture := func(tx *gorm.DB) {
		mu.Lock()
		sent = append(sent, strings.Join(strings.Fields(tx.Statement.SQL.String()), " "))
		mu.Unlock()
	}
	name := fmt.Sprintf("test:qc1_legacy_facts:%d", queryCounterSeq.Add(1))
	if err := s.app.DB.Callback().Raw().After("gorm:raw").Register(name, capture); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.app.DB.Callback().Raw().Remove(name) })
	if err := s.app.DB.Callback().Row().After("gorm:row").Register(name, capture); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.app.DB.Callback().Row().Remove(name) })
	if code, body := s.discover(legacyDiscoveryPath); code != http.StatusOK || !strings.Contains(body, `"requestId"`) {
		t.Fatalf("discovery: %d %s, want one listed pair", code, body)
	}
	mu.Lock()
	defer mu.Unlock()
	for kind, marker := range qc1LegacyPlanMarkers {
		found := false
		for _, q := range sent {
			if strings.Contains(q, marker) {
				found = true
			}
		}
		if !found {
			t.Errorf("no statement a legacy discovery page sent contains the %s marker %q", kind, marker)
		}
	}
}

// qc1LegacyMarkersText is deploy/digitalocean/qc1/legacy-markers.sql as this test runs it: the psql variables substituted as psql would, and
// the one psql meta line it may carry dropped. Any other meta line fails the test, so the file cannot do anything the test does not run.
func qc1LegacyMarkersText(t *testing.T, token string, tombstone, only int64) string {
	t.Helper()
	raw, err := os.ReadFile("deploy/digitalocean/qc1/legacy-markers.sql")
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, line := range strings.Split(string(raw), "\n") {
		switch {
		case line == `\set ON_ERROR_STOP on`:
		case strings.HasPrefix(line, `\`):
			t.Fatalf("legacy-markers.sql has a psql meta line this test does not run: %q", line)
		default:
			lines = append(lines, line)
		}
	}
	if strings.ContainsAny(token, "'\\") {
		t.Fatalf("token %q is not a plain literal", token)
	}
	text := strings.NewReplacer(":'token'", "'"+token+"'", ":'tombstone'", fmt.Sprintf("'%d'", tombstone), ":'only_id'", fmt.Sprintf("'%d'", only)).Replace(strings.Join(lines, "\n"))
	if strings.Contains(text, ":'") {
		t.Fatalf("legacy-markers.sql has a psql variable this test does not set:\n%s", text)
	}
	return text
}

// qc1RunMarkers runs the file on one pinned connection and, when its guard refuses, ends the transaction it opened so the pool is not left in an aborted one.
func qc1RunMarkers(t *testing.T, db *gorm.DB, text string) error {
	t.Helper()
	return db.Connection(func(tx *gorm.DB) error {
		if err := tx.Exec(text).Error; err != nil {
			_ = tx.Exec("ROLLBACK").Error
			return err
		}
		return nil
	})
}

// The forged-marker file is the one piece of the harness that writes decisions. It must hide exactly the pairs it names from the application's
// own selector, write a marker shaped like the application's, and refuse everything but the harness's own disposable database: a sentinel written into
// that database and cluster by the harness, the QC-1 site, a soft-deleted account with the tombstone email, and only group_add requests. Nothing a real
// database could also hold (a domain, a site name) is enough, and the happy case below runs on users whose emails are NOT @example.invalid.
func TestQC1LegacyMarkersSQL(t *testing.T) {
	r := newSaveRig(t)
	db := r.direct
	live, gone, real := r.target("live"), r.target("gone"), r.target("real")
	const token = "11111111-2222-3333-4444-555555555555"
	exec := func(sql string, args ...any) {
		t.Helper()
		if err := db.Exec(sql, args...).Error; err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	count := func(sql string, args ...any) (n int) {
		t.Helper()
		if err := db.Raw(sql, args...).Scan(&n).Error; err != nil {
			t.Fatal(err)
		}
		return n
	}
	markers := func() int {
		return count(`SELECT count(*) FROM audit_logs WHERE action IN ('add.reason_kept','add.reason_removed')`)
	}
	harnessDB := func() {
		exec(`DROP TABLE IF EXISTS qc1_disposable`)
		exec(`CREATE TABLE qc1_disposable (token text PRIMARY KEY, system_identifier bigint NOT NULL)`)
		exec(`INSERT INTO qc1_disposable SELECT ?, system_identifier FROM pg_control_system()`, token)
		exec(`DELETE FROM settings WHERE key = 'site_name'`)
		exec(`INSERT INTO settings (key, value) VALUES ('site_name', 'QC-1')`)
	}
	t.Cleanup(func() { _ = db.Exec(`DROP TABLE IF EXISTS qc1_disposable`).Error })
	tombstone := func(u *models.User) {
		exec(`UPDATE users SET deleted_at = now(), email = 'deleted-' || id || '@deleted.invalid' WHERE id = ?`, u.ID)
	}
	tombstone(gone)
	reqA := r.askAdd(gone, live, 1) // the account as requester
	reqB := r.askAdd(live, gone, 2) // the account as subject
	reqC := r.askAdd(gone, live, 3) // a request the file is asked to skip with only_id
	cands := func(ids ...uint) []legacyCandidate {
		var out []legacyCandidate
		for _, id := range ids {
			out = append(out, legacyCandidate{legacyPair{int64(id), int64(gone.ID)}, requestKindOf(policy.RequestGroupAdd)})
		}
		return out
	}
	decided := func(ids ...uint) map[legacyPair]bool {
		t.Helper()
		got, err := legacyMarkersIn(db, cands(ids...))
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	refuse := func(why, needle string, token string, target int64, only int64) {
		t.Helper()
		before := markers()
		err := qc1RunMarkers(t, db, qc1LegacyMarkersText(t, token, target, only))
		if err == nil {
			t.Fatalf("%s: the file ran", why)
		}
		if !strings.Contains(err.Error(), needle) {
			t.Fatalf("%s: refused with %q, want a refusal naming %q", why, err, needle)
		}
		if markers() != before {
			t.Fatalf("%s: a refusal wrote %d marker(s)", why, markers()-before)
		}
		if n := count(`SELECT 1`); n != 1 {
			t.Fatalf("%s: the connection pool is unusable after the refusal", why)
		}
	}

	// A database that merely LOOKS like the harness's: QC-1 site, an admin at @example.invalid, only @example.invalid users and a real tombstone. No sentinel, no run.
	exec(`UPDATE users SET email = id || '-sim@example.invalid' WHERE deleted_at IS NULL`)
	exec(`UPDATE users SET email = 'admin@example.invalid' WHERE id = ?`, r.admin.ID)
	exec(`DELETE FROM settings WHERE key = 'site_name'`)
	exec(`INSERT INTO settings (key, value) VALUES ('site_name', 'QC-1')`)
	exec(`DROP TABLE IF EXISTS qc1_disposable`)
	refuse("a real-looking database with misleading fixture domains and no sentinel", "no qc1_disposable table", token, int64(gone.ID), 0)

	harnessDB()
	for _, c := range []struct{ why, needle, token string }{
		{"a wrong token", "token or cluster does not match", "99999999-2222-3333-4444-555555555555"},
	} {
		refuse(c.why, c.needle, c.token, int64(gone.ID), 0)
	}
	exec(`UPDATE qc1_disposable SET system_identifier = system_identifier + 1`)
	refuse("a sentinel from another cluster", "token or cluster does not match", token, int64(gone.ID), 0)
	harnessDB()
	exec(`INSERT INTO qc1_disposable VALUES ('another', 1)`)
	refuse("two sentinels", "token or cluster does not match", token, int64(gone.ID), 0)
	harnessDB()
	exec(`UPDATE settings SET value = 'Grace Church' WHERE key = 'site_name'`)
	refuse("another site", "the site is not QC-1", token, int64(gone.ID), 0)
	harnessDB()
	refuse("a live account", "not a soft-deleted account with the tombstone email", token, int64(live.ID), 0)
	exec(`UPDATE users SET email = 'deleted-' || id || '@deleted.invalid' WHERE id = ?`, live.ID)
	refuse("a live account whose email only looks like a tombstone", "not a soft-deleted account with the tombstone email", token, int64(live.ID), 0)
	exec(`UPDATE users SET deleted_at = now() WHERE id = ?`, real.ID)
	refuse("a deleted account whose email is not a tombstone", "not a soft-deleted account with the tombstone email", token, int64(real.ID), 0)
	contact := r.ask(gone, live, 4) // a contact-purpose request with a reason: refused, never guessed
	refuse("a contact-purpose request of the account", "only group_add requests are marked", token, int64(gone.ID), 0)
	exec(`DELETE FROM contact_requests WHERE id = ?`, contact.ID)
	refuse("a request with no reason to mark", "nothing to mark", token, int64(gone.ID), int64(reqA.ID+1000))
	refuse("a negative only_id", "tombstone must be positive", token, int64(gone.ID), -1)

	// The happy case: the forged marker hides exactly the pair it names, the application's selector decides it, and its shape is the application's own.
	if got := decided(reqA.ID, reqB.ID, reqC.ID); len(got) != 0 {
		t.Fatalf("pairs decided before any marker: %v", got)
	}
	before, _ := legacyWalk(t, db, 0, 100)
	if len(before) != 3 {
		t.Fatalf("%d pairs listed before the markers, want 3", len(before))
	}
	if err := qc1RunMarkers(t, db, qc1LegacyMarkersText(t, token, int64(gone.ID), int64(reqC.ID))); err != nil {
		t.Fatalf("only_id: %v", err)
	}
	if got := decided(reqA.ID, reqB.ID, reqC.ID); len(got) != 1 || !got[legacyPair{int64(reqC.ID), int64(gone.ID)}] {
		t.Fatalf("only_id marked %v, want exactly the one request it names", got)
	}
	if err := qc1RunMarkers(t, db, qc1LegacyMarkersText(t, token, int64(gone.ID), 0)); err != nil {
		t.Fatalf("all: %v", err)
	}
	if got := decided(reqA.ID, reqB.ID, reqC.ID); len(got) != 3 {
		t.Fatalf("after marking everything %d pairs are decided, want 3", len(got))
	}
	if items, _ := legacyWalk(t, db, 0, 100); len(items) != 0 {
		t.Fatalf("%v still listed after every pair was marked", items)
	}
	// The same key set and types as a marker the application writes for a real save.
	reqD := r.askAdd(real, live, 5)
	items := r.items(real.ID)
	if w := func() int { _, w := r.save(real.ID, choose(items, "keep")); return w.Code }(); w != http.StatusOK {
		t.Fatalf("a real save answered %d", w)
	}
	shape := func(request uint, forged bool) string {
		var s string
		q := `SELECT target_type || '|' || action || '|' || (SELECT string_agg(k, ',' ORDER BY k) FROM jsonb_object_keys(meta_json) k) || '|' || jsonb_typeof(meta_json->'erasedUserId') || '|' || jsonb_typeof(meta_json->'reviewVersion') FROM audit_logs WHERE target_id = ? AND action = 'add.reason_kept' ORDER BY id DESC LIMIT 1`
		if err := db.Raw(q, request).Scan(&s).Error; err != nil {
			t.Fatal(err)
		}
		return s
	}
	if app, forged := shape(reqD.ID, false), shape(reqA.ID, true); app == "" || app != forged {
		t.Fatalf("the forged marker's shape %q is not the application's %q", forged, app)
	}
}
