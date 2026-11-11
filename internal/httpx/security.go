// Package httpx holds the HTTP plumbing that needs nothing from the app: the
// security headers, the body cap, the redacting access log, the health probe,
// the web manifest and the embedded single-page-app server. It reports its
// refusals through the caller's error writer, so the API's error codes stay in
// the root package.
package httpx

import "github.com/gin-gonic/gin"

// contentSecurityPolicy covers the SPA and /setup (DEC-37, 10.1). Scripts
// and styles come only from this origin; React sets focal points through
// the CSSOM, which CSP allows without 'unsafe-inline'. Images come only from
// this origin, because uploads are the only image source (DEC-49), plus
// data: (assets Vite inlines) and blob: (upload previews before they are
// sent). The service worker and the manifest come only from this origin too
// (M3.13c1); both already fell back to 'self', and naming them keeps it that
// way.
const contentSecurityPolicy = "default-src 'self'; script-src 'self'; style-src 'self'; " +
	"img-src 'self' data: blob:; connect-src 'self'; font-src 'self'; object-src 'none'; " +
	"worker-src 'self'; manifest-src 'self'; " +
	"base-uri 'self'; form-action 'self'; frame-ancestors 'self'"

// SecurityHeaders sets the headers on every response, so they hold behind
// any proxy. HSTS goes out only when cookies are Secure, which means the
// site is served over HTTPS (never in dev).
func SecurityHeaders(hsts bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		h := c.Writer.Header()
		h.Set("Content-Security-Policy", contentSecurityPolicy)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("X-Frame-Options", "SAMEORIGIN")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		if hsts {
			h.Set("Strict-Transport-Security", "max-age=31536000")
		}
		c.Next()
	}
}
