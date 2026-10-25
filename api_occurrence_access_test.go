package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"fold/internal/events"
	"fold/models"
	"gorm.io/gorm"
)

type occurrenceReadFixture struct {
	app                      *App
	issuer, recipient, other *models.User
	admin, client, stranger  *apiClient
	group                    uint
	event                    eventJSON
	dates                    []models.EventOccurrence
	access                   models.OccurrenceAccess
}

// Seed successful invitation provenance using the real schema, not a route
// which would prematurely enable the deferred occurrence-add mutation.
func seedReadInvitation(t *testing.T, app *App, issuer, subject *models.User, ev eventJSON, occ models.EventOccurrence) models.OccurrenceAccess {
	t.Helper()
	now := time.Now().UTC().Add(-time.Minute)
	request := models.ContactRequest{RequesterID: issuer.ID, SubjectUserID: subject.ID,
		Purpose: "occurrence_add", ContextType: "event", ContextID: ev.ID, OccurrenceID: &occ.ID,
		Fields: "", AddAttendanceMode: models.AttendanceInPerson, NeedsAdmin: subject.AccountType == models.AccountTeen,
		SubjectDecision: "pending", AdminDecision: "not_needed", Status: "pending", CreatedAt: now.Add(-time.Hour), ExpiresAt: now.AddDate(0, 0, 14)}
	if request.NeedsAdmin {
		request.AdminDecision = "pending"
	}
	access := models.OccurrenceAccess{}
	err := app.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&request).Error; err != nil {
			return err
		}
		reg := models.Registration{UserID: &subject.ID, OccurrenceID: occ.ID, Status: models.RegGoing, Source: "admin", AttendanceMode: models.AttendanceInPerson}
		if err := tx.Create(&reg).Error; err != nil {
			return err
		}
		update := map[string]any{"subject_decision": "approved", "subject_decided_by": subject.ID, "subject_decided_at": now,
			"status": "granted", "completed_at": now, "completed_target_id": reg.ID}
		if request.NeedsAdmin {
			update["admin_decision"] = "approved"
			update["admin_decided_by"] = issuer.ID
			update["admin_decided_at"] = now
		}
		if err := tx.Model(&request).Updates(update).Error; err != nil {
			return err
		}
		organizer, organizerID := "event", ev.ID
		if ev.GroupID != nil {
			organizer, organizerID = "group", *ev.GroupID
		}
		access = models.OccurrenceAccess{SubjectUserID: subject.ID, OccurrenceID: occ.ID, EventID: ev.ID,
			OrganizerType: organizer, OrganizerID: organizerID, IssuerID: issuer.ID, RequestID: &request.ID,
			RegistrationID: reg.ID, Status: "active", GrantedAt: now}
		return tx.Create(&access).Error
	})
	if err != nil {
		t.Fatal("seed valid invitation:", err)
	}
	return access
}

func newOccurrenceReadFixture(t *testing.T) occurrenceReadFixture {
	t.Helper()
	app := testApp(t)
	issuer := newTestUser(t, app, "invitation issuer", "password1234", models.RoleAdmin)
	recipient := newTestUser(t, app, "invitation recipient", "password1234", models.RoleMember)
	other := newTestUser(t, app, "other recipient", "password1234", models.RoleMember)
	admin := loginAPI(t, app, issuer.Name, "password1234")
	group := createFeedGroup(t, admin, "Hidden group identity", models.VisibilityInviteOnly, "Hidden group fallback")
	ev := createFeedEvent(t, admin, group, "Hidden series title", feedStart(7), "FREQ=WEEKLY;COUNT=3")
	var dates []models.EventOccurrence
	if err := app.DB.Where("event_id = ?", ev.ID).Order("starts_at").Find(&dates).Error; err != nil || len(dates) != 3 {
		t.Fatal(err, len(dates))
	}
	title, location := "Invited date title", "Invited date place"
	if err := app.DB.Model(&dates[1]).Updates(map[string]any{"title_override": title, "location_override": location}).Error; err != nil {
		t.Fatal(err)
	}
	if err := app.DB.Model(&models.Event{}).Where("id = ?", ev.ID).Updates(map[string]any{"video_provider": "link", "video_url": "https://example.com/private-video", "video_reveal": "registered_only"}).Error; err != nil {
		t.Fatal(err)
	}
	access := seedReadInvitation(t, app, issuer, recipient, ev, dates[1])
	return occurrenceReadFixture{app: app, issuer: issuer, recipient: recipient, other: other, admin: admin,
		client: loginAPI(t, app, recipient.Name, "password1234"), stranger: loginAPI(t, app, other.Name, "password1234"), group: group, event: ev, dates: dates, access: access}
}

func dateReadPath(ev uint, date string) string { return "/api/v1" + occurrenceDatePath(ev, date) }

func TestOccurrenceDateReadExactAuthorityAndAllowlist(t *testing.T) {
	f := newOccurrenceReadFixture(t)
	path := dateReadPath(f.event.ID, f.dates[1].OriginalDate)
	w := f.client.do("GET", path, nil, false)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var body struct {
		Occurrence map[string]json.RawMessage `json:"occurrence"`
	}
	decodeJSON(t, w, &body)
	keys := []string{}
	for key := range body.Occurrence {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	want := strings.Fields("canBlock canDecline canSeeAttendees canSeeVideo date dateUrl durationMinutes endsAt endsLocal eventId guestsAllowed id location myAttendanceMode myGuestCount myRegistration remoteAvailable startsAt startsLocal title tzid videoHidden videoProvider videoUrl")
	sort.Strings(want)
	if !reflect.DeepEqual(keys, want) {
		t.Fatalf("exact key allowlist: %v, want %v", keys, want)
	}
	raw := w.Body.String()
	for _, forbidden := range []string{"Hidden group", "Hidden series", "issuer", "@", "recurrenceRule", "series", "hosts", "attendees\"", "contact", "groupId", "canManage"} {
		if strings.Contains(raw, forbidden) {
			t.Fatalf("forbidden %q: %s", forbidden, raw)
		}
	}
	for _, expected := range []string{`"title":"Invited date title"`, `"location":"Invited date place"`, `"canDecline":true`, `"canBlock":true`, `"canSeeAttendees":false`, `"canSeeVideo":true`, `"myRegistration":"going"`, `private-video`} {
		if !strings.Contains(raw, expected) {
			t.Fatalf("missing %s: %s", expected, raw)
		}
	}
	apiDo(t, f.stranger, "GET", path, nil, 404)
	apiDo(t, newAPIClient(t, f.app), "GET", path, nil, 404)
	for _, date := range []string{f.dates[0].OriginalDate, f.dates[2].OriginalDate, "not-a-date"} {
		apiDo(t, f.client, "GET", dateReadPath(f.event.ID, date), nil, 404)
	}
	for _, path := range []string{fmt.Sprintf("/api/v1/events/%d", f.event.ID), fmt.Sprintf("/api/v1/groups/%d", f.group), fmt.Sprintf("/api/v1/groups/%d/members", f.group), fmt.Sprintf("/api/v1/groups/%d/events", f.group)} {
		apiDo(t, f.client, "GET", path, nil, 404)
	}
	apiDo(t, f.client, "PUT", fmt.Sprintf("/api/v1/events/%d", f.event.ID), map[string]any{"title": "No authority"}, 404)
	apiDo(t, f.client, "POST", fmt.Sprintf("/api/v1/events/%d/registrations/add", f.event.ID), map[string]any{"date": f.dates[1].OriginalDate, "userId": f.other.ID}, 404)
	// Rich lists remain ordinary-only, even when a caller guesses the parent ID.
	if rows := f.app.occurrencesForEvents([]uint{f.event.ID}, f.dates[0].OriginalDate, f.dates[2].OriginalDate, nil, f.recipient); len(rows) != 0 {
		t.Fatal("ordinary list widened", rows)
	}
	seedReadInvitation(t, f.app, f.issuer, f.other, f.event, f.dates[2])
	apiDo(t, f.stranger, "GET", dateReadPath(f.event.ID, f.dates[2].OriginalDate), nil, 200)
	apiDo(t, f.stranger, "GET", path, nil, 404)
	apiDo(t, f.client, "GET", dateReadPath(f.event.ID, f.dates[2].OriginalDate), nil, 404)
	// Independent membership preserves the existing rich, series-level view.
	member := newTestUser(t, f.app, "independent member", "password1234", models.RoleMember)
	if err := f.app.DB.Create(&models.Membership{UserID: member.ID, GroupID: f.group, Role: models.MembershipMember, Status: "active"}).Error; err != nil {
		t.Fatal(err)
	}
	rows := f.app.occurrencesForEvents([]uint{f.event.ID}, f.dates[0].OriginalDate, f.dates[2].OriginalDate, nil, member)
	if len(rows) != 3 || rows[0].GroupID == nil || rows[0].RecurrenceRule == "" {
		t.Fatalf("ordinary view changed: %+v", rows)
	}
}

func TestOccurrenceDateReadLiveFactsFailClosed(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(occurrenceReadFixture) error
	}{
		{"disabled", func(f occurrenceReadFixture) error {
			return f.app.DB.Model(f.recipient).Update("status", models.UserDisabled).Error
		}},
		{"account deleted", func(f occurrenceReadFixture) error { return f.app.DB.Delete(f.recipient).Error }},
		{"organizer changed", func(f occurrenceReadFixture) error {
			group := createFeedGroup(t, f.admin, "Replacement organizer", models.VisibilityInviteOnly, "")
			return f.app.DB.Model(&models.Event{}).Where("id = ?", f.event.ID).Update("group_id", group).Error
		}},
		{"cancelled", func(f occurrenceReadFixture) error {
			return f.app.DB.Model(&f.dates[1]).Update("status", models.OccurrenceCancelled).Error
		}},
		{"occurrence deleted", func(f occurrenceReadFixture) error {
			if err := f.app.DB.Where("occurrence_id = ?", f.dates[1].ID).Delete(&models.Registration{}).Error; err != nil {
				return err
			}
			return f.app.DB.Delete(&f.dates[1]).Error
		}},
		{"event deleted", func(f occurrenceReadFixture) error { return f.app.DB.Delete(&models.Event{}, f.event.ID).Error }},
		{"group deleted", func(f occurrenceReadFixture) error { return f.app.DB.Delete(&models.Group{}, f.group).Error }},
		{"group archived", func(f occurrenceReadFixture) error {
			return f.app.DB.Model(&models.Group{}).Where("id = ?", f.group).Update("status", models.GroupArchived).Error
		}},
		{"registration removed", func(f occurrenceReadFixture) error {
			return f.app.DB.Model(&models.Registration{}).Where("id = ?", f.access.RegistrationID).Update("status", models.RegRemoved).Error
		}},
		{"revoked", func(f occurrenceReadFixture) error {
			return f.app.DB.Model(&f.access).Updates(map[string]any{"status": "ended", "ended_at": time.Now().UTC(), "end_reason": "removed", "ended_by": f.issuer.ID}).Error
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newOccurrenceReadFixture(t)
			if err := tc.mutate(f); err != nil {
				t.Fatal(err)
			}
			w := f.client.do("GET", dateReadPath(f.event.ID, f.dates[1].OriginalDate), nil, false)
			if w.Code != 404 && !((tc.name == "disabled" || tc.name == "account deleted") && w.Code == 401) {
				t.Fatal(w.Code, w.Body.String())
			}
			reads, err := f.app.scopedOccurrenceReads(f.recipient, f.app.DB.Where("event_id = ?", f.event.ID), time.Now().UTC())
			if err != nil || len(reads) != 0 {
				t.Fatalf("live facts did not close: %v %+v", err, reads)
			}
			page, raw := getCalendar(t, f.client, "from="+f.dates[0].OriginalDate+"&to="+f.dates[2].OriginalDate+"&tz=UTC&mine=1", 200)
			if len(page.Occurrences) != 0 {
				t.Fatal("calendar retained invalid invitation", raw)
			}
			feed := f.app.buildFeed(f.recipient, feedOptions{personal: true, scope: models.CalendarFeedAll}, time.Now().UTC())
			if len(feed.Events) != 0 || len(feed.Singles) != 0 {
				t.Fatal("feed retained invalid invitation", feed)
			}
		})
	}
}

func TestOccurrenceDateReadStableRescheduleAndIssuerIndependence(t *testing.T) {
	f := newOccurrenceReadFixture(t)
	path := dateReadPath(f.event.ID, f.dates[1].OriginalDate)
	next := f.dates[1].StartsAt.Add(3 * time.Hour)
	if err := f.app.DB.Model(&f.dates[1]).Updates(map[string]any{"starts_at": next, "ends_at": next.Add(time.Hour), "starts_at_override": next}).Error; err != nil {
		t.Fatal(err)
	}
	// Accepted invitations are not contact grants: issuer loss does not end them.
	if err := f.app.DB.Model(f.issuer).Updates(map[string]any{"role": models.RoleMember, "status": models.UserDisabled}).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.app.DB.Model(&models.Membership{}).Where("user_id = ?", f.issuer.ID).Update("role", models.MembershipMember).Error; err != nil {
		t.Fatal(err)
	}
	w := f.client.do("GET", path, nil, false)
	if w.Code != 200 || !strings.Contains(w.Body.String(), next.UTC().Format(time.RFC3339)) {
		t.Fatal(w.Code, w.Body.String())
	}
	added := models.EventOccurrence{EventID: f.event.ID, OriginalDate: next.AddDate(0, 0, 1).Format("2006-01-02"), StartsAt: next.AddDate(0, 0, 1), EndsAt: next.AddDate(0, 0, 1).Add(time.Hour), Status: models.OccurrenceScheduled}
	if err := f.app.DB.Create(&added).Error; err != nil {
		t.Fatal(err)
	}
	apiDo(t, f.client, "GET", dateReadPath(f.event.ID, added.OriginalDate), nil, 404)
	// A block does not cancel a different already accepted date.
	if err := f.app.DB.Create(&models.ManagerAddBlock{SubjectUserID: f.recipient.ID, OrganizerType: "group", OrganizerID: f.group}).Error; err != nil {
		t.Fatal(err)
	}
	apiDo(t, f.client, "GET", path, nil, http.StatusOK)
	// Regenerating the same date key creates a different occurrence, not a copy
	// of the old invitation; retained access still names the deleted stable ID.
	if err := f.app.DB.Where("occurrence_id = ?", f.dates[1].ID).Delete(&models.Registration{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.app.DB.Delete(&f.dates[1]).Error; err != nil {
		t.Fatal(err)
	}
	replacement := models.EventOccurrence{EventID: f.event.ID, OriginalDate: f.dates[1].OriginalDate, StartsAt: next, EndsAt: next.Add(time.Hour), Status: models.OccurrenceScheduled}
	if err := f.app.DB.Create(&replacement).Error; err != nil {
		t.Fatal(err)
	}
	apiDo(t, f.client, "GET", path, nil, 404)
}

func TestOccurrenceDateReadTeenConsentAndVideoAreCurrent(t *testing.T) {
	f := newOccurrenceReadFixture(t)
	teen := newTestUser(t, f.app, "invited teen", "password1234", models.RoleMember)
	birth := time.Now().UTC().AddDate(-15, 0, 0)
	if err := f.app.DB.Model(teen).Updates(map[string]any{"account_type": models.AccountTeen, "birth_date": birth}).Error; err != nil {
		t.Fatal(err)
	}
	granted := models.TeenConsent{TeenUserID: teen.ID, Scope: models.TeenConsentAccount, Status: models.ConsentGranted, GrantedBy: f.issuer.ID, GranterRole: models.ConsentAdmin, EvidenceText: "Written guardian consent", At: time.Now().Add(-time.Hour)}
	if err := f.app.DB.Create(&granted).Error; err != nil {
		t.Fatal(err)
	}
	seedReadInvitation(t, f.app, f.issuer, teen, f.event, f.dates[0])
	client := loginAPI(t, f.app, teen.Name, "password1234")
	path := dateReadPath(f.event.ID, f.dates[0].OriginalDate)
	apiDo(t, client, "GET", path, nil, 200)
	// Fail closed even if status cleanup has not yet changed the active user.
	revoked := models.TeenConsent{TeenUserID: teen.ID, Scope: models.TeenConsentAccount, Status: models.ConsentRevoked, GrantedBy: f.issuer.ID, GranterRole: models.ConsentAdmin, EvidenceText: "Written guardian consent", At: time.Now()}
	if err := f.app.DB.Create(&revoked).Error; err != nil {
		t.Fatal(err)
	}
	apiDo(t, client, "GET", path, nil, 404)
	reads, err := f.app.scopedOccurrenceReads(teen, f.app.DB.Where("event_id = ?", f.event.ID), time.Now().UTC())
	if err != nil || len(reads) != 0 {
		t.Fatal("revoked consent", err, reads)
	}
	// Shared group membership or an original manager is not inherited video
	// authority for an override-only recipient; only this date's Going reveals.
	if err := f.app.DB.Model(&models.Registration{}).Where("id = ?", f.access.RegistrationID).Update("status", models.RegMaybe).Error; err != nil {
		t.Fatal(err)
	}
	w := f.client.do("GET", dateReadPath(f.event.ID, f.dates[1].OriginalDate), nil, false)
	if w.Code != 200 || strings.Contains(w.Body.String(), "private-video") || !strings.Contains(w.Body.String(), `"canSeeVideo":false`) || !strings.Contains(w.Body.String(), `"videoHidden":true`) {
		t.Fatal(w.Code, w.Body.String())
	}
	raw := string(events.RenderFeed(f.app.buildFeed(f.recipient, feedOptions{personal: true, scope: models.CalendarFeedAll}, time.Now().UTC())))
	if strings.Contains(raw, "private-video") {
		t.Fatal("feed inherited video authority", raw)
	}
	if !strings.Contains(raw, "STATUS:TENTATIVE") || strings.Contains(raw, "STATUS:CONFIRMED") {
		t.Fatal("Maybe invitation must remain tentative in the personal feed")
	}
}

func TestOccurrenceDateReadPendingAskAndRegistrationAreNotAuthority(t *testing.T) {
	f := newOccurrenceReadFixture(t)
	now := time.Now().UTC()
	req := models.ContactRequest{RequesterID: f.issuer.ID, SubjectUserID: f.other.ID, Purpose: "occurrence_add", ContextType: "event", ContextID: f.event.ID, OccurrenceID: &f.dates[0].ID, Fields: "", AddAttendanceMode: models.AttendanceInPerson, SubjectDecision: "pending", AdminDecision: "not_needed", Status: "pending", CreatedAt: now, ExpiresAt: now.AddDate(0, 0, 14)}
	if err := f.app.DB.Create(&req).Error; err != nil {
		t.Fatal(err)
	}
	reg := models.Registration{UserID: &f.other.ID, OccurrenceID: f.dates[0].ID, Status: models.RegGoing, Source: models.RegSourceAdmin, AttendanceMode: models.AttendanceInPerson}
	if err := f.app.DB.Create(&reg).Error; err != nil {
		t.Fatal(err)
	}
	apiDo(t, f.stranger, "GET", dateReadPath(f.event.ID, f.dates[0].OriginalDate), nil, 404)
	page, raw := getCalendar(t, f.stranger, "from="+f.dates[0].OriginalDate+"&to="+f.dates[2].OriginalDate+"&mine=1", 200)
	if len(page.Occurrences) != 0 {
		t.Fatal("pending ask or registration widened calendar", raw)
	}
	feed := f.app.buildFeed(f.other, feedOptions{personal: true, scope: models.CalendarFeedAll}, now)
	if len(feed.Events) != 0 || len(feed.Singles) != 0 {
		t.Fatal("pending ask or registration widened feed", feed)
	}
}

func TestOccurrenceInvitationBatchMatchesScalarAndFailsClosed(t *testing.T) {
	f := newOccurrenceReadFixture(t)
	now := time.Now().UTC()
	rows := []models.OccurrenceAccess{f.access}
	for _, field := range []string{"subject", "event", "group", "date", "registration"} {
		r := f.access
		switch field {
		case "subject":
			r.SubjectUserID = 999999
		case "event":
			r.EventID = 999999
		case "group":
			r.OrganizerID = 999999
		case "date":
			r.OccurrenceID = 999999
		case "registration":
			r.RegistrationID = 999999
		}
		rows = append(rows, r)
	}
	compare := func() {
		t.Helper()
		facts, err := loadOccurrenceInvitationFacts(f.app.DB, rows, now)
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range rows {
			reason, err := f.app.occurrenceAccessEndReason(f.app.DB, row, now)
			if err != nil {
				t.Fatal(err)
			}
			if reason != facts.endReason(row) {
				t.Fatalf("reason batch=%q scalar=%q row=%+v", facts.endReason(row), reason, row)
			}
			live, err := liveOccurrenceInvitationIn(f.app.DB, row, now)
			if err != nil {
				t.Fatal(err)
			} // duplicate IDs in synthetic variants are checked individually.
			single, err := occurrenceInvitationReadsIn(f.app.DB, []models.OccurrenceAccess{row}, now)
			if err != nil || (single[row.ID] != nil) != live {
				t.Fatalf("invitation parity %v %v", live, err)
			}
		}
	}
	compare()
	birth := now.AddDate(-15, 0, 0)
	if err := f.app.DB.Model(f.recipient).Updates(map[string]any{"account_type": models.AccountTeen, "birth_date": birth}).Error; err != nil {
		t.Fatal(err)
	}
	compare() // No current account consent: both loaders exclude the teen.
	if err := f.app.DB.Create(&models.TeenConsent{TeenUserID: f.recipient.ID, Scope: models.TeenConsentAccount, Status: models.ConsentGranted, GrantedBy: f.issuer.ID, GranterRole: "admin", EvidenceText: "parent consent", At: now.Add(-time.Minute)}).Error; err != nil {
		t.Fatal(err)
	}
	compare()
	n := collectionReadCounter(t, f.app.DB)
	*n = 0
	if _, err := loadOccurrenceInvitationFacts(f.app.DB, []models.OccurrenceAccess{f.access}, now); err != nil {
		t.Fatal(err)
	}
	small := *n
	many := make([]models.OccurrenceAccess, 100)
	for i := range many {
		many[i] = f.access
	}
	*n = 0
	if _, err := loadOccurrenceInvitationFacts(f.app.DB, many, now); err != nil {
		t.Fatal(err)
	}
	if *n != small || *n > 6 {
		t.Fatalf("fact reads %d->%d budget6", small, *n)
	}
	t.Logf("teen lifecycle fact reads 1=%d 100=%d", small, *n)
	if err := f.app.DB.Model(&f.dates[1]).Update("status", models.OccurrenceCancelled).Error; err != nil {
		t.Fatal(err)
	}
	compare()
	if err := f.app.DB.Model(f.recipient).Update("status", models.UserDisabled).Error; err != nil {
		t.Fatal(err)
	}
	compare()
	_ = f.app.DB.Callback().Query().Before("gorm:query").Register("invitation:fail", func(db *gorm.DB) {
		if db.Statement.Table == "registrations" {
			db.AddError(fmt.Errorf("injected provenance read failure"))
		}
	})
	defer f.app.DB.Callback().Query().Remove("invitation:fail")
	if _, err := occurrenceInvitationReadsIn(f.app.DB, rows, now); err == nil {
		t.Fatal("invitation fact error ignored")
	}
	if _, err := f.app.reconcileOccurrenceAccesses(f.app.DB, now, contactChange{EventID: f.event.ID}); err == nil {
		t.Fatal("lifecycle fact error ignored")
	}
}
