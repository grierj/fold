package httpx

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// Refuse writes an error response with the caller's error body. The root
// package passes jsonError, so a refusal here carries the API's error shape.
type Refuse func(c *gin.Context, status int, msg string)

// LimitBody makes reads past n fail, so oversized bodies are refused
// instead of buffered. tooLarge writes the answer for a body declared over n
// (the root package passes its declared 413); LimitBody aborts after it.
func LimitBody(n int64, tooLarge gin.HandlerFunc) gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.ContentLength > n {
			c.Abort()
			tooLarge(c)
			return
		}
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, n)
		c.Next()
	}
}
