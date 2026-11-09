package httpx

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"
)

// Healthz answers 200 while ping succeeds and 503 otherwise, with no body.
func Healthz(ping func(ctx context.Context) error) gin.HandlerFunc {
	return func(c *gin.Context) {
		if err := ping(c.Request.Context()); err != nil {
			c.Status(http.StatusServiceUnavailable)
			return
		}
		c.Status(http.StatusOK)
	}
}
