package main

import "github.com/gin-gonic/gin"

// e2eRoutes is nil in every build except one made with -tags e2eclock, which
// is the browser tests' binary only (web/e2e/server.sh and run.sh). Release
// images, `go build` and `make test` never set that tag, so no route or clock
// seam below exists in anything that ships. The seam itself is in
// attempt_clock_e2e.go.
var e2eRoutes func(a *App, v1 *gin.RouterGroup)
