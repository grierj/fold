package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"fold/models"

	"github.com/gin-gonic/gin"
)

// The service worker is served from the root with no-cache, so the
// browser checks for a new one; a missing worker is a 404, never the shell.
func TestServiceWorkerIsServedNoCache(t *testing.T) {
	files := fstest.MapFS{
		"index.html":       {Data: []byte("<!doctype html><title>Fold</title>")},
		"sw.js":            {Data: []byte("self.addEventListener('push', () => {});")},
		"assets/index.js":  {Data: []byte("console.log(1)")},
		"assets/brand/x.p": {Data: []byte("x")},
	}
	r := gin.New()
	r.NoRoute(serveSPAFrom(files))
	get := func(r *gin.Engine, path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		return w
	}
	w := get(r, "/sw.js")
	if w.Code != http.StatusOK || w.Header().Get("Cache-Control") != "no-cache" {
		t.Fatalf("/sw.js: %d, Cache-Control %q", w.Code, w.Header().Get("Cache-Control"))
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/javascript") {
		t.Fatalf("/sw.js Content-Type %q", ct)
	}
	if !strings.Contains(w.Body.String(), "addEventListener('push'") {
		t.Fatalf("/sw.js body %q", w.Body.String())
	}
	// The shell and the hashed assets keep their own rules.
	if got := get(r, "/").Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("/: Cache-Control %q", got)
	}
	if got := get(r, "/assets/index.js").Header().Get("Cache-Control"); !strings.Contains(got, "immutable") {
		t.Fatalf("assets: Cache-Control %q", got)
	}

	delete(files, "sw.js")
	r = gin.New()
	r.NoRoute(serveSPAFrom(files))
	if w := get(r, "/sw.js"); w.Code != http.StatusNotFound {
		t.Fatalf("missing /sw.js: %d %q", w.Code, w.Body.String())
	}
	if w := get(r, "/groups/1"); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "<title>Fold</title>") {
		t.Fatalf("client route: %d", w.Code)
	}
}

// The manifest names the Home Screen app after the church (DEC-124); a
// short name over 12 characters becomes "Fold".
func TestManifestCarriesTheChurchName(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "sam", "password1234", models.RoleMember)
	r := app.setupRouter()
	read := func() (webManifest, *httptest.ResponseRecorder) {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("GET", "/manifest.webmanifest", nil))
		var m webManifest
		if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
			t.Fatalf("manifest: %d %v %q", w.Code, err, w.Body.String())
		}
		return m, w
	}
	m, w := read()
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "application/manifest+json" || w.Header().Get("Cache-Control") != "no-cache" {
		t.Fatalf("manifest response %d %v", w.Code, w.Header())
	}
	if m.Name != "Fold" || m.ShortName != "Fold" {
		t.Fatalf("no church name: %+v", m)
	}
	if m.StartURL != "/?source=homescreen" || m.Scope != "/" || m.Display != "standalone" || m.BackgroundColor == "" || m.ThemeColor == "" {
		t.Fatalf("manifest fields: %+v", m)
	}
	var sizes []string
	for _, i := range m.Icons {
		sizes = append(sizes, i.Sizes+" "+i.Purpose)
	}
	if strings.Join(sizes, ",") != "192x192 ,512x512 ,512x512 maskable" {
		t.Fatalf("icons: %v", sizes)
	}
	for _, i := range m.Icons {
		if !strings.HasPrefix(i.Src, "/assets/brand/") || i.Type != "image/png" {
			t.Fatalf("icon %+v", i)
		}
	}

	for name, short := range map[string]string{
		"Grace Church":           "Grace Church", // 12 characters
		"Iglesia Fé y Esperanza": "Fold",
		"Église Sœur":            "Église Sœur", // runes, not bytes
	} {
		app.DB.Where("key = ?", "site_name").Delete(&models.Setting{})
		if err := app.DB.Create(&models.Setting{Key: "site_name", Value: name}).Error; err != nil {
			t.Fatal(err)
		}
		m, _ := read()
		if m.Name != name || m.ShortName != short {
			t.Fatalf("%q: name %q, short_name %q", name, m.Name, m.ShortName)
		}
	}
}

// The CSP names the worker and manifest sources (research 2.3).
func TestCSPAllowsOwnWorkerAndManifest(t *testing.T) {
	app := testApp(t)
	w := httptest.NewRecorder()
	app.setupRouter().ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	csp := w.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "worker-src 'self'; manifest-src 'self'") {
		t.Fatalf("CSP: %q", csp)
	}
}
