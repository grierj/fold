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
	"strings"
	"sync"
	"testing"

	"fold/internal/images"
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
	f.PlanMarkers = qc1PlanMarkers
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
}

// The harness loads its background rows with deploy/digitalocean/qc1/filler.sql
// and scenario.sql. Each names the columns it inserts into contact_requests:
// every one must exist, and every column a row cannot do without (NOT NULL,
// no default) must be among them, so a migration that adds one fails here and
// not hours into a measurement.
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
}
