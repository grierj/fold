package main

import (
	"encoding/json"
	"fmt"
	"fold/internal/audit"
	"fold/internal/policy"
	"fold/models"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"
)

func TestGroupMorePrivateTightensAndCancels(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
	admin := loginAPI(t, app, "admin", "password1234")
	gid := createGroupAs(t, admin, "Garden Club", models.VisibilityPublic)
	apiDo(t, admin, "PUT", "/api/v1/groups/"+itoa(gid), map[string]any{"location": "12 Elm Street"}, http.StatusOK)
	mem := newTestUser(t, app, "mem", "password1234", models.RoleMember)
	addMemberEmail(t, admin, gid, mem.Email)
	memC := loginAPI(t, app, "mem", "password1234")
	out := newTestUser(t, app, "out", "password1234", models.RoleMember)
	outC := loginAPI(t, app, "out", "password1234")

	ev := scheduleGroupEvent(t, admin, gid, "Picnic")
	register(t, memC, ev.ID)
	register(t, outC, ev.ID)

	// Public -> private: everyone registered can still see it.
	apiDo(t, admin, "PUT", "/api/v1/groups/"+itoa(gid), map[string]any{"visibility": models.VisibilityPrivate}, http.StatusOK)
	if e := eventRow(t, app, ev.ID); e.Visibility != models.VisibilityPrivate || e.LeaderOpened {
		t.Fatalf("private: %+v", e)
	}
	if regCount(app, out, ev.ID) != 1 {
		t.Fatal("private shut out a signed-in registrant")
	}

	// Preview before saving.
	var impact visibilityImpactJSON
	decodeJSON(t, admin.do("GET", "/api/v1/groups/"+itoa(gid)+"/visibility-impact?visibility=invite_only", nil, false), &impact)
	if len(impact.Tightened) != 1 || impact.Tightened[0].ID != ev.ID || impact.Tightened[0].Registrants != 1 || len(impact.LeaderOpened) != 0 {
		t.Fatalf("impact: %+v", impact)
	}
	apiDo(t, memC, "GET", "/api/v1/groups/"+itoa(gid)+"/visibility-impact?visibility=invite_only", nil, http.StatusForbidden)

	// Private -> invite-only: tightened, the outsider is cancelled and told.
	apiDo(t, admin, "PUT", "/api/v1/groups/"+itoa(gid), map[string]any{"visibility": models.VisibilityInviteOnly}, http.StatusOK)
	if e := eventRow(t, app, ev.ID); e.Visibility != models.VisibilityInviteOnly {
		t.Fatalf("invite-only: %+v", e)
	}
	if regCount(app, out, ev.ID) != 1 || regCount(app, mem, ev.ID) != 1 {
		t.Fatalf("registrations: out %d, mem %d", regCount(app, out, ev.ID), regCount(app, mem, ev.ID))
	}
	rows, metas := auditRows(t, app, audit.RegistrationCancelled)
	if len(rows) != 1 || rows[0].TargetID != ev.ID || metas[0].UserID != out.ID {
		t.Fatalf("cancel audit: %+v", metas)
	}
	var changes []models.AuditLog
	app.DB.Where("action = ? AND target_id = ?", audit.VisibilityChanged, ev.ID).Order("id").Find(&changes)
	if len(changes) != 2 || !strings.Contains(changes[1].MetaJSON, "group_more_private") {
		t.Fatalf("tighten audit: %+v", changes)
	}

	found := false
	for _, n := range noticeIDs(t, outC) {
		if n.Kind == "registration.cancelled" {
			found = true
			raw, _ := json.Marshal(n)
			if !strings.Contains(n.Title, "Picnic") || strings.Contains(string(raw), "Garden Club") ||
				strings.Contains(string(raw), "Elm Street") || strings.Contains(n.Link, "/groups/") || strings.Contains(n.Link, "/events/") {
				t.Fatalf("notice leaks the group: %s", raw)
			}
		}
	}
	if !found {
		t.Fatalf("outsider not told: %+v", noticeIDs(t, outC))
	}
	for _, n := range noticeIDs(t, memC) {
		if n.Kind == "registration.cancelled" {
			t.Fatalf("member told: %+v", n)
		}
	}
	getEvent(t, outC, ev.ID, http.StatusNotFound)
}

func TestLeaderOpenedEventsAskBeforeTightening(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
	admin := loginAPI(t, app, "admin", "password1234")
	newTestUser(t, app, "admin2", "password1234", models.RoleAdmin)
	admin2 := loginAPI(t, app, "admin2", "password1234")
	gid := createGroupAs(t, admin, "Youth", models.VisibilityPrivate)
	leadUser := newTestUser(t, app, "lead", "password1234", models.RoleMember)
	lid := addMemberEmail(t, admin, gid, leadUser.Email)
	apiDo(t, admin, "PUT", fmt.Sprintf("/api/v1/groups/%d/members/%d/role", gid, lid), map[string]any{"role": "leader"}, http.StatusOK)
	lead := loginAPI(t, app, "lead", "password1234")
	out := newTestUser(t, app, "out", "password1234", models.RoleMember)
	outC := loginAPI(t, app, "out", "password1234")

	inherited := scheduleGroupEvent(t, lead, gid, "Bible study")
	kept := scheduleGroupEvent(t, lead, gid, "Car wash")
	tightened := scheduleGroupEvent(t, lead, gid, "Movie night")
	for _, id := range []uint{kept.ID, tightened.ID} {
		setVisibility(t, lead, id, models.VisibilityPublic, "", http.StatusOK)
		if !eventRow(t, app, id).LeaderOpened {
			t.Fatal("opening wider should set the marker")
		}
	}
	register(t, outC, kept.ID)
	register(t, outC, tightened.ID)

	// Opening the group further changes nothing.
	apiDo(t, lead, "PUT", "/api/v1/groups/"+itoa(gid), map[string]any{"visibility": models.VisibilityPublic}, http.StatusOK)
	if e := eventRow(t, app, inherited.ID); e.Visibility != models.VisibilityPrivate {
		t.Fatalf("opening the group moved an inherited event: %+v", e)
	}

	// No decision: 409 with the list, and nothing is saved.
	w := lead.do("PUT", "/api/v1/groups/"+itoa(gid), map[string]any{"visibility": models.VisibilityInviteOnly}, true)
	if w.Code != http.StatusConflict {
		t.Fatalf("want 409: %d %s", w.Code, w.Body.String())
	}
	var conflict struct {
		NeedsDecision bool                 `json:"needsDecision"`
		Impact        visibilityImpactJSON `json:"impact"`
	}
	decodeJSON(t, w, &conflict)
	if !conflict.NeedsDecision || len(conflict.Impact.LeaderOpened) != 2 || len(conflict.Impact.Tightened) != 1 ||
		conflict.Impact.Tightened[0].ID != inherited.ID || conflict.Impact.LeaderOpened[0].Registrants != 1 {
		t.Fatalf("conflict: %+v", conflict)
	}
	var g models.Group
	app.DB.First(&g, gid)
	if g.Visibility != models.VisibilityPublic || eventRow(t, app, inherited.ID).Visibility != models.VisibilityPrivate {
		t.Fatal("a refused change saved something")
	}
	// A bad or partial decision is refused the same way.
	if c := lead.do("PUT", "/api/v1/groups/"+itoa(gid), map[string]any{"visibility": models.VisibilityInviteOnly,
		"openedEvents": map[string]string{itoa(kept.ID): "keep", itoa(tightened.ID): "maybe"}}, true).Code; c != http.StatusConflict {
		t.Fatalf("bad decision: %d", c)
	}

	apiDo(t, lead, "PUT", "/api/v1/groups/"+itoa(gid), map[string]any{"visibility": models.VisibilityInviteOnly,
		"openedEvents": map[string]string{itoa(kept.ID): "keep", itoa(tightened.ID): "tighten"}}, http.StatusOK)
	if e := eventRow(t, app, inherited.ID); e.Visibility != models.VisibilityInviteOnly {
		t.Fatalf("inherited: %+v", e)
	}
	if e := eventRow(t, app, kept.ID); e.Visibility != models.VisibilityPublic || !e.LeaderOpened {
		t.Fatalf("kept: %+v", e)
	}
	if e := eventRow(t, app, tightened.ID); e.Visibility != models.VisibilityInviteOnly || e.LeaderOpened {
		t.Fatalf("tightened: %+v", e)
	}
	if regCount(app, out, kept.ID) != 1 || regCount(app, out, tightened.ID) != 1 {
		t.Fatal("only the tightened event cancels outsiders")
	}
	var keptRows []models.AuditLog
	app.DB.Where("action = ? AND target_id = ?", audit.VisibilityOverride, kept.ID).Order("id").Find(&keptRows)
	if len(keptRows) != 2 || !strings.Contains(keptRows[1].MetaJSON, "Kept open") {
		t.Fatalf("keep is a logged override: %+v", keptRows)
	}
	if !hasVisibilityNotice(noticeIDs(t, admin2), "Car wash", "Kept open") {
		t.Fatalf("other admins see the kept override: %+v", noticeIDs(t, admin2))
	}
	// The outsider still sees the kept event, with a count only.
	page, raw := getEvent(t, outC, kept.ID, http.StatusOK)
	if page.Occurrences[0].AttendeesVisible || strings.Contains(raw, "Youth") {
		t.Fatalf("kept event for an outsider: %s", raw)
	}
}

func TestVisibilityImpactCountsHouseholdSubjectsAcrossDates(t *testing.T) {
	app := testApp(t)
	admin, gid := openGroup(t, app, "HouseholdVisibility")
	eventID, date := seedCappedEvent(t, app, admin, gid, nil, true)
	holder, c, spouse, child := householdFamily(t, app, admin, "visibilityholder")
	answer := func(date string) {
		batchAnswer(t, c, eventID, date, map[string]any{"status": "going", "people": []map[string]any{{"personId": spouse.ID, "status": "going"}, {"personId": child.ID, "status": "going"}}}, http.StatusOK)
	}
	answer(date)
	var o models.EventOccurrence
	if err := app.DB.Where("event_id = ?", eventID).First(&o).Error; err != nil {
		t.Fatal(err)
	}
	o.ID = 0
	o.StartsAt = o.StartsAt.AddDate(0, 0, 7)
	o.EndsAt = o.EndsAt.AddDate(0, 0, 7)
	o.OriginalDate = o.StartsAt.Format("2006-01-02")
	if err := app.DB.Omit("Event").Create(&o).Error; err != nil {
		t.Fatal(err)
	}
	answer(o.OriginalDate)
	var ev models.Event
	app.DB.First(&ev, eventID)
	impact, err := eventImpactOf(app.DB, &ev, models.VisibilityInviteOnly)
	if err != nil {
		t.Fatal(err)
	}
	if len(impact.Tightened) != 1 || impact.Tightened[0].Registrants != 3 {
		t.Fatalf("subject impact %+v", impact)
	}
	apiDo(t, admin, "PUT", "/api/v1/admin/users/"+itoa(holder.ID), map[string]any{"status": "disabled"}, http.StatusOK)
	apiDo(t, admin, "PUT", "/api/v1/events/"+itoa(eventID)+"/visibility", map[string]any{"visibility": "invite_only", "confirm": true}, http.StatusOK)
	var n int64
	app.DB.Model(&models.Registration{}).Where("(user_id = ? OR registered_by = ?) AND status <> ?", holder.ID, holder.ID, models.RegCancelled).Count(&n)
	if n != 0 {
		t.Fatalf("disabled answers can resurface: %d", n)
	}
}

func TestOccurrenceInvitationVisibilityImpactAndCommitAgree(t *testing.T) {
	for _, throughGroup := range []bool{false, true} {
		t.Run(fmt.Sprint("group=", throughGroup), func(t *testing.T) {
			f := newOccurrenceReadFixture(t)
			if err := f.app.DB.Model(&models.Event{}).Where("id = ?", f.event.ID).Updates(map[string]any{"visibility": models.VisibilityPublic, "leader_opened": false}).Error; err != nil {
				t.Fatal(err)
			}
			if err := f.app.DB.Model(&models.Group{}).Where("id = ?", f.group).Update("visibility", models.VisibilityPublic).Error; err != nil {
				t.Fatal(err)
			}
			// The explicit date alone causes no cancellation. A voluntary sibling
			// is an ordinary loser even though it belongs to the same subject.
			var ev models.Event
			if err := f.app.DB.First(&ev, f.event.ID).Error; err != nil {
				t.Fatal(err)
			}
			impact, err := eventImpactOf(f.app.DB, &ev, models.VisibilityInviteOnly)
			if err != nil || len(impact.Tightened) != 1 || impact.Tightened[0].Registrants != 0 {
				t.Fatal("live exact invite counted as loser", impact, err)
			}
			apiDo(t, f.client, "PUT", "/api/v1/events/"+itoa(f.event.ID)+"/registrations", map[string]any{"date": f.dates[0].OriginalDate, "status": "going"}, 200)
			impact, err = eventImpactOf(f.app.DB, &ev, models.VisibilityInviteOnly)
			if err != nil || impact.Tightened[0].Registrants != 1 {
				t.Fatal("sibling not counted", impact, err)
			}
			if throughGroup {
				apiDo(t, f.admin, "PUT", "/api/v1/groups/"+itoa(f.group), map[string]any{"visibility": models.VisibilityInviteOnly, "confirm": true}, 200)
			} else {
				apiDo(t, f.admin, "PUT", "/api/v1/events/"+itoa(f.event.ID)+"/visibility", map[string]any{"visibility": models.VisibilityInviteOnly, "confirm": true}, 200)
			}
			if contactCount(t, f.app, &models.Registration{}, "user_id = ? AND occurrence_id = ? AND status <> ?", f.recipient.ID, f.dates[0].ID, models.RegCancelled) != 0 {
				t.Fatal("ordinary sibling kept")
			}
			if contactCount(t, f.app, &models.Registration{}, "id = ?", f.access.RegistrationID) != 1 || savedOccurrenceAccess(t, f).Status != "active" {
				t.Fatal("explicit date cancelled")
			}
			apiDo(t, f.client, "GET", dateReadPath(f.event.ID, f.dates[1].OriginalDate), nil, 200)
			apiDo(t, f.client, "GET", "/api/v1/events/"+itoa(f.event.ID), nil, 404)
		})
	}
}

func TestVisibilityInvitationCollectionReadBudget(t *testing.T) {
	for _, scope := range []string{"event", "group"} {
		var smallPreview, smallCommit int
		for _, size := range []int{1, 100} {
			t.Run(fmt.Sprintf("%s/%d", scope, size), func(t *testing.T) {
				f := newOccurrenceReadFixture(t)
				if err := f.app.DB.Model(&models.Group{}).Where("id = ?", f.group).Update("visibility", models.VisibilityPublic).Error; err != nil {
					t.Fatal(err)
				}
				if err := f.app.DB.Model(&models.Event{}).Where("id = ?", f.event.ID).Update("visibility", models.VisibilityPublic).Error; err != nil {
					t.Fatal(err)
				}
				past := time.Now().UTC().AddDate(0, -1, 0)
				if err := f.app.DB.Model(&f.dates[2]).Updates(map[string]any{"starts_at": past, "ends_at": past.Add(time.Hour)}).Error; err != nil {
					t.Fatal(err)
				}
				// Retained history is active but irrelevant to upcoming visibility candidates.
				for i := 0; i < size; i++ {
					u := *f.recipient
					u.ID = 0
					u.Email = fmt.Sprintf("budget%d@example.com", i)
					if err := f.app.DB.Create(&u).Error; err != nil {
						t.Fatal(err)
					}
					seedReadInvitation(t, f.app, f.issuer, &u, f.event, f.dates[0])
					seedReadInvitation(t, f.app, f.issuer, &u, f.event, f.dates[2])
				}
				reg := models.Registration{UserID: &f.other.ID, OccurrenceID: f.dates[0].ID, Status: models.RegGoing, Source: "web", AttendanceMode: models.AttendanceInPerson}
				if err := f.app.DB.Create(&reg).Error; err != nil {
					t.Fatal(err)
				}
				root := "/api/v1/events/" + itoa(f.event.ID)
				if scope == "group" {
					root = "/api/v1/groups/" + itoa(f.group)
				}
				n := collectionReadCounter(t, f.app.DB)
				w := f.admin.do("GET", root+"/visibility-impact?visibility=invite_only", nil, false)
				if w.Code != 200 {
					t.Fatal(w.Code, w.Body.String())
				}
				preview := *n
				if scope == "group" {
					var j visibilityImpactJSON
					if err := json.Unmarshal(w.Body.Bytes(), &j); err != nil || len(j.Tightened) != 1 || j.Tightened[0].Registrants != 1 {
						t.Fatal(err, w.Body.String())
					}
				} else {
					if !strings.Contains(w.Body.String(), `"registrants":1`) {
						t.Fatal(w.Body.String())
					}
				}
				*n = 0
				commitPath := root
				if scope == "event" {
					commitPath += "/visibility"
				}
				w = f.admin.do("PUT", commitPath, map[string]any{"visibility": models.VisibilityInviteOnly, "confirm": true}, true)
				if w.Code != 200 {
					t.Fatal(w.Code, w.Body.String())
				}
				commit := *n
				var remains int64
				f.app.DB.Model(&models.Registration{}).Where("id = ? AND status <> ?", reg.ID, models.RegCancelled).Count(&remains)
				if remains != 0 {
					t.Fatal("ordinary loser survived")
				}
				f.app.DB.Model(&models.OccurrenceAccess{}).Where("status = ?", policy.OccurrenceAccessActive).Count(&remains)
				if remains != int64(2*size+1) {
					t.Fatal("live invitations changed", remains)
				}
				if size == 1 {
					smallPreview, smallCommit = preview, commit
				} else if preview != smallPreview || commit != smallCommit {
					t.Fatalf("reads grow with invitations/history preview %d->%d commit %d->%d", smallPreview, preview, smallCommit, commit)
				}
				t.Logf("%s invitations=%d future+%d past preview=%d commit=%d", scope, size, size, preview, commit)
			})
		}
	}
}

func TestVisibilityBatchExcludesEndedAndUnconsentedInvitations(t *testing.T) {
	for _, scope := range []string{"event", "group"} {
		for _, state := range []string{"teen_without_consent", "ended"} {
			t.Run(scope+"/"+state, func(t *testing.T) {
				f := newOccurrenceReadFixture(t)
				if err := f.app.DB.Model(&models.Group{}).Where("id = ?", f.group).Update("visibility", models.VisibilityPublic).Error; err != nil {
					t.Fatal(err)
				}
				if err := f.app.DB.Model(&models.Event{}).Where("id = ?", f.event.ID).Update("visibility", models.VisibilityPublic).Error; err != nil {
					t.Fatal(err)
				}
				if state == "teen_without_consent" {
					birth := time.Now().AddDate(-15, 0, 0)
					if err := f.app.DB.Model(f.recipient).Updates(map[string]any{"account_type": models.AccountTeen, "birth_date": birth}).Error; err != nil {
						t.Fatal(err)
					}
				} else {
					if err := f.app.DB.Model(&f.access).Updates(map[string]any{"status": policy.OccurrenceAccessEnded, "ended_at": time.Now(), "end_reason": policy.OccurrenceDeclined, "ended_by": f.recipient.ID}).Error; err != nil {
						t.Fatal(err)
					}
				}
				root := "/api/v1/events/" + itoa(f.event.ID)
				if scope == "group" {
					root = "/api/v1/groups/" + itoa(f.group)
				}
				w := f.admin.do("GET", root+"/visibility-impact?visibility=invite_only", nil, false)
				if w.Code != 200 || !strings.Contains(w.Body.String(), `"registrants":1`) {
					t.Fatal(w.Code, w.Body.String())
				}
				if scope == "event" {
					root += "/visibility"
				}
				apiDo(t, f.admin, "PUT", root, map[string]any{"visibility": models.VisibilityInviteOnly, "confirm": true}, 200)
				if contactCount(t, f.app, &models.Registration{}, "id = ? AND status <> ?", f.access.RegistrationID, models.RegCancelled) != 0 {
					t.Fatal("stale invitation kept registration")
				}
				apiDo(t, f.client, "GET", dateReadPath(f.event.ID, f.dates[1].OriginalDate), nil, 404)
			})
		}
	}
}

func TestUpdateGroupTighteningUsesLockedVisibilityAndDecisions(t *testing.T) {
	app, admin, _, ev, dates := dateVisibilityFixture(t, models.VisibilityPrivate)
	gid := *ev.GroupID
	path := "/api/v1/groups/" + itoa(gid)
	apiDo(t, admin, "PUT", fmt.Sprintf("/api/v1/events/%d/occurrences/%s", ev.ID, dates[0].OriginalDate), map[string]any{"visibility": models.VisibilityPublic}, http.StatusOK)
	opened := scheduleGroupEvent(t, admin, gid, "Opened series")
	setVisibility(t, admin, opened.ID, models.VisibilityPublic, "", http.StatusOK)
	gate := holdContactGate(t, app)
	// This is a visibility no-op at preflight, but becomes tightening at the gate.
	response := queueContact(func() *httptest.ResponseRecorder {
		return admin.do("PUT", path, map[string]any{"name": "Must roll back", "visibility": models.VisibilityPrivate}, true)
	})
	waitContactWaiters(t, app, 1)
	if err := gate.Model(&models.Group{}).Where("id = ?", gid).Update("visibility", models.VisibilityPublic).Error; err != nil {
		t.Fatal(err)
	}
	if err := gate.Commit().Error; err != nil {
		t.Fatal(err)
	}
	w := awaitContactResponse(t, response)
	if w.Code != http.StatusConflict {
		t.Fatalf("missing decisions: %d %s", w.Code, w.Body.String())
	}
	var conflict struct {
		Impact visibilityImpactJSON `json:"impact"`
	}
	decodeJSON(t, w, &conflict)
	keys := map[string]bool{}
	for _, row := range conflict.Impact.LeaderOpened {
		key := row.DecisionKey
		if key == "" {
			key = itoa(row.ID)
		}
		keys[key] = true
	}
	if len(keys) != 2 || !keys[itoa(opened.ID)] || !keys[dateDecisionKey(dates[0].ID)] {
		t.Fatalf("lost opened series/date decisions: %+v", conflict.Impact)
	}
	var saved models.Group
	if err := app.DB.First(&saved, gid).Error; err != nil {
		t.Fatal(err)
	}
	if saved.Visibility != models.VisibilityPublic || saved.Name == "Must roll back" {
		t.Fatal("decision conflict saved patch")
	}
	apiDo(t, admin, "PUT", path, map[string]any{"visibility": models.VisibilityPrivate, "openedEvents": map[string]string{
		itoa(opened.ID): openedTighten, dateDecisionKey(dates[0].ID): openedTighten,
	}}, http.StatusOK)
	var date models.EventOccurrence
	if err := app.DB.First(&date, dates[0].ID).Error; err != nil {
		t.Fatal(err)
	}
	if eventRow(t, app, opened.ID).Visibility != models.VisibilityPrivate || date.VisibilityOverride == nil || *date.VisibilityOverride != models.VisibilityPrivate {
		t.Fatal("opened series/date decisions were not applied")
	}
	// The audit row records the leader-opened flag the event had before it
	// was tightened, not the cleared one.
	var tightened models.AuditLog
	if err := app.DB.Where("action = ? AND target_id = ? AND meta_json::text LIKE ?", audit.VisibilityChanged, opened.ID, "%leader_chose_tighten%").
		First(&tightened).Error; err != nil {
		t.Fatal(err)
	}
	if meta := strings.ReplaceAll(tightened.MetaJSON, " ", ""); !strings.Contains(meta, `"oldLeaderOpened":true`) || !strings.Contains(meta, `"leaderOpened":false`) {
		t.Fatalf("tighten audit meta: %s", tightened.MetaJSON)
	}
}

func TestUpdateGroupTighteningFailureRollsBackDependents(t *testing.T) {
	for _, table := range []string{"audit_logs", "notifications"} {
		t.Run(table, func(t *testing.T) {
			app := testApp(t)
			newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
			admin := loginAPI(t, app, "admin", "password1234")
			gid := createGroupAs(t, admin, "Original", models.VisibilityPublic)
			ev := scheduleGroupEvent(t, admin, gid, "Rollback event")
			outsider := newTestUser(t, app, "outsider", "password1234", models.RoleMember)
			oc := loginAPI(t, app, outsider.Email, "password1234")
			register(t, oc, ev.ID)
			audits := contactCount(t, app, &models.AuditLog{}, "1=1")
			notices := contactCount(t, app, &models.Notification{}, "1=1")
			mail := contactCount(t, app, &models.MailOutbox{}, "1=1")
			const callback = "group-update:fail-tightening"
			if err := app.DB.Callback().Create().Before("gorm:create").Register(callback, func(db *gorm.DB) {
				if db.Statement.Table == table {
					db.AddError(fmt.Errorf("injected %s failure", table))
				}
			}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { app.DB.Callback().Create().Remove(callback) })
			apiDo(t, admin, "PUT", "/api/v1/groups/"+itoa(gid), map[string]any{"name": "Failed patch", "visibility": models.VisibilityInviteOnly}, http.StatusInternalServerError)
			var saved models.Group
			if err := app.DB.First(&saved, gid).Error; err != nil {
				t.Fatal(err)
			}
			var reg models.Registration
			if err := app.DB.Where("user_id = ?", outsider.ID).First(&reg).Error; err != nil {
				t.Fatal(err)
			}
			if saved.Name != "Original" || saved.Visibility != models.VisibilityPublic || eventRow(t, app, ev.ID).Visibility != models.VisibilityPublic || reg.Status != models.RegGoing ||
				contactCount(t, app, &models.AuditLog{}, "1=1") != audits || contactCount(t, app, &models.Notification{}, "1=1") != notices || contactCount(t, app, &models.MailOutbox{}, "1=1") != mail {
				t.Fatal("failed tightening left group/event/registration/audit/notification/mail changes")
			}
		})
	}
}
