package main

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"fold/internal/policy"
	"fold/models"

	"gorm.io/gorm"
)

type contactReadFixture struct {
	*contactAPIFixture
	now                     time.Time
	groupEvent, churchEvent models.Event
	groupOcc, churchOcc     models.EventOccurrence
	requests                []models.ContactRequest
	grants                  []models.ContactGrant
}

func newContactReadFixture(t *testing.T) *contactReadFixture {
	t.Helper()
	f := &contactReadFixture{contactAPIFixture: newContactAPIFixture(t, true), now: time.Now().UTC().Truncate(time.Microsecond)}
	if err := f.app.DB.Model(f.group).Update("visibility", models.VisibilityInviteOnly).Error; err != nil {
		t.Fatal(err)
	}
	f.group.Visibility = models.VisibilityInviteOnly
	f.groupEvent, f.groupOcc = seedEvent(t, f.app, f.group, f.leader.ID, f.now.Add(-policy.ContactEventGrace-2*time.Hour))
	f.churchEvent, f.churchOcc = seedEvent(t, f.app, f.group, f.admin.ID, f.now.Add(48*time.Hour))
	if err := f.app.DB.Model(&f.churchEvent).Updates(map[string]any{"group_id": nil, "visibility": models.VisibilityPrivate}).Error; err != nil {
		t.Fatal(err)
	}
	f.churchEvent.GroupID, f.churchEvent.Visibility = nil, models.VisibilityPrivate
	for _, h := range []models.EventHost{
		{EventID: f.groupEvent.ID, UserID: f.leader.ID, AddedBy: f.admin.ID}, // A host row never widens a group event.
		{EventID: f.churchEvent.ID, UserID: f.leader.ID, AddedBy: f.admin.ID},
	} {
		if err := f.app.DB.Create(&h).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, occ := range []models.EventOccurrence{f.groupOcc, f.churchOcc} {
		if err := f.app.DB.Create(&models.Registration{UserID: &f.subject.ID, OccurrenceID: occ.ID, Status: models.RegGoing}).Error; err != nil {
			t.Fatal(err)
		}
	}
	public := models.VisibilityPublic
	if err := f.app.DB.Model(&f.groupOcc).Update("visibility_override", public).Error; err != nil {
		t.Fatal(err)
	}
	cancelled := models.EventOccurrence{EventID: f.groupEvent.ID, OriginalDate: f.now.AddDate(0, 1, 0).Format("2006-01-02"), StartsAt: f.now.AddDate(0, 1, 0), EndsAt: f.now.AddDate(0, 1, 0).Add(time.Hour), Status: models.OccurrenceCancelled}
	if err := f.app.DB.Create(&cancelled).Error; err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *contactReadFixture) seed(t *testing.T, total int, grants bool) {
	t.Helper()
	contexts := []struct {
		typ string
		id  uint
	}{{policy.ContactGroup, f.group.ID}, {policy.ContactEvent, f.groupEvent.ID}, {policy.ContactEvent, f.churchEvent.ID}}
	for len(f.requests) < total {
		c := contexts[len(f.requests)%len(contexts)]
		r := models.ContactRequest{Purpose: policy.RequestContact, RequesterID: f.leader.ID, SubjectUserID: f.subject.ID, ContextType: c.typ, ContextID: c.id, Fields: policy.ContactBoth, Reason: "private safeguarding reason", NeedsAdmin: true, SubjectDecision: policy.ContactPending, AdminDecision: policy.ContactPending, Status: policy.ContactPending, CreatedAt: f.now, ExpiresAt: f.now.Add(24 * time.Hour)}
		if grants {
			r.SubjectDecision, r.AdminDecision, r.Status = policy.ContactApproved, policy.ContactApproved, policy.ContactGranted
			r.SubjectDecidedBy, r.AdminDecidedBy = &f.subject.ID, &f.admin.ID
			r.SubjectDecidedAt, r.AdminDecidedAt, r.CompletedAt = &f.now, &f.now, &f.now
		}
		if err := f.app.DB.Create(&r).Error; err != nil {
			t.Fatal(err)
		}
		f.requests = append(f.requests, r)
		if grants {
			g := models.ContactGrant{RequestID: r.ID, LeaderID: r.RequesterID, SubjectUserID: r.SubjectUserID, ContextType: r.ContextType, ContextID: r.ContextID, Fields: r.Fields, GrantedAt: *r.CompletedAt}
			if err := f.app.DB.Create(&g).Error; err != nil {
				t.Fatal(err)
			}
			f.grants = append(f.grants, g)
		}
	}
}

func requestPage(t *testing.T, c *apiClient, suffix string) ([]contactRequestJSON, string) {
	t.Helper()
	w := c.do("GET", "/api/v1/contact-requests"+suffix, nil, false)
	if w.Code != 200 {
		t.Fatalf("requests: %d %s", w.Code, w.Body.String())
	}
	var body struct {
		Requests []contactRequestJSON `json:"requests"`
	}
	decodeJSON(t, w, &body)
	return body.Requests, w.Body.String()
}

func grantPage(t *testing.T, c *apiClient, suffix string) ([]contactGrantJSON, string) {
	t.Helper()
	w := c.do("GET", "/api/v1/contact-grants"+suffix, nil, false)
	if w.Code != 200 {
		t.Fatalf("grants: %d %s", w.Code, w.Body.String())
	}
	var body struct {
		Grants []contactGrantJSON `json:"grants"`
	}
	decodeJSON(t, w, &body)
	return body.Grants, w.Body.String()
}

func assertContactPageIDs[T interface {
	contactRequestJSON | contactGrantJSON
}](t *testing.T, rows []T, want []uint) {
	t.Helper()
	got := make([]uint, len(rows))
	for i, row := range rows {
		switch v := any(row).(type) {
		case contactRequestJSON:
			got[i] = v.ID
		case contactGrantJSON:
			got[i] = v.ID
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("IDs %v want %v", got, want)
	}
}

func (f *contactReadFixture) compareRequests(t *testing.T, viewers ...*models.User) {
	t.Helper()
	for _, viewer := range viewers {
		op := &contactOperation{actor: viewer, now: f.now.Add(time.Second), zone: time.UTC}
		batch, err := f.app.presentContactRequests(f.app.DB, op, f.requests)
		if err != nil {
			t.Fatal(err)
		}
		scalar := []contactRequestJSON{}
		actor := contactAccount(viewer, op.now, op.zone)
		for _, r := range f.requests {
			if !policy.CanViewContactRequest(actor, contactRequestPolicy(r)) {
				continue
			}
			row, err := f.app.presentContactRequest(f.app.DB, op, r)
			if err != nil {
				t.Fatal(err)
			}
			scalar = append(scalar, row)
		}
		if !reflect.DeepEqual(batch, scalar) {
			t.Fatalf("request parity viewer %d\nbatch=%+v\nscalar=%+v", viewer.ID, batch, scalar)
		}
	}
}

// presentContactGrant is the test-only scalar presenter preserved as the batch parity oracle.
func (a *App) presentContactGrant(tx *gorm.DB, op *contactOperation, g models.ContactGrant, r models.ContactRequest) (contactGrantJSON, error) {
	if r.Purpose != policy.RequestContact {
		return contactGrantJSON{}, errors.New("not a contact request")
	}
	f, _, err := a.loadContactFacts(tx, contactScope(r), op.now)
	if err != nil {
		return contactGrantJSON{}, err
	}
	guardian, err := contactConfirmedGuardian(tx, op.actor.ID, r.SubjectUserID)
	if err != nil {
		return contactGrantJSON{}, err
	}
	actor := contactAccount(op.actor, op.now, op.zone)
	p := contactGrantPolicy(g, r)
	contextID, err := a.contactContextVisible(tx, op.actor, r)
	if err != nil {
		return contactGrantJSON{}, err
	}
	return contactGrantJSON{ID: g.ID, RequestID: r.ID, RequesterID: r.RequesterID, RequesterName: nameIn(tx, r.RequesterID), SubjectUserID: r.SubjectUserID, SubjectName: nameIn(tx, r.SubjectUserID), ContextType: r.ContextType, ContextID: contextID, Fields: r.Fields, NeedsAdmin: r.NeedsAdmin, GrantedAt: g.GrantedAt, EndedAt: g.EndedAt, EndReason: g.EndReason, EndedBy: g.EndedBy, CanReveal: policy.CanRevealContactGrant(actor, contactScope(r), p, f), CanRevoke: policy.ContactGrantRevocationReason(actor, guardian, p) != ""}, nil
}

func (f *contactReadFixture) compareGrants(t *testing.T, viewers ...*models.User) {
	t.Helper()
	requests := map[uint]models.ContactRequest{}
	for _, r := range f.requests {
		requests[r.ID] = r
	}
	for _, viewer := range viewers {
		op := &contactOperation{actor: viewer, now: f.now.Add(time.Second), zone: time.UTC}
		batch, err := f.app.presentContactGrants(f.app.DB, op, f.grants)
		if err != nil {
			t.Fatal(err)
		}
		scalar := []contactGrantJSON{}
		actor := contactAccount(viewer, op.now, op.zone)
		for _, g := range f.grants {
			r := requests[g.RequestID]
			guardian, err := contactConfirmedGuardian(f.app.DB, viewer.ID, r.SubjectUserID)
			if err != nil {
				t.Fatal(err)
			}
			facts, _, err := f.app.loadContactFacts(f.app.DB, contactScope(r), op.now)
			if err != nil {
				t.Fatal(err)
			}
			teen := facts.Subject.Person != nil && facts.Subject.Person.Teen && facts.Subject.AgeYears != nil && *facts.Subject.AgeYears >= 13 && *facts.Subject.AgeYears < 18
			if !policy.CanViewContactGrant(actor, guardian, teen, contactGrantPolicy(g, r)) {
				continue
			}
			row, err := f.app.presentContactGrant(f.app.DB, op, g, r)
			if err != nil {
				t.Fatal(err)
			}
			scalar = append(scalar, row)
		}
		if !reflect.DeepEqual(batch, scalar) {
			t.Fatalf("grant parity viewer %d\nbatch=%+v\nscalar=%+v", viewer.ID, batch, scalar)
		}
	}
}

func mutateContactReadFacts(t *testing.T, f *contactReadFixture, compare func(...*models.User)) {
	t.Helper()
	all := []*models.User{f.leader, f.admin, f.subject, f.guardian, f.outsider}
	if err := f.app.DB.Model(&models.Membership{}).Where("group_id = ? AND user_id = ?", f.group.ID, f.leader.ID).Update("status", models.MembershipLeft).Error; err != nil {
		t.Fatal(err)
	}
	compare(all...)
	if err := f.app.DB.Model(&models.Registration{}).Where("user_id = ?", f.subject.ID).Update("status", models.RegCancelled).Error; err != nil {
		t.Fatal(err)
	}
	compare(all...)
	if err := f.app.DB.Model(&models.Registration{}).Where("user_id = ?", f.subject.ID).Update("status", models.RegRemoved).Error; err != nil {
		t.Fatal(err)
	}
	compare(all...)
	if err := f.app.DB.Where("user_id = ?", f.leader.ID).Delete(&models.EventHost{}).Error; err != nil {
		t.Fatal(err)
	}
	compare(all...)
	if err := f.app.DB.Model(&models.GuardianLink{}).Where("guardian_user_id = ? AND teen_user_id = ?", f.guardian.ID, f.subject.ID).Update("status", models.GuardianRevoked).Error; err != nil {
		t.Fatal(err)
	}
	compare(all...)
	if err := f.app.DB.Model(f.subject).Update("status", models.UserDisabled).Error; err != nil {
		t.Fatal(err)
	}
	compare(f.leader, f.admin)
	if err := f.app.DB.Delete(f.subject).Error; err != nil {
		t.Fatal(err)
	}
	compare(f.leader, f.admin)
}

func TestContactRequestPageReadBudgetAndScalarParity(t *testing.T) {
	f := newContactReadFixture(t)
	f.seed(t, 3, false)
	n := collectionReadCounter(t, f.app.DB)
	*n = 0
	small, raw := requestPage(t, f.ac, "")
	smallReads := *n
	if len(small) != 3 || strings.Contains(raw, f.subject.Email) || strings.Contains(raw, f.subject.Phone) {
		t.Fatalf("small request page rows=%d leaked=%q", len(small), raw)
	}
	f.seed(t, 30, false)
	*n = 0
	many, raw := requestPage(t, f.ac, "")
	manyReads := *n
	if len(many) != 30 || manyReads != smallReads || manyReads > 16 || strings.Contains(raw, f.subject.Email) || strings.Contains(raw, f.subject.Phone) {
		t.Fatalf("request page rows=%d reads 3=%d 30=%d", len(many), smallReads, manyReads)
	}
	before := "?before=" + itoa(f.requests[3].ID)
	page1, _ := requestPage(t, f.ac, before)
	page2, _ := requestPage(t, f.ac, before)
	want := []uint{f.requests[2].ID, f.requests[1].ID, f.requests[0].ID}
	assertContactPageIDs(t, page1, want)
	if !reflect.DeepEqual(page1, page2) {
		t.Fatal("request cursor order is unstable")
	}
	f.compareRequests(t, f.leader, f.admin, f.subject, f.guardian, f.outsider)
	if got, _ := requestPage(t, f.gc, ""); len(got) != 0 {
		t.Fatal("guardian received request list", got)
	}
	mutateContactReadFacts(t, f, func(viewers ...*models.User) { f.compareRequests(t, viewers...) })
	for _, row := range mustPresentContactRequests(t, f) {
		if row.SubjectName != f.subject.PublicName() {
			t.Fatal("soft-deleted subject name was not retained", row)
		}
		if row.CanSubjectDecide || row.CanAdminDecide {
			t.Fatal("soft-deleted subject retained decision eligibility", row)
		}
	}
	t.Logf("contact request page reads mixed3=%d mixed30=%d", smallReads, manyReads)
}

func mustPresentContactRequests(t *testing.T, f *contactReadFixture) []contactRequestJSON {
	t.Helper()
	rows, err := f.app.presentContactRequests(f.app.DB, &contactOperation{actor: f.admin, now: f.now.Add(time.Second), zone: time.UTC}, f.requests)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func TestContactGrantPageReadBudgetAndScalarParity(t *testing.T) {
	f := newContactReadFixture(t)
	f.seed(t, 3, true)
	n := collectionReadCounter(t, f.app.DB)
	*n = 0
	small, raw := grantPage(t, f.ac, "")
	smallReads := *n
	if len(small) != 3 || strings.Contains(raw, f.subject.Email) || strings.Contains(raw, f.subject.Phone) {
		t.Fatalf("small grant page rows=%d leaked=%q", len(small), raw)
	}
	f.seed(t, 30, true)
	*n = 0
	many, raw := grantPage(t, f.ac, "")
	manyReads := *n
	if len(many) != 30 || manyReads != smallReads || manyReads > 16 || strings.Contains(raw, f.subject.Email) || strings.Contains(raw, f.subject.Phone) {
		t.Fatalf("grant page rows=%d reads 3=%d 30=%d", len(many), smallReads, manyReads)
	}
	before := "?before=" + itoa(f.grants[3].ID)
	page1, _ := grantPage(t, f.ac, before)
	page2, _ := grantPage(t, f.ac, before)
	want := []uint{f.grants[2].ID, f.grants[1].ID, f.grants[0].ID}
	assertContactPageIDs(t, page1, want)
	if !reflect.DeepEqual(page1, page2) {
		t.Fatal("grant cursor order is unstable")
	}
	f.compareGrants(t, f.leader, f.admin, f.subject, f.guardian, f.outsider)
	guardian, _ := grantPage(t, f.gc, "")
	if len(guardian) != 30 {
		t.Fatal("guardian active teen grants", len(guardian))
	}
	requests := map[uint]models.ContactRequest{}
	for _, r := range f.requests {
		requests[r.ID] = r
	}
	for _, row := range guardian {
		r := requests[row.RequestID]
		if r.ContextID == f.churchEvent.ID {
			if row.ContextID == nil || *row.ContextID != f.churchEvent.ID {
				t.Fatal("signed-in guardian cannot see private church event context", row)
			}
		} else if row.ContextID != nil {
			t.Fatal("invite-only group or group series exposed to guardian", row)
		}
	}
	mutateContactReadFacts(t, f, func(viewers ...*models.User) { f.compareGrants(t, viewers...) })
	rows, err := f.app.presentContactGrants(f.app.DB, &contactOperation{actor: f.admin, now: f.now.Add(time.Second), zone: time.UTC}, f.grants)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.SubjectName != f.subject.PublicName() {
			t.Fatal("soft-deleted subject name was not retained", row)
		}
		if row.CanReveal {
			t.Fatal("soft-deleted subject retained reveal eligibility", row)
		}
	}
	t.Logf("contact grant page reads mixed3=%d mixed30=%d", smallReads, manyReads)
}

func TestContactPageFactReadFailureFailsClosed(t *testing.T) {
	f := newContactReadFixture(t)
	f.seed(t, 3, true)
	name := "contact-page:fail-memberships"
	if err := f.app.DB.Callback().Query().Before("gorm:query").Register(name, func(db *gorm.DB) {
		if db.Statement.Table == "memberships" {
			db.AddError(errors.New("injected contact page read failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.app.DB.Callback().Query().Remove(name) })
	for _, route := range []string{"/api/v1/contact-requests", "/api/v1/contact-grants"} {
		if w := f.ac.do("GET", route, nil, false); w.Code != 500 {
			t.Fatalf("%s: %d %s", route, w.Code, w.Body.String())
		}
	}
}
