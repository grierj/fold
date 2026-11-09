//go:build e2eclock

package main

import (
	"net/http"
	"os"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
)

// A test-only clock for the browser tests of attempt ids (T07B-1c). An id's
// age is the server's own monotonic time, which a browser cannot move, so this
// file, compiled only with -tags e2eclock, adds an offset to the App clock and an
// admin-only route that changes it. It does nothing unless FOLD_E2E_CLOCK=1 is
// set (web/e2e/server.sh sets it); it never exists in a build without the tag.
func init() {
	e2eRoutes = func(a *App, v1 *gin.RouterGroup) {
		if os.Getenv("FOLD_E2E_CLOCK") != "1" {
			return
		}
		var offset atomic.Int64
		a.clock = func() time.Time { return time.Now().Add(time.Duration(offset.Load())) }
		// POST {"offsetSeconds": N} sets how far ahead the attempt clock runs.
		v1.POST("/e2e/attempt-clock", a.apiAuth(), a.apiAdmin(), func(c *gin.Context) {
			var body struct {
				OffsetSeconds int64 `json:"offsetSeconds"`
			}
			if err := c.ShouldBindJSON(&body); err != nil || body.OffsetSeconds < 0 || body.OffsetSeconds > 24*3600 {
				jsonError(c, http.StatusBadRequest, "invalid request")
				return
			}
			offset.Store(body.OffsetSeconds * int64(time.Second))
			c.JSON(http.StatusOK, gin.H{"offsetSeconds": body.OffsetSeconds})
		})
	}
}
