package main

import (
	"errors"
	"fmt"
	"log"
	"net/http"

	"fold/internal/jobs"

	"github.com/gin-gonic/gin"
)

// Error responses carry a stable "code" next to the English "error"
// (UI makeover PR 4, DEC-61). The SPA shows its own wording for the code
// (web/src/lib/errors.ts) and never the "error" text, which stays for logs
// and API clients.
//
// An error a person can act on is an *apiError value, declared once with its
// status, code and message (api_error_values.go beside the code that raises
// it). Domain code returns the value, wraps it with %w, or compares to it
// with errors.Is, and a handler answers any error with fail(c, err): the code
// travels with the error, so rewording a message never changes it.
// TestWebErrorCopyCoversEveryCode keeps the SPA's map in step with every
// declared code.
//
// jsonError(c, status, msg) is only for a message with no code of its own:
// its code is the general one for the status (statusCodes), and the SPA says
// something general ("We couldn't save that. Please try again."). It must
// not carry a declared apiError's message (TestJSONErrorSendsNoDeclaredMessage).

// apiError is an error with its HTTP answer: status, code, message, and any
// extra JSON keys the body carries.
type apiError struct {
	Status  int
	Code    string
	Message string
	// Extra keys sit beside "error" and "code"; the map is never written
	// after the value is declared.
	Extra gin.H
	// base is the value this one answers differently in one context
	// (see with); errors.Is(withErr, base) is true.
	base *apiError
}

func (e *apiError) Error() string { return e.Message }

// Is makes a context-specific copy equal to the value it was made from.
func (e *apiError) Is(target error) bool {
	t, ok := target.(*apiError)
	return ok && e.base != nil && (e.base == t || e.base.Is(t))
}

// with answers the same refusal with extra body keys, where one context needs
// them and another must not send them. errors.Is still matches the base.
func (e *apiError) with(extra gin.H) *apiError {
	c := *e
	c.base = e
	c.Extra = extra
	return &c
}

// apiErrExtra is apiErr for an answer that always carries extra body keys.
func apiErrExtra(status int, code, msg string, extra gin.H) *apiError {
	e := apiErr(status, code, msg)
	e.Extra = extra
	return e
}

// apiErrWith declares base with extra body keys (see with), for a value that
// is fixed rather than built per request.
func apiErrWith(base *apiError, extra gin.H) *apiError {
	e := base.with(extra)
	declaredAPIErrors = append(declaredAPIErrors, e)
	return e
}

// body is the JSON of the answer. A 5xx apiError carries real copy on
// purpose (the account-deletion outcomes); every other 5xx goes through fail's
// log-and-scrub (the log line has no address or token: jobs.CleanError).
func (e *apiError) body() gin.H {
	b := gin.H{"error": e.Message, "code": e.Code}
	for k, v := range e.Extra {
		b[k] = v
	}
	return b
}

// declaredAPIErrors lists every value made by apiErr, for the table and
// guard tests. Values made by apiErrf at run time are not in it.
var declaredAPIErrors []*apiError

// apiErr declares a coded error. An empty code is the general code for the
// status, for a refusal that needs a value (to compare to) but no code of its
// own.
func apiErr(status int, code, msg string) *apiError {
	if code == "" {
		code = statusCode(status)
	}
	e := &apiError{Status: status, Code: code, Message: msg}
	declaredAPIErrors = append(declaredAPIErrors, e)
	return e
}

// apiErrf is apiErr for a message built at run time (it names a number). It
// is not declared, so the tables do not list it; its code must still be in
// ERROR_COPY, which TestWebErrorCopyCoversEveryCode reads from the call.
func apiErrf(status int, code, format string, args ...any) *apiError {
	return &apiError{Status: status, Code: code, Message: fmt.Sprintf(format, args...)}
}

// answerer is an error that builds its own answer, for a body that carries
// data (the events a deletion still needs a host for).
type answerer interface{ apiAnswer() *apiError }

// isAPIError reports whether fail would answer err with its own answer rather
// than as an unplanned failure.
func isAPIError(err error) bool {
	var ae *apiError
	var an answerer
	return errors.As(err, &ae) || errors.As(err, &an)
}

// fail answers err: the apiError in its chain, else the answer its answerer
// builds. Anything else is a failure nobody planned for: it is logged here and
// answered 500 with no detail.
func fail(c *gin.Context, err error) {
	var ae *apiError
	if errors.As(err, &ae) {
		c.JSON(ae.Status, ae.body())
		return
	}
	var an answerer
	if errors.As(err, &an) {
		ae = an.apiAnswer()
		c.JSON(ae.Status, ae.body())
		return
	}
	jsonError(c, http.StatusInternalServerError, jobs.CleanError(err))
}

// statusCodes name the general code for a status with no specific code.
var statusCodes = map[int]string{
	http.StatusBadRequest:            "invalid",
	http.StatusUnauthorized:          "signed_out",
	http.StatusForbidden:             "not_allowed",
	http.StatusNotFound:              "not_found",
	http.StatusConflict:              "conflict",
	http.StatusRequestEntityTooLarge: "file_too_large",
	http.StatusTooManyRequests:       "rate_limited",
}

// statusCode is the general code for status.
func statusCode(status int) string {
	if code, ok := statusCodes[status]; ok {
		return code
	}
	return "server_error"
}

// errorBody is the JSON for an uncoded error response: the English message
// and the code for the status. A 5xx message is logged and replaced, so no
// internal detail leaves.
func errorBody(status int, msg string) gin.H {
	if status >= 500 {
		log.Printf("api error %d: %s", status, msg)
		return gin.H{"error": "server error", "code": "server_error"}
	}
	return gin.H{"error": msg, "code": statusCode(status)}
}

// jsonError answers with a message that has no code of its own (see the top
// of this file).
func jsonError(c *gin.Context, status int, msg string) {
	c.JSON(status, errorBody(status, msg))
}
