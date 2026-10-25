package main

import (
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"fold/internal/audit"
	"fold/internal/notify"
	"fold/internal/policy"
	"fold/models"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func savedOccurrenceAccess(t *testing.T, f occurrenceReadFixture) models.OccurrenceAccess {
	t.Helper()
	var row models.OccurrenceAccess
	if err := f.app.DB.First(&row, f.access.ID).Error; err != nil {
		t.Fatal(err)
	}
	return row
}

func TestOccurrenceMutationLifecycleImmediateIrreversibleAndIdempotent(t *testing.T) {
	for _, scenario := range []string{"disable", "cancel", "registration_cancel", "registration_remove", "event_delete", "group_delete", "occurrence_delete"} {
		t.Run(scenario, func(t *testing.T) {
			f := newOccurrenceReadFixture(t)
			change := contactChange{EventID: f.event.ID}
			want := policy.OccurrenceContextChanged
			mutate := func(tx *gorm.DB) error { return nil }
			switch scenario {
			case "disable":
				change = contactChange{UserID: f.recipient.ID}
				want = policy.ContactAccountDisabled
				mutate = func(tx *gorm.DB) error { return tx.Model(f.recipient).Update("status", models.UserDisabled).Error }
			case "cancel":
				want = policy.OccurrenceCancelled
				mutate = func(tx *gorm.DB) error {
					return tx.Model(&f.dates[1]).Update("status", models.OccurrenceCancelled).Error
				}
			case "registration_cancel", "registration_remove":
				status := models.RegCancelled
				if scenario == "registration_remove" {
					status = models.RegRemoved
				}
				mutate = func(tx *gorm.DB) error {
					return tx.Model(&models.Registration{}).Where("id = ?", f.access.RegistrationID).Update("status", status).Error
				}
			case "event_delete":
				want = policy.OccurrenceEventDeleted
				mutate = func(tx *gorm.DB) error { return deleteEventRows(tx, []uint{f.event.ID}) }
			case "group_delete":
				change = contactChange{GroupID: f.group}
				want = policy.OccurrenceGroupDeleted
				mutate = func(tx *gorm.DB) error { return tx.Delete(&models.Group{}, f.group).Error }
			case "occurrence_delete":
				want = policy.OccurrenceDeleted
				mutate = func(tx *gorm.DB) error {
					if err := tx.Where("occurrence_id = ?", f.dates[1].ID).Delete(&models.Registration{}).Error; err != nil {
						return err
					}
					return tx.Delete(&f.dates[1]).Error
				}
			}
			if err := f.app.contactMutation(change, mutate); err != nil {
				t.Fatal(err)
			}
			row := savedOccurrenceAccess(t, f)
			if row.Status != policy.OccurrenceAccessEnded || row.EndReason != want || row.EndedAt == nil {
				t.Fatalf("not immediately ended: %+v", row)
			}
			if scenario == "disable" {
				if err := f.app.contactMutation(change, func(tx *gorm.DB) error { return tx.Model(f.recipient).Update("status", models.UserActive).Error }); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "cancel" {
				if err := f.app.contactMutation(change, func(tx *gorm.DB) error {
					return tx.Model(&f.dates[1]).Update("status", models.OccurrenceScheduled).Error
				}); err != nil {
					t.Fatal(err)
				}
			}
			if err := f.app.sweepContacts(t.Context(), time.Now().UTC()); err != nil {
				t.Fatal(err)
			}
			if savedOccurrenceAccess(t, f).Status != policy.OccurrenceAccessEnded {
				t.Fatal("restoration revived invitation")
			}
			if n := contactCount(t, f.app, &models.AuditLog{}, "action = ? AND target_id = ?", audit.OccurrenceAccessEnded, row.ID); n != 1 {
				t.Fatal("duplicate/missing end audit", n)
			}
			var notices []models.Notification
			if err := f.app.DB.Where("kind = ? AND subject_id = ?", notify.OccurrenceUpdated, row.ID).Find(&notices).Error; err != nil {
				t.Fatal(err)
			}
			if len(notices) > 1 {
				t.Fatal("duplicate lifecycle notice")
			}
			for _, notice := range notices {
				for _, secret := range []string{"Hidden group", "Hidden series", f.recipient.Email, f.issuer.Email} {
					if strings.Contains(notice.Body+notice.Title, secret) {
						t.Fatal("unsafe notice", notice)
					}
				}
				if notice.Link != occurrenceDatePath(row.EventID, f.dates[1].OriginalDate) {
					t.Fatal("not exact-date navigation", notice.Link)
				}
			}
		})
	}
}

func TestOccurrenceMutationLifecycleAuditFailureRollsBackDomain(t *testing.T) {
	f := newOccurrenceReadFixture(t)
	name := "occurrence:test_end_audit_failure"
	if err := f.app.DB.Callback().Create().Before("gorm:create").Register(name, func(db *gorm.DB) {
		if row, ok := db.Statement.Dest.(*models.AuditLog); ok && row.Action == audit.OccurrenceAccessEnded {
			db.AddError(errors.New("injected occurrence audit failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer f.app.DB.Callback().Create().Remove(name)
	err := f.app.contactMutation(contactChange{UserID: f.recipient.ID}, func(tx *gorm.DB) error { return tx.Model(f.recipient).Update("status", models.UserDisabled).Error })
	if err == nil {
		t.Fatal("audit failure accepted mutation")
	}
	if savedOccurrenceAccess(t, f).Status != policy.OccurrenceAccessActive {
		t.Fatal("end survived rollback")
	}
	var user models.User
	if err := f.app.DB.First(&user, f.recipient.ID).Error; err != nil {
		t.Fatal(err)
	}
	if user.Status != models.UserActive {
		t.Fatal("disable survived rollback")
	}
	if contactCount(t, f.app, &models.Notification{}, "kind = ? AND subject_id = ?", notify.OccurrenceUpdated, f.access.ID) != 0 {
		t.Fatal("notice survived rollback")
	}
}

func TestOccurrenceMutationLifecycleIssuerBlockAndRescheduleIndependence(t *testing.T) {
	f := newOccurrenceReadFixture(t)
	if err := f.app.contactMutation(contactChange{UserID: f.issuer.ID}, func(tx *gorm.DB) error {
		if err := tx.Model(f.issuer).Update("status", models.UserDisabled).Error; err != nil {
			return err
		}
		return tx.Create(&models.ManagerAddBlock{SubjectUserID: f.recipient.ID, OrganizerType: policy.ContactGroup, OrganizerID: f.group}).Error
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.app.contactMutation(contactChange{EventID: f.event.ID}, func(tx *gorm.DB) error {
		return tx.Model(&f.dates[1]).Update("starts_at", f.dates[1].StartsAt.Add(time.Hour)).Error
	}); err != nil {
		t.Fatal(err)
	}
	if savedOccurrenceAccess(t, f).Status != policy.OccurrenceAccessActive {
		t.Fatal("issuer/block/reschedule ended accepted date")
	}
	apiDo(t, f.client, "GET", dateReadPath(f.event.ID, f.dates[1].OriginalDate), nil, 200)
}

func TestOccurrenceMutationPendingPurposeDispatchAndNoResurrection(t *testing.T) {
	for _, scenario := range []string{"expiry", "block", "role_loss", "disable", "cancel"} {
		t.Run(scenario, func(t *testing.T) {
			f := newOccurrenceReadFixture(t)
			now := time.Now().UTC()
			r := models.ContactRequest{Purpose: policy.RequestOccurrenceAdd, RequesterID: f.issuer.ID, SubjectUserID: f.other.ID, ContextType: policy.ContactEvent, ContextID: f.event.ID, OccurrenceID: &f.dates[0].ID, AddAttendanceMode: models.AttendanceInPerson, SubjectDecision: policy.ContactPending, AdminDecision: policy.ContactNotNeeded, Status: policy.ContactPending, CreatedAt: now, ExpiresAt: now.Add(policy.ContactRequestLifetime)}
			if err := f.app.DB.Create(&r).Error; err != nil {
				t.Fatal(err)
			}
			change := contactChange{EventID: f.event.ID}
			mutate := func(tx *gorm.DB) error { return nil }
			switch scenario {
			case "block":
				mutate = func(tx *gorm.DB) error {
					return tx.Create(&models.ManagerAddBlock{SubjectUserID: f.other.ID, OrganizerType: policy.ContactGroup, OrganizerID: f.group}).Error
				}
			case "role_loss":
				mutate = func(tx *gorm.DB) error {
					if err := tx.Model(f.issuer).Update("role", models.RoleMember).Error; err != nil {
						return err
					}
					return tx.Where("user_id = ?", f.issuer.ID).Delete(&models.Membership{}).Error
				}
			case "disable":
				change = contactChange{UserID: f.other.ID}
				mutate = func(tx *gorm.DB) error { return tx.Model(f.other).Update("status", models.UserDisabled).Error }
			case "cancel":
				mutate = func(tx *gorm.DB) error {
					return tx.Model(&f.dates[0]).Update("status", models.OccurrenceCancelled).Error
				}
			}
			if scenario == "expiry" {
				if err := f.app.sweepContacts(t.Context(), r.ExpiresAt); err != nil {
					t.Fatal(err)
				}
			} else if err := f.app.contactMutation(change, mutate); err != nil {
				t.Fatal(err)
			}
			var saved models.ContactRequest
			if err := f.app.DB.First(&saved, r.ID).Error; err != nil {
				t.Fatal(err)
			}
			want := policy.ContactEnded
			if scenario == "expiry" {
				want = policy.ContactExpired
			}
			if saved.Status != want {
				t.Fatalf("pending occurrence ask not dispatched: %+v", saved)
			}
			if err := f.app.sweepContacts(t.Context(), time.Now().UTC().Add(15*24*time.Hour)); err != nil {
				t.Fatal(err)
			}
			if contactCount(t, f.app, &models.AuditLog{}, "target_type = ? AND target_id = ? AND action IN ?", "add_request", r.ID, []string{audit.AddEnded, audit.AddExpired}) != 1 {
				t.Fatal("duplicate/missing pending terminal audit")
			}
		})
	}
}

func TestOccurrenceMutationParallelCancellationEndsOnce(t *testing.T) {
	f := newOccurrenceReadFixture(t)
	if err := f.app.DB.Model(&models.Event{}).Where("id = ?", f.event.ID).Update("visibility", models.VisibilityPublic).Error; err != nil {
		t.Fatal(err)
	}
	apiDo(t, f.stranger, "PUT", "/api/v1/events/"+itoa(f.event.ID)+"/registrations", map[string]any{"date": f.dates[1].OriginalDate, "status": "going"}, 200)
	path := "/api/v1/events/" + itoa(f.event.ID) + "/cancel"
	gate := holdContactGate(t, f.app)
	firstClient, secondClient := cloneContactClient(f.admin), cloneContactClient(f.admin)
	first := queueContact(func() *httptest.ResponseRecorder {
		return firstClient.do("POST", path, map[string]string{"date": f.dates[1].OriginalDate}, true)
	})
	waitContactWaiters(t, f.app, 1)
	second := queueContact(func() *httptest.ResponseRecorder {
		return secondClient.do("POST", path, map[string]string{"date": f.dates[1].OriginalDate}, true)
	})
	waitContactWaiters(t, f.app, 2)
	if err := gate.Commit().Error; err != nil {
		t.Fatal(err)
	}
	for _, ch := range []<-chan *httptest.ResponseRecorder{first, second} {
		if w := awaitContactResponse(t, ch); w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if savedOccurrenceAccess(t, f).Status != policy.OccurrenceAccessEnded {
		t.Fatal("parallel cancellation left invitation active")
	}
	if n := contactCount(t, f.app, &models.AuditLog{}, "action = ? AND target_id = ?", audit.OccurrenceAccessEnded, f.access.ID); n != 1 {
		t.Fatal("duplicate end audit", n)
	}
	apiDo(t, f.admin, "POST", path, map[string]string{"date": f.dates[1].OriginalDate}, 200)
	var notices []models.Notification
	if err := f.app.DB.Where("user_id = ? AND kind = ? AND link = ?", f.recipient.ID, notify.OccurrenceUpdated, occurrenceDatePath(f.event.ID, f.dates[1].OriginalDate)).Find(&notices).Error; err != nil {
		t.Fatal(err)
	}
	if len(notices) != 1 {
		t.Fatal("duplicate/missing ended notice/email candidate", notices)
	}
	if n := contactCount(t, f.app, &models.Notification{}, "user_id = ? AND kind = ? AND subject_id = ?", f.other.ID, notify.OccurrenceCancelled, f.dates[1].ID); n != 1 {
		t.Fatal("ordinary cancellation notice lost/duplicated", n)
	}
	if n := contactCount(t, f.app, &models.NotificationDelivery{}, "notification_id = ? AND channel = ?", notices[0].ID, models.DeliveryEmail); n != 1 {
		t.Fatal("duplicate/missing end email delivery", n)
	}
	if n := contactCount(t, f.app, &models.AuditLog{}, "action = ? AND target_id = ?", audit.OccurrenceAccessEnded, f.access.ID); n != 1 {
		t.Fatal("replay duplicated end audit", n)
	}
}

func TestOccurrenceMutationConsentLossEndsBeforeRegrant(t *testing.T) {
	f := newOccurrenceReadFixture(t)
	teen := newTestUser(t, f.app, "lifecycle invited teen", "password1234", models.RoleMember)
	birth := time.Now().UTC().AddDate(-15, 0, 0)
	if err := f.app.DB.Model(teen).Updates(map[string]any{"account_type": models.AccountTeen, "birth_date": birth}).Error; err != nil {
		t.Fatal(err)
	}
	consent := models.TeenConsent{TeenUserID: teen.ID, Scope: models.TeenConsentAccount, Status: models.ConsentGranted, GrantedBy: f.issuer.ID, GranterRole: models.ConsentAdmin, EvidenceText: "Written guardian consent", At: time.Now().Add(-time.Hour)}
	if err := f.app.DB.Create(&consent).Error; err != nil {
		t.Fatal(err)
	}
	access := seedReadInvitation(t, f.app, f.issuer, teen, f.event, f.dates[0])
	if err := f.app.contactMutation(contactChange{UserID: teen.ID}, func(tx *gorm.DB) error {
		return tx.Create(&models.TeenConsent{TeenUserID: teen.ID, Scope: models.TeenConsentAccount, Status: models.ConsentRevoked, GrantedBy: f.issuer.ID, GranterRole: models.ConsentAdmin, At: time.Now()}).Error
	}); err != nil {
		t.Fatal(err)
	}
	var saved models.OccurrenceAccess
	if err := f.app.DB.First(&saved, access.ID).Error; err != nil {
		t.Fatal(err)
	}
	if saved.Status != policy.OccurrenceAccessEnded || saved.EndReason != policy.OccurrenceConsentLost {
		t.Fatalf("consent loss not irreversible: %+v", saved)
	}
	if err := f.app.contactMutation(contactChange{UserID: teen.ID}, func(tx *gorm.DB) error {
		return tx.Create(&models.TeenConsent{TeenUserID: teen.ID, Scope: models.TeenConsentAccount, Status: models.ConsentGranted, GrantedBy: f.issuer.ID, GranterRole: models.ConsentAdmin, EvidenceText: "Renewed guardian consent", At: time.Now().Add(time.Second)}).Error
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.app.DB.First(&saved, access.ID).Error; err != nil {
		t.Fatal(err)
	}
	if saved.Status != policy.OccurrenceAccessEnded {
		t.Fatal("consent regrant revived invitation")
	}
}

func occurrenceMutationPath(f occurrenceReadFixture, action string) string {
	return dateReadPath(f.event.ID, f.dates[1].OriginalDate) + "/" + action
}

func TestOccurrenceRecipientControlsHiddenScopeAndNoRevival(t *testing.T) {
	for _, operation := range []string{"decline", "block", "registration"} {
		t.Run(operation, func(t *testing.T) {
			f := newOccurrenceReadFixture(t)
			otherDate := seedReadInvitation(t, f.app, f.issuer, f.recipient, f.event, f.dates[2])
			method, body := "POST", any(nil)
			if operation == "registration" {
				method, body = "PUT", map[string]any{"status": "declined"}
			}
			w := f.stranger.do(method, occurrenceMutationPath(f, operation), body, true)
			if w.Code != 404 {
				t.Fatal("unrelated recipient", w.Code, w.Body.String())
			}
			w = f.client.do(method, occurrenceMutationPath(f, operation), body, true)
			if w.Code != 200 || strings.Contains(w.Body.String(), "group") || strings.Contains(w.Body.String(), "Hidden") {
				t.Fatal(w.Code, w.Body.String())
			}
			row := savedOccurrenceAccess(t, f)
			if row.Status != "ended" || row.EndedBy == nil || *row.EndedBy != f.recipient.ID {
				t.Fatal("selected access not ended", row)
			}
			var reg models.Registration
			if err := f.app.DB.First(&reg, row.RegistrationID).Error; err != nil {
				t.Fatal(err)
			}
			if reg.Status != models.RegDeclined {
				t.Fatal("attendance not cancelled", reg)
			}
			if err := f.app.DB.First(&otherDate, otherDate.ID).Error; err != nil {
				t.Fatal(err)
			}
			if otherDate.Status != "active" {
				t.Fatal("other accepted date cancelled")
			}
			apiDo(t, f.client, "GET", dateReadPath(f.event.ID, f.dates[1].OriginalDate), nil, 404)
			apiDo(t, f.client, "GET", dateReadPath(f.event.ID, f.dates[2].OriginalDate), nil, 200)
			apiDo(t, f.client, "PUT", occurrenceMutationPath(f, "registration"), map[string]any{"status": "going"}, 404)
			if operation == "block" {
				if contactCount(t, f.app, &models.ManagerAddBlock{}, "subject_user_id = ? AND organizer_type = ? AND organizer_id = ?", f.recipient.ID, "group", f.group) != 1 {
					t.Fatal("incorrect organizer scope")
				}
				// Every legacy account target format must honor the private block.
				if err := f.app.DB.Model(&models.Event{}).Where("id = ?", f.event.ID).Update("visibility", models.VisibilityPublic).Error; err != nil {
					t.Fatal(err)
				}
				for _, target := range []map[string]any{{"userId": f.recipient.ID}, {"email": f.recipient.Email}} {
					target["date"] = f.dates[0].OriginalDate
					apiDo(t, f.admin, "POST", "/api/v1/events/"+itoa(f.event.ID)+"/registrations/add", target, 403)
				}
				// Voluntary ordinary RSVP is independent; it cannot reopen access.
				apiDo(t, f.client, "PUT", "/api/v1/events/"+itoa(f.event.ID)+"/registrations", map[string]any{"date": f.dates[1].OriginalDate, "status": "going"}, 200)
				if savedOccurrenceAccess(t, f).Status != "ended" {
					t.Fatal("ordinary RSVP revived provenance")
				}
			}
		})
	}
}

func TestOccurrenceScopedRSVPSafeAndCurrent(t *testing.T) {
	f := newOccurrenceReadFixture(t)
	apiDo(t, f.client, "PUT", occurrenceMutationPath(f, "registration"), map[string]any{"status": "maybe"}, 200)
	if savedOccurrenceAccess(t, f).Status != "active" {
		t.Fatal("maybe ended invitation")
	}
	w := f.client.do("GET", dateReadPath(f.event.ID, f.dates[1].OriginalDate), nil, false)
	if w.Code != 200 || strings.Contains(w.Body.String(), "private-video") {
		t.Fatal("maybe retained video", w.Body.String())
	}
	apiDo(t, f.client, "PUT", occurrenceMutationPath(f, "registration"), map[string]any{"status": "going", "people": []map[string]any{{"personId": 1, "status": "going"}}}, 404)
	apiDo(t, f.client, "PUT", occurrenceMutationPath(f, "registration"), map[string]any{"status": "going"}, 200)
	apiDo(t, f.client, "PUT", dateReadPath(f.event.ID, f.dates[0].OriginalDate)+"/registration", map[string]any{"status": "going"}, 404)
}

func TestOccurrenceRecipientBlockClosesOnlyConflictingPendingAsks(t *testing.T) {
	f := newOccurrenceReadFixture(t)
	now := time.Now().UTC()
	requests := []models.ContactRequest{}
	for _, subject := range []uint{f.recipient.ID, f.other.ID} {
		r := models.ContactRequest{Purpose: policy.RequestOccurrenceAdd, RequesterID: f.issuer.ID, SubjectUserID: subject, ContextType: "event", ContextID: f.event.ID, OccurrenceID: &f.dates[0].ID, AddAttendanceMode: "in_person", SubjectDecision: "pending", AdminDecision: "not_needed", Status: "pending", CreatedAt: now, ExpiresAt: now.Add(policy.ContactRequestLifetime)}
		if err := f.app.DB.Create(&r).Error; err != nil {
			t.Fatal(err)
		}
		if err := f.app.DB.Transaction(func(tx *gorm.DB) error {
			_, err := contactNotice(tx, r, notify.ContactSubjectReview, []uint{subject}, "test")
			return err
		}); err != nil {
			t.Fatal(err)
		}
		requests = append(requests, r)
	}
	apiDo(t, f.client, "POST", occurrenceMutationPath(f, "block"), nil, 200)
	for i, r := range requests {
		if err := f.app.DB.First(&r, r.ID).Error; err != nil {
			t.Fatal(err)
		}
		want := "ended"
		if i == 1 {
			want = "pending"
		}
		if r.Status != want {
			t.Fatalf("incorrect pending closure %+v", r)
		}
		if i == 0 && contactCount(t, f.app, &models.Notification{}, "kind = ? AND subject_id = ? AND resolved_at IS NULL", notify.AddSubjectReview, r.ID) != 0 {
			t.Fatal("conflicting task still open")
		}
	}
}

func TestOccurrenceRecipientEndPromotesOnceAndAuditFailureRollsBack(t *testing.T) {
	f := newOccurrenceReadFixture(t)
	if err := f.app.DB.Model(&models.Event{}).Where("id = ?", f.event.ID).Updates(map[string]any{"visibility": models.VisibilityPublic, "capacity": 1, "waitlist_enabled": true}).Error; err != nil {
		t.Fatal(err)
	}
	apiDo(t, f.stranger, "PUT", "/api/v1/events/"+itoa(f.event.ID)+"/registrations", map[string]any{"date": f.dates[1].OriginalDate, "status": "going"}, 200)
	name := "occurrence:test_recipient_audit_failure"
	if err := f.app.DB.Callback().Create().Before("gorm:create").Register(name, func(db *gorm.DB) {
		if row, ok := db.Statement.Dest.(*models.AuditLog); ok && row.Action == audit.OccurrenceAccessEnded {
			db.AddError(errors.New("injected recipient end audit failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	apiDo(t, f.client, "POST", occurrenceMutationPath(f, "block"), nil, 500)
	if savedOccurrenceAccess(t, f).Status != "active" {
		t.Fatal("failed end persisted")
	}
	if contactCount(t, f.app, &models.ManagerAddBlock{}, "subject_user_id = ?", f.recipient.ID) != 0 {
		t.Fatal("block survived rollback")
	}
	if contactCount(t, f.app, &models.Registration{}, "user_id = ? AND status = ?", f.other.ID, models.RegWaitlisted) != 1 {
		t.Fatal("promotion survived rollback")
	}
	f.app.DB.Callback().Create().Remove(name)
	apiDo(t, f.client, "POST", occurrenceMutationPath(f, "decline"), nil, 200)
	apiDo(t, f.client, "POST", occurrenceMutationPath(f, "decline"), nil, 404)
	if contactCount(t, f.app, &models.Registration{}, "user_id = ? AND status = ?", f.other.ID, models.RegGoing) != 1 {
		t.Fatal("released seat not promoted")
	}
	if contactCount(t, f.app, &models.AuditLog{}, "action = ? AND target_id = ?", audit.OccurrenceAccessEnded, f.access.ID) != 1 {
		t.Fatal("duplicate end")
	}
}

func TestOccurrenceOrdinaryRSVPDeclineEndsInvitationImmediately(t *testing.T) {
	f := newOccurrenceReadFixture(t)
	if err := f.app.DB.Model(&models.Event{}).Where("id = ?", f.event.ID).Update("visibility", models.VisibilityPublic).Error; err != nil {
		t.Fatal(err)
	}
	apiDo(t, f.client, "PUT", "/api/v1/events/"+itoa(f.event.ID)+"/registrations", map[string]any{"date": f.dates[1].OriginalDate, "status": "declined"}, 200)
	if savedOccurrenceAccess(t, f).Status != "ended" {
		t.Fatal("ordinary RSVP decline left special access")
	}
}

func TestOccurrenceManagerRemovalUsesCurrentAuthorityAndPromotes(t *testing.T) {
	f := newOccurrenceReadFixture(t)
	if err := f.app.DB.Model(&models.Event{}).Where("id = ?", f.event.ID).Updates(map[string]any{"visibility": models.VisibilityPublic, "capacity": 1, "waitlist_enabled": true}).Error; err != nil {
		t.Fatal(err)
	}
	apiDo(t, f.stranger, "PUT", "/api/v1/events/"+itoa(f.event.ID)+"/registrations", map[string]any{"date": f.dates[1].OriginalDate, "status": "going"}, 200)
	path := "/api/v1/events/" + itoa(f.event.ID) + "/registrations/remove"
	apiDo(t, f.admin, "POST", path, map[string]any{"registrationId": f.access.RegistrationID, "date": f.dates[1].OriginalDate, "note": "Never disclose hidden management reason"}, 200)
	if savedOccurrenceAccess(t, f).EndReason != policy.OccurrenceRemoved {
		t.Fatal("manager removal not immediate")
	}
	if contactCount(t, f.app, &models.Registration{}, "user_id = ? AND status = ?", f.other.ID, models.RegGoing) != 1 {
		t.Fatal("manager removal did not release invitation seat")
	}
	apiDo(t, f.admin, "POST", path, map[string]any{"registrationId": f.access.RegistrationID, "date": f.dates[1].OriginalDate}, 404)
}

func TestOccurrenceRecipientGateRaceAndStaleSession(t *testing.T) {
	for _, scenario := range []string{"decline_block", "stale_session", "manager_role_loss"} {
		t.Run(scenario, func(t *testing.T) {
			f := newOccurrenceReadFixture(t)
			gate := holdContactGate(t, f.app)
			path := occurrenceMutationPath(f, "decline")
			client := cloneContactClient(f.client)
			body := any(nil)
			if scenario == "manager_role_loss" {
				path = "/api/v1/events/" + itoa(f.event.ID) + "/registrations/remove"
				client = cloneContactClient(f.admin)
				body = map[string]any{"registrationId": f.access.RegistrationID, "date": f.dates[1].OriginalDate}
			}
			first := queueContact(func() *httptest.ResponseRecorder { return client.do("POST", path, body, true) })
			waitContactWaiters(t, f.app, 1)
			if scenario == "decline_block" {
				secondClient := cloneContactClient(f.client)
				second := queueContact(func() *httptest.ResponseRecorder {
					return secondClient.do("POST", occurrenceMutationPath(f, "block"), nil, true)
				})
				waitContactWaiters(t, f.app, 2)
				if err := gate.Commit().Error; err != nil {
					t.Fatal(err)
				}
				codes := map[int]int{}
				for _, ch := range []<-chan *httptest.ResponseRecorder{first, second} {
					codes[awaitContactResponse(t, ch).Code]++
				}
				if codes[200] != 1 || codes[404] != 1 {
					t.Fatal("race outcomes", codes)
				}
				if contactCount(t, f.app, &models.AuditLog{}, "action = ? AND target_id = ?", audit.OccurrenceAccessEnded, f.access.ID) != 1 {
					t.Fatal("race ended twice")
				}
			} else {
				if scenario == "stale_session" {
					if err := gate.Model(f.recipient).Update("session_version", gorm.Expr("session_version + 1")).Error; err != nil {
						t.Fatal(err)
					}
				} else {
					if err := gate.Model(f.issuer).Update("role", models.RoleMember).Error; err != nil {
						t.Fatal(err)
					}
					if err := gate.Where("user_id = ?", f.issuer.ID).Delete(&models.Membership{}).Error; err != nil {
						t.Fatal(err)
					}
				}
				if err := gate.Commit().Error; err != nil {
					t.Fatal(err)
				}
				w := awaitContactResponse(t, first)
				want := 404
				if scenario == "manager_role_loss" {
					want = 403
				}
				if w.Code != want {
					t.Fatal("stale authority", w.Code, w.Body.String())
				}
				if savedOccurrenceAccess(t, f).Status != "active" {
					t.Fatal("stale actor ended access")
				}
			}
		})
	}
}

func TestOccurrenceRecipientCannotGoAfterRemoteAndGuestRulesChange(t *testing.T) {
	f := newOccurrenceReadFixture(t)
	if err := f.app.DB.Model(&models.Registration{}).Where("id = ?", f.access.RegistrationID).Updates(map[string]any{"attendance_mode": models.AttendanceRemote, "guest_count": 2}).Error; err != nil {
		t.Fatal(err)
	}
	apiDo(t, f.client, "POST", occurrenceMutationPath(f, "decline"), nil, 200)
	if savedOccurrenceAccess(t, f).Status != "ended" {
		t.Fatal("changed capabilities blocked cancellation")
	}
}

func TestOccurrenceRecipientChurchWideBlockIsEventScoped(t *testing.T) {
	f := newOccurrenceReadFixture(t)
	w := f.admin.do("POST", "/api/v1/events", map[string]any{"title": "Church date", "startsLocal": feedStart(12).Format("2006-01-02T15:04"), "durationMinutes": 60, "tzid": "UTC", "visibility": models.VisibilityPrivate}, true)
	if w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
	var b struct {
		Event eventJSON `json:"event"`
	}
	decodeJSON(t, w, &b)
	var occ models.EventOccurrence
	if err := f.app.DB.Where("event_id = ?", b.Event.ID).First(&occ).Error; err != nil {
		t.Fatal(err)
	}
	seedReadInvitation(t, f.app, f.issuer, f.recipient, b.Event, occ)
	apiDo(t, f.client, "POST", dateReadPath(b.Event.ID, occ.OriginalDate)+"/block", nil, 200)
	if contactCount(t, f.app, &models.ManagerAddBlock{}, "subject_user_id = ? AND organizer_type = ? AND organizer_id = ?", f.recipient.ID, "event", b.Event.ID) != 1 {
		t.Fatal("church-wide block not event-scoped")
	}
	if savedOccurrenceAccess(t, f).Status != "active" {
		t.Fatal("unrelated group invitation changed")
	}
}

func TestOccurrenceRecipientConsentLossRefusesMutation(t *testing.T) {
	f := newOccurrenceReadFixture(t)
	teen := newTestUser(t, f.app, "recipient consent teen", "password1234", models.RoleMember)
	birth := time.Now().UTC().AddDate(-15, 0, 0)
	if err := f.app.DB.Model(teen).Updates(map[string]any{"account_type": models.AccountTeen, "birth_date": birth}).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.app.DB.Create(&models.TeenConsent{TeenUserID: teen.ID, Scope: models.TeenConsentAccount, Status: models.ConsentGranted, GrantedBy: f.issuer.ID, GranterRole: models.ConsentAdmin, EvidenceText: "Written guardian consent", At: time.Now().Add(-time.Hour)}).Error; err != nil {
		t.Fatal(err)
	}
	seedReadInvitation(t, f.app, f.issuer, teen, f.event, f.dates[0])
	client := loginAPI(t, f.app, teen.Name, "password1234")
	if err := f.app.DB.Create(&models.TeenConsent{TeenUserID: teen.ID, Scope: models.TeenConsentAccount, Status: models.ConsentRevoked, GrantedBy: f.issuer.ID, GranterRole: models.ConsentAdmin, At: time.Now()}).Error; err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"decline", "block", "registration"} {
		method, body := "POST", any(nil)
		if action == "registration" {
			method, body = "PUT", map[string]any{"status": "going"}
		}
		apiDo(t, client, method, dateReadPath(f.event.ID, f.dates[0].OriginalDate)+"/"+action, body, 404)
	}
}

func TestOccurrenceRegistrationCoreRejectsUnverifiedTeenAndWrongPurpose(t *testing.T) {
	f := newGroupAskFixture(t)
	ev, occ := seedEvent(t, f.app, f.group, f.leader.ID, time.Now().Add(24*time.Hour))
	groupAsk := createGroupAsk(t, f, f.lc, f.leader, false)
	for _, proof := range []*verifiedOccurrenceAdd{nil, {requestID: groupAsk.ID}} {
		err := f.app.DB.Transaction(func(tx *gorm.DB) error {
			if err := lockContacts(tx); err != nil {
				return err
			}
			var subject models.User
			if err := tx.Clauses(clause.Locking{Strength: "NO KEY UPDATE"}).First(&subject, f.subject.ID).Error; err != nil {
				return err
			}
			locked, err := lockOccurrence(tx, occ.ID)
			if err != nil {
				return err
			}
			_, _, err = f.app.addAccountRegistrationLocked(tx, &ev, locked, subject.ID, f.leader.ID, managerRegistrationOptions{approved: proof, mode: models.AttendanceInPerson}, time.Now().UTC(), "")
			return err
		})
		if !errors.Is(err, errPeopleAddForbidden) {
			t.Fatal("core accepted unverified teen", err)
		}
	}
	if contactCount(t, f.app, &models.Registration{}, "occurrence_id = ? AND user_id = ?", occ.ID, f.subject.ID) != 0 {
		t.Fatal("failed proof wrote attendance")
	}
}

func TestOccurrenceRecipientMiddlewareRefusalsAreAudited(t *testing.T) {
	f := newOccurrenceReadFixture(t)
	for _, action := range []string{"decline", "block", "registration"} {
		method := "POST"
		if action == "registration" {
			method = "PUT"
		}
		w := f.client.do(method, occurrenceMutationPath(f, action), map[string]any{"status": "going", "secret": "do not log this"}, false)
		if w.Code != 403 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if contactCount(t, f.app, &models.AuditLog{}, "action = ? AND target_type = ?", audit.OccurrenceRefused, "occurrence_endpoint") != 3 {
		t.Fatal("missing middleware refusals")
	}
	if savedOccurrenceAccess(t, f).Status != "active" {
		t.Fatal("middleware refusal ran mutation")
	}
}

func createOccurrenceAsk(t *testing.T, f occurrenceReadFixture, subject *models.User, occurrence models.EventOccurrence, extra map[string]any) addRequestJSON {
	t.Helper()
	f.app.Config.SessionSecret = []byte("occurrence-ask-test-secret")
	selection, err := f.app.newPeopleSelection(f.issuer.ID, subject.ID, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	body := map[string]any{"selectionId": selection, "contextType": "event", "contextId": f.event.ID, "occurrenceId": occurrence.ID}
	for key, value := range extra {
		body[key] = value
	}
	w := f.admin.do("POST", "/api/v1/add-requests", body, true)
	if w.Code != 201 {
		t.Fatal("create occurrence ask", w.Code, w.Body.String())
	}
	var b struct {
		Request addRequestJSON `json:"request"`
	}
	decodeJSON(t, w, &b)
	return b.Request
}

func TestOccurrenceAskAdultExactPreviewAndAtomicCompletion(t *testing.T) {
	f := newOccurrenceReadFixture(t)
	r := createOccurrenceAsk(t, f, f.other, f.dates[0], map[string]any{"reason": "Private location and contact should never enter preview"})
	w := f.stranger.do("GET", "/api/v1/add-requests/"+itoa(r.ID), nil, false)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, key := range []string{"Private location", "groupName", "contextId", "requesterName", "subjectName", "location", "hosts", "recurrenceRule", f.issuer.Email} {
		if strings.Contains(w.Body.String(), key) {
			t.Fatal("unsafe preview", key, w.Body.String())
		}
	}
	if r.EventTitle == "" || r.Date != f.dates[0].OriginalDate || r.StartsAt == nil {
		t.Fatal("missing minimal preview", r)
	}
	apiDo(t, f.stranger, "GET", dateReadPath(f.event.ID, f.dates[0].OriginalDate), nil, 404)
	apiDo(t, f.stranger, "POST", "/api/v1/add-requests/"+itoa(r.ID)+"/decision", map[string]string{"party": "subject", "decision": "approved"}, 200)
	apiDo(t, f.stranger, "GET", dateReadPath(f.event.ID, f.dates[0].OriginalDate), nil, 200)
	apiDo(t, f.stranger, "GET", dateReadPath(f.event.ID, f.dates[1].OriginalDate), nil, 404)
	apiDo(t, f.stranger, "GET", "/api/v1/events/"+itoa(f.event.ID), nil, 404)
	if contactCount(t, f.app, &models.OccurrenceAccess{}, "request_id = ? AND status = ?", r.ID, "active") != 1 {
		t.Fatal("missing request access")
	}
	if contactCount(t, f.app, &models.ContactGrant{}, "request_id = ?", r.ID) != 0 {
		t.Fatal("add became contact grant")
	}
	apiDo(t, f.stranger, "POST", "/api/v1/add-requests/"+itoa(r.ID)+"/decision", map[string]string{"party": "subject", "decision": "approved"}, 403)
}

func TestOccurrenceAskCapacityAndChildcareRetainDecisionsThenRetry(t *testing.T) {
	for _, kind := range []string{"capacity", "childcare"} {
		t.Run(kind, func(t *testing.T) {
			f := newOccurrenceReadFixture(t)
			extra := map[string]any{}
			updates := map[string]any{"capacity": 1}
			if kind == "childcare" {
				updates = map[string]any{"childcare_available": true, "childcare_capacity": 1}
				extra["childcareCount"] = 1
			}
			if err := f.app.DB.Model(&models.Event{}).Where("id = ?", f.event.ID).Updates(updates).Error; err != nil {
				t.Fatal(err)
			}
			r := createOccurrenceAsk(t, f, f.other, f.dates[1], extra)
			if kind == "childcare" {
				if err := f.app.DB.Model(&models.Registration{}).Where("id = ?", f.access.RegistrationID).Update("childcare_count", 1).Error; err != nil {
					t.Fatal(err)
				}
			}
			w := addDecision(f.stranger, r.ID, "subject", "approved")
			if w.Code != 200 || !strings.Contains(w.Body.String(), `"completionProblem":"`+kind+`"`) {
				t.Fatal(w.Code, w.Body.String())
			}
			var saved models.ContactRequest
			if err := f.app.DB.First(&saved, r.ID).Error; err != nil {
				t.Fatal(err)
			}
			if saved.Status != "pending" || saved.SubjectDecision != "approved" || saved.CompletedTargetID != nil {
				t.Fatal("approval rolled back or false success", saved)
			}
			if contactCount(t, f.app, &models.Registration{}, "occurrence_id = ? AND user_id = ?", f.dates[1].ID, f.other.ID) != 0 {
				t.Fatal("partial registration")
			}
			if contactCount(t, f.app, &models.Notification{}, "kind = ? AND subject_id = ? AND resolved_at IS NULL", notify.AddSubjectReview, r.ID) != 0 {
				t.Fatal("decision task not resolved")
			}
			if err := f.app.DB.Model(&models.Event{}).Where("id = ?", f.event.ID).Updates(map[string]any{"capacity": 2, "childcare_capacity": 2}).Error; err != nil {
				t.Fatal(err)
			}
			apiDo(t, f.admin, "POST", "/api/v1/add-requests/"+itoa(r.ID)+"/complete", nil, 200)
			if err := f.app.DB.First(&saved, r.ID).Error; err != nil {
				t.Fatal(err)
			}
			if saved.Status != "granted" || !saved.ExpiresAt.Equal(r.ExpiresAt) {
				t.Fatal("retry failure or extended expiry", saved)
			}
		})
	}
}

func TestOccurrenceAskTeenBothOrdersAndSafeGuardianNotice(t *testing.T) {
	for _, first := range []string{"subject", "admin"} {
		t.Run(first, func(t *testing.T) {
			f := newOccurrenceReadFixture(t)
			teen := f.other
			birth := time.Now().AddDate(-15, 0, 0)
			if err := f.app.DB.Model(teen).Updates(map[string]any{"account_type": models.AccountTeen, "birth_date": birth}).Error; err != nil {
				t.Fatal(err)
			}
			if err := f.app.DB.Create(&models.TeenConsent{TeenUserID: teen.ID, Scope: models.TeenConsentAccount, Status: models.ConsentGranted, GrantedBy: f.issuer.ID, GranterRole: models.ConsentAdmin, EvidenceText: "Written consent", At: time.Now().Add(-time.Hour)}).Error; err != nil {
				t.Fatal(err)
			}
			guardian := newTestUser(t, f.app, "completion guardian", "password1234", models.RoleMember)
			if err := f.app.DB.Create(&models.GuardianLink{GuardianUserID: guardian.ID, TeenUserID: &teen.ID, Status: models.GuardianConfirmed}).Error; err != nil {
				t.Fatal(err)
			}
			r := createOccurrenceAsk(t, f, teen, f.dates[0], nil)
			clients := map[string]*apiClient{"subject": f.stranger, "admin": f.admin}
			second := "admin"
			if first == "admin" {
				second = "subject"
			}
			for _, party := range []string{first, second} {
				if w := addDecision(clients[party], r.ID, party, "approved"); w.Code != 200 {
					t.Fatal(w.Code, w.Body.String())
				}
			}
			if contactCount(t, f.app, &models.OccurrenceAccess{}, "request_id = ?", r.ID) != 1 {
				t.Fatal("teen not completed")
			}
			var notices []models.Notification
			if err := f.app.DB.Where("kind = ? AND user_id = ?", notify.OccurrenceAdded, guardian.ID).Find(&notices).Error; err != nil {
				t.Fatal(err)
			}
			if len(notices) != 1 || notices[0].Link != occurrenceDatePath(f.event.ID, f.dates[0].OriginalDate) {
				t.Fatal("guardian safe copy", notices)
			}
		})
	}
}

func TestOccurrenceAskParallelCompletionAndNewAskAfterEnd(t *testing.T) {
	f := newOccurrenceReadFixture(t)
	r := createOccurrenceAsk(t, f, f.other, f.dates[0], nil)
	gate := holdContactGate(t, f.app)
	firstClient, secondClient := cloneContactClient(f.stranger), cloneContactClient(f.stranger)
	first := queueContact(func() *httptest.ResponseRecorder { return addDecision(firstClient, r.ID, "subject", "approved") })
	waitContactWaiters(t, f.app, 1)
	second := queueContact(func() *httptest.ResponseRecorder { return addDecision(secondClient, r.ID, "subject", "approved") })
	waitContactWaiters(t, f.app, 2)
	if err := gate.Commit().Error; err != nil {
		t.Fatal(err)
	}
	codes := map[int]int{}
	for _, ch := range []<-chan *httptest.ResponseRecorder{first, second} {
		codes[awaitContactResponse(t, ch).Code]++
	}
	if codes[200] != 1 || codes[403] != 1 {
		t.Fatal(codes)
	}
	if contactCount(t, f.app, &models.OccurrenceAccess{}, "request_id = ?", r.ID) != 1 || contactCount(t, f.app, &models.Registration{}, "occurrence_id = ? AND user_id = ?", f.dates[0].ID, f.other.ID) != 1 {
		t.Fatal("duplicate effects")
	}
	apiDo(t, f.stranger, "POST", dateReadPath(f.event.ID, f.dates[0].OriginalDate)+"/decline", nil, 200)
	next := createOccurrenceAsk(t, f, f.other, f.dates[0], nil)
	apiDo(t, f.stranger, "POST", "/api/v1/add-requests/"+itoa(next.ID)+"/decision", map[string]string{"party": "subject", "decision": "approved"}, 200)
	if contactCount(t, f.app, &models.OccurrenceAccess{}, "subject_user_id = ? AND occurrence_id = ?", f.other.ID, f.dates[0].ID) != 2 {
		t.Fatal("new ask did not append retained history")
	}
	apiDo(t, f.stranger, "POST", "/api/v1/add-requests/"+itoa(r.ID)+"/complete", nil, 403)
}

func TestOccurrenceDirectAdultsRaceCapacityProvenanceAndRateLimit(t *testing.T) {
	f := newOccurrenceReadFixture(t)
	if err := f.app.DB.Model(&models.Event{}).Where("id = ?", f.event.ID).Updates(map[string]any{"visibility": models.VisibilityPublic, "capacity": 2}).Error; err != nil {
		t.Fatal(err)
	}
	users := []*models.User{}
	for i := 0; i < 10; i++ {
		users = append(users, newTestUser(t, f.app, "seat racer "+itoa(uint(i)), "password1234", models.RoleMember))
	}
	gate := holdContactGate(t, f.app)
	channels := []<-chan *httptest.ResponseRecorder{}
	for i, u := range users {
		client, target := cloneContactClient(f.admin), u
		channels = append(channels, queueContact(func() *httptest.ResponseRecorder {
			return client.do("POST", "/api/v1/events/"+itoa(f.event.ID)+"/registrations/add", map[string]any{"userId": target.ID, "date": f.dates[1].OriginalDate}, true)
		}))
		waitContactWaiters(t, f.app, i+1)
	}
	if err := gate.Commit().Error; err != nil {
		t.Fatal(err)
	}
	codes := map[int]int{}
	for _, ch := range channels {
		w := awaitContactResponse(t, ch)
		codes[w.Code]++
	}
	if codes[200] != 1 || codes[409] != 9 {
		t.Fatal("capacity race", codes)
	}
	if contactCount(t, f.app, &models.Registration{}, "occurrence_id = ? AND status = ?", f.dates[1].ID, models.RegGoing) != 2 {
		t.Fatal("overfilled")
	}
	var added models.OccurrenceAccess
	if err := f.app.DB.Where("occurrence_id = ? AND request_id IS NULL", f.dates[1].ID).First(&added).Error; err != nil {
		t.Fatal(err)
	}
	apiDo(t, f.admin, "POST", "/api/v1/events/"+itoa(f.event.ID)+"/registrations/add", map[string]any{"userId": added.SubjectUserID, "date": f.dates[1].OriginalDate}, 200)
	if contactCount(t, f.app, &models.OccurrenceAccess{}, "subject_user_id = ? AND occurrence_id = ?", added.SubjectUserID, added.OccurrenceID) != 1 {
		t.Fatal("duplicate provenance")
	}
	if err := f.app.DB.Model(&models.LoginThrottle{}).Where("key = ?", "manager-date-add:"+itoa(f.issuer.ID)).Update("failures", managerDateAddLimit).Error; err != nil {
		t.Fatal(err)
	}
	w := f.admin.do("POST", "/api/v1/events/"+itoa(f.event.ID)+"/registrations/add", map[string]any{"email": users[0].Email, "date": f.dates[0].OriginalDate}, true)
	if w.Code != 409 || !strings.Contains(w.Body.String(), "date_add_throttled") {
		t.Fatal("throttle", w.Code, w.Body.String())
	}
	admin := f.admin.do("GET", "/api/v1/events/"+itoa(f.event.ID), nil, false)
	if admin.Code != 200 || !strings.Contains(admin.Body.String(), `"addedByLeader":true`) {
		t.Fatal("manager provenance", admin.Body.String())
	}
	normal := f.client.do("GET", "/api/v1/events/"+itoa(f.event.ID), nil, false)
	if strings.Contains(normal.Body.String(), "addedByLeader") {
		t.Fatal("provenance leaked")
	}
}

func TestOccurrenceAskBlockBeforeFinalDecisionAndAuditFailure(t *testing.T) {
	f := newOccurrenceReadFixture(t)
	r := createOccurrenceAsk(t, f, f.recipient, f.dates[0], nil)
	gate := holdContactGate(t, f.app)
	blockClient, decisionClient := cloneContactClient(f.client), cloneContactClient(f.client)
	block := queueContact(func() *httptest.ResponseRecorder {
		return blockClient.do("POST", occurrenceMutationPath(f, "block"), nil, true)
	})
	waitContactWaiters(t, f.app, 1)
	decision := queueContact(func() *httptest.ResponseRecorder { return addDecision(decisionClient, r.ID, "subject", "approved") })
	waitContactWaiters(t, f.app, 2)
	if err := gate.Commit().Error; err != nil {
		t.Fatal(err)
	}
	if w := awaitContactResponse(t, block); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := awaitContactResponse(t, decision); w.Code != 403 {
		t.Fatal(w.Code, w.Body.String())
	}
	if contactCount(t, f.app, &models.OccurrenceAccess{}, "request_id = ?", r.ID) != 0 {
		t.Fatal("block lost to stale approval")
	}
	// A different subject's completion audit failure must roll back all effects.
	other := createOccurrenceAsk(t, f, f.other, f.dates[0], nil)
	name := "occurrence:test_completion_failure"
	if err := f.app.DB.Callback().Create().Before("gorm:create").Register(name, func(db *gorm.DB) {
		if row, ok := db.Statement.Dest.(*models.AuditLog); ok && row.Action == audit.AddCompleted {
			db.AddError(errors.New("injected completion audit failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer f.app.DB.Callback().Create().Remove(name)
	apiDo(t, f.stranger, "POST", "/api/v1/add-requests/"+itoa(other.ID)+"/decision", map[string]string{"party": "subject", "decision": "approved"}, 500)
	var saved models.ContactRequest
	if err := f.app.DB.First(&saved, other.ID).Error; err != nil {
		t.Fatal(err)
	}
	if saved.SubjectDecision != "pending" || saved.Status != "pending" || contactCount(t, f.app, &models.OccurrenceAccess{}, "request_id = ?", other.ID) != 0 || contactCount(t, f.app, &models.Registration{}, "user_id = ? AND occurrence_id = ?", f.other.ID, f.dates[0].ID) != 0 {
		t.Fatal("partial completion survived audit failure")
	}
}

func TestOccurrenceAskTerminalChangesPreventCompletion(t *testing.T) {
	for _, scenario := range []string{"denied", "expired", "disabled", "role", "removed"} {
		t.Run(scenario, func(t *testing.T) {
			f := newOccurrenceReadFixture(t)
			r := createOccurrenceAsk(t, f, f.other, f.dates[0], nil)
			switch scenario {
			case "denied":
				apiDo(t, f.stranger, "POST", "/api/v1/add-requests/"+itoa(r.ID)+"/decision", map[string]string{"party": "subject", "decision": "denied"}, 200)
			case "expired":
				if err := f.app.sweepContacts(t.Context(), r.ExpiresAt); err != nil {
					t.Fatal(err)
				}
			case "disabled":
				if err := f.app.contactMutation(contactChange{UserID: f.other.ID}, func(tx *gorm.DB) error { return tx.Model(f.other).Update("status", models.UserDisabled).Error }); err != nil {
					t.Fatal(err)
				}
			case "role":
				if err := f.app.contactMutation(contactChange{UserID: f.issuer.ID}, func(tx *gorm.DB) error {
					if err := tx.Model(f.issuer).Update("role", models.RoleMember).Error; err != nil {
						return err
					}
					return tx.Where("user_id = ?", f.issuer.ID).Delete(&models.Membership{}).Error
				}); err != nil {
					t.Fatal(err)
				}
			case "removed":
				reg := models.Registration{UserID: &f.other.ID, OccurrenceID: f.dates[0].ID, Status: models.RegMaybe, AttendanceMode: models.AttendanceInPerson}
				if err := f.app.DB.Create(&reg).Error; err != nil {
					t.Fatal(err)
				}
				apiDo(t, f.admin, "POST", "/api/v1/events/"+itoa(f.event.ID)+"/registrations/remove", map[string]any{"registrationId": reg.ID, "date": f.dates[0].OriginalDate}, 200)
			}
			// A still-active admin can retry, but no terminal request can complete.
			client := f.stranger
			if scenario == "disabled" {
				client = f.admin
			}
			apiDo(t, client, "POST", "/api/v1/add-requests/"+itoa(r.ID)+"/complete", nil, 403)
			if contactCount(t, f.app, &models.OccurrenceAccess{}, "request_id = ?", r.ID) != 0 {
				t.Fatal("terminal request created access")
			}
		})
	}
}

func TestOccurrenceNewInvitationInvalidatesPromotionToken(t *testing.T) {
	f := newOccurrenceReadFixture(t)
	if err := f.app.DB.Model(&models.Event{}).Where("id = ?", f.event.ID).Updates(map[string]any{"visibility": models.VisibilityPublic, "capacity": 1, "waitlist_enabled": true}).Error; err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/events/" + itoa(f.event.ID) + "/registrations"
	answer := func(c *apiClient, status string) {
		apiDo(t, c, "PUT", path, map[string]any{"date": f.dates[0].OriginalDate, "status": status}, 200)
	}
	answer(f.client, "going")
	answer(f.stranger, "going")
	answer(f.client, "declined")
	raw := manageTokenInEmail(t, latestEmail(t, f.app, f.other.Email))
	manage := "/api/v1/guest-registrations/manage/" + raw
	anon := anonClient(t, f.app, "10.3.8.1")
	apiDo(t, anon, "GET", manage, nil, 200)
	apiDo(t, f.admin, "POST", path+"/add", map[string]any{"userId": f.other.ID, "date": f.dates[0].OriginalDate}, 200)
	apiDo(t, anon, "GET", manage, nil, 404)
	apiDo(t, anon, "POST", manage+"/cancel", nil, 404)
	// Ordinary promotion during an active invitation rotates a credential
	// even though its safe invitation notice does not disclose that token.
	answer(f.stranger, "maybe")
	answer(f.client, "going")
	answer(f.stranger, "going")
	answer(f.client, "declined")
	var retained models.Registration
	if err := f.app.DB.Where("user_id = ? AND occurrence_id = ?", f.other.ID, f.dates[0].ID).First(&retained).Error; err != nil {
		t.Fatal(err)
	}
	if retained.ManageTokenHash == "" {
		t.Fatal("real active-invitation promotion did not rotate credential")
	}
	apiDo(t, f.admin, "POST", path+"/add", map[string]any{"userId": f.other.ID, "date": f.dates[0].OriginalDate}, 200)
	var updated models.Registration
	if err := f.app.DB.First(&updated, retained.ID).Error; err != nil {
		t.Fatal(err)
	}
	if updated.ManageTokenHash != retained.ManageTokenHash {
		t.Fatal("active invitation update invalidated credential")
	}
	apiDo(t, f.stranger, "POST", dateReadPath(f.event.ID, f.dates[0].OriginalDate)+"/decline", nil, 200)
	r := createOccurrenceAsk(t, f, f.other, f.dates[0], nil)
	apiDo(t, f.stranger, "POST", "/api/v1/add-requests/"+itoa(r.ID)+"/decision", map[string]string{"party": "subject", "decision": "approved"}, 200)
	apiDo(t, anon, "GET", manage, nil, 404)
	apiDo(t, anon, "POST", manage+"/cancel", nil, 404)
	if err := f.app.DB.First(&updated, retained.ID).Error; err != nil {
		t.Fatal(err)
	}
	if updated.ManageTokenHash != "" {
		t.Fatal("fresh approved reinvitation retained old credential")
	}
	if regStatus(t, f.app, f.other.ID, f.dates[0].ID) != models.RegGoing || contactCount(t, f.app, &models.OccurrenceAccess{}, "request_id = ? AND status = ?", r.ID, policy.OccurrenceAccessActive) != 1 {
		t.Fatal("old promotion token affected replacement invitation")
	}
}

func TestOccurrenceAddPreservesTokenOnActiveUpdateAndFailedAdd(t *testing.T) {
	for _, scenario := range []string{"active_update", "failed_add"} {
		t.Run(scenario, func(t *testing.T) {
			f := newOccurrenceReadFixture(t)
			if err := f.app.DB.Model(&models.Event{}).Where("id = ?", f.event.ID).Update("visibility", models.VisibilityPublic).Error; err != nil {
				t.Fatal(err)
			}
			regID := f.access.RegistrationID
			date := f.dates[1].OriginalDate
			if scenario == "failed_add" {
				date = f.dates[0].OriginalDate
				reg := models.Registration{UserID: &f.recipient.ID, OccurrenceID: f.dates[0].ID, Status: models.RegMaybe, AttendanceMode: models.AttendanceInPerson}
				if err := f.app.DB.Create(&reg).Error; err != nil {
					t.Fatal(err)
				}
				regID = reg.ID
				if err := f.app.DB.Model(&models.Event{}).Where("id = ?", f.event.ID).Update("capacity", 1).Error; err != nil {
					t.Fatal(err)
				}
				apiDo(t, f.stranger, "PUT", "/api/v1/events/"+itoa(f.event.ID)+"/registrations", map[string]any{"date": date, "status": "going"}, 200)
			}
			hash := hashToken("existing-credential")
			if err := f.app.DB.Model(&models.Registration{}).Where("id = ?", regID).Update("manage_token_hash", hash).Error; err != nil {
				t.Fatal(err)
			}
			want := 200
			if scenario == "failed_add" {
				want = 409
			}
			apiDo(t, f.admin, "POST", "/api/v1/events/"+itoa(f.event.ID)+"/registrations/add", map[string]any{"userId": f.recipient.ID, "date": date}, want)
			var reg models.Registration
			if err := f.app.DB.First(&reg, regID).Error; err != nil {
				t.Fatal(err)
			}
			if reg.ManageTokenHash != hash {
				t.Fatal("non-minting add erased credential")
			}
		})
	}
}

func TestOccurrenceOrdinaryRSVPRetryRevalidatesEndedInvitation(t *testing.T) {
	for _, scenario := range []string{"tighten_then_end", "disable_reenable"} {
		t.Run(scenario, func(t *testing.T) {
			f := newOccurrenceReadFixture(t)
			if err := f.app.DB.Model(&models.Registration{}).Where("id = ?", f.access.RegistrationID).Update("status", models.RegMaybe).Error; err != nil {
				t.Fatal(err)
			}
			if err := f.app.DB.Model(&models.Event{}).Where("id = ?", f.event.ID).Update("visibility", models.VisibilityPublic).Error; err != nil {
				t.Fatal(err)
			}
			gate := holdContactGate(t, f.app)
			var tighten, end <-chan *httptest.ResponseRecorder
			if scenario == "tighten_then_end" {
				manager, recipient := cloneContactClient(f.admin), cloneContactClient(f.client)
				tighten = queueContact(func() *httptest.ResponseRecorder {
					return manager.do("PUT", "/api/v1/events/"+itoa(f.event.ID)+"/visibility", map[string]any{"visibility": models.VisibilityInviteOnly, "confirm": true}, true)
				})
				waitContactWaiters(t, f.app, 1)
				end = queueContact(func() *httptest.ResponseRecorder {
					return recipient.do("POST", occurrenceMutationPath(f, "decline"), nil, true)
				})
				waitContactWaiters(t, f.app, 2)
			}
			client := cloneContactClient(f.client)
			rsvp := queueContact(func() *httptest.ResponseRecorder {
				return client.do("PUT", "/api/v1/events/"+itoa(f.event.ID)+"/registrations", map[string]any{"date": f.dates[1].OriginalDate, "status": "going"}, true)
			})
			n := 1
			if scenario == "tighten_then_end" {
				n = 3
			}
			waitContactWaiters(t, f.app, n)
			if scenario == "disable_reenable" {
				if err := gate.Model(f.recipient).Updates(map[string]any{"status": models.UserDisabled, "session_version": gorm.Expr("session_version + 1")}).Error; err != nil {
					t.Fatal(err)
				}
				if _, err := f.app.reconcileContacts(gate, time.Now().UTC(), contactChange{UserID: f.recipient.ID}); err != nil {
					t.Fatal(err)
				}
				if err := gate.Model(f.recipient).Update("status", models.UserActive).Error; err != nil {
					t.Fatal(err)
				}
			}
			if err := gate.Commit().Error; err != nil {
				t.Fatal(err)
			}
			if tighten != nil {
				for _, ch := range []<-chan *httptest.ResponseRecorder{tighten, end} {
					if w := awaitContactResponse(t, ch); w.Code != 200 {
						t.Fatal(w.Code, w.Body.String())
					}
				}
			}
			if w := awaitContactResponse(t, rsvp); w.Code != 404 {
				t.Fatal("queued RSVP reused stale authority", w.Code, w.Body.String())
			}
			if savedOccurrenceAccess(t, f).Status != policy.OccurrenceAccessEnded {
				t.Fatal("invitation not ended")
			}
			if regStatus(t, f.app, f.recipient.ID, f.dates[1].ID) == models.RegGoing {
				t.Fatal("attendance resurrected")
			}
		})
	}
}

func TestOccurrenceDirectAddEveryPreflightRefusalAuditedSafely(t *testing.T) {
	for _, scenario := range []string{"missing", "hidden", "nonmanager", "childcare_negative", "childcare_unavailable", "childcare_note", "guests", "malformed", "household_missing_date", "household_invalid_holder"} {
		t.Run(scenario, func(t *testing.T) {
			f := newOccurrenceReadFixture(t)
			for _, auditFailure := range []bool{false, true} {
				path := "/api/v1/events/" + itoa(f.event.ID) + "/registrations/add"
				client, want := f.admin, 400
				body := map[string]any{"userId": f.other.ID, "date": f.dates[0].OriginalDate, "email": "secret@example.org", "selectionId": "secret-selection", "reason": "secret-reason"}
				// Keep private submitted data on destination failures; valid target shape
				// on option failures proves we reach the specific validation branch.
				switch scenario {
				case "missing":
					path = "/api/v1/events/987654321/registrations/add"
					want = 404
				case "hidden":
					client = f.stranger
					want = 404
				case "nonmanager":
					client, want = f.stranger, 403
					if err := f.app.DB.Model(&models.Event{}).Where("id = ?", f.event.ID).Update("visibility", models.VisibilityPublic).Error; err != nil {
						t.Fatal(err)
					}
				default:
					delete(body, "selectionId")
					delete(body, "email")
					switch scenario {
					case "childcare_negative":
						body["childcareCount"] = -1
					case "childcare_unavailable":
						body["childcareCount"] = 1
					case "childcare_note":
						body["childcareAgesNote"] = strings.Repeat("secret-note", 30)
					case "guests":
						body["guestCount"] = -1
					case "malformed":
						body["userId"] = "secret-invalid-id"
					case "household_missing_date", "household_invalid_holder":
						delete(body, "userId")
						body["personId"], body["registeredBy"], want = 456789, 987654321, 404
						if scenario == "household_missing_date" {
							body["date"] = "2999-01-01"
						}
					}
				}
				name := "occurrence:test_refusal_audit_failure"
				if auditFailure {
					want = 500
					if err := f.app.DB.Callback().Create().Before("gorm:create").Register(name, func(db *gorm.DB) {
						if row, ok := db.Statement.Dest.(*models.AuditLog); ok && row.Action == audit.OccurrenceRefused {
							db.AddError(errors.New("injected refusal audit failure"))
						}
					}); err != nil {
						t.Fatal(err)
					}
				}
				before := contactCount(t, f.app, &models.AuditLog{}, "action = ?", audit.OccurrenceRefused)
				w := client.do("POST", path, body, true)
				if auditFailure {
					f.app.DB.Callback().Create().Remove(name)
				}
				if w.Code != want {
					t.Fatal(w.Code, w.Body.String())
				}
				n := contactCount(t, f.app, &models.AuditLog{}, "action = ?", audit.OccurrenceRefused)
				if (!auditFailure && n != before+1) || (auditFailure && n != before) {
					t.Fatal("wrong refusal audit count", before, n)
				}
				var audits []models.AuditLog
				if err := f.app.DB.Where("action = ?", audit.OccurrenceRefused).Find(&audits).Error; err != nil {
					t.Fatal(err)
				}
				for _, row := range audits {
					if row.TargetID != 0 || row.TargetType != "occurrence_endpoint" || row.MetaJSON != `{"operation": "manager_add"}` {
						t.Fatalf("unsafe refusal audit %+v", row)
					}
				}
				if contactCount(t, f.app, &models.Registration{}, "occurrence_id = ?", f.dates[0].ID) != 0 || contactCount(t, f.app, &models.OccurrenceAccess{}, "occurrence_id = ?", f.dates[0].ID) != 0 {
					t.Fatal("refused add mutated domain")
				}
			}
		})
	}
}

func occurrenceAskTeen(t *testing.T, f occurrenceReadFixture, birth time.Time) {
	t.Helper()
	if err := f.app.DB.Model(f.other).Updates(map[string]any{"account_type": models.AccountTeen, "birth_date": birth}).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.app.DB.Create(&models.TeenConsent{TeenUserID: f.other.ID, Scope: models.TeenConsentAccount, Status: models.ConsentGranted, GrantedBy: f.issuer.ID, GranterRole: models.ConsentAdmin, EvidenceText: "Written consent", At: time.Now().Add(-time.Hour)}).Error; err != nil {
		t.Fatal(err)
	}
}

func TestOccurrenceAskFinalTeenDecisionQueuedAuthorityLoss(t *testing.T) {
	for _, scenario := range []string{"consent", "session", "requester_role"} {
		t.Run(scenario, func(t *testing.T) {
			f := newOccurrenceReadFixture(t)
			occurrenceAskTeen(t, f, time.Now().UTC().AddDate(-15, 0, 0))
			r := createOccurrenceAsk(t, f, f.other, f.dates[0], nil)
			if w := addDecision(f.admin, r.ID, "admin", "approved"); w.Code != 200 {
				t.Fatal(w.Code, w.Body.String())
			}
			gate := holdContactGate(t, f.app)
			client := cloneContactClient(f.stranger)
			decision := queueContact(func() *httptest.ResponseRecorder { return addDecision(client, r.ID, "subject", "approved") })
			waitContactWaiters(t, f.app, 1)
			switch scenario {
			case "consent":
				if err := gate.Create(&models.TeenConsent{TeenUserID: f.other.ID, Scope: models.TeenConsentAccount, Status: models.ConsentRevoked, GrantedBy: f.issuer.ID, GranterRole: models.ConsentAdmin, At: time.Now()}).Error; err != nil {
					t.Fatal(err)
				}
			case "session":
				if err := gate.Model(f.other).Update("session_version", gorm.Expr("session_version + 1")).Error; err != nil {
					t.Fatal(err)
				}
			case "requester_role":
				if err := gate.Model(f.issuer).Update("role", models.RoleMember).Error; err != nil {
					t.Fatal(err)
				}
				if err := gate.Where("user_id = ?", f.issuer.ID).Delete(&models.Membership{}).Error; err != nil {
					t.Fatal(err)
				}
			}
			if err := gate.Commit().Error; err != nil {
				t.Fatal(err)
			}
			if w := awaitContactResponse(t, decision); w.Code != 403 {
				t.Fatal(w.Code, w.Body.String())
			}
			var saved models.ContactRequest
			if err := f.app.DB.First(&saved, r.ID).Error; err != nil {
				t.Fatal(err)
			}
			if saved.Status == policy.ContactGranted || saved.SubjectDecision != policy.ContactPending || saved.CompletedTargetID != nil || contactCount(t, f.app, &models.OccurrenceAccess{}, "request_id = ?", r.ID) != 0 || contactCount(t, f.app, &models.Registration{}, "user_id = ? AND occurrence_id = ?", f.other.ID, f.dates[0].ID) != 0 {
				t.Fatal("stale final teen decision completed", saved)
			}
		})
	}
}

// The gate is already acquired; prove completion is waiting on a DOMAIN row,
// rather than hoping a sleep places the operation before its critical section.
func waitOccurrenceDomainWaiter(t *testing.T, app *App) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var n int64
		if err := app.DB.Raw(`SELECT count(*) FROM pg_stat_activity WHERE datname = current_database() AND wait_event_type = 'Lock' AND query LIKE '%event_occurrences%FOR UPDATE%'`).Scan(&n).Error; err != nil {
			t.Fatal(err)
		}
		if n > 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("completion never waited on occurrence domain lock")
}

func TestOccurrenceAskExpiryWhileWaitingOnDomainLock(t *testing.T) {
	f := newOccurrenceReadFixture(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	r := models.ContactRequest{Purpose: policy.RequestOccurrenceAdd, RequesterID: f.issuer.ID, SubjectUserID: f.other.ID, ContextType: policy.ContactEvent, ContextID: f.event.ID, OccurrenceID: &f.dates[0].ID, AddAttendanceMode: models.AttendanceInPerson, SubjectDecision: policy.ContactPending, AdminDecision: policy.ContactNotNeeded, Status: policy.ContactPending, CreatedAt: now.Add(-time.Hour), ExpiresAt: now.Add(2 * time.Second)}
	if err := f.app.DB.Create(&r).Error; err != nil {
		t.Fatal(err)
	}
	blocker := f.app.DB.Begin()
	t.Cleanup(func() { blocker.Rollback() })
	if _, err := lockOccurrence(blocker, f.dates[0].ID); err != nil {
		t.Fatal(err)
	}
	client := cloneContactClient(f.stranger)
	decision := queueContact(func() *httptest.ResponseRecorder { return addDecision(client, r.ID, "subject", "approved") })
	waitOccurrenceDomainWaiter(t, f.app)
	<-time.After(time.Until(r.ExpiresAt.Add(time.Millisecond)))
	if err := blocker.Commit().Error; err != nil {
		t.Fatal(err)
	}
	if w := awaitContactResponse(t, decision); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if err := f.app.DB.First(&r, r.ID).Error; err != nil {
		t.Fatal(err)
	}
	if r.Status != policy.ContactExpired || r.CompletedTargetID != nil || contactCount(t, f.app, &models.OccurrenceAccess{}, "request_id = ?", r.ID) != 0 || contactCount(t, f.app, &models.Registration{}, "user_id = ? AND occurrence_id = ?", f.other.ID, f.dates[0].ID) != 0 {
		t.Fatal("expired completion minted attendance", r)
	}
}

func TestOccurrenceAskBirthdayRechecksClockAfterDomainLock(t *testing.T) {
	f := newOccurrenceReadFixture(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	occurrenceAskTeen(t, f, now.AddDate(-18, 0, 0))
	// Model the operation time captured before midnight, without sleeping
	// until a real birthday. All persisted facts, locks and completion are real.
	beforeBirthday := now.AddDate(0, 0, -1)
	r := models.ContactRequest{Purpose: policy.RequestOccurrenceAdd, RequesterID: f.issuer.ID, SubjectUserID: f.other.ID, ContextType: policy.ContactEvent, ContextID: f.event.ID, OccurrenceID: &f.dates[0].ID, AddAttendanceMode: models.AttendanceInPerson, NeedsAdmin: true, SubjectDecision: policy.ContactApproved, AdminDecision: policy.ContactApproved, Status: policy.ContactPending, CreatedAt: beforeBirthday.Add(-time.Hour), ExpiresAt: now.Add(time.Hour)}
	setRequestDecision(&r, f.other.ID, beforeBirthday, policy.ContactSubject, policy.ContactApproved)
	setRequestDecision(&r, f.issuer.ID, beforeBirthday, policy.ContactAdmin, policy.ContactApproved)
	if err := f.app.DB.Create(&r).Error; err != nil {
		t.Fatal(err)
	}
	blocker := f.app.DB.Begin()
	t.Cleanup(func() { blocker.Rollback() })
	if _, err := lockOccurrence(blocker, f.dates[0].ID); err != nil {
		t.Fatal(err)
	}
	completed := make(chan error, 1)
	go func() {
		completed <- f.app.DB.Transaction(func(tx *gorm.DB) error {
			if err := lockContacts(tx); err != nil {
				return err
			}
			op := contactOperation{actor: f.issuer, now: beforeBirthday, zone: time.UTC}
			return f.app.completeAddAsk(tx, &op, &r, "")
		})
	}()
	waitOccurrenceDomainWaiter(t, f.app)
	if err := blocker.Commit().Error; err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-completed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("birthday completion timed out")
	}
	if err := f.app.DB.First(&r, r.ID).Error; err != nil {
		t.Fatal(err)
	}
	if r.Status != policy.ContactEnded || r.EndReason != policy.ContactTurned18 || r.CompletedTargetID != nil || contactCount(t, f.app, &models.OccurrenceAccess{}, "request_id = ?", r.ID) != 0 || contactCount(t, f.app, &models.Registration{}, "user_id = ? AND occurrence_id = ?", f.other.ID, f.dates[0].ID) != 0 {
		t.Fatal("birthday completion minted attendance", r)
	}
}

func TestOccurrenceAskCompletionAtomicOnProvenanceNoticeAndResolutionFailures(t *testing.T) {
	for _, scenario := range []string{"provenance", "notice", "task_resolution"} {
		t.Run(scenario, func(t *testing.T) {
			f := newOccurrenceReadFixture(t)
			r := createOccurrenceAsk(t, f, f.other, f.dates[0], nil)
			// Fully approved but pending is the real capacity-refusal retry state.
			// Leave the review task unresolved to exercise the final resolver too.
			if err := f.app.DB.Model(&models.ContactRequest{}).Where("id = ?", r.ID).Updates(map[string]any{"subject_decision": policy.ContactApproved, "subject_decided_by": f.other.ID, "subject_decided_at": time.Now()}).Error; err != nil {
				t.Fatal(err)
			}
			beforeNotices := contactCount(t, f.app, &models.Notification{}, "1=1")
			beforeAudits := contactCount(t, f.app, &models.AuditLog{}, "1=1")
			name := "occurrence:test_atomic_" + scenario
			fail := func(db *gorm.DB) {
				inject := false
				switch scenario {
				case "provenance":
					_, inject = db.Statement.Dest.(*models.OccurrenceAccess)
				case "notice":
					if row, ok := db.Statement.Dest.(*models.Notification); ok {
						inject = row.Kind == notify.OccurrenceAdded
					}
				case "task_resolution":
					if updates, ok := db.Statement.Dest.(map[string]any); ok && db.Statement.Table == "notifications" {
						_, inject = updates["resolved_at"]
					}
				}
				if inject {
					db.AddError(errors.New("injected " + scenario + " failure"))
				}
			}
			if scenario == "task_resolution" {
				if err := f.app.DB.Callback().Update().Before("gorm:update").Register(name, fail); err != nil {
					t.Fatal(err)
				}
				defer f.app.DB.Callback().Update().Remove(name)
			} else {
				if err := f.app.DB.Callback().Create().Before("gorm:create").Register(name, fail); err != nil {
					t.Fatal(err)
				}
				defer f.app.DB.Callback().Create().Remove(name)
			}
			apiDo(t, f.admin, "POST", "/api/v1/add-requests/"+itoa(r.ID)+"/complete", nil, 500)
			var saved models.ContactRequest
			if err := f.app.DB.First(&saved, r.ID).Error; err != nil {
				t.Fatal(err)
			}
			if saved.Status != policy.ContactPending || saved.CompletedTargetID != nil || contactCount(t, f.app, &models.OccurrenceAccess{}, "request_id = ?", r.ID) != 0 || contactCount(t, f.app, &models.Registration{}, "user_id = ? AND occurrence_id = ?", f.other.ID, f.dates[0].ID) != 0 {
				t.Fatal("partial completion survived failure", saved)
			}
			if contactCount(t, f.app, &models.Notification{}, "1=1") != beforeNotices || contactCount(t, f.app, &models.AuditLog{}, "1=1") != beforeAudits {
				t.Fatal("notice/audit survived failed transaction")
			}
			if contactCount(t, f.app, &models.Notification{}, "kind = ? AND subject_id = ? AND waiting AND resolved_at IS NULL", notify.AddSubjectReview, r.ID) != 1 {
				t.Fatal("review task resolution survived failure")
			}
		})
	}
}
