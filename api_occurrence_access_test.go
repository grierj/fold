package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"fold/internal/events"
	"fold/internal/policy"
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
	access                   accessRow
}

// Seed successful invitation provenance using the real schema, not a route
// which would prematurely enable the deferred occurrence-add mutation.
func seedReadInvitation(t *testing.T, app *App, issuer, subject *models.User, ev eventJSON, occ models.EventOccurrence) accessRow {
	t.Helper()
	now := time.Now().UTC().Add(-time.Minute)
	request := models.ContactRequest{RequesterID: issuer.ID, SubjectUserID: subject.ID,
		Purpose: "occurrence_add", ContextType: "event", ContextID: ev.ID, OccurrenceID: &occ.ID,
		Fields: "", AddAttendanceMode: models.AttendanceInPerson, NeedsAdmin: subject.AccountType == models.AccountTeen,
		SubjectDecision: "pending", AdminDecision: "not_needed", Status: "pending", CreatedAt: now.Add(-time.Hour), ExpiresAt: now.AddDate(0, 0, 14)}
	if request.NeedsAdmin {
		request.AdminDecision = "pending"
	}
	var access accessRow
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
		issuerID := issuer.ID
		var err error
		access, err = seedAccessGrantIn(tx, reg.ID, accessGrant{Origin: policy.OccurrenceOriginManagerAdd, IssuerID: &issuerID, RequestID: &request.ID}, &issuerID, now)
		return err
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

// grants is the fixture's registration as a one-element grant batch.
func (f occurrenceReadFixture) grants(t testing.TB) []models.Registration {
	t.Helper()
	var r models.Registration
	if err := f.app.DB.First(&r, f.access.ID).Error; err != nil {
		t.Fatal(err)
	}
	return []models.Registration{r}
}

func dateReadPath(ev uint, date string) string { return "/api/v1" + occurrenceDatePath(ev, date) }

// newDepartureReadFixture inserts departure provenance while the subject's
// membership is active (the T2 database contract), then removes only that
// membership. It deliberately does not use a departure route, which belongs to
// T4, and therefore exercises the retained reader/lifecycle seam directly.
func newDepartureReadFixture(t *testing.T, visibility, status, origin string) occurrenceReadFixture {
	t.Helper()
	app := testApp(t)
	issuer := newTestUser(t, app, "departure actor", "password1234", models.RoleAdmin)
	recipient := newTestUser(t, app, "departed recipient", "password1234", models.RoleMember)
	other := newTestUser(t, app, "departure outsider", "password1234", models.RoleMember)
	admin := loginAPI(t, app, issuer.Name, "password1234")
	group := createFeedGroup(t, admin, "Retained hidden group", visibility, "Retained group place")
	ev := createFeedEvent(t, admin, group, "Retained hidden series", feedStart(7), "FREQ=WEEKLY;COUNT=3")
	// Private groups remain discoverable to signed-in people. Make the event
	// itself invite-only so the exact-date reader, not ordinary series policy,
	// is what authorizes the retained private-group fixture.
	if visibility == models.VisibilityPrivate {
		if err := app.DB.Model(&models.Event{}).Where("id = ?", ev.ID).Update("visibility", models.VisibilityInviteOnly).Error; err != nil {
			t.Fatal(err)
		}
		ev.Visibility = models.VisibilityInviteOnly
	}
	var dates []models.EventOccurrence
	if err := app.DB.Where("event_id = ?", ev.ID).Order("starts_at").Find(&dates).Error; err != nil || len(dates) != 3 {
		t.Fatal(err, len(dates))
	}
	title, location := "Retained date title", "Retained date place"
	if err := app.DB.Model(&dates[1]).Updates(map[string]any{"title_override": title, "location_override": location}).Error; err != nil {
		t.Fatal(err)
	}
	membership := models.Membership{UserID: recipient.ID, GroupID: group, Role: models.MembershipMember, Status: "active"}
	var access accessRow
	if err := app.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&membership).Error; err != nil {
			return err
		}
		reg := models.Registration{UserID: &recipient.ID, OccurrenceID: dates[1].ID, Status: status, Source: models.RegSourceAdmin, AttendanceMode: models.AttendanceInPerson}
		if status == models.RegWaitlisted {
			at := time.Now().UTC().Add(-time.Minute)
			reg.WaitlistedAt = &at
		}
		if err := tx.Create(&reg).Error; err != nil {
			return err
		}
		actorID := recipient.ID
		if origin == policy.OccurrenceOriginDepartureRemoval {
			actorID = issuer.ID
		}
		var err error
		access, err = seedAccessGrantIn(tx, reg.ID, accessGrant{Origin: origin, DepartureMembershipID: &membership.ID, DepartureActorID: &actorID}, &actorID, time.Now().UTC())
		return err
	}); err != nil {
		t.Fatal("seed validated departure access:", err)
	}
	if err := app.contactMutation(contactChange{UserID: recipient.ID}, func(tx *gorm.DB) error {
		return tx.Model(&models.Membership{}).Where("id = ? AND status = ?", membership.ID, "active").Update("status", "removed").Error
	}); err != nil {
		t.Fatal("remove retained membership:", err)
	}
	return occurrenceReadFixture{app: app, issuer: issuer, recipient: recipient, other: other, admin: admin,
		client: loginAPI(t, app, recipient.Name, "password1234"), stranger: loginAPI(t, app, other.Name, "password1234"), group: group, event: ev, dates: dates, access: access}
}

func TestOccurrenceDateReadExactAuthorityAndAllowlist(t *testing.T) {
	f := newOccurrenceReadFixture(t)
	path := dateReadPath(f.event.ID, models.FormatCalendarDate(f.dates[1].OriginalDate))
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
	for _, date := range []string{models.FormatCalendarDate(f.dates[0].OriginalDate), models.FormatCalendarDate(f.dates[2].OriginalDate), "not-a-date"} {
		apiDo(t, f.client, "GET", dateReadPath(f.event.ID, date), nil, 404)
	}
	for _, path := range []string{fmt.Sprintf("/api/v1/events/%d", f.event.ID), fmt.Sprintf("/api/v1/groups/%d", f.group), fmt.Sprintf("/api/v1/groups/%d/members", f.group), fmt.Sprintf("/api/v1/groups/%d/events", f.group)} {
		apiDo(t, f.client, "GET", path, nil, 404)
	}
	apiDo(t, f.client, "PUT", fmt.Sprintf("/api/v1/events/%d", f.event.ID), map[string]any{"title": "No authority"}, 404)
	apiDo(t, f.client, "POST", fmt.Sprintf("/api/v1/events/%d/registrations/add", f.event.ID), map[string]any{"date": models.FormatCalendarDate(f.dates[1].OriginalDate), "userId": f.other.ID}, 404)
	// Rich lists remain ordinary-only, even when a caller guesses the parent ID.
	if rows := f.app.occurrencesForEvents([]uint{f.event.ID}, models.FormatCalendarDate(f.dates[0].OriginalDate), models.FormatCalendarDate(f.dates[2].OriginalDate), nil, f.recipient); len(rows) != 0 {
		t.Fatal("ordinary list widened", rows)
	}
	seedReadInvitation(t, f.app, f.issuer, f.other, f.event, f.dates[2])
	apiDo(t, f.stranger, "GET", dateReadPath(f.event.ID, models.FormatCalendarDate(f.dates[2].OriginalDate)), nil, 200)
	apiDo(t, f.stranger, "GET", path, nil, 404)
	apiDo(t, f.client, "GET", dateReadPath(f.event.ID, models.FormatCalendarDate(f.dates[2].OriginalDate)), nil, 404)
	// Independent membership preserves the existing rich, series-level view.
	member := newTestUser(t, f.app, "independent member", "password1234", models.RoleMember)
	if err := f.app.DB.Create(&models.Membership{UserID: member.ID, GroupID: f.group, Role: models.MembershipMember, Status: "active"}).Error; err != nil {
		t.Fatal(err)
	}
	rows := f.app.occurrencesForEvents([]uint{f.event.ID}, models.FormatCalendarDate(f.dates[0].OriginalDate), models.FormatCalendarDate(f.dates[2].OriginalDate), nil, member)
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
			// The organizer is derived from the event, so moving a date to a
			// group that is not active is what closes the invitation.
			group := createFeedGroup(t, f.admin, "Replacement organizer", models.VisibilityInviteOnly, "")
			if err := f.app.DB.Model(&models.Group{}).Where("id = ?", group).Update("status", models.GroupArchived).Error; err != nil {
				return err
			}
			return f.app.DB.Model(&models.Event{}).Where("id = ?", f.event.ID).Update("group_id", group).Error
		}},
		{"cancelled", func(f occurrenceReadFixture) error {
			return f.app.DB.Model(&f.dates[1]).Update("status", models.OccurrenceCancelled).Error
		}},
		{"occurrence deleted", func(f occurrenceReadFixture) error {
			// A live grant cannot be deleted with its row: end it first.
			endSeededAccess(t, f.app.DB, f.access.ID, policy.OccurrenceDeleted, nil, time.Now().UTC())
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
			endSeededAccess(t, f.app.DB, f.access.ID, policy.OccurrenceRemoved, &f.issuer.ID, time.Now().UTC())
			return nil
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newOccurrenceReadFixture(t)
			if err := tc.mutate(f); err != nil {
				t.Fatal(err)
			}
			w := f.client.do("GET", dateReadPath(f.event.ID, models.FormatCalendarDate(f.dates[1].OriginalDate)), nil, false)
			if w.Code != 404 && !((tc.name == "disabled" || tc.name == "account deleted") && w.Code == 401) {
				t.Fatal(w.Code, w.Body.String())
			}
			reads, err := f.app.scopedOccurrenceReads(f.recipient, f.app.DB.Where("event_id = ?", f.event.ID), time.Now().UTC())
			if err != nil || len(reads) != 0 {
				t.Fatalf("live facts did not close: %v %+v", err, reads)
			}
			page, raw := getCalendar(t, f.client, "from="+models.FormatCalendarDate(f.dates[0].OriginalDate)+"&to="+models.FormatCalendarDate(f.dates[2].OriginalDate)+"&tz=UTC&mine=1", 200)
			if len(page.Occurrences) != 0 {
				t.Fatal("calendar retained invalid invitation", raw)
			}
			feed := mustBuildFeed(t, f.app, f.recipient, feedOptions{personal: true, scope: models.CalendarFeedAll}, time.Now().UTC())
			if len(feed.Singles) != 0 {
				t.Fatal("feed retained invalid invitation", feed)
			}
		})
	}
}

func TestOccurrenceDateReadStableRescheduleAndIssuerIndependence(t *testing.T) {
	f := newOccurrenceReadFixture(t)
	path := dateReadPath(f.event.ID, models.FormatCalendarDate(f.dates[1].OriginalDate))
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
	added := models.EventOccurrence{EventID: f.event.ID, OriginalDate: calDate(next.AddDate(0, 0, 1).Format("2006-01-02")), StartsAt: next.AddDate(0, 0, 1), EndsAt: next.AddDate(0, 0, 1).Add(time.Hour), Status: models.OccurrenceScheduled}
	if err := f.app.DB.Create(&added).Error; err != nil {
		t.Fatal(err)
	}
	apiDo(t, f.client, "GET", dateReadPath(f.event.ID, models.FormatCalendarDate(added.OriginalDate)), nil, 404)
	// A block does not cancel a different already accepted date.
	if err := f.app.DB.Create(&models.ManagerAddBlock{SubjectUserID: f.recipient.ID, OrganizerType: "group", OrganizerID: f.group}).Error; err != nil {
		t.Fatal(err)
	}
	apiDo(t, f.client, "GET", path, nil, http.StatusOK)
	// Regenerating the same date key creates a different occurrence, not a copy
	// of the old invitation; retained access still names the deleted stable ID.
	// The grant ends with its date before the row can go: a live grant is never deleted.
	endSeededAccess(t, f.app.DB, f.access.ID, policy.OccurrenceDeleted, nil, time.Now().UTC())
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
	path := dateReadPath(f.event.ID, models.FormatCalendarDate(f.dates[0].OriginalDate))
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
	w := f.client.do("GET", dateReadPath(f.event.ID, models.FormatCalendarDate(f.dates[1].OriginalDate)), nil, false)
	if w.Code != 200 || strings.Contains(w.Body.String(), "private-video") || !strings.Contains(w.Body.String(), `"canSeeVideo":false`) || !strings.Contains(w.Body.String(), `"videoHidden":true`) {
		t.Fatal(w.Code, w.Body.String())
	}
	raw := string(events.RenderFeed(mustBuildFeed(t, f.app, f.recipient, feedOptions{personal: true, scope: models.CalendarFeedAll}, time.Now().UTC())))
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
	apiDo(t, f.stranger, "GET", dateReadPath(f.event.ID, models.FormatCalendarDate(f.dates[0].OriginalDate)), nil, 404)
	page, raw := getCalendar(t, f.stranger, "from="+models.FormatCalendarDate(f.dates[0].OriginalDate)+"&to="+models.FormatCalendarDate(f.dates[2].OriginalDate)+"&mine=1", 200)
	if len(page.Occurrences) != 0 {
		t.Fatal("pending ask or registration widened calendar", raw)
	}
	feed := mustBuildFeed(t, f.app, f.other, feedOptions{personal: true, scope: models.CalendarFeedAll}, now)
	if len(feed.Singles) != 0 {
		t.Fatal("pending ask or registration widened feed", feed)
	}
}

func TestOccurrenceInvitationBatchMatchesScalarAndFailsClosed(t *testing.T) {
	f := newOccurrenceReadFixture(t)
	now := time.Now().UTC()
	// The grant is a property of the registration, so a synthetic variant is a
	// registration whose subject or date no longer matches the live schema.
	variants := func() []models.Registration {
		var base models.Registration
		if err := f.app.DB.First(&base, f.access.ID).Error; err != nil {
			t.Fatal(err)
		}
		rows := []models.Registration{base}
		subject, missing := uint(999999), uint(999999)
		bySubject, byDate := base, base
		bySubject.UserID = &subject
		byDate.OccurrenceID = missing
		return append(rows, bySubject, byDate)
	}
	rows := variants()
	compare := func() {
		t.Helper()
		rows = variants()
		facts, err := loadOccurrenceInvitationFacts(f.app.DB, rows, now)
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range rows {
			reason, err := f.app.occurrenceAccessEndReason(f.app.DB, row, now)
			if err != nil {
				t.Fatal(err)
			}
			if reason != facts.endReason(row, now) {
				t.Fatalf("reason batch=%q scalar=%q row=%+v", facts.endReason(row, now), reason, row)
			}
			live, err := liveOccurrenceInvitationIn(f.app.DB, row, now)
			if err != nil {
				t.Fatal(err)
			} // duplicate IDs in synthetic variants are checked individually.
			single, err := occurrenceInvitationReadsIn(f.app.DB, []models.Registration{row}, now)
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
	if _, err := loadOccurrenceInvitationFacts(f.app.DB, rows[:1], now); err != nil {
		t.Fatal(err)
	}
	small := *n
	many := make([]models.Registration, 100)
	for i := range many {
		many[i] = rows[0]
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
	// The batch reads the dates, events and groups; the lifecycle sweep also
	// reads the live grants. Either failing is an error, never an empty answer.
	for _, tc := range []struct {
		table string
		run   func() error
	}{
		{"event_occurrences", func() error { _, err := occurrenceInvitationReadsIn(f.app.DB, rows, now); return err }},
		{"registrations", func() error {
			err := f.app.reconcileOccurrenceAccesses(f.app.DB, now, contactChange{EventID: f.event.ID})
			return err
		}},
	} {
		name := "invitation:fail:" + tc.table
		_ = f.app.DB.Callback().Query().Before("gorm:query").Register(name, func(db *gorm.DB) {
			if db.Statement.Table == tc.table {
				db.AddError(fmt.Errorf("injected provenance read failure"))
			}
		})
		err := tc.run()
		f.app.DB.Callback().Query().Remove(name)
		if err == nil {
			t.Fatalf("%s read error ignored", tc.table)
		}
	}
}

func TestDepartureOccurrenceAccessRetainsOnlyTheSanitizedDate(t *testing.T) {
	for _, tc := range []struct {
		visibility string
		status     string
		origin     string
	}{
		{models.VisibilityPrivate, models.RegGoing, policy.OccurrenceOriginDepartureSelf},
		{models.VisibilityInviteOnly, models.RegMaybe, policy.OccurrenceOriginDepartureRemoval},
		{models.VisibilityInviteOnly, models.RegWaitlisted, policy.OccurrenceOriginDepartureSelf},
	} {
		t.Run(tc.visibility+"/"+tc.status+"/"+tc.origin, func(t *testing.T) {
			f := newDepartureReadFixture(t, tc.visibility, tc.status, tc.origin)
			if savedOccurrenceAccess(t, f).Status != policy.OccurrenceAccessActive {
				t.Fatal("membership departure ended retained access")
			}
			if err := f.app.DB.Create(&models.ManagerAddBlock{SubjectUserID: f.recipient.ID, OrganizerType: policy.ContactGroup, OrganizerID: f.group}).Error; err != nil {
				t.Fatal(err)
			}
			if err := f.app.sweepContacts(t.Context(), time.Now().UTC()); err != nil || savedOccurrenceAccess(t, f).Status != policy.OccurrenceAccessActive {
				t.Fatal("standing departure access was ended by a manager-add block", err)
			}
			path := dateReadPath(f.event.ID, models.FormatCalendarDate(f.dates[1].OriginalDate))
			w := f.client.do("GET", path, nil, false)
			if w.Code != http.StatusOK {
				t.Fatal(w.Code, w.Body.String())
			}
			raw := w.Body.String()
			for _, forbidden := range []string{"Retained hidden group", "Retained hidden series", "departure actor", "issuer", "departureActor", "groupId", "recurrenceRule", "attendees\"", "canManage"} {
				if strings.Contains(raw, forbidden) {
					t.Fatalf("retained date leaked %q: %s", forbidden, raw)
				}
			}
			if !strings.Contains(raw, `"title":"Retained date title"`) || !strings.Contains(raw, `"location":"Retained date place"`) || !strings.Contains(raw, `"myRegistration":"`+tc.status+`"`) {
				t.Fatal("sanitized retained date incomplete", raw)
			}
			for _, hidden := range []string{models.FormatCalendarDate(f.dates[0].OriginalDate), models.FormatCalendarDate(f.dates[2].OriginalDate)} {
				apiDo(t, f.client, "GET", dateReadPath(f.event.ID, hidden), nil, http.StatusNotFound)
			}
			apiDo(t, f.client, "GET", fmt.Sprintf("/api/v1/events/%d", f.event.ID), nil, http.StatusNotFound)
			if tc.visibility == models.VisibilityInviteOnly {
				for _, parent := range []string{fmt.Sprintf("/api/v1/groups/%d", f.group), fmt.Sprintf("/api/v1/groups/%d/members", f.group), fmt.Sprintf("/api/v1/groups/%d/events", f.group)} {
					apiDo(t, f.client, "GET", parent, nil, http.StatusNotFound)
				}
			}

			page, calendarRaw := getCalendar(t, f.client, "from="+models.FormatCalendarDate(f.dates[0].OriginalDate)+"&to="+models.FormatCalendarDate(f.dates[2].OriginalDate)+"&tz=UTC&mine=1", http.StatusOK)
			if len(page.Occurrences) != 1 || page.Occurrences[0].Date != models.FormatCalendarDate(f.dates[1].OriginalDate) || page.Occurrences[0].MyStatus == nil || *page.Occurrences[0].MyStatus != tc.status {
				t.Fatalf("retained calendar=%+v raw=%s", page.Occurrences, calendarRaw)
			}
			if strings.Contains(calendarRaw, "Retained hidden group") || strings.Contains(calendarRaw, "Retained hidden series") || strings.Contains(calendarRaw, "groupId") {
				t.Fatal("calendar leaked retained parent", calendarRaw)
			}
			feed := mustBuildFeed(t, f.app, f.recipient, feedOptions{personal: true, scope: models.CalendarFeedMine}, time.Now().UTC())
			feedRaw := string(events.RenderFeed(feed))
			if len(feed.Singles) != 1 || !strings.Contains(feedRaw, "Retained date title") || strings.Contains(feedRaw, "Retained hidden group") || strings.Contains(feedRaw, "Retained hidden series") {
				t.Fatalf("retained personal feed widened: %s", feedRaw)
			}
			publicRaw := string(events.RenderFeed(mustBuildFeed(t, f.app, nil, feedOptions{name: "Public"}, time.Now().UTC())))
			gid := f.group
			groupRaw := string(events.RenderFeed(mustBuildFeed(t, f.app, nil, feedOptions{name: "Group", onlyGroupID: &gid}, time.Now().UTC())))
			if strings.Contains(publicRaw+groupRaw, "Retained date title") {
				t.Fatal("exact-date authority changed a public/group feed")
			}

			apiDo(t, f.client, "PUT", path+"/registration", map[string]any{"status": models.RegGoing}, http.StatusOK)
			if savedOccurrenceAccess(t, f).Status != policy.OccurrenceAccessActive || regStatus(t, f.app, f.recipient.ID, f.dates[1].ID) != models.RegGoing {
				t.Fatal("own RSVP did not retain departure authority")
			}
		})
	}
}

func TestDepartureOccurrenceAccessExactEndAndMoveCannotResurrect(t *testing.T) {
	f := newDepartureReadFixture(t, models.VisibilityInviteOnly, models.RegGoing, policy.OccurrenceOriginDepartureSelf)
	boundary := f.dates[1].EndsAt
	before, err := occurrenceInvitationReadsIn(f.app.DB, f.grants(t), boundary.Add(-time.Nanosecond))
	if err != nil || before[f.access.ID] == nil {
		t.Fatal("departure access ended before boundary", err)
	}
	at, err := occurrenceInvitationReadsIn(f.app.DB, f.grants(t), boundary)
	if err != nil || at[f.access.ID] != nil {
		t.Fatal("departure access survived exact end", err, at)
	}
	if err := f.app.sweepContacts(t.Context(), boundary); err != nil {
		t.Fatal(err)
	}
	ended := savedOccurrenceAccess(t, f)
	if ended.Status != policy.OccurrenceAccessEnded || ended.EndReason != policy.OccurrenceEnded || ended.EndedAt == nil || !ended.EndedAt.Equal(boundary) {
		t.Fatalf("expiry not persisted at boundary: %+v", ended)
	}
	future := boundary.AddDate(0, 0, 30)
	if err := f.app.contactMutation(contactChange{EventID: f.event.ID}, func(tx *gorm.DB) error {
		return tx.Model(&models.EventOccurrence{}).Where("id = ?", f.dates[1].ID).Updates(map[string]any{"starts_at": future, "ends_at": future.Add(time.Hour)}).Error
	}); err != nil {
		t.Fatal(err)
	}
	apiDo(t, f.client, "GET", dateReadPath(f.event.ID, models.FormatCalendarDate(f.dates[1].OriginalDate)), nil, http.StatusNotFound)
	if savedOccurrenceAccess(t, f).Status != policy.OccurrenceAccessEnded {
		t.Fatal("reschedule resurrected retained access")
	}

	// Model a process that missed the daily sweep: reads close immediately at
	// the old end, and the event-edit transaction persists that expiry before
	// moving the occurrence into the future.
	missed := newDepartureReadFixture(t, models.VisibilityInviteOnly, models.RegGoing, policy.OccurrenceOriginDepartureRemoval)
	past := time.Now().UTC().Add(-time.Hour)
	if err := missed.app.DB.Model(&models.EventOccurrence{}).Where("id = ?", missed.dates[1].ID).Updates(map[string]any{"starts_at": past.Add(-time.Hour), "ends_at": past}).Error; err != nil {
		t.Fatal(err)
	}
	reads, err := missed.app.scopedOccurrenceReads(missed.recipient, missed.app.DB.Where("id = ?", missed.dates[1].ID), time.Now().UTC())
	if err != nil || len(reads) != 0 {
		t.Fatal("read-time expiry stayed open before persistence", err, reads)
	}
	future = time.Now().UTC().AddDate(0, 0, 30)
	if err := missed.app.contactMutation(contactChange{EventID: missed.event.ID}, func(tx *gorm.DB) error {
		return tx.Model(&models.EventOccurrence{}).Where("id = ?", missed.dates[1].ID).Updates(map[string]any{"starts_at": future, "ends_at": future.Add(time.Hour)}).Error
	}); err != nil {
		t.Fatal(err)
	}
	moved := savedOccurrenceAccess(t, missed)
	if moved.Status != policy.OccurrenceAccessEnded || moved.EndReason != policy.OccurrenceEnded {
		t.Fatalf("move resurrected unswept expiry: %+v", moved)
	}
}

func TestManagerOccurrenceAccessKeepsEstablishedPostDateSemantics(t *testing.T) {
	f := newOccurrenceReadFixture(t)
	if err := f.app.sweepContacts(t.Context(), f.dates[1].EndsAt.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if savedOccurrenceAccess(t, f).Status != policy.OccurrenceAccessActive {
		t.Fatal("M1.14 retroactively expired a manager invitation")
	}
}

func TestOccurrenceDateOrganizerFollowsOrdinaryGroupVisibility(t *testing.T) {
	f := newOccurrenceReadFixture(t)
	read := func() dateOccurrenceJSON {
		t.Helper()
		w := f.client.do("GET", dateReadPath(f.event.ID, models.FormatCalendarDate(f.dates[1].OriginalDate)), nil, false)
		if w.Code != http.StatusOK {
			t.Fatalf("read date: %d %s", w.Code, w.Body.String())
		}
		var body struct {
			Occurrence dateOccurrenceJSON `json:"occurrence"`
		}
		decodeJSON(t, w, &body)
		return body.Occurrence
	}
	// An exact-date invitation alone still carries no parent-group metadata.
	invited := read()
	if invited.Visibility != "" || invited.GroupID != nil || invited.GroupName != "" || invited.HostedBy != "" {
		t.Fatalf("invitation widened organizer disclosures: %+v", invited)
	}
	// Opening this date does not open the group or the rest of the series.
	if err := f.app.DB.Model(&f.dates[1]).Update("visibility_override", models.VisibilityPublic).Error; err != nil {
		t.Fatal(err)
	}
	opened := read()
	if opened.Visibility != models.VisibilityPublic || opened.GroupID != nil || opened.GroupName != "" || opened.HostedBy != f.app.siteName() {
		t.Fatalf("hidden group must retain the church fallback: %+v", opened)
	}
	apiDo(t, f.client, "GET", fmt.Sprintf("/api/v1/groups/%d", f.group), nil, http.StatusNotFound)
	apiDo(t, f.client, "GET", fmt.Sprintf("/api/v1/events/%d", f.event.ID), nil, http.StatusNotFound)
	// Independent group membership permits attribution, even while the same
	// person also has an exact-date invitation. The invite itself grants none.
	membership := models.Membership{UserID: f.recipient.ID, GroupID: f.group, Role: models.MembershipMember, Status: "active"}
	if err := f.app.DB.Create(&membership).Error; err != nil {
		t.Fatal(err)
	}
	member := read()
	if member.GroupID == nil || *member.GroupID != f.group || member.GroupName != "Hidden group identity" || member.HostedBy != "" || !member.CanDecline {
		t.Fatalf("ordinary group member organizer: %+v", member)
	}
}

func TestOccurrenceDateChurchWideOrganizer(t *testing.T) {
	f := newOccurrenceReadFixture(t)
	w := f.admin.do("POST", "/api/v1/events", map[string]any{
		"title": "Church gathering", "startsLocal": feedStart(10).Format("2006-01-02T15:04"), "durationMinutes": 60, "tzid": "UTC",
	}, true)
	if w.Code != http.StatusCreated {
		t.Fatalf("create church event: %d %s", w.Code, w.Body.String())
	}
	var created struct {
		Event eventJSON `json:"event"`
	}
	decodeJSON(t, w, &created)
	var date models.EventOccurrence
	if err := f.app.DB.Where("event_id = ?", created.Event.ID).Order("starts_at").First(&date).Error; err != nil {
		t.Fatal(err)
	}
	w = newAPIClient(t, f.app).do("GET", dateReadPath(created.Event.ID, models.FormatCalendarDate(date.OriginalDate)), nil, false)
	if w.Code != http.StatusOK {
		t.Fatalf("public church date: %d %s", w.Code, w.Body.String())
	}
	var body struct {
		Occurrence dateOccurrenceJSON `json:"occurrence"`
	}
	decodeJSON(t, w, &body)
	if row := body.Occurrence; row.HostedBy != f.app.siteName() || row.GroupID != nil || row.GroupName != "" {
		t.Fatalf("church-wide organizer: %+v", row)
	}
}

// The one-date download (DEC-93) is the date page in another format: the same
// 404s, the same video rule, and the feeds' UID.
func TestOccurrenceCalendarFollowsTheDatePage(t *testing.T) {
	f := newOccurrenceReadFixture(t)
	path := dateReadPath(f.event.ID, models.FormatCalendarDate(f.dates[1].OriginalDate)) + "/calendar.ics"
	for name, client := range map[string]*apiClient{"stranger": f.stranger, "signed out": newAPIClient(t, f.app)} {
		if w := client.do("GET", path, nil, false); w.Code != http.StatusNotFound || strings.Contains(w.Body.String(), "VCALENDAR") {
			t.Fatalf("%s: %d %s", name, w.Code, w.Body.String())
		}
	}
	if w := f.client.do("GET", dateReadPath(f.event.ID, models.FormatCalendarDate(f.dates[0].OriginalDate))+"/calendar.ics", nil, false); w.Code != http.StatusNotFound {
		t.Fatalf("another date of the hidden series: %d", w.Code)
	}

	w := f.client.do("GET", path, nil, false)
	body := w.Body.String()
	if w.Code != http.StatusOK || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/calendar") ||
		w.Header().Get("Content-Disposition") != `attachment; filename="event-`+models.FormatCalendarDate(f.dates[1].OriginalDate)+`.ics"` {
		t.Fatalf("download: %d %v", w.Code, w.Header())
	}
	for _, want := range []string{"BEGIN:VEVENT", "SUMMARY:Invited date title", "LOCATION:Invited date place", "private-video", "STATUS:CONFIRMED"} {
		if !strings.Contains(body, want) {
			t.Fatalf("going download lacks %q:\n%s", want, body)
		}
	}
	for _, leak := range []string{"Hidden series title", "Hidden group identity", "X-WR-CALNAME"} {
		if strings.Contains(body, leak) {
			t.Fatalf("download leaks %q:\n%s", leak, body)
		}
	}
	feed := string(events.RenderFeed(mustBuildFeed(t, f.app, f.recipient, feedOptions{personal: true, scope: models.CalendarFeedAll}, time.Now().UTC())))
	uid := regexp.MustCompile(`UID:[^\r\n]+`).FindString(body)
	if uid == "" || !strings.Contains(feed, uid) {
		t.Fatalf("UID %q differs from the personal feed:\n%s", uid, feed)
	}

	if err := f.app.DB.Model(&models.Registration{}).Where("id = ?", f.access.RegistrationID).Update("status", models.RegMaybe).Error; err != nil {
		t.Fatal(err)
	}
	body = f.client.do("GET", path, nil, false).Body.String()
	if strings.Contains(body, "private-video") || !strings.Contains(body, "STATUS:TENTATIVE") {
		t.Fatalf("maybe download:\n%s", body)
	}
}

func TestOccurrenceCalendarPublicDateSignedOut(t *testing.T) {
	f := newOccurrenceReadFixture(t)
	w := f.admin.do("POST", "/api/v1/events", map[string]any{
		"title": "Church picnic", "startsLocal": feedStart(10).Format("2006-01-02T15:04"), "durationMinutes": 60, "tzid": "UTC",
	}, true)
	if w.Code != http.StatusCreated {
		t.Fatalf("create church event: %d %s", w.Code, w.Body.String())
	}
	var created struct {
		Event eventJSON `json:"event"`
	}
	decodeJSON(t, w, &created)
	var date models.EventOccurrence
	if err := f.app.DB.Where("event_id = ?", created.Event.ID).First(&date).Error; err != nil {
		t.Fatal(err)
	}
	w = newAPIClient(t, f.app).do("GET", dateReadPath(created.Event.ID, models.FormatCalendarDate(date.OriginalDate))+"/calendar.ics", nil, false)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "SUMMARY:Church picnic") || strings.Contains(w.Body.String(), "STATUS:") {
		t.Fatalf("public date: %d %s", w.Code, w.Body.String())
	}
}

// One table runs the shared corruption predicate through the Go loader and
// through the direct-SQL trigger: a malformed access snapshot anywhere in the
// two actions and the registration target fails the grant closed, even when its
// subject and date cannot match; valid rows are filtered normally; rows outside
// the scope do not take part.
func TestAccessSnapshotCorruptionPredicateGoAndSQLAgree(t *testing.T) {
	f := newOccurrenceReadFixture(t)
	reg := models.Registration{UserID: &f.other.ID, OccurrenceID: f.dates[0].ID, Status: models.RegGoing, Source: models.RegSourceAdmin, AttendanceMode: models.AttendanceInPerson}
	if err := f.app.DB.Create(&reg).Error; err != nil {
		t.Fatal(err)
	}
	const (
		added, ended = "occurrence_access.added", "occurrence_access.ended"
		big          = "9223372036854775807"
		over         = "9223372036854775808"
	)
	valid := `"subjectUserId":5,"occurrenceId":6,"grantedAtMicro":1788000000000000`
	other := fmt.Sprintf(`{%s,"endedAtMicro":1788000000000001}`, valid)
	for _, tc := range []struct {
		name, action, target, meta string
		corrupt, repeat            bool
	}{
		{"missing subject", added, "registration", `{"occurrenceId":6,"grantedAtMicro":1}`, true, false},
		{"missing occurrence", added, "registration", `{"subjectUserId":5,"grantedAtMicro":1}`, true, false},
		{"missing grant stamp", added, "registration", `{"subjectUserId":5,"occurrenceId":6}`, true, false},
		{"ended without an end stamp", ended, "registration", `{` + valid + `}`, true, false},
		{"null subject", added, "registration", `{"subjectUserId":null,"occurrenceId":6,"grantedAtMicro":1}`, true, false},
		{"string subject", added, "registration", `{"subjectUserId":"5","occurrenceId":6,"grantedAtMicro":1}`, true, false},
		{"boolean occurrence", added, "registration", `{"subjectUserId":5,"occurrenceId":true,"grantedAtMicro":1}`, true, false},
		{"object stamp", added, "registration", `{"subjectUserId":5,"occurrenceId":6,"grantedAtMicro":{"n":1}}`, true, false},
		{"array subject", added, "registration", `{"subjectUserId":[5],"occurrenceId":6,"grantedAtMicro":1}`, true, false},
		{"fractional subject", added, "registration", `{"subjectUserId":1.5,"occurrenceId":6,"grantedAtMicro":1}`, true, false},
		{"decimal-point integer", added, "registration", `{"subjectUserId":5.0,"occurrenceId":6,"grantedAtMicro":1}`, true, false},
		{"zero occurrence", added, "registration", `{"subjectUserId":5,"occurrenceId":0,"grantedAtMicro":1}`, true, false},
		{"negative stamp", added, "registration", `{"subjectUserId":5,"occurrenceId":6,"grantedAtMicro":-1}`, true, false},
		{"overflowing subject", added, "registration", `{"subjectUserId":` + over + `,"occurrenceId":6,"grantedAtMicro":1}`, true, false},
		{"huge end stamp", ended, "registration", `{` + valid + `,"endedAtMicro":99999999999999999999}`, true, false},
		{"array metadata", added, "registration", `[]`, true, false},
		{"scalar metadata", ended, "registration", `"text"`, true, false},
		{"boundary integers are valid", ended, "registration", `{"subjectUserId":` + big + `,"occurrenceId":` + big + `,"grantedAtMicro":` + big + `,"endedAtMicro":` + big + `}`, false, false},
		{"unrelated valid rows are filtered", added, "registration", `{` + valid + `}`, false, false},
		{"unrelated valid ended row", ended, "registration", other, false, false},
		{"another action is not access history", "registration.changed", "registration", `{"subjectUserId":"x"}`, false, false},
		{"a block action is not authority", "occurrence.blocked", "registration", `{"subjectUserId":"x"}`, false, false},
		{"another target type does not participate", added, "event", `{"subjectUserId":"x"}`, false, false},
		{"the old target label does not participate", added, "occurrence_access", `{"subjectUserId":"x"}`, false, false},
		{"a matching valid row makes the add a repeat", added, "registration", fmt.Sprintf(`{"subjectUserId":%d,"occurrenceId":%d,"grantedAtMicro":1788000000000000}`, f.other.ID, f.dates[0].ID), false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stop := errors.New("roll back the probe")
			var goErr, sqlErr error
			var history map[accessKey]accessHistory
			_ = f.app.DB.Transaction(func(tx *gorm.DB) error {
				if err := tx.Exec(`INSERT INTO audit_logs(action, target_type, target_id, meta_json) VALUES (?, ?, 1, ?::jsonb)`, tc.action, tc.target, tc.meta).Error; err != nil {
					t.Fatal(err)
				}
				history, goErr = loadAccessHistory(tx, []uint{f.other.ID}, []uint{f.dates[0].ID})
				sqlErr = tx.Exec(`UPDATE registrations SET access_origin = 'manager_add', access_issuer_id = ?, access_granted_at = now() WHERE id = ?`, f.issuer.ID, reg.ID).Error
				return stop
			})
			switch {
			case tc.corrupt:
				if !errors.Is(goErr, errAccessHistoryCorrupt) {
					t.Fatalf("Go loader accepted corrupt history: %v", goErr)
				}
				if sqlErr == nil || !strings.Contains(sqlErr.Error(), "history is corrupt") {
					t.Fatalf("trigger accepted corrupt history: %v", sqlErr)
				}
			case tc.repeat:
				if goErr != nil || !history[accessKey{f.other.ID, f.dates[0].ID}].Granted {
					t.Fatalf("Go loader missed the matching row: %v %+v", goErr, history)
				}
				if sqlErr == nil || !strings.Contains(sqlErr.Error(), "repeat manager add") {
					t.Fatalf("trigger did not treat the audited grant as a repeat: %v", sqlErr)
				}
			default:
				if goErr != nil || sqlErr != nil {
					t.Fatalf("valid or out-of-scope rows blocked the grant: go=%v sql=%v", goErr, sqlErr)
				}
			}
		})
	}
}

// The history loader is one scan: a corrupt snapshot fails closed even with no
// subject or date to match, and a valid snapshot for someone else is neither
// returned nor an error.
func TestLoadAccessHistoryOneScanFailsClosedAndFilters(t *testing.T) {
	f := newOccurrenceReadFixture(t)
	stop := errors.New("roll back the probe")
	probe := func(meta string, subjects, occurrences []uint) (map[accessKey]accessHistory, error) {
		var history map[accessKey]accessHistory
		var err error
		_ = f.app.DB.Transaction(func(tx *gorm.DB) error {
			if meta != "" {
				if e := tx.Exec(`INSERT INTO audit_logs(action, target_type, target_id, meta_json) VALUES ('occurrence_access.added', 'registration', 1, ?::jsonb)`, meta).Error; e != nil {
					t.Fatal(e)
				}
			}
			history, err = loadAccessHistory(tx, subjects, occurrences)
			return stop
		})
		return history, err
	}
	if _, err := probe(`{"subjectUserId":"x","occurrenceId":6,"grantedAtMicro":1}`, nil, nil); !errors.Is(err, errAccessHistoryCorrupt) {
		t.Fatalf("an unrelated corrupt row with nothing to match: %v", err)
	}
	if got, err := probe(`{"subjectUserId":5,"occurrenceId":6,"grantedAtMicro":1}`, []uint{f.other.ID}, []uint{f.dates[0].ID}); err != nil || len(got) != 0 {
		t.Fatalf("a valid row for someone else: %v %+v", err, got)
	}
	if got, err := probe(`{"subjectUserId":5,"occurrenceId":6,"grantedAtMicro":1}`, nil, nil); err != nil || len(got) != 0 {
		t.Fatalf("a valid row with nothing to match: %v %+v", err, got)
	}
}
