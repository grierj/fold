package main

import (
	"encoding/csv"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"fold/internal/audit"
	"fold/internal/policy"
	"fold/models"
	"gorm.io/gorm"
)

func historyPath(id uint) string {
	return "/api/v1/admin/users/" + strconv.FormatUint(uint64(id), 10) + "/contact-history"
}
func getHistory(t *testing.T, client *apiClient, id uint, query string) contactHistoryJSON {
	t.Helper()
	w := client.do("GET", historyPath(id)+query, nil, false)
	if w.Code != 200 {
		t.Fatalf("history %d: %s", w.Code, w.Body.String())
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("cacheable history")
	}
	var out contactHistoryJSON
	decodeJSON(t, w, &out)
	return out
}
func historyHas(t *testing.T, entries []contactHistoryEntryJSON, action string) {
	t.Helper()
	for _, e := range entries {
		if e.Action == action {
			return
		}
	}
	t.Fatalf("missing %s in %+v", action, entries)
}
func historyBothParties(t *testing.T, f *contactAPIFixture, actions ...string) {
	t.Helper()
	for _, id := range []uint{f.leader.ID, f.subject.ID} {
		entries := getHistory(t, f.ac, id, "?limit=100").Entries
		for _, action := range actions {
			historyHas(t, entries, action)
		}
	}
}

func TestContactHistoryAuthorizationFiltersAndAbsentPerson(t *testing.T) {
	f := newContactAPIFixture(t, false)
	f.create(t, "email", "hidden-reason")
	for _, client := range []*apiClient{f.lc, f.sc, f.gc, f.oc, newAPIClient(t, f.app)} {
		for _, suffix := range []string{"", ".csv"} {
			w := client.do("GET", historyPath(f.subject.ID)+suffix, nil, false)
			if w.Code != 403 && w.Code != 401 {
				t.Fatal(w.Code, w.Body.String())
			}
		}
	}
	if len(getHistory(t, f.ac, 999999, "").Entries) != 0 {
		t.Fatal("missing person not empty")
	}
	emptyCSV := f.ac.do("GET", historyPath(999999)+".csv", nil, false)
	if emptyCSV.Code != 200 {
		t.Fatal(emptyCSV.Code, emptyCSV.Body.String())
	}
	if records, err := csv.NewReader(strings.NewReader(emptyCSV.Body.String())).ReadAll(); err != nil || len(records) != 1 {
		t.Fatal("absent history export", records, err)
	}
	for _, query := range []string{"?limit=0", "?limit=101", "?before=-1", "?before=1.5", "?before=9223372036854775808", "?contextId=1", "?contextType=user", "?purpose=any", "?action=user.deleted", "?from=bad", "?from=2026-01-02T00:00:00Z&until=2026-01-01T00:00:00Z", "?limit=1&limit=2", "?unknown=1", "?before=", "?from=%ZZ"} {
		w := f.ac.do("GET", historyPath(f.subject.ID)+query, nil, false)
		if w.Code != 400 {
			t.Fatalf("%s: %d %s", query, w.Code, w.Body.String())
		}
	}
	for _, path := range []string{"0", "-1", "abc", "9223372036854775808"} {
		if w := f.ac.do("GET", "/api/v1/admin/users/"+path+"/contact-history", nil, false); w.Code != 400 {
			t.Fatal(path, w.Code)
		}
	}
	for _, query := range []string{"?limit=1", "?before=1"} {
		if w := f.ac.do("GET", historyPath(f.subject.ID)+".csv"+query, nil, false); w.Code != 400 {
			t.Fatal(w.Code)
		}
	}
	// Authorization is loaded again for each request, not retained in a client.
	if err := f.app.DB.Model(f.admin).Update("role", models.RoleMember).Error; err != nil {
		t.Fatal(err)
	}
	if w := f.ac.do("GET", historyPath(f.subject.ID), nil, false); w.Code != 403 {
		t.Fatal("stale admin", w.Code)
	}
	if err := f.app.DB.Model(f.admin).Updates(map[string]any{"role": models.RoleAdmin, "session_version": f.admin.SessionVersion + 1}).Error; err != nil {
		t.Fatal(err)
	}
	if w := f.ac.do("GET", historyPath(f.subject.ID)+".csv", nil, false); w.Code != 401 {
		t.Fatal("stale session", w.Code)
	}
	f.ac = loginAPI(t, f.app, f.admin.Email, "password1234")
	if err := f.app.DB.Model(f.admin).Update("status", models.UserDisabled).Error; err != nil {
		t.Fatal(err)
	}
	if w := f.ac.do("GET", historyPath(f.subject.ID), nil, false); w.Code != 401 {
		t.Fatal("disabled admin", w.Code)
	}
}

// Real HTTP transitions, with only the pending request clock moved to exercise
// expiry. Existing s1-s4 concurrency/notification tests remain the domain proof.
func TestContactHistoryContactPathWalkAndDeletionSurvival(t *testing.T) {
	f := newContactAPIFixture(t, true)
	r := f.create(t, "both", "never-export-this-reason")
	g := f.grant(t, r)
	if w := f.reveal(f.lc, g, "email"); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := f.reveal(f.oc, g, "email"); w.Code != 403 {
		t.Fatal(w.Code)
	}
	apiDo(t, f.gc, "POST", "/api/v1/contact-grants/"+itoa(g.ID)+"/revoke", nil, 200)
	r = f.create(t, "phone", "")
	if w := contactDecision(f.sc, r.ID, "subject", "denied"); w.Code != 200 {
		t.Fatal(w.Code)
	}
	r = f.create(t, "phone", "")
	if w := contactDecision(f.ac, r.ID, "admin", "denied"); w.Code != 200 {
		t.Fatal(w.Code)
	}
	r = f.create(t, "phone", "")
	apiDo(t, f.lc, "POST", "/api/v1/contact-requests/"+itoa(r.ID)+"/withdraw", nil, 200)
	r = f.create(t, "email", "")
	past := time.Now().UTC().Add(-15 * 24 * time.Hour)
	if err := f.app.DB.Model(&models.ContactRequest{}).Where("id = ?", r.ID).Updates(map[string]any{"created_at": past, "expires_at": past.Add(14 * 24 * time.Hour)}).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.app.sweepContacts(t.Context(), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	r = f.create(t, "email", "")
	f.grant(t, r)
	// Actual HTTP role/context loss ends a grant, not a synthetic audit row.
	removePath := "/api/v1/admin/groups/" + itoa(f.group.ID) + "/members/" + itoa(f.subject.ID)
	previewPath := "/api/v1/groups/" + itoa(f.group.ID) + "/members/" + itoa(f.subject.ID) + "/departure-preview"
	if w := confirmDeparture(t, f.ac, "DELETE", removePath, previewPath, ""); w.Code != 200 {
		t.Fatalf("remove: %d %s", w.Code, w.Body.String())
	}
	historyBothParties(t, f, audit.ContactRequested, audit.ContactDecision, audit.ContactGranted, audit.ContactReveal, audit.ContactRefused, audit.ContactRevoked, audit.ContactWithdrawn, audit.ContactExpired, audit.ContactEnded)
	entries := getHistory(t, f.ac, f.subject.ID, "?limit=100").Entries
	for _, e := range entries {
		if e.Purpose != "contact" {
			continue
		}
		if e.RequesterID == "" || e.RequesterID != historyID(f.leader.ID) || e.SubjectUserID == "" || e.SubjectUserID != historyID(f.subject.ID) {
			t.Fatalf("attribution %+v", e)
		}
	}
	// Actual deletion routes remove the account/context and convenience notices;
	// retained IDs and history must remain identical with neither live join.
	apiDo(t, f.ac, "DELETE", "/api/v1/admin/users/"+itoa(f.subject.ID), nil, 200)
	apiDo(t, f.ac, "DELETE", "/api/v1/admin/groups/"+itoa(f.group.ID), nil, 200)
	before := getHistory(t, f.ac, f.subject.ID, "?limit=100").Entries
	apiDo(t, f.ac, "DELETE", "/api/v1/admin/users/"+itoa(f.leader.ID), nil, 200)
	if err := f.app.DB.Where("1=1").Delete(&models.Notification{}).Error; err != nil {
		t.Fatal(err)
	}
	after := getHistory(t, f.ac, f.subject.ID, "?limit=100").Entries
	if !reflect.DeepEqual(before, after) {
		t.Fatal("deleted dependencies changed retained history")
	}
	historyBothParties(t, f, audit.ContactReveal, audit.ContactExpired, audit.ContactEnded)
	w := f.ac.do("GET", historyPath(f.subject.ID)+".csv", nil, false)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, secret := range []string{"never-export-this-reason", f.subject.Email, f.subject.Phone, "metaJson", "127.0.0.1"} {
		if strings.Contains(w.Body.String(), secret) {
			t.Fatal("export leaked", secret)
		}
	}
}

func TestContactHistoryAddPathWalk(t *testing.T) {
	t.Run("group", func(t *testing.T) {
		f := newGroupAskFixture(t)
		for _, party := range []string{"subject", "admin"} {
			r := createGroupAsk(t, f, f.lc, f.leader, false)
			client := f.sc
			if party == "admin" {
				client = f.ac
			}
			if w := addDecision(client, r.ID, party, "denied"); w.Code != 200 {
				t.Fatal(w.Code, w.Body.String())
			}
		}
		withdrawn := createGroupAsk(t, f, f.lc, f.leader, false)
		apiDo(t, f.lc, "POST", "/api/v1/add-requests/"+itoa(withdrawn.ID)+"/withdraw", nil, 200)
		createGroupAsk(t, f, f.lc, f.leader, false)
		// Fake sweep clock, not a synthetic expired audit or bypass of the
		// immutable add-request creation timestamp.
		if err := f.app.sweepContacts(t.Context(), time.Now().UTC().Add(15*24*time.Hour)); err != nil {
			t.Fatal(err)
		}
		r := createGroupAsk(t, f, f.lc, f.leader, false)
		if w := addDecision(f.ac, r.ID, "admin", "approved"); w.Code != 200 {
			t.Fatal(w.Code)
		}
		if w := addDecision(f.sc, r.ID, "subject", "approved"); w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		if w := addDecision(f.sc, r.ID, "subject", "approved"); w.Code != 403 {
			t.Fatal(w.Code)
		}
		historyBothParties(t, f, audit.AddRequested, audit.AddDecision, audit.AddCompleted, audit.AddRefused, audit.MemberAdded, audit.AddWithdrawn, audit.AddExpired)
		apiDo(t, f.ac, "POST", "/api/v1/admin/groups/"+itoa(f.group.ID)+"/members", map[string]any{"userId": f.outsider.ID}, 200)
		historyHas(t, getHistory(t, f.ac, f.outsider.ID, "").Entries, audit.MemberAdded)
		createGroupAsk(t, f, f.lc, f.leader, false)
		apiDo(t, f.ac, "DELETE", "/api/v1/admin/groups/"+itoa(f.group.ID), nil, 200)
		historyBothParties(t, f, audit.AddEnded)
		for _, e := range getHistory(t, f.ac, f.subject.ID, "?purpose=group_add").Entries {
			if e.Purpose != "group_add" || e.Action == audit.ContactGranted {
				t.Fatal(e)
			}
		}
	})
	t.Run("date", func(t *testing.T) {
		// The original recipient's invitation is a schema fixture from s4. This
		// walk uses HTTP asks/add decisions for the other recipient on two dates.
		f := newOccurrenceReadFixture(t)
		r := createOccurrenceAsk(t, f, f.other, f.dates[0], nil)
		if w := addDecision(f.stranger, r.ID, "subject", "approved"); w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		var access models.Registration
		if err := f.app.DB.Where("access_request_id = ?", r.ID).First(&access).Error; err != nil {
			t.Fatal(err)
		}
		apiDo(t, f.admin, "POST", "/api/v1/events/"+itoa(f.event.ID)+"/registrations/remove", map[string]any{"registrationId": access.ID, "date": models.FormatCalendarDate(f.dates[0].OriginalDate)}, 200)
		r = createOccurrenceAsk(t, f, f.other, f.dates[2], nil)
		if w := addDecision(f.stranger, r.ID, "subject", "approved"); w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		apiDo(t, f.stranger, "POST", dateReadPath(f.event.ID, models.FormatCalendarDate(f.dates[2].OriginalDate))+"/block", nil, 200)
		apiDo(t, f.stranger, "POST", dateReadPath(f.event.ID, models.FormatCalendarDate(f.dates[2].OriginalDate))+"/decline", nil, 404)
		for _, id := range []uint{f.other.ID, f.issuer.ID} {
			entries := getHistory(t, f.admin, id, "?limit=100").Entries
			for _, action := range []string{audit.AddRequested, audit.AddDecision, audit.AddCompleted, audit.OccurrenceAccessAdded, audit.OccurrenceAccessEnded, audit.OccurrenceBlocked, audit.RegistrationRemoved} {
				historyHas(t, entries, action)
			}
		}
		// Generic refusal is actor-only: it must not invent a subject/selection.
		entries := getHistory(t, f.admin, f.other.ID, "?action=occurrence.refused").Entries
		if len(entries) != 1 || entries[0].SubjectUserID != "" || entries[0].RequesterID != "" {
			t.Fatal(entries)
		}
		if err := f.app.DB.Model(&models.Event{}).Where("id = ?", f.event.ID).Update("visibility", models.VisibilityPublic).Error; err != nil {
			t.Fatal(err)
		}
		direct := newTestUser(t, f.app, "direct history adult", "password1234", models.RoleMember)
		apiDo(t, f.admin, "POST", "/api/v1/events/"+itoa(f.event.ID)+"/registrations/add", map[string]any{"userId": direct.ID, "date": models.FormatCalendarDate(f.dates[1].OriginalDate)}, 200)
		for _, action := range []string{audit.RegistrationAdded, audit.OccurrenceAccessAdded} {
			historyHas(t, getHistory(t, f.admin, direct.ID, "").Entries, action)
		}
		apiDo(t, f.admin, "DELETE", "/api/v1/admin/groups/"+itoa(f.group), nil, 200)
		apiDo(t, f.admin, "DELETE", "/api/v1/admin/users/"+itoa(f.other.ID), nil, 200)
		historyHas(t, getHistory(t, f.admin, f.other.ID, "?limit=100").Entries, audit.OccurrenceAccessAdded)
	})
}

func seedHistoryAudit(t *testing.T, app *App, entry audit.Entry) {
	t.Helper()
	if err := audit.Write(app.DB, entry); err != nil {
		t.Fatal(err)
	}
}

func TestContactHistoryCursorExportBoundsAndTypedAttribution(t *testing.T) {
	app := testApp(t)
	admin := newTestUser(t, app, "history-admin", "password1234", models.RoleAdmin)
	client := loginAPI(t, app, admin.Email, "password1234")
	// Seed audit-only historical domain rows to test retained IDs 1 vs 11,
	// malformed metadata, equal timestamps, and a >page-size export cheaply.
	for i := 0; i < 225; i++ {
		seedHistoryAudit(t, app, audit.Entry{Action: audit.MemberAdded, TargetType: "group", TargetID: 42, Meta: map[string]any{"userId": uint(11), "note": "=NEVER EXPORTED \"秘密\",\r\n"}})
	}
	seedHistoryAudit(t, app, audit.Entry{Action: audit.MemberAdded, TargetType: "group", TargetID: 42, Meta: map[string]any{"userId": uint(1)}})
	for _, bad := range []any{"11", 11.5, -11, map[string]any{"id": 11}, nil, uint64(9223372036854775808)} {
		seedHistoryAudit(t, app, audit.Entry{Action: audit.MemberAdded, TargetType: "group", TargetID: 42, Meta: map[string]any{"userId": bad}})
	}
	for _, meta := range []string{`null`, `[]`, `42`, `"unexpected"`} {
		if err := app.DB.Create(&models.AuditLog{Action: audit.MemberAdded, TargetType: "group", TargetID: 42, MetaJSON: meta}).Error; err != nil {
			t.Fatal(err)
		}
	}
	maxID := uint(9223372036854775807)
	seedHistoryAudit(t, app, audit.Entry{Action: audit.MemberAdded, TargetType: "group", TargetID: 42, Meta: map[string]any{"userId": maxID}})
	if entries := getHistory(t, client, maxID, "").Entries; len(entries) != 1 || entries[0].SubjectUserID == "" || entries[0].SubjectUserID != historyID(maxID) {
		t.Fatal("retained bigint ID", entries)
	}
	seedHistoryAudit(t, app, audit.Entry{Actor: &admin.ID, Action: audit.ContactRefused, TargetType: "contact_request", Meta: map[string]any{"requesterId": 999, "subjectUserId": 11, "contextId": 777, "fields": "email"}})
	if n := len(getHistory(t, client, 11, "?limit=100").Entries); n != 100 {
		t.Fatal("page cap", n)
	}
	onlyOne := getHistory(t, client, 1, "?action=membership.added&contextType=group&contextId=42&limit=100")
	if len(onlyOne.Entries) != 1 {
		t.Fatal("IDs 1 and 11 collided", onlyOne)
	}
	for _, e := range onlyOne.Entries {
		if e.SubjectUserID == "" {
			t.Fatal(e)
		}
	}
	if len(getHistory(t, client, 999, "").Entries) != 0 {
		t.Fatal("submitted requester restored")
	}
	same := time.Now().UTC().Truncate(time.Second)
	if err := app.DB.Model(&models.AuditLog{}).Where("1=1").Update("at", same).Error; err != nil {
		t.Fatal(err)
	}
	var ids []string
	before := ""
	for {
		p := getHistory(t, client, 11, "?limit=17"+before)
		for _, e := range p.Entries {
			if e.SubjectUserID == "" || e.SubjectUserID != "11" {
				t.Fatal("invented subject", e)
			}
			ids = append(ids, e.ID)
		}
		if p.NextBefore == "" {
			break
		}
		before = "&before=" + p.NextBefore
	}
	if len(ids) != 225 {
		t.Fatal("missing/duplicate", len(ids))
	}
	for i := 1; i < len(ids); i++ {
		n, _ := historyPositiveID(ids[i])
		prev, _ := historyPositiveID(ids[i-1])
		if n >= prev {
			t.Fatal("unstable exclusive cursor")
		}
	}
	// The client's notice drain is harness work, not handler reads.
	client.holdNotices = true
	n := collectionReadCounter(t, app.DB)
	*n = 0
	getHistory(t, client, 11, "?limit=100")
	if *n > 4 {
		t.Fatal("history read budget", *n)
	}
	*n = 0
	pages, actorLocks, zoneReads := 0, 0, 0
	if err := app.DB.Callback().Row().Before("gorm:row").Register("history_page_budget", func(db *gorm.DB) {
		if db.Statement.TableExpr != nil && strings.Contains(db.Statement.TableExpr.SQL, "AS history") {
			pages++
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.DB.Callback().Row().Remove("history_page_budget") })
	if err := app.DB.Callback().Query().Before("gorm:query").Register("history_authorization_budget", func(db *gorm.DB) {
		if db.Statement.Table == "settings" {
			zoneReads++
		}
		if _, locked := db.Statement.Clauses["FOR"]; db.Statement.Table == "users" && locked {
			actorLocks++
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.DB.Callback().Query().Remove("history_authorization_budget") })
	w := client.do("GET", historyPath(11)+".csv?purpose=group_add&contextType=group&contextId=42", nil, false)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	// Old budget 7 + initial churchTimezone + final actor FOR SHARE + final
	// contactZone = 10. Authorization reads are constant, not per history row.
	if pages != 3 || actorLocks != 1 || zoneReads != 2 {
		t.Fatal("export query shape", pages, actorLocks, zoneReads)
	}
	if *n > 10 {
		t.Fatal("export page read budget", *n)
	}
	if w.Header().Get("Content-Disposition") != `attachment; filename="contact-history-11.csv"` {
		t.Fatal(w.Header())
	}
	records, err := csv.NewReader(strings.NewReader(w.Body.String())).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 226 || !reflect.DeepEqual(records[0], contactHistoryCSVHeader) {
		t.Fatal("truncated export", len(records))
	}
	for i, r := range records[1:] {
		if r[0] != ids[i] {
			t.Fatal("CSV lost/duplicated/reordered", i, r)
		}
	}
	if !strings.Contains(w.Body.String(), "\r\n") || strings.Contains(w.Body.String(), "NEVER EXPORTED") {
		t.Fatal("unsafe CSV", w.Body.String())
	}
	var logged models.AuditLog
	if err := app.DB.Where("action = ?", audit.ContactHistoryExported).First(&logged).Error; err != nil {
		t.Fatal(err)
	}
	if logged.TargetID != 11 || !strings.Contains(logged.MetaJSON, `"count": 225`) {
		t.Fatal(logged)
	}
}

func TestContactHistoryCSVEncodingAndFormulaProtection(t *testing.T) {
	values := []string{"=1+1", "+SUM(1)", "-2", "@SUM(1)", " \t=1", "\r=1", "\n+1", "\ttext", "\u2003@x", "\u0000text", "\ufeff=1", "plain", "秘密,\"quoted\"\r\nsecond"}
	var buf strings.Builder
	writer := csv.NewWriter(&buf)
	writer.UseCRLF = true
	for _, v := range values {
		if err := writer.Write([]string{contactHistoryCSVCell(v)}); err != nil {
			t.Fatal(err)
		}
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		t.Fatal(err)
	}
	rows, err := csv.NewReader(strings.NewReader(buf.String())).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	for i, v := range values {
		want := v
		if i < 11 {
			want = "'" + v
		}
		want = strings.ReplaceAll(want, "\r\n", "\n")
		want = strings.ReplaceAll(want, "\r", "")
		if rows[i][0] != want {
			t.Fatalf("%q -> %q want %q", v, rows[i][0], want)
		}
	}
	if !strings.Contains(buf.String(), `""quoted""`) {
		t.Fatal("missing CSV quoting")
	}
}

func TestContactHistoryExportFailuresEmitNoProtectedBytes(t *testing.T) {
	for _, failure := range []string{"audit", "query"} {
		t.Run(failure, func(t *testing.T) {
			app := testApp(t)
			temp := t.TempDir()
			t.Setenv("TMPDIR", temp)
			admin := newTestUser(t, app, "failure-admin", "password1234", models.RoleAdmin)
			client := loginAPI(t, app, admin.Email, "password1234")
			seedHistoryAudit(t, app, audit.Entry{Actor: &admin.ID, Action: audit.MemberAdded, TargetType: "group", TargetID: 4, Meta: map[string]any{"userId": 22}})
			if failure == "audit" {
				name := "history_export_failure"
				if err := app.DB.Callback().Create().Before("gorm:create").Register(name, func(db *gorm.DB) {
					if log, ok := db.Statement.Dest.(*models.AuditLog); ok && log.Action == audit.ContactHistoryExported {
						db.AddError(errors.New("injected export audit failure"))
					}
				}); err != nil {
					t.Fatal(err)
				}
				defer app.DB.Callback().Create().Remove(name)
			} else {
				// Fail a later internal page after the first 100 entries were materialized.
				for i := 0; i < 110; i++ {
					seedHistoryAudit(t, app, audit.Entry{Actor: &admin.ID, Action: audit.MemberAdded, TargetType: "group", TargetID: 4, Meta: map[string]any{"userId": 22}})
				}
				calls := 0
				name := "history_export_query_failure"
				if err := app.DB.Callback().Row().Before("gorm:row").Register(name, func(db *gorm.DB) {
					if db.Statement.TableExpr != nil && strings.Contains(db.Statement.TableExpr.SQL, "AS history") {
						calls++
						if calls == 2 {
							db.AddError(errors.New("injected later-page failure"))
						}
					}
				}); err != nil {
					t.Fatal(err)
				}
				defer app.DB.Callback().Row().Remove(name)
			}
			w := client.do("GET", historyPath(22)+".csv", nil, false)
			if w.Code != 500 || !strings.Contains(w.Body.String(), "server_error") || strings.Contains(w.Body.String(), "membership.added") || w.Header().Get("Content-Disposition") != "" || strings.Contains(w.Header().Get("Content-Type"), "text/csv") {
				t.Fatal("partial protected export", w.Code, w.Header(), w.Body.String())
			}
			if contactCount(t, app, &models.AuditLog{}, "action = ?", audit.ContactHistoryExported) != 0 {
				t.Fatal("failed export audited as success")
			}
			assertHistoryTempEmpty(t, temp)
		})
	}
}

func TestContactHistoryValidFilters(t *testing.T) {
	f := newContactAPIFixture(t, false)
	f.grant(t, f.create(t, "both", ""))
	at := time.Now().UTC()
	query := "?purpose=contact&action=contact.granted&contextType=group&contextId=" + itoa(f.group.ID) + "&from=" + url.QueryEscape(at.Add(-time.Hour).Format(time.RFC3339Nano)) + "&until=" + url.QueryEscape(at.Add(time.Hour).Format(time.RFC3339Nano))
	p := getHistory(t, f.ac, f.subject.ID, query)
	if len(p.Entries) != 1 || p.Entries[0].Outcome != "granted" || p.Entries[0].Fields != "both" {
		t.Fatal(p)
	}
	if len(getHistory(t, f.ac, f.subject.ID, "?occurrenceId=9999").Entries) != 0 {
		t.Fatal("occurrence filter ignored")
	}
	// Deliberately wrong action/target pairing never gains attribution.
	seedHistoryAudit(t, f.app, audit.Entry{Action: audit.ContactGranted, TargetType: "group", TargetID: f.group.ID, Meta: map[string]any{"userId": f.subject.ID}})
	if len(getHistory(t, f.ac, f.subject.ID, query).Entries) != 1 {
		t.Fatal("unknown relation accepted")
	}
}

func TestContactHistoryReadsDoNotAcquireContactMutationGate(t *testing.T) {
	f := newContactAPIFixture(t, false)
	f.create(t, "email", "")
	gate := holdContactGate(t, f.app)
	defer gate.Rollback()
	for _, suffix := range []string{"", ".csv"} {
		client := cloneContactClient(f.ac)
		ch := queueContact(func() *httptest.ResponseRecorder {
			return client.do("GET", historyPath(f.subject.ID)+suffix, nil, false)
		})
		if w := awaitContactResponse(t, ch); w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
}

func TestContactHistoryRejectsZeroTimeBounds(t *testing.T) {
	f := newContactAPIFixture(t, false)
	f.create(t, "email", "")
	for _, suffix := range []string{"", ".csv"} {
		for _, query := range []string{"?from=0001-01-01T00:00:00Z", "?until=0001-01-01T00:00:00Z", "?from=0001-01-01T00:00:00Z&until=0001-01-01T00:00:00Z"} {
			w := f.ac.do("GET", historyPath(f.subject.ID)+suffix+query, nil, false)
			if w.Code != 400 || !strings.Contains(w.Body.String(), `"code":"contact_history_filters"`) {
				t.Fatal(suffix, query, w.Code, w.Body.String())
			}
		}
	}
}

func TestContactHistoryAdultUsesChurchCalendarDay(t *testing.T) {
	app := testApp(t)
	if err := app.DB.Create(&models.Setting{Key: settingTimezone, Value: "America/Los_Angeles"}).Error; err != nil {
		t.Fatal(err)
	}
	birth := time.Date(2008, 10, 25, 0, 0, 0, 0, time.UTC)
	user := newTestUser(t, app, "birthday-admin", "password1234", models.RoleAdmin)
	user.BirthDate = &birth
	// Already the 18th birthday in UTC, still the previous day at church.
	instant := time.Date(2026, 10, 25, 1, 0, 0, 0, time.UTC)
	if app.canViewContactHistoryAt(user, instant) {
		t.Fatal("UTC birthday authorized a locally underage admin")
	}
	if !app.canViewContactHistoryAt(user, instant.Add(7*time.Hour)) {
		t.Fatal("church birthday denied an adult admin")
	}
	user.AccountType = models.AccountTeen
	if app.canViewContactHistoryAt(user, instant.Add(7*time.Hour)) {
		t.Fatal("teen admin authorized on birthday")
	}
}

func TestContactHistoryExactDecimalStringIDs(t *testing.T) {
	app := testApp(t)
	admin := newTestUser(t, app, "exact-id-admin", "password1234", models.RoleAdmin)
	client := loginAPI(t, app, admin.Email, "password1234")
	maxID := uint(9223372036854775807)
	for _, id := range []uint{9007199254740992, 9007199254740993, maxID} {
		if err := app.DB.Create(&models.AuditLog{ID: id, At: time.Now().UTC(), ActorID: &maxID, Action: audit.MemberAdded, TargetType: "group", TargetID: maxID, MetaJSON: `{"userId":9223372036854775807}`}).Error; err != nil {
			t.Fatal(err)
		}
	}
	before := ""
	for i, want := range []string{"9223372036854775807", "9007199254740993", "9007199254740992"} {
		p := getHistory(t, client, maxID, "?limit=1"+before)
		if len(p.Entries) != 1 || p.Entries[0].ID != want || p.Entries[0].ActorID != historyID(maxID) || p.Entries[0].SubjectUserID != historyID(maxID) || p.Entries[0].ContextID != historyID(maxID) {
			t.Fatal("inexact history IDs", p)
		}
		if i < 2 {
			if p.NextBefore != want {
				t.Fatal("inexact cursor", p)
			}
			before = "&before=" + p.NextBefore
		} else if p.NextBefore != "" {
			t.Fatal("unexpected final cursor", p)
		}
	}
	// Every optional ID uses the same exact serialization, not just audit IDs.
	dto := contactHistoryDTO(contactHistoryEntry{ID: maxID, ActorID: &maxID, RequesterID: &maxID, SubjectUserID: &maxID, RequestID: &maxID, AccessID: &maxID, ContextID: &maxID, OccurrenceID: &maxID})
	data, err := json.Marshal(dto)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"id", "actorId", "requesterId", "subjectUserId", "requestId", "accessId", "contextId", "occurrenceId"} {
		if raw[key] != "9223372036854775807" {
			t.Fatal("non-string or inexact ID", key, raw[key])
		}
	}
	data, err = json.Marshal(contactHistoryDTO(contactHistoryEntry{ID: 1}))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "actorId") {
		t.Fatal("absent optional ID not omitted", string(data))
	}
}

func assertHistoryTempEmpty(t *testing.T, dir string) {
	t.Helper()
	files, err := os.ReadDir(dir)
	if err != nil || len(files) != 0 {
		t.Fatal("export temporary file not cleaned", files, err)
	}
}

// Pause before the second page: page one has already been materialized and the
// repeatable-read snapshot is established. No production-only barrier is needed.
func pauseHistoryAfterFirstPage(t *testing.T, db *gorm.DB) (<-chan struct{}, func()) {
	t.Helper()
	reached, resume := make(chan struct{}), make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(resume) }) }
	calls := 0
	const name = "history_after_first_page"
	if err := db.Callback().Row().Before("gorm:row").Register(name, func(tx *gorm.DB) {
		if tx.Statement.TableExpr == nil || !strings.Contains(tx.Statement.TableExpr.SQL, "AS history") {
			return
		}
		calls++
		if calls == 2 {
			close(reached)
			select {
			case <-resume:
			case <-tx.Statement.Context.Done():
				tx.AddError(tx.Statement.Context.Err())
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { release(); _ = db.Callback().Row().Remove(name) })
	return reached, release
}

func TestContactHistoryExportRechecksCurrentAuthority(t *testing.T) {
	for _, mutation := range []string{"demotion", "session", "disabled", "deleted"} {
		t.Run(mutation, func(t *testing.T) {
			app := testApp(t)
			temp := t.TempDir()
			t.Setenv("TMPDIR", temp)
			admin := newTestUser(t, app, "revoked-export-admin", "password1234", models.RoleAdmin)
			client := cloneContactClient(loginAPI(t, app, admin.Email, "password1234"))
			for i := 0; i < 125; i++ {
				seedHistoryAudit(t, app, audit.Entry{Actor: &admin.ID, Action: audit.MemberAdded, TargetType: "group", TargetID: 42})
			}
			reached, resume := pauseHistoryAfterFirstPage(t, app.DB)
			ch := queueContact(func() *httptest.ResponseRecorder { return client.do("GET", historyPath(admin.ID)+".csv", nil, false) })
			select {
			case <-reached:
			case <-time.After(8 * time.Second):
				t.Fatal("export never reached page barrier")
			}
			var err error
			switch mutation {
			case "demotion":
				err = app.DB.Model(admin).Update("role", models.RoleMember).Error
			case "session":
				err = app.DB.Model(admin).Update("session_version", admin.SessionVersion+1).Error
			case "disabled":
				err = app.DB.Model(admin).Update("status", models.UserDisabled).Error
			case "deleted":
				err = app.DB.Delete(admin).Error
			}
			if err != nil {
				t.Fatal(err)
			}
			resume()
			w := awaitContactResponse(t, ch)
			if w.Code != 500 || w.Header().Get("Content-Disposition") != "" || strings.Contains(w.Header().Get("Content-Type"), "text/csv") || strings.Contains(w.Body.String(), "membership.added") {
				t.Fatal("revoked authority released CSV", w.Code, w.Header(), w.Body.String())
			}
			if contactCount(t, app, &models.AuditLog{}, "action = ?", audit.ContactHistoryExported) != 0 {
				t.Fatal("revoked export audited as success")
			}
			assertHistoryTempEmpty(t, temp)
		})
	}
}

func TestContactHistoryExportSnapshotExcludesConcurrentAppend(t *testing.T) {
	app := testApp(t)
	temp := t.TempDir()
	t.Setenv("TMPDIR", temp)
	admin := newTestUser(t, app, "snapshot-export-admin", "password1234", models.RoleAdmin)
	client := cloneContactClient(loginAPI(t, app, admin.Email, "password1234"))
	for i := 0; i < 225; i++ {
		if err := app.DB.Create(&models.AuditLog{ID: uint(1001 + i), At: time.Now().UTC(), ActorID: &admin.ID, Action: audit.MemberAdded, TargetType: "group", TargetID: 42}).Error; err != nil {
			t.Fatal(err)
		}
	}
	reached, resume := pauseHistoryAfterFirstPage(t, app.DB)
	ch := queueContact(func() *httptest.ResponseRecorder { return client.do("GET", historyPath(admin.ID)+".csv", nil, false) })
	select {
	case <-reached:
	case <-time.After(8 * time.Second):
		t.Fatal("export never reached page barrier")
	}
	files, err := os.ReadDir(temp)
	if err != nil || len(files) != 1 {
		t.Fatal("missing private export file", files, err)
	}
	info, err := files[0].Info()
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("export file not private", info, err)
	}
	// Append a normal higher ID and a previously unused lower ID: the latter
	// would enter a later keyset page without repeatable-read isolation. This
	// tests the snapshot itself, not merely the descending-cursor exclusion.
	for _, id := range []uint{1226, 1000} {
		if err := app.DB.Create(&models.AuditLog{ID: id, At: time.Now().UTC(), ActorID: &admin.ID, Action: audit.MemberRemoved, TargetType: "group", TargetID: 42}).Error; err != nil {
			t.Fatal(err)
		}
	}
	resume()
	w := awaitContactResponse(t, ch)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	rows, err := csv.NewReader(strings.NewReader(w.Body.String())).ReadAll()
	if err != nil || len(rows) != 226 {
		t.Fatal("inconsistent snapshot", len(rows), err)
	}
	for _, row := range rows[1:] {
		if row[2] != audit.MemberAdded {
			t.Fatal("concurrent row leaked into snapshot", row)
		}
	}
	assertHistoryTempEmpty(t, temp)
}

// Exact-date history is read from the three action-scoped registration
// snapshots, so it keeps issuer, request, origin and the registration ID after
// a re-grant on the same registration and after the registration and its event
// are deleted; the wire shape is the one the page has always read. The two
// grants here come from different issuers and different requests, and each
// history entry is matched to its own audit row by ID, never by action.
func TestContactHistoryReadsAccessSnapshotsAfterDeletion(t *testing.T) {
	f := newOccurrenceReadFixture(t)
	date := models.FormatCalendarDate(f.dates[1].OriginalDate)
	declinePath := dateReadPath(f.event.ID, date) + "/decline"
	firstRequest := *f.access.RequestID

	// Grant one (the fixture's) ends when the recipient declines it. A second
	// admin then asks again; the recipient approves, which grants on the same
	// registration, and declines that grant too.
	apiDo(t, f.client, "POST", declinePath, nil, 200)
	issuer2 := newTestUser(t, f.app, "second history issuer", "password1234", models.RoleAdmin)
	issuer2C := loginAPI(t, f.app, "second history issuer", "password1234")
	second := createDateAsk(t, f.app, issuer2C, issuer2, f.recipient, f.event.ID, f.dates[1].ID)
	if second.ID == firstRequest {
		t.Fatal("the second ask reused the first request")
	}
	if w := addDecision(f.client, second.ID, "subject", "approved"); w.Code != 200 {
		t.Fatalf("second approval: %d %s", w.Code, w.Body.String())
	}
	if row := loadAccessRow(t, f.app.DB, f.access.ID); row.Status != policy.OccurrenceAccessActive || ptrUint(row.RequestID) != second.ID || ptrUint(row.IssuerID) != issuer2.ID {
		t.Fatalf("the re-grant did not land on the same registration: %+v", row)
	}
	if contactCount(t, f.app, &models.Registration{}, "user_id = ? AND occurrence_id = ?", f.recipient.ID, f.dates[1].ID) != 1 {
		t.Fatal("the re-grant created a second registration")
	}
	apiDo(t, f.client, "POST", declinePath, nil, 200)

	// The stored snapshots are the authority for origin (the history DTO omits
	// it): four rows, two grants, each with its own issuer, request and stamps.
	rows := accessSnapshotsFor(t, f.app.DB, f.access.ID)
	wantAction := []string{audit.OccurrenceAccessAdded, audit.OccurrenceAccessEnded, audit.OccurrenceAccessAdded, audit.OccurrenceAccessEnded}
	type grant struct{ issuer, request uint }
	wantGrant := []grant{{f.issuer.ID, firstRequest}, {f.issuer.ID, firstRequest}, {issuer2.ID, second.ID}, {issuer2.ID, second.ID}}
	if len(rows) != 4 {
		t.Fatalf("access snapshots = %+v", rows)
	}
	for i, s := range rows {
		if s.Action != wantAction[i] || s.Meta["origin"] != policy.OccurrenceOriginManagerAdd || s.Meta["registrationId"] != float64(f.access.ID) ||
			s.Meta["issuerId"] != float64(wantGrant[i].issuer) || s.Meta["requestId"] != float64(wantGrant[i].request) {
			t.Fatalf("snapshot %d lost its attribution: %+v", i, s)
		}
	}
	if wantGrant[0] == wantGrant[2] || rows[0].Meta["grantedAtMicro"] != rows[1].Meta["grantedAtMicro"] || rows[2].Meta["grantedAtMicro"] != rows[3].Meta["grantedAtMicro"] ||
		rows[0].Meta["grantedAtMicro"] == rows[2].Meta["grantedAtMicro"] ||
		snapshotInstant(t, rows[2], "grantedAtMicro").Before(snapshotInstant(t, rows[1], "endedAtMicro")) {
		t.Fatalf("the two grants are not told apart by their stamps: %+v", rows)
	}

	// check finds each snapshot's own history entry by its audit row ID and
	// compares it to that grant's issuer and request. The recipient sees all
	// four; each issuer sees the entries of the grant they issued. It returns
	// the recipient's entries, in snapshot order.
	check := func(label string) []contactHistoryEntryJSON {
		t.Helper()
		var mine []contactHistoryEntryJSON
		for _, person := range []struct {
			id   uint
			sees []int
		}{{f.recipient.ID, []int{0, 1, 2, 3}}, {f.issuer.ID, []int{0, 1}}, {issuer2.ID, []int{2, 3}}} {
			entries := getHistory(t, f.admin, person.id, "?limit=100").Entries
			for _, i := range person.sees {
				var e *contactHistoryEntryJSON
				for j := range entries {
					if entries[j].ID == itoa(rows[i].ID) {
						e = &entries[j]
						break
					}
				}
				if e == nil {
					t.Fatalf("%s: snapshot %d (audit %d) missing for person %d in %+v", label, i, rows[i].ID, person.id, entries)
				}
				outcome := "added"
				if rows[i].Action == audit.OccurrenceAccessEnded {
					outcome = "ended"
				}
				if e.Action != rows[i].Action || e.Outcome != outcome || e.RequesterID != itoa(wantGrant[i].issuer) || e.RequestID != itoa(wantGrant[i].request) ||
					e.SubjectUserID != itoa(f.recipient.ID) || e.AccessID != itoa(f.access.ID) || e.Purpose != "occurrence_add" || e.ContextType != "event" ||
					e.ContextID != itoa(f.event.ID) || e.OccurrenceID != itoa(f.dates[1].ID) {
					t.Fatalf("%s: snapshot %d lost its attribution for person %d: %+v", label, i, person.id, *e)
				}
				if person.id == f.recipient.ID {
					mine = append(mine, *e)
				}
			}
		}
		return mine
	}
	before := check("before deletion")
	if err := f.app.contactMutation(contactChange{EventID: f.event.ID}, func(tx *gorm.DB) error { return deleteEventRows(tx, []uint{f.event.ID}) }); err != nil {
		t.Fatal(err)
	}
	if contactCount(t, f.app, &models.Registration{}, "id = ?", f.access.ID) != 0 {
		t.Fatal("the registration should be gone")
	}
	after := check("after deletion")
	if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(accessSnapshotsFor(t, f.app.DB, f.access.ID), rows) {
		t.Fatalf("deleting the registration and event changed the history:\nbefore %+v\nafter  %+v", before, after)
	}

	// IDs above 2^53 stay exact decimal strings, malformed metadata is omitted
	// field by field, and an unrelated registration action is not an access row.
	const bigSubject, bigIssuer = uint(9007199254740993), uint(9007199254740995)
	seedHistoryAudit(t, f.app, audit.Entry{Action: audit.OccurrenceAccessAdded, TargetType: "registration", TargetID: 77,
		Meta: map[string]any{"subjectUserId": bigSubject, "issuerId": bigIssuer, "occurrenceId": 3, "eventId": 4, "registrationId": 77, "grantedAtMicro": 5, "origin": "manager_add"}})
	entries := getHistory(t, f.admin, bigSubject, "").Entries
	if len(entries) != 1 || entries[0].SubjectUserID != "9007199254740993" || entries[0].RequesterID != "9007199254740995" || entries[0].AccessID != "77" || entries[0].ContextID != "4" {
		t.Fatalf("large IDs: %+v", entries)
	}
	actor := f.other.ID
	seedHistoryAudit(t, f.app, audit.Entry{Actor: &actor, Action: audit.OccurrenceAccessAdded, TargetType: "registration", TargetID: 78,
		Meta: map[string]any{"subjectUserId": "5", "issuerId": 1.5, "requestId": -1, "eventId": 1e30, "occurrenceId": nil, "origin": "manager_add"}})
	seedHistoryAudit(t, f.app, audit.Entry{Actor: &actor, Action: audit.RegistrationChanged, TargetType: "registration", TargetID: 79,
		Meta: map[string]any{"subjectUserId": f.other.ID, "occurrenceId": f.dates[0].ID}})
	seedHistoryAudit(t, f.app, audit.Entry{Actor: &actor, Action: audit.OccurrenceAccessAdded, TargetType: "occurrence_access", TargetID: 80,
		Meta: map[string]any{"subjectUserId": f.other.ID}})
	mine := getHistory(t, f.admin, f.other.ID, "?limit=100").Entries
	var malformed *contactHistoryEntryJSON
	for i, e := range mine {
		switch e.AccessID {
		case "78":
			malformed = &mine[i]
		case "79", "80":
			t.Fatalf("a non-access row was presented as exact-date history: %+v", e)
		}
		if e.Action == audit.RegistrationChanged {
			t.Fatalf("registration.changed leaked into contact history: %+v", e)
		}
	}
	if malformed == nil || malformed.SubjectUserID != "" || malformed.RequesterID != "" || malformed.RequestID != "" || malformed.ContextID != "" || malformed.OccurrenceID != "" || malformed.Action != audit.OccurrenceAccessAdded {
		t.Fatalf("malformed fields must be omitted, never cast: %+v", malformed)
	}
	if w := f.admin.do("GET", historyPath(f.other.ID)+".csv", nil, false); w.Code != 200 {
		t.Fatalf("export with malformed metadata: %d %s", w.Code, w.Body.String())
	}
}

// The history wire shape does not move with its storage: the same sixteen keys,
// and accessId now carries the registration ID.
func TestContactHistoryJSONKeysAreUnchanged(t *testing.T) {
	raw, err := json.Marshal(contactHistoryEntryJSON{ID: "1", Action: "a", ActorID: "2", RequesterID: "3", SubjectUserID: "4", Purpose: "p", RequestID: "5",
		AccessID: "6", ContextType: "event", ContextID: "7", OccurrenceID: "8", Fields: "f", Outcome: "o", Party: "q", Decision: "d"})
	if err != nil {
		t.Fatal(err)
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(raw, &keys); err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(keys))
	for k := range keys {
		got = append(got, k)
	}
	want := strings.Fields("id at action actorId requesterId subjectUserId purpose requestId accessId contextType contextId occurrenceId fields outcome party decision")
	sort.Strings(got)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("history keys = %v, want %v", got, want)
	}
	if !reflect.DeepEqual(contactHistoryCSVHeader, strings.Fields("auditId at action actorId requesterId subjectUserId purpose requestId accessId contextType contextId occurrenceId fields outcome party decision")) {
		t.Fatalf("CSV header moved: %v", contactHistoryCSVHeader)
	}
}
