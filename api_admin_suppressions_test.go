package main

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

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

// suppressHashed is suppress as the R4-S binary writes it, with the address
// hash beside the address. suppress writes the legacy NULL-hash shape, so
// the two together cover both arms of the lookup.
func suppressHashed(t *testing.T, app *App, address, reason string) {
	t.Helper()
	h := mail.StoredAddressHash(address)
	if err := app.DB.Create(&models.Suppression{Channel: "email", Address: address, AddressHash: &h, Reason: reason, Provider: "postmark"}).Error; err != nil {
		t.Fatal(err)
	}
}

// suppressionSpec is one suppression row for an entry-point test: legacy
// (NULL hash) or hashed, open or cleared.
type suppressionSpec struct {
	hashed, cleared bool
	reason          string
}

// suppressSpecs writes the rows for address in order, so the first spec is
// the oldest row (ascending id).
func suppressSpecs(t *testing.T, app *App, address string, specs ...suppressionSpec) {
	t.Helper()
	for _, sp := range specs {
		row := models.Suppression{Channel: "email", Address: address, Reason: sp.reason, Provider: "postmark"}
		if sp.hashed {
			h := mail.StoredAddressHash(address)
			row.AddressHash = &h
		}
		if sp.cleared {
			now := time.Now()
			row.ClearedAt = &now
		}
		if err := app.DB.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
}

// suppressionCase is a set of rows and what each kind of mail does with it
// (DEC-112): required is the reason that refuses required account mail ("" =
// it goes out; a complaint alone and cleared rows let it through), optional
// is the reason that refuses optional and broadcast mail ("" = it goes
// out). With both a complaint and a hard bounce open, required mail is
// refused with the bounce and optional mail with the older row's reason;
// which of the two wins optional mail was unspecified before R4-S, and is
// now the older row.
type suppressionCase struct {
	name               string
	specs              []suppressionSpec
	required, optional string
}

func suppressionCases() []suppressionCase {
	const hb, cp, mn = models.SuppressHardBounce, models.SuppressComplaint, models.SuppressManual
	return []suppressionCase{
		{"legacy hard bounce", []suppressionSpec{{reason: hb}}, hb, hb},
		{"hashed hard bounce", []suppressionSpec{{hashed: true, reason: hb}}, hb, hb},
		{"hashed manual", []suppressionSpec{{hashed: true, reason: mn}}, mn, mn},
		{"legacy complaint", []suppressionSpec{{reason: cp}}, "", cp},
		{"hashed complaint", []suppressionSpec{{hashed: true, reason: cp}}, "", cp},
		{"legacy cleared hard bounce", []suppressionSpec{{cleared: true, reason: hb}}, "", ""},
		{"hashed cleared hard bounce", []suppressionSpec{{hashed: true, cleared: true, reason: hb}}, "", ""},
		{"hashed complaint then bounce", []suppressionSpec{{hashed: true, reason: cp}, {hashed: true, reason: hb}}, hb, cp},
		{"hashed bounce then complaint", []suppressionSpec{{hashed: true, reason: hb}, {hashed: true, reason: cp}}, hb, hb},
		{"legacy complaint then bounce", []suppressionSpec{{reason: cp}, {reason: hb}}, hb, cp},
		{"legacy bounce then complaint", []suppressionSpec{{reason: hb}, {reason: cp}}, hb, hb},
	}
}

// suppressAs writes a hashed or legacy row with an explicit created_at.
func suppressAs(t *testing.T, app *App, address, reason string, hashed bool, createdAt time.Time) {
	t.Helper()
	row := models.Suppression{Channel: "email", Address: address, Reason: reason, CreatedAt: createdAt}
	if hashed {
		h := mail.StoredAddressHash(address)
		row.AddressHash = &h
	}
	if err := app.DB.Create(&row).Error; err != nil {
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

// The admin list and Clear read hashed and legacy NULL-hash rows alike, a
// person with both reasons shows the hard bounce since the earlier row
// whichever was written first, and Clear clears both and audits both (R4-S3).
func TestAdminSuppressionsHashedLegacyAndBothReasons(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
	admin := loginAPI(t, app, "admin", "password1234")
	t0 := time.Now().UTC().Add(-48 * time.Hour).Truncate(time.Second)
	users := map[string]*models.User{}
	for _, name := range []string{"hashed", "legacy", "complaintfirst", "bouncefirst", "legacyboth", "cleared"} {
		users[name] = newTestUser(t, app, name, "password1234", models.RoleMember)
	}
	// Stored mixed-case: the lookup normalizes it.
	app.DB.Model(users["hashed"]).Update("email", " Hashed@Example.com ")
	users["hashed"].Email = " Hashed@Example.com "
	suppressAs(t, app, "hashed@example.com", models.SuppressHardBounce, true, t0)
	suppressAs(t, app, "legacy@example.com", models.SuppressComplaint, false, t0)
	// Each person has both reasons open, older row first by id and by time.
	suppressAs(t, app, "complaintfirst@example.com", models.SuppressComplaint, true, t0)
	suppressAs(t, app, "complaintfirst@example.com", models.SuppressHardBounce, true, t0.Add(time.Hour))
	suppressAs(t, app, "bouncefirst@example.com", models.SuppressHardBounce, true, t0)
	suppressAs(t, app, "bouncefirst@example.com", models.SuppressComplaint, true, t0.Add(time.Hour))
	suppressAs(t, app, "legacyboth@example.com", models.SuppressComplaint, false, t0)
	suppressAs(t, app, "legacyboth@example.com", models.SuppressHardBounce, false, t0.Add(time.Hour))
	cleared := time.Now()
	h := mail.StoredAddressHash("cleared@example.com")
	if err := app.DB.Create(&models.Suppression{Channel: "email", Address: "cleared@example.com", AddressHash: &h,
		Reason: models.SuppressHardBounce, ClearedAt: &cleared}).Error; err != nil {
		t.Fatal(err)
	}

	var out struct {
		Users []userJSON `json:"users"`
	}
	decodeJSON(t, admin.do("GET", "/api/v1/admin/users", nil, false), &out)
	got := map[string]*emailSuppressionJSON{}
	for _, u := range out.Users {
		if u.EmailSuppression != nil {
			got[u.Name] = u.EmailSuppression
		}
	}
	want := map[string]string{
		"hashed": models.SuppressHardBounce, "legacy": models.SuppressComplaint,
		"complaintfirst": models.SuppressHardBounce, "bouncefirst": models.SuppressHardBounce, "legacyboth": models.SuppressHardBounce,
	}
	if len(got) != len(want) {
		t.Fatalf("suppressions shown: %v", got)
	}
	for name, reason := range want {
		if got[name] == nil || got[name].Reason != reason || !got[name].Since.Equal(t0) {
			t.Errorf("%s: %+v, want %s since %s", name, got[name], reason, t0)
		}
	}

	for _, name := range []string{"hashed", "legacy", "complaintfirst", "legacyboth"} {
		w := admin.do("POST", "/api/v1/admin/users/"+itoa(users[name].ID)+"/email-suppression/clear", nil, true)
		wantN := `"cleared":1`
		if strings.Contains(name, "first") || name == "legacyboth" {
			wantN = `"cleared":2`
		}
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), wantN) {
			t.Fatalf("clear %s: %d %s, want %s", name, w.Code, w.Body.String(), wantN)
		}
	}
	var logs []models.AuditLog
	if err := app.DB.Where("action = ? AND target_id = ?", audit.EmailSuppressionCleared, users["complaintfirst"].ID).Find(&logs).Error; err != nil || len(logs) != 1 {
		t.Fatalf("audit rows for complaintfirst: %v, %v", logs, err)
	}
	var meta struct {
		Reasons []string `json:"reasons"`
	}
	if err := json.Unmarshal([]byte(logs[0].MetaJSON), &meta); err != nil {
		t.Fatal(err)
	}
	slices.Sort(meta.Reasons)
	if !slices.Equal(meta.Reasons, []string{models.SuppressComplaint, models.SuppressHardBounce}) {
		t.Fatalf("audit reasons = %v, want both", meta.Reasons)
	}
	// Only bouncefirst is still open.
	var open []models.Suppression
	app.DB.Where("cleared_at IS NULL").Find(&open)
	if len(open) != 2 || open[0].Address != "bouncefirst@example.com" || open[1].Address != "bouncefirst@example.com" {
		t.Fatalf("still open: %+v", open)
	}
}
