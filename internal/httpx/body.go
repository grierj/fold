package httpx

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// Refuse writes an error response with the caller's error body. The root
// package passes jsonError, so every refusal here carries an API error code.
type Refuse func(c *gin.Context, status int, msg string)

// LimitBody makes reads past n fail, so oversized bodies are refused
// instead of buffered.
func LimitBody(n int64, refuse Refuse) gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.ContentLength > n {
			c.Abort()
			refuse(c, http.StatusRequestEntityTooLarge, "request too large")
			return
		}
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, n)
		c.Next()
	}
}
