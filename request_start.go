package main

import (
	"time"

	"github.com/gin-gonic/gin"
)

// requestStartKey holds the time stampRequestStart saw the request.
const requestStartKey = "requestStart"

// stampRequestStart records when the request reached the router, before any
// middleware reads its body.
func stampRequestStart() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Set(requestStartKey, time.Now())
		c.Next()
	}
}

// requestStart is when the request reached the router. The CSRF middleware
// parses a multipart form, which reads a whole upload body before the handler
// runs, so a handler's own clock would start after a slow body.
func requestStart(c *gin.Context) time.Time {
	if v, ok := c.Get(requestStartKey); ok {
		if t, ok := v.(time.Time); ok {
			return t
		}
	}
	return time.Now()
}
