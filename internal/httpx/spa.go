package httpx

import (
	"bytes"
	"fmt"
	"html/template"
	"io/fs"
	"log"
	"net/http"
	"path"
	"regexp"
	"strings"

	"github.com/gin-gonic/gin"
)

// serviceWorkerPath is the worker's file in web/dist. Served from the root,
// its scope is the whole site.
const serviceWorkerPath = "sw.js"

// DefaultOGImagePath is the social image a public event page names.
const DefaultOGImagePath = "/assets/brand/og-default-1200x630.png"

// stylesheetRe finds the CSS bundle Vite links from index.html.
var stylesheetRe = regexp.MustCompile(`<link rel="stylesheet"[^>]*href="(/assets/[^"]+\.css)"`)

var (
	eventPageRe   = regexp.MustCompile(`^/events/[0-9]+$`)
	closingHeadRe = regexp.MustCompile(`(?i)</head>`)
)

// Stylesheet returns the path of the SPA's CSS bundle, which the
// server-rendered pages (setup, dev outbox) share so they use the same
// tokens and font. It is empty when the bundle (dist, rooted at index.html)
// is not built; those pages then render unstyled.
func Stylesheet(dist fs.FS) string {
	b, err := fs.ReadFile(dist, "index.html")
	if err != nil {
		return ""
	}
	m := stylesheetRe.FindSubmatch(b)
	if m == nil {
		return ""
	}
	return string(m[1])
}

// ServeSPA serves a built bundle (web/dist, or a test's files), falling back to
// index.html for client-side routes. API paths and missing static files get a
// JSON 404 through refuse. origin is the public base URL for social metadata.
func ServeSPA(sub fs.FS, origin string, refuse Refuse) gin.HandlerFunc {
	built := sub != nil
	if built {
		if _, err := fs.Stat(sub, "index.html"); err != nil {
			built = false
		}
	}
	if !built {
		log.Println("WARNING: web/dist is empty — run `npm run build` in web/ to build the frontend")
	}
	notFound := func(c *gin.Context) { refuse(c, http.StatusNotFound, "not found") }
	return func(c *gin.Context) {
		if strings.HasPrefix(c.Request.URL.Path, "/api/") {
			c.Header("Cache-Control", "no-store")
			notFound(c)
			return
		}
		if !built {
			c.String(http.StatusServiceUnavailable,
				"Frontend not built. Run `npm run build` in web/ and restart.")
			return
		}
		servedPath := strings.TrimPrefix(c.Request.URL.Path, "/")
		if servedPath == "" {
			servedPath = "index.html"
		}
		if _, err := fs.Stat(sub, servedPath); err != nil {
			if servedPath == serviceWorkerPath {
				// Never the shell in the worker's place: registering
				// HTML as a script fails in a confusing way.
				notFound(c)
				return
			}
			if path.Ext(path.Base(servedPath)) != "" {
				// A missing static asset must not receive the SPA shell.
				notFound(c)
				return
			}
			// Client-side route — let the SPA router handle it.
			servedPath = "index.html"
		}
		// ServeContent, not http.FileServer: FileServer 301-redirects
		// index.html serves to "./", which breaks the SPA fallback.
		data, err := fs.ReadFile(sub, servedPath)
		if err != nil {
			notFound(c)
			return
		}
		info, err := fs.Stat(sub, servedPath)
		if err != nil || info.IsDir() {
			notFound(c)
			return
		}
		if servedPath == "index.html" {
			// no-store: always fetch the shell fresh (a rebuilt image is what
			// runs), and keep the page out of the back/forward cache, so going
			// back after logout never shows the previous session's screen.
			c.Header("Cache-Control", "no-store")
			if eventPageRe.MatchString(c.Request.URL.Path) {
				data = eventImageMeta(data, origin+DefaultOGImagePath)
			}
		} else if servedPath == serviceWorkerPath {
			// no-cache: the browser checks for a new worker on every
			// registration and update instead of guessing a lifetime for
			// a root file with no header (research 2.3).
			c.Header("Cache-Control", "no-cache")
		} else if strings.HasPrefix(servedPath, "assets/") {
			c.Header("Cache-Control", "public, max-age=31536000, immutable")
		}
		http.ServeContent(c.Writer, c.Request, servedPath, info.ModTime(), bytes.NewReader(data))
	}
}

// eventImageMeta adds the default social image to a public event shell.
func eventImageMeta(index []byte, imageURL string) []byte {
	head := closingHeadRe.FindIndex(index)
	if head == nil {
		return index
	}
	escapedURL := template.HTMLEscapeString(imageURL)
	meta := []byte(fmt.Sprintf(`<meta property="og:image" content="%s">
<meta property="og:image:width" content="1200">
<meta property="og:image:height" content="630">
<meta name="twitter:card" content="summary_large_image">
<meta name="twitter:image" content="%s">
`, escapedURL, escapedURL))
	withMeta := make([]byte, 0, len(index)+len(meta))
	withMeta = append(withMeta, index[:head[0]]...)
	withMeta = append(withMeta, meta...)
	withMeta = append(withMeta, index[head[0]:]...)
	return withMeta
}
