package httpx

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/gin-gonic/gin"
)

// refuse stands in for the API's error writer.
func refuse(c *gin.Context, status int, msg string) { c.JSON(status, gin.H{"error": msg}) }

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
	r.NoRoute(ServeSPA(files, "https://church.example", refuse))
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
	r.NoRoute(ServeSPA(files, "https://church.example", refuse))
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
		r.NoRoute(ServeSPA(files, origin, refuse))
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
		imageURL := origin + DefaultOGImagePath
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

// The setup and outbox pages link the SPA's CSS bundle from index.html.
func TestStylesheetReadsTheViteLink(t *testing.T) {
	fsys := fstest.MapFS{"index.html": {Data: []byte(`<head><script type="module" crossorigin src="/assets/index-a1.js"></script>
<link rel="stylesheet" crossorigin href="/assets/index-B5rK.css"></head>`)}}
	if got := Stylesheet(fsys); got != "/assets/index-B5rK.css" {
		t.Errorf("got %q", got)
	}
	if got := Stylesheet(fstest.MapFS{}); got != "" {
		t.Errorf("unbuilt: got %q", got)
	}
}
