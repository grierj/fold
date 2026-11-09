package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"fold/internal/audit"
	"fold/internal/images"
	"fold/internal/images/imagetest"
	"fold/internal/notify"
	"fold/models"
	"fold/storage"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func TestFocalPoints(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "grier", "password1234", "member")
	leader := newTestUser(t, app, "leader", "password1234", "member")
	c := loginAs(t, app, "grier", "password1234")

	w := c.do("PUT", "/api/v1/me", map[string]interface{}{
		"avatarFocal": map[string]float64{"x": 30, "y": 70},
	}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("PUT /me focal: %d %s", w.Code, w.Body.String())
	}
	var me struct {
		User userJSON `json:"user"`
	}
	decodeJSON(t, w, &me)
	if me.User.AvatarFocalX != 30 || me.User.AvatarFocalY != 70 {
		t.Fatalf("focal not saved: %+v", me.User)
	}

	// Clamping.
	w = c.do("PUT", "/api/v1/me", map[string]interface{}{
		"avatarFocal": map[string]float64{"x": 150, "y": -20},
	}, true)
	decodeJSON(t, w, &me)
	if me.User.AvatarFocalX != 100 || me.User.AvatarFocalY != 0 {
		t.Fatalf("focal not clamped: %+v", me.User)
	}

	// Group cover focal (leader). Management checks the leader membership,
	// which the API creates alongside LeaderID.
	g := models.Group{Name: "G", LeaderID: leader.ID, Status: models.GroupActive}
	app.DB.Create(&g)
	app.DB.Create(&models.Membership{
		UserID: leader.ID, GroupID: g.ID,
		Role: models.MembershipLeader, Status: "active", JoinedAt: time.Now().UTC(),
	})
	lc := loginAs(t, app, "leader", "password1234")
	w = lc.do("PUT", "/api/v1/groups/"+itoa2(g.ID), map[string]interface{}{
		"coverFocal": map[string]float64{"x": 25, "y": 25},
	}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("PUT group focal: %d %s", w.Code, w.Body.String())
	}
	var gr struct {
		Group groupSummaryJSON `json:"group"`
	}
	decodeJSON(t, w, &gr)
	if gr.Group.CoverFocalX != 25 || gr.Group.CoverFocalY != 25 {
		t.Fatalf("cover focal not saved: %+v", gr.Group)
	}
}

func TestCoverFocalUpdateKeepsUpload(t *testing.T) {
	app := testApp(t)
	leader := newTestUser(t, app, "leader", "password1234", models.RoleMember)
	g := models.Group{Name: "Band", LeaderID: leader.ID, Status: models.GroupActive}
	if err := app.DB.Create(&g).Error; err != nil {
		t.Fatalf("create group: %v", err)
	}
	if err := app.DB.Create(&models.Membership{
		UserID: leader.ID, GroupID: g.ID,
		Role: models.MembershipLeader, Status: "active", JoinedAt: time.Now().UTC(),
	}).Error; err != nil {
		t.Fatal(err)
	}
	c := loginAPI(t, app, "leader", "password1234")
	gid := itoa(g.ID)
	w := c.doMultipart("POST", "/api/v1/groups/"+gid+"/cover", "cover", "cover.png", tinyPNG(t))
	if w.Code != http.StatusOK {
		t.Fatalf("upload: %d %s", w.Code, w.Body.String())
	}
	var uploaded struct {
		ImageURL string `json:"imageUrl"`
	}
	decodeJSON(t, w, &uploaded)

	w = c.do("PUT", "/api/v1/groups/"+gid, map[string]any{
		"coverFocal": map[string]float64{"x": 10, "y": 20},
	}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("focal: %d %s", w.Code, w.Body.String())
	}
	var got models.Group
	if err := app.DB.First(&got, g.ID).Error; err != nil {
		t.Fatal(err)
	}
	if got.ImageURL != uploaded.ImageURL || !strings.HasPrefix(got.ImageURL, "/uploads/") {
		t.Fatalf("cover URL changed: %q", got.ImageURL)
	}
	if got.CoverFocalX != 10 || got.CoverFocalY != 20 {
		t.Fatalf("focal: %v %v", got.CoverFocalX, got.CoverFocalY)
	}
}

// M1.2 acceptance: changing the sign-in email sends a verification link to
// the new address and a notice to the old one; the change applies only after
// verification and ends every other session.
func TestVerifiedEmailChange(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "alice", "password1234", models.RoleMember)
	ac := loginAPI(t, app, "alice", "password1234")
	other := loginAPI(t, app, "alice", "password1234") // a second session

	w := ac.do("PUT", "/api/v1/me", map[string]string{
		"email":           "alice.new@example.com",
		"currentPassword": "password1234",
	}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("request change: %d %s", w.Code, w.Body.String())
	}
	var body struct {
		VerificationSent bool `json:"verificationSent"`
	}
	decodeJSON(t, w, &body)
	if !body.VerificationSent {
		t.Fatalf("no verificationSent: %s", w.Body.String())
	}
	// The address hasn't changed yet.
	var u models.User
	if err := app.DB.Where("email = ?", "alice@example.com").First(&u).Error; err != nil {
		t.Fatalf("email changed before verification: %v", err)
	}

	// Verification link to the new address, notice to the old one.
	verifyMail := latestEmail(t, app, "alice.new@example.com")
	if !strings.Contains(verifyMail.Subject, "Verify") {
		t.Fatalf("verify subject: %q", verifyMail.Subject)
	}
	noticeMail := latestEmail(t, app, "alice@example.com")
	if !strings.Contains(noticeMail.Subject, "changing") {
		t.Fatalf("notice subject: %q", noticeMail.Subject)
	}
	token := tokenInEmail(t, verifyMail)

	// Verify: the change applies.
	w = ac.do("POST", "/api/v1/me/email/verify", map[string]string{"token": token}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("verify: %d %s", w.Code, w.Body.String())
	}
	if err := app.DB.Where("email = ?", "alice.new@example.com").First(&u).Error; err != nil {
		t.Fatalf("email not changed: %v", err)
	}
	if auditCount(t, app, audit.EmailChangeRequested) != 1 ||
		auditCount(t, app, audit.EmailChanged) != 1 {
		t.Fatalf("email change not audited")
	}

	// Every other session ended...
	w = other.do("GET", "/api/v1/auth/me", nil, false)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("other session alive: %d", w.Code)
	}
	// ...but this one still works.
	w = ac.do("GET", "/api/v1/auth/me", nil, false)
	if w.Code != http.StatusOK {
		t.Fatalf("own session ended: %d", w.Code)
	}
	// Single use.
	w = ac.do("POST", "/api/v1/me/email/verify", map[string]string{"token": token}, true)
	if w.Code == http.StatusOK {
		t.Fatalf("verification token reused")
	}

	// Login works with the new address.
	loginAPI(t, app, "alice.new", "password1234")
}

func TestEmailChangeRequiresCurrentPassword(t *testing.T) {
	app := testApp(t)
	alice := newTestUser(t, app, "alice", "password1234", models.RoleMember)
	known := newTestUser(t, app, "known", "password1234", models.RoleMember)
	ac := loginAPI(t, app, "alice", "password1234")

	for _, attempt := range []struct {
		name     string
		email    string
		password string
	}{
		{name: "missing password known address", email: known.Email},
		{name: "missing password new address", email: "new@example.com"},
		{name: "wrong password known address", email: known.Email, password: " password1234"},
		{name: "wrong password new address", email: "new@example.com", password: " password1234"},
	} {
		t.Run(attempt.name, func(t *testing.T) {
			w := ac.do("PUT", "/api/v1/me", map[string]string{
				"email":           attempt.email,
				"currentPassword": attempt.password,
			}, true)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status: %d %s", w.Code, w.Body.String())
			}
			var body struct {
				Code string `json:"code"`
			}
			decodeJSON(t, w, &body)
			if body.Code != "wrong_password" {
				t.Fatalf("code: %q, want wrong_password", body.Code)
			}
		})
	}

	var tokens, mails, budgets int64
	app.DB.Model(&models.AccountToken{}).Where("user_id = ? AND used_at IS NULL", alice.ID).Count(&tokens)
	app.DB.Model(&models.MailOutbox{}).Count(&mails)
	app.DB.Model(&models.LoginThrottle{}).Where("key LIKE ?", "emailchange:%").Count(&budgets)
	if tokens != 0 || mails != 0 || budgets != 0 {
		t.Fatalf("failed proof mutated state: tokens=%d mails=%d budgets=%d", tokens, mails, budgets)
	}

	apiDo(t, ac, "PUT", "/api/v1/me", map[string]string{
		"email":           known.Email,
		"currentPassword": "password1234",
	}, http.StatusConflict)
	var actorBudgets []models.LoginThrottle
	app.DB.Where("key LIKE ?", "emailchange:user:"+strconv.FormatUint(uint64(alice.ID), 10)+":%").Find(&actorBudgets)
	if len(actorBudgets) != 2 || actorBudgets[0].Failures != 1 || actorBudgets[1].Failures != 1 {
		t.Fatalf("existing address actor cost: %+v", actorBudgets)
	}

	apiDo(t, ac, "PUT", "/api/v1/me", map[string]string{
		"email":           "fresh@example.com",
		"currentPassword": "password1234",
	}, http.StatusOK)
	var verifyTokens, cancelTokens int64
	app.DB.Model(&models.AccountToken{}).Where("user_id = ? AND kind = ? AND used_at IS NULL", alice.ID, models.TokenEmailVerify).Count(&verifyTokens)
	app.DB.Model(&models.AccountToken{}).Where("user_id = ? AND kind = ? AND used_at IS NULL", alice.ID, models.TokenEmailChangeCancel).Count(&cancelTokens)
	app.DB.Model(&models.MailOutbox{}).Count(&mails)
	if verifyTokens != 1 || cancelTokens != 1 || mails != 2 {
		t.Fatalf("valid change: verify=%d cancel=%d mails=%d", verifyTokens, cancelTokens, mails)
	}
	if mailsTo(app, known.Email) != 0 || mailsTo(app, alice.Email) != 1 || mailsTo(app, "fresh@example.com") != 1 {
		t.Fatalf("valid change mail recipients were not old and fresh addresses")
	}
}

func TestUpdateMe(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "grier", "password1234", "member")
	c := loginAPI(t, app, "grier", "password1234")

	w := c.do("PUT", "/api/v1/me", map[string]string{
		"name":        "Grier Johnson",
		"displayName": "Grier",
		"email":       "Grier@Example.com", // same address: allowed
		"phone":       "555-0100",
	}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("PUT /me: %d %s", w.Code, w.Body.String())
	}
	var resp struct {
		User userJSON `json:"user"`
	}
	decodeJSON(t, w, &resp)
	if resp.User.DisplayName != "Grier" || resp.User.Name != "Grier Johnson" ||
		resp.User.Email != "grier@example.com" || resp.User.Phone != "555-0100" {
		t.Fatalf("unexpected user json: %+v", resp.User)
	}
	if resp.User.AvatarURL != "" {
		t.Fatalf("expected empty avatarUrl, got %q", resp.User.AvatarURL)
	}

	// Partial update: only displayName.
	w = c.do("PUT", "/api/v1/me", map[string]string{"displayName": "GJ"}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("PUT /me partial: %d %s", w.Code, w.Body.String())
	}
	decodeJSON(t, w, &resp)
	if resp.User.DisplayName != "GJ" {
		t.Fatalf("displayName not updated: %+v", resp.User)
	}
	if resp.User.Name != "Grier Johnson" {
		t.Fatalf("name should be unchanged: %+v", resp.User)
	}
}

func TestUpdateMeValidation(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "grier", "password1234", "member")
	c := loginAPI(t, app, "grier", "password1234")

	w := c.do("PUT", "/api/v1/me", map[string]string{"email": "not-an-email"}, true)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("bad email: got %d, want 400", w.Code)
	}
	w = c.do("PUT", "/api/v1/me", map[string]string{"name": strings.Repeat("x", 101)}, true)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("long name: got %d, want 400", w.Code)
	}
	// The email is the login: it can't be cleared, and changing it starts
	// a verification flow (M1.2) instead of applying directly.
	w = c.do("PUT", "/api/v1/me", map[string]string{"email": ""}, true)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("clear email: got %d, want 400", w.Code)
	}
	w = c.do("PUT", "/api/v1/me", map[string]string{
		"email":           "other@example.com",
		"currentPassword": "password1234",
	}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("change email: got %d, want 200", w.Code)
	}
	var changed struct {
		VerificationSent bool `json:"verificationSent"`
		User             struct {
			Email string `json:"email"`
		} `json:"user"`
	}
	decodeJSON(t, w, &changed)
	if !changed.VerificationSent {
		t.Fatalf("no verification started: %s", w.Body.String())
	}
	if changed.User.Email != "grier@example.com" {
		t.Fatalf("email changed before verification: %q", changed.User.Email)
	}
}

func TestAvatarUploadAndDelete(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "grier", "password1234", "member")
	c := loginAPI(t, app, "grier", "password1234")

	w := c.doMultipart("POST", "/api/v1/me/avatar", "avatar", "me.png", tinyPNG(t))
	if w.Code != http.StatusOK {
		t.Fatalf("avatar upload: %d %s", w.Code, w.Body.String())
	}
	var resp struct {
		AvatarURL string `json:"avatarUrl"`
	}
	decodeJSON(t, w, &resp)
	if !strings.HasPrefix(resp.AvatarURL, "/uploads/avatars/") {
		t.Fatalf("unexpected avatarUrl %q", resp.AvatarURL)
	}

	// The file is publicly served.
	getW := httptest.NewRecorder()
	app.setupRouter().ServeHTTP(getW, httptest.NewRequest("GET", resp.AvatarURL, nil))
	if getW.Code != http.StatusOK {
		t.Fatalf("GET avatar: %d", getW.Code)
	}
	if ct := getW.Header().Get("Content-Type"); ct != "image/png" {
		t.Fatalf("avatar content-type %q", ct)
	}
	if getW.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("missing nosniff header")
	}

	// /auth/me reflects the avatar.
	w = c.do("GET", "/api/v1/auth/me", nil, false)
	var me struct {
		User userJSON `json:"user"`
	}
	decodeJSON(t, w, &me)
	if me.User.AvatarURL != resp.AvatarURL {
		t.Fatalf("me avatarUrl %q != %q", me.User.AvatarURL, resp.AvatarURL)
	}

	// Delete it.
	w = c.do("DELETE", "/api/v1/me/avatar", nil, true)
	if w.Code != http.StatusOK {
		t.Fatalf("avatar delete: %d %s", w.Code, w.Body.String())
	}
	var u models.User
	app.DB.Where("email = ?", testEmail("grier")).First(&u)
	if u.AvatarKey != "" {
		t.Fatalf("avatar key not cleared")
	}
	// The request path deletes no file; the cleanup job does.
	drainAvatarJobs(t, app)
	// The file is gone.
	getW = httptest.NewRecorder()
	app.setupRouter().ServeHTTP(getW, httptest.NewRequest("GET", resp.AvatarURL, nil))
	if getW.Code != http.StatusNotFound {
		t.Fatalf("GET deleted avatar: %d, want 404", getW.Code)
	}
}

func TestAvatarUploadRejectsNonImage(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "grier", "password1234", "member")
	c := loginAPI(t, app, "grier", "password1234")

	w := c.doMultipart("POST", "/api/v1/me/avatar", "avatar", "evil.html",
		[]byte("<script>alert(1)</script>"))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("non-image upload: got %d, want 400", w.Code)
	}
}

func TestGroupCoverUpload(t *testing.T) {
	app := testApp(t)
	leader := newTestUser(t, app, "grier", "password1234", "member")
	other := newTestUser(t, app, "other", "password1234", "member")
	_ = other
	g := newTestGroup(t, app, leader, "Test Group", models.GroupActive)

	leaderClient := loginAPI(t, app, "grier", "password1234")
	coverPath := "/api/v1/groups/" + strconv.Itoa(int(g.ID)) + "/cover"
	w := leaderClient.doMultipart("POST", coverPath, "cover", "cover.png", tinyPNG(t))
	if w.Code != http.StatusOK {
		t.Fatalf("cover upload: %d %s", w.Code, w.Body.String())
	}
	var resp struct {
		ImageURL string `json:"imageUrl"`
	}
	decodeJSON(t, w, &resp)
	if !strings.HasPrefix(resp.ImageURL, "/uploads/covers/") {
		t.Fatalf("unexpected imageUrl %q", resp.ImageURL)
	}

	// Non-leader, non-admin cannot upload.
	otherClient := loginAPI(t, app, "other", "password1234")
	w = otherClient.doMultipart("POST", coverPath, "cover", "cover.png", tinyPNG(t))
	if w.Code != http.StatusForbidden {
		t.Fatalf("non-leader cover upload: got %d, want 403", w.Code)
	}

	// Leader can delete it.
	w = leaderClient.do("DELETE", coverPath, nil, true)
	if w.Code != http.StatusOK {
		t.Fatalf("cover delete: %d %s", w.Code, w.Body.String())
	}
	var got models.Group
	app.DB.First(&got, g.ID)
	if got.ImageURL != "" {
		t.Fatalf("cover not cleared")
	}
}

func TestServeUploadRejectsTraversal(t *testing.T) {
	app := testApp(t)
	r := app.setupRouter()
	for _, p := range []string{"/uploads/../main.go", "/uploads/%2e%2e/main.go"} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("GET", p, nil))
		if w.Code != http.StatusNotFound {
			t.Fatalf("GET %s: got %d, want 404", p, w.Code)
		}
	}
}

// embeddingUploads wraps *storage.LocalStore the way test barriers do: Open
// is promoted, so serving keeps working while a wrapper is installed.
type embeddingUploads struct{ *storage.LocalStore }

// noOpenUploads hides Open, like a store with no local files (S3).
type noOpenUploads struct{ storage.Store }

func writeUploadFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", rel, err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}

func getUpload(t *testing.T, app *App, path string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	app.setupRouter().ServeHTTP(w, httptest.NewRequest("GET", path, nil))
	return w
}

// serveUpload hands out only the exact regular file a key names: a symlink
// entry, a symlinked owner folder or a FIFO answers 404, and a FIFO does not
// hang the request.
func TestServeUploadServesOnlyRegularFiles(t *testing.T) {
	app := testApp(t)
	dir := app.Uploads.(*storage.LocalStore).Dir
	writeUploadFile(t, dir, "avatars/1/real.png", "real bytes")
	writeUploadFile(t, dir, "avatars/2/secret.png", "owner two")
	writeUploadFile(t, dir, "covers/3/z.png", "a cover")
	for link, target := range map[string]string{
		"avatars/1/to-secret": "../2/secret.png",
		"avatars/1/to-cover":  "../../covers/3/z.png",
		"avatars/4":           "2",
	} {
		if err := os.Symlink(target, filepath.Join(dir, filepath.FromSlash(link))); err != nil {
			t.Fatalf("symlink %s: %v", link, err)
		}
	}
	if err := syscall.Mkfifo(filepath.Join(dir, "avatars/1/pipe.png"), 0o644); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}

	w := getUpload(t, app, "/uploads/avatars/1/real.png")
	if w.Code != http.StatusOK || w.Body.String() != "real bytes" {
		t.Fatalf("real file: %d %q", w.Code, w.Body.String())
	}
	if w.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("missing nosniff")
	}
	if cc := w.Header().Get("Cache-Control"); cc != "public, max-age=86400" {
		t.Fatalf("Cache-Control %q", cc)
	}
	if w := getUpload(t, app, "/uploads/covers/3/z.png"); w.Code != http.StatusOK {
		t.Fatalf("cover: %d", w.Code)
	}

	for _, p := range []string{
		"/uploads/avatars/1/to-secret",
		"/uploads/avatars/1/to-cover",
		"/uploads/avatars/4/secret.png",
		"/uploads/avatars/1/pipe.png",
		"/uploads/avatars/1/missing.png",
		"/uploads/avatars/1",
		"/uploads/avatars/01/real.png",
	} {
		done := make(chan int, 1)
		go func() { done <- getUpload(t, app, p).Code }()
		select {
		case code := <-done:
			if code != http.StatusNotFound {
				t.Fatalf("GET %s: %d, want 404", p, code)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("GET %s blocked", p)
		}
	}
}

func TestServeUploadWorksThroughWrappersAndRedirectsWithoutOpen(t *testing.T) {
	app := testApp(t)
	local := app.Uploads.(*storage.LocalStore)
	writeUploadFile(t, local.Dir, "avatars/1/real.png", "real bytes")

	app.Uploads = &embeddingUploads{local}
	if w := getUpload(t, app, "/uploads/avatars/1/real.png"); w.Code != http.StatusOK || w.Body.String() != "real bytes" {
		t.Fatalf("wrapped store: %d %q", w.Code, w.Body.String())
	}

	app.Uploads = noOpenUploads{local}
	w := getUpload(t, app, "/uploads/avatars/1/real.png")
	if w.Code != http.StatusFound || w.Header().Get("Location") != "/uploads/avatars/1/real.png" {
		t.Fatalf("store without Open: %d %q", w.Code, w.Header().Get("Location"))
	}
}

// Covers are upload-only: an imageUrl in a create or update body is ignored.
func TestGroupCoverLinkIgnored(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
	c := loginAPI(t, app, "admin", "password1234")
	w := c.do("POST", "/api/v1/groups", map[string]any{"name": "Choir", "imageUrl": "https://example.com/a.jpg"}, true)
	var created struct {
		Group groupSummaryJSON `json:"group"`
	}
	decodeJSON(t, w, &created)
	if created.Group.ImageURL != "" {
		t.Fatalf("create stored link: %q", created.Group.ImageURL)
	}
	c.do("PUT", "/api/v1/groups/"+itoa(created.Group.ID), map[string]any{"imageUrl": "javascript:alert(1)"}, true)
	var g models.Group
	app.DB.First(&g, created.Group.ID)
	if g.ImageURL != "" {
		t.Fatalf("update stored link: %q", g.ImageURL)
	}
	if w := c.do("POST", "/api/v1/me/avatar/url", map[string]string{"url": "https://example.com/a.png"}, true); w.Code != http.StatusNotFound {
		t.Fatalf("avatar url endpoint still exists: %d", w.Code)
	}
}

// The profile time zone (DEC-58) starts empty (the church's), takes any
// IANA name, and refuses names time.LoadLocation would quietly accept.
func TestUpdateMeTimezone(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "grier", "password1234", "member")
	c := loginAPI(t, app, "grier", "password1234")
	var me struct {
		User struct {
			Timezone string `json:"timezone"`
		} `json:"user"`
	}
	decodeJSON(t, c.do("GET", "/api/v1/auth/me", nil, false), &me)
	if me.User.Timezone != "" {
		t.Fatalf("new user zone: %q, want empty", me.User.Timezone)
	}
	for _, bad := range []string{"", "Local", "Mars/Olympus", strings.Repeat("A", 65)} {
		if w := c.do("PUT", "/api/v1/me", map[string]string{"timezone": bad}, true); w.Code != http.StatusBadRequest {
			t.Fatalf("zone %q: got %d, want 400", bad, w.Code)
		}
	}
	w := c.do("PUT", "/api/v1/me", map[string]string{"timezone": " America/Boise "}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("set zone: %d %s", w.Code, w.Body.String())
	}
	decodeJSON(t, w, &me)
	if me.User.Timezone != "America/Boise" {
		t.Fatalf("saved zone: %q", me.User.Timezone)
	}
}

// #92: the email-change form can't be used to spam an address. Past the
// limit the answer is the same (DEC-52) but nothing is sent.
func TestEmailChangeRateLimited(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "alice", "password1234", models.RoleMember)
	ac := loginAPI(t, app, "alice", "password1234")
	for i := 0; i < targetHourLimit; i++ {
		apiDo(t, ac, "PUT", "/api/v1/me", map[string]string{
			"email":           "victim@example.com",
			"currentPassword": "password1234",
		}, http.StatusOK)
	}
	apiDo(t, ac, "PUT", "/api/v1/me", map[string]string{
		"email":           "victim@example.com",
		"currentPassword": "password1234",
	}, http.StatusOK)
	var n int64
	app.DB.Model(&models.MailOutbox{}).Where("to_address = ?", "victim@example.com").Count(&n)
	if n != targetHourLimit {
		t.Fatalf("victim got %d emails, want %d", n, targetHourLimit)
	}
	// The per-user limit applies across targets too.
	apiDo(t, ac, "PUT", "/api/v1/me", map[string]string{
		"email":           "other@example.com",
		"currentPassword": "password1234",
	}, http.StatusOK)
	// Another member can't add to the same victim either.
	newTestUser(t, app, "bob", "password1234", models.RoleMember)
	bc := loginAPI(t, app, "bob", "password1234")
	apiDo(t, bc, "PUT", "/api/v1/me", map[string]string{
		"email":           "victim@example.com",
		"currentPassword": "password1234",
	}, http.StatusOK)
	for addr, want := range map[string]int64{"victim@example.com": targetHourLimit, "other@example.com": 0} {
		app.DB.Model(&models.MailOutbox{}).Where("to_address = ?", addr).Count(&n)
		if n != want {
			t.Fatalf("%s got %d emails, want %d", addr, n, want)
		}
	}
}

func TestEmailChangeExistingAccountConflictsConsumeActorLimit(t *testing.T) {
	app := testApp(t)
	alice := newTestUser(t, app, "alice", "password1234", models.RoleMember)
	known := newTestUser(t, app, "known", "password1234", models.RoleMember)
	ac := loginAPI(t, app, "alice", "password1234")

	for i := 0; i < targetHourLimit; i++ {
		apiDo(t, ac, "PUT", "/api/v1/me", map[string]string{
			"email":           known.Email,
			"currentPassword": "password1234",
		}, http.StatusConflict)
	}
	for _, address := range []string{known.Email, "new@example.com"} {
		w := ac.do("PUT", "/api/v1/me", map[string]string{
			"email":           address,
			"currentPassword": "password1234",
		}, true)
		if w.Code != http.StatusOK {
			t.Fatalf("limited request for %s: %d %s", address, w.Code, w.Body.String())
		}
		var body struct {
			VerificationSent bool `json:"verificationSent"`
		}
		decodeJSON(t, w, &body)
		if !body.VerificationSent {
			t.Fatalf("limited request for %s was not uniform: %s", address, w.Body.String())
		}
	}

	var tokens, mails int64
	app.DB.Model(&models.AccountToken{}).Where("kind = ? AND user_id = ?", models.TokenEmailVerify, alice.ID).Count(&tokens)
	app.DB.Model(&models.MailOutbox{}).Count(&mails)
	if tokens != 0 || mails != 0 {
		t.Fatalf("refused changes mutated tokens or outbox: tokens=%d mails=%d", tokens, mails)
	}
}

func TestExhaustedEmailChangeActorDoesNotSpendFreshTargetLimit(t *testing.T) {
	app := testApp(t)
	alice := newTestUser(t, app, "alice", "password1234", models.RoleMember)
	known := newTestUser(t, app, "known", "password1234", models.RoleMember)
	ac := loginAPI(t, app, "alice", "password1234")

	apiDo(t, ac, "PUT", "/api/v1/me", map[string]string{
		"email":           "pending@example.com",
		"currentPassword": "password1234",
	}, http.StatusOK)
	var before models.AccountToken
	if err := app.DB.Where("kind = ? AND user_id = ? AND used_at IS NULL", models.TokenEmailVerify, alice.ID).First(&before).Error; err != nil {
		t.Fatalf("pending verification token: %v", err)
	}
	for i := 1; i < targetHourLimit; i++ {
		apiDo(t, ac, "PUT", "/api/v1/me", map[string]string{
			"email":           known.Email,
			"currentPassword": "password1234",
		}, http.StatusConflict)
	}
	apiDo(t, ac, "PUT", "/api/v1/me", map[string]string{
		"email":           "fresh@example.com",
		"currentPassword": "password1234",
	}, http.StatusOK)

	var after models.AccountToken
	if err := app.DB.Where("kind = ? AND user_id = ? AND used_at IS NULL", models.TokenEmailVerify, alice.ID).First(&after).Error; err != nil {
		t.Fatalf("live verification token after limited request: %v", err)
	}
	if after.ID != before.ID || after.TokenHash != before.TokenHash || after.Payload != before.Payload {
		t.Fatalf("limited request replaced live token: before=%+v after=%+v", before, after)
	}
	if n := mailsTo(app, "fresh@example.com"); n != 0 {
		t.Fatalf("exhausted actor sent %d fresh-address emails", n)
	}

	newTestUser(t, app, "bob", "password1234", models.RoleMember)
	bc := loginAPI(t, app, "bob", "password1234")
	for i := 0; i < targetHourLimit; i++ {
		apiDo(t, bc, "PUT", "/api/v1/me", map[string]string{
			"email":           "fresh@example.com",
			"currentPassword": "password1234",
		}, http.StatusOK)
	}
	if n := mailsTo(app, "fresh@example.com"); n != targetHourLimit {
		t.Fatalf("fresh address got %d emails from second actor, want %d", n, targetHourLimit)
	}
}

// ---------------------------------------------------------------------------
// My household (M1.9 slice 1)
// ---------------------------------------------------------------------------

func getMyHousehold(t *testing.T, c *apiClient) householdJSON {
	t.Helper()
	w := c.do("GET", "/api/v1/me/household", nil, false)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /me/household: %d %s", w.Code, w.Body.String())
	}
	var body struct {
		Household householdJSON `json:"household"`
	}
	decodeJSON(t, w, &body)
	return body.Household
}

func addMyPerson(t *testing.T, c *apiClient, body map[string]any) householdPersonJSON {
	t.Helper()
	w := c.do("POST", "/api/v1/me/household/people", body, true)
	if w.Code != http.StatusCreated {
		t.Fatalf("POST /me/household/people %v: %d %s", body, w.Code, w.Body.String())
	}
	var out struct {
		Person householdPersonJSON `json:"person"`
	}
	decodeJSON(t, w, &out)
	return out.Person
}

func TestMyHouseholdCreatedOnFirstUse(t *testing.T) {
	app := testApp(t)
	u := newTestUser(t, app, "john", "password1234", models.RoleMember)
	app.DB.Model(u).Update("name", "John Smith")
	c := loginAPI(t, app, "john", "password1234")

	// GET is read-only (Warden, #106): no household yet means null, and
	// nothing is written.
	if w := c.do("GET", "/api/v1/me/household", nil, false); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"household":null`) {
		t.Fatalf("GET before first use: %d %s", w.Code, w.Body.String())
	}
	var none int64
	app.DB.Model(&models.HouseholdAccount{}).Where("user_id = ?", u.ID).Count(&none)
	if none != 0 {
		t.Fatalf("GET created a household account row")
	}
	// PUT creates it on first use.
	if w := c.do("PUT", "/api/v1/me/household", map[string]string{}, true); w.Code != http.StatusOK {
		t.Fatalf("PUT first use: %d %s", w.Code, w.Body.String())
	}

	h := getMyHousehold(t, c)
	if h.Name != "Smith household" || h.CreatedBy != u.ID || len(h.People) != 0 {
		t.Fatalf("first household: %+v", h)
	}
	if len(h.Accounts) != 1 || h.Accounts[0].UserID != u.ID || h.Accounts[0].Relation != models.HouseholdRelationGuardian {
		t.Fatalf("household accounts: %+v", h.Accounts)
	}
	// Created once: a later GET is the same household, audited once.
	if again := getMyHousehold(t, c); again.ID != h.ID {
		t.Fatalf("second GET made household %d, want %d", again.ID, h.ID)
	}
	if rows, _ := auditRows(t, app, audit.HouseholdCreated); len(rows) != 1 || rows[0].TargetType != "household" || rows[0].TargetID != h.ID {
		t.Fatalf("household.created audit: %+v", rows)
	}
	var count int64
	app.DB.Model(&models.HouseholdAccount{}).Where("user_id = ?", u.ID).Count(&count)
	if count != 1 {
		t.Fatalf("household accounts for one user: %d, want 1", count)
	}

	// Editable name and address; an empty name is refused.
	w := c.do("PUT", "/api/v1/me/household", map[string]string{"name": "The Smiths", "address": "1 Oak St"}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("PUT /me/household: %d %s", w.Code, w.Body.String())
	}
	if h = getMyHousehold(t, c); h.Name != "The Smiths" || h.Address != "1 Oak St" {
		t.Fatalf("edited household: %+v", h)
	}
	w = c.do("PUT", "/api/v1/me/household", map[string]string{"name": " "}, true)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("empty household name: %d, want 400", w.Code)
	}

	// Signed out: the auth answer, and no household appears.
	anon := newAPIClient(t, app)
	anon.fetchCSRF()
	if w := anon.do("GET", "/api/v1/me/household", nil, false); w.Code != http.StatusUnauthorized {
		t.Fatalf("signed-out household: %d, want 401", w.Code)
	}
}

func TestTeenAccountGetsNoHousehold(t *testing.T) {
	app := testApp(t)
	teenUser := newTestUser(t, app, "teen", "password1234", models.RoleMember)
	app.DB.Model(teenUser).Update("account_type", models.AccountTeen)
	c := loginAPI(t, app, "teen", "password1234")
	if w := c.do("GET", "/api/v1/me/household", nil, false); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"household":null`) {
		t.Fatalf("teen GET household: %d %s, want null", w.Code, w.Body.String())
	}
	if w := c.do("PUT", "/api/v1/me/household", map[string]string{"name": "Mine"}, true); w.Code != http.StatusForbidden {
		t.Fatalf("teen PUT household: %d %s, want 403", w.Code, w.Body.String())
	}
	var count int64
	app.DB.Model(&models.HouseholdAccount{}).Where("user_id = ?", teenUser.ID).Count(&count)
	if count != 0 {
		t.Fatalf("a teen got a household account row: %d", count)
	}
}

func TestAddHouseholdPeopleAndLinks(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
	newTestUser(t, app, "pat", "password1234", models.RoleMember)
	admin := loginAPI(t, app, "admin", "password1234")
	c := loginAPI(t, app, "pat", "password1234")

	// An adult needs no approval: no link, no admin task.
	spouse := addMyPerson(t, c, map[string]any{"firstName": "Mary", "lastName": "Smith", "kind": "adult"})
	if spouse.LinkStatus != "none" || spouse.GuardianLinkID != nil || spouse.Name != "Mary Smith" {
		t.Fatalf("adult person: %+v", spouse)
	}
	var links int64
	app.DB.Model(&models.GuardianLink{}).Count(&links)
	if links != 0 {
		t.Fatalf("adult person made %d guardian links", links)
	}
	if _, _, waiting, _ := badge(t, admin, ""); waiting != 0 {
		t.Fatalf("admin waiting after adult: %d", waiting)
	}

	// A child waits for an admin: a pending link, a waiting bell item,
	// and a household.person_added audit row.
	child := addMyPerson(t, c, map[string]any{
		"firstName": "Kid", "lastName": "Smith", "kind": "child",
		"birthDate": "2020-05-01", "careNotes": "Peanut allergy", "photoConsent": true,
	})
	if child.LinkStatus != models.GuardianPending || child.GuardianLinkID == nil || child.BirthDate != "2020-05-01" || !child.PhotoConsent || child.CareNotes != nil {
		t.Fatalf("child person: %+v", child)
	}
	var link models.GuardianLink
	if err := app.DB.First(&link, *child.GuardianLinkID).Error; err != nil {
		t.Fatal(err)
	}
	if link.Status != models.GuardianPending || link.PersonID == nil || *link.PersonID != child.ID || link.TeenUserID != nil {
		t.Fatalf("guardian link: %+v", link)
	}
	if _, _, waiting, _ := badge(t, admin, ""); waiting != 1 {
		t.Fatalf("admin waiting after child: %d, want 1", waiting)
	}
	if n := findKind(feed(t, admin, ""), "household.link_pending"); n == nil || !n.Waiting {
		t.Fatalf("admin feed has no waiting household link: %+v", feed(t, admin, ""))
	}
	if rows, _ := auditRows(t, app, audit.HouseholdPersonAdded); len(rows) != 2 {
		t.Fatalf("household.person_added audit rows: %d, want 2", len(rows))
	}

	// A teen waits the same way.
	teen := addMyPerson(t, c, map[string]any{"firstName": "Tess", "kind": "teen", "birthDate": "2012-03-04"})
	if teen.LinkStatus != models.GuardianPending {
		t.Fatalf("teen person: %+v", teen)
	}
	if h := getMyHousehold(t, c); len(h.People) != 3 {
		t.Fatalf("household people: %+v", h.People)
	}

	// Validation: bad kind, no first name, bad birth date.
	for name, body := range map[string]map[string]any{
		"bad kind":      {"firstName": "X", "kind": "alien"},
		"no first name": {"kind": "adult"},
		"bad date":      {"firstName": "X", "kind": "child", "birthDate": "2020-02-30"},
	} {
		if w := c.do("POST", "/api/v1/me/household/people", body, true); w.Code != http.StatusBadRequest {
			t.Fatalf("%s: %d %s, want 400", name, w.Code, w.Body.String())
		}
	}
	w := c.do("POST", "/api/v1/me/household/people", map[string]any{"firstName": "X", "kind": "alien"}, true)
	if !strings.Contains(w.Body.String(), `"code":"invalid_kind"`) {
		t.Fatalf("bad kind code: %s", w.Body.String())
	}
}

func TestHouseholdPersonEditAndRemove(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
	newTestUser(t, app, "pat", "password1234", models.RoleMember)
	admin := loginAPI(t, app, "admin", "password1234")
	c := loginAPI(t, app, "pat", "password1234")

	child := addMyPerson(t, c, map[string]any{"firstName": "Kid", "kind": "child", "birthDate": "2021-01-02"})
	path := "/api/v1/me/household/people/" + itoa2(child.ID)

	w := c.do("PUT", path, map[string]any{"firstName": "Kidd", "careNotes": "Asthma inhaler"}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("edit person: %d %s", w.Code, w.Body.String())
	}
	var edited struct {
		Person householdPersonJSON `json:"person"`
	}
	decodeJSON(t, w, &edited)
	if edited.Person.FirstName != "Kidd" || edited.Person.CareNotes != nil || edited.Person.LinkStatus != models.GuardianPending {
		t.Fatalf("edited person: %+v", edited.Person)
	}

	// Becoming an adult revokes the pending link and closes the task.
	if _, _, waiting, _ := badge(t, admin, ""); waiting != 1 {
		t.Fatalf("admin waiting before kind change: %d", waiting)
	}
	w = c.do("PUT", path, map[string]any{"kind": "adult", "birthDate": "1990-01-02"}, true)
	decodeJSON(t, w, &edited)
	if edited.Person.Kind != "adult" || edited.Person.LinkStatus != "none" {
		t.Fatalf("adult after kind change: %+v", edited.Person)
	}
	var link models.GuardianLink
	app.DB.Where("person_id = ?", child.ID).First(&link)
	if link.Status != models.GuardianRevoked {
		t.Fatalf("link after kind change: %+v", link)
	}
	if _, _, waiting, _ := badge(t, admin, ""); waiting != 0 {
		t.Fatalf("admin waiting after revoke: %d", waiting)
	}
	if rows, _ := auditRows(t, app, audit.GuardianLinkRevoked); len(rows) != 1 {
		t.Fatalf("guardian_link.revoked audit rows: %d", len(rows))
	}

	// Removing is a soft delete of the household person's place in the list.
	w = c.do("DELETE", path, nil, true)
	if w.Code != http.StatusOK {
		t.Fatalf("remove person: %d %s", w.Code, w.Body.String())
	}
	if h := getMyHousehold(t, c); len(h.People) != 0 {
		t.Fatalf("people after removal: %+v", h.People)
	}
	var row models.HouseholdPerson
	if err := app.DB.Unscoped().First(&row, child.ID).Error; err != nil || !row.DeletedAt.Valid {
		t.Fatalf("removed person row should survive soft-deleted: %+v %v", row, err)
	}
	if rows, _ := auditRows(t, app, audit.HouseholdPersonRemoved); len(rows) != 1 {
		t.Fatalf("household.person_removed audit rows: %d", len(rows))
	}
	// Gone stays gone.
	if w := c.do("DELETE", path, nil, true); w.Code != http.StatusNotFound {
		t.Fatalf("second remove: %d, want 404", w.Code)
	}

	// Removing a still-pending child withdraws the admin task.
	pendingChild := addMyPerson(t, c, map[string]any{"firstName": "Pip", "kind": "child"})
	if _, _, waiting, _ := badge(t, admin, ""); waiting != 1 {
		t.Fatalf("admin waiting before removing Pip: %d", waiting)
	}
	apiDo(t, c, "DELETE", "/api/v1/me/household/people/"+itoa2(pendingChild.ID), nil, http.StatusOK)
	if _, _, waiting, _ := badge(t, admin, ""); waiting != 0 {
		t.Fatalf("admin waiting after removing pending child: %d", waiting)
	}
}

func TestHouseholdIsolation(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "ana", "password1234", models.RoleMember)
	newTestUser(t, app, "ben", "password1234", models.RoleMember)
	ana := loginAPI(t, app, "ana", "password1234")
	ben := loginAPI(t, app, "ben", "password1234")

	child := addMyPerson(t, ana, map[string]any{"firstName": "AnaKid", "kind": "child"})
	// Ben's household is his own: no people, a different id.
	hb := getMyHousehold(t, ben)
	if hb.ID == getMyHousehold(t, ana).ID || len(hb.People) != 0 {
		t.Fatalf("Ben's household: %+v", hb)
	}
	// Ben can't edit or remove Ana's person, and the answer is 404.
	path := "/api/v1/me/household/people/" + itoa2(child.ID)
	if w := ben.do("PUT", path, map[string]any{"firstName": "Hacked"}, true); w.Code != http.StatusNotFound {
		t.Fatalf("Ben edits Ana's person: %d, want 404", w.Code)
	}
	if w := ben.do("DELETE", path, nil, true); w.Code != http.StatusNotFound {
		t.Fatalf("Ben removes Ana's person: %d, want 404", w.Code)
	}
	if h := getMyHousehold(t, ana); len(h.People) != 1 || h.People[0].FirstName != "AnaKid" {
		t.Fatalf("Ana's household after Ben's attempts: %+v", h.People)
	}
}

func TestHouseholdRegistrationAgeAndLifecycle(t *testing.T) {
	app := testApp(t)
	admin, gid := openGroup(t, app, "PersonLifecycle")
	eventID, date := seedChildcareEvent(t, app, admin, gid, intPtr(2), intPtr(1), true)
	_, c, spouse, child := householdFamily(t, app, admin, "lifecycle")
	result := batchAnswer(t, c, eventID, date, map[string]any{"people": []map[string]any{{"personId": spouse.ID, "status": "going"}, {"personId": child.ID, "status": "going", "childcareRequested": true}}}, http.StatusOK)
	spousePath := "/api/v1/me/household/people/" + itoa(spouse.ID)
	apiDo(t, c, "PUT", spousePath, map[string]any{"birthDate": ""}, http.StatusConflict)
	apiDo(t, c, "PUT", "/api/v1/me/household/people/"+itoa(child.ID), map[string]any{"kind": "adult"}, http.StatusConflict)
	apiDo(t, c, "POST", "/api/v1/me/household/people", map[string]any{"firstName": "Minor", "kind": "adult", "birthDate": "2020-01-01"}, http.StatusConflict)
	unknown := addMyPerson(t, c, map[string]any{"firstName": "Unknown", "kind": "adult"})
	if unknown.RegistrationEligibility != "birth_date_required" {
		t.Fatalf("unknown adult eligibility %+v", unknown)
	}
	batchAnswer(t, c, eventID, date, map[string]any{"people": []map[string]any{{"personId": unknown.ID, "status": "going"}}}, http.StatusConflict)
	apiDo(t, c, "PUT", "/api/v1/me/household/people/"+itoa(child.ID), map[string]any{"kind": "teen", "birthDate": "2012-01-01"}, http.StatusOK)
	var r models.Registration
	if err := app.DB.First(&r, result.People[1].RegistrationID).Error; err != nil {
		t.Fatal(err)
	}
	if r.Status != models.RegGoing || r.ChildcareRequested {
		t.Fatalf("kind edit %+v", r)
	}
	apiDo(t, c, "DELETE", spousePath, nil, http.StatusOK)
	r = models.Registration{}
	if err := app.DB.First(&r, result.People[0].RegistrationID).Error; err != nil {
		t.Fatal(err)
	}
	if r.Status != models.RegCancelled || r.ManageTokenHash != "" {
		t.Fatalf("person delete %+v", r)
	}
}

func TestHouseholdProfileNotesRequireGuardian(t *testing.T) {
	app := testApp(t)
	admin, _ := openGroup(t, app, "Notes")
	holder, c, _, child := householdFamily(t, app, admin, "careholder")
	second := newTestUser(t, app, "nonGuardian", "password1234", models.RoleMember)
	h := getMyHousehold(t, c)
	if err := app.DB.Create(&models.HouseholdAccount{HouseholdID: h.ID, UserID: second.ID, Relation: models.HouseholdRelationAdult}).Error; err != nil {
		t.Fatal(err)
	}
	other := loginAPI(t, app, "nonGuardian", "password1234")
	raw := apiDo(t, other, "GET", "/api/v1/me/household", nil, http.StatusOK)
	if strings.Contains(raw, "Private allergy note") || strings.Contains(raw, `"careNotes"`) {
		t.Fatalf("unguarded notes: %s", raw)
	}
	apiDo(t, other, "PUT", "/api/v1/me/household/people/"+itoa(child.ID), map[string]any{"firstName": "Kitty"}, http.StatusOK)
	h = getMyHousehold(t, c)
	found := false
	for _, p := range h.People {
		if p.ID == child.ID {
			found = true
			if p.CareNotes == nil || *p.CareNotes != "Private allergy note" {
				t.Fatalf("omitted care cleared: %+v", p)
			}
		}
	}
	if !found {
		t.Fatal("child disappeared")
	}
	var link models.GuardianLink
	app.DB.Where("person_id = ? AND guardian_user_id = ?", child.ID, holder.ID).First(&link)
	if link.Status != models.GuardianConfirmed {
		t.Fatal("edit revoked a confirmed guardian")
	}
}

func TestProfileContactPrivacyPreference(t *testing.T) {
	app := testApp(t)
	adult := newTestUser(t, app, "adult", "password1234", models.RoleMember)
	client := loginAPI(t, app, adult.Email, "password1234")
	var me struct {
		User userJSON `json:"user"`
	}
	w := client.do("GET", "/api/v1/auth/me", nil, false)
	decodeJSON(t, w, &me)
	if me.User.HideContactFromLeaders {
		t.Fatal("adult preference must default to off")
	}
	for _, hidden := range []bool{true, true, false} {
		w = client.do("PUT", "/api/v1/me", map[string]any{"hideContactFromLeaders": hidden}, true)
		if w.Code != http.StatusOK {
			t.Fatalf("save: %d %s", w.Code, w.Body.String())
		}
		decodeJSON(t, w, &me)
		if me.User.HideContactFromLeaders != hidden || me.User.Email != adult.Email {
			t.Fatalf("self response: %+v", me.User)
		}
		var saved models.User
		if err := app.DB.First(&saved, adult.ID).Error; err != nil {
			t.Fatal(err)
		}
		if saved.HideContactFromLeaders != hidden {
			t.Fatalf("stored preference: %+v", saved)
		}
	}
	var count int64
	if err := app.DB.Model(&models.AuditLog{}).Where("action = ? AND target_id = ?", audit.ContactPrivacyChanged, adult.ID).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("privacy changes must audit once, no-op never: %d", count)
	}
	// This endpoint always changes the actor, not a submitted user ID.
	other := newTestUser(t, app, "other", "password1234", models.RoleMember)
	w = client.do("PUT", "/api/v1/me", map[string]any{"id": other.ID, "hideContactFromLeaders": true}, true)
	if w.Code != http.StatusOK {
		t.Fatal(w.Body.String())
	}
	if err := app.DB.First(other, other.ID).Error; err != nil {
		t.Fatal(err)
	}
	if other.HideContactFromLeaders {
		t.Fatal("changed someone else's preference")
	}
	// The ordinary CSRF and authenticated profile boundaries still apply.
	w = client.do("PUT", "/api/v1/me", map[string]any{"hideContactFromLeaders": false}, false)
	if w.Code != http.StatusForbidden {
		t.Fatalf("missing CSRF: %d", w.Code)
	}
	out := newAPIClient(t, app)
	out.fetchCSRF()
	w = out.do("PUT", "/api/v1/me", map[string]any{"hideContactFromLeaders": true}, true)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("signed out: %d", w.Code)
	}
}

func TestProfileTeenContactAlwaysHidden(t *testing.T) {
	app := testApp(t)
	teen := newTestUser(t, app, "teen", "password1234", models.RoleMember)
	birth := time.Now().UTC().AddDate(-15, 0, 0)
	if err := app.DB.Model(teen).Updates(map[string]any{"account_type": models.AccountTeen, "birth_date": birth}).Error; err != nil {
		t.Fatal(err)
	}
	client := loginAPI(t, app, teen.Email, "password1234")
	for _, pref := range []bool{false, true} {
		w := client.do("PUT", "/api/v1/me", map[string]any{"hideContactFromLeaders": pref, "displayName": "Teen name"}, true)
		if w.Code != http.StatusOK {
			t.Fatalf("save: %d %s", w.Code, w.Body.String())
		}
		var me struct {
			User userJSON `json:"user"`
		}
		decodeJSON(t, w, &me)
		if !me.User.HideContactFromLeaders || me.User.DisplayName != "Teen name" || me.User.Email != teen.Email {
			t.Fatalf("effective teen preference/self contact: %+v", me.User)
		}
		if err := app.DB.First(teen, teen.ID).Error; err != nil {
			t.Fatal(err)
		}
		if teen.HideContactFromLeaders {
			t.Fatal("teen changed the stored preference")
		}
	}
	var count int64
	app.DB.Model(&models.AuditLog{}).Where("action = ?", audit.ContactPrivacyChanged).Count(&count)
	if count != 0 {
		t.Fatalf("ignored teen preference audited as change: %d", count)
	}
}

func TestProfileContactPrivacyAuditFailureRollsBack(t *testing.T) {
	app := testApp(t)
	adult := newTestUser(t, app, "adult", "password1234", models.RoleMember)
	client := loginAPI(t, app, adult.Email, "password1234")
	const callback = "test:contact_privacy_audit_failure"
	if err := app.DB.Callback().Create().Before("gorm:create").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "audit_logs" {
			tx.AddError(errors.New("audit unavailable"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { app.DB.Callback().Create().Remove(callback) })
	w := client.do("PUT", "/api/v1/me", map[string]any{"hideContactFromLeaders": true, "phone": "changed"}, true)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("save with failed audit: %d %s", w.Code, w.Body.String())
	}
	if err := app.DB.First(adult, adult.ID).Error; err != nil {
		t.Fatal(err)
	}
	if adult.HideContactFromLeaders || adult.Phone != "" {
		t.Fatalf("audit failure committed profile: %+v", adult)
	}
}

// A refused email change is all-or-nothing: the other fields in the same
// request are not saved when the password proof fails.
func TestEmailChangeWrongPasswordSavesNothingElse(t *testing.T) {
	app := testApp(t)
	alice := newTestUser(t, app, "alice", "password1234", models.RoleMember)
	if err := app.DB.Model(alice).Updates(map[string]any{"name": "Alice Original", "phone": "555-0100"}).Error; err != nil {
		t.Fatal(err)
	}
	ac := loginAPI(t, app, "alice", "password1234")
	w := ac.do("PUT", "/api/v1/me", map[string]string{
		"name": "Changed Name", "phone": "555-9999",
		"email": "elsewhere@example.com", "currentPassword": "not-the-password",
	}, true)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status: %d %s", w.Code, w.Body.String())
	}
	var got models.User
	if err := app.DB.First(&got, alice.ID).Error; err != nil {
		t.Fatal(err)
	}
	if got.Name != "Alice Original" || got.Phone != "555-0100" {
		t.Fatalf("profile changed despite the refused email change: name=%q phone=%q", got.Name, got.Phone)
	}
	// With the right password the name and the email request both go through.
	apiDo(t, ac, "PUT", "/api/v1/me", map[string]string{
		"name": "Changed Name", "email": "elsewhere@example.com", "currentPassword": "password1234",
	}, http.StatusOK)
	if err := app.DB.First(&got, alice.ID).Error; err != nil || got.Name != "Changed Name" {
		t.Fatalf("name after a good proof: %q %v", got.Name, err)
	}
}

func TestProfileNameLimitCountsCharactersNotBytes(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "alice", "password1234", models.RoleMember)
	ac := loginAPI(t, app, "alice", "password1234")
	long := strings.Repeat("é", 100) // 200 bytes, 100 characters
	apiDo(t, ac, "PUT", "/api/v1/me", map[string]string{"displayName": long}, http.StatusOK)
	apiDo(t, ac, "PUT", "/api/v1/me", map[string]string{"displayName": long + "é"}, http.StatusBadRequest)
}

// uploadedBytes fetches a stored upload back through the public route.
func uploadedBytes(t *testing.T, app *App, url string) []byte {
	t.Helper()
	w := httptest.NewRecorder()
	app.setupRouter().ServeHTTP(w, httptest.NewRequest("GET", url, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("GET %s: %d", url, w.Code)
	}
	return w.Body.Bytes()
}

func TestAvatarUploadStripsEXIF(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "grier", "password1234", "member")
	c := loginAPI(t, app, "grier", "password1234")

	// Orientation 6 on a 64 x 96 JPEG with GPS tags in the EXIF block.
	in := imagetest.WithEXIF(imagetest.JPEGBytes(t, imagetest.BlockImage(2, 3, imagetest.GridColors)), imagetest.ExifBlock(6))
	w := c.doMultipart("POST", "/api/v1/me/avatar", "avatar", "me.jpg", in)
	if w.Code != http.StatusOK {
		t.Fatalf("avatar upload: %d %s", w.Code, w.Body.String())
	}
	var resp struct {
		AvatarURL string `json:"avatarUrl"`
	}
	decodeJSON(t, w, &resp)
	stored := uploadedBytes(t, app, resp.AvatarURL)
	for name, pat := range map[string][]byte{"Exif marker": []byte("Exif"), "maker string": imagetest.GPSMarker, "GPS latitude": imagetest.GPSLatitude} {
		if bytes.Contains(stored, pat) {
			t.Errorf("stored avatar still contains the %s", name)
		}
	}
	img, err := jpeg.Decode(bytes.NewReader(stored))
	if err != nil {
		t.Fatalf("stored avatar is not a JPEG: %v", err)
	}
	// Turned upright: 3 blocks wide, 2 tall.
	imagetest.AssertGrid(t, img, 3, 2, []color.RGBA{imagetest.CMagenta, imagetest.CBlue, imagetest.CRed, imagetest.CCyan, imagetest.CYellow, imagetest.CGreen})
}

func TestGroupCoverUploadStripsPNGText(t *testing.T) {
	app := testApp(t)
	leader := newTestUser(t, app, "grier", "password1234", "member")
	g := newTestGroup(t, app, leader, "Test Group", models.GroupActive)
	c := loginAPI(t, app, "grier", "password1234")

	coverPath := "/api/v1/groups/" + strconv.Itoa(int(g.ID)) + "/cover"
	w := c.doMultipart("POST", coverPath, "cover", "cover.png", imagetest.PNGWithText(t, imagetest.BlockImage(2, 3, imagetest.GridColors)))
	if w.Code != http.StatusOK {
		t.Fatalf("cover upload: %d %s", w.Code, w.Body.String())
	}
	var resp struct {
		ImageURL string `json:"imageUrl"`
	}
	decodeJSON(t, w, &resp)
	stored := uploadedBytes(t, app, resp.ImageURL)
	if bytes.Contains(stored, []byte("tEXt")) || bytes.Contains(stored, imagetest.GPSMarker) {
		t.Fatal("stored cover still has its text chunk")
	}
	if _, err := png.Decode(bytes.NewReader(stored)); err != nil {
		t.Fatalf("stored cover is not a PNG: %v", err)
	}
}

func TestAnimatedGIFAvatarStaysAnimated(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "grier", "password1234", "member")
	c := loginAPI(t, app, "grier", "password1234")

	w := c.doMultipart("POST", "/api/v1/me/avatar", "avatar", "me.gif", imagetest.AnimatedGIF(t, 3))
	if w.Code != http.StatusOK {
		t.Fatalf("avatar upload: %d %s", w.Code, w.Body.String())
	}
	var resp struct {
		AvatarURL string `json:"avatarUrl"`
	}
	decodeJSON(t, w, &resp)
	g, err := gif.DecodeAll(bytes.NewReader(uploadedBytes(t, app, resp.AvatarURL)))
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Image) != 3 || !reflect.DeepEqual(g.Delay, []int{10, 20, 30}) {
		t.Fatalf("animation changed: %d frames, delays %v", len(g.Image), g.Delay)
	}
}

func TestImageUploadRefusalsAreActionable(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "grier", "password1234", "member")
	c := loginAPI(t, app, "grier", "password1234")

	webp := append([]byte("RIFF\x24\x00\x00\x00WEBPVP8 "), make([]byte, 32)...)
	for name, tc := range map[string]struct {
		data []byte
		code string
	}{
		"webp":         {webp, "image_unsupported"},
		"huge header":  {imagetest.PNGHeaderOnly(100000, 100000), "image_too_large"},
		"many frames":  {imagetest.AnimatedGIF(t, images.MaxGIFFrames+1), "image_too_large"},
		"corrupt jpeg": {[]byte("\xFF\xD8\xFF\xE0 not really a jpeg at all"), "file_unreadable"},
	} {
		w := c.doMultipart("POST", "/api/v1/me/avatar", "avatar", "me.img", tc.data)
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), `"code":"`+tc.code+`"`) {
			t.Errorf("%s: got %d %s, want 400 with code %s", name, w.Code, w.Body.String(), tc.code)
		}
	}
	var u models.User
	app.DB.Where("email = ?", testEmail("grier")).First(&u)
	if u.AvatarKey != "" {
		t.Error("a refused upload must not set an avatar")
	}
}

// N-11 (inventory 9.2.2, a plain entry): adding a child to the household starts
// a guardian link and asks every admin to confirm it, while holding only the
// household lock. An admin erased while the link is created is left out in
// either order, and the other admin is still asked.
func TestGuardianLinkNoticeSerializesWithAdminErasure(t *testing.T) {
	runErasureBarriers(t, 7402005, func(t *testing.T) erasureScene {
		app := testApp(t)
		kept := newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
		victim := newTestUser(t, app, "admin2", "password1234", models.RoleAdmin)
		newTestUser(t, app, "parent", "password1234", models.RoleMember)
		parent := loginAPI(t, app, "parent", "password1234")
		// First use creates the household before the race; the race is the add.
		if w := parent.do("PUT", "/api/v1/me/household", map[string]string{}, true); w.Code != http.StatusOK {
			t.Fatalf("household: %d %s", w.Code, w.Body.String())
		}
		parent.holdNotices = true
		return erasureScene{app: app, victim: victim,
			run: func() error {
				return doExpect(parent, "POST", "/api/v1/me/household/people",
					map[string]any{"firstName": "Kit", "kind": "child", "birthDate": "2020-05-01"}, http.StatusCreated)
			},
			told: func(t *testing.T) {
				if n := contactCount(t, app, &models.Notification{}, "user_id = ? AND kind = ?", kept.ID, notify.HouseholdLinkPending); n != 1 {
					t.Fatalf("the other admin has %d link tasks, want 1", n)
				}
			},
		}
	})
}

// Replacing a photo three times deletes nothing on the request path; any
// number of cleanup runs afterwards leave only the latest.
func TestAvatarReplacementLeavesOnlyTheLatest(t *testing.T) {
	app := testApp(t)
	u := newTestUser(t, app, "grier", "password1234", "member")
	c := loginAPI(t, app, "grier", "password1234")
	var keys []string
	auditBefore := auditTotal(t, app)
	for i := 0; i < 3; i++ {
		if w := c.doMultipart("POST", "/api/v1/me/avatar", "avatar", "me.png", tinyPNG(t)); w.Code != http.StatusOK {
			t.Fatalf("upload %d: %d %s", i, w.Code, w.Body.String())
		}
		keys = append(keys, avatarKeyOf(t, app, u.ID))
	}
	if n := auditTotal(t, app); n != auditBefore {
		t.Fatalf("uploads wrote %d audit rows", n-auditBefore)
	}
	assertAvatarPayloads(t, app)
	if got := avatarFiles(t, app, u.ID); len(got) != 3 {
		t.Fatalf("the request path removed files: %v", got)
	}
	if js := avatarJobsFor(t, app, u.ID); len(js) != 1 || js[0].Status != models.JobQueued {
		t.Fatalf("three uploads with no runner should leave one queued job: %+v", js)
	}
	for i := 0; i < 2; i++ {
		if err := app.runAvatarCleanup(context.Background(), u.ID); err != nil {
			t.Fatal(err)
		}
	}
	if got := avatarFiles(t, app, u.ID); len(got) != 1 || "avatars/"+itoa(u.ID)+"/"+got[0] != keys[2] {
		t.Fatalf("files %v, want only %s", got, keys[2])
	}
	// A fourth upload after a run adds a fresh job, and both runs keep it.
	if w := c.doMultipart("POST", "/api/v1/me/avatar", "avatar", "me.png", tinyPNG(t)); w.Code != http.StatusOK {
		t.Fatalf("upload D: %d", w.Code)
	}
	d := avatarKeyOf(t, app, u.ID)
	drainAvatarJobs(t, app)
	if err := app.runAvatarCleanup(context.Background(), u.ID); err != nil {
		t.Fatal(err)
	}
	if got := avatarFiles(t, app, u.ID); len(got) != 1 || "avatars/"+itoa(u.ID)+"/"+got[0] != d {
		t.Fatalf("files %v, want only %s", got, d)
	}
}

// Removing again after the earlier job is dead or dismissed queues fresh work
// even though the key is already empty, so orphans do not wait for the sweep.
func TestAvatarRemovalQueuesFreshWorkEvenWithEmptyKey(t *testing.T) {
	for _, status := range []string{models.JobDead, models.JobDismissed} {
		t.Run(status, func(t *testing.T) {
			app := testApp(t)
			u := newTestUser(t, app, "grier", "password1234", "member")
			c := loginAPI(t, app, "grier", "password1234")
			writeUploadFile(t, avatarRootDir(app), fmt.Sprintf("avatars/%d/orphan.png", u.ID), "x")
			if err := app.DB.Create(&models.Job{Kind: kindAvatarCleanup, Payload: fmt.Sprintf(`{"userId":%d}`, u.ID),
				Status: status, MaxAttempts: 5}).Error; err != nil {
				t.Fatal(err)
			}
			auditBefore := auditTotal(t, app)
			if w := c.do("DELETE", "/api/v1/me/avatar", nil, true); w.Code != http.StatusOK {
				t.Fatalf("remove: %d %s", w.Code, w.Body.String())
			}
			if c.do("DELETE", "/api/v1/me/avatar", nil, true).Code != http.StatusOK {
				t.Fatal("second remove failed")
			}
			if n := auditTotal(t, app); n != auditBefore {
				t.Fatalf("removals wrote %d audit rows", n-auditBefore)
			}
			if js := avatarJobsFor(t, app, u.ID); len(js) != 2 || js[1].Status != models.JobQueued {
				t.Fatalf("jobs %+v, want the %s one plus one queued", js, status)
			}
			assertAvatarPayloads(t, app)
			drainAvatarJobs(t, app)
			if got := avatarFiles(t, app, u.ID); got != nil {
				t.Fatalf("orphans remain: %v", got)
			}
		})
	}
}

// Upload and removal recheck the session and account under the row lock. A
// request that waited while the account changed answers 401 (or 403 for a
// teen upload) and writes nothing.
func TestAvatarRequestsRecheckTheAccountUnderTheLock(t *testing.T) {
	for name, tc := range map[string]struct {
		set    string
		upload int
		remove int
	}{
		"session bumped": {"session_version = session_version + 1", http.StatusUnauthorized, http.StatusUnauthorized},
		"logout bumped":  {"logout_version = logout_version + 1", http.StatusUnauthorized, http.StatusUnauthorized},
		"disabled":       {"status = 'disabled'", http.StatusUnauthorized, http.StatusUnauthorized},
		"became a teen":  {"account_type = 'teen', birth_date = '2012-01-02'", http.StatusForbidden, http.StatusOK},
	} {
		for _, verb := range []string{"POST", "DELETE"} {
			t.Run(name+" "+verb, func(t *testing.T) {
				app := testApp(t)
				store := installAvatarBarrier(app)
				u, cur := avatarUser(t, app, "grier")
				c := loginAPI(t, app, "grier", "pw")
				holder, pid := beginOwnedTx(t, app.DB)
				if err := holder.Exec("SELECT id FROM users WHERE id = ? FOR UPDATE", u.ID).Error; err != nil {
					t.Fatal(err)
				}
				data := tinyPNG(t)
				auditBefore := auditTotal(t, app)
				resp := make(chan *httptest.ResponseRecorder, 1)
				go func() {
					if verb == "POST" {
						resp <- c.doMultipart("POST", "/api/v1/me/avatar", "avatar", "me.png", data)
					} else {
						resp <- c.do("DELETE", "/api/v1/me/avatar", nil, true)
					}
				}()
				waiterOn(t, app.DB, pid)
				if err := holder.Exec("UPDATE users SET "+tc.set+" WHERE id = ?", u.ID).Error; err != nil {
					t.Fatal(err)
				}
				if err := holder.Commit().Error; err != nil {
					t.Fatal(err)
				}
				var w *httptest.ResponseRecorder
				select {
				case w = <-resp:
				case <-time.After(10 * time.Second):
					t.Fatal("the request never finished")
				}
				want := tc.upload
				if verb == "DELETE" {
					want = tc.remove
				}
				if w.Code != want {
					t.Fatalf("%s answered %d %s, want %d", verb, w.Code, w.Body.String(), want)
				}
				if want == http.StatusForbidden && !strings.Contains(w.Body.String(), "teen_avatar") {
					t.Fatalf("teen upload: %s", w.Body.String())
				}
				assertNoAvatarLeak(t, app, "response", w.Body.String(), cur, strings.TrimSuffix(strings.TrimPrefix(cur, "avatars/"+itoa(u.ID)+"/"), ".png"))
				if n := auditTotal(t, app); n != auditBefore {
					t.Fatalf("an avatar request wrote %d audit rows", n-auditBefore)
				}
				if store.putCount() != 0 {
					t.Fatal("a file was put")
				}
				jobsMade := len(avatarJobsFor(t, app, u.ID))
				switch {
				case verb == "DELETE" && want == http.StatusOK:
					if avatarKeyOf(t, app, u.ID) != "" || jobsMade != 1 {
						t.Fatalf("teen removal: key %q, %d jobs", avatarKeyOf(t, app, u.ID), jobsMade)
					}
				default:
					if avatarKeyOf(t, app, u.ID) != cur || jobsMade != 0 {
						t.Fatalf("key %q, %d jobs after a refused request", avatarKeyOf(t, app, u.ID), jobsMade)
					}
					if got := avatarFiles(t, app, u.ID); len(got) != 1 {
						t.Fatalf("files %v", got)
					}
				}
			})
		}
	}
}

// A row held past the 5 second lock_timeout makes upload and removal answer
// 500 without a Put, a file or a job.
func TestAvatarRequestsGiveUpWaitingForTheRow(t *testing.T) {
	app := testApp(t)
	store := installAvatarBarrier(app)
	u, cur := avatarUser(t, app, "grier")
	c := loginAPI(t, app, "grier", "pw")
	holder, _ := beginOwnedTx(t, app.DB)
	if err := holder.Exec("SELECT id FROM users WHERE id = ? FOR UPDATE", u.ID).Error; err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	w := c.doMultipart("POST", "/api/v1/me/avatar", "avatar", "me.png", tinyPNG(t))
	if w.Code != http.StatusInternalServerError || !strings.Contains(w.Body.String(), `"code":"server_error"`) {
		t.Fatalf("upload: %d %s", w.Code, w.Body.String())
	}
	if d := time.Since(start); d > 8*time.Second {
		t.Fatalf("upload took %s", d)
	}
	start = time.Now()
	w = c.do("DELETE", "/api/v1/me/avatar", nil, true)
	if w.Code != http.StatusInternalServerError || !strings.Contains(w.Body.String(), `"code":"server_error"`) {
		t.Fatalf("removal: %d %s", w.Code, w.Body.String())
	}
	if d := time.Since(start); d > 8*time.Second {
		t.Fatalf("removal took %s", d)
	}
	if store.putCount() != 0 || avatarKeyOf(t, app, u.ID) != cur || len(avatarJobsFor(t, app, u.ID)) != 0 || len(avatarFiles(t, app, u.ID)) != 1 {
		t.Fatal("a refused request left something behind")
	}
}

// Put under the row lock is bounded at 10 seconds: a store that never
// answers ends the upload with a 500 at about that time, with the key, the
// folder and the queue as they were, and the row is free again.
func TestAvatarPutUnderTheLockIsBoundedAtTenSeconds(t *testing.T) {
	app := testApp(t)
	store := installAvatarBarrier(app)
	store.putGate = make(chan struct{}) // never opened; Put honors its context
	u, cur := avatarUser(t, app, "grier")
	c := loginAPI(t, app, "grier", "pw")
	start := time.Now()
	w := c.doMultipart("POST", "/api/v1/me/avatar", "avatar", "me.png", tinyPNG(t))
	d := time.Since(start)
	if w.Code != http.StatusInternalServerError || !strings.Contains(w.Body.String(), `"code":"server_error"`) {
		t.Fatalf("upload: %d %s", w.Code, w.Body.String())
	}
	if d < avatarPutTimeout-time.Second || d > avatarPutTimeout+4*time.Second {
		t.Fatalf("upload ended after %s, want about %s", d, avatarPutTimeout)
	}
	if avatarKeyOf(t, app, u.ID) != cur || len(avatarJobsFor(t, app, u.ID)) != 0 || len(avatarFiles(t, app, u.ID)) != 1 {
		t.Fatal("a store that never answered left something behind")
	}
	if w := c.do("DELETE", "/api/v1/me/avatar", nil, true); w.Code != http.StatusOK {
		t.Fatalf("removal after the timed-out upload: %d %s", w.Code, w.Body.String())
	}
}

// A body that takes longer than the handler's budget leaves a deadline
// already past: the upload answers 500 with no Put, no file and no job. The
// body is a real slow read, so this takes about 52 seconds.
func TestSlowUploadBodyLeavesNoTimeForTheTransaction(t *testing.T) {
	if testing.Short() {
		t.Skip("reads a request body for 52 seconds")
	}
	app := testApp(t)
	store := installAvatarBarrier(app)
	u := newTestUser(t, app, "grier", "pw", "member")
	c := loginAPI(t, app, "grier", "pw")
	data := tinyPNG(t)

	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	go func() {
		fw, err := mw.CreateFormFile("avatar", "me.png")
		if err == nil {
			_, err = fw.Write(data)
		}
		// The closing boundary arrives after the handler's whole budget.
		time.Sleep(avatarHandlerBudget + 2*time.Second)
		if err == nil {
			err = mw.Close()
		}
		pw.CloseWithError(err)
	}()
	req := httptest.NewRequest("POST", "/api/v1/me/avatar", pr)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	for _, ck := range c.jar {
		req.AddCookie(ck)
	}
	req.Header.Set("X-CSRF-Token", c.token)
	w := httptest.NewRecorder()
	c.router.ServeHTTP(w, req)
	if w.Code != http.StatusInternalServerError || !strings.Contains(w.Body.String(), `"code":"server_error"`) {
		t.Fatalf("slow upload: %d %s", w.Code, w.Body.String())
	}
	if store.putCount() != 0 || avatarFiles(t, app, u.ID) != nil || len(avatarJobsFor(t, app, u.ID)) != 0 {
		t.Fatal("a late upload left something behind")
	}
}

// storeAvatar with a snapshot from before an erasure, or a deadline already
// past, makes no Put, no folder and no job.
func TestStoreAvatarRefusesStaleSnapshotAndPastDeadline(t *testing.T) {
	app := testApp(t)
	store := installAvatarBarrier(app)
	u := newTestUser(t, app, "grier", "pw", "member")
	data := tinyPNG(t)
	store1 := func(deadline time.Time) error {
		_, err := app.storeAvatar(context.Background(), u, bytes.NewReader(data), int64(len(data)), "image/png", deadline)
		return err
	}
	if err := store1(time.Now().Add(-time.Second)); !errors.Is(err, errAvatarOutOfDate) {
		t.Fatalf("a past deadline: %v", err)
	}
	if store.putCount() != 0 || avatarFiles(t, app, u.ID) != nil || len(avatarJobsFor(t, app, u.ID)) != 0 {
		t.Fatal("a past deadline left something behind")
	}
	if err := app.DB.Exec("UPDATE users SET deleted_at = now(), session_version = session_version + 1 WHERE id = ?", u.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := store1(time.Now().Add(time.Minute)); !errors.Is(err, errSecuritySession) {
		t.Fatalf("stale snapshot: %v", err)
	}
	if store.putCount() != 0 || avatarFiles(t, app, u.ID) != nil || len(avatarJobsFor(t, app, u.ID)) != 0 {
		t.Fatal("a refused store left something behind")
	}
}

// installFailAvatarCommit makes any change to users.avatar_key fail at COMMIT.
func installFailAvatarCommit(t *testing.T, app *App) {
	t.Helper()
	for _, q := range []string{
		`CREATE FUNCTION fold_test_fail_avatar_commit() RETURNS trigger LANGUAGE plpgsql AS $$
			BEGIN RAISE EXCEPTION 'commit refused'; END $$`,
		`CREATE CONSTRAINT TRIGGER fold_test_fail_avatar_commit AFTER UPDATE ON users DEFERRABLE INITIALLY DEFERRED
			FOR EACH ROW WHEN (OLD.avatar_key IS DISTINCT FROM NEW.avatar_key) EXECUTE FUNCTION fold_test_fail_avatar_commit()`,
	} {
		if err := app.DB.Exec(q).Error; err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		app.DB.Exec("DROP TRIGGER IF EXISTS fold_test_fail_avatar_commit ON users")
		app.DB.Exec("DROP FUNCTION IF EXISTS fold_test_fail_avatar_commit()")
	})
}

func TestAvatarUploadWithFailedCommitKeepsOldPhotoAndQueuesFallback(t *testing.T) {
	for _, rejectJobs := range []bool{false, true} {
		t.Run(fmt.Sprintf("jobs rejected %v", rejectJobs), func(t *testing.T) {
			app := testApp(t)
			store := installAvatarBarrier(app)
			u := newTestUser(t, app, "grier", "password1234", "member")
			c := loginAPI(t, app, "grier", "password1234")
			if w := c.doMultipart("POST", "/api/v1/me/avatar", "avatar", "me.png", tinyPNG(t)); w.Code != http.StatusOK {
				t.Fatalf("first upload: %d", w.Code)
			}
			drainAvatarJobs(t, app)
			oldKey := avatarKeyOf(t, app, u.ID)
			// The first upload's cleanup job stays as a done row; only rows newer
			// than this mark came from the failed upload.
			var mark uint
			for _, j := range avatarJobsFor(t, app, u.ID) {
				mark = max(mark, j.ID)
			}
			installFailAvatarCommit(t, app)
			if rejectJobs {
				installRejectAvatarJobs(t, app)
			}
			w := c.doMultipart("POST", "/api/v1/me/avatar", "avatar", "me.png", tinyPNG(t))
			if w.Code != http.StatusInternalServerError {
				t.Fatalf("upload with a failing commit: %d %s", w.Code, w.Body.String())
			}
			if store.putCount() != 2 {
				t.Fatalf("%d puts, want the first upload and the failed one", store.putCount())
			}
			if avatarKeyOf(t, app, u.ID) != oldKey {
				t.Fatal("the key changed")
			}
			if got := avatarFiles(t, app, u.ID); len(got) != 2 {
				t.Fatalf("files %v, want the old photo and the new orphan", got)
			}
			if w := getUpload(t, app, "/uploads/"+oldKey); w.Code != http.StatusOK {
				t.Fatalf("old photo: %d", w.Code)
			}
			queued := []models.Job{}
			for _, j := range avatarJobsFor(t, app, u.ID) {
				if j.ID > mark {
					queued = append(queued, j)
				}
			}
			if rejectJobs {
				if len(queued) != 0 {
					t.Fatalf("a job was written despite the failing insert: %+v", queued)
				}
				app.DB.Exec("DROP TRIGGER IF EXISTS fold_test_reject_avatar_jobs ON jobs")
				if err := app.sweepAvatars(context.Background(), time.Now()); err != nil {
					t.Fatal(err)
				}
			} else if len(queued) != 1 {
				t.Fatalf("fallback jobs: %+v", queued)
			}
			assertAvatarPayloads(t, app)
			drainAvatarJobs(t, app)
			if got := avatarFiles(t, app, u.ID); len(got) != 1 || "avatars/"+itoa(u.ID)+"/"+got[0] != oldKey {
				t.Fatalf("files %v, want only the old photo", got)
			}
			if w := getUpload(t, app, "/uploads/"+oldKey); w.Code != http.StatusOK {
				t.Fatalf("old photo after cleanup: %d", w.Code)
			}
		})
	}
}

// Successive replacements, with cleanup runs interleaved (one starting before
// the last upload commits and waiting on its lock), leave only the latest.
// A job that failed once is not coalesced into a later upload's work: both
// runs keep the newer photo.
func TestAvatarReplacementsInterleavedWithCleanupKeepOnlyTheLatest(t *testing.T) {
	app := testApp(t)
	store := installAvatarBarrier(app)
	u := newTestUser(t, app, "grier", "password1234", "member")
	c := loginAPI(t, app, "grier", "password1234")
	upload := func() int {
		return c.doMultipart("POST", "/api/v1/me/avatar", "avatar", "me.png", tinyPNG(t)).Code
	}
	only := func(step string) string {
		t.Helper()
		key := avatarKeyOf(t, app, u.ID)
		if got := avatarFiles(t, app, u.ID); len(got) != 1 || "avatars/"+itoa(u.ID)+"/"+got[0] != key {
			t.Fatalf("after %s: files %v, want only %s", step, got, key)
		}
		return key
	}
	if upload() != http.StatusOK {
		t.Fatal("upload A")
	}
	if err := app.runAvatarCleanup(context.Background(), u.ID); err != nil {
		t.Fatal(err)
	}
	a := only("A")
	if upload() != http.StatusOK {
		t.Fatal("upload B")
	}
	if err := app.runAvatarCleanup(context.Background(), u.ID); err != nil {
		t.Fatal(err)
	}
	if b := only("B"); b == a {
		t.Fatal("B replaced nothing")
	}

	// C is held inside Put while a cleanup run waits on the row.
	store.forgetSignals()
	store.putGate = make(chan struct{})
	resp := make(chan int, 1)
	go func() { resp <- upload() }()
	awaitSignal(t, store.putEntered)
	cleanup := make(chan error, 1)
	go func() { cleanup <- app.runAvatarCleanup(context.Background(), u.ID) }()
	waitForLockedUsersQuery(t, app, "%users%FOR NO KEY UPDATE%")
	close(store.putGate)
	if code := <-resp; code != http.StatusOK {
		t.Fatalf("upload C: %d", code)
	}
	if err := <-cleanup; err != nil {
		t.Fatal(err)
	}
	cKey := only("C")
	// Runs in any order, any number of times, change nothing more.
	drainAvatarJobs(t, app)
	for i := 0; i < 2; i++ {
		if err := app.runAvatarCleanup(context.Background(), u.ID); err != nil {
			t.Fatal(err)
		}
	}
	if got := only("repeated runs"); got != cKey {
		t.Fatalf("the key moved to %s", got)
	}

	// A job that failed once: the first CleanOwner returns an error.
	store.mu.Lock()
	store.cleanErr = errors.New("storage unavailable")
	store.mu.Unlock()
	if upload() != http.StatusOK {
		t.Fatal("upload for the failing job")
	}
	drainAvatarJobs(t, app)
	failed := avatarJobsFor(t, app, u.ID)
	last := failed[len(failed)-1]
	if last.Status != models.JobFailed || last.LastError != errAvatarCleanupFailed.Error() {
		t.Fatalf("the failing job: %+v", last)
	}
	store.mu.Lock()
	store.cleanErr = nil
	store.mu.Unlock()
	if upload() != http.StatusOK {
		t.Fatal("upload D")
	}
	d := avatarKeyOf(t, app, u.ID)
	after := avatarJobsFor(t, app, u.ID)
	if len(after) != len(failed)+1 || after[len(after)-1].Status != models.JobQueued || after[len(after)-1].UniqueKey != nil {
		t.Fatalf("upload D after a failed job should add a fresh queued row: %+v", after)
	}
	if err := app.DB.Model(&models.Job{}).Where("id = ?", last.ID).Update("run_at", time.Now().Add(-time.Second)).Error; err != nil {
		t.Fatal(err)
	}
	drainAvatarJobs(t, app)
	if got := only("both runs"); got != d {
		t.Fatalf("files kept %s, want %s", got, d)
	}
	for _, j := range avatarJobsFor(t, app, u.ID) {
		if j.Status != models.JobDone {
			t.Fatalf("job %d is %s", j.ID, j.Status)
		}
	}
	assertAvatarPayloads(t, app)
}

// A cleanup that starts while an upload is held inside Put waits for the row,
// then keeps the new photo and removes the old one; the upload's own job
// keeps it too.
func TestAvatarCleanupWaitsForBlockedUploadAndKeepsTheNewPhoto(t *testing.T) {
	app := testApp(t)
	store := installAvatarBarrier(app)
	u := newTestUser(t, app, "grier", "password1234", "member")
	c := loginAPI(t, app, "grier", "password1234")
	if w := c.doMultipart("POST", "/api/v1/me/avatar", "avatar", "me.png", tinyPNG(t)); w.Code != http.StatusOK {
		t.Fatalf("first upload: %d", w.Code)
	}
	drainAvatarJobs(t, app)
	oldKey := avatarKeyOf(t, app, u.ID)

	store.forgetSignals()
	store.putGate = make(chan struct{})
	resp := make(chan int, 1)
	go func() {
		resp <- c.doMultipart("POST", "/api/v1/me/avatar", "avatar", "me.png", tinyPNG(t)).Code
	}()
	awaitSignal(t, store.putEntered)
	cleanup := make(chan error, 1)
	go func() { cleanup <- app.runAvatarCleanup(context.Background(), u.ID) }()
	waitForLockedUsersQuery(t, app, "%users%FOR NO KEY UPDATE%")
	select {
	case err := <-cleanup:
		t.Fatalf("the cleanup ran while the upload held the row: %v", err)
	default:
	}
	close(store.putGate)
	if code := <-resp; code != http.StatusOK {
		t.Fatalf("upload: %d", code)
	}
	select {
	case err := <-cleanup:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the cleanup never finished")
	}
	newKey := avatarKeyOf(t, app, u.ID)
	if newKey == oldKey {
		t.Fatal("the upload kept the old key")
	}
	if got := avatarFiles(t, app, u.ID); len(got) != 1 || "avatars/"+itoa(u.ID)+"/"+got[0] != newKey {
		t.Fatalf("files %v, want only the new photo", got)
	}
	drainAvatarJobs(t, app)
	if got := avatarFiles(t, app, u.ID); len(got) != 1 || "avatars/"+itoa(u.ID)+"/"+got[0] != newKey {
		t.Fatalf("after the upload's own job: files %v", got)
	}
	if w := getUpload(t, app, "/uploads/"+newKey); w.Code != http.StatusOK {
		t.Fatalf("new photo: %d", w.Code)
	}
}

// ---------------------------------------------------------------------------
// Household self-service writers revalidate the acting account (M7.4 R4-3a-1,
// inventory 9.6 requirements 1 to 4, F-10 writer side). Every writer takes
// the acting user row FOR NO KEY UPDATE before the household lock, so an
// erasure, a disable or a session change that commits first leaves the writer
// nothing to save, and one that waits for the writer finds a finished write.
// Rows the writer committed before an erasure stay until R4-3b scrubs
// households (a stated R4-3b limit, not asserted here).
// ---------------------------------------------------------------------------

const hwPW = erasurePW

// hwScene is an admin (the erasure's actor), a signed-in member who writes
// the household, and, when seeded, that member's household with one adult.
type hwScene struct {
	coreErasureScene
	u   *models.User
	c   *apiClient
	hid uint // the member's household; 0 before first use
	pid uint // an adult in it; 0 before first use
}

func newHWScene(t *testing.T, seeded bool) hwScene {
	t.Helper()
	s := hwScene{coreErasureScene: newCoreErasureScene(t)}
	s.ac.holdNotices = true // F1: before anything is seeded
	s.u = newTestUser(t, s.app, "hw-writer", hwPW, models.RoleMember)
	if err := s.app.DB.Model(s.u).Update("name", "Wanda Writer").Error; err != nil {
		t.Fatal(err)
	}
	s.c = newAPIClient(t, s.app)
	s.c.holdNotices = true
	s.c.fetchCSRF()
	if w := s.c.do("POST", "/api/v1/auth/login", map[string]string{"email": testEmail("hw-writer"), "password": hwPW}, true); w.Code != http.StatusOK {
		t.Fatalf("login: %d %s", w.Code, w.Body.String())
	}
	if seeded {
		apiDo(t, s.c, "PUT", "/api/v1/me/household", map[string]any{"name": "Writers", "address": "1 Oak St"}, http.StatusOK)
		s.hid = getMyHousehold(t, s.c).ID
		s.pid = addMyPerson(t, s.c, map[string]any{"firstName": "Ada", "kind": "adult"}).ID
	}
	return s
}

// hwRoute is one household writer: its request, the status it answers for a
// live account (read from api_profile.go: PUT household 200, POST person 201,
// PUT person 200, DELETE person 200), the audit rows it writes, and the first
// write to households or household_people it makes. firstWrite matches only
// facts GORM has set before the step (F6): Table, Where, Dest.
type hwRoute struct {
	name       string
	method     string
	firstUse   bool // the member has no household yet
	status     int
	writes     map[string]int64 // audit rows a live request adds, by action
	path       func(s hwScene) string
	body       any
	firstWrite func(s hwScene) erasureTestPoint
}

func (r hwRoute) send(c *apiClient, s hwScene) <-chan httpResult {
	return bgDo(c, r.method, r.path(s), r.body)
}

func (r hwRoute) do(c *apiClient, s hwScene) httpResult {
	w := c.do(r.method, r.path(s), r.body, true)
	return httpResult{w.Code, w.Body.String()}
}

var hwAuditActions = []string{audit.HouseholdCreated, audit.HouseholdUpdated, audit.HouseholdPersonAdded,
	audit.HouseholdPersonUpdated, audit.HouseholdPersonRemoved}

var hwRoutes = []hwRoute{
	{
		name: "PUT household", method: "PUT", status: http.StatusOK,
		writes: map[string]int64{audit.HouseholdUpdated: 1},
		path:   func(hwScene) string { return "/api/v1/me/household" },
		body:   map[string]any{"name": "Renamed", "address": "2 Elm St"},
		firstWrite: func(s hwScene) erasureTestPoint {
			return erasureTestPoint{Processor: "update", Step: "gorm:update", Table: "households",
				Where: &erasureTestWhere{SQL: "id = ?", Vars: map[int]any{0: s.hid}}}
		},
	},
	{
		name: "POST person", method: "POST", status: http.StatusCreated,
		writes: map[string]int64{audit.HouseholdPersonAdded: 1},
		path:   func(hwScene) string { return "/api/v1/me/household/people" },
		body:   map[string]any{"firstName": "Bea", "kind": "adult"},
		firstWrite: func(hwScene) erasureTestPoint {
			return erasureTestPoint{Processor: "create", Step: "gorm:create", Table: "household_people"}
		},
	},
	{
		name: "PUT person", method: "PUT", status: http.StatusOK,
		writes: map[string]int64{audit.HouseholdPersonUpdated: 1},
		path:   func(s hwScene) string { return "/api/v1/me/household/people/" + itoa(s.pid) },
		body:   map[string]any{"firstName": "Changed"},
		firstWrite: func(hwScene) erasureTestPoint {
			return erasureTestPoint{Processor: "update", Step: "gorm:update", Table: "household_people",
				Assign: map[string]any{"first_name": "Changed"}}
		},
	},
	{
		name: "DELETE person", method: "DELETE", status: http.StatusOK,
		writes: map[string]int64{audit.HouseholdPersonRemoved: 1},
		path:   func(s hwScene) string { return "/api/v1/me/household/people/" + itoa(s.pid) },
		firstWrite: func(hwScene) erasureTestPoint {
			return erasureTestPoint{Processor: "delete", Step: "gorm:delete", Table: "household_people"}
		},
	},
	{
		name: "first use PUT household", method: "PUT", firstUse: true, status: http.StatusOK,
		writes: map[string]int64{audit.HouseholdCreated: 1, audit.HouseholdUpdated: 1},
		path:   func(hwScene) string { return "/api/v1/me/household" },
		body:   map[string]any{"name": "Fresh"},
	},
	{
		name: "first use POST person", method: "POST", firstUse: true, status: http.StatusCreated,
		writes: map[string]int64{audit.HouseholdCreated: 1, audit.HouseholdPersonAdded: 1},
		path:   func(hwScene) string { return "/api/v1/me/household/people" },
		body:   map[string]any{"firstName": "Bea", "kind": "adult"},
	},
}

// hwState is every row a household writer could add or change, plus the audit
// log and the notifications, as one string.
func hwState(t *testing.T, db *gorm.DB) string {
	t.Helper()
	var parts []string
	for _, table := range []string{"households", "household_accounts", "household_people", "guardian_links", "audit_logs", "notifications"} {
		parts = append(parts, table+": "+dumpRows(t, db, table, "id", "true"))
	}
	return strings.Join(parts, "\n")
}

// hwHoldUser is a test transaction that takes uid FOR UPDATE, as the erasure
// does, then runs mutate (an UPDATE's SET list, or empty) without committing.
// It returns its backend PID (F7) and the commit. Cleanup rolls back, so a
// failed assertion never strands a request.
func hwHoldUser(t *testing.T, app *App, uid uint, mutate string) (blocker int, commit func()) {
	t.Helper()
	tx := app.DB.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	t.Cleanup(func() { _ = tx.Rollback() })
	var locked models.User
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&locked, uid).Error; err != nil {
		t.Fatal(err)
	}
	if err := tx.Raw("SELECT pg_backend_pid()").Scan(&blocker).Error; err != nil {
		t.Fatal(err)
	}
	if mutate != "" {
		if err := tx.Exec("UPDATE users SET "+mutate+" WHERE id = ?", uid).Error; err != nil {
			t.Fatal(err)
		}
	}
	return blocker, func() {
		if err := tx.Commit().Error; err != nil {
			t.Fatal(err)
		}
	}
}

// hwWaiters waits until want backends are blocked on a lock while running a
// users FOR NO KEY UPDATE select (the writer's user lock) and returns their
// PIDs.
func hwWaiters(t *testing.T, app *App, want int) []int {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var pids []int
		if err := app.DB.Raw(`SELECT pid FROM pg_stat_activity WHERE datname = current_database()
			AND wait_event_type = 'Lock' AND state = 'active' AND query LIKE ? ORDER BY pid`,
			`%"users"%FOR NO KEY UPDATE%`).Scan(&pids).Error; err != nil {
			t.Fatal(err)
		}
		if len(pids) == want {
			return pids
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("never saw %d backends waiting on the user lock", want)
	return nil
}

// hwOneBlockedBy waits until at least one of the waiters is blocked by the
// blocker's PID (F7). Two requests on one row queue, so the second may wait
// on the first instead.
func hwOneBlockedBy(t *testing.T, app *App, waiters []int, blocker int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, w := range waiters {
			var blocked bool
			if err := app.DB.Raw("SELECT ? = ANY(pg_blocking_pids(?))", blocker, w).Scan(&blocked).Error; err != nil {
				t.Fatal(err)
			}
			if blocked {
				return
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("no waiter in %v ever waited on session %d", waiters, blocker)
}

// hwRefusedWhileLocked: the request reaches its user lock while the test
// transaction holds the row, which then commits mutate. Every route answers
// 401 unauthorized and nothing it could write changes.
func hwRefusedWhileLocked(t *testing.T, mutate string) {
	for _, r := range hwRoutes {
		t.Run(r.name, func(t *testing.T) {
			s := newHWScene(t, !r.firstUse)
			before := hwState(t, s.app.DB)
			blocker, commit := hwHoldUser(t, s.app, s.u.ID, mutate)
			res := r.send(s.c, s)
			waiter := hwWaiters(t, s.app, 1)[0]
			waitDBBlocked(t, s.app.DB, waiter, blocker)
			commit()
			got := awaitResult(t, res)
			if got.code != http.StatusUnauthorized || !strings.Contains(got.body, `"unauthorized"`) {
				t.Fatalf("%s: %d %s, want 401 unauthorized", r.name, got.code, got.body)
			}
			if after := hwState(t, s.app.DB); after != before {
				t.Errorf("a refused %s changed household, audit or notification rows:\nbefore %s\nafter  %s", r.name, before, after)
			}
		})
	}
}

// TestHouseholdWritersRefuseAfterTheAccountIsErased: erasure first. The
// account is soft-deleted with a new session version before the request's
// lock is granted.
func TestHouseholdWritersRefuseAfterTheAccountIsErased(t *testing.T) {
	hwRefusedWhileLocked(t, "session_version = session_version + 1, deleted_at = now()")
}

// TestHouseholdWritersRefuseAStaleSession: only session_version changed (a
// password change or "sign out everywhere"), no delete.
func TestHouseholdWritersRefuseAStaleSession(t *testing.T) {
	hwRefusedWhileLocked(t, "session_version = session_version + 1")
}

// TestHouseholdWriterFirstThenRealErasure: the writer holds the user lock and
// the household lock and is paused before its first write; the real DELETE
// /admin/users/:id waits at its user lock behind the writer, then both finish
// with no deadlock.
func TestHouseholdWriterFirstThenRealErasure(t *testing.T) {
	for _, r := range hwRoutes {
		if r.firstUse {
			continue
		}
		t.Run(r.name, func(t *testing.T) {
			s := newHWScene(t, true)
			states := erasureTestSQLStates(t, s.app)
			var writerPID atomic.Int64
			p := r.firstWrite(s)
			p.PID = &writerPID
			reached, release := erasureTestPause(t, s.app.DB, p)
			res := r.send(s.c, s)
			awaitDBSignal(t, reached)
			del := bgDo(s.ac, "DELETE", "/api/v1/admin/users/"+itoa(s.u.ID), nil)
			waiter := waitForRowLockWaiter(t, s.app)
			waitDBBlocked(t, s.app.DB, waiter, int(writerPID.Load()))
			release()
			if got := awaitResult(t, res); got.code != r.status {
				t.Fatalf("%s: %d %s, want %d", r.name, got.code, got.body, r.status)
			}
			if got := awaitResult(t, del); got.code != http.StatusOK {
				t.Fatalf("erasure: %d %s, want 200", got.code, got.body)
			}
			if n := states.Count("40P01"); n != 0 {
				t.Errorf("%d deadlocks recorded", n)
			}
		})
	}
}

// TestHouseholdWritersStillWorkForALiveAccount: each route answers its own
// status and writes exactly the audit rows it always did, once.
func TestHouseholdWritersStillWorkForALiveAccount(t *testing.T) {
	for _, r := range hwRoutes {
		t.Run(r.name, func(t *testing.T) {
			s := newHWScene(t, !r.firstUse)
			before := map[string]int64{}
			for _, action := range hwAuditActions {
				before[action] = auditCount(t, s.app, action)
			}
			if got := r.do(s.c, s); got.code != r.status {
				t.Fatalf("%s: %d %s, want %d", r.name, got.code, got.body, r.status)
			}
			for _, action := range hwAuditActions {
				if got, want := auditCount(t, s.app, action)-before[action], r.writes[action]; got != want {
					t.Errorf("%s wrote %d %s rows, want %d", r.name, got, action, want)
				}
			}
			if n := countRows(t, s.app.DB, "household_accounts", "user_id = ?", s.u.ID); n != 1 {
				t.Errorf("%d household seats for the member, want 1", n)
			}
		})
	}
}

// TestHouseholdFirstUseNamesTheLockedRow: the name is read under the user
// lock, so a rename that commits while the request waits is the one used.
func TestHouseholdFirstUseNamesTheLockedRow(t *testing.T) {
	s := newHWScene(t, false)
	blocker, commit := hwHoldUser(t, s.app, s.u.ID, "name = 'Wanda Newname'")
	res := bgDo(s.c, "PUT", "/api/v1/me/household", map[string]any{})
	waitDBBlocked(t, s.app.DB, hwWaiters(t, s.app, 1)[0], blocker)
	commit()
	if got := awaitResult(t, res); got.code != http.StatusOK {
		t.Fatalf("first use: %d %s, want 200", got.code, got.body)
	}
	var name string
	if err := s.app.DB.Raw("SELECT name FROM households WHERE created_by = ?", s.u.ID).Scan(&name).Error; err != nil {
		t.Fatal(err)
	}
	if name != "Newname household" {
		t.Errorf("household name %q, want it built from the locked row", name)
	}
}

// TestHouseholdConcurrentFirstUseMakesOneHousehold: two first uses for one
// user wait on the same user lock, then leave one household and one seat.
func TestHouseholdConcurrentFirstUseMakesOneHousehold(t *testing.T) {
	s := newHWScene(t, false)
	second := *s.c
	second.jar = map[string]*http.Cookie{}
	for name, ck := range s.c.jar {
		second.jar[name] = ck
	}
	blocker, commit := hwHoldUser(t, s.app, s.u.ID, "")
	one := bgDo(s.c, "PUT", "/api/v1/me/household", map[string]any{"name": "One"})
	two := bgDo(&second, "PUT", "/api/v1/me/household", map[string]any{"name": "Two"})
	waiters := hwWaiters(t, s.app, 2)
	hwOneBlockedBy(t, s.app, waiters, blocker) // the other may queue behind its peer
	commit()
	for _, ch := range []<-chan httpResult{one, two} {
		if got := awaitResult(t, ch); got.code != http.StatusOK {
			t.Fatalf("first use: %d %s, want 200", got.code, got.body)
		}
	}
	if n := countRows(t, s.app.DB, "households", "created_by = ?", s.u.ID); n != 1 {
		t.Errorf("%d households, want 1", n)
	}
	if n := countRows(t, s.app.DB, "household_accounts", "user_id = ?", s.u.ID); n != 1 {
		t.Errorf("%d seats, want 1", n)
	}
	if n := auditCount(t, s.app, audit.HouseholdCreated); n != 1 {
		t.Errorf("%d household.created rows, want 1", n)
	}
}
