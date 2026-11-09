package main

import (
	"errors"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"
)

// bindOptionalJSON reads a request body whose fields are all optional: an
// empty body is fine, a body that is not valid JSON answers 400 and reports
// false, instead of being taken for an empty one.
func bindOptionalJSON(c *gin.Context, body any) bool {
	if err := c.ShouldBindJSON(body); err != nil && !errors.Is(err, io.EOF) {
		jsonError(c, http.StatusBadRequest, "invalid request body")
		return false
	}
	return true
}
