package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// Secrets travel in some URLs: calendar feed tokens (/cal/u/<token>.ics),
// guest manage links, invitation links (/api/v1/invitations/<token>), and the ?token= of email links the SPA serves. The
// access log keeps the route but never the secret, and drops every query
// string, so a log file or a log shipper can't replay a link.
var secretPathPrefixes = []string{
	"/cal/u/",
	"/guest/manage/",
	"/api/v1/guest-registrations/manage/",
	"/api/v1/invitations/",
}

func redactLogPath(path string) string {
	for _, p := range secretPathPrefixes {
		if !strings.HasPrefix(path, p) {
			continue
		}
		rest := path[len(p):]
		if path == "/api/v1/invitations/accept" {
			return path // a fixed route; its token travels in the body
		}
		tail := ""
		if i := strings.IndexByte(rest, '/'); i >= 0 {
			tail = rest[i:]
		} else if strings.HasSuffix(rest, ".ics") {
			tail = ".ics"
		}
		return p + "REDACTED" + tail
	}
	return path
}

// accessLog is gin's default log line with redactLogPath applied. gin's
// formatter appends the raw query to Path; the request's own path is
// used instead so no query string is logged.
func accessLog() gin.HandlerFunc {
	return gin.LoggerWithConfig(gin.LoggerConfig{Formatter: func(p gin.LogFormatterParams) string {
		path := p.Path
		if p.Request != nil {
			path = p.Request.URL.Path
		}
		return fmt.Sprintf("[GIN] %v | %3d | %13v | %15s | %-7s %#v\n%s",
			p.TimeStamp.Format("2006/01/02 - 15:04:05"), p.StatusCode,
			p.Latency.Truncate(time.Microsecond), p.ClientIP, p.Method,
			redactLogPath(path), p.ErrorMessage)
	}})
}
