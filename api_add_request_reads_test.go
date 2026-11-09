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
	// The rows that name an occurrence carry its plain YYYY-MM-DD. The
	// fixture's series runs at 19:00 UTC, so each start time names its date.
	allowed := map[string]bool{f.dates[0].StartsAt.UTC().Format("2006-01-02"): true, f.dates[2].StartsAt.UTC().Format("2006-01-02"): true}
	named := 0
	for _, row := range got {
		if row.Purpose != policy.RequestOccurrenceAdd {
			continue
		}
		named++
		if !allowed[row.Date] || row.StartsAt == nil || row.Date != row.StartsAt.UTC().Format("2006-01-02") {
			t.Fatalf("add request date = %q (startsAt %v), want one of %v", row.Date, row.StartsAt, allowed)
		}
	}
	if named != 80 {
		t.Fatalf("checked %d occurrence add request dates, want 80", named)
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
			single, err := f.app.presentAddRequest(f.app.DB, op, r)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(batch[i], single) {
				t.Fatalf("row %d batch %+v page of one %+v", r.ID, batch[i], single)
			}
			scalar, err := f.app.scalarAddRequest(f.app.DB, op, r)
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

// scalarAddRequest is the test-only scalar presenter, kept as the page loader's
// parity oracle: it decides from loadAddFacts, the loader the finalizers use,
// one request at a time, so the page's batched facts are checked against them.
func (a *App) scalarAddRequest(tx *gorm.DB, op *contactOperation, r models.ContactRequest) (addRequestJSON, error) {
	f, err := a.loadAddFacts(tx, r, op.now)
	if err != nil {
		return addRequestJSON{}, err
	}
	p, actor := addRequestPolicy(r), contactAccount(op.actor, op.now, op.zone)
	out := addRequestJSON{ID: r.ID, Purpose: r.Purpose, Reason: r.Reason, OverLimit: r.AddOverLimit, NeedsAdmin: r.NeedsAdmin, SubjectDecision: r.SubjectDecision, AdminDecision: r.AdminDecision, Status: r.Status, CreatedAt: r.CreatedAt, ExpiresAt: r.ExpiresAt, CompletedAt: r.CompletedAt, EndReason: r.EndReason,
		CanSubjectDecide: policy.AddRequestStatusAfterDecision(actor, policy.ContactSubject, policy.ContactApproved, p, f) != "", CanAdminDecide: policy.AddRequestStatusAfterDecision(actor, policy.ContactAdmin, policy.ContactApproved, p, f) != "", CanWithdraw: policy.CanWithdrawAddRequest(actor, p, op.now), CanComplete: policy.CanViewAddRequest(actor, p) && policy.CanFinalizeAddRequest(p, f)}
	if r.Purpose == policy.RequestOccurrenceAdd {
		out.Reason = ""
		out.CompletionProblem = op.completionProblem
		var ev models.Event
		var occ models.EventOccurrence
		if err := tx.Where("id = ?", r.ContextID).Limit(1).Find(&ev).Error; err != nil {
			return out, err
		}
		if err := tx.Where("id = ? AND event_id = ?", r.OccurrenceID, r.ContextID).Limit(1).Find(&occ).Error; err != nil {
			return out, err
		}
		if ev.ID != 0 && occ.ID != 0 {
			out.EventTitle = ev.Title
			if occ.TitleOverride != nil {
				out.EventTitle = *occ.TitleOverride
			}
			out.Date, out.StartsAt, out.EndsAt, out.TZID = models.FormatCalendarDate(occ.OriginalDate), &occ.StartsAt, &occ.EndsAt, ev.TZID
		}
		return out, nil
	}
	out.ContextID, err = a.scalarContextVisible(tx, op.actor, r)
	if err != nil {
		return out, err
	}
	if out.ContextID != nil {
		var g models.Group
		if err = tx.First(&g, *out.ContextID).Error; err != nil {
			return out, err
		}
		if shown := a.groupShownTo(op.actor, &g, relIn(tx, op.actor.ID, g.ID)); shown != nil {
			out.GroupName = shown.Name
		} // group-ok: groupShownTo applies current viewer visibility
		out.RequesterName, out.SubjectName = nameIn(tx, r.RequesterID), nameIn(tx, r.SubjectUserID)
	}
	return out, nil
}
