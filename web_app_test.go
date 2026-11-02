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
	r.NoRoute(serveSPAFrom(files, "https://church.example"))
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
	r.NoRoute(serveSPAFrom(files, "https://church.example"))
	if w := get(r, "/sw.js"); w.Code != http.StatusNotFound {
		t.Fatalf("missing /sw.js: %d %q", w.Code, w.Body.String())
	}
	if w := get(r, "/groups/1"); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "<title>Fold</title>") {
		t.Fatalf("client route: %d", w.Code)
	}
}

func TestSPAServesStaticFilesAndEventImageMeta(t *testing.T) {
	const shell = "<!doctype html><html><head><title>Fold</title></head><body><div id=\"root\"></div></body></html>"
	const origin = "https://church.example"
	files := fstest.MapFS{
		"index.html":  {Data: []byte(shell)},
		"favicon.ico": {Data: []byte{0, 0, 1, 0}},
	}
	serve := func(path string) *httptest.ResponseRecorder {
		r := gin.New()
		r.NoRoute(serveSPAFrom(files, origin))
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		return w
	}

	t.Run("present favicon is an image", func(t *testing.T) {
		w := serve("/favicon.ico")
		if w.Code != http.StatusOK || !strings.Contains(w.Header().Get("Content-Type"), "image") {
			t.Fatalf("favicon: %d, Content-Type %q", w.Code, w.Header().Get("Content-Type"))
		}
	})

	t.Run("missing favicon is a 404, not the shell", func(t *testing.T) {
		delete(files, "favicon.ico")
		w := serve("/favicon.ico")
		if w.Code != http.StatusNotFound || strings.Contains(w.Body.String(), "<title>Fold</title>") {
			t.Fatalf("missing favicon: %d %q", w.Code, w.Body.String())
		}
	})

	t.Run("numeric event gets the default social image", func(t *testing.T) {
		w := serve("/events/42")
		body := w.Body.String()
		imageURL := origin + defaultOGImagePath
		for _, want := range []string{
			`property="og:image" content="` + imageURL + `"`,
			`property="og:image:width" content="1200"`,
			`property="og:image:height" content="630"`,
			`name="twitter:card" content="summary_large_image"`,
			`name="twitter:image" content="` + imageURL + `"`,
		} {
			if !strings.Contains(body, want) {
				t.Errorf("/events/42 body missing %q: %s", want, body)
			}
		}
	})

	t.Run("client route still gets the shell", func(t *testing.T) {
		w := serve("/groups/1")
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "<title>Fold</title>") {
			t.Fatalf("client route: %d %q", w.Code, w.Body.String())
		}
	})

	t.Run("only an exact numeric event gets social image metadata", func(t *testing.T) {
		for _, path := range []string{"/events/new", "/events/42/edit"} {
			w := serve(path)
			if w.Code != http.StatusOK || strings.Contains(w.Body.String(), `property="og:image"`) {
				t.Errorf("%s: %d %q", path, w.Code, w.Body.String())
			}
		}
	})
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
	if m.ID != "/" || m.StartURL != "/?source=homescreen" || m.Scope != "/" || m.Display != "standalone" || m.BackgroundColor == "" || m.ThemeColor == "" {
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
