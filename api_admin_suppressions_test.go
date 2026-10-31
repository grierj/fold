package main

import (
	"net/http"
	"strings"
	"testing"

	"fold/internal/audit"
	"fold/internal/mail"
	"fold/models"
)

func suppress(t *testing.T, app *App, address, reason string) {
	t.Helper()
	if err := app.DB.Create(&models.Suppression{Channel: "email", Address: address, Reason: reason, Provider: "postmark"}).Error; err != nil {
		t.Fatal(err)
	}
}

// Admins see why Fold stopped mailing someone, strongest reason first, and
// clear it; members can't; a cleared address gets mail again (DEC-113).
func TestAdminSeesAndClearsSuppressions(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
	sam := newTestUser(t, app, "sam", "password1234", models.RoleMember)
	pat := newTestUser(t, app, "pat", "password1234", models.RoleMember)
	admin, member := loginAPI(t, app, "admin", "password1234"), loginAPI(t, app, "sam", "password1234")
	// A login stored with capitals still matches its lowercased suppression.
	app.DB.Model(sam).Update("email", "Sam.Mixed@Example.com")
	sam.Email = "Sam.Mixed@Example.com"
	suppress(t, app, strings.ToLower(sam.Email), models.SuppressComplaint)
	suppress(t, app, strings.ToLower(sam.Email), models.SuppressHardBounce)
	suppress(t, app, strings.ToLower(pat.Email), models.SuppressComplaint)

	var out struct {
		Users []userJSON `json:"users"`
	}
	decodeJSON(t, admin.do("GET", "/api/v1/admin/users", nil, false), &out)
	got := map[string]string{}
	for _, u := range out.Users {
		if u.EmailSuppression != nil {
			got[u.Name] = u.EmailSuppression.Reason
		}
	}
	if len(got) != 2 || got[sam.Name] != models.SuppressHardBounce || got[pat.Name] != models.SuppressComplaint {
		t.Fatalf("suppressions shown: %v", got)
	}

	path := "/api/v1/admin/users/" + itoa(sam.ID) + "/email-suppression/clear"
	if w := member.do("POST", path, nil, true); w.Code != http.StatusForbidden {
		t.Fatalf("member: %d", w.Code)
	}
	if w := admin.do("POST", "/api/v1/admin/users/99999/email-suppression/clear", nil, true); w.Code != http.StatusNotFound {
		t.Fatalf("missing user: %d", w.Code)
	}
	w := admin.do("POST", path, nil, true)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"cleared":2`) {
		t.Fatalf("clear: %d %s", w.Code, w.Body.String())
	}
	if auditCount(t, app, audit.EmailSuppressionCleared) != 1 {
		t.Fatal("clearing should be audited")
	}
	// Clearing again changes nothing and writes no second audit row.
	if w := admin.do("POST", path, nil, true); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"cleared":0`) {
		t.Fatalf("clear again: %d %s", w.Code, w.Body.String())
	}
	if auditCount(t, app, audit.EmailSuppressionCleared) != 1 {
		t.Fatal("a no-op clear wrote an audit row")
	}
	var kept int64
	app.DB.Model(&models.Suppression{}).Where("address = ? AND cleared_at IS NOT NULL", strings.ToLower(sam.Email)).Count(&kept)
	if kept != 2 {
		t.Fatal("cleared suppressions stay as history")
	}
	if err := app.Mailer.Send(t.Context(), mail.Message{To: sam.Email, Subject: "Hello", Text: "again"}); err != nil {
		t.Fatalf("a cleared address should get mail: %v", err)
	}
}
