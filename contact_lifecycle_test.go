package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"fold/internal/audit"
	"fold/internal/notify"
	"fold/internal/policy"
	"fold/models"

	"gorm.io/gorm"
)

// Queue on the real PostgreSQL advisory gate, not sleeps or a mock mutex.
// pg_stat_activity proves each contender reached the gate before release.
func holdContactGate(t *testing.T, a *App) *gorm.DB {
	t.Helper()
	tx := a.DB.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	if err := lockContacts(tx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tx.Rollback() })
	return tx
}
func waitContactWaiters(t *testing.T, a *App, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var count int64
		err := a.DB.Raw("SELECT count(*) FROM pg_stat_activity WHERE datname = current_database() AND wait_event = 'advisory' AND query LIKE 'SELECT pg_advisory_xact_lock%'").Scan(&count).Error
		if err != nil {
			t.Fatal(err)
		}
		if count >= int64(n) {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d contact gate contenders", n)
}
func awaitContactResponse(t *testing.T, ch <-chan *httptest.ResponseRecorder) *httptest.ResponseRecorder {
	t.Helper()
	select {
	case w := <-ch:
		return w
	case <-time.After(8 * time.Second):
		t.Fatal("contact contender timed out (possible deadlock)")
		return nil
	}
}
func queueContact(fn func() *httptest.ResponseRecorder) <-chan *httptest.ResponseRecorder {
	ch := make(chan *httptest.ResponseRecorder, 1)
	go func() { ch <- fn() }()
	return ch
}
func cloneContactClient(c *apiClient) *apiClient {
	copy := *c
	copy.jar = map[string]*http.Cookie{}
	for k, v := range c.jar {
		copy.jar[k] = v
	}
	return &copy
}
func TestContactParallelApprovalsOneGrant(t *testing.T) {
	f := newContactAPIFixture(t, true)
	r := f.create(t, "both", "")
	gate := holdContactGate(t, f.app)
	subject := queueContact(func() *httptest.ResponseRecorder { return contactDecision(f.sc, r.ID, "subject", "approved") })
	waitContactWaiters(t, f.app, 1)
	admin := queueContact(func() *httptest.ResponseRecorder { return contactDecision(f.ac, r.ID, "admin", "approved") })
	waitContactWaiters(t, f.app, 2)
	duplicate := queueContact(func() *httptest.ResponseRecorder {
		return contactDecision(cloneContactClient(f.ac), r.ID, "admin", "approved")
	})
	waitContactWaiters(t, f.app, 3)
	gate.Rollback()
	for _, ch := range []<-chan *httptest.ResponseRecorder{subject, admin} {
		w := awaitContactResponse(t, ch)
		if w.Code != 200 {
			t.Fatalf("approve %d %s", w.Code, w.Body.String())
		}
	}
	if w := awaitContactResponse(t, duplicate); w.Code != 403 {
		t.Fatalf("replay %d", w.Code)
	}
	if contactCount(t, f.app, &models.ContactGrant{}, "request_id = ?", r.ID) != 1 || contactCount(t, f.app, &models.AuditLog{}, "action = ?", audit.ContactGranted) != 1 {
		t.Fatal("duplicate grant or audit")
	}
}
func TestContactTerminalWinsApproval(t *testing.T) {
	for _, terminal := range []string{"deny", "withdraw", "expiry"} {
		t.Run(terminal, func(t *testing.T) {
			f := newContactAPIFixture(t, true)
			r := f.create(t, "email", "")
			if w := contactDecision(f.sc, r.ID, "subject", "approved"); w.Code != 200 {
				t.Fatal(w.Body.String())
			}
			// Both decisions retain prior facts even when a later terminal transition wins.
			if terminal == "expiry" {
				var saved models.ContactRequest
				f.app.DB.First(&saved, r.ID)
				created := time.Now().UTC().Add(-15 * 24 * time.Hour)
				expires := created.Add(policy.ContactRequestLifetime)
				decided := created.Add(time.Hour)
				if err := f.app.DB.Model(&saved).Updates(map[string]any{"created_at": created, "expires_at": expires, "subject_decided_at": decided}).Error; err != nil {
					t.Fatal(err)
				}
			}
			gate := holdContactGate(t, f.app)
			var first <-chan *httptest.ResponseRecorder
			if terminal == "deny" {
				first = queueContact(func() *httptest.ResponseRecorder { return contactDecision(f.ac, r.ID, "admin", "denied") })
			} else if terminal == "withdraw" {
				first = queueContact(func() *httptest.ResponseRecorder {
					return f.lc.do("POST", "/api/v1/contact-requests/"+itoa(r.ID)+"/withdraw", nil, true)
				})
			} else {
				first = queueContact(func() *httptest.ResponseRecorder {
					err := f.app.contactMutation(contactChange{UserID: f.leader.ID}, func(*gorm.DB) error { return nil })
					w := httptest.NewRecorder()
					if err != nil {
						w.WriteHeader(500)
					} else {
						w.WriteHeader(200)
					}
					return w
				})
			}
			waitContactWaiters(t, f.app, 1)
			approvalClient := cloneContactClient(f.ac)
			approval := queueContact(func() *httptest.ResponseRecorder { return contactDecision(approvalClient, r.ID, "admin", "approved") })
			waitContactWaiters(t, f.app, 2)
			gate.Rollback()
			if w := awaitContactResponse(t, first); w.Code != 200 {
				t.Fatalf("terminal %d %s", w.Code, w.Body.String())
			}
			if w := awaitContactResponse(t, approval); w.Code != 403 {
				t.Fatalf("resurrection %d %s", w.Code, w.Body.String())
			}
			var saved models.ContactRequest
			f.app.DB.First(&saved, r.ID)
			want := map[string]string{"deny": "denied", "withdraw": "withdrawn", "expiry": "expired"}[terminal]
			if saved.Status != want || saved.SubjectDecision != "approved" {
				t.Fatalf("terminal %#v", saved)
			}
			if contactCount(t, f.app, &models.ContactGrant{}, "request_id = ?", r.ID) != 0 {
				t.Fatal("terminal gained grant")
			}
		})
	}
}
func TestContactConcurrentOpenCap(t *testing.T) {
	f := newContactAPIFixture(t, false)
	gate := holdContactGate(t, f.app)
	clients := make([]*apiClient, 16)
	for i := range clients {
		clients[i] = cloneContactClient(f.lc)
	}
	channels := make([]<-chan *httptest.ResponseRecorder, 16)
	for i, c := range clients {
		channels[i] = queueContact(func() *httptest.ResponseRecorder {
			return c.do("POST", "/api/v1/contact-requests", map[string]any{"subjectUserId": f.subject.ID, "contextType": "group", "contextId": f.group.ID, "fields": "email"}, true)
		})
	}
	waitContactWaiters(t, f.app, 16)
	gate.Rollback()
	created := 0
	for _, ch := range channels {
		w := awaitContactResponse(t, ch)
		if w.Code == 201 {
			created++
		} else if w.Code != 409 {
			t.Fatalf("cap result %d %s", w.Code, w.Body.String())
		}
	}
	if created != 10 || contactCount(t, f.app, &models.ContactRequest{}, "status = ?", "pending") != 10 {
		t.Fatalf("open count %d", created)
	}
}
func TestContactAuthorityMutationWinsReveal(t *testing.T) {
	for _, mutation := range []string{"revoke", "disable-subject", "disable-requester", "delete-subject", "role-loss", "member-loss", "handoff"} {
		t.Run(mutation, func(t *testing.T) {
			f := newContactAPIFixture(t, false)
			r := f.create(t, "both", "")
			g := f.grant(t, r)
			// An extra leader lets demotion occur without invoking the sole-leader guard.
			if err := f.app.DB.Create(&models.Membership{UserID: f.admin.ID, GroupID: f.group.ID, Role: models.MembershipLeader, Status: "active"}).Error; err != nil {
				t.Fatal(err)
			}
			gate := holdContactGate(t, f.app)
			first := queueContact(func() *httptest.ResponseRecorder {
				switch mutation {
				case "revoke":
					return f.sc.do("POST", "/api/v1/contact-grants/"+itoa(g.ID)+"/revoke", nil, true)
				case "disable-subject":
					return f.ac.do("PUT", "/api/v1/admin/users/"+itoa(f.subject.ID), map[string]any{"status": "disabled"}, true)
				case "disable-requester":
					return f.ac.do("PUT", "/api/v1/admin/users/"+itoa(f.leader.ID), map[string]any{"status": "disabled"}, true)
				case "delete-subject":
					return f.ac.do("DELETE", "/api/v1/admin/users/"+itoa(f.subject.ID), nil, true)
				case "role-loss":
					return f.ac.do("PUT", "/api/v1/groups/"+itoa(f.group.ID)+"/members/"+itoa(f.leader.ID)+"/role", map[string]any{"role": "member"}, true)
				case "member-loss":
					return f.sc.do("POST", "/api/v1/groups/"+itoa(f.group.ID)+"/leave", nil, true)
				default:
					// Force sole-leader handoff, preserving the account-deletion user's prefix.
					return f.ac.do("DELETE", "/api/v1/admin/users/"+itoa(f.leader.ID), map[string]any{"successors": map[string]uint{itoa(f.group.ID): f.admin.ID}}, true)
				}
			})
			waitContactWaiters(t, f.app, 1)
			reveal := queueContact(func() *httptest.ResponseRecorder { return f.reveal(f.lc, g, "email") })
			waitContactWaiters(t, f.app, 2)
			gate.Rollback()
			if w := awaitContactResponse(t, first); w.Code != 200 {
				t.Fatalf("mutation %d %s", w.Code, w.Body.String())
			}
			if w := awaitContactResponse(t, reveal); w.Code == 200 || w.Code == 500 {
				t.Fatalf("reveal after mutation %d %s", w.Code, w.Body.String())
			}
			if contactCount(t, f.app, &models.AuditLog{}, "action = ?", audit.ContactReveal) != 0 {
				t.Fatal("ended access revealed")
			}
			f.app.DB.First(&g, g.ID)
			if g.EndedAt == nil {
				t.Fatal("mutation did not end grant")
			}
			var saved models.ContactRequest
			f.app.DB.First(&saved, r.ID)
			if saved.Status != "granted" {
				t.Fatal("lost successful history")
			}
		})
	}
}
func TestContactDisableWinsPendingTaskDecision(t *testing.T) {
	f := newContactAPIFixture(t, true)
	r := f.create(t, "email", "")
	if w := contactDecision(f.sc, r.ID, "subject", "approved"); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	gate := holdContactGate(t, f.app)
	disable := queueContact(func() *httptest.ResponseRecorder {
		return f.ac.do("PUT", "/api/v1/admin/users/"+itoa(f.subject.ID), map[string]any{"status": "disabled"}, true)
	})
	waitContactWaiters(t, f.app, 1)
	approve := queueContact(func() *httptest.ResponseRecorder {
		return contactDecision(cloneContactClient(f.ac), r.ID, "admin", "approved")
	})
	waitContactWaiters(t, f.app, 2)
	gate.Rollback()
	if w := awaitContactResponse(t, disable); w.Code != 200 {
		t.Fatalf("disable %d %s", w.Code, w.Body.String())
	}
	if w := awaitContactResponse(t, approve); w.Code != 403 {
		t.Fatalf("approve %d", w.Code)
	}
	if contactCount(t, f.app, &models.Notification{}, "subject_type = ? AND subject_id = ? AND waiting AND resolved_at IS NULL", "contact_request", r.ID) != 0 {
		t.Fatal("orphaned waiting task")
	}
	var saved models.ContactRequest
	f.app.DB.First(&saved, r.ID)
	if saved.Status != "ended" || saved.EndReason != "account_disabled" {
		t.Fatalf("pending loss %#v", saved)
	}
}
func TestContactSweepsBoundariesAndIdempotence(t *testing.T) {
	f := newContactAPIFixture(t, true)
	r := f.create(t, "email", "")
	g := f.grant(t, r)
	// Church-local midnight, not UTC midnight; leave account_type stale (DEC-83).
	if err := f.app.DB.Create(&models.Setting{Key: settingTimezone, Value: "America/Los_Angeles"}).Error; err != nil {
		t.Fatal(err)
	}
	birth := time.Date(2008, 10, 25, 0, 0, 0, 0, time.UTC)
	f.app.DB.Model(f.subject).Update("birth_date", birth)
	boundary := time.Date(2026, 10, 25, 7, 0, 0, 0, time.UTC)
	if err := f.app.sweepContacts(context.Background(), boundary.Add(-time.Microsecond)); err != nil {
		t.Fatal(err)
	}
	f.app.DB.First(&g, g.ID)
	if g.EndedAt != nil {
		t.Fatal("ended before local adulthood")
	}
	if err := f.app.sweepContacts(context.Background(), boundary); err != nil {
		t.Fatal(err)
	}
	f.app.DB.First(&g, g.ID)
	if g.EndReason != policy.ContactTurned18 {
		t.Fatalf("birthday %#v", g)
	}
	audits := contactCount(t, f.app, &models.AuditLog{}, "action = ?", audit.ContactEnded)
	notices := contactCount(t, f.app, &models.Notification{}, "kind = ?", notify.ContactUpdated)
	for i := 0; i < 2; i++ {
		if err := f.app.sweepContacts(context.Background(), boundary.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	if audits != contactCount(t, f.app, &models.AuditLog{}, "action = ?", audit.ContactEnded) || notices != contactCount(t, f.app, &models.Notification{}, "kind = ?", notify.ContactUpdated) {
		t.Fatal("repeated sweep duplicated terminal facts")
	}
}
func TestContactEventGraceAndHostRemoval(t *testing.T) {
	for _, end := range []string{"grace", "host-removal", "registration-loss", "delete-event", "delete-group"} {
		t.Run(end, func(t *testing.T) {
			f := newContactAPIFixture(t, false)
			now := time.Now().UTC().Truncate(time.Microsecond)
			ev, occ := seedEvent(t, f.app, f.group, f.leader.ID, now.Add(-24*time.Hour))
			if err := f.app.DB.Model(&ev).Update("group_id", nil).Error; err != nil {
				t.Fatal(err)
			}
			ev.GroupID = nil
			for _, id := range []uint{f.leader.ID, f.admin.ID} {
				if err := f.app.DB.Create(&models.EventHost{EventID: ev.ID, UserID: id, AddedBy: f.admin.ID}).Error; err != nil {
					t.Fatal(err)
				}
			}
			reg := models.Registration{OccurrenceID: occ.ID, UserID: &f.subject.ID, Status: models.RegGoing}
			if err := f.app.DB.Create(&reg).Error; err != nil {
				t.Fatal(err)
			}
			w := f.lc.do("POST", "/api/v1/contact-requests", map[string]any{"subjectUserId": f.subject.ID, "contextType": "event", "contextId": ev.ID, "fields": "email"}, true)
			if w.Code != 201 {
				t.Fatalf("event request %d %s", w.Code, w.Body.String())
			}
			var body struct {
				Request contactRequestJSON `json:"request"`
			}
			decodeJSON(t, w, &body)
			g := f.grant(t, body.Request)
			switch end {
			case "grace":
				boundary := occ.EndsAt.Add(policy.ContactEventGrace)
				// Another earlier date must not be mistaken for the final occurrence.
				earlier := models.EventOccurrence{EventID: ev.ID, OriginalDate: "2025-01-01", StartsAt: occ.StartsAt.Add(-time.Hour), EndsAt: occ.EndsAt.Add(-time.Hour), Status: models.OccurrenceScheduled}
				f.app.DB.Create(&earlier)
				if err := f.app.sweepContacts(context.Background(), boundary.Add(-time.Microsecond)); err != nil {
					t.Fatal(err)
				}
				f.app.DB.First(&g, g.ID)
				if g.EndedAt != nil {
					t.Fatal("ended before exact grace")
				}
				if err := f.app.sweepContacts(context.Background(), boundary); err != nil {
					t.Fatal(err)
				}
			case "host-removal":
				w = f.ac.do("DELETE", "/api/v1/events/"+itoa(ev.ID)+"/hosts/"+itoa(f.leader.ID), nil, true)
				if w.Code != 200 {
					t.Fatalf("remove host %d %s", w.Code, w.Body.String())
				}
			case "registration-loss":
				// Move date to future so the ordinary pruning path applies.
				f.app.DB.Model(&occ).Updates(map[string]any{"starts_at": now.Add(time.Hour), "ends_at": now.Add(2 * time.Hour)})
				w = f.lc.do("POST", "/api/v1/events/"+itoa(ev.ID)+"/registrations/remove", map[string]any{"date": occ.OriginalDate, "registrationId": reg.ID}, true)
				if w.Code != 200 {
					t.Fatalf("remove registration %d %s", w.Code, w.Body.String())
				}
			case "delete-event":
				w = f.ac.do("DELETE", "/api/v1/events/"+itoa(ev.ID), nil, true)
				if w.Code != 200 {
					t.Fatalf("delete event %d %s", w.Code, w.Body.String())
				}
			case "delete-group":
				// Make it a group event again before deleting its context.
				f.app.DB.Model(&ev).Update("group_id", f.group.ID)
				if err := f.app.deleteGroup(context.Background(), f.group, &f.admin.ID); err != nil {
					t.Fatal(err)
				}
			}
			f.app.DB.First(&g, g.ID)
			if g.EndedAt == nil {
				t.Fatal("event authority not ended")
			}
			if w = f.reveal(f.lc, g, "email"); w.Code != 403 {
				t.Fatalf("ended event reveal %d %s", w.Code, w.Body.String())
			}
		})
	}
}
func TestContactAuditFailureRollsBackAndFailsClosed(t *testing.T) {
	f := newContactAPIFixture(t, false)
	r := f.create(t, "email", "")
	g := f.grant(t, r)
	name := "contact:test_audit_failure"
	if err := f.app.DB.Callback().Create().Before("gorm:create").Register(name, func(db *gorm.DB) {
		if db.Statement.Table == "audit_logs" {
			db.AddError(errors.New("injected audit failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer f.app.DB.Callback().Create().Remove(name)
	if w := f.reveal(f.lc, g, "email"); w.Code != 500 || strings.Contains(w.Body.String(), f.subject.Email) {
		t.Fatalf("audit fail reveal %d %s", w.Code, w.Body.String())
	}
	if w := f.reveal(f.oc, g, "email"); w.Code != 500 {
		t.Fatalf("refusal audit failure %d", w.Code)
	}
	w := f.sc.do("POST", "/api/v1/contact-grants/"+itoa(g.ID)+"/revoke", nil, true)
	if w.Code != 500 {
		t.Fatalf("revoke audit %d", w.Code)
	}
	f.app.DB.First(&g, g.ID)
	if g.EndedAt != nil {
		t.Fatal("revoke survived failed audit")
	}
	w = f.ac.do("PUT", "/api/v1/admin/users/"+itoa(f.subject.ID), map[string]any{"status": "disabled"}, true)
	if w.Code != 500 {
		t.Fatalf("disable audit %d", w.Code)
	}
	var subject models.User
	f.app.DB.First(&subject, f.subject.ID)
	if subject.Status != models.UserActive {
		t.Fatal("disable survived failed audit")
	}
}

func TestContactPendingExpiryExactBoundary(t *testing.T) {
	f := newContactAPIFixture(t, false)
	r := f.create(t, "email", "")
	if err := f.app.sweepContacts(context.Background(), r.ExpiresAt.Add(-time.Microsecond)); err != nil {
		t.Fatal(err)
	}
	var saved models.ContactRequest
	f.app.DB.First(&saved, r.ID)
	if saved.Status != "pending" {
		t.Fatal("expired early")
	}
	if err := f.app.sweepContacts(context.Background(), r.ExpiresAt); err != nil {
		t.Fatal(err)
	}
	f.app.DB.First(&saved, r.ID)
	if saved.Status != "expired" || saved.CompletedAt == nil || !saved.CompletedAt.Equal(r.ExpiresAt) {
		t.Fatalf("expiry %#v", saved)
	}
	if contactCount(t, f.app, &models.AuditLog{}, "action = ?", audit.ContactExpired) != 1 {
		t.Fatal("missing expiry audit")
	}
	for i := 0; i < 2; i++ {
		if err := f.app.sweepContacts(context.Background(), r.ExpiresAt.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	if contactCount(t, f.app, &models.AuditLog{}, "action = ?", audit.ContactExpired) != 1 {
		t.Fatal("duplicate expiry audit")
	}
	if contactCount(t, f.app, &models.Notification{}, "subject_type = ? AND subject_id = ? AND waiting AND resolved_at IS NULL", "contact_request", r.ID) != 0 {
		t.Fatal("expiry left task waiting")
	}
	if w := contactDecision(f.sc, r.ID, "subject", "approved"); w.Code != 403 {
		t.Fatalf("expired decision %d", w.Code)
	}
}
func TestContactDecisionAndLifecycleAuditFailure(t *testing.T) {
	f := newContactAPIFixture(t, false)
	r := f.create(t, "email", "")
	action := audit.ContactGranted
	name := "contact:test_transition_audit_failure"
	if err := f.app.DB.Callback().Create().Before("gorm:create").Register(name, func(db *gorm.DB) {
		if row, ok := db.Statement.Dest.(*models.AuditLog); ok && row.Action == action {
			db.AddError(errors.New("injected contact audit failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer f.app.DB.Callback().Create().Remove(name)
	if w := contactDecision(f.sc, r.ID, "subject", "approved"); w.Code != 500 {
		t.Fatalf("grant audit %d %s", w.Code, w.Body.String())
	}
	var saved models.ContactRequest
	f.app.DB.First(&saved, r.ID)
	if saved.Status != "pending" || saved.SubjectDecision != "pending" || contactCount(t, f.app, &models.ContactGrant{}, "request_id = ?", r.ID) != 0 {
		t.Fatal("failed grant audit left approval/grant")
	}
	if contactCount(t, f.app, &models.Notification{}, "subject_id = ? AND kind = ? AND resolved_at IS NULL", r.ID, notify.ContactSubjectReview) != 1 {
		t.Fatal("failed grant resolved task")
	}
	action = ""
	g := f.grant(t, r)
	if err := f.app.DB.Create(&models.Membership{UserID: f.admin.ID, GroupID: f.group.ID, Role: models.MembershipLeader, Status: "active"}).Error; err != nil {
		t.Fatal(err)
	}
	action = audit.ContactEnded
	w := f.ac.do("PUT", "/api/v1/groups/"+itoa(f.group.ID)+"/members/"+itoa(f.leader.ID)+"/role", map[string]any{"role": "member"}, true)
	if w.Code != 500 {
		t.Fatalf("lifecycle audit %d %s", w.Code, w.Body.String())
	}
	f.app.DB.First(&g, g.ID)
	if g.EndedAt != nil || !relIn(f.app.DB, f.leader.ID, f.group.ID).Leader {
		t.Fatal("domain role loss or grant end survived failed lifecycle audit")
	}
}
func TestContactRestoredRoleCannotResurrect(t *testing.T) {
	f := newContactAPIFixture(t, false)
	r := f.create(t, "email", "")
	g := f.grant(t, r)
	if err := f.app.DB.Create(&models.Membership{UserID: f.admin.ID, GroupID: f.group.ID, Role: models.MembershipLeader, Status: "active"}).Error; err != nil {
		t.Fatal(err)
	}
	for _, role := range []string{models.MembershipMember, models.MembershipLeader} {
		w := f.ac.do("PUT", "/api/v1/groups/"+itoa(f.group.ID)+"/members/"+itoa(f.leader.ID)+"/role", map[string]any{"role": role}, true)
		if w.Code != 200 {
			t.Fatalf("role %d %s", w.Code, w.Body.String())
		}
	}
	if w := f.reveal(f.lc, g, "email"); w.Code != 403 {
		t.Fatalf("restored role resurrected grant %d", w.Code)
	}
}
func TestContactConsentRevocationEndsAccess(t *testing.T) {
	for _, caller := range []string{"admin", "guardian"} {
		t.Run(caller, func(t *testing.T) {
			f := newContactAPIFixture(t, true)
			r := f.create(t, "email", "")
			g := f.grant(t, r)
			var w *httptest.ResponseRecorder
			if caller == "admin" {
				w = f.ac.do("POST", "/api/v1/admin/users/"+itoa(f.subject.ID)+"/teen-consents", map[string]any{"scope": "account", "status": "revoked"}, true)
			} else {
				var link models.GuardianLink
				f.app.DB.Where("teen_user_id = ? AND guardian_user_id = ?", f.subject.ID, f.guardian.ID).First(&link)
				w = f.gc.do("POST", "/api/v1/me/guardian-links/"+itoa(link.ID)+"/consents", map[string]any{"scope": "account", "status": "revoked"}, true)
			}
			if w.Code != 200 {
				t.Fatalf("consent %d %s", w.Code, w.Body.String())
			}
			f.app.DB.First(&g, g.ID)
			if g.EndReason != policy.ContactAccountDisabled {
				t.Fatalf("consent did not end %#v", g)
			}
			if w = f.reveal(f.lc, g, "email"); w.Code != 403 {
				t.Fatalf("consent reveal %d", w.Code)
			}
		})
	}
}
