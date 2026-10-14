package main

import (
	"bytes"
	"image"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"fold/models"
)

// tinyPNG returns a valid 1x1 PNG for upload tests.
func tinyPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}

func (c *apiClient) doMultipart(method, path, field, filename string, data []byte) *httptest.ResponseRecorder {
	var b bytes.Buffer
	w := multipart.NewWriter(&b)
	fw, err := w.CreateFormFile(field, filename)
	if err != nil {
		c.t.Fatalf("create form file: %v", err)
	}
	if _, err := fw.Write(data); err != nil {
		c.t.Fatalf("write form file: %v", err)
	}
	if err := w.Close(); err != nil {
		c.t.Fatalf("close writer: %v", err)
	}
	req := httptest.NewRequest(method, path, &b)
	req.Header.Set("Content-Type", w.FormDataContentType())
	for _, ck := range c.jar {
		req.AddCookie(ck)
	}
	req.Header.Set("X-CSRF-Token", c.token)
	rec := httptest.NewRecorder()
	c.router.ServeHTTP(rec, req)
	for _, ck := range rec.Result().Cookies() {
		c.jar[ck.Name] = ck
	}
	return rec
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
	w = c.do("PUT", "/api/v1/me", map[string]string{"email": "other@example.com"}, true)
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
