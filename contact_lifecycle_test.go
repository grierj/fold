package main

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"fold/internal/audit"
	"fold/internal/notify"
	"fold/internal/policy"
	"fold/internal/testdb"
	"fold/internal/txguard"
	"fold/models"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/stdlib"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
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
			var memberLossBody map[string]any
			if mutation == "member-loss" {
				memberLossBody = departureConfirmBody(t, f.sc, "/api/v1/groups/"+itoa(f.group.ID)+"/departure-preview")
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
					return f.sc.do("POST", "/api/v1/groups/"+itoa(f.group.ID)+"/leave", memberLossBody, true)
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
	zone, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Fatal(err)
	}
	// Keep the fake birthday after the real grant, even when this test runs
	// years later. October's local midnight is still not UTC midnight.
	year := g.GrantedAt.In(zone).Year() + 1
	birth := time.Date(year-18, 10, 25, 0, 0, 0, 0, time.UTC)
	if err := f.app.DB.Model(f.subject).Update("birth_date", birth).Error; err != nil {
		t.Fatal(err)
	}
	boundary := time.Date(year, 10, 25, 0, 0, 0, 0, zone).UTC()
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
				earlier := models.EventOccurrence{EventID: ev.ID, OriginalDate: calDate("2025-01-01"), StartsAt: occ.StartsAt.Add(-time.Hour), EndsAt: occ.EndsAt.Add(-time.Hour), Status: models.OccurrenceScheduled}
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
				w = f.lc.do("POST", "/api/v1/events/"+itoa(ev.ID)+"/registrations/remove", map[string]any{"date": models.FormatCalendarDate(occ.OriginalDate), "registrationId": reg.ID}, true)
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

func TestGroupAskParallelFinalDecisionsExactlyOneMembership(t *testing.T) {
	f := newGroupAskFixture(t)
	r := createGroupAsk(t, f, f.lc, f.leader, false)
	gate := holdContactGate(t, f.app)
	subject := queueContact(func() *httptest.ResponseRecorder { return addDecision(f.sc, r.ID, "subject", "approved") })
	waitContactWaiters(t, f.app, 1)
	admin := queueContact(func() *httptest.ResponseRecorder { return addDecision(f.ac, r.ID, "admin", "approved") })
	waitContactWaiters(t, f.app, 2)
	duplicate := queueContact(func() *httptest.ResponseRecorder {
		return addDecision(cloneContactClient(f.ac), r.ID, "admin", "approved")
	})
	waitContactWaiters(t, f.app, 3)
	gate.Rollback()
	for _, ch := range []<-chan *httptest.ResponseRecorder{subject, admin} {
		if w := awaitContactResponse(t, ch); w.Code != 200 {
			t.Fatalf("approve: %d %s", w.Code, w.Body.String())
		}
	}
	if w := awaitContactResponse(t, duplicate); w.Code != 403 {
		t.Fatalf("replay: %d", w.Code)
	}
	if contactCount(t, f.app, &models.Membership{}, "user_id = ? AND group_id = ? AND status = 'active'", f.subject.ID, f.group.ID) != 1 || contactCount(t, f.app, &models.AuditLog{}, "action = ? AND target_id = ?", audit.AddCompleted, r.ID) != 1 {
		t.Fatal("duplicate or absent completed add")
	}
	if savedAsk(t, f, r.ID).CompletedTargetID == nil {
		t.Fatal("no membership provenance")
	}
}
func TestGroupAskImmediateLifecycleAndNoResurrection(t *testing.T) {
	for _, mutation := range []string{"role_loss", "disable_subject", "disable_requester", "context_loss", "adult_age", "expiry", "withdraw"} {
		t.Run(mutation, func(t *testing.T) {
			f := newGroupAskFixture(t)
			r := createGroupAsk(t, f, f.lc, f.leader, false)
			if w := addDecision(f.sc, r.ID, "subject", "approved"); w.Code != 200 {
				t.Fatal(w.Body.String())
			}
			want, reason := "ended", ""
			switch mutation {
			case "role_loss":
				if err := f.app.DB.Create(&models.Membership{UserID: f.admin.ID, GroupID: f.group.ID, Role: models.MembershipLeader, Status: "active"}).Error; err != nil {
					t.Fatal(err)
				}
				if w := f.ac.do("PUT", "/api/v1/groups/"+itoa(f.group.ID)+"/members/"+itoa(f.leader.ID)+"/role", map[string]any{"role": "member"}, true); w.Code != 200 {
					t.Fatal(w.Body.String())
				}
				reason = policy.ContactLeftRole
				// Restoring the role cannot revive the old ask.
				if w := f.ac.do("PUT", "/api/v1/groups/"+itoa(f.group.ID)+"/members/"+itoa(f.leader.ID)+"/role", map[string]any{"role": "leader"}, true); w.Code != 200 {
					t.Fatal(w.Body.String())
				}
			case "disable_subject", "disable_requester":
				u := f.subject
				if mutation == "disable_requester" {
					if err := f.app.DB.Create(&models.Membership{UserID: f.admin.ID, GroupID: f.group.ID, Role: models.MembershipLeader, Status: "active"}).Error; err != nil {
						t.Fatal(err)
					}
					u = f.leader
				}
				if w := f.ac.do("PUT", "/api/v1/admin/users/"+itoa(u.ID), map[string]any{"status": "disabled"}, true); w.Code != 200 {
					t.Fatal(w.Body.String())
				}
				reason = policy.ContactAccountDisabled
				if w := f.ac.do("PUT", "/api/v1/admin/users/"+itoa(u.ID), map[string]any{"status": "active"}, true); w.Code != 200 {
					t.Fatal(w.Body.String())
				}
			case "context_loss":
				if err := f.app.contactMutation(contactChange{GroupID: f.group.ID}, func(tx *gorm.DB) error { return tx.Model(f.group).Update("status", models.GroupArchived).Error }); err != nil {
					t.Fatal(err)
				}
				reason = policy.ContactContextEnded
			case "adult_age":
				birth := time.Now().AddDate(-18, 0, 0)
				if err := f.app.contactMutation(contactChange{UserID: f.subject.ID}, func(tx *gorm.DB) error { return tx.Model(f.subject).Update("birth_date", birth).Error }); err != nil {
					t.Fatal(err)
				}
				reason = policy.ContactTurned18
			case "expiry":
				if err := f.app.sweepContacts(t.Context(), time.Now().Add(15*24*time.Hour)); err != nil {
					t.Fatal(err)
				}
				want = "expired"
			case "withdraw":
				if w := f.lc.do("POST", "/api/v1/add-requests/"+itoa(r.ID)+"/withdraw", nil, true); w.Code != 200 {
					t.Fatal(w.Body.String())
				}
				want = "withdrawn"
			}
			saved := savedAsk(t, f, r.ID)
			if saved.Status != want || saved.EndReason != reason {
				t.Fatalf("immediate end missing: %+v", saved)
			}
			if w := addDecision(f.ac, r.ID, "admin", "approved"); w.Code != 403 {
				t.Fatalf("resurrection: %d %s", w.Code, w.Body.String())
			}
			if w := f.ac.do("POST", "/api/v1/add-requests/"+itoa(r.ID)+"/complete", nil, true); w.Code != 403 {
				t.Fatalf("completion resurrection: %d", w.Code)
			}
			before := contactCount(t, f.app, &models.AuditLog{}, "target_type = ? AND target_id = ? AND action IN ?", "add_request", r.ID, []string{audit.AddEnded, audit.AddExpired, audit.AddWithdrawn})
			for i := 0; i < 2; i++ {
				if err := f.app.sweepContacts(t.Context(), time.Now().Add(16*24*time.Hour)); err != nil {
					t.Fatal(err)
				}
			}
			if before != 1 || contactCount(t, f.app, &models.AuditLog{}, "target_type = ? AND target_id = ? AND action IN ?", "add_request", r.ID, []string{audit.AddEnded, audit.AddExpired, audit.AddWithdrawn}) != before {
				t.Fatal("repeated sweep rewrote terminal history")
			}
			if contactCount(t, f.app, &models.Notification{}, "subject_id = ? AND kind IN ? AND resolved_at IS NULL", r.ID, []string{notify.AddSubjectReview, notify.AddAdminReview}) != 0 {
				t.Fatal("terminal ask left tasks")
			}
		})
	}
}
func TestGroupAskAuditFailureRollsBackEffectDecisionsAndTasks(t *testing.T) {
	f := newGroupAskFixture(t)
	r := createGroupAsk(t, f, f.lc, f.leader, false)
	if w := addDecision(f.sc, r.ID, "subject", "approved"); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	action := audit.AddCompleted
	name := "group-ask:audit-failure"
	if err := f.app.DB.Callback().Create().Before("gorm:create").Register(name, func(db *gorm.DB) {
		if row, ok := db.Statement.Dest.(*models.AuditLog); ok && row.Action == action {
			db.AddError(errors.New("injected add audit failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer f.app.DB.Callback().Create().Remove(name)
	if w := addDecision(f.ac, r.ID, "admin", "approved"); w.Code != 500 {
		t.Fatalf("audit failure: %d %s", w.Code, w.Body.String())
	}
	saved := savedAsk(t, f, r.ID)
	if saved.Status != "pending" || saved.AdminDecision != "pending" || saved.CompletedTargetID != nil || contactCount(t, f.app, &models.Membership{}, "group_id = ? AND user_id = ?", f.group.ID, f.subject.ID) != 0 {
		t.Fatal("audit failure leaked completion")
	}
	if contactCount(t, f.app, &models.Notification{}, "subject_id = ? AND kind = ? AND resolved_at IS NULL", r.ID, notify.AddAdminReview) != 1 {
		t.Fatal("audit failure resolved admin task")
	}
	action = audit.AddEnded
	if err := f.app.DB.Create(&models.Membership{UserID: f.admin.ID, GroupID: f.group.ID, Role: models.MembershipLeader, Status: "active"}).Error; err != nil {
		t.Fatal(err)
	}
	if w := f.ac.do("PUT", "/api/v1/groups/"+itoa(f.group.ID)+"/members/"+itoa(f.leader.ID)+"/role", map[string]any{"role": "member"}, true); w.Code != 500 {
		t.Fatalf("end audit failure: %d %s", w.Code, w.Body.String())
	}
	if savedAsk(t, f, r.ID).Status != "pending" || !relIn(f.app.DB, f.leader.ID, f.group.ID).Leader {
		t.Fatal("lifecycle failure did not roll back domain mutation")
	}
	action = ""
	if w := addDecision(f.ac, r.ID, "admin", "approved"); w.Code != 200 {
		t.Fatalf("retry: %d %s", w.Code, w.Body.String())
	}
	if savedAsk(t, f, r.ID).Status != "granted" {
		t.Fatal("successful retry not granted")
	}
}
func TestGroupAskCapIncludesContactRequests(t *testing.T) {
	f := newContactAPIFixture(t, true)
	f.app.Config.SessionSecret = []byte("group-ask-test-secret")
	for i := 0; i < 9; i++ {
		f.create(t, "email", "")
	}
	r := createGroupAsk(t, f, f.lc, f.leader, false)
	if r.Status != "pending" {
		t.Fatal("not pending")
	}
	token, err := f.app.newPeopleSelection(f.leader.ID, f.subject.ID, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if w := f.lc.do("POST", "/api/v1/add-requests", map[string]any{"selectionId": token, "contextType": "group", "contextId": f.group.ID}, true); w.Code != 409 || !strings.Contains(w.Body.String(), `"code":"request_open_limit"`) {
		t.Fatalf("mixed cap: %d %s", w.Code, w.Body.String())
	}
	if w := f.lc.do("POST", "/api/v1/contact-requests", map[string]any{"subjectUserId": f.subject.ID, "contextType": "group", "contextId": f.group.ID, "fields": "phone"}, true); w.Code != 409 || !strings.Contains(w.Body.String(), `"code":"request_open_limit"`) {
		t.Fatalf("reverse mixed cap: %d %s", w.Code, w.Body.String())
	}
}

func TestGroupAskApprovedCapacityPendingStillExpires(t *testing.T) {
	f := newGroupAskFixture(t)
	r := createGroupAsk(t, f, f.lc, f.leader, false)
	if err := f.app.DB.Model(f.group).Update("member_limit", 1).Error; err != nil {
		t.Fatal(err)
	}
	for _, step := range []struct {
		c *apiClient
		p string
	}{{f.sc, "subject"}, {f.ac, "admin"}} {
		if w := addDecision(step.c, r.ID, step.p, "approved"); w.Code != 200 {
			t.Fatal(w.Body.String())
		}
	}
	before := savedAsk(t, f, r.ID)
	if before.Status != "pending" || before.AdminDecision != "approved" || before.SubjectDecision != "approved" {
		t.Fatalf("not approved/pending: %+v", before)
	}
	if err := f.app.sweepContacts(t.Context(), time.Now().Add(15*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := f.app.DB.Model(f.group).Update("member_limit", 2).Error; err != nil {
		t.Fatal(err)
	}
	if w := f.lc.do("POST", "/api/v1/add-requests/"+itoa(r.ID)+"/complete", nil, true); w.Code != 403 {
		t.Fatalf("expired capacity request resurrected: %d", w.Code)
	}
	after := savedAsk(t, f, r.ID)
	if after.Status != "expired" || !after.ExpiresAt.Equal(before.ExpiresAt) || after.CompletedTargetID != nil {
		t.Fatalf("expiry changed contract: %+v", after)
	}
}

// ---------------------------------------------------------------------------
// R4-CTX: the context-bound contact transaction (contactMutationCtx).
//
// An App whose handle goes through the fault proxy (internal/testdb/pgproxy.go)
// runs the call; the shared handle is the control and the observer. Nothing
// here equates the call's return with the server letting go: each test polls
// pg_locks and pg_stat_activity for the backend the proxy log names. Tests that
// set the package bounds restore them and stay serial (no root-package test
// uses t.Parallel).
// ---------------------------------------------------------------------------

func setContactBounds(t *testing.T, reserve, serverCap time.Duration) {
	t.Helper()
	oldReserve, oldCap := contactCommitReserve, contactServerCap
	contactCommitReserve, contactServerCap = reserve, serverCap
	t.Cleanup(func() { contactCommitReserve, contactServerCap = oldReserve, oldCap })
}

type ctxRig struct {
	t     *testing.T
	app   *App // the control and observer, straight to the server
	papp  *App // the same app through the proxy
	proxy *testdb.Proxy
}

func newCtxRigFor(t *testing.T, app *App) *ctxRig {
	t.Helper()
	var name string
	if err := app.DB.Raw("SELECT current_database()").Scan(&name).Error; err != nil {
		t.Fatal(err)
	}
	proxy := testdb.NewProxy(t)
	db, err := proxy.Open(name)
	if err != nil {
		t.Fatal(err)
	}
	papp := &App{DB: db, Store: app.Store, Config: app.Config, Uploads: app.Uploads, Mailer: app.Mailer}
	return &ctxRig{t: t, app: app, papp: papp, proxy: proxy}
}

func newCtxRig(t *testing.T) *ctxRig { return newCtxRigFor(t, testApp(t)) }

// ctxCall is one contactMutationCtx call on its own goroutine.
type ctxCall struct {
	start, deadline, returned time.Time
	err                       error
}

func (r *ctxRig) call(budget time.Duration, change contactChange, fn func(*gorm.DB) error) <-chan *ctxCall {
	ch := make(chan *ctxCall, 1)
	c := &ctxCall{start: time.Now()}
	c.deadline = c.start.Add(budget)
	go func() {
		ctx, cancel := context.WithDeadline(context.Background(), c.deadline)
		defer cancel()
		c.err = r.papp.contactMutationCtx(ctx, change, fn)
		c.returned = time.Now()
		ch <- c
	}()
	return ch
}

func (r *ctxRig) await(ch <-chan *ctxCall) *ctxCall {
	r.t.Helper()
	select {
	case c := <-ch:
		return c
	case <-time.After(30 * time.Second):
		r.t.Fatal("contactMutationCtx never returned")
		return nil
	}
}

// sentinel writes one settings row, the mark of a callback that ran and was
// (or was not) rolled back.
func sentinel(ran *atomic.Bool) func(*gorm.DB) error {
	return func(tx *gorm.DB) error {
		ran.Store(true)
		return tx.Create(&models.Setting{Key: "ctx-sentinel", Value: "x"}).Error
	}
}

func (r *ctxRig) seed() string {
	return dumpRows(r.t, r.app.DB, "settings", "key", "true")
}

// pidOf polls the proxy log for the backend that ran a statement containing sql.
func (r *ctxRig) pidOf(sql string) int {
	r.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, e := range r.proxy.Find(testdb.KindStatement, "") {
			if e.PID != 0 && strings.Contains(e.SQL, sql) {
				return int(e.PID)
			}
		}
		time.Sleep(time.Millisecond)
	}
	r.t.Fatalf("no statement containing %q reached the proxy", sql)
	return 0
}

func (r *ctxRig) backendState(pid int) (state, waitType string, ok bool) {
	r.t.Helper()
	var row struct {
		State    string
		WaitType string
	}
	res := r.app.DB.Raw("SELECT coalesce(state, '') AS state, coalesce(wait_event_type, '') AS wait_type FROM pg_stat_activity WHERE pid = ?", pid).Scan(&row)
	if res.Error != nil {
		r.t.Fatal(res.Error)
	}
	return row.State, row.WaitType, res.RowsAffected > 0
}

func (r *ctxRig) lockRows(pid int, where string) int64 {
	r.t.Helper()
	var n int64
	if err := r.app.DB.Raw("SELECT count(*) FROM pg_locks WHERE pid = ? AND "+where, pid).Scan(&n).Error; err != nil {
		r.t.Fatal(err)
	}
	return n
}

// awaitReleased polls until the backend holds and awaits nothing (it is gone,
// or no lock row names it) and returns when that was first seen.
// awaitCancel waits until the proxy has logged the driver's cancel request with
// the given action (dropped or held). The cancel is sent from the driver's own
// goroutine after the deadline, so the call returning does not mean it arrived.
func (r *ctxRig) awaitCancel(action string) {
	r.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for len(r.proxy.Find(testdb.KindCancel, action)) == 0 {
		if time.Now().After(deadline) {
			r.t.Fatalf("the proxy never logged a %s cancel", action)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (r *ctxRig) awaitReleased(pid int, within time.Duration) time.Time {
	r.t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if r.lockRows(pid, "true") == 0 {
			return time.Now()
		}
		time.Sleep(10 * time.Millisecond)
	}
	r.t.Fatalf("backend %d still holds or awaits locks after %s", pid, within)
	return time.Time{}
}

func (r *ctxRig) awaitState(pid int, within time.Duration, want func(state, waitType string, ok bool) bool, what string) time.Time {
	r.t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if st, wt, ok := r.backendState(pid); want(st, wt, ok) {
			return time.Now()
		}
		time.Sleep(5 * time.Millisecond)
	}
	st, wt, ok := r.backendState(pid)
	r.t.Fatalf("backend %d never reached %s (last: %q %q present=%v)", pid, what, st, wt, ok)
	return time.Time{}
}

func (r *ctxRig) gateFree() bool {
	r.t.Helper()
	var ok bool
	if err := r.app.DB.Transaction(func(tx *gorm.DB) error {
		return tx.Raw("SELECT pg_try_advisory_xact_lock(?)", contactLockID).Scan(&ok).Error
	}); err != nil {
		r.t.Fatal(err)
	}
	return ok
}

func waiting(_, wt string, ok bool) bool { return ok && wt == "Lock" }

// statementsOf are the logged statement events (not the commit and rollback
// echoes) in order.
func (r *ctxRig) statements() []testdb.Event { return r.proxy.Find(testdb.KindStatement, "") }

// Gate-wait expiry, no faults: the call returns at its deadline and the
// server lets go of the gate soon after, by the cancel and Terminate it was
// sent.
func TestContactMutationCtxGateWaitExpiry(t *testing.T) {
	setContactBounds(t, 100*time.Millisecond, contactServerCap)
	r := newCtxRig(t)
	control := r.seed()
	release, _ := holdAdvisory(t, r.app.DB, contactLockID)
	defer release()
	var ran atomic.Bool
	ch := r.call(300*time.Millisecond, contactChange{}, sentinel(&ran))
	pid := r.pidOf("pg_advisory_xact_lock")
	r.awaitState(pid, time.Second, waiting, "waiting on the gate")
	c := r.await(ch)
	if elapsed := c.returned.Sub(c.start); elapsed > 1300*time.Millisecond {
		t.Errorf("returned after %s, want within 1.3s", elapsed)
	}
	if classifyContactErr(c.err) != outcomeTimedOut || ran.Load() {
		t.Errorf("err %v (outcome %d), callback ran %v; want timed out and not run", c.err, classifyContactErr(c.err), ran.Load())
	}
	if rollbackConfirmed(c.err) || !errors.Is(c.err, errContactRollback) {
		t.Errorf("after a context expiry the rollback is never confirmed: %v", c.err)
	}
	// D + S + I + 1s, with I the budget.
	bound := c.deadline.Sub(c.start) + contactServerCap + 300*time.Millisecond + time.Second
	released := r.awaitReleased(pid, bound)
	t.Logf("returned %s after D; backend released %s after D", c.returned.Sub(c.deadline), released.Sub(c.deadline))
	if len(r.proxy.Find(testdb.KindCancel, testdb.ActionForwarded)) != 1 || len(r.proxy.Find(testdb.KindTerminate, testdb.ActionForwarded)) != 1 {
		t.Errorf("want the cancel and Terminate forwarded: %+v", r.proxy.Events())
	}
	if got := r.seed(); got != control {
		t.Errorf("database changed:\n%s\n%s", got, control)
	}
}

// The server cap below the budget: the gate wait ends by statement_timeout
// and the ROLLBACK is answered before the call returns.
func TestContactMutationCtxServerCapBelowBudget(t *testing.T) {
	setContactBounds(t, 100*time.Millisecond, time.Second)
	r := newCtxRig(t)
	control := r.seed()
	release, _ := holdAdvisory(t, r.app.DB, contactLockID)
	var ran atomic.Bool
	c := r.await(r.call(5*time.Second, contactChange{}, sentinel(&ran)))
	elapsed := c.returned.Sub(c.start)
	if elapsed < 900*time.Millisecond || elapsed > 2500*time.Millisecond {
		t.Errorf("returned after %s, want about 1s", elapsed)
	}
	if pgCode(c.err) != "57014" || classifyContactErr(c.err) != outcomeTimedOut || !rollbackConfirmed(c.err) || ran.Load() {
		t.Errorf("err %v (code %q): want an acknowledged 57014 timed out, callback not run", c.err, pgCode(c.err))
	}
	rb := r.proxy.Find(testdb.KindRollback, testdb.ActionForwarded)
	if len(rb) != 1 || !rb[0].Time.Before(c.returned) {
		t.Errorf("want one ROLLBACK forwarded before the return: %+v", rb)
	}
	release()
	if !r.gateFree() {
		t.Error("the gate is not free after the holder let go")
	}
	st := r.statements()
	begin, cfg, gate := -1, -1, -1
	for i, e := range st {
		switch {
		case begin < 0 && strings.EqualFold(strings.TrimSpace(e.SQL), "begin"):
			begin = i
		case cfg < 0 && strings.Contains(e.SQL, "set_config('statement_timeout'"):
			cfg = i
		case gate < 0 && strings.Contains(e.SQL, "pg_advisory_xact_lock"):
			gate = i
		}
	}
	if begin < 0 || cfg != begin+1 || gate <= cfg {
		t.Errorf("statement order: begin %d, set_config %d, gate %d; want set_config right after begin and before the gate", begin, cfg, gate)
	}
	if got := r.seed(); got != control {
		t.Errorf("database changed:\n%s\n%s", got, control)
	}
}

// The three withheld-socket runs: the cancel is dropped, so only Terminate,
// FIN or the idle timeout can end the backend.
func TestContactMutationCtxCancelDroppedTerminateWithheld(t *testing.T) {
	const match = "idle_in_transaction_session_timeout"
	t.Run("gate released after the return: the idle timeout ends it", func(t *testing.T) {
		budget := 1500 * time.Millisecond
		setContactBounds(t, 100*time.Millisecond, 5*time.Second)
		r := newCtxRig(t)
		control := r.seed()
		release, _ := holdAdvisory(t, r.app.DB, contactLockID)
		r.proxy.Arm(testdb.Faults{Match: match, Cancel: testdb.CancelDrop, Close: testdb.CloseWithholdAll})
		var ran atomic.Bool
		ch := r.call(budget, contactChange{}, sentinel(&ran))
		pid := r.pidOf("pg_advisory_xact_lock")
		c := r.await(ch)
		if _, wt, ok := r.backendState(pid); !ok || wt != "Lock" {
			t.Fatalf("at the return the backend should still wait on the gate: present=%v wait %q", ok, wt)
		}
		r.awaitCancel(testdb.ActionDropped)
		g := time.Now()
		release()
		r.awaitState(pid, time.Second, func(s, _ string, ok bool) bool { return ok && s == "idle in transaction" }, "idle in transaction")
		ended := r.awaitState(pid, budget+2*time.Second, func(_, _ string, ok bool) bool { return !ok }, "gone")
		if d := ended.Sub(g); d < budget-150*time.Millisecond || d > budget+time.Second {
			t.Errorf("the backend ended %s after the gate was released, want about the idle timeout (%s)", d, budget)
		}
		for _, e := range r.statements() {
			if e.Time.After(c.deadline.Add(50 * time.Millisecond)) {
				t.Errorf("a statement was forwarded after D: %+v", e)
			}
		}
		if len(r.proxy.Find(testdb.KindCommit, "")) != 0 || ran.Load() {
			t.Errorf("a COMMIT reached the proxy or the callback ran")
		}
		for _, kind := range []string{testdb.KindCancel, testdb.KindTerminate, testdb.KindFIN} {
			if f := r.proxy.Find(kind, testdb.ActionForwarded); len(f) != 0 {
				t.Errorf("%s forwarded: %+v", kind, f)
			}
		}
		if got := r.seed(); got != control {
			t.Errorf("database changed")
		}
	})
	t.Run("gate never released: the cap, then the idle timeout", func(t *testing.T) {
		budget := time.Second
		setContactBounds(t, 100*time.Millisecond, 2*time.Second)
		r := newCtxRig(t)
		release, _ := holdAdvisory(t, r.app.DB, contactLockID)
		defer release()
		r.proxy.Arm(testdb.Faults{Match: match, Cancel: testdb.CancelDrop, Close: testdb.CloseWithholdAll})
		var ran atomic.Bool
		ch := r.call(budget, contactChange{}, sentinel(&ran))
		pid := r.pidOf("pg_advisory_xact_lock")
		c := r.await(ch)
		aborted := r.awaitState(pid, 5*time.Second, func(s, _ string, ok bool) bool { return ok && s == "idle in transaction (aborted)" }, "idle in transaction (aborted)")
		if d := aborted.Sub(c.start); d < 1800*time.Millisecond {
			t.Errorf("the statement ended %s after the start, want about the cap (2s)", d)
		}
		// The idle timeout is a session setting, so the abort does not revert
		// it: the aborted backend holds nothing and ends about I after the
		// abort, with no cancel, Terminate or FIN having reached it.
		bound := c.start.Add(budget + contactServerCap + budget + time.Second)
		ended := r.awaitState(pid, time.Until(bound), func(_, _ string, ok bool) bool { return !ok }, "gone (idle timeout)")
		if d := ended.Sub(aborted); d < budget-300*time.Millisecond {
			t.Errorf("the backend ended %s after the abort, want about the idle timeout (%s)", d, budget)
		}
		if got := r.lockRows(pid, "true"); got != 0 {
			t.Errorf("a gone backend still names %d lock rows", got)
		}
		for _, kind := range []string{testdb.KindCancel, testdb.KindTerminate, testdb.KindFIN} {
			if f := r.proxy.Find(kind, testdb.ActionForwarded); len(f) != 0 {
				t.Errorf("%s forwarded: %+v", kind, f)
			}
		}
	})
	t.Run("Terminate forwarded, FIN held: Terminate ends it", func(t *testing.T) {
		budget := 1500 * time.Millisecond
		setContactBounds(t, 100*time.Millisecond, 5*time.Second)
		r := newCtxRig(t)
		release, _ := holdAdvisory(t, r.app.DB, contactLockID)
		r.proxy.Arm(testdb.Faults{Match: match, Cancel: testdb.CancelDrop, Close: testdb.CloseWithholdFIN})
		var ran atomic.Bool
		ch := r.call(budget, contactChange{}, sentinel(&ran))
		pid := r.pidOf("pg_advisory_xact_lock")
		r.await(ch)
		r.awaitCancel(testdb.ActionDropped)
		g := time.Now()
		release()
		ended := r.awaitState(pid, budget, func(_, _ string, ok bool) bool { return !ok }, "gone before the idle timeout")
		if d := ended.Sub(g); d > budget/2 {
			t.Errorf("ended %s after the gate was released, want right after (Terminate), not the idle timeout", d)
		}
		if f := r.proxy.Find(testdb.KindTerminate, testdb.ActionForwarded); len(f) != 1 {
			t.Errorf("want Terminate forwarded: %+v", r.proxy.Events())
		}
		// The proxy sees the client's close just after the server has gone
		// on the Terminate, so give the FIN event a moment to be logged.
		var held []testdb.Event
		for stop := time.Now().Add(2 * time.Second); time.Now().Before(stop); time.Sleep(10 * time.Millisecond) {
			if held = r.proxy.Find(testdb.KindFIN, testdb.ActionHeld); len(held) > 0 {
				break
			}
		}
		if len(held) != 1 {
			t.Errorf("want the FIN held: %+v", r.proxy.Events())
		}
		if f := r.proxy.Find(testdb.KindFIN, testdb.ActionForwarded); len(f) != 0 {
			t.Errorf("the FIN was forwarded: %+v", f)
		}
	})
}

// The cancel arrives late: the backend keeps waiting, then ends its wait with
// 57014 once the cancel lands, and the idle timeout (Terminate is withheld)
// lets go of everything.
func TestContactMutationCtxCancelDelayed(t *testing.T) {
	const match = "idle_in_transaction_session_timeout"
	budget := 1500 * time.Millisecond
	t.Run("in the gate wait", func(t *testing.T) {
		setContactBounds(t, 100*time.Millisecond, 5*time.Second)
		r := newCtxRig(t)
		control := r.seed()
		release, _ := holdAdvisory(t, r.app.DB, contactLockID)
		defer release()
		r.proxy.Arm(testdb.Faults{Match: match, Cancel: testdb.CancelHold, Close: testdb.CloseWithholdAll})
		var ran atomic.Bool
		ch := r.call(budget, contactChange{}, sentinel(&ran))
		pid := r.pidOf("pg_advisory_xact_lock")
		c := r.await(ch)
		if _, wt, ok := r.backendState(pid); !ok || wt != "Lock" {
			t.Fatalf("at the return the backend should still wait: present=%v wait %q", ok, wt)
		}
		r.awaitCancel(testdb.ActionHeld)
		released := time.Now()
		if !c.returned.Before(released) {
			t.Error("the call should return before the cancel is released")
		}
		r.proxy.ReleaseCancel()
		r.awaitState(pid, 2*time.Second, func(s, _ string, ok bool) bool { return ok && s == "idle in transaction (aborted)" }, "aborted by the cancel")
		end := r.awaitReleased(pid, c.deadline.Sub(c.start)+contactServerCap+budget+time.Second)
		t.Logf("returned %s after D; locks released %s after the cancel", c.returned.Sub(c.deadline), end.Sub(released))
		r.awaitState(pid, time.Until(c.start.Add(budget+contactServerCap+budget+time.Second)), func(_, _ string, ok bool) bool { return !ok }, "gone (idle timeout)")
		cancels, terms := r.proxy.Find(testdb.KindCancel, testdb.ActionForwarded), r.proxy.Find(testdb.KindTerminate, "")
		if len(cancels) != 1 || len(terms) == 0 || terms[0].Time.Before(cancels[0].Time) {
			t.Errorf("want the Terminate seen only after the cancel completed: cancel %+v, Terminate %+v", cancels, terms)
		}
		if got := r.seed(); got != control || ran.Load() {
			t.Errorf("database changed or the callback ran")
		}
	})
	t.Run("in reconciliation", func(t *testing.T) {
		setContactBounds(t, 100*time.Millisecond, 5*time.Second)
		f := newContactAPIFixture(t, false)
		req := f.create(t, "email", "")
		r := newCtxRigFor(t, f.app)
		blocker, _ := beginOwnedTx(t, r.app.DB)
		if err := blocker.Exec("SELECT id FROM contact_requests WHERE id = ? FOR UPDATE", req.ID).Error; err != nil {
			t.Fatal(err)
		}
		r.proxy.Arm(testdb.Faults{Match: match, Cancel: testdb.CancelHold, Close: testdb.CloseWithholdAll})
		var ran atomic.Bool
		ch := r.call(budget, contactChange{UserID: f.subject.ID}, sentinel(&ran))
		pid := r.pidOf("FROM \"contact_requests\"")
		r.awaitBlocked(pid)
		c := r.await(ch)
		if r.lockRows(pid, "locktype = 'advisory' AND granted") != 1 || r.lockRows(pid, "locktype = 'transactionid' AND granted") == 0 {
			t.Error("after the return the backend should still hold the gate and the callback's row lock")
		}
		r.proxy.ReleaseCancel()
		r.awaitReleased(pid, c.deadline.Sub(c.start)+contactServerCap+budget+time.Second)
		if n := contactCount(t, f.app, &models.Setting{}, "key = ?", "ctx-sentinel"); n != 0 || !ran.Load() {
			t.Errorf("sentinel rows %d (callback ran %v), want the write rolled back", n, ran.Load())
		}
	})
}

func (r *ctxRig) awaitBlocked(pid int) {
	r.t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		var blockers string
		if err := r.app.DB.Raw("SELECT pg_blocking_pids(?)::text", pid).Scan(&blockers).Error; err != nil {
			r.t.Fatal(err)
		}
		if blockers != "{}" {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	r.t.Fatalf("backend %d was never blocked", pid)
}

// Reconciliation expiry with no faults: nothing of the callback survives and
// the gate frees once the server has let go.
func TestContactMutationCtxReconciliationExpiry(t *testing.T) {
	setContactBounds(t, 100*time.Millisecond, 5*time.Second)
	f := newContactAPIFixture(t, false)
	req := f.create(t, "email", "")
	r := newCtxRigFor(t, f.app)
	blocker, _ := beginOwnedTx(t, r.app.DB)
	if err := blocker.Exec("SELECT id FROM contact_requests WHERE id = ? FOR UPDATE", req.ID).Error; err != nil {
		t.Fatal(err)
	}
	var ran atomic.Bool
	ch := r.call(time.Second, contactChange{UserID: f.subject.ID}, sentinel(&ran))
	pid := r.pidOf("FROM \"contact_requests\"")
	r.awaitBlocked(pid)
	c := r.await(ch)
	if classifyContactErr(c.err) != outcomeTimedOut {
		t.Errorf("err %v, want timed out", c.err)
	}
	bound := c.deadline.Add(contactServerCap + time.Second + time.Second)
	for !r.gateFree() {
		if time.Now().After(bound) {
			t.Fatal("the gate was not released within D + S + I + 1s")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if n := contactCount(t, f.app, &models.Setting{}, "key = ?", "ctx-sentinel"); n != 0 || !ran.Load() {
		t.Errorf("sentinel rows %d (callback ran %v), want the write rolled back", n, ran.Load())
	}
}

// The default 2 s reserve and the classifier.
func TestContactMutationCtxReserveAndClassifier(t *testing.T) {
	t.Run("reserve left after reconciliation", func(t *testing.T) {
		r := newCtxRig(t)
		control := r.seed()
		proceed := make(chan struct{})
		time.AfterFunc(1500*time.Millisecond, func() { close(proceed) })
		var ran atomic.Bool
		c := r.await(r.call(3*time.Second, contactChange{}, func(tx *gorm.DB) error {
			<-proceed
			return sentinel(&ran)(tx)
		}))
		if !errors.Is(c.err, errCommitReserve) || classifyContactErr(c.err) != outcomeTimedOut || !rollbackConfirmed(c.err) {
			t.Errorf("err %v, want errCommitReserve, timed out, rollback confirmed", c.err)
		}
		if len(r.proxy.Find(testdb.KindCommit, "")) != 0 {
			t.Error("a COMMIT was sent")
		}
		if got := r.seed(); got != control {
			t.Error("the sentinel was stored")
		}
	})
	t.Run("budget under the reserve sends nothing", func(t *testing.T) {
		r := newCtxRig(t)
		before := len(r.proxy.Events())
		var ran atomic.Bool
		c := r.await(r.call(1500*time.Millisecond, contactChange{}, sentinel(&ran)))
		if !errors.Is(c.err, errCommitReserve) || ran.Load() || len(r.proxy.Events()) != before {
			t.Errorf("err %v, ran %v, events %d -> %d; want errCommitReserve and nothing sent", c.err, ran.Load(), before, len(r.proxy.Events()))
		}
	})
	t.Run("classification table", func(t *testing.T) {
		pg := func(code string) error { return &pgconn.PgError{Code: code} }
		for name, cause := range map[string]error{
			"deadline": context.DeadlineExceeded, "canceled": context.Canceled, "57014": pg("57014"),
			"unexpected EOF": io.ErrUnexpectedEOF, "23514": pg("23514"),
		} {
			if got := classifyContactErr(&contactCommitError{Cause: cause}); got != outcomeUnknown {
				t.Errorf("commit error wrapping %s: outcome %d, want unknown", name, got)
			}
			if rollbackConfirmed(&contactCommitError{Cause: cause}) {
				t.Errorf("a commit error wrapping %s counts as a confirmed rollback", name)
			}
		}
		for name, tc := range map[string]struct {
			err  error
			want contactOutcome
		}{
			"reserve":  {errCommitReserve, outcomeTimedOut},
			"deadline": {context.DeadlineExceeded, outcomeTimedOut},
			"canceled": {context.Canceled, outcomeTimedOut},
			"57014":    {pg("57014"), outcomeTimedOut},
			"55P03":    {pg("55P03"), outcomeFailed},
			"plain":    {errors.New("boom"), outcomeFailed},
		} {
			if got := classifyContactErr(tc.err); got != tc.want {
				t.Errorf("%s bare: outcome %d, want %d", name, got, tc.want)
			}
			if !rollbackConfirmed(tc.err) {
				t.Errorf("%s bare: rollback should count as confirmed", name)
			}
			wrapped := &contactRollbackError{Cause: tc.err, Rollback: sql.ErrTxDone}
			if got := classifyContactErr(wrapped); got != tc.want {
				t.Errorf("%s in a rollback error: outcome %d, want %d", name, got, tc.want)
			}
			if rollbackConfirmed(wrapped) || !errors.Is(wrapped, errContactRollback) || !errors.Is(wrapped, tc.err) {
				t.Errorf("%s in a rollback error: confirmed %v, Is rollback %v, Is cause %v", name, rollbackConfirmed(wrapped), errors.Is(wrapped, errContactRollback), errors.Is(wrapped, tc.err))
			}
		}
	})
}

// Rollback completion is separate from the outcome.
func TestContactMutationCtxRollbackCompletion(t *testing.T) {
	setContactBounds(t, 100*time.Millisecond, 5*time.Second)
	lockNotAvailable := &pgconn.PgError{Code: "55P03"}
	run := func(t *testing.T, fault testdb.Faults) (*ctxRig, *ctxCall) {
		r := newCtxRig(t)
		fault.Match = "set_config('statement_timeout'"
		r.proxy.Arm(fault)
		return r, r.await(r.call(5*time.Second, contactChange{}, func(*gorm.DB) error { return lockNotAvailable }))
	}
	for name, fault := range map[string]testdb.Faults{
		"closed without forwarding":         {Rollback: testdb.RollbackCloseUnforwarded},
		"forwarded, then closed unanswered": {Rollback: testdb.RollbackForwardThenClose},
	} {
		t.Run(name, func(t *testing.T) {
			r, c := run(t, fault)
			if !errors.Is(c.err, errContactRollback) || pgCode(c.err) != "55P03" || classifyContactErr(c.err) != outcomeFailed || rollbackConfirmed(c.err) {
				t.Errorf("err %v: want a rollback error around 55P03, failed, not confirmed", c.err)
			}
			pid := r.pidOf("set_config('statement_timeout'")
			r.awaitReleased(pid, c.deadline.Sub(c.start)+contactServerCap+5*time.Second)
		})
	}
	t.Run("forwarded and answered", func(t *testing.T) {
		_, c := run(t, testdb.Faults{})
		if c.err != lockNotAvailable || !rollbackConfirmed(c.err) || classifyContactErr(c.err) != outcomeFailed {
			t.Errorf("err %v: want the 55P03 unchanged and confirmed", c.err)
		}
	})
}

// The helpers: no deadline installs nothing, and withStatementTimeout never
// raises what contactServerCap set.
func TestContactMutationCtxHelpers(t *testing.T) {
	t.Run("no deadline sends no set_config", func(t *testing.T) {
		r := newCtxRig(t)
		if err := r.papp.contactMutation(contactChange{}, func(tx *gorm.DB) error { return tx.Exec("SELECT 1").Error }); err != nil {
			t.Fatal(err)
		}
		for _, e := range r.statements() {
			if strings.Contains(e.SQL, "set_config") {
				t.Errorf("a set_config was sent: %q", e.SQL)
			}
		}
	})
	show := func(t *testing.T, tx *gorm.DB) string {
		t.Helper()
		var v string
		if err := tx.Raw("SHOW statement_timeout").Scan(&v).Error; err != nil {
			t.Fatal(err)
		}
		return v
	}
	t.Run("inside the call the recipient bound is the smaller", func(t *testing.T) {
		setContactBounds(t, 100*time.Millisecond, time.Second)
		app := testApp(t)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		var inside, after string
		if err := app.contactMutationCtx(ctx, contactChange{}, func(tx *gorm.DB) error {
			if err := withStatementTimeout(tx, erasureRecipientWait, func() error {
				inside = show(t, tx)
				return nil
			}); err != nil {
				return err
			}
			after = show(t, tx)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if inside != "1s" || after != "1s" {
			t.Errorf("inside %q, after %q, want 1s both (the cap is never raised and is restored)", inside, after)
		}
	})
	t.Run("outside the call the bound is as before", func(t *testing.T) {
		app := testApp(t)
		var inside, after string
		if err := app.contactMutation(contactChange{}, func(tx *gorm.DB) error {
			if err := withStatementTimeout(tx, erasureRecipientWait, func() error {
				inside = show(t, tx)
				return nil
			}); err != nil {
				return err
			}
			after = show(t, tx)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if inside != "20s" || after == "20s" {
			t.Errorf("inside %q, after %q, want 20s inside and the session value after", inside, after)
		}
	})
}

// Exercise database/sql's transaction cancellation and connection release with
// synthetic release barriers and a real PostgreSQL/pgx pool.
func TestContactMutationConnCancellationCleanup(t *testing.T) {
	for _, automatic := range []bool{true, false} {
		name := "explicit rollback cancels context"
		if automatic {
			name = "automatic rollback still releasing"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			c := &contactCleanupConn{
				rollbackStarted: make(chan struct{}), releaseRollback: make(chan struct{}), closed: make(chan struct{}),
				cancelCloses: automatic,
			}
			var release sync.Once
			unblock := func() { release.Do(func() { close(c.releaseRollback) }) }
			t.Cleanup(unblock)
			sqlDB := sql.OpenDB(&contactCleanupConnector{conn: c})
			t.Cleanup(func() { unblock(); _ = sqlDB.Close() })
			db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{
				DisableAutomaticPing: true, Logger: logger.Discard,
			})
			if err != nil {
				t.Fatal(err)
			}
			if !automatic {
				// The caller owns this rollback even though cancellation happens
				// before it returns. It must still discard the session setting.
				c.onRollback = cancel
				unblock()
			}
			app := &App{DB: db}
			cause := errors.New("callback failed")
			if automatic {
				cause = context.Canceled
			}
			done := make(chan error, 1)
			go func() {
				done <- app.contactMutationCtx(ctx, contactChange{}, func(*gorm.DB) error {
					if automatic {
						cancel()
						<-c.rollbackStarted
					}
					return cause
				})
			}()
			select {
			case err = <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("contact mutation waited for cancellation cleanup")
			}
			if !errors.Is(err, cause) || rollbackConfirmed(err) == automatic {
				t.Fatalf("error %v: cause %v, rollback confirmed %v, automatic %v", err, cause, rollbackConfirmed(err), automatic)
			}
			if automatic {
				if !errors.Is(err, errContactRollback) || classifyContactErr(err) != outcomeTimedOut {
					t.Fatalf("automatic rollback outcome = %v", err)
				}
				select {
				case <-c.closed:
					t.Fatal("connection closed before its rollback finished")
				default:
				}
			}
			unblock()
			select {
			case <-c.closed:
			case <-time.After(5 * time.Second):
				t.Fatal("connection with a session timeout was not discarded")
			}
			// Observe physical close before pool cleanup: closing the pool
			// first would hide returning the session to it on the explicit path.
		})
	}
	t.Run("real pgx pool", testContactMutationConnCancellationCleanupPGX)
}

// Unlike the synthetic release-race cases above, this uses pgx's actual
// database/sql driver, PostgreSQL, and the wire proxy. Cancellation of an idle
// callback never sends a ROLLBACK on the wire: pgx rejects the already-canceled
// context and closes the connection. Its query tracer provides the barrier
// before that rejection, after database/sql has claimed automatic rollback.
func testContactMutationConnCancellationCleanupPGX(t *testing.T) {
	for _, automatic := range []bool{true, false} {
		name := "commit canceled with reply held"
		if automatic {
			name = "ErrTxDone while automatic rollback is in flight"
		}
		t.Run(name, func(t *testing.T) {
			database := testdb.Fresh(t, true)
			proxy := testdb.NewProxy(t)
			dsn, err := testdb.URL(database)
			if err != nil {
				t.Fatal(err)
			}
			u, err := url.Parse(dsn)
			if err != nil {
				t.Fatal(err)
			}
			u.Host = proxy.Addr()
			query := u.Query()
			query.Set("sslmode", "disable")
			u.RawQuery = query.Encode()
			config, err := pgx.ParseConfig(u.String())
			if err != nil {
				t.Fatal(err)
			}
			trace := &contactCleanupPGXTrace{
				rollbackStarted: make(chan error, 1), releaseRollback: make(chan struct{}),
			}
			config.Tracer = trace
			sqlDB := stdlib.OpenDB(*config)
			const poolSize = 3
			sqlDB.SetMaxOpenConns(poolSize)
			sqlDB.SetMaxIdleConns(poolSize)
			watch, stop := context.WithTimeout(context.Background(), 30*time.Second)
			defer stop()
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			var release sync.Once
			unblock := func() { release.Do(func() { close(trace.releaseRollback) }) }
			t.Cleanup(func() {
				cancel()
				unblock()
				proxy.ReleaseReply()
				proxy.ReleaseClose()
				_ = sqlDB.Close()
			})
			db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{
				DisableAutomaticPing: true, Logger: logger.Discard,
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := db.Use(txguard.Plugin{}); err != nil {
				t.Fatal(err)
			}
			app := &App{DB: db}
			take := func() *sql.Conn {
				t.Helper()
				conn, err := sqlDB.Conn(watch)
				if err != nil {
					t.Fatalf("acquire a pool slot after cancellation cleanup: %v", err)
				}
				t.Cleanup(func() { _ = conn.Close() })
				return conn
			}
			show := func(conn *sql.Conn) (uint32, string) {
				t.Helper()
				var pid uint32
				var timeout string
				if err := conn.QueryRowContext(watch, "SELECT pg_backend_pid()").Scan(&pid); err != nil {
					t.Fatal(err)
				}
				if err := conn.QueryRowContext(watch, "SHOW idle_in_transaction_session_timeout").Scan(&timeout); err != nil {
					t.Fatal(err)
				}
				return pid, timeout
			}
			// Read the configured server default on pristine sessions, rather
			// than assuming it is zero. Keep two slots occupied throughout the
			// mutation so acquiring the third must wait for its release.
			var held []*sql.Conn
			var serverDefault string
			var mutationPID uint32
			for i := range poolSize {
				conn := take()
				held = append(held, conn)
				pid, timeout := show(conn)
				if i == 0 {
					serverDefault = timeout
				} else if timeout != serverDefault {
					t.Fatalf("pristine backend %d timeout %q, want server default %q", pid, timeout, serverDefault)
				}
				mutationPID = pid
			}
			if err := held[poolSize-1].Close(); err != nil {
				t.Fatal(err)
			}
			held = held[:poolSize-1]
			proxy.Arm(testdb.Faults{
				Match: "set_config('idle_in_transaction_session_timeout'", Commit: testdb.CommitHoldReply,
				Cancel: testdb.CancelDrop, Close: testdb.CloseWithholdAll,
			})
			entered := make(chan struct{}, 1)
			done := make(chan error, 1)
			go func() {
				done <- app.contactMutationCtx(ctx, contactChange{}, func(tx *gorm.DB) error {
					var pid uint32
					var timeout string
					if err := tx.Raw("SELECT pg_backend_pid(), current_setting('idle_in_transaction_session_timeout')").Row().Scan(&pid, &timeout); err != nil {
						return err
					}
					if pid != mutationPID || timeout == serverDefault {
						return fmt.Errorf("callback backend %d timeout %q, want backend %d with a non-default timeout", pid, timeout, mutationPID)
					}
					entered <- struct{}{}
					if !automatic {
						return nil
					}
					<-ctx.Done()
					select {
					case rollbackContextErr := <-trace.rollbackStarted:
						if !errors.Is(rollbackContextErr, context.Canceled) {
							return fmt.Errorf("pgx automatic rollback context: %v", rollbackContextErr)
						}
					case <-watch.Done():
						return errors.New("pgx automatic rollback did not start")
					}
					return context.Canceled
				})
			}()
			select {
			case <-entered:
			case err := <-done:
				t.Fatalf("mutation ended before its callback barrier: %v", err)
			case <-watch.Done():
				t.Fatal("mutation did not enter its callback")
			}
			if !automatic {
				// pgx uses a simple Query for COMMIT, and database/sql has
				// already claimed Commit before this proxy event. Hold its
				// reply until cleanup, so cancellation must end the driver call.
				// Poll an observed protocol event; elapsed time never orders it.
				tick := time.NewTicker(time.Millisecond)
				defer tick.Stop()
				for len(proxy.Find(testdb.KindCommit, testdb.ActionHeld)) == 0 {
					select {
					case err := <-done:
						t.Fatalf("mutation ended before COMMIT was held: %v", err)
					case <-watch.Done():
						t.Fatal("COMMIT never reached the proxy")
					case <-tick.C:
					}
				}
			}
			cancel()
			select {
			case err = <-done:
			case <-watch.Done():
				t.Fatal("mutation waited for cancellation cleanup")
			}
			if automatic {
				var rollbackErr *contactRollbackError
				if !errors.As(err, &rollbackErr) || !errors.Is(rollbackErr.Rollback, sql.ErrTxDone) || !errors.Is(err, context.Canceled) || classifyContactErr(err) != outcomeTimedOut {
					t.Fatalf("automatic rollback outcome = %v, want ErrTxDone and canceled callback", err)
				}
				if got := sqlDB.Stats().InUse; got != poolSize {
					t.Fatalf("%d occupied pool slots while pgx rollback is held, want %d", got, poolSize)
				}
			} else {
				var commitErr *contactCommitError
				if !errors.As(err, &commitErr) || !errors.Is(commitErr.Cause, context.Canceled) || classifyContactErr(err) != outcomeUnknown {
					t.Fatalf("commit cancellation outcome = %v, want unknown commit wrapping cancellation", err)
				}
			}
			if rollbackConfirmed(err) || len(proxy.Find(testdb.KindRollback, "")) != 0 {
				t.Fatalf("cancellation claimed a confirmed or wire rollback: %v, events %+v", err, proxy.Events())
			}
			unblock()
			held = append(held, take())
			// This checkout can complete only after the affected slot was
			// returned or discarded. Never close the pool to make it pass.
			idlePIDs := make(map[uint32]bool, poolSize)
			for _, conn := range held {
				pid, timeout := show(conn)
				if timeout != serverDefault {
					t.Fatalf("backend %d leaked timeout %q, want server default %q", pid, timeout, serverDefault)
				}
				idlePIDs[pid] = true
				if err := conn.Close(); err != nil {
					t.Fatal(err)
				}
			}
			if stats := sqlDB.Stats(); stats.Idle != poolSize || stats.InUse != 0 {
				t.Fatalf("pool not wholly idle after cleanup: %+v", stats)
			}
			// Retain each checkout to defeat LIFO reuse of just one clean
			// session. Check every idle backend, including the affected slot.
			for range poolSize {
				pid, timeout := show(take())
				if !idlePIDs[pid] || timeout != serverDefault {
					t.Fatalf("reused backend %d timeout %q, want an unvisited idle backend with server default %q", pid, timeout, serverDefault)
				}
				delete(idlePIDs, pid)
			}
			if stats := sqlDB.Stats(); len(idlePIDs) != 0 || stats.Idle != 0 || stats.InUse != poolSize {
				t.Fatalf("did not inspect every reused idle connection: remaining %v, stats %+v", idlePIDs, stats)
			}
		})
	}
}

// The tracer only delays entry into pgx's real rollback. It does not substitute
// an error, change the canceled context, or close the connection itself.
type contactCleanupPGXTrace struct {
	rollbackStarted chan error
	releaseRollback chan struct{}
}

func (c *contactCleanupPGXTrace) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if data.SQL == "rollback" {
		c.rollbackStarted <- ctx.Err()
		<-c.releaseRollback
	}
	return ctx
}

func (*contactCleanupPGXTrace) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

type contactCleanupConnector struct {
	conn *contactCleanupConn
}

func (c *contactCleanupConnector) Connect(context.Context) (driver.Conn, error) {
	return c.conn, nil
}
func (*contactCleanupConnector) Driver() driver.Driver { return contactCleanupDriver{} }

type contactCleanupDriver struct{}

func (contactCleanupDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("use the test connector")
}

type contactCleanupConn struct {
	rollbackStarted chan struct{}
	releaseRollback chan struct{}
	closed          chan struct{}
	onRollback      func()
	cancelCloses    bool
	closeOnce       sync.Once
}

func (*contactCleanupConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unexpected prepare")
}
func (c *contactCleanupConn) Close() error {
	c.closeOnce.Do(func() { close(c.closed) })
	return nil
}
func (c *contactCleanupConn) Begin() (driver.Tx, error) {
	return c, nil
}
func (c *contactCleanupConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	return c, nil
}
func (*contactCleanupConn) ExecContext(context.Context, string, []driver.NamedValue) (driver.Result, error) {
	return driver.RowsAffected(0), nil
}
func (*contactCleanupConn) Commit() error { return errors.New("unexpected commit") }
func (c *contactCleanupConn) Rollback() error {
	close(c.rollbackStarted)
	if c.onRollback != nil {
		c.onRollback()
	}
	<-c.releaseRollback
	if c.cancelCloses {
		// pgx Rollback on a canceled context fails and closes its connection
		// before database/sql releases it, even if Conn.Close owns the release.
		_ = c.Close()
		return context.Canceled
	}
	return nil
}

// ---------------------------------------------------------------------------
// T07B-1b: the actor seal against household conversion (R5-C01, R6-C02,
// R7-C01, R7-C02). TestActorSealVsConversion runs the real DELETE and
// invitation-acceptance routes with the seams of the design (deleteBeforeSeal,
// conversionAfterUserLocks, deleteBeforeRetry, sealAttempts). EVIDENCE RULES,
// each applied before any retry count, response, ledger state or settle answer
// is asserted: (E1) the waiter is the unique backend waiting on the holder's
// pid, with the statement the schedule names and an ungranted pg_locks row;
// (E2) a parked holder is idle in transaction and waits on nothing; (E3) held
// modes come from NOWAIT probes on a third connection, each rolled back at once;
// (E4) later entries of deleteBeforeSeal re-run (E2) and the probe on A only.
// ---------------------------------------------------------------------------

// probeNoWait reports whether a third connection can take the row lock now.
// FOR KEY SHARE fails only while someone holds FOR UPDATE; FOR SHARE fails
// while someone holds FOR NO KEY UPDATE or stronger. The probe never waits and
// holds nothing when it returns.
func probeNoWait(t testing.TB, db *gorm.DB, user uint, mode string) bool {
	t.Helper()
	tx := db.Begin()
	if tx.Error != nil {
		t.Errorf("probe: %v", tx.Error)
		return false
	}
	defer tx.Rollback()
	err := tx.Exec("SELECT 1 FROM users WHERE id = ? FOR "+mode+" NOWAIT", user).Error
	if err == nil {
		return true
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "55P03" {
		return false
	}
	t.Errorf("probe on user %d FOR %s: %v", user, mode, err)
	return false
}

type waiterRow struct {
	PID   int    `gorm:"column:pid"`
	Query string `gorm:"column:query"`
}

// waiterBlockedBy is (E1): the unique backend in pg_stat_activity waiting on a
// lock with holder among its blockers, whose statement contains fragment and
// that has an ungranted transactionid or tuple lock. None or several within
// five seconds fails the test.
func waiterBlockedBy(t testing.TB, db *gorm.DB, holder int, fragment string) int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var rows []waiterRow
		if err := db.Raw(`SELECT pid, query FROM pg_stat_activity WHERE datname = current_database()
			AND wait_event_type = 'Lock' AND ? = ANY(pg_blocking_pids(pid))`, holder).Scan(&rows).Error; err != nil {
			t.Fatalf("find the waiter: %v", err)
		}
		if len(rows) > 1 {
			t.Fatalf("%d backends wait on backend %d, want exactly one: %+v", len(rows), holder, rows)
		}
		if len(rows) == 1 && strings.Contains(rows[0].Query, fragment) {
			var n int64
			if err := db.Raw(`SELECT count(*) FROM pg_locks WHERE pid = ? AND NOT granted AND locktype IN ('transactionid', 'tuple')`, rows[0].PID).Scan(&n).Error; err != nil {
				t.Fatalf("pg_locks: %v", err)
			}
			if n > 0 {
				return rows[0].PID
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("no unique waiter on backend %d with a statement containing %q: %+v", holder, fragment, rows)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// assertWaitsOnNothing is (E2): the backend is parked inside its transaction
// and is blocked by nobody.
func assertWaitsOnNothing(t testing.TB, db *gorm.DB, pid int, who string) {
	t.Helper()
	var state string
	var blockers int
	if err := db.Raw("SELECT coalesce((SELECT state FROM pg_stat_activity WHERE pid = ?), '')", pid).Scan(&state).Error; err != nil {
		t.Errorf("%s state: %v", who, err)
		return
	}
	if err := db.Raw("SELECT cardinality(pg_blocking_pids(?))", pid).Scan(&blockers).Error; err != nil {
		t.Errorf("%s blockers: %v", who, err)
		return
	}
	if state != "idle in transaction" || blockers != 0 {
		t.Errorf("%s (backend %d) is %q with %d blockers, want parked: idle in transaction and waiting on nothing", who, pid, state, blockers)
	}
}

func backendPID(tx *gorm.DB) int {
	var pid int
	_ = tx.Raw("SELECT pg_backend_pid()").Scan(&pid).Error
	return pid
}

// parkPoint is a seam that stops its first entry inside the open transaction
// until opened, and runs later on every later entry.
type parkPoint struct {
	entered chan int
	release chan struct{}
	once    sync.Once
	n       atomic.Int32
	later   func(tx *gorm.DB)
}

func newParkPoint(t testing.TB) *parkPoint {
	p := &parkPoint{entered: make(chan int, 1), release: make(chan struct{})}
	t.Cleanup(p.open)
	return p
}

func (p *parkPoint) open() { p.once.Do(func() { close(p.release) }) }

func (p *parkPoint) seam() func(*gorm.DB) {
	return func(tx *gorm.DB) {
		if p.n.Add(1) == 1 {
			p.entered <- backendPID(tx)
			<-p.release
			return
		}
		if p.later != nil {
			p.later(tx)
		}
	}
}

func (p *parkPoint) await(t testing.TB) int {
	t.Helper()
	select {
	case pid := <-p.entered:
		return pid
	case <-time.After(20 * time.Second):
		t.Fatal("the seam was never reached")
		return 0
	}
}

func awaitRec(t testing.TB, ch <-chan *httptest.ResponseRecorder, what string) *httptest.ResponseRecorder {
	t.Helper()
	select {
	case w := <-ch:
		return w
	case <-time.After(30 * time.Second):
		t.Fatalf("%s never returned", what)
		return nil
	}
}

// logCapture collects the standard logger's output for one test.
type logCapture struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *logCapture) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *logCapture) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func captureServerLog(t testing.TB) *logCapture {
	old := log.Writer()
	lc := &logCapture{}
	log.SetOutput(lc)
	t.Cleanup(func() { log.SetOutput(old) })
	return lc
}

type sealOpts struct {
	bInHousehold bool // B is a second account of A's household (populations P1, P3)
	bFirst       bool // B.id < A.id (P3)
	registration bool // B registered P on an occurrence that has already started (S3h)
	proxied      bool // serve through the fault proxy (statement log)
}

// sealScene: A (the household's adult account, the inviter and the acting
// admin), B (an admin, the deletion target) and C (an admin outside every
// conversionLockIDs source), with an invitation from A to the household's adult
// person P.
type sealScene struct {
	logs    *logCapture
	t       *testing.T
	app     *App // direct handle: control and observer
	serve   *App // the App whose routes run (the proxied copy when proxied)
	rig     *ctxRig
	a, b, c *models.User
	aClient *apiClient
	person  householdPersonJSON
	token   string
	// review and reasonID are set by seedReasoned: the deletion of B then carries
	// a review that removes the one reason B is party to.
	review   map[string]any
	reasonID uint
}

const sealPW = "password1234"

func newSealScene(t *testing.T, o sealOpts) *sealScene {
	t.Helper()
	logs := captureServerLog(t)
	app := testApp(t)
	adminC, gid := openGroup(t, app, "Seal")
	s := &sealScene{t: t, app: app, serve: app, logs: logs}
	newB := func() *models.User { return newTestUser(t, app, "bob", sealPW, models.RoleAdmin) }
	if o.bFirst {
		s.b = newB()
	}
	holder, hc, person, _ := householdFamily(t, app, adminC, "alice")
	if !o.bFirst {
		s.b = newB()
	}
	s.a, s.person = holder, person
	if err := app.DB.Exec("UPDATE users SET role = 'admin' WHERE id = ?", s.a.ID).Error; err != nil {
		t.Fatal(err)
	}
	s.c = &models.User{}
	if err := app.DB.Where("email = ?", testEmail("admin")).First(s.c).Error; err != nil {
		t.Fatal(err)
	}
	if (s.a.ID < s.b.ID) == o.bFirst {
		t.Fatalf("user ids: A %d B %d, want B before A = %v", s.a.ID, s.b.ID, o.bFirst)
	}
	if o.bInHousehold {
		var acct models.HouseholdAccount
		if err := app.DB.Where("user_id = ?", s.a.ID).First(&acct).Error; err != nil {
			t.Fatal(err)
		}
		if err := app.DB.Create(&models.HouseholdAccount{HouseholdID: acct.HouseholdID, UserID: s.b.ID, Relation: models.HouseholdRelationAdult}).Error; err != nil {
			t.Fatal(err)
		}
	}
	if o.registration {
		eventID, _ := seedCappedEvent(t, app, adminC, gid, intPtr(30), true)
		now := time.Now().UTC().Truncate(time.Second)
		start := now.Add(-26 * time.Hour)
		occ := models.EventOccurrence{EventID: eventID, OriginalDate: calDate(start.Format("2006-01-02")), StartsAt: start, EndsAt: start.Add(time.Hour), Status: models.OccurrenceScheduled}
		if err := app.DB.Create(&occ).Error; err != nil {
			t.Fatal(err)
		}
		reg := models.Registration{OccurrenceID: occ.ID, HouseholdPersonID: &person.ID, RegisteredBy: &s.b.ID, Status: models.RegGoing,
			AttendanceMode: models.AttendanceInPerson, Source: "admin", CreatedAt: now, UpdatedAt: now}
		if err := app.DB.Create(&reg).Error; err != nil {
			t.Fatal(err)
		}
	}
	s.token = inviteHouseholdPerson(t, app, hc, person.ID, "independent@example.com")
	if o.proxied {
		s.rig = newCtxRigFor(t, app)
		s.serve = s.rig.papp
	}
	s.aClient = s.client()
	return s
}

// client is a fresh session of A on the serving App, one per concurrent request.
func (s *sealScene) client() *apiClient {
	c := loginAPI(s.t, s.serve, "alice", sealPW)
	c.holdNotices = true
	return c
}

func (s *sealScene) mint(c *apiClient, subject uint) string {
	s.t.Helper()
	w := c.do("POST", "/api/v1/admin/operation-attempts", map[string]any{"kind": attemptKindAccountDelete, "subjectUserId": subject}, true)
	if w.Code != http.StatusOK {
		s.t.Fatalf("mint: %d %s", w.Code, w.Body.String())
	}
	var out attemptMintJSON
	decodeJSON(s.t, w, &out)
	return out.AttemptID
}

func (s *sealScene) startDelete(c *apiClient, id string) <-chan *httptest.ResponseRecorder {
	ch := make(chan *httptest.ResponseRecorder, 1)
	body := map[string]any{"attemptId": id}
	if s.review != nil {
		body["reasonReview"] = s.review["reasonReview"]
	}
	go func() {
		ch <- c.do("DELETE", "/api/v1/admin/users/"+itoa(s.b.ID), body, true)
	}()
	return ch
}

// seedReasoned gives B a reason to review (a withdrawn add request C made about
// B), reads the review pages as A before any route is parked, and chooses to
// remove it. The view audit this writes is the only one the scene has.
func (s *sealScene) seedReasoned() {
	s.t.Helper()
	r := erasureMakeRequest(s.t, s.app.DB, policy.RequestGroupAdd, policy.ContactWithdrawn, s.c.ID, s.b.ID, 1, 0, 0, 1, "")
	s.reasonID = r.ID
	rev, ok := s.aClient.withChosenReasons("/api/v1/admin/users/"+itoa(s.b.ID), nil, "remove").(map[string]any)
	if !ok {
		s.t.Fatal("the review pages returned no reason for B")
	}
	if n := rev["reasonReview"].(map[string]any)["count"]; n != "1" {
		s.t.Fatalf("review count %v, want 1", n)
	}
	s.review = rev
}

// wantReasonDecidedOnce: the blanked set and the decision audit come only from
// the applied attempt (a rolled-back attempt leaves neither).
func (s *sealScene) wantReasonDecidedOnce() {
	s.t.Helper()
	var text string
	if err := s.app.DB.Raw("SELECT reason FROM contact_requests WHERE id = ?", s.reasonID).Scan(&text).Error; err != nil || text != "" {
		s.t.Errorf("reason %d = %q (%v), want blanked", s.reasonID, text, err)
	}
	if n := s.count(&models.AuditLog{}, "action IN ?", decisionActions); n != 1 {
		s.t.Errorf("%d decision audit rows, want 1", n)
	}
	if n := s.count(&models.AuditLog{}, "action = ? AND target_id = ?", audit.AddReasonRemoved, s.reasonID); n != 1 {
		s.t.Errorf("%d add.reason_removed rows for request %d, want 1", n, s.reasonID)
	}
	if n := s.count(&models.AuditLog{}, "action = ?", audit.UserErasureReasonsViewed); n != 1 {
		s.t.Errorf("%d view audit rows, want the one from the seeding", n)
	}
}

func (s *sealScene) startConversion() <-chan *httptest.ResponseRecorder {
	anon := newAPIClient(s.t, s.serve)
	anon.holdNotices = true
	anon.fetchCSRF()
	ch := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		ch <- anon.do("POST", "/api/v1/invitations/accept", map[string]any{"ageBand": "adult", "token": s.token, "password": "password1234"}, true)
	}()
	return ch
}

func (s *sealScene) settle(c *apiClient, id string) (int, attemptSettleJSON) {
	s.t.Helper()
	w := c.do("POST", "/api/v1/admin/operation-attempts/settle",
		map[string]any{"attemptId": id, "kind": attemptKindAccountDelete, "subjectUserId": s.b.ID}, true)
	var out attemptSettleJSON
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

func (s *sealScene) ledger(id string) attemptState {
	s.t.Helper()
	nonce, _, ok := s.serve.openAttempt(id, attemptKindAccountDelete, s.a.ID, s.b.ID)
	if !ok {
		s.t.Fatal("the id does not open for A and B")
	}
	st, _ := s.serve.attempts.ledger.look(nonce)
	return st
}

func (s *sealScene) count(model any, where string, args ...any) int64 {
	s.t.Helper()
	return contactCount(s.t, s.app, model, where, args...)
}

func (s *sealScene) noDatabaseErrorsIn(what string, bodies ...string) {
	s.t.Helper()
	s.noServerErrors(what, false)
	s.noErrorsInBodies(what, bodies...)
}

func (s *sealScene) noErrorsInBodies(what string, bodies ...string) {
	s.t.Helper()
	for _, b := range bodies {
		for _, leak := range []string{"40P01", "55P03", "NOWAIT", "deadlock"} {
			if strings.Contains(b, leak) {
				s.t.Errorf("%s: a response carries %q: %s", what, leak, b)
			}
		}
	}
}

// noServerErrors reads the server log captured since the scene began: no
// deadlock (40P01) and, unless a schedule ends in the busy answer on purpose, no
// lock_not_available (55P03) may have reached it.
func (s *sealScene) noServerErrors(what string, busyIsExpected bool) {
	s.t.Helper()
	text := s.logs.String()
	leaks := []string{"40P01", "deadlock detected"}
	if !busyIsExpected {
		leaks = append(leaks, "55P03", "NOWAIT")
	}
	for _, leak := range leaks {
		if strings.Contains(text, leak) {
			s.t.Errorf("%s: the server log carries %q:\n%s", what, leak, text)
		}
	}
}

// laterEntry is (E4) for an attempt that follows a busy one: the entry is
// recorded by sealAttempts, (E2) holds for the deletion's own backend, and the
// FOR SHARE probe on A alone says whether the conversion still holds A.
func (s *sealScene) laterEntry(aFree bool) func(*gorm.DB) {
	return func(tx *gorm.DB) {
		assertWaitsOnNothing(s.t, s.app.DB, backendPID(tx), "the deletion (later attempt)")
		if probeNoWait(s.t, s.app.DB, s.a.ID, "SHARE") != aFree {
			s.t.Errorf("later attempt: FOR SHARE on A free = %v, want %v", !aFree, aFree)
		}
	}
}

// wantSettledApplied settles an applied id after both routes have responded:
// no conversion holds A, so the seal runs once.
func (s *sealScene) wantSettledApplied(id string) {
	s.t.Helper()
	counter := &atomic.Int64{}
	s.serve.sealAttempts = counter
	_, got := s.settle(s.client(), id)
	wantSettle(s.t, got, settleApplied, nil)
	if counter.Load() != 1 {
		s.t.Errorf("settle sealed %d times, want 1 (no conversion holds A)", counter.Load())
	}
}

func (s *sealScene) wantPending(w *httptest.ResponseRecorder) {
	s.t.Helper()
	var out invitationAcceptanceJSON
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &out) != nil || !out.PendingApproval {
		s.t.Fatalf("conversion: %d %s, want 200 pendingApproval", w.Code, w.Body.String())
	}
}

// conversionRows are the conversion's own rows, each exactly once.
func (s *sealScene) wantConversionRowsOnce() {
	s.t.Helper()
	if n := s.count(&models.AccountRequest{}, "source = ? AND status = ? AND requested_by = ?", models.RequestHouseholdConversion, models.RequestPending, s.a.ID); n != 1 {
		s.t.Errorf("%d household_conversion requests, want 1", n)
	}
	for _, action := range []string{audit.AccountRequested, audit.InvitationAccepted} {
		if n := s.count(&models.AuditLog{}, "action = ?", action); n != 1 {
			s.t.Errorf("%d %s audit rows, want 1", n, action)
		}
	}
	if n := s.count(&models.Invitation{}, "accepted_at IS NOT NULL"); n != 1 {
		s.t.Errorf("%d accepted invitations, want 1", n)
	}
}

func (s *sealScene) wantDeleted(id string, sealed int64, counter *atomic.Int64) {
	s.t.Helper()
	if got := counter.Load(); got != sealed {
		s.t.Errorf("sealAttempts = %d, want %d", got, sealed)
	}
	if n := s.count(&models.AuditLog{}, "action = ? AND target_id = ?", audit.UserDeleted, s.b.ID); n != 1 {
		s.t.Errorf("%d user.deleted rows, want 1", n)
	}
	var receipts int64
	if err := s.app.DB.Raw("SELECT count(*) FROM admin_operation_attempts").Scan(&receipts).Error; err != nil || receipts != 1 {
		s.t.Errorf("receipts = %d (%v), want 1", receipts, err)
	}
	if s.ledger(id) != attemptApplied {
		s.t.Errorf("ledger = %v, want applied", s.ledger(id))
	}
}

func TestActorSealVsConversion(t *testing.T) {
	withWindow := func(t *testing.T, d time.Duration) {
		old := deleteRetryWindow
		deleteRetryWindow = d
		t.Cleanup(func() { deleteRetryWindow = old })
	}

	// S1 (P1, A.id < B.id): the deletion of B is parked at deleteBeforeSeal; the
	// conversion takes A and waits on B; the seal then answers 55P03, the
	// rollback is acknowledged, the conversion commits, and attempt 2 applies.
	for _, reasoned := range []bool{false, true} {
		t.Run(fmt.Sprintf("S1 deletion parked, conversion waits on B (reasoned=%v)", reasoned), func(t *testing.T) {
			withWindow(t, 30*time.Second)
			s := newSealScene(t, sealOpts{bInHousehold: true})
			if reasoned {
				s.seedReasoned()
			}
			id := s.mint(s.aClient, s.b.ID)
			counter := &atomic.Int64{}
			s.serve.sealAttempts = counter
			park := newParkPoint(t)
			park.later = s.laterEntry(true) // the conversion has committed before attempt 2
			s.serve.deleteBeforeSeal = park.seam()
			convDone := make(chan struct{})
			s.serve.deleteBeforeRetry = func() { <-convDone }

			del := s.startDelete(s.aClient, id)
			dpid := park.await(t)
			if probeNoWait(t, s.app.DB, s.b.ID, "KEY SHARE") {
				t.Fatal("the deletion should hold B FOR UPDATE")
			}
			conv := s.startConversion()
			waiterBlockedBy(t, s.app.DB, dpid, "FOR NO KEY UPDATE")
			if probeNoWait(t, s.app.DB, s.a.ID, "SHARE") || !probeNoWait(t, s.app.DB, s.a.ID, "KEY SHARE") {
				t.Fatal("the conversion should hold A FOR NO KEY UPDATE and the deletion only a plain read of A")
			}
			assertWaitsOnNothing(t, s.app.DB, dpid, "the deletion")
			park.open()

			cw := awaitRec(t, conv, "the conversion")
			s.wantPending(cw)
			close(convDone)
			dw := awaitRec(t, del, "the deletion")
			if dw.Code != http.StatusOK {
				t.Fatalf("the deletion: %d %s", dw.Code, dw.Body.String())
			}
			s.noDatabaseErrorsIn("S1", cw.Body.String(), dw.Body.String())
			s.wantDeleted(id, 2, counter)
			s.wantConversionRowsOnce()
			if park.n.Load() != 2 {
				t.Errorf("deleteBeforeSeal entered %d times, want 2", park.n.Load())
			}
			if reasoned {
				s.wantReasonDecidedOnce()
			}
			s.wantSettledApplied(id)
		})
	}

	// S1 at the PRODUCTION retry window (deleteRetryWindow left at 2 s): the
	// deletion stays parked past the window with the conversion waiting on B.
	// When it is released the seal answers 55P03, the rollback is acknowledged,
	// and no second attempt begins, because a new attempt may start only within
	// the window: 409 admin_account_busy, one sealed attempt, nothing deleted.
	t.Run("S1 at the production retry window", func(t *testing.T) {
		s := newSealScene(t, sealOpts{bInHousehold: true})
		id := s.mint(s.aClient, s.b.ID)
		counter := &atomic.Int64{}
		s.serve.sealAttempts = counter
		park := newParkPoint(t)
		s.serve.deleteBeforeSeal = park.seam()
		del := s.startDelete(s.aClient, id)
		dpid := park.await(t)
		conv := s.startConversion()
		waiterBlockedBy(t, s.app.DB, dpid, "FOR NO KEY UPDATE")
		time.Sleep(deleteRetryWindow + 300*time.Millisecond)
		park.open()
		cw := awaitRec(t, conv, "the conversion")
		s.wantPending(cw)
		dw := awaitRec(t, del, "the deletion")
		if dw.Code != http.StatusConflict || codeOf(t, dw) != "admin_account_busy" {
			t.Fatalf("the deletion: %d %s, want 409 admin_account_busy (no retry after the window)", dw.Code, dw.Body.String())
		}
		if counter.Load() != 1 || park.n.Load() != 1 {
			t.Fatalf("sealed attempts %d, seam entries %d, want 1 and 1: a new attempt began after the window", counter.Load(), park.n.Load())
		}
		s.noErrorsInBodies("S1 production window", cw.Body.String(), dw.Body.String())
		s.noServerErrors("S1 production window", true)
		if s.ledger(id) != attemptRolledBack || s.count(&models.AdminOperationAttempt{}, "true") != 0 {
			t.Error("the id should end rolled back with no receipt")
		}
		s.wantConversionRowsOnce()
		_, got := s.settle(s.client(), id)
		wantSettle(t, got, settleNotApplied, nil)
	})

	// S2 (P2): the conversion is parked at conversionAfterUserLocks holding A;
	// the deletion of B reaches its seal three times and answers admin_account_busy;
	// settle during the park answers checking (R7-C01).
	t.Run("S2 conversion parked, deletion busy three times", func(t *testing.T) {
		s := newSealScene(t, sealOpts{proxied: true})
		id := s.mint(s.aClient, s.b.ID)
		counter := &atomic.Int64{}
		s.serve.sealAttempts = counter
		convPark := newParkPoint(t)
		s.serve.conversionAfterUserLocks = convPark.seam()
		conv := s.startConversion()
		cpid := convPark.await(t)
		assertWaitsOnNothing(t, s.app.DB, cpid, "the conversion")
		if probeNoWait(t, s.app.DB, s.a.ID, "SHARE") || !probeNoWait(t, s.app.DB, s.a.ID, "KEY SHARE") || !probeNoWait(t, s.app.DB, s.b.ID, "SHARE") {
			t.Fatal("the conversion should hold A FOR NO KEY UPDATE and nobody should hold B")
		}
		before := erasureWaitSnapshot(t, s.app.DB)
		entries := atomic.Int32{}
		s.serve.deleteBeforeSeal = func(tx *gorm.DB) {
			entries.Add(1)
			pid := backendPID(tx)
			if probeNoWait(t, s.app.DB, s.b.ID, "KEY SHARE") {
				t.Errorf("entry %d: the deletion should hold B FOR UPDATE", entries.Load())
			}
			assertWaitsOnNothing(t, s.app.DB, pid, "the deletion")
			assertWaitsOnNothing(t, s.app.DB, cpid, "the conversion")
			if probeNoWait(t, s.app.DB, s.a.ID, "SHARE") {
				t.Errorf("entry %d: A should still be held by the conversion", entries.Load())
			}
		}
		dw := awaitRec(t, s.startDelete(s.aClient, id), "the deletion")
		if dw.Code != http.StatusConflict || codeOf(t, dw) != "admin_account_busy" {
			t.Fatalf("the deletion: %d %s, want 409 admin_account_busy", dw.Code, dw.Body.String())
		}
		s.noErrorsInBodies("S2", dw.Body.String())
		s.noServerErrors("S2", true) // the exhausted busy answer is logged by design; no deadlock may be
		if counter.Load() != 3 || entries.Load() != 3 {
			t.Fatalf("sealed attempts %d, seam entries %d, want 3 and 3", counter.Load(), entries.Load())
		}
		// B is untouched and the attempt is rolled back.
		if after := erasureWaitSnapshot(t, s.app.DB); after != before {
			t.Error("a busy deletion changed the database")
		}
		if s.ledger(id) != attemptRolledBack || s.count(&models.AdminOperationAttempt{}, "true") != 0 {
			t.Error("the id should end rolled back with no receipt")
		}
		resub := s.startDelete(s.aClient, id)
		if w := awaitRec(t, resub, "the resubmission"); w.Code != http.StatusConflict || codeOf(t, w) != "attempt_closed" {
			t.Fatalf("resubmission: %d %s, want 409 attempt_closed", w.Code, w.Body.String())
		}

		// SETTLE DURING THE PARK (R7-C01).
		var fenceEntries atomic.Int32
		s.serve.settleBeforeFence = func(*gorm.DB) { fenceEntries.Add(1) }
		s.serve.settleAfterFence = func(*gorm.DB) { fenceEntries.Add(1) }
		s.serve.deleteBeforeSeal = nil
		sc := s.client()
		assertWaitsOnNothing(t, s.app.DB, cpid, "the conversion")
		if probeNoWait(t, s.app.DB, s.a.ID, "SHARE") {
			t.Fatal("A should be held by the conversion before the settle")
		}
		counter.Store(0)
		mark := len(s.rig.proxy.Events())
		started := time.Now()
		code, got := s.settle(sc, id)
		if code != http.StatusOK || got.Result != settleChecking || got.SubjectDeleted != nil {
			t.Fatalf("settle during the park: %d %+v, want checking with no other field", code, got)
		}
		if took := time.Since(started); took > 2*time.Second {
			t.Errorf("settle took %s, want its three sealed transactions inside 2 s", took)
		}
		if probeNoWait(t, s.app.DB, s.a.ID, "SHARE") {
			t.Fatal("A should still be held by the conversion after the settle")
		}
		if counter.Load() != 3 || fenceEntries.Load() != 0 || s.ledger(id) != attemptRolledBack {
			t.Fatalf("settle during the park: sealed %d (want 3), fence seam entries %d (want 0), ledger %v (want rolled back)", counter.Load(), fenceEntries.Load(), s.ledger(id))
		}
		assertSealedAndRolledBack(t, s.rig.proxy.Events()[mark:], 3)

		// The conversion is released; its 200 is awaited.
		convPark.open()
		cw := awaitRec(t, conv, "the conversion")
		s.wantPending(cw)
		s.wantConversionRowsOnce()
		for u, want := range map[*models.User]int64{s.a: 0, s.b: 1, s.c: 1} {
			if got := countOf(t, s.app, u.ID, notify.AccountRequested); got != want {
				t.Errorf("user %d has %d account.requested notices, want %d", u.ID, got, want)
			}
		}
		counter.Store(0)
		code, got = s.settle(sc, id)
		if code != http.StatusOK || got.Result != settleNotApplied || got.SubjectDeleted != nil || counter.Load() != 1 {
			t.Fatalf("settle after the conversion: %d %+v sealed %d, want not_applied without subjectDeleted in one sealed transaction", code, got, counter.Load())
		}
		if s.ledger(id) != attemptRolledBack {
			t.Error("settle changed the ledger")
		}
		// Only now is a fresh id minted to delete B.
		fresh := s.mint(s.aClient, s.b.ID)
		counter.Store(0)
		dw = awaitRec(t, s.startDelete(s.aClient, fresh), "the fresh deletion")
		if dw.Code != http.StatusOK || counter.Load() != 1 {
			t.Fatalf("the fresh deletion: %d %s sealed %d, want 200 with one sealed attempt", dw.Code, dw.Body.String(), counter.Load())
		}
		code, got = s.settle(sc, id)
		wantSettle(t, got, settleNotApplied, ptr(true))
		if code != http.StatusOK {
			t.Fatalf("settle of the first id: %d", code)
		}
		_, got = s.settle(sc, fresh)
		wantSettle(t, got, settleApplied, nil)
	})

	// S3 and S3h (P3: B.id < A.id plus an admin C outside every source). The
	// deletion of B is parked holding B FOR UPDATE; the conversion waits on B
	// before it can lock A; the seal finds no holder.
	for _, withRegistration := range []bool{false, true} {
		name := "S3 deleted participant, B a household account only"
		if withRegistration {
			name = "S3h deleted participant, B also registered the person"
		}
		t.Run(name, func(t *testing.T) {
			withWindow(t, 30*time.Second)
			s := newSealScene(t, sealOpts{bInHousehold: true, bFirst: true, registration: withRegistration})
			id := s.mint(s.aClient, s.b.ID)
			counter := &atomic.Int64{}
			s.serve.sealAttempts = counter
			var entries atomic.Int32
			s.serve.conversionAfterUserLocks = func(*gorm.DB) { entries.Add(1) }
			park := newParkPoint(t)
			s.serve.deleteBeforeSeal = park.seam()

			del := s.startDelete(s.aClient, id)
			dpid := park.await(t)
			if probeNoWait(t, s.app.DB, s.b.ID, "KEY SHARE") {
				t.Fatal("the deletion should hold B FOR UPDATE")
			}
			conv := s.startConversion()
			waiterBlockedBy(t, s.app.DB, dpid, "FOR NO KEY UPDATE")
			if !probeNoWait(t, s.app.DB, s.a.ID, "SHARE") || !probeNoWait(t, s.app.DB, s.c.ID, "SHARE") {
				t.Fatal("the conversion holds no users row yet: FOR SHARE on A and C should succeed")
			}
			assertWaitsOnNothing(t, s.app.DB, dpid, "the deletion")
			park.open()

			dw := awaitRec(t, del, "the deletion")
			cw := awaitRec(t, conv, "the conversion")
			if dw.Code != http.StatusOK {
				t.Fatalf("the deletion: %d %s", dw.Code, dw.Body.String())
			}
			s.noDatabaseErrorsIn("S3", cw.Body.String(), dw.Body.String())
			s.wantDeleted(id, 1, counter)
			if entries.Load() != 1 {
				t.Errorf("conversionAfterUserLocks entered %d times, want once", entries.Load())
			}
			// Settle only after both routes have responded.
			_, got := s.settle(s.client(), id)
			wantSettle(t, got, settleApplied, nil)
			if !withRegistration {
				s.wantPending(cw)
				s.wantConversionRowsOnce()
				for u, want := range map[*models.User]int64{s.a: 0, s.c: 1} {
					if got := countOf(t, s.app, u.ID, notify.AccountRequested); got != want {
						t.Errorf("user %d has %d account.requested notices, want %d", u.ID, got, want)
					}
				}
				if n := s.count(&models.Notification{}, "user_id = ?", s.b.ID); n != 0 {
					t.Errorf("deleted B has %d notifications", n)
				}
				return
			}
			var body struct {
				Error string `json:"error"`
			}
			_ = json.Unmarshal(cw.Body.Bytes(), &body)
			if cw.Code != http.StatusConflict || body.Error != errConversionHolder.Error() {
				t.Fatalf("conversion: %d %s, want 409 %q", cw.Code, cw.Body.String(), errConversionHolder.Error())
			}
			if s.count(&models.AccountRequest{}, "source = ?", models.RequestHouseholdConversion) != 0 ||
				s.count(&models.AuditLog{}, "action IN (?, ?)", audit.AccountRequested, audit.InvitationAccepted) != 0 ||
				s.count(&models.Invitation{}, "accepted_at IS NOT NULL OR revoked_at IS NOT NULL") != 0 {
				t.Error("a refused conversion left a request, an audit row or a changed invitation")
			}
			var registeredBy uint
			if err := s.app.DB.Raw("SELECT registered_by FROM registrations WHERE household_person_id = ?", s.person.ID).Scan(&registeredBy).Error; err != nil || registeredBy != s.b.ID {
				t.Errorf("the registration keeps registered_by = %d, got %d (%v)", s.b.ID, registeredBy, err)
			}
			if got := countOf(t, s.app, s.c.ID, notify.AccountRequested); got != 0 {
				t.Errorf("C got %d account.requested notices from a refused conversion", got)
			}
		})
	}

	// S4 (P1, conversion first): the conversion is parked holding A and B; the
	// deletion waits for B before it reaches its seal, then applies.
	t.Run("S4 conversion parked first, deletion waits on B", func(t *testing.T) {
		withWindow(t, 30*time.Second)
		s := newSealScene(t, sealOpts{bInHousehold: true})
		id := s.mint(s.aClient, s.b.ID)
		counter := &atomic.Int64{}
		s.serve.sealAttempts = counter
		convPark := newParkPoint(t)
		s.serve.conversionAfterUserLocks = convPark.seam()
		conv := s.startConversion()
		cpid := convPark.await(t)
		for _, u := range []uint{s.a.ID, s.b.ID} {
			if probeNoWait(t, s.app.DB, u, "SHARE") || !probeNoWait(t, s.app.DB, u, "KEY SHARE") {
				t.Fatalf("the conversion should hold user %d FOR NO KEY UPDATE", u)
			}
		}
		assertWaitsOnNothing(t, s.app.DB, cpid, "the conversion")
		del := s.startDelete(s.aClient, id)
		waiterBlockedBy(t, s.app.DB, cpid, "FOR UPDATE")
		if counter.Load() != 0 {
			t.Fatalf("the deletion reached its seal %d times while waiting for B", counter.Load())
		}
		convPark.open()
		cw := awaitRec(t, conv, "the conversion")
		dw := awaitRec(t, del, "the deletion")
		s.wantPending(cw)
		if dw.Code != http.StatusOK {
			t.Fatalf("the deletion: %d %s", dw.Code, dw.Body.String())
		}
		s.noDatabaseErrorsIn("S4", cw.Body.String(), dw.Body.String())
		s.wantDeleted(id, 1, counter)
		s.wantConversionRowsOnce()
		s.wantSettledApplied(id)
	})

	// S5 (P2, the recipient edge): the deletion of B is parked; the conversion
	// waits for B in notifyAccountRequested's recipient lock while holding A.
	for _, reasoned := range []bool{false, true} {
		t.Run(fmt.Sprintf("S5 conversion waits on B at the recipient lock (reasoned=%v)", reasoned), func(t *testing.T) {
			withWindow(t, 30*time.Second)
			s := newSealScene(t, sealOpts{})
			if reasoned {
				s.seedReasoned()
			}
			id := s.mint(s.aClient, s.b.ID)
			counter := &atomic.Int64{}
			s.serve.sealAttempts = counter
			park := newParkPoint(t)
			park.later = s.laterEntry(true)
			s.serve.deleteBeforeSeal = park.seam()
			convDone := make(chan struct{})
			s.serve.deleteBeforeRetry = func() { <-convDone }
			del := s.startDelete(s.aClient, id)
			dpid := park.await(t)
			conv := s.startConversion()
			waiterBlockedBy(t, s.app.DB, dpid, "FOR KEY SHARE")
			if probeNoWait(t, s.app.DB, s.a.ID, "SHARE") {
				t.Fatal("the conversion should hold A FOR NO KEY UPDATE")
			}
			assertWaitsOnNothing(t, s.app.DB, dpid, "the deletion")
			park.open()
			cw := awaitRec(t, conv, "the conversion")
			s.wantPending(cw)
			close(convDone)
			dw := awaitRec(t, del, "the deletion")
			if dw.Code != http.StatusOK {
				t.Fatalf("the deletion: %d %s", dw.Code, dw.Body.String())
			}
			s.noDatabaseErrorsIn("S5", cw.Body.String(), dw.Body.String())
			s.wantDeleted(id, 2, counter)
			s.wantConversionRowsOnce()
			if park.n.Load() != 2 {
				t.Errorf("deleteBeforeSeal entered %d times, want 2", park.n.Load())
			}
			if reasoned {
				s.wantReasonDecidedOnce()
			}
			s.wantSettledApplied(id)
		})
	}
}

// assertSealedAndRolledBack reads a proxy log slice: want settle transactions
// ended with currentAdminIn's statement, each followed on its connection by an
// acknowledged ROLLBACK and nothing else.
func assertSealedAndRolledBack(t testing.TB, events []testdb.Event, want int) {
	t.Helper()
	sealed := 0
	for i, e := range events {
		if e.Kind != testdb.KindStatement || !strings.Contains(e.SQL, "FOR SHARE NOWAIT") || !strings.Contains(e.SQL, "logout_version") {
			continue
		}
		sealed++
		next := -1
		for j := i + 1; j < len(events); j++ {
			if events[j].Conn != e.Conn {
				continue
			}
			next = j
			break
		}
		if next < 0 {
			t.Errorf("nothing follows the sealed statement on connection %d", e.Conn)
		} else if !(events[next].Kind == testdb.KindRollback || strings.EqualFold(strings.TrimSpace(events[next].SQL), "ROLLBACK")) {
			t.Errorf("the sealed statement on connection %d is followed by %+v, not ROLLBACK", e.Conn, events[next])
		}
	}
	if sealed != want {
		t.Errorf("%d sealed transactions in the proxy log, want %d", sealed, want)
	}
}
