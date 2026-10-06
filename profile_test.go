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

	"church-groups/models"
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
		"email":       "grier@example.org",
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
		resp.User.Email != "grier@example.org" || resp.User.Phone != "555-0100" {
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
	// Clearing the email is allowed.
	w = c.do("PUT", "/api/v1/me", map[string]string{"email": ""}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("clear email: got %d %s", w.Code, w.Body.String())
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
	app.DB.Where("username = ?", "grier").First(&u)
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
	g := models.Group{Name: "Test Group", LeaderID: leader.ID, Status: models.GroupActive}
	if err := app.DB.Create(&g).Error; err != nil {
		t.Fatalf("create group: %v", err)
	}

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

