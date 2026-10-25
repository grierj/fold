package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"fold/internal/policy"
	"fold/models"
	"gorm.io/gorm"
	"reflect"
	"testing"
	"time"
)

func collectionReadCounter(t *testing.T, db *gorm.DB) *int {
	t.Helper()
	n := new(int)
	for _, kind := range []string{"query", "row", "raw"} {
		name := "collection:" + kind
		cb := func(*gorm.DB) { *n++ }
		switch kind {
		case "query":
			_ = db.Callback().Query().Before("gorm:query").Register(name, cb)
			t.Cleanup(func() { _ = db.Callback().Query().Remove(name) })
		case "row":
			_ = db.Callback().Row().Before("gorm:row").Register(name, cb)
			t.Cleanup(func() { _ = db.Callback().Row().Remove(name) })
		case "raw":
			_ = db.Callback().Raw().Before("gorm:raw").Register(name, cb)
			t.Cleanup(func() { _ = db.Callback().Raw().Remove(name) })
		}
	}
	return n
}

func TestAddRequestPageReadBudgetAndScalarParity(t *testing.T) {
	f := newOccurrenceReadFixture(t)
	teen := newTestUser(t, f.app, "page teen", "password1234", models.RoleMember)
	birth := time.Now().AddDate(-15, 0, 0)
	if err := f.app.DB.Model(teen).Updates(map[string]any{"account_type": models.AccountTeen, "birth_date": birth}).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.app.DB.First(teen, teen.ID).Error; err != nil {
		t.Fatal(err)
	}
	// Pending requests are deliberately seeded, not finalized by this read test.
	now := time.Now().UTC().Truncate(time.Microsecond)
	base := models.ContactRequest{Purpose: policy.RequestOccurrenceAdd, RequesterID: f.issuer.ID, SubjectUserID: f.other.ID, ContextType: policy.ContactEvent, ContextID: f.event.ID, OccurrenceID: &f.dates[0].ID, AddAttendanceMode: models.AttendanceInPerson, SubjectDecision: policy.ContactPending, AdminDecision: policy.ContactNotNeeded, Status: policy.ContactPending, CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
	if err := f.app.DB.Create(&base).Error; err != nil {
		t.Fatal(err)
	}
	n := collectionReadCounter(t, f.app.DB)
	read := func() []addRequestJSON {
		t.Helper()
		*n = 0
		w := f.admin.do("GET", "/api/v1/add-requests", nil, false)
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		var out struct {
			Requests []addRequestJSON `json:"requests"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out.Requests
	}
	read()
	small := *n
	for i := 0; i < 99; i++ {
		r := base
		r.ID = 0 // Vary requester, subject, purpose and destination within one admin page.
		if i%2 == 0 {
			r.OccurrenceID = &f.dates[2].ID
		}
		if i%3 == 0 {
			r.RequesterID = f.other.ID
			r.SubjectUserID = teen.ID
			r.NeedsAdmin = true
			r.AdminDecision = policy.ContactPending
		}
		if i%5 == 0 {
			r.Purpose = policy.RequestGroupAdd
			r.ContextType = policy.ContactGroup
			r.ContextID = f.group
			r.OccurrenceID = nil
			r.AddAttendanceMode = ""
		}
		if err := f.app.DB.Create(&r).Error; err != nil {
			t.Fatal(err)
		}
	}
	got := read()
	many := *n
	if len(got) != 100 || many > small+3 || many > 20 {
		t.Fatalf("pending rows %d reads one=%d many=%d (budget20)", len(got), small, many)
	}
	// Compare every DTO with the existing scalar presenter under the same clock.
	op := &contactOperation{actor: f.issuer, now: now, zone: time.UTC}
	var rs []models.ContactRequest
	if err := f.app.DB.Where("purpose IN ?", []string{policy.RequestGroupAdd, policy.RequestOccurrenceAdd}).Order("id DESC").Limit(100).Find(&rs).Error; err != nil {
		t.Fatal(err)
	}
	*n = 0
	if _, err := f.app.presentAddRequests(f.app.DB, op, []models.ContactRequest{base}); err != nil {
		t.Fatal(err)
	}
	pendingOneFacts := *n
	*n = 0
	if _, err := f.app.presentAddRequests(f.app.DB, op, rs); err != nil {
		t.Fatal(err)
	}
	pendingManyFacts := *n
	if pendingManyFacts > pendingOneFacts+3 || pendingManyFacts > 11 {
		t.Fatalf("pending batch reads 1=%d 100=%d", pendingOneFacts, pendingManyFacts)
	}
	t.Logf("pending page fact reads 1=%d mixed100=%d", pendingOneFacts, pendingManyFacts)
	compare := func() {
		t.Helper()
		batch, err := f.app.presentAddRequests(f.app.DB, op, rs)
		if err != nil {
			t.Fatal(err)
		}
		for i, r := range rs {
			scalar, err := f.app.presentAddRequest(f.app.DB, op, r)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(batch[i], scalar) {
				t.Fatalf("row %d batch %+v scalar %+v", r.ID, batch[i], scalar)
			}
		}
	}
	compare()
	// The same history exposes no hidden group/name context to a nonmember.
	op.actor = f.recipient
	compare()
	op.actor = f.issuer
	if err := f.app.DB.Create(&models.TeenConsent{TeenUserID: teen.ID, Scope: models.TeenConsentAccount, Status: models.ConsentGranted, GrantedBy: f.issuer.ID, GranterRole: "admin", EvidenceText: "verified consent", At: now.Add(-time.Minute)}).Error; err != nil {
		t.Fatal(err)
	}
	compare()
	if err := f.app.DB.Create(&models.ManagerAddBlock{SubjectUserID: teen.ID, OrganizerType: policy.ContactGroup, OrganizerID: f.group}).Error; err != nil {
		t.Fatal(err)
	}
	compare()
	// Fresh role loss, subject consent and block state cannot reuse page facts.
	if err := f.app.DB.Model(f.other).Update("status", models.UserDisabled).Error; err != nil {
		t.Fatal(err)
	}
	compare()
	if err := f.app.DB.Model(&models.ContactRequest{}).Where("status = ?", policy.ContactPending).Updates(map[string]any{"status": policy.ContactWithdrawn, "completed_at": now, "withdrawn_by_id": gorm.Expr("requester_id")}).Error; err != nil {
		t.Fatal(err)
	}
	terminal := read()
	terminalMany := *n
	if len(terminal) != 100 || terminalMany > 12 {
		t.Fatalf("terminal rows %d reads=%d budget12", len(terminal), terminalMany)
	}
	for _, r := range terminal {
		if r.CanAdminDecide || r.CanSubjectDecide || r.CanComplete || r.CanWithdraw {
			t.Fatal("terminal capabilities", r)
		}
	}
	// One terminal page of the same purposes must have the same bounded read set.
	*n = 0
	w := f.admin.do("GET", fmt.Sprintf("/api/v1/add-requests?before=%d", base.ID), nil, false)
	terminalSmall := *n
	if terminalMany > terminalSmall+3 {
		t.Fatalf("terminal read growth one=%d mixed100=%d", terminalSmall, terminalMany)
	}
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	t.Logf("pending one=%d mixed100=%d terminal1=%d mixed100=%d", small, many, terminalSmall, terminalMany)
	// A failed fact batch must fail closed, not produce permissive empty facts.
	_ = f.app.DB.Callback().Query().Before("gorm:query").Register("page:fail", func(db *gorm.DB) {
		if db.Statement.Table == "event_occurrences" {
			db.AddError(errors.New("page read failed"))
		}
	})
	defer f.app.DB.Callback().Query().Remove("page:fail")
	if w := f.admin.do("GET", "/api/v1/add-requests", nil, false); w.Code != 500 {
		t.Fatal("read failure", w.Code, w.Body.String())
	}
}
